package guestsession

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

var ErrClosed = errors.New("guest session closed")
var ErrOverflow = errors.New("guest session output queue exceeded")

// Driver owns one already-retained, separately bound guest channel. New neither
// dials nor creates a VM. The caller must provide the actual VM's private stream.
// A canceled consumer read does not destroy the PTY; Resume must retain this same
// Driver, with data-channel authorization/revocation handled by the AWS owner.
type Driver struct {
	conn      net.Conn
	codec     *Codec
	ctx       context.Context
	stop      func() bool
	mu        sync.Mutex
	writeMu   sync.Mutex
	id        ID
	started   bool
	closed    bool
	err       error
	ready     chan struct{}
	done      chan struct{}
	output    chan []byte
	closeOnce sync.Once
	closeDone chan struct{}
	readDone  chan struct{}
	closeErr  error
	bound     time.Time
	expiry    *time.Timer
}

// New may return a nonnil failed Driver with an error after taking ownership.
// Retain that receipt and call WaitCleanup; an error is not proof of cleanup.
func New(ctx context.Context, conn net.Conn, expectedUUID string) (*Driver, error) {
	if ctx == nil || conn == nil || !ValidUUID(expectedUUID) {
		return nil, ErrProtocol
	}
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.After(time.Now()) || time.Until(deadline) > 15*time.Minute {
		return nil, fmt.Errorf("bounded guest session lifetime required")
	}
	d := &Driver{conn: conn, codec: NewCodec(conn), ctx: ctx, ready: make(chan struct{}), done: make(chan struct{}), output: make(chan []byte, 32), closeDone: make(chan struct{}), readDone: make(chan struct{}), bound: deadline}
	d.stop = context.AfterFunc(ctx, func() { d.fail(ctx.Err()) })
	d.expiry = time.AfterFunc(time.Until(deadline), func() { d.fail(context.DeadlineExceeded) })
	success := false
	defer func() {
		if !success {
			d.stop()
			d.expiry.Stop()
		}
	}()
	handshake := time.Now().Add(5 * time.Second)
	if deadline.Before(handshake) {
		handshake = deadline
	}
	if err := conn.SetDeadline(handshake); err != nil {
		d.fail(err)
		return d, err
	}
	f, err := d.codec.Read()
	if err != nil || f.Kind != Hello || string(f.Payload) != expectedUUID {
		if err == nil {
			err = ErrProtocol
		}
		d.fail(err)
		return d, err
	}
	if err = conn.SetDeadline(deadline); err != nil {
		d.fail(err)
		return d, err
	}
	if err = ctx.Err(); err != nil {
		d.fail(err)
		return d, err
	}
	success = true
	return d, nil
}
func (d *Driver) fail(err error) {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.err = err
		d.closed = true
		d.mu.Unlock()
		close(d.done)
		go func() { err := d.conn.Close(); d.mu.Lock(); d.closeErr = err; d.mu.Unlock(); close(d.closeDone) }()
	})
}

// Close requests revocation and waits only for bounded transport/read-loop
// cleanup. It never certifies that a guest PTY has exited; only Exit does that.
func (d *Driver) Revoke() {
	d.fail(ErrClosed)
	if d.stop != nil {
		d.stop()
	}
	if d.expiry != nil {
		d.expiry.Stop()
	}
}
func (d *Driver) Close() error {
	d.Revoke()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return d.WaitCleanup(ctx)
}
func (d *Driver) WaitCleanup(ctx context.Context) error {
	select {
	case <-d.closeDone:
	case <-ctx.Done():
		return fmt.Errorf("transport close unconfirmed: %w", ctx.Err())
	}
	d.mu.Lock()
	closeErr, started := d.closeErr, d.started
	d.mu.Unlock()
	if started {
		select {
		case <-d.readDone:
		case <-ctx.Done():
			return errors.Join(closeErr, fmt.Errorf("guest reader join unconfirmed: %w", ctx.Err()))
		}
	}
	return closeErr
}

