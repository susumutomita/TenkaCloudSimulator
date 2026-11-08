package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudStorageAuthenticatedCompositionIsolationAndRestart(t *testing.T) {
	type host struct {
		server    *httptest.Server
		access    *Access
		close     func() error
		token, db string
	}
	cfg := DefaultCloudStorageConfig()
	start := func(db, token string) *host {
		t.Helper()
		handler, closeStore, err := NewWithCloudStorage(db, cfg)
		if err != nil {
			t.Fatal(err)
		}
		access, err := NewAccess(handler, token, false)
		if err != nil {
			closeStore()
			t.Fatal(err)
		}
		return &host{httptest.NewServer(access), access, closeStore, token, db}
	}
	stop := func(h *host) {
		h.server.Close()
		if err := h.close(); err != nil {
			t.Fatal(err)
		}
	}
	a := start(filepath.Join(t.TempDir(), "a.db"), strings.Repeat("a", 64))
	defer func() { stop(a) }()
	b := start(filepath.Join(t.TempDir(), "b.db"), strings.Repeat("b", 64))
	defer stop(b)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	call := func(h *host, method, path, body, token string, headers map[string]string, want int) ([]byte, http.Header) {
		t.Helper()
		req, err := http.NewRequest(method, h.server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("X-Simulator-Token", token)
		}
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d body=%q", method, path, response.StatusCode, want, data)
		}
		return data, response.Header
	}
	gcp := "/_simulator/providers/gcp/storage/v1/b"
	bucket := "composition-bucket"
	azure := "/_simulator/providers/azure/" + cfg.AzureAccount + "/composition-container"
	native := map[string]string{"x-ms-version": "2023-11-03"}
	call(a, "GET", gcp+"?project="+cfg.GCPProject, "", "", nil, 401)
	call(a, "GET", gcp+"?project="+cfg.GCPProject, "", b.token, nil, 401)
	call(b, "GET", azure+"?restype=container", "", a.token, native, 401)
	// The operator can select a reader exercise identity but gains no provider-owner grant.
	reader := map[string]string{"X-Simulator-Principal": "reader", "Content-Type": "application/json"}
	call(a, "POST", gcp+"?project="+cfg.GCPProject, `{"name":"forbidden-bucket"}`, a.token, reader, 403)
	call(a, "PUT", azure+"?restype=container", "", a.token, map[string]string{"x-ms-version": "2023-11-03", "X-Simulator-Principal": "reader"}, 403)
	for _, h := range []*host{a, b} {
		call(h, "POST", gcp+"?project="+cfg.GCPProject, `{"name":"`+bucket+`"}`, h.token, map[string]string{"Content-Type": "application/json"}, 200)
		call(h, "PUT", azure+"?restype=container", "", h.token, native, 201)
	}
	keys := []string{"a/b", "a//b", "a/../b", "a%2Fb", "space key", "日本語"}
	generations := map[string]string{}
	for _, h := range []*host{a, b} {
		label := "first:"
		if h == b {
			label = "second:"
		}
		for _, key := range keys {
			body := label + key
			data, _ := call(h, "POST", "/_simulator/providers/gcp/upload/storage/v1/b/"+bucket+"/o?uploadType=media&name="+url.QueryEscape(key), body, h.token, map[string]string{"Content-Type": "text/plain"}, 200)
			var object struct {
				Generation string `json:"generation"`
			}
			if json.Unmarshal(data, &object) != nil || object.Generation == "" {
				t.Fatal("missing generation")
			}
			if h == a {
				generations[key] = object.Generation
			}
			call(h, "PUT", azure+"/"+url.PathEscape(key), body, h.token, map[string]string{"x-ms-version": "2023-11-03", "x-ms-blob-type": "BlockBlob", "Content-Type": "text/plain"}, 201)
		}
	}
	verify := func(h *host, label string) {
		t.Helper()
		for _, key := range keys {
			data, _ := call(h, "GET", gcp+"/"+bucket+"/o/"+url.PathEscape(key)+"?alt=media", "", h.token, nil, 200)
			if string(data) != label+key {
				t.Fatalf("GCP key collision: %q", key)
			}
			data, headers := call(h, "GET", azure+"/"+url.PathEscape(key), "", h.token, native, 200)
			if string(data) != label+key || !strings.HasPrefix(headers.Get("Content-Disposition"), "attachment") {
				t.Fatalf("Azure key collision or unsafe download: %q", key)
			}
		}
	}
	verify(a, "first:")
	verify(b, "second:")
	// Persisted values and generations survive an actual database close/reopen.
	oldDB, oldToken := a.db, a.token
	stop(a)
	a = start(oldDB, oldToken)
	verify(a, "first:")
	verify(b, "second:")
	key := keys[0]
	call(a, "POST", "/_simulator/providers/gcp/upload/storage/v1/b/"+bucket+"/o?uploadType=media&name="+url.QueryEscape(key)+"&ifGenerationMatch=0", "replacement", a.token, nil, 412)
	data, _ := call(a, "GET", gcp+"/"+bucket+"/o/"+url.PathEscape(key), "", a.token, nil, 200)
	var saved struct {
		Generation string `json:"generation"`
	}
	if json.Unmarshal(data, &saved) != nil || saved.Generation != generations[key] {
		t.Fatal("generation changed on rejected mutation or restart")
	}
	call(a, "DELETE", azure+"/"+url.PathEscape(key), "", a.token, map[string]string{"x-ms-version": "2023-11-03", "If-Match": "\"wrong\""}, 412)
	verify(a, "first:")
	for _, key := range keys {
		call(a, "DELETE", gcp+"/"+bucket+"/o/"+url.PathEscape(key), "", a.token, nil, 204)
		call(a, "DELETE", azure+"/"+url.PathEscape(key), "", a.token, native, 202)
	}
	call(a, "DELETE", gcp+"/"+bucket, "", a.token, nil, 204)
	call(a, "DELETE", azure+"?restype=container", "", a.token, native, 202)
	verify(b, "second:")
}

