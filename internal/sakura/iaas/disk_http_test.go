package iaas

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const diskPath = "/cloud/zone/is1a/api/cloud/1.1/disk"
const validDiskJSON = `{"Disk":{"Name":"empty config","Plan":{"ID":4},"SizeMB":20480}}`

type actorKey struct{}

func trustedActor() Actor {
	return Actor{testConfig.Owner, testConfig.Environment, testConfig.Project, testConfig.Zone}
}
func testHandler(t *testing.T, s *DiskStore) *DiskHandler {
	t.Helper()
	h, e := NewDiskHandler(s, testConfig, func(ctx context.Context) (Actor, bool) { v, ok := ctx.Value(actorKey{}).(Actor); return v, ok })
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func diskRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), actorKey{}, trustedActor()))
	r.SetBasicAuth("TCSIMSAKURA", "dummy-local-only")
	if method == "POST" || method == "PUT" {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}
func diskCall(t *testing.T, h http.Handler, method, path, body string, status int) map[string]any {
	t.Helper()
	return diskResult(t, h, diskRequest(method, path, body), status)
}
func diskResult(t *testing.T, h http.Handler, r *http.Request, status int) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: got %d want %d: %s", r.Method, r.URL.Path, w.Code, status, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
		t.Fatal(w.Header())
	}
	var v map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
		t.Fatal(e)
	}
	if status >= 400 {
		if v["is_ok"] != false || v["error_code"] == nil || v["status"] != fmt.Sprintf("%d %s", status, http.StatusText(status)) {
			t.Fatal(v)
		}
		for _, field := range []string{"Disk", "Disks", "Name", "Description"} {
			if _, ok := v[field]; ok {
				t.Fatal("error returned metadata", v)
			}
		}
	}
	return v
}
func resultDisk(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	d, ok := v["Disk"].(map[string]any)
	if !ok {
		t.Fatal("missing saved Disk", v)
	}
	for _, field := range []string{"Availability", "Storage", "Server", "Generation", "Owner", "ServiceClass", "MigratedMB", "JobStatus"} {
		if _, exists := d[field]; exists {
			t.Fatal("invented/private projection", field)
		}
	}
	return d
}
func TestDiskHTTPLifecycleNativeProjectionAndCommittedReopen(t *testing.T) {
	s, _, path := fixture(t)
	h := testHandler(t, s)
	input := `{"Disk":{"Name":"a+%/日本語","Description":"saved","Plan":{"ID":"4"},"SizeMB":20480,"Connection":"virtio","Tags":[],"Icon":{}},"KMSKey":{},"DistantFrom":[]}`
	created := diskCall(t, h, "POST", diskPath, input, 201)
	if created["is_ok"] != true || created["Success"] != "Created" {
		t.Fatal(created)
	}
	d := resultDisk(t, created)
	id, ok := d["ID"].(string)
	if !ok || d["Name"] != "a+%/日本語" || d["Connection"] != "virtio" || d["SizeMB"] != float64(20480) {
		t.Fatal(d)
	}
	second := resultDisk(t, diskCall(t, h, "POST", diskPath, input, 201))
	if second["ID"] == id {
		t.Fatal("same-name identity adopted")
	}
	read := resultDisk(t, diskCall(t, h, "GET", diskPath+"/"+id, "", 200))
	if read["Description"] != "saved" {
		t.Fatal(read)
	}
	renamed := resultDisk(t, diskCall(t, h, "PUT", diskPath+"/"+id, `{"Disk":{"Name":"renamed","Tags":null,"Icon":null,"Connection":"virtio"}}`, 200))
	if renamed["Description"] != "saved" || renamed["Name"] != "renamed" {
		t.Fatal(renamed)
	}
	cleared := resultDisk(t, diskCall(t, h, "PUT", diskPath+"/"+id, `{"Disk":{"Description":""}}`, 200))
	if cleared["Description"] != "" || cleared["Name"] != "renamed" {
		t.Fatal(cleared)
	}
	p := diskCall(t, h, "GET", diskPath+`?{"From":0,"Count":1}`, "", 200)
	if p["Total"] != float64(2) || p["Count"] != float64(1) || p["From"] != float64(0) {
		t.Fatal(p)
	}
	p = diskCall(t, h, "GET", diskPath+"?"+url.PathEscape(`{"From":1,"Count":0}`), "", 200)
	if p["Count"] != float64(1) || p["Disks"].([]any)[0].(map[string]any)["ID"] != second["ID"] {
		t.Fatal(p)
	}
	base, e := core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer base.Close()
	reopened, e := NewDiskStore(base.Database(), testConfig)
	if e != nil {
		t.Fatal(e)
	}
	h2 := testHandler(t, reopened)
	persisted := resultDisk(t, diskCall(t, h2, "GET", diskPath+"/"+id, "", 200))
	if persisted["Name"] != "renamed" || persisted["Description"] != "" {
		t.Fatal(persisted)
	}
	gone := diskCall(t, h2, "DELETE", diskPath+"/"+id, "", 200)
	if gone["Success"] != true || gone["is_ok"] != true {
		t.Fatal(gone)
	}
	diskCall(t, h, "GET", diskPath+"/"+id, "", 404)
	i, _ := strconv.ParseInt(second["ID"].(string), 10, 64)
	if e = reopened.Delete(context.Background(), i); e != nil {
		t.Fatal(e)
	}
	p = diskCall(t, h, "GET", diskPath, "", 200)
	if p["Total"] != float64(0) || len(p["Disks"].([]any)) != 0 {
		t.Fatal(p)
	}
}
func TestDiskHTTPStrictAdmissionNoMutation(t *testing.T) {
	s, db, _ := fixture(t)
	h := testHandler(t, s)
	d := mustCreate(t, s, "retained")
	id := diskPath + "/" + strconv.FormatInt(d.ID, 10)
	before := frame(t, db)
	cases := []struct {
		name, method, path, body string
		status                   int
	}{
		{"duplicate envelope", "POST", diskPath, `{"Disk":{},"Disk":{}}`, 400},
		{"escaped duplicate", "POST", diskPath, `{"Disk":{"Name":"a","\u004eame":"b"}}`, 400},
		{"duplicate nested plan", "POST", diskPath, `{"Disk":{"Name":"a","Plan":{"ID":4,"ID":4},"SizeMB":20480}}`, 400},
		{"case", "POST", diskPath, `{"Disk":{"name":"a"}}`, 400},
		{"trailing", "POST", diskPath, validDiskJSON + `{}`, 400},
		{"null disk", "POST", diskPath, `{"Disk":null}`, 400},
		{"null name", "POST", diskPath, `{"Disk":{"Name":null}}`, 400},
		{"description null", "PUT", id, `{"Disk":{"Description":null}}`, 400},
		{"invalid utf8", "POST", diskPath, "{\"Disk\":{\"Name\":\"" + string([]byte{255}) + "\"}}", 400},
		{"high surrogate", "POST", diskPath, `{"Disk":{"Name":"\ud800"}}`, 400},
		{"low surrogate", "POST", diskPath, `{"Disk":{"Name":"\udc00"}}`, 400},
		{"missing pair", "POST", diskPath, `{"Disk":{"Name":"\ud800\u0041"}}`, 400},
		{"unknown false", "POST", diskPath, `{"Disk":{"Name":"a"},"BootAtAvailable":false}`, 501},
		{"unknown null", "PUT", id, `{"Disk":{"SourceDisk":null}}`, 501},
		{"tags", "PUT", id, `{"Disk":{"Tags":["tag"]}}`, 501},
		{"real icon unsupported", "PUT", id, `{"Disk":{"Icon":{"ID":1}}}`, 501},
		{"kms member zero", "POST", diskPath, `{"Disk":{},"KMSKey":{"ID":0}}`, 501},
		{"placement", "POST", diskPath, `{"Disk":{},"DistantFrom":[1]}`, 501},
		{"plan2", "POST", diskPath, `{"Disk":{"Name":"a","Plan":{"ID":2},"SizeMB":20480}}`, 501},
		{"wrong size", "POST", diskPath, `{"Disk":{"Name":"a","Plan":{"ID":4},"SizeMB":1}}`, 501},
		{"plan float", "POST", diskPath, `{"Disk":{"Name":"a","Plan":{"ID":4.0},"SizeMB":20480}}`, 400},
		{"plan leading zero", "POST", diskPath, `{"Disk":{"Name":"a","Plan":{"ID":"04"},"SizeMB":20480}}`, 400},
		{"size string", "POST", diskPath, `{"Disk":{"Name":"a","Plan":{"ID":4},"SizeMB":"20480"}}`, 400},
		{"connection", "PUT", id, `{"Disk":{"Connection":"ide"}}`, 501},
		{"empty name", "PUT", id, `{"Disk":{"Name":""}}`, 400},
		{"control name", "PUT", id, `{"Disk":{"Name":"a\n"}}`, 400},
		{"query filter", "GET", diskPath + `?{"Filter":{}}`, "", 501},
		{"query sort empty", "GET", diskPath + `?{"Sort":[]}`, "", 501},
		{"query case", "GET", diskPath + `?{"count":1}`, "", 400},
		{"query duplicate", "GET", diskPath + `?{"Count":1,"Count":0}`, "", 400},
		{"query forms", "GET", diskPath + `?From=0&Count=1`, "", 400},
		{"query double encoded", "GET", diskPath + `?%257B%257D`, "", 400},
		{"query malformed escape", "GET", diskPath + `?%GG`, "", 400},
		{"query body", "GET", diskPath + `?{}`, `{}`, 400},
		{"query count string", "GET", diskPath + `?{"Count":"1"}`, "", 400},
		{"query count negative", "GET", diskPath + `?{"Count":-1}`, "", 400},
		{"query count101", "GET", diskPath + `?{"Count":101}`, "", 400},
		{"query from exponent", "GET", diskPath + `?{"From":1e1}`, "", 400},
		{"query overflow", "GET", diskPath + `?{"From":9223372036854775808}`, "", 400},
		{"nonlist query", "GET", id + `?{}`, "", 400},
		{"delete body", "DELETE", id, `{}`, 400},
		{"id zero", "GET", diskPath + `/0`, "", 400},
		{"id leading zero", "GET", diskPath + `/01`, "", 400},
		{"id overflow", "GET", diskPath + `/9223372036854775808`, "", 400},
		{"escaped delimiter", "GET", diskPath + `%2F1`, "", 400},
		{"suboperation", "PUT", id + `/config`, `{}`, 501},
		{"wrong zone", "GET", strings.Replace(diskPath, "is1a", "tk1a", 1), "", 404},
		{"method", "PATCH", id, `{}`, 405},
		{"head unsupported", "HEAD", id, "", 405},
		{"body cap", "POST", diskPath, strings.Repeat("x", 65537), 413},
		{"query cap", "GET", diskPath + "?" + strings.Repeat("x", 8193), "", 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diskCall(t, h, tc.method, tc.path, tc.body, tc.status)
			if frame(t, db) != before {
				t.Fatal("refusal changed full persisted graph")
			}
		})
	}
	diskCall(t, h, "PUT", id, `{"Disk":{"Name":"repaired"}}`, 200)
}
func TestDiskHTTPActorAndDummyMarkerAreDistinct(t *testing.T) {
	s, db, _ := fixture(t)
	h := testHandler(t, s)
	mustCreate(t, s, "private name")
	before := frame(t, db)
	for _, a := range []Actor{{}, {"other", testConfig.Environment, testConfig.Project, testConfig.Zone}, {testConfig.Owner, "other", testConfig.Project, testConfig.Zone}, {testConfig.Owner, testConfig.Environment, "other", testConfig.Zone}, {testConfig.Owner, testConfig.Environment, testConfig.Project, "other"}} {
		r := diskRequest("GET", diskPath, "")
		r = r.WithContext(context.WithValue(r.Context(), actorKey{}, a))
		r.Header.Set("X-Simulator-Access-Role", "operator")
		r.Header.Set("X-Simulator-Principal", testConfig.Owner)
		diskResult(t, h, r, 403)
	}
	r := diskRequest("GET", diskPath, "")
	r = r.WithContext(context.Background())
	r.Header.Set("X-Simulator-Token", "not-a-host-grant")
	diskResult(t, h, r, 403)
	for _, headers := range [][]string{nil, {"Basic " + base64.StdEncoding.EncodeToString([]byte("other:dummy-local-only"))}, {"Basic invalid"}, {"Basic " + base64.StdEncoding.EncodeToString([]byte("TCSIMSAKURA:dummy-local-only")), "Basic duplicate"}} {
		r := diskRequest("GET", diskPath, "")
		r.Header.Del("Authorization")
		for _, v := range headers {
			r.Header.Add("Authorization", v)
		}
		diskResult(t, h, r, 401)
	}
	if frame(t, db) != before {
		t.Fatal("denied authority changed data")
	}
	exec(t, db, "UPDATE sakura_disk_scope SET owner='drift'")
	r = diskRequest("GET", diskPath, "")
	diskResult(t, h, r, 500)
	r = r.WithContext(context.WithValue(r.Context(), actorKey{}, Actor{Subject: "other"}))
	diskResult(t, h, r, 403)
}
func TestDiskHTTPFailuresRollbackSanitizeAndRepair(t *testing.T) {
	for _, kind := range []string{"insert", "update", "delete"} {
		t.Run(kind, func(t *testing.T) {
			s, db, _ := fixture(t)
			h := testHandler(t, s)
			d := mustCreate(t, s, "retain")
			before := frame(t, db)
			id := diskPath + "/" + strconv.FormatInt(d.ID, 10)
			event := map[string]string{"insert": "INSERT", "update": "UPDATE", "delete": "DELETE"}[kind]
			exec(t, db, "CREATE TRIGGER ignore_http BEFORE "+event+" ON sakura_disks BEGIN SELECT RAISE(IGNORE); END")
			method, path, body := "POST", diskPath, validDiskJSON
			if kind == "update" {
				method, path, body = "PUT", id, `{"Disk":{"Name":"changed"}}`
			}
			if kind == "delete" {
				method, path, body = "DELETE", id, ""
			}
			v := diskCall(t, h, method, path, body, 500)
			if v["error_code"] != "simulator_internal" || strings.Contains(fmt.Sprint(v), "sakura_disks") {
				t.Fatal(v)
			}
			if frame(t, db) != before {
				t.Fatal("failed HTTP operation partially committed")
			}
			exec(t, db, "DROP TRIGGER ignore_http")
			status := 200
			if kind == "insert" {
				status = 201
			}
			diskCall(t, h, method, path, body, status)
		})
	}
	s, db, _ := fixture(t)
	h := testHandler(t, s)
	d := mustCreate(t, s, "retain")
	before := frame(t, db)
	r := diskRequest("POST", diskPath, validDiskJSON)
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	r = r.WithContext(ctx)
	diskResult(t, h, r, 500)
	if frame(t, db) != before {
		t.Fatal("canceled request wrote data")
	}
	exec(t, db, "ALTER TABLE sakura_disks RENAME COLUMN description TO secret_column_fault")
	v := diskCall(t, h, "GET", diskPath+"/"+strconv.FormatInt(d.ID, 10), "", 500)
	if strings.Contains(fmt.Sprint(v), "secret_column_fault") {
		t.Fatal("SQL error leaked")
	}
	exec(t, db, "ALTER TABLE sakura_disks RENAME COLUMN secret_column_fault TO description")
	if frame(t, db) != before {
		t.Fatal("read failure changed data")
	}
	diskCall(t, h, "GET", diskPath+"/"+strconv.FormatInt(d.ID, 10), "", 200)
}
func TestDiskHTTPQuotaUnicodeAndMediaType(t *testing.T) {
	s, db, _ := fixture(t)
	h := testHandler(t, s)
	for _, body := range []string{`{"Disk":{"Name":"\ud83d\ude00","Plan":{"ID":4},"SizeMB":20480}}`, `{"Disk":{"Name":"\\ud800","Plan":{"ID":4},"SizeMB":20480}}`} {
		diskCall(t, h, "POST", diskPath, body, 201)
	}
	p := diskCall(t, h, "GET", diskPath, "", 200)
	disks := p["Disks"].([]any)
	if disks[0].(map[string]any)["Name"] != "😀" || disks[1].(map[string]any)["Name"] != `\ud800` {
		t.Fatal(disks)
	}
	before := frame(t, db)
	for _, contentType := range []string{"", "text/plain", "application/json; foo=bar", "application/json; charset=latin1"} {
		r := diskRequest("POST", diskPath, validDiskJSON)
		r.Header.Del("Content-Type")
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		diskResult(t, h, r, 415)
	}
	r := diskRequest("POST", diskPath, validDiskJSON)
	r.Header.Add("Content-Type", "application/json")
	diskResult(t, h, r, 415)
	if frame(t, db) != before {
		t.Fatal("media type refusal mutated state")
	}
	for i := 2; i < 100; i++ {
		mustCreate(t, s, "bounded")
	}
	before = frame(t, db)
	diskCall(t, h, "POST", diskPath, validDiskJSON, 406)
	if frame(t, db) != before {
		t.Fatal("quota mutation")
	}
	p = diskCall(t, h, "GET", diskPath+`?{"Count":0,"From":1}`, "", 200)
	if p["Total"] != float64(100) || p["Count"] != float64(99) {
		t.Fatal(p["Total"], p["Count"])
	}
	if _, e := NewDiskHandler(nil, testConfig, func(context.Context) (Actor, bool) { return Actor{}, false }); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	other := testConfig
	other.Owner = "other"
	if _, e := NewDiskHandler(s, other, func(context.Context) (Actor, bool) { return Actor{}, false }); !errors.Is(e, ErrScope) {
		t.Fatal(e)
	}
}

