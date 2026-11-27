package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/vfs"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
)

const (
	ec2NativeMainRepairCommand = "repair-ec2-native-main-route"
	maxEC2MainRepairFileBytes  = 256 << 20
)

type mainRepairFile struct {
	path string
	file *os.File
	info os.FileInfo
}

func openMainRepairFile(path string) (*mainRepairFile, error) {
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil, errors.New("repair requires an existing private SQLite filepath")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	parent, err := os.Stat(dir)
	if err != nil || !parent.IsDir() || !isOwnedMainRepairFile(parent) || parent.Mode().Perm()&0077 != 0 {
		return nil, errors.New("repair files must be in an operator-owned private directory")
	}
	abs = filepath.Join(dir, filepath.Base(abs))
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() || !isOwnedMainRepairFile(info) ||
		info.Mode().Perm() != 0400 && info.Mode().Perm() != 0600 || info.Size() < 100 || info.Size() > maxEC2MainRepairFileBytes {
		return nil, errors.New("repair needs an owned 0400/0600 regular SQLite file of at most 256 MiB; no symlink or hard link")
	}
	file, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	f := &mainRepairFile{abs, file, info}
	if err := f.checkIdentity(true); err != nil {
		_ = file.Close()
		return nil, err
	}
	var header [100]byte
	if _, err := file.ReadAt(header[:], 0); err != nil || string(header[:16]) != "SQLite format 3\x00" || header[18] != 1 || header[19] != 1 {
		_ = file.Close()
		return nil, errors.New("repair accepts only existing rollback-journal SQLite state, never WAL")
	}
	return f, nil
}

func (f *mainRepairFile) checkIdentity(noJournals bool) error {
	link, err := os.Lstat(f.path)
	if err != nil || !link.Mode().IsRegular() || !isOwnedMainRepairFile(link) || !os.SameFile(f.info, link) || link.Mode().Perm() != f.info.Mode().Perm() {
		return errors.New("repair file identity changed")
	}
	held, err := f.file.Stat()
	if err != nil || !os.SameFile(link, held) {
		return errors.New("repair file handle no longer identifies the selected state")
	}
	if noJournals {
		for _, suffix := range []string{"-journal", "-wal", "-shm"} {
			if _, err := os.Lstat(f.path + suffix); !errors.Is(err, os.ErrNotExist) {
				return errors.New("repair requires stopped state without SQLite sidecars")
			}
		}
	}
	return nil
}

func (f *mainRepairFile) fingerprint(ctx context.Context) ([32]byte, error) {
	var result [32]byte
	if err := f.checkIdentity(true); err != nil {
		return result, err
	}
	before, err := f.file.Stat()
	if err != nil || before.Size() < 100 || before.Size() > maxEC2MainRepairFileBytes {
		return result, errors.New("repair file size is out of bounds")
	}
	hasher := sha256.New()
	reader := io.NewSectionReader(f.file, 0, before.Size())
	buf := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		n, err := reader.Read(buf)
		_, _ = hasher.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, err
		}
	}
	after, err := f.file.Stat()
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return result, errors.New("repair file changed while reading")
	}
	if err := f.checkIdentity(true); err != nil {
		return result, err
	}
	copy(result[:], hasher.Sum(nil))
	return result, nil
}

// The pathname and connection pool are not proof of which inode SQLite
// opened. Admit the pinned VFS/mode and actual file, then use this exact
// leased connection for the transaction (including the pre-write hash).
func (f *mainRepairFile) verifyVFS(conn *sql.Conn, writable bool) error {
	return conn.Raw(func(raw any) error {
		wrap, ok := raw.(sqlitedriver.Conn)
		if !ok {
			return errors.New("repair requires the pinned SQLite driver")
		}
		actual := wrap.Raw()
		// The pinned v0.30 API's second bool is sqlite3_db_readonly < 0,
		// i.e. an unknown schema despite the library naming it "ok".
		if ro, unknown := actual.ReadOnly("main"); unknown || ro == writable {
			return errors.New("SQLite opened the wrong repair access mode")
		}
		fs, err := actual.FileControl("main", sqlite3.FCNTL_VFS_POINTER)
		if err != nil || fs != vfs.Find("os") {
			return errors.New("repair requires the pinned local SQLite VFS")
		}
		file, err := actual.FileControl("main", sqlite3.FCNTL_FILE_POINTER)
		if err != nil {
			return err
		}
		if wrapper, ok := file.(vfs.FileUnwrap); ok {
			file = wrapper.Unwrap()
		}
		native, ok := file.(interface{ Stat() (os.FileInfo, error) })
		if !ok {
			return errors.New("repair could not identify the live SQLite file")
		}
		got, err := native.Stat()
		if err != nil || !os.SameFile(f.info, got) {
			return errors.New("live SQLite file does not match the witnessed repair inode")
		}
		return nil
	})
}

func (f *mainRepairFile) database(ctx context.Context, writable bool) (*sql.Conn, func() error, error) {
	mode := "ro"
	if writable {
		if f.info.Mode().Perm() != 0600 {
			return nil, nil, errors.New("the working copy must be writable by its operator (0600)")
		}
		mode = "rw" // never create or bootstrap a database
	}
	if err := f.checkIdentity(true); err != nil {
		return nil, nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: f.path, RawQuery: "mode=" + mode}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, nil, errors.Join(err, db.Close())
	}
	closeDB := func() error { return errors.Join(conn.Close(), db.Close()) }
	if err := f.verifyVFS(conn, writable); err != nil {
		return nil, nil, errors.Join(err, closeDB())
	}
	for _, pragma := range []string{"PRAGMA foreign_keys=ON;", "PRAGMA trusted_schema=OFF;", "PRAGMA busy_timeout=2000;"} {
		if _, err := conn.ExecContext(ctx, pragma); err != nil {
			return nil, nil, errors.Join(fmt.Errorf("configure offline SQLite connection: %w", err), closeDB())
		}
	}
	return conn, closeDB, nil
}

