package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestStorageHTTPRejectionBoundariesPreserveState(t *testing.T) {
	db, s := fixture(t, ":memory:", "boundary", projectPolicy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "boundary-bucket")
	upload(t, s, "project-one", "boundary-bucket", "saved", "do-not-leak", "0")
	cases := []struct {
		method, path, body, content string
		status                      int
	}{
		{"POST", "/storage/v1/b?project=project-one", `{"name":12}`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":true}`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":"broken"`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name"`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `[[],[null]]`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", strings.Repeat("[", 18) + strings.Repeat("]", 18), "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":"\udc00"}`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":"\ud800x"}`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":"\ud800\u0041"}`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":"\uZZZZ"}`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":"\u12`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":"x` + "\\", "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", "{\"name\":\"\xff\"}", "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", strings.Repeat(" ", 64<<10) + `{}`, "application/json", 413},
		{"POST", "/storage/v1/b?project=project-one&prefix=", `{"name":"another-bucket"}`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":"127.0.0.1"}`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":"google-reserved"}`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":"` + strings.Repeat("a", 64) + `"}`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":"abc..def"}`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":"` + strings.Repeat("a", 64) + `.example"}`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":"another-bucket","storageClass":"COLDLINE"}`, "application/json", 400},
		{"POST", "/storage/v1/b?project=project-one", `{"name":"another-bucket","location":"MOON1"}`, "application/json", 400},
		{"PATCH", "/storage/v1/b", "", "", 405}, {"GET", "/unknown", "", "", 404},
		{"GET", "/storage/v1/unknown", "", "", 404}, {"GET", "/storage/v1/b/INVALID", "", "", 400},
		{"PATCH", "/storage/v1/b/boundary-bucket", "", "", 405},
		{"POST", "/storage/v1/b/boundary-bucket/o", "", "", 405},
		{"GET", "/upload/storage/v1/b", "", "", 400},
		{"POST", "/upload/storage/v1/b/boundary-bucket", "", "", 400},
		{"GET", "/upload/storage/v1/b/boundary-bucket/o", "", "", 405},
		{"GET", "/storage/v1/b/boundary-bucket/unknown", "", "", 404},
		{"GET", "/storage/v1/b/boundary-bucket/o/.", "", "", 400},
		{"GET", "/storage/v1/b/boundary-bucket/o/saved?alt=unknown", "", "", 400},
		{"DELETE", "/storage/v1/b/boundary-bucket/o/saved?alt=json", "", "", 400},
		{"PATCH", "/storage/v1/b/boundary-bucket/o/saved", "", "", 405},
		{"GET", "/storage/v1/b/boundary-bucket/o?prettyPrint=maybe", "", "", 400},
		{"GET", "/storage/v1/b/boundary-bucket/o?prefix=%FF", "", "", 400},
		{"GET", "/storage/v1/b/boundary-bucket/o?prefix=%", "", "", 400},
		{"GET", "/download/storage/v1/b?alt=media", "", "", 404},
		{"GET", "/download/storage/v1/b/boundary-bucket?alt=media", "", "", 404},
		{"GET", "/download/storage/v1/b/boundary-bucket/o?alt=media", "", "", 404},
		{"POST", "/upload/storage/v1/b/boundary-bucket/o?uploadType=media&name=", "", "text/plain", 400},
		{"POST", "/upload/storage/v1/b/boundary-bucket/o?uploadType=media&name=new", "x", "not a mime;", 400},
		{"POST", "/upload/storage/v1/b/boundary-bucket/o?uploadType=media&name=new", "x", strings.Repeat("x", 257), 400},
	}
	for i, tc := range cases {
		before := storageSnapshot(t, db)
		w := request(s, "project-one", tc.method, tc.path, tc.body, tc.content)
		mustStatus(t, w, tc.status)
		if after := storageSnapshot(t, db); after != before {
			t.Fatalf("rejection%d changed persisted state", i)
		}
	}
	for _, header := range []string{"Content-Encoding", "If-Match", "If-None-Match", "Range", "X-Goog-Encryption-Key", "X-Goog-User-Project", "X-HTTP-Method-Override"} {
		before := storageSnapshot(t, db)
		r := httptest.NewRequest("GET", "/storage/v1/b/boundary-bucket/o/saved", nil).WithContext(WithActor(context.Background(), Actor{"project-one", s.environment}))
		r.Header.Set(header, "unsupported")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		mustStatus(t, w, 400)
		if after := storageSnapshot(t, db); after != before {
			t.Fatal("unsupported header changed state", header)
		}
	}
	expectMedia(t, s, "project-one", "boundary-bucket", "saved", "do-not-leak")
}

