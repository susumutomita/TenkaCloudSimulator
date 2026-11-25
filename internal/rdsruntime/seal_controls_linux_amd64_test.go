//go:build linux && amd64

package rdsruntime

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// These ELF *headers* exercise the seal parser only. They have no code, no
// entry point and no loadable segment. They are never executed. The optional
// TestActualOwnedPostgreSQL is the only positive runtime acceptance here.
func layoutHeader(t *testing.T, interp string) []byte {
	t.Helper()
	h := elf.Header64{Type: uint16(elf.ET_DYN), Machine: uint16(elf.EM_X86_64), Version: 1,
		Ehsize: 64, Phoff: 64, Phentsize: 56}
	copy(h.Ident[:], []byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), 1})
	if interp != "" {
		h.Phnum = 1
	}
	var b bytes.Buffer
	write := func(v any) {
		t.Helper()
		if err := binary.Write(&b, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	write(h)
	if interp != "" {
		write(elf.Prog64{Type: uint32(elf.PT_INTERP), Off: 120, Filesz: uint64(len(interp))})
		b.WriteString(interp)
	}
	return b.Bytes()
}

func writeFixture(t *testing.T, name string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(name, data, mode); err != nil {
		t.Fatal(err)
	}
}

func fixturePin(t *testing.T, dir string) string {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	sum, err := bundleDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(sum)
}

func TestSealValidationDoesNotTrustDirectoryOrFileReplacement(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"bin", "lib"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	dirPin := fixturePin(t, dir)
	if _, err := OpenBundle(dir, dirPin); !errors.Is(err, ErrBundle) {
		t.Fatal("empty layout approved", err)
	}
	exe := layoutHeader(t, "/lib64/ld-linux-x86-64.so.2\x00")
	lib := layoutHeader(t, "")
	for i, name := range fixed {
		data := exe
		if i == 3 {
			data = lib
		}
		writeFixture(t, filepath.Join(dir, name), data, 0700)
	}
	pin := fixturePin(t, dir)
	b, err := OpenBundle(dir, pin)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.verify(); err != nil {
		t.Fatal(err)
	}
	p, err := b.executable(0)
	if err != nil || p != filepath.Join(dir, fixed[0]) {
		t.Fatal("incorrect sealed asset", p, err)
	}
	// Check pre-launch admission only. Occupied/missing work roots must
	// refuse before invoking any program, including these inert files.
	occupied := t.TempDir()
	if err := os.Chmod(occupied, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(occupied, ownedSlot), 0700); err != nil {
		t.Fatal(err)
	}
	for _, work := range []string{occupied, filepath.Join(occupied, "missing")} {
		if r, err := Start(t.Context(), b, work); r != nil || !errors.Is(err, ErrWork) {
			t.Fatal("unsafe root admitted", err)
		}
	}
	// CommandContext refuses to launch a child when already cancelled. The
	// prepare phase must conservatively return no credential and cleanup debt.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stage := &Runtime{bundle: b, home: occupied}
	if password, err := stage.prepare(ctx); password != "" || !errors.Is(err, ErrStart) || !errors.Is(err, ErrCleanupDebt) {
		t.Fatal("cancelled preparation returned credentials or clean-start status", err)
	}
	if _, err := os.Lstat(stage.dataPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled initdb created data", err)
	}
	writeFixture(t, p+".new", exe, 0700)
	if err := os.Rename(p+".new", p); err != nil {
		t.Fatal(err)
	}
	if err := b.verify(); !errors.Is(err, ErrBundle) {
		t.Fatal("byte-identical replacement adopted", err)
	}
	if _, err := b.executable(0); !errors.Is(err, ErrBundle) {
		t.Fatal("replacement executable accepted", err)
	}
	if err := os.Rename(dir, dir+"-moved"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Rename(dir+"-moved", dir); err != nil {
			t.Error(err)
		}
	})
	if err := b.verify(); !errors.Is(err, ErrBundle) {
		t.Fatal("missing bundle still verified", err)
	}
	if _, err := b.executable(0); !errors.Is(err, ErrBundle) {
		t.Fatal("missing bundle still executable", err)
	}
}

func TestSealRejectsWrongELFInterpreterAndLibraryKinds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		data   []byte
		shared bool
	}{
		{"text", []byte("not an ELF file"), false},
		{"missing-interpreter", layoutHeader(t, ""), false},
		{"unexpected-interpreter", layoutHeader(t, "/wrong/loader\x00"), false},
		{"oversize-interpreter", layoutHeader(t, strings.Repeat("x", 97)), false},
		{"library-with-interpreter", layoutHeader(t, "/lib64/ld-linux-x86-64.so.2\x00"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "header")
			writeFixture(t, p, tc.data, 0600)
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := checkedELF(f, dir, tc.shared); !errors.Is(err, ErrBundle) {
				t.Fatal("unapproved ELF accepted", err)
			}
		})
	}
}

func TestRootAdmissionAndCancellationLeaveFilesAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := CheckIdleWorkRoot(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := CheckIdleWorkRoot(dir); !errors.Is(err, ErrWork) {
		t.Fatal("nonprivate work root accepted", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(dir, "retained"), []byte("owned elsewhere"), 0600)
	for _, p := range []string{dir, dir + "/.", dir + "/retained", "relative-path", dir + "/missing"} {
		if err := CheckIdleWorkRoot(p); !errors.Is(err, ErrWork) {
			t.Fatal("unsafe or occupied root accepted", p, err)
		}
	}
	if r, err := Start(nil, nil, dir); r != nil || !errors.Is(err, ErrWork) {
		t.Fatal("nil context admitted", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if r, err := Start(cancelled, nil, dir); r != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "retained")); err != nil || string(data) != "owned elsewhere" {
		t.Fatal("admission touched retained data", err)
	}
}

func TestSealRejectsUnrepresentableOrUnboundedEntries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(string) error
	}{
		{"ambiguous-name", func(dir string) error { return os.WriteFile(filepath.Join(dir, "line\nbreak"), nil, 0600) }},
		{"dangling-link", func(dir string) error { return os.Symlink("missing", filepath.Join(dir, "item")) }},
		{"fifo", func(dir string) error { return syscall.Mkfifo(filepath.Join(dir, "item"), 0600) }},
		{"writable-path", func(dir string) error {
			p := filepath.Join(dir, "item")
			if err := os.Mkdir(p, 0700); err != nil {
				return err
			}
			return os.Chmod(p, 0777)
		}},
		{"sparse-oversize", func(dir string) error {
			p := filepath.Join(dir, "item")
			if err := os.WriteFile(p, nil, 0600); err != nil {
				return err
			}
			return os.Truncate(p, maxBundleBytes+1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := tc.build(dir); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if _, err := bundleDigest(root); !errors.Is(err, ErrBundle) {
				t.Fatal("unsafe entry included", err)
			}
		})
	}
}
