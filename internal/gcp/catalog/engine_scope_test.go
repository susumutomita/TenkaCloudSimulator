package catalog

import "testing"

// twinAPI serves clusters under the same relative names as AlloyDB, as
// Cluster Director does, so the two services must not share resources.
func twinAPI(t *testing.T) (*API, *Model) {
	t.Helper()
	base := "v1/projects/{projectsId}/locations/{locationsId}/clusters"
	idParam := map[string]Parameter{"clusterId": {Type: "string", Location: "query"}}
	model := &Model{Key: "twin.v1", Name: "twin", Version: "v1", Title: "Twin",
		Source: Source{Package: "google-api-python-client", SHA256: "test", License: "Apache-2.0"},
		Routes: map[string]Route{
			"projects.locations.clusters.create": {HTTPMethod: "POST", FlatPath: base},
			"projects.locations.clusters.get":    {HTTPMethod: "GET", FlatPath: base + "/{clustersId}"},
			"projects.locations.clusters.delete": {HTTPMethod: "DELETE", FlatPath: base + "/{clustersId}"},
		},
		Methods: map[string]*Method{
			"projects.locations.clusters.create": {HTTPMethod: "POST", FlatPath: base, Parameters: idParam, Request: "Cluster", Response: "Cluster"},
			"projects.locations.clusters.get":    {HTTPMethod: "GET", FlatPath: base + "/{clustersId}", Parameters: map[string]Parameter{"view": {Type: "string", Location: "query"}}, Response: "Cluster"},
			"projects.locations.clusters.delete": {HTTPMethod: "DELETE", FlatPath: base + "/{clustersId}", Response: "Cluster"},
		},
		Schemas: map[string]*Property{"Cluster": {Type: "object", Properties: map[string]*Property{"name": {Type: "string"}, "note": {Type: "string"}}}},
	}
	if err := validateModel(model); err != nil {
		t.Fatal(err)
	}
	api := &API{Key: "twin.v1", Title: "Twin", Issues: []int{1}, Scope: "location", TypeURL: "type.googleapis.com/test.", Bounds: []string{"test"},
		Collections: []*Collection{{ID: "clusters", Schema: "Cluster", IDParam: "clusterId", Permission: "twin.clusters"}},
		Bindings: []Binding{
			{Method: "projects.locations.clusters.create", Kind: Create, Collection: "clusters"},
			{Method: "projects.locations.clusters.get", Kind: Get, Collection: "clusters", Refuse: []string{"view"}},
			{Method: "projects.locations.clusters.delete", Kind: Delete, Collection: "clusters"},
		}}
	return api, model
}

// Relative resource names are scoped per API, as Google full resource names
// (//<service>.googleapis.com/...) are: equal names in two services are
// separate resources with separate children.
func TestResourceNamesAreScopedPerAPI(t *testing.T) {
	h := newHarness(t, alloyDB)
	api, model := twinAPI(t)
	b, err := bindAPI(api, model)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	twin := "/twin/v1/projects/tenkacloud-local/locations/us-central1/clusters"
	expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=same", map[string]any{"network": "n"}), 200, "")
	expect(t, h.call("owner", "POST", loc+"/clusters/same/instances?instanceId=i", map[string]any{"instanceType": "PRIMARY"}), 200, "")
	expect(t, h.call("owner", "POST", twin+"?clusterId=same", map[string]any{"note": "twin"}), 200, "")
	expect(t, h.call("owner", "POST", twin+"?clusterId=same", map[string]any{}), 409, "ALREADY_EXISTS")
	if got := h.call("owner", "GET", twin+"/same", nil); got.body["note"] != "twin" {
		t.Fatalf("twin resource %v", got.body)
	}
	if got := h.call("owner", "GET", loc+"/clusters/same", nil); got.body["note"] != nil || got.body["state"] != "READY" {
		t.Fatalf("alloydb resource %v", got.body)
	}
	// The AlloyDB instance is not a child of the twin cluster.
	expect(t, h.call("owner", "DELETE", twin+"/same", nil), 200, "")
	expect(t, h.call("owner", "GET", twin+"/same", nil), 404, "NOT_FOUND")
	expect(t, h.call("owner", "GET", loc+"/clusters/same/instances/i", nil), 200, "")
	expect(t, h.call("owner", "DELETE", loc+"/clusters/same", nil), 400, "FAILED_PRECONDITION")
}

// Refused query parameters fail closed at binding and at request time.
func TestRefusedQueryParameters(t *testing.T) {
	h := newHarness(t, alloyDB)
	api, model := twinAPI(t)
	b, err := bindAPI(api, model)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	twin := "/twin/v1/projects/tenkacloud-local/locations/us-central1/clusters"
	expect(t, h.call("owner", "POST", twin+"?clusterId=c", map[string]any{}), 200, "")
	expect(t, h.call("owner", "GET", twin+"/c?view=FULL", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", twin+"/c?view=", nil), 200, "")
	expect(t, h.call("owner", "GET", twin+"/c", nil), 200, "")
	api, model = twinAPI(t)
	api.Bindings[0].Refuse = []string{"missing"}
	if _, err := bindAPI(api, model); err == nil {
		t.Fatal("a refused parameter must be an official query parameter")
	}
}
