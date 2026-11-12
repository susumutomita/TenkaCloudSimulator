//go:build linux

// Guest-only program. This executable belongs in a reviewed pinned guest image,
// never in the host server process. main refuses to start a shell until it has
// opened the fixed virtio character device and obtained the guest DMI UUID.
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestsession"
	"golang.org/x/sys/unix"
)

const device = "/dev/virtio-ports/org.tenkacloud.session.0"
const maxSessionBytes = 4 << 20

// Fixed guest locations. They are variables only so host-side tests can point
// the unchanged relay logic at a temporary marker, PTY and shell; production
// never assigns them.
var (
	geteuid         = os.Geteuid
	imageMarkerPath = "/etc/tenkacloud/session-relay-image"
	productUUIDPath = "/sys/class/dmi/id/product_uuid"
	sessionDevice   = device
	guestShell      = "/bin/bash"
	guestHome       = "/root"
)

func main() { os.Exit(relayExitCode(run())) }

// Successful session teardown returns EOF to request a fresh service process.
func relayExitCode(err error) int {
	if err != nil {
		return 1
	}
	return 0
}
func run() error {
	if geteuid() != 0 {
		return fmt.Errorf("guest relay requires guest root")
	}
	marker, err := os.ReadFile(imageMarkerPath)
	if err != nil || string(marker) != "tenkacloud-guest-session-v1\n" {
		return fmt.Errorf("guest image marker missing")
	}
	raw, err := os.ReadFile(productUUIDPath)
	uuid := strings.ToLower(strings.TrimSpace(string(raw)))
	if err != nil || !guestsession.ValidUUID(uuid) {
		return guestsession.ErrProtocol
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	portFD, err := unix.Open(sessionDevice, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	port := os.NewFile(uintptr(portFD), "guest-session-port")
	defer port.Close()
	st, err := port.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("guest port is not a character device")
	}
	readCtx, stopRead := context.WithCancel(ctx)
	defer stopRead()
	codec := guestsession.NewCodec(duplexFD{reader: boundedFD{ctx: readCtx, fd: portFD}, writer: boundedFD{ctx: ctx, fd: portFD}})
	if err = codec.Write(guestsession.Frame{Kind: guestsession.Hello, Payload: []byte(uuid)}); err != nil {
		return err
	}
	// One PTY per retained channel; systemd restarts the relay after channel EOF.
	for {
		frame, err := codec.Read()
		if err != nil {
			return err
		}
		if frame.Kind != guestsession.Start {
			return guestsession.ErrProtocol
		}
		if err = servePTY(ctx, stopRead, codec, frame); err != nil {
			return err
		}
	}
}
func servePTY(ctx context.Context, stopRead context.CancelFunc, codec *guestsession.Codec, start guestsession.Frame) error {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	master := os.NewFile(uintptr(fd), "guest-pty")
	defer master.Close()
	streamCtx, stopIO := context.WithCancel(ctx)
	defer stopIO()
	stream := boundedFD{ctx: streamCtx, fd: fd}
	if err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		return err
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		return err
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return err
	}
	defer slave.Close()
	resize := func(b []byte) error {
		return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: binary.BigEndian.Uint16(b[:2]), Col: binary.BigEndian.Uint16(b[2:])})
	}
	if err = resize(start.Payload); err != nil {
		return err
	}
	// This command is executed exclusively inside the qualified guest image.
	cmd := exec.Command(guestShell, guestShellArguments()...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "TERM=xterm-256color", "LANG=C.UTF-8"}
	cmd.Dir = guestHome
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err = cmd.Start(); err != nil {
		return err
	}
	slave.Close()
	kill := func() { _ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL) }
	defer kill()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	if err = codec.Write(guestsession.Frame{Kind: guestsession.Ready, Session: start.Session}); err != nil {
		kill()
		select {
		case <-waited:
		case <-time.After(time.Second):
			return fmt.Errorf("guest cleanup unconfirmed")
		}
		return err
	}
	outputDone := make(chan error, 1)
	go func() {
		total := 0
		buf := make([]byte, guestsession.MaxPayload)
		for {
			n, e := stream.Read(buf)
			if n > 0 {
				total += n
				if total > maxSessionBytes {
					outputDone <- fmt.Errorf("guest output bound")
					return
				}
				if e = codec.Write(guestsession.Frame{Kind: guestsession.Output, Session: start.Session, Payload: buf[:n]}); e != nil {
					outputDone <- e
					return
				}
			}
			if e != nil {
				if e == io.EOF || e == unix.EIO {
					outputDone <- nil
				} else {
					outputDone <- e
				}
				return
			}
		}
	}()
	inputDone := make(chan error, 1)
	go func() {
		total := 0
		for {
			frame, e := codec.Read()
			if e != nil {
				inputDone <- e
				return
			}
			if frame.Session != start.Session {
				inputDone <- guestsession.ErrProtocol
				return
			}
			switch frame.Kind {
			case guestsession.Input:
				total += len(frame.Payload)
				if total > maxSessionBytes {
					inputDone <- fmt.Errorf("guest input bound")
					return
				}
				var n int
				n, e = stream.Write(frame.Payload)
				if e == nil && n != len(frame.Payload) {
					e = io.ErrShortWrite
				}
			case guestsession.Resize:
				e = resize(frame.Payload)
			case guestsession.Close:
				inputDone <- nil
				return
			default:
				e = guestsession.ErrProtocol
			}
			if e != nil {
				inputDone <- e
				return
			}
		}
	}()

	inputFinished, outputFinished, processFinished := false, false, false
	var inputErr, outputErr error
	naturalExit := false
	select {
	case inputErr = <-inputDone:
		inputFinished = true
	case outputErr = <-outputDone:
		outputFinished = true
	case <-ctx.Done():
	case <-waited:
		processFinished = true
		naturalExit = true
	}
	// Natural shell exit must first drain its remaining PTY output. Killing or
	// canceling the reader immediately can discard the final command's text.
	if !naturalExit {
		kill()
	}
	if !processFinished {
		select {
		case <-waited:
			processFinished = true
		case <-time.After(time.Second):
			return fmt.Errorf("guest process cleanup unconfirmed")
		}
	}
	if !outputFinished {
		select {
		case outputErr = <-outputDone:
			outputFinished = true
		case <-time.After(time.Second):
			return fmt.Errorf("guest output drain unconfirmed")
		}
	}
	if outputErr != nil {
		return fmt.Errorf("guest output failed: %w", outputErr)
	}
	if inputFinished && inputErr != nil {
		return fmt.Errorf("guest input failed: %w", inputErr)
	}
	// Stop the independently cancelable nonblocking reader and join before Exit.
	stopRead()
	if !inputFinished {
		select {
		case inputErr = <-inputDone:
			inputFinished = true
			if inputErr != nil && !errors.Is(inputErr, context.Canceled) {
				return fmt.Errorf("guest input failed during drain: %w", inputErr)
			}
		case <-time.After(time.Second):
			return fmt.Errorf("guest input cleanup unconfirmed")
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	status := make([]byte, 4)
	binary.BigEndian.PutUint32(status, uint32(int32(cmd.ProcessState.ExitCode())))
	if err = codec.Write(guestsession.Frame{Kind: guestsession.Exit, Session: start.Session, Payload: status}); err != nil {
		return err
	}
	return io.EOF
}

// Login profiles are guest files installed by the unchanged template UserData.
func guestShellArguments() []string { return []string{"--login", "-i"} }
