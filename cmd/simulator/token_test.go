package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOperatorTokenPrivatePersistence(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/operator"
	token, err := operatorToken(path)
	if err != nil || len(token) != 64 {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	again, err := operatorToken(path)
	if err != nil || again != token {
		t.Fatal("token changed", err)
	}
	for _, content := range []string{"short", strings.Repeat("x", 513)} {
		if err = os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = operatorToken(path); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = operatorToken(path); err == nil {
		t.Fatal("public token accepted")
	}
	if _, err = operatorToken(dir); err == nil {
		t.Fatal("directory token accepted")
	}
	if err = os.Symlink(path, dir+"/link"); err != nil {
		t.Fatal(err)
	}
	if _, err = operatorToken(dir + "/link"); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err = operatorToken(dir + "/missing/token"); err == nil {
		t.Fatal("missing parent accepted")
	}
}
func TestAuthenticatedCLIStartsAndCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	path := t.TempDir() + "/token"
	if err := run(ctx, []string{"--listen", "127.0.0.1:0", "--token-file", path}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--listen", "127.0.0.1:0", "--token-file", t.TempDir()}, {"--listen", "127.0.0.1:0", "--token-file", t.TempDir() + "/missing/token"}, {"--listen", "127.0.0.1:0", "--allow-remote", "--unsafe-no-auth"}} {
		if err := run(context.Background(), args, io.Discard); err == nil {
			t.Fatal(args)
		}
	}
}
