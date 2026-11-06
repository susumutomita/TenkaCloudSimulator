package catalog

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExceptionSelectionPrefersSpecificModeledNames(t *testing.T) {
	m := syntheticModel()
	for _, name := range []string{"AlreadyExistsException", "FederatedResourceAlreadyExistsException", "ConflictException", "StateMachineAlreadyExists", "PipelineNameInUseException",
		"EncryptionKeyNotFoundException", "RepositoryDoesNotExistException", "TagsLimitExceededException", "TooManyRequestsException", "UnknownResourceException", "InvalidParametersException", "TrailLimitExceededException"} {
		m.Shapes[name] = &Shape{Type: "structure", Exception: true}
	}
	b := &binding{spec: syntheticSpec(), model: m}
	cases := []struct {
		errors []string
		kind   string
		want   string
	}{
		{[]string{"FederatedResourceAlreadyExistsException", "AlreadyExistsException"}, "conflict", "AlreadyExistsException"},
		{[]string{"ConflictException", "StateMachineAlreadyExists"}, "conflict", "StateMachineAlreadyExists"},
		{[]string{"PipelineNameInUseException", "ValidationException"}, "conflict", "PipelineNameInUseException"},
		{[]string{"EncryptionKeyNotFoundException", "RepositoryDoesNotExistException"}, "notfound", "RepositoryDoesNotExistException"},
		{[]string{"TagsLimitExceededException", "TrailLimitExceededException"}, "limit", "TrailLimitExceededException"},
		{[]string{"TagsLimitExceededException", "TrailLimitExceededException"}, "tags", "TagsLimitExceededException"},
		{[]string{"TooManyRequestsException"}, "limit", "LimitExceededException"},
		{[]string{"UnknownResourceException"}, "notfound", "UnknownResourceException"},
		{[]string{"InvalidParametersException"}, "notfound", "InvalidParametersException"},
		{[]string{"ConflictException"}, "dependents", "ConflictException"},
	}
	for _, c := range cases {
		m.Operations["GetWidget"].Errors = c.errors
		if got, _ := b.errorCode("GetWidget", c.kind); got != c.want {
			t.Fatalf("%s %v = %s, want %s", c.kind, c.errors, got, c.want)
		}
	}
}

// wireModel uses official locationName wire keys at every level and a REST
// route with a trailing slash.
func wireModel() *Model {
	text := &Shape{Type: "string"}
	return &Model{
		Name: "wire", Source: Source{Package: "botocore", Version: "test", SHA256: "test", License: "Apache-2.0"},
		Metadata: Metadata{Protocol: "rest-json", SigningName: "wire"},
		Routes: map[string]Route{
			"CreateBroker": {HTTP: HTTP{Method: "POST", RequestURI: "/v1/brokers"}},
			"ListBrokers":  {HTTP: HTTP{Method: "GET", RequestURI: "/v1/brokers"}},
			"ListTags":     {HTTP: HTTP{Method: "GET", RequestURI: "/v1/tags/"}},
		},
		Operations: map[string]*Operation{
			"CreateBroker": {Name: "CreateBroker", HTTP: HTTP{Method: "POST", RequestURI: "/v1/brokers"}, Input: "CreateBrokerRequest", Output: "CreateBrokerResponse"},
			"ListBrokers":  {Name: "ListBrokers", HTTP: HTTP{Method: "GET", RequestURI: "/v1/brokers"}, Input: "ListBrokersRequest", Output: "ListBrokersResponse"},
		},
		Shapes: map[string]*Shape{
			"Text":     text,
			"Flag":     {Type: "boolean"},
			"Count":    {Type: "integer"},
			"Names":    {Type: "list", Member: &Member{Shape: "Text"}},
			"Settings": {Type: "structure", Members: map[string]Member{"EngineVersion": {Shape: "Text", LocationName: "engineVersion"}}},
			"Broker": {Type: "structure", Members: map[string]Member{"BrokerName": {Shape: "Text", LocationName: "brokerName"}, "BrokerArn": {Shape: "Text", LocationName: "brokerArn"},
				"Settings": {Shape: "Settings", LocationName: "settings"}, "Created": {Shape: "Text", LocationName: "created"}}},
			"Brokers":              {Type: "list", Member: &Member{Shape: "Broker"}},
			"CreateBrokerRequest":  {Type: "structure", Required: []string{"BrokerName"}, Members: map[string]Member{"BrokerName": {Shape: "Text", LocationName: "brokerName"}, "Settings": {Shape: "Settings", LocationName: "settings"}}},
			"CreateBrokerResponse": {Type: "structure", Members: map[string]Member{"BrokerArn": {Shape: "Text", LocationName: "brokerArn"}, "Settings": {Shape: "Settings", LocationName: "settings"}}},
			"ListBrokersRequest":   {Type: "structure", Members: map[string]Member{"Names": {Shape: "Names", Location: "querystring", LocationName: "names"}, "MaxRecords": {Shape: "Count", Location: "querystring", LocationName: "maxRecords"}, "Marker": {Shape: "Text", Location: "querystring", LocationName: "marker"}}},
			"ListBrokersResponse":  {Type: "structure", Members: map[string]Member{"Brokers": {Shape: "Brokers", LocationName: "brokers"}, "Missing": {Shape: "Names", LocationName: "missing"}, "Marker": {Shape: "Text", LocationName: "marker"}, "More": {Shape: "Flag", LocationName: "more"}}},
		},
	}
}

