package guesttcp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

func operationKey(n uint64) (key HTTPProbeOperationKey) {
	binary.BigEndian.PutUint64(key[8:], n)
	return key
}
func startOperation(t *testing.T, f *probeHTTPFixture, ctx context.Context, key HTTPProbeOperationKey, port uint16, path string) *HTTPProbeOperation {
	t.Helper()
	h, err := f.transport.StartHTTPObservation(ctx, key, port, path)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func operationResult(t *testing.T, h *HTTPProbeOperation) HTTPProbeOperationSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := h.WaitResult(ctx)
	if err != nil || !s.ResultReady {
		t.Fatal("result not ready", s, err)
	}
	return s
}
func operationSettled(t *testing.T, h *HTTPProbeOperation) HTTPProbeOperationSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.WaitSettlement(ctx); err != nil {
		t.Fatal("settlement", err)
	}
	s, err := h.Snapshot()
	if err != nil || !s.Settled || !s.ResultReady {
		t.Fatal("settlement preceded result", s, err)
	}
	return s
}
func operationPending(t *testing.T, h *HTTPProbeOperation) {
	t.Helper()
	ctx := passiveDeadlineParent{Context: context.Background(), deadline: time.Now().Add(20 * time.Millisecond)}
	if err := h.WaitSettlement(ctx); !errors.Is(err, ErrDrainPending) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("pending wait fabricated settlement", err)
	}
	s, err := h.Snapshot()
	if err != nil || s.Settled {
		t.Fatal("pending snapshot", s, err)
	}
}

func TestHTTPProbeOperationExactBindingAndDuplicate(t *testing.T) {
	owner := new(probeHTTPMemoryOwner)
	f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), func(c net.Conn, r probeHTTPRequest) {
		status := 302
		if r.route.PeerPort == 8080 {
			status = 503
		}
		_, _ = fmt.Fprintf(c, "HTTP/1.1 %d observed\r\nLocation: http://ignored.invalid/\r\nContent-Length: 0\r\n\r\n", status)
	})
	for i, port := range []uint16{80, 8080} {
		key := operationKey(uint64(i + 1))
		path, status := "/", 302
		if i == 1 {
			path, status = "/healthz", 503
		}
		h := startOperation(t, f, context.Background(), key, port, path)
		// Concurrent rediscovery must return identity, not create another request.
		var wg sync.WaitGroup
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				duplicate, err := f.transport.StartHTTPObservation(canceledTransportContext(), key, port, path)
				if err != nil || duplicate != h {
					t.Error("duplicate changed operation", err)
				}
				got, found := f.transport.LookupHTTPObservation(key)
				if !found || got != h {
					t.Error("lookup lost operation")
				}
				s, err := h.Snapshot()
				if err != nil || (s.Settled && !s.ResultReady) {
					t.Error("inconsistent snapshot", s, err)
				}
			}()
		}
		wg.Wait()
		s := operationSettled(t, h)
		if s.ResultError != nil || s.Observation.StatusCode != status || s.Observation.Path != path || s.Observation.Route.PeerPort != port {
			t.Fatal("actual status/target lost", s)
		}
		req := <-f.requests
		if s.Observation.Route != req.route || req.request.RequestURI != path || req.request.Method != "GET" || req.request.Host != fmt.Sprintf("10.0.1.4:%d", port) {
			t.Fatal("actual tuple/request not bound", s, req)
		}
		if dup, err := f.transport.StartHTTPObservation(nil, key, port, path); err != nil || dup != h {
			t.Fatal("discovery validated a new context", err)
		}
		if dup, err := f.transport.StartHTTPObservation(context.Background(), key, port, "/different"); dup != nil || !errors.Is(err, ErrHTTPProbeOperationBinding) {
			t.Fatal("path rebound", err)
		}
		if dup, err := f.transport.StartHTTPObservation(context.Background(), key, port+1, path); dup != nil || !errors.Is(err, ErrHTTPProbeOperationBinding) {
			t.Fatal("port rebound", err)
		}
		assertProbeHTTPSharedLive(t, f)
	}
	if len(f.requests) != 0 || len(owner.snapshot()) != 2 {
		t.Fatal("duplicate executed or redirect followed")
	}
	// Rewrapping the same NIC cannot reset retention or identity.
	alias := &PersistentTransport{nic: f.transport.nic}
	if h, ok := alias.LookupHTTPObservation(operationKey(1)); !ok || h == nil {
		t.Fatal("NIC identity lost")
	}
	unknown := &PersistentTransport{nic: &persistentNIC{}}
	if h, ok := unknown.LookupHTTPObservation(operationKey(1)); ok || h != nil {
		t.Fatal("recreation invented settlement")
	}
}

