package catalog

import (
	"strings"
	"testing"
	"time"
)

// composeAPI combines wrapped requests with full-name body IDs, and
// full-resource replacement with wrapped and query update masks.
func composeAPI(t *testing.T) (*API, *Model) {
	t.Helper()
	root := "v1/projects/{projectsId}/locations/{locationsId}"
	gadget := root + "/gadgets/{gadgetsId}"
	gizmo := root + "/gizmos/{gizmosId}"
	methods := map[string]*Method{
		"projects.locations.gadgets.create": {HTTPMethod: "POST", FlatPath: root + "/gadgets", Request: "CreateGadgetRequest", Response: "Operation"},
		"projects.locations.gadgets.get":    {HTTPMethod: "GET", FlatPath: gadget, Response: "Gadget"},
		"projects.locations.gadgets.patch":  {HTTPMethod: "PATCH", FlatPath: gadget, Request: "UpdateGadgetRequest", Response: "Gadget"},
		"projects.locations.gizmos.create":  {HTTPMethod: "POST", FlatPath: root + "/gizmos", Request: "Gizmo", Response: "Gizmo"},
		"projects.locations.gizmos.patch":   {HTTPMethod: "PATCH", FlatPath: gizmo, Request: "Gizmo", Response: "Gizmo", Parameters: map[string]Parameter{"updateMask": {Type: "string", Location: "query"}}},
	}
	routes := map[string]Route{}
	for id, m := range methods {
		routes[id] = Route{HTTPMethod: m.HTTPMethod, FlatPath: m.FlatPath}
	}
	model := &Model{Key: "wrap.v1", Name: "wrap", Version: "v1", Title: "Wrapped", Source: Source{Package: "google-api-python-client", SHA256: "test", License: "Apache-2.0"},
		Routes: routes, Methods: methods, Schemas: map[string]*Property{
			"Gadget": {Type: "object", Properties: map[string]*Property{"name": {Type: "string"}, "size": {Type: "integer", Required: true}, "note": {Type: "string"},
				"createTime": {Type: "string", ReadOnly: true}}},
			"CreateGadgetRequest": {Type: "object", Properties: map[string]*Property{"gadgetId": {Type: "string"}, "gadget": {Ref: "Gadget"}}},
			"UpdateGadgetRequest": {Type: "object", Properties: map[string]*Property{"gadget": {Ref: "Gadget", Required: true}, "updateMask": {Type: "string"}}},
			"Gizmo":               {Type: "object", Properties: map[string]*Property{"name": {Type: "string", ReadOnly: true}, "id": {Type: "string"}, "note": {Type: "string"}}},
			"Operation":           {Type: "object", Properties: map[string]*Property{"name": {Type: "string"}, "done": {Type: "boolean"}, "response": {Type: "object"}}},
		}}
	if err := validateModel(model); err != nil {
		t.Fatal(err)
	}
	api := &API{Key: "wrap.v1", Title: "Wrapped", Issues: []int{1}, Scope: "location", TypeURL: "type.googleapis.com/wrap.v1.", Bounds: []string{"test"},
		Collections: []*Collection{
			{ID: "gadgets", Schema: "Gadget", Message: "type.googleapis.com/full.Gadget", IDField: "name", FullName: true, Permission: "wrap.gadgets",
				Generate: map[string]any{"createTime": "$now"}},
			{ID: "gizmos", Schema: "Gizmo", IDField: "id", Permission: "wrap.gizmos"},
		},
		Bindings: []Binding{
			{Method: "projects.locations.gadgets.create", Kind: Create, Collection: "gadgets", Unwrap: func(body map[string]any) (map[string]any, string, error) {
				gadget, _ := body["gadget"].(map[string]any)
				id, _ := body["gadgetId"].(string)
				return gadget, id, nil
			}},
			{Method: "projects.locations.gadgets.get", Kind: Get, Collection: "gadgets"},
			{Method: "projects.locations.gadgets.patch", Kind: Replace, Collection: "gadgets", Unwrap: func(body map[string]any) (map[string]any, string, error) {
				gadget, _ := body["gadget"].(map[string]any)
				mask, _ := body["updateMask"].(string)
				return gadget, mask, nil
			}},
			{Method: "projects.locations.gizmos.create", Kind: Create, Collection: "gizmos"},
			{Method: "projects.locations.gizmos.patch", Kind: Replace, Collection: "gizmos"},
		}}
	return api, model
}

