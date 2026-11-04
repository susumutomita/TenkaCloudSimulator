//go:build linux

package main

import "golang.org/x/sys/unix"

func terminalDescriptor(fd uintptr) bool {
	_, err := unix.IoctlGetTermios(int(fd), unix.TCGETS)
	return err == nil
}
