package guesttcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// This owner models a memory fence only. It cannot authorize AWS/physical I/O.
type httpMemoryOwner struct {
	mu           sync.Mutex
	revoked      bool
	observations []HTTPObservation
	accept       func(context.Context, HTTPObservation) error
}

func (o *httpMemoryOwner) CommitFrame(ctx context.Context, _ FrameDirection, _ []byte, apply func() error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.revoked {
		return ErrClosed
	}
	if err := observationContextError(ctx); err != nil {
		return err
	}
	return apply()
}
func (o *httpMemoryOwner) CommitHTTPObservation(ctx context.Context, v HTTPObservation) error {
	if o.accept != nil {
		if err := o.accept(ctx, v); err != nil {
			return err
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.revoked {
		return ErrClosed
	}
	if err := observationContextError(ctx); err != nil {
		return err
	}
	o.observations = append(o.observations, v)
	return nil
}
func (o *httpMemoryOwner) revoke()    { o.mu.Lock(); o.revoked = true; o.mu.Unlock() }
func (o *httpMemoryOwner) count() int { o.mu.Lock(); defer o.mu.Unlock(); return len(o.observations) }

type httpFixture struct {
	transport      *Transport
	requests       chan *http.Request
	owner          *httpMemoryOwner
	cancelLifetime context.CancelFunc
}

// Uses only two independent gVisor stacks and bounded channel streams. The
// net.Listener returned by gatewayPeer belongs to that memory stack, NOT the OS.
func newHTTPFixture(t *testing.T, owner *httpMemoryOwner, wrap func(*memoryStream) io.ReadWriteCloser, reply func(net.Conn, *http.Request)) *httpFixture {
	t.Helper()
	c, ac := config(), arpBinding(config())
	ctx, cancel := context.WithCancel(context.Background())
	local, remote := memoryStreams()
	var stream io.ReadWriteCloser = local
	if wrap != nil {
		stream = wrap(local)
	}
	tr, err := NewTransport(ctx, c, stream, owner, &ac)
	if err != nil {
		t.Fatal(err)
	}
	s, link, ln := gatewayPeer(t, c, ac)
	requests := make(chan *http.Request, 1)
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		decoder := guestnet.NewDecoder(remote)
		for {
			frame, err := decoder.ReadFrame()
			if err != nil {
				return
			}
			p := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(frame)})
			link.InjectInbound(0, p)
			p.DecRef()
		}
	}()
	go func() {
		defer workers.Done()
		encoder := guestnet.NewEncoder(remote)
		for {
			p := link.ReadContext(ctx)
			if p == nil {
				return
			}
			view := p.ToView()
			frame := append([]byte(nil), view.AsSlice()...)
			view.Release()
			p.DecRef()
			if encoder.WriteFrame(frame) != nil {
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		r, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		requests <- r
		reply(conn, r)
	}()
	t.Cleanup(func() {
		cancel()
		_ = remote.Close()
		_ = tr.Close()
		_ = ln.Close()
		link.Close()
		s.Destroy()
		workers.Wait()
	})
	return &httpFixture{tr, requests, owner, cancel}
}
func observeHTTPFixture(t *testing.T, f *httpFixture, path string) (HTTPObservation, error) {
	t.Helper()
	o, err := NewHTTPObserver(f.transport, path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return o.Observe(ctx)
}
func waitHTTPWorkers(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(observationSlots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := len(observationSlots); n != 0 {
		t.Fatalf("observation workers retained: %d", n)
	}
}

func TestHTTPObservationActualStatusesAndFixedRequest(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://must-not-be-contacted.invalid:1234")
	t.Setenv("ALL_PROXY", "http://must-not-be-contacted.invalid:1234")
	for _, path := range []string{"/", "/healthz"} {
		for _, status := range []int{200, 204, 302, 401, 503} {
			t.Run(fmt.Sprintf("%s/%d", path, status), func(t *testing.T) {
				owner := new(httpMemoryOwner)
				f := newHTTPFixture(t, owner, nil, func(c net.Conn, _ *http.Request) {
					_, _ = fmt.Fprintf(c, "HTTP/1.1 %d observed\r\nLocation: http://elsewhere.invalid/steal\r\nSet-Cookie: secret=value\r\nContent-Length: 0\r\n\r\n", status)
				})
				v, err := observeHTTPFixture(t, f, path)
				if err != nil || v.StatusCode != status || v.StartedAt.IsZero() || v.ReceivedAt.Before(v.StartedAt) || owner.count() != 1 {
					t.Fatalf("observation=%+v error=%v accepted=%d", v, err, owner.count())
				}
				r := <-f.requests
				if r.Method != "GET" || r.RequestURI != path || r.Host != "10.0.1.4:80" || !r.Close || r.ContentLength != 0 || len(r.Header) != 1 || r.Header.Get("Connection") != "close" {
					t.Fatalf("unexpected fixed request: %+v", r)
				}
				select {
				case <-f.transport.done:
				default:
					t.Fatal("accepted before transport drained")
				}
				waitHTTPWorkers(t)
			})
		}
	}
}

func TestHTTPObservationBoundedResponseParsing(t *testing.T) {
	const final = "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"
	cases := []struct {
		name, response string
		good           bool
	}{
		{"http10", "HTTP/1.0 404 Missing\r\n\r\n", true},
		{"eight informational", strings.Repeat("HTTP/1.1 103 Hint\r\n\r\n", 8) + final, true},
		{"nine informational", strings.Repeat("HTTP/1.1 103 Hint\r\n\r\n", 9) + final, false},
		{"64 fields", "HTTP/1.1 200 OK\r\n" + strings.Repeat("X-A: a\r\n", 64) + "\r\n", true},
		{"65 fields", "HTTP/1.1 200 OK\r\n" + strings.Repeat("X-A: a\r\n", 65) + "\r\n", false},
		{"duplicate normalized fields", "HTTP/1.1 200 OK\r\n" + strings.Repeat("Content-Length: 0\r\n", 65) + "\r\n", false},
		{"cumulative fields", strings.Repeat("HTTP/1.1 103 Hint\r\n"+strings.Repeat("X: a\r\n", 8)+"\r\n", 8) + final, false},
		{"exact byte budget", "HTTP/1.1 200 OK\r\nX: " + strings.Repeat("x", maxHTTPHeaderBytes-len("HTTP/1.1 200 OK\r\nX: \r\n\r\n")) + "\r\n\r\n", true},
		{"one byte over budget", "HTTP/1.1 200 OK\r\nX: " + strings.Repeat("x", 1+maxHTTPHeaderBytes-len("HTTP/1.1 200 OK\r\nX: \r\n\r\n")) + "\r\n\r\n", false},
		{"oversized field", "HTTP/1.1 200 OK\r\nX: " + strings.Repeat("x", maxHTTPHeaderBytes) + "\r\n\r\n", false},
		{"oversized status", "HTTP/1.1 200 " + strings.Repeat("x", maxHTTPHeaderBytes) + "\r\n\r\n", false},
		{"cumulative bytes", strings.Repeat("HTTP/1.1 103 Hint\r\nX: "+strings.Repeat("x", 1050)+"\r\n\r\n", 8) + final, false},
		{"upgrade", "HTTP/1.1 101 Switching Protocols\r\n\r\n", false},
		{"unsupported version", "HTTP/2.0 200 OK\r\n\r\n", false},
		{"bad status", "HTTP/1.1 600 Bad\r\n\r\n", false},
		{"malformed status", "garbage\r\n\r\n", false},
		{"partial header", "HTTP/1.1 200 OK\r\nX: partial", false},
		{"bare LF", "HTTP/1.1 200 OK\n\n", false},
		{"folded header", "HTTP/1.1 200 OK\r\nX: a\r\n folded\r\n\r\n", false},
		{"conflicting lengths", "HTTP/1.1 200 OK\r\nContent-Length: 1\r\nContent-Length: 2\r\n\r\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner := new(httpMemoryOwner)
			f := newHTTPFixture(t, owner, nil, func(c net.Conn, _ *http.Request) { _, _ = io.WriteString(c, tc.response) })
			v, err := observeHTTPFixture(t, f, "/")
			if tc.good {
				if err != nil || v.StatusCode == 0 || owner.count() != 1 {
					t.Fatalf("valid response=%+v err=%v", v, err)
				}
			} else if err == nil || v != (HTTPObservation{}) || owner.count() != 0 {
				t.Fatalf("malformed response accepted=%+v err=%v", v, err)
			}
			waitHTTPWorkers(t)
		})
	}
}

func TestHTTPObservationDoesNotWaitForBody(t *testing.T) {
	owner := new(httpMemoryOwner)
	f := newHTTPFixture(t, owner, nil, func(c net.Conn, _ *http.Request) {
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 1000000000\r\n\r\n")
		var b [1]byte
		_, _ = c.Read(b[:]) // Header-only observation closes without a body.
	})
	v, err := observeHTTPFixture(t, f, "/")
	if err != nil || v.StatusCode != 200 {
		t.Fatalf("header-only result=%+v err=%v", v, err)
	}
	waitHTTPWorkers(t)
}

func TestHTTPObservationDeadlineAndRevocation(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			owner := new(httpMemoryOwner)
			f := newHTTPFixture(t, owner, nil, func(c net.Conn, _ *http.Request) {
				_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nX: partial")
				var b [1]byte
				_, _ = c.Read(b[:])
			})
			o, err := NewHTTPObserver(f.transport, "/")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if revoke {
				go func() { <-f.requests; owner.revoke(); f.transport.beginClose() }()
			}
			start := time.Now()
			v, err := o.Observe(ctx)
			if err == nil || v != (HTTPObservation{}) || owner.count() != 0 || time.Since(start) > time.Second {
				t.Fatalf("deadline/revoked result=%+v err=%v", v, err)
			}
			waitHTTPWorkers(t)
		})
	}
}

