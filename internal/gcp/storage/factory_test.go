package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type fixtureSubjectKey struct{}

func projectFactoryConfig(environment string) FactoryConfig {
	return FactoryConfig{Environment: environment, Projects: []ProjectBinding{{Project: Project{"project-one", "1001"}, OwnerSubject: "exercise-one"}, {Project: Project{"project-two", "1002"}, OwnerSubject: "exercise-two"}}, ExtractSubject: func(ctx context.Context) (string, bool) {
		subject, ok := ctx.Value(fixtureSubjectKey{}).(string)
		return subject, ok
	}}
}

// This bridge is a test fixture, not production provider/Access integration.
// The fixed listener actor models trusted authentication; no request headers
// choose it. Prefix stripping preserves the paired decoded/escaped path.
func factoryServer(handler http.Handler, subject string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subject != "" {
			r = r.WithContext(context.WithValue(r.Context(), fixtureSubjectKey{}, subject))
		}
		const prefix = "/_simulator/providers/gcp"
		if strings.HasPrefix(r.URL.EscapedPath(), prefix+"/") {
			clone := r.Clone(r.Context())
			u := *r.URL
			clone.URL = &u
			escaped := strings.TrimPrefix(u.EscapedPath(), prefix)
			decoded, err := url.PathUnescape(escaped)
			if err != nil {
				http.Error(w, "fixture invalid path", 400)
				return
			}
			u.Path = decoded
			u.RawPath = escaped
			r = clone
		}
		handler.ServeHTTP(w, r)
	}))
}

func actualRequest(t *testing.T, server *httptest.Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		t.Fatal("invalid bounded response", err)
	}
	w := httptest.NewRecorder()
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Body.Write(data)
	return w
}
func factorySnapshot(t *testing.T, db *core.Store) string {
	t.Helper()
	rows, err := db.Database().Query("SELECT environment,project,subject FROM gcp_storage_project_owners ORDER BY environment,project")
	if err != nil {
		t.Fatal(err)
	}
	var owners [][3]string
	for rows.Next() {
		var owner [3]string
		if err = rows.Scan(&owner[0], &owner[1], &owner[2]); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		owners = append(owners, owner)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	encoded, err := json.Marshal(owners)
	if err != nil {
		t.Fatal(err)
	}
	return storageSnapshot(t, db) + string(encoded)
}

func TestStorageProjectFactoryActualHTTPContextPrefixAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factory.sqlite")
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	config := projectFactoryConfig("host-fixed")
	handler, err := NewProjectHandler(db.Database(), config)
	if err != nil {
		t.Fatal(err)
	}
	owner := factoryServer(handler, "exercise-one")
	peer := factoryServer(handler, "exercise-two")
	reader := factoryServer(handler, "reader")
	missing := factoryServer(handler, "")
	defer func() { owner.Close(); peer.Close(); reader.Close(); missing.Close() }()
	const prefix = "/_simulator/providers/gcp"
	mustStatus(t, actualRequest(t, owner, "POST", prefix+"/storage/v1/b?project=project-one", `{"name":"factory-bucket"}`, nil), 200)
	mustStatus(t, actualRequest(t, peer, "POST", "/storage/v1/b?project=project-two", `{"name":"peer-factory-bucket"}`, nil), 200)
	mustStatus(t, actualRequest(t, owner, "GET", prefix+"/storage/v1/b/factory-bucket", "", nil), 200)
	listedBuckets := mustStatus(t, actualRequest(t, owner, "GET", prefix+"/storage/v1/b?project=project-one", "", nil), 200)
	if len(listedBuckets["items"].([]any)) != 1 || listedBuckets["items"].([]any)[0].(map[string]any)["name"] != "factory-bucket" {
		t.Fatal("factory project listing mixed ownership", listedBuckets)
	}
	keys := []string{"a/b", "a//b", "a/./b", "a/../b", "b", "./b", "/b", "%2F", "/", "%252F", "a%2Fb", "日本語/☁", "é"}
	want := map[string]bool{}
	for _, key := range keys {
		want[key] = true
		mustStatus(t, actualRequest(t, owner, "POST", prefix+"/upload/storage/v1/b/factory-bucket/o?uploadType=media&name="+url.QueryEscape(key), "body:"+key, map[string]string{"Content-Type": "text/plain"}), 200)
	}
	mustStatus(t, actualRequest(t, owner, "POST", prefix+"/upload/storage/v1/b/factory-bucket/o?uploadType=media&name=a%2Fb", "body:a/b", map[string]string{"Content-Type": "text/plain"}), 200)
	before := factorySnapshot(t, db)
	for _, actorServer := range []*httptest.Server{reader, peer, missing} {
		for _, attempt := range []struct{ method, path, body string }{{"GET", prefix + objectPath("factory-bucket", "a/b"), ""}, {"DELETE", prefix + objectPath("factory-bucket", "a/b"), ""}, {"POST", prefix + "/upload/storage/v1/b/factory-bucket/o?uploadType=media&name=a%2Fb", "forbidden"}, {"POST", prefix + "/storage/v1/b?project=project-one", `{"name":"forbidden-bucket"}`}} {
			mustStatus(t, actualRequest(t, actorServer, attempt.method, attempt.path, attempt.body, map[string]string{"X-Simulator-Identity": "exercise-one", "X-Project-ID": "project-one", "X-Environment": "host-fixed", "X-Credential-Role": "operator"}), 403)
			if after := factorySnapshot(t, db); after != before {
				t.Fatal("refused context/header request changed state")
			}
		}
	}
	verify := func() {
		t.Helper()
		listed := mustStatus(t, actualRequest(t, owner, "GET", prefix+"/storage/v1/b/factory-bucket/o", "", nil), 200)
		got := map[string]bool{}
		for _, item := range listed["items"].([]any) {
			got[item.(map[string]any)["name"].(string)] = true
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("actual HTTP key set %v want %v", got, want)
		}
		for _, key := range keys {
			w := actualRequest(t, owner, "GET", prefix+objectPath("factory-bucket", key)+"?alt=media", "", nil)
			if w.Code != 200 || w.Body.String() != "body:"+key {
				t.Fatal("actual HTTP exact key mismatch", key, w.Code, w.Body.String())
			}
		}
	}
	verify()
	owner.Close()
	peer.Close()
	reader.Close()
	missing.Close()
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	handler, err = NewProjectHandler(db.Database(), config)
	if err != nil {
		t.Fatal(err)
	}
	owner = factoryServer(handler, "exercise-one")
	peer = factoryServer(handler, "exercise-two")
	reader = factoryServer(handler, "reader")
	missing = factoryServer(handler, "")
	verify()
	for _, key := range keys {
		mustStatus(t, actualRequest(t, owner, "DELETE", prefix+objectPath("factory-bucket", key), "", nil), 204)
	}
	mustStatus(t, actualRequest(t, owner, "DELETE", prefix+"/storage/v1/b/factory-bucket", "", nil), 204)
	mustStatus(t, actualRequest(t, peer, "DELETE", "/storage/v1/b/peer-factory-bucket", "", nil), 204)
}

