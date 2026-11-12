//go:build !linux

package guestprocess

import (
	"context"
	"os"
)

const PrivateFD = 3

type LaunchAttempt struct{}

func BeginSupervisorBoundLaunch(ctx context.Context, spec Spec, privateFile *os.File) (*LaunchAttempt, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := validate(spec); err != nil {
		return nil, err
	}
	if privateFile == nil {
		return nil, ErrInvalid
	}
	return nil, ErrUnsupported
}
func (*LaunchAttempt) Wait(ctx context.Context) (*Process, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return nil, ErrUnsupported
}
func (*LaunchAttempt) Cancel() error { return ErrUnsupported }
func (*LaunchAttempt) WaitCleanup(ctx context.Context) (Exit, error) {
	if err := checkContext(ctx); err != nil {
		return Exit{}, err
	}
	return Exit{}, ErrUnsupported
}
