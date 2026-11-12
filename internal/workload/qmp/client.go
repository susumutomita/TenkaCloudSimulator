// Package qmp implements bounded lifecycle control over a trusted, private QMP
// stream. It neither launches VMs nor establishes transport or process ownership.
package qmp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	DefaultRPCTimeout = 2 * time.Second
	MaxRPCTimeout     = 5 * time.Second
	MaxFrameBytes     = 64 << 10
	MaxEvents         = 32 // Per complete operation, including Stop's status verification.
)

var (
	ErrClosed    = errors.New("QMP client is closed")
	ErrProtocol  = errors.New("invalid QMP response")
	ErrLimit     = errors.New("QMP response limit exceeded")
	ErrIdentity  = errors.New("QMP VM identity mismatch")
	ErrNotPaused = errors.New("QMP did not confirm paused VM")
)

// Transport must be already authorized, private and exclusively owned. Close
// and SetDeadline must be concurrency safe and promptly unblock Read/Write;
// deadlines must bound both. New takes ownership even on initialization failure.
type Transport interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
}
type Options struct{ RPCTimeout time.Duration }

// Status is a momentary QEMU observation, not proof of durable containment or
// host process exit. Paused CPUs do not imply stopped host device IO.
type Status struct {
	Running bool
	State   string
}

// RemoteError is an explicit correlated command rejection. Text is untrusted.
type RemoteError struct{ Class, Description string }

func (e *RemoteError) Error() string {
	return "QMP rejected request: " + e.Class + ": " + e.Description
}

// ControlUncertainError means stop/quit may have taken effect but its required
// outcome was not verified. Never automatically replay, reconnect or release a
// lease based on this error. Closing the stream does not contain the VM.
type ControlUncertainError struct {
	Command string
	Err     error
}

func (e *ControlUncertainError) Error() string {
	return "QMP " + e.Command + " outcome uncertain: " + e.Err.Error()
}
func (e *ControlUncertainError) Unwrap() error { return e.Err }

// Client must not be copied. All operations are serialized including handshake.
// Protocol/IO failure poisons the stream permanently; there is no reconnect.
type Client struct {
	transport Transport
	reader    *bufio.Reader
	timeout   time.Duration
	gate      chan struct{}
	done      chan struct{}
	once      sync.Once
	closeErr  error
	id        uint64
}