// This is trusted actor composition across two metadata databases, not a host
// token/session or standalone provider-isolation test.
func TestDiskHTTPSeparateDatabaseAndActorScopes(t *testing.T) {
	s, db, _ := fixture(t)
	h := testHandler(t, s)
	a := mustCreate(t, s, "team-a-only")
	other, e := core.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	cfg := Config{"practice-b", "project-b", "is1a", "team-b"}
	peer, e := NewDiskStore(other.Database(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	ph, e := NewDiskHandler(peer, cfg, func(ctx context.Context) (Actor, bool) { v, ok := ctx.Value(actorKey{}).(Actor); return v, ok })
	if e != nil {
		t.Fatal(e)
	}
	b := mustCreate(t, peer, "team-b-only")
	if a.ID != b.ID {
		t.Fatal("fixture must use equal numeric IDs in distinct DBs")
	}
	af, bf := frame(t, db), frame(t, other.Database())
	path := diskPath + "/" + strconv.FormatInt(a.ID, 10)
	// An actor from the first environment cannot select the other DB's equal ID.
	diskCall(t, ph, "DELETE", path, "", 403)
	r := diskRequest("GET", path, "")
	r = r.WithContext(context.WithValue(r.Context(), actorKey{}, Actor{cfg.Owner, cfg.Environment, cfg.Project, cfg.Zone}))
	r.Header.Set("X-Simulator-Principal", testConfig.Owner)
	d := resultDisk(t, diskResult(t, ph, r, 200))
	if d["Name"] != "team-b-only" {
		t.Fatal(d)
	}
	// The first handler cannot accept that peer actor despite forged operator headers.
	r = diskRequest("DELETE", path, "")
	r = r.WithContext(context.WithValue(r.Context(), actorKey{}, Actor{cfg.Owner, cfg.Environment, cfg.Project, cfg.Zone}))
	r.Header.Set("X-Simulator-Access-Role", "operator")
	diskResult(t, h, r, 403)
	if frame(t, db) != af || frame(t, other.Database()) != bf {
		t.Fatal("peer or denied owner changed state")
	}
	d = resultDisk(t, diskCall(t, h, "GET", path, "", 200))
	if d["Name"] != "team-a-only" {
		t.Fatal(d)
	}
}

type bodyReaderFunc func([]byte) (int, error)

func (f bodyReaderFunc) Read(p []byte) (int, error) { return f(p) }
func TestDiskHTTPBodyFailuresAreAtomicAndSanitized(t *testing.T) {
	s, db, _ := fixture(t)
	h := testHandler(t, s)
	mustCreate(t, s, "retained")
	before := frame(t, db)
	r := diskRequest("POST", diskPath, validDiskJSON)
	r.Body = io.NopCloser(bodyReaderFunc(func([]byte) (int, error) { return 0, errors.New("private-reader-fault") }))
	v := diskResult(t, h, r, 500)
	if strings.Contains(fmt.Sprint(v), "private-reader-fault") {
		t.Fatal("reader details leaked")
	}
	if frame(t, db) != before {
		t.Fatal("reader failure committed")
	}
	r = diskRequest("POST", diskPath, validDiskJSON)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)
	read := false
	r.Body = io.NopCloser(bodyReaderFunc(func(p []byte) (int, error) {
		if read {
			return 0, io.EOF
		}
		read = true
		n := copy(p, validDiskJSON)
		cancel()
		return n, io.EOF
	}))
	diskResult(t, h, r, 500)
	if frame(t, db) != before {
		t.Fatal("cancellation during body read committed")
	}
	// A real nil body must remain usable for native GET, without a synthetic reader.
	r = diskRequest("GET", diskPath, "")
	r.Body = nil
	diskResult(t, h, r, 200)
	diskCall(t, h, "POST", diskPath, validDiskJSON, 201)
}
func TestDiskHTTPIncompleteJSONAndRequiredFieldsRefuseAtomically(t *testing.T) {
	s, db, _ := fixture(t)
	h := testHandler(t, s)
	d := mustCreate(t, s, "retained")
	path := diskPath + "/" + strconv.FormatInt(d.ID, 10)
	before := frame(t, db)
	cases := []struct {
		body   string
		status int
	}{
		{`{`, 400}, {`{"Disk":`, 400}, {`{"Disk":{"Name":`, 400}, {`{"Disk":{"Name":"x"`, 400},
		{`{"Disk":[`, 400}, {`{"Disk":[1,`, 400}, {`{"Disk":[1`, 400}, {`{"Disk":{"Name":"x",`, 400},
		{`{"Disk":{"Name":"unfinished\`, 400}, {`{"Disk":{"Name":"\u12`, 400}, {`{"Disk":{"Name":"\uZZZZ"}}`, 400},
		{`{"Disk":{"Name":"\ud800\uZZZZ"}}`, 400},
		{strings.Repeat(`{"nested":`, 18) + `0` + strings.Repeat(`}`, 18), 400},
		{`{"Disk":{"Plan":{"ID":4},"SizeMB":20480}}`, 400},
		{`{"Disk":{"Name":"new","Description":false,"Plan":{"ID":4},"SizeMB":20480}}`, 400},
		{`{"Disk":{"Name":"new","Plan":{"ID":null},"SizeMB":20480}}`, 400},
		{`{"Disk":{"Name":"new","Plan":{},"SizeMB":20480}}`, 400},
		{`{"Disk":{"Name":"new","SizeMB":20480}}`, 400},
		{`{"Disk":{"Name":"new","Plan":{"ID":4}}}`, 400},
		{`{"Disk":{"Name":"new","Connection":false,"Plan":{"ID":4},"SizeMB":20480}}`, 400},
	}
	for i, tc := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			diskCall(t, h, "POST", diskPath, tc.body, tc.status)
			if frame(t, db) != before {
				t.Fatal("incomplete/invalid request changed complete data frame")
			}
		})
	}
	diskCall(t, h, "PUT", path, `{"Disk":{"Name":null}}`, 400)
	if frame(t, db) != before {
		t.Fatal("null update mutated state")
	}
	r := diskRequest("GET", diskPath, "")
	r.Header.Set("Authorization", "Basic")
	diskResult(t, h, r, 401)
	badZone := testConfig
	badZone.Zone = "is1a/b"
	base, e := core.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer base.Close()
	badStore, e := NewDiskStore(base.Database(), badZone)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = NewDiskHandler(badStore, badZone, func(context.Context) (Actor, bool) { return Actor{}, false }); !errors.Is(e, ErrInvalid) {
		t.Fatal("unsafe route zone accepted", e)
	}
	diskCall(t, h, "PUT", path, `{"Disk":{"Name":"repaired"}}`, 200)
}
