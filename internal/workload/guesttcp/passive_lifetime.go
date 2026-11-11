package guesttcp

import (
	"context"
	"time"
)

// MaxPassiveLifetime bounds an explicitly requested boot-service window. It is
// not a sliding timeout, renewable lease or permission to retain a physical NIC.
// All cumulative frame, byte, attempt and worker limits remain unchanged.
const MaxPassiveLifetime = 15 * time.Minute

// passiveDeadline validates the original requested expiry before applying an
// earlier parent deadline. The resulting equal-or-earlier deadline lets
// context.WithDeadline install an independent timer, even if a custom parent's
// cancellation notification is delayed. Every caller also checks absolute time
// at admission; userspace timers are not physical enforcement during suspension.
func passiveDeadline(parent context.Context, deadline time.Time) (time.Time, error) {
	if nilInterface(parent) || deadline.IsZero() {
		return time.Time{}, ErrConfig
	}
	if err := parent.Err(); err != nil {
		return time.Time{}, err
	}
	now := time.Now()
	if !now.Before(deadline) {
		return time.Time{}, context.DeadlineExceeded
	}
	if deadline.After(now.Add(MaxPassiveLifetime)) {
		return time.Time{}, ErrConfig
	}
	if earlier, ok := parent.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	if !now.Before(deadline) {
		return time.Time{}, context.DeadlineExceeded
	}
	return deadline, nil
}
