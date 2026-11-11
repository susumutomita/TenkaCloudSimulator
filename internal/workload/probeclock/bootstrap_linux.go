//go:build linux

package probeclock

import (
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
)

type bootstrapOps struct {
	open     func(int) io.WriteCloser
	flags    func(int) (int, error)
	setFlags func(int, int) error
	mode     func(int) (uint32, error)
	clock    func() (*Clock, error)
}

// BootstrapInherited uses only fixed launcher-owned anonymous FD3. Every close
// and close-on-exec operation is checked before startup may create guest children.
func BootstrapInherited() (*Clock, error) {
	return bootstrapFD(3, bootstrapOps{
		open:     func(fd int) io.WriteCloser { return os.NewFile(uintptr(fd), "private-clock-bootstrap") },
		flags:    func(fd int) (int, error) { return unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0) },
		setFlags: func(fd, flags int) error { _, e := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags); return e },
		mode:     func(fd int) (uint32, error) { var st unix.Stat_t; e := unix.Fstat(fd, &st); return st.Mode, e }, clock: New,
	})
}
func bootstrapFD(fd int, ops bootstrapOps) (clock *Clock, err error) {
	if fd != 3 {
		return nil, ErrClock
	}
	f := ops.open(fd)
	if f == nil {
		return nil, ErrClock
	}
	defer func() {
		err = errors.Join(err, f.Close())
		if err != nil {
			clock = nil
		}
	}()
	flags, e := ops.flags(fd)
	if e != nil {
		return nil, e
	}
	if e = ops.setFlags(fd, flags|unix.FD_CLOEXEC); e != nil {
		return nil, e
	}
	flags, e = ops.flags(fd)
	if e != nil || flags&unix.FD_CLOEXEC == 0 {
		return nil, errors.Join(ErrClock, e)
	}
	mode, e := ops.mode(fd)
	if e != nil {
		return nil, e
	}
	// Node stdio pipes may use anonymous AF_UNIX socket pairs. No regular file,
	// device, environment value or pathname is accepted as bootstrap authority.
	if mode&unix.S_IFMT != unix.S_IFIFO && mode&unix.S_IFMT != unix.S_IFSOCK {
		return nil, ErrClock
	}
	clock, err = ops.clock()
	if err != nil {
		return nil, err
	}
	if err = json.NewEncoder(f).Encode(clock.Bootstrap()); err != nil {
		return nil, err
	}
	return clock, nil
}

// CloseInheritedExecutable consumes only the pinned launcher executable FD4.
// An uncertain close fails startup; repeating close could affect a reused FD.
func CloseInheritedExecutable() error                 { return closeExecutableFD(unix.Close) }
func closeExecutableFD(closeFD func(int) error) error { return closeFD(4) }
