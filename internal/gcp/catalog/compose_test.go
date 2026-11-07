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

// zonalAPI places full-name body IDs, implicit parent segments and
// operations in zones of a project-scope API.
func zonalAPI(t *testing.T) *binding {
	t.Helper()
	zone := "v1/projects/{projectsId}/locations/{locationsId}"
	node := zone + "/nodes/{nodesId}"
	methods := map[string]*Method{
		"projects.locations.nodes.create":              {HTTPMethod: "POST", FlatPath: zone + "/nodes", Request: "Node", Response: "Operation"},
		"projects.locations.nodes.get":                 {HTTPMethod: "GET", FlatPath: node, Response: "Node"},
		"projects.locations.nodes.delete":              {HTTPMethod: "DELETE", FlatPath: node, Response: "Operation"},
		"projects.locations.nodes.groups.ports.create": {HTTPMethod: "POST", FlatPath: node + "/groups/{groupsId}/ports", Request: "Node", Response: "Node", Parameters: map[string]Parameter{"portId": {Type: "string", Location: "query"}}},
		"projects.locations.nodes.groups.ports.get":    {HTTPMethod: "GET", FlatPath: node + "/groups/{groupsId}/ports/{portsId}", Response: "Node"},
		"projects.locations.operations.get":            {HTTPMethod: "GET", FlatPath: zone + "/operations/{operationsId}", Response: "Operation"},
	}
	routes := map[string]Route{}
	for id, m := range methods {
		routes[id] = Route{HTTPMethod: m.HTTPMethod, FlatPath: m.FlatPath}
	}
	model := &Model{Key: "mix.v1", Name: "mix", Version: "v1", Title: "Mixed", Source: Source{Package: "google-api-python-client", SHA256: "test", License: "Apache-2.0"},
		Routes: routes, Methods: methods, Schemas: map[string]*Property{
			"Node":      {Type: "object", Properties: map[string]*Property{"name": {Type: "string"}, "note": {Type: "string"}}},
			"Operation": {Type: "object", Properties: map[string]*Property{"name": {Type: "string"}, "done": {Type: "boolean"}, "response": {Type: "object"}}},
		}}
	if err := validateModel(model); err != nil {
		t.Fatal(err)
	}
	api := &API{Key: "mix.v1", Title: "Mixed", Issues: []int{1}, Scope: "project", TypeURL: "type.googleapis.com/mix.v1.", Bounds: []string{"test"},
		Collections: []*Collection{
			{ID: "nodes", Schema: "Node", Message: "MixNode", IDField: "name", FullName: true, Zonal: true, Permission: "mix.nodes"},
			{ID: "ports", Schema: "Node", Parent: "nodes", Implicit: "groups", IDParam: "portId", Zonal: true, Permission: "mix.ports"},
		},
		Bindings: []Binding{
			{Method: "projects.locations.nodes.create", Kind: Create, Collection: "nodes"},
			{Method: "projects.locations.nodes.get", Kind: Get, Collection: "nodes"},
			{Method: "projects.locations.nodes.delete", Kind: Delete, Collection: "nodes"},
			{Method: "projects.locations.nodes.groups.ports.create", Kind: Create, Collection: "ports"},
			{Method: "projects.locations.nodes.groups.ports.get", Kind: Get, Collection: "ports"},
			{Method: "projects.locations.operations.get", Kind: OperationGet},
		}}
	b, err := bindAPI(api, model)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestZonalFullNamesImplicitChildrenAndOperations(t *testing.T) {
	h := newHarness(t)
	if err := h.engine.add(zonalAPI(t)); err != nil {
		t.Fatal(err)
	}
	zone := "/mix/v1/projects/tenkacloud-local/locations/us-central1-a"
	node := "projects/tenkacloud-local/locations/us-central1-a/nodes/n1"
	expect(t, h.call("owner", "POST", "/mix/v1/projects/tenkacloud-local/locations/us-central1/nodes", map[string]any{"name": "projects/tenkacloud-local/locations/us-central1/nodes/n1"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", zone+"/nodes", map[string]any{"name": "projects/tenkacloud-local/locations/us-central1-b/nodes/n1"}), 400, "INVALID_ARGUMENT")
	// Only published zones of supported regions are zones.
	expect(t, h.call("owner", "POST", "/mix/v1/projects/tenkacloud-local/locations/us-central1-d/nodes", map[string]any{"name": "projects/tenkacloud-local/locations/us-central1-d/nodes/n1"}), 400, "INVALID_ARGUMENT")
	created := h.call("owner", "POST", zone+"/nodes", map[string]any{"name": "projects/100000000001/locations/us-central1-a/nodes/n1"})
	expect(t, created, 200, "")
	op, _ := created.body["name"].(string)
	response := created.body["response"].(map[string]any)
	if !strings.HasPrefix(op, "projects/tenkacloud-local/locations/us-central1-a/operations/") || response["@type"] != "type.googleapis.com/mix.v1.MixNode" || response["name"] != node {
		t.Fatalf("zonal full-name create %v", created.body)
	}
	expect(t, h.call("owner", "GET", "/mix/v1/"+op, nil), 200, "")
	expect(t, h.call("owner", "POST", zone+"/nodes/missing/groups/g1/ports?portId=p1", map[string]any{}), 404, "NOT_FOUND")
	expect(t, h.call("owner", "POST", zone+"/nodes/n1/groups/Bad_Group/ports?portId=p1", map[string]any{}), 400, "INVALID_ARGUMENT")
	port := h.call("owner", "POST", zone+"/nodes/n1/groups/g1/ports?portId=p1", map[string]any{})
	expect(t, port, 200, "")
	if port.body["name"] != node+"/groups/g1/ports/p1" {
		t.Fatalf("implicit zonal child %v", port.body)
	}
	expect(t, h.call("owner", "GET", zone+"/nodes/n1/groups/g1/ports/p1", nil), 200, "")
	expect(t, h.call("owner", "DELETE", zone+"/nodes/n1", nil), 400, "FAILED_PRECONDITION")
}

// refsAPI is an AIP API whose references use nested paths, alternatives,
// weak targets, an override and a conflict rule, as Compute Engine and the
// storage APIs do.
func refsAPI(t *testing.T) *binding {
	t.Helper()
	root := "v1/projects/{projectsId}/locations/{locationsId}"
	methods := map[string]*Method{}
	routes := map[string]Route{}
	for _, c := range []string{"pools", "caches", "links"} {
		methods["projects.locations."+c+".create"] = &Method{HTTPMethod: "POST", FlatPath: root + "/" + c, Request: "Item", Response: "Item",
			Parameters: map[string]Parameter{"itemId": {Type: "string", Location: "query"}, "dryRunMode": {Type: "string", Location: "query"}}}
		methods["projects.locations."+c+".patch"] = &Method{HTTPMethod: "PATCH", FlatPath: root + "/" + c + "/{id}", Request: "Item", Response: "Item",
			Parameters: map[string]Parameter{"updateMask": {Type: "string", Location: "query"}}}
		methods["projects.locations."+c+".delete"] = &Method{HTTPMethod: "DELETE", FlatPath: root + "/" + c + "/{id}", Response: "Empty",
			Parameters: map[string]Parameter{"ignoreLinks": {Type: "boolean", Location: "query"}}}
	}
	for id, m := range methods {
		routes[id] = Route{HTTPMethod: m.HTTPMethod, FlatPath: m.FlatPath}
	}
	model := &Model{Key: "links.v1", Name: "links", Version: "v1", Title: "Links", Source: Source{Package: "google-api-python-client", SHA256: "test", License: "Apache-2.0"},
		Routes: routes, Methods: methods, Schemas: map[string]*Property{
			"Item": {Type: "object", Properties: map[string]*Property{"name": {Type: "string"}, "note": {Type: "string"}, "range": {Type: "string"}, "origin": {Type: "string"},
				"spec": {Type: "object", Properties: map[string]*Property{"backends": {Type: "array", Items: &Property{Type: "object", Properties: map[string]*Property{"target": {Type: "string"}}}}}}}},
			"Empty": {Type: "object", Properties: map[string]*Property{}},
		}}
	if err := validateModel(model); err != nil {
		t.Fatal(err)
	}
	api := &API{Key: "links.v1", Title: "Links", Issues: []int{1}, Scope: "location", TypeURL: "type.googleapis.com/links.v1.", Bounds: []string{"test"}}
	for _, c := range []string{"pools", "caches", "links"} {
		api.Collections = append(api.Collections, &Collection{ID: c, Schema: "Item", IDParam: "itemId", Permission: "links." + c, Mutable: []string{"note", "spec", "range", "origin"}})
	}
	api.Collections[2].References = map[string]Reference{
		"spec.backends[].target": {Collection: "pools", Or: []string{"caches"}, Blocks: true, Override: "ignoreLinks"},
		"origin":                 {Collection: "pools"},
	}
	api.Collections[2].Conflict = func(candidate, other map[string]any) error {
		if candidate["range"] != nil && candidate["range"] == other["range"] {
			return invalid("The range overlaps.")
		}
		return nil
	}
	for id := range methods {
		parts := strings.Split(id, ".")
		kind := map[string]Kind{"create": Create, "patch": Patch, "delete": Delete}[parts[3]]
		bound := Binding{Method: id, Kind: kind, Collection: parts[2]}
		if kind == Create {
			bound.Refuse = []string{"dryRunMode"}
		}
		api.Bindings = append(api.Bindings, bound)
	}
	b, err := bindAPI(api, model)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Unified references: nested paths and alternatives resolve inside the
// write, project numbers normalize, a wrong collection is NOT_FOUND for AIP
// APIs, updates re-check only written paths, weak references never block,
// a blocking one yields to its override, and conflicts apply to AIP APIs.
func TestNestedAlternativeReferencesOverridesAndConflicts(t *testing.T) {
	h := newHarness(t)
	if err := h.engine.add(refsAPI(t)); err != nil {
		t.Fatal(err)
	}
	base := "/links/v1/projects/tenkacloud-local/locations/us-central1"
	full := "projects/tenkacloud-local/locations/us-central1"
	backends := func(targets ...string) map[string]any {
		list := []any{}
		for _, target := range targets {
			list = append(list, map[string]any{"target": target})
		}
		return map[string]any{"backends": list}
	}
	expect(t, h.call("owner", "POST", base+"/pools?itemId=p", map[string]any{}), 200, "")
	expect(t, h.call("owner", "POST", base+"/caches?itemId=c", map[string]any{}), 200, "")
	expect(t, h.call("owner", "POST", base+"/pools?itemId=old", map[string]any{}), 200, "")
	// A refused parameter is accepted at its default value only.
	expect(t, h.call("owner", "POST", base+"/pools?itemId=x&dryRunMode=FULL", map[string]any{}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/pools?itemId=y&dryRunMode=", map[string]any{}), 200, "")
	expect(t, h.call("owner", "POST", base+"/links?itemId=l", map[string]any{"spec": backends(full + "/pools/missing")}), 404, "NOT_FOUND")
	expect(t, h.call("owner", "POST", base+"/links?itemId=l", map[string]any{"spec": backends(full + "/links/l")}), 404, "NOT_FOUND")
	expect(t, h.call("owner", "POST", base+"/links?itemId=l", map[string]any{"spec": backends("not-a-name")}), 404, "NOT_FOUND")
	link := h.call("owner", "POST", base+"/links?itemId=l", map[string]any{"range": "r1", "origin": full + "/pools/old",
		"spec": backends("projects/100000000001/locations/us-central1/pools/p", full+"/caches/c")})
	expect(t, link, 200, "")
	if got := link.body["spec"].(map[string]any)["backends"].([]any)[0].(map[string]any)["target"]; got != full+"/pools/p" {
		t.Fatalf("nested references normalize project numbers: %v", link.body)
	}
	expect(t, h.call("owner", "POST", base+"/links?itemId=m", map[string]any{"range": "r1"}), 400, "INVALID_ARGUMENT")
	// Weak references never block; an update that does not write them
	// does not re-check them.
	expect(t, h.call("owner", "DELETE", base+"/pools/old", nil), 200, "")
	expect(t, h.call("owner", "PATCH", base+"/links/l?updateMask=note", map[string]any{"note": "kept"}), 200, "")
	expect(t, h.call("owner", "PATCH", base+"/links/l?updateMask=origin", map[string]any{"origin": full + "/pools/old"}), 404, "NOT_FOUND")
	expect(t, h.call("owner", "PATCH", base+"/links/l?updateMask=range", map[string]any{"range": "r2"}), 200, "")
	// Blocking references through either alternative keep their targets.
	for _, target := range []string{"/pools/p", "/caches/c"} {
		if denied := h.call("owner", "DELETE", base+target, nil); denied.status != 400 || !strings.Contains(denied.raw, "still referenced by") {
			t.Fatalf("blocked delete of %s: %s", target, denied.raw)
		}
	}
	expect(t, h.call("owner", "DELETE", base+"/caches/c?ignoreLinks=true", nil), 200, "")
	expect(t, h.call("owner", "PATCH", base+"/links/l?updateMask=spec", map[string]any{"spec": backends(full + "/pools/p")}), 200, "")
	expect(t, h.call("owner", "DELETE", base+"/links/l", nil), 200, "")
	expect(t, h.call("owner", "DELETE", base+"/pools/p", nil), 200, "")
}

// Templates with equally many literal segments resolve to the one whose
// literals come first.
func TestRouteTiesPreferEarlierLiterals(t *testing.T) {
	b := &binding{model: &Model{Routes: map[string]Route{
		"late":  {HTTPMethod: "GET", FlatPath: "{a}/b/c"},
		"early": {HTTPMethod: "GET", FlatPath: "a/b/{c}"},
		"more":  {HTTPMethod: "GET", FlatPath: "{a}/{b}/x"},
	}}}
	for _, id := range []string{"early", "late", "more"} {
		segments := splitPath(b.model.Routes[id].FlatPath)
		b.routes = append(b.routes, route{method: id, segments: segments})
	}
	if rt, ok := b.match("GET", []string{"a", "b", "c"}); !ok || rt.method != "early" {
		t.Fatalf("tie %v", rt)
	}
	if rt, ok := b.match("GET", []string{"q", "b", "c"}); !ok || rt.method != "late" {
		t.Fatalf("single match %v", rt)
	}
}

// Descriptors tell the console how to name and create resources: full and
// short body names, zonal collections, and required fields a scalar form
// cannot collect. The body ID field is not offered twice.
func TestDescriptorsCarryNamingAndFormShapes(t *testing.T) {
	h := newHarness(t)
	for _, b := range []*binding{zonalAPI(t)} {
		if err := h.engine.add(b); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]func(CollectionDescriptor) bool{
		"composer/environments": func(c CollectionDescriptor) bool { return c.IDField == "name" && c.FullName && !c.ShortName },
		"logging/sinks":         func(c CollectionDescriptor) bool { return c.IDField == "name" && c.ShortName && !c.FullName },
		"ids/endpoints":         func(c CollectionDescriptor) bool { return c.Zonal && c.IDParam == "endpointId" },
		"spanner/instances":     func(c CollectionDescriptor) bool { return len(c.Unformed) == 1 && c.Unformed[0] == "instance" },
		"mix/nodes":             func(c CollectionDescriptor) bool { return c.FullName && c.Zonal },
		"dataproc/autoscalingPolicies": func(c CollectionDescriptor) bool {
			for _, f := range c.Fields {
				if f.Name == c.IDField {
					return false
				}
			}
			return c.IDField == "id"
		},
	}
	for _, d := range h.engine.Describe() {
		for _, c := range d.Collections {
			if check, ok := want[d.Name+"/"+c.ID]; ok {
				if !check(c) {
					t.Fatalf("descriptor %s/%s: %+v", d.Name, c.ID, c)
				}
				delete(want, d.Name+"/"+c.ID)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing descriptors %v", want)
	}
	if zones := Zones([]string{"global", "us-east1"}); len(zones) != 3 || zones[0] != "us-east1-b" {
		t.Fatalf("zones %v", zones)
	}
}
