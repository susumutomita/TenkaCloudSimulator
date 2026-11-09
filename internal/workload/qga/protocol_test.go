package qga

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestStrictStatusSchema(t *testing.T) {
	for _, data := range []string{
		`null`, `[]`, `{}`, `{"exited":null}`, `{"exited":"false"}`,
		`{"exited":true}`, `{"exited":true,"exitcode":0,"signal":9}`,
		`{"exited":true,"exitcode":null}`, `{"exited":true,"exitcode":"-1"}`,
		`{"exited":true,"exitcode":1.5}`, `{"exited":true,"signal":-1.5}`,
		`{"exited":true,"exitcode":9223372036854775808}`,
		`{"exited":true,"exitcode":-9223372036854775809}`,
		`{"exited":true,"signal":9223372036854775808}`,
		`{"exited":true,"signal":-9223372036854775809}`,
		`{"exited":true,"signal":null}`, `{"exited":true,"signal":"9"}`,
		`{"exited":true,"exitcode":0,"out-truncated":null}`,
		`{"exited":true,"exitcode":0,"err-truncated":"false"}`,
		`{"exited":true,"exitcode":0,"out-data":1}`,
		`{"exited":true,"exitcode":0,"err-data":null}`,
		`{"exited":true,"exitcode":0,"out-data":"YQ"}`,
		`{"exited":true,"exitcode":0,"err-data":"YQ==\r"}`,
		`{"exited":false,"signal":9}`, `{"exited":false,"exited":true}`,
		`{"exited":false,"unexpected":true}`,
	} {
		if _, err := parseStatus([]byte(data)); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: %v", data, err)
		}
	}
}

func TestExactCombinedOutputLimit(t *testing.T) {
	stdout := bytes.Repeat([]byte{0, 255}, MaxOutputBytes/4)
	stderr := bytes.Repeat([]byte{42}, MaxOutputBytes-len(stdout))
	data, err := json.Marshal(map[string]any{"exited": true, "exitcode": 123, "out-data": base64.StdEncoding.EncodeToString(stdout), "err-data": base64.StdEncoding.EncodeToString(stderr), "out-truncated": false, "err-truncated": false})
	if err != nil {
		t.Fatal(err)
	}
	result, err := parseStatus(data)
	if err != nil || !bytes.Equal(result.Stdout, stdout) || !bytes.Equal(result.Stderr, stderr) || *result.ExitCode != 123 {
		t.Fatalf("exact combined bound: %v", err)
	}
	for _, data := range []string{"=", "==", "===", "====", "YQ===", "Y Q==", "AA_A", "AA-A"} {
		if _, err := decodeOutput(data, MaxOutputBytes); !errors.Is(err, ErrProtocol) {
			t.Errorf("invalid base64 %q: %v", data, err)
		}
	}
	if _, err := decodeOutput(base64.StdEncoding.EncodeToString(make([]byte, MaxOutputBytes+1)), MaxOutputBytes); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
}

func TestObjectAndEnvelopeRejectAmbiguity(t *testing.T) {
	for _, data := range []string{
		``, `null`, `[]`, `"x"`, `{`, `{"id":`, `{"id":2`,
		`{"id":2,`, `{"id":2} invalid`, `{"id":2} {}`,
		`{"id":2,"return":0,"return":1}`, `{"id":2.0,"return":1}`,
		`{"id":null,"return":1}`, `{"id":2,"error":null}`,
		`{"id":2,"error":{"class":"","desc":"x"}}`,
		`{"id":2,"error":{"class":"X","desc":null}}`,
		`{"id":2,"error":{"class":"X","desc":"x","desc":"y"}}`,
		`{"id":2,"error":{"class":"X","desc":"x","unexpected":1}}`,
	} {
		if _, err := parseEnvelope([]byte(data), 2); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: %v", data, err)
		}
	}
	for _, data := range []string{`null`, `{}`, `{"pid":null}`, `{"pid":-1}`, `{"pid":1.1}`, `{"pid":9223372036854775808}`} {
		if _, err := parsePID([]byte(data)); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: %v", data, err)
		}
	}
}

