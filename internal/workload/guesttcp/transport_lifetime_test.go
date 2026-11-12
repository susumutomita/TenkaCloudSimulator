package guesttcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

func canceledTransportContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func requirePendingDrain(t *testing.T, err, cause error) {
	t.Helper()
	if !errors.Is(err, ErrDrainPending) || !errors.Is(err, cause) || errors.Is(err, ErrDrainUncertain) {
		t.Fatalf("drain result = %v, want pending with %v", err, cause)
	}
}

func joinTransport(t *testing.T, tr *Transport) {
	t.Helper()
	awaitSignal(t, tr.done, "transport owned cleanup")
	if err := tr.CloseContext(canceledTransportContext()); err != nil {
		t.Fatalf("observed completed cleanup = %v", err)
	}
}

// A deliberately nonconforming committer ignores cancellation. Engine teardown
// must not permit an unbounded number of these retained transport workers.
func TestTransportLifetimeBlockedCommittersRetainCapacity(t *testing.T) {
	baseline := len(transportSlots)
	if baseline != 0 {
		t.Fatalf("unexpected retained transports: %d", baseline)
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	var transports []*Transport
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		for _, tr := range transports {
			joinTransport(t, tr)
		}
	})
	lateResults := make(chan error, cap(transportSlots))
	var remotes []*memoryStream
	for i := 0; i < cap(transportSlots); i++ {
		local, remote := memoryStreams()
		entered := make(chan struct{})
		owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
			close(entered)
			<-release
			err := apply()
			lateResults <- err
			return err
		})
		ac := arpBinding(config())
		tr, err := NewTransport(context.Background(), config(), local, owner, &ac)
		if err != nil {
			t.Fatal(err)
		}
		transports = append(transports, tr)
		remotes = append(remotes, remote)
		if err := guestnet.NewEncoder(remote).WriteFrame(requestARP(ac)); err != nil {
			t.Fatal(err)
		}
		awaitSignal(t, entered, "nonconforming committer admission")
		requirePendingDrain(t, tr.CloseContext(canceledTransportContext()), context.Canceled)
		awaitSignal(t, local.closed, "stream closed before committer joined")
		// This concrete in-memory teardown joins even while CommitFrame is
		// blocked. Releasing the engine slot must not release transport debt.
		tr.engine.Close()
	}
	if len(transportSlots) != cap(transportSlots) || len(slots) != 0 {
		t.Fatalf("transport=%d engine=%d slots, want retained transports only", len(transportSlots), len(slots))
	}
	candidate := &fixedReadStream{Reader: bytes.NewReader(nil)}
	if tr, err := NewTransport(context.Background(), config(), candidate, memoryCommitter, nil); tr != nil || !errors.Is(err, ErrCapacity) {
		t.Fatalf("blocked transport capacity bypass: %v", err)
	}
	if tr, err := NewTransport(canceledTransportContext(), config(), candidate, memoryCommitter, nil); tr != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled constructor: %v", err)
	}
	if candidate.closes.Load() != 0 {
		t.Fatal("constructor failure consumed caller stream")
	}
	releaseOnce.Do(func() { close(release) })
	for _, tr := range transports {
		joinTransport(t, tr)
	}
	for range transports {
		if err := <-lateResults; !errors.Is(err, context.Canceled) {
			t.Fatalf("late callback admitted: %v", err)
		}
	}
	for _, remote := range remotes {
		if len(remote.in) != 0 {
			t.Fatal("delayed owner emitted a frame after cancellation")
		}
	}
	if len(transportSlots) != baseline {
		t.Fatal("completed transports did not restore capacity")
	}
	local, _ := memoryStreams()
	replacement, err := NewTransport(context.Background(), config(), local, memoryCommitter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
}

type stalledCloseStream struct {
	*memoryStream
	entered, release chan struct{}
	closes           atomic.Int32
}

func (s *stalledCloseStream) Close() error {
	s.closes.Add(1)
	close(s.entered)
	<-s.release
	return s.memoryStream.Close()
}

func TestTransportLifetimeCloseHasOneBoundedGrace(t *testing.T) {
	baseline := len(transportSlots)
	local, _ := memoryStreams()
	stream := &stalledCloseStream{memoryStream: local, entered: make(chan struct{}), release: make(chan struct{})}
	tr, err := NewTransport(context.Background(), config(), stream, memoryCommitter, nil)
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(stream.release) }); joinTransport(t, tr) })
	requirePendingDrain(t, tr.CloseContext(canceledTransportContext()), context.Canceled)
	awaitSignal(t, stream.entered, "blocked stream close")
	tr.engine.Close()
	if len(transportSlots) != baseline+1 {
		t.Fatal("blocked Close released transport capacity")
	}
	drainBy := tr.drainBy
	// Exercise the actual net.Conn Close adapter, with no socket or TCP dial.
	conn := &transportConnection{owner: tr}
	start := time.Now()
	requirePendingDrain(t, conn.Close(), context.DeadlineExceeded)
	if elapsed := time.Since(start); elapsed > transportWaitTimeout+time.Second {
		t.Fatalf("default connection.Close was not bounded: %v", elapsed)
	}
	start = time.Now()
	requirePendingDrain(t, tr.Close(), context.DeadlineExceeded)
	if time.Since(start) > time.Second || tr.drainBy != drainBy || stream.closes.Load() != 1 {
		t.Fatal("repeated close extended grace or repeated stream.Close")
	}
	releaseOnce.Do(func() { close(stream.release) })
	joinTransport(t, tr)
	if len(transportSlots) != baseline {
		t.Fatal("joined close did not release capacity")
	}
}

