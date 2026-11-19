package server

// Recorder composition is a separate layer from the frozen socket oracle.
// Every request enters production NewAccess -> server -> provider registry;
// no actor injection, leaf direct call, fake DNS handler or socket is used.
import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type dnsHandlerHost struct {
	handler        http.Handler
	token, project string
}

func dnsHandlerStart(t *testing.T, project, letter string) dnsHandlerHost {
	t.Helper()
	cfg := DefaultCloudStorageConfig()
	cfg.GCPProject = project
	cfg.GCPOwner = "learner"
	if project == "project-two" {
		cfg.GCPProjectNumber = "100000000002"
	}
	h, closeState, err := NewWithCloudStorage(filepath.Join(t.TempDir(), "state.db"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeState(); err != nil {
			t.Error(err)
		}
	})
	token := strings.Repeat(letter, 64)
	access, err := NewAccess(h, token, false)
	if err != nil {
		t.Fatal(err)
	}
	return dnsHandlerHost{access, token, project}
}
func (h dnsHandlerHost) root() string {
	return "/_simulator/providers/gcp/dns/v1/projects/" + h.project + "/managedZones"
}
func (h dnsHandlerHost) call(t *testing.T, method, path, token, selected string, body any, want int) map[string]any {
	t.Helper()
	var text string
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		text = string(raw)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1:4566"+path, strings.NewReader(text))
	req.RemoteAddr = "127.0.0.1:32100"
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Simulator-Token", token)
	}
	if selected != "" {
		req.Header.Set("X-Simulator-Principal", selected)
	}
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, req)
	if recorder.Code != want {
		t.Fatalf("recorder %s %s: status=%d want=%d body=%q", method, path, recorder.Code, want, recorder.Body.String())
	}
	out := map[string]any{}
	if recorder.Body.Len() != 0 && want < 400 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}
