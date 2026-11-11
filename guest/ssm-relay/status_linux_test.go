//go:build linux

package main

import (
	"io"
	"testing"
)

// Pure status mapping only; never calls run, main, PTY, a process or device IO.
func TestRelayProcessStatusRequestsFreshHello(t *testing.T) {
	if relayExitCode(io.EOF) != 1 {
		t.Fatal("completed session would not restart relay")
	}
	if relayExitCode(nil) != 0 {
		t.Fatal("nil status changed")
	}
}

func TestRelayGuestLoginProfileContract(t *testing.T) {
	args := guestShellArguments()
	if len(args) != 2 || args[0] != "--login" || args[1] != "-i" {
		t.Fatal("guest login profile disabled")
	}
}
