package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestsession"
)

const flowGuestUUID = "0123abcd-4567-89ab-cdef-0123456789ab"

// flowGuest is a memory guest relay: Hello, Ready on Start, echo Input,
// acknowledge Resize, Exit on Close. It never runs a process.
type flowBackend struct {
	opened atomic.Int32
	closed chan struct{}
}

func (b *flowBackend) ResolveSessionTarget(_ context.Context, region, id string) (aws.SSMSessionTarget, error) {
	return aws.SSMSessionTarget{Region: region, StackID: "stack-flow", InstanceID: id}, nil
}
func (b *flowBackend) OpenSessionChannel(ctx context.Context, _ aws.SSMSessionTarget) (*guestsession.Driver, error) {
	b.opened.Add(1)
	host, guest := net.Pipe()
	go func() {
		defer guest.Close()
		codec := guestsession.NewCodec(guest)
		if codec.Write(guestsession.Frame{Kind: guestsession.Hello, Payload: []byte(flowGuestUUID)}) != nil {
			return
		}
		for {
			f, err := codec.Read()
			if err != nil {
				return
			}
			switch f.Kind {
			case guestsession.Start:
				err = codec.Write(guestsession.Frame{Kind: guestsession.Ready, Session: f.Session})
			case guestsession.Input:
				err = codec.Write(guestsession.Frame{Kind: guestsession.Output, Session: f.Session, Payload: append([]byte("echo:"), f.Payload...)})
			case guestsession.Resize:
				err = codec.Write(guestsession.Frame{Kind: guestsession.Output, Session: f.Session, Payload: []byte("resized")})
			case guestsession.Close:
				_ = codec.Write(guestsession.Frame{Kind: guestsession.Exit, Session: f.Session, Payload: []byte{0, 0, 0, 0}})
				if b.closed != nil {
					select {
					case b.closed <- struct{}{}:
					default:
					}
				}
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return guestsession.New(ctx, host, flowGuestUUID)
}

type terminalFlow struct {
	t       *testing.T
	server  *httptest.Server
	cookie  *http.Cookie
	backend *flowBackend
}

func newTerminalFlow(t *testing.T) *terminalFlow {
	t.Helper()
	a, c, _ := terminalFixture(t)
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	backend := &flowBackend{closed: make(chan struct{}, 1)}
	sessions, err := aws.NewSSMSessionStore(db.Database(), backend, "ws://127.0.0.1:4566/_simulator/v1/terminal-channel")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sessions.Shutdown(ctx)
	})
	a.next = terminalHandler{store: sessions}
	w := terminalRedeem(a, terminalCode(t, a, c))
	cookies := w.Result().Cookies()
	if w.Code != 303 || len(cookies) != 1 {
		t.Fatal("terminal handoff not redeemed", w.Code)
	}
	server := httptest.NewServer(a)
	t.Cleanup(server.Close)
	return &terminalFlow{t: t, server: server, cookie: cookies[0], backend: backend}
}

func (f *terminalFlow) control(action, body string) (int, []byte) {
	f.t.Helper()
	r, _ := http.NewRequest("POST", f.server.URL+"/_simulator/v1/terminal-control", strings.NewReader(body))
	r.Host = "127.0.0.1:4566"
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "AmazonSSM."+action)
	r.Header.Set("Origin", "http://127.0.0.1:4566")
	r.AddCookie(f.cookie)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// wsConn is a minimal masked-text WebSocket client for the terminal subset.
type wsConn struct {
	conn net.Conn
	read *bufio.Reader
}

func (f *terminalFlow) dial(id string, header map[string]string) (*wsConn, string) {
	f.t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.server.URL, "http://"))
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	h := map[string]string{"Host": "127.0.0.1:4566", "Origin": "http://127.0.0.1:4566", "Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": base64.StdEncoding.EncodeToString(key), "Cookie": f.cookie.Name + "=" + f.cookie.Value}
	for k, v := range header {
		if v == "" {
			delete(h, k)
		} else {
			h[k] = v
		}
	}
	req := "GET /_simulator/v1/terminal-channel/" + id + " HTTP/1.1\r\n"
	for k, v := range h {
		req += k + ": " + v + "\r\n"
	}
	if _, err = conn.Write([]byte(req + "\r\n")); err != nil {
		f.t.Fatal(err)
	}
	read := bufio.NewReader(conn)
	status, err := read.ReadString('\n')
	if err != nil {
		f.t.Fatal(err)
	}
	for {
		line, err := read.ReadString('\n')
		if err != nil || line == "\r\n" {
			break
		}
	}
	return &wsConn{conn: conn, read: read}, strings.TrimSpace(status)
}

