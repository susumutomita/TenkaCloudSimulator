//go:build darwin

package main

import "golang.org/x/sys/unix"

func terminalDescriptor(fd uintptr) bool {
	_, err := unix.IoctlGetTermios(int(fd), unix.TIOCGETA)
	return err == nil
}
