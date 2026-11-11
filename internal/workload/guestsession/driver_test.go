package guestsession

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// Pure in-memory pipes: no socket, PTY, process, or guest is created.
func TestDriverReadyNeverOverridesClosedOrCanceled(t *testing.T) {
	lifetime, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	d := &Driver{ctx: lifetime, bound: time.Now().Add(time.Minute), ready: make(chan struct{}), closed: true}
	close(d.ready)
	if d.readyResult(context.Background()) == nil || d.Active() {
		t.Fatal("closed ready accepted")
	}
	d.closed = false
	ctx, stop := context.WithCancel(context.Background())
	stop()
	if !errors.Is(d.readyResult(ctx), context.Canceled) {
		t.Fatal("caller cancellation lost")
	}
	cancel()
	if d.readyResult(context.Background()) == nil || d.Active() {
		t.Fatal("lifetime cancellation lost")
	}
}
func TestDriverMemoryHandshakeRejectsForeignVM(t *testing.T) {
	host, guest := net.Pipe()
	defer guest.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		_ = NewCodec(guest).Write(Frame{Kind: Hello, Payload: []byte("11111111-1111-1111-1111-111111111111")})
	}()
	if d, err := New(ctx, host, "22222222-2222-2222-2222-222222222222"); err == nil {
		d.Close()
		t.Fatal("foreign VM accepted")
	}
}

func TestDriverMemoryInteractiveLifecycle(t *testing.T) {
	host, guest := net.Pipe()
	defer guest.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	const uuid = "11111111-1111-1111-1111-111111111111"
	completed := make(chan error, 1)
	go func() {
		codec := NewCodec(guest)
		if err := codec.Write(Frame{Kind: Hello, Payload: []byte(uuid)}); err != nil {
			completed <- err
			return
		}
		start, err := codec.Read()
		if err != nil {
			completed <- err
			return
		}
		if start.Kind != Start {
			completed <- ErrProtocol
			return
		}
		if err = codec.Write(Frame{Kind: Ready, Session: start.Session}); err != nil {
			completed <- err
			return
		}
		input, err := codec.Read()
		if err != nil {
			completed <- err
			return
		}
		if input.Kind != Input || string(input.Payload) != "echo test\n" {
			completed <- ErrProtocol
			return
		}
		if err = codec.Write(Frame{Kind: Output, Session: start.Session, Payload: []byte("test\r\n")}); err != nil {
			completed <- err
			return
		}
		closeFrame, err := codec.Read()
		if err != nil {
			completed <- err
			return
		}
		if closeFrame.Kind != Close {
			completed <- ErrProtocol
			return
		}
		completed <- codec.Write(Frame{Kind: Exit, Session: start.Session, Payload: make([]byte, 4)})
	}()
	driver, err := New(ctx, host, uuid)
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	var id ID
	id[0] = 1
	if err = driver.Open(ctx, id, 24, 80); err != nil {
		t.Fatal(err)
	}
	if err = driver.Input([]byte("echo test\n")); err != nil {
		t.Fatal(err)
	}
	output, err := driver.Next(ctx)
	if err != nil || string(output) != "test\r\n" {
		t.Fatalf("output %q: %v", output, err)
	}
	if err = driver.Terminate(ctx); err != nil {
		t.Fatal(err)
	}
	if driver.Active() {
		t.Fatal("exited guest active")
	}
	if err = <-completed; err != nil {
		t.Fatal(err)
	}
}

func TestDriverCleanExitDrainsQueuedFinalOutput(t *testing.T) {
	ctx := context.Background()
	d := &Driver{ctx: ctx, done: make(chan struct{}), output: make(chan []byte, 1), closed: true}
	d.output <- []byte("final line")
	close(d.done)
	b, err := d.Next(ctx)
	if err != nil || string(b) != "final line" {
		t.Fatalf("lost final output %q %v", b, err)
	}
	if _, err = d.Next(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF got %v", err)
	}
	d.err = ErrProtocol
	d.output <- []byte("untrusted")
	if b, err = d.Next(ctx); err == nil || len(b) != 0 {
		t.Fatal("failed stream leaked queued bytes")
	}
}

type closeErrorConn struct {
	net.Conn
	failure error
}

func (c closeErrorConn) Close() error { _ = c.Conn.Close(); return c.failure }
func TestDriverRetainsCloseErrorAndUnknownReaderDebt(t *testing.T) {
	host, guest := net.Pipe()
	defer guest.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	failure := errors.New("close receipt failed")
	go func() {
		_ = NewCodec(guest).Write(Frame{Kind: Hello, Payload: []byte("11111111-1111-1111-1111-111111111111")})
	}()
	d, err := New(ctx, closeErrorConn{host, failure}, "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); !errors.Is(err, failure) {
		t.Fatalf("close error lost: %v", err)
	}
	if d.ExitAcknowledged() {
		t.Fatal("transport close invented guest Exit")
	}
	// A transport close is not a read-loop join. Preserve that pending debt.
	d.mu.Lock()
	d.started = true
	d.mu.Unlock()
	short, stop := context.WithTimeout(context.Background(), time.Millisecond)
	defer stop()
	if err = d.WaitCleanup(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reader debt lost: %v", err)
	}
	close(d.readDone)
	if err = d.WaitCleanup(ctx); !errors.Is(err, failure) {
		t.Fatalf("close error lost after join: %v", err)
	}
}

func TestDriverSilentDeadlineCannotRemainReady(t *testing.T) {
	d := &Driver{ctx: context.Background(), bound: time.Now().Add(-time.Nanosecond), ready: make(chan struct{})}
	close(d.ready)
	if d.Active() || !errors.Is(d.readyResult(context.Background()), context.DeadlineExceeded) {
		t.Fatal("absolute deadline ignored")
	}
}
