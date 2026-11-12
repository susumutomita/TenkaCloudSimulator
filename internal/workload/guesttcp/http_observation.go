package guesttcp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	HTTPObservationTimeout = 8 * time.Second
	maxHTTPHeaderBytes     = 8 << 10
	maxHTTPHeaderFields    = 64
	maxHTTPInformational   = 8
)

var (
	ErrHTTPResponse  = errors.New("unsupported private HTTP response")
	observationSlots = make(chan struct{}, 8)
)

// HTTPObservation contains only the status actually received and local timing.
// It contains no response headers/body, destination, credentials or score.
// ReceivedAt is when the complete final response header was parsed, not proof
// that a response body completed or that a service remains available later.
type HTTPObservation struct {
	StatusCode            int
	StartedAt, ReceivedAt time.Time
}

// HTTPObservationCommitter must be implemented by the SAME immutable physical
// owner supplied to NewTransport. After transport cleanup has completed, it must
// synchronously serialize acceptance of this observation with current owner,
// incarnation and policy revocation. Reject stale ownership, expired contexts
// and uncertain cleanup; never resurrect an owner merely because I/O ended.
//
// A successful call is the observation's linearization point, not a permit for
// future traffic or scoring. Provider-specific persistence/authorization lives
// outside this package. This package has no production implementation: a fake
// owner or a prior metadata snapshot cannot qualify physical authority.
//
// The owner must honor cancellation and never block indefinitely. A violation
// cannot block the caller past its deadline, but retains an observation slot
// until the call actually returns. A timed-out call must not later publish.
type HTTPObservationCommitter interface {
	CommitHTTPObservation(context.Context, HTTPObservation) error
}

// HTTPObserver binds a single-use request to an existing concrete Transport.
// The trusted server chooses path during construction, never from guest/host
// request input. Only GET / and GET /healthz are supported. No hostname, URL,
// port, query, credentials, headers or body can be supplied to Observe.
// Construction does not consume/close the transport. Once admitted, Observe
// consumes it even on error; invalid/canceled/capacity failures before admission
// leave it untouched. Do not bind multiple observers to the same transport.
// There is no production binding/host endpoint or runtime-default change here.
type HTTPObserver struct {
	transport *Transport
	owner     HTTPObservationCommitter
	path      string
	mu        sync.Mutex
	started   bool
}

func NewHTTPObserver(t *Transport, path string) (*HTTPObserver, error) {
	if t == nil || t.engine == nil || nilInterface(t.lifetime) || (path != "/" && path != "/healthz") {
		return nil, ErrConfig
	}
	owner, ok := t.owner.(HTTPObservationCommitter)
	if !ok || nilInterface(owner) {
		return nil, ErrConfig
	}
	if err := t.admissionError(); err != nil {
		return nil, err
	}
	return &HTTPObserver{transport: t, owner: owner, path: path}, nil
}

// Observe performs one bounded HTTP/1 exchange over actual userspace TCP and
// framing. There is no host dialer, DNS, ambient proxy, cookie jar, redirect,
// automatic retry, HTTP/2, decompression or response body consumption. Redirect
// and error status codes are returned as received, not promoted to success.
//
// The caller's deadline, the original transport deadline and eight seconds all
// bound this call. A timeout requests transport closure, but does not establish
// cleanup; transport and observation admission retain unfinished work. Failed,
// revoked, malformed or uncertain work returns the zero observation.
func (o *HTTPObserver) Observe(ctx context.Context) (HTTPObservation, error) {
	if o == nil || o.transport == nil || nilInterface(ctx) {
		return HTTPObservation{}, ErrConfig
	}
	if err := observationContextError(ctx); err != nil {
		return HTTPObservation{}, err
	}
	if err := o.transport.admissionError(); err != nil {
		return HTTPObservation{}, err
	}
	o.mu.Lock()
	if o.started {
		o.mu.Unlock()
		return HTTPObservation{}, ErrCapacity
	}
	select {
	case observationSlots <- struct{}{}:
	default:
		o.mu.Unlock()
		return HTTPObservation{}, ErrCapacity
	}
	o.started = true
	o.mu.Unlock()
	started := time.Now()
	deadline := started.Add(HTTPObservationTimeout)
	if expires, ok := o.transport.ctx.Deadline(); ok && expires.Before(deadline) {
		deadline = expires
	}
	if earlier, ok := ctx.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	stopLifetime := context.AfterFunc(o.transport.lifetime, cancel)
	defer stopLifetime()
	call := &httpObservationContext{Context: bounded, lifetime: o.transport.lifetime, cancel: cancel}
	type result struct {
		observation HTTPObservation
		err         error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-observationSlots }()
		observation, err := o.observe(call, cancel, started)
		done <- result{observation, err}
	}()
	select {
	case r := <-done:
		if err := observationContextError(call); err != nil {
			return HTTPObservation{}, err
		}
		return r.observation, r.err
	case <-call.Done():
		o.transport.beginClose()
		return HTTPObservation{}, call.Err()
	}
}

