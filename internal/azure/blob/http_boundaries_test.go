package blob

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func fullBlobSnapshot(t *testing.T, s *Service) string {
	t.Helper()
	dump := map[string][]map[string]any{}
	for _, table := range []string{"azure_blob_accounts", "azure_blob_containers", "azure_blob_objects"} {
		rows, err := s.db.Query("SELECT * FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		names, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(names))
			dest := make([]any, len(names))
			for i := range values {
				dest[i] = &values[i]
			}
			if err = rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			row := map[string]any{}
			for i, n := range names {
				row[n] = values[i]
			}
			dump[table] = append(dump[table], row)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	raw, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func assertNativeError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d want%d: %s", w.Code, status, w.Body.String())
	}
	var e struct {
		XMLName xml.Name `xml:"Error"`
		Code    string
		Message string
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.Code != code || w.Header().Get("x-ms-error-code") != code || e.Message == "" {
		t.Fatal("wrong native error schema/code", e.Code, w.Header())
	}
	if w.Header().Get("ETag") != "" || w.Header().Get("Content-MD5") != "" || strings.Contains(e.Message, "private-peer-bytes") {
		t.Fatal("error disclosed persisted object state")
	}
}
func boundedWire(s *Service, method, path string, body io.Reader, headers http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("x-ms-version", APIVersion)
	for k, v := range headers {
		r.Header[k] = v
	}
	r = r.WithContext(WithActor(r.Context(), *learner()))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestAzureBlobNativeValidationRefusesWithoutChangingEitherContainer(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	create(t, s, "peer")
	put(t, s, "box", "saved", "saved", nil, 201)
	put(t, s, "peer", "saved", "private-peer-bytes", nil, 201)
	before := fullBlobSnapshot(t, s)
	cases := []struct {
		name, method, path, body string
		headers                  http.Header
		code                     string
		status                   int
	}{
		{"version", "GET", "/accountone/box/saved", "", http.Header{"X-Ms-Version": {"future"}}, "InvalidHeaderValue", 400},
		{"query-size", "GET", "/accountone?comp=list&prefix=" + strings.Repeat("x", 16384), "", nil, "InvalidQueryParameterValue", 400},
		{"bad-query", "GET", "/accountone?comp=list&prefix=%zz", "", nil, "InvalidQueryParameterValue", 400},
		{"duplicate-query", "GET", "/accountone?comp=list&comp=list", "", nil, "UnsupportedQueryParameter", 400},
		{"client-id", "GET", "/accountone/box/saved", "", http.Header{"X-Ms-Client-Request-Id": {strings.Repeat("x", 1025)}}, "InvalidHeaderValue", 400},
		{"client-control", "GET", "/accountone/box/saved", "", http.Header{"X-Ms-Client-Request-Id": {"bad\tvalue"}}, "InvalidHeaderValue", 400},
		{"encoding", "GET", "/accountone/box/saved", "", http.Header{"Content-Encoding": {"gzip"}}, "UnsupportedHeader", 400},
		{"date", "GET", "/accountone/box/saved", "", http.Header{"If-Modified-Since": {"Wed, 04 Nov 2026 00:00:00 GMT"}}, "UnsupportedHeader", 400},
		{"listing-only-query", "GET", "/accountone/box/saved?prefix=x", "", nil, "UnsupportedQueryParameter", 400},
		{"container-condition", "DELETE", "/accountone/box?restype=container", "", http.Header{"If-Match": {"*"}}, "UnsupportedHeader", 400},
		{"upload-header-on-read", "GET", "/accountone/box/saved", "", http.Header{"X-Ms-Blob-Type": {"BlockBlob"}}, "UnsupportedHeader", 400},
		{"read-metadata", "GET", "/accountone/box/saved", "", http.Header{"X-Ms-Meta-A": {"no"}}, "UnsupportedHeader", 400},
		{"duplicate-metadata", "PUT", "/accountone/new?restype=container", "", http.Header{"x-ms-meta-A": {"one"}, "X-Ms-Meta-A": {"two"}}, "InvalidMetadata", 400},
		{"nonascii-metadata", "PUT", "/accountone/new?restype=container", "", http.Header{"X-Ms-Meta-A": {"雪"}}, "InvalidMetadata", 400},
		{"large-metadata", "PUT", "/accountone/new?restype=container", "", http.Header{"X-Ms-Meta-A": {strings.Repeat("x", 8192)}}, "MetadataTooLarge", 400},
		{"repeat-native-header", "GET", "/accountone/box/saved", "", http.Header{"X-Ms-Client-Request-Id": {"a", "b"}}, "InvalidHeaderValue", 400},
		{"repeat-condition", "GET", "/accountone/box/saved", "", http.Header{"If-Match": {"*", "*"}}, "InvalidHeaderValue", 400},
		{"bad-maxresults", "GET", "/accountone?comp=list&maxresults=0", "", nil, "InvalidQueryParameterValue", 400},
		{"include-unsupported", "GET", "/accountone?comp=list&include=tags", "", nil, "UnsupportedQueryParameter", 400},
		{"wrong-blob-kind", "PUT", "/accountone/box/saved", "body", http.Header{"X-Ms-Blob-Type": {"PageBlob"}}, "InvalidBlobType", 400},
		{"wrong-mime", "PUT", "/accountone/box/saved", "body", http.Header{"X-Ms-Blob-Type": {"BlockBlob"}, "Content-Type": {"not a mime type"}}, "InvalidHeaderValue", 400},
		{"type-label-size", "PUT", "/accountone/box/saved", "body", http.Header{"X-Ms-Blob-Type": {"BlockBlob"}, "Content-Type": {"text/" + strings.Repeat("a", 260)}}, "InvalidHeaderValue", 400},
		{"blob-oversize", "PUT", "/accountone/box/saved", strings.Repeat("x", MaxBlobBytes+1), http.Header{"X-Ms-Blob-Type": {"BlockBlob"}}, "RequestBodyTooLarge", 413},
		{"body-on-get", "GET", "/accountone/box/saved", "x", nil, "RequestBodyTooLarge", 413},
		{"bad-checksum", "PUT", "/accountone/box/saved", "body", http.Header{"X-Ms-Blob-Type": {"BlockBlob"}, "X-Ms-Blob-Content-Md5": {"bad"}}, "Md5Mismatch", 400},
		{"unknown-account", "GET", "/otheraccount/box/saved", "", nil, "ResourceNotFound", 404},
		{"bad-container", "GET", "/accountone/a--b/saved", "", nil, "InvalidUri", 400},
		{"empty-key", "GET", "/accountone/box/", "", nil, "InvalidUri", 400},
		{"missing-parent", "GET", "/accountone/missing/key", "", nil, "ContainerNotFound", 404},
		{"missing-object", "DELETE", "/accountone/box/missing", "", nil, "BlobNotFound", 404},
		{"duplicate-container", "PUT", "/accountone/box?restype=container", "", nil, "ContainerAlreadyExists", 409},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := boundedWire(s, c.method, c.path, strings.NewReader(c.body), c.headers)
			assertNativeError(t, w, c.status, c.code)
			if fullBlobSnapshot(t, s) != before {
				t.Fatal("rejection changed saved graph or peer")
			}
		})
	}
	h := http.Header{}
	for i := 0; i < 33; i++ {
		h.Set(fmt.Sprintf("x-ms-meta-k%d", i), "value")
	}
	assertNativeError(t, boundedWire(s, "PUT", "/accountone/new?restype=container", nil, h), 400, "MetadataTooLarge")
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("metadata refusal changed state")
	}
	good := boundedWire(s, "GET", "/accountone/box/saved?timeout=1", nil, http.Header{"X-Ms-Client-Request-Id": {"client-001"}})
	if good.Code != 200 || good.Header().Get("x-ms-client-request-id") != "client-001" {
		t.Fatal("valid timeout/request-id rejected")
	}
	sum := md5.Sum([]byte("valid"))
	w := boundedWire(s, "PUT", "/accountone/box/positive", strings.NewReader("valid"), http.Header{"X-Ms-Blob-Type": {"BlockBlob"}, "X-Ms-Blob-Content-Md5": {base64.StdEncoding.EncodeToString(sum[:])}, "X-Ms-Blob-Content-Type": {"text/plain"}})
	if w.Code != 201 {
		t.Fatal("valid checksum/native type rejected", w.Body.String())
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, fmt.Errorf("injected read failure") }
func TestAzureBlobBodyReadAndRawPathRejectionsAreAtomic(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	put(t, s, "box", "peer", "private-peer-bytes", nil, 201)
	before := fullBlobSnapshot(t, s)
	assertNativeError(t, boundedWire(s, "PUT", "/accountone/box/new", failingReader{}, http.Header{"X-Ms-Blob-Type": {"BlockBlob"}}), 400, "InvalidRequestBody")
	for _, u := range []*url.URL{{Path: "/accountone/box/peer", RawPath: "/accountone/box/%zz"}, {Path: "/accountone/box/peer", RawPath: "/accountone/box/other"}, {Path: "not-absolute"}, {Path: "/ACCOUNT/box/peer"}, {Path: "/accountone/box/" + strings.Repeat("x", 1025)}, {Path: "/accountone/box/bad\u0080"}} {
		r := httptest.NewRequest("GET", "/", nil)
		r.URL = u
		r.Header.Set("x-ms-version", APIVersion)
		r = r.WithContext(WithActor(r.Context(), *learner()))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		assertNativeError(t, w, 400, "InvalidUri")
	}
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("wire rejection mutated existing state")
	}
}
