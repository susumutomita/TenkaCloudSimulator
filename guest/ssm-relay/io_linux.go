//go:build linux

package main

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"time"
)

// All guest character-device IO uses nonblocking descriptors and bounded poll.
// Closing an fd is not used as evidence that an uninterruptible write stopped.
type boundedFD struct {
	ctx context.Context
	fd  int
}

func (f boundedFD) wait(events int16) error {
	for {
		if err := f.ctx.Err(); err != nil {
			return err
		}
		ms := 50
		if deadline, ok := f.ctx.Deadline(); ok {
			left := time.Until(deadline)
			if left <= 0 {
				return context.DeadlineExceeded
			}
			if left < 50*time.Millisecond {
				ms = int(left/time.Millisecond) + 1
			}
		}
		p := []unix.PollFd{{Fd: int32(f.fd), Events: events}}
		n, err := unix.Poll(p, ms)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n > 0 {
			if p[0].Revents&unix.POLLNVAL != 0 {
				return unix.EBADF
			}
			return nil
		}
	}
}
func (f boundedFD) Read(p []byte) (int, error) {
	for {
		if err := f.ctx.Err(); err != nil {
			return 0, err
		}
		n, err := unix.Read(f.fd, p)
		if err == unix.EINTR {
			continue
		}
		if errors.Is(err, unix.EAGAIN) {
			if err = f.wait(unix.POLLIN); err != nil {
				return 0, err
			}
			continue
		}
		if err == nil && n == 0 {
			return 0, io.EOF
		}
		return n, err
	}
}
func (f boundedFD) Write(p []byte) (int, error) {
	for {
		if err := f.ctx.Err(); err != nil {
			return 0, err
		}
		n, err := unix.Write(f.fd, p)
		if err == unix.EINTR {
			continue
		}
		if errors.Is(err, unix.EAGAIN) {
			if err = f.wait(unix.POLLOUT); err != nil {
				return 0, err
			}
			continue
		}
		return n, err
	}
}

// Read cancellation joins input without disabling the final bounded Exit write.
type duplexFD struct{ reader, writer boundedFD }

func (f duplexFD) Read(p []byte) (int, error)  { return f.reader.Read(p) }
func (f duplexFD) Write(p []byte) (int, error) { return f.writer.Write(p) }