// AfterFunc wakes a blocked operation, but its goroutine may lag cancellation.
// Err synchronously propagates the original lifetime before owner admission;
// cancel also closes Done before returning the error, preserving its contract.
type httpObservationContext struct {
	context.Context
	lifetime context.Context
	cancel   context.CancelFunc
}

func (c *httpObservationContext) Err() error {
	if c.lifetime.Err() != nil {
		c.cancel()
	}
	return c.Context.Err()
}

func observationContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func (o *HTTPObserver) observe(ctx context.Context, cancel context.CancelFunc, started time.Time) (observation HTTPObservation, err error) {
	t := o.transport
	// Propagate transport revocation during network I/O. Stop this hook only
	// before our own intentional teardown; final owner acceptance must then
	// independently fence lease/policy revocation, including that handoff race.
	// The separate original lifetime remains attached through final acceptance.
	stop := context.AfterFunc(t.ctx, cancel)
	defer stop()
	cleaned := false
	defer func() {
		stop()
		if !cleaned {
			err = errors.Join(err, t.CloseContext(ctx))
		}
		if err != nil {
			observation = HTTPObservation{}
		}
	}()
	conn, err := t.Dial(ctx)
	if err != nil {
		return HTTPObservation{}, err
	}
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return HTTPObservation{}, err
	}
	address := net.JoinHostPort(t.engine.config.PeerIP.String(), strconv.Itoa(int(t.engine.config.Port)))
	request := "GET " + o.path + " HTTP/1.1\r\nHost: " + address + "\r\nConnection: close\r\n\r\n"
	n, err := io.WriteString(conn, request)
	if err != nil {
		return HTTPObservation{}, err
	}
	if n != len(request) {
		return HTTPObservation{}, io.ErrShortWrite
	}
	observation, err = readHTTPObservationHeader(conn, started)
	if err != nil {
		return HTTPObservation{}, err
	}
	if err := errors.Join(observationContextError(ctx), t.admissionError()); err != nil {
		return HTTPObservation{}, err
	}
	stop()
	if err := errors.Join(observationContextError(ctx), t.admissionError()); err != nil {
		return HTTPObservation{}, err
	}
	if err := t.CloseContext(ctx); err != nil {
		return HTTPObservation{}, err
	}
	cleaned = true
	// The transport retains its first selected terminal cause, not every pump
	// failure during teardown. Only our exact intentional cancellation is
	// expected here: errors.Is would also accept a joined cancellation plus a
	// real protocol/owner failure. Final current-owner acceptance remains required.
	if t.err != context.Canceled {
		return HTTPObservation{}, errors.Join(ErrHTTPResponse, t.err)
	}
	if err := observationContextError(ctx); err != nil {
		return HTTPObservation{}, err
	}
	if err := o.owner.CommitHTTPObservation(ctx, observation); err != nil {
		return HTTPObservation{}, err
	}
	if err := observationContextError(ctx); err != nil {
		return HTTPObservation{}, err
	}
	return observation, nil
}

// readHTTPObservationHeader bounds raw headers before net/http normalization.
// It reads no response body and is shared by exchange and persistent probes.
func readHTTPObservationHeader(conn net.Conn, started time.Time) (HTTPObservation, error) {
	var observation HTTPObservation
	reader := bufio.NewReaderSize(&io.LimitedReader{R: conn, N: maxHTTPHeaderBytes}, 1024)
	fields := 0
	for informational := 0; ; {
		// Bound raw fields before net/http normalizes duplicate Content-Length
		// or removes Transfer-Encoding. Obsolete folded fields are unsupported.
		var header strings.Builder
		for lineNumber := 0; ; lineNumber++ {
			line, lineErr := reader.ReadString('\n')
			if lineErr != nil || !strings.HasSuffix(line, "\r\n") {
				return HTTPObservation{}, errors.Join(ErrHTTPResponse, lineErr)
			}
			header.WriteString(line)
			if lineNumber > 0 {
				if line == "\r\n" {
					break
				}
				fields++
				if fields > maxHTTPHeaderFields || line[0] == ' ' || line[0] == '\t' {
					return HTTPObservation{}, ErrHTTPResponse
				}
			}
		}
		response, readErr := http.ReadResponse(bufio.NewReader(strings.NewReader(header.String())), &http.Request{Method: http.MethodGet})
		if readErr != nil {
			return HTTPObservation{}, errors.Join(ErrHTTPResponse, readErr)
		}
		// ReadResponse allocates no body worker. Do not call Body.Close here:
		// its server-style reader may drain the body. Closing our sole TCP
		// transport below releases it without reading any response body.
		if fields > maxHTTPHeaderFields || response.ProtoMajor != 1 || (response.ProtoMinor != 0 && response.ProtoMinor != 1) || response.StatusCode < 100 || response.StatusCode > 599 || response.StatusCode == http.StatusSwitchingProtocols {
			return HTTPObservation{}, ErrHTTPResponse
		}
		if response.StatusCode < 200 {
			informational++
			if informational > maxHTTPInformational {
				return HTTPObservation{}, ErrHTTPResponse
			}
			continue
		}
		observation = HTTPObservation{StatusCode: response.StatusCode, StartedAt: started, ReceivedAt: time.Now()}
		break
	}
	return observation, nil
}
