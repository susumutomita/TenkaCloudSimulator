package appengine

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	pythonruntime "github.com/susumutomita/TenkaCloudSimulator/internal/workload/python"
)

type subjectKey struct{}

const project = "tenkacloud-local"

type harness struct {
	t       *testing.T
	db      *sql.DB
	path    string
	engine  *Engine
	mu      sync.Mutex
	objects map[string][]byte // bucket/object -> data
	now     time.Time
	runner  Runner
}

type options struct {
	runner Runner
	path   string
}

func newHarness(t *testing.T, opts ...options) *harness {
	t.Helper()
	o := options{}
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.path == "" {
		o.path = filepath.Join(t.TempDir(), "state.db")
	}
	store, err := core.New(o.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := &harness{t: t, db: store.Database(), path: o.path, objects: map[string][]byte{}, now: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), runner: o.runner}
	engine, err := New(store.Database(), h.config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	h.engine = engine
	return h
}

func (h *harness) config() Config {
	return Config{Project: project, Owner: "owner",
		Subject: func(ctx context.Context) (string, bool) { s, ok := ctx.Value(subjectKey{}).(string); return s, ok },
		Source: func(ctx context.Context, bucket, object string, limit int64) ([]byte, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			data, ok := h.objects[bucket+"/"+object]
			if !ok {
				return nil, ErrSourceNotFound
			}
			if int64(len(data)) > limit {
				return nil, ErrSourceTooLarge
			}
			return data, nil
		},
		Now: func() time.Time { return h.now }, Runner: h.runner}
}

func (h *harness) put(name string, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.objects[name] = data
}

type reply struct {
	status int
	body   map[string]any
	raw    string
	header http.Header
}

func (h *harness) call(subject, method, path string, body any) reply {
	h.t.Helper()
	var data []byte
	switch b := body.(type) {
	case nil:
	case string:
		data = []byte(b)
	default:
		data, _ = json.Marshal(b)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	if subject != "" {
		req = req.WithContext(context.WithValue(req.Context(), subjectKey{}, subject))
	}
	recorder := httptest.NewRecorder()
	h.engine.ServeHTTP(recorder, req)
	out := reply{status: recorder.Code, raw: recorder.Body.String(), header: recorder.Header()}
	_ = json.Unmarshal(recorder.Body.Bytes(), &out.body)
	return out
}

// ok calls as the owner and requires HTTP 200.
func (h *harness) ok(method, path string, body any) map[string]any {
	h.t.Helper()
	r := h.call("owner", method, path, body)
	if r.status != 200 {
		h.t.Fatalf("%s %s: %d %s", method, path, r.status, r.raw)
	}
	return r.body
}

// fails calls as the owner and requires the google.rpc.Status code.
func (h *harness) fails(code, method, path string, body any) string {
	h.t.Helper()
	r := h.call("owner", method, path, body)
	errBody, _ := r.body["error"].(map[string]any)
	if errBody == nil || errBody["status"] != code {
		h.t.Fatalf("%s %s: want %s, got %d %s", method, path, code, r.status, r.raw)
	}
	return errBody["message"].(string)
}

const base = "/appengine/v1/apps/" + project

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(content))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// app is a WSGI application that reports its version and request.
const app = `import os
def app(environ, start_response):
    body = ("%s/%s %s %s %s" % (os.environ["GAE_SERVICE"], os.environ["GAE_VERSION"], environ["REQUEST_METHOD"], environ["PATH_INFO"], environ["QUERY_STRING"])).encode()
    start_response("200 OK", [("Content-Type", "text/plain"), ("Set-Cookie", "x=1"), ("X-Custom", "a")])
    return [body]
`

func (h *harness) createApp(location string) {
	h.t.Helper()
	h.ok("POST", "/appengine/v1/apps", map[string]any{"id": project, "locationId": location})
}

func (h *harness) deploy(service, id string, files map[string]string) map[string]any {
	h.t.Helper()
	h.put("src/"+service+"-"+id+".zip", zipOf(h.t, files))
	return h.ok("POST", base+"/services/"+service+"/versions", map[string]any{"id": id, "runtime": "python312",
		"deployment": map[string]any{"zip": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/" + service + "-" + id + ".zip"}}})
}

// fakeRunner records invocations and answers with a fixed result.
type fakeRunner struct {
	mu     sync.Mutex
	calls  []pythonruntime.Invocation
	result pythonruntime.Result
	err    error
}

func (f *fakeRunner) Run(ctx context.Context, in pythonruntime.Invocation) (pythonruntime.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	return f.result, f.err
}

func wsgiResult(status string, headers [][2]string, body string) pythonruntime.Result {
	payload, _ := json.Marshal(map[string]any{"status": status, "headers": headers, "body": b64([]byte(body))})
	return pythonruntime.Result{Payload: payload}
}

func b64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }
