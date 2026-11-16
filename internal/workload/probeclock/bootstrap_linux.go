//go:build linux

package probeclock

import "golang.org/x/sys/unix"

// CloseInheritedExecutable consumes the Linux launcher's pinned executable FD4.
func CloseInheritedExecutable() error { return closeExecutableFD(unix.Close) }
