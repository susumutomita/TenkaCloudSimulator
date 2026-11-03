package python

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func TestExchangeFramesBoundsAndSanitizedFailure(t *testing.T) {
	makeStream := func(call Exchange) (*exchangeInput, *exchangeOutput, *cappedOutput) {
		input := &exchangeInput{context: context.Background(), initial: bytes.NewReader(nil), replies: make(chan []byte, 1)}
		output := &cappedOutput{}
		return input, &exchangeOutput{context: context.Background(), call: call, input: input, output: output}, output
	}
	call := func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"result":42}`), nil
	}
	t.Run("partial-frames-and-logs", func(t *testing.T) {
		in, out, logs := makeStream(call)
		for _, p := range []string{"ordinary log\n\x1eTCS", "IM_CALL {\"op\":\"read\"}", "\n"} {
			if _, err := out.Write([]byte(p)); err != nil {
				t.Fatal(err)
			}
		}
		buf := make([]byte, 256)
		n, err := in.Read(buf)
		if err != nil || string(buf[:n]) != "{\"result\":42}\n" || logs.String() != "ordinary log\n" {
			t.Fatal(string(buf[:n]), logs.String(), err)
		}
		if _, err = out.Write([]byte("last log")); err != nil {
			t.Fatal(err)
		}
		if err = out.finish(); err != nil || logs.String() != "ordinary log\nlast log" {
			t.Fatal(logs.String(), err)
		}
	})
	t.Run("no-host-error-detail", func(t *testing.T) {
		in, out, _ := makeStream(func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("private-host-detail")
		})
		if _, err := out.Write([]byte("\x1eTCSIM_CALL {}\n")); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 256)
		n, _ := in.Read(buf)
		if strings.Contains(string(buf[:n]), "private-host-detail") || !bytes.Contains(buf[:n], []byte("CapabilityFailure")) {
			t.Fatal(string(buf[:n]))
		}
	})
	for name, message := range map[string]string{"bad-json": "\x1eTCSIM_CALL invalid\n", "large-call": "\x1eTCSIM_CALL " + strings.Repeat("x", maxExchangeMessage+1), "large-output": strings.Repeat("x", MaxOutputBytes+1)} {
		t.Run(name, func(t *testing.T) {
			_, out, _ := makeStream(call)
			if _, err := out.Write([]byte(message)); err == nil {
				t.Fatal("accepted invalid frame")
			}
		})
	}
	t.Run("unread-response", func(t *testing.T) {
		_, out, _ := makeStream(call)
		if _, err := out.Write([]byte("\x1eTCSIM_CALL {}\n\x1eTCSIM_CALL {}\n")); err == nil {
			t.Fatal("unbounded queued responses")
		}
	})
	t.Run("response-limit", func(t *testing.T) {
		_, out, _ := makeStream(func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`"` + strings.Repeat("x", maxExchangeMessage) + `"`), nil
		})
		if _, err := out.Write([]byte("\x1eTCSIM_CALL {}\n")); err == nil {
			t.Fatal("oversized reply")
		}
	})
	t.Run("incomplete-frame", func(t *testing.T) {
		_, out, _ := makeStream(call)
		_, _ = out.Write([]byte("\x1eTCSIM_CALL {}"))
		if out.finish() == nil {
			t.Fatal("incomplete frame accepted")
		}
	})
	t.Run("call-count", func(t *testing.T) {
		in, out, _ := makeStream(call)
		for i := 0; i < maxExchangeCalls; i++ {
			if _, err := out.Write([]byte("\x1eTCSIM_CALL {}\n")); err != nil {
				t.Fatal(i, err)
			}
			buf := make([]byte, 100)
			_, _ = in.Read(buf)
		}
		if _, err := out.Write([]byte("\x1eTCSIM_CALL {}\n")); err == nil {
			t.Fatal("call quota bypassed")
		}
	})
	t.Run("cancel-read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		in := exchangeInput{context: ctx, initial: bytes.NewReader(nil), replies: make(chan []byte, 1)}
		if _, err := in.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("aggregate-budget", func(t *testing.T) {
		reply := json.RawMessage(`"` + strings.Repeat("r", 32768) + `"`)
		in, out, _ := makeStream(func(context.Context, json.RawMessage) (json.RawMessage, error) { return reply, nil })
		message := []byte("\x1eTCSIM_CALL " + `"` + strings.Repeat("q", 32768) + `"` + "\n")
		failed := false
		for i := 0; i < 32; i++ {
			if _, err := out.Write(message); err != nil {
				failed = true
				break
			}
			buf := make([]byte, 65536)
			_, _ = in.Read(buf)
		}
		if !failed {
			t.Fatal("aggregate capability quota bypassed")
		}
	})
	t.Run("invalid-response", func(t *testing.T) {
		_, out, _ := makeStream(func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage("bad-json"), nil
		})
		if _, err := out.Write([]byte("\x1eTCSIM_CALL {}\n")); err == nil {
			t.Fatal("invalid reply accepted")
		}
	})

	var _ io.Reader = (*exchangeInput)(nil)
}
func TestRealPythonExplicitCapabilityAndTrustedModule(t *testing.T) {
	runtime, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	source := "import safeclient\ndef handler(event,context):\n    return safeclient.read(event)\n"
	modules := fstest.MapFS{"safeclient.py": &fstest.MapFile{Data: []byte("from _simulator_bridge import call\ndef read(event): return call({'request':event})\n")}}
	in := Invocation{Code: sourceZIP(t, source), Handler: "index.handler", Event: json.RawMessage(`{"name":"demo"}`), Timeout: 15 * time.Second, Modules: modules, Exchange: func(ctx context.Context, message json.RawMessage) (json.RawMessage, error) {
		if !bytes.Contains(message, []byte(`"name":"demo"`)) {
			return nil, errors.New("unexpected request")
		}
		return json.RawMessage(`{"answer":42}`), nil
	}}
	result, err := runtime.Run(context.Background(), in)
	if err != nil || result.FunctionError || !bytes.Contains(result.Payload, []byte(`"answer": 42`)) || strings.Contains(result.Logs, "TCSIM_CALL") {
		t.Fatal(string(result.Payload), err)
	}
	in.Exchange = nil
	result, err = runtime.Run(context.Background(), in)
	if err != nil || !result.FunctionError || !bytes.Contains(result.Payload, []byte("ModuleNotFoundError")) {
		t.Fatal(string(result.Payload), err)
	}
}

func TestCapabilityReadCancellationAndConcurrentIsolation(t *testing.T) {
	runtime, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	source := "from _simulator_bridge import call\ndef handler(event,context): return call(event)\n"
	code := sourceZIP(t, source)
	t.Run("four-independent-channels", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				event := json.RawMessage(fmt.Sprintf(`{"id":%d}`, id))
				result, err := runtime.Run(context.Background(), Invocation{Code: code, Handler: "index.handler", Event: event, Timeout: 15 * time.Second, Exchange: func(ctx context.Context, message json.RawMessage) (json.RawMessage, error) {
					return append(json.RawMessage(nil), message...), nil
				}})
				var got struct{ ID int }
				if err != nil || result.FunctionError || json.Unmarshal(result.Payload, &got) != nil || got.ID != id {
					t.Errorf("channel%d: %s %v", id, result.Payload, err)
				}
			}(i)
		}
		wg.Wait()
	})
	t.Run("blocked-stdin-cancels", func(t *testing.T) {
		result, err := runtime.Run(context.Background(), Invocation{Code: sourceZIP(t, "import sys\ndef handler(event,context): return sys.stdin.read()\n"), Handler: "index.handler", Event: json.RawMessage(`{}`), Timeout: time.Second, Exchange: func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }})
		if !errors.Is(err, context.DeadlineExceeded) || result.FunctionError {
			t.Fatal(result, err)
		}
	})
	t.Run("close-cancels-active-exchange", func(t *testing.T) {
		reached := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, err := runtime.Run(context.Background(), Invocation{Code: code, Handler: "index.handler", Event: json.RawMessage(`{}`), Timeout: 30 * time.Second, Exchange: func(ctx context.Context, message json.RawMessage) (json.RawMessage, error) {
				close(reached)
				<-ctx.Done()
				return nil, ctx.Err()
			}})
			done <- err
		}()
		select {
		case <-reached:
		case <-time.After(20 * time.Second):
			t.Fatal("guest never reached exchange")
		}
		closed := make(chan error, 1)
		go func() { closed <- runtime.Close(context.Background()) }()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("active exchange did not cancel")
		}
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Close waited past exchange cancellation")
		}
	})
}
