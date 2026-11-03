package python

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func sourceZIP(t *testing.T, source string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	z := zip.NewWriter(&buffer)
	f, e := z.Create("index.py")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write([]byte(source)); e != nil {
		t.Fatal(e)
	}
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	return buffer.Bytes()
}

func TestActualPythonAndSandboxBoundaries(t *testing.T) {
	r, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close(context.Background())
	invoke := func(source string, timeout time.Duration) (Result, error) {
		return r.Run(context.Background(), Invocation{Code: sourceZIP(t, source), Handler: "index.handler", Event: json.RawMessage(`{"value":7}`), Environment: map[string]string{"FUNCTION_VALUE": "local value"}, Context: map[string]string{"function_name": "test-function"}, Timeout: timeout})
	}
	t.Run("real-code-sqlite-hash-context", func(t *testing.T) {
		result, e := invoke(`import sqlite3, hashlib, os
def handler(event, context):
    db=sqlite3.connect(':memory:')
    db.execute('create table values_table (value integer)')
    db.execute('insert into values_table values (?)', (event['value'],))
    return {'sql':db.execute('select value * 6 from values_table').fetchone()[0], 'hash':hashlib.sha256(b'test').hexdigest(), 'env':os.environ['FUNCTION_VALUE'], 'function':context.function_name}
`, 3*time.Second)
		if e != nil || result.FunctionError {
			t.Fatalf("%+v %v", result, e)
		}
		var actual map[string]any
		if json.Unmarshal(result.Payload, &actual) != nil || actual["sql"] != float64(42) || actual["hash"] != "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08" || actual["env"] != "local value" || actual["function"] != "test-function" {
			t.Fatalf("%s", result.Payload)
		}
	})
	t.Run("python-error-is-result", func(t *testing.T) {
		result, e := invoke("def handler(event, context):\n    raise ValueError('expected failure')\n", 3*time.Second)
		if e != nil || !result.FunctionError || !bytes.Contains(result.Payload, []byte(`"errorType": "ValueError"`)) {
			t.Fatalf("%+v %v", result, e)
		}
	})
	t.Run("no-host-capabilities", func(t *testing.T) {
		file, e := os.CreateTemp(t.TempDir(), "host-sentinel")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = file.WriteString("private test sentinel"); e != nil {
			t.Fatal(e)
		}
		_ = file.Close()
		t.Setenv("TCSIM_HOST_SENTINEL", "must not inherit")
		source := fmt.Sprintf(`import os, socket
def handler(event, context):
    assert 'TCSIM_HOST_SENTINEL' not in os.environ
    for path in ['/etc/passwd', %q, '/python/../../etc/passwd', '/var/task/../../etc/passwd']:
        try:
            open(path).read()
            raise AssertionError('host file accessible')
        except OSError:
            pass
    for path in ['/var/task/index.py', '/python/lib/python3.12/os.py']:
        try:
            with open(path, 'w') as output:
                output.write('changed')
                output.flush()
            raise AssertionError('read-only code was writable')
        except OSError:
            pass
    try:
        os.system('echo not allowed')
        raise AssertionError('host process accessible')
    except (AttributeError, OSError):
        pass
    try:
        connection = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        connection.connect(('127.0.0.1', 1))
        raise AssertionError('host socket accessible')
    except (OSError, AttributeError):
        pass
    return 'sandboxed'
`, file.Name())
		result, e := invoke(source, 3*time.Second)
		if e != nil || result.FunctionError || string(result.Payload) != `"sandboxed"` {
			t.Fatalf("%+v %v", result, e)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		start := time.Now()
		result, e := invoke("def handler(event, context):\n    print('entered loop', flush=True)\n    while True: pass\n", 4*time.Second)
		if e == nil || !strings.Contains(result.Logs, "entered loop") || time.Since(start) > 6*time.Second {
			t.Fatalf("deadline failed: %v %s", e, time.Since(start))
		}
	})
	t.Run("bounded-memory", func(t *testing.T) {
		result, e := invoke("def handler(event, context):\n    return len(b'x' * (256 << 20))\n", 3*time.Second)
		if e == nil && (!result.FunctionError || !bytes.Contains(result.Payload, []byte("MemoryError"))) {
			t.Fatalf("memory bound failed: %+v", result)
		}
	})
	t.Run("real-clock-and-cancelable-sleep", func(t *testing.T) {
		result, e := invoke("import time\ndef handler(event, context):\n    start=time.monotonic()\n    time.sleep(0.05)\n    return {'wall':time.time(), 'elapsed':time.monotonic()-start}\n", 5*time.Second)
		var data map[string]float64
		if e != nil || result.FunctionError || json.Unmarshal(result.Payload, &data) != nil || data["wall"] < 1700000000 || data["elapsed"] < 0.04 {
			t.Fatalf("clock/sleep: %+v %v", result, e)
		}
		start := time.Now()
		result, e = invoke("import time\ndef handler(event, context):\n    print('entered sleep',flush=True)\n    time.sleep(3600)\n", 4*time.Second)
		if e == nil || !strings.Contains(result.Logs, "entered sleep") || time.Since(start) > 6*time.Second {
			t.Fatalf("sleep escaped deadline: %+v %v", result, e)
		}
	})
	t.Run("early-exit-and-invalid-protocol", func(t *testing.T) {
		for _, source := range []string{"import os\ndef handler(event, context): os._exit(0)", "import os\ndef handler(event, context):\n    print('\\x1eTCSIM_RESULT invalid JSON',flush=True)\n    os._exit(0)", "import os\ndef handler(event, context): os._exit(2)"} {
			if _, e := invoke(source, 5*time.Second); e == nil {
				t.Error("early exit accepted as function result")
			}
		}
	})
	t.Run("bounded-output", func(t *testing.T) {
		_, e := invoke("def handler(event, context):\n    print('x' * (300 << 10))\n    return 'unexpected'\n", 3*time.Second)
		if e == nil || !strings.Contains(e.Error(), "output exceeded") {
			t.Fatalf("output bound failed: %v", e)
		}
	})
	t.Run("fresh-concurrent-invocations", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				result, e := invoke("counter=0\ndef handler(event, context):\n    global counter\n    counter+=1\n    return counter\n", 5*time.Second)
				if e != nil || result.FunctionError || string(result.Payload) != "1" {
					t.Errorf("isolation: %+v %v", result, e)
				}
			}()
		}
		wg.Wait()
	})
	t.Run("validation-and-cancellation", func(t *testing.T) {
		valid := Invocation{Code: sourceZIP(t, "def handler(event, context): return 1"), Handler: "index.handler", Event: json.RawMessage(`{}`), Timeout: time.Second}
		invalidCode := valid
		invalidCode.Code = []byte("not ZIP")
		if _, e := r.Run(context.Background(), invalidCode); e == nil {
			t.Error("invalid executable code accepted")
		}
		for _, alter := range []func(*Invocation){func(in *Invocation) { in.Event = []byte("invalid") }, func(in *Invocation) { in.Timeout = 0 }, func(in *Invocation) { in.Environment = map[string]string{"bad=key": "value"} }, func(in *Invocation) { in.Environment = map[string]string{"key": strings.Repeat("x", 8193)} }, func(in *Invocation) {
			in.Context = map[string]string{}
			for i := 0; i < 33; i++ {
				in.Context[fmt.Sprint(i)] = "x"
			}
		}} {
			in := valid
			alter(&in)
			if _, e := r.Run(context.Background(), in); e == nil {
				t.Error("invalid input accepted")
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, e := r.Run(ctx, valid); e == nil {
			t.Error("canceled invocation accepted")
		}
	})
}

func TestUntrustedCodeZIP(t *testing.T) {
	valid := sourceZIP(t, "def handler(event, context): return 1")
	for _, entry := range []struct {
		code    []byte
		handler string
	}{{nil, "index.handler"}, {[]byte("invalid"), "index.handler"}, {valid, "../../host"}, {valid, "missing.handler"}, {bytes.Repeat([]byte{0}, MaxCodeBytes+1), "index.handler"}} {
		if _, e := ValidateCode(entry.code, entry.handler); e == nil {
			t.Error("invalid ZIP/handler accepted")
		}
	}
	for _, name := range []string{"../index.py", "/index.py", "a\\index.py", "index.so"} {
		var buffer bytes.Buffer
		z := zip.NewWriter(&buffer)
		f, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = f.Write([]byte("test"))
		_ = z.Close()
		if _, e = ValidateCode(buffer.Bytes(), "index.handler"); e == nil {
			t.Errorf("unsafe entry accepted: %s", name)
		}
	}
	for _, mode := range []string{"symlink", "duplicate", "bomb", "many"} {
		var buffer bytes.Buffer
		z := zip.NewWriter(&buffer)
		header := &zip.FileHeader{Name: "index.py", Method: zip.Deflate}
		if mode == "symlink" {
			header.SetMode(os.ModeSymlink | 0o777)
		}
		f, e := z.CreateHeader(header)
		if e != nil {
			t.Fatal(e)
		}
		data := []byte("source")
		if mode == "bomb" {
			data = bytes.Repeat([]byte("x"), MaxExpandedBytes+1)
		}
		_, _ = f.Write(data)
		if mode == "duplicate" {
			_, _ = z.Create("index.py")
		}
		if mode == "many" {
			for i := 0; i < 65; i++ {
				_, _ = z.Create(fmt.Sprintf("file%d.py", i))
			}
		}
		_ = z.Close()
		if _, e = ValidateCode(buffer.Bytes(), "index.handler"); e == nil {
			t.Errorf("unsafe ZIP accepted: %s", mode)
		}
	}
}

func TestCloseInterruptsSleepAndRejectsNewInvocations(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	ctx := experimental.WithFunctionListenerFactory(context.Background(), experimental.FunctionListenerFactoryFunc(func(def api.FunctionDefinition) experimental.FunctionListener {
		if def.ModuleName() == "wasi_snapshot_preview1" && def.Name() == "poll_oneoff" {
			return experimental.FunctionListenerFunc(func(context.Context, api.Module, api.FunctionDefinition, []uint64, experimental.StackIterator) {
				once.Do(func() { time.AfterFunc(100*time.Millisecond, func() { close(entered) }) })
			})
		}
		return nil
	}))
	r, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close(context.Background())
	in := Invocation{Code: sourceZIP(t, "import time\ndef handler(event,context):\n    time.sleep(3600)\n"), Handler: "index.handler", Event: json.RawMessage(`{}`), Timeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { _, err := r.Run(context.Background(), in); done <- err }()
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("real WASI sleep was not reached")
	}
	if err = r.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("sleep completed after shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("runtime close left a sleeping invocation")
	}
	if _, err = r.Run(context.Background(), in); err == nil {
		t.Fatal("closed runtime started a new guest")
	}
}
