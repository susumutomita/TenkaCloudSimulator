//go:build linux || darwin

package probeclock

import (
	"bytes"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"testing"
	"time"
)

type fakeBootstrap struct {
	bytes.Buffer
	closed   int
	closeErr error
}

func (f *fakeBootstrap) Close() error { f.closed++; return f.closeErr }
func TestBootstrapCheckedFDLifecycle(t *testing.T) {
	for _, fault := range []string{"none", "get", "set", "missing", "close", "regular", "write"} {
		t.Run(fault, func(t *testing.T) {
			f := &fakeBootstrap{}
			if fault == "close" {
				f.closeErr = errors.New("uncertain close")
			}
			flags := 0
			ops := bootstrapOps{
				open: func(int) io.WriteCloser { return f }, flags: func(int) (int, error) {
					if fault == "get" {
						return 0, ErrClock
					}
					return flags, nil
				},
				setFlags: func(_ int, n int) error {
					if fault == "set" {
						return ErrClock
					}
					if fault != "missing" {
						flags = n
					}
					return nil
				},
				mode: func(int) (uint32, error) {
					if fault == "regular" {
						return unix.S_IFREG, nil
					}
					return unix.S_IFIFO, nil
				},
				clock: func() (*Clock, error) {
					if fault == "write" {
						return nil, ErrClock
					}
					return newClock(time.Now(), 10_000, bytes.NewReader(bytes.Repeat([]byte{9}, 48)))
				},
			}
			c, e := bootstrapFD(3, ops)
			if (e == nil) != (fault == "none") || f.closed != 1 {
				t.Fatal(fault, e, f.closed)
			}
			if e != nil && c != nil {
				t.Fatal("failed close/bootstrap returned capability")
			}
			if fault != "none" && fault != "close" && f.Len() != 0 {
				t.Fatal("bootstrap before CLOEXEC/validation")
			}
		})
	}
}
func TestBootstrapCannotSelectOtherFD(t *testing.T) {
	if _, e := bootstrapFD(4, bootstrapOps{}); e == nil {
		t.Fatal("caller FD accepted")
	}
}
func TestExecutableCloseFailureDoesNotRetryOrTouchProofFD(t *testing.T) {
	calls := 0
	failure := errors.New("uncertain close")
	e := closeExecutableFD(func(fd int) error {
		calls++
		if fd != 4 {
			t.Fatal("wrong inherited descriptor")
		}
		return failure
	})
	if !errors.Is(e, failure) || calls != 1 {
		t.Fatal("failed close hidden or retried", e, calls)
	}
}
