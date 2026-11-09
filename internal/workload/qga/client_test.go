package qga

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testRequest struct {
	Execute   string          `json:"execute"`
	Arguments json.RawMessage `json:"arguments"`
	ID        int64           `json:"id"`
}

type testPeer struct {
	conn   net.Conn
	reader *bufio.Reader
}

func (p *testPeer) receive(name string) (testRequest, error) {
	line, err := p.reader.ReadBytes('\n')
	if err != nil {
		return testRequest{}, err
	}
	var req testRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return req, err
	}
	if req.Execute != name || req.ID <= 0 {
		return req, fmt.Errorf("unexpected request: %s", line)
	}
	return req, nil
}

func (p *testPeer) reply(req testRequest, result any) error {
	data, err := json.Marshal(map[string]any{"return": result, "id": req.ID})
	if err != nil {
		return err
	}
	_, err = p.conn.Write(append(data, '\n'))
	return err
}

func (p *testPeer) syncRequest() (testRequest, int64, error) {
	b, err := p.reader.ReadByte()
	if err != nil || b != 0xff {
		return testRequest{}, 0, fmt.Errorf("missing request reset sentinel: %x, %v", b, err)
	}
	req, err := p.receive("guest-sync-delimited")
	if err != nil {
		return req, 0, err
	}
	var args struct {
		ID int64 `json:"id"`
	}
	if err = json.Unmarshal(req.Arguments, &args); err != nil {
		return req, 0, err
	}
	return req, args.ID, nil
}

func (p *testPeer) syncReply(req testRequest, nonce int64) error {
	if _, err := p.conn.Write([]byte{0xff}); err != nil {
		return err
	}
	return p.reply(req, nonce)
}

// net.Pipe is pure in-memory IO: no sockets, files, processes, VMs or network.
func testTransport(t *testing.T, script func(*testPeer) error) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	finished := make(chan error, 1)
	go func() {
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(10 * time.Second))
		finished <- script(&testPeer{conn: server, reader: bufio.NewReader(server)})
	}()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		select {
		case err := <-finished:
			if err != nil {
				t.Errorf("in-memory peer: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("in-memory peer did not stop")
		}
	})
	return client
}

