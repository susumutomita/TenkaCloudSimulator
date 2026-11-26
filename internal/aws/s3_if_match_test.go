package aws

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestS3ReadIfMatchSyntax(t *testing.T) {
	const selected = "\"selected\""
	for _, c := range []struct {
		name   string
		fields []string
		want   bool
	}{
		{"wildcard alone", []string{" \t*\t "}, true},
		{"exact", []string{selected}, true},
		{"matching last", []string{"\"other\", \t" + selected}, true},
		{"matching first with quoted comma", []string{selected + ", \"other,value\""}, true},
		{"two field lines", []string{"\"other,value\"", selected}, true},
		{"weak member is valid before a strong match", []string{"W/\"other\", " + selected}, true},
		{"permitted bytes in an opaque tag", []string{"\"opaque,slash\\\x80\", " + selected}, true},
		{"syntactically valid empty opaque tag", []string{"\"\", " + selected}, true},
		{"empty field", []string{""}, false},
		{"no match", []string{"\"other\", \"another\""}, false},
		{"weak only cannot match", []string{"W/" + selected + ", W/\"other\""}, false},
		{"incomplete weak marker", []string{selected + ", W/"}, false},
		{"lowercase weak marker", []string{"w/\"other\", " + selected}, false},
		{"invalid rear member", []string{selected + ", not-quoted"}, false},
		{"invalid rear field", []string{selected, "not-quoted"}, false},
		{"missing separator", []string{selected + " \"other\""}, false},
		{"unterminated quote", []string{selected + ", \"other"}, false},
		{"wildcard in list", []string{"*, " + selected}, false},
		{"wildcard plus another line", []string{selected, "*"}, false},
		{"wildcard with suffix", []string{"*trailing"}, false},
		{"leading empty member", []string{"," + selected}, false},
		{"trailing empty member", []string{selected + ", \t"}, false},
		{"interior empty member", []string{selected + ",, \"other\""}, false},
		{"space inside tag", []string{"\"other value\", " + selected}, false},
		{"DEL inside tag", []string{"\"other\x7f\", " + selected}, false},
		{"control inside tag", []string{"\"other\x01\", " + selected}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := s3ReadIfMatch(c.fields, selected); got != c.want {
				t.Fatalf("s3ReadIfMatch(%q) = %v, want %v", c.fields, got, c.want)
			}
		})
	}
}

func TestS3ReadIfMatchValidatesSelectedVersionAndPreservesOtherConditions(t *testing.T) {
	db, store := s3TestState(t)
	s3Enable(t, store, "Enabled")
	older := s3Put(t, store, "older fixture")
	current := s3Put(t, store, "current fixture")
	g := &Gateway{Store: db, S3: store}
	const key = "/versions-test/evidence"
	const versions = "/versions-test?versions"
	const staleDate = "Thu, 01 Jan 1970 00:00:01 GMT"
	oldPath := key + "?versionId=" + older.ID
	match := []string{older.ETag + ", " + current.ETag}

	wire := func(method, path, body string, values []string, headers http.Header) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		for _, value := range values {
			req.Header.Add("If-Match", value)
		}
		for name, value := range headers {
			req.Header[name] = value
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, req)
		if !reflect.DeepEqual(req.Header.Values("If-Match"), values) {
			t.Fatal("native handler modified the caller's header")
		}
		return w
	}
	expect := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("got %d, want %d: %s", w.Code, status, w.Body.String())
		}
	}
	view := wire("GET", versions, "", nil, nil)
	expect(view, 200)
	for _, method := range []string{"GET", "HEAD"} {
		ref := wire(method, key, "", []string{current.ETag}, nil)
		expect(ref, 200)
		for _, values := range [][]string{match, {older.ETag, current.ETag}, {"*"}} {
			got := wire(method, key, "", values, nil)
			expect(got, 200)
			for _, header := range []string{"ETag", "X-Amz-Version-Id", "Last-Modified", "Content-Length", "Content-Type", "Content-Disposition"} {
				if ref.Header().Get(header) == "" || got.Header().Get(header) != ref.Header().Get(header) {
					t.Fatalf("%s changed %s", method, header)
				}
			}
			if got.Body.String() != ref.Body.String() || method == "HEAD" && got.Body.Len() != 0 {
				t.Fatalf("%s changed the selected representation", method)
			}
		}
		got := wire(method, oldPath, "", match, nil)
		expect(got, 200)
		if got.Header().Get("ETag") != older.ETag || got.Header().Get("X-Amz-Version-Id") != older.ID ||
			method == "GET" && got.Body.String() != "older fixture" {
			t.Fatal("version-specific If-Match selected the current representation")
		}
		for _, condition := range [][]string{{current.ETag + ", \"nonmatch\""}, {"W/" + older.ETag}, {older.ETag + ", malformed"}} {
			refusal := wire(method, oldPath, "", condition, nil)
			expect(refusal, 412)
			var body failure
			if err := xml.Unmarshal(refusal.Body.Bytes(), &body); err != nil || body.Code != "PreconditionFailed" ||
				refusal.Header().Get("ETag") != older.ETag {
				t.Fatalf("wrong precondition failure frame: %v %s", err, refusal.Body.String())
			}
		}
		expect(wire(method, key, "", nil, http.Header{"If-Unmodified-Since": {staleDate}}), 412)
		expect(wire(method, key, "", match, http.Header{"If-Unmodified-Since": {staleDate}}), 200)
		expect(wire(method, key, "", match, http.Header{"If-None-Match": {current.ETag}}), 304)
		expect(wire(method, "/versions-test/not-present", "", []string{"*"}, nil), 404)
		partial := wire(method, key, "", match, http.Header{"Range": {"bytes=1-3"}})
		expect(partial, 206)
		if partial.Header().Get("Content-Length") != "3" || method == "GET" && partial.Body.String() != "urr" ||
			method == "HEAD" && partial.Body.Len() != 0 {
			t.Fatal("selected match changed the range or HEAD behavior")
		}
	}
	// Deliberately do not widen the existing write precondition contract.
	expect(wire("PUT", key, "must not be written", match, nil), 412)
	after := wire("GET", versions, "", nil, nil)
	expect(after, 200)
	if after.Body.String() != view.Body.String() {
		t.Fatal("conditional reads or refused write changed version inventory")
	}
}