func (c *wsConn) send(op byte, payload []byte) error {
	header := []byte{0x80 | op}
	switch {
	case len(payload) < 126:
		header = append(header, 0x80|byte(len(payload)))
	default:
		header = append(header, 0x80|126, byte(len(payload)>>8), byte(len(payload)))
	}
	mask := []byte{1, 2, 3, 4}
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	_, err := c.conn.Write(append(append(header, mask...), masked...))
	return err
}
func (c *wsConn) sendJSON(v any) error {
	raw, _ := json.Marshal(v)
	return c.send(1, raw)
}
func (c *wsConn) next() (map[string]string, error) {
	var h [2]byte
	if _, err := io.ReadFull(c.read, h[:]); err != nil {
		return nil, err
	}
	size := int(h[1] & 127)
	if size == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(c.read, ext[:]); err != nil {
			return nil, err
		}
		size = int(binary.BigEndian.Uint16(ext[:]))
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(c.read, payload); err != nil {
		return nil, err
	}
	var out map[string]string
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *wsConn) expectOutput(t *testing.T, want string) {
	t.Helper()
	m, err := c.next()
	if err != nil || m["type"] != "output" {
		t.Fatalf("output frame %v %v", m, err)
	}
	data, _ := base64.StdEncoding.DecodeString(m["data"])
	if string(data) != want {
		t.Fatalf("output %q, want %q", data, want)
	}
}

func startFlowSession(t *testing.T, f *terminalFlow) aws.SSMSessionResponse {
	t.Helper()
	code, raw := f.control("StartSession", `{"Target":"i-12345678"}`)
	if code != 200 {
		t.Fatalf("StartSession %d %s", code, raw)
	}
	var started aws.SSMSessionResponse
	if json.Unmarshal(raw, &started) != nil || started.SessionID == "" || len(started.TokenValue) != 64 || !strings.HasSuffix(started.StreamURL, "/"+started.SessionID) {
		t.Fatalf("session response %s", raw)
	}
	return started
}

