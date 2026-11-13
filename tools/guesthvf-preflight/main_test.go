package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvalidOriginalDoesNotCreateCopy(t *testing.T) {
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	original := filepath.Join(root, "original")
	if e = os.WriteFile(original, []byte("not the official image"), 0600); e != nil {
		t.Fatal(e)
	}
	alias := filepath.Join(root, "alias")
	if e = os.Symlink(original, alias); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"", filepath.Join(root, "missing"), alias, original, root} {
		if _, e = verify(path, root); e == nil {
			t.Fatal("invalid original accepted", path)
		}
	}
	files, e := os.ReadDir(root)
	if e != nil {
		t.Fatal(e)
	}
	if len(files) != 2 {
		t.Fatal("created copy on invalid input", files)
	}
	data, _ := os.ReadFile(original)
	if string(data) != "not the official image" {
		t.Fatal("original changed")
	}
}

func TestPinnedCopyAdmissionAndFailureCleanup(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "original")
	scratch := filepath.Join(root, "scratch")
	if err = os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 512)
	copy(header, "QFI\xfb")
	binary.BigEndian.PutUint32(header[4:], 3)
	binary.BigEndian.PutUint32(header[20:], 9)
	binary.BigEndian.PutUint64(header[24:], 4096)
	binary.BigEndian.PutUint32(header[96:], 4)
	binary.BigEndian.PutUint32(header[100:], 104)
	for _, tc := range []struct {
		name              string
		badPin, badHeader bool
	}{
		{name: "verified copy"}, {name: "pin mismatch", badPin: true}, {name: "external backing rejected", badHeader: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := bytes.Clone(header)
			if tc.badHeader {
				binary.BigEndian.PutUint64(data[8:], 256)
			}
			if err := os.WriteFile(original, data, 0600); err != nil {
				t.Fatal(err)
			}
			digest := fmt.Sprintf("%x", sha512.Sum512(data))
			if tc.badPin {
				digest = strings.Repeat("0", 128)
			}
			r, err := verifyPinned(original, scratch, int64(len(data)), digest)
			if tc.badPin || tc.badHeader {
				if err == nil || r != (result{}) {
					t.Fatalf("failure accepted: %+v %v", r, err)
				}
			} else if err != nil || r.Result != "pinned-arm-image-preflight-pass" || !r.CopyRemoved || r.BattleReady || r.CopySHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
				t.Fatalf("copy evidence: %+v %v", r, err)
			}
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 0 {
				t.Fatalf("copy leaked: %v %v", entries, err)
			}
			got, err := os.ReadFile(original)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("original modified: %v", err)
			}
		})
	}
	// The public entry point must not admit the synthetic fixture's self-pin.
	if r, err := verify(original, scratch); err == nil || r != (result{}) {
		t.Fatal("official pin was bypassed")
	}
}

type failingOutput struct{}

func (failingOutput) Write([]byte) (int, error) { return 0, errors.New("test output unavailable") }
func TestCLIRejectsInvalidInputWithoutSuccessOutput(t *testing.T) {
	for _, args := range [][]string{nil, {"--unknown"}, {"--original"}, {"extra"}, {"--original", "missing", "--scratch", "missing"}} {
		var out, diagnostics bytes.Buffer
		if code := run(args, &out, &diagnostics); code == 0 || out.Len() != 0 || diagnostics.Len() == 0 {
			t.Fatalf("args=%v code=%d out=%q err=%q", args, code, out.String(), diagnostics.String())
		}
	}
}
func TestResultOutputFailureIsNotSuccess(t *testing.T) {
	r := result{Result: "pinned-arm-image-preflight-pass", CopySHA256: strings.Repeat("a", 64), CopyRemoved: true}
	var out, diagnostics bytes.Buffer
	if code := writeResult(&out, &diagnostics, r); code != 0 || diagnostics.Len() != 0 {
		t.Fatalf("code=%d err=%q", code, diagnostics.String())
	}
	var got result
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got != r || got.BattleReady {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	if code := writeResult(failingOutput{}, &diagnostics, r); code == 0 || !strings.Contains(diagnostics.String(), "test output unavailable") {
		t.Fatalf("write failure code=%d err=%q", code, diagnostics.String())
	}
}

func TestCLIHelpDoesNotValidateOrEmitReceipt(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if code := run([]string{"--help"}, &out, &diagnostics); code != 0 || out.Len() != 0 || !strings.Contains(diagnostics.String(), "canonical pinned original image") {
		t.Fatalf("help code=%d out=%q err=%q", code, out.String(), diagnostics.String())
	}
}
