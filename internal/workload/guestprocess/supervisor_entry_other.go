//go:build !darwin

package guestprocess

// RunSupervisorIfRequested is a no-op outside Darwin, where the kernel binds a
// supervisor-bound child to its creator without a helper process.
func RunSupervisorIfRequested() {}
