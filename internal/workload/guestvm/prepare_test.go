package guestvm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestbundle"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
)

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gvm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
func digest(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func imageHeader() []byte {
	b := make([]byte, 512)
	copy(b, "QFI\xfb")
	binary.BigEndian.PutUint32(b[4:8], 3)
	binary.BigEndian.PutUint32(b[20:24], 9)
	binary.BigEndian.PutUint64(b[24:32], 1<<20)
	binary.BigEndian.PutUint32(b[100:104], 104)
	return b
}
func testBundle(t *testing.T, disk []byte) (*guestbundle.Bundle, Layout) {
	t.Helper()
	return testBundleAt(t, disk, shortDir(t))
}
func testBundleAt(t *testing.T, disk []byte, parent string) (*guestbundle.Bundle, Layout) {
	t.Helper()
	layout := Layout{"bin/qemu", "firmware/bios", "images/base.qcow2"}
	m := guestbundle.Manifest{OS: runtime.GOOS, Arch: runtime.GOARCH}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for i, f := range []struct {
		name string
		data []byte
	}{{layout.Emulator, []byte("not executable code")}, {layout.Firmware, []byte("firmware")}, {layout.BaseDisk, disk}} {
		m.Files = append(m.Files, guestbundle.File{Path: f.name, Size: int64(len(f.data)), SHA256: digest(f.data), Executable: i == 0})
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0600, Size: int64(len(f.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	m.ArchiveSHA256 = digest(archive.Bytes())
	b, err := guestbundle.Extract(context.Background(), parent, bytes.NewReader(archive.Bytes()), m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b, layout
}
func acquire() guestlease.Intent {
	return guestlease.Intent{Fence: strings.Repeat("a", 64), Operation: 1, Action: guestlease.Acquire}
}
func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("deployment plan is Linux/amd64 only")
	}
}
func TestPreparePrivateIndependentStateAndPausedPlan(t *testing.T) {
	requireLinux(t)
	base := imageHeader()
	b, layout := testBundle(t, base)
	parent := shortDir(t)
	userData := []byte("#!/bin/sh\necho unchanged\n")
	p, err := Prepare(context.Background(), parent, b, layout, acquire(), userData)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p.Directory())
	if err != nil || st.Mode().Perm() != 0700 {
		t.Fatal(st, err)
	}
	diskPath := filepath.Join(p.Directory(), "root.qcow2")
	copyDisk, err := os.ReadFile(diskPath)
	if err != nil || !bytes.Equal(base, copyDisk) {
		t.Fatal("disk copy mismatch", err)
	}
	seed, err := os.ReadFile(filepath.Join(p.Directory(), "seed.iso"))
	if err != nil || !bytes.Contains(seed, userData) {
		t.Fatal("seed lost UserData", err)
	}
	raw, err := os.ReadFile(filepath.Join(p.Directory(), "prepared.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Record
	if json.Unmarshal(raw, &saved) != nil || saved != p.Record() || saved.Intent != acquire() || saved.BaseSHA256 != digest(base) || saved.SeedSHA256 != digest(seed) {
		t.Fatal("receipt mismatch")
	}
	if saved.UUID == "" || saved.Lease != "vm-"+acquire().Fence {
		t.Fatal("missing immutable binding")
	}
	for name, mode := range map[string]os.FileMode{"root.qcow2": 0600, "seed.iso": 0400, "prepared.json": 0400} {
		st, err := os.Stat(filepath.Join(p.Directory(), name))
		if err != nil || st.Mode().Perm() != mode {
			t.Fatal("permissions", name, err)
		}
	}
	if err = os.WriteFile(diskPath, []byte("guest changes"), 0600); err != nil {
		t.Fatal(err)
	}
	original, _ := b.Path(layout.BaseDisk)
	unchanged, _ := os.ReadFile(original)
	if !bytes.Equal(unchanged, base) {
		t.Fatal("guest disk aliases base")
	}
	args := p.Invocation()
	if args.Executable == "" || args.Env == nil || len(args.Env) != 0 {
		t.Fatal("missing executable or inherited environment")
	}
	joined := strings.Join(args.Args, "\n")
	for _, required := range []string{"-S\n", "-nic\nnone", "-monitor\nnone", "-serial\nnone", "-no-user-config", "-nodefaults", "-sandbox", "-uuid\n" + saved.UUID, "-L\n", "\"backing\":null", "\"read-only\":true"} {
		if !strings.Contains(joined, required) {
			t.Fatal("missing fixed restriction", required)
		}
	}
	if strings.Contains(joined, string(userData)) || strings.Contains(joined, "hostfwd") || strings.Contains(joined, "-fsdev") {
		t.Fatal("unexpected data/host access in plan")
	}
	for _, socket := range []string{p.QMPSocket(), p.QGASocket()} {
		if _, err := os.Stat(socket); !os.IsNotExist(err) {
			t.Fatal("preparation created a socket", err)
		}
	}
	otherIntent := acquire()
	otherIntent.Fence = strings.Repeat("b", 64)
	other, err := Prepare(context.Background(), parent, b, layout, otherIntent, userData)
	if err != nil {
		t.Fatal(err)
	}
	if other.Directory() == p.Directory() || other.Record().Lease == saved.Lease || other.Record().UUID == saved.UUID {
		t.Fatal("guest deployment alias")
	}
	args.Args[0] = "changed"
	if p.Invocation().Args[0] == "changed" {
		t.Fatal("invocation aliases caller memory")
	}
}
func TestPrepareRejectsInvalidAuthorityAndPaths(t *testing.T) {
	requireLinux(t)
	b, layout := testBundle(t, imageHeader())
	parent := shortDir(t)
	for _, intent := range []guestlease.Intent{{}, {Fence: "../bad", Operation: 1, Action: guestlease.Acquire}, {Fence: acquire().Fence, Operation: 2, Action: guestlease.Contain}, {Fence: acquire().Fence, Operation: 1, Action: guestlease.Acquire, Lease: "selected"}} {
		if _, err := Prepare(context.Background(), parent, b, layout, intent, []byte("x")); err == nil {
			t.Fatal("invalid intent accepted")
		}
	}
	for _, path := range []string{"relative", "/tmp/comma,path", "/tmp/colon:path", "/tmp/new\nline"} {
		if _, err := Prepare(context.Background(), path, b, layout, acquire(), []byte("x")); err == nil {
			t.Fatal("invalid parent", path)
		}
	}
	for _, l := range []Layout{{"missing", layout.Firmware, layout.BaseDisk}, {layout.Firmware, layout.Emulator, layout.BaseDisk}, {layout.Emulator, layout.Firmware, layout.Firmware}, {layout.Emulator, layout.BaseDisk, layout.BaseDisk}, {layout.Emulator, layout.Firmware, "missing"}} {
		if _, err := Prepare(context.Background(), parent, b, l, acquire(), []byte("x")); err == nil {
			t.Fatal("invalid roles accepted")
		}
	}
	if _, err := Prepare(nil, parent, b, layout, acquire(), []byte("x")); err == nil {
		t.Fatal("nil context")
	}
	if _, err := Prepare(context.Background(), parent, nil, layout, acquire(), []byte("x")); err == nil {
		t.Fatal("nil bundle")
	}
	if _, err := Prepare(context.Background(), parent, b, layout, acquire(), nil); err == nil {
		t.Fatal("missing seed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Prepare(ctx, parent, b, layout, acquire(), []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	files, err := os.ReadDir(parent)
	if err != nil || len(files) != 0 {
		t.Fatal("invalid preparation left state", err)
	}
}
func TestPrepareCleansRejectedImageAndChangedAssets(t *testing.T) {
	requireLinux(t)
	for _, kind := range []string{"invalid-image", "wrong-hash", "symlink", "size-change"} {
		t.Run(kind, func(t *testing.T) {
			image := imageHeader()
			if kind == "invalid-image" {
				copy(image, "bad!")
			}
			b, layout := testBundle(t, image)
			parent := shortDir(t)
			file, _ := b.Path(layout.BaseDisk)
			switch kind {
			case "wrong-hash":
				os.Chmod(file, 0600)
				image[len(image)-1] ^= 1
				if err := os.WriteFile(file, image, 0600); err != nil {
					t.Fatal(err)
				}
			case "size-change":
				os.Chmod(file, 0600)
				if err := os.WriteFile(file, []byte("short"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/nonexistent", file); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Prepare(context.Background(), parent, b, layout, acquire(), []byte("x")); err == nil {
				t.Fatal("invalid image accepted")
			}
			files, err := os.ReadDir(parent)
			if err != nil || len(files) != 0 {
				t.Fatal("partial files remain", err)
			}
		})
	}
}
func TestPrepareRejectsLongSocketPathsWithoutCreatingSockets(t *testing.T) {
	requireLinux(t)
	b, layout := testBundle(t, imageHeader())
	parent := filepath.Join(shortDir(t), strings.Repeat("x", 100))
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), parent, b, layout, acquire(), []byte("x")); err == nil {
		t.Fatal("long socket path accepted")
	}
	files, _ := os.ReadDir(parent)
	if len(files) != 0 {
		t.Fatal("partial state")
	}
}
func TestQCOWBoundaryRejectsExternalReferences(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]byte)
	}{
		{"version", func(h []byte) { binary.BigEndian.PutUint32(h[4:8], 1) }},
		{"backing-offset", func(h []byte) { binary.BigEndian.PutUint64(h[8:16], 104) }},
		{"backing-name", func(h []byte) { binary.BigEndian.PutUint32(h[16:20], 10) }},
		{"encrypted", func(h []byte) { binary.BigEndian.PutUint32(h[32:36], 1) }},
		{"snapshots", func(h []byte) { binary.BigEndian.PutUint32(h[60:64], 1) }},
		{"snapshot-offset", func(h []byte) { binary.BigEndian.PutUint64(h[64:72], 512) }},
		{"external-data", func(h []byte) { binary.BigEndian.PutUint64(h[72:80], 4) }},
		{"unknown-feature", func(h []byte) { binary.BigEndian.PutUint64(h[72:80], 1<<63) }},
		{"header-extension", func(h []byte) { binary.BigEndian.PutUint32(h[100:104], 112) }},
		{"small-cluster", func(h []byte) { binary.BigEndian.PutUint32(h[20:24], 8) }},
		{"large-cluster", func(h []byte) { binary.BigEndian.PutUint32(h[20:24], 22) }},
		{"empty-virtual", func(h []byte) { binary.BigEndian.PutUint64(h[24:32], 0) }},
		{"large-virtual", func(h []byte) { binary.BigEndian.PutUint64(h[24:32], MaxVirtualDiskBytes+1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := imageHeader()
			tc.edit(h)
			file := filepath.Join(t.TempDir(), "base")
			if err := os.WriteFile(file, h, 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(file)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := validateDisk(f, int64(len(h))); err == nil {
				t.Fatal("unsafe header accepted")
			}
		})
	}
}
func TestPreparedImageHeader(t *testing.T) {
	path := os.Getenv("TENKA_TEST_QCOW2")
	if path == "" {
		t.Skip("set TENKA_TEST_QCOW2 to a trusted pinned image; header validation only")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDisk(f, st.Size()); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRejectsRelativeBundlePaths(t *testing.T) {
	requireLinux(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, shortDir(t))
	if err != nil {
		t.Fatal(err)
	}
	b, layout := testBundleAt(t, imageHeader(), relative)
	parent := shortDir(t)
	if _, err := Prepare(context.Background(), parent, b, layout, acquire(), []byte("x")); err == nil {
		t.Fatal("relative asset paths accepted")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("partial state", err)
	}
}

func TestPreparationFileAndContextFailures(t *testing.T) {
	requireLinux(t)
	b, layout := testBundle(t, imageHeader())
	parent := shortDir(t)
	if _, err := Prepare(context.Background(), filepath.Join(parent, "absent"), b, layout, acquire(), []byte("x")); err == nil {
		t.Fatal("missing parent accepted")
	}
	path, _ := b.Path(layout.Emulator)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), parent, b, layout, acquire(), []byte("x")); err == nil {
		t.Fatal("removed executable accepted")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), parent, b, layout, acquire(), []byte("x")); err == nil {
		t.Fatal("closed bundle accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := contextReader{ctx, strings.NewReader("x")}
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	file := filepath.Join(parent, "existing")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(file, []byte("replace"), 0400); err == nil {
		t.Fatal("private file overwritten")
	}
	contents, _ := os.ReadFile(file)
	if string(contents) != "keep" {
		t.Fatal("existing file changed")
	}
	if err := syncDirectory(filepath.Join(parent, "missing")); err == nil {
		t.Fatal("missing directory sync succeeded")
	}
}

func TestQCOWHeaderVersionAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header []byte
		size   int64
		ok     bool
	}{
		{"v2", func() []byte { h := imageHeader(); binary.BigEndian.PutUint32(h[4:8], 2); return h }(), 512, true},
		{"short-header", []byte("QFI\xfb"), 4, false},
		{"short-cluster", imageHeader()[:104], 104, false},
		{"short-v3", imageHeader()[:72], 512, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "image")
			if err := os.WriteFile(file, tc.header, 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(file)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			err = validateDisk(f, tc.size)
			if (err == nil) != tc.ok {
				t.Fatal(err)
			}
		})
	}
	f, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := validateDisk(f, 512); err == nil {
		t.Fatal("closed file accepted")
	}
}

func TestPrepareRejectsShortBaseBeforeWriting(t *testing.T) {
	requireLinux(t)
	bundle, layout := testBundle(t, []byte("not an image"))
	parent := shortDir(t)
	if _, err := Prepare(context.Background(), parent, bundle, layout, acquire(), []byte("x")); err == nil {
		t.Fatal("short base image accepted")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("partial state", err)
	}
}
