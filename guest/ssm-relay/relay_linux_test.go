//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestsession"
	"golang.org/x/sys/unix"
)

const testUUID = "0123abcd-4567-89ab-cdef-0123456789ab"

var testSession = guestsession.ID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

// openRawPTY returns a raw-mode PTY pair: the slave path stands in for the
// virtio character device, the master is the host side of that port.
func openRawPTY(t *testing.T) (*os.File, string) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Skip("no PTY support:", err)
	}
	master := os.NewFile(uintptr(fd), "test-ptmx")
	t.Cleanup(func() { master.Close() })
	if err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/dev/pts/%d", n)
	slave, err := os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Keep a slave reference open so the master never reads EIO between the
	// relay's open/close of the device path.
	t.Cleanup(func() { slave.Close() })
	for _, f := range []int{int(slave.Fd()), fd} {
		tio, err := unix.IoctlGetTermios(f, unix.TCGETS)
		if err != nil {
			t.Fatal(err)
		}
		tio.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
		tio.Oflag &^= unix.OPOST
		tio.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
		tio.Cflag &^= unix.CSIZE | unix.PARENB
		tio.Cflag |= unix.CS8
		tio.Cc[unix.VMIN], tio.Cc[unix.VTIME] = 1, 0
		if err = unix.IoctlSetTermios(f, unix.TCSETS, tio); err != nil {
			t.Fatal(err)
		}
	}
	return master, path
}

