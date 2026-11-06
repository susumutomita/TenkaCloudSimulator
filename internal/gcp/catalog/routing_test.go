package catalog

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// newHarnessAt is newHarness with explicit supported locations.
func newHarnessAt(t *testing.T, locations []string, apis ...*API) *harness {
	t.Helper()
	store, err := core.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := &harness{t: t, now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
	engine, err := New(store.Database(), Config{Project: "tenkacloud-local", ProjectNumber: "100000000001", Owner: "owner", Locations: locations,
		Subject: func(ctx context.Context) (string, bool) { s, ok := ctx.Value(subjectKey{}).(string); return s, ok }, Now: func() time.Time { return h.now }}, apis)
	if err != nil {
		t.Fatal(err)
	}
	h.engine = engine
	return h
}

// geoAPI has overlapping official bindings, a project-scope collection
// under a location, a recursive delete and a zonal collection.
func geoAPI(t *testing.T) *binding {
	t.Helper()
	item := &Property{Type: "object", Properties: map[string]*Property{"name": {Type: "string"}}}
	op := &Property{Type: "object", Properties: map[string]*Property{"name": {Type: "string"}, "done": {Type: "boolean"}, "response": {Type: "object"}}}
	paths := map[string][2]string{
		"items.get":                                  {"GET", "v1/{v1Id}/{v1Id1}/items/{itemsId}"},
		"projects.items.create":                      {"POST", "v1/projects/{projectsId}/items"},
		"projects.items.get":                         {"GET", "v1/projects/{projectsId}/items/{itemsId}"},
		"projects.locations.buckets.create":          {"POST", "v1/projects/{projectsId}/locations/{locationsId}/buckets"},
		"projects.locations.buckets.delete":          {"DELETE", "v1/projects/{projectsId}/locations/{locationsId}/buckets/{bucketsId}"},
		"projects.locations.buckets.children.create": {"POST", "v1/projects/{projectsId}/locations/{locationsId}/buckets/{bucketsId}/children"},
		"projects.locations.buckets.children.get":    {"GET", "v1/projects/{projectsId}/locations/{locationsId}/buckets/{bucketsId}/children/{childrenId}"},
		"projects.locations.zonals.create":           {"POST", "v1/projects/{projectsId}/locations/{locationsId}/zonals"},
		"projects.locations.operations.get":          {"GET", "v1/projects/{projectsId}/locations/{locationsId}/operations/{operationsId}"},
	}
	model := &Model{Key: "geo.v1", Name: "geo", Version: "v1", Title: "Geo",
		Source:  Source{Package: "google-api-python-client", SHA256: "test", License: "Apache-2.0"},
		Routes:  map[string]Route{},
		Methods: map[string]*Method{},
		Schemas: map[string]*Property{"Item": item, "Operation": op, "Empty": {Type: "object", Properties: map[string]*Property{}}},
	}
	for id, p := range paths {
		model.Routes[id] = Route{HTTPMethod: p[0], FlatPath: p[1]}
		if id == "items.get" {
			continue
		}
		m := &Method{HTTPMethod: p[0], FlatPath: p[1], Response: "Item", Parameters: map[string]Parameter{}}
		switch id {
		case "projects.items.create":
			m.Request, m.Parameters["itemId"] = "Item", Parameter{Type: "string", Location: "query"}
		case "projects.locations.buckets.create":
			m.Request, m.Parameters["bucketId"] = "Item", Parameter{Type: "string", Location: "query"}
		case "projects.locations.buckets.children.create":
			m.Request, m.Parameters["childId"] = "Item", Parameter{Type: "string", Location: "query"}
		case "projects.locations.zonals.create":
			m.Request, m.Response, m.Parameters["zonalId"] = "Item", "Operation", Parameter{Type: "string", Location: "query"}
		case "projects.locations.buckets.delete":
			m.Response = "Empty"
		case "projects.locations.operations.get":
			m.Response = "Operation"
		}
		model.Methods[id] = m
	}
	if err := validateModel(model); err != nil {
		t.Fatal(err)
	}
	api := &API{Key: "geo.v1", Title: "Geo", Issues: []int{1}, Scope: "project", TypeURL: "type.googleapis.com/test.", Bounds: []string{"test"},
		Collections: []*Collection{
			{ID: "items", Schema: "Item", IDParam: "itemId", Permission: "geo.items"},
			{ID: "buckets", Schema: "Item", IDParam: "bucketId", Permission: "geo.buckets", Recursive: true},
			{ID: "children", Schema: "Item", Parent: "buckets", IDParam: "childId", Permission: "geo.children"},
			{ID: "zonals", Schema: "Item", IDParam: "zonalId", Permission: "geo.zonals", Zonal: true},
		},
		Bindings: []Binding{
			{Method: "projects.items.create", Kind: Create, Collection: "items"},
			{Method: "projects.items.get", Kind: Get, Collection: "items"},
			{Method: "projects.locations.buckets.create", Kind: Create, Collection: "buckets"},
			{Method: "projects.locations.buckets.delete", Kind: Delete, Collection: "buckets"},
			{Method: "projects.locations.buckets.children.create", Kind: Create, Collection: "children"},
			{Method: "projects.locations.buckets.children.get", Kind: Get, Collection: "children"},
			{Method: "projects.locations.zonals.create", Kind: Create, Collection: "zonals"},
			{Method: "projects.locations.operations.get", Kind: OperationGet},
		}}
	b, err := bindAPI(api, model)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOverlappingBindingsLocationRootsRecursiveDeleteAndZones(t *testing.T) {
	h := newHarnessAt(t, []string{"global", "us-central1"})
	if err := h.engine.add(geoAPI(t)); err != nil {
		t.Fatal(err)
	}
	base := "/geo/v1/projects/tenkacloud-local"
	// The projects/{project} binding wins over the unreviewed {+parent} alias.
	expect(t, h.call("owner", "GET", base+"/items/missing", nil), 404, "NOT_FOUND")
	expect(t, h.call("owner", "POST", base+"/items?itemId=one", map[string]any{}), 200, "")
	expect(t, h.call("owner", "GET", base+"/items/one", nil), 200, "")
	expect(t, h.call("owner", "GET", "/geo/v1/folders/f/items/one", nil), 501, "UNIMPLEMENTED")
	// A project-scope collection lives under the location its path names.
	bucket := h.call("owner", "POST", base+"/locations/us-central1/buckets?bucketId=logs", map[string]any{})
	expect(t, bucket, 200, "")
	if bucket.body["name"] != "projects/tenkacloud-local/locations/us-central1/buckets/logs" {
		t.Fatalf("location-rooted name %v", bucket.body)
	}
	expect(t, h.call("owner", "POST", base+"/locations/europe-west1/buckets?bucketId=logs", map[string]any{}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/locations/us-central1/buckets/logs/children?childId=c", map[string]any{}), 200, "")
	// Recursive delete removes children without force.
	expect(t, h.call("owner", "DELETE", base+"/locations/us-central1/buckets/logs", nil), 200, "")
	expect(t, h.call("owner", "GET", base+"/locations/us-central1/buckets/logs/children/c", nil), 404, "NOT_FOUND")
	// Zonal collections take zones of supported regions only.
	zonal := h.call("owner", "POST", base+"/locations/us-central1-a/zonals?zonalId=z", map[string]any{})
	expect(t, zonal, 200, "")
	expect(t, h.call("owner", "GET", "/geo/v1/"+zonal.body["name"].(string), nil), 200, "")
	for _, location := range []string{"us-central1", "global-a", "us-central1-ab", "us-central1-1", "europe-west1-b"} {
		expect(t, h.call("owner", "POST", base+"/locations/"+location+"/zonals?zonalId=z", map[string]any{}), 400, "INVALID_ARGUMENT")
	}
	expect(t, h.call("owner", "POST", base+"/locations/us-central1-a/buckets?bucketId=b", map[string]any{}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", base+"/locations/us-central1/operations/operation-missing", nil), 404, "NOT_FOUND")
	for _, d := range h.engine.Describe() {
		if d.Name == "geo" && (!d.Collections[3].Zonal || d.Collections[0].Zonal) {
			t.Fatalf("zonal descriptors %+v", d.Collections)
		}
	}
}
