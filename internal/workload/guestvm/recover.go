package guestvm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qmp"
)

var receiptUUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

// Recovered is an inspected preparation receipt, NOT a live-process handle or
// lease state. It intentionally has no Launch, Invocation or disk-copy method.
// Reopening a receipt must never become permission to replay an uncertain start.
type Recovered struct {
	record    Record
	directory string
}

func (r *Recovered) Record() Record    { return r.record }
func (r *Recovered) Directory() string { return r.directory }
func (r *Recovered) QMPSocket() string { return filepath.Join(r.directory, "qmp.sock") }
func (r *Recovered) QGASocket() string { return filepath.Join(r.directory, "qga.sock") }

// OpenControl requires an independently authorized private transport. Receipt
// inspection and UUID verification do not authenticate a process or prove that
// the original guest is alive, contained or released. This method never dials.
func (r *Recovered) OpenControl(ctx context.Context, transport qmp.Transport, options qmp.Options) (*qmp.Client, error) {
	return qmp.New(ctx, transport, r.record.UUID, options)
}

// InspectPrepared reads existing files only. expected must come from the
// trusted coordinator's independently committed immutable mapping, not from
// reading this same receipt and accepting whatever identity it supplies.
//
// It checks canonical receipt bytes, exact expected identity, private regular
// files and immutable seed integrity. The root disk is deliberately NOT compared
// to the base-image hash: legitimate guest writes change it. Only its private
// regular-file shape is checked; image health, growth quotas and live-process
// ownership require separate runtime evidence. No absence/error proves cleanup.
func InspectPrepared(ctx context.Context, directory string, expected Record) (*Recovered, error) {
	if ctx == nil {
		return nil, errors.New("guest inspection context required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validReceipt(expected) {
		return nil, errors.New("invalid expected guest receipt")
	}
	if err := checkPrivateDirectory(directory); err != nil {
		return nil, err
	}
	if !validPrivateEndpoints(directory, expected.SessionImageSHA256 != "") {
		return nil, errors.New("invalid private guest control path")
	}
	raw, err := readPrivate(ctx, filepath.Join(directory, "prepared.json"), 0400, 4096)
	if err != nil {
		return nil, err
	}
	var actual Record
	if json.Unmarshal(raw, &actual) != nil || !validReceipt(actual) || actual != expected {
		return nil, errors.New("guest receipt identity mismatch")
	}
	canonical, err := json.Marshal(actual)
	if err != nil || !bytes.Equal(raw, canonical) {
		return nil, errors.New("noncanonical guest receipt")
	}
	seed, err := readPrivate(ctx, filepath.Join(directory, "seed.iso"), 0400, 64<<10)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(seed)
	if hex.EncodeToString(hash[:]) != actual.SeedSHA256 {
		return nil, errors.New("guest seed integrity mismatch")
	}
	disk, err := os.Lstat(filepath.Join(directory, "root.qcow2"))
	if err != nil {
		return nil, err
	}
	if !disk.Mode().IsRegular() || disk.Mode().Perm() != 0600 || disk.Size() < 72 {
		return nil, errors.New("invalid private guest disk")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Recovered{record: actual, directory: directory}, nil
}
func validReceipt(r Record) bool {
	return (r.SessionImageSHA256 == "" || r.SessionImageSHA256 == r.BaseSHA256) && r.Version == 1 && fencePattern.MatchString(r.Intent.Fence) && r.Intent.Operation == 1 && r.Intent.Action == guestlease.Acquire && r.Intent.Lease == "" && r.Lease == "vm-"+r.Intent.Fence && receiptUUID.MatchString(r.UUID) && fencePattern.MatchString(r.BaseSHA256) && fencePattern.MatchString(r.SeedSHA256)
}
func readPrivate(ctx context.Context, path string, mode os.FileMode, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm() != mode || before.Size() <= 0 || before.Size() > limit {
		return nil, errors.New("invalid private guest file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, statErr := file.Stat()
	if statErr != nil || !os.SameFile(before, after) {
		return nil, errors.Join(errors.New("guest file changed during inspection"), statErr, file.Close())
	}
	data, readErr := io.ReadAll(io.LimitReader(contextReader{ctx, file}, limit+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if int64(len(data)) > limit || int64(len(data)) != before.Size() {
		return nil, errors.New("guest file size changed during inspection")
	}
	return data, nil
}

// The lifecycle owner must protect ancestors against concurrent replacement.
// This rejects aliases at inspection time; it is not a host filesystem sandbox.
func checkPrivateDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, ",:\x00\r\n") {
		return errors.New("invalid private guest directory spelling")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if canonical != path {
		return errors.New("guest directory aliases a symlink")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("guest directory is not private")
	}
	return nil
}