func TestTerminalParticipantSessionRoundTrip(t *testing.T) {
	f := newTerminalFlow(t)
	started := startFlowSession(t, f)
	if code, _ := f.control("StartSession", `{"Target":"i-12345678"}`); code != 409 {
		t.Fatal("second session on a busy target accepted", code)
	}
	ws, status := f.dial(started.SessionID, nil)
	if !strings.Contains(status, "101") {
		t.Fatal("upgrade refused:", status)
	}
	if err := ws.sendJSON(map[string]string{"type": "open", "token": started.TokenValue}); err != nil {
		t.Fatal(err)
	}
	if m, err := ws.next(); err != nil || m["type"] != "ready" {
		t.Fatalf("ready %v %v", m, err)
	}
	if err := ws.sendJSON(map[string]string{"type": "input", "data": base64.StdEncoding.EncodeToString([]byte("systemctl start nginx\n"))}); err != nil {
		t.Fatal(err)
	}
	ws.expectOutput(t, "echo:systemctl start nginx\n")
	if err := ws.sendJSON(map[string]any{"type": "resize", "rows": 40, "cols": 120}); err != nil {
		t.Fatal(err)
	}
	ws.expectOutput(t, "resized")
	// A one-use token cannot open a second channel.
	ws2, _ := f.dial(started.SessionID, nil)
	_ = ws2.sendJSON(map[string]string{"type": "open", "token": started.TokenValue})
	if _, err := ws2.next(); err == nil {
		t.Fatal("replayed channel token accepted")
	}
	// Invalid client frames close the channel instead of being interpreted.
	if err := ws.sendJSON(map[string]any{"type": "resize", "rows": 0, "cols": 80}); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.next(); err == nil {
		t.Fatal("invalid resize kept the channel open")
	}
	// Resume rotates a fresh token for the same live guest session.
	code, raw := f.control("ResumeSession", `{"SessionId":"`+started.SessionID+`"}`)
	if code != 200 {
		t.Fatalf("ResumeSession %d %s", code, raw)
	}
	var resumed aws.SSMSessionResponse
	if json.Unmarshal(raw, &resumed) != nil || resumed.TokenValue == started.TokenValue || resumed.SessionID != started.SessionID {
		t.Fatalf("resume response %s", raw)
	}
	ws3, _ := f.dial(started.SessionID, nil)
	_ = ws3.sendJSON(map[string]string{"type": "open", "token": resumed.TokenValue})
	if m, err := ws3.next(); err != nil || m["type"] != "ready" {
		t.Fatalf("resumed channel %v %v", m, err)
	}
	_ = ws3.sendJSON(map[string]string{"type": "input", "data": base64.StdEncoding.EncodeToString([]byte("id\n"))})
	ws3.expectOutput(t, "echo:id\n")
	_ = ws3.send(1, []byte(`{"type":"unknown"}`))
	if _, err := ws3.next(); err == nil {
		t.Fatal("unknown frame type kept the channel open")
	}
	code, raw = f.control("TerminateSession", `{"SessionId":"`+started.SessionID+`"}`)
	if code != 200 || !strings.Contains(string(raw), started.SessionID) {
		t.Fatalf("TerminateSession %d %s", code, raw)
	}
	select {
	case <-f.backend.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("guest never received Close")
	}
	if f.backend.opened.Load() != 1 {
		t.Fatal("guest channel acquired more than once")
	}
}

func TestTerminalChannelRejectsMalformedUpgradesAndOpen(t *testing.T) {
	f := newTerminalFlow(t)
	started := startFlowSession(t, f)
	for name, h := range map[string]map[string]string{
		"subprotocol": {"Sec-WebSocket-Protocol": "chat"},
		"no origin":   {"Origin": ""},
		"bad version": {"Sec-WebSocket-Version": "8"},
		"bad key":     {"Sec-WebSocket-Key": "c2hvcnQ="},
		"no upgrade":  {"Upgrade": "h2c"},
	} {
		if _, status := f.dial(started.SessionID, h); strings.Contains(status, "101") {
			t.Fatalf("%s: malformed upgrade accepted: %s", name, status)
		}
	}
	for name, first := range map[string]string{
		"wrong type":  `{"Type":"input","Token":"` + started.TokenValue + `"}`,
		"short token": `{"Type":"open","Token":"x"}`,
		"bad token":   `{"Type":"open","Token":"` + strings.Repeat("0", 64) + `"}`,
		"extra field": `{"Type":"open","Token":"` + started.TokenValue + `","X":1}`,
		"not json":    `open`,
	} {
		ws, status := f.dial(started.SessionID, nil)
		if !strings.Contains(status, "101") {
			t.Fatal(name, status)
		}
		_ = ws.send(1, []byte(first))
		if _, err := ws.next(); err == nil {
			t.Fatalf("%s: invalid open accepted", name)
		}
	}
	// The untouched token still opens exactly once after those refusals.
	ws, _ := f.dial(started.SessionID, nil)
	_ = ws.sendJSON(map[string]string{"type": "open", "token": started.TokenValue})
	if m, err := ws.next(); err != nil || m["type"] != "ready" {
		t.Fatalf("valid token refused after invalid attempts: %v %v", m, err)
	}
	_ = ws.send(1, []byte(`{"type":"input","data":"%%%"}`))
	if _, err := ws.next(); err == nil {
		t.Fatal("non-base64 input kept the channel open")
	}
}