func TestTransportLifetimeWaitDoesNotRevoke(t *testing.T) {
	local, _ := memoryStreams()
	tr, err := NewTransport(context.Background(), config(), local, memoryCommitter, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close(); joinTransport(t, tr) })
	if err := tr.WaitContext(nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("nil observation context = %v", err)
	}
	if err := tr.CloseContext(nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("nil close context = %v", err)
	}
	requirePendingDrain(t, tr.WaitContext(canceledTransportContext()), context.Canceled)
	if tr.ctx.Err() != nil {
		t.Fatal("read-only wait or invalid close revoked transport")
	}
	engineDeadline, _ := tr.engine.ctx.Deadline()
	transportDeadline, _ := tr.ctx.Deadline()
	if transportDeadline != engineDeadline {
		t.Fatal("transport extended original engine lifetime")
	}
	const closers = 12
	results := make(chan error, closers)
	for range closers {
		go func() { results <- tr.CloseContext(context.Background()) }()
	}
	for range closers {
		if err := <-results; err != nil {
			t.Fatalf("concurrent close = %v", err)
		}
	}
	if err := tr.WaitContext(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("terminal result = %v", err)
	}
}

type failedCloseStream struct {
	*fixedReadStream
	err error
}

func (s *failedCloseStream) Close() error { s.closes.Add(1); return s.err }

func TestTransportLifetimeFailedCloseRetainsDebt(t *testing.T) {
	baseline := len(transportSlots)
	failure := errors.New("test stream closure failed")
	stream := &failedCloseStream{fixedReadStream: &fixedReadStream{Reader: bytes.NewReader(nil)}, err: failure}
	tr, err := NewTransport(context.Background(), config(), stream, memoryCommitter, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, tr.done, "failed close joins owned calls")
	if len(transportSlots) != baseline+1 {
		t.Fatal("failed stream close lost unresolved ownership debt")
	}
	// This fixture has no real stream resource. Production intentionally has no
	// API to forgive this debt. Remove only the test's token after asserting it.
	t.Cleanup(func() { <-transportSlots })
	for range 3 {
		err := tr.CloseContext(canceledTransportContext())
		if !errors.Is(err, ErrDrainUncertain) || !errors.Is(err, failure) || errors.Is(err, ErrDrainPending) {
			t.Fatalf("joined but uncertain stream result = %v", err)
		}
	}
	if err := tr.Wait(); !errors.Is(err, io.EOF) || !errors.Is(err, ErrDrainUncertain) {
		t.Fatalf("terminal result lost stream uncertainty: %v", err)
	}
	if stream.closes.Load() != 1 || len(transportSlots) != baseline+1 {
		t.Fatal("repeated observation retried or forgave failed close")
	}
}

// Simulates a deadline whose timer notification has not yet been scheduled.
// Admission must check the absolute deadline, not rely only on ctx.Err().
type elapsedDeadlineContext struct{ context.Context }

func (elapsedDeadlineContext) Deadline() (time.Time, bool) { return time.Now().Add(-time.Second), true }

func TestTransportLifetimeAbsoluteAdmissionDeadline(t *testing.T) {
	var admitted atomic.Int32
	owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
		admitted.Add(1)
		return apply()
	})
	tr := &Transport{ctx: elapsedDeadlineContext{context.Background()}, owner: owner}
	if err := tr.commit(ToGuest, nil, func() error { t.Fatal("expired admission applied"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired absolute admission = %v", err)
	}
	if admitted.Load() != 0 {
		t.Fatal("expired admission invoked dependency")
	}
}

func TestTransportLifetimeDialCancellationReturnsBeforeBlockedClose(t *testing.T) {
	local, _ := memoryStreams()
	stream := &stalledCloseStream{memoryStream: local, entered: make(chan struct{}), release: make(chan struct{})}
	tr, err := NewTransport(context.Background(), config(), stream, memoryCommitter, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(stream.release); joinTransport(t, tr) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	conn, err := tr.Dial(ctx)
	if conn != nil {
		t.Fatal("canceled dial returned a connection")
	}
	requirePendingDrain(t, err, context.DeadlineExceeded)
	if time.Since(start) > time.Second {
		t.Fatal("dial waited for blocked stream close")
	}
	awaitSignal(t, stream.entered, "dial cancellation close")
}

func TestTransportLifetimeEngineCancellationGatesAdmissionBeforeSupervisor(t *testing.T) {
	engineCtx, cancel := context.WithCancel(context.Background())
	cancel()
	tr := &Transport{ctx: context.Background(), engine: &Engine{ctx: engineCtx}, owner: memoryCommitter}
	if err := tr.commit(ToGuest, nil, func() error { t.Fatal("revoked engine admitted a frame"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("revoked engine admission = %v", err)
	}
}

func TestTransportLifetimeInFlightCommitCannotReportLateSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr := &Transport{ctx: ctx, owner: memoryCommitter}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- tr.commit(ToGuest, nil, func() error { close(entered); <-release; return nil })
	}()
	awaitSignal(t, entered, "in-flight commit")
	cancel()
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("late successful dependency result = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("commit did not join released dependency")
	}
	// An entered physical write could already have effects. This check denies
	// late success; it does not claim to recall bytes or enforce physical revoke.
}
