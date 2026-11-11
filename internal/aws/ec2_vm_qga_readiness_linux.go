//go:build linux

package aws

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qga"
)

const vmQGAStartupBudget = 120 * time.Second
const vmQGAMaxHandshakes = 60

type qgaBootOperations struct {
	dial      func(context.Context, string) (qga.Transport, error)
	handshake func(context.Context, qga.Transport) (io.Closer, error)
	pause     func(context.Context) error
}
type trackedQGATransport struct {
	qga.Transport
	once     sync.Once
	closeErr error
}

func (t *trackedQGATransport) Close() error {
	t.once.Do(func() { t.closeErr = t.Transport.Close() })
	return t.closeErr
}

// Only the side-effect-free guest-sync-delimited initialization may be retried.
// No guest-exec handle exists yet. Dial/permission/protocol failures are terminal;
// the original admission/lifetime bounds and per-RPC limit are never renewed.
func awaitCanonicalQGA(parent context.Context, path string, ops qgaBootOperations) (io.Closer, error) {
	if parent == nil {
		return nil, errGuestLease
	}
	ctx, cancel := context.WithTimeout(parent, vmQGAStartupBudget)
	defer cancel()
	for attempt := 0; attempt < vmQGAMaxHandshakes; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := ops.dial(ctx, path)
		if err != nil {
			if raw != nil {
				return raw, err
			}
			return nil, err
		}
		if raw == nil {
			return nil, errGuestLease
		}
		transport := &trackedQGATransport{Transport: raw}
		client, err := ops.handshake(ctx, transport)
		if err == nil && client != nil {
			return client, nil
		}
		if client != nil {
			return client, errors.Join(err, ErrEC2GuestAttachmentPending)
		}
		closeErr := transport.Close() // At most one underlying Close, including New failure.
		if closeErr != nil {
			return transport, errors.Join(err, closeErr)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var timeout net.Error
		if err == nil || (!errors.Is(err, context.DeadlineExceeded) && (!errors.As(err, &timeout) || !timeout.Timeout())) {
			return nil, errors.Join(err, errGuestLease)
		}
		if attempt+1 == vmQGAMaxHandshakes {
			return nil, context.DeadlineExceeded
		}
		if err = ops.pause(ctx); err != nil {
			return nil, err
		}
	}
	return nil, context.DeadlineExceeded
}
