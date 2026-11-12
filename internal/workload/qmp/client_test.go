package qmp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

const testUUID = "12345678-1234-5678-9abc-123456789abc"
const greeting = `{"QMP":{"version":{"qemu":{"major":9,"minor":2,"micro":0},"package":""},"capabilities":[]}}`

type peer struct {
	t *testing.T
	c net.Conn
	r *bufio.Reader
}

func (p peer) send(s string) {
	p.t.Helper()
	if _, err := io.WriteString(p.c, s+"\n"); err != nil {
		p.t.Errorf("peer write: %v", err)
	}
}
func (p peer) request(command string) uint64 {
	p.t.Helper()
	line, err := p.r.ReadBytes('\n')
	if err != nil {
		p.t.Errorf("peer read: %v", err)
		return 0
	}
	var req struct {
		Execute string
		ID      uint64
	}
	if json.Unmarshal(line, &req) != nil || req.Execute != command {
		p.t.Errorf("request %s, want %s", line, command)
	}
	return req.ID
}
func (p peer) reply(id uint64, result string) {
	p.send(fmt.Sprintf(`{"return":%s,"id":%d}`, result, id))
}
func (p peer) handshake() {
	p.send(greeting)
	p.reply(p.request("qmp_capabilities"), `{}`)
	p.reply(p.request("query-uuid"), `{"UUID":"`+testUUID+`"}`)
}
func fixture(t *testing.T, fn func(peer)) net.Conn {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); defer b.Close(); fn(peer{t, b, bufio.NewReader(b)}) }()
	t.Cleanup(func() { a.Close(); b.Close(); <-done })
	return a
}
func client(t *testing.T, fn func(peer)) *Client {
	t.Helper()
	c, err := New(context.Background(), fixture(t, func(p peer) { p.handshake(); fn(p) }), testUUID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func TestLifecycle(t *testing.T) {
	c := client(t, func(p peer) {
		p.reply(p.request("query-status"), `{"running":true,"status":"running","singlestep":false}`)
		id := p.request("stop")
		p.send(`{"event":"STOP","timestamp":{"seconds":1,"microseconds":2},"data":{}}`)
		p.reply(id, `{}`)
		p.reply(p.request("query-status"), `{"running":false,"status":"paused"}`)
		p.reply(p.request("quit"), `{}`)
	})
	s, err := c.QueryStatus(context.Background())
	if err != nil || !s.Running {
		t.Fatalf("%+v %v", s, err)
	}
	s, err = c.Stop(context.Background())
	if err != nil || s.State != "paused" {
		t.Fatalf("%+v %v", s, err)
	}
	if err = c.Quit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = c.QueryStatus(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
func TestStopRequiresObservedPause(t *testing.T) {
	for _, response := range []string{`{"running":true,"status":"running"}`, `{"running":false,"status":"shutdown"}`, `{"running":true,"status":"paused"}`} {
		t.Run(response, func(t *testing.T) {
			c := client(t, func(p peer) { p.reply(p.request("stop"), `{}`); p.reply(p.request("query-status"), response) })
			_, err := c.Stop(context.Background())
			var uncertain *ControlUncertainError
			if !errors.As(err, &uncertain) {
				t.Fatal(err)
			}
			if _, err = c.QueryStatus(context.Background()); !errors.Is(err, ErrClosed) {
				t.Fatal(err)
			}
		})
	}
}
func TestQuitAmbiguity(t *testing.T) {
	for _, reply := range []string{"", `{"return":{},"id":999}`, `{"return":{"unexpected":true},"id":3}`, `{"return":{},"id":3,"id":3}`, `{"error":{"class":"X","desc":"bad"},"return":{},"id":3}`} {
		t.Run(reply, func(t *testing.T) {
			c := client(t, func(p peer) {
				p.request("quit")
				if reply != "" {
					p.send(reply)
				}
			})
			err := c.Quit(context.Background())
			var uncertain *ControlUncertainError
			if !errors.As(err, &uncertain) {
				t.Fatal(err)
			}
		})
	}
}
func TestRemoteRejection(t *testing.T) {
	c := client(t, func(p peer) {
		id := p.request("stop")
		p.send(fmt.Sprintf(`{"error":{"class":"GenericError","desc":"rejected"},"id":%d}`, id))
		p.reply(p.request("query-status"), `{"running":true,"status":"running"}`)
	})
	_, err := c.Stop(context.Background())
	var remote *RemoteError
	var uncertain *ControlUncertainError
	if !errors.As(err, &remote) || errors.As(err, &uncertain) {
		t.Fatal(err)
	}
	if _, err = c.QueryStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestHandshakeIdentity(t *testing.T) {
	for _, uuid := range []string{"00000000-0000-0000-0000-000000000000", "22345678-1234-5678-9abc-123456789abc", "bad"} {
		t.Run(uuid, func(t *testing.T) {
			transport := fixture(t, func(p peer) {
				p.send(greeting)
				p.reply(p.request("qmp_capabilities"), `{}`)
				p.reply(p.request("query-uuid"), `{"UUID":"`+uuid+`"}`)
			})
			_, err := New(context.Background(), transport, testUUID, Options{})
			if !errors.Is(err, ErrIdentity) {
				t.Fatal(err)
			}
		})
	}
}
func TestEventLimit(t *testing.T) {
	c := client(t, func(p peer) {
		p.request("query-status")
		for i := 0; i <= MaxEvents; i++ {
			p.send(`{"event":"STOP"}`)
		}
	})
	_, err := c.QueryStatus(context.Background())
	if !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
}
func TestFrameLimit(t *testing.T) {
	c := client(t, func(p peer) {
		p.request("query-status")
		_, _ = io.WriteString(p.c, strings.Repeat("x", MaxFrameBytes+2))
	})
	_, err := c.QueryStatus(context.Background())
	if !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
}
func TestCancellation(t *testing.T) {
	read := make(chan struct{})
	c := client(t, func(p peer) { p.request("quit"); close(read); _, _ = p.r.ReadByte() })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- c.Quit(ctx) }()
	<-read
	cancel()
	err := <-done
	var uncertain *ControlUncertainError
	if !errors.As(err, &uncertain) || !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestDeadlineAndQueue(t *testing.T) {
	read := make(chan struct{})
	c := client(t, func(p peer) { p.request("query-status"); close(read); _, _ = p.r.ReadByte() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error)
	go func() { _, err := c.QueryStatus(ctx); done <- err }()
	<-read
	queued, qcancel := context.WithCancel(context.Background())
	qcancel()
	if err := c.Quit(queued); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("expected deadline")
	}
}
func TestConcurrentSerialization(t *testing.T) {
	const n = 10
	c := client(t, func(p peer) {
		for i := 0; i < n; i++ {
			p.reply(p.request("query-status"), `{"running":true,"status":"running"}`)
		}
	})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Go(func() {
			if _, err := c.QueryStatus(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}
func TestInvalidOptions(t *testing.T) {
	if _, err := New(context.Background(), nil, testUUID, Options{}); err == nil {
		t.Fatal("nil")
	}
	for _, opt := range []Options{{RPCTimeout: -1}, {RPCTimeout: MaxRPCTimeout + 1}} {
		a, b := net.Pipe()
		defer b.Close()
		if _, err := New(context.Background(), a, testUUID, opt); err == nil {
			t.Fatal("options")
		}
	}
	a, b := net.Pipe()
	defer b.Close()
	if _, err := New(context.Background(), a, "bad", Options{}); err == nil {
		t.Fatal("uuid")
	}
}
func TestMalformedProtocol(t *testing.T) {
	for _, s := range []string{`null`, `[]`, `{"id":1,"id":2}`, `{"a":{"b":1,"b":2}}`, `{} {}`, "\xff", strings.Repeat("[", 34) + strings.Repeat("]", 34)} {
		if err := validateJSON([]byte(s)); err == nil && s != "null" && s != "[]" {
			t.Errorf("accepted %s", s)
		}
	}
	for _, s := range []string{`{}`, `null`, `{"QMP":null}`, `{"QMP":{"version":{},"capabilities":[]}}`} {
		if parseGreeting([]byte(s)) == nil {
			t.Error(s)
		}
	}
	for _, s := range []string{`{}`, `null`, `{"running":null,"status":"paused"}`, `{"running":false,"status":"unknown"}`, `{"running":false,"status":"paused","singlestep":null}`} {
		if _, err := parseStatus([]byte(s)); err == nil {
			t.Error(s)
		}
	}
	for _, s := range []string{`{"event":""}`, `{"event":"STOP","id":1}`, `{"event":"STOP","data":null}`, `{"event":"STOP","timestamp":{"seconds":-1,"microseconds":0}}`} {
		var f map[string]json.RawMessage
		json.Unmarshal([]byte(s), &f)
		if parseEvent(f) == nil {
			t.Error(s)
		}
	}
}

// Faults exercise bounded IO without network or filesystem listeners.
type faultTransport struct {
	net.Conn
	deadlineErr error
	writeMode   int
}

func (f *faultTransport) SetDeadline(d time.Time) error {
	if f.deadlineErr != nil {
		return f.deadlineErr
	}
	return f.Conn.SetDeadline(d)
}
func (f *faultTransport) Write(p []byte) (int, error) {
	switch f.writeMode {
	case 1:
		return 0, nil
	case 2:
		return 1, io.ErrUnexpectedEOF
	case 3:
		return len(p) + 1, nil
	}
	return f.Conn.Write(p)
}
func TestTransportFaults(t *testing.T) {
	for mode := 1; mode <= 3; mode++ {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			transport := &faultTransport{Conn: fixture(t, func(p peer) { p.handshake(); _, _ = p.r.ReadByte() })}
			c, err := New(context.Background(), transport, testUUID, Options{})
			if err != nil {
				t.Fatal(err)
			}
			transport.writeMode = mode
			err = c.Quit(context.Background())
			if err == nil {
				t.Fatal("fault accepted")
			}
			var uncertain *ControlUncertainError
			if errors.As(err, &uncertain) != (mode != 1) {
				t.Fatal(err)
			}
		})
	}
	a, b := net.Pipe()
	defer b.Close()
	_, err := New(context.Background(), &faultTransport{Conn: a, deadlineErr: io.ErrClosedPipe}, testUUID, Options{})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}
func TestMalformedHandshakeAndResponses(t *testing.T) {
	for _, g := range []string{`{}`, `{"QMP":{"version":{"qemu":{"major":-1,"minor":0,"micro":0},"package":""},"capabilities":[]}}`, `{"QMP":{"version":{"qemu":{},"package":null},"capabilities":[]}}`, `{"QMP":{"version":{},"capabilities":null}}`} {
		t.Run(g, func(t *testing.T) {
			transport := fixture(t, func(p peer) { p.send(g) })
			if _, err := New(context.Background(), transport, testUUID, Options{}); err == nil {
				t.Fatal("greeting accepted")
			}
		})
	}
	for _, reply := range []string{`{"return":null,"id":3}`, `{"id":3}`, `{"error":{"class":"","desc":"bad"},"id":3}`, `{"error":null,"id":3}`, `{"event":null}`, `{"return":{},"id":3,"unknown":1}`, `[1]`, `{invalid`} {
		t.Run(reply, func(t *testing.T) {
			c := client(t, func(p peer) { p.request("query-status"); p.send(reply) })
			if _, err := c.QueryStatus(context.Background()); err == nil {
				t.Fatal("response accepted")
			}
		})
	}
}
func TestCanceledNewAndConcurrentClose(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(ctx, a, testUUID, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	read := make(chan struct{})
	c := client(t, func(p peer) { p.request("stop"); close(read); _, _ = p.r.ReadByte() })
	done := make(chan error)
	go func() { _, err := c.Stop(context.Background()); done <- err }()
	<-read
	c.Close()
	var uncertain *ControlUncertainError
	if err := <-done; !errors.As(err, &uncertain) {
		t.Fatal(err)
	}
}

func TestCanonicalNICActivationMemoryOnly(t *testing.T) {
	c := client(t, func(p peer) {
		p.reply(p.request("query-status"), `{"running":false,"status":"prelaunch"}`)
		p.reply(p.request("cont"), `{}`)
		p.reply(p.request("query-status"), `{"running":true,"status":"running"}`)
	})
	status, err := c.Activate(context.Background())
	if err != nil || !status.Running {
		t.Fatal(status, err)
	}
}
func TestCanonicalNICActivationUncertainMemoryOnly(t *testing.T) {
	c := client(t, func(p peer) {
		p.reply(p.request("query-status"), `{"running":false,"status":"paused"}`)
		p.reply(p.request("cont"), `{}`)
		p.reply(p.request("query-status"), `{"running":false,"status":"paused"}`)
	})
	_, err := c.Activate(context.Background())
	var uncertain *ControlUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatal("unconfirmed activation accepted", err)
	}
	if _, err = c.QueryStatus(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatal("uncertain activation not poisoned", err)
	}
}
