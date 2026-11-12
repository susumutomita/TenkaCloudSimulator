//go:build !linux

package guestprocess

import "context"

type Process struct{}

func Launch(ctx context.Context, spec Spec) (*Process, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := validate(spec); err != nil {
		return nil, err
	}
	return nil, ErrUnsupported
}
func (*Process) Wait(ctx context.Context) (Exit, error) {
	if err := checkContext(ctx); err != nil {
		return Exit{}, err
	}
	return Exit{}, ErrUnsupported
}
func (*Process) Kill(ctx context.Context) (Exit, error) {
	if err := checkContext(ctx); err != nil {
		return Exit{}, err
	}
	return Exit{}, ErrUnsupported
}
func (*Process) Contain(ctx context.Context) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	return ErrUnsupported
}

// LaunchSupervisorBound is unsupported outside Linux.
func LaunchSupervisorBound(ctx context.Context, spec Spec) (*Process, error) {
	return Launch(ctx, spec)
}
