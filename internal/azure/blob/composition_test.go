package blob

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

type authenticatedFixtureKey struct{}

func TestAzureBlobOwnedCompositionProviderHTTPPersistenceAndRefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "azure.sqlite")
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	extractor := func(ctx context.Context) (AuthenticatedActor, bool) {
		a, ok := ctx.Value(authenticatedFixtureKey{}).(AuthenticatedActor)
		return a, ok
	}
	if _, bad := NewOwnedHandler(db.Database(), Config{Account: "accountone", Owner: "owner", Environment: "bad\x7flabel"}, extractor); bad == nil {
		t.Fatal("invalid fixed environment accepted")
	}
	if _, err = NewOwnedHandler(db.Database(), config("accountone", "owner"), nil); err == nil {
		t.Fatal("missing extractor accepted")
	}
	start := func(cfg Config, identity string) *httptest.Server {
		handler, e := NewOwnedHandler(db.Database(), cfg, extractor)
		if e != nil {
			t.Fatal(e)
		}
		registry := provider.New("")
		if e = registry.Register("azure", handler); e != nil {
			t.Fatal(e)
		}
		// Only this trusted fixture middleware establishes authentication context.
		// It deliberately never reads request identity/role headers. Real Access
		// authentication is a later root integration gate, not proved by this fixture.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := WithActor(r.Context(), Actor{Subject: "owner", Environment: cfg.Environment})
			if identity != "" {
				ctx = context.WithValue(ctx, authenticatedFixtureKey{}, AuthenticatedActor{ExerciseIdentity: identity})
			}
			registry.ServeHTTP(w, r.WithContext(ctx))
		}))
		t.Cleanup(server.Close)
		return server
	}
	owner := start(config("accountone", "owner"), "owner")
	reader := start(config("accountone", "owner"), "reader")
	missing := start(config("accountone", "owner"), "")
	peerCfg := config("accountone", "peer-owner")
	peerCfg.Environment = "peer-environment"
	peer := start(peerCfg, "peer-owner")
	call := func(server *httptest.Server, method, path, body string, headers map[string]string, want int) (http.Header, string) {
		t.Helper()
		r, e := http.NewRequest(method, server.URL+provider.Prefix+"azure"+path, bytes.NewBufferString(body))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("x-ms-version", APIVersion)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		response, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		b, e := io.ReadAll(response.Body)
		if e != nil {
			t.Fatal(e)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s got%d want%d: %s", method, path, response.StatusCode, want, b)
		}
		return response.Header, string(b)
	}
	for _, server := range []*httptest.Server{owner, peer} {
		call(server, "PUT", "/accountone/box?restype=container", "", nil, 201)
	}
	keys := []string{"same", "a/b", "a//b", "a/./b", "a/../b", "snow-雪", "a+b", "a b"}
	tags := map[string]string{}
	for _, key := range keys {
		h, _ := call(owner, "PUT", "/accountone/box/"+url.PathEscape(key), "owner:"+key, map[string]string{"x-ms-blob-type": "BlockBlob", "Content-Type": "text/html"}, 201)
		tags[key] = h.Get("ETag")
		call(peer, "PUT", "/accountone/box/"+url.PathEscape(key), "peer:"+key, map[string]string{"x-ms-blob-type": "BlockBlob"}, 201)
	}
	call(owner, "GET", "/accountone?comp=list", "", nil, 200)
	call(owner, "HEAD", "/accountone/box?restype=container", "", nil, 200)
	call(owner, "HEAD", "/accountone/box/same", "", nil, 200)
	call(owner, "GET", "/accountone/box?restype=container&comp=list", "", nil, 200)
	service, err := New(db.Database(), config("accountone", "owner"), permitted)
	if err != nil {
		t.Fatal(err)
	}
	before := persistedRows(t, service)
	for _, server := range []*httptest.Server{reader, missing} {
		forged := map[string]string{"X-Exercise-Identity": "owner", "X-Credential-Role": "operator", "If-None-Match": tags["same"]}
		h, _ := call(server, "GET", "/accountone/box/same", "", forged, 403)
		if h.Get("ETag") != "" {
			t.Fatal("denied read leaked tag")
		}
		call(server, "PUT", "/accountone/box/same", "changed", map[string]string{"x-ms-blob-type": "BlockBlob", "X-Exercise-Identity": "owner"}, 403)
		call(server, "DELETE", "/accountone/box/same", "", nil, 403)
		call(server, "GET", "/accountone/box?restype=container&comp=list", "", nil, 403)
	}
	if !reflect.DeepEqual(before, persistedRows(t, service)) {
		t.Fatal("nonowner requests changed persisted objects")
	}
	owner.Close()
	reopened, e := core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	db = reopened
	owner = start(config("accountone", "owner"), "owner") // a new SQLite connection and leaf restore saved identity
	for _, key := range keys {
		h, b := call(owner, "GET", "/accountone/box/"+url.PathEscape(key), "", nil, 200)
		if b != "owner:"+key || h.Get("ETag") != tags[key] || h.Get("Content-Disposition") != "attachment" {
			t.Fatal("restart/key/content presentation mismatch", key, h, b)
		}
		_, b = call(peer, "GET", "/accountone/box/"+url.PathEscape(key), "", nil, 200)
		if b != "peer:"+key {
			t.Fatal("environment crossover", key, b)
		}
	}
	call(owner, "DELETE", "/accountone/box/same", "", nil, 202)
	call(owner, "GET", "/accountone/box/same", "", nil, 404)
	_, peerBody := call(peer, "GET", "/accountone/box/same", "", nil, 200)
	if peerBody != "peer:same" {
		t.Fatal("owner delete reached other environment")
	}
	for _, server := range []*httptest.Server{owner, peer} {
		call(server, "DELETE", "/accountone/box?restype=container", "", nil, 202)
	}
	if len(persistedRows(t, service)) != 0 {
		t.Fatal("cleanup left objects")
	}
}
