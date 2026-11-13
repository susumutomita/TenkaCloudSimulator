//go:build darwin || linux

package guesthvf

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func header() []byte {
	b := make([]byte, 512)
	copy(b, "QFI\xfb")
	binary.BigEndian.PutUint32(b[4:], 3)
	binary.BigEndian.PutUint32(b[20:], 9)
	binary.BigEndian.PutUint64(b[24:], 4096)
	binary.BigEndian.PutUint32(b[96:], 4)
	binary.BigEndian.PutUint32(b[100:], 104)
	return b
}
func pin(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func TestStandaloneHeaderRejectsExternalAuthority(t *testing.T) {
	if checkStandaloneHeader(bytes.NewReader(header()), 512) != nil {
		t.Fatal("minimal subset rejected")
	}
	for _, off := range []int{4, 8, 16, 32, 60, 64, 72, 80, 88, 96, 100} {
		b := header()
		b[off+3] ^= 1
		if checkStandaloneHeader(bytes.NewReader(b), 512) == nil {
			t.Fatalf("header field %d", off)
		}
	}
	for _, kind := range []uint32{0xe2792aca, 0x44415441, 0x0537be77, 0x23852875, 0x12345678} {
		b := header()
		binary.BigEndian.PutUint32(b[104:], kind)
		if checkStandaloneHeader(bytes.NewReader(b), 512) == nil {
			t.Fatalf("extension %x", kind)
		}
	}
	b := header()
	binary.BigEndian.PutUint32(b[104:], 0x6803f857)
	binary.BigEndian.PutUint32(b[108:], 48)
	if checkStandaloneHeader(bytes.NewReader(b), 512) != nil {
		t.Fatal("display table rejected")
	}
	binary.BigEndian.PutUint32(b[108:], 4096)
	if checkStandaloneHeader(bytes.NewReader(b), 512) == nil {
		t.Fatal("overflow")
	}
}
func TestIndependentImageKeepsOriginalUnchanged(t *testing.T) {
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	b := header()
	origPath := filepath.Join(dir, "original")
	copyPath := filepath.Join(dir, "candidate")
	for _, p := range []string{origPath, copyPath} {
		if e = os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	original, e := os.Open(origPath)
	if e != nil {
		t.Fatal(e)
	}
	defer original.Close()
	f, e := OpenIndependentImage(copyPath, pin(b), original)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := f.Write([]byte("x")); e == nil {
		t.Fatal("validation FD is writable")
	}
	f.Close()
	unchanged, _ := os.ReadFile(origPath)
	if !bytes.Equal(unchanged, b) {
		t.Fatal("original changed")
	}
	if _, e = OpenIndependentImage(origPath, pin(b), original); e == nil {
		t.Fatal("original admitted")
	}
	if _, e = OpenIndependentImage(copyPath, pin([]byte("bad")), original); e == nil {
		t.Fatal("bad pin")
	}
	alias := filepath.Join(dir, "alias")
	if e = os.Symlink(copyPath, alias); e != nil {
		t.Fatal(e)
	}
	if _, e = OpenIndependentImage(alias, pin(b), original); e == nil {
		t.Fatal("symlink")
	}
	link := filepath.Join(dir, "hardlink")
	if e = os.Link(copyPath, link); e != nil {
		t.Fatal(e)
	}
	if _, e = OpenIndependentImage(copyPath, pin(b), original); e == nil {
		t.Fatal("shared inode")
	}
	if e = os.Remove(link); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(copyPath, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = OpenIndependentImage(copyPath, pin(b), original); e == nil {
		t.Fatal("public image")
	}
	os.Chmod(copyPath, 0600)
	if e = os.Chmod(dir, 0755); e != nil {
		t.Fatal(e)
	}
	if _, e = OpenIndependentImage(copyPath, pin(b), original); e == nil {
		t.Fatal("public directory")
	}
}

func TestImagePreflightRejectsMissingReferenceAndMalformedPin(t *testing.T) {
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	os.Chmod(dir, 0700)
	p := filepath.Join(dir, "image")
	b := header()
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = OpenIndependentImage(p, pin(b), nil); e == nil {
		t.Fatal("nil original")
	}
	original, e := os.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	for _, digest := range []string{"", "bad", pin(b) + "00"} {
		if _, e = OpenIndependentImage(p, digest, original); e == nil {
			t.Fatal("malformed digest")
		}
	}
	original.Close()
	if _, e = OpenIndependentImage(p, pin(b), original); e == nil {
		t.Fatal("closed original")
	}
}
func TestImageHeaderRejectsTruncatedAndDuplicateExtensions(t *testing.T) {
	b := header()
	for _, n := range []int{0, 72, 103, 104, 107} {
		if checkStandaloneHeader(bytes.NewReader(b[:n]), 512) == nil {
			t.Fatalf("truncated %d", n)
		}
	}
	for _, offset := range []int{104, 160} {
		binary.BigEndian.PutUint32(b[offset:], 0x6803f857)
		binary.BigEndian.PutUint32(b[offset+4:], 48)
	}
	if checkStandaloneHeader(bytes.NewReader(b), 512) == nil {
		t.Fatal("duplicate table")
	}
	b = header()
	binary.BigEndian.PutUint32(b[108:], 8)
	if checkStandaloneHeader(bytes.NewReader(b), 512) == nil {
		t.Fatal("nonempty terminator")
	}
}

func TestImageHeaderAllowsOnlyZeroOptionalV3Fields(t *testing.T) {
	b := header()
	binary.BigEndian.PutUint32(b[100:], 112)
	if checkStandaloneHeader(bytes.NewReader(b), 512) != nil {
		t.Fatal("zero optional header rejected")
	}
	for _, off := range []int{104, 105, 111} {
		c := append([]byte(nil), b...)
		c[off] = 1
		if checkStandaloneHeader(bytes.NewReader(c), 512) == nil {
			t.Fatal("nonzero optional field")
		}
	}
	binary.BigEndian.PutUint32(b[100:], 120)
	if checkStandaloneHeader(bytes.NewReader(b), 512) == nil {
		t.Fatal("unknown header admitted")
	}
}

func TestOptionalHeaderExtensionBoundaries(t *testing.T) {
	b := header()
	binary.BigEndian.PutUint32(b[100:], 112)
	binary.BigEndian.PutUint32(b[112:], 0x6803f857)
	binary.BigEndian.PutUint32(b[116:], 48)
	if err := checkStandaloneHeader(bytes.NewReader(b), 512); err != nil {
		t.Fatal("optional header plus display table rejected", err)
	}
	for _, size := range []int{104, 111, 112, 119, 168, 175} {
		if err := checkStandaloneHeader(bytes.NewReader(b[:size]), 512); err == nil {
			t.Fatalf("truncated optional/extension read admitted at %d", size)
		}
	}
	binary.BigEndian.PutUint32(b[168:], 0x44415441)
	if checkStandaloneHeader(bytes.NewReader(b), 512) == nil {
		t.Fatal("external data after display table accepted")
	}
	binary.BigEndian.PutUint32(b[168:], 0x6803f857)
	binary.BigEndian.PutUint32(b[172:], 48)
	if checkStandaloneHeader(bytes.NewReader(b), 512) == nil {
		t.Fatal("duplicate feature table after optional header accepted")
	}
	// A complete display table must not excuse a truncated end marker.
	b = header()
	binary.BigEndian.PutUint32(b[104:], 0x6803f857)
	binary.BigEndian.PutUint32(b[108:], 384)
	if checkStandaloneHeader(bytes.NewReader(b[:496]), 512) == nil {
		t.Fatal("missing terminator read accepted")
	}
}
func TestImageDimensionAdmissionBounds(t *testing.T) {
	for _, bits := range []uint32{0, 8, 22, 32} {
		b := header()
		binary.BigEndian.PutUint32(b[20:], bits)
		if checkStandaloneHeader(bytes.NewReader(b), 512) == nil {
			t.Fatalf("cluster exponent %d", bits)
		}
	}
	for _, virtualSize := range []uint64{0, uint64(maxImageBytes) + 1} {
		b := header()
		binary.BigEndian.PutUint64(b[24:], virtualSize)
		if checkStandaloneHeader(bytes.NewReader(b), 512) == nil {
			t.Fatalf("virtual size %d", virtualSize)
		}
	}
	b := header()
	binary.BigEndian.PutUint64(b[24:], uint64(maxImageBytes))
	if checkStandaloneHeader(bytes.NewReader(b), 512) != nil {
		t.Fatal("virtual upper bound rejected")
	}
	if checkStandaloneHeader(bytes.NewReader(b), 511) == nil {
		t.Fatal("file shorter than first cluster accepted")
	}
}

// A candidate is untrusted as a file type even when its name is canonical and
// its parent is private. In particular, opening a FIFO must not await a writer.
func TestIndependentImageRejectsNonRegularAndShortCandidates(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	b := header()
	originalPath := filepath.Join(dir, "original")
	if err = os.WriteFile(originalPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	fifo := filepath.Join(dir, "fifo")
	if err = syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(dir, "directory")
	if err = os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	short := filepath.Join(dir, "short")
	if err = os.WriteFile(short, b[:511], 0600); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{fifo, directory, short} {
		t.Run(filepath.Base(candidate), func(t *testing.T) {
			f, err := OpenIndependentImage(candidate, pin(b), original)
			if f != nil {
				f.Close()
				t.Fatal("rejected candidate returned an owned descriptor")
			}
			if err == nil {
				t.Fatal("invalid file type or size admitted")
			}
		})
	}
	got, err := os.ReadFile(originalPath)
	if err != nil || !bytes.Equal(got, b) {
		t.Fatal("original changed", err)
	}
}
