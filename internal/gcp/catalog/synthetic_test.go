package catalog

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// syntheticAPI uses a service path prefix, project scope, server-generated
// IDs and every generator.
func syntheticAPI(t *testing.T) (*API, *Model) {
	model := &Model{Key: "synth.v1", Name: "synth", Version: "v1", Title: "Synthetic", ServicePath: "synth/",
		Source: Source{Package: "google-api-python-client", SHA256: "test", License: "Apache-2.0"},
		Routes: map[string]Route{
			"projects.widgets.create": {HTTPMethod: "POST", FlatPath: "v1/projects/{projectsId}/widgets"},
			"projects.widgets.get":    {HTTPMethod: "GET", FlatPath: "v1/projects/{projectsId}/widgets/{widgetsId}"},
		},
		Methods: map[string]*Method{
			"projects.widgets.create": {HTTPMethod: "POST", FlatPath: "v1/projects/{projectsId}/widgets", Request: "Widget", Response: "Widget"},
			"projects.widgets.get":    {HTTPMethod: "GET", FlatPath: "v1/projects/{projectsId}/widgets/{widgetsId}", Response: "Widget"},
		},
		Schemas: map[string]*Property{"Widget": {Type: "object", Properties: map[string]*Property{
			"name": {Type: "string"}, "id": {Type: "string", ReadOnly: true}, "project": {Type: "string", ReadOnly: true}, "location": {Type: "string", ReadOnly: true},
			"parent": {Type: "string", ReadOnly: true}, "mystery": {Type: "string", ReadOnly: true}, "blob": {Type: "string"}, "note": {Type: "string"}}}},
	}
	if err := validateModel(model); err != nil {
		t.Fatal(err)
	}
	api := &API{Key: "synth.v1", Title: "Synthetic", Issues: []int{1}, Scope: "project", TypeURL: "type.googleapis.com/test.", Bounds: []string{"test"},
		Collections: []*Collection{{ID: "widgets", Schema: "Widget", Permission: "synth.widgets",
			Generate: map[string]any{"id": "$id", "project": "$project", "location": "$location", "parent": "$parent", "mystery": "$unknown"}}},
		Bindings: []Binding{{Method: "projects.widgets.create", Kind: Create, Collection: "widgets"}, {Method: "projects.widgets.get", Kind: Get, Collection: "widgets"}}}
	return api, model
}