func multipartBody(t *testing.T, metadata string, parts int, firstType, transfer string) (string, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for i := 0; i < parts; i++ {
		h := textproto.MIMEHeader{}
		kind := "text/plain"
		data := "media"
		if i == 0 {
			kind = firstType
			data = metadata
		}
		h.Set("Content-Type", kind)
		if transfer != "" {
			h.Set("Content-Transfer-Encoding", transfer)
		}
		part, err := writer.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(part, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.String(), "multipart/related; boundary=" + writer.Boundary()
}

func TestStorageMultipartMetadataAndReadFailureBoundaries(t *testing.T) {
	db, s := fixture(t, ":memory:", "mime", projectPolicy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "mime-bucket")
	inputs := []string{`{"name":"new","metadata":{"x":1}}`, `{"name":"new","metadata":{"x":null}}`, `{"name":"new","metadata":{"x":"a","x":"b"}}`, `{"name":"new","metadata":[]}`, `{"name":"new","metadata":{"":"x"}}`, `{"name":"new","metadata":{"x":"\u0000"}}`, `{"name":"new","metadata":{"x":"` + strings.Repeat("x", 257) + `"}}`, `{"name":"new","metadata":{"` + strings.Repeat("x", 65) + `":"v"}}`}
	big := map[string]string{}
	for i := 0; i < 17; i++ {
		big[strings.Repeat("k", i+1)] = "v"
	}
	encoded, err := json.Marshal(map[string]any{"name": "new", "metadata": big})
	if err != nil {
		t.Fatal(err)
	}
	inputs = append(inputs, string(encoded))
	for _, metadata := range inputs {
		body, content := multipartBody(t, metadata, 2, "application/json", "")
		before := storageSnapshot(t, db)
		mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/mime-bucket/o?uploadType=multipart", body, content), 400)
		if after := storageSnapshot(t, db); after != before {
			t.Fatal("invalid metadata mutation")
		}
	}
	for _, tc := range []struct {
		parts          int
		kind, transfer string
	}{{0, "application/json", ""}, {1, "application/json", ""}, {3, "application/json", ""}, {2, "text/plain", ""}, {2, "application/json", "quoted-printable"}} {
		body, content := multipartBody(t, `{"name":"new"}`, tc.parts, tc.kind, tc.transfer)
		before := storageSnapshot(t, db)
		mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/mime-bucket/o?uploadType=multipart", body, content), 400)
		if after := storageSnapshot(t, db); after != before {
			t.Fatal("malformed multipart changed state")
		}
	}
	for _, content := range []string{"multipart/form-data; boundary=x", "multipart/related", "invalid mime;"} {
		mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/mime-bucket/o?uploadType=multipart", "bad", content), 400)
	}
	body, content := multipartBody(t, `{"name":"\ud83c\udf0e","metadata":{"test":"escaped\\text"}}`, 2, "application/json", "binary")
	out := mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/mime-bucket/o?uploadType=multipart", body, content), 200)
	if out["name"] != "🌎" {
		t.Fatal("valid surrogate pair was changed", out)
	}
	before := storageSnapshot(t, db)
	r := httptest.NewRequest("POST", "/upload/storage/v1/b/mime-bucket/o?uploadType=media&name=failed", nil).WithContext(WithActor(context.Background(), Actor{"project-one", s.environment}))
	r.Body = io.NopCloser(failedBody{})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	mustStatus(t, w, 400)
	if after := storageSnapshot(t, db); after != before {
		t.Fatal("body IO failure changed state")
	}
}

type failedBody struct{}

func (failedBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestStorageListingScopesTokensAndQuotas(t *testing.T) {
	db, s := fixture(t, ":memory:", "lists", projectPolicy, Limits{3, 3, 16, 64})
	defer db.Close()
	for _, name := range []string{"list-one", "list-two", "list-z"} {
		create(t, s, "project-one", name)
	}
	page := mustStatus(t, request(s, "project-one", "GET", "/storage/v1/b?project=project-one&maxResults=1&prefix=list-&prettyPrint=true", "", ""), 200)
	token := page["nextPageToken"].(string)
	second := mustStatus(t, request(s, "project-one", "GET", "/storage/v1/b?project=project-one&maxResults=1&prefix=list-&pageToken="+url.QueryEscape(token), "", ""), 200)
	if second["items"].([]any)[0].(map[string]any)["name"] != "list-two" {
		t.Fatal(second)
	}
	before := storageSnapshot(t, db)
	mustStatus(t, request(s, "project-one", "POST", "/storage/v1/b?project=project-one", `{"name":"list-over-budget"}`, "application/json"), 429)
	if after := storageSnapshot(t, db); after != before {
		t.Fatal("bucket quota changed state")
	}
	for _, suffix := range []string{"maxResults=01", "prefix=" + url.QueryEscape(strings.Repeat("x", 1025)), "pageToken=???", "pageToken=" + strings.Repeat("A", 8193), "pageToken=" + base64.RawURLEncoding.EncodeToString([]byte(`{"Kind":null}`)), "pageToken=" + base64.RawURLEncoding.EncodeToString([]byte(`{"unknown":"x"}`)), "pageToken=" + base64.RawURLEncoding.EncodeToString([]byte(`{}`))} {
		mustStatus(t, request(s, "project-one", "GET", "/storage/v1/b?project=project-one&"+suffix, "", ""), 400)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err = json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	payload["Scope"] = "project-two"
	tampered, _ := json.Marshal(payload)
	mustStatus(t, request(s, "project-one", "GET", "/storage/v1/b?project=project-one&prefix=list-&pageToken="+base64.RawURLEncoding.EncodeToString(tampered), "", ""), 400)
	for _, key := range []string{"folder/a", "folder/b", "flat"} {
		upload(t, s, "project-one", "list-one", key, key, "0")
	}
	objects := mustStatus(t, request(s, "project-one", "GET", "/storage/v1/b/list-one/o?prefix=folder%2F", "", ""), 200)
	if len(objects["items"].([]any)) != 2 {
		t.Fatal(objects)
	}
	objects = mustStatus(t, request(s, "project-one", "GET", "/storage/v1/b/list-one/o?delimiter=%2F&maxResults=1", "", ""), 200)
	if objects["items"].([]any)[0].(map[string]any)["name"] != "flat" {
		t.Fatal(objects)
	}
	objectToken := objects["nextPageToken"].(string)
	mustStatus(t, request(s, "project-one", "GET", "/storage/v1/b/list-two/o?delimiter=%2F&pageToken="+url.QueryEscape(objectToken), "", ""), 400)
	mustStatus(t, request(s, "operator", "GET", "/storage/v1/b?project=not-configured", "", ""), 400)
	mustStatus(t, request(s, "operator", "POST", "/storage/v1/b?project=not-configured", `{"name":"unknown-owner"}`, "application/json"), 400)
	mustStatus(t, request(s, "project-one", "POST", "/storage/v1/b?project=project-one", `{"name":"list-one"}`, "application/json"), 409)
}

func TestStorageConstructorValidationAndForeignKeyDrift(t *testing.T) {
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := Config{Environment: "constructor", Projects: []Project{{"project-one", "1"}}, AuthorizeTx: projectPolicy}
	cases := []Config{{Environment: "bad space", Projects: base.Projects, AuthorizeTx: projectPolicy}, {Environment: "constructor", AuthorizeTx: projectPolicy}}
	for _, project := range []Project{{"bad", "1"}, {"project-one", "0"}, {"project-one", "01"}, {"project-one", "not-number"}} {
		bad := base
		bad.Projects = []Project{project}
		cases = append(cases, bad)
	}
	for _, limit := range []Limits{{-1, 1, 1, 1}, {101, 1, 1, 1}, {1, 10001, 1, 1}, {1, 1, (1 << 20) + 1, 64 << 20}, {1, 1, 2, 1}, {1, 1, 1, (64 << 20) + 1}} {
		bad := base
		bad.Limits = limit
		cases = append(cases, bad)
	}
	duplicate := base
	duplicate.Projects = []Project{{"project-one", "1"}, {"project-one", "2"}}
	cases = append(cases, duplicate)
	for _, bad := range cases {
		if _, err = New(db.Database(), bad); err == nil {
			t.Fatal("invalid configuration accepted", bad.Environment, bad.Projects, bad.Limits)
		}
	}
	if _, err = New(nil, base); err == nil {
		t.Fatal("nil DB accepted")
	}
	if _, err = db.Database().Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err = New(db.Database(), base); err == nil {
		t.Fatal("disabled foreign keys accepted")
	}
	if _, err = db.Database().Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	s, err := New(db.Database(), base)
	if err != nil {
		t.Fatal(err)
	}
	create(t, s, "project-one", "valid-parent")
	if _, err = db.Database().Exec("PRAGMA foreign_keys=OFF; UPDATE gcp_storage_buckets SET project='project-missing'; PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	if _, err = New(db.Database(), base); err == nil {
		t.Fatal("unknown persisted bucket owner accepted")
	}
	if _, err = db.Database().Exec("PRAGMA foreign_keys=OFF; UPDATE gcp_storage_buckets SET project='project-one'; INSERT INTO gcp_storage_objects VALUES('constructor','missing-parent','key',X'','text/plain','{}',1,'AAAAAA==','1B2M2Y8AsgTpgAmY7PhCfg==','time','time'); PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	if _, err = New(db.Database(), base); err == nil {
		t.Fatal("orphan graph accepted")
	}
	if _, err = db.Database().Exec("DELETE FROM gcp_storage_objects; PRAGMA ignore_check_constraints=ON; UPDATE gcp_storage_sequence SET next_generation=-1; PRAGMA ignore_check_constraints=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err = New(db.Database(), base); err == nil {
		t.Fatal("negative saved sequence accepted")
	}
	closed, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if _, err = New(closed.Database(), base); err == nil {
		t.Fatal("closed DB constructor succeeded")
	}
}

func TestStorageCanceledAndPolicyInfrastructureFailuresDoNotLeakOrMutate(t *testing.T) {
	policy := func(context.Context, *sql.Tx, Actor, string, Resource) error {
		return errors.New("private policy detail")
	}
	db, s := fixture(t, ":memory:", "policy-failure", policy, Limits{})
	defer db.Close()
	before := storageSnapshot(t, db)
	w := request(s, "operator", "POST", "/storage/v1/b?project=project-one", `{"name":"failed-policy"}`, "application/json")
	mustStatus(t, w, 500)
	if strings.Contains(w.Body.String(), "private") {
		t.Fatal("policy infrastructure error leaked")
	}
	if after := storageSnapshot(t, db); after != before {
		t.Fatal("policy failure mutated state")
	}
	ctx, cancel := context.WithCancel(WithActor(context.Background(), Actor{"operator", s.environment}))
	cancel()
	r := httptest.NewRequest("POST", "/storage/v1/b?project=project-one", strings.NewReader(`{"name":"canceled-create"}`)).WithContext(ctx)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	mustStatus(t, w, 500)
	if after := storageSnapshot(t, db); after != before {
		t.Fatal("canceled request mutated state")
	}
}

func TestStorageMetadataRepresentationAndMultipartSizeContracts(t *testing.T) {
	db, s := fixture(t, ":memory:", "representation", projectPolicy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "representation-bucket")
	created := mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/representation-bucket/o?uploadType=media&name=key", "abc", ""), 200)
	if created["contentType"] != "application/octet-stream" {
		t.Fatal("missing content type did not use documented default", created)
	}
	metadata := mustStatus(t, request(s, "project-one", "GET", "/storage/v1/b/representation-bucket/o/key?alt=json&projection=noAcl&prettyPrint=true", "", ""), 200)
	if metadata["name"] != "key" || metadata["size"] != "3" || metadata["crc32c"] != "Nks/tw==" || metadata["md5Hash"] != "kAFQmDzST7DWlj99KOF/cg==" || metadata["generation"] != created["generation"] {
		t.Fatal("JSON metadata is not the current stored object", metadata)
	}
	before := storageSnapshot(t, db)
	for _, path := range []string{"/storage/v1/b?project=project-one&unknown=true", "/storage/v1/b/representation-bucket/o/key?unknown=true", "/storage/v1/b/representation-bucket/o/key?ifGenerationMatch=-1"} {
		mustStatus(t, request(s, "project-one", "GET", path, "", ""), 400)
	}
	const envelope = "--local-boundary\r\nContent-Type: application/json\r\n\r\n{\"name\":\"new\"}\r\n--local-boundary\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nabc\r\n--local-boundary--\r\n"
	mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/representation-bucket/o?uploadType=multipart", envelope, "multipart/related; boundary=local-boundary"), 400)
	mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/representation-bucket/o?uploadType=multipart", strings.Repeat("x", (1<<20)+(64<<10)+1), "multipart/related; boundary=local-boundary"), 413)
	var b bytes.Buffer
	writer := multipart.NewWriter(&b)
	for i, body := range []string{`{"name":"new"}`, strings.Repeat("x", (1<<20)+1)} {
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", "text/plain")
		if i == 0 {
			h.Set("Content-Type", "application/json")
		}
		part, err := writer.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(part, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/representation-bucket/o?uploadType=multipart", b.String(), "multipart/related; boundary="+writer.Boundary()), 413)
	if after := storageSnapshot(t, db); after != before {
		t.Fatal("metadata read or invalid representation/size request changed state")
	}
}
