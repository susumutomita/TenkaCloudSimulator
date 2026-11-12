package server

import (
	"bufio"
	"bytes"
	"context"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTerminalMemoryControlEnvelopeBeforeAuthority(t *testing.T) {
	h := terminalHandler{store: &aws.SSMSessionStore{}}
	cases := []struct {
		method, action, body string
		status               int
	}{
		{"GET", "StartSession", `{"Target":"i-own"}`, 400},
		{"POST", "StartSession", `{"Target":"i-peer"}`, 403},
		{"POST", "StartSession", `{"Target":"i-own","SessionId":"x"}`, 403},
		{"POST", "StartSession", `{"Target":"i-own","Unknown":1}`, 400},
		{"POST", "StartSession", `{"Target":"i-own","target":"i-peer"}`, 400},
		{"POST", "ResumeSession", `{"SessionId":""}`, 400},
		{"POST", "ResumeSession", `{"SessionId":"s","Target":"i-own"}`, 400},
		{"POST", "TerminateSession", `{"SessionId":"s","DocumentName":"x"}`, 400},
		{"POST", "Unknown", `{}`, 400},
		{"POST", "StartSession", `{"Target":"i-own"}`, 409},
		{"POST", "ResumeSession", `{"SessionId":"s"}`, 409},
		{"POST", "TerminateSession", `{"SessionId":"s"}`, 409},
		{"POST", "StartSession", strings.Repeat(" ", 4097), 400},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, "http://localhost/_simulator/v1/terminal-control", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "AmazonSSM."+tc.action)
		w := httptest.NewRecorder()
		h.control(w, r, terminalScope{Target: "i-own", Region: "us-east-1"})
		if w.Code != tc.status {
			t.Fatalf("%s %s expected%d got%d %s", tc.action, tc.body, tc.status, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/unknown", "/_simulator/v1/terminal-channel/", "/_simulator/v1/terminal-channel/a/b"} {
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(context.WithValue(r.Context(), terminalScopeKey{}, terminalScope{}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 400
		if path == "/unknown" {
			want = 404
		}
		if w.Code != want {
			t.Fatalf("path%s got%d", path, w.Code)
		}
	}
}
func TestTerminalMemoryControlFramesAndJSONDepth(t *testing.T) {
	masked := func(op byte, p []byte) []byte {
		raw := []byte{0x80 | op, 0x80 | byte(len(p))}
		if len(p) >= 126 {
			raw = []byte{0x80 | op, 0xfe, byte(len(p) >> 8), byte(len(p))}
		}
		raw = append(raw, 1, 2, 3, 4)
		for i, c := range p {
			raw = append(raw, c^byte(i%4+1))
		}
		return raw
	}
	conn := &terminalTestConn{}
	raw := append(masked(9, []byte("ping")), masked(10, []byte("pong"))...)
	raw = append(raw, masked(1, []byte("text"))...)
	wire := terminalWire{conn: conn, read: bufio.NewReader(bytes.NewReader(raw))}
	got, err := wire.receive()
	if err != nil || string(got) != "text" {
		t.Fatal(string(got), err)
	}
	if !bytes.Equal(conn.Bytes(), []byte{0x8a, 4, 'p', 'i', 'n', 'g'}) {
		t.Fatal("ping did not receive exact pong")
	}
	for _, data := range [][]byte{masked(8, nil), {0x81, 0xfe, 0x2e, 0xe1}, {0x89, 0xfe, 0, 126}, {0x81, 0xfe, 0}, {0x81, 0x81, 1}, {0x81, 0x81, 1, 2, 3, 4}} {
		w := terminalWire{conn: &terminalTestConn{}, read: bufio.NewReader(bytes.NewReader(data))}
		if _, err = w.receive(); err == nil {
			t.Fatal("truncated/overbound/closed frame accepted")
		}
	}

	for _, oversized := range [][]byte{masked(1, make([]byte, 12001)), append(masked(9, make([]byte, 126)), masked(1, []byte("must not read"))...)} {
		sink := &terminalTestConn{}
		w := terminalWire{conn: sink, read: bufio.NewReader(bytes.NewReader(oversized))}
		if _, err := w.receive(); err == nil || err.Error() != "frame limit" || sink.writes != 0 {
			t.Fatalf("complete oversized frame was not bounded before payload/pong: %v writes=%d", err, sink.writes)
		}
	}
	closed := terminalWire{read: bufio.NewReader(bytes.NewReader(masked(8, nil)))}
	if _, err = closed.receive(); err != io.EOF {
		t.Fatal("close is not EOF", err)
	}
	if wire.sendFrame(1, make([]byte, 12001)) == nil {
		t.Fatal("oversized output accepted")
	}
	for _, data := range []string{`{"a":[1,true,null,{"b":"x"}]}`, `{"a":[]}`, `{"a":{}}`} {
		if !terminalJSON([]byte(data)) {
			t.Fatal("valid nested JSON refused", data)
		}
	}
	for _, data := range []string{`{"a":[1,]}`, `{"a":{"b":1,"B":2}}`, `{"a":` + strings.Repeat("[", 9) + `0` + strings.Repeat("]", 9) + `}`, `{"a":`} {
		if terminalJSON([]byte(data)) {
			t.Fatal("ambiguous/malformed/deep JSON accepted", data)
		}
	}
}
