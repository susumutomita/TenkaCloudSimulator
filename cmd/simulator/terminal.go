package main

import (
	"io"
	"os"
)

// File-only is the default. Even explicit --show-token never writes a secret to
// a redirected output or CI job; TERM and character-device mode are insufficient.
func interactiveTokenOutput(output io.Writer) bool {
	if _, ci := os.LookupEnv("CI"); ci {
		return false
	}
	file, ok := output.(*os.File)
	return ok && terminalDescriptor(file.Fd())
}
