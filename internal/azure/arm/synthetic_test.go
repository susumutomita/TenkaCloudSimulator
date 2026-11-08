package arm

import (
	"encoding/json"
	"strings"
	"testing"
)

const synthPrefix = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Test.Widgets/widgets"

// syntheticModel is a minimal official-shaped model for fail-closed tests.
func syntheticModel() *Model {
	source := Source{Repository: "github.com/Azure/azure-rest-api-specs", Commit: strings.Repeat("a", 40), License: "MIT", Files: []SourceFile{{Path: "specification/x.json", SHA256: strings.Repeat("b", 64)}}}
	path := func(name, in string) *Parameter {
		return &Parameter{Name: name, In: in, Required: true, Type: "string"}
	}
	common := []*Parameter{path("subscriptionId", "path"), path("resourceGroupName", "path"), {Name: "api-version", In: "query", Required: true}}
	item := append(append([]*Parameter{}, common...), path("widgetName", "path"))
	ops := map[string]*Operation{
		"Widgets_Put":    {Method: "PUT", Path: synthPrefix + "/{widgetName}", Parameters: append(append([]*Parameter{}, item...), &Parameter{Name: "body", In: "body", Required: true, Schema: "Widget"}), Responses: map[string]string{"200": "Widget", "201": "Widget"}},
		"Widgets_Get":    {Method: "GET", Path: synthPrefix + "/{widgetName}", Parameters: item, Responses: map[string]string{"200": "Widget"}},
		"Widgets_List":   {Method: "GET", Path: synthPrefix, Parameters: append(append([]*Parameter{}, common...), &Parameter{Name: "$top", In: "query", Type: "integer"}), Responses: map[string]string{"200": "WidgetList"}, Pageable: &Pageable{ItemName: "value"}},
		"Widgets_Delete": {Method: "DELETE", Path: synthPrefix + "/{widgetName}", Parameters: item, Responses: map[string]string{"200": ""}},
	}
	routes := map[string]*Route{}
	for id, op := range ops {
		routes[id] = &Route{Method: op.Method, Path: op.Path}
	}
	routes["Widgets_Reset"] = &Route{Method: "POST", Path: synthPrefix + "/{widgetName}/reset"}
	return &Model{Namespace: "Test.Widgets", APIVersion: "2026-01-01", Source: source, Routes: routes, Operations: ops, Definitions: map[string]*Schema{
		"Widget":     {Type: "object", Properties: map[string]*Schema{"id": {Type: "string", ReadOnly: true}, "name": {Type: "string", ReadOnly: true}, "location": {Type: "string"}, "properties": {Type: "object", Properties: map[string]*Schema{"size": {Type: "integer"}}}}},
		"WidgetList": {Type: "object", Properties: map[string]*Schema{"value": {Type: "array", Items: &Schema{Ref: "Widget"}}}},
	}}
}

func syntheticProvider() *Provider {
	return &Provider{Namespace: "Test.Widgets", APIVersion: "2026-01-01", Title: "Widgets", Issues: []int{1}, Bounds: []string{"test"},
		Types: []*ResourceType{{Name: "widgets", Title: "Widgets", Limit: 2, Form: []string{"properties.size"}}},
		Bindings: []Binding{{Operation: "Widgets_Put", Kind: Put, Type: "widgets"}, {Operation: "Widgets_Get", Kind: Get, Type: "widgets"},
			{Operation: "Widgets_List", Kind: List, Type: "widgets", Query: []string{"$top"}}, {Operation: "Widgets_Delete", Kind: Delete, Type: "widgets"}}}
}

func bindSynthetic(h *harness, p *Provider, m *Model) error {
	if err := validateModel(m); err != nil {
		return err
	}
	return h.engine.add(p, m)
}

