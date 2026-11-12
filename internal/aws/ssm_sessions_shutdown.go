package aws

import (
	"context"
	"errors"
	"fmt"
)

// Shutdown is a trusted host lifecycle boundary, not a synthetic participant
// TerminateSession request. The VM owner must still perform its own teardown.
// An error retains unknown guest/transport cleanup debt; callers must not turn
// it into a successful termination receipt or close SQLite before recording it.
func (s *SSMSessionStore) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	entries := make([]*ssmLiveSession, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, e)
	}
	s.mu.Unlock()
	for _, e := range entries {
		e.mu.Lock()
		e.state = "shutdown"
		e.tokenUsed = true
		e.generation++
		if e.channelCancel != nil {
			e.channelCancel()
		}
		e.mu.Unlock()
	}
	var failures []error
	for _, e := range entries {
		// Cancelled consumers release the gate; no new data-channel admission is
		// possible after marking every entry shutdown.
		joined := make(chan struct{})
		go func() { e.channelGate.Lock(); e.channelGate.Unlock(); close(joined) }()
		select {
		case <-joined:
		case <-ctx.Done():
			failures = append(failures, fmt.Errorf("session %s consumer join: %w", e.id, ctx.Err()))
			e.cancelLifetime()
			continue
		}
		e.mu.Lock()
		driver := e.driver
		e.mu.Unlock()
		if driver == nil {
			e.cancelLifetime()
			failures = append(failures, fmt.Errorf("session %s has no guest exit receipt", e.id))
			continue
		}
		clean := true
		if err := driver.Terminate(ctx); err != nil {
			clean = false
			failures = append(failures, fmt.Errorf("session %s guest exit: %w", e.id, err))
		}
		e.cancelLifetime()
		driver.Revoke() // WaitCleanup below reports the exact bounded outcome.
		if err := driver.WaitCleanup(ctx); err != nil {
			clean = false
			failures = append(failures, fmt.Errorf("session %s transport cleanup: %w", e.id, err))
		}
		if clean {
			var from string
			if err := s.db.QueryRowContext(ctx, `SELECT state FROM aws_ssm_sessions WHERE id=?`, e.id).Scan(&from); err != nil {
				failures = append(failures, err)
				continue
			}
			if from != "connected" && from != "closing" && from != "terminated" {
				failures = append(failures, fmt.Errorf("session %s has unsupported persisted shutdown state", e.id))
				continue
			}
			if err := s.transition(ctx, e, from, "terminated"); err != nil {
				failures = append(failures, err)
				continue
			}
			e.mu.Lock()
			e.state = "terminated"
			e.mu.Unlock()
		}
	}
	return errors.Join(failures...)
}