// ExitAcknowledged is distinct from a local transport Close.
func (d *Driver) ExitAcknowledged() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed && d.err == nil
}
func (d *Driver) write(f Frame) error {
	if !time.Now().Before(d.bound) {
		d.fail(context.DeadlineExceeded)
		return context.DeadlineExceeded
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	select {
	case <-d.done:
		return ErrClosed
	default:
	}
	deadline := time.Now().Add(5 * time.Second)
	if end, _ := d.ctx.Deadline(); end.Before(deadline) {
		deadline = end
	}
	if err := d.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	if err := d.codec.Write(f); err != nil {
		d.fail(err)
		return err
	}
	return nil
}
func (d *Driver) Open(ctx context.Context, id ID, rows, cols uint16) error {
	if ctx == nil {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	size, err := Size(rows, cols)
	if err != nil || id == (ID{}) {
		return ErrProtocol
	}
	d.mu.Lock()
	if d.started || d.closed {
		d.mu.Unlock()
		return ErrClosed
	}
	d.started = true
	d.id = id
	d.mu.Unlock()
	if err = d.write(Frame{Start, id, size}); err != nil {
		close(d.readDone)
		return err
	}
	go func() { defer close(d.readDone); d.readLoop() }()
	select {
	case <-d.ready:
		return d.readyResult(ctx)
	case <-d.done:
		return d.failure()
	case <-ctx.Done():
		d.fail(ctx.Err())
		return ctx.Err()
	}
}
func (d *Driver) failure() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return d.err
	}
	return ErrClosed
}
func (d *Driver) readLoop() {
	ready := false
	total := 0
	for {
		f, err := d.codec.Read()
		if err != nil {
			d.fail(err)
			return
		}
		if f.Session != d.id {
			d.fail(ErrProtocol)
			return
		}
		if !ready {
			if f.Kind != Ready {
				d.fail(ErrProtocol)
				return
			}
			ready = true
			close(d.ready)
			continue
		}
		switch f.Kind {
		case Output:
			total += len(f.Payload)
			if total > 4<<20 {
				d.fail(ErrOverflow)
				return
			}
			select {
			case d.output <- f.Payload:
			default:
				d.fail(ErrOverflow)
				return
			}
		case Exit:
			d.fail(nil)
			return
		case Failure:
			d.fail(fmt.Errorf("guest session unavailable"))
			return
		default:
			d.fail(ErrProtocol)
			return
		}
	}
}
func (d *Driver) Input(data []byte) error {
	if len(data) == 0 || len(data) > MaxPayload {
		return ErrProtocol
	}
	select {
	case <-d.ready:
	default:
		return ErrClosed
	}
	return d.write(Frame{Input, d.id, append([]byte(nil), data...)})
}
func (d *Driver) Resize(rows, cols uint16) error {
	size, err := Size(rows, cols)
	if err != nil {
		return err
	}
	select {
	case <-d.ready:
	default:
		return ErrClosed
	}
	return d.write(Frame{Resize, d.id, size})
}
func (d *Driver) Next(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for {
		select {
		case <-d.done:
			d.mu.Lock()
			failure := d.err
			d.mu.Unlock()
			if failure != nil {
				return nil, failure
			}
			// A verified clean Exit follows all Output frames. Deliver already queued
			// bytes before EOF; protocol failures never release their buffered output.
			select {
			case b := <-d.output:
				return d.deliver(ctx, b)
			default:
				return nil, io.EOF
			}
		default:
		}
		select {
		case b := <-d.output:
			return d.deliver(ctx, b)
		case <-d.done:
			continue
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
func (d *Driver) deliver(ctx context.Context, b []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed && d.err != nil {
		return nil, d.err
	}
	return b, nil
}
func (d *Driver) Terminate(ctx context.Context) error {
	if d.ExitAcknowledged() {
		return nil
	}
	if err := d.write(Frame{Close, d.id, nil}); err != nil {
		return err
	}
	select {
	case <-d.done:
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.err
	case <-ctx.Done():
		d.fail(ctx.Err())
		return ctx.Err()
	}
}
func (d *Driver) Active() bool {
	select {
	case <-d.ready:
	default:
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.closed && d.ctx.Err() == nil && time.Now().Before(d.bound)
}

// Ready is an observed event, not permission to ignore a concurrent close.
func (d *Driver) readyResult(ctx context.Context) error {
	if !time.Now().Before(d.bound) {
		return context.DeadlineExceeded
	}
	if ctx == nil {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		if d.err != nil {
			return d.err
		}
		return ErrClosed
	}
	return nil
}