func New(ctx context.Context, transport Transport, expectedUUID string, options Options) (*Client, error) {
	if transport == nil {
		return nil, errors.New("QMP transport is required")
	}
	c := &Client{transport: transport, reader: bufio.NewReader(transport), timeout: options.RPCTimeout, gate: make(chan struct{}, 1), done: make(chan struct{})}
	if c.timeout == 0 {
		c.timeout = DefaultRPCTimeout
	}
	if c.timeout < 0 || c.timeout > MaxRPCTimeout || !validUUID(expectedUUID) {
		_ = c.Close()
		return nil, errors.New("invalid QMP timeout or expected UUID")
	}
	op, release, err := c.acquire(ctx)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	defer release()
	frame, err := c.frame()
	if err == nil {
		err = parseGreeting(frame)
	}
	events := 0
	if err == nil {
		var data json.RawMessage
		data, _, err = c.exchange(op, "qmp_capabilities", &events)
		if err == nil {
			err = emptyResult(data)
		}
	}
	if err == nil {
		var data json.RawMessage
		data, _, err = c.exchange(op, "query-uuid", &events)
		if err == nil {
			var f map[string]json.RawMessage
			f, err = object(data, "UUID")
			var uuid string
			if err == nil {
				err = field(f, "UUID", &uuid)
			}
			if err == nil && (!validUUID(uuid) || !strings.EqualFold(uuid, expectedUUID)) {
				err = ErrIdentity
			}
		}
	}
	if err == nil {
		err = op.Err()
	}
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Close closes only the stream. It does not stop QEMU or guest commands.
func (c *Client) Close() error {
	c.once.Do(func() { close(c.done); c.closeErr = c.transport.Close() })
	return c.closeErr
}
func (c *Client) acquire(ctx context.Context) (context.Context, func(), error) {
	op, cancel := context.WithTimeout(ctx, c.timeout)
	select {
	case <-op.Done():
		cancel()
		return nil, nil, op.Err()
	case <-c.done:
		cancel()
		return nil, nil, ErrClosed
	case c.gate <- struct{}{}:
	}
	fail := func(err error) (context.Context, func(), error) { <-c.gate; cancel(); return nil, nil, err }
	if err := op.Err(); err != nil {
		return fail(err)
	}
	select {
	case <-c.done:
		return fail(ErrClosed)
	default:
	}
	deadline, _ := op.Deadline()
	if err := c.transport.SetDeadline(deadline); err != nil {
		_ = c.Close()
		return fail(err)
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(op, func() { _ = c.Close(); close(finished) })
	return op, func() {
		if !stop() {
			<-finished
		}
		cancel()
		<-c.gate
	}, nil
}
func (c *Client) QueryStatus(ctx context.Context) (Status, error) {
	op, release, err := c.acquire(ctx)
	if err != nil {
		return Status{}, err
	}
	defer release()
	events := 0
	return c.status(op, &events)
}
func (c *Client) status(ctx context.Context, events *int) (Status, error) {
	data, _, err := c.exchange(ctx, "query-status", events)
	if err != nil {
		return Status{}, err
	}
	status, err := parseStatus(data)
	if err != nil {
		_ = c.Close()
	}
	return status, err
}

// Stop sends stop once, then queries actual status within the same time/event
// budget. A STOP event or empty acknowledgement alone is never sufficient.
func (c *Client) Stop(ctx context.Context) (Status, error) {
	op, release, err := c.acquire(ctx)
	if err != nil {
		return Status{}, err
	}
	defer release()
	events := 0
	if err = c.control(op, "stop", &events); err != nil {
		return Status{}, err
	}
	status, err := c.status(op, &events)
	if err == nil && (status.Running || status.State != "paused") {
		err = ErrNotPaused
	}
	if err != nil {
		_ = c.Close()
		return Status{}, &ControlUncertainError{Command: "stop", Err: err}
	}
	return status, nil
}

// Quit reports only an explicit QMP acknowledgement. Even nil is NOT process
// exit or Released evidence: the lifecycle owner must independently reap/verify
// its exact owned process. EOF without acknowledgement is uncertain, not success.
// A successful Quit closes this client and cannot be replayed through it.
func (c *Client) Quit(ctx context.Context) error {
	op, release, err := c.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	events := 0
	err = c.control(op, "quit", &events)
	if err == nil {
		_ = c.Close()
	}
	return err
}
func (c *Client) control(ctx context.Context, command string, events *int) error {
	data, sent, err := c.exchange(ctx, command, events)
	if err == nil {
		err = emptyResult(data)
		if err != nil {
			_ = c.Close()
		}
	}
	var remote *RemoteError
	if err != nil && sent && !errors.As(err, &remote) {
		return &ControlUncertainError{Command: command, Err: err}
	}
	return err
}
func (c *Client) exchange(ctx context.Context, command string, events *int) (data json.RawMessage, sent bool, err error) {
	defer func() {
		if err != nil {
			var remote *RemoteError
			if !errors.As(err, &remote) {
				_ = c.Close()
				if ctx.Err() != nil {
					err = ctx.Err()
				}
			}
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	c.id++
	if c.id == 0 {
		err = ErrLimit
		return
	}
	request, _ := json.Marshal(struct {
		Execute string `json:"execute"`
		ID      uint64 `json:"id"`
	}{command, c.id})
	request = append(request, '\n')
	for len(request) > 0 {
		var n int
		n, err = c.transport.Write(request)
		if n > 0 {
			sent = true
		}
		if n < 0 || n > len(request) {
			err = ErrProtocol
			return
		}
		request = request[n:]
		if err != nil {
			return
		}
		if n == 0 {
			err = io.ErrNoProgress
			return
		}
	}
	for {
		var frame []byte
		frame, err = c.frame()
		if err != nil {
			return
		}
		var f map[string]json.RawMessage
		f, err = object(frame, "return", "error", "id", "event", "data", "timestamp")
		if err != nil {
			return
		}
		if _, ok := f["event"]; ok {
			if err = parseEvent(f); err != nil {
				return
			}
			*events++
			if *events > MaxEvents {
				err = ErrLimit
				return
			}
			continue
		}
		if len(f) != 2 {
			err = ErrProtocol
			return
		}
		var id uint64
		if field(f, "id", &id) != nil || id != c.id {
			err = ErrProtocol
			return
		}
		if data = f["return"]; data != nil {
			if string(data) == "null" {
				err = ErrProtocol
			}
			return
		}
		var e map[string]json.RawMessage
		e, err = object(f["error"], "class", "desc")
		if err != nil {
			return
		}
		remote := &RemoteError{}
		if field(e, "class", &remote.Class) != nil || field(e, "desc", &remote.Description) != nil || remote.Class == "" {
			err = ErrProtocol
			return
		}
		err = remote
		return
	}
}
func (c *Client) frame() ([]byte, error) {
	var frame []byte
	for {
		part, err := c.reader.ReadSlice('\n')
		if len(frame)+len(part) > MaxFrameBytes+1 {
			return nil, ErrLimit
		}
		frame = append(frame, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err = validateJSON(frame); err != nil {
			return nil, err
		}
		return frame, nil
	}
}
func validUUID(s string) bool {
	if len(s) != 36 || s == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// Activate starts this already UUID-verified paused VM once. It is not a
// readiness or guest-agent result. Unknown results poison this client; no retry.
func (c *Client) Activate(ctx context.Context) (Status, error) {
	op, release, err := c.acquire(ctx)
	if err != nil {
		return Status{}, err
	}
	defer release()
	events := 0
	status, err := c.status(op, &events)
	if err != nil {
		return Status{}, err
	}
	if status.Running || (status.State != "paused" && status.State != "prelaunch") {
		return Status{}, ErrNotPaused
	}
	if err = c.control(op, "cont", &events); err != nil {
		return Status{}, err
	}
	status, err = c.status(op, &events)
	if err == nil && (!status.Running || status.State != "running") {
		err = ErrProtocol
	}
	if err != nil {
		_ = c.Close()
		return Status{}, &ControlUncertainError{Command: "cont", Err: err}
	}
	return status, nil
}
