package iaas

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const localServerCollection = "/cloud/zone/is1a/api/cloud/1.1/server"

func serverContractHandler(t *testing.T, db *sql.DB) *ServerHandler {
	t.Helper()
	h, err := NewServerHandler(db, testConfig, func(context.Context) (Actor, bool) {
		return Actor{testConfig.Owner, testConfig.Environment, testConfig.Project, testConfig.Zone}, true
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func serverContractRequest(t *testing.T, h http.Handler, method, path, query, body string, contentTypes []string, status int) map[string]any {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.URL.RawQuery = query
	r.SetBasicAuth("TCSIMSAKURA", "dummy-local-only")
	for _, value := range contentTypes {
		r.Header.Add("Content-Type", value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s %q: got %d, want %d: %s", method, path, query, w.Code, status, w.Body.String())
	}
	var data map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if status >= 400 {
		if _, ok := data["Server"]; ok {
			t.Fatal("refusal returned a Server")
		}
		if _, ok := data["Servers"]; ok {
			t.Fatal("refusal returned a list")
		}
	}
	return data
}

func serverContractPage(t *testing.T, h http.Handler, query string, from, total int, want ...string) map[string]any {
	t.Helper()
	page := serverContractRequest(t, h, http.MethodGet, localServerCollection, query, "", nil, http.StatusOK)
	rows, ok := page["Servers"].([]any)
	if !ok || len(rows) != len(want) || page["From"] != float64(from) || page["Count"] != float64(len(want)) || page["Total"] != float64(total) || page["is_ok"] != true {
		t.Fatalf("unexpected page %#v; want from=%d total=%d ids=%v", page, from, total, want)
	}
	for i, item := range rows {
		row, ok := item.(map[string]any)
		if !ok || row["ID"] != want[i] {
			t.Fatalf("page item %d=%v want ID %s", i, item, want[i])
		}
	}
	return page
}

func TestServerContractPaginationReopenAndDiskFrame(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "server-list.db")
	open := func() (*core.Store, *DiskStore, *ServerHandler) {
		base, err := core.New(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { base.Close() })
		disk, err := NewDiskStore(base.Database(), testConfig)
		if err != nil {
			t.Fatal(err)
		}
		return base, disk, serverContractHandler(t, base.Database())
	}
	base, disk, h := open()
	mustCreate(t, disk, "disk remains independent")
	diskFrame := frame(t, base.Database())
	var ids []string
	for i := 1; i <= 3; i++ {
		body := fmt.Sprintf(`{"Server":{"Name":"server-%d","ServerPlan":{"CPU":2,"MemoryMB":4096},"PrivateHost":{}}}`, i)
		reply := serverContractRequest(t, h, http.MethodPost, localServerCollection, "", body, []string{"application/json; charset=UTF-8"}, http.StatusCreated)
		row, ok := reply["Server"].(map[string]any)
		if !ok || row["ID"] != strconv.Itoa(i) || row["Name"] != fmt.Sprintf("server-%d", i) || row["Description"] != "" {
			t.Fatalf("unexpected created row %#v", row)
		}
		ids = append(ids, strconv.Itoa(i))
	}
	serverContractPage(t, h, `{"From":1,"Count":1}`, 1, 3, ids[1])
	serverContractRequest(t, h, http.MethodDelete, localServerCollection+"/"+ids[1], "", "", nil, http.StatusOK)
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}
	base, _, h = open()
	want := []string{ids[0], ids[2]}
	before := serverContractPage(t, h, "", 0, 2, want...)
	if got := serverContractPage(t, h, `{"Count":0}`, 0, 2, want...); !reflect.DeepEqual(got, before) {
		t.Fatal("zero Count did not use a default-sized page")
	}
	serverContractPage(t, h, url.PathEscape(`{"From":1,"Count":1}`), 1, 2, ids[2])
	serverContractPage(t, h, `{"From":2}`, 2, 2)
	for _, q := range []struct {
		name, data string
		status     int
	}{
		{"invalid percent escape", "%Q1", 400},
		{"incomplete object", "{", 400},
		{"unknown key", `{"Page":1}`, 400},
		{"fractional From", `{"From":1.5}`, 400},
		{"negative Count", `{"Count":-1}`, 400},
		{"unsupported Count type", `{"Count":"1"}`, 400},
		{"overlong query", strings.Repeat(" ", 8193), 413},
	} {
		t.Run(q.name, func(t *testing.T) {
			serverContractRequest(t, h, http.MethodGet, localServerCollection, q.data, "", nil, q.status)
			if got := serverContractPage(t, h, "", 0, 2, want...); !reflect.DeepEqual(got, before) {
				t.Fatal("invalid pagination changed persistent metadata")
			}
		})
	}
	if got, err := h.store.Get(ctx, 2); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted row returned after restart", got, err)
	}
	if frame(t, base.Database()) != diskFrame {
		t.Fatal("server lifecycle changed reopened Disk metadata")
	}
}

func TestServerContractUnsupportedMetadataKeepsSnapshot(t *testing.T) {
	_, db, _ := fixture(t)
	h := serverContractHandler(t, db)
	media := []string{"application/json"}
	body := `{"Server":{"Name":"kept","Description":"initial","ServerPlan":{"CPU":2,"MemoryMB":4096}}}`
	serverContractRequest(t, h, http.MethodPost, localServerCollection, "", body, media, 201)
	before := serverContractPage(t, h, "", 0, 1, "1")
	for _, tc := range []struct {
		name, body string
		media      []string
		status     int
	}{
		{"missing media", body, nil, 415},
		{"duplicate media", body, []string{"application/json", "application/json"}, 415},
		{"unknown media param", body, []string{"application/json; version=1"}, 415},
		{"wrong charset", body, []string{"application/json; charset=latin1"}, 415},
		{"missing ServerPlan", `{"Server":{"Name":"x"}}`, media, 400},
		{"invalid description type", `{"Server":{"Name":"x","Description":[]}}`, media, 400},
		{"CPU type", strings.Replace(body, `"CPU":2`, `"CPU":"2"`, 1), media, 400},
		{"RAM type", strings.Replace(body, `"MemoryMB":4096`, `"MemoryMB":true`, 1), media, 400},
		{"nonstandard commitment", strings.Replace(body, `"CPU":2`, `"CPU":2,"Commitment":"other"`, 1), media, 400},
		{"invalid private host type", strings.Replace(body, `"Name":"kept"`, `"Name":"x","PrivateHost":true`, 1), media, 400},
		{"non-neutral private description", strings.Replace(body, `"Name":"kept"`, `"Name":"x","PrivateHost":{"Description":"host"}`, 1), media, 400},
		{"non-neutral private tags", strings.Replace(body, `"Name":"kept"`, `"Name":"x","PrivateHost":{"Tags":["host"]}`, 1), media, 400},
		{"unsupported private field", strings.Replace(body, `"Name":"kept"`, `"Name":"x","PrivateHost":{"Other":0}`, 1), media, 400},
		{"non-neutral tags", strings.Replace(body, `"Name":"kept"`, `"Name":"x","Tags":["host"]`, 1), media, 400},
		{"unsupported interface", strings.Replace(body, `"Name":"kept"`, `"Name":"x","InterfaceDriver":"other"`, 1), media, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverContractRequest(t, h, http.MethodPost, localServerCollection, "", tc.body, tc.media, tc.status)
			if got := serverContractPage(t, h, "", 0, 1, "1"); !reflect.DeepEqual(got, before) {
				t.Fatal("refusal changed metadata")
			}
		})
	}
	next := serverContractRequest(t, h, http.MethodPost, localServerCollection, "", body, media, 201)
	if next["Server"].(map[string]any)["ID"] != "2" {
		t.Fatal("refusal consumed an ID")
	}
}

