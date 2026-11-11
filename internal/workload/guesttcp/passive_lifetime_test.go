package guesttcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

func TestPassiveBootDeadlineValidationAndFailureOwnership(t *testing.T) {
	for _, name := range []string{"nil parent", "zero", "expired", "too distant", "canceled", "expired parent", "too distant with earlier parent"} {
		t.Run(name, func(t *testing.T) {
			parent := context.Background()
			deadline := time.Now().Add(time.Minute)
			want := ErrConfig
			switch name {
			case "nil parent":
				parent = nil
			case "zero":
				deadline = time.Time{}
			case "expired":
				deadline = time.Now().Add(-time.Second)
				want = context.DeadlineExceeded
			case "too distant", "too distant with earlier parent":
				deadline = time.Now().Add(MaxPassiveLifetime + time.Minute)
				if name == "too distant with earlier parent" {
					parent = passiveDeadlineParent{Context: parent, deadline: time.Now().Add(time.Minute)}
				}
			case "canceled":
				parent = canceledTransportContext()
				want = context.Canceled
			case "expired parent":
				parent = elapsedDeadlineContext{parent}
				want = context.DeadlineExceeded
			}
			stacks, workers, debt := len(slots), passiveWorkerCount(), len(transportSlots)
			listener, err := NewListenerUntil(parent, listenConfig(), deadline)
			if listener != nil || !errors.Is(err, want) {
				t.Fatalf("listener result %v, want %v", err, want)
			}
			stream, service := newBlockedStream(false), newTransportServiceFixture()
			transport, err := NewPassiveTransportUntil(parent, stream, memoryCommitter, nil, service, transportServiceRoute(), deadline)
			if transport != nil || !errors.Is(err, want) {
				t.Fatalf("transport result %v, want %v", err, want)
			}
			if len(slots) != stacks || passiveWorkerCount() != workers || len(transportSlots) != debt || stream.closes.Load() != 0 || service.closes.Load() != 0 {
				t.Fatal("invalid boot deadline consumed capacity or dependency ownership")
			}
		})
	}
}

func TestPassiveBootDeadlineOriginalBoundAndIndependentParentExpiry(t *testing.T) {
	for _, delayedParent := range []bool{false, true} {
		t.Run(map[bool]string{false: "original deadline", true: "earlier parent notification delayed"}[delayedParent], func(t *testing.T) {
			deadline := time.Now().Add(time.Minute)
			parent := context.Background()
			want := deadline
			if delayedParent {
				want = time.Now().Add(30 * time.Millisecond)
				parent = passiveDeadlineParent{Context: parent, deadline: want}
			}
			listener, err := NewListenerUntil(parent, listenConfig(), deadline)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			local, _ := memoryStreams()
			tr, err := NewPassiveTransportUntil(parent, local, memoryCommitter, nil, listener, transportServiceRoute(), deadline)
			if err != nil {
				t.Fatal(err)
			}
			defer tr.Close()
			got, ok := tr.ctx.Deadline()
			if !ok || !got.Equal(want) || !listener.expires.Equal(want) {
				t.Fatal("constructor renewed or truncated the original boot deadline")
			}
			if delayedParent {
				accepted := make(chan error, 1)
				go func() { _, err := listener.Accept(); accepted <- err }()
				awaitSignal(t, tr.done, "fixed boot parent expiry")
				if err := tr.Wait(); err == nil {
					t.Fatal("expired boot transport reported success")
				}
				select {
				case err := <-accepted:
					if err == nil {
						t.Fatal("Accept succeeded after boot deadline")
					}
				case <-time.After(time.Second):
					t.Fatal("pending Accept relied on delayed parent notification")
				}
			}
		})
	}
}

func TestPassiveBootWindowRetainsListenerAttemptAndFrameBudgets(t *testing.T) {
	l, err := NewListenerUntil(context.Background(), listenConfig(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := range maxListenerSessions {
		if err := l.Inject(listenFrame(listenConfig(), uint16(41000+i), uint32(i+1), 2)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Inject(listenFrame(listenConfig(), 42000, 123, 2)); !errors.Is(err, ErrCapacity) {
		t.Fatal("longer lifetime replenished retained attempts", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	l, err = NewListenerUntil(context.Background(), listenConfig(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.mu.Lock()
	l.packets = maxFrames
	l.mu.Unlock()
	if err := l.Inject(listenFrame(listenConfig(), 41000, 123, 2)); !errors.Is(err, ErrCapacity) {
		t.Fatal("longer lifetime bypassed cumulative frame budget", err)
	}
}

func TestPassiveBootSharedDeadlineRejectsPausedDeliveryCallback(t *testing.T) {
	parent := context.Background()
	deadline := time.Now().Add(150 * time.Millisecond)
	service, err := NewListenerUntil(parent, listenConfig(), deadline)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	local, remote := memoryStreams()
	entered, release := make(chan struct{}), make(chan struct{})
	callback := make(chan error, 1)
	owner := commitFunc(func(_ context.Context, direction FrameDirection, _ []byte, apply func() error) error {
		if direction == FromGuest {
			return apply()
		}
		close(entered)
		// This bounded delayed owner intentionally invokes its callback after
		// the shared deadline. Admission must reject before writing a frame
		// that the still-live service produced before that deadline.
		<-release
		err := apply()
		callback <- err
		return err
	})
	tr, err := NewPassiveTransportUntil(parent, local, owner, nil, service, transportServiceRoute(), deadline)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	defer close(release)
	if err := guestnet.NewEncoder(remote).WriteFrame(listenFrame(listenConfig(), 41000, 123, 2)); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, entered, "owner admitted pre-deadline response")
	awaitSignal(t, tr.ctx.Done(), "shared boot deadline")
	// Unblock without closing here so deferred cleanup also handles a failure
	// before the owner was reached, and the release channel is closed once.
	release <- struct{}{}
	select {
	case err := <-callback:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("late callback reached stream instead of deadline guard: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("owner callback did not return")
	}
	awaitSignal(t, tr.done, "expired boot transport cleanup")
	if err := tr.Wait(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("late boot delivery reported success", err)
	}
	if len(remote.in) != 0 {
		t.Fatal("late response reached the guest stream")
	}
}
