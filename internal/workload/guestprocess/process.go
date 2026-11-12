// Package guestprocess owns one trusted host child for the lifetime of this
// supervisor. It is not a sandbox, durable lease backend, or participant API.
package guestprocess

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var (
	// ErrOwnershipUncertain means a child may be running without a usable owner.
	// Do not classify this as rejected/no-effect or retry the launch.
	ErrOwnershipUncertain = errors.New("guestprocess: child ownership uncertain")
	// ErrSupervisorBudget means no additional pinned creator thread is available.
	ErrSupervisorBudget = errors.New("guestprocess: supervisor-bound launch budget exhausted")
	ErrUnsupported      = errors.New("guestprocess: operation unsupported")
	ErrInvalid          = errors.New("guestprocess: invalid trusted launch specification")
)

// Spec must be constructed exclusively by a trusted runtime after verifying its
// executable and assets in private storage. No field may come from participant
// input. Args excludes argv[0]. Env must be explicit (empty is permitted, nil is
// not). The caller must not mutate slices concurrently with Launch.
//
// This package does not verify file provenance, prevent executable replacement,
// constrain executable behavior, or isolate host resources. In particular the
// trusted executable must not daemonize or leave helper processes behind.
type Spec struct {
	Executable string
	Args       []string
	Env        []string
}

// Exit is evidence only for the single owned process, never its descendants or
// a VM lease. Reaped is true only following a successful kernel wait operation.
type Exit struct {
	Reaped bool
	// Code is the child exit code, or -1 when killed by a signal.
	Code int
}

func validate(s Spec) error {
	if !filepath.IsAbs(s.Executable) || strings.ContainsRune(s.Executable, 0) || s.Env == nil {
		return ErrInvalid
	}
	for _, arg := range s.Args {
		if strings.ContainsRune(arg, 0) {
			return ErrInvalid
		}
	}
	keys := make(map[string]bool, len(s.Env))
	for _, entry := range s.Env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || keys[key] || strings.ContainsRune(entry, 0) {
			return ErrInvalid
		}
		keys[key] = true
	}
	return nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalid)
	}
	return ctx.Err()
}
