package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// This is a synthetic project policy, not an implementation of Google IAM.
func projectPolicy(_ context.Context, _ *sql.Tx, actor Actor, _ string, r Resource) error {
	if actor.Subject == "operator" || actor.Subject == r.Project {
		return nil
	}
	return ErrDenied
}
func fixture(t *testing.T, path, env string, authorize AuthorizeTx, limits Limits) (*core.Store, *Store) {
	t.Helper()
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(db.Database(), Config{Environment: env, Projects: []Project{{"project-one", "1001"}, {"project-two", "1002"}}, AuthorizeTx: authorize, Limits: limits})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, s
}
func request(s *Store, subject, method, path, body, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if subject != "" {
		r = r.WithContext(WithActor(r.Context(), Actor{subject, s.environment}))
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func mustStatus(t *testing.T, w *httptest.ResponseRecorder, want int) map[string]any {
	t.Helper()
	if w.Code != want {
		t.Fatalf("HTTP %d want %d: %s", w.Code, want, w.Body.String())
	}
	out := map[string]any{}
	if want != 204 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}
func create(t *testing.T, s *Store, subject, name string) {
	t.Helper()
	mustStatus(t, request(s, subject, "POST", "/storage/v1/b?project="+subject, `{"name":"`+name+`"}`, "application/json"), 200)
}
func upload(t *testing.T, s *Store, subject, bucket, name, body, condition string) map[string]any {
	t.Helper()
	path := "/upload/storage/v1/b/" + bucket + "/o?uploadType=media&name=" + url.QueryEscape(name)
	if condition != "" {
		path += "&ifGenerationMatch=" + condition
	}
	return mustStatus(t, request(s, subject, "POST", path, body, "text/plain"), 200)
}
func objectPath(bucket, name string) string {
	return "/storage/v1/b/" + bucket + "/o/" + url.PathEscape(name)
}
func expectMedia(t *testing.T, s *Store, subject, bucket, name, body string) {
	t.Helper()
	w := request(s, subject, "GET", objectPath(bucket, name)+"?alt=media", "", "")
	if w.Code != 200 || w.Body.String() != body {
		t.Fatalf("key %q got %d %q want %q", name, w.Code, w.Body.String(), body)
	}
	if !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("unsafe media headers", w.Header())
	}
}
func keySet(t *testing.T, s *Store, subject, bucket string) map[string]bool {
	t.Helper()
	out := mustStatus(t, request(s, subject, "GET", "/storage/v1/b/"+bucket+"/o", "", ""), 200)
	keys := map[string]bool{}
	for _, item := range out["items"].([]any) {
		keys[item.(map[string]any)["name"].(string)] = true
	}
	return keys
}

func storageSnapshot(t *testing.T, db *core.Store) string {
	t.Helper()
	var snapshot []any
	for _, query := range []string{
		"SELECT * FROM gcp_storage_projects ORDER BY environment,id",
		"SELECT * FROM gcp_storage_buckets ORDER BY environment,name",
		"SELECT * FROM gcp_storage_objects ORDER BY environment,bucket,name",
		"SELECT * FROM gcp_storage_sequence ORDER BY environment",
		"SELECT environment,count(*),SUM(length(data)+length(CAST(metadata AS BLOB))) FROM gcp_storage_objects GROUP BY environment ORDER BY environment",
	} {
		rows, err := db.Database().Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var values [][]any
		for rows.Next() {
			row := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range row {
				targets[i] = &row[i]
			}
			if err = rows.Scan(targets...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			values = append(values, row)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		snapshot = append(snapshot, values)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func multipartUpload(t *testing.T, s *Store, name, body, crc, md string) *httptest.ResponseRecorder {
	t.Helper()
	var envelope bytes.Buffer
	writer := multipart.NewWriter(&envelope)
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", "application/json")
	part, err := writer.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]any{"name": name, "crc32c": crc, "md5Hash": md})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(metadata); err != nil {
		t.Fatal(err)
	}
	h = textproto.MIMEHeader{}
	h.Set("Content-Type", "application/octet-stream")
	part, err = writer.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return request(s, "project-one", "POST", "/upload/storage/v1/b/checksum-bucket/o?uploadType=multipart", envelope.String(), "multipart/related; boundary="+writer.Boundary())
}

func TestStorageIndependentChecksumVectorsAndRefusalFrame(t *testing.T) {
	db, s := fixture(t, ":memory:", "checksums", projectPolicy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "checksum-bucket")
	// Independent primary numeric vectors, base64-encoded literally rather than
	// generated by checksums(): Go1.26.10 hash/crc32/crc32_test.go golden and
	// crypto/md5/md5_test.go golden (the latter includes RFC1321 A.5 vectors).
	// RFC3720 B.4 additionally lists the32zero/ff CRC octets in iSCSI order;
	// Cloud Storage instead serializes the numeric CRC in big-endian order.
	vectors := []struct{ body, crc, md string }{
		{"", "AAAAAA==", "1B2M2Y8AsgTpgAmY7PhCfg=="},
		{"a", "wdBDMA==", "DMF1ucDxtqgxw5niaXcmYQ=="},
		{"abc", "Nks/tw==", "kAFQmDzST7DWlj99KOF/cg=="},
		{strings.Repeat("\x00", 32), "ipE2qg==", ""},
		{strings.Repeat("\xff", 32), "YqirQw==", ""},
	}
	for i, v := range vectors {
		name := fmt.Sprintf("vector-%d", i)
		out := mustStatus(t, multipartUpload(t, s, name, v.body, v.crc, v.md), 200)
		if out["crc32c"] != v.crc || v.md != "" && out["md5Hash"] != v.md {
			t.Fatalf("independent vector %d mismatch: %v", i, out)
		}
		expectMedia(t, s, "project-one", "checksum-bucket", name, v.body)
	}
	for _, name := range []string{"vector-2", "must-stay-absent"} {
		for _, bad := range []struct{ crc, md string }{{"AAAAAA==", "kAFQmDzST7DWlj99KOF/cg=="}, {"Nks/tw==", "1B2M2Y8AsgTpgAmY7PhCfg=="}} {
			before := storageSnapshot(t, db)
			mustStatus(t, multipartUpload(t, s, name, "abc", bad.crc, bad.md), 400)
			if after := storageSnapshot(t, db); after != before {
				t.Fatal("bad checksum changed object/sequence/budget state")
			}
		}
	}
}

func TestStorageGenerationNeverReusedAcrossDeletesAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.sqlite")
	db, s := fixture(t, path, "freshness", projectPolicy, Limits{})
	defer func() { _ = db.Close() }()
	create(t, s, "project-two", "untouched-peer")
	upload(t, s, "project-two", "untouched-peer", "sentinel", "peer", "0")
	create(t, s, "project-one", "fresh-bucket")
	first := upload(t, s, "project-one", "fresh-bucket", "key", "first", "0")["generation"].(string)
	mustStatus(t, request(s, "project-one", "DELETE", objectPath("fresh-bucket", "key")+"?ifGenerationMatch="+first, "", ""), 204)
	second := upload(t, s, "project-one", "fresh-bucket", "key", "second", "0")["generation"].(string)
	newer := func(previous, current string) {
		t.Helper()
		a, e := strconv.ParseInt(previous, 10, 64)
		if e != nil {
			t.Fatal(e)
		}
		b, e := strconv.ParseInt(current, 10, 64)
		if e != nil || b <= a {
			t.Fatalf("generation reused/regressed: %s -> %s (%v)", previous, current, e)
		}
	}
	newer(first, second)
	assertStale := func(stale string) {
		t.Helper()
		before := storageSnapshot(t, db)
		mustStatus(t, request(s, "project-one", "GET", objectPath("fresh-bucket", "key")+"?ifGenerationMatch="+stale, "", ""), 412)
		mustStatus(t, request(s, "project-one", "DELETE", objectPath("fresh-bucket", "key")+"?ifGenerationMatch="+stale, "", ""), 412)
		mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/fresh-bucket/o?uploadType=media&name=key&ifGenerationMatch="+stale, "stale-write", "text/plain"), 412)
		if after := storageSnapshot(t, db); after != before {
			t.Fatal("stale generation mutated persisted state")
		}
	}
	assertStale(first)
	mustStatus(t, request(s, "project-one", "DELETE", objectPath("fresh-bucket", "key"), "", ""), 204)
	mustStatus(t, request(s, "project-one", "DELETE", "/storage/v1/b/fresh-bucket", "", ""), 204)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, s = fixture(t, path, "freshness", projectPolicy, Limits{})
	create(t, s, "project-one", "fresh-bucket")
	third := upload(t, s, "project-one", "fresh-bucket", "key", "third", "0")["generation"].(string)
	newer(second, third)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, s = fixture(t, path, "freshness", projectPolicy, Limits{})
	assertStale(first)
	assertStale(second)
	expectMedia(t, s, "project-one", "fresh-bucket", "key", "third")
	expectMedia(t, s, "project-two", "untouched-peer", "sentinel", "peer")
}

func TestStorageObjectSegmentDoesNotSwallowUnsupportedSubresources(t *testing.T) {
	db, s := fixture(t, ":memory:", "route-segments", projectPolicy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "route-bucket")
	create(t, s, "project-two", "peer-route-bucket")
	keys := []string{"foo", "foo/acl", "foo/acl/user-demo", "foo/acl/user-demo/extra", "foo%2Facl%2Fuser-demo"}
	bodies := map[string]string{}
	for i, key := range keys {
		bodies[key] = fmt.Sprintf("distinct-%d", i)
		upload(t, s, "project-one", "route-bucket", key, bodies[key], "0")
	}
	upload(t, s, "project-two", "peer-route-bucket", "foo/acl/user-demo", "peer", "0")
	for _, tail := range []string{"foo/acl", "foo/acl/user-demo", "foo/acl/user-demo/extra", "foo%2Facl/user-demo"} {
		for _, method := range []string{"DELETE", "GET"} {
			before := storageSnapshot(t, db)
			mustStatus(t, request(s, "project-one", method, "/storage/v1/b/route-bucket/o/"+tail, "", ""), 501)
			if after := storageSnapshot(t, db); after != before {
				t.Fatal("unsupported subresource changed state", method, tail)
			}
		}
	}
	for key, body := range bodies {
		expectMedia(t, s, "project-one", "route-bucket", key, body)
	}
	// The percent-encoded single segment is a real literal object, never ACL API.
	target := "foo/acl/user-demo"
	mustStatus(t, request(s, "project-one", "DELETE", objectPath("route-bucket", target), "", ""), 204)
	delete(bodies, target)
	if got := keySet(t, s, "project-one", "route-bucket"); len(got) != len(bodies) {
		t.Fatal("encoded deletion changed key count", got)
	} else {
		for key, body := range bodies {
			if !got[key] {
				t.Fatal("encoded deletion removed neighbor", key)
			}
			expectMedia(t, s, "project-one", "route-bucket", key, body)
		}
	}
	expectMedia(t, s, "project-two", "peer-route-bucket", "foo/acl/user-demo", "peer")
	// A trusted adapter accidentally supplying inconsistent Path/RawPath must
	// fail closed instead of letting EscapedPath silently discard its RawPath.
	r := httptest.NewRequest("DELETE", objectPath("route-bucket", "foo"), nil)
	r.URL.RawPath = "/storage/v1/b/route-bucket/o/foo%2Facl"
	r = r.WithContext(WithActor(r.Context(), Actor{"project-one", s.environment}))
	w := httptest.NewRecorder()
	before := storageSnapshot(t, db)
	s.ServeHTTP(w, r)
	mustStatus(t, w, 400)
	if after := storageSnapshot(t, db); after != before {
		t.Fatal("invalid paired URL changed state")
	}
}

func TestStorageExactCollidingKeysAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, s := fixture(t, path, "exercise-a", projectPolicy, Limits{})
	create(t, s, "project-one", "literal-keys")
	keys := []string{"a/b", "a//b", "a/./b", "a/../b", "b", "./b", "/b", "%2F", "/", "%252F", "a%2Fb", "日本語/☁", "é"}
	want := map[string]bool{}
	bodies := map[string]string{}
	for i, key := range keys {
		want[key] = true
		bodies[key] = fmt.Sprintf("unique-body-%d", i)
		upload(t, s, "project-one", "literal-keys", key, bodies[key], "0")
	}
	verify := func() {
		t.Helper()
		if got := keySet(t, s, "project-one", "literal-keys"); !reflect.DeepEqual(got, want) {
			t.Fatalf("exact key set: %v want %v", got, want)
		}
		for key, body := range bodies {
			expectMedia(t, s, "project-one", "literal-keys", key, body)
		}
	}
	verify()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, s = fixture(t, path, "exercise-a", projectPolicy, Limits{})
	defer db.Close()
	verify()
	for i := len(keys) - 1; i >= 0; i-- {
		key := keys[i]
		mustStatus(t, request(s, "project-one", "DELETE", objectPath("literal-keys", key), "", ""), 204)
		delete(want, key)
		delete(bodies, key)
		verify()
	}
	mustStatus(t, request(s, "project-one", "DELETE", "/storage/v1/b/literal-keys", "", ""), 204)
}

func TestStorageActualOwnerAuthorizationAndEnvironment(t *testing.T) {
	db, s := fixture(t, ":memory:", "environment-a", projectPolicy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "shared-global-name")
	upload(t, s, "project-one", "shared-global-name", "secret", "one", "0")
	create(t, s, "project-two", "peer-project-data")
	upload(t, s, "project-two", "peer-project-data", "secret", "two", "0")
	// Typed actor is mandatory; HTTP headers cannot manufacture one.
	r := httptest.NewRequest("GET", "/storage/v1/b/shared-global-name", nil)
	r.Header.Set("X-Project-ID", "project-one")
	r.Header.Set("Authorization", "Bearer synthetic")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	mustStatus(t, w, 401)
	for _, path := range []string{"/storage/v1/b/shared-global-name", "/storage/v1/b/shared-global-name/o", "/storage/v1/b/shared-global-name/o/secret?alt=media", "/storage/v1/b/missing-bucket"} {
		mustStatus(t, request(s, "project-two", "GET", path, "", ""), 403)
	}
	mustStatus(t, request(s, "project-two", "DELETE", "/storage/v1/b/shared-global-name/o/secret", "", ""), 403)
	mustStatus(t, request(s, "project-two", "POST", "/upload/storage/v1/b/shared-global-name/o?uploadType=media&name=secret", "two", "text/plain"), 403)
	mustStatus(t, request(s, "project-two", "POST", "/storage/v1/b?project=project-two", `{"name":"shared-global-name"}`, "application/json"), 403)
	expectMedia(t, s, "project-one", "shared-global-name", "secret", "one")
	expectMedia(t, s, "project-two", "peer-project-data", "secret", "two")
	for _, project := range []string{"project-one", "project-two"} {
		listed := mustStatus(t, request(s, project, "GET", "/storage/v1/b?project="+project, "", ""), 200)
		items := listed["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["projectNumber"] != map[string]string{"project-one": "1001", "project-two": "1002"}[project] {
			t.Fatal("project bucket list mixed", listed)
		}
	}
	// Same SQLite, different trusted environment: global names are isolated locally.
	s2, err := New(db.Database(), Config{Environment: "environment-b", Projects: []Project{{"project-one", "1001"}, {"project-two", "1002"}}, AuthorizeTx: projectPolicy})
	if err != nil {
		t.Fatal(err)
	}
	create(t, s2, "project-two", "shared-global-name")
	upload(t, s2, "project-two", "shared-global-name", "secret", "other-environment", "0")
	expectMedia(t, s, "project-one", "shared-global-name", "secret", "one")
	expectMedia(t, s2, "project-two", "shared-global-name", "secret", "other-environment")
	r = httptest.NewRequest("GET", "/storage/v1/b/shared-global-name", nil).WithContext(WithActor(context.Background(), Actor{"project-two", "environment-b"}))
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	mustStatus(t, w, 401)
}

func TestStorageOverwritePermissionsGenerationAndBudgetRollback(t *testing.T) {
	denyDelete := false
	policy := func(ctx context.Context, tx *sql.Tx, a Actor, p string, r Resource) error {
		if denyDelete && p == "storage.objects.delete" {
			return ErrDenied
		}
		return projectPolicy(ctx, tx, a, p, r)
	}
	db, s := fixture(t, ":memory:", "budget", policy, Limits{2, 2, 16, 32})
	defer db.Close()
	create(t, s, "project-one", "budget-bucket")
	first := upload(t, s, "project-one", "budget-bucket", "key", "initial", "0")
	gen := first["generation"].(string)
	denyDelete = true
	mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/budget-bucket/o?uploadType=media&name=key", "denied", "text/plain"), 403)
	denyDelete = false
	for _, condition := range []string{"0", "999"} {
		mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/budget-bucket/o?uploadType=media&name=key&ifGenerationMatch="+condition, "wrong", "text/plain"), 412)
	}
	mustStatus(t, request(s, "project-one", "DELETE", objectPath("budget-bucket", "key")+"?ifGenerationMatch=999", "", ""), 412)
	expectMedia(t, s, "project-one", "budget-bucket", "key", "initial")
	second := upload(t, s, "project-one", "budget-bucket", "key", "updated", gen)
	if second["generation"] == gen {
		t.Fatal("overwrite did not allocate a new generation")
	}
	mustStatus(t, request(s, "project-one", "DELETE", "/storage/v1/b/budget-bucket", "", ""), 409)
	upload(t, s, "project-one", "budget-bucket", "second", "1234567890123456", "0")
	var before int64
	if err := db.Database().QueryRow("SELECT next_generation FROM gcp_storage_sequence WHERE environment='budget'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/budget-bucket/o?uploadType=media&name=third", "x", "text/plain"), 429)
	mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/budget-bucket/o?uploadType=media&name=key", "1234567890123456", "text/plain"), 429)
	var after int64
	if err := db.Database().QueryRow("SELECT next_generation FROM gcp_storage_sequence WHERE environment='budget'").Scan(&after); err != nil || after != before {
		t.Fatal("budget refusal changed generation", before, after, err)
	}
	expectMedia(t, s, "project-one", "budget-bucket", "key", "updated")
	for _, key := range []string{"key", "second"} {
		mustStatus(t, request(s, "project-one", "DELETE", objectPath("budget-bucket", key), "", ""), 204)
	}
	mustStatus(t, request(s, "project-one", "DELETE", "/storage/v1/b/budget-bucket", "", ""), 204)
}

