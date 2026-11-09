package python

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCapturedLogTailBoundsAndFrames(t *testing.T) {
	for _, tc := range []struct{ name, stdout, pending, stderr, want string }{
		{"empty", "", "", "", ""},
		{"stderr-follows-stdout", "out\n", "partial", "err\n", "out\npartialerr\n"},
		{"past-legacy-prefix", strings.Repeat("a", 70<<10) + "last", "", "", strings.Repeat("a", MaxLogTailBytes-4) + "last"},
		{"exact-limit", strings.Repeat("a", MaxLogTailBytes), "", "", strings.Repeat("a", MaxLogTailBytes)},
		{"stderr-tail", "out", "", strings.Repeat("e", MaxLogTailBytes+1), strings.Repeat("e", MaxLogTailBytes)},
		{"byte-not-rune-limit", strings.Repeat("猫", 2000), "", "", string([]byte(strings.Repeat("猫", 2000))[6000-MaxLogTailBytes:])},
		{"frames", "before\n\x1eTCSIM_CALL {\"secret\":1}\nafter\n\x1eTCSIM_RESULT {\"payload\":2}\n", "", "err", "before\nafter\nerr"},
		{"partial-frame", "before\n\x1eTCSIM_", "CALL {\"secret\":", "err", "before\nerr"},
		{"inline-and-stderr-frames", "guest\x1eTCSIM_RESULT hidden\n", "", "stderr\x1eTCSIM_CALL hidden", "gueststderr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := capturedLogTail([]byte(tc.stdout), []byte(tc.pending), []byte(tc.stderr))
			if string(got) != tc.want || len(got) > MaxLogTailBytes {
				t.Fatalf("tail length=%d want=%d; mismatch=%v", len(got), len(tc.want), string(got) != tc.want)
			}
		})
	}
}

func TestActualPythonLogTail(t *testing.T) {
	r, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close(context.Background())
	for _, tc := range []struct {
		name, source, suffix string
		failure, stopped     bool
	}{
		{"success", "print('a' * 70000)\n    print('actual final 猫')\n    return {'payload_only':'not a log'}", "actual final 猫\n\n", false, false},
		{"handler-error", "print('before error', flush=True)\n    raise ValueError('payload only')", "before error\n\n", true, false},
		{"stderr-order", "import sys\n    print('err', file=sys.stderr, flush=True)\n    print('out', flush=True)\n    return None", "out\n\nerr\n", false, false},
		{"timeout-pending", "import sys\n    print('before timeout', end='', flush=True)\n    print('stderr timeout', file=sys.stderr, flush=True)\n    while True: pass", "before timeoutstderr timeout\n", false, true},
		{"output-limit", "import sys\n    print('before', flush=True)\n    sys.stdout.write('a' * 300000)\n    sys.stdout.flush()", "OSError: [Errno 29] I/O error\n", false, true},
		{"exchange", "import _simulator_bridge\n    print('before')\n    _simulator_bridge.call({'hidden_request':'dummy'})\n    print('after')\n    return 'hidden_result'", "before\n\nafter\n\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			timeout := 3 * time.Second
			var exchange Exchange
			if tc.name != "stderr-order" && tc.name != "handler-error" {
				exchange = func(context.Context, json.RawMessage) (json.RawMessage, error) {
					return json.RawMessage(`{"hidden_response":"dummy"}`), nil
				}
			}
			result, err := r.Run(context.Background(), Invocation{Code: sourceZIP(t, "def handler(event, context):\n    "+tc.source+"\n"), Handler: "index.handler", Event: json.RawMessage(`{}`), Timeout: timeout, Exchange: exchange})
			if tc.name == "timeout-pending" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout must stop with its deadline: %v", err)
			}
			if (err != nil) != tc.stopped || result.FunctionError != tc.failure {
				t.Fatalf("error=%v functionError=%v", err, result.FunctionError)
			}
			if !bytes.HasSuffix(result.LogTail, []byte(tc.suffix)) || len(result.LogTail) > MaxLogTailBytes {
				t.Fatalf("tail length=%d suffix=%q", len(result.LogTail), result.LogTail)
			}
			if tc.name == "output-limit" && (len(result.LogTail) != MaxLogTailBytes || !bytes.HasPrefix(result.LogTail, []byte(strings.Repeat("a", 32))) || !bytes.Contains(result.LogTail, []byte("Traceback (most recent call last):"))) {
				t.Fatal("overflow must retain bounded stdout followed by actual stderr")
			}
			if bytes.Contains(result.LogTail, []byte("TCSIM_")) || bytes.Contains(result.LogTail, []byte("hidden_")) || bytes.Contains(result.LogTail, []byte("payload only")) {
				t.Fatal("protocol/payload leaked")
			}
			if tc.name == "success" && (len(result.LogTail) != MaxLogTailBytes || len(result.Logs) != 64<<10 || strings.Contains(result.Logs, "actual final")) {
				t.Fatal("tail derived from legacy prefix")
			}
		})
	}
}
