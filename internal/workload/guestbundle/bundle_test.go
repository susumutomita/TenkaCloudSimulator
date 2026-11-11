package guestbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func fixture(t *testing.T, headers []*tar.Header, bodies [][]byte) ([]byte, Manifest) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	m := Manifest{OS: runtime.GOOS, Arch: runtime.GOARCH}
	for i, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(bodies[i]); err != nil {
			t.Fatal(err)
		}
		m.Files = append(m.Files, File{Path: h.Name, Size: h.Size, SHA256: sum(bodies[i]), Executable: i == 0})
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	m.ArchiveSHA256 = sum(buf.Bytes())
	return buf.Bytes(), m
}
func good(t *testing.T) ([]byte, Manifest) {
	return fixture(t, []*tar.Header{{Name: "bin/emulator", Size: 7, Mode: 0777, Typeflag: tar.TypeReg}, {Name: "images/base", Size: 4, Mode: 0666, Typeflag: tar.TypeReg}}, [][]byte{[]byte("runtime"), []byte("disk")})
}
func TestExtractPrivateVerifiedAssets(t *testing.T) {
	data, m := good(t)
	parent := t.TempDir()
	b, err := Extract(context.Background(), parent, bytes.NewReader(data), m)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range m.Files {
		p, err := b.Path(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(p)
		if err != nil || sum(raw) != f.SHA256 {
			t.Fatalf("read/hash: %v", err)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0400)
		if f.Executable {
			want = 0500
		}
		if info.Mode().Perm() != want {
			t.Fatal(info.Mode())
		}
	}
	info, err := os.Stat(b.directory)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("private directory: %v %v", info, err)
	}
	if _, err := b.Path("../outside"); err == nil {
		t.Fatal("unknown asset accepted")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Path("bin/emulator"); err == nil {
		t.Fatal("closed asset")
	}
	var nilBundle *Bundle
	if err := nilBundle.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := nilBundle.Path("x"); err == nil {
		t.Fatal("nil asset")
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 0 {
		t.Fatal("cleanup retained files")
	}
}
func TestExtractRejectsManifest(t *testing.T) {
	changes := map[string]func(*Manifest){
		"platform": func(m *Manifest) { m.OS = "unsupported" }, "arch": func(m *Manifest) { m.Arch = "unsupported" },
		"archivehash": func(m *Manifest) { m.ArchiveSHA256 = "bad" }, "empty": func(m *Manifest) { m.Files = nil },
		"duplicate": func(m *Manifest) { m.Files = append(m.Files, m.Files[0]) }, "traversal": func(m *Manifest) { m.Files[0].Path = "../outside" },
		"absolute": func(m *Manifest) { m.Files[0].Path = "/outside" }, "windows": func(m *Manifest) { m.Files[0].Path = "C:\\outside" },
		"dot": func(m *Manifest) { m.Files[0].Path = "." }, "negative": func(m *Manifest) { m.Files[0].Size = -1 },
		"size": func(m *Manifest) { m.Files[0].Size = MaxBytes + 1 }, "total": func(m *Manifest) { m.Files[0].Size = MaxBytes },
		"filehash": func(m *Manifest) { m.Files[0].SHA256 = strings.Repeat("A", 64) }, "count": func(m *Manifest) { m.Files = make([]File, MaxFiles+1) },
		"parentfile": func(m *Manifest) { m.Files[1].Path = "bin/emulator/child" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			data, m := good(t)
			change(&m)
			parent := t.TempDir()
			if _, err := Extract(context.Background(), parent, bytes.NewReader(data), m); err == nil {
				t.Fatal("invalid manifest accepted")
			}
			entries, _ := os.ReadDir(parent)
			if len(entries) != 0 {
				t.Fatal("partial output")
			}
		})
	}
}
func TestExtractRejectsCorruptArchives(t *testing.T) {
	cases := map[string]func([]byte, *Manifest) []byte{
		"filehash":    func(b []byte, m *Manifest) []byte { m.Files[0].SHA256 = sum([]byte("changed")); return b },
		"archivehash": func(b []byte, m *Manifest) []byte { m.ArchiveSHA256 = sum([]byte("changed")); return b },
		"missing": func(b []byte, m *Manifest) []byte {
			m.Files = append(m.Files, File{Path: "missing", SHA256: sum(nil)})
			return b
		},
		"unknown":     func(b []byte, m *Manifest) []byte { m.Files = m.Files[:1]; return b },
		"size":        func(b []byte, m *Manifest) []byte { m.Files[0].Size++; return b },
		"invalidgzip": func(b []byte, m *Manifest) []byte { return []byte("bad") },
		"truncated":   func(b []byte, m *Manifest) []byte { return b[:len(b)-5] },
		"trailing": func(b []byte, m *Manifest) []byte {
			b = append(b, []byte("junk")...)
			m.ArchiveSHA256 = sum(b)
			return b
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			data, m := good(t)
			data = change(data, &m)
			parent := t.TempDir()
			if _, err := Extract(context.Background(), parent, bytes.NewReader(data), m); err == nil {
				t.Fatal("corrupt archive accepted")
			}
			entries, _ := os.ReadDir(parent)
			if len(entries) != 0 {
				t.Fatal("partial output")
			}
		})
	}
}
func TestExtractRejectsArchiveTypesAndDuplicate(t *testing.T) {
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeDir, tar.TypeChar, tar.TypeFifo} {
		t.Run(string(kind), func(t *testing.T) {
			h := &tar.Header{Name: "asset", Typeflag: kind, Linkname: "outside", Mode: 0777}
			if kind == tar.TypeDir {
				h.Linkname = ""
			}
			data, m := fixture(t, []*tar.Header{h}, [][]byte{nil})
			if _, err := Extract(context.Background(), t.TempDir(), bytes.NewReader(data), m); err == nil {
				t.Fatal("unsafe entry accepted")
			}
		})
	}
	data, m := fixture(t, []*tar.Header{{Name: "asset", Typeflag: tar.TypeReg}, {Name: "asset", Typeflag: tar.TypeReg}}, [][]byte{nil, nil})
	m.Files = m.Files[:1]
	if _, err := Extract(context.Background(), t.TempDir(), bytes.NewReader(data), m); err == nil {
		t.Fatal("duplicate entry accepted")
	}
}
func TestExtractCancellationAndInputFailure(t *testing.T) {
	data, m := good(t)
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Extract(ctx, parent, bytes.NewReader(data), m); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Extract(nil, parent, bytes.NewReader(data), m); err == nil {
		t.Fatal("nil context")
	}
	if _, err := Extract(context.Background(), parent, nil, m); err == nil {
		t.Fatal("nil input")
	}
	if _, err := Extract(context.Background(), filepath.Join(parent, "missing"), bytes.NewReader(data), m); err == nil {
		t.Fatal("missing parent")
	}
	r := contextReader{ctx, strings.NewReader("x")}
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Extract(context.Background(), parent, brokenReader{}, m); err == nil {
		t.Fatal("read failure")
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 0 {
		t.Fatal("partial output")
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// This opt-in test verifies actual prepared assets without executing any helper
// or VM. The supplied manifest is build-time local evidence, not release approval.
func TestPreparedAssets(t *testing.T) {
	manifestPath := os.Getenv("TENKA_TEST_GUEST_MANIFEST")
	archivePath := os.Getenv("TENKA_TEST_GUEST_ARCHIVE")
	if manifestPath == "" && archivePath == "" {
		t.Skip("no prepared assets supplied")
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	b, err := Extract(context.Background(), t.TempDir(), archive, m)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInfoReturnsPinnedCopyAndRejectsClosedBundle(t *testing.T) {
	data, m := good(t)
	b, err := Extract(context.Background(), t.TempDir(), bytes.NewReader(data), m)
	if err != nil {
		t.Fatal(err)
	}
	info, err := b.Info("bin/emulator")
	if err != nil || info != m.Files[0] {
		t.Fatal(info, err)
	}
	info.SHA256 = "changed"
	again, err := b.Info("bin/emulator")
	if err != nil || again.SHA256 != m.Files[0].SHA256 {
		t.Fatal("metadata was mutable", err)
	}
	if _, err := b.Info("unknown"); err == nil {
		t.Fatal("unknown asset")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Info("bin/emulator"); err == nil {
		t.Fatal("closed bundle")
	}
	var missing *Bundle
	if _, err := missing.Info("bin/emulator"); err == nil {
		t.Fatal("nil bundle")
	}
}