func TestStorageMultipartChecksumsAndPagination(t *testing.T) {
	db, s := fixture(t, ":memory:", "multipart", projectPolicy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "multipart-bucket")
	data := "<html>not executable</html>"
	crc, md := checksums([]byte(data))
	var envelope bytes.Buffer
	writer := multipart.NewWriter(&envelope)
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", "application/json")
	part, err := writer.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"name": "ignored-metadata-name", "bucket": "ignored-bucket", "contentType": "text/html", "metadata": map[string]string{"lesson": "safe"}, "crc32c": crc, "md5Hash": md})
	if _, err = part.Write(meta); err != nil {
		t.Fatal(err)
	}
	h = textproto.MIMEHeader{}
	h.Set("Content-Type", "application/octet-stream")
	part, err = writer.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	out := mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/multipart-bucket/o?uploadType=multipart&name=actual%2Fname", envelope.String(), "multipart/related; boundary="+writer.Boundary()), 200)
	if out["name"] != "actual/name" || out["crc32c"] != crc || out["md5Hash"] != md {
		t.Fatal(out)
	}
	expectMedia(t, s, "project-one", "multipart-bucket", "actual/name", data)
	for _, key := range []string{"a", "b", "dir/one", "dir/two"} {
		upload(t, s, "project-one", "multipart-bucket", key, key, "0")
	}
	first := mustStatus(t, request(s, "project-one", "GET", "/storage/v1/b/multipart-bucket/o?maxResults=1&delimiter=%2F", "", ""), 200)
	if first["items"].([]any)[0].(map[string]any)["name"] != "a" {
		t.Fatal(first)
	}
	token := first["nextPageToken"].(string)
	second := mustStatus(t, request(s, "project-one", "GET", "/storage/v1/b/multipart-bucket/o?maxResults=1&delimiter=%2F&pageToken="+url.QueryEscape(token), "", ""), 200)
	if second["prefixes"].([]any)[0] != "actual/" {
		t.Fatal(second)
	}
	mustStatus(t, request(s, "project-one", "GET", "/storage/v1/b/multipart-bucket/o?pageToken="+url.QueryEscape(token), "", ""), 400)
	mustStatus(t, request(s, "operator", "GET", "/storage/v1/b/multipart-bucket/o?delimiter=%2F&pageToken="+url.QueryEscape(token), "", ""), 400)
}

