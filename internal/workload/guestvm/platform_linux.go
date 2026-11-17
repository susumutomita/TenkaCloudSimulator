//go:build linux

package guestvm

import "runtime"

// maxUnixSocketPath is the longest socket path Linux sun_path holds (108 bytes
// including the terminating NUL).
const maxUnixSocketPath = 107

// launchPlatformSupported names the qualified hosts for the fixed x86_64 TCG
// plan. The host architecture only selects the emulator build.
func launchPlatformSupported() bool { return runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64" }

// qemuSandboxArgs enables QEMU's seccomp sandbox, which exists only on Linux.
var qemuSandboxArgs = []string{"-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny"}
