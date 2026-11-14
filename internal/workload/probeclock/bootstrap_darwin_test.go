//go:build darwin

package probeclock

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDarwinBootstrapChild(t *testing.T) {
	mode := os.Getenv("TCS_TEST_CLOCK_FD3")
	if mode == "" {
		return
	}
	c, e := BootstrapInherited()
	if mode == "pipe" {
		if e != nil || c == nil {
			os.Exit(21)
		}
	} else if mode == "regular" {
		if !errors.Is(e, ErrClock) || c != nil {
			os.Exit(22)
		}
	} else {
		os.Exit(23)
	}
	if _, e = unix.FcntlInt(3, unix.F_GETFD, 0); !errors.Is(e, unix.EBADF) {
		os.Exit(24)
	}
	os.Exit(0)
}
func bootstrapCommand(t *testing.T, mode string, f *os.File) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDarwinBootstrapChild$")
	cmd.Env = []string{"TCS_TEST_CLOCK_FD3=" + mode}
	cmd.ExtraFiles = []*os.File{f}
	output := new(bytes.Buffer)
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd, output
}
func TestDarwinPrivateInheritedPipeBootstrap(t *testing.T) {
	read, write, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	checkPrivateBootstrap(t, read, write)
}
func TestDarwinPrivateInheritedSocketBootstrap(t *testing.T) {
	pair, e := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if e != nil {
		t.Fatal(e)
	}
	unix.CloseOnExec(pair[0])
	unix.CloseOnExec(pair[1])
	if e = unix.SetNonblock(pair[0], true); e != nil {
		unix.Close(pair[0])
		unix.Close(pair[1])
		t.Fatal(e)
	}
	checkPrivateBootstrap(t, os.NewFile(uintptr(pair[0]), "private-parent"), os.NewFile(uintptr(pair[1]), "private-child"))
}
func checkPrivateBootstrap(t *testing.T, read, write *os.File) {
	t.Helper()
	defer read.Close()
	defer write.Close()
	var e error
	if e = read.SetReadDeadline(time.Now().Add(5 * time.Second)); e != nil {
		t.Fatal(e)
	}
	cmd, output := bootstrapCommand(t, "pipe", write)
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	if e = write.Close(); e != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(e)
	}
	data, readErr := io.ReadAll(io.LimitReader(read, 1024))
	waitErr := cmd.Wait()
	if readErr != nil || waitErr != nil {
		t.Fatal("private pipe bootstrap failed", readErr, waitErr)
	}
	if output.Len() != 0 {
		t.Fatal("bootstrap helper wrote public output")
	}
	var b Bootstrap
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&b) != nil {
		t.Fatal("invalid private bootstrap")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		t.Fatal("multiple records")
	}
	key, e := hex.DecodeString(b.Key)
	if e != nil || len(key) != 32 || b.Clock != darwinUptimeRaw || b.Version != Protocol || !strings.HasPrefix(b.Domain, "mono-") || len(b.Domain) != 37 {
		t.Fatal("bootstrap schema mismatch")
	}
}
func TestDarwinInheritedRegularFileRejectedWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-pipe")
	if e := os.WriteFile(path, []byte("unchanged"), 0600); e != nil {
		t.Fatal(e)
	}
	file, e := os.OpenFile(path, os.O_RDWR, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	cmd, output := bootstrapCommand(t, "regular", file)
	if e = cmd.Run(); e != nil {
		t.Fatal("regular descriptor rejection failed", e)
	}
	if output.Len() != 0 {
		t.Fatal("helper wrote public output")
	}
	data, e := os.ReadFile(path)
	if e != nil || string(data) != "unchanged" {
		t.Fatal("rejected descriptor was written")
	}
}
