//go:build linux || darwin

package guestprocess

// Bound supervisor-bound launches independently from the scheduler's GOMAXPROCS.
// On Linux this budget bounds creator-thread retention; on Darwin it bounds the
// private supervisor processes. It never bounds the number of live children.
const maxSupervisorBound = 16

var supervisorSlots = make(chan struct{}, maxSupervisorBound)