func TestStorageRejectUnsupportedOrAmbiguousInput(t *testing.T) {
	db, s := fixture(t, ":memory:", "strict", projectPolicy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "strict-bucket")
	for _, body := range []string{`{"name":"other-bucket","name":"duplicate-bucket"}`, `{"name":"other-bucket","iamConfiguration":{}}`, `{"name":null}`, `{"name":"lone-\ud800"}`, `{"name":"other-bucket"} {}`, `{"Name":"other-bucket"}`} {
		mustStatus(t, request(s, "project-one", "POST", "/storage/v1/b?project=project-one", body, "application/json"), 400)
	}
	for _, query := range []string{"uploadType=resumable&name=x", "uploadType=media&name=x&generation=1", "uploadType=media&name=x&ifGenerationMatch=-1", "uploadType=media&name=x&ifGenerationMatch=01", "uploadType=media&name=x&name=y", "uploadType=media&name=x&projection=full"} {
		mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/strict-bucket/o?"+query, "x", "text/plain"), 400)
	}
	for _, path := range []string{"/storage/v1/b/strict-bucket?userProject=project-two", "/storage/v1/b/strict-bucket/o?versions=true", "/storage/v1/b/strict-bucket/o?maxResults=0", "/storage/v1/b/strict-bucket/o?delimiter=x"} {
		mustStatus(t, request(s, "project-one", "GET", path, "", ""), 400)
	}
	w := request(s, "project-one", "POST", "/upload/storage/v1/b/strict-bucket/o?uploadType=media&name=x", strings.Repeat("x", (1<<20)+1), "text/plain")
	mustStatus(t, w, 413)
	if len(keySet(t, s, "project-one", "strict-bucket")) != 0 {
		t.Fatal("invalid requests created objects")
	}
}

