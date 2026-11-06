package catalog

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
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
