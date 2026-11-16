//go:build darwin

package probeclock

// Darwin still has no qualified FD4 executable-launch contract. Enabling FD3
// clock transport alone must not make the Battle runtime startable.
func CloseInheritedExecutable() error { return ErrClock }
