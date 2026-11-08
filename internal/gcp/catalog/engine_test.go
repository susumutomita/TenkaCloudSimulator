package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type subjectKey struct{}

type harness struct {
	t      *testing.T
	engine *Engine
	now    time.Time
}

func newHarness(t *testing.T, apis ...*API) *harness {
	t.Helper()
	store, err := core.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if len(apis) == 0 {
		apis = APIs()
	}
	h := &harness{t: t, now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
	engine, err := New(store.Database(), Config{Project: "tenkacloud-local", ProjectNumber: "100000000001", Owner: "owner", Locations: []string{"us-central1", "asia-northeast1"},
		Subject: func(ctx context.Context) (string, bool) { s, ok := ctx.Value(subjectKey{}).(string); return s, ok }, Now: func() time.Time { return h.now }}, apis)
	if err != nil {
		t.Fatal(err)
	}
	h.engine = engine
	return h
}

type reply struct {
	status int
	body   map[string]any
	raw    string
}

func (h *harness) call(subject, method, path string, body any) reply {
	h.t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	if subject != "" {
		req = req.WithContext(context.WithValue(req.Context(), subjectKey{}, subject))
	}
	recorder := httptest.NewRecorder()
	h.engine.ServeHTTP(recorder, req)
	out := reply{status: recorder.Code, raw: recorder.Body.String()}
	_ = json.Unmarshal(recorder.Body.Bytes(), &out.body)
	return out
}

func (r reply) code() string {
	if e, ok := r.body["error"].(map[string]any); ok {
		s, _ := e["status"].(string)
		return s
	}
	return ""
}

func expect(t *testing.T, r reply, status int, code string) {
	t.Helper()
	if r.status != status || code != "" && r.code() != code {
		t.Fatalf("want %d %s, got %d %s: %s", status, code, r.status, r.code(), r.raw)
	}
}

const loc = "/alloydb/v1/projects/tenkacloud-local/locations/us-central1"

func TestAlloyDBLifecycleWithOperationsAndChildren(t *testing.T) {
	h := newHarness(t)
	created := h.call("owner", "POST", loc+"/clusters?clusterId=main&alt=json", map[string]any{"networkConfig": map[string]any{"network": "projects/tenkacloud-local/global/networks/default"}, "labels": map[string]any{"env": "dev"}, "state": "FAILED"})
	expect(t, created, 200, "")
	if created.body["done"] != true || !strings.HasPrefix(created.body["name"].(string), "projects/tenkacloud-local/locations/us-central1/operations/operation-") {
		t.Fatalf("operation %v", created.body)
	}
	response := created.body["response"].(map[string]any)
	if response["@type"] != "type.googleapis.com/google.cloud.alloydb.v1.Cluster" || response["state"] != "READY" || response["name"] != "projects/tenkacloud-local/locations/us-central1/clusters/main" {
		t.Fatalf("response %v", response)
	}
	op := h.call("owner", "GET", "/alloydb/v1/"+created.body["name"].(string), nil)
	expect(t, op, 200, "")
	if ops := h.call("owner", "GET", loc+"/operations", nil).body["operations"].([]any); len(ops) != 1 {
		t.Fatalf("operations %v", ops)
	}
	expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=main", map[string]any{"network": "n"}), 409, "ALREADY_EXISTS")
	expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=Bad_ID", map[string]any{"network": "n"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=x", map[string]any{}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=x", map[string]any{"network": "n", "bogus": 1}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", loc+"/clusters", map[string]any{"network": "n"}), 400, "INVALID_ARGUMENT")
	got := h.call("owner", "GET", loc+"/clusters/main", nil)
	expect(t, got, 200, "")
	if got.body["labels"].(map[string]any)["env"] != "dev" || got.body["state"] != "READY" {
		t.Fatalf("output-only state must be server-assigned: %v", got.body)
	}
	instance := h.call("owner", "POST", loc+"/clusters/main/instances?instanceId=primary", map[string]any{"instanceType": "PRIMARY"})
	expect(t, instance, 200, "")
	expect(t, h.call("owner", "POST", loc+"/clusters/missing/instances?instanceId=p", map[string]any{"instanceType": "PRIMARY"}), 404, "NOT_FOUND")
	expect(t, h.call("owner", "POST", loc+"/clusters/main/users?userId=app", map[string]any{"password": "secret"}), 400, "INVALID_ARGUMENT")
	user := h.call("owner", "POST", loc+"/clusters/main/users?userId=app", map[string]any{"databaseRoles": []any{"alloydbsuperuser"}})
	expect(t, user, 200, "")
	if user.body["name"] != "projects/tenkacloud-local/locations/us-central1/clusters/main/users/app" || user.body["done"] != nil {
		t.Fatalf("non-LRO create %v", user.body)
	}
	patched := h.call("owner", "PATCH", loc+"/clusters/main?updateMask=displayName", map[string]any{"displayName": "Main"})
	expect(t, patched, 200, "")
	expect(t, h.call("owner", "PATCH", loc+"/clusters/main?updateMask=databaseVersion", map[string]any{"databaseVersion": "POSTGRES_15"}), 400, "INVALID_ARGUMENT")
	if h.call("owner", "GET", loc+"/clusters/main", nil).body["displayName"] != "Main" {
		t.Fatal("patch did not persist")
	}
	expect(t, h.call("owner", "DELETE", loc+"/clusters/main", nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "POST", loc+"/clusters/main/instances/primary:restart", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "POST", loc+"/clusters/main:restart", map[string]any{}), 404, "NOT_FOUND")
	expect(t, h.call("owner", "GET", loc+"/clusters?filter=state%3DREADY", nil), 400, "INVALID_ARGUMENT")
	page := h.call("owner", "GET", loc+"/clusters/main/instances?pageSize=1", nil)
	if items := page.body["instances"].([]any); len(items) != 1 {
		t.Fatalf("instances %v", page.body)
	}
	deleted := h.call("owner", "DELETE", loc+"/clusters/main?force=true", nil)
	expect(t, deleted, 200, "")
	if deleted.body["response"].(map[string]any)["@type"] != "type.googleapis.com/google.protobuf.Empty" {
		t.Fatalf("delete operation %v", deleted.body)
	}
	expect(t, h.call("owner", "GET", loc+"/clusters/main/users/app", nil), 404, "NOT_FOUND")
}

