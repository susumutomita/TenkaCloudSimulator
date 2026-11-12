package guesttcp

import (
	"context"
	"time"
)

// MaxPassiveLifetime bounds an explicitly requested service window, including a
// Battle VM's whole runtime. It is a hard upper bound, not a sliding timeout,
// renewable lease or permission to retain a physical NIC beyond the requested
// deadline. All cumulative frame, byte, attempt and worker limits remain
// unchanged, so long windows still end at the first exhausted budget.
const MaxPassiveLifetime = 24 * time.Hour

// DefaultBattleLifetime is the runtime window used when an operator does not
// request one explicitly: long enough for a typical Battle, never above the max.
const DefaultBattleLifetime = 8 * time.Hour

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