func TestCloudStorageParticipantUsesConfiguredOwnerWithoutAWSGrant(t *testing.T) {
	cfg := DefaultCloudStorageConfig()
	cfg.GCPOwner = "learner"
	cfg.AzureOwner = "learner"
	handler, closeStore, err := NewWithCloudStorage(filepath.Join(t.TempDir(), "participant.db"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	operator := strings.Repeat("c", 64)
	access, err := NewAccess(handler, operator, false)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(access)
	defer s.Close()
	request := func(method, path, body, token, selected string, azure bool, want int) []byte {
		t.Helper()
		r, err := http.NewRequest(method, s.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-Simulator-Token", token)
		r.Header.Set("Content-Type", "application/json")
		if selected != "" {
			r.Header.Set("X-Simulator-Principal", selected)
		}
		if azure {
			r.Header.Set("x-ms-version", "2023-11-03")
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
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d want%d %s", method, path, resp.StatusCode, want, data)
		}
		return data
	}
	issued := request("POST", "/_simulator/v1/access", `{"Principal":"learner"}`, operator, "", false, 200)
	var grant struct{ AccessToken string }
	if json.Unmarshal(issued, &grant) != nil || grant.AccessToken == "" {
		t.Fatal("participant token not minted")
	}
	gcp := "/_simulator/providers/gcp/storage/v1/b?project=" + cfg.GCPProject
	azure := "/_simulator/providers/azure/" + cfg.AzureAccount + "/learner-container?restype=container"
	request("POST", gcp, `{"name":"learner-bucket"}`, operator, "", false, 403)
	request("POST", gcp, `{"name":"learner-bucket"}`, grant.AccessToken, "simulator-admin", false, 403)
	request("POST", gcp, `{"name":"learner-bucket"}`, grant.AccessToken, "", false, 200)
	request("PUT", azure, "", grant.AccessToken, "", true, 201)
	request("GET", gcp, "", grant.AccessToken, "", false, 200)
	request("HEAD", azure, "", grant.AccessToken, "", true, 200)
	request("DELETE", "/_simulator/providers/gcp/storage/v1/b/learner-bucket", "", grant.AccessToken, "", false, 204)
	request("DELETE", azure, "", grant.AccessToken, "", true, 202)
}
