//go:build darwin

package probeclock

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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
	if testing.CoverMode() != "" {
		dir := flag.Lookup("test.gocoverdir")
		if dir == nil || dir.Value.String() == "" {
			t.Fatal("covered bootstrap test requires the parent coverage directory")
		}
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+dir.Value.String())
	}
	cmd.ExtraFiles = []*os.File{f}
	output := new(bytes.Buffer)
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd, output
}

func TestDarwinBootstrapCoverageCountersJoinParent(t *testing.T) {
	if testing.CoverMode() == "" {
		t.Skip("requires coverage instrumentation")
	}
	file, err := os.CreateTemp(t.TempDir(), "rejected-file")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cmd, output := bootstrapCommand(t, "regular", file)
	if err := cmd.Run(); err != nil || output.Len() != 0 {
		t.Fatal("coverage child must exit silently", err)
	}
	dir := flag.Lookup("test.gocoverdir").Value.String()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "covcounters.") && strings.Contains(file.Name(), fmt.Sprintf(".%d.", cmd.Process.Pid)) {
			found = true
		}
	}
	if !found {
		t.Fatal("child counters are missing from the parent report's input directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tool := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "tool", "covdata", "func", "-i="+dir)
	data, err := tool.CombinedOutput()
	if err != nil {
		t.Fatal("merge child coverage", err, string(data))
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[1] == "BootstrapInherited" && fields[2] == "100.0%" {
			return
		}
	}
	t.Fatal("merged child counters do not cover BootstrapInherited")
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