// An Unwrap key names the new resource; without one the full name in the
// unwrapped resource does. A replacement takes no update mask, whether
// wrapped or in the query, and checks the resource schema's required fields.
func TestWrappedFullNamesAndMasklessReplacements(t *testing.T) {
	h := newHarness(t)
	api, model := composeAPI(t)
	b, err := bindAPI(api, model)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	base := "/wrap/v1/projects/tenkacloud-local/locations/us-central1"
	full := "projects/tenkacloud-local/locations/us-central1/gadgets/g1"
	expect(t, h.call("owner", "POST", base+"/gadgets", map[string]any{"gadget": map[string]any{"size": 1}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/gadgets", map[string]any{"gadget": map[string]any{"size": 1, "name": "projects/tenkacloud-local/locations/asia-northeast1/gadgets/g1"}}), 400, "INVALID_ARGUMENT")
	created := h.call("owner", "POST", base+"/gadgets", map[string]any{"gadget": map[string]any{"size": 1, "note": "n", "name": "projects/100000000001/locations/us-central1/gadgets/g1"}})
	expect(t, created, 200, "")
	response := created.body["response"].(map[string]any)
	if response["@type"] != "type.googleapis.com/full.Gadget" || response["name"] != full {
		t.Fatalf("wrapped full-name create %v", created.body)
	}
	keyed := h.call("owner", "POST", base+"/gadgets", map[string]any{"gadgetId": "g2", "gadget": map[string]any{"size": 1}})
	expect(t, keyed, 200, "")
	if keyed.body["response"].(map[string]any)["name"] != "projects/tenkacloud-local/locations/us-central1/gadgets/g2" {
		t.Fatalf("an Unwrap key names the resource: %v", keyed.body)
	}
	expect(t, h.call("owner", "POST", base+"/gadgets", map[string]any{"gadgetId": "Bad_ID", "gadget": map[string]any{"size": 1}}), 400, "INVALID_ARGUMENT")

	expect(t, h.call("owner", "PATCH", base+"/gadgets/g1", map[string]any{"gadget": map[string]any{"size": 2}, "updateMask": "size"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PATCH", base+"/gadgets/g1", map[string]any{"gadget": map[string]any{"size": 2, "name": "projects/tenkacloud-local/locations/us-central1/gadgets/g2"}}), 400, "INVALID_ARGUMENT")
	if missing := h.call("owner", "PATCH", base+"/gadgets/g1", map[string]any{"gadget": map[string]any{"note": "x"}}); missing.status != 400 || !strings.Contains(missing.raw, "'size'") {
		t.Fatalf("a wrapped replacement checks the resource schema: %s", missing.raw)
	}
	h.now = h.now.Add(time.Minute)
	replaced := h.call("owner", "PATCH", base+"/gadgets/g1", map[string]any{"gadget": map[string]any{"size": 3, "name": "projects/100000000001/locations/us-central1/gadgets/g1"}})
	expect(t, replaced, 200, "")
	if replaced.body["size"] != float64(3) || replaced.body["note"] != nil || replaced.body["name"] != full || replaced.body["createTime"] != response["createTime"] {
		t.Fatalf("wrapped replacement %v", replaced.body)
	}

	expect(t, h.call("owner", "POST", base+"/gizmos", map[string]any{"id": "z1", "note": "keep?"}), 200, "")
	expect(t, h.call("owner", "PATCH", base+"/gizmos/z1?updateMask=note", map[string]any{"note": "x"}), 400, "INVALID_ARGUMENT")
	gizmo := h.call("owner", "PATCH", base+"/gizmos/z1", map[string]any{})
	expect(t, gizmo, 200, "")
	if gizmo.body["note"] != nil || gizmo.body["id"] != "z1" {
		t.Fatalf("query-mask replacement %v", gizmo.body)
	}

	for _, broken := range []func(*Collection){
		func(c *Collection) { c.IDField = "" },
		func(c *Collection) { c.ShortName = true },
	} {
		api, model := composeAPI(t)
		broken(api.Collections[0])
		if _, err := bindAPI(api, model); err == nil {
			t.Fatal("a full name needs a body ID field and a full stored name")
		}
	}
}
