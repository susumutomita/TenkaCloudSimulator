package iaas

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerHTTPRefusalBoundaries(t *testing.T) {
	_, db, _ := fixture(t)
	extract := func(context.Context) (Actor, bool) {
		return Actor{testConfig.Owner, testConfig.Environment, testConfig.Project, testConfig.Zone}, true
	}
	h, e := NewServerHandler(db, testConfig, extract)
	if e != nil {
		t.Fatal(e)
	}
	base := "/cloud/zone/is1a/api/cloud/1.1/server"
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", base, "{\"Server\":{\"Name\":\"bad\",\"ServerPlan\":{\"CPU\":2,\"MemoryMB\":4096},\"WaitDiskMigration\":true}}", 400},
		{"POST", base, "{\"Server\":{\"Name\":\"bad\",\"ServerPlan\":{\"CPU\":2,\"MemoryMB\":4096,\"ConfidentialVM\":true}}}", 400},
		{"GET", base + "?{\"Count\":101}", "", 400}, {"GET", base + "/01", "", 400},
		{"POST", base + "/1/power", "", 400}, {"PATCH", base, "", 405}, {"GET", base, "{}", 400},
		{"POST", base, strings.Repeat("x", 65537), 413},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.SetBasicAuth("TCSIMSAKURA", "dummy-local-only")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s %s: %d want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
	page, e := h.store.ListFromCount(context.Background(), 0, 100)
	if e != nil || page.Total != 0 {
		t.Fatal("refusal changed state", page, e)
	}
	r := httptest.NewRequest("GET", base, nil)
	r.SetBasicAuth("TCSIMSAKURA", "dummy-local-only")
	r.Header.Add("Authorization", "Basic invalid")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}
