package server

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

type terminalTestConn struct {
	bytes.Buffer
	deadlineErr error
	writes      int
	closed      bool
}

func (c *terminalTestConn) Read(p []byte) (int, error)       { return c.Buffer.Read(p) }
func (c *terminalTestConn) Write(p []byte) (int, error)      { c.writes++; return c.Buffer.Write(p) }
func (c *terminalTestConn) Close() error                     { c.closed = true; return nil }
func (c *terminalTestConn) LocalAddr() net.Addr              { return nil }
func (c *terminalTestConn) RemoteAddr() net.Addr             { return nil }
func (c *terminalTestConn) SetDeadline(time.Time) error      { return c.deadlineErr }
func (c *terminalTestConn) SetReadDeadline(time.Time) error  { return c.deadlineErr }
func (c *terminalTestConn) SetWriteDeadline(time.Time) error { return c.deadlineErr }
func TestTerminalStrictJSON(t *testing.T) {
	for _, raw := range []string{"", "   ", `[]`, `{"Type":"x","type":"y"}`, `{"type":"x","t\u0079pe":"y"}`, "{\"x\":\"\xff\"}", `{} {}`} {
		if terminalJSON([]byte(raw)) {
			t.Fatalf("accepted invalid %q", raw)
		}
	}
	if !terminalJSON([]byte(`{"type":"resize","rows":24,"cols":80}`)) {
		t.Fatal("valid rejected")
	}
}
func TestTerminalWireDeadlineFailure(t *testing.T) {
	want := errors.New("deadline unavailable")
	c := &terminalTestConn{deadlineErr: want}
	w := terminalWire{conn: c}
	if !errors.Is(w.send(map[string]string{"type": "ready"}), want) || c.writes != 0 {
		t.Fatal("write attempted without deadline")
	}
}
func TestTerminalWireMaskAndBounds(t *testing.T) {
	for _, raw := range [][]byte{{0x81, 0x01, 'x'}, {0x01, 0x80, 0, 0, 0, 0}, {0x82, 0x80, 0, 0, 0, 0}, {0x81, 0xff}} {
		w := terminalWire{read: bufio.NewReader(bytes.NewReader(raw))}
		if _, e := w.receive(); e == nil {
			t.Fatal("invalid accepted")
		}
	}
	w := terminalWire{read: bufio.NewReader(bytes.NewReader([]byte{0x81, 0x81, 1, 2, 3, 4, 'x' ^ 1}))}
	data, e := w.receive()
	if e != nil || string(data) != "x" {
		t.Fatal(data, e)
	}
}

type terminalHijack struct {
	c      *terminalTestConn
	header http.Header
}

func (h *terminalHijack) Header() http.Header         { return h.header }
func (h *terminalHijack) Write(p []byte) (int, error) { return len(p), nil }
func (h *terminalHijack) WriteHeader(int)             {}
func (h *terminalHijack) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.c, bufio.NewReadWriter(bufio.NewReader(bytes.NewReader(nil)), bufio.NewWriter(h.c)), nil
}
func TestTerminalHandshakeDeadlineBeforeFlush(t *testing.T) {
	c := &terminalTestConn{deadlineErr: io.ErrClosedPipe}
	w := &terminalHijack{c: c, header: make(http.Header)}
	r, _ := http.NewRequest("GET", "http://localhost/_simulator/v1/terminal-channel/s", nil)
	r.Header.Set("Origin", "http://localhost")
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	terminalHandler{}.channel(w, r, "s")
	if c.writes != 0 || !c.closed {
		t.Fatal("unbounded handshake write")
	}
}

func TestTerminalWireLengthBoundaries(t *testing.T) {
	for _, n := range []int{125, 126, 127, 128} {
		raw := []byte{0x81, 0x80 | byte(n)}
		if n >= 126 {
			raw = []byte{0x81, 0xfe, byte(n >> 8), byte(n)}
		}
		raw = append(raw, 1, 2, 3, 4)
		for i := 0; i < n; i++ {
			raw = append(raw, 'x'^byte(i%4+1))
		}
		w := terminalWire{read: bufio.NewReader(bytes.NewReader(raw))}
		data, e := w.receive()
		if e != nil || !bytes.Equal(data, bytes.Repeat([]byte{'x'}, n)) {
			t.Fatalf("length %d: %v", n, e)
		}
	}
	w := terminalWire{read: bufio.NewReader(bytes.NewReader([]byte{0x81, 0xfe, 0, 125}))}
	if _, e := w.receive(); e == nil {
		t.Fatal("noncanonical extended length accepted")
	}
}
