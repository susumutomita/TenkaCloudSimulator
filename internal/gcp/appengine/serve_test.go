package appengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pythonruntime "github.com/susumutomita/TenkaCloudSimulator/internal/workload/python"
)

// request sends a data-plane request from the given client address.
func (h *harness) request(method, host, path, remote string, body string, header http.Header) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, "http://127.0.0.1:4566/appspot/"+host+path, strings.NewReader(body))
	req.RemoteAddr = remote
	for k, v := range header {
		req.Header[k] = v
	}
	req = req.WithContext(context.WithValue(req.Context(), subjectKey{}, "participant"))
	w := httptest.NewRecorder()
	h.engine.ServeHTTP(w, req)
	return w
}

const echo = `import json, os
def app(environ, start_response):
    if environ["PATH_INFO"] == "/fail":
        raise ValueError("deliberate failure")
    payload = {"service": os.environ["GAE_SERVICE"], "version": os.environ["GAE_VERSION"], "project": os.environ["GOOGLE_CLOUD_PROJECT"],
               "greeting": os.environ.get("GREETING"), "method": environ["REQUEST_METHOD"], "path": environ["PATH_INFO"],
               "script": environ["SCRIPT_NAME"], "query": environ["QUERY_STRING"], "body": environ["wsgi.input"].read().decode(),
               "type": environ.get("CONTENT_TYPE"), "cookie": environ.get("HTTP_COOKIE"), "token": environ.get("HTTP_X_SIMULATOR_TOKEN"),
               "auth": environ.get("HTTP_AUTHORIZATION"), "custom": environ.get("HTTP_X_TRACE"), "remote": environ["REMOTE_ADDR"]}
    start_response("201 Created", [("Content-Type", "application/json"), ("Set-Cookie", "session=1"), ("X-Custom", "1"),
                                   ("Content-Security-Policy", "script-src *"), ("ETag", "abc")])
    return [json.dumps(payload).encode()]
`

func TestDataPlaneRunsTheDeployedWSGIVersionInTheEmbeddedPythonRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the embedded interpreter")
	}
	h := newHarness(t)
	h.createApp("asia-northeast1")
	h.put("src/echo.zip", zipOf(t, map[string]string{"main.py": echo}))
	h.ok("POST", base+"/services/default/versions", map[string]any{"id": "v1", "runtime": "python312", "envVariables": map[string]any{"GREETING": "hello"},
		"deployment": map[string]any{"zip": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/echo.zip"}}})
	<-h.engine.warm()
	header := http.Header{"Content-Type": {"text/plain"}, "Cookie": {"tcsim_abc=secret; app=1"}, "X-Simulator-Token": {"secret"},
		"Authorization": {"Bearer secret"}, "X-Trace": {"t1"}}
	w := h.request("POST", project+".an.r.appspot.com", "/a%20b/c?x=1&y=2", "192.0.2.10:5555", "posted", header)
	if w.Code != 201 {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err, w.Body.String())
	}
	want := map[string]any{"service": "default", "version": "v1", "project": project, "greeting": "hello", "method": "POST", "path": "/a b/c",
		"script": DataPlanePrefix + project + ".an.r.appspot.com", "query": "x=1&y=2", "body": "posted", "type": "text/plain", "cookie": "app=1",
		"token": nil, "auth": nil, "custom": "t1", "remote": "192.0.2.10"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %v want %v", k, got[k], v)
		}
	}
	if w.Header().Get("Set-Cookie") != "" || w.Header().Get("X-Custom") != "" || w.Header().Get("ETag") != "abc" ||
		w.Header().Get("Content-Security-Policy") != dataPlaneSecurity || w.Header().Get("X-Simulator-AppEngine-Version") != "default/v1" {
		t.Fatalf("response headers: %v", w.Header())
	}
	// A raised exception is an HTTP 500 naming the error, as a local aid.
	if w := h.request("GET", "v1-dot-"+project+".appspot.com", "/fail", "192.0.2.10:1", "", nil); w.Code != 500 || !strings.Contains(w.Body.String(), "ValueError: deliberate failure") {
		t.Fatalf("failure: %d %s", w.Code, w.Body.String())
	}
	// HEAD returns headers only.
	if w := h.request("HEAD", project+".appspot.com", "", "192.0.2.10:1", "", nil); w.Code != 201 || w.Body.Len() != 0 {
		t.Fatalf("head: %d %q", w.Code, w.Body.String())
	}
}

