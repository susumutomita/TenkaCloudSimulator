//go:build darwin

package guestprocess

import (
	"context"
	"errors"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Darwin has no PDEATHSIG. A Darwin child is instead bound to its private
// supervisor process (see process_darwin.go), so the creator thread needs no pin.
func lockCreatorThread() {}

// launchBound starts the supervisor-bound child for one attempt.
func launchBound(ctx context.Context, spec Spec, private *os.File, start startFunc) (*Process, error) {
	return launchWithFile(ctx, spec, private, start)
}

// requireAnonymousPipe rejects named FIFOs that fstat alone would accept. XNU
// answers F_GETPATH only for vnode-backed descriptors: a named FIFO has a path,
// while an anonymous pipe is not a vnode and is refused with EBADF.
func requireAnonymousPipe(fd int) error {
	path := make([]byte, unix.PathMax)
	_, err := unix.FcntlInt(uintptr(fd), unix.F_GETPATH, int(uintptr(unsafe.Pointer(&path[0]))))
	runtime.KeepAlive(path)
	switch {
	case errors.Is(err, unix.EBADF):
		return nil
	case err == nil:
		return ErrUnsupported
	default:
		return err
	}
}

// socketPair matches Linux SOCK_CLOEXEC|SOCK_NONBLOCK. Darwin socketpair takes
// no type flags, so close-on-exec is set while holding ForkLock: no concurrent
// fork can inherit either end before it is marked.
func socketPair() ([2]int, error) {
	syscall.ForkLock.RLock()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.CloseOnExec(fds[0])
		unix.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return [2]int{}, err
	}
	for _, fd := range fds {
		if err := unix.SetNonblock(fd, true); err != nil {
			return [2]int{}, errors.Join(err, unix.Close(fds[0]), unix.Close(fds[1]))
		}
	}
	return fds, nil
}
