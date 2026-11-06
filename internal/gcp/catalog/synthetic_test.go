package catalog

import (
	"net/http/httptest"
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