func TestServerContractStoredStateAndAllocatorFailures(t *testing.T) {
	ctx := context.Background()
	disk, db, _ := fixture(t)
	mustCreate(t, disk, "disk survives a server store error")
	diskFrame := frame(t, db)
	s, err := NewServerStore(db, testConfig)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Create(ctx, CreateServerInput{"first", "description", 2, 4096})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(ctx, CreateServerInput{"second", "", 2, 4096})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		col string
		bad any
		old any
	}{
		{"created", "missing timestamp", b.Created.Format("2006-01-02T15:04:05.999999999Z07:00")},
		{"modified", "missing timestamp", b.Modified.Format("2006-01-02T15:04:05.999999999Z07:00")},
		{"generation", "not a persisted generation", b.Generation},
	} {
		t.Run(tc.col, func(t *testing.T) {
			stmt := "UPDATE sakura_servers SET " + tc.col + "=? WHERE id=?"
			if _, err := db.Exec(stmt, tc.bad, b.ID); err != nil {
				t.Fatal(err)
			}
			if row, err := s.Get(ctx, b.ID); !errors.Is(err, ErrCorrupt) || row != (Server{}) {
				t.Fatal("corrupt row was exposed", row, err)
			}
			if page, err := s.ListFromCount(ctx, 0, 2); !errors.Is(err, ErrCorrupt) || len(page.Servers) != 0 || page.Total != 0 {
				t.Fatal("list returned a partial page", page, err)
			}
			if row, err := s.Get(ctx, a.ID); err != nil || row != a {
				t.Fatal("peer row was affected", row, err)
			}
			if _, err := db.Exec(stmt, tc.old, b.ID); err != nil {
				t.Fatal(err)
			}
			if row, err := s.Get(ctx, b.ID); err != nil || row != b {
				t.Fatal("fixture repair did not restore durable baseline", row, err)
			}
		})
	}
	if row, err := s.UpdateNameDesc(ctx, a.ID, ServerPatch{Name: &a.Name, Description: &a.Description}, a.Generation); err != nil || row != a {
		t.Fatal("idempotent update altered stored timestamps/identity", row, err)
	}
	invalid := ""
	for _, tc := range []struct {
		patch ServerPatch
		gen   []string
		want  error
	}{
		{ServerPatch{Name: &invalid}, nil, ErrInvalid},
		{ServerPatch{}, []string{"stale"}, ErrConflict},
		{ServerPatch{}, []string{""}, ErrInvalid},
	} {
		if _, err := s.UpdateNameDesc(ctx, a.ID, tc.patch, tc.gen...); !errors.Is(err, tc.want) {
			t.Fatalf("rejected update got %v want %v", err, tc.want)
		}
		if row, err := s.Get(ctx, a.ID); err != nil || row != a {
			t.Fatal("rejected update changed row", row, err)
		}
	}
	for _, id := range []int64{0, 99} {
		if err := s.Delete(ctx, id); !(errors.Is(err, ErrInvalid) && id == 0 || errors.Is(err, ErrNotFound) && id == 99) {
			t.Fatalf("delete %d: %v", id, err)
		}
	}
	for _, pair := range [][2]int64{{-1, 1}, {0, 0}, {0, 101}} {
		if page, err := s.ListFromCount(ctx, pair[0], int(pair[1])); !errors.Is(err, ErrInvalid) || len(page.Servers) != 0 {
			t.Fatal("invalid page was not rejected", page, err)
		}
	}
	if _, err := db.Exec("UPDATE sakura_server_scope SET next_id=?", int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, CreateServerInput{"out-of-ids", "", 2, 4096}); !errors.Is(err, ErrQuota) {
		t.Fatal("allocator overflow was not rejected", err)
	}
	var next int64
	if err := db.QueryRow("SELECT next_id FROM sakura_server_scope").Scan(&next); err != nil || next != math.MaxInt64 {
		t.Fatal("allocator rejection changed durable counter", next, err)
	}
	if page, err := s.ListFromCount(ctx, 0, 2); err != nil || !reflect.DeepEqual(page.Servers, []Server{a, b}) || frame(t, db) != diskFrame {
		t.Fatal("allocator rejection changed live records", page, err)
	}
}

