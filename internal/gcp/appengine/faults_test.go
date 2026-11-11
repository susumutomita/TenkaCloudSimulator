package appengine

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// snapshot counts every provider table, so a failed request can be shown to
// leave the state as it was.
func (h *harness) snapshot() string {
	h.t.Helper()
	var out []string
	for _, table := range []string{"gcp_appengine_applications", "gcp_appengine_services", "gcp_appengine_versions", "gcp_appengine_allocations", "gcp_appengine_firewall", "gcp_appengine_operations"} {
		var n int
		if err := h.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s=%d", table, n))
	}
	var status, shard, action string
	_ = h.db.QueryRow("SELECT serving_status FROM gcp_appengine_applications").Scan(&status)
	_ = h.db.QueryRow("SELECT group_concat(id || ':' || shard_by) FROM gcp_appengine_services").Scan(&shard)
	_ = h.db.QueryRow("SELECT group_concat(priority || ':' || action || ':' || source_range) FROM gcp_appengine_firewall").Scan(&action)
	var shares string
	_ = h.db.QueryRow("SELECT group_concat(service || '/' || version || '=' || share) FROM gcp_appengine_allocations").Scan(&shares)
	var versions string
	_ = h.db.QueryRow("SELECT group_concat(service || '/' || id || ':' || serving_status) FROM gcp_appengine_versions").Scan(&versions)
	return strings.Join(append(out, status, shard, action, shares, versions), " ")
}

func (h *harness) exec(statement string) {
	h.t.Helper()
	if _, err := h.db.Exec(statement); err != nil {
		h.t.Fatalf("%s: %v", statement, err)
	}
}

func faultHarness(t *testing.T) *harness {
	h := newHarness(t, options{runner: &fakeRunner{result: wsgiResult("200 OK", nil, "ok")}})
	h.createApp("us-central")
	h.deploy("default", "v1", map[string]string{"main.py": app})
	h.deploy("default", "v2", map[string]string{"main.py": app})
	h.deploy("api", "v1", map[string]string{"main.py": app})
	h.ok("POST", base+"/firewall/ingressRules", map[string]any{"priority": 10, "action": "ALLOW", "sourceRange": "10.0.0.0/8"})
	h.put("src/new.zip", zipOf(t, map[string]string{"main.py": app}))
	return h
}

