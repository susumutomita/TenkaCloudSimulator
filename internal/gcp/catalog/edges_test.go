package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestBodiesAreValidatedAgainstDiscoverySchemas(t *testing.T) {
	h := newHarness(t)
	base := map[string]any{"networkConfig": map[string]any{"network": "n"}}
	with := func(key string, value any) map[string]any {
		out := map[string]any{"networkConfig": map[string]any{"network": "n"}}
		out[key] = value
		return out
	}
	bad := map[string]map[string]any{
		"unknown nested":   {"networkConfig": map[string]any{"network": "n", "zzz": 1}},
		"enum":             with("databaseVersion", "POSTGRES_1"),
		"string type":      with("displayName", 3),
		"map value":        with("labels", map[string]any{"a": 1}),
		"null":             with("displayName", nil),
		"object type":      with("networkConfig", "x"),
		"array type":       {"networkConfig": map[string]any{"network": "n"}, "automatedBackupPolicy": map[string]any{"weeklySchedule": map[string]any{"startTimes": "x"}}},
		"boolean type":     {"networkConfig": map[string]any{"network": "n"}, "automatedBackupPolicy": map[string]any{"enabled": "yes"}},
		"duration":         {"networkConfig": map[string]any{"network": "n"}, "automatedBackupPolicy": map[string]any{"backupWindow": "1h"}},
		"integer":          {"networkConfig": map[string]any{"network": "n"}, "automatedBackupPolicy": map[string]any{"weeklySchedule": map[string]any{"startTimes": []any{map[string]any{"hours": "x"}}}}},
		"datetime":         {"networkConfig": map[string]any{"network": "n"}, "continuousBackupInfo": "x", "maintenanceUpdatePolicy": map[string]any{"denyMaintenancePeriods": []any{map[string]any{"startDate": "x"}}}},
		"missing network":  {"displayName": "x"},
		"required missing": nil,
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			if body == nil {
				expect(t, h.call("owner", "POST", loc+"/clusters/c/instances?instanceId=i", map[string]any{}), 404, "")
				return
			}
			expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=bad", body), 400, "INVALID_ARGUMENT")
		})
	}
	ok := h.call("owner", "POST", loc+"/clusters?clusterId=ok", map[string]any{"networkConfig": map[string]any{"network": "n"},
		"automatedBackupPolicy": map[string]any{"enabled": true, "backupWindow": "3600s", "weeklySchedule": map[string]any{"startTimes": []any{map[string]any{"hours": 2}}}}, "displayName": "OK"})
	expect(t, ok, 200, "")
	_ = base
	expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=x2", "not an object"), 400, "INVALID_ARGUMENT")
	req := httptest.NewRequest("POST", loc+"/clusters?clusterId=x3", strings.NewReader("{bad"))
	req = req.WithContext(context.WithValue(req.Context(), subjectKey{}, "owner"))
	recorder := httptest.NewRecorder()
	h.engine.ServeHTTP(recorder, req)
	if recorder.Code != 400 {
		t.Fatalf("malformed JSON %d", recorder.Code)
	}
	expect(t, h.call("owner", "GET", loc+"/clusters/ok?view=CLUSTER_VIEW_BASIC", nil), 200, "")
	expect(t, h.call("owner", "GET", loc+"/clusters/ok?view=NOT_A_VIEW", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", loc+"/clusters?pageSize=x", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "DELETE", loc+"/clusters/ok?force=maybe", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", loc+"/clusters/ok", map[string]any{"x": 1}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", loc+"/clusters?pageSize=1&pageSize=2", nil), 400, "INVALID_ARGUMENT")
}

func TestValueConversionsFollowTheProtoJSONMapping(t *testing.T) {
	d := &decoder{model: &Model{Schemas: map[string]*Property{}}}
	cases := []struct {
		p    *Property
		in   any
		want any
		ok   bool
	}{
		{&Property{Type: "string", Format: "int64"}, "9007199254740993", "9007199254740993", true},
		{&Property{Type: "string", Format: "int64"}, json.Number("12"), "12", true},
		{&Property{Type: "string", Format: "uint64"}, "-1", nil, false},
		{&Property{Type: "integer", Format: "uint32"}, json.Number("7"), int64(7), true},
		{&Property{Type: "integer", Format: "int32"}, json.Number("99999999999"), nil, false},
		{&Property{Type: "integer"}, true, nil, false},
		{&Property{Type: "string", Format: "uint64"}, "18", "18", true},
		{&Property{Type: "number"}, json.Number("1.5"), 1.5, true},
		{&Property{Type: "number"}, "NaN", "NaN", true},
		{&Property{Type: "number"}, "nan", nil, false},
		{&Property{Type: "number"}, json.Number("1e999"), nil, false},
		{&Property{Type: "string", Format: "byte"}, "aGk=", "aGk=", true},
		{&Property{Type: "string", Format: "byte"}, "aGk-", "aGk-", true},
		{&Property{Type: "string", Format: "byte"}, "%%", nil, false},
		{&Property{Type: "string", Format: "google-datetime"}, "2026-11-06T12:00:00+09:00", "2026-11-06T03:00:00Z", true},
		{&Property{Type: "string"}, "\xff", nil, false},
		{&Property{Type: "any"}, map[string]any{"a": []any{json.Number("1")}}, nil, true},
		{&Property{Type: "object"}, map[string]any{"@type": "x"}, nil, true},
		{&Property{Type: "mystery"}, "x", nil, false},
	}
	for i, c := range cases {
		got, err := d.decode(c.p, c.in, "f", 0)
		if (err == nil) != c.ok || c.want != nil && got != c.want {
			t.Fatalf("case %d: %v %v", i, got, err)
		}
	}
	if _, err := scalarParameter(Parameter{Type: "integer"}, "x"); err == nil {
		t.Fatal("integer parameter")
	}
	if v, _ := scalarParameter(Parameter{Type: "string", Enum: []string{"A"}}, "A"); v != "A" {
		t.Fatal("enum parameter")
	}
	deep := map[string]any{}
	cursor := deep
	for i := 0; i < maxDepth+2; i++ {
		next := map[string]any{}
		cursor["x"] = next
		cursor = next
	}
	if _, err := d.decode(&Property{Type: "any"}, deep, "f", 0); err == nil {
		t.Fatal("depth bound")
	}
	if (&status{"INTERNAL", "m"}).Error() != "m" {
		t.Fatal("status error text")
	}
}

func TestIdempotencyValidateOnlyEtagsAndPatches(t *testing.T) {
	h := newHarness(t)
	body := map[string]any{"networkConfig": map[string]any{"network": "n"}}
	first := h.call("owner", "POST", loc+"/clusters?clusterId=idem&requestId=r1", body)
	replay := h.call("owner", "POST", loc+"/clusters?clusterId=idem&requestId=r1", body)
	expect(t, replay, 200, "")
	if first.body["name"] != replay.body["name"] {
		t.Fatalf("requestId replay must return the original operation: %v %v", first.body, replay.body)
	}
	expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=other&requestId=r1", body), 400, "INVALID_ARGUMENT")
	validated := h.call("owner", "POST", loc+"/clusters?clusterId=dry&validateOnly=true", body)
	expect(t, validated, 200, "")
	expect(t, h.call("owner", "GET", loc+"/clusters/dry", nil), 404, "NOT_FOUND")
	cluster := h.call("owner", "GET", loc+"/clusters/idem", nil).body
	expect(t, h.call("owner", "PATCH", loc+"/clusters/idem", map[string]any{"etag": "stale", "displayName": "x"}), 409, "ABORTED")
	expect(t, h.call("owner", "PATCH", loc+"/clusters/idem", map[string]any{"etag": cluster["etag"], "displayName": "Renamed", "labels": map[string]any{"a": "b"}}), 200, "")
	expect(t, h.call("owner", "PATCH", loc+"/clusters/idem?updateMask=labels", map[string]any{}), 200, "")
	got := h.call("owner", "GET", loc+"/clusters/idem", nil).body
	if got["displayName"] != "Renamed" || got["labels"] != nil || got["etag"] == cluster["etag"] {
		t.Fatalf("patch semantics %v", got)
	}
	expect(t, h.call("owner", "PATCH", loc+"/clusters/idem?allowMissing=true", map[string]any{"displayName": "x"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PATCH", loc+"/clusters/idem?validateOnly=true", map[string]any{"displayName": "Dry"}), 200, "")
	if h.call("owner", "GET", loc+"/clusters/idem", nil).body["displayName"] != "Renamed" {
		t.Fatal("validateOnly patch must not persist")
	}
	expect(t, h.call("owner", "DELETE", loc+"/clusters/idem?etag=stale", nil), 409, "ABORTED")
	expect(t, h.call("owner", "DELETE", loc+"/clusters/idem?validateOnly=true", nil), 200, "")
	expect(t, h.call("owner", "GET", loc+"/clusters/idem", nil), 200, "")
	expect(t, h.call("owner", "GET", loc+"/operations?filter=done", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", "/alloydb/v1/projects/tenkacloud-local/locations/us-central1/operations/operation-missing", nil), 404, "NOT_FOUND")
	for i := 0; i < 3; i++ {
		h.call("owner", "POST", loc+"/clusters/idem/users?userId=u"+string(rune('a'+i)), map[string]any{})
	}
	page := h.call("owner", "GET", loc+"/clusters/idem/users?pageSize=2", nil)
	if len(page.body["users"].([]any)) != 2 || page.body["nextPageToken"] == nil {
		t.Fatalf("first page %v", page.body)
	}
	next := h.call("owner", "GET", loc+"/clusters/idem/users?pageToken="+page.body["nextPageToken"].(string), nil)
	if len(next.body["users"].([]any)) != 1 {
		t.Fatalf("second page %v", next.body)
	}
	expect(t, h.call("owner", "GET", loc+"/clusters/idem/users?pageToken=garbage", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", loc+"/clusters/idem/users?pageSize=-1", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", loc+"/clusters?pageToken="+page.body["nextPageToken"].(string), nil), 400, "INVALID_ARGUMENT")
}

func TestSpecificationsAndModelsFailClosed(t *testing.T) {
	model, err := loadModel("alloydb.v1")
	if err != nil {
		t.Fatal(err)
	}
	clone := func() *API {
		c := *alloyDB
		c.Collections = append([]*Collection(nil), alloyDB.Collections...)
		c.Bindings = append([]Binding(nil), alloyDB.Bindings...)
		return &c
	}
	broken := map[string]func(*API){
		"title":      func(a *API) { a.Title = "" },
		"scope":      func(a *API) { a.Scope = "region" },
		"type url":   func(a *API) { a.TypeURL = "x" },
		"schema":     func(a *API) { c := *a.Collections[0]; c.Schema = "Nope"; a.Collections[0] = &c },
		"parent":     func(a *API) { c := *a.Collections[1]; c.Parent = "nope"; a.Collections[1] = &c },
		"method":     func(a *API) { a.Bindings = append(a.Bindings, Binding{Method: "projects.locations.nope", Kind: Get}) },
		"duplicate":  func(a *API) { a.Bindings = append(a.Bindings, a.Bindings[0]) },
		"collection": func(a *API) { a.Bindings[0].Collection = "nope" },
		"kind":       func(a *API) { a.Bindings[0].Kind = "destroy" },
		"unbound":    func(a *API) { a.Bindings = a.Bindings[1:] },
		"permission": func(a *API) { c := *a.Collections[0]; c.Permission = ""; a.Collections[0] = &c },
	}
	for name, mutate := range broken {
		api := clone()
		mutate(api)
		if _, err := bindAPI(api, model); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	bad := *model
	bad.Source.SHA256 = ""
	if validateModel(&bad) == nil {
		t.Fatal("missing provenance")
	}
	bad = *model
	bad.Schemas = map[string]*Property{"X": {Ref: "Missing"}}
	bad.Methods = map[string]*Method{}
	if validateModel(&bad) == nil {
		t.Fatal("dangling schema reference")
	}
	bad = *model
	bad.Methods = map[string]*Method{"m": {Request: "Missing"}}
	if validateModel(&bad) == nil {
		t.Fatal("dangling method reference")
	}
	if _, err := loadModel("missing.v1"); err == nil {
		t.Fatal("missing model")
	}
	if _, err := New(nil, Config{}, nil); err == nil {
		t.Fatal("incomplete config")
	}
	h := newHarness(t)
	if err := h.engine.add(h.engine.order[0]); err == nil {
		t.Fatal("duplicate API registration")
	}
	if _, err := newStore(nil); err == nil {
		t.Fatal("nil database")
	}
	if len(embeddedModels()) != len(APIs()) {
		t.Fatal("embedded models must match reviewed APIs")
	}
}

func TestStorageFaultsAreInternalAndLeaveNoPartialState(t *testing.T) {
	h := newHarness(t)
	db := h.engine.store.db
	h.call("owner", "POST", loc+"/clusters?clusterId=keep", map[string]any{"network": "n"})
	exec := func(statement string) {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE TRIGGER f BEFORE INSERT ON gcp_catalog_operations BEGIN SELECT RAISE(ABORT, 'x'); END")
	expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=lost", map[string]any{"network": "n"}), 500, "INTERNAL")
	expect(t, h.call("owner", "GET", loc+"/clusters/lost", nil), 404, "NOT_FOUND")
	exec("DROP TRIGGER f")
	exec("CREATE TRIGGER g BEFORE UPDATE ON gcp_catalog_resources BEGIN SELECT RAISE(IGNORE); END")
	expect(t, h.call("owner", "PATCH", loc+"/clusters/keep", map[string]any{"displayName": "x"}), 409, "ABORTED")
	exec("DROP TRIGGER g")
	exec("UPDATE gcp_catalog_resources SET document='nope'")
	expect(t, h.call("owner", "GET", loc+"/clusters/keep", nil), 500, "INTERNAL")
	expect(t, h.call("owner", "GET", loc+"/clusters", nil), 500, "INTERNAL")
	exec("DROP TABLE gcp_catalog_operations")
	expect(t, h.call("owner", "GET", loc+"/operations", nil), 500, "INTERNAL")
	exec("DROP TABLE gcp_catalog_requests")
	expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=r&requestId=x", map[string]any{"network": "n"}), 500, "INTERNAL")
	exec("DROP TABLE gcp_catalog_sequence")
	exec("DROP TABLE gcp_catalog_resources")
	expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=z", map[string]any{"network": "n"}), 500, "INTERNAL")
	expect(t, h.call("owner", "DELETE", loc+"/clusters/keep", nil), 500, "INTERNAL")
	var unused sql.NullString
	_ = unused
}

func TestDescriptorsAndReferenceCoverReviewedMethods(t *testing.T) {
	h := newHarness(t)
	d := h.engine.Describe()
	if len(d) != len(APIs()) || d[0].Endpoint != "/_simulator/providers/gcp/alloydb/" || len(d[0].Collections) != 3 {
		t.Fatalf("descriptors %+v", d)
	}
	clusters := d[0].Collections[0]
	if clusters.Methods["list"].Items != "clusters" || clusters.Methods["create"].HTTPMethod != "POST" || len(clusters.Fields) == 0 {
		t.Fatalf("collection descriptor %+v", clusters)
	}
	doc := h.engine.OpenAPI()["alloydb.v1"]
	paths := doc["paths"].(map[string]any)
	if _, ok := paths["/v1/projects/{projectsId}/locations/{locationsId}/clusters"]; !ok || doc["openapi"] != "3.1.0" {
		t.Fatalf("reference paths %v", paths)
	}
	if len(withoutUnspecified([]string{"A_UNSPECIFIED", "B"})) != 1 {
		t.Fatal("unspecified enum values are not offered")
	}
}