func TestRequestsReachVersionsThroughTheirHostnameSplitAndFirewall(t *testing.T) {
	runner := &fakeRunner{result: wsgiResult("200 OK", [][2]string{{"Content-Type", "text/plain"}}, "ok")}
	h := newHarness(t, options{runner: runner})
	host := project + ".uc.r.appspot.com"
	if w := h.request("GET", host, "/", "127.0.0.1:1", "", nil); w.Code != 404 {
		t.Fatalf("no application: %d", w.Code)
	}
	h.createApp("us-central")
	if w := h.request("GET", host, "/", "127.0.0.1:1", "", nil); w.Code != 404 {
		t.Fatalf("no default service: %d", w.Code)
	}
	h.deploy("default", "v1", map[string]string{"main.py": app})
	h.deploy("default", "v2", map[string]string{"main.py": app})
	h.deploy("api", "v1", map[string]string{"main.py": app})
	version := func(w *httptest.ResponseRecorder) string { return w.Header().Get("X-Simulator-AppEngine-Version") }
	for hostname, want := range map[string]string{
		host: "default/v1", project + ".appspot.com": "default/v1", "API-dot-" + project + ".appspot.com": "api/v1",
		"v2-dot-" + project + ".uc.r.appspot.com": "default/v2", "v1-dot-api-dot-" + project + ".appspot.com": "api/v1",
	} {
		if w := h.request("GET", hostname, "/", "127.0.0.1:1", "", nil); w.Code != 200 || version(w) != want {
			t.Errorf("%s: %d %q", hostname, w.Code, version(w))
		}
	}
	for _, hostname := range []string{"other.appspot.com", project + ".example.com", "v9-dot-" + project + ".appspot.com", "v9-dot-api-dot-" + project + ".appspot.com",
		"v1-dot-web-dot-" + project + ".appspot.com", "a-dot-b-dot-c-dot-" + project + ".appspot.com", project + ".ew.r.appspot.com"} {
		if w := h.request("GET", hostname, "/", "127.0.0.1:1", "", nil); w.Code != 404 {
			t.Errorf("%s: %d", hostname, w.Code)
		}
	}
	if w := h.request("GET", "", "", "127.0.0.1:1", "", nil); w.Code != 404 {
		t.Fatalf("missing hostname: %d", w.Code)
	}
	// IP splits are stable per client; cookie splits follow GOOGAPPUID.
	h.ok("PATCH", base+"/services/default?updateMask=split", map[string]any{"split": map[string]any{"shardBy": "COOKIE", "allocations": map[string]any{"v1": 0.3, "v2": 0.7}}})
	for cookie, want := range map[string]string{"0": "default/v1", "299": "default/v1", "300": "default/v2", "999": "default/v2"} {
		w := h.request("GET", host, "/", "127.0.0.1:1", "", http.Header{"Cookie": {"GOOGAPPUID=" + cookie}})
		if version(w) != want || w.Header().Get("Set-Cookie") != "" {
			t.Errorf("cookie %s: %q %v", cookie, version(w), w.Header())
		}
	}
	w := h.request("GET", host, "/", "127.0.0.1:1", "", http.Header{"Cookie": {"GOOGAPPUID=1000"}})
	if !strings.HasPrefix(w.Header().Get("Set-Cookie"), "GOOGAPPUID=") || !strings.Contains(w.Header().Get("Set-Cookie"), "Path="+DataPlanePrefix+host+"/") {
		t.Fatalf("new cookie: %v", w.Header())
	}
	if strings.Contains(w.Header().Get("Set-Cookie"), "Secure") {
		t.Fatalf("a plain-HTTP shard cookie must not be Secure: %v", w.Header())
	}
	secure := httptest.NewRequest("GET", "https://127.0.0.1/appspot/"+host+"/", nil)
	secure.RemoteAddr = "127.0.0.1:1"
	secure = secure.WithContext(context.WithValue(secure.Context(), subjectKey{}, "participant"))
	rec := httptest.NewRecorder()
	h.engine.ServeHTTP(rec, secure)
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "; Secure") {
		t.Fatalf("a TLS shard cookie must be Secure: %v", rec.Header())
	}
	h.ok("PATCH", base+"/services/default?updateMask=split", map[string]any{"split": map[string]any{"shardBy": "IP", "allocations": map[string]any{"v1": 0.5, "v2": 0.5}}})
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		remote := fmt.Sprintf("10.0.%d.%d:1", i/8, i%8+1)
		first, second := version(h.request("GET", host, "/", remote, "", nil)), version(h.request("GET", host, "/", remote, "", nil))
		if first != second {
			t.Fatalf("IP split is not stable for %s", remote)
		}
		seen[first] = true
	}
	if !seen["default/v1"] || !seen["default/v2"] {
		t.Fatalf("IP split never chose both versions: %v", seen)
	}
	h.ok("PATCH", base+"/services/default?updateMask=split", map[string]any{"split": map[string]any{"shardBy": "RANDOM", "allocations": map[string]any{"v2": 1}}})
	if version(h.request("GET", host, "/", "127.0.0.1:1", "", nil)) != "default/v2" {
		t.Fatal("random split with one version")
	}
	// The firewall decides before any code runs.
	calls := len(runner.calls)
	h.ok("POST", base+"/firewall/ingressRules", map[string]any{"priority": 1, "action": "DENY", "sourceRange": "192.0.2.0/24"})
	if w := h.request("GET", host, "/", "192.0.2.9:1", "", nil); w.Code != 403 || len(runner.calls) != calls {
		t.Fatalf("firewall deny: %d", w.Code)
	}
	if w := h.request("GET", host, "/", "127.0.0.1:1", "", nil); w.Code != 200 {
		t.Fatalf("other address: %d", w.Code)
	}
	if w := h.request("GET", host, "/", "unknown", "", nil); w.Code != 403 {
		t.Fatalf("unknown address: %d", w.Code)
	}
	// Stopped versions and disabled applications do not serve.
	h.ok("PATCH", base+"/services/default/versions/v2?updateMask=servingStatus", map[string]any{"servingStatus": "STOPPED"})
	if w := h.request("GET", host, "/", "127.0.0.1:1", "", nil); w.Code != 503 {
		t.Fatalf("stopped: %d", w.Code)
	}
	h.ok("PATCH", base+"?updateMask=servingStatus", map[string]any{"servingStatus": "USER_DISABLED"})
	if w := h.request("GET", "v1-dot-api-dot-"+project+".appspot.com", "/", "127.0.0.1:1", "", nil); w.Code != 503 {
		t.Fatalf("disabled: %d", w.Code)
	}
	// The data plane needs an authenticated session.
	req := httptest.NewRequest("GET", "/appspot/"+host+"/", nil)
	rec = httptest.NewRecorder()
	h.engine.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("anonymous: %d", rec.Code)
	}
}