func TestHTTPProbeOperationDiscoveryWaitDoesNotCancel(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	owner := &probeHTTPMemoryOwner{before: func(context.Context, ProbeHTTPObservation) error { close(entered); <-release; return nil }}
	f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), probeHTTPReply)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	h := startOperation(t, f, context.Background(), operationKey(1), 80, "/")
	awaitSignal(t, entered, "publication")
	before, _ := h.Snapshot()
	discovery, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := f.transport.StartHTTPObservation(discovery, operationKey(1), 80, "/"); err != nil || got != h {
		t.Fatal("duplicate", err)
	}
	if s, err := h.WaitResult(discovery); !errors.Is(err, context.Canceled) || s.Settled {
		t.Fatal("result wait", s, err)
	}
	operationPending(t, h)
	after, _ := h.Snapshot()
	if after != before || len(observationSlots) != 1 {
		t.Fatal("discovery changed original operation", before, after)
	}
	once.Do(func() { close(release) })
	s := operationSettled(t, h)
	if s.ResultError != nil || s.Observation.StatusCode != 200 || len(owner.snapshot()) != 1 {
		t.Fatal("wait canceled original", s)
	}
	assertProbeHTTPSharedLive(t, f)
}

func TestHTTPProbeOperationPublicationCancellationRetainsGlobalCapacity(t *testing.T) {
	waitHTTPWorkers(t)
	entered, release := make(chan struct{}, 8), make(chan struct{})
	var once sync.Once
	owner := &probeHTTPMemoryOwner{before: func(context.Context, ProbeHTTPObservation) error { entered <- struct{}{}; <-release; return nil }}
	f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), probeHTTPReply)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var held []*HTTPProbeOperation
	for i := 0; i < cap(observationSlots); i++ {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		h := startOperation(t, f, ctx, operationKey(uint64(i+1)), 80, "/")
		held = append(held, h)
		awaitSignal(t, entered, "publication")
		cancel()
		s := operationResult(t, h)
		if !errors.Is(s.ResultError, context.Canceled) || s.Observation != (ProbeHTTPObservation{}) || s.Settled {
			t.Fatal("cancellation lost pending work", s)
		}
		operationPending(t, h)
	}
	if len(observationSlots) != 8 {
		t.Fatal("unsettled capacity released")
	}
	rejected := startOperation(t, f, context.Background(), operationKey(100), 80, "/")
	if s := operationSettled(t, rejected); !errors.Is(s.ResultError, ErrCapacity) {
		t.Fatal("capacity rejection did not settle", s)
	}
	// The legacy concrete observer consumes the very same global slot pool.
	legacy := newProbeObserver(t, f, 8080, "/healthz")
	if _, err := legacy.Observe(context.Background()); !errors.Is(err, ErrCapacity) {
		t.Fatal("legacy bypassed capacity", err)
	}
	once.Do(func() { close(release) })
	for _, h := range held {
		s := operationSettled(t, h)
		if !errors.Is(s.ResultError, context.Canceled) || s.Observation != (ProbeHTTPObservation{}) {
			t.Fatal("late settlement replaced error", s)
		}
	}
	if len(observationSlots) != 0 || len(owner.snapshot()) != 0 {
		t.Fatal("late publication or retained slot")
	}
	dup := startOperation(t, f, context.Background(), operationKey(100), 80, "/")
	if dup != rejected || len(f.requests) != 8 {
		t.Fatal("rejected tombstone retried")
	}
	if s := operationSettled(t, startOperation(t, f, context.Background(), operationKey(101), 8080, "/healthz")); s.ResultError != nil || s.Observation.StatusCode != 200 {
		t.Fatal("capacity did not recover", s)
	}
	assertProbeHTTPSharedLive(t, f)
}

func TestHTTPProbeOperationChildDrainTimeout(t *testing.T) {
	for _, point := range []string{"SYN", "header"} {
		t.Run(point, func(t *testing.T) {
			waitHTTPWorkers(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var blockOnce, releaseOnce sync.Once
			owner := new(probeHTTPMemoryOwner)
			owner.frame = func(_ context.Context, d FrameDirection, frame []byte, apply func() error) error {
				packet, err := guestnet.ParseTCPFrame(frame)
				selected := point == "SYN" && err == nil && d == ToGuest && packet.Flags&2 != 0
				selected = selected || (point == "header" && d == FromGuest && bytes.Contains(frame, []byte("HTTP/1.1 200")))
				if !selected {
					return apply()
				}
				if point == "header" {
					if err := apply(); err != nil {
						return err
					}
				}
				blockOnce.Do(func() { close(entered); <-release })
				if point == "SYN" {
					return apply()
				}
				return nil
			}
			f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), probeHTTPReply)
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			ctx := passiveDeadlineParent{Context: context.Background(), deadline: time.Now().Add(250 * time.Millisecond)}
			h := startOperation(t, f, ctx, operationKey(1), 80, "/")
			awaitSignal(t, entered, "child drain")
			s := operationResult(t, h)
			// An entered Transmit revokes the registry at this same deadline.
			// That cancellation may reach the observer before its own timer.
			// Neither outcome permits an early result or fabricated settlement.
			expired := errors.Is(s.ResultError, context.DeadlineExceeded) || errors.Is(s.ResultError, context.Canceled)
			if time.Now().Before(ctx.deadline) || !s.Deadline.Equal(ctx.deadline) || !expired || s.Settled || s.Observation != (ProbeHTTPObservation{}) {
				t.Fatal("child timeout", s)
			}
			operationPending(t, h)
			f.transport.nic.mu.Lock()
			children := len(f.transport.nic.probes)
			f.transport.nic.mu.Unlock()
			if len(observationSlots) != 1 || children != 1 || len(owner.snapshot()) != 0 {
				t.Fatal("unjoined child retired", children)
			}
			releaseOnce.Do(func() { close(release) })
			final := operationSettled(t, h)
			if final.ResultError != s.ResultError || final.Observation != s.Observation || len(observationSlots) != 0 || len(owner.snapshot()) != 0 {
				t.Fatal("settlement changed result", final)
			}
			if point == "SYN" && len(f.requests) != 0 {
				t.Fatal("late canceled SYN reached peer")
			}
		})
	}
}

func TestHTTPProbeOperationRetainedFailuresAndQuota(t *testing.T) {
	owner := new(probeHTTPMemoryOwner)
	f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), probeHTTPReply)
	if h, err := f.transport.StartHTTPObservation(context.Background(), HTTPProbeOperationKey{}, 80, "/"); h != nil || !errors.Is(err, ErrConfig) {
		t.Fatal("zero key", err)
	}
	if h, err := f.transport.StartHTTPObservation(context.Background(), operationKey(1), 80, "/arbitrary"); h != nil || !errors.Is(err, ErrConfig) {
		t.Fatal("path", err)
	}
	var typedNil *probeObservationContext
	if h, err := f.transport.StartHTTPObservation(typedNil, operationKey(1), 80, "/"); h != nil || !errors.Is(err, ErrConfig) {
		t.Fatal("typed nil initial context", err)
	}
	// The final retention check must also reject a candidate paused before a
	// different set of calls consumes the remaining budget.
	delayed := &operationDeadlineContext{Context: context.Background(), entered: make(chan struct{}), release: make(chan struct{}), deadline: time.Now().Add(time.Minute)}
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(delayed.release) }) })
	racingResult := make(chan error, 1)
	f.callers.Add(1)
	go func() {
		defer f.callers.Done()
		h, err := f.transport.StartHTTPObservation(delayed, operationKey(3000), 80, "/")
		if h != nil {
			t.Error("racing full quota retained a new handle")
		}
		racingResult <- err
	}()
	awaitSignal(t, delayed.entered, "pre-quota deadline capture")
	for i := 1; i <= guestnet.MaxProbeSessions; i++ {
		port := uint16(80)
		ctx := canceledTransportContext()
		want := error(context.Canceled)
		if i%2 == 0 {
			port = 443
			ctx = context.Background()
			want = ErrConfig
		}
		h := startOperation(t, f, ctx, operationKey(uint64(i)), port, "/")
		s := operationSettled(t, h)
		if !errors.Is(s.ResultError, want) || s.Observation != (ProbeHTTPObservation{}) {
			t.Fatal("early rejection did not settle", s)
		}
	}
	if len(f.requests) != 0 || len(observationSlots) != 0 {
		t.Fatal("early rejection executed")
	}
	releaseOnce.Do(func() { close(delayed.release) })
	if err := <-racingResult; !errors.Is(err, ErrCapacity) {
		t.Fatal("quota changed during capture", err)
	}

	if h, err := f.transport.StartHTTPObservation(context.Background(), operationKey(2000), 80, "/"); h != nil || !errors.Is(err, ErrCapacity) {
		t.Fatal("retention quota bypassed", err)
	}
	alias := &PersistentTransport{nic: f.transport.nic}
	if h, err := alias.StartHTTPObservation(operationUnreadableContext{}, operationKey(2001), 80, "/"); h != nil || !errors.Is(err, ErrCapacity) {
		t.Fatal("NIC alias reset quota", err)
	}
	original, _ := alias.LookupHTTPObservation(operationKey(1))
	duplicate := startOperation(t, f, context.Background(), operationKey(1), 80, "/")
	if duplicate != original || !errors.Is(operationSettled(t, duplicate).ResultError, context.Canceled) {
		t.Fatal("tombstone reused")
	}
	if _, ok := alias.LookupHTTPObservation(operationKey(2000)); ok {
		t.Fatal("quota rejection recorded a handle")
	}
	if len(f.transport.nic.operations) != guestnet.MaxProbeSessions {
		t.Fatal("unbounded retention")
	}
	assertProbeHTTPSharedLive(t, f)
}

