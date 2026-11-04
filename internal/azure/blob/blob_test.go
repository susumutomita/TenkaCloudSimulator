package blob

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func fixture(t *testing.T, path string, cfg Config, auth Authorizer) (*core.Store, *Service) {
	t.Helper()
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := New(db.Database(), cfg, auth)
	if err != nil {
		t.Fatal(err)
	}
	return db, s
}
func config(account, owner string) Config {
	return Config{Account: account, Owner: owner, Environment: "local-test-environment"}
}
func permitted(ctx context.Context, tx *sql.Tx, a Authorization) (bool, error) {
	return a.Actor.Subject == "learner" && a.Resource.Owner == "owner", nil
}
func request(t *testing.T, s *Service, method, path string, body []byte, headers map[string]string, actor *Actor, code int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("x-ms-version", APIVersion)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	if actor != nil {
		r = r.WithContext(WithActor(r.Context(), *actor))
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != code {
		t.Fatalf("%s %s: got%d want%d %s", method, path, w.Code, code, w.Body.String())
	}
	return w
}
func learner() *Actor { return &Actor{Subject: "learner", Environment: "local-test-environment"} }
func create(t *testing.T, s *Service, name string) {
	request(t, s, "PUT", "/"+s.config.Account+"/"+name+"?restype=container", nil, nil, learner(), 201)
}
func put(t *testing.T, s *Service, container, key, value string, extra map[string]string, code int) *httptest.ResponseRecorder {
	h := map[string]string{"x-ms-blob-type": "BlockBlob", "Content-Type": "text/plain"}
	for k, v := range extra {
		h[k] = v
	}
	return request(t, s, "PUT", "/"+s.config.Account+"/"+container+"/"+url.PathEscape(key), []byte(value), h, learner(), code)
}
func TestAzureBlobConstructorActorAndOwnerFailClosed(t *testing.T) {
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = New(db.Database(), config("accountone", "owner"), nil); err == nil {
		t.Fatal("nil authorization accepted")
	}
	var n int
	if err = db.Database().QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'azure_blob_%'").Scan(&n); err != nil || n != 0 {
		t.Fatal("invalid constructor changed schema", n, err)
	}
	calls := 0
	auth := func(ctx context.Context, tx *sql.Tx, a Authorization) (bool, error) { calls++; return true, nil }
	s, err := New(db.Database(), config("accountone", "owner"), auth)
	if err != nil {
		t.Fatal(err)
	}
	path := "/accountone/box?restype=container"
	request(t, s, "PUT", path, nil, map[string]string{"X-Simulator-Principal": "simulator-admin", "x-ms-meta-owner": "forged"}, nil, 403)
	request(t, s, "PUT", path, nil, nil, &Actor{Subject: "learner", Environment: "foreign"}, 403)
	if calls != 0 {
		t.Fatal("missing/foreign actor reached grant callback")
	}
	create(t, s, "box")
	if calls != 1 {
		t.Fatal(calls)
	}
	if _, err = New(db.Database(), config("accountone", "different-owner"), auth); err == nil {
		t.Fatal("persisted owner adopted")
	}
	if _, err = New(nil, config("accountone", "owner"), auth); err == nil {
		t.Fatal("nil database accepted")
	}
	request(t, s, "GET", "/accountone?comp=list", nil, map[string]string{"Authorization": "Bearer real-looking-not-used"}, learner(), 403)
}

// Independent literals from Microsoft REST operation permission sections and
// the Storage Blob Data Contributor role's Actions/DataActions collections.
// Deliberately do not call permissionFor or use its prefix/kind constants here.
var expectedPermissions = map[Operation]struct{ Name, Kind string }{
	"CreateContainer":        {"Microsoft.Storage/storageAccounts/blobServices/containers/write", "Action"},
	"ListContainers":         {"Microsoft.Storage/storageAccounts/blobServices/containers/read", "Action"},
	"GetContainerProperties": {"Microsoft.Storage/storageAccounts/blobServices/containers/read", "Action"},
	"DeleteContainer":        {"Microsoft.Storage/storageAccounts/blobServices/containers/delete", "Action"},
	"PutBlob":                {"Microsoft.Storage/storageAccounts/blobServices/containers/blobs/write", "DataAction"},
	"GetBlob":                {"Microsoft.Storage/storageAccounts/blobServices/containers/blobs/read", "DataAction"},
	"GetBlobProperties":      {"Microsoft.Storage/storageAccounts/blobServices/containers/blobs/read", "DataAction"},
	"ListBlobs":              {"Microsoft.Storage/storageAccounts/blobServices/containers/blobs/read", "DataAction"},
	"DeleteBlob":             {"Microsoft.Storage/storageAccounts/blobServices/containers/blobs/delete", "DataAction"},
}

