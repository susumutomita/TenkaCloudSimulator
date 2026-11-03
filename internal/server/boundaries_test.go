package server

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

// Unsupported requests must fail without changing the shared resource state.
func TestUnsupportedWireOptionsDoNotMutate(t *testing.T) {
	s := instance(t)
	status(t, s, "PUT", "/boundary-bucket", "", 200, nil)
	status(t, s, "PUT", "/boundary-bucket/key", "original", 200, nil)
	cases := []struct {
		method, path, body string
		code               int
		headers            map[string]string
	}{
		{"POST", "/", "", 405, nil},
		{"GET", "/?location", "", 501, nil},
		{"GET", "/", "", 501, map[string]string{"X-Amz-Security-Token": "token"}},
		{"GET", "/?X-Amz-Credential=dummy", "", 501, nil},
		{"GET", "/", "", 501, map[string]string{"x-amz-expected-bucket-owner": "owner"}},
		{"PUT", "/boundary-bucket/key", "bad", 501, map[string]string{"X-Amz-Content-Sha256": "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"}},
		{"PUT", "/boundary-bucket/key", "bad", 501, map[string]string{"x-amz-server-side-encryption": "AES256"}},
		{"GET", "/boundary-bucket/key?location", "", 501, nil},
		{"PUT", "/boundary-bucket?location", "", 501, nil},
		{"POST", "/boundary-bucket", "", 405, nil},
		{"POST", "/boundary-bucket/key", "", 405, nil},
		{"GET", "/boundary-bucket?location", "", 200, nil},
		{"GET", "/missing-bucket?location", "", 404, nil},
		{"GET", "/boundary-bucket?list-type=2&encoding-type=invalid", "", 400, nil},
		{"GET", "/boundary-bucket?list-type=2&prefix=%01", "", 400, nil},
		{"GET", "/boundary-bucket?list-type=2&continuation-token=!!!", "", 400, nil},
		{"GET", "/boundary-bucket/key", "", 412, map[string]string{"If-Match": "wrong"}},
		{"GET", "/boundary-bucket/key", "", 416, map[string]string{"Range": "bytes=99-100"}},
		{"PUT", "/too-large-config", strings.Repeat("x", 4097), 400, nil},
		{"PUT", "/boundary-bucket/key", strings.Repeat("x", (8<<20)+1), 413, nil},
		{"PUT", "/boundary-bucket/" + strings.Repeat("x", 1025), "bad", 400, nil},
		{"PUT", "/boundary-bucket/%01", "bad", 400, nil},
	}
	for _, tc := range cases {
		status(t, s, tc.method, tc.path, tc.body, tc.code, tc.headers)
	}
	if got := status(t, s, "GET", "/boundary-bucket/key", "", 200, nil); string(got) != "original" {
		t.Fatalf("unsupported request changed bytes: %q", got)
	}
	status(t, s, "HEAD", "/boundary-bucket", "", 200, nil)
	status(t, s, "PUT", "/boundary-bucket", "", 409, nil)
	status(t, s, "PUT", "/boundary-bucket", `<CreateBucketConfiguration><LocationConstraint>us-west-2</LocationConstraint></CreateBucketConfiguration>`, 409, nil)
}

func TestControlAPILifecycleAndValidation(t *testing.T) {
	s := instance(t)
	root := "/_simulator/v1/iam/principals"
	policy := `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:*","Resource":"*"}}`
	status(t, s, "POST", root, "", 405, nil)
	status(t, s, "GET", root+"/missing/policy", "", 404, nil)
	for _, path := range []string{root + "/bad.name/policy", root + "/reader/extra", root + "/simulator-admin/policy"} {
		status(t, s, "PUT", path, policy, 400, nil)
	}
	status(t, s, "PUT", root+"/reader/policy", strings.Repeat("x", (32<<10)+1), 413, nil)
	status(t, s, "PUT", root+"/reader/policy", policy, 200, nil)
	if got := status(t, s, "GET", root, "", 200, nil); !bytes.Contains(got, []byte(`"reader"`)) {
		t.Fatal(string(got))
	}
	if got := status(t, s, "GET", root+"/reader/policy", "", 200, nil); !bytes.Contains(got, []byte(`"s3:*"`)) {
		t.Fatal(string(got))
	}
	status(t, s, "POST", root+"/reader/policy", "", 405, nil)
	status(t, s, "DELETE", root+"/reader/policy", "", 204, nil)
	status(t, s, "GET", root+"/reader/policy", "", 404, nil)
	status(t, s, "POST", "/_simulator/health", "", 405, nil)
	status(t, s, "POST", "/_simulator/v1/capabilities", "", 405, nil)
	status(t, s, "GET", "/_simulator/v1/unknown", "", 404, nil)
	status(t, s, "GET", "/_simulator/licenses/SCALAR-LICENSE", "", 200, nil)
}

func TestClosedStateControlErrors(t *testing.T) {
	h, closeState, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err = closeState(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{"GET", "/_simulator/v1/iam/principals", "", 500},
		{"GET", "/_simulator/v1/iam/principals/reader/policy", "", 500},
		{"DELETE", "/_simulator/v1/iam/principals/reader/policy", "", 500},
		{"PUT", "/_simulator/v1/iam/principals/reader/policy", `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:*","Resource":"*"}}`, 400},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("x-simulator-principal", "reader")
	h.ServeHTTP(w, r)
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
	if _, _, err = New(t.TempDir()); err == nil {
		t.Fatal("accepted directory as DB file")
	}
}

func TestCreateBucketRejectsObjectOnlyOptionsBeforeWriting(t *testing.T) {
	s := instance(t)
	for _, header := range []string{"x-amz-acl", "x-amz-server-side-encryption", "x-amz-tagging", "x-amz-storage-class", "x-amz-copy-source"} {
		t.Run(header, func(t *testing.T) {
			status(t, s, "PUT", "/unsupported-bucket", "", 501, map[string]string{header: "unsupported"})
			status(t, s, "HEAD", "/unsupported-bucket", "", 404, nil)
		})
	}
}
