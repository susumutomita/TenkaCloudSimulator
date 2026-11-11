package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// App Engine deploys from the native Cloud Storage provider of the same
// process, runs the version through the authenticated provider route and
// keeps its state across a database reopen.
func TestAppEngineDeploysFromNativeCloudStorageAndServesThroughTheProviderRoute(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the embedded interpreter")
	}
	cfg := DefaultCloudStorageConfig()
	path := filepath.Join(t.TempDir(), "appengine.db")
	token := strings.Repeat("c", 64)
	start := func() (*httptest.Server, func() error) {
		handler, closeStore, err := NewWithCloudStorage(path, cfg)
		if err != nil {
			t.Fatal(err)
		}
		access, err := NewAccess(handler, token, false)
		if err != nil {
			t.Fatal(err)
		}
		return httptest.NewServer(access), closeStore
	}
	server, closeStore := start()
	call := func(method, path, contentType string, body []byte, headers map[string]string, want int) []byte {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
		req.Header.Set("X-Simulator-Token", token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		if response.StatusCode != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, response.StatusCode, want, data)
		}
		return data
	}
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	f, _ := w.Create("main.py")
	_, _ = f.Write([]byte("def app(environ, start_response):\n    start_response('200 OK', [('Content-Type', 'text/plain')])\n    return [b'served from Cloud Storage']\n"))
	_ = w.Close()
	gcp := "/_simulator/providers/gcp"
	call("POST", gcp+"/storage/v1/b?project="+cfg.GCPProject, "application/json", []byte(`{"name":"appengine-src"}`), nil, 200)
	call("POST", gcp+"/upload/storage/v1/b/appengine-src/o?uploadType=media&name=app.zip", "application/zip", archive.Bytes(), nil, 200)
	apps := gcp + "/appengine/v1/apps"
	call("POST", apps, "application/json", []byte(`{"id":"`+cfg.GCPProject+`","locationId":"asia-northeast1"}`), nil, 200)
	version := []byte(`{"id":"v1","runtime":"python312","deployment":{"zip":{"sourceUrl":"https://storage.googleapis.com/appengine-src/app.zip"}}}`)
	// Another exercise identity is refused by the owner-only admin API.
	call("POST", apps+"/"+cfg.GCPProject+"/services/default/versions", "application/json", version, map[string]string{"X-Simulator-Principal": "reader"}, 403)
	call("POST", apps+"/"+cfg.GCPProject+"/services/default/versions", "application/json", []byte(`{"id":"v0","runtime":"python312","deployment":{"zip":{"sourceUrl":"https://storage.googleapis.com/appengine-src/missing.zip"}}}`), nil, 400)
	call("POST", apps+"/"+cfg.GCPProject+"/services/default/versions", "application/json", version, nil, 200)
	host := gcp + "/appspot/" + cfg.GCPProject + ".an.r.appspot.com/"
	if got := string(call("GET", host, "", nil, nil, 200)); got != "served from Cloud Storage" {
		t.Fatalf("served %q", got)
	}
	// The data plane is reachable by any authenticated session of this team.
	call("GET", host, "", nil, map[string]string{"X-Simulator-Principal": "reader"}, 200)
	// Deleting the source object does not change the deployed copy.
	call("DELETE", gcp+"/storage/v1/b/appengine-src/o/app.zip", "", nil, nil, 204)
	server.Close()
	if err := closeStore(); err != nil {
		t.Fatal(err)
	}
	server, closeStore = start()
	defer func() { server.Close(); _ = closeStore() }()
	if got := string(call("GET", host, "", nil, nil, 200)); got != "served from Cloud Storage" {
		t.Fatalf("served after restart %q", got)
	}
	var capabilities struct {
		AppEngine struct {
			Endpoint, DataPlane, Reference string
			Methods                        []string
			OfficialMethods                int
		} `json:"gcpAppEngine"`
	}
	if err := json.Unmarshal(call("GET", "/_simulator/v1/capabilities", "", nil, nil, 200), &capabilities); err != nil {
		t.Fatal(err)
	}
	c := capabilities.AppEngine
	if c.Endpoint != "/_simulator/providers/gcp/appengine/" || c.DataPlane != "/_simulator/providers/gcp/appspot/{hostname}/" || len(c.Methods) != 22 || c.OfficialMethods != 59 || c.Reference != "/_simulator/docs/gcp-appengine.html" {
		t.Fatalf("capabilities: %+v", c)
	}
	// Anonymous callers get the Google error envelope from the access layer.
	response, err := http.Get(server.URL + apps + "/" + cfg.GCPProject)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("anonymous: %d", response.StatusCode)
	}
}
