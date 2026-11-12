package server

import (
	"bufio"
	"context"
	"crypto/sha1" // RFC 6455 handshake, not credential hashing.
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
)

// terminalHandler accepts only the private Access-created terminal context.
// Its store enforces the original participant credential and target again.
type terminalHandler struct{ store *aws.SSMSessionStore }

func (h terminalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	scope, ok := r.Context().Value(terminalScopeKey{}).(terminalScope)
	if !ok || h.store == nil {
		http.Error(w, "Participant terminal unavailable", 403)
		return
	}
	if r.URL.Path == "/_simulator/v1/terminal-control" {
		h.control(w, r, scope)
		return
	}
	prefix := "/_simulator/v1/terminal-channel/"
	if !strings.HasPrefix(r.URL.Path, prefix) || r.Method != "GET" {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, prefix)
	if id == "" || len(id) > 256 || strings.ContainsAny(id, "/?#") {
		http.Error(w, "Invalid session", 400)
		return
	}
	h.channel(w, r, id)
}
func (h terminalHandler) control(w http.ResponseWriter, r *http.Request, scope terminalScope) {
	if r.Method != "POST" || r.Header.Get("Content-Type") != "application/x-amz-json-1.1" || len(r.Header.Values("X-Amz-Target")) != 1 {
		http.Error(w, "Invalid terminal request", 400)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || !terminalJSON(raw) {
		http.Error(w, "Invalid terminal request", 400)
		return
	}
	var input struct{ Target, DocumentName, SessionId string }
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil {
		http.Error(w, "Invalid terminal request", 400)
		return
	}
	var value any
	switch r.Header.Get("X-Amz-Target") {
	case "AmazonSSM.StartSession":
		if input.Target != scope.Target || input.SessionId != "" {
			http.Error(w, "Terminal target mismatch", 403)
			return
		}
		value, err = h.store.Start(r, scope.Region, input.Target, input.DocumentName)
	case "AmazonSSM.ResumeSession":
		if input.Target != "" || input.DocumentName != "" || input.SessionId == "" {
			http.Error(w, "Invalid terminal request", 400)
			return
		}
		value, err = h.store.Resume(r, scope.Region, input.SessionId)
	case "AmazonSSM.TerminateSession":
		if input.Target != "" || input.DocumentName != "" || input.SessionId == "" {
			http.Error(w, "Invalid terminal request", 400)
			return
		}
		err = h.store.Terminate(r, scope.Region, input.SessionId)
		value = map[string]string{"SessionId": input.SessionId}
	default:
		http.Error(w, "Unsupported terminal action", 400)
		return
	}
	if err != nil {
		http.Error(w, "Terminal operation refused or outcome unknown", 409)
		return
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	_ = json.NewEncoder(w).Encode(value)
}

// Single-message, masked text subset. Fragmentation and extensions are rejected,
// not silently interpreted as the AWS ssmmessages protocol.
type terminalWire struct {
	conn net.Conn
	read *bufio.Reader
	mu   sync.Mutex
}

func (t *terminalWire) receive() ([]byte, error) {
	for {
		var header [2]byte
		if _, e := io.ReadFull(t.read, header[:]); e != nil {
			return nil, e
		}
		op := header[0] & 15
		if header[0]&0xf0 != 0x80 || header[1]&0x80 == 0 {
			return nil, errors.New("invalid terminal frame")
		}
		marker := header[1] & 127
		if marker == 127 {
			return nil, errors.New("64-bit frame lengths unsupported")
		}
		size := uint64(marker)
		if size == 126 {
			var b [2]byte
			if _, e := io.ReadFull(t.read, b[:]); e != nil {
				return nil, e
			}
			size = uint64(binary.BigEndian.Uint16(b[:]))
			if size < 126 {
				return nil, errors.New("noncanonical frame")
			}
		}
		if size > 12000 || (op >= 8 && size > 125) {
			return nil, errors.New("frame limit")
		}
		var mask [4]byte
		if _, e := io.ReadFull(t.read, mask[:]); e != nil {
			return nil, e
		}
		data := make([]byte, int(size))
		if _, e := io.ReadFull(t.read, data); e != nil {
			return nil, e
		}
		for i := range data {
			data[i] ^= mask[i%4]
		}
		switch op {
		case 1:
			return data, nil
		case 8:
			return nil, io.EOF
		case 9:
			if e := t.sendFrame(10, data); e != nil {
				return nil, e
			}
		case 10:
		default:
			return nil, errors.New("unsupported frame")
		}
	}
}
func (t *terminalWire) sendFrame(op byte, data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(data) > 12000 {
		return errors.New("output limit")
	}
	if err := t.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	header := []byte{0x80 | op, byte(len(data))}
	if len(data) >= 126 {
		header = []byte{0x80 | op, 126, byte(len(data) >> 8), byte(len(data))}
	}
	payload := append(header, data...)
	for len(payload) > 0 {
		n, e := t.conn.Write(payload)
		if e != nil {
			return e
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		payload = payload[n:]
	}
	return nil
}
func (t *terminalWire) send(value any) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	return t.sendFrame(1, raw)
}
func (h terminalHandler) channel(w http.ResponseWriter, r *http.Request, id string) {
	for _, name := range []string{"Origin", "Connection", "Upgrade", "Sec-WebSocket-Key", "Sec-WebSocket-Version"} {
		if len(r.Header.Values(name)) != 1 {
			http.Error(w, "Ambiguous terminal upgrade", 400)
			return
		}
	}
	if r.Header.Get("Sec-WebSocket-Protocol") != "" {
		http.Error(w, "Unsupported terminal subprotocol", 400)
		return
	}

	key, err := base64.StdEncoding.DecodeString(r.Header.Get("Sec-WebSocket-Key"))
	connection := false
	for _, v := range strings.Split(r.Header.Get("Connection"), ",") {
		connection = connection || strings.EqualFold(strings.TrimSpace(v), "upgrade")
	}
	if !connection || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Version") != "13" || err != nil || len(key) != 16 || r.Header.Get("Origin") == "" {
		http.Error(w, "Unsupported terminal upgrade", 400)
		return
	}
	hijack, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Terminal upgrade unavailable", 500)
		return
	}
	conn, rw, err := hijack.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return
	}
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if err != nil || rw.Flush() != nil {
		return
	}
	wire := terminalWire{conn: conn, read: rw.Reader}
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return
	}
	raw, err := wire.receive()
	if err != nil || !terminalJSON(raw) {
		return
	}
	var first struct{ Type, Token string }
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&first) != nil || first.Type != "open" || len(first.Token) < 16 || len(first.Token) > 4096 {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	channel, err := h.store.OpenDataChannel(r.WithContext(ctx), id, first.Token)
	first.Token = ""
	if err != nil {
		return
	}
	defer channel.Close()
	if wire.send(map[string]string{"type": "ready"}) != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			data, e := channel.Next()
			if e != nil || len(data) > 8192 {
				cancel()
				_ = conn.Close()
				return
			}
			if e = wire.send(map[string]string{"type": "output", "data": base64.StdEncoding.EncodeToString(data)}); e != nil {
				cancel()
				_ = conn.Close()
				return
			}
		}
	}()
	defer func() {
		cancel()
		_ = channel.Close()
		_ = conn.Close()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C: /* Retained store ownership is not reported as joined. */
		}
	}()
	for {
		if err := conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
			return
		}
		raw, err = wire.receive()
		if err != nil || !terminalJSON(raw) {
			return
		}
		var frame struct {
			Type       string
			Data       *string
			Rows, Cols *uint16
		}
		d = json.NewDecoder(strings.NewReader(string(raw)))
		d.DisallowUnknownFields()
		if d.Decode(&frame) != nil {
			return
		}
		switch frame.Type {
		case "input":
			if frame.Data == nil || frame.Rows != nil || frame.Cols != nil {
				return
			}
			data, e := base64.StdEncoding.Strict().DecodeString(*frame.Data)
			if e != nil || len(data) > 8192 {
				return
			}
			if channel.Input(data) != nil {
				return
			}
		case "resize":
			if frame.Data != nil || frame.Rows == nil || frame.Cols == nil || *frame.Rows < 1 || *frame.Rows > 300 || *frame.Cols < 1 || *frame.Cols > 500 {
				return
			}
			if channel.Resize(*frame.Rows, *frame.Cols) != nil {
				return
			}
		default:
			return
		}
	}
}
