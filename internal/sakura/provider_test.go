package sakura_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	common "github.com/susumutomita/TenkaCloudSimulator/internal/provider"
	"github.com/susumutomita/TenkaCloudSimulator/internal/sakura"
	"github.com/susumutomita/TenkaCloudSimulator/internal/sakura/iaas"
	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
)

const nativePath = "/cloud/zone/is1a/api/cloud/1.1/disk"

var config = sakura.Config{Environment: "team-a", Project: "practice", Zone: "is1a", Owner: "learner"}

func database(t *testing.T) (*core.Store, *sql.DB) {
	t.Helper()
	base, e := core.New(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = base.Close() })
	return base, base.Database()
}
func construct(t *testing.T, db *sql.DB, cfg sakura.Config) http.Handler {
	t.Helper()
	h, e := sakura.NewProvider(db, cfg)
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func actor() common.Actor {
	return common.Actor{CredentialIdentity: "learner", ExerciseIdentity: "learner", CredentialRole: common.CredentialParticipant}
}
func request(method, path, body string, a *common.Actor) *http.Request {
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	r.SetBasicAuth("TCSIMSAKURA", "dummy-local-only")
	r.Header.Set("Content-Type", "application/json")
	if a != nil {
		r = r.WithContext(common.WithActor(r.Context(), *a))
	}
	return r
}
func call(t *testing.T, h http.Handler, r *http.Request, status int) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: status%d wanted%d (body redacted)", r.Method, r.URL.Path, w.Code, status)
	}
	var v map[string]any
	if strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
	}
	if status >= 400 {
		for _, key := range []string{"Disk", "Disks", "AccessToken"} {
			if _, ok := v[key]; ok {
				t.Fatal("refusal disclosed data")
			}
		}
	}
	return v
}
func frame(t *testing.T, db *sql.DB) string {
	t.Helper()
	out := map[string]any{}
	for _, table := range []string{"sakura_disk_scope", "sakura_disks"} {
		rows, e := db.Query("SELECT * FROM " + table + " ORDER BY 1")
		if e != nil {
			t.Fatal(e)
		}
		columns, e := rows.Columns()
		if e != nil {
			rows.Close()
			t.Fatal(e)
		}
		data := [][]any{}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if e = rows.Scan(pointers...); e != nil {
				rows.Close()
				t.Fatal(e)
			}
			data = append(data, values)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			t.Fatal(e)
		}
		out[table] = data
	}
	encoded, e := json.Marshal(out)
	if e != nil {
		t.Fatal(e)
	}
	return string(encoded)
}
func metadataCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if e := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('sakura_disk_scope','sakura_disks')").Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func TestSakuraProviderPreflightHasNoImplicitScopeOrDatabaseEffects(t *testing.T) {
	_, db := database(t)
	for _, bad := range []sakura.Config{{}, {Environment: "team-a", Project: "practice", Zone: "is1a"}, {Environment: " team-a", Project: "practice", Zone: "is1a", Owner: "learner"}, {Environment: "team-a", Project: "bad\x00", Zone: "is1a", Owner: "learner"}, {Environment: "team-a", Project: "practice", Zone: "is1a/b", Owner: "learner"}, {Environment: "team-a", Project: "practice", Zone: "Is1a", Owner: "learner"}, {Environment: "team-a", Project: "practice", Zone: "-is1a", Owner: "learner"}, {Environment: "team-a", Project: "practice", Zone: "is1a", Owner: "other identity"}, {Environment: "team-a", Project: "practice", Zone: "is1a", Owner: strings.Repeat("x", 65)}} {
		if e := sakura.ValidateConfig(bad); !errors.Is(e, iaas.ErrInvalid) {
			t.Fatal("invalid preflight accepted", e)
		}
		if h, e := sakura.NewProvider(db, bad); h != nil || !errors.Is(e, iaas.ErrInvalid) {
			t.Fatal("invalid factory accepted", e)
		}
		if metadataCount(t, db) != 0 {
			t.Fatal("invalid preflight created a namespace")
		}
		if e := db.Ping(); e != nil {
			t.Fatal("borrowed DB closed", e)
		}
	}
	if _, e := sakura.NewProvider(nil, config); !errors.Is(e, iaas.ErrInvalid) {
		t.Fatal(e)
	}
	if e := sakura.ValidateConfig(config); e != nil {
		t.Fatal(e)
	}
}
func TestSakuraProviderUsesActualTypedActorNotHeadersOrOperatorRole(t *testing.T) {
	_, db := database(t)
	cfg := config
	h := construct(t, db, cfg)
	cfg.Owner = "changed-after-construction"
	cfg.Environment = "changed" // factory must keep the original value copy
	good := actor()
	call(t, h, request("POST", nativePath, `{"Disk":{"Name":"saved","Plan":{"ID":4},"SizeMB":20480}}`, &good), 201)
	before := frame(t, db)
	for _, a := range []*common.Actor{nil, {}, {CredentialIdentity: "host", ExerciseIdentity: "simulator-admin", CredentialRole: common.CredentialOperator}, {CredentialIdentity: "host", ExerciseIdentity: "other", CredentialRole: common.CredentialOperator}, {CredentialIdentity: "learner", ExerciseIdentity: "other", CredentialRole: common.CredentialOperator}, {CredentialIdentity: "other", ExerciseIdentity: "learner", CredentialRole: common.CredentialParticipant}, {CredentialIdentity: "learner", ExerciseIdentity: "learner", CredentialRole: "unknown"}} {
		r := request("DELETE", nativePath+"/1", "", a)
		r.Header.Set("X-Simulator-Principal", "learner")
		r.Header.Set("X-Simulator-Access-Role", "operator")
		r.Header.Set("X-Sakura-Project", "practice")
		r.Header.Set("X-Sakura-Zone", "is1a")
		call(t, h, r, 403)
		if frame(t, db) != before {
			t.Fatal("denied caller changed data")
		}
	}
	// Raw labels do not retarget a trusted actor. There is no HTTP realm selector.
	r := request("GET", nativePath+"/1", "", &good)
	r.Header.Set("X-Sakura-Project", "other")
	r.Header.Set("X-Sakura-Zone", "other")
	r.Header.Set("X-Simulator-Environment", "other")
	v := call(t, h, r, 200)
	disk := v["Disk"].(map[string]any)
	if disk["Name"] != "saved" {
		t.Fatal("request retargeted data")
	}
	if _, ok := disk["Availability"]; ok {
		t.Fatal("invented storage readiness")
	}
	operator := common.Actor{CredentialIdentity: "host", ExerciseIdentity: "learner", CredentialRole: common.CredentialOperator}
	call(t, h, request("GET", nativePath, "", &operator), 200)
	if frame(t, db) != before {
		t.Fatal("read changed state")
	}
}
func TestSakuraProviderBoundRealmRefusesAdoptionAndKeepsBorrowedDatabase(t *testing.T) {
	_, db := database(t)
	h := construct(t, db, config)
	good := actor()
	call(t, h, request("POST", nativePath, `{"Disk":{"Name":"retained","Plan":{"ID":4},"SizeMB":20480}}`, &good), 201)
	before := frame(t, db)
	for _, changed := range []sakura.Config{{Environment: "other", Project: config.Project, Zone: config.Zone, Owner: config.Owner}, {Environment: config.Environment, Project: "other", Zone: config.Zone, Owner: config.Owner}, {Environment: config.Environment, Project: config.Project, Zone: "tk1a", Owner: config.Owner}, {Environment: config.Environment, Project: config.Project, Zone: config.Zone, Owner: "other"}} {
		if next, e := sakura.NewProvider(db, changed); next != nil || !errors.Is(e, iaas.ErrScope) {
			t.Fatal("persisted realm adopted", e)
		}
		if e := db.Ping(); e != nil {
			t.Fatal("factory closed borrowed DB", e)
		}
		if frame(t, db) != before {
			t.Fatal("mismatched factory changed bound realm")
		}
	}
	if _, e := db.Exec("ALTER TABLE sakura_disk_scope RENAME COLUMN owner TO temporarily_missing"); e != nil {
		t.Fatal(e)
	}
	if next, e := sakura.NewProvider(db, config); next != nil || e == nil {
		t.Fatal("schema failure accepted")
	}
	if e := db.Ping(); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec("ALTER TABLE sakura_disk_scope RENAME COLUMN temporarily_missing TO owner"); e != nil {
		t.Fatal(e)
	}
	if frame(t, db) != before {
		t.Fatal("failed constructor mutated state")
	}
	repaired := construct(t, db, config)
	call(t, repaired, request("GET", nativePath+"/1", "", &good), 200)
	if _, e := db.Exec("UPDATE sakura_disk_scope SET owner='drift'"); e != nil {
		t.Fatal(e)
	}
	drift := frame(t, db)
	call(t, h, request("DELETE", nativePath+"/1", "", &good), 500)
	if frame(t, db) != drift {
		t.Fatal("runtime drift was adopted")
	}
}
func TestSakuraProviderRegistryKeepsLiteralNativePaths(t *testing.T) {
	_, db := database(t)
	h := construct(t, db, config)
	registry := common.New("aws")
	fallbacks := 0
	if e := registry.Register("aws", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fallbacks++; http.NotFound(w, r) })); e != nil {
		t.Fatal(e)
	}
	if e := registry.Register(sakura.ProviderName, h); e != nil {
		t.Fatal(e)
	}
	good := actor()
	mount := common.Prefix + sakura.ProviderName
	call(t, registry, request("POST", mount+nativePath, `{"Disk":{"Name":"literal +%/日本語","Plan":{"ID":4},"SizeMB":20480}}`, &good), 201)
	before := frame(t, db)
	for _, tc := range []struct {
		path   string
		status int
	}{{mount + strings.Replace(nativePath, "is1a", "tk1a", 1), 404}, {mount + strings.Replace(nativePath, "is1a", "%69s1a", 1), 400}, {mount + strings.Replace(nativePath, "/cloud/zone", "/cloud%2Fzone", 1), 400}, {mount + strings.Replace(nativePath, "/cloud/zone", "/cloud//zone", 1), 404}, {mount + strings.Replace(nativePath, "is1a", "other/../is1a", 1), 404}, {mount + nativePath + "/01", 400}, {common.Prefix + "sakura%2Fcloud/zone/is1a/api/cloud/1.1/disk", 404}} {
		call(t, registry, request("GET", tc.path, "", &good), tc.status)
		if frame(t, db) != before {
			t.Fatal("literal-path refusal changed data")
		}
	}
	if fallbacks != 0 {
		t.Fatal("Sakura route fell through to AWS")
	}
	v := call(t, registry, request("GET", mount+nativePath+`?{"From":0,"Count":0}`, "", &good), 200)
	if v["Total"] != float64(1) || v["Count"] != float64(1) {
		t.Fatal("raw native query lost")
	}
}
func TestSakuraProviderComposesWithRealHostAccessAndIndependentAuthorities(t *testing.T) {
	_, dbA := database(t)
	_, dbB := database(t)
	cfgB := config
	cfgB.Environment = "team-b"
	a := construct(t, dbA, config)
	b := construct(t, dbB, cfgB)
	tokenA, tokenB := strings.Repeat("a", 64), strings.Repeat("b", 64) // disposable test-only values, never real credentials
	accessA, e := server.NewAccess(a, tokenA, false)
	if e != nil {
		t.Fatal(e)
	}
	accessB, e := server.NewAccess(b, tokenB, false)
	if e != nil {
		t.Fatal(e)
	}
	authenticated := func(h http.Handler, method, path, token, selected, body string, status int) map[string]any {
		r := request(method, path, body, nil)
		if token != "" {
			r.Header.Set("X-Simulator-Token", token)
		}
		if selected != "" {
			r.Header.Set("X-Simulator-Principal", selected)
		}
		return call(t, h, r, status)
	}
	authenticated(accessA, "GET", nativePath, "", config.Owner, "", 401)
	authenticated(accessA, "GET", nativePath, tokenB, config.Owner, "", 401)
	authenticated(accessB, "GET", nativePath, tokenA, config.Owner, "", 401)
	authenticated(accessA, "GET", nativePath, tokenA, "", "", 403)
	for i, entry := range []struct {
		h           http.Handler
		token, name string
	}{{accessA, tokenA, "only-a"}, {accessB, tokenB, "only-b"}} {
		body := fmt.Sprintf(`{"Disk":{"Name":%q,"Plan":{"ID":4},"SizeMB":20480}}`, entry.name)
		v := authenticated(entry.h, "POST", nativePath, entry.token, config.Owner, body, 201)
		if v["Disk"].(map[string]any)["ID"] != "1" {
			t.Fatal("separate DB equal-ID fixture failed", i)
		}
	}
	peer := frame(t, dbB)
	authenticated(accessA, "DELETE", nativePath+"/1", tokenA, config.Owner, "", 200)
	if frame(t, dbB) != peer {
		t.Fatal("equal owner identity mixed database state")
	}
	v := authenticated(accessB, "GET", nativePath+"/1", tokenB, config.Owner, "", 200)
	if v["Disk"].(map[string]any)["Name"] != "only-b" {
		t.Fatal("wrong authority resolved peer state")
	}
	// This real middleware fixture is recorder-based, not a standalone CLI/process.
}