func wireSpec() *Service {
	return &Service{Model: "wire", Title: "Wire", IAMPrefix: "wire", Issues: []int{1}, Bounds: []string{"test"},
		Resources: []*Resource{{Kind: "broker", Shape: "Broker", ID: "name", ARN: "arn:{partition}:wire:{region}:{account}:broker/{name}", ARNMember: "BrokerArn", NameMember: "BrokerName",
			Generate: map[string]any{"Settings": map[string]any{"EngineVersion": "$name"}, "Created": "$now"}}},
		Ops: []Op{{Name: "CreateBroker", Kind: Create, Resource: "broker"}, {Name: "ListBrokers", Kind: List, Resource: "broker", Names: "Names", Failures: "Missing", HasMore: "More"}}}
}

func TestOfficialWireNamesTrailingSlashesAndStringFailures(t *testing.T) {
	h := newHarness(t, ecr)
	b, err := bindModel(wireSpec(), wireModel())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	created := h.rest("wire", "POST", "/v1/brokers", map[string]any{"brokerName": "b1"})
	expect(t, created, 200, "")
	if created.body["brokerArn"] != "arn:aws:wire:us-east-1:000000000000:broker/b1" || created.body["settings"].(map[string]any)["engineVersion"] != "b1" {
		t.Fatalf("wire names and nested generators: %v", created.body)
	}
	expect(t, h.rest("wire", "POST", "/v1/brokers", map[string]any{"BrokerName": "b2"}), 400, "")
	expect(t, h.rest("wire", "POST", "/v1/brokers", map[string]any{"brokerName": "b3", "settings": map[string]any{"engineVersion": "5.18"}}), 200, "")
	listed := h.rest("wire", "GET", "/v1/brokers?names=b3&names=ghost&maxRecords=1", nil)
	expect(t, listed, 200, "")
	brokers := listed.body["brokers"].([]any)
	if len(brokers) != 1 || brokers[0].(map[string]any)["settings"].(map[string]any)["engineVersion"] != "5.18" || listed.body["missing"].([]any)[0] != "ghost" || listed.body["more"] != false {
		t.Fatalf("list %v", listed.body)
	}
	paged := h.rest("wire", "GET", "/v1/brokers?maxRecords=1", nil)
	if paged.body["more"] != true || paged.body["marker"] == nil {
		t.Fatalf("has-more and marker pagination %v", paged.body)
	}
	expect(t, h.rest("wire", "GET", "/v1/tags/", nil), 501, "UnknownOperationException")
	expect(t, h.rest("wire", "GET", "/v1/tags", nil), 404, "UnknownOperationException")
	bad := wireSpec()
	bad.Ops[1].HasMore = "Nope"
	if _, err := bindModel(bad, wireModel()); err == nil {
		t.Fatal("has-more member must exist")
	}
}

func TestAlternateAndNestedLookupsIdempotentDeletesAndStoredSingletons(t *testing.T) {
	spec := syntheticSpec()
	spec.Ops[1].LookupAlt = []string{"Alias"}
	spec.Ops[3].IdempotentDelete = true
	model := syntheticModel()
	model.Shapes["IDInput"] = &Shape{Type: "structure", Members: map[string]Member{"WidgetId": {Shape: "Text"}, "Alias": {Shape: "Text"}}}
	h := newHarness(t, ecr)
	b, err := bindModel(spec, model)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	created := h.json1("synthetic", "CreateWidget", map[string]any{"Name": "alt"})
	expect(t, h.json1("synthetic", "GetWidget", map[string]any{"Alias": "alt"}), 200, "")
	expect(t, h.json1("synthetic", "GetWidget", map[string]any{"WidgetId": str(created.body["WidgetId"])}), 200, "")
	expect(t, h.json1("synthetic", "DeleteWidget", map[string]any{"WidgetId": "w-gone"}), 200, "")
	if got := lookupPath(map[string]any{"Query": map[string]any{"Name": "q"}}, "Query.Name"); got != "q" {
		t.Fatalf("dotted lookup %v", got)
	}
	if lookupPath(map[string]any{"Query": "x"}, "Query.Name") != nil {
		t.Fatal("dotted lookup through a scalar is empty")
	}
	f := featureHarness(t)
	g := f.binding("features")
	g.ops["DeleteSettings"] = &Op{Name: "DeleteSettings", Kind: Delete, Resource: "settings"}
	q := &request{e: f.engine, b: g, op: g.ops["DeleteSettings"], official: &Operation{Name: "DeleteSettings"}, region: "us-east-1", input: map[string]any{}, ctx: t.Context(), w: httptest.NewRecorder()}
	if _, err := q.remove(); err == nil || !strings.Contains(err.Error(), "no stored configuration") {
		t.Fatalf("deleting an unstored singleton is not found: %v", err)
	}
	tagged := &request{b: f.binding("features")}
	if tagged.tagMember("PutRuleInput") != "Tags" {
		t.Fatal("the official Tags member is preferred")
	}
}