func TestHTTPProbeOperationCapturedDeadlineAndRevocation(t *testing.T) {
	for _, bound := range []string{"eight seconds", "caller", "NIC", "revocation"} {
		t.Run(bound, func(t *testing.T) {
			captured := make(chan time.Time, 1)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			owner := &probeHTTPMemoryOwner{before: func(ctx context.Context, _ ProbeHTTPObservation) error {
				d, _ := ctx.Deadline()
				captured <- d
				close(entered)
				<-release
				return nil
			}}
			var caller, parent context.Context = context.Background(), context.Background()
			d := time.Now().Add(3 * time.Second)
			if bound == "caller" {
				caller = passiveDeadlineParent{Context: context.Background(), deadline: d}
			}
			if bound == "NIC" {
				parent = passiveDeadlineParent{Context: context.Background(), deadline: d}
			}
			f := newProbeHTTPFixture(t, owner, parent, persistentConfig(), probeHTTPReply)
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			h := startOperation(t, f, caller, operationKey(1), 80, "/")
			awaitSignal(t, entered, "owner publication")
			s, _ := h.Snapshot()
			want := s.StartedAt.Add(HTTPObservationTimeout)
			if bound == "caller" || bound == "NIC" {
				want = d
			}
			if actual := <-captured; !s.Deadline.Equal(want) || !actual.Equal(want) {
				t.Fatal("original deadline reset", s.Deadline, actual, want)
			}
			if bound == "revocation" {
				f.cancelLifetime()
				s = operationResult(t, h)
				if s.ResultError == nil || s.Settled {
					t.Fatal("revocation settled blocked work", s)
				}
				operationPending(t, h)
			}
			once.Do(func() { close(release) })
			final := operationSettled(t, h)
			if bound == "revocation" {
				if final.ResultError == nil || len(owner.snapshot()) != 0 {
					t.Fatal("late revoked success", final)
				}
				rejected := startOperation(t, f, context.Background(), operationKey(2), 80, "/")
				if r := operationSettled(t, rejected); r.ResultError == nil {
					t.Fatal("revoked NIC admitted work", r)
				}
			} else if final.ResultError != nil || final.Observation.StatusCode != 200 {
				t.Fatal("valid result", final)
			}
		})
	}
}

// A deliberately stalled context setup models dispatch delay without replacing
// the concrete observer. Discovery must remain available while setup is stalled.
type operationSetupContext struct {
	context.Context
	deadline time.Time
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (c *operationSetupContext) Deadline() (time.Time, bool) { return c.deadline, true }
func (c *operationSetupContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered); <-c.release })
	return nil
}
func TestHTTPProbeOperationRecordedBeforeDelayedDispatch(t *testing.T) {
	owner := new(probeHTTPMemoryOwner)
	f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), probeHTTPReply)
	ctx := &operationSetupContext{Context: context.Background(), deadline: time.Now().Add(50 * time.Millisecond), entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(ctx.release) }) })
	returned := make(chan *HTTPProbeOperation, 1)
	f.callers.Add(1)
	go func() {
		defer f.callers.Done()
		h, err := f.transport.StartHTTPObservation(ctx, operationKey(1), 80, "/")
		if err != nil {
			t.Error(err)
		}
		returned <- h
	}()
	awaitSignal(t, ctx.entered, "context setup")
	// Do these reads on a bounded worker so a lock regression fails rather than
	// hanging the test before its gate-release cleanup can run.
	discovered := make(chan *HTTPProbeOperation, 1)
	go func() { h, _ := f.transport.LookupHTTPObservation(operationKey(1)); discovered <- h }()
	var h *HTTPProbeOperation
	select {
	case h = <-discovered:
	case <-time.After(time.Second):
		t.Fatal("lookup blocked behind setup")
	}
	if h == nil {
		t.Fatal("operation not recorded before setup")
	}
	s, _ := h.Snapshot()
	if s.ResultReady || s.Settled || !s.Deadline.Equal(ctx.deadline) {
		t.Fatal("premature state", s)
	}
	if duplicate, err := f.transport.StartHTTPObservation(nil, operationKey(1), 80, "/"); err != nil || duplicate != h {
		t.Fatal("discovery during setup", err)
	}
	<-time.After(time.Until(ctx.deadline) + time.Millisecond)
	once.Do(func() { close(ctx.release) })
	if got := <-returned; got != h {
		t.Fatal("dispatch changed identity")
	}
	final := operationSettled(t, h)
	if !errors.Is(final.ResultError, context.DeadlineExceeded) || !final.Deadline.Equal(s.Deadline) || final.StartedAt != s.StartedAt || len(f.requests) != 0 {
		t.Fatal("delayed dispatch renewed operation", final)
	}
}