func (h dnsHandlerHost) mint(t *testing.T, who string) string {
	t.Helper()
	out := h.call(t, "POST", "/_simulator/v1/access", h.token, "", map[string]string{"Principal": who}, 200)
	token, ok := out["AccessToken"].(string)
	if !ok || token == "" {
		t.Fatal("production middleware did not issue participant token")
	}
	return token
}
func (h dnsHandlerHost) create(t *testing.T, token string) {
	t.Helper()
	out := h.call(t, "POST", h.root(), token, "", map[string]any{"name": "handler-zone", "dnsName": "example.test.", "description": "handler composition oracle"}, 200)
	if out["name"] != "handler-zone" || out["dnsName"] != "example.test." || out["id"] == nil {
		t.Fatal("zone creation was vacuous", out)
	}
}
func (h dnsHandlerHost) snapshot(t *testing.T, token string) string {
	t.Helper()
	base := h.root() + "/handler-zone"
	return dnsCompositionJSON([]any{h.call(t, "GET", h.root(), token, "", nil, 200), h.call(t, "GET", base, token, "", nil, 200), h.call(t, "GET", base+"/rrsets", token, "", nil, 200), h.call(t, "GET", base+"/changes", token, "", nil, 200)})
}
func (h dnsHandlerHost) record(t *testing.T, token, ip string) {
	t.Helper()
	base := h.root() + "/handler-zone"
	want := map[string]string{}
	if ip != "" {
		want["www.example.test."] = ip
	}
	if err := dnsCompositionRecords(h.call(t, "GET", base+"/rrsets", token, "", nil, 200), want); err != nil {
		t.Fatal(err)
	}
	if ip != "" {
		out := h.call(t, "GET", base+"/rrsets/www.example.test./A", token, "", nil, 200)
		if out["name"] != "www.example.test." || out["ttl"] != float64(300) || dnsCompositionJSON(out["rrdatas"]) != dnsCompositionJSON([]string{ip}) {
			t.Fatal("wrong get data", out)
		}
	}
}
func TestGCPDNSHandlerCompositionRouteCRUD(t *testing.T) {
	h := dnsHandlerStart(t, "project-one", "a")
	owner := h.mint(t, "learner")
	h.create(t, owner)
	base := h.root() + "/handler-zone"
	a, b := dnsCompositionA("192.0.2.1"), dnsCompositionA("192.0.2.2")
	h.record(t, owner, "")
	receipt := h.call(t, "POST", base+"/changes", owner, "", dnsCompositionChange([]any{a}, nil), 200)
	id, ok := receipt["id"].(string)
	if !ok || id == "" || receipt["status"] != "done" {
		t.Fatal("no completed change")
	}
	if dnsCompositionJSON(h.call(t, "GET", base+"/changes/"+id, owner, "", nil, 200)) != dnsCompositionJSON(receipt) {
		t.Fatal("change get mismatch")
	}
	if dnsCompositionJSON(h.call(t, "GET", base+"/changes", owner, "", nil, 200)["changes"]) != dnsCompositionJSON([]any{receipt}) {
		t.Fatal("change list mismatch")
	}
	h.record(t, owner, "192.0.2.1")
	h.call(t, "POST", base+"/changes", owner, "", dnsCompositionChange([]any{b}, []any{a}), 200)
	h.record(t, owner, "192.0.2.2")
	h.call(t, "DELETE", base, owner, "", nil, 400)
	h.call(t, "POST", base+"/changes", owner, "", dnsCompositionChange(nil, []any{b}), 200)
	h.record(t, owner, "")
	h.call(t, "GET", base+"/rrsets/www.example.test./A", owner, "", nil, 404)
	h.call(t, "DELETE", base, owner, "", nil, 200)
	h.call(t, "GET", base, owner, "", nil, 404)
	if dnsCompositionJSON(h.call(t, "GET", h.root(), owner, "", nil, 200)["managedZones"]) != "[]" {
		t.Fatal("cleanup retained zone")
	}
}
func TestGCPDNSHandlerCompositionProjectOwnerState(t *testing.T) {
	a, b := dnsHandlerStart(t, "project-one", "b"), dnsHandlerStart(t, "project-two", "c")
	ownerA, ownerB, outsider := a.mint(t, "learner"), b.mint(t, "learner"), a.mint(t, "outsider")
	for i, h := range []dnsHandlerHost{a, b} {
		token := []string{ownerA, ownerB}[i]
		h.create(t, token)
		h.call(t, "POST", h.root()+"/handler-zone/changes", token, "", dnsCompositionChange([]any{dnsCompositionA([]string{"192.0.2.1", "192.0.2.9"}[i])}, nil), 200)
	}
	beforeA, beforeB := a.snapshot(t, ownerA), b.snapshot(t, ownerB)
	base := a.root() + "/handler-zone"
	check := func() {
		if a.snapshot(t, ownerA) != beforeA || b.snapshot(t, ownerB) != beforeB {
			t.Fatal("denial changed a project")
		}
	}
	for _, tc := range []struct {
		token, selected, path string
		code                  int
	}{
		{a.token, "", base + "/changes", 403}, {outsider, "", base + "/changes", 403}, {outsider, "learner", base + "/changes", 403},
		{ownerA, "simulator-admin", base + "/changes", 403}, {ownerB, "", base + "/changes", 401},
		{ownerA, "", strings.Replace(base, "project-one", "project-two", 1) + "/changes", 403},
	} {
		a.call(t, "POST", tc.path, tc.token, tc.selected, dnsCompositionChange(nil, []any{dnsCompositionA("192.0.2.1")}), tc.code)
		check()
	}
	a.call(t, "GET", strings.Replace(base, "project-one", "project-two", 1), ownerA, "", nil, 403)
	check()
	b.record(t, ownerB, "192.0.2.9")
	for i, h := range []dnsHandlerHost{a, b} {
		token := []string{ownerA, ownerB}[i]
		h.call(t, "POST", h.root()+"/handler-zone/changes", token, "", dnsCompositionChange(nil, []any{dnsCompositionA([]string{"192.0.2.1", "192.0.2.9"}[i])}), 200)
		h.call(t, "DELETE", h.root()+"/handler-zone", token, "", nil, 200)
	}
}
func TestGCPDNSHandlerAuthenticationBeforeDispatch(t *testing.T) {
	a, b := dnsHandlerStart(t, "project-one", "d"), dnsHandlerStart(t, "project-two", "e")
	owner, outsider := a.mint(t, "learner"), a.mint(t, "outsider")
	for _, token := range []string{"", "invalid", b.token} {
		a.call(t, "GET", a.root(), token, "", nil, 401)
	}
	a.call(t, "GET", a.root(), outsider, "learner", nil, 403)
	a.call(t, "GET", a.root(), owner, "simulator-admin", nil, 403)
	// Positive authenticated control is a supported existing GCP route, not DNS.
	out := a.call(t, "GET", "/_simulator/providers/gcp/storage/v1/b?project=project-one", owner, "", nil, 200)
	if out["kind"] != "storage#buckets" {
		t.Fatal("GCP authenticated control missing", out)
	}
}
func TestGCPDNSHandlerExistingGCPRoutes(t *testing.T) {
	h := dnsHandlerStart(t, "project-one", "f")
	owner := h.mint(t, "learner")
	root := "/_simulator/providers/gcp"
	h.call(t, "POST", root+"/storage/v1/b?project=project-one", owner, "", map[string]string{"name": "handler-existing-bucket"}, 200)
	out := h.call(t, "GET", root+"/storage/v1/b/handler-existing-bucket", owner, "", nil, 200)
	if out["name"] != "handler-existing-bucket" {
		t.Fatal(out)
	}
	h.call(t, "DELETE", root+"/storage/v1/b/handler-existing-bucket", owner, "", nil, 204)
	catalog := h.call(t, "GET", root+"/compute/compute/v1/projects/project-one/global/backendBuckets", owner, "", nil, 200)
	if len(catalog) == 0 {
		t.Fatal("empty catalog response")
	}
	app := h.call(t, "GET", root+"/appengine/v1/apps/project-one/locations", owner, "", nil, 200)
	locations, ok := app["locations"].([]any)
	if !ok || len(locations) == 0 {
		t.Fatal("App Engine route lost", app)
	}
}
