package aws

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWAFHTTPRefusesMalformedTransportAndIdentifiers(t *testing.T) {
	store, _ := wafFixture(t)
	deep := strings.Repeat("[", 40) + strings.Repeat("]", 40)
	id := "a1b2c3d4-0000-4000-8000-000000000000"
	for name, c := range map[string]struct{ operation, body string }{
		"deep JSON":      {"ListWebACLs", `{"Scope":"REGIONAL","Tags":` + deep + `}`},
		"oversized body": {"ListWebACLs", `{"Scope":"REGIONAL","NextMarker":"` + strings.Repeat("a", 262144) + `"}`},
		"bad Id":         {"GetWebACL", `{"Name":"incident","Id":"nope","Scope":"REGIONAL"}`},
		"bad Name":       {"GetWebACL", `{"Name":"bad name!","Id":"` + id + `","Scope":"REGIONAL"}`},
		"bad LockToken":  {"DeleteWebACL", `{"Name":"incident","Id":"` + id + `","Scope":"REGIONAL","LockToken":"nope"}`},
		"zero Limit":     {"ListWebACLs", `{"Scope":"REGIONAL","Limit":0}`},
		"bad NextMarker": {"ListWebACLs", `{"Scope":"REGIONAL","NextMarker":"nope"}`},
		"trailing value": {"ListWebACLs", `{"Scope":"REGIONAL"} {}`},
	} {
		w := wafCall(store, c.operation, c.body, wafAllow)
		var out map[string]string
		if w.Code != 400 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out["__type"] != "WAFInvalidParameterException" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	for name, edit := range map[string]func() (string, string){
		"GET":    func() (string, string) { return "GET", "/" },
		"query":  func() (string, string) { return "POST", "/?x=1" },
		"path":   func() (string, string) { return "POST", "/acl" },
		"region": func() (string, string) { return "POST", "/" },
	} {
		method, target := edit()
		r := httptest.NewRequest(method, target, strings.NewReader(`{"Scope":"REGIONAL"}`))
		r.Header.Set("X-Amz-Target", "AWSWAF_20190729.ListWebACLs")
		w := httptest.NewRecorder()
		region := wafTestRegion
		if name == "region" {
			region = "mars-1"
		}
		store.ServeHTTP(w, r, region, wafAllow)
		if w.Code != 400 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if w := wafCall(store, "ListWebACLs", `{"Scope":"REGIONAL","Limit":5}`, wafAllow); w.Code != 200 {
		t.Fatal("bounded list refused", w.Code, w.Body.String())
	}
}
