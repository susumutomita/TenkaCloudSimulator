//go:build !linux && !darwin && !windows

package main

// Conservatively keep file-only output on unverified console platforms.
func terminalDescriptor(uintptr) bool { return false }
