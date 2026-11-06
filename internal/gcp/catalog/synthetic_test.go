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

// referenceAPI exercises references, deletion protection, operations named
// under a resource and operation response type names.
func referenceAPI(t *testing.T) (*API, *Model) {
	t.Helper()
	pool := "v1/projects/{projectsId}/pools/{poolsId}"
	volume := "v1/projects/{projectsId}/volumes/{volumesId}"
	methods := map[string]*Method{
		"projects.pools.create":             {HTTPMethod: "POST", FlatPath: "v1/projects/{projectsId}/pools", Request: "SynthPool", Response: "Operation", Parameters: map[string]Parameter{"poolId": {Type: "string", Location: "query"}}},
		"projects.pools.patch":              {HTTPMethod: "PATCH", FlatPath: pool, Request: "SynthPool", Response: "Operation", Parameters: map[string]Parameter{"updateMask": {Type: "string", Location: "query"}}},
		"projects.pools.delete":             {HTTPMethod: "DELETE", FlatPath: pool, Response: "Operation", Parameters: map[string]Parameter{"ignoreReferences": {Type: "boolean", Location: "query"}}},
		"projects.pools.members.create":     {HTTPMethod: "POST", FlatPath: pool + "/members", Request: "CreateMemberRequest", Response: "Volume"},
		"projects.pools.members.patch":      {HTTPMethod: "PATCH", FlatPath: pool + "/members/{membersId}", Request: "UpdateMemberRequest", Response: "Volume"},
		"projects.pools.groups.tags.create": {HTTPMethod: "POST", FlatPath: pool + "/groups/{groupsId}/tags", Request: "Volume", Response: "Volume"},
		"projects.pools.groups.tags.list":   {HTTPMethod: "GET", FlatPath: pool + "/groups/{groupsId}/tags", Response: "ListTagsResponse"},
		"projects.pools.members.get":        {HTTPMethod: "GET", FlatPath: pool + "/members/{membersId}", Response: "Volume"},
		"projects.pools.operations.get":     {HTTPMethod: "GET", FlatPath: pool + "/operations/{operationsId}", Response: "Operation"},
		"projects.pools.operations.list":    {HTTPMethod: "GET", FlatPath: pool + "/operations", Response: "ListOperationsResponse"},
		"projects.volumes.create":           {HTTPMethod: "POST", FlatPath: "v1/projects/{projectsId}/volumes", Request: "Volume", Response: "Volume", Parameters: map[string]Parameter{"volumeId": {Type: "string", Location: "query"}}},
		"projects.volumes.patch":            {HTTPMethod: "PATCH", FlatPath: volume, Request: "Volume", Response: "Volume", Parameters: map[string]Parameter{"updateMask": {Type: "string", Location: "query"}}},
		"projects.volumes.delete":           {HTTPMethod: "DELETE", FlatPath: volume, Response: "Empty"},
	}
	routes := map[string]Route{}
	for id, m := range methods {
		routes[id] = Route{HTTPMethod: m.HTTPMethod, FlatPath: m.FlatPath}
	}
	model := &Model{Key: "refs.v1", Name: "refs", Version: "v1", Title: "References", Source: Source{Package: "google-api-python-client", SHA256: "test", License: "Apache-2.0"},
		Routes: routes, Methods: methods, Schemas: map[string]*Property{
			"SynthPool":              {Type: "object", Properties: map[string]*Property{"name": {Type: "string"}, "protected": {Type: "boolean"}, "note": {Type: "string"}}},
			"Volume":                 {Type: "object", Properties: map[string]*Property{"name": {Type: "string"}, "pool": {Type: "string"}, "origin": {Type: "string"}, "note": {Type: "string"}}},
			"Operation":              {Type: "object", Properties: map[string]*Property{"name": {Type: "string"}, "done": {Type: "boolean"}, "response": {Type: "object"}}},
			"ListOperationsResponse": {Type: "object", Properties: map[string]*Property{"operations": {Type: "array", Items: &Property{Ref: "Operation"}}}},
			"Empty":                  {Type: "object", Properties: map[string]*Property{}},
			"ListTagsResponse":       {Type: "object", Properties: map[string]*Property{"tags": {Type: "array", Items: &Property{Ref: "Volume"}}}},
			"CreateMemberRequest":    {Type: "object", Properties: map[string]*Property{"memberId": {Type: "string"}, "member": {Ref: "Volume"}}},
			"UpdateMemberRequest":    {Type: "object", Properties: map[string]*Property{"fieldMask": {Type: "string"}, "member": {Ref: "Volume"}}},
		}}
	if err := validateModel(model); err != nil {
		t.Fatal(err)
	}
	api := &API{Key: "refs.v1", Title: "References", Issues: []int{1}, Scope: "project", TypeURL: "type.googleapis.com/test.", Bounds: []string{"test"},
		Collections: []*Collection{
			{ID: "pools", Schema: "SynthPool", Message: "type.googleapis.com/other.Pool", IDParam: "poolId", Permission: "refs.pools", Operations: "pools", Cascade: true, Mutable: []string{"protected", "note"}, Protection: map[string]any{"protected": true}},
			{ID: "tags", Schema: "Volume", Parent: "pools", Implicit: "groups", Permission: "refs.tags"},
			{ID: "members", Schema: "Volume", Parent: "pools", Permission: "refs.members", Mutable: []string{"note"}, Protection: map[string]any{"note": "locked"}},
			{ID: "volumes", Schema: "Volume", IDParam: "volumeId", Permission: "refs.volumes", Mutable: []string{"origin", "note"},
				References: map[string]Reference{"pool": {Collection: "pools", Blocks: true, Override: "ignoreReferences"}, "origin": {Collection: "pools"}}},
		}}
	for id := range methods {
		kind, collection := Kind(id[strings.LastIndex(id, ".")+1:]), "volumes"
		if strings.HasPrefix(id, "projects.pools.") {
			collection = "pools"
		}
		if strings.HasPrefix(id, "projects.pools.members.") {
			collection = "members"
		}
		if strings.HasPrefix(id, "projects.pools.groups.tags.") {
			collection = "tags"
		}
		if strings.Contains(id, ".operations.") {
			kind, collection = OperationGet, ""
			if strings.HasSuffix(id, ".list") {
				kind = OperationList
			}
		}
		b := Binding{Method: id, Kind: kind, Collection: collection}
		switch id {
		case "projects.pools.members.create":
			b.Unwrap = func(body map[string]any) (map[string]any, string, error) {
				member, _ := body["member"].(map[string]any)
				id, _ := body["memberId"].(string)
				if id == "" {
					return nil, "", invalid("Field 'memberId' is required.")
				}
				return member, id, nil
			}
		case "projects.pools.members.patch":
			b.Unwrap = func(body map[string]any) (map[string]any, string, error) {
				member, _ := body["member"].(map[string]any)
				mask, _ := body["fieldMask"].(string)
				if mask == "" {
					return nil, "", invalid("Field 'fieldMask' is required.")
				}
				return member, mask, nil
			}
		case "projects.pools.operations.get":
			b.Permission = "refs.poolOperations.get"
		}
		api.Bindings = append(api.Bindings, b)
	}
	return api, model
}