func TestHTTPProbeOperationInvalidReadsNeverSettle(t *testing.T) {
	var h *HTTPProbeOperation
	if s, err := h.Snapshot(); !errors.Is(err, ErrConfig) || s.Settled || s.ResultReady {
		t.Fatal("nil snapshot", s, err)
	}
	if err := h.WaitSettlement(context.Background()); !errors.Is(err, ErrConfig) {
		t.Fatal("nil settlement", err)
	}
	if s, err := h.WaitResult(context.Background()); !errors.Is(err, ErrConfig) || s.Settled {
		t.Fatal("nil result", s, err)
	}
}

type operationDeadlineContext struct {
	context.Context
	entered  chan struct{}
	release  chan struct{}
	deadline time.Time
}

func (c *operationDeadlineContext) Deadline() (time.Time, bool) {
	close(c.entered)
	<-c.release
	return c.deadline, true
}
func TestHTTPProbeOperationRacingDeadlineCapture(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			owner := new(probeHTTPMemoryOwner)
			f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), probeHTTPReply)
			delayed := &operationDeadlineContext{Context: context.Background(), entered: make(chan struct{}), release: make(chan struct{}), deadline: time.Now().Add(time.Second)}
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(delayed.release) }) })
			type startResult struct {
				h   *HTTPProbeOperation
				err error
			}
			result := make(chan startResult, 1)
			f.callers.Add(1)
			go func() {
				defer f.callers.Done()
				h, err := f.transport.StartHTTPObservation(delayed, operationKey(1), 80, "/")
				result <- startResult{h, err}
			}()
			awaitSignal(t, delayed.entered, "deadline capture")
			port := uint16(80)
			if !same {
				port = 8080
			}
			// Run on a bounded worker to detect a registry lock held during Deadline.
			winning := make(chan startResult, 1)
			f.callers.Add(1)
			go func() {
				defer f.callers.Done()
				h, err := f.transport.StartHTTPObservation(context.Background(), operationKey(1), port, "/")
				winning <- startResult{h, err}
			}()
			var winner startResult
			select {
			case winner = <-winning:
			case <-time.After(time.Second):
				t.Fatal("deadline blocked another admission")
			}
			if winner.err != nil {
				t.Fatal(winner.err)
			}
			operationSettled(t, winner.h)
			once.Do(func() { close(delayed.release) })
			loser := <-result
			if same {
				if loser.err != nil || loser.h != winner.h {
					t.Fatal("racing duplicate replaced winner", loser)
				}
			} else if !errors.Is(loser.err, ErrHTTPProbeOperationBinding) || loser.h != nil {
				t.Fatal("racing target mismatch admitted", loser)
			}
			if len(f.requests) != 1 || len(owner.snapshot()) != 1 {
				t.Fatal("losing candidate executed")
			}
		})
	}
}

// No method on this discovery context may run when the retained quota is full.
type operationUnreadableContext struct{}

func (operationUnreadableContext) Deadline() (time.Time, bool) { panic("unexpected Deadline") }
func (operationUnreadableContext) Done() <-chan struct{}       { panic("unexpected Done") }
func (operationUnreadableContext) Err() error                  { panic("unexpected Err") }
func (operationUnreadableContext) Value(any) any               { panic("unexpected Value") }
func TestHTTPProbeOperationNilWaitContexts(t *testing.T) {
	h := &HTTPProbeOperation{ready: make(chan struct{}), settled: make(chan struct{})}
	var typedNil *probeObservationContext
	for _, ctx := range []context.Context{nil, typedNil} {
		if _, err := h.WaitResult(ctx); !errors.Is(err, ErrConfig) {
			t.Fatal("nil result wait", err)
		}
		if err := h.WaitSettlement(ctx); !errors.Is(err, ErrConfig) {
			t.Fatal("nil settlement wait", err)
		}
	}
}