func TestInvalidWSGIResponsesAndSandboxFailuresAreGatewayErrors(t *testing.T) {
	runner := &fakeRunner{}
	h := newHarness(t, options{runner: runner})
	h.createApp("us-central")
	h.deploy("default", "v1", map[string]string{"main.py": app})
	host := project + ".appspot.com"
	for name, c := range map[string]struct {
		result pythonruntime.Result
		err    error
		code   int
	}{
		"deadline":      {err: context.DeadlineExceeded, code: 504},
		"sandbox":       {err: errors.New("exited"), code: 502},
		"exception":     {result: pythonruntime.Result{FunctionError: true, Payload: json.RawMessage(`{"errorType":"KeyError","errorMessage":"x"}`)}, code: 500},
		"not json":      {result: pythonruntime.Result{Payload: json.RawMessage(`"text"`)}, code: 502},
		"status text":   {result: wsgiResult("OK", nil, ""), code: 502},
		"informational": {result: wsgiResult("101 Switching", nil, ""), code: 502},
		"body":          {result: pythonruntime.Result{Payload: json.RawMessage(`{"status":"200 OK","headers":[],"body":"%%%"}`)}, code: 502},
		"redirect":      {result: wsgiResult("302 Found", [][2]string{{"Location", "https://example.com/"}}, ""), code: 302},
		"default type":  {result: wsgiResult("200 OK", [][2]string{{"Bad Header", "x"}, {"Content-Language", "ja\r\nX: y"}}, "<p>"), code: 200},
	} {
		runner.result, runner.err = c.result, c.err
		w := h.request("GET", host, "/", "127.0.0.1:1", "", nil)
		if w.Code != c.code {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if name == "redirect" && w.Header().Get("Location") != "" {
			t.Error("Location must not leave the simulator origin")
		}
		if name == "default type" && (w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Content-Language") != "") {
			t.Errorf("headers: %v", w.Header())
		}
	}
	runner.result, runner.err = wsgiResult("200 OK", nil, "ok"), nil
	if w := h.request("POST", host, "/", "127.0.0.1:1", strings.Repeat("x", maxRequestBodyBytes+1), nil); w.Code != 413 {
		t.Fatalf("large body: %d", w.Code)
	}
	if w := h.request("GET", host, "/", "127.0.0.1:1", "", http.Header{"X-Big": {strings.Repeat("x", maxHeaderBytes)}}); w.Code != 431 {
		t.Fatalf("large headers: %d", w.Code)
	}
	last := runner.calls[len(runner.calls)-1]
	var event map[string]any
	_ = json.Unmarshal(last.Event, &event)
	if last.Handler != "_tcsim_appengine.handle" || last.Timeout != 10*time.Second || last.Environment["GAE_ENV"] != "standard" || event["path"] != "/" {
		t.Fatalf("invocation: %+v %v", last, event)
	}
	if _, err := pythonruntime.ValidateCode(last.Code, last.Handler); err != nil {
		t.Fatalf("bundle: %v", err)
	}
	// A corrupt stored version is an internal error, not guest output.
	if _, err := h.db.Exec("UPDATE gcp_appengine_versions SET code = x'00'"); err != nil {
		t.Fatal(err)
	}
	if w := h.request("GET", host, "/", "127.0.0.1:1", "", nil); w.Code != 500 {
		t.Fatalf("corrupt code: %d", w.Code)
	}
}

func TestClosedEngineRefusesToRunVersions(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	h.createApp("us-central")
	h.deploy("default", "v1", map[string]string{"main.py": app})
	if err := h.engine.Close(); err != nil {
		t.Fatal(err)
	}
	if w := h.request("GET", project+".appspot.com", "/", "127.0.0.1:1", "", nil); w.Code != 503 {
		t.Fatalf("closed: %d %s", w.Code, w.Body.String())
	}
	if _, err := h.engine.python(context.Background()); err == nil {
		t.Fatal("a closed engine started a runtime")
	}
	<-h.engine.warm()
}

func TestRuntimeStartsOnceAndAWaitingRequestHonoursItsContext(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the embedded interpreter")
	}
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.engine.python(ctx); err == nil {
		t.Fatal("a cancelled request waited for the runtime")
	}
	first, err := h.engine.python(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.engine.python(context.Background())
	if err != nil || first != second {
		t.Fatal("the runtime started twice")
	}
	if err := h.engine.Close(); err != nil {
		t.Fatal(err)
	}
}
