package guesttcp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type revocableServiceFixture struct {
	*transportServiceFixture
	lifetime context.Context
	cancel   context.CancelFunc
	reads    atomic.Int32
}

func (s *revocableServiceFixture) Lifetime() context.Context {
	s.reads.Add(1)
	return s.lifetime
}

func (s *revocableServiceFixture) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	return s.transportServiceFixture.Close()
}

func TestRevocableServiceConstructorFailurePreservesOwnership(t *testing.T) {
	for _, mode := range []string{"boot", "passive", "shared"} {
		for _, kind := range []string{"nil service", "nil lifetime", "typed nil lifetime", "zero deadline", "expired", "canceled"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				ctx := context.Context(passiveDeadlineParent{Context: context.Background(), deadline: time.Now().Add(time.Minute)})
				want := ErrConfig
				switch kind {
				case "nil lifetime":
					ctx = nil
				case "typed nil lifetime":
					var absent *passiveDeadlineParent
					ctx = absent
				case "zero deadline":
					ctx = passiveDeadlineParent{Context: context.Background()}
				case "expired":
					ctx = passiveDeadlineParent{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
					want = context.DeadlineExceeded
				case "canceled":
					ctx = canceledTransportContext()
					want = context.Canceled
				}
				fixture := &revocableServiceFixture{transportServiceFixture: newTransportServiceFixture(), lifetime: ctx}
				var service PassiveService = fixture
				if kind == "nil service" {
					var absent *revocableServiceFixture
					service = absent
				}
				stream := newBlockedStream(false)
				stacks, workers, debt := len(slots), passiveWorkerCount(), len(transportSlots)
				var tr *Transport
				var err error
				switch mode {
				case "boot":
					tr, err = NewPassiveTransportUntil(context.Background(), stream, memoryCommitter, nil, service, transportServiceRoute(), time.Now().Add(time.Minute))
				case "passive":
					tr, err = NewPassiveTransport(context.Background(), stream, memoryCommitter, nil, service, transportServiceRoute())
				case "shared":
					tr, err = NewTransportWithService(context.Background(), config(), stream, memoryCommitter, nil, service, transportServiceRoute())
				}
				if tr != nil || !errors.Is(err, want) {
					if tr != nil {
						_ = tr.Close()
					}
					t.Fatalf("invalid service lifetime accepted: %v, want %v", err, want)
				}
				if len(slots) != stacks || passiveWorkerCount() != workers || len(transportSlots) != debt || stream.closes.Load() != 0 || fixture.closes.Load() != 0 {
					t.Fatal("constructor rejection consumed capacity or caller ownership")
				}
			})
		}
	}
}

func TestRevocableBootServiceRequiredButShortLegacyServicePreserved(t *testing.T) {
	for _, kind := range []string{"plain", "no deadline"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newTransportServiceFixture()
			var service PassiveService = fixture
			if kind == "no deadline" {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				service = &revocableServiceFixture{transportServiceFixture: fixture, lifetime: ctx, cancel: cancel}
			}
			local, _ := memoryStreams()
			tr, err := NewPassiveTransportUntil(context.Background(), local, memoryCommitter, nil, service, transportServiceRoute(), time.Now().Add(time.Minute))
			if tr != nil || !errors.Is(err, ErrConfig) || fixture.closes.Load() != 0 {
				t.Fatal("boot service missing explicit bounded lifetime accepted", err)
			}
			tr, err = NewPassiveTransport(context.Background(), local, memoryCommitter, nil, service, transportServiceRoute())
			if err != nil {
				t.Fatal("legacy short service was rejected", err)
			}
			if err := tr.Close(); err != nil || fixture.closes.Load() != 1 {
				t.Fatal("legacy short service ownership changed", err)
			}
		})
	}
}

func TestRevocableServiceRejectsPausedCallbackAfterIndependentRevocation(t *testing.T) {
	for _, kind := range []string{"cancel", "deadline", "delayed deadline notification"} {
		t.Run(kind, func(t *testing.T) {
			serviceDeadline := time.Now().Add(time.Minute)
			if kind != "cancel" {
				serviceDeadline = time.Now().Add(100 * time.Millisecond)
			}
			ctx, cancel := context.WithDeadline(context.Background(), serviceDeadline)
			defer cancel()
			if kind == "delayed deadline notification" {
				cancel()
				ctx = passiveDeadlineParent{Context: context.Background(), deadline: serviceDeadline}
			}
			service := &revocableServiceFixture{transportServiceFixture: newTransportServiceFixture(), lifetime: ctx, cancel: cancel}
			local, remote := memoryStreams()
			entered, release := make(chan struct{}), make(chan struct{})
			callback := make(chan error, 1)
			owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
				close(entered)
				<-release
				err := apply()
				callback <- err
				return err
			})
			tr, err := NewPassiveTransportUntil(context.Background(), local, owner, nil, service, transportServiceRoute(), time.Now().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			defer func() { once.Do(func() { close(release) }); joinTransport(t, tr) }()
			service.output <- transportServiceOutput()
			awaitSignal(t, entered, "service response awaits owner callback")
			want := context.DeadlineExceeded
			if kind == "cancel" {
				cancel()
				want = context.Canceled
			}
			// Proactive closure must not wait for ReadFrame to be called again,
			// nor for the currently pending owner callback to return.
			awaitSignal(t, local.closed, "independent service revocation closes stream")
			once.Do(func() { close(release) })
			if err := <-callback; !errors.Is(err, want) {
				t.Fatalf("late delivery callback ignored service lifetime: %v", err)
			}
			joinTransport(t, tr)
			if len(remote.in) != 0 || service.reads.Load() != 1 || tr.Wait() == nil {
				t.Fatal("revoked service delivered bytes, reread its lifetime or reported success")
			}
		})
	}
}

