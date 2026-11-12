package guestsession

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

const scriptedUUID = "11111111-1111-1111-1111-111111111111"

var scriptedID = ID{1}

// scriptedDriver completes the hello handshake over an in-memory pipe and
// then lets script play the guest side after the host's Start frame.
func scriptedDriver(t *testing.T, ctx context.Context, script func(c *Codec, start Frame)) *Driver {
	t.Helper()
	host, guest := net.Pipe()
	t.Cleanup(func() { _ = guest.Close() })
	go func() {
		c := NewCodec(guest)
		if c.Write(Frame{Kind: Hello, Payload: []byte(scriptedUUID)}) != nil {
			return
		}
		start, err := c.Read()
		if err != nil || start.Kind != Start {
			return
		}
		script(c, start)
	}()
	d, err := New(ctx, host, scriptedUUID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func bounded(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestDriverConstructionRequiresBoundedIdentifiedChannel(t *testing.T) {
	host, guest := net.Pipe()
	defer host.Close()
	defer guest.Close()
	ok := bounded(t, time.Second)
	long, cancel := context.WithTimeout(context.Background(), 16*time.Minute)
	defer cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for name, c := range map[string]struct {
		ctx  context.Context
		conn net.Conn
		uuid string
	}{
		"nil conn":       {ok, nil, scriptedUUID},
		"invalid uuid":   {ok, host, "not-a-uuid"},
		"no deadline":    {context.Background(), host, scriptedUUID},
		"over 15 min":    {long, host, scriptedUUID},
		"expired":        {expired, host, scriptedUUID},
		"uppercase uuid": {ok, host, strings.ToUpper("aaaaaaaa-1111-1111-1111-111111111111")},
	} {
		if d, err := New(c.ctx, c.conn, c.uuid); err == nil || d != nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestDriverOpenRefusesInvalidOrRepeatedStarts(t *testing.T) {
	ctx := bounded(t, 2*time.Second)
	d := scriptedDriver(t, ctx, func(c *Codec, start Frame) {
		_ = c.Write(Frame{Kind: Ready, Session: start.Session})
		_, _ = c.Read()
	})
	if err := d.Input([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatal("input before ready", err)
	}
	if err := d.Resize(24, 80); !errors.Is(err, ErrClosed) {
		t.Fatal("resize before ready", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	//lint:ignore SA1012 nil context is the refused input under test
	if err := d.Open(nil, scriptedID, 24, 80); !errors.Is(err, ErrClosed) { //nolint:staticcheck
		t.Fatal("nil context", err)
	}
	if err := d.Open(canceled, scriptedID, 24, 80); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled open", err)
	}
	if err := d.Open(ctx, ID{}, 24, 80); !errors.Is(err, ErrProtocol) {
		t.Fatal("zero session", err)
	}
	if err := d.Open(ctx, scriptedID, 0, 80); !errors.Is(err, ErrProtocol) {
		t.Fatal("zero rows", err)
	}
	if err := d.Open(ctx, scriptedID, 24, 80); err != nil {
		t.Fatal(err)
	}
	if err := d.Open(ctx, scriptedID, 24, 80); !errors.Is(err, ErrClosed) {
		t.Fatal("second start", err)
	}
	if _, err := d.Next(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled next", err)
	}
	if err := d.Input(nil); !errors.Is(err, ErrProtocol) {
		t.Fatal("empty input", err)
	}
	if err := d.Resize(0, 0); err == nil {
		t.Fatal("zero resize accepted")
	}
}

// Every guest misbehavior ends the session with a failure that Next reports;
// none is silently skipped or treated as a clean exit.
func TestDriverFailsClosedOnGuestProtocolViolations(t *testing.T) {
	for name, c := range map[string]struct {
		script  func(c *Codec, start Frame)
		openErr bool
		want    error
	}{
		"output before ready":   {func(c *Codec, s Frame) { _ = c.Write(Frame{Kind: Output, Session: s.Session, Payload: []byte("x")}) }, true, ErrProtocol},
		"foreign session ready": {func(c *Codec, s Frame) { _ = c.Write(Frame{Kind: Ready, Session: ID{9}}) }, true, ErrProtocol},
		"guest hangs up":        {func(c *Codec, s Frame) { _ = c.stream.(net.Conn).Close() }, true, nil},
		"foreign session output": {func(c *Codec, s Frame) {
			_ = c.Write(Frame{Kind: Ready, Session: s.Session})
			_ = c.Write(Frame{Kind: Output, Session: ID{9}, Payload: []byte("x")})
		}, false, ErrProtocol},
		"guest failure": {func(c *Codec, s Frame) {
			_ = c.Write(Frame{Kind: Ready, Session: s.Session})
			_ = c.Write(Frame{Kind: Failure, Session: s.Session, Payload: []byte("unavailable")})
		}, false, nil},
		"second ready": {func(c *Codec, s Frame) {
			_ = c.Write(Frame{Kind: Ready, Session: s.Session})
			_ = c.Write(Frame{Kind: Ready, Session: s.Session})
		}, false, ErrProtocol},
		"unread output overflow": {func(c *Codec, s Frame) {
			_ = c.Write(Frame{Kind: Ready, Session: s.Session})
			for i := 0; i < 40; i++ {
				if c.Write(Frame{Kind: Output, Session: s.Session, Payload: []byte("x")}) != nil {
					return
				}
			}
		}, false, ErrOverflow},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := bounded(t, 2*time.Second)
			d := scriptedDriver(t, ctx, c.script)
			err := d.Open(ctx, scriptedID, 24, 80)
			if c.openErr || err != nil {
				// A violation right after Ready may be judged before Open
				// observes Ready; either way the same failure is reported.
				if err == nil || (c.want != nil && !errors.Is(err, c.want)) {
					t.Fatalf("open: %v", err)
				}
				return
			}
			if c.want == ErrOverflow {
				deadline := time.Now().Add(time.Second)
				for d.Active() && time.Now().Before(deadline) {
					time.Sleep(5 * time.Millisecond)
				}
			}
			for {
				_, err = d.Next(ctx)
				if err != nil {
					break
				}
			}
			if c.want != nil && !errors.Is(err, c.want) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("next: %v", err)
			}
			if d.Active() || d.ExitAcknowledged() {
				t.Fatal("violating guest left active or acknowledged")
			}
		})
	}
}

func TestDriverOpenAndTerminateAreBoundedByCaller(t *testing.T) {
	lifetime := bounded(t, 3*time.Second)
	silent := scriptedDriver(t, lifetime, func(c *Codec, s Frame) { _, _ = c.Read() })
	short := bounded(t, 50*time.Millisecond)
	if err := silent.Open(short, scriptedID, 24, 80); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("silent guest open", err)
	}
	if silent.Active() {
		t.Fatal("abandoned open left active")
	}

	noExit := scriptedDriver(t, lifetime, func(c *Codec, s Frame) {
		_ = c.Write(Frame{Kind: Ready, Session: s.Session})
		for {
			if _, err := c.Read(); err != nil {
				return
			}
		}
	})
	if err := noExit.Open(lifetime, scriptedID, 24, 80); err != nil {
		t.Fatal(err)
	}
	short = bounded(t, 50*time.Millisecond)
	if err := noExit.Terminate(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("terminate without exit", err)
	}
	if noExit.ExitAcknowledged() {
		t.Fatal("unacknowledged exit certified")
	}
	if err := noExit.Terminate(lifetime); !errors.Is(err, ErrClosed) {
		t.Fatal("terminate after failure", err)
	}
	if err := noExit.Input([]byte("late")); !errors.Is(err, ErrClosed) {
		t.Fatal("input after failure", err)
	}
}