func testClient(t *testing.T, script func(*testPeer) error, options Options) *Client {
	t.Helper()
	conn := testTransport(t, func(p *testPeer) error {
		req, nonce, err := p.syncRequest()
		if err != nil {
			return err
		}
		if err = p.syncReply(req, nonce); err != nil {
			return err
		}
		return script(p)
	})
	c, err := New(context.Background(), conn, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func mustStart(t *testing.T, c *Client) Handle {
	t.Helper()
	h, err := c.StartCommand(context.Background(), Command{Path: "/bin/true"})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func expectUncertain(t *testing.T, err error) {
	t.Helper()
	var uncertain *StartUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("expected uncertain start, got %v", err)
	}
	if !strings.Contains(err.Error(), "may have started") {
		t.Fatal(err)
	}
}

func TestCommandLifecycleAndReusedPID(t *testing.T) {
	c := testClient(t, func(p *testPeer) error {
		for run := range 2 {
			req, err := p.receive("guest-exec")
			if err != nil {
				return err
			}
			var args commandArguments
			if err = json.Unmarshal(req.Arguments, &args); err != nil {
				return err
			}
			if args.Path != "/bin/echo" || len(args.Args) != 2 || args.Args[0] != "hello" || args.Args[1] != "" || args.Input != "AAH/" || !args.Capture {
				return fmt.Errorf("wrong exec arguments: %+v", args)
			}
			if err = p.reply(req, map[string]int{"pid": 101}); err != nil {
				return err
			}
			req, err = p.receive("guest-exec-status")
			if err != nil {
				return err
			}
			if string(req.Arguments) != `{"pid":101}` {
				return fmt.Errorf("wrong PID: %s", req.Arguments)
			}
			if err = p.reply(req, map[string]bool{"exited": false}); err != nil {
				return err
			}
			req, err = p.receive("guest-exec-status")
			if err != nil {
				return err
			}
			status := map[string]any{"exited": true, "out-data": "aGVsbG8K", "err-data": "AP8="}
			if run == 0 {
				status["exitcode"] = 0
			} else {
				status["signal"] = 15
			}
			if err = p.reply(req, status); err != nil {
				return err
			}
		}
		return nil
	}, Options{})
	for run := range 2 {
		h, err := c.StartCommand(context.Background(), Command{Path: "/bin/echo", Args: []string{"hello", ""}, Stdin: []byte{0, 1, 255}})
		if err != nil {
			t.Fatal(err)
		}
		copyHandle := h
		status, err := c.CommandStatus(context.Background(), h)
		if err != nil || status.Exited {
			t.Fatalf("running: %+v, %v", status, err)
		}
		status, err = c.CommandStatus(context.Background(), h)
		if err != nil || !status.Exited || string(status.Stdout) != "hello\n" || !bytes.Equal(status.Stderr, []byte{0, 255}) {
			t.Fatalf("exited: %+v, %v", status, err)
		}
		if run == 0 && (status.ExitCode == nil || *status.ExitCode != 0 || status.Signal != nil) {
			t.Fatal(status)
		}
		if run == 1 && (status.Signal == nil || *status.Signal != 15 || status.ExitCode != nil) {
			t.Fatal(status)
		}
		if _, err = c.CommandStatus(context.Background(), copyHandle); !errors.Is(err, ErrInvalidHandle) {
			t.Fatalf("retired handle: %v", err)
		}
	}
}

func TestHandleIsolationAndDuplicatePID(t *testing.T) {
	c := testClient(t, func(p *testPeer) error {
		for range 2 {
			req, err := p.receive("guest-exec")
			if err != nil {
				return err
			}
			if err = p.reply(req, map[string]int{"pid": 44}); err != nil {
				return err
			}
		}
		return nil
	}, Options{})
	h := mustStart(t, c)
	other := testClient(t, func(p *testPeer) error { _, _ = io.Copy(io.Discard, p.reader); return nil }, Options{})
	for _, invalid := range []Handle{{}, h} {
		if _, err := other.CommandStatus(context.Background(), invalid); !errors.Is(err, ErrInvalidHandle) {
			t.Fatalf("cross-client/zero handle: %v", err)
		}
	}
	_, err := c.StartCommand(context.Background(), Command{Path: "/bin/true"})
	expectUncertain(t, err)
	if !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
	if _, err = c.CommandStatus(context.Background(), h); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestCommandAdmissionDoesNotSend(t *testing.T) {
	c := testClient(t, func(p *testPeer) error {
		req, err := p.receive("guest-exec")
		if err != nil {
			return err
		}
		return p.reply(req, map[string]int{"pid": 1})
	}, Options{})
	bad := []Command{
		{}, {Path: strings.Repeat("x", MaxPathBytes+1)}, {Path: "x\x00"}, {Path: string([]byte{255})},
		{Path: "x", Args: make([]string, MaxArgs+1)}, {Path: "x", Args: []string{strings.Repeat("y", MaxArgumentBytes)}},
		{Path: "x", Args: []string{"a\x00"}}, {Path: "x", Stdin: make([]byte, MaxStdinBytes+1)},
	}
	for _, command := range bad {
		if _, err := c.StartCommand(context.Background(), command); !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("bad command accepted: %v", err)
		}
	}
	// Valid raw byte bounds can still exceed the serialized request limit after
	// JSON escaping and base64 encoding. This must also send no request.
	_, err := c.StartCommand(context.Background(), Command{Path: "x", Args: []string{strings.Repeat("\x01", MaxArgumentBytes-1)}, Stdin: make([]byte, MaxStdinBytes)})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("escaped request: %v", err)
	}
	mustStart(t, c)
}

func TestCapacityAndRetirement(t *testing.T) {
	c := testClient(t, func(p *testPeer) error {
		for i := range MaxCommands {
			req, err := p.receive("guest-exec")
			if err != nil {
				return err
			}
			if err = p.reply(req, map[string]int{"pid": i + 1}); err != nil {
				return err
			}
		}
		req, err := p.receive("guest-exec-status")
		if err != nil {
			return err
		}
		if err = p.reply(req, map[string]any{"exited": true, "exitcode": 0}); err != nil {
			return err
		}
		req, err = p.receive("guest-exec")
		if err != nil {
			return err
		}
		return p.reply(req, map[string]int{"pid": 1})
	}, Options{})
	var first Handle
	for i := range MaxCommands {
		h := mustStart(t, c)
		if i == 0 {
			first = h
		}
	}
	if _, err := c.StartCommand(context.Background(), Command{Path: "x"}); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if _, err := c.CommandStatus(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	mustStart(t, c)
	if _, err := c.CommandStatus(context.Background(), first); !errors.Is(err, ErrInvalidHandle) {
		t.Fatal(err)
	}
}

func TestExplicitRemoteErrors(t *testing.T) {
	c := testClient(t, func(p *testPeer) error {
		req, err := p.receive("guest-exec")
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(p.conn, "{\"id\":%d,\"error\":{\"class\":\"GenericError\",\"desc\":\"not found\"}}\n", req.ID); err != nil {
			return err
		}
		req, err = p.receive("guest-exec")
		if err != nil {
			return err
		}
		if err = p.reply(req, map[string]int{"pid": 1}); err != nil {
			return err
		}
		req, err = p.receive("guest-exec-status")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(p.conn, "{\"id\":%d,\"error\":{\"class\":\"GenericError\",\"desc\":\"unknown PID\"}}\n", req.ID)
		return err
	}, Options{})
	_, err := c.StartCommand(context.Background(), Command{Path: "x"})
	var remote *RemoteError
	var uncertain *StartUncertainError
	if !errors.As(err, &remote) || errors.As(err, &uncertain) || remote.Class != "GenericError" || !strings.Contains(err.Error(), "not found") {
		t.Fatal(err)
	}
	h := mustStart(t, c)
	if _, err = c.CommandStatus(context.Background(), h); !errors.As(err, &remote) {
		t.Fatal(err)
	}
	if _, err = c.CommandStatus(context.Background(), h); !errors.Is(err, ErrInvalidHandle) {
		t.Fatal(err)
	}
}

func TestBadStartResponsesPermanentlyClose(t *testing.T) {
	cases := map[string]string{
		"stale":          `{"id":1,"return":{"pid":1}}`,
		"future":         `{"id":3,"return":{"pid":1}}`,
		"missing-id":     `{"return":{"pid":1}}`,
		"string-id":      `{"id":"2","return":{"pid":1}}`,
		"duplicate-id":   `{"id":1,"id":2,"return":{"pid":1}}`,
		"duplicate-pid":  `{"id":2,"return":{"pid":1,"pid":2}}`,
		"both":           `{"id":2,"return":{"pid":1},"error":{"class":"x","desc":"y"}}`,
		"null":           `{"id":2,"return":null}`,
		"missing":        `{"id":2}`,
		"wrong-pid":      `{"id":2,"return":{"pid":0}}`,
		"string-pid":     `{"id":2,"return":{"pid":"1"}}`,
		"extra-pid":      `{"id":2,"return":{"pid":1,"extra":true}}`,
		"bad-error":      `{"id":2,"error":{"class":"x"}}`,
		"malformed":      `{`,
		"trailing":       `{"id":2,"return":{"pid":1}} {}`,
		"extra-envelope": `{"id":2,"return":{"pid":1},"event":"x"}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			c := testClient(t, func(p *testPeer) error {
				if _, err := p.receive("guest-exec"); err != nil {
					return err
				}
				_, err := fmt.Fprintln(p.conn, line)
				return err
			}, Options{})
			_, err := c.StartCommand(context.Background(), Command{Path: "x"})
			expectUncertain(t, err)
			if !errors.Is(err, ErrProtocol) {
				t.Fatal(err)
			}
			if _, err = c.StartCommand(context.Background(), Command{Path: "x"}); !errors.Is(err, ErrClosed) {
				t.Fatal(err)
			}
		})
	}
}

func TestOutputFailuresAndHandleRetirement(t *testing.T) {
	cases := map[string]struct {
		status   map[string]any
		want     error
		reusable bool
	}{
		"stdout-truncated":    {map[string]any{"exited": true, "exitcode": 0, "out-truncated": true, "out-data": "YQ=="}, ErrOutputTruncated, true},
		"stderr-truncated":    {map[string]any{"exited": true, "signal": 9, "err-truncated": true}, ErrOutputTruncated, true},
		"base64":              {map[string]any{"exited": true, "exitcode": 0, "out-data": "?"}, ErrProtocol, false},
		"base64-newline":      {map[string]any{"exited": true, "exitcode": 0, "out-data": "YQ==\n"}, ErrProtocol, false},
		"base64-noncanonical": {map[string]any{"exited": true, "exitcode": 0, "out-data": "YR=="}, ErrProtocol, false},
		"combined-limit":      {map[string]any{"exited": true, "exitcode": 0, "out-data": base64.StdEncoding.EncodeToString(make([]byte, MaxOutputBytes)), "err-data": "YQ=="}, ErrLimit, false},
		"pending-output":      {map[string]any{"exited": false, "out-data": "YQ=="}, ErrProtocol, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := testClient(t, func(p *testPeer) error {
				req, err := p.receive("guest-exec")
				if err != nil {
					return err
				}
				if err = p.reply(req, map[string]int{"pid": 7}); err != nil {
					return err
				}
				req, err = p.receive("guest-exec-status")
				if err != nil {
					return err
				}
				if err = p.reply(req, tc.status); err != nil {
					return err
				}
				if tc.reusable {
					req, err = p.receive("guest-exec")
					if err != nil {
						return err
					}
					return p.reply(req, map[string]int{"pid": 7})
				}
				return nil
			}, Options{})
			h := mustStart(t, c)
			status, err := c.CommandStatus(context.Background(), h)
			if !errors.Is(err, tc.want) || status.Exited || status.Stdout != nil || status.Stderr != nil {
				t.Fatalf("leaked partial result: %+v %v", status, err)
			}
			if tc.reusable {
				if _, err = c.CommandStatus(context.Background(), h); !errors.Is(err, ErrInvalidHandle) {
					t.Fatal(err)
				}
				mustStart(t, c)
			} else {
				if _, err = c.CommandStatus(context.Background(), h); !errors.Is(err, ErrClosed) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSyncDiscardsStaleBytesAndReplies(t *testing.T) {
	conn := testTransport(t, func(p *testPeer) error {
		req, nonce, err := p.syncRequest()
		if err != nil {
			return err
		}
		// Missing delimiter, malformed stale frame, wrong ID and wrong echo are
		// not sufficient. A later sentinel must reset even a partial JSON frame.
		if _, err = fmt.Fprintf(p.conn, "garbage{\"id\":%d,\"return\":%d}\n\xffbad\n\xff{\"id\":999,\"return\":%d}\n\xff{\"id\":%d,\"return\":-1}\n\xff{\"broken\":", req.ID, nonce, nonce, req.ID); err != nil {
			return err
		}
		if _, err = fmt.Fprintf(p.conn, "\xff{\"id\":%d,\"error\":{\"class\":\"GenericError\",\"desc\":\"stale\"}}\n", req.ID); err != nil {
			return err
		}
		return p.syncReply(req, nonce)
	})
	c, err := New(context.Background(), conn, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

func TestSyncFailuresAreBounded(t *testing.T) {
	for _, mode := range []string{"stale-budget", "frame-budget", "eof", "wrong-echo", "remote-error", "no-delimiter"} {
		t.Run(mode, func(t *testing.T) {
			conn := testTransport(t, func(p *testPeer) error {
				req, nonce, err := p.syncRequest()
				if err != nil {
					return err
				}
				switch mode {
				case "stale-budget":
					_, _ = p.conn.Write(bytes.Repeat([]byte{0xff}, MaxSyncBytes+1))
				case "frame-budget":
					_, _ = p.conn.Write(append([]byte{0xff}, bytes.Repeat([]byte{'x'}, MaxFrameBytes+1)...))
				case "wrong-echo":
					_ = p.syncReply(req, nonce^1)
				case "remote-error":
					_, _ = fmt.Fprintf(p.conn, "\xff{\"id\":%d,\"error\":{\"class\":\"CommandNotFound\",\"desc\":\"disabled\"}}\n", req.ID)
				case "no-delimiter":
					_ = p.reply(req, nonce)
				}
				return nil
			})
			c, err := New(context.Background(), conn, Options{})
			if err == nil || c != nil {
				t.Fatalf("sync unexpectedly succeeded: %v", err)
			}
			if strings.HasSuffix(mode, "budget") && !errors.Is(err, ErrLimit) {
				t.Fatal(err)
			}
			if mode == "remote-error" {
				var remote *RemoteError
				if errors.As(err, &remote) {
					t.Fatal("stale error incorrectly correlated without the random echo")
				}
			}
		})
	}
}

func TestResponseFrameAndEOFBounds(t *testing.T) {
	for _, mode := range []string{"frame", "partial-eof", "empty-eof"} {
		t.Run(mode, func(t *testing.T) {
			c := testClient(t, func(p *testPeer) error {
				if _, err := p.receive("guest-exec"); err != nil {
					return err
				}
				if mode == "frame" {
					_, _ = p.conn.Write(bytes.Repeat([]byte{' '}, MaxFrameBytes+1))
				}
				if mode == "partial-eof" {
					_, _ = p.conn.Write([]byte(`{"id":2`))
				}
				return nil
			}, Options{})
			_, err := c.StartCommand(context.Background(), Command{Path: "x"})
			expectUncertain(t, err)
			want := error(io.EOF)
			if mode == "frame" {
				want = ErrLimit
			}
			if mode == "partial-eof" {
				want = io.ErrUnexpectedEOF
			}
			if !errors.Is(err, want) {
				t.Fatal(err)
			}
		})
	}
}

func TestCancellationAndHardTimeoutAreNotProcessCancellation(t *testing.T) {
	for _, mode := range []string{"cancel", "hard-timeout", "close"} {
		t.Run(mode, func(t *testing.T) {
			received := make(chan struct{})
			c := testClient(t, func(p *testPeer) error {
				if _, err := p.receive("guest-exec"); err != nil {
					return err
				}
				close(received)
				_, _ = io.Copy(io.Discard, p.reader)
				return nil
			}, Options{RPCTimeout: 100 * time.Millisecond})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := c.StartCommand(ctx, Command{Path: "/long-running-guest-process"}); done <- err }()
			<-received
			if mode == "cancel" {
				cancel()
			}
			if mode == "close" {
				_ = c.Close()
			}
			select {
			case err := <-done:
				expectUncertain(t, err)
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("RPC did not stop")
			}
			if _, err := c.StartCommand(context.Background(), Command{Path: "x"}); !errors.Is(err, ErrClosed) {
				t.Fatal(err)
			}
		})
	}
}

func TestQueueCancellationDoesNotPoisonActiveRPC(t *testing.T) {
	received, releasePeer := make(chan struct{}), make(chan struct{})
	c := testClient(t, func(p *testPeer) error {
		req, err := p.receive("guest-exec")
		if err != nil {
			return err
		}
		close(received)
		<-releasePeer
		if err = p.reply(req, map[string]int{"pid": 1}); err != nil {
			return err
		}
		req, err = p.receive("guest-exec")
		if err != nil {
			return err
		}
		return p.reply(req, map[string]int{"pid": 2})
	}, Options{})
	done := make(chan error, 1)
	go func() { _, err := c.StartCommand(context.Background(), Command{Path: "x"}); done <- err }()
	<-received
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.StartCommand(ctx, Command{Path: "x"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	var uncertain *StartUncertainError
	if errors.As(err, &uncertain) {
		t.Fatal("queued command was marked transmitted")
	}
	close(releasePeer)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	mustStart(t, c)
}

func TestConcurrentCommandsAndClose(t *testing.T) {
	c := testClient(t, func(p *testPeer) error {
		for i := range 16 {
			req, err := p.receive("guest-exec")
			if err != nil {
				return err
			}
			if req.ID != int64(i+2) {
				return fmt.Errorf("non-serial ID: %d", req.ID)
			}
			if err = p.reply(req, map[string]int{"pid": i + 1}); err != nil {
				return err
			}
		}
		_, _ = io.Copy(io.Discard, p.reader)
		return nil
	}, Options{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, err := c.StartCommand(context.Background(), Command{Path: "x"}); err != nil {
				t.Errorf("concurrent start: %v", err)
			}
		})
	}
	wg.Wait()
	for range 16 {
		wg.Go(func() { _ = c.Close(); _, _ = c.StartCommand(context.Background(), Command{Path: "x"}) })
	}
	wg.Wait()
}

func TestCanceledContextAndInvalidOptions(t *testing.T) {
	if _, err := New(context.Background(), nil, Options{}); err == nil {
		t.Fatal("nil accepted")
	}
	for _, timeout := range []time.Duration{-1, MaxRPCTimeout + 1} {
		conn := testTransport(t, func(p *testPeer) error { _, _ = io.Copy(io.Discard, p.reader); return nil })
		if _, err := New(context.Background(), conn, Options{RPCTimeout: timeout}); err == nil {
			t.Fatal("bad timeout accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn := testTransport(t, func(p *testPeer) error { _, _ = io.Copy(io.Discard, p.reader); return nil })
	if _, err := New(ctx, conn, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	c := testClient(t, func(p *testPeer) error { _, _ = io.Copy(io.Discard, p.reader); return nil }, Options{})
	if _, err := c.StartCommand(ctx, Command{Path: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// An injected wrapper exercises trusted-transport failures without OS handles.
type faultTransport struct {
	net.Conn
	deadlineError atomic.Bool
	writeMode     atomic.Int32
	closeCalls    atomic.Int32
}

func (f *faultTransport) SetDeadline(d time.Time) error {
	if f.deadlineError.Load() {
		return errors.New("deadline failure")
	}
	return f.Conn.SetDeadline(d)
}
func (f *faultTransport) Write(p []byte) (int, error) {
	switch f.writeMode.Load() {
	case 1:
		return 0, io.ErrClosedPipe
	case 2:
		return 1, io.ErrShortWrite
	case 3:
		return 0, nil
	}
	return f.Conn.Write(p)
}
func (f *faultTransport) Close() error { f.closeCalls.Add(1); return f.Conn.Close() }

func TestTransportFailuresAndIDExhaustion(t *testing.T) {
	for _, mode := range []string{"deadline", "zero-write", "partial-write", "short-write", "id"} {
		t.Run(mode, func(t *testing.T) {
			conn := testTransport(t, func(p *testPeer) error {
				req, nonce, err := p.syncRequest()
				if err != nil {
					return err
				}
				if err = p.syncReply(req, nonce); err != nil {
					return err
				}
				_, _ = io.Copy(io.Discard, p.reader)
				return nil
			})
			f := &faultTransport{Conn: conn}
			c, err := New(context.Background(), f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "deadline":
				f.deadlineError.Store(true)
			case "zero-write":
				f.writeMode.Store(1)
			case "partial-write":
				f.writeMode.Store(2)
			case "short-write":
				f.writeMode.Store(3)
			case "id":
				c.nextID = math.MaxInt64
			}
			_, err = c.StartCommand(context.Background(), Command{Path: "x"})
			if err == nil {
				t.Fatal("failure accepted")
			}
			var uncertain *StartUncertainError
			if errors.As(err, &uncertain) != (mode == "partial-write") {
				t.Fatalf("wrong uncertainty: %v", err)
			}
			_ = c.Close()
			_ = c.Close()
			if f.closeCalls.Load() != 1 {
				t.Fatalf("Close calls: %d", f.closeCalls.Load())
			}
		})
	}
}

func TestSignedStatusRetiresHandleWithoutPoisoningClient(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       int64
	}{
		{"windows-access-violation", "signal", -1073741819},
		{"windows-high-bit-exit", "exitcode", -2147483648},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(p *testPeer) error {
				for run := range 2 {
					req, err := p.receive("guest-exec")
					if err != nil {
						return err
					}
					// Reusing the reaped PID must not revive the previous handle.
					if err = p.reply(req, map[string]int{"pid": 123}); err != nil {
						return err
					}
					req, err = p.receive("guest-exec-status")
					if err != nil {
						return err
					}
					status := map[string]any{"exited": true, "out-data": "b2s="}
					if run == 0 {
						status[tc.field] = tc.value
					} else {
						status["exitcode"] = 0
					}
					if err = p.reply(req, status); err != nil {
						return err
					}
				}
				return nil
			}, Options{})
			old := mustStart(t, c)
			status, err := c.CommandStatus(context.Background(), old)
			if err != nil {
				t.Fatalf("valid signed guest status failed: %v", err)
			}
			got, other := status.ExitCode, status.Signal
			if tc.field == "signal" {
				got, other = status.Signal, status.ExitCode
			}
			if !status.Exited || got == nil || *got != tc.value || other != nil || string(status.Stdout) != "ok" {
				t.Fatalf("wrong signed status: %+v", status)
			}
			if _, err = c.CommandStatus(context.Background(), old); !errors.Is(err, ErrInvalidHandle) {
				t.Fatalf("old handle not retired: %v", err)
			}
			next := mustStart(t, c)
			if _, err = c.CommandStatus(context.Background(), old); !errors.Is(err, ErrInvalidHandle) {
				t.Fatalf("PID reuse revived old handle: %v", err)
			}
			status, err = c.CommandStatus(context.Background(), next)
			if err != nil || !status.Exited || status.ExitCode == nil || *status.ExitCode != 0 || status.Signal != nil {
				t.Fatalf("client poisoned after signed result: %+v, %v", status, err)
			}
		})
	}
}