func TestStorageAtomicFailureAndPersistedCorruption(t *testing.T) {
	db, s := fixture(t, ":memory:", "fault", projectPolicy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "fault-bucket")
	old := upload(t, s, "project-one", "fault-bucket", "key", "prior", "0")
	_, err := db.Database().Exec(`CREATE TRIGGER storage_abort BEFORE UPDATE ON gcp_storage_objects BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/fault-bucket/o?uploadType=media&name=key", "new", "text/plain"), 500)
	var generation int64
	if err = db.Database().QueryRow("SELECT next_generation FROM gcp_storage_sequence WHERE environment='fault'").Scan(&generation); err != nil || strconv.FormatInt(generation, 10) != old["generation"] {
		t.Fatal("failed write did not roll back sequence", generation, err)
	}
	expectMedia(t, s, "project-one", "fault-bucket", "key", "prior")
	if _, err = db.Database().Exec("DROP TRIGGER storage_abort; CREATE TRIGGER storage_ignore BEFORE UPDATE ON gcp_storage_objects BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/fault-bucket/o?uploadType=media&name=key", "ignored", "text/plain"), 500)
	expectMedia(t, s, "project-one", "fault-bucket", "key", "prior")
	if _, err = db.Database().Exec("DROP TRIGGER storage_ignore; UPDATE gcp_storage_objects SET crc32c='corrupt'"); err != nil {
		t.Fatal(err)
	}
	w := request(s, "project-one", "GET", objectPath("fault-bucket", "key")+"?alt=media", "", "")
	mustStatus(t, w, 500)
	if strings.Contains(w.Body.String(), "prior") || strings.Contains(w.Body.String(), "corrupt") {
		t.Fatal("corruption leaked stored fields")
	}
}

func TestStorageGenerationCompareAndSwapConcurrent(t *testing.T) {
	db, s := fixture(t, ":memory:", "cas", projectPolicy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "cas-bucket")
	first := upload(t, s, "project-one", "cas-bucket", "key", "original", "0")
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, value := range []string{"winner-a", "winner-b"} {
		wg.Add(1)
		go func(value string) {
			defer wg.Done()
			statuses <- request(s, "project-one", "POST", "/upload/storage/v1/b/cas-bucket/o?uploadType=media&name=key&ifGenerationMatch="+first["generation"].(string), value, "text/plain").Code
		}(value)
	}
	wg.Wait()
	close(statuses)
	got := map[int]int{}
	for status := range statuses {
		got[status]++
	}
	if got[200] != 1 || got[412] != 1 {
		t.Fatal("CAS must have exactly one winner", got)
	}
}

func TestStorageConstructorAndMissingOwnerCallback(t *testing.T) {
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := Config{Environment: "constructor", Projects: []Project{{"project-one", "1"}}, AuthorizeTx: projectPolicy}
	bad := base
	bad.AuthorizeTx = nil
	if _, err = New(db.Database(), bad); err == nil {
		t.Fatal("nil authorizer accepted")
	}
	seen := false
	base.AuthorizeTx = func(_ context.Context, _ *sql.Tx, _ Actor, p string, r Resource) error {
		if p != "storage.buckets.get" || r.Exists || r.Project != "" || r.Bucket != "missing-bucket" {
			t.Fatalf("invented missing owner: %s %#v", p, r)
		}
		seen = true
		return ErrDenied
	}
	s, err := New(db.Database(), base)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "project-one", "GET", "/storage/v1/b/missing-bucket", "", ""), 403)
	if !seen {
		t.Fatal("missing resource bypassed callback")
	}
}

func TestStorageRequiredPolicyReadsSameTransaction(t *testing.T) {
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Database().Exec(`CREATE TABLE local_storage_grants(subject TEXT,project TEXT,permission TEXT,PRIMARY KEY(subject,project,permission));INSERT INTO local_storage_grants VALUES('exercise-user','project-one','storage.buckets.create'),('exercise-user','project-one','storage.buckets.get')`); err != nil {
		t.Fatal(err)
	}
	policy := func(ctx context.Context, tx *sql.Tx, a Actor, p string, r Resource) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM local_storage_grants WHERE subject=? AND project=? AND permission=?", a.Subject, r.Project, p).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return ErrDenied
		}
		return nil
	}
	s, err := New(db.Database(), Config{Environment: "policy", Projects: []Project{{"project-one", "1001"}}, AuthorizeTx: policy})
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "exercise-user", "POST", "/storage/v1/b?project=project-one", `{"name":"policy-bucket"}`, "application/json"), 200)
	mustStatus(t, request(s, "exercise-user", "GET", "/storage/v1/b/policy-bucket", "", ""), 200)
	if _, err = db.Database().Exec("DELETE FROM local_storage_grants WHERE permission='storage.buckets.get'"); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "exercise-user", "GET", "/storage/v1/b/policy-bucket", "", ""), 403)
	mustStatus(t, request(s, "exercise-user", "DELETE", "/storage/v1/b/policy-bucket", "", ""), 403)
	var count int
	if err = db.Database().QueryRow("SELECT count(*) FROM gcp_storage_buckets WHERE environment='policy'").Scan(&count); err != nil || count != 1 {
		t.Fatal("denied deletion changed state", count, err)
	}
}

func TestStorageReopenRejectsChangedConfigurationAndCorruptSequence(t *testing.T) {
	db, s := fixture(t, ":memory:", "config", projectPolicy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "configured-bucket")
	upload(t, s, "project-one", "configured-bucket", "key", "value", "0")
	for _, cfg := range []Config{
		{Environment: "config", Projects: []Project{{"project-one", "9999"}}, AuthorizeTx: projectPolicy},
		{Environment: "config", Projects: []Project{{"project-two", "1002"}}, AuthorizeTx: projectPolicy},
		{Environment: "config", Projects: []Project{{"project-one", "1001"}}, AuthorizeTx: projectPolicy, Limits: Limits{1, 1, 2, 2}},
		{Environment: "config", Projects: []Project{{"project-one", "1001"}, {"project-two", "1001"}}, AuthorizeTx: projectPolicy},
	} {
		if _, err := New(db.Database(), cfg); err == nil {
			t.Fatal("invalid persisted/configured binding accepted", cfg.Projects, cfg.Limits)
		}
	}
	if _, err := db.Database().Exec("UPDATE gcp_storage_sequence SET next_generation=0 WHERE environment='config'"); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db.Database(), Config{Environment: "config", Projects: []Project{{"project-one", "1001"}, {"project-two", "1002"}}, AuthorizeTx: projectPolicy}); err == nil {
		t.Fatal("sequence behind existing generation accepted")
	}
}
