//go:build linux || darwin

package aws

import (
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qga"
)

const vmQGAStartupBudget = 120 * time.Second
const vmQGAMaxHandshakes = 60

// vmQGAMaxBusyDials bounds dials refused by a full listen queue: one per pause
// for the whole startup budget.
const vmQGAMaxBusyDials = 1200

type qgaBootOperations struct {
	dial      func(context.Context, string) (qga.Transport, error)
	handshake func(context.Context, qga.Transport) (io.Closer, error)
	pause     func(context.Context) error
	// busy reports a dial refused only because the agent socket's listen
	// queue is full. Nil means every dial failure is terminal.
	busy func(error) bool
}

// qgaListenerBusy is true only on macOS, where connect(2) reports a full
// listen queue as ECONNREFUSED. QEMU there keeps holding an earlier closed
// readiness attempt until the guest agent opens its port, so later attempts
// queue up and are refused while the guest boots. On Linux a refused
// connection means no listener remains and stays terminal.
func qgaListenerBusy(err error) bool {
	return runtime.GOOS == "darwin" && errors.Is(err, syscall.ECONNREFUSED)
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
// No guest-exec handle exists yet. Dial/permission/protocol failures are terminal,
// except a dial that ops.busy attributes to a full listen queue; it is retried
// after a pause without a handshake. The original admission/lifetime bounds and
// per-RPC limit are never renewed.
func awaitCanonicalQGA(parent context.Context, path string, ops qgaBootOperations) (io.Closer, error) {
	if parent == nil {
		return nil, errGuestLease
	}
	ctx, cancel := context.WithTimeout(parent, vmQGAStartupBudget)
	defer cancel()
	busyDials := 0
	for attempt := 0; attempt < vmQGAMaxHandshakes; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := dialQGA(ctx, path, ops, &busyDials)
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

// dialQGA dials once, and again after a pause while ops.busy attributes the
// refusal to a full listen queue. busyDials is shared by the whole readiness
// wait so the bound never renews.
func dialQGA(ctx context.Context, path string, ops qgaBootOperations, busyDials *int) (qga.Transport, error) {
	for {
		raw, err := ops.dial(ctx, path)
		if err == nil || raw != nil || ops.busy == nil || !ops.busy(err) || *busyDials == vmQGAMaxBusyDials {
			return raw, err
		}
		*busyDials++
		if err = ops.pause(ctx); err != nil {
			return nil, err
		}
	}
}
