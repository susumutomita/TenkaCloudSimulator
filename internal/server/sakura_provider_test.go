package server_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/sakura"
	"github.com/susumutomita/TenkaCloudSimulator/internal/sakura/iaas"
	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
)

const sakuraServerPath = "/_simulator/providers/sakura/cloud/zone/is1a/api/cloud/1.1/disk"

var sakuraServerScope = sakura.Config{Environment: "team-a", Project: "practice", Zone: "is1a", Owner: "learner"}

func sakuraServerRequest(t *testing.T, h http.Handler, method, path, body string, auth bool, status int) map[string]any {
	t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	r.SetBasicAuth("TCSIMSAKURA", "dummy-local-only")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Simulator-Principal", "learner")
	if auth {
		r.Header.Set("X-Simulator-Token", strings.Repeat("s", 64))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s status%d wanted%d (body redacted)", method, path, w.Code, status)
	}
	var v map[string]any
	if strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
	}
	return v
}
func sakuraServerObserver(t *testing.T, path string) *core.Store {
	t.Helper()
	s, e := core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func sakuraServerFrame(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, e := db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if e != nil {
		t.Fatal(e)
	}
	var names []string
	for rows.Next() {
		var n string
		if e = rows.Scan(&n); e != nil {
			t.Fatal(e)
		}
		names = append(names, n)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		t.Fatal(e)
	}
	world := map[string]any{}
	for _, name := range names {
		query := "SELECT * FROM \"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""
		rows, e = db.Query(query)
		if e != nil {
			t.Fatal(e)
		}
		cols, e := rows.Columns()
		if e != nil {
			t.Fatal(e)
		}
		encodedRows := []string{}
		for rows.Next() {
			values := make([]any, len(cols))
			p := make([]any, len(values))
			for i := range values {
				p[i] = &values[i]
			}
			if e = rows.Scan(p...); e != nil {
				t.Fatal(e)
			}
			b, e := json.Marshal(values)
			if e != nil {
				t.Fatal(e)
			}
			encodedRows = append(encodedRows, string(b))
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			t.Fatal(e)
		}
		sort.Strings(encodedRows)
		world[name] = encodedRows
	}
	b, e := json.Marshal(world)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func sakuraAssertPeerCapabilities(t *testing.T, caps map[string]any) {
	t.Helper()
	services, ok := caps["services"].(map[string]any)
	if !ok {
		t.Fatal("missing native AWS services")
	}
	kms, ok := services["kms"].(map[string]any)
	if !ok {
		t.Fatal("missing native KMS capability")
	}
	if !reflect.DeepEqual(kms["operations"], []any{"CreateKey", "DescribeKey", "ListKeys", "EnableKey", "DisableKey", "GetKeyPolicy", "Encrypt", "Decrypt", "CreateAlias", "ListAliases", "UpdateAlias", "DeleteAlias"}) {
		t.Fatal("incoming twelve-operation KMS capability changed")
	}
	storage, ok := caps["cloudStorage"].(map[string]any)
	if !ok || len(storage) != 2 || storage["gcp"] == nil || storage["azure"] == nil {
		t.Fatal("peer storage capabilities changed")
	}
}

func TestSakuraServerDefaultDisabledPreservesLegacyProviderSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	h, closeState, e := server.New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer closeState()
	caps := sakuraServerRequest(t, h, "GET", "/_simulator/v1/capabilities", "", false, 200)
	sakuraAssertPeerCapabilities(t, caps)
	if !reflect.DeepEqual(caps["providers"], []any{"aws", "azure", "gcp"}) {
		t.Fatal("default provider set changed")
	}
	if _, ok := caps["sakura"]; ok {
		t.Fatal("disabled provider advertised")
	}
	storage := caps["cloudStorage"].(map[string]any)
	if len(storage) != 2 || storage["gcp"] == nil || storage["azure"] == nil {
		t.Fatal("legacy cloud storage capability changed")
	}
	sakuraServerRequest(t, h, "GET", sakuraServerPath, "", false, 404)
	observer := sakuraServerObserver(t, path)
	var count int
	if e = observer.Database().QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('sakura_disk_scope','sakura_disks')").Scan(&count); e != nil || count != 0 {
		t.Fatal("disabled provider initialized storage", e)
	}
}
func TestSakuraServerOptInUsesRegistryAuthenticationAndCopiedScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	scope := sakuraServerScope
	options := server.DefaultProviderConfig()
	options.SakuraDisks = &scope
	h, closeState, e := server.NewWithProviders(path, options)
	if e != nil {
		t.Fatal(e)
	}
	defer closeState()
	scope.Zone = "tk1a"
	scope.Owner = "changed"
	// Trusted embedding without host authentication must not synthesize an owner.
	sakuraServerRequest(t, h, "GET", sakuraServerPath, "", false, 403)
	access, e := server.NewAccess(h, strings.Repeat("s", 64), false)
	if e != nil {
		t.Fatal(e)
	}
	sakuraServerRequest(t, access, "GET", sakuraServerPath, "", false, 401)
	created := sakuraServerRequest(t, access, "POST", sakuraServerPath, `{"Disk":{"Name":"actual-root","Plan":{"ID":4},"SizeMB":20480}}`, true, 201)
	disk := created["Disk"].(map[string]any)
	if disk["ID"] != "1" || disk["Name"] != "actual-root" {
		t.Fatal("root did not create actual Disk")
	}
	if _, ok := disk["Availability"]; ok {
		t.Fatal("invented physical readiness")
	}
	caps := sakuraServerRequest(t, access, "GET", "/_simulator/v1/capabilities", "", true, 200)
	sakuraAssertPeerCapabilities(t, caps)
	if !reflect.DeepEqual(caps["providers"], []any{"aws", "azure", "gcp", "sakura"}) {
		t.Fatal("optional registry missing provider")
	}
	capability := caps["sakura"].(map[string]any)
	if capability["endpoint"] != sakuraServerPath || capability["availabilityReported"] != false || capability["console"] != true || capability["sdkVerified"] != false {
		t.Fatal("misleading optional capability")
	}
	if !reflect.DeepEqual(capability["operations"], []any{"GET /disk", "POST /disk", "GET /disk/{id}", "PUT /disk/{id}", "DELETE /disk/{id}"}) {
		t.Fatal("capability not exact HTTP subset")
	}
	if _, ok := capability["owner"]; ok {
		t.Fatal("capability leaked owner")
	}
	sakuraServerRequest(t, access, "GET", strings.Replace(sakuraServerPath, "is1a", "%69s1a", 1), "", true, 400)
	sakuraServerRequest(t, access, "GET", strings.Replace(sakuraServerPath, "is1a", "tk1a", 1), "", true, 404)
	observer := sakuraServerObserver(t, path)
	var name string
	if e = observer.Database().QueryRow("SELECT name FROM sakura_disks WHERE id=1").Scan(&name); e != nil || name != "actual-root" {
		t.Fatal("HTTP/SQLite state diverged", e)
	}
}
func TestSakuraServerConstructorFailurePreservesStateAndAllowsRepair(t *testing.T) {
	dir := t.TempDir()
	missingPath := filepath.Join(dir, "not-created", "state.db")
	invalid := server.DefaultProviderConfig()
	invalid.SakuraDisks = &sakura.Config{}
	if h, closeState, e := server.NewWithProviders(missingPath, invalid); h != nil || closeState != nil || !errors.Is(e, iaas.ErrInvalid) {
		t.Fatal("invalid preflight reached filesystem", e)
	}
	if _, e := os.Stat(filepath.Dir(missingPath)); !os.IsNotExist(e) {
		t.Fatal("invalid constructor created state directory")
	}
	path := filepath.Join(dir, "valid.db")
	scope := sakuraServerScope
	options := server.DefaultProviderConfig()
	options.SakuraDisks = &scope
	h, closeState, e := server.NewWithProviders(path, options)
	if e != nil {
		t.Fatal(e)
	}
	access, e := server.NewAccess(h, strings.Repeat("s", 64), false)
	if e != nil {
		t.Fatal(e)
	}
	sakuraServerRequest(t, access, "POST", sakuraServerPath, `{"Disk":{"Name":"retained","Plan":{"ID":4},"SizeMB":20480}}`, true, 201)
	if e = closeState(); e != nil {
		t.Fatal(e)
	}
	observer := sakuraServerObserver(t, path)
	before := sakuraServerFrame(t, observer.Database())
	for _, field := range []string{"environment", "project", "zone", "owner"} {
		changed := scope
		switch field {
		case "environment":
			changed.Environment = "other"
		case "project":
			changed.Project = "other"
		case "zone":
			changed.Zone = "tk1a"
		case "owner":
			changed.Owner = "other"
		}
		bad := options
		bad.SakuraDisks = &changed
		handler, closer, err := server.NewWithProviders(path, bad)
		if handler != nil || closer != nil || !errors.Is(err, iaas.ErrScope) {
			t.Fatalf("wrong %s adopted state: %v", field, err)
		}
		if e = observer.Database().Ping(); e != nil {
			t.Fatal(e)
		}
		if sakuraServerFrame(t, observer.Database()) != before {
			t.Fatal("failed startup changed any provider data")
		}
	}
	repaired, closer, e := server.NewWithProviders(path, options)
	if e != nil {
		t.Fatal(e)
	}
	defer closer()
	authenticated, e := server.NewAccess(repaired, strings.Repeat("s", 64), false)
	if e != nil {
		t.Fatal(e)
	}
	value := sakuraServerRequest(t, authenticated, "GET", sakuraServerPath+"/1", "", true, 200)
	if fmt.Sprint(value["Disk"].(map[string]any)["Name"]) != "retained" {
		t.Fatal("repair lost saved resource")
	}
}