func TestProjectOwnerAndLocationAuthority(t *testing.T) {
	h := newHarness(t)
	expect(t, h.call("", "GET", loc+"/clusters", nil), 401, "UNAUTHENTICATED")
	expect(t, h.call("intruder", "GET", loc+"/clusters", nil), 403, "PERMISSION_DENIED")
	expect(t, h.call("owner", "GET", "/alloydb/v1/projects/other-project/locations/us-central1/clusters", nil), 403, "PERMISSION_DENIED")
	expect(t, h.call("owner", "GET", "/alloydb/v1/projects/tenkacloud-local/locations/mars-1/clusters", nil), 400, "INVALID_ARGUMENT")
	created := h.call("owner", "POST", "/alloydb/v1/projects/100000000001/locations/us-central1/clusters?clusterId=num", map[string]any{"network": "n"})
	expect(t, created, 200, "")
	if created.body["response"].(map[string]any)["name"] != "projects/tenkacloud-local/locations/us-central1/clusters/num" {
		t.Fatalf("project numbers normalize to the project ID: %v", created.body)
	}
	expect(t, h.call("owner", "GET", "/alloydb/v1/projects/tenkacloud-local/locations/asia-northeast1/clusters/num", nil), 404, "NOT_FOUND")
	expect(t, h.call("owner", "GET", "/nothing/v1/x", nil), 404, "NOT_FOUND")
	expect(t, h.call("owner", "GET", loc+"/clusters?alt=proto", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", loc+"/clusters?unknown=1", nil), 400, "INVALID_ARGUMENT")
}
