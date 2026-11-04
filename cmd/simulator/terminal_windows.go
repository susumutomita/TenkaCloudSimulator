//go:build windows

package main

import "golang.org/x/sys/windows"

func terminalDescriptor(fd uintptr) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(fd), &mode) == nil
}
