package guesttcp

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

type recordedDeadline struct {
	kind string
	time time.Time
}

type deadlineRecorder struct {
	net.Conn
	calls []recordedDeadline
}

func (r *deadlineRecorder) SetDeadline(t time.Time) error {
	r.calls = append(r.calls, recordedDeadline{"both", t})
	return nil
}
func (r *deadlineRecorder) SetReadDeadline(t time.Time) error {
	r.calls = append(r.calls, recordedDeadline{"read", t})
	return nil
}
func (r *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	r.calls = append(r.calls, recordedDeadline{"write", t})
	return nil
}

func TestConnectionDeadlinesUseAbsoluteOwnerExpiry(t *testing.T) {
	expires := time.Now().Add(MaxLifetime)
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	defer cancel()
	raw := &deadlineRecorder{}
	c := &connection{conn: raw, owner: &Engine{ctx: ctx}}
	for _, method := range []struct {
		kind string
		set  func(time.Time) error
	}{{"both", c.SetDeadline}, {"read", c.SetReadDeadline}, {"write", c.SetWriteDeadline}} {
		for _, deadline := range []struct {
			name        string
			input, want time.Time
		}{
			{"zero", time.Time{}, expires},
			{"past", expires.Add(-2 * MaxLifetime), expires.Add(-2 * MaxLifetime)},
			{"earlier", expires.Add(-time.Second), expires.Add(-time.Second)},
			{"equal", expires, expires},
			{"far future", expires.Add(365 * 24 * time.Hour), expires},
		} {
			t.Run(method.kind+"/"+deadline.name, func(t *testing.T) {
				if err := method.set(deadline.input); err != nil {
					t.Fatal(err)
				}
				got := raw.calls[len(raw.calls)-1]
				if got.kind != method.kind || !got.time.Equal(deadline.want) {
					t.Fatalf("deadline %+v, want %s %s", got, method.kind, deadline.want)
				}
			})
		}
	}
	// Cancellation must reject mutation even before the Close watcher has run.
	cancel()
	count := len(raw.calls)
	for _, set := range []func(time.Time) error{c.SetDeadline, c.SetReadDeadline, c.SetWriteDeadline} {
		if err := set(expires); !errors.Is(err, ErrClosed) {
			t.Fatalf("canceled owner deadline = %v", err)
		}
	}
	if len(raw.calls) != count {
		t.Fatal("canceled owner reached underlying deadline setter")
	}
}

type elapsedOwnerContext struct {
	context.Context
	expires time.Time
}

func (c elapsedOwnerContext) Deadline() (time.Time, bool) { return c.expires, true }

func TestConnectionDeadlineRejectsElapsedOwnerBeforeCancellation(t *testing.T) {
	// Simulate context timer delivery lag without relying on scheduler timing.
	ctx := elapsedOwnerContext{context.Background(), time.Now().Add(-time.Second)}
	raw := &deadlineRecorder{}
	c := &connection{conn: raw, owner: &Engine{ctx: ctx}}
	for _, set := range []func(time.Time) error{c.SetDeadline, c.SetReadDeadline, c.SetWriteDeadline} {
		if err := set(time.Time{}); !errors.Is(err, ErrClosed) {
			t.Fatalf("elapsed owner accepted deadline before cancellation: %v", err)
		}
	}
	if len(raw.calls) != 0 {
		t.Fatal("elapsed owner reached underlying deadline setter")
	}
}

type blockingDeadlineRecorder struct {
	deadlineRecorder
	entered, release chan struct{}
	closed           bool
}

func (r *blockingDeadlineRecorder) SetReadDeadline(t time.Time) error {
	close(r.entered)
	<-r.release
	return r.deadlineRecorder.SetReadDeadline(t)
}
func (r *blockingDeadlineRecorder) Close() error { r.closed = true; return nil }

func TestConnectionDeadlineMutationFinishesBeforeCleanup(t *testing.T) {
	e, err := New(config())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	raw := &blockingDeadlineRecorder{entered: make(chan struct{}), release: make(chan struct{})}
	// Only this unit test substitutes a recorder to hold the exact point where
	// the raw timer setter is in progress. Real gonet teardown is tested below.
	e.mu.Lock()
	e.conn = raw
	e.mu.Unlock()
	c := &connection{conn: raw, owner: e}
	set := make(chan error, 1)
	go func() { set <- c.SetReadDeadline(time.Now().Add(time.Hour)) }()
	<-raw.entered
	closed := make(chan struct{})
	go func() { e.Close(); close(closed) }()
	select {
	case <-closed:
		close(raw.release)
		t.Fatal("teardown overtook an admitted deadline mutation")
	case <-time.After(20 * time.Millisecond):
	}
	close(raw.release)
	if err := <-set; err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("teardown did not join deadline mutation")
	}
	if len(raw.calls) != 2 || raw.calls[0].kind != "read" || raw.calls[1].kind != "both" || !raw.calls[1].time.IsZero() || !raw.closed {
		t.Fatalf("deadline cleanup order = %+v, closed=%t", raw.calls, raw.closed)
	}
	if err := c.SetDeadline(time.Now().Add(time.Hour)); !errors.Is(err, ErrClosed) || len(raw.calls) != 2 {
		t.Fatalf("deadline recreated after cleanup: %v, %+v", err, raw.calls)
	}
}