// A failing SQLite write is an INTERNAL error without private detail, and
// the whole request rolls back.
func TestStorageWriteFailuresAreInternalAndRollBackTheRequest(t *testing.T) {
	h := faultHarness(t)
	newVersion := func(service, id string) map[string]any {
		return map[string]any{"id": id, "runtime": "python312", "deployment": map[string]any{"zip": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/new.zip"}}}
	}
	split := map[string]any{"split": map[string]any{"allocations": map[string]any{"v1": 0.5, "v2": 0.5}}}
	rule := map[string]any{"priority": 20, "action": "DENY", "sourceRange": "*"}
	cases := []struct {
		trigger, method, path string
		body                  any
	}{
		{"BEFORE INSERT ON gcp_appengine_operations", "PATCH", base + "?updateMask=servingStatus", map[string]any{"servingStatus": "USER_DISABLED"}},
		{"BEFORE UPDATE ON gcp_appengine_sequence", "PATCH", base + "?updateMask=servingStatus", map[string]any{"servingStatus": "USER_DISABLED"}},
		{"BEFORE UPDATE ON gcp_appengine_applications", "PATCH", base + "?updateMask=servingStatus", map[string]any{"servingStatus": "USER_DISABLED"}},
		{"BEFORE DELETE ON gcp_appengine_allocations", "PATCH", base + "/services/default?updateMask=split", split},
		{"BEFORE INSERT ON gcp_appengine_allocations", "PATCH", base + "/services/default?updateMask=split", split},
		{"BEFORE UPDATE ON gcp_appengine_services", "PATCH", base + "/services/default?updateMask=split", split},
		{"BEFORE DELETE ON gcp_appengine_allocations", "DELETE", base + "/services/api", nil},
		{"BEFORE DELETE ON gcp_appengine_versions", "DELETE", base + "/services/api", nil},
		{"BEFORE DELETE ON gcp_appengine_services", "DELETE", base + "/services/api", nil},
		{"BEFORE DELETE ON gcp_appengine_versions", "DELETE", base + "/services/default/versions/v2", nil},
		{"BEFORE UPDATE ON gcp_appengine_versions", "PATCH", base + "/services/default/versions/v2?updateMask=servingStatus", map[string]any{"servingStatus": "STOPPED"}},
		{"BEFORE INSERT ON gcp_appengine_versions", "POST", base + "/services/default/versions", newVersion("default", "v3")},
		{"BEFORE UPDATE ON gcp_appengine_sequence", "POST", base + "/services/default/versions", newVersion("default", "v3")},
		{"BEFORE INSERT ON gcp_appengine_services", "POST", base + "/services/web/versions", newVersion("web", "v1")},
		{"BEFORE INSERT ON gcp_appengine_allocations", "POST", base + "/services/web/versions", newVersion("web", "v1")},
		{"BEFORE INSERT ON gcp_appengine_firewall", "POST", base + "/firewall/ingressRules", rule},
		{"BEFORE UPDATE ON gcp_appengine_firewall", "PATCH", base + "/firewall/ingressRules/10?updateMask=action", map[string]any{"action": "DENY"}},
		{"BEFORE DELETE ON gcp_appengine_firewall", "DELETE", base + "/firewall/ingressRules/10", nil},
		{"BEFORE DELETE ON gcp_appengine_firewall", "POST", base + "/firewall/ingressRules:batchUpdate", map[string]any{"ingressRules": []any{rule}}},
		{"BEFORE UPDATE ON gcp_appengine_firewall", "POST", base + "/firewall/ingressRules:batchUpdate", map[string]any{"ingressRules": []any{rule}}},
		{"BEFORE INSERT ON gcp_appengine_firewall", "POST", base + "/firewall/ingressRules:batchUpdate", map[string]any{"ingressRules": []any{rule}}},
	}
	for _, c := range cases {
		before := h.snapshot()
		h.exec("CREATE TRIGGER fail " + c.trigger + " BEGIN SELECT RAISE(ABORT, 'private failure detail'); END")
		r := h.call("owner", c.method, c.path, c.body)
		h.exec("DROP TRIGGER fail")
		if r.status != 500 || !strings.Contains(r.raw, `"INTERNAL"`) || strings.Contains(r.raw, "private") {
			t.Errorf("%s %s with %s: %d %s", c.method, c.path, c.trigger, r.status, r.raw)
		}
		if after := h.snapshot(); after != before {
			t.Errorf("%s %s with %s changed state:\n%s\n%s", c.method, c.path, c.trigger, before, after)
		}
	}
	// Creating the application fails cleanly on a fresh database too.
	fresh := newHarness(t, options{runner: &fakeRunner{}})
	for _, trigger := range []string{"BEFORE INSERT ON gcp_appengine_applications", "BEFORE INSERT ON gcp_appengine_firewall", "BEFORE INSERT ON gcp_appengine_operations"} {
		fresh.exec("CREATE TRIGGER fail " + trigger + " BEGIN SELECT RAISE(ABORT, 'private'); END")
		fresh.fails("INTERNAL", "POST", "/appengine/v1/apps", map[string]any{"id": project})
		fresh.exec("DROP TRIGGER fail")
		fresh.fails("NOT_FOUND", "GET", base, nil)
	}
}

// A table the request needs is unreadable: every read reports INTERNAL, and
// the data plane answers 500 without running code.
func TestStorageReadFailuresAreInternalOnEveryPath(t *testing.T) {
	reads := []struct{ method, path string }{
		{"GET", base}, {"GET", base + "/services"}, {"GET", base + "/services/default"}, {"GET", base + "/services/default/versions"},
		{"GET", base + "/services/default/versions/v1"}, {"GET", base + "/firewall/ingressRules"}, {"GET", base + "/firewall/ingressRules/10"},
		{"GET", base + "/operations"}, {"GET", base + "/operations/x"},
	}
	writes := []struct {
		method, path string
		body         any
	}{
		{"PATCH", base + "?updateMask=servingStatus", map[string]any{"servingStatus": "SERVING"}},
		{"POST", "/appengine/v1/apps", map[string]any{"id": project}},
		{"PATCH", base + "/services/default?updateMask=split", map[string]any{"split": map[string]any{"allocations": map[string]any{"v1": 1}}}},
		{"DELETE", base + "/services/api", nil},
		{"DELETE", base + "/services/default/versions/v2", nil},
		{"PATCH", base + "/services/default/versions/v2?updateMask=servingStatus", map[string]any{"servingStatus": "SERVING"}},
		{"POST", base + "/services/default/versions", map[string]any{"id": "v9", "runtime": "python312", "deployment": map[string]any{"zip": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/new.zip"}}}},
		{"POST", base + "/services/web/versions", map[string]any{"id": "v1", "runtime": "python312", "deployment": map[string]any{"zip": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/new.zip"}}}},
		{"POST", base + "/firewall/ingressRules", map[string]any{"priority": 30, "action": "DENY", "sourceRange": "*"}},
		{"PATCH", base + "/firewall/ingressRules/10?updateMask=action", map[string]any{"action": "DENY"}},
		{"DELETE", base + "/firewall/ingressRules/10", nil},
		{"POST", base + "/firewall/ingressRules:batchUpdate", map[string]any{}},
	}
	for _, table := range []string{"gcp_appengine_applications", "gcp_appengine_services", "gcp_appengine_versions", "gcp_appengine_allocations", "gcp_appengine_firewall", "gcp_appengine_operations"} {
		// Each table gets a fresh application, so earlier requests that
		// did not need the table cannot hide later failures.
		h := faultHarness(t)
		runner := h.runner.(*fakeRunner)
		h.exec("ALTER TABLE " + table + " RENAME TO hidden_table")
		failures := 0
		for _, c := range reads {
			if r := h.call("owner", c.method, c.path, nil); r.status == 500 {
				failures++
			}
		}
		calls := len(runner.calls)
		w := h.request("GET", project+".appspot.com", "/", "10.0.0.1:1", "", nil)
		if table != "gcp_appengine_operations" && (w.Code != 500 || len(runner.calls) != calls) {
			t.Errorf("data plane without %s: %d", table, w.Code)
		}
		for _, c := range writes {
			if r := h.call("owner", c.method, c.path, c.body); r.status == 500 {
				failures++
			}
		}
		h.exec("ALTER TABLE hidden_table RENAME TO " + table)
		if failures == 0 {
			t.Errorf("no request needed %s", table)
		}
	}
	// A corrupt stored environment or operation is reported, not served.
	h := faultHarness(t)
	h.exec("UPDATE gcp_appengine_versions SET env_variables = 'not json' WHERE id = 'v2'")
	h.fails("INTERNAL", "GET", base+"/services/default/versions/v2", nil)
	h.fails("INTERNAL", "GET", base+"/services/default/versions", nil)
	h.exec(`UPDATE gcp_appengine_versions SET env_variables = '{}' WHERE id = 'v2'`)
	h.exec("UPDATE gcp_appengine_operations SET document = 'not json'")
	h.fails("INTERNAL", "GET", base+"/operations", nil)
	// A closed database fails every path, including the data plane.
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	h.fails("INTERNAL", "GET", base, nil)
	h.fails("INTERNAL", "PATCH", base+"?updateMask=servingStatus", map[string]any{"servingStatus": "SERVING"})
	if w := h.request("GET", project+".appspot.com", "/", "10.0.0.1:1", "", nil); w.Code != 500 {
		t.Fatalf("closed database data plane: %d", w.Code)
	}
}

func TestEngineHelpersReportOwnershipStatusAndDeadline(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	for path, want := range map[string]bool{"/appengine/v1/apps": true, "/appspot/x.appspot.com/": true, "/storage/v1/b": false, "/alloydb/v1/projects": false, "/": false} {
		req, _ := http.NewRequest("GET", "http://127.0.0.1"+path, nil)
		if h.engine.Owns(req) != want {
			t.Errorf("Owns(%s) = %v", path, !want)
		}
	}
	if (&status{"NOT_FOUND", "gone"}).Error() != "gone" || h.engine.RequestTimeout() != 10*time.Second {
		t.Fatal("helpers")
	}
	if official, _ := h.engine.model.field("Missing", "id"); official {
		t.Fatal("a missing schema has no fields")
	}
	if _, err := h.engine.python(context.Background()); err != nil {
		t.Fatal(err)
	}
}
