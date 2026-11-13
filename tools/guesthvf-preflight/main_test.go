package main

import (
	"os"
	"path/filepath"
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