func TestServerContractBindingCannotAdoptPartialNamespace(t *testing.T) {
	for _, missing := range []string{"sakura_servers", "sakura_server_scope", "binding row"} {
		t.Run(missing, func(t *testing.T) {
			disk, db, _ := fixture(t)
			mustCreate(t, disk, "persistent disk")
			diskFrame := frame(t, db)
			if _, err := NewServerStore(db, testConfig); err != nil {
				t.Fatal(err)
			}
			if missing == "binding row" {
				exec(t, db, "DELETE FROM sakura_server_scope")
			} else {
				exec(t, db, "DROP TABLE "+missing)
			}
			var before, after int
			q := "SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('sakura_servers','sakura_server_scope')"
			if err := db.QueryRow(q).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if s, err := NewServerStore(db, testConfig); !errors.Is(err, ErrScope) || s != nil {
				t.Fatal("adopted a namespace without full binding", s, err)
			}
			if err := db.QueryRow(q).Scan(&after); err != nil || after != before || frame(t, db) != diskFrame {
				t.Fatal("failed mount changed tables or Disk namespace", before, after, err)
			}
			if missing == "binding row" {
				if err := db.QueryRow("SELECT count(*) FROM sakura_server_scope").Scan(&after); err != nil || after != 0 {
					t.Fatal("failed mount inserted a binding", after, err)
				}
			}
		})
	}
}
