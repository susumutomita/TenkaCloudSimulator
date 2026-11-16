package main

import (
	"errors"
	"runtime"
)

// errBattlePlatform rejects hosts on which the Battle VM runtime is not qualified.
var errBattlePlatform = errors.New("Battle runtime requires Linux/amd64 or macOS/arm64")

// battlePlatformSupported names the qualified Battle hosts: Linux/amd64, and
// Apple silicon running the same x86_64 guest under QEMU TCG (no HVF).
func battlePlatformSupported() bool {
	return (runtime.GOOS == "linux" && runtime.GOARCH == "amd64") || (runtime.GOOS == "darwin" && runtime.GOARCH == "arm64")
}
