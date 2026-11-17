//go:build linux

package guestprocess

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// lockCreatorThread pins the creator goroutine so that its retirement delivers
// the child's PDEATHSIG. The caller never unlocks.
func lockCreatorThread() { runtime.LockOSThread() }

// launchBound starts the supervisor-bound child for one attempt.
func launchBound(ctx context.Context, spec Spec, private *os.File, start startFunc) (*Process, error) {
	return launchWithFile(ctx, spec, syscall.SIGKILL, private, start)
}

// requireAnonymousPipe rejects named FIFOs that fstat alone would accept.
func requireAnonymousPipe(fd int) error {
	target, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		return err
	}
	if !strings.HasPrefix(target, "pipe:[") || !strings.HasSuffix(target, "]") {
		return ErrUnsupported
	}
	return nil
}

func socketPair() ([2]int, error) {
	return unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
}
