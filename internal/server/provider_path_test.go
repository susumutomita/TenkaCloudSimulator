package server

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestProviderPrefixPreservesLiteralKeys(t *testing.T) {
	s := instance(t)
	// A redirect is a failure even if following it would reach a successful route.
	s.Client().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	status(t, s, "PUT", "/prefix-bucket", "", 200, nil)
	keys := []string{"b", "a/b", "a/../b", "a/./b", "a//b", "a///b", "a/%2F/b", "a/%2f/b", "a/%25/b", "a/%/b", "a/%252F/b", "a/雪/é", "./b"}
	prefixes := []string{"", "/_simulator/providers/aws"}
	expected := map[string]string{}
	pathFor := func(prefix, key string) string {
		segments := strings.Split(key, "/")
		for i := range segments {
			segments[i] = url.PathEscape(segments[i])
		}
		// Keep literal dot segments and slashes; encode percent and UTF-8 once.
		return prefix + "/prefix-bucket/" + strings.Join(segments, "/")
	}
	assertSnapshot := func() {
		t.Helper()
		wantKeys := make([]string, 0, len(expected))
		for key := range expected {
			wantKeys = append(wantKeys, key)
		}
		sort.Strings(wantKeys)
		for _, prefix := range prefixes {
			var listing struct {
				IsTruncated bool
				Contents    []struct{ Key string }
			}
			if err := xml.Unmarshal(status(t, s, "GET", prefix+"/prefix-bucket?list-type=2", "", 200, nil), &listing); err != nil {
				t.Fatal(err)
			}
			listed := make([]string, 0, len(listing.Contents))
			for _, item := range listing.Contents {
				listed = append(listed, item.Key)
			}
			sort.Strings(listed)
			if listing.IsTruncated || !slices.Equal(listed, wantKeys) {
				t.Fatalf("exact object-key set changed at %q: got %q want %q", prefix, listed, wantKeys)
			}
			for key, body := range expected {
				if got := status(t, s, "GET", pathFor(prefix, key), "", 200, nil); string(got) != body {
					t.Fatalf("object %q changed at %q: got %q want %q", key, prefix, got, body)
				}
			}
		}
	}
	// All potentially colliding keys coexist with distinct values. Agreement
	// between two equally wrong endpoints cannot satisfy the exact list set.
	for i, key := range keys {
		expected[key] = fmt.Sprintf("seed-%d:%s", i, key)
		status(t, s, "PUT", pathFor(prefixes[i%2], key), expected[key], 200, nil)
		assertSnapshot()
	}
	for i, key := range keys {
		for writer, prefix := range prefixes {
			path := pathFor(prefix, key)
			if got := status(t, s, "GET", path, "", 200, nil); string(got) != expected[key] {
				t.Fatal("GET changed key", key)
			}
			assertSnapshot()
			head, _ := call(t, s, "HEAD", path, "", nil)
			if head.StatusCode != 200 || head.Header.Get("Content-Length") != strconv.Itoa(len(expected[key])) {
				t.Fatalf("HEAD changed key %q: %d %v", key, head.StatusCode, head.Header)
			}
			assertSnapshot()
			expected[key] = fmt.Sprintf("updated-%d-%d:%s", writer, i, key)
			status(t, s, "PUT", path, expected[key], 200, nil)
			assertSnapshot()
			status(t, s, "DELETE", path, "", 204, nil)
			delete(expected, key)
			assertSnapshot()
			for _, reader := range prefixes {
				status(t, s, "GET", pathFor(reader, key), "", 404, nil)
				assertSnapshot()
			}
			expected[key] = fmt.Sprintf("restored-%d-%d:%s", writer, i, key)
			status(t, s, "PUT", path, expected[key], 200, nil)
			assertSnapshot()
		}
	}
	// Encoded slash is a URI spelling of the three-slash key, whereas the
	// percent-containing neighbors above must survive an additional decode.
	for _, prefix := range prefixes {
		if got := status(t, s, "GET", prefix+"/prefix-bucket/a/%2F/b", "", 200, nil); string(got) != expected["a///b"] {
			t.Fatal("encoded slash did not address the exact decoded key")
		}
		assertSnapshot()
	}
	for i, key := range keys {
		status(t, s, "DELETE", pathFor(prefixes[i%2], key), "", 204, nil)
		delete(expected, key)
		assertSnapshot()
	}
	status(t, s, "DELETE", "/prefix-bucket", "", 204, nil)
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
