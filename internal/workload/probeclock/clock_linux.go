//go:build linux

package probeclock

import (
	"golang.org/x/sys/unix"
	"time"
)

// New is supported only on Linux, whose Go runtime monotonic component uses
// CLOCK_MONOTONIC. It intentionally does not use CLOCK_REALTIME or BOOTTIME.
func New() (*Clock, error) {
	anchor := time.Now()
	var ts unix.Timespec
	if e := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); e != nil {
		return nil, e
	}
	if time.Since(anchor) > time.Millisecond {
		return nil, ErrClock
	}
	if ts.Sec < 0 || ts.Sec > (1<<53-1)/1000 || ts.Nsec < 0 || ts.Nsec >= 1_000_000_000 {
		return nil, ErrClock
	}
	return newClock(anchor, ts.Sec*1000+ts.Nsec/1_000_000, randomSource)
}
