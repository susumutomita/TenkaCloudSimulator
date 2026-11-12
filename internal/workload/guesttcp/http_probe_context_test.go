package guesttcp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestProbeObservationContextKeepsOriginalDeadlineError(t *testing.T) {
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	nic, cancelNIC := context.WithCancel(context.Background())
	defer cancelNIC()
	ctx := &probeObservationContext{Context: expired, nic: &persistentNIC{base: &Transport{ctx: nic}}, cancel: stop}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatal("initial error", ctx.Err())
	}
	cancelNIC()
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatal("NIC revocation changed terminal context error", ctx.Err())
	}
}

type probeDoneOnlyContext struct {
	context.Context
	done chan struct{}
}

func (c probeDoneOnlyContext) Done() <-chan struct{} { return c.done }
func TestProbeObservationContextNormalizesServiceRevocationError(t *testing.T) {
	call, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	close(done)
	service := probeDoneOnlyContext{context.Background(), done}
	ctx := &probeObservationContext{Context: call, nic: &persistentNIC{base: &Transport{ctx: context.Background(), serviceLifetime: service}}, cancel: cancel}
	if ctx.Err() != context.Canceled || ctx.Err() != context.Canceled {
		t.Fatal("non-context admission error escaped", ctx.Err())
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("Err did not revoke Done")
	}
}

// The short observer shares the independent original-deadline fix. Its caller
// must return even if a trusted owner violates cancellation while publishing.
func TestHTTPObservationDelayedParentBoundsBlockedPublication(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	owner := &httpMemoryOwner{accept: func(context.Context, HTTPObservation) error { close(entered); <-release; return nil }}
	fixture := newHTTPFixture(t, owner, nil, func(conn net.Conn, _ *http.Request) {
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
	})
	observer, err := NewHTTPObserver(fixture.transport, "/")
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		v   HTTPObservation
		err error
	}
	done := make(chan result, 1)
	t.Cleanup(func() { once.Do(func() { close(release) }); _ = fixture.transport.Close(); waitHTTPWorkers(t) })
	parent := passiveDeadlineParent{Context: context.Background(), deadline: time.Now().Add(200 * time.Millisecond)}
	go func() { v, err := observer.Observe(parent); done <- result{v, err} }()
	awaitSignal(t, entered, "short observer owner publication")
	select {
	case got := <-done:
		if got.v != (HTTPObservation{}) || !errors.Is(got.err, context.DeadlineExceeded) {
			t.Fatal("deadline result", got.v, got.err)
		}
	case <-time.After(time.Second):
		once.Do(func() { close(release) })
		<-done
		t.Fatal("observer depended on delayed parent timer")
	}
	if len(observationSlots) == 0 {
		t.Fatal("blocked owner released observation capacity")
	}
	once.Do(func() { close(release) })
	waitHTTPWorkers(t)
	if owner.count() != 0 {
		t.Fatal("expired publication accepted")
	}
}