func mainRepairPlanDigest(region, vpc string, source [32]byte) string {
	input := aws.EC2NativeMainRepairFormat + "\x00" + region + "\x00" + vpc + "\x00" + string(source[:])
	digest := sha256.Sum256([]byte(input))
	return hex.EncodeToString(digest[:])
}

func readMainRepairPlan(ctx context.Context, f *mainRepairFile, region, vpc string) (plan aws.EC2NativeMainRepairPlan, source [32]byte, err error) {
	source, err = f.fingerprint(ctx)
	if err != nil {
		return
	}
	db, closeDB, err := f.database(ctx, false)
	if err != nil {
		return
	}
	plan, err = aws.PlanEC2NativeMainRouteRepair(ctx, db, region, vpc)
	if err = errors.Join(err, closeDB()); err != nil {
		err = fmt.Errorf("inspect read-only legacy route state: %w", err)
		return
	}
	if live, e := f.fingerprint(ctx); e != nil || live != source {
		err = errors.New("repair state changed during read-only plan")
		return
	}
	if plan.Status == "ready" {
		plan.PlanDigest = mainRepairPlanDigest(region, vpc, source)
	}
	return
}

func runEC2NativeMainRepair(ctx context.Context, args []string, output io.Writer) error {
	if !ec2NativeMainRepairSupported {
		return errors.New("offline native route repair requires a stock Linux or macOS build with supported SQLite file locking")
	}
	if len(args) == 0 || args[0] != "plan" && args[0] != "apply" {
		return errors.New("repair-ec2-native-main-route requires plan or apply")
	}
	applying := args[0] == "apply"
	flags := flag.NewFlagSet(ec2NativeMainRepairCommand+" "+args[0], flag.ContinueOnError)
	flags.SetOutput(output)
	state := flags.String("state", "", "stopped existing local SQLite state (apply: working copy only)")
	region := flags.String("region", "", "exact saved region")
	vpc := flags.String("vpc-id", "", "exact native VPC ID")
	var originalPath, expected *string
	if applying {
		originalPath = flags.String("original", "", "separate unchanged private backup, opened read-only")
		expected = flags.String("expect-plan", "", "exact digest from the original ready plan")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected repair arguments")
	}
	if err := aws.ValidateEC2NativeMainRepairScope(*region, *vpc); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	working, err := openMainRepairFile(*state)
	if err != nil {
		return err
	}
	defer working.file.Close()
	if !applying {
		plan, _, err := readMainRepairPlan(ctx, working, *region, *vpc)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(plan)
	}
	if raw, err := hex.DecodeString(*expected); err != nil || len(raw) != sha256.Size || hex.EncodeToString(raw) != *expected {
		return errors.New("apply requires a lowercase exact plan digest")
	}
	original, err := openMainRepairFile(*originalPath)
	if err != nil {
		return err
	}
	defer original.file.Close()
	if os.SameFile(original.info, working.info) {
		return errors.New("apply requires a distinct working copy; the original is never written")
	}
	baseline, source, err := readMainRepairPlan(ctx, original, *region, *vpc)
	if err != nil {
		return err
	}
	if baseline.Status != "ready" || baseline.PlanDigest != *expected {
		return errors.New("stale repair plan: the unchanged original does not match")
	}
	observed, observedSource, err := readMainRepairPlan(ctx, working, *region, *vpc)
	if err != nil {
		return err
	}
	if observed.Status == "already-repaired" && observed.PlanDigest == *expected {
		return json.NewEncoder(output).Encode(observed)
	}
	if observed.Status != "ready" || observedSource != source || observed.PlanDigest != *expected {
		return errors.New("stale repair plan: the stopped working copy does not match the original")
	}
	return commitMainRepair(ctx, working, original, *region, *vpc, *expected, source, output)
}

func commitMainRepair(ctx context.Context, working, original *mainRepairFile, region, vpc, digest string, source [32]byte, output io.Writer) error {
	db, closeDB, err := working.database(ctx, true)
	if err != nil {
		return err
	}
	guard := func(f *mainRepairFile) error {
		if live, err := f.fingerprint(ctx); err != nil || live != source {
			return errors.New("repair state changed after plan; no result committed")
		}
		return nil
	}
	plan, err := aws.ApplyEC2NativeMainRouteRepair(ctx, db, region, vpc, digest,
		func() error { return guard(working) },
		func() error { return errors.Join(working.checkIdentity(false), guard(original)) })
	if err = errors.Join(err, closeDB()); err != nil {
		return err
	}
	// Verify the durable result from a new read-only connection. If an IO
	// error makes commitment uncertain, do not retry automatically.
	confirmed, _, err := readMainRepairPlan(ctx, working, region, vpc)
	if err != nil {
		return fmt.Errorf("apply committed; exact readback unavailable, re-plan before retrying: %w", err)
	}
	if confirmed.Status != "already-repaired" || confirmed.PlanDigest != digest {
		return errors.New("apply committed; exact readback was not verified, re-plan before retrying")
	}
	return json.NewEncoder(output).Encode(plan)
}