// setGuest points the relay's fixed guest locations at temporary fixtures.
func setGuest(t *testing.T, euid int, marker, uuid, dev string) {
	t.Helper()
	dir := t.TempDir()
	old := []string{imageMarkerPath, productUUIDPath, sessionDevice, guestShell, guestHome}
	oldEUID := geteuid
	t.Cleanup(func() {
		imageMarkerPath, productUUIDPath, sessionDevice, guestShell, guestHome = old[0], old[1], old[2], old[3], old[4]
		geteuid = oldEUID
	})
	geteuid = func() int { return euid }
	imageMarkerPath, productUUIDPath = filepath.Join(dir, "marker"), filepath.Join(dir, "uuid")
	if marker != "" {
		if err := os.WriteFile(imageMarkerPath, []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if uuid != "" {
		if err := os.WriteFile(productUUIDPath, []byte(uuid), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sessionDevice = dev
	guestShell, guestHome = "/bin/bash", dir
}

func TestRelayRefusesUnqualifiedGuest(t *testing.T) {
	const marker = "tenkacloud-guest-session-v1\n"
	regular := filepath.Join(t.TempDir(), "not-a-device")
	if err := os.WriteFile(regular, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		euid                 int
		marker, uuid, device string
		want                 string
	}{
		"not root":       {1000, marker, testUUID, regular, "guest root"},
		"no marker":      {0, "", testUUID, regular, "marker missing"},
		"wrong marker":   {0, "tenkacloud-guest-session-v0\n", testUUID, regular, "marker missing"},
		"no uuid":        {0, marker, "", regular, guestsession.ErrProtocol.Error()},
		"invalid uuid":   {0, marker, "not-a-uuid\n", regular, guestsession.ErrProtocol.Error()},
		"missing device": {0, marker, strings.ToUpper(testUUID) + "\n", filepath.Join(t.TempDir(), "absent"), "no such file"},
		"regular file":   {0, marker, testUUID, regular, "not a character device"},
	} {
		t.Run(name, func(t *testing.T) {
			setGuest(t, c.euid, c.marker, c.uuid, c.device)
			err := run()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("run() = %v, want %q", err, c.want)
			}
			if relayExitCode(err) != 1 {
				t.Fatal("refusal would not restart the relay")
			}
		})
	}
}

func startFrame(t *testing.T, rows, cols uint16) guestsession.Frame {
	t.Helper()
	size, err := guestsession.Size(rows, cols)
	if err != nil {
		t.Fatal(err)
	}
	return guestsession.Frame{Kind: guestsession.Start, Session: testSession, Payload: size}
}

// readUntilExit collects Output frames until Exit and returns text and status.
func readUntilExit(t *testing.T, host *guestsession.Codec) (string, int32) {
	t.Helper()
	var out bytes.Buffer
	for {
		f, err := host.Read()
		if err != nil {
			t.Fatalf("host read after %q: %v", out.String(), err)
		}
		switch f.Kind {
		case guestsession.Output:
			if f.Session != testSession {
				t.Fatal("output for another session")
			}
			out.Write(f.Payload)
		case guestsession.Exit:
			return out.String(), int32(binary.BigEndian.Uint32(f.Payload))
		default:
			t.Fatalf("unexpected frame kind %d", f.Kind)
		}
	}
}

func TestRelayRunsOneShellSessionOverTheCharacterDevice(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	master, dev := openRawPTY(t)
	setGuest(t, 0, "tenkacloud-guest-session-v1\n", strings.ToUpper(testUUID)+"\n", dev)
	done := make(chan error, 1)
	go func() { done <- run() }()
	host := guestsession.NewCodec(master)
	hello, err := host.Read()
	if err != nil || hello.Kind != guestsession.Hello || string(hello.Payload) != testUUID {
		t.Fatalf("hello %+v %v", hello, err)
	}
	if err = host.Write(startFrame(t, 24, 80)); err != nil {
		t.Fatal(err)
	}
	if ready, err := host.Read(); err != nil || ready.Kind != guestsession.Ready || ready.Session != testSession {
		t.Fatalf("ready %+v %v", ready, err)
	}
	resize, _ := guestsession.Size(40, 120)
	if err = host.Write(guestsession.Frame{Kind: guestsession.Resize, Session: testSession, Payload: resize}); err != nil {
		t.Fatal(err)
	}
	cmd := "stty size; echo relay-$((40+2)); pwd; exit 7\n"
	if err = host.Write(guestsession.Frame{Kind: guestsession.Input, Session: testSession, Payload: []byte(cmd)}); err != nil {
		t.Fatal(err)
	}
	text, status := readUntilExit(t, host)
	if status != 7 || !strings.Contains(text, "relay-42") || !strings.Contains(text, "40 120") || !strings.Contains(text, guestHome) {
		t.Fatalf("status %d output %q", status, text)
	}
	select {
	case err = <-done:
		if !errors.Is(err, io.EOF) || relayExitCode(err) != 1 {
			t.Fatalf("completed session must end the relay with EOF: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("relay did not finish after shell exit")
	}
}

func TestRelayRejectsNonStartFrameAfterHello(t *testing.T) {
	master, dev := openRawPTY(t)
	setGuest(t, 0, "tenkacloud-guest-session-v1\n", testUUID, dev)
	done := make(chan error, 1)
	go func() { done <- run() }()
	host := guestsession.NewCodec(master)
	if _, err := host.Read(); err != nil {
		t.Fatal(err)
	}
	if err := host.Write(guestsession.Frame{Kind: guestsession.Close, Session: testSession}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, guestsession.ErrProtocol) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("relay accepted a non-Start frame")
	}
}

// pipeStream is the host<->relay channel for direct servePTY tests.
type pipeStream struct {
	io.Reader
	io.Writer
}

func newPipes(t *testing.T) (relay, host *guestsession.Codec, closeHost func()) {
	t.Helper()
	r1, w1, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r2, w2, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r1.Close(); w1.Close(); r2.Close(); w2.Close() })
	return guestsession.NewCodec(pipeStream{r1, w2}), guestsession.NewCodec(pipeStream{r2, w1}), func() { w1.Close() }
}

func servePTYAsync(t *testing.T, relay *guestsession.Codec, start guestsession.Frame) (<-chan error, context.CancelFunc) {
	t.Helper()
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	old := []string{guestShell, guestHome}
	t.Cleanup(func() { guestShell, guestHome = old[0], old[1] })
	guestShell, guestHome = "/bin/bash", t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- servePTY(ctx, func() {}, relay, start) }()
	return done, cancel
}

func waitServe(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("servePTY did not return")
		return nil
	}
}

func TestServePTYHostCloseKillsShellAndReportsExit(t *testing.T) {
	relay, host, _ := newPipes(t)
	done, _ := servePTYAsync(t, relay, startFrame(t, 24, 80))
	if f, err := host.Read(); err != nil || f.Kind != guestsession.Ready {
		t.Fatalf("ready %+v %v", f, err)
	}
	if err := host.Write(guestsession.Frame{Kind: guestsession.Close, Session: testSession}); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			if f, err := host.Read(); err != nil || f.Kind == guestsession.Exit {
				return
			}
		}
	}()
	if err := waitServe(t, done); !errors.Is(err, io.EOF) {
		t.Fatalf("host close must end with EOF after Exit: %v", err)
	}
}