func TestStorageProjectFactoryImmutableAuthorityAndAtomicConstruction(t *testing.T) {
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	config := projectFactoryConfig("owners")
	handler, err := NewProjectHandler(db.Database(), config)
	if err != nil {
		t.Fatal(err)
	}
	before := factorySnapshot(t, db)
	bad := projectFactoryConfig("owners")
	bad.Projects[0].OwnerSubject = "new-owner"
	bad.Projects = append(bad.Projects, ProjectBinding{Project: Project{"project-three", "1003"}, OwnerSubject: "third-owner"})
	if _, err = NewProjectHandler(db.Database(), bad); err == nil {
		t.Fatal("ownerchange accepted")
	}
	if after := factorySnapshot(t, db); after != before {
		t.Fatal("constructor failure did not roll back new project/bindings")
	}
	bad = projectFactoryConfig("owners")
	bad.Projects = bad.Projects[:1]
	if _, err = NewProjectHandler(db.Database(), bad); err == nil {
		t.Fatal("persisted owner binding removed")
	}
	bad = projectFactoryConfig("owners")
	bad.ExtractSubject = nil
	if _, err = NewProjectHandler(db.Database(), bad); err == nil {
		t.Fatal("missing extractor accepted")
	}
	// Mutating the caller's config slice cannot change the factory's policy.
	config.Projects[0].OwnerSubject = "reader"
	owner := factoryServer(handler, "exercise-one")
	defer owner.Close()
	mustStatus(t, actualRequest(t, owner, "POST", "/storage/v1/b?project=project-one", `{"name":"immutable-owner"}`, nil), 200)
	reader := factoryServer(handler, "reader")
	defer reader.Close()
	mustStatus(t, actualRequest(t, reader, "GET", "/storage/v1/b/immutable-owner", "", nil), 403)
	// The finite permission allowset must not grow implicitly with a future API.
	h := handler.(*projectHandler)
	ctx := WithActor(context.Background(), Actor{"exercise-one", "owners"})
	_, err = h.storage.transaction(ctx, func(tx *sql.Tx, a Actor) (any, error) {
		return nil, h.storage.permit(ctx, tx, a, "storage.buckets.setIamPolicy", Resource{Project: "project-one", Bucket: "immutable-owner", Exists: true})
	})
	if err == nil {
		t.Fatal("unsupported permission silently granted")
	}
}

func TestStorageProjectFactoryRefusesUnboundResourceAdoption(t *testing.T) {
	db, leaf := fixture(t, ":memory:", "unbound", projectPolicy, Limits{})
	defer db.Close()
	create(t, leaf, "project-one", "unbound-bucket")
	emptyBefore := storageSnapshot(t, db)
	if _, err := NewProjectHandler(db.Database(), projectFactoryConfig("unbound")); err == nil {
		t.Fatal("preexisting empty unbound bucket adopted")
	}
	if after := storageSnapshot(t, db); after != emptyBefore {
		t.Fatal("failed empty bucket adoption changed state")
	}
	upload(t, leaf, "project-one", "unbound-bucket", "key", "original", "0")
	before := storageSnapshot(t, db)
	if _, err := NewProjectHandler(db.Database(), projectFactoryConfig("unbound")); err == nil {
		t.Fatal("preexisting unbound buckets adopted")
	}
	if after := storageSnapshot(t, db); after != before {
		t.Fatal("failed adoption changed existing state")
	}
	var tables int
	if err := db.Database().QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='gcp_storage_project_owners'").Scan(&tables); err != nil || tables != 0 {
		t.Fatal("failed constructor left owner schema behind", tables, err)
	}
}
