//go:build darwin

package probeclock

import (
	"golang.org/x/sys/unix"
	"time"
)

// New anchors to Darwin's CLOCK_UPTIME_RAW, which Apple documents as
// mach_absolute_time converted by mach_timebase_info, excluding system sleep.
// Go's Darwin monotonic time uses that same source. Never label this Linux's
// CLOCK_MONOTONIC or substitute Darwin CLOCK_MONOTONIC (which includes sleep).
// This constructor does NOT qualify executable launch or Battle on Darwin.
func New() (*Clock, error) {
	anchor := time.Now()
	var ts unix.Timespec
	if e := unix.ClockGettime(unix.CLOCK_UPTIME_RAW, &ts); e != nil {
		return nil, e
	}
	if time.Since(anchor) > time.Millisecond {
		return nil, ErrClock
	}
	if ts.Sec < 0 || ts.Sec > (1<<53-1)/1000 || ts.Nsec < 0 || ts.Nsec >= 1_000_000_000 {
		return nil, ErrClock
	}
	return newPlatformClock(anchor, ts.Sec*1000+ts.Nsec/1_000_000, randomSource, darwinUptimeRaw)
}
