package main

import (
	"errors"
	"runtime"
)

// errBattlePlatform rejects hosts on which the Battle VM runtime is not qualified.
var errBattlePlatform = errors.New("Battle runtime requires Linux or macOS on amd64 or arm64")

// battlePlatformSupported names the qualified Battle hosts: Linux or macOS on
// amd64 or arm64. Every host runs the same x86_64 guest under QEMU TCG (no KVM
// or HVF), so the host architecture only selects the emulator build. Windows
// hosts run the Linux build under WSL2.
func battlePlatformSupported() bool {
	return (runtime.GOOS == "linux" || runtime.GOOS == "darwin") && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64")
}