type changingServiceDeadline struct {
	context.Context
	expires atomic.Int64
	reads   atomic.Int32
}

func (c *changingServiceDeadline) Deadline() (time.Time, bool) {
	c.reads.Add(1)
	return time.Unix(0, c.expires.Load()), true
}

func TestRevocableServiceCapturesOriginalDeadlineOnce(t *testing.T) {
	ctx := &changingServiceDeadline{Context: context.Background()}
	ctx.expires.Store(time.Now().Add(50 * time.Millisecond).UnixNano())
	service := &revocableServiceFixture{transportServiceFixture: newTransportServiceFixture(), lifetime: ctx}
	local, _ := memoryStreams()
	tr, err := NewPassiveTransportUntil(context.Background(), local, memoryCommitter, nil, service, transportServiceRoute(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	// Deliberately violate the immutable dependency contract: the captured
	// original bound must still expire, not renew on later deadline reads.
	ctx.expires.Store(time.Now().Add(time.Hour).UnixNano())
	awaitSignal(t, tr.done, "original service deadline independent of notifications")
	if !errors.Is(tr.Wait(), context.DeadlineExceeded) || service.reads.Load() != 1 || ctx.reads.Load() != 1 {
		t.Fatal("service context/deadline was not captured once")
	}
}

func TestRevocableServiceCancellationRetainsUnresolvedCleanup(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
	defer cancel()
	service := &revocableServiceFixture{transportServiceFixture: newTransportServiceFixture(), lifetime: ctx, cancel: cancel}
	release := make(chan struct{})
	service.closeRelease, service.readRelease = release, release
	service.readEntered = make(chan struct{})
	local, _ := memoryStreams()
	baseline := len(transportSlots)
	tr, err := NewPassiveTransportUntil(context.Background(), local, memoryCommitter, nil, service, transportServiceRoute(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	defer func() { once.Do(func() { close(release) }); joinTransport(t, tr) }()
	awaitSignal(t, service.readEntered, "retained service read")
	cancel()
	awaitSignal(t, local.closed, "stream closed despite blocked service")
	awaitSignal(t, service.closeEntered, "service cleanup starts despite blocked read")
	requirePendingDrain(t, tr.CloseContext(canceledTransportContext()), context.Canceled)
	if len(transportSlots) != baseline+1 {
		t.Fatal("service cancellation forgave pending cleanup")
	}
	once.Do(func() { close(release) })
	joinTransport(t, tr)
	if len(transportSlots) != baseline || service.closes.Load() != 1 {
		t.Fatal("resolved service cleanup did not release capacity exactly once")
	}
}

func TestRevocableServiceCancellationRetainsFailedCleanup(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
	defer cancel()
	service := &revocableServiceFixture{transportServiceFixture: newTransportServiceFixture(), lifetime: ctx, cancel: cancel}
	failure := errors.New("service resource closure unconfirmed")
	service.closeError = failure
	local, _ := memoryStreams()
	baseline := len(transportSlots)
	tr, err := NewPassiveTransportUntil(context.Background(), local, memoryCommitter, nil, service, transportServiceRoute(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	awaitSignal(t, tr.done, "failed revoked service cleanup")
	if err := tr.Close(); !errors.Is(err, ErrDrainUncertain) || !errors.Is(err, failure) || len(transportSlots) != baseline+1 || service.closes.Load() != 1 {
		t.Fatal("service cancellation hid uncertain cleanup", err)
	}
	// This fixture owns no external resource. Production has no debt waiver.
	t.Cleanup(func() { <-transportSlots })
}

func TestListenerLifetimeIsStableAndZeroIsInvalid(t *testing.T) {
	var absent *Listener
	var zero Listener
	if absent.Lifetime() != nil || zero.Lifetime() != nil {
		t.Fatal("zero listener invented a service lifetime")
	}
	l, err := NewListener(context.Background(), listenConfig())
	if err != nil {
		t.Fatal(err)
	}
	lifetime := l.Lifetime()
	if lifetime == nil || lifetime != l.Lifetime() || lifetime.Err() != nil {
		t.Fatal("listener lifetime is not stable")
	}
	_ = l.Close()
	if !errors.Is(lifetime.Err(), context.Canceled) {
		t.Fatal("listener close did not revoke its admission lifetime")
	}
}
