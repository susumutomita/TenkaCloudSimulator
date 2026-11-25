package server

import (
	"crypto/md5"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAzureRangeHeadersTraverseAuthenticatedProviderBoundary(t *testing.T) {
	cfg := DefaultCloudStorageConfig()
	handler, closeStore, err := NewWithCloudStorage(filepath.Join(t.TempDir(), "azure-range.sqlite"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeStore(); err != nil {
			t.Error(err)
		}
	})
	token := strings.Repeat("a", 64)
	access, err := NewAccess(handler, token, false)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, accessToken string, headers map[string]string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		r.Header.Set("x-ms-version", "2023-11-03")
		if accessToken != "" {
			r.Header.Set("X-Simulator-Token", accessToken)
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		access.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("status %d want %d: %s", w.Code, status, w.Body.String())
		}
		return w
	}
	container := "/_simulator/providers/azure/" + cfg.AzureAccount + "/range-check"
	const stored = "0123456789"
	const selected = "2345"
	call(http.MethodPut, container+"?restype=container", "", token, nil, http.StatusCreated)
	call(http.MethodPut, container+"/data", stored, token, map[string]string{"x-ms-blob-type": "BlockBlob"}, http.StatusCreated)
	headers := map[string]string{"x-ms-range": "bytes=2-5", "x-ms-range-get-content-md5": "true"}
	for _, actor := range []struct {
		token  string
		status int
		header map[string]string
	}{
		{"", http.StatusUnauthorized, headers},
		{token, http.StatusForbidden, map[string]string{
			"x-ms-range": "bytes=2-5", "x-ms-range-get-content-md5": "true", "X-Simulator-Principal": "reader",
		}},
	} {
		w := call(http.MethodGet, container+"/data", "", actor.token, actor.header, actor.status)
		for _, k := range []string{"x-ms-blob-content-md5", "Content-MD5", "Content-Range"} {
			if w.Result().Header.Get(k) != "" {
				t.Fatalf("status %d leaked %s", actor.status, k)
			}
		}
	}
	w := call(http.MethodGet, container+"/data", "", token, headers, http.StatusPartialContent)
	sum := func(payload string) string {
		h := md5.Sum([]byte(payload))
		return base64.StdEncoding.EncodeToString(h[:])
	}
	if hdr := w.Result().Header; w.Body.String() != selected || hdr.Get("Content-Range") != "bytes 2-5/10" ||
		hdr.Get("x-ms-blob-content-md5") != sum(stored) || hdr.Get("Content-MD5") != sum(selected) {
		t.Fatalf("range metadata did not traverse authenticated provider route: %v", hdr)
	}
}