func TestExactInputAndFrameLimits(t *testing.T) {
	command := Command{Path: strings.Repeat("x", MaxPathBytes), Args: append([]string{strings.Repeat("y", MaxArgumentBytes-MaxPathBytes)}, make([]string, MaxArgs-1)...), Stdin: make([]byte, MaxStdinBytes)}
	if err := validateCommand(command); err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(request{Execute: "guest-exec", Arguments: execArguments(command), ID: 2})
	if err != nil || len(wire) > MaxRequestBytes {
		t.Fatalf("valid input exceeds request: %d, %v", len(wire), err)
	}
	c := testClient(t, func(p *testPeer) error {
		req, err := p.receive("guest-exec")
		if err != nil {
			return err
		}
		line := fmt.Sprintf(`{"id":%d,"return":{"pid":1}}`, req.ID)
		line += strings.Repeat(" ", MaxFrameBytes-len(line))
		_, err = io.WriteString(p.conn, line+"\n")
		return err
	}, Options{})
	if _, err := c.StartCommand(context.Background(), command); err != nil {
		t.Fatal(err)
	}
}

func TestStatusTimeoutClosesWithoutPretendingProcessStopped(t *testing.T) {
	c := testClient(t, func(p *testPeer) error {
		req, err := p.receive("guest-exec")
		if err != nil {
			return err
		}
		if err = p.reply(req, map[string]int{"pid": 1}); err != nil {
			return err
		}
		if _, err = p.receive("guest-exec-status"); err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, p.reader)
		return nil
	}, Options{RPCTimeout: 100 * time.Millisecond})
	h := mustStart(t, c)
	status, err := c.CommandStatus(context.Background(), h)
	if err == nil || status.Exited {
		t.Fatalf("timeout claimed exit: %+v, %v", status, err)
	}
	if _, err = c.CommandStatus(context.Background(), h); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestBlockedWriteAndSyncTimeouts(t *testing.T) {
	for _, mode := range []string{"write", "sync"} {
		t.Run(mode, func(t *testing.T) {
			conn := testTransport(t, func(p *testPeer) error {
				if mode == "sync" {
					if _, _, err := p.syncRequest(); err != nil {
						return err
					}
				}
				// Wait for client closure without reading the request in the write case.
				if mode == "write" {
					time.Sleep(150 * time.Millisecond)
				} else {
					_, _ = io.Copy(io.Discard, p.reader)
				}
				return nil
			})
			start := time.Now()
			if _, err := New(context.Background(), conn, Options{RPCTimeout: 50 * time.Millisecond}); err == nil {
				t.Fatal("blocked IO succeeded")
			}
			if time.Since(start) > time.Second {
				t.Fatal("hard timeout did not bound IO")
			}
		})
	}
}

func TestSyncNonceIsFresh(t *testing.T) {
	var values []int64
	for range 8 {
		conn := testTransport(t, func(p *testPeer) error {
			req, nonce, err := p.syncRequest()
			if err != nil {
				return err
			}
			values = append(values, nonce)
			return p.syncReply(req, nonce)
		})
		c, err := New(context.Background(), conn, Options{})
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
	}
	seen := make(map[int64]bool)
	for _, nonce := range values {
		if nonce < 0 || seen[nonce] {
			t.Fatalf("not a fresh nonnegative int64 nonce: %d", nonce)
		}
		seen[nonce] = true
	}
}

func TestSignedStatusValues(t *testing.T) {
	// QAPI int fields are signed int64. In particular, QEMU's Windows gint
	// process status can represent high-bit exit codes and exceptions negatively.
	for _, name := range []string{"exitcode", "signal"} {
		for _, value := range []int64{-1 << 63, -2147483648, -1073741819, -1, 0, 1, 1<<63 - 1} {
			t.Run(fmt.Sprintf("%s/%d", name, value), func(t *testing.T) {
				wire := fmt.Sprintf(`{"exited":true,%q:%d}`, name, value)
				status, err := parseStatus([]byte(wire))
				if err != nil {
					t.Fatalf("valid signed QAPI integer rejected: %v", err)
				}
				got, other := status.ExitCode, status.Signal
				if name == "signal" {
					got, other = status.Signal, status.ExitCode
				}
				if !status.Exited || got == nil || *got != value || other != nil {
					t.Fatalf("wrong signed status: %+v", status)
				}
			})
		}
	}
}