func TestServePTYRejectsForeignSessionInput(t *testing.T) {
	relay, host, _ := newPipes(t)
	done, _ := servePTYAsync(t, relay, startFrame(t, 24, 80))
	if _, err := host.Read(); err != nil {
		t.Fatal(err)
	}
	other := testSession
	other[0] ^= 0xff
	if err := host.Write(guestsession.Frame{Kind: guestsession.Input, Session: other, Payload: []byte("id\n")}); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, pipeReader(host)) }()
	if err := waitServe(t, done); err == nil || !errors.Is(err, guestsession.ErrProtocol) {
		t.Fatalf("foreign session input accepted: %v", err)
	}
}

func TestServePTYRejectsUnexpectedFrameKind(t *testing.T) {
	relay, host, _ := newPipes(t)
	done, _ := servePTYAsync(t, relay, startFrame(t, 24, 80))
	if _, err := host.Read(); err != nil {
		t.Fatal(err)
	}
	if err := host.Write(guestsession.Frame{Kind: guestsession.Ready, Session: testSession}); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, pipeReader(host)) }()
	if err := waitServe(t, done); err == nil || !strings.Contains(err.Error(), "guest input failed") {
		t.Fatalf("unexpected frame kind accepted: %v", err)
	}
}

func TestServePTYCancellationStopsShell(t *testing.T) {
	relay, host, _ := newPipes(t)
	done, cancel := servePTYAsync(t, relay, startFrame(t, 24, 80))
	if _, err := host.Read(); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, pipeReader(host)) }()
	cancel()
	if err := waitServe(t, done); err == nil || !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("canceled session reported %v", err)
	}
}

func TestServePTYReadyWriteFailureCleansUp(t *testing.T) {
	relay, _, _ := newPipes(t)
	broken := guestsession.NewCodec(pipeStream{strings.NewReader(""), failingWriter{}})
	_ = relay
	done, _ := servePTYAsync(t, broken, startFrame(t, 24, 80))
	if err := waitServe(t, done); err == nil || !strings.Contains(err.Error(), "write refused") {
		t.Fatalf("ready write failure not reported: %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write refused") }

// pipeReader drains a host codec's underlying frames for tests that only care
// about the relay's result.
func pipeReader(c *guestsession.Codec) io.Reader { return codecDrain{c} }

type codecDrain struct{ c *guestsession.Codec }

func (d codecDrain) Read(p []byte) (int, error) {
	if _, err := d.c.Read(); err != nil {
		return 0, err
	}
	return 0, nil
}

func rawPipe(t *testing.T) (r, w int) {
	t.Helper()
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(p[0]); unix.Close(p[1]) })
	return p[0], p[1]
}

func TestBoundedFDHonorsCancellationAndDeadlines(t *testing.T) {
	r, w := rawPipe(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (boundedFD{ctx: canceled, fd: r}).Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
	if _, err := (boundedFD{ctx: canceled, fd: w}).Write([]byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write: %v", err)
	}
	short, stop := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer stop()
	started := time.Now()
	if _, err := (boundedFD{ctx: short, fd: r}).Read(make([]byte, 1)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("empty pipe read past deadline: %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("bounded read overstayed its deadline")
	}
	// Fill the pipe so writes hit EAGAIN and then the bounded poll deadline.
	full, stopFull := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer stopFull()
	chunk := bytes.Repeat([]byte{'a'}, 1<<16)
	writer := boundedFD{ctx: full, fd: w}
	var err error
	for i := 0; i < 64 && err == nil; i++ {
		_, err = writer.Write(chunk)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("full pipe write: %v", err)
	}
	// Data, a waited read, then EOF through the duplex wrapper.
	r2, w2 := rawPipe(t)
	d := duplexFD{reader: boundedFD{ctx: context.Background(), fd: r2}, writer: boundedFD{ctx: context.Background(), fd: w2}}
	go func() {
		time.Sleep(80 * time.Millisecond)
		_, _ = d.Write([]byte("ok"))
		unix.Close(w2)
	}()
	buf := make([]byte, 8)
	if n, err := d.Read(buf); n != 2 || err != nil || string(buf[:2]) != "ok" {
		t.Fatal(n, err)
	}
	if _, err := d.Read(buf); err != io.EOF {
		t.Fatalf("closed writer must read EOF: %v", err)
	}
	// A descriptor that is not open is reported, never waited on.
	if _, err := (boundedFD{ctx: context.Background(), fd: -1}).Read(buf); err == nil {
		t.Fatal("read from invalid fd succeeded")
	}
}