func TestHTTPObservationRevalidationAfterCleanup(t *testing.T) {
	owner := new(httpMemoryOwner)
	entered, release := make(chan struct{}), make(chan struct{})
	owner.accept = func(context.Context, HTTPObservation) error { close(entered); <-release; return nil }
	f := newHTTPFixture(t, owner, nil, func(c net.Conn, _ *http.Request) { _, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n") })
	o, _ := NewHTTPObserver(f.transport, "/")
	results := make(chan error, 1)
	go func() {
		v, err := o.Observe(context.Background())
		if v != (HTTPObservation{}) {
			err = errors.New("revoked observation was returned")
		}
		results <- err
	}()
	awaitSignal(t, entered, "final observation fence")
	select {
	case <-f.transport.done:
	default:
		t.Fatal("final fence entered before cleanup")
	}
	owner.revoke()
	close(release)
	if err := <-results; !errors.Is(err, ErrClosed) {
		t.Fatalf("revoked final result=%v", err)
	}
	if owner.count() != 0 {
		t.Fatal("revoked observation published")
	}
	waitHTTPWorkers(t)
}

type failedHTTPMemoryClose struct {
	*memoryStream
	closes atomic.Int32
}

func (s *failedHTTPMemoryClose) Close() error {
	s.closes.Add(1)
	_ = s.memoryStream.Close()
	return errors.New("synthetic uncertain close")
}

func TestHTTPObservationUncertainCleanupRejectsStatus(t *testing.T) {
	baseline := len(transportSlots)
	owner := new(httpMemoryOwner)
	var stream *failedHTTPMemoryClose
	f := newHTTPFixture(t, owner, func(s *memoryStream) io.ReadWriteCloser {
		stream = &failedHTTPMemoryClose{memoryStream: s}
		return stream
	}, func(c net.Conn, _ *http.Request) { _, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n") })
	v, err := observeHTTPFixture(t, f, "/")
	if !errors.Is(err, ErrDrainUncertain) || v != (HTTPObservation{}) || owner.count() != 0 || stream.closes.Load() != 1 || len(transportSlots) != baseline+1 {
		t.Fatalf("uncertain result=%+v err=%v", v, err)
	}
	waitHTTPWorkers(t)
	// Only this channel fixture's test debt is removed, after verifying it.
	// Production intentionally has no forgiveness/reset API.
	<-transportSlots
}

func TestHTTPObservationSingleUseAndConfiguration(t *testing.T) {
	owner := new(httpMemoryOwner)
	f := newHTTPFixture(t, owner, nil, func(c net.Conn, _ *http.Request) { _, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n") })
	for _, path := range []string{"", "http://host/", "//host/", "/healthz?secret=x", "/\r\nAuthorization: bad", "/other"} {
		if o, err := NewHTTPObserver(f.transport, path); o != nil || !errors.Is(err, ErrConfig) {
			t.Fatalf("invalid path %q admitted", path)
		}
	}
	if o, err := NewHTTPObserver(nil, "/"); o != nil || !errors.Is(err, ErrConfig) {
		t.Fatal("nil transport admitted")
	}
	o, _ := NewHTTPObserver(f.transport, "/")
	if _, err := o.Observe(nil); !errors.Is(err, ErrConfig) {
		t.Fatal("nil context admitted")
	}
	if _, err := o.Observe(canceledTransportContext()); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled context admitted")
	}
	if _, err := o.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v, err := o.Observe(context.Background()); err == nil || v != (HTTPObservation{}) || owner.count() != 1 {
		t.Fatal("observation replayed")
	}
	waitHTTPWorkers(t)
	local, _ := memoryStreams()
	tr, err := NewTransport(context.Background(), config(), local, memoryCommitter, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if o, err := NewHTTPObserver(tr, "/"); o != nil || !errors.Is(err, ErrConfig) {
		t.Fatal("owner missing final fence admitted")
	}
}

func TestHTTPObservationBlockedOwnerRetainsAdmission(t *testing.T) {
	waitHTTPWorkers(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); waitHTTPWorkers(t) })
	for i := 0; i < cap(observationSlots); i++ {
		owner := new(httpMemoryOwner)
		entered := make(chan struct{})
		owner.accept = func(context.Context, HTTPObservation) error { close(entered); <-release; return nil }
		f := newHTTPFixture(t, owner, nil, func(c net.Conn, _ *http.Request) { _, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n") })
		o, _ := NewHTTPObserver(f.transport, "/")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			v, err := o.Observe(ctx)
			if v != (HTTPObservation{}) {
				err = errors.New("timed-out observation returned")
			}
			done <- err
		}()
		awaitSignal(t, entered, "blocked final owner")
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked owner result=%v", err)
		}
		cancel()
		if owner.count() != 0 {
			t.Fatal("timed-out owner published")
		}
	}
	if len(observationSlots) != cap(observationSlots) || len(transportSlots) != 0 || len(slots) != 0 {
		t.Fatal("blocked owner did not retain only observation debt")
	}
	owner := new(httpMemoryOwner)
	f := newHTTPFixture(t, owner, nil, func(c net.Conn, _ *http.Request) { _, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n") })
	o, _ := NewHTTPObserver(f.transport, "/")
	if v, err := o.Observe(context.Background()); !errors.Is(err, ErrCapacity) || v != (HTTPObservation{}) {
		t.Fatalf("capacity result=%+v %v", v, err)
	}
	releaseOnce.Do(func() { close(release) })
	waitHTTPWorkers(t)
	if _, err := o.Observe(context.Background()); err != nil {
		t.Fatalf("capacity failed to recover: %v", err)
	}
	waitHTTPWorkers(t)
}

func TestHTTPObservationPendingCleanupRejectsStatus(t *testing.T) {
	owner := new(httpMemoryOwner)
	var stream *stalledCloseStream
	f := newHTTPFixture(t, owner, func(s *memoryStream) io.ReadWriteCloser {
		stream = &stalledCloseStream{memoryStream: s, entered: make(chan struct{}), release: make(chan struct{})}
		return stream
	}, func(c net.Conn, _ *http.Request) { _, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n") })
	t.Cleanup(func() { close(stream.release); joinTransport(t, f.transport) })
	o, _ := NewHTTPObserver(f.transport, "/")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	v, err := o.Observe(ctx)
	if err == nil || v != (HTTPObservation{}) || owner.count() != 0 {
		t.Fatalf("pending cleanup result=%+v error=%v", v, err)
	}
	awaitSignal(t, stream.entered, "pending HTTP cleanup")
	if len(transportSlots) != 1 {
		t.Fatal("pending HTTP cleanup released transport debt")
	}
	waitHTTPWorkers(t)
}

func TestHTTPObservationOriginalLifetimeAfterCleanup(t *testing.T) {
	owner := new(httpMemoryOwner)
	entered, release := make(chan struct{}), make(chan struct{})
	owner.accept = func(context.Context, HTTPObservation) error { close(entered); <-release; return nil }
	f := newHTTPFixture(t, owner, nil, func(c net.Conn, _ *http.Request) { _, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n") })
	o, _ := NewHTTPObserver(f.transport, "/")
	results := make(chan error, 1)
	go func() {
		v, err := o.Observe(context.Background())
		if v != (HTTPObservation{}) {
			err = errors.New("canceled lifetime observation returned")
		}
		results <- err
	}()
	awaitSignal(t, entered, "final fence after own cleanup")
	f.cancelLifetime()
	close(release)
	if err := <-results; !errors.Is(err, context.Canceled) {
		t.Fatalf("original lifetime cancellation lost: %v", err)
	}
	waitHTTPWorkers(t)
	if owner.count() != 0 {
		t.Fatal("original lifetime cancellation published")
	}
}

// Inject only the recorded terminal cause at the existing cleanup boundary.
// The supervisor writes its cause before starting Close, and Observe reads it
// only after Close returns and done closes, so these fixture writes are ordered.
// HTTP itself still traverses the real memory TCP/framed transport.
type terminalCauseHTTPStream struct {
	*memoryStream
	setCause func()
}

func (s *terminalCauseHTTPStream) Close() error {
	s.setCause()
	return s.memoryStream.Close()
}

func TestHTTPObservationRejectsMixedTerminalFailure(t *testing.T) {
	cases := []struct {
		name  string
		cause error
		good  bool
	}{
		{"pure cancellation", context.Canceled, true},
		{"truncation and cancellation", errors.Join(io.ErrUnexpectedEOF, context.Canceled), false},
		{"commit failure and cancellation", errors.Join(ErrCommit, context.Canceled), false},
		{"nested failure and cancellation", fmt.Errorf("nested: %w", errors.Join(ErrCommit, context.Canceled)), false},
		{"wrapped cancellation is not expected shutdown", fmt.Errorf("wrapped: %w", context.Canceled), false},
		{"missing terminal cause", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner := new(httpMemoryOwner)
			var f *httpFixture
			f = newHTTPFixture(t, owner, func(s *memoryStream) io.ReadWriteCloser {
				return &terminalCauseHTTPStream{memoryStream: s, setCause: func() { f.transport.err = tc.cause }}
			}, func(c net.Conn, _ *http.Request) { _, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n") })
			v, err := observeHTTPFixture(t, f, "/")
			if tc.good {
				if err != nil || v.StatusCode != 200 || owner.count() != 1 {
					t.Fatalf("pure shutdown result=%+v error=%v", v, err)
				}
			} else if err == nil || v != (HTTPObservation{}) || owner.count() != 0 {
				t.Fatalf("terminal failure result=%+v error=%v accepted=%d", v, err, owner.count())
			}
			waitHTTPWorkers(t)
		})
	}
}

func TestHTTPObservationNonNotifyingDeadlineRetainsOwnerDebt(t *testing.T) {
	waitHTTPWorkers(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }); waitHTTPWorkers(t) })
	owner := new(httpMemoryOwner)
	captured := make(chan time.Time, 1)
	owner.accept = func(ctx context.Context, _ HTTPObservation) error {
		d, _ := ctx.Deadline()
		captured <- d
		close(entered)
		<-release
		return nil
	}
	f := newHTTPFixture(t, owner, nil, func(c net.Conn, _ *http.Request) { _, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n") })
	o, _ := NewHTTPObserver(f.transport, "/")
	deadline := time.Now().Add(2 * time.Second)
	ctx := ownedSilentDeadline{context.Background(), deadline}
	result := make(chan error, 1)
	go func() {
		v, e := o.Observe(ctx)
		if v != (HTTPObservation{}) {
			e = errors.New("expired observation returned")
		}
		result <- e
	}()
	awaitSignal(t, entered, "non-notifying deadline owner")
	if d := <-captured; !d.Equal(deadline) {
		t.Fatalf("deadline rebased: %v vs %v", d, deadline)
	}
	select {
	case e := <-result:
		if !errors.Is(e, context.DeadlineExceeded) {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deadline result watchdog")
	}
	if time.Now().Before(deadline) {
		t.Fatal("premature deadline result")
	}
	if time.Now().After(deadline.Add(time.Second)) {
		t.Fatal("late deadline result")
	}
	if owner.count() != 0 || len(observationSlots) != 1 {
		t.Fatal("expired owner lost retained debt")
	}
	once.Do(func() { close(release) })
	waitHTTPWorkers(t)
	if owner.count() != 0 {
		t.Fatal("late owner published expired observation")
	}
}