// deadlineMemoryPeer reuses the real independent gVisor peer. It opens no host
// sockets or processes. Optional payload loss fills the real TCP send buffer
// without depending on the peer listener's inherited receive window size.
func deadlineMemoryPeer(t *testing.T, dropPayload bool) (*Engine, <-chan net.Conn) {
	t.Helper()
	e, err := New(config())
	if err != nil {
		t.Fatal(err)
	}
	s, link, listener := peer(t, config())
	ctx, cancel := context.WithTimeout(context.Background(), MaxLifetime+2*time.Second)
	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() {
		defer pumps.Done()
		for {
			frame, err := e.ReadFrame(ctx)
			if err != nil {
				return
			}
			if dropPayload {
				parsed, err := guestnet.ParseTCPFrame(frame)
				if err != nil {
					return
				}
				if parsed.PayloadLength != 0 {
					continue
				}
			}
			packet := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(frame)})
			link.InjectInbound(0, packet)
			packet.DecRef()
		}
	}()
	go func() {
		defer pumps.Done()
		for {
			packet := link.ReadContext(ctx)
			if packet == nil {
				return
			}
			view := packet.ToView()
			frame := append([]byte(nil), view.AsSlice()...)
			view.Release()
			packet.DecRef()
			if e.Inject(frame) != nil {
				return
			}
		}
	}()
	accepted := make(chan net.Conn, 1)
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	t.Cleanup(func() {
		cancel()
		e.Close()
		listener.Close()
		pumps.Wait()
		<-acceptDone
		link.Close()
		s.Destroy()
	})
	return e, accepted
}

func deadlineMemoryConnection(t *testing.T, dropPayload bool) (*Engine, *connection, net.Conn) {
	t.Helper()
	e, accepted := deadlineMemoryPeer(t, dropPayload)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := e.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case remote := <-accepted:
		t.Cleanup(func() { remote.SetDeadline(time.Time{}); remote.Close() })
		return e, conn.(*connection), remote
	case <-ctx.Done():
		t.Fatal("memory peer did not accept")
		return nil, nil, nil
	}
}

// The pinned upstream has private timer fields but no timer inspection API.
// Read only their nil state, without unsafe or changing upstream internals, to
// ensure a rejected setter is not hiding a timer leak after endpoint teardown.
func assertGonetTimers(t *testing.T, c *connection, active bool) {
	t.Helper()
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	raw, ok := c.conn.(*gonet.TCPConn)
	if !ok {
		t.Fatalf("not a real gVisor connection: %T", c.conn)
	}
	timers := reflect.ValueOf(raw).Elem().FieldByName("deadlineTimer")
	for _, name := range []string{"readTimer", "writeTimer"} {
		if field := timers.FieldByName(name); !field.IsValid() || field.IsNil() == active {
			t.Fatalf("%s timer state does not match active=%t", name, active)
		}
	}
}

func assertDeadlinesRevoked(t *testing.T, c *connection) {
	t.Helper()
	for _, set := range []func(time.Time) error{c.SetDeadline, c.SetReadDeadline, c.SetWriteDeadline} {
		for _, deadline := range []time.Time{{}, time.Now().Add(-time.Second), time.Now().Add(365 * 24 * time.Hour)} {
			if err := set(deadline); !errors.Is(err, ErrClosed) {
				t.Fatalf("deadline accepted after revocation: %v", err)
			}
		}
	}
	assertGonetTimers(t, c, false)
}

func TestConnectionDeadlineTimersClearedOnClose(t *testing.T) {
	for _, viaConnection := range []bool{false, true} {
		t.Run(map[bool]string{false: "engine", true: "connection"}[viaConnection], func(t *testing.T) {
			e, conn, _ := deadlineMemoryConnection(t, false)
			if err := conn.SetDeadline(time.Now().Add(365 * 24 * time.Hour)); err != nil {
				t.Fatal(err)
			}
			assertGonetTimers(t, conn, true)
			if viaConnection {
				if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				e.Close()
			}
			assertDeadlinesRevoked(t, conn)
		})
	}
}

