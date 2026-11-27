//go:build (!linux && !darwin) || sqlite3_flock || sqlite3_dotlk

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEC2NativeMainRepairRefusesUnsupportedLockingBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "untouched.sqlite")
	var stdout bytes.Buffer
	err := run(t.Context(), []string{ec2NativeMainRepairCommand, "plan", "--state", path,
		"--region", "us-east-1", "--vpc-id", "vpc-00000000000000172"}, &stdout)
	if err == nil || !strings.Contains(err.Error(), "supported SQLite file locking") || stdout.Len() != 0 {
		t.Fatal(err, stdout.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unsupported build initialized a database", err)
	}
}