func TestReferencesProtectionAndOperationAnchors(t *testing.T) {
	h := newHarness(t)
	api, model := referenceAPI(t)
	b, err := bindAPI(api, model)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	base := "/refs/v1/projects/tenkacloud-local"
	created := h.call("owner", "POST", base+"/pools?poolId=a", map[string]any{})
	expect(t, created, 200, "")
	if !strings.HasPrefix(created.body["name"].(string), "projects/tenkacloud-local/pools/a/operations/operation-") || created.body["response"].(map[string]any)["@type"] != "type.googleapis.com/other.Pool" {
		t.Fatalf("anchored operation %v", created.body)
	}
	expect(t, h.call("owner", "GET", "/refs/v1/"+created.body["name"].(string), nil), 200, "")
	expect(t, h.call("owner", "POST", base+"/pools?poolId=b", map[string]any{}), 200, "")
	if ops := h.call("owner", "GET", base+"/pools/a/operations", nil).body["operations"].([]any); len(ops) != 1 {
		t.Fatalf("operations under pool a %v", ops)
	}
	expect(t, h.call("owner", "POST", base+"/volumes?volumeId=lost", map[string]any{"pool": "projects/tenkacloud-local/pools/missing"}), 404, "NOT_FOUND")
	volume := h.call("owner", "POST", base+"/volumes?volumeId=v", map[string]any{"pool": "projects/100000000001/pools/a", "origin": "projects/tenkacloud-local/pools/b"})
	expect(t, volume, 200, "")
	if volume.body["pool"] != "projects/tenkacloud-local/pools/a" {
		t.Fatalf("references normalize project numbers %v", volume.body)
	}
	expect(t, h.call("owner", "POST", base+"/volumes?volumeId=w", map[string]any{"pool": "projects/tenkacloud-local/volumes/v"}), 404, "NOT_FOUND")
	expect(t, h.call("owner", "DELETE", base+"/pools/a", nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "DELETE", base+"/pools/b", nil), 200, "")
	expect(t, h.call("owner", "PATCH", base+"/volumes/v?updateMask=note", map[string]any{"note": "kept"}), 200, "")
	expect(t, h.call("owner", "PATCH", base+"/volumes/v?updateMask=origin", map[string]any{"origin": "projects/tenkacloud-local/pools/b"}), 404, "NOT_FOUND")
	expect(t, h.call("owner", "PATCH", base+"/pools/a?updateMask=protected", map[string]any{"protected": true}), 200, "")
	expect(t, h.call("owner", "DELETE", base+"/volumes/v", nil), 200, "")
	expect(t, h.call("owner", "DELETE", base+"/pools/a", nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "PATCH", base+"/pools/a?updateMask=protected", map[string]any{"protected": false}), 200, "")
	expect(t, h.call("owner", "DELETE", base+"/pools/a", nil), 200, "")
	// A delete method without a force parameter cascades, and its override
	// parameter lifts a blocking reference.
	h.call("owner", "POST", base+"/pools?poolId=d", map[string]any{})
	expect(t, h.call("owner", "POST", base+"/pools/d/members", map[string]any{"member": map[string]any{"note": "child"}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/pools/d/members", map[string]any{"memberId": "Bad_ID"}), 400, "INVALID_ARGUMENT")
	member := h.call("owner", "POST", base+"/pools/d/members", map[string]any{"memberId": "m1", "member": map[string]any{"note": "locked"}})
	expect(t, member, 200, "")
	if member.body["name"] != "projects/tenkacloud-local/pools/d/members/m1" || member.body["note"] != "locked" || member.body["memberId"] != nil {
		t.Fatalf("unwrapped create %v", member.body)
	}
	expect(t, h.call("owner", "DELETE", base+"/pools/d?ignoreReferences=true", nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "PATCH", base+"/pools/d/members/m1", map[string]any{"member": map[string]any{"note": "x"}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PATCH", base+"/pools/d/members/m1", map[string]any{"fieldMask": "name", "member": map[string]any{"name": "x"}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PATCH", base+"/pools/d/members/m1", map[string]any{"fieldMask": "note", "member": map[string]any{"note": "child"}}), 200, "")
	denied := h.call("intruder", "GET", "/refs/v1/"+created.body["name"].(string), nil)
	if denied.status != 403 || !strings.Contains(denied.raw, "refs.poolOperations.get") {
		t.Fatalf("permission override %s", denied.raw)
	}
	h.call("owner", "POST", base+"/volumes?volumeId=z", map[string]any{"pool": "projects/tenkacloud-local/pools/d"})
	expect(t, h.call("owner", "DELETE", base+"/pools/d", nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "DELETE", base+"/pools/d?ignoreReferences=true", nil), 200, "")
	expect(t, h.call("owner", "GET", "/refs/v1/"+member.body["name"].(string), nil), 404, "NOT_FOUND")
	h.call("owner", "POST", base+"/pools?poolId=c", map[string]any{})
	h.call("owner", "POST", base+"/volumes?volumeId=x", map[string]any{"pool": "projects/tenkacloud-local/pools/c"})
	// Implicit parent segments are validated but not stored, and their
	// children cascade with the stored parent.
	h.call("owner", "POST", base+"/pools?poolId=g", map[string]any{})
	tag := h.call("owner", "POST", base+"/pools/g/groups/blue/tags", map[string]any{"note": "t"})
	expect(t, tag, 200, "")
	if !strings.HasPrefix(tag.body["name"].(string), "projects/tenkacloud-local/pools/g/groups/blue/tags/") {
		t.Fatalf("implicit child name %v", tag.body)
	}
	if items := h.call("owner", "GET", base+"/pools/g/groups/blue/tags", nil).body["tags"].([]any); len(items) != 1 {
		t.Fatalf("implicit children %v", items)
	}
	expect(t, h.call("owner", "POST", base+"/pools/g/groups/Bad_Group/tags", map[string]any{}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/pools/missing/groups/blue/tags", map[string]any{}), 404, "NOT_FOUND")
	expect(t, h.call("owner", "DELETE", base+"/pools/g", nil), 200, "")
	if items := h.call("owner", "GET", base+"/pools/g/groups/blue/tags", nil); items.status != 404 {
		t.Fatalf("implicit children outlived their parent: %v", items.raw)
	}
	for _, d := range h.engine.Describe() {
		if d.Name == "refs" && d.Collections[1].Implicit != "groups" {
			t.Fatalf("descriptor %+v", d.Collections[1])
		}
	}
	// A corrupt child fails the protected-child scan of a cascade.
	h.call("owner", "POST", base+"/pools?poolId=e", map[string]any{})
	h.call("owner", "POST", base+"/pools/e/members", map[string]any{"memberId": "bad"})
	if _, err := h.engine.store.db.Exec("UPDATE gcp_catalog_resources SET document='nope' WHERE id='bad'"); err != nil {
		t.Fatal(err)
	}
	expect(t, h.call("owner", "DELETE", base+"/pools/e", nil), 500, "INTERNAL")
	for _, statement := range []string{"UPDATE gcp_catalog_resources SET document='nope' WHERE id='x'", "DROP TABLE gcp_catalog_resources"} {
		if _, err := h.engine.store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
		expect(t, h.call("owner", "DELETE", base+"/pools/c", nil), 500, "INTERNAL")
	}
	expect(t, h.call("owner", "POST", base+"/volumes?volumeId=y", map[string]any{"pool": "projects/tenkacloud-local/pools/c"}), 500, "INTERNAL")
	for name, broken := range map[string]func(*API){
		"reference field":      func(a *API) { a.Collections[1].References = map[string]Reference{"nope": {Collection: "pools"}} },
		"reference collection": func(a *API) { a.Collections[1].References = map[string]Reference{"pool": {Collection: "nope"}} },
		"operations anchor":    func(a *API) { a.Collections[3].Operations = "pools" },
	} {
		api, model := referenceAPI(t)
		broken(api)
		if _, err := bindAPI(api, model); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if ancestor("projects/p/x/1", "y") != "projects/p/x/1" {
		t.Fatal("an unknown anchor keeps the resource name")
	}
}

// Resource names are unique per API: two services may each hold the same
// projects/{project}/.../widgets/{id} name.
func TestSyntheticResourceNamesAreScopedPerAPI(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"synth", "twin"} {
		api, model := syntheticAPI(t)
		model.Key, model.Name, api.Key = name+".v1", name, name+".v1"
		api.Collections[0].IDField = "name"
		b, err := bindAPI(api, model)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.engine.add(b); err != nil {
			t.Fatal(err)
		}
	}
	for _, api := range []string{"synth", "twin"} {
		created := h.call("owner", "POST", "/"+api+"/synth/v1/projects/tenkacloud-local/widgets", map[string]any{"name": "same", "note": api})
		expect(t, created, 200, "")
	}
	for _, api := range []string{"synth", "twin"} {
		got := h.call("owner", "GET", "/"+api+"/synth/v1/projects/tenkacloud-local/widgets/same", nil)
		expect(t, got, 200, "")
		if got.body["note"] != api {
			t.Fatalf("%s read another API's resource: %v", api, got.body)
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
		Collections: []*Collection{{ID: "widgets", Schema: "SynthV1Widget", Message: "Widget", IDField: "name", FullName: true, Permission: "synth.widgets"}},
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
