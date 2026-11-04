package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestProviderPrefixPreservesLiteralKeys(t *testing.T) {
	s := instance(t)
	// A redirect is a failure even if following it would reach a successful route.
	s.Client().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	status(t, s, "PUT", "/prefix-bucket", "", 200, nil)
	status(t, s, "PUT", "/prefix-bucket/victim", "sentinel", 200, nil)
	for i, key := range []string{"a/../victim", "a/./victim", "a//victim", "./victim", "a///victim", "a/%252F/victim", "a/%2525/victim", "a/%2F/victim", "a/雪/é"} {
		root := "/prefix-bucket/" + key
		prefixed := "/_simulator/providers/aws" + root
		before := fmt.Sprintf("literal-%d", i)
		status(t, s, "PUT", root, before, 200, nil)
		if got := status(t, s, "GET", prefixed, "", 200, nil); string(got) != before {
			t.Fatalf("prefixed GET changed key %q: %q", key, got)
		}
		head, _ := call(t, s, "HEAD", prefixed, "", nil)
		if head.StatusCode != 200 || head.Header.Get("Content-Length") != strconv.Itoa(len(before)) {
			t.Fatalf("HEAD changed key %q: %d %v", key, head.StatusCode, head.Header)
		}
		status(t, s, "PUT", prefixed, "replacement", 200, nil)
		if got := status(t, s, "GET", root, "", 200, nil); string(got) != "replacement" {
			t.Fatalf("prefixed PUT changed key %q: %q", key, got)
		}
		status(t, s, "DELETE", prefixed, "", 204, nil)
		status(t, s, "GET", root, "", 404, nil)
		if got := status(t, s, "GET", "/prefix-bucket/victim", "", 200, nil); string(got) != "sentinel" {
			t.Fatal("neighbor object changed", string(got))
		}
	}
	status(t, s, "GET", "/_simulator/providers/missing/a/../victim", "", 404, nil)
}

func TestProviderPrefixAccessGuards(t *testing.T) {
	base := instance(t)
	token := strings.Repeat("p", 64)
	access, err := NewAccess(base.Config.Handler, token, false)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(access)
	t.Cleanup(s.Close)
	s.Client().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	headers := map[string]string{"X-Simulator-Token": token}
	status(t, s, "PUT", "/guard-bucket", "", 200, headers)
	path := "/_simulator/providers/aws/guard-bucket/a/../sentinel"
	status(t, s, "PUT", path, "exact-key", 401, nil)
	status(t, s, "PUT", path, "exact-key", 200, headers)
	status(t, s, "DELETE", path, "", 401, nil)
	status(t, s, "DELETE", path, "", 403, map[string]string{"X-Simulator-Token": token, "Origin": "https://foreign.invalid"})
	if got := status(t, s, "GET", "/guard-bucket/a/../sentinel", "", 200, headers); string(got) != "exact-key" {
		t.Fatal("denied prefixed request changed the literal object")
	}
	status(t, s, "GET", "/guard-bucket/sentinel", "", 404, headers)
	status(t, s, "GET", "/_simulator/providers/missing/a/../sentinel", "", 404, headers)
}

func TestProviderPrefixLiteralKeyIAM(t *testing.T) {
	s := instance(t)
	s.Client().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	status(t, s, "PUT", "/path-iam-bucket", "", 200, nil)
	status(t, s, "PUT", "/path-iam-bucket/a/../victim", "private-literal", 200, nil)
	status(t, s, "PUT", "/path-iam-bucket/victim", "public-neighbor", 200, nil)
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::path-iam-bucket/*"},{"Effect":"Deny","Action":"s3:GetObject","Resource":"arn:aws:s3:::path-iam-bucket/a/../victim"}]}`
	status(t, s, "PUT", "/_simulator/v1/iam/principals/path-reader/policy", policy, 200, nil)
	reader := map[string]string{"X-Simulator-Principal": "path-reader"}
	for _, prefix := range []string{"", "/_simulator/providers/aws"} {
		response, _ := call(t, s, "GET", prefix+"/path-iam-bucket/a/../victim", "", reader)
		if response.StatusCode != 403 || response.Header.Get("X-Simulator-IAM-Decision") != "explicitDeny" {
			t.Fatal("literal key IAM decision changed", response.StatusCode, response.Header)
		}
		if got := status(t, s, "GET", prefix+"/path-iam-bucket/victim", "", 200, reader); string(got) != "public-neighbor" {
			t.Fatal("neighbor key changed", string(got))
		}
	}
}