func TestSyntheticProviderServesWithoutNextLinks(t *testing.T) {
	h := newHarness(t)
	if err := bindSynthetic(h, syntheticProvider(), syntheticModel()); err != nil {
		t.Fatal(err)
	}
	h.group("rg1")
	api := "?api-version=2026-01-01"
	base := rg + "/providers/Test.Widgets/widgets/"
	expect(t, h.do("PUT", base+"a"+api, map[string]any{"location": "eastus", "properties": map[string]any{"size": 1}}), 201, "")
	expect(t, h.do("PUT", base+"b"+api, map[string]any{"location": "eastus"}), 201, "")
	expect(t, h.do("PUT", base+"c"+api, map[string]any{"location": "eastus"}), 409, "QuotaExceeded")
	page := expect(t, h.do("GET", rg+"/providers/Test.Widgets/widgets"+api+"&$top=1", nil), 200, "").body
	if len(page["value"].([]any)) != 2 || page["nextLink"] != nil {
		t.Fatalf("an operation without nextLinkName returns the whole scope: %v", page)
	}
	expect(t, h.do("POST", base+"a/reset"+api, nil), 501, "NotImplemented")
	expect(t, h.do("DELETE", base+"zz"+api, nil), 404, "ResourceNotFound")
	expect(t, h.do("DELETE", base+"a"+api, nil), 200, "")
	if err := h.engine.add(syntheticProvider(), syntheticModel()); err == nil {
		t.Fatal("a provider registers once")
	}
}

