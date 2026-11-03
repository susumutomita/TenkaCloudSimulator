package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestAdministrativeProtocolAndStateFailures(t *testing.T) {
	s := instance(t)
	policy := `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:ListBucket","Resource":"*"}}`
	status(t, s, "PUT", "/_simulator/v1/iam/principals/reader/policy", policy, 200, nil)
	var names struct {
		Principals []string `json:"principals"`
	}
	if err := json.Unmarshal(status(t, s, "GET", "/_simulator/v1/iam/principals", "", 200, nil), &names); err != nil || len(names.Principals) != 2 {
		t.Fatalf("actual identities not listed: %v %v", names, err)
	}
	var saved core.Policy
	if err := json.Unmarshal(status(t, s, "GET", "/_simulator/v1/iam/principals/reader/policy", "", 200, nil), &saved); err != nil || len(saved.Statement) != 1 || saved.Statement[0].Action[0] != "s3:ListBucket" {
		t.Fatalf("stored policy changed: %v %v", saved, err)
	}
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{"POST", "/_simulator/health", "", 405}, {"POST", "/_simulator/v1/capabilities", "", 405},
		{"GET", "/_simulator/v1/unknown", "", 404}, {"POST", "/_simulator/v1/iam/principals", "", 405},
		{"GET", "/_simulator/v1/iam/principals/missing/policy", "", 404},
		{"PUT", "/_simulator/v1/iam/principals/simulator-admin/policy", policy, 400},
		{"PATCH", "/_simulator/v1/iam/principals/reader/policy", policy, 405},
		{"PUT", "/_simulator/v1/iam/principals/reader/policy", strings.Repeat(" ", 32769), 413},
	} {
		status(t, s, tc.method, tc.path, tc.body, tc.code, nil)
	}
	status(t, s, "GET", "/", "", 403, map[string]string{"Origin": "null"})
	status(t, s, "GET", "/", "", 403, map[string]string{"Origin": "ftp://localhost"})
	h, closeState, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err = closeState(); err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct{ method, path, body string }{{"GET", "/_simulator/v1/iam/principals", ""}, {"PUT", "/_simulator/v1/iam/principals/reader/policy", policy}, {"GET", "/_simulator/v1/iam/principals/reader/policy", ""}, {"DELETE", "/_simulator/v1/iam/principals/reader/policy", ""}, {"GET", "/", ""}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(request.method, request.path, strings.NewReader(request.body))
		h.ServeHTTP(w, r)
		if w.Code != 500 {
			t.Fatalf("closed state returned %d for %s", w.Code, request.path)
		}
	}
	path := filepath.Join(t.TempDir(), "state.db")
	persistent, cleanup, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	w := httptest.NewRecorder()
	persistent.ServeHTTP(w, httptest.NewRequest("GET", "/_simulator/v1/capabilities", nil))
	if !strings.Contains(w.Body.String(), `"persistence":"file"`) {
		t.Fatal(w.Body.String())
	}
	if _, _, err = New(t.TempDir()); err == nil {
		t.Fatal("invalid database path was accepted")
	}
}
func TestS3UnsupportedInputAndConditionalFailures(t *testing.T) {
	s := instance(t)
	status(t, s, "PUT", "/boundary-bucket", "", 200, nil)
	for _, tc := range []struct {
		method, path, body string
		code               int
		headers            map[string]string
	}{
		{"POST", "/", "", 405, nil}, {"POST", "/boundary-bucket", "", 405, nil}, {"PATCH", "/boundary-bucket/file", "", 405, nil},
		{"GET", "/?prefix=invalid", "", 501, nil}, {"PUT", "/boundary-bucket?list-type=2", "", 501, nil}, {"GET", "/boundary-bucket/file?location", "", 501, nil},
		{"PUT", "/boundary-bucket?location", "", 501, nil},
		{"GET", "/", "", 501, map[string]string{"X-Amz-Security-Token": "dummy"}},
		{"GET", "/?X-Amz-Credential=dummy", "", 501, nil},
		{"GET", "/", "", 501, map[string]string{"x-amz-unknown-configuration": "value"}},
		{"GET", "/", "", 501, map[string]string{"x-amz-request-payer": "requester"}},
		{"PUT", "/boundary-bucket/invalid%00key", "body", 400, nil},
		{"PUT", "/boundary-bucket/" + strings.Repeat("a", 1025), "body", 400, nil},
		{"PUT", "/huge-configuration", strings.Repeat("x", 4097), 400, nil},
		{"PUT", "/boundary-bucket/huge", strings.Repeat("x", core.MaxObjectBytes+1), 413, nil},
		{"PUT", "/boundary-bucket/file", "data", 501, map[string]string{"x-amz-acl": "public-read"}},
		{"GET", "/missing-bucket?location", "", 404, nil},
		{"GET", "/boundary-bucket?list-type=2&prefix=%00", "", 400, nil},
		{"GET", "/boundary-bucket?list-type=2&encoding-type=unsupported", "", 400, nil},
		{"GET", "/boundary-bucket?list-type=2&continuation-token=not-a-cursor", "", 400, nil},
	} {
		status(t, s, tc.method, tc.path, tc.body, tc.code, tc.headers)
	}
	status(t, s, "PUT", "/boundary-bucket", "", 409, nil)
	status(t, s, "GET", "/boundary-bucket?location", "", 200, nil)
	status(t, s, "GET", "/", "", 501, map[string]string{"X-Amz-Content-Sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER"})
	status(t, s, "PUT", "/boundary-bucket/%FF", "value", 400, nil)
	status(t, s, "PUT", "/boundary-bucket/file", "hello world", 200, nil)
	status(t, s, "PUT", "/boundary-bucket/file", "replacement", 200, map[string]string{"If-Match": "*"})
	status(t, s, "GET", "/boundary-bucket/file", "", 412, map[string]string{"If-Match": `"wrong"`})
	for _, bad := range []string{"bytes=99-100", "bytes=-0", "items=0-2", "bytes=3-1", "bytes=0-1,3-4", "bytes=nope-3"} {
		status(t, s, "GET", "/boundary-bucket/file", "", 416, map[string]string{"Range": bad})
	}
	for _, good := range []string{"bytes=-3", "bytes=2-"} {
		r, b := call(t, s, "GET", "/boundary-bucket/file", "", map[string]string{"Range": good})
		if r.StatusCode != http.StatusPartialContent || len(b) == 0 {
			t.Fatalf("valid range %q: %d %q", good, r.StatusCode, b)
		}
	}
	status(t, s, "DELETE", "/missing-bucket/file", "", 404, nil)
}
