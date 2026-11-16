package server

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// frame reads one raw server frame so a test can act as a browser: it answers
// pings itself and tells data frames from control frames.
func (c *wsConn) frame(deadline time.Time) (byte, []byte, error) {
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return 0, nil, err
	}
	var h [2]byte
	if _, err := io.ReadFull(c.read, h[:]); err != nil {
		return 0, nil, err
	}
	size := int(h[1] & 127)
	if size == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(c.read, ext[:]); err != nil {
			return 0, nil, err
		}
		size = int(binary.BigEndian.Uint16(ext[:]))
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(c.read, payload); err != nil {
		return 0, nil, err
	}
	return h[0] & 15, payload, nil
}

// A real guest showed that a participant who watched output without typing for
// 60 seconds lost the channel, and that output arriving after the drop then
// made the session unrecoverable. Answering pings must keep a watcher attached;
// a peer that stops answering must still be dropped, leaving a resumable session.
func TestTerminalChannelKeepsWatchingParticipantAndDropsSilentPeer(t *testing.T) {
	const idle, ping = 600 * time.Millisecond, 100 * time.Millisecond
	f := newTerminalFlowWithLiveness(t, idle, ping)
	started := startFlowSession(t, f)
	ws, status := f.dial(started.SessionID, nil)
	if status != "HTTP/1.1 101 Switching Protocols" {
		t.Fatal("upgrade refused:", status)
	}
	if err := ws.sendJSON(map[string]string{"type": "open", "token": started.TokenValue}); err != nil {
		t.Fatal(err)
	}
	if op, raw, err := ws.frame(time.Now().Add(5 * time.Second)); err != nil || op != 1 || string(raw) != `{"type":"ready"}` {
		t.Fatalf("ready frame %d %q %v", op, raw, err)
	}

	// Watch for three idle limits without sending a single text frame.
	watchUntil := time.Now().Add(3 * idle)
	answered := 0
	for time.Now().Before(watchUntil) {
		op, payload, err := ws.frame(watchUntil)
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			break
		}
		if err != nil {
			t.Fatalf("channel closed on a watching participant after %d answered pings: %v", answered, err)
		}
		if op != 9 {
			t.Fatalf("unexpected frame %d %q while idle", op, payload)
		}
		if err = ws.send(10, payload); err != nil {
			t.Fatal(err)
		}
		answered++
	}
	if answered < 3 {
		t.Fatalf("server probed the peer %d times in three idle limits", answered)
	}

	// The same guest shell is still attached.
	if err := ws.sendJSON(map[string]string{"type": "input", "data": base64.StdEncoding.EncodeToString([]byte("id\n"))}); err != nil {
		t.Fatal(err)
	}
	echoed := false
	for !echoed {
		op, payload, err := ws.frame(time.Now().Add(5 * time.Second))
		if err != nil {
			t.Fatalf("no output after the idle period: %v", err)
		}
		if op == 9 {
			if err = ws.send(10, payload); err != nil {
				t.Fatal(err)
			}
			continue
		}
		var m map[string]string
		if op != 1 || json.Unmarshal(payload, &m) != nil || m["type"] != "output" {
			t.Fatalf("unexpected frame %d %q", op, payload)
		}
		data, _ := base64.StdEncoding.DecodeString(m["data"])
		echoed = string(data) == "echo:id\n"
	}

	// A peer that no longer answers pings is dropped within a bounded time.
	silent := time.Now()
	for {
		if _, _, err := ws.frame(silent.Add(10 * idle)); err != nil {
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				t.Fatal("silent peer was never dropped")
			}
			break
		}
	}
	if elapsed := time.Since(silent); elapsed < idle/2 || elapsed > 5*idle {
		t.Fatalf("silent peer dropped after %v, idle limit %v", elapsed, idle)
	}

	// The drop detaches the consumer only: the retained session resumes.
	code, raw := f.control("ResumeSession", `{"SessionId":"`+started.SessionID+`"}`)
	if code != 200 {
		t.Fatalf("ResumeSession after a liveness drop %d %s", code, raw)
	}
	if f.backend.opened.Load() != 1 {
		t.Fatal("guest channel acquired more than once")
	}
}
