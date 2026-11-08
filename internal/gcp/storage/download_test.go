package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageDownloadCurrentGenerationSelectionAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "download.sqlite")
	db, s := fixture(t, path, "download-generation", projectPolicy, Limits{})
	defer func() { _ = db.Close() }()
	create(t, s, "project-one", "download-bucket")
	create(t, s, "project-two", "download-peer")
	keys := []string{"foo", "foo/acl/entity", "foo%2Facl%2Fentity", "a/../b", "b", "a//b", "a/b", "日本語/☁"}
	generations := map[string]string{}
	for _, key := range keys {
		out := upload(t, s, "project-one", "download-bucket", key, "original:"+key, "0")
		generations[key] = out["generation"].(string)
	}
	stale := generations["a/../b"]
	generations["a/../b"] = upload(t, s, "project-one", "download-bucket", "a/../b", "current:a/../b", stale)["generation"].(string)
	downloadPath := func(key string) string {
		return "/download" + objectPath("download-bucket", key) + "?alt=media&generation=" + generations[key]
	}
	verify := func() {
		t.Helper()
		for _, key := range keys {
			w := request(s, "project-one", "GET", downloadPath(key), "", "")
			want := "original:" + key
			if key == "a/../b" {
				want = "current:a/../b"
			}
			if w.Code != 200 || w.Body.String() != want || w.Header().Get("X-Goog-Generation") != generations[key] {
				t.Fatal("download wrong literal key/generation", key, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
				t.Fatal("download bypassed media safety headers")
			}
		}
	}
	verify()
	before := storageSnapshot(t, db)
	for _, uri := range []string{
		"/download" + objectPath("download-bucket", "a/../b") + "?alt=media&generation=" + stale,
		objectPath("download-bucket", "a/../b") + "?generation=" + stale,
	} {
		mustStatus(t, request(s, "project-one", "GET", uri, "", ""), 404)
	}
	mustStatus(t, request(s, "project-one", "DELETE", objectPath("download-bucket", "a/../b")+"?generation="+stale, "", ""), 404)
	mustStatus(t, request(s, "project-one", "GET", downloadPath("a/../b")+"&ifGenerationMatch="+stale, "", ""), 412)
	mustStatus(t, request(s, "project-one", "DELETE", objectPath("download-bucket", "a/../b")+"?generation="+generations["a/../b"]+"&ifGenerationMatch="+stale, "", ""), 412)
	for _, subject := range []string{"project-two", "reader"} {
		mustStatus(t, request(s, subject, "GET", downloadPath("a/../b"), "", ""), 403)
		mustStatus(t, request(s, subject, "GET", "/download"+objectPath("download-bucket", "a/../b")+"?alt=media&generation="+stale, "", ""), 403)
		mustStatus(t, request(s, subject, "DELETE", objectPath("download-bucket", "a/../b")+"?generation="+generations["a/../b"], "", ""), 403)
	}
	for _, selector := range []string{"0", "-1", "01", "9223372036854775808", "no"} {
		mustStatus(t, request(s, "project-one", "GET", objectPath("download-bucket", "foo")+"?generation="+selector, "", ""), 400)
	}
	mustStatus(t, request(s, "project-one", "DELETE", downloadPath("foo"), "", ""), 405)
	mustStatus(t, request(s, "project-one", "GET", "/download"+objectPath("download-bucket", "foo"), "", ""), 400)
	mustStatus(t, request(s, "project-one", "GET", "/download/storage/v1/b/download-bucket/o/foo/acl/entity?alt=media", "", ""), 501)
	if after := storageSnapshot(t, db); after != before {
		t.Fatal("selector/permission/unsupported download refusal changed state")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, s = fixture(t, path, "download-generation", projectPolicy, Limits{})
	verify()
	mustStatus(t, request(s, "project-one", "DELETE", objectPath("download-bucket", "a/../b")+"?generation="+generations["a/../b"], "", ""), 204)
	mustStatus(t, request(s, "project-one", "GET", downloadPath("a/../b"), "", ""), 404)
	for _, key := range keys {
		if key != "a/../b" {
			expectMedia(t, s, "project-one", "download-bucket", key, "original:"+key)
		}
	}
}