type trackedBody struct {
	reads int
	fail  bool
}

func (b *trackedBody) Read([]byte) (int, error) {
	b.reads++
	if b.fail {
		return 0, errors.New("private test body fault")
	}
	return 0, io.EOF
}
func (*trackedBody) Close() error { return nil }
func TestSakuraProviderDenialPrecedesBodyAndObservableDatabaseFault(t *testing.T) {
	_, db := database(t)
	h := construct(t, db, config)
	token := strings.Repeat("c", 64)
	access, e := server.NewAccess(h, token, false)
	if e != nil {
		t.Fatal(e)
	}
	makeRequest := func(path, principal string, validMarker bool, body *trackedBody) *http.Request {
		r := request("GET", path, "", nil)
		r.Header.Set("X-Simulator-Token", token)
		r.Header.Set("X-Simulator-Principal", principal)
		if !validMarker {
			r.SetBasicAuth("not-the-public-marker", "dummy-local-only")
		}
		r.Body = body
		return r
	}
	create := request("POST", nativePath, `{"Disk":{"Name":"retained","Plan":{"ID":4},"SizeMB":20480}}`, nil)
	create.Header.Set("X-Simulator-Token", token)
	create.Header.Set("X-Simulator-Principal", config.Owner)
	call(t, access, create, 201)
	before := frame(t, db)
	if _, e = db.Exec("ALTER TABLE sakura_disk_scope RENAME COLUMN owner TO missing_owner"); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"1", "999"} {
		for _, tc := range []struct {
			principal string
			marker    bool
			status    int
		}{{"other", true, 403}, {"other", false, 403}, {config.Owner, false, 401}} {
			body := &trackedBody{fail: true}
			call(t, access, makeRequest(nativePath+"/"+id, tc.principal, tc.marker, body), tc.status)
			if body.reads != 0 {
				t.Fatal("denied request consumed its body")
			}
			if frame(t, db) != before {
				t.Fatal("denied request changed retained data")
			}
		}
		// Authorized requests do encounter the real poisoned binding, independently
		// demonstrating that the denial tests did not run against an inert fixture.
		body := &trackedBody{}
		call(t, access, makeRequest(nativePath+"/"+id, config.Owner, true, body), 500)
		if body.reads == 0 {
			t.Fatal("authorized request did not reach body admission")
		}
		if frame(t, db) != before {
			t.Fatal("failed authorized lookup changed data")
		}
	}
	if _, e = db.Exec("ALTER TABLE sakura_disk_scope RENAME COLUMN missing_owner TO owner"); e != nil {
		t.Fatal(e)
	}
	if frame(t, db) != before {
		t.Fatal("repair changed stored graph")
	}
	for _, tc := range []struct {
		id     string
		status int
	}{{"1", 200}, {"999", 404}} {
		body := &trackedBody{}
		call(t, access, makeRequest(nativePath+"/"+tc.id, config.Owner, true, body), tc.status)
	}
	// Error priority + bodyread0 are observable evidence. They do not instrument or
	// prove that every possible implementation executes zero SQL read syscalls.
}