func TestConnectionDeadlineIOAndReset(t *testing.T) {
	_, conn, remote := deadlineMemoryConnection(t, false)
	if conn.LocalAddr() == nil || conn.RemoteAddr() == nil {
		t.Fatal("connection addresses lost")
	}
	read := make(chan error, 1)
	go func() { _, err := conn.Read(make([]byte, 1)); read <- err }()
	if err := conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-read:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("read deadline = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("read deadline did not interrupt I/O")
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 2)
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "ok" {
		t.Fatalf("read after deadline reset = %q, %v", got, err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if n, err := conn.Write([]byte("ok")); n != 0 || err == nil {
		t.Fatalf("past write deadline = %d, %v", n, err)
	}
	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(remote, got); err != nil || string(got) != "ok" {
		t.Fatalf("write after deadline reset = %q, %v", got, err)
	}
}

func TestConnectionCloseInterruptsBlockedIO(t *testing.T) {
	e, conn, _ := deadlineMemoryConnection(t, true)
	if err := conn.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	read, write := make(chan error, 1), make(chan error, 1)
	go func() { _, err := conn.Read(make([]byte, 1)); read <- err }()
	go func() { _, err := conn.Write(make([]byte, 128<<10)); write <- err }()
	for _, done := range []<-chan error{read, write} {
		select {
		case err := <-done:
			t.Fatalf("I/O did not block: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	e.Close()
	for _, done := range []<-chan error{read, write} {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("blocked I/O succeeded after close")
			}
		case <-time.After(time.Second):
			t.Fatal("blocked I/O prevented close")
		}
	}
	assertDeadlinesRevoked(t, conn)
}

func TestConnectionDeadlineMutationRacesClose(t *testing.T) {
	e, conn, _ := deadlineMemoryConnection(t, false)
	var setters sync.WaitGroup
	ready := make(chan struct{}, 3)
	start := make(chan struct{})
	for _, set := range []func(time.Time) error{conn.SetDeadline, conn.SetReadDeadline, conn.SetWriteDeadline} {
		setters.Add(1)
		go func(set func(time.Time) error) {
			defer setters.Done()
			if err := set(time.Now().Add(time.Hour)); err != nil {
				t.Errorf("initial deadline: %v", err)
			}
			ready <- struct{}{}
			<-start
			for i := 0; i < 128; i++ {
				deadline := time.Now().Add(time.Hour)
				if i%2 == 0 {
					deadline = time.Time{}
				}
				if err := set(deadline); err != nil && !errors.Is(err, ErrClosed) {
					t.Errorf("deadline race: %v", err)
				}
			}
		}(set)
	}
	for i := 0; i < 3; i++ {
		<-ready
	}
	close(start)
	e.Close()
	setters.Wait()
	assertDeadlinesRevoked(t, conn)
}

func TestConnectionDeadlineDialCloseRace(t *testing.T) {
	for i := 0; i < 16; i++ {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			e, accepted := deadlineMemoryPeer(t, false)
			done := make(chan net.Conn, 1)
			go func() { conn, _ := e.Dial(context.Background()); done <- conn }()
			// The real peer handshake must progress before Close can race
			// with successful endpoint registration and returning the wrapper.
			select {
			case remote := <-accepted:
				t.Cleanup(func() { remote.Close() })
			case <-time.After(time.Second):
				t.Fatal("peer handshake did not complete")
			}
			e.Close()
			select {
			case conn := <-done:
				if conn != nil {
					assertDeadlinesRevoked(t, conn.(*connection))
				}
			case <-time.After(time.Second):
				t.Fatal("dial and close deadlocked")
			}
		})
	}
}

func TestConnectionDeadlineOwnerExpiry(t *testing.T) {
	e, conn, _ := deadlineMemoryConnection(t, false)
	if err := conn.SetDeadline(time.Now().Add(365 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	read := make(chan error, 1)
	go func() { _, err := conn.Read(make([]byte, 1)); read <- err }()
	select {
	case <-e.ctx.Done():
	case <-time.After(MaxLifetime + time.Second):
		t.Fatal("caller deadline extended owner lifetime")
	}
	e.Close()
	select {
	case err := <-read:
		if err == nil {
			t.Fatal("read succeeded after owner expiry")
		}
	case <-time.After(time.Second):
		t.Fatal("read outlived owner expiry")
	}
	assertDeadlinesRevoked(t, conn)
}