func TestAzureBlobAllOperationsAuthorizeActualResourceAndPersist(t *testing.T) {
	seen := map[Operation]int{}
	auth := func(ctx context.Context, tx *sql.Tx, a Authorization) (bool, error) {
		var owner string
		err := tx.QueryRowContext(ctx, "SELECT owner FROM azure_blob_accounts WHERE environment=? AND account=?", a.Resource.Environment, a.Resource.Account).Scan(&owner)
		if err != nil {
			return false, err
		}
		expected, known := expectedPermissions[a.Operation]
		if !known || owner != a.Resource.Owner || a.Actor.Subject != "learner" || a.Resource.Provider != "azure" || a.Permission.Name != expected.Name || string(a.Permission.Kind) != expected.Kind {
			return false, fmt.Errorf("wrong trusted scope")
		}
		seen[a.Operation]++
		return true, nil
	}
	path := filepath.Join(t.TempDir(), "state.sqlite")
	_, s := fixture(t, path, config("accountone", "owner"), auth)
	create(t, s, "box")
	request(t, s, "HEAD", "/accountone/box?restype=container", nil, nil, learner(), 200)
	first := put(t, s, "box", "hello", "<script>unsafe()</script>", map[string]string{"Content-Type": "text/html", "x-ms-meta-owner": "untrusted-metadata"}, 201)
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("missing ETag")
	}
	get := request(t, s, "GET", "/accountone/box/hello", nil, nil, learner(), 200)
	if get.Body.String() != "<script>unsafe()</script>" || get.Header().Get("Content-Disposition") != "attachment" || !strings.Contains(get.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("unsafe content presentation", get.Header())
	}
	head := request(t, s, "HEAD", "/accountone/box/hello", nil, nil, learner(), 200)
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") != "25" {
		t.Fatal("head body/size", head.Header(), head.Body.String())
	}
	request(t, s, "GET", "/accountone?comp=list", nil, nil, learner(), 200)
	request(t, s, "GET", "/accountone/box?restype=container&comp=list&include=metadata", nil, nil, learner(), 200)
	db2, s2 := fixture(t, path, config("accountone", "owner"), auth)
	_ = db2
	if w := request(t, s2, "GET", "/accountone/box/hello", nil, nil, learner(), 200); w.Header().Get("ETag") != tag {
		t.Fatal("restart changed identity")
	}
	request(t, s, "DELETE", "/accountone/box/hello", nil, nil, learner(), 202)
	put(t, s, "box", "cascade", "child", nil, 201)
	request(t, s, "DELETE", "/accountone/box?restype=container", nil, nil, learner(), 202)
	request(t, s, "GET", "/accountone/box/cascade", nil, nil, learner(), 404)
	for op := range expectedPermissions {
		if seen[op] == 0 {
			t.Fatal("operation missed transaction authorization", op)
		}
	}
}
func TestAzureBlobLiteralNeighborsAndConditionalWrites(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	keys := []string{"a/b", "a//b", "a/./b", "a/../b", "a/%2Fb", "a%2Fb", "a+b", "a b", "雪/%", "%", "%2F", "/lead", "tail/"}
	tags := map[string]string{}
	for i, k := range keys {
		tags[k] = put(t, s, "box", k, fmt.Sprintf("value-%d", i), nil, 201).Header().Get("ETag")
	}
	for i, k := range keys {
		w := request(t, s, "GET", "/accountone/box/"+url.PathEscape(k), nil, nil, learner(), 200)
		if w.Body.String() != fmt.Sprintf("value-%d", i) {
			t.Fatal("neighbor collision", k, w.Body.String())
		}
	}
	var listing struct {
		Blobs []struct {
			Name string `xml:"Name"`
		} `xml:"Blobs>Blob"`
	}
	w := request(t, s, "GET", "/accountone/box?restype=container&comp=list", nil, nil, learner(), 200)
	if err := xml.Unmarshal(w.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, b := range listing.Blobs {
		got[b.Name] = true
	}
	if len(got) != len(keys) {
		t.Fatal(got)
	}
	for _, k := range keys {
		if !got[k] {
			t.Fatal("missing literal key", k)
		}
	}
	put(t, s, "box", "a/b", "stale", map[string]string{"If-Match": `"wrong"`}, 412)
	put(t, s, "box", "a/b", "existing", map[string]string{"If-None-Match": "*"}, 412)
	old := tags["a/b"]
	next := put(t, s, "box", "a/b", "updated", map[string]string{"If-Match": old}, 201).Header().Get("ETag")
	if next == old {
		t.Fatal("ETag ABA")
	}
	request(t, s, "GET", "/accountone/box/a/b", nil, map[string]string{"If-None-Match": next}, learner(), 304)
	request(t, s, "DELETE", "/accountone/box/a/b", nil, map[string]string{"If-Match": old}, learner(), 412)
	request(t, s, "DELETE", "/accountone/box/a/b", nil, map[string]string{"If-Match": next}, learner(), 202)
	request(t, s, "GET", "/accountone/box?restype=container&comp=list", nil, nil, learner(), 200)
	request(t, s, "PUT", "/accountone/box%2Fforeign/evil", []byte("x"), map[string]string{"x-ms-blob-type": "BlockBlob"}, learner(), 400)
}
func TestAzureBlobSameNameAccountsAndDeniedOperationsDoNotMix(t *testing.T) {
	db, a := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	b, err := New(db.Database(), config("accounttwo", "other"), func(ctx context.Context, tx *sql.Tx, r Authorization) (bool, error) {
		return r.Actor.Subject == "learner" && r.Resource.Owner == "other", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	create(t, a, "box")
	create(t, b, "box")
	put(t, a, "box", "same", "first", nil, 201)
	put(t, b, "box", "same", "second", nil, 201)
	if w := request(t, b, "GET", "/accounttwo/box/same", nil, nil, learner(), 200); w.Body.String() != "second" {
		t.Fatal(w.Body.String())
	}
	request(t, a, "GET", "/accounttwo/box/same", nil, nil, learner(), 404)
	deny, err := New(db.Database(), config("accountone", "owner"), func(context.Context, *sql.Tx, Authorization) (bool, error) { return false, nil })
	if err != nil {
		t.Fatal(err)
	}
	request(t, deny, "GET", "/accountone/box/same", nil, map[string]string{"X-Simulator-Principal": "simulator-admin"}, learner(), 403)
	put(t, deny, "box", "same", "denied", nil, 403)
	request(t, deny, "DELETE", "/accountone/box?restype=container", nil, nil, learner(), 403)
	if w := request(t, a, "GET", "/accountone/box/same", nil, nil, learner(), 200); w.Body.String() != "first" {
		t.Fatal("denial mutated data")
	}
	broken, err := New(db.Database(), config("accountone", "owner"), func(context.Context, *sql.Tx, Authorization) (bool, error) {
		return false, fmt.Errorf("private policy storage failure")
	})
	if err != nil {
		t.Fatal(err)
	}
	w := request(t, broken, "GET", "/accountone/box/same", nil, nil, learner(), 500)
	if strings.Contains(w.Body.String(), "private") {
		t.Fatal("callback error leaked")
	}
}
func TestAzureBlobPaginationBindingAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	_, s := fixture(t, path, config("accountone", "owner"), permitted)
	create(t, s, "box")
	create(t, s, "other")
	for _, key := range []string{"p/a", "p/b", "p/c"} {
		put(t, s, "box", key, key, nil, 201)
	}
	var result struct {
		Names []string `xml:"Blobs>Blob>Name"`
		Next  string   `xml:"NextMarker"`
	}
	w := request(t, s, "GET", "/accountone/box?restype=container&comp=list&prefix=p%2F&maxresults=1", nil, nil, learner(), 200)
	if err := xml.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Names, []string{"p/a"}) || result.Next == "" {
		t.Fatal(result)
	}
	token := url.QueryEscape(result.Next)
	request(t, s, "GET", "/accountone/other?restype=container&comp=list&prefix=p%2F&marker="+token, nil, nil, learner(), 400)
	request(t, s, "GET", "/accountone/box?restype=container&comp=list&prefix=wrong&marker="+token, nil, nil, learner(), 400)
	_, s2 := fixture(t, path, config("accountone", "owner"), permitted)
	w = request(t, s2, "GET", "/accountone/box?restype=container&comp=list&prefix=p%2F&maxresults=1&marker="+token, nil, nil, learner(), 200)
	result.Names = nil
	if err := xml.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Names, []string{"p/b"}) {
		t.Fatal(result)
	}
}
func TestAzureBlobConcurrentConditionalCreateOnlyOneWins(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	start := make(chan struct{})
	out := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r := httptest.NewRequest("PUT", "/accountone/box/race", strings.NewReader("winner"))
			r.Header.Set("x-ms-version", APIVersion)
			r.Header.Set("x-ms-blob-type", "BlockBlob")
			r.Header.Set("If-None-Match", "*")
			r = r.WithContext(WithActor(r.Context(), *learner()))
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			out <- w.Code
		}()
	}
	close(start)
	wg.Wait()
	close(out)
	codes := map[int]int{}
	for code := range out {
		codes[code]++
	}
	if codes[201] != 1 || codes[412] != 1 {
		t.Fatal(codes)
	}
}
