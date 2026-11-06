package catalog

import (
	"context"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// flatModel is a Compute Engine shaped discovery subset: separate path
// parameters, body IDs, slash-style custom verbs and operation resources.
func flatModel(t *testing.T) *Model {
	t.Helper()
	routes := map[string]Route{
		"networks.insert":          {HTTPMethod: "POST", FlatPath: "projects/{project}/global/networks"},
		"networks.get":             {HTTPMethod: "GET", FlatPath: "projects/{project}/global/networks/{network}"},
		"networks.list":            {HTTPMethod: "GET", FlatPath: "projects/{project}/global/networks"},
		"networks.patch":           {HTTPMethod: "PATCH", FlatPath: "projects/{project}/global/networks/{network}"},
		"networks.delete":          {HTTPMethod: "DELETE", FlatPath: "projects/{project}/global/networks/{network}"},
		"networks.listUsable":      {HTTPMethod: "GET", FlatPath: "projects/{project}/global/networks/listUsable"},
		"subnetworks.insert":       {HTTPMethod: "POST", FlatPath: "projects/{project}/regions/{region}/subnetworks"},
		"subnetworks.patch":        {HTTPMethod: "PATCH", FlatPath: "projects/{project}/regions/{region}/subnetworks/{subnetwork}"},
		"subnetworks.get":          {HTTPMethod: "GET", FlatPath: "projects/{project}/regions/{region}/subnetworks/{subnetwork}"},
		"subnetworks.delete":       {HTTPMethod: "DELETE", FlatPath: "projects/{project}/regions/{region}/subnetworks/{subnetwork}"},
		"disks.insert":             {HTTPMethod: "POST", FlatPath: "projects/{project}/zones/{zone}/disks"},
		"disks.get":                {HTTPMethod: "GET", FlatPath: "projects/{project}/zones/{zone}/disks/{disk}"},
		"disks.setLabels":          {HTTPMethod: "POST", FlatPath: "projects/{project}/zones/{zone}/disks/{resource}/setLabels"},
		"disks.resize":             {HTTPMethod: "POST", FlatPath: "projects/{project}/zones/{zone}/disks/{disk}/resize"},
		"hosts.list":               {HTTPMethod: "GET", FlatPath: "projects/{project}/zones/{zone}/{association}/hosts"},
		"things.list":              {HTTPMethod: "GET", FlatPath: "projects/{project}/things"},
		"globalOperations.get":     {HTTPMethod: "GET", FlatPath: "projects/{project}/global/operations/{operation}"},
		"globalOperations.list":    {HTTPMethod: "GET", FlatPath: "projects/{project}/global/operations"},
		"globalOperations.wait":    {HTTPMethod: "POST", FlatPath: "projects/{project}/global/operations/{operation}/wait"},
		"regionOperations.get":     {HTTPMethod: "GET", FlatPath: "projects/{project}/regions/{region}/operations/{operation}"},
		"zoneOperations.list":      {HTTPMethod: "GET", FlatPath: "projects/{project}/zones/{zone}/operations"},
		"networks.getEffectiveFoo": {HTTPMethod: "GET", FlatPath: "projects/{project}/global/networks/{network}/getEffectiveFoo"},
	}
	query := func(names ...string) map[string]Parameter {
		out := map[string]Parameter{}
		for _, n := range names {
			out[n] = Parameter{Type: "string", Location: "query"}
		}
		return out
	}
	page := map[string]Parameter{"maxResults": {Type: "integer", Location: "query"}, "pageToken": {Type: "string", Location: "query"},
		"filter": {Type: "string", Location: "query"}, "orderBy": {Type: "string", Location: "query"}}
	methods := map[string]*Method{
		"networks.insert":       {Request: "Network", Response: "Operation", Parameters: query("requestId")},
		"networks.get":          {Response: "Network"},
		"networks.list":         {Response: "NetworkList", Parameters: page},
		"networks.patch":        {Request: "Network", Response: "Operation", Parameters: query("requestId")},
		"networks.delete":       {Response: "Operation", Parameters: query("requestId")},
		"subnetworks.insert":    {Request: "Subnetwork", Response: "Operation"},
		"subnetworks.patch":     {Request: "Subnetwork", Response: "Operation", Parameters: query("drainTimeoutSeconds")},
		"subnetworks.get":       {Response: "Subnetwork"},
		"subnetworks.delete":    {Response: "Operation"},
		"disks.insert":          {Request: "Disk", Response: "Operation", Parameters: query("sourceImage")},
		"disks.get":             {Response: "Disk"},
		"disks.setLabels":       {Request: "SetLabelsRequest", Response: "Operation"},
		"disks.resize":          {Request: "ResizeRequest", Response: "Operation"},
		"things.list":           {Response: "NetworkList"},
		"globalOperations.get":  {Response: "Operation"},
		"globalOperations.list": {Response: "OperationList", Parameters: page},
		"globalOperations.wait": {Response: "Operation"},
		"regionOperations.get":  {Response: "Operation"},
		"zoneOperations.list":   {Response: "OperationList", Parameters: page},
	}
	for id, m := range methods {
		m.HTTPMethod, m.Path = routes[id].HTTPMethod, routes[id].FlatPath
	}
	str := func() *Property { return &Property{Type: "string"} }
	out := func() *Property { return &Property{Type: "string", ReadOnly: true} }
	common := func(kind string, extra map[string]*Property) *Property {
		p := &Property{Type: "object", Properties: map[string]*Property{
			"kind": {Type: "string", ReadOnly: true, Default: kind}, "id": {Type: "string", Format: "uint64", ReadOnly: true},
			"name": str(), "selfLink": out(), "creationTimestamp": out(), "description": str()}}
		for k, v := range extra {
			p.Properties[k] = v
		}
		return p
	}
	model := &Model{Key: "flat.v1", Name: "flat", Version: "v1", Title: "Flat", ServicePath: "flat/v1/", RootURL: "https://flat.googleapis.com/",
		Source: Source{Package: "google-api-python-client", SHA256: "test", License: "Apache-2.0"}, Routes: routes, Methods: methods,
		Schemas: map[string]*Property{
			"Network": common("flat#network", map[string]*Property{"mtu": {Type: "integer", Format: "int32"},
				"routingConfig": {Type: "object", Properties: map[string]*Property{"routingMode": str(), "bgpAlways": str()}},
				"peerings":      {Type: "array", ReadOnly: true, Items: &Property{Type: "string"}}}),
			"Subnetwork": common("flat#subnetwork", map[string]*Property{"region": str(), "fingerprint": {Type: "string", Format: "byte"}, "network": str(),
				"peers":       {Type: "array", Items: &Property{Type: "object", Properties: map[string]*Property{"subnetwork": str(), "weight": {Type: "integer"}}}},
				"ipCidrRange": {Type: "string", RequiredBy: []string{"flat.subnetworks.insert"}}}),
			"Disk": common("flat#disk", map[string]*Property{"zone": out(), "region": out(), "sizeGb": {Type: "string", Format: "int64"}, "sourceDisk": str(),
				"labels": {Type: "object", Additional: &Property{Type: "string"}}, "labelFingerprint": {Type: "string", Format: "byte"}}),
			"SetLabelsRequest": {Type: "object", Properties: map[string]*Property{"labels": {Type: "object", Additional: &Property{Type: "string"}},
				"labelFingerprint": {Type: "string", Format: "byte"}, "kind": {Type: "string", ReadOnly: true}}},
			"ResizeRequest": {Type: "object", Properties: map[string]*Property{"sizeGb": {Type: "string", Format: "int64"}}},
			"NetworkList": {Type: "object", Properties: map[string]*Property{"kind": {Type: "string", ReadOnly: true, Default: "flat#networkList"},
				"items": {Type: "array", Items: &Property{Ref: "Network"}}, "nextPageToken": str(), "selfLink": out()}},
			"Operation": {Type: "object", Properties: map[string]*Property{"kind": {Type: "string", ReadOnly: true, Default: "flat#operation"},
				"id": str(), "name": str(), "operationType": str(), "status": {Type: "string", Enum: []string{"DONE", "PENDING", "RUNNING"}},
				"progress": {Type: "integer"}, "targetLink": str(), "targetId": str(), "user": str(), "insertTime": str(), "startTime": str(),
				"endTime": str(), "selfLink": str(), "zone": str(), "region": str(), "clientOperationId": str()}},
			"OperationList": {Type: "object", Properties: map[string]*Property{"kind": {Type: "string", ReadOnly: true, Default: "flat#operationList"},
				"items": {Type: "array", Items: &Property{Ref: "Operation"}}, "nextPageToken": str(), "warning": {Type: "object"}}},
		}}
	if err := validateModel(model); err != nil {
		t.Fatal(err)
	}
	return model
}

func growOnly(current, body map[string]any) error {
	next, _ := strconv.ParseInt(body["sizeGb"].(string), 10, 64)
	previous, _ := strconv.ParseInt(current["sizeGb"].(string), 10, 64)
	if next <= previous {
		return invalid("Disks only grow.")
	}
	return nil
}

func flatAPI() *API {
	name := regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)
	return &API{Key: "flat.v1", Title: "Flat", Issues: []int{1}, Scope: "zonal", SelfLink: "https://www.googleapis.com/flat/v1/", Bounds: []string{"test"},
		Collections: []*Collection{
			{ID: "networks", Schema: "Network", IDField: "name", ShortName: true, IDPattern: name, Limit: 3, Permission: "flat.networks", Mutable: []string{"routingConfig", "mtu"}},
			{ID: "subnetworks", Schema: "Subnetwork", IDField: "name", ShortName: true, Permission: "flat.subnetworks", Mutable: []string{"description", "peers"},
				Generate: map[string]any{"fingerprint": "$fingerprint"}, References: map[string][]string{"network": {"networks"}, "peers[].subnetwork": {"subnetworks"}},
				Conflict: func(candidate, other map[string]any) error {
					if candidate["ipCidrRange"] == other["ipCidrRange"] {
						return invalid("The range overlaps.")
					}
					return nil
				}},
			{ID: "disks", Schema: "Disk", IDField: "name", ShortName: true, Permission: "flat.disks",
				Generate: map[string]any{"labelFingerprint": "$fingerprint", "sizeGb": "10"}, Weak: map[string][]string{"sourceDisk": {"disks"}}},
			{ID: "things", Schema: "Network", Permission: "flat.things"},
		},
		Bindings: []Binding{
			{Method: "networks.insert", Kind: Create, Collection: "networks"},
			{Method: "networks.get", Kind: Get, Collection: "networks"},
			{Method: "networks.list", Kind: List, Collection: "networks"},
			{Method: "networks.patch", Kind: Patch, Collection: "networks"},
			{Method: "networks.delete", Kind: Delete, Collection: "networks"},
			{Method: "subnetworks.insert", Kind: Create, Collection: "subnetworks"},
			{Method: "subnetworks.patch", Kind: Patch, Collection: "subnetworks", Fingerprint: "fingerprint", Refuse: []string{"drainTimeoutSeconds"}},
			{Method: "disks.insert", Kind: Create, Collection: "disks", Refuse: []string{"sourceImage"}},
			{Method: "disks.get", Kind: Get, Collection: "disks"},
			{Method: "disks.setLabels", Kind: Set, Collection: "disks", Fingerprint: "labelFingerprint"},
			{Method: "disks.resize", Kind: Set, Collection: "disks", Transition: growOnly},
			{Method: "things.list", Kind: List, Collection: "things"},
			{Method: "globalOperations.get", Kind: OperationGet},
			{Method: "globalOperations.list", Kind: OperationList},
			{Method: "globalOperations.wait", Kind: OperationGet},
			{Method: "regionOperations.get", Kind: OperationGet},
			{Method: "zoneOperations.list", Kind: OperationList},
			{Method: "subnetworks.get", Kind: Get, Collection: "subnetworks"},
			{Method: "subnetworks.delete", Kind: Delete, Collection: "subnetworks"},
		}}
}

func flatHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	b, err := bindAPI(flatAPI(), flatModel(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	return h
}

const flatRoot = "/flat/flat/v1/projects/tenkacloud-local"

func TestFlatResourcesAndOperations(t *testing.T) {
	h := flatHarness(t)
	created := h.call("owner", "POST", flatRoot+"/global/networks?requestId=r-1", map[string]any{"name": "vpc", "mtu": 1460, "routingConfig": map[string]any{"routingMode": "REGIONAL", "bgpAlways": "x"}, "peerings": []any{"ignored"}})
	expect(t, created, 200, "")
	op := created.body
	link := "https://www.googleapis.com/flat/v1/projects/tenkacloud-local/global/networks/vpc"
	if op["kind"] != "flat#operation" || op["status"] != "DONE" || op["operationType"] != "insert" || op["targetLink"] != link || op["progress"] != float64(100) ||
		op["user"] != "owner" || op["clientOperationId"] != "r-1" || op["zone"] != nil || !strings.HasPrefix(op["name"].(string), "operation-") ||
		op["selfLink"] != "https://www.googleapis.com/flat/v1/projects/tenkacloud-local/global/operations/"+op["name"].(string) {
		t.Fatalf("operation %v", op)
	}
	if h.engine.Describe()[1].Name != "flat" {
		t.Fatal("flat API registered")
	}
	replay := h.call("owner", "POST", flatRoot+"/global/networks?requestId=r-1", map[string]any{"name": "vpc", "mtu": 1460, "routingConfig": map[string]any{"routingMode": "REGIONAL", "bgpAlways": "x"}})
	if replay.body["name"] != op["name"] {
		t.Fatalf("requestId replays the stored operation: %v", replay.body)
	}
	got := h.call("owner", "GET", flatRoot+"/global/networks/vpc", nil)
	expect(t, got, 200, "")
	network := got.body
	if network["name"] != "vpc" || network["kind"] != "flat#network" || network["selfLink"] != link || len(network["id"].(string)) != 19 ||
		network["creationTimestamp"] != "2026-11-06T12:00:00Z" || network["peerings"] != nil || op["targetId"] != network["id"] {
		t.Fatalf("network %v", network)
	}
	fetched := h.call("owner", "GET", flatRoot+"/global/operations/"+op["name"].(string), nil)
	waited := h.call("owner", "POST", flatRoot+"/global/operations/"+op["name"].(string)+"/wait", nil)
	expect(t, waited, 200, "")
	if fetched.body["name"] != op["name"] || waited.body["status"] != "DONE" || waited.raw != fetched.raw {
		t.Fatalf("operation get/wait %v %v", fetched.body, waited.body)
	}
	if permission := h.callHeader("GET", flatRoot+"/global/operations/"+op["name"].(string)); permission != "flat.globalOperations.get" {
		t.Fatalf("operation permission %q", permission)
	}
	expect(t, h.call("owner", "POST", flatRoot+"/global/networks", map[string]any{"name": "vpc"}), 409, "ALREADY_EXISTS")
	expect(t, h.call("owner", "POST", flatRoot+"/global/networks", map[string]any{"name": "Bad_Name"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", flatRoot+"/global/networks", map[string]any{"mtu": 1500}), 400, "INVALID_ARGUMENT")

	// JSON merge patch: nested objects merge; the unchanged name is accepted.
	patched := h.call("owner", "PATCH", flatRoot+"/global/networks/vpc", map[string]any{"name": "vpc", "routingConfig": map[string]any{"routingMode": "GLOBAL"}, "selfLink": "x"})
	expect(t, patched, 200, "")
	if patched.body["operationType"] != "patch" || patched.body["targetId"] != network["id"] {
		t.Fatalf("patch operation %v", patched.body)
	}
	config := h.call("owner", "GET", flatRoot+"/global/networks/vpc", nil).body["routingConfig"].(map[string]any)
	if config["routingMode"] != "GLOBAL" || config["bgpAlways"] != "x" {
		t.Fatalf("merge patch %v", config)
	}
	expect(t, h.call("owner", "PATCH", flatRoot+"/global/networks/vpc", map[string]any{"mtu": 1500}), 200, "")
	expect(t, h.call("owner", "PATCH", flatRoot+"/global/networks/vpc", map[string]any{"name": "other"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PATCH", flatRoot+"/global/networks/vpc", map[string]any{"description": "x"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PATCH", flatRoot+"/global/networks/missing", map[string]any{"mtu": 1500}), 404, "NOT_FOUND")

	// Lists carry the official kind, selfLink and maxResults paging.
	h.call("owner", "POST", flatRoot+"/global/networks", map[string]any{"name": "second"})
	h.call("owner", "POST", flatRoot+"/global/networks", map[string]any{"name": "third"})
	expect(t, h.call("owner", "POST", flatRoot+"/global/networks", map[string]any{"name": "fourth"}), 429, "RESOURCE_EXHAUSTED")
	first := h.call("owner", "GET", flatRoot+"/global/networks?maxResults=2", nil)
	if first.body["kind"] != "flat#networkList" || len(first.body["items"].([]any)) != 2 || first.body["nextPageToken"] == nil ||
		first.body["selfLink"] != "https://www.googleapis.com/flat/v1/projects/tenkacloud-local/global/networks" {
		t.Fatalf("list %v", first.body)
	}
	rest := h.call("owner", "GET", flatRoot+"/global/networks?pageToken="+first.body["nextPageToken"].(string), nil)
	if len(rest.body["items"].([]any)) != 1 {
		t.Fatalf("second page %v", rest.body)
	}
	expect(t, h.call("owner", "GET", flatRoot+"/global/networks?maxResults=-1", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", flatRoot+"/global/networks?orderBy=name", nil), 400, "INVALID_ARGUMENT")

	// Operation lists page with maxResults and refuse filters.
	ops := h.call("owner", "GET", flatRoot+"/global/operations?maxResults=2", nil)
	if ops.body["kind"] != "flat#operationList" || len(ops.body["items"].([]any)) != 2 || ops.body["nextPageToken"] == nil {
		t.Fatalf("operations %v", ops.body)
	}
	more := h.call("owner", "GET", flatRoot+"/global/operations?pageToken="+ops.body["nextPageToken"].(string), nil)
	if len(more.body["items"].([]any)) != 3 || more.body["nextPageToken"] != nil {
		t.Fatalf("operation page %v", more.body)
	}
	expect(t, h.call("owner", "GET", flatRoot+"/global/operations?pageToken=junk", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", flatRoot+"/global/operations?orderBy=x", nil), 400, "INVALID_ARGUMENT")

	deleted := h.call("owner", "DELETE", flatRoot+"/global/networks/vpc", nil)
	expect(t, deleted, 200, "")
	if deleted.body["operationType"] != "delete" || deleted.body["targetLink"] != link || deleted.body["targetId"] != network["id"] {
		t.Fatalf("delete operation %v", deleted.body)
	}
	expect(t, h.call("owner", "GET", flatRoot+"/global/networks/vpc", nil), 404, "NOT_FOUND")
	expect(t, h.call("owner", "GET", flatRoot+"/global/operations/operation-missing", nil), 404, "NOT_FOUND")
}

// callHeader returns the permission a denied caller is told it lacks; the
// denial leaves state unchanged.
func (h *harness) callHeader(method, path string) string {
	h.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req = req.WithContext(context.WithValue(req.Context(), subjectKey{}, "intruder"))
	recorder := httptest.NewRecorder()
	h.engine.ServeHTTP(recorder, req)
	if recorder.Code != 403 {
		h.t.Fatalf("intruder was not denied: %d", recorder.Code)
	}
	return recorder.Header().Get("X-Simulator-GCP-Permission")
}

func TestFlatScopesZonesAndCustomVerbs(t *testing.T) {
	h := flatHarness(t)
	subnet := h.call("owner", "POST", flatRoot+"/regions/us-central1/subnetworks", map[string]any{"name": "app", "ipCidrRange": "10.0.0.0/24", "region": "elsewhere"})
	expect(t, subnet, 200, "")
	if subnet.body["region"] != "https://www.googleapis.com/flat/v1/projects/tenkacloud-local/regions/us-central1" {
		t.Fatalf("regional operation %v", subnet.body)
	}
	op := h.call("owner", "GET", "/flat/flat/v1/projects/100000000001/regions/us-central1/operations/"+subnet.body["name"].(string), nil)
	expect(t, op, 200, "")
	expect(t, h.call("owner", "POST", flatRoot+"/regions/us-central1/subnetworks", map[string]any{"name": "nocidr"}), 400, "INVALID_ARGUMENT")
	for _, scope := range []string{"regions/mars-1", "regions/global", "regions/us-east1"} {
		expect(t, h.call("owner", "POST", flatRoot+"/"+scope+"/subnetworks", map[string]any{"name": "x", "ipCidrRange": "10.0.0.0/24"}), 400, "INVALID_ARGUMENT")
	}
	expect(t, h.call("owner", "GET", flatRoot+"/things", nil), 400, "INVALID_ARGUMENT")

	// A Patch fingerprint guards the update and is regenerated.
	stale := map[string]any{"fingerprint": "AAAAAAAAAAA=", "description": "x"}
	expect(t, h.call("owner", "PATCH", flatRoot+"/regions/us-central1/subnetworks/app", stale), 409, "ABORTED")
	expect(t, h.call("owner", "PATCH", flatRoot+"/regions/us-central1/subnetworks/app?drainTimeoutSeconds=5", map[string]any{"description": "x"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PATCH", flatRoot+"/regions/us-central1/subnetworks/app", map[string]any{"description": "first"}), 200, "")

	// Zonal resources validate published zones.
	for _, zone := range []string{"us-central1-d", "us-east1-b", "nodash", "global-a"} {
		expect(t, h.call("owner", "POST", flatRoot+"/zones/"+zone+"/disks", map[string]any{"name": "d"}), 400, "INVALID_ARGUMENT")
	}
	disk := h.call("owner", "POST", flatRoot+"/zones/us-central1-a/disks", map[string]any{"name": "data", "labels": map[string]any{"a": "b"}, "labelFingerprint": "Y2xpZW50"})
	expect(t, disk, 200, "")
	zone := "https://www.googleapis.com/flat/v1/projects/tenkacloud-local/zones/us-central1-a"
	if disk.body["zone"] != zone || disk.body["region"] != nil {
		t.Fatalf("zonal operation %v", disk.body)
	}
	expect(t, h.call("owner", "POST", flatRoot+"/zones/us-central1-a/disks?sourceImage=debian", map[string]any{"name": "img"}), 400, "INVALID_ARGUMENT")
	stored := h.call("owner", "GET", flatRoot+"/zones/us-central1-a/disks/data", nil).body
	if stored["zone"] != zone || stored["region"] != nil || stored["sizeGb"] != "10" || stored["labelFingerprint"] == "Y2xpZW50" {
		t.Fatalf("disk %v", stored)
	}

	// Set custom verbs write every request field and honor fingerprints.
	path := flatRoot + "/zones/us-central1-a/disks/data"
	expect(t, h.call("owner", "POST", path+"/setLabels", map[string]any{"labelFingerprint": "c3RhbGU=", "labels": map[string]any{"x": "y"}}), 409, "ABORTED")
	labeled := h.call("owner", "POST", path+"/setLabels", map[string]any{"labelFingerprint": stored["labelFingerprint"], "labels": map[string]any{"x": "y"}})
	expect(t, labeled, 200, "")
	if labeled.body["operationType"] != "setLabels" || h.callHeader("POST", path+"/setLabels") != "flat.disks.setLabels" {
		t.Fatalf("setLabels %v", labeled.body)
	}
	after := h.call("owner", "GET", path, nil).body
	if after["labels"].(map[string]any)["x"] != "y" || after["labels"].(map[string]any)["a"] != nil || after["labelFingerprint"] == stored["labelFingerprint"] {
		t.Fatalf("labels %v", after)
	}
	expect(t, h.call("owner", "POST", path+"/setLabels", map[string]any{}), 200, "")
	if h.call("owner", "GET", path, nil).body["labels"] != nil {
		t.Fatal("setLabels without labels clears them")
	}
	expect(t, h.call("owner", "POST", path+"/resize", map[string]any{"sizeGb": "5"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", path+"/resize", map[string]any{"sizeGb": "20"}), 200, "")
	if h.call("owner", "GET", path, nil).body["sizeGb"] != "20" {
		t.Fatal("resize persists")
	}
	expect(t, h.call("owner", "POST", flatRoot+"/zones/us-central1-a/disks/missing/resize", map[string]any{"sizeGb": "20"}), 404, "NOT_FOUND")
	ops := h.call("owner", "GET", flatRoot+"/zones/us-central1-a/operations", nil)
	if len(ops.body["items"].([]any)) != 4 {
		t.Fatalf("zone operations %v", ops.body)
	}

	// Literal routes win over overlapping templates; unbound methods are refused.
	expect(t, h.call("owner", "GET", flatRoot+"/global/networks/listUsable", nil), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "GET", flatRoot+"/zones/us-central1-a/disks/hosts", nil), 404, "NOT_FOUND")
	expect(t, h.call("owner", "GET", flatRoot+"/zones/us-central1-a/other/hosts", nil), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "GET", flatRoot+"/global/networks/app/getEffectiveFoo", nil), 501, "UNIMPLEMENTED")

	// A stored row of another collection is not addressable through this one.
	if _, err := h.engine.store.db.Exec("UPDATE gcp_catalog_resources SET collection='networks' WHERE id='data'"); err != nil {
		t.Fatal(err)
	}
	expect(t, h.call("owner", "GET", path, nil), 404, "NOT_FOUND")
}

func TestFlatSpecificationsFailClosed(t *testing.T) {
	model := flatModel(t)
	broken := map[string]func(*API){
		"selfLink":         func(a *API) { a.SelfLink = "http://x/" },
		"selfLink slash":   func(a *API) { a.SelfLink = "https://x" },
		"type url":         func(a *API) { a.SelfLink = "" },
		"set unknown":      func(a *API) { a.Bindings[9].Collection = "networks" },
		"set no body":      func(a *API) { a.Bindings[9].Method, a.Bindings[1].Method = "networks.get", "disks.setLabels" },
		"fingerprint":      func(a *API) { a.Bindings[3].Fingerprint = "etag" },
		"fingerprint kind": func(a *API) { a.Bindings[0].Fingerprint = "name" },
		"refuse":           func(a *API) { a.Bindings[0].Refuse = []string{"nope"} },
	}
	for name, mutate := range broken {
		api := flatAPI()
		mutate(api)
		if _, err := bindAPI(api, model); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	h := flatHarness(t)
	var flat Descriptor
	for _, d := range h.engine.Describe() {
		if d.Name == "flat" {
			flat = d
		}
	}
	disks, subnetworks := flat.Collections[2], flat.Collections[1]
	if disks.IDField != "name" || disks.Methods["setLabels"].Path == "" || disks.Methods["resize"].HTTPMethod != "POST" {
		t.Fatalf("custom methods are described by verb: %+v", disks)
	}
	for _, f := range subnetworks.Fields {
		if f.Name == "ipCidrRange" && !f.Required {
			t.Fatal("per-method required annotations are described")
		}
	}
	if _, ok := zoneRegion("us-central1"); ok {
		t.Fatal("a region is not a zone")
	}
	m := &Model{Name: "x", Version: "v1", Source: Source{Package: "google-api-python-client", SHA256: "x"}, Methods: map[string]*Method{"a.get": {Path: "projects/{project}"}}}
	if err := validateModel(m); err != nil || m.Methods["a.get"].FlatPath != "projects/{project}" {
		t.Fatal("methods with only an official path use it as their flat path")
	}
}

func TestMergePatchFollowsRFC7396(t *testing.T) {
	target := map[string]any{"a": map[string]any{"b": map[string]any{"c": "1", "d": "2"}, "e": "3"}, "f": []any{"x"}, "g": "scalar"}
	mergePatch(target, map[string]any{"a": map[string]any{"b": map[string]any{"c": "9"}}, "f": []any{"y"}, "g": map[string]any{"h": "i"}})
	a := target["a"].(map[string]any)
	if a["b"].(map[string]any)["c"] != "9" || a["b"].(map[string]any)["d"] != "2" || a["e"] != "3" || target["f"].([]any)[0] != "y" || target["g"].(map[string]any)["h"] != "i" {
		t.Fatalf("merge %v", target)
	}
	defaults(nil, target)
}

func TestFlatReferencesResolveAndProtectDeletion(t *testing.T) {
	h := flatHarness(t)
	subnets := flatRoot + "/regions/us-central1/subnetworks"
	ranges := 0
	subnet := func(name, network string) reply {
		ranges++
		return h.call("owner", "POST", subnets, map[string]any{"name": name, "ipCidrRange": "10.2." + strconv.Itoa(ranges) + ".0/24", "network": network})
	}
	expect(t, subnet("early", "global/networks/vpc"), 404, "NOT_FOUND")
	expect(t, h.call("owner", "POST", flatRoot+"/global/networks", map[string]any{"name": "vpc"}), 200, "")
	link := "https://www.googleapis.com/flat/v1/projects/tenkacloud-local/global/networks/vpc"
	for i, form := range []string{"global/networks/vpc", "projects/100000000001/global/networks/vpc", "https://flat.googleapis.com/flat/v1/projects/tenkacloud-local/global/networks/vpc", link} {
		name := "s" + strconv.Itoa(i)
		expect(t, subnet(name, form), 200, "")
		if got := h.call("owner", "GET", subnets+"/"+name, nil); got.body["network"] != link {
			t.Fatalf("reference %q stored as %v", form, got.body["network"])
		}
	}
	for form, code := range map[string]string{"vpc": "INVALID_ARGUMENT", "projects//global/networks/vpc": "INVALID_ARGUMENT",
		"projects/other/global/networks/vpc": "NOT_FOUND", "regions/us-central1/subnetworks/s0": "INVALID_ARGUMENT"} {
		r := subnet("bad", form)
		if r.code() != code {
			t.Fatalf("reference %q: %s", form, r.raw)
		}
	}
	// Nested list references resolve too, inside patches as well.
	peered := h.call("owner", "POST", subnets, map[string]any{"name": "peered", "ipCidrRange": "10.1.0.0/24", "peers": []any{map[string]any{"subnetwork": "regions/us-central1/subnetworks/s0", "weight": 1}}})
	expect(t, peered, 200, "")
	peers := h.call("owner", "GET", subnets+"/peered", nil).body["peers"].([]any)
	if peers[0].(map[string]any)["subnetwork"] != "https://www.googleapis.com/flat/v1/projects/tenkacloud-local/regions/us-central1/subnetworks/s0" {
		t.Fatalf("nested reference %v", peers)
	}
	expect(t, h.call("owner", "PATCH", subnets+"/peered", map[string]any{"peers": []any{map[string]any{"subnetwork": "regions/us-central1/subnetworks/gone"}}}), 404, "NOT_FOUND")
	expect(t, h.call("owner", "DELETE", subnets+"/s0", nil), 400, "FAILED_PRECONDITION")
	// A resource that references only itself can still be deleted.
	expect(t, h.call("owner", "PATCH", subnets+"/peered", map[string]any{"peers": []any{map[string]any{"subnetwork": "regions/us-central1/subnetworks/peered"}}}), 200, "")
	expect(t, h.call("owner", "DELETE", subnets+"/s0", nil), 200, "")
	expect(t, h.call("owner", "DELETE", subnets+"/peered", nil), 200, "")
	expect(t, h.call("owner", "DELETE", flatRoot+"/global/networks/vpc", nil), 400, "FAILED_PRECONDITION")
	for i := 1; i < 4; i++ {
		expect(t, h.call("owner", "DELETE", subnets+"/s"+strconv.Itoa(i), nil), 200, "")
	}
	expect(t, h.call("owner", "DELETE", flatRoot+"/global/networks/vpc", nil), 200, "")

	model := flatModel(t)
	for name, refs := range map[string]map[string][]string{
		"unknown field":      {"nope": {"networks"}},
		"not a list":         {"network[]": {"networks"}},
		"not a string":       {"peers": {"networks"}},
		"unknown collection": {"network": {"nope"}},
		"no targets":         {"network": nil},
	} {
		api := flatAPI()
		c := *api.Collections[1]
		c.References = refs
		api.Collections[1] = &c
		if _, err := bindAPI(api, model); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	h.call("owner", "POST", flatRoot+"/global/networks", map[string]any{"name": "keep"})
	h.call("owner", "POST", subnets, map[string]any{"name": "user", "ipCidrRange": "10.9.0.0/24", "network": "global/networks/keep"})
	if _, err := h.engine.store.db.Exec("UPDATE gcp_catalog_resources SET document='nope' WHERE id='user'"); err != nil {
		t.Fatal(err)
	}
	expect(t, h.call("owner", "DELETE", flatRoot+"/global/networks/keep", nil), 500, "INTERNAL")
}

func TestFlatWeakReferencesConflictsAndRestatedFields(t *testing.T) {
	h := flatHarness(t)
	disks := flatRoot + "/zones/us-central1-a/disks"
	expect(t, h.call("owner", "POST", disks, map[string]any{"name": "clone", "sourceDisk": "zones/us-central1-a/disks/origin"}), 404, "NOT_FOUND")
	expect(t, h.call("owner", "POST", disks, map[string]any{"name": "origin"}), 200, "")
	expect(t, h.call("owner", "POST", disks, map[string]any{"name": "clone", "sourceDisk": "zones/us-central1-a/disks/origin"}), 200, "")
	if h.call("owner", "GET", disks+"/clone", nil).body["sourceDisk"] != "https://www.googleapis.com/flat/v1/projects/tenkacloud-local/zones/us-central1-a/disks/origin" {
		t.Fatal("weak references are canonical")
	}
	// The source may be deleted; later updates do not re-check it.
	if _, err := h.engine.store.db.Exec("DELETE FROM gcp_catalog_resources WHERE id='origin'"); err != nil {
		t.Fatal(err)
	}
	expect(t, h.call("owner", "POST", disks+"/clone/resize", map[string]any{"sizeGb": "30"}), 200, "")

	subnets := flatRoot + "/regions/us-central1/subnetworks"
	expect(t, h.call("owner", "POST", subnets, map[string]any{"name": "a", "ipCidrRange": "10.0.0.0/24"}), 200, "")
	expect(t, h.call("owner", "POST", subnets, map[string]any{"name": "b", "ipCidrRange": "10.0.0.0/24"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", subnets, map[string]any{"name": "b", "ipCidrRange": "10.0.1.0/24"}), 200, "")
	// A resource never conflicts with itself; restated immutable fields are accepted.
	b := h.call("owner", "GET", subnets+"/b", nil).body
	expect(t, h.call("owner", "PATCH", subnets+"/b", map[string]any{"ipCidrRange": "10.0.1.0/24", "description": "same", "fingerprint": b["fingerprint"]}), 200, "")
	expect(t, h.call("owner", "PATCH", subnets+"/b", map[string]any{"ipCidrRange": "10.0.9.0/24"}), 400, "INVALID_ARGUMENT")
	if _, err := h.engine.store.db.Exec("UPDATE gcp_catalog_resources SET document=replace(document, '10.0.1.0', '10.0.0.0') WHERE id='b'"); err != nil {
		t.Fatal(err)
	}
	expect(t, h.call("owner", "PATCH", subnets+"/b", map[string]any{"description": "again"}), 400, "INVALID_ARGUMENT")
	if _, err := h.engine.store.db.Exec("UPDATE gcp_catalog_resources SET document='nope' WHERE id='a'"); err != nil {
		t.Fatal(err)
	}
	expect(t, h.call("owner", "POST", subnets, map[string]any{"name": "c", "ipCidrRange": "10.0.2.0/24"}), 500, "INTERNAL")
}
