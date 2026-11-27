//go:build (linux || darwin) && !sqlite3_flock && !sqlite3_dotlk

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/vfs/memdb"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const boundaryRepairRegion = "us-east-1"
const boundaryRepairVPC = "vpc-00000000000000419"

type repairCLIBoundary struct {
	t                 *testing.T
	original, working string
	initial           []byte
}

func makeRepairCLIBoundary(t *testing.T) repairCLIBoundary {
	t.Helper()
	original := repairCLIDB(t, "legacy-original.sqlite")
	store, err := core.New(original)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := aws.NewStackStore(store.Database()); err != nil {
		t.Fatal(err)
	}
	for _, row := range []aws.EC2NetworkResource{
		{ID: boundaryRepairVPC, Kind: "AWS::EC2::VPC", Region: boundaryRepairRegion, StackID: "ec2:native", CIDR: "10.44.0.0/16", DNSSupport: true, Revision: 1},
		{ID: "rtb-00000000000000420", Kind: "AWS::EC2::RouteTable", Region: boundaryRepairRegion, StackID: "ec2:native", VPCID: boundaryRepairVPC, Revision: 1},
	} {
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Database().ExecContext(t.Context(), `INSERT INTO aws_ec2_network_resources VALUES(?,?,?,?,?,?)`,
			row.Region, row.ID, row.StackID, row.Kind, string(raw), row.Revision); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	initial := boundaryRepairBytes(t, original)
	working := filepath.Join(filepath.Dir(original), "working.sqlite")
	if err := os.WriteFile(working, initial, 0600); err != nil {
		t.Fatal(err)
	}
	return repairCLIBoundary{t, original, working, initial}
}

func boundaryRepairBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (f repairCLIBoundary) args(operation, path, vpc string) []string {
	return []string{ec2NativeMainRepairCommand, operation, "--state", path, "--region", boundaryRepairRegion, "--vpc-id", vpc}
}

func (f repairCLIBoundary) refuse(want string, args ...string) {
	f.t.Helper()
	var out bytes.Buffer
	if err := run(f.t.Context(), args, &out); err == nil || !strings.Contains(err.Error(), want) {
		f.t.Fatal(want, err, out.String())
	}
	if out.Len() > 0 && !strings.HasPrefix(out.String(), "flag provided but not defined:") {
		f.t.Fatal("refusal unexpectedly emitted a plan or success receipt", out.String())
	}
}

func (f repairCLIBoundary) digest(vpc string) string {
	return mainRepairPlanDigest(boundaryRepairRegion, vpc, sha256.Sum256(f.initial))
}

func (f repairCLIBoundary) apply(original, digest, vpc string) []string {
	return append(f.args("apply", f.working, vpc), "--original", original, "--expect-plan", digest)
}

func TestEC2NativeMainRepairReadsPlanThroughMainEntry(t *testing.T) {
	f := makeRepairCLIBoundary(t)
	destination, err := os.CreateTemp(t.TempDir(), "maintenance-output-")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	func() {
		args, stdout := os.Args, os.Stdout
		defer func() { os.Args, os.Stdout = args, stdout }()
		os.Args = append([]string{"simulator"}, f.args("plan", f.original, boundaryRepairVPC)...)
		os.Stdout = destination
		main()
	}()
	var plan aws.EC2NativeMainRepairPlan
	if err := json.Unmarshal(boundaryRepairBytes(t, destination.Name()), &plan); err != nil ||
		plan.Status != "ready" || plan.VPCID != boundaryRepairVPC || plan.PlanDigest != f.digest(boundaryRepairVPC) {
		t.Fatal(plan, err)
	}
	if !bytes.Equal(boundaryRepairBytes(t, f.original), f.initial) || !bytes.Equal(boundaryRepairBytes(t, f.working), f.initial) {
		t.Fatal("CLI entry plan changed an on-disk image")
	}
}

func TestEC2NativeMainRepairCLIRefusesAmbiguousOrMissingScope(t *testing.T) {
	f := makeRepairCLIBoundary(t)
	missing := f.original + ".never-create"
	baselineDigest := f.digest(boundaryRepairVPC)
	for _, tt := range []struct {
		want string
		args []string
	}{
		{"requires plan or apply", []string{ec2NativeMainRepairCommand}},
		{"requires plan or apply", []string{ec2NativeMainRepairCommand, "serve"}},
		{"flag provided but not defined", append(f.args("plan", f.original, boundaryRepairVPC), "--listen", "0.0.0.0:0")},
		{"unexpected repair arguments", append(f.args("plan", f.original, boundaryRepairVPC), "another-vpc")},
		{"choose one exact region and native VPC ID", f.args("plan", f.original, "vpc-*")},
		{"lowercase exact plan digest", f.apply(f.original, strings.ToUpper(baselineDigest), boundaryRepairVPC)},
		{"owned 0400/0600 regular SQLite", f.apply(missing, baselineDigest, boundaryRepairVPC)},
		{"InvalidVpcID.NotFound", f.args("plan", f.original, "vpc-00000000000000466")},
		{"InvalidVpcID.NotFound", f.apply(f.original, baselineDigest, "vpc-00000000000000466")},
		{"no such file or directory", f.args("plan", filepath.Join(missing, "not-created.sqlite"), boundaryRepairVPC)},
	} {
		f.refuse(tt.want, tt.args...)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("a failed command created new state", err)
	}
	for _, path := range []string{f.original, f.working} {
		if !bytes.Equal(f.initial, boundaryRepairBytes(t, path)) {
			t.Fatal("argument refusal rewrote state", path)
		}
	}
}

func TestEC2NativeMainRepairCLIProtectsFileModeAndJournal(t *testing.T) {
	f := makeRepairCLIBoundary(t)
	dir := filepath.Join(filepath.Dir(f.original), "not-private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	publicDirCopy := filepath.Join(dir, "public-parent.sqlite")
	if err := os.WriteFile(publicDirCopy, f.initial, 0600); err != nil {
		t.Fatal(err)
	}
	f.refuse("operator-owned private directory", f.args("plan", publicDirCopy, boundaryRepairVPC)...)
	for _, suffix := range []string{"-wal", "-journal", "-shm"} {
		path := f.original + suffix
		if err := os.WriteFile(path, []byte("leave the existing journal alone"), 0600); err != nil {
			t.Fatal(err)
		}
		f.refuse("without SQLite sidecars", f.args("plan", f.original, boundaryRepairVPC)...)
		if !bytes.Equal(boundaryRepairBytes(t, path), []byte("leave the existing journal alone")) {
			t.Fatal("preflight modified sidecar")
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	wal := filepath.Join(filepath.Dir(f.original), "wal-mode.sqlite")
	if err := os.WriteFile(wal, f.initial, 0600); err != nil {
		t.Fatal(err)
	}
	st, err := core.New(wal)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	journalErr := st.Database().QueryRowContext(t.Context(), `PRAGMA journal_mode=WAL`).Scan(&mode)
	if err := st.Close(); err != nil || journalErr != nil || mode != "wal" {
		t.Fatal(mode, journalErr, err)
	}
	priorWAL := boundaryRepairBytes(t, wal)
	f.refuse("never WAL", f.args("plan", wal, boundaryRepairVPC)...)
	if !bytes.Equal(priorWAL, boundaryRepairBytes(t, wal)) {
		t.Fatal("repair changed a refused WAL file")
	}
	if err := os.Chmod(f.working, 0400); err != nil {
		t.Fatal(err)
	}
	f.refuse("working copy must be writable", f.apply(f.original, f.digest(boundaryRepairVPC), boundaryRepairVPC)...)
	if !bytes.Equal(boundaryRepairBytes(t, f.original), f.initial) || !bytes.Equal(boundaryRepairBytes(t, f.working), f.initial) {
		t.Fatal("unsafe file scope changed bytes")
	}
}

func TestEC2NativeMainRepairCLIReportsFreshAndRefusesChangedWorkingCopy(t *testing.T) {
	f := makeRepairCLIBoundary(t)
	store, err := core.New(f.working)
	if err != nil {
		t.Fatal(err)
	}
	stacks, err := aws.NewStackStore(store.Database())
	if err != nil {
		t.Fatal(err)
	}
	g := &aws.Gateway{Stacks: stacks, Identities: core.NewIdentities(store)}
	form := url.Values{"Action": {"CreateVpc"}, "Version": {"2016-11-15"}, "CidrBlock": {"10.45.0.0/16"}}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Simulator-Service", "ec2")
	r.Header.Set("X-Simulator-Region", boundaryRepairRegion)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var id string
	if err := store.Database().QueryRowContext(t.Context(),
		`SELECT id FROM aws_ec2_network_resources WHERE region=? AND kind='AWS::EC2::VPC' AND id<>?`, boundaryRepairRegion, boundaryRepairVPC).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := boundaryRepairBytes(t, f.working)
	var out bytes.Buffer
	if err := run(t.Context(), f.args("plan", f.working, id), &out); err != nil {
		t.Fatal(err)
	}
	var plan aws.EC2NativeMainRepairPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil || plan.Status != "already-native" || plan.VPCID != id || plan.ExtraPersistedRows != 0 || plan.PlanDigest != "" {
		t.Fatal(plan, err)
	}
	f.refuse("working copy does not match", f.apply(f.original, f.digest(boundaryRepairVPC), boundaryRepairVPC)...)
	if !bytes.Equal(before, boundaryRepairBytes(t, f.working)) || !bytes.Equal(f.initial, boundaryRepairBytes(t, f.original)) {
		t.Fatal("changed working copy was rewritten after an unrelated valid allocation")
	}
}

func TestEC2NativeMainRepairWitnessLossRefusesAndRollsBack(t *testing.T) {
	for _, when := range []string{"before-write", "before-commit"} {
		t.Run(when, func(t *testing.T) {
			f := makeRepairCLIBoundary(t)
			old, err := openMainRepairFile(f.original)
			if err != nil {
				t.Fatal(err)
			}
			defer old.file.Close()
			copyFile, err := openMainRepairFile(f.working)
			if err != nil {
				t.Fatal(err)
			}
			defer copyFile.file.Close()
			witness := sha256.Sum256(f.initial)
			if when == "before-write" {
				// Another ordinary validated write between planning and lock admission.
				st, err := core.New(f.working)
				if err != nil {
					t.Fatal(err)
				}
				if err := st.Create("fixture-state-drift", boundaryRepairRegion); err != nil {
					t.Fatal(err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				// The genuine exclusive transaction can stage writes, but final
				// commit cannot proceed after losing the original held descriptor.
				if err := old.file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before := boundaryRepairBytes(t, f.working)
			var out bytes.Buffer
			if err := commitMainRepair(t.Context(), copyFile, old, boundaryRepairRegion, boundaryRepairVPC,
				f.digest(boundaryRepairVPC), witness, &out); err == nil || !strings.Contains(err.Error(), "state changed after plan") || out.Len() != 0 {
				t.Fatal(err, out.String())
			}
			if !bytes.Equal(before, boundaryRepairBytes(t, f.working)) || !bytes.Equal(f.initial, boundaryRepairBytes(t, f.original)) {
				t.Fatal("witness loss left durable changes")
			}
		})
	}
}

func TestEC2NativeMainRepairRejectsDifferentVFSAndCanceledFingerprint(t *testing.T) {
	f := makeRepairCLIBoundary(t)
	state, err := openMainRepairFile(f.working)
	if err != nil {
		t.Fatal(err)
	}
	defer state.file.Close()
	alternate, err := sql.Open("sqlite3", "file:/legacy-main-state?vfs=memdb&mode=memory")
	if err != nil {
		t.Fatal(err)
	}
	defer alternate.Close()
	conn, err := alternate.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := state.verifyVFS(conn, true); err == nil || !strings.Contains(err.Error(), "pinned local SQLite VFS") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := state.fingerprint(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, err := readMainRepairPlan(ctx, state, boundaryRepairRegion, boundaryRepairVPC); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !bytes.Equal(f.initial, boundaryRepairBytes(t, f.working)) {
		t.Fatal("cancellation rewrote state")
	}
}

type repairFileReadContext struct {
	context.Context
	once   sync.Once
	onRead func()
}

func (c *repairFileReadContext) Err() error {
	c.once.Do(c.onRead)
	return c.Context.Err()
}

func TestEC2NativeMainRepairHashRejectsChangingHeldFiles(t *testing.T) {
	for _, scenario := range []string{"oversize", "lost-read-handle", "changed-metadata", "sidecar-during-read", "closed-before-open", "canceled-open"} {
		t.Run(scenario, func(t *testing.T) {
			f := makeRepairCLIBoundary(t)
			state, err := openMainRepairFile(f.working)
			if err != nil {
				t.Fatal(err)
			}
			defer state.file.Close()
			if scenario == "closed-before-open" || scenario == "canceled-open" {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if scenario == "closed-before-open" {
					if err := os.WriteFile(f.working+"-journal", []byte("independent stopped-copy witness"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					cancel()
				}
				conn, closeDB, err := state.database(ctx, false)
				if closeDB != nil {
					_ = closeDB()
				}
				if conn != nil || err == nil || scenario == "closed-before-open" && !strings.Contains(err.Error(), "without SQLite sidecars") ||
					scenario == "canceled-open" && !errors.Is(err, context.Canceled) {
					t.Fatal(conn, err)
				}
				if !bytes.Equal(f.initial, boundaryRepairBytes(t, f.working)) {
					t.Fatal("a refused open changed the copy")
				}
				return
			}
			var want string
			before := t.Context()
			if scenario == "oversize" {
				// Grow only a disposable file sparsely after initial ownership
				// admission, so the live fingerprint must enforce its own limit.
				if err := os.Truncate(f.working, maxEC2MainRepairFileBytes+1); err != nil {
					t.Fatal(err)
				}
				want = "file size is out of bounds"
			} else {
				var action func() error
				switch scenario {
				case "lost-read-handle":
					action, want = state.file.Close, "file already closed"
				case "changed-metadata":
					mod := state.info.ModTime().Add(2 * time.Second)
					action, want = func() error { return os.Chtimes(f.working, mod, mod) }, "file changed while reading"
				case "sidecar-during-read":
					action, want = func() error { return os.WriteFile(f.working+"-wal", []byte("separate writer marker"), 0600) }, "without SQLite sidecars"
				}
				// The initial stat has already happened when fingerprint asks
				// whether to continue streaming; the filesystem fault is real.
				before = &repairFileReadContext{Context: before, onRead: func() {
					if err := action(); err != nil {
						t.Fatal(err)
					}
				}}
			}
			if got, err := state.fingerprint(before); got != ([32]byte{}) || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatal(want, got, err)
			}
			if !bytes.Equal(f.initial, boundaryRepairBytes(t, f.original)) {
				t.Fatal("a failed hash changed the unmodified source")
			}
		})
	}
}

func TestEC2NativeMainRepairApplyRefusesMissingWorkingVPC(t *testing.T) {
	f := makeRepairCLIBoundary(t)
	store, err := core.New(f.working)
	if err != nil {
		t.Fatal(err)
	}
	stacks, err := aws.NewStackStore(store.Database())
	if err != nil {
		t.Fatal(err)
	}
	g := &aws.Gateway{Stacks: stacks, Identities: core.NewIdentities(store)}
	for _, form := range []url.Values{
		{"Action": {"DeleteRouteTable"}, "RouteTableId": {"rtb-00000000000000420"}},
		{"Action": {"DeleteVpc"}, "VpcId": {boundaryRepairVPC}},
	} {
		form.Set("Version", "2016-11-15")
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-Simulator-Service", "ec2")
		r.Header.Set("X-Simulator-Region", boundaryRepairRegion)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	deleted := boundaryRepairBytes(t, f.working)
	f.refuse("InvalidVpcID.NotFound", f.apply(f.original, f.digest(boundaryRepairVPC), boundaryRepairVPC)...)
	if !bytes.Equal(f.initial, boundaryRepairBytes(t, f.original)) || !bytes.Equal(deleted, boundaryRepairBytes(t, f.working)) {
		t.Fatal("repair resurrected the VPC or changed its original")
	}
}