func TestSpecificationsAndModelsFailClosed(t *testing.T) {
	h := newHarness(t)
	broken := map[string]func(*Provider, *Model){
		"title":          func(p *Provider, m *Model) { p.Title = "" },
		"api-version":    func(p *Provider, m *Model) { p.APIVersion = "2020-01-01" },
		"type title":     func(p *Provider, m *Model) { p.Types[0].Title = "" },
		"type limit":     func(p *Provider, m *Model) { p.Types[0].Limit = 0 },
		"duplicate type": func(p *Provider, m *Model) { p.Types = append(p.Types, p.Types[0]) },
		"orphan child": func(p *Provider, m *Model) {
			p.Types = append(p.Types, &ResourceType{Name: "gadgets/parts", Title: "x", Limit: 1})
		},
		"unknown op":   func(p *Provider, m *Model) { p.Bindings[0].Operation = "Widgets_Missing" },
		"unknown type": func(p *Provider, m *Model) { p.Bindings[0].Type = "gadgets" },
		"twice":        func(p *Provider, m *Model) { p.Bindings = append(p.Bindings, p.Bindings[0]) },
		"unbound":      func(p *Provider, m *Model) { p.Bindings = p.Bindings[1:] },
		"no get": func(p *Provider, m *Model) {
			p.Bindings = p.Bindings[:1]
			delete(m.Operations, "Widgets_Get")
			delete(m.Operations, "Widgets_List")
			delete(m.Operations, "Widgets_Delete")
		},
		"no 200": func(p *Provider, m *Model) {
			m.Operations["Widgets_Put"].Responses = map[string]string{"201": "Widget"}
		},
		"bad ref":    func(p *Provider, m *Model) { p.Types[0].Refs = []Ref{{Path: "x", Type: "nope"}} },
		"bad form":   func(p *Provider, m *Model) { p.Types[0].Form = []string{"properties.missing"} },
		"kind":       func(p *Provider, m *Model) { p.Bindings[1].Kind = "destroy" },
		"method":     func(p *Provider, m *Model) { p.Bindings[1].Kind = Delete },
		"list shape": func(p *Provider, m *Model) { p.Bindings[1].Kind = List },
		"item shape": func(p *Provider, m *Model) { p.Bindings[2].Kind = Get },
		"pageable":   func(p *Provider, m *Model) { m.Operations["Widgets_List"].Pageable = nil },
		"body": func(p *Provider, m *Model) {
			m.Operations["Widgets_Put"].Parameters = m.Operations["Widgets_Get"].Parameters
		},
		"query": func(p *Provider, m *Model) { p.Bindings[1].Query = []string{"$expand"} },
		"no subscription": func(p *Provider, m *Model) {
			setPath(m, "Widgets_Get", "/tenants/{t}/providers/Test.Widgets/widgets/{widgetName}")
		},
		"namespace": func(p *Provider, m *Model) {
			setPath(m, "Widgets_Get", "/subscriptions/{s}/resourceGroups/{g}/providers/Other.Ns/widgets/{widgetName}")
		},
		"variable type": func(p *Provider, m *Model) {
			setPath(m, "Widgets_Get", "/subscriptions/{s}/resourceGroups/{g}/providers/Test.Widgets/{type}/{widgetName}")
		},
		"double type": func(p *Provider, m *Model) {
			setPath(m, "Widgets_Get", "/subscriptions/{s}/resourceGroups/{g}/providers/Test.Widgets/widgets/parts/{widgetName}")
		},
		"other type": func(p *Provider, m *Model) {
			setPath(m, "Widgets_Get", "/subscriptions/{s}/resourceGroups/{g}/providers/Test.Widgets/gadgets/{widgetName}")
		},
		"subscription": func(p *Provider, m *Model) {
			setPath(m, "Widgets_Get", "/subscriptions/{s}/providers/Test.Widgets/widgets/{widgetName}")
		},
		"group path": func(p *Provider, m *Model) {
			p.Namespace = "Microsoft.Resources"
			m.Namespace = "Microsoft.Resources"
			p.Types[0].Name = "resourceGroups"
			retype(p, "resourceGroups")
		},
	}
	for name, mutate := range broken {
		p, m := syntheticProvider(), syntheticModel()
		mutate(p, m)
		if err := bindSynthetic(h, p, m); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	models := map[string]func(*Model){
		"provenance":      func(m *Model) { m.Source.License = "" },
		"file":            func(m *Model) { m.Source.Files[0].SHA256 = "short" },
		"route":           func(m *Model) { delete(m.Routes, "Widgets_Get") },
		"body definition": func(m *Model) { m.Operations["Widgets_Put"].Parameters[4].Schema = "Missing" },
		"response":        func(m *Model) { m.Operations["Widgets_Get"].Responses["200"] = "Missing" },
		"parameter":       func(m *Model) { m.Operations["Widgets_Get"].Parameters[3].Pattern = "(?<=x)" },
		"reference":       func(m *Model) { m.Definitions["X"] = &Schema{Ref: "Missing"} },
		"subtype": func(m *Model) {
			m.Definitions["X"] = &Schema{Discriminator: "k", Subtypes: map[string]string{"a": "Missing"}}
		},
		"pattern":    func(m *Model) { m.Definitions["X"] = &Schema{Type: "string", Pattern: "(?<=x)"} },
		"additional": func(m *Model) { m.Definitions["X"] = &Schema{Type: "object", Additional: json.RawMessage(`"x"`)} },
	}
	for name, mutate := range models {
		m := syntheticModel()
		mutate(m)
		if validateModel(m) == nil {
			t.Fatalf("model %s accepted", name)
		}
	}
	if _, err := loadModel("Missing.Namespace"); err == nil {
		t.Fatal("a missing model is refused")
	}
	if len(embeddedModels()) != len(Providers()) {
		t.Fatal("embedded models must match reviewed providers")
	}
	m := syntheticModel()
	m.Definitions["Loop"] = &Schema{Ref: "Loop"}
	if m.resolve(&Schema{Ref: "Loop"}) == nil || m.isObject(&Schema{Ref: "Missing"}) {
		t.Fatal("resolution stays bounded")
	}
}

func setPath(m *Model, id, path string) {
	m.Operations[id].Path = path
	m.Routes[id].Path = path
}

func retype(p *Provider, name string) {
	for i := range p.Bindings {
		p.Bindings[i].Type = name
	}
}
