package server

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func instance(t *testing.T) *httptest.Server {
	t.Helper()
	h, closeState, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() {
		srv.Close()
		if err := closeState(); err != nil {
			t.Error(err)
		}
	})
	return srv
}
func call(t *testing.T, s *httptest.Server, method, path, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	r, err := http.NewRequest(method, s.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	resp, err := s.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}
func status(t *testing.T, s *httptest.Server, method, path, body string, want int, headers map[string]string) []byte {
	t.Helper()
	resp, data := call(t, s, method, path, body, headers)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: want %d got %d %s", method, path, want, resp.StatusCode, data)
	}
	return data
}
func TestS3LifecycleAndXMLContract(t *testing.T) {
	s := instance(t)
	status(t, s, "PUT", "/practice-bucket", "", 200, nil)
	listed := status(t, s, "GET", "/", "", 200, nil)
	if !bytes.Contains(listed, []byte("<BucketRegion>us-east-1</BucketRegion>")) {
		t.Fatal(string(listed))
	}
	status(t, s, "PUT", "/practice-bucket/docs/a%20%26%20b.txt", "hello world", 200, map[string]string{"Content-Type": "text/plain", "If-None-Match": "*"})
	status(t, s, "PUT", "/practice-bucket/docs/a%20%26%20b.txt", "overwrite", 412, map[string]string{"If-None-Match": "*"})
	status(t, s, "PUT", "/practice-bucket/other.txt", "other", 200, nil)
	r, data := call(t, s, "GET", "/practice-bucket/docs/a%20%26%20b.txt", "", map[string]string{"Range": "bytes=0-4"})
	if r.StatusCode != 206 || string(data) != "hello" {
		t.Fatalf("range: %d %s", r.StatusCode, data)
	}
	r, _ = call(t, s, "HEAD", "/practice-bucket/docs/a%20%26%20b.txt", "", nil)
	if r.StatusCode != 200 || r.Header.Get("Content-Length") != "11" || r.Header.Get("ETag") == "" {
		t.Fatal(r.Header)
	}
	status(t, s, "GET", "/practice-bucket/docs/a%20%26%20b.txt", "", 304, map[string]string{"If-None-Match": r.Header.Get("ETag")})
	grouped := status(t, s, "GET", "/practice-bucket?list-type=2&delimiter=/", "", 200, nil)
	if !bytes.Contains(grouped, []byte("<CommonPrefixes><Prefix>docs/</Prefix></CommonPrefixes>")) {
		t.Fatal(string(grouped))
	}
	page := status(t, s, "GET", "/practice-bucket?list-type=2&max-keys=1", "", 200, nil)
	var result struct {
		Token     string `xml:"NextContinuationToken"`
		Keys      int    `xml:"KeyCount"`
		Truncated bool   `xml:"IsTruncated"`
	}
	if err := xml.Unmarshal(page, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.Keys != 1 || result.Token == "" {
		t.Fatal(string(page))
	}
	last := status(t, s, "GET", "/practice-bucket?list-type=2&max-keys=1&continuation-token="+result.Token, "", 200, nil)
	if !bytes.Contains(last, []byte("<Key>other.txt</Key>")) {
		t.Fatal(string(last))
	}
	status(t, s, "GET", "/practice-bucket?list-type=2&prefix=other&continuation-token="+result.Token, "", 400, nil)
	encoded := status(t, s, "GET", "/practice-bucket?list-type=2&encoding-type=url&prefix=docs", "", 200, nil)
	if !bytes.Contains(encoded, []byte("docs%2Fa%20%26%20b.txt")) {
		t.Fatal(string(encoded))
	}
	status(t, s, "DELETE", "/practice-bucket", "", 409, nil)
	status(t, s, "DELETE", "/practice-bucket/docs/a%20%26%20b.txt", "", 204, nil)
	status(t, s, "DELETE", "/practice-bucket/other.txt", "", 204, nil)
	status(t, s, "DELETE", "/practice-bucket/not-there", "", 204, nil)
	status(t, s, "DELETE", "/practice-bucket", "", 204, nil)
	status(t, s, "HEAD", "/practice-bucket", "", 404, nil)
}
func TestIAMEnforcedAcrossAPIAndControl(t *testing.T) {
	s := instance(t)
	status(t, s, "PUT", "/iam-example", "", 200, nil)
	status(t, s, "PUT", "/iam-example/public/file", "public", 200, nil)
	status(t, s, "PUT", "/iam-example/private/file", "private", 200, nil)
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:ListBucket","s3:ListAllMyBuckets"],"Resource":"*"},{"Effect":"Deny","Action":"s3:GetObject","Resource":"arn:aws:s3:::iam-example/private/*"}]}`
	status(t, s, "PUT", "/_simulator/v1/iam/principals/reader/policy", policy, 200, nil)
	reader := map[string]string{"x-simulator-principal": "reader"}
	status(t, s, "GET", "/iam-example/public/file", "", 200, reader)
	r, b := call(t, s, "GET", "/iam-example/private/file", "", reader)
	if r.StatusCode != 403 || r.Header.Get("X-Simulator-IAM-Decision") != "explicitDeny" {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	r, _ = call(t, s, "PUT", "/iam-example/public/file", "changed", reader)
	if r.StatusCode != 403 || r.Header.Get("X-Simulator-IAM-Decision") != "implicitDeny" {
		t.Fatal(r.Header)
	}
	status(t, s, "GET", "/iam-example/public/file", "", 403, map[string]string{"x-simulator-principal": "unknown"})
	status(t, s, "PUT", "/_simulator/v1/iam/principals/reader/policy", policy, 403, reader)
	if got := status(t, s, "GET", "/iam-example/public/file", "", 200, nil); string(got) != "public" {
		t.Fatal("denied operation mutated state")
	}
	invalid := strings.Replace(policy, `"Effect":"Allow"`, `"Effect":"Allow","Condition":{}`, 1)
	status(t, s, "PUT", "/_simulator/v1/iam/principals/reader/policy", invalid, 400, nil)
	status(t, s, "GET", "/iam-example/private/file", "", 403, reader)
	status(t, s, "DELETE", "/_simulator/v1/iam/principals/reader/policy", "", 204, nil)
	status(t, s, "GET", "/iam-example/public/file", "", 403, reader)
}
func TestProtocolFailuresAndBoundaries(t *testing.T) {
	s := instance(t)
	for _, tc := range []struct {
		method, path, body string
		want               int
		headers            map[string]string
	}{
		{"PUT", "/Invalid_Bucket", "", 400, nil}, {"PUT", "/192.168.1.1", "", 400, nil},
		{"PUT", "/bad-region", `<CreateBucketConfiguration><LocationConstraint>unknown</LocationConstraint></CreateBucketConfiguration>`, 400, nil},
		{"PUT", "/xml-bucket", `<wrong/>`, 400, nil},
		{"GET", "/", "", 403, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/20261103/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=dummy"}},
		{"GET", "/", "", 200, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMEXAMPLE/20261103/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=dummy"}},
		{"GET", "/", "", 403, map[string]string{"Origin": "https://elsewhere.invalid"}},
		{"GET", "/", "", 403, map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{"GET", "/missing?list-type=2", "", 404, nil},
		{"GET", "/missing?versioning", "", 501, nil},
		{"GET", "/missing/key?uploadId=abc", "", 501, nil},
	} {
		status(t, s, tc.method, tc.path, tc.body, tc.want, tc.headers)
	}
	status(t, s, "PUT", "/checksums", "", 200, nil)
	status(t, s, "PUT", "/checksums/file", "data", 400, map[string]string{"Content-MD5": "invalid"})
	status(t, s, "GET", "/checksums/file", "", 404, nil)
	status(t, s, "PUT", "/checksums/file", "data", 200, map[string]string{"Content-MD5": "jXd/OF09/siBXSD3SWAm3A=="})
	status(t, s, "PUT", "/checksums/file", "bad", 501, map[string]string{"x-amz-copy-source": "/source/key"})
	status(t, s, "PUT", "/checksums/file", "bad", 501, map[string]string{"Content-Encoding": "aws-chunked"})
	status(t, s, "PUT", "/checksums/file", "bad", 501, map[string]string{"x-amz-meta-ignored": "no"})
	status(t, s, "GET", "/checksums?list-type=2&max-keys=-1", "", 400, nil)
	status(t, s, "GET", "/checksums?list-type=2&max-keys=0", "", 200, nil)
	status(t, s, "GET", "/checksums", "", 501, nil)
	// S3 keys are not normalized by a web router, including adjacent slash/dot segments.
	status(t, s, "PUT", "/checksums/a//../key", "raw-key", 200, nil)
	if b := status(t, s, "GET", "/checksums/a//../key", "", 200, nil); string(b) != "raw-key" {
		t.Fatal(string(b))
	}
	// Provider control lives under an invalid bucket-name prefix, leaving valid names usable.
	status(t, s, "PUT", "/console", "", 200, nil)
	status(t, s, "PUT", "/console/app.js", "bytes", 200, nil)
	status(t, s, "GET", "/_simulator/console/", "", 200, nil)
	status(t, s, "GET", "/_simulator/docs/", "", 200, nil)
	var spec map[string]any
	if err := json.Unmarshal(status(t, s, "GET", "/_simulator/docs/openapi.json", "", 200, nil), &spec); err != nil || spec["openapi"] != "3.1.0" {
		t.Fatal("invalid OpenAPI", err)
	}
}
func TestEveryAdvertisedOperationHasWireCoverage(t *testing.T) {
	s := instance(t)
	var manifest struct {
		Operations []string `json:"operations"`
	}
	if err := json.Unmarshal(status(t, s, "GET", "/_simulator/v1/capabilities", "", 200, nil), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Operations) != 10 {
		t.Fatal("Update lifecycle conformance when capabilities change")
	}
	status(t, s, "PUT", "/tokyo-bucket", `<CreateBucketConfiguration><LocationConstraint>ap-northeast-1</LocationConstraint></CreateBucketConfiguration>`, 200, nil)
	r, _ := call(t, s, "HEAD", "/tokyo-bucket", "", nil)
	if r.Header.Get("x-amz-bucket-region") != "ap-northeast-1" {
		t.Fatal(r.Header)
	}
	b := status(t, s, "GET", "/tokyo-bucket?location", "", 200, nil)
	if !bytes.Contains(b, []byte("ap-northeast-1")) {
		t.Fatal(string(b))
	}
}
func TestDatabaseFailureIsInternalError(t *testing.T) {
	h, closeState, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err = closeState(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 500 || !strings.Contains(w.Body.String(), "InternalError") {
		t.Fatalf("database failure: %d %s", w.Code, w.Body.String())
	}
}
