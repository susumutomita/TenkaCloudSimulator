//go:build darwin

package guestvm

import "runtime"

// maxUnixSocketPath is the longest socket path Darwin sun_path holds (104 bytes
// including the terminating NUL).
const maxUnixSocketPath = 103

// launchPlatformSupported names the qualified hosts for the fixed x86_64 TCG
// plan: Apple silicon and Intel Macs. HVF is not used.
func launchPlatformSupported() bool { return runtime.GOARCH == "arm64" || runtime.GOARCH == "amd64" }

// QEMU's -sandbox is seccomp-based and is not built on Darwin. The Darwin child
// supervisor (guestprocess) still binds the emulator to its owner; no syscall
// filter is claimed for it.
var qemuSandboxArgs []string