func TestServicePathsServerIDsAndGenerators(t *testing.T) {
	h := newHarness(t)
	api, model := syntheticAPI(t)
	b, err := bindAPI(api, model)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	if !h.engine.Owns(httptest.NewRequest("GET", "/synth/synth/v1/projects/p/widgets", nil)) || h.engine.Owns(httptest.NewRequest("GET", "/storage/v1/b", nil)) {
		t.Fatal("ownership follows registered API names")
	}
	created := h.call("owner", "POST", "/synth/synth/v1/projects/tenkacloud-local/widgets", map[string]any{"id": "ignored"})
	expect(t, created, 200, "")
	id := created.body["id"].(string)
	if len(id) != 16 || created.body["project"] != "tenkacloud-local" || created.body["location"] != "" || created.body["parent"] != "projects/tenkacloud-local" || created.body["mystery"] != nil {
		t.Fatalf("generated fields %v", created.body)
	}
	expect(t, h.call("owner", "GET", "/synth/synth/v1/projects/tenkacloud-local/widgets/"+id, nil), 200, "")
	expect(t, h.call("owner", "GET", "/synth/other/v1/projects/tenkacloud-local/widgets/"+id, nil), 404, "NOT_FOUND")
	expect(t, h.call("owner", "GET", "/synth", nil), 404, "NOT_FOUND")
	expect(t, h.call("owner", "GET", "/synth/synth/v1/projects/tenkacloud-local/widgets/%2F", nil), 404, "NOT_FOUND")
	expect(t, h.call("owner", "GET", "/synth/synth/v1/widgets", nil), 404, "NOT_FOUND")
	expect(t, h.call("owner", "POST", "/synth/synth/v1/projects/tenkacloud-local/widgets", map[string]any{"blob": strings.Repeat("x", 40<<10), "note": strings.Repeat("y", 40<<10)}), 400, "INVALID_ARGUMENT")
	recorder := httptest.NewRecorder()
	fail(recorder, errString("boom"))
	if recorder.Code != 500 {
		t.Fatal("unknown errors are INTERNAL")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// Body-supplied IDs, custom ID rules and short names serve APIs such as
// Cloud Logging whose create bodies carry the ID in "name".
func TestBodyIDsPatternsAndShortNames(t *testing.T) {
	h := newHarness(t)
	api, model := syntheticAPI(t)
	c := api.Collections[0]
	c.IDField, c.ShortName, c.IDPattern = "name", true, regexp.MustCompile(`^[A-Za-z0-9_]{1,10}$`)
	b, err := bindAPI(api, model)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	base := "/synth/synth/v1/projects/tenkacloud-local/widgets"
	expect(t, h.call("owner", "POST", base, map[string]any{"note": "x"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base, map[string]any{"name": "has-dash"}), 400, "INVALID_ARGUMENT")
	created := h.call("owner", "POST", base, map[string]any{"name": "My_Sink"})
	expect(t, created, 200, "")
	if created.body["name"] != "My_Sink" || created.body["id"] != "My_Sink" {
		t.Fatalf("short name %v", created.body)
	}
	got := h.call("owner", "GET", base+"/My_Sink", nil)
	expect(t, got, 200, "")
	if got.body["name"] != "My_Sink" {
		t.Fatalf("stored short name %v", got.body)
	}
	expect(t, h.call("owner", "POST", base, map[string]any{"name": "My_Sink"}), 409, "ALREADY_EXISTS")
	for _, broken := range []func(*Collection){
		func(c *Collection) { c.IDParam = "widgetId" },
		func(c *Collection) { c.IDParam, c.IDField = "", "missing" },
	} {
		api, model := syntheticAPI(t)
		api.Collections[0].IDField = "name"
		broken(api.Collections[0])
		if _, err := bindAPI(api, model); err == nil {
			t.Fatal("an ambiguous or unknown ID source must be refused")
		}
	}
}

// Full resource names in create bodies, protobuf message names that differ
// from discovery schema names and deletes that return the deleted resource
// serve APIs such as Cloud Composer, Dataplex and BigLake.
func TestFullNameIDsMessageTypesAndDeletedResources(t *testing.T) {
	h := newHarness(t)
	widget := &Property{Type: "object", Properties: map[string]*Property{"name": {Type: "string"}, "note": {Type: "string"}}}
	operation := &Property{Type: "object", Properties: map[string]*Property{"name": {Type: "string"}, "done": {Type: "boolean"}, "response": {Type: "object"}}}
	flat := "v1/projects/{projectsId}/locations/{locationsId}/widgets"
	model := &Model{Key: "synth.v1", Name: "synth", Version: "v1", Title: "Synthetic",
		Source: Source{Package: "google-api-python-client", SHA256: "test", License: "Apache-2.0"},
		Routes: map[string]Route{"projects.locations.widgets.create": {HTTPMethod: "POST", FlatPath: flat}, "projects.locations.widgets.delete": {HTTPMethod: "DELETE", FlatPath: flat + "/{widgetsId}"}},
		Methods: map[string]*Method{
			"projects.locations.widgets.create": {HTTPMethod: "POST", FlatPath: flat, Request: "SynthV1Widget", Response: "Operation"},
			"projects.locations.widgets.delete": {HTTPMethod: "DELETE", FlatPath: flat + "/{widgetsId}", Response: "SynthV1Widget"},
		},
		Schemas: map[string]*Property{"SynthV1Widget": widget, "Operation": operation}}
	if err := validateModel(model); err != nil {
		t.Fatal(err)
	}
	api := &API{Key: "synth.v1", Title: "Synthetic", Issues: []int{1}, Scope: "location", TypeURL: "type.googleapis.com/test.v1.", Bounds: []string{"test"},
		Collections: []*Collection{{ID: "widgets", Schema: "SynthV1Widget", Type: "Widget", IDField: "name", FullName: true, Permission: "synth.widgets"}},
		Bindings:    []Binding{{Method: "projects.locations.widgets.create", Kind: Create, Collection: "widgets"}, {Method: "projects.locations.widgets.delete", Kind: Delete, Collection: "widgets"}}}
	b, err := bindAPI(api, model)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	base := "/synth/v1/projects/tenkacloud-local/locations/us-central1/widgets"
	expect(t, h.call("owner", "POST", base, map[string]any{"name": "w1"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base, map[string]any{"name": "projects/tenkacloud-local/locations/asia-northeast1/widgets/w1"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base, map[string]any{"name": "projects/tenkacloud-local/locations/us-central1/widgets/Bad/x"}), 400, "INVALID_ARGUMENT")
	created := h.call("owner", "POST", base, map[string]any{"name": "projects/100000000001/locations/us-central1/widgets/w1", "note": "n"})
	expect(t, created, 200, "")
	response := created.body["response"].(map[string]any)
	if response["@type"] != "type.googleapis.com/test.v1.Widget" || response["name"] != "projects/tenkacloud-local/locations/us-central1/widgets/w1" {
		t.Fatalf("operation response %v", created.body)
	}
	deleted := h.call("owner", "DELETE", base+"/w1", nil)
	expect(t, deleted, 200, "")
	if deleted.body["name"] != "projects/tenkacloud-local/locations/us-central1/widgets/w1" || deleted.body["note"] != "n" {
		t.Fatalf("delete must return the deleted resource: %v", deleted.body)
	}
	expect(t, h.call("owner", "DELETE", base+"/w1", nil), 404, "NOT_FOUND")
}

// Resource names are scoped by API: two services may each own
// projects/p/.../widgets/w, as Looker and Data Fusion both own instances.
func TestResourceNamesAreScopedByAPI(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"synth", "other"} {
		api, model := syntheticAPI(t)
		api.Key, model.Key, model.Name, model.ServicePath = name+".v1", name+".v1", name, ""
		api.Collections[0].IDField = "name"
		b, err := bindAPI(api, model)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.engine.add(b); err != nil {
			t.Fatal(err)
		}
	}
	synth, other := "/synth/v1/projects/tenkacloud-local/widgets", "/other/v1/projects/tenkacloud-local/widgets"
	expect(t, h.call("owner", "POST", synth, map[string]any{"name": "w", "note": "synth"}), 200, "")
	expect(t, h.call("owner", "GET", other+"/w", nil), 404, "NOT_FOUND")
	expect(t, h.call("owner", "POST", other, map[string]any{"name": "w", "note": "other"}), 200, "")
	if got := h.call("owner", "GET", synth+"/w", nil); got.body["note"] != "synth" {
		t.Fatalf("synth resource %v", got.body)
	}
	if got := h.call("owner", "GET", other+"/w", nil); got.body["note"] != "other" {
		t.Fatalf("other resource %v", got.body)
	}
}

// Full-resource updates (PUT) keep the ID and output fields and clear the
// writable fields the body omits, as Dataproc autoscaling policies do.
func TestReplaceUpdatesKeepIdentityAndClearOmittedFields(t *testing.T) {
	h := newHarness(t)
	widget := &Property{Type: "object", Properties: map[string]*Property{"name": {Type: "string", ReadOnly: true}, "id": {Type: "string", Required: true},
		"size": {Type: "integer", Required: true}, "note": {Type: "string"}, "etag": {Type: "string"}, "createTime": {Type: "string", ReadOnly: true}, "updateTime": {Type: "string", ReadOnly: true}}}
	flat := "v1/projects/{projectsId}/locations/{locationsId}/widgets"
	methods := map[string]*Method{
		"projects.locations.widgets.create": {HTTPMethod: "POST", FlatPath: flat, Request: "Widget", Response: "Widget"},
		"projects.locations.widgets.update": {HTTPMethod: "PUT", FlatPath: flat + "/{widgetsId}", Request: "Widget", Response: "Widget"},
	}
	model := &Model{Key: "synth.v1", Name: "synth", Version: "v1", Title: "Synthetic", Source: Source{Package: "google-api-python-client", SHA256: "test", License: "Apache-2.0"},
		Routes: map[string]Route{}, Methods: methods, Schemas: map[string]*Property{"Widget": widget}}
	for id, m := range methods {
		model.Routes[id] = Route{HTTPMethod: m.HTTPMethod, FlatPath: m.FlatPath}
	}
	if err := validateModel(model); err != nil {
		t.Fatal(err)
	}
	api := &API{Key: "synth.v1", Title: "Synthetic", Issues: []int{1}, Scope: "location", TypeURL: "type.googleapis.com/test.v1.", Bounds: []string{"test"},
		Collections: []*Collection{{ID: "widgets", Schema: "Widget", IDField: "id", Permission: "synth.widgets", Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "etag": "$etag"}}},
		Bindings:    []Binding{{Method: "projects.locations.widgets.create", Kind: Create, Collection: "widgets"}, {Method: "projects.locations.widgets.update", Kind: Replace, Collection: "widgets"}}}
	b, err := bindAPI(api, model)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	base := "/synth/v1/projects/tenkacloud-local/locations/us-central1/widgets"
	created := h.call("owner", "POST", base, map[string]any{"id": "w1", "size": 1, "note": "keep?"})
	expect(t, created, 200, "")
	expect(t, h.call("owner", "PUT", base+"/w1", map[string]any{"id": "w2", "size": 2}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PUT", base+"/w1", map[string]any{"id": "w1"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PUT", base+"/w1", map[string]any{"id": "w1", "size": 2, "etag": "stale"}), 409, "ABORTED")
	expect(t, h.call("owner", "PUT", base+"/missing", map[string]any{"id": "missing", "size": 2}), 404, "NOT_FOUND")
	h.now = h.now.Add(time.Minute)
	replaced := h.call("owner", "PUT", base+"/w1", map[string]any{"id": "w1", "size": 2, "etag": created.body["etag"]})
	expect(t, replaced, 200, "")
	got := replaced.body
	if got["size"] != float64(2) || got["note"] != nil || got["name"] != "projects/tenkacloud-local/locations/us-central1/widgets/w1" ||
		got["createTime"] != created.body["createTime"] || got["updateTime"] == created.body["updateTime"] || got["etag"] == created.body["etag"] {
		t.Fatalf("replace semantics %v (created %v)", got, created.body)
	}
}
