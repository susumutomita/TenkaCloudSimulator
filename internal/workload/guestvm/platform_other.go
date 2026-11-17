//go:build !linux && !darwin

package guestvm

const maxUnixSocketPath = 103

func launchPlatformSupported() bool { return false }

var qemuSandboxArgs []string
