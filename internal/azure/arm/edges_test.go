package arm

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	rgAPI  = "?api-version=2025-04-01"
	kvAPI  = "?api-version=2026-05-15"
	netAPI = "?api-version=2026-03-01"
)

func TestRoutingVersionsAndUnboundOperations(t *testing.T) {
	h := newHarness(t)
	h.group("rg1")
	expect(t, h.do("GET", "/nowhere"+kvAPI, nil), 404, "NotFound")
	expect(t, h.do("GET", "/"+kvAPI, nil), 404, "NotFound")
	// Generic official routes such as Resources_GetById apply only with their own api-version.
	expect(t, h.do("GET", "/nowhere"+rgAPI, nil), 501, "NotImplemented")
	expect(t, h.do("GET", rg+"/providers/Microsoft.Nothing/things/x"+kvAPI, nil), 400, "InvalidResourceNamespace")
	expect(t, h.do("GET", rg+"/providers/Microsoft.KeyVault/widgets/x"+kvAPI, nil), 404, "InvalidResourceType")
	expect(t, h.do("POST", rg+"/providers/Microsoft.KeyVault/vaults/x"+kvAPI, nil), 405, "MethodNotAllowed")
	expect(t, h.do("GET", rg+"/providers/Microsoft.KeyVault/vaults/a%2Fb"+kvAPI, nil), 400, "InvalidRequestUri")
	expect(t, h.do("GET", rg+"/providers/Microsoft.KeyVault/vaults/x", nil), 400, "MissingApiVersionParameter")
	expect(t, h.do("GET", rg+"/providers/Microsoft.KeyVault/vaults/x?api-version=", nil), 400, "MissingApiVersionParameter")
	expect(t, h.do("GET", rg+"/providers/Microsoft.KeyVault/vaults/x?api-version=2026-05-15&api-version=2026-05-15", nil), 400, "InvalidApiVersionParameter")
	expect(t, h.do("GET", rg+"/providers/Microsoft.KeyVault/vaults/x?api-version=2025-05-01", nil), 400, "InvalidApiVersionParameter")
	unbound := expect(t, h.do("POST", "/subscriptions/"+sub+"/providers/Microsoft.KeyVault/checkNameAvailability"+kvAPI, map[string]any{"name": "x"}), 501, "NotImplemented")
	if !strings.Contains(unbound.raw, "Vaults_CheckNameAvailability") {
		t.Fatalf("501 names the official operation: %s", unbound.raw)
	}
	// A multi-segment {scope} route is matched and refused, never guessed.
	expect(t, h.do("GET", rg+"/providers/Microsoft.Web/sites/s/providers/Microsoft.Resources/deployments/d"+rgAPI, nil), 501, "NotImplemented")
	// The most specific official route wins over generic Resources_* routes.
	expect(t, h.do("GET", rg+"/providers/Microsoft.Network/virtualNetworks/none/subnets"+netAPI, nil), 404, "ParentResourceNotFound")
	recorder := httptest.NewRecorder()
	h.engine.ServeHTTP(recorder, httptest.NewRequest("GET", rg+rgAPI, nil))
	if recorder.Code != 401 || recorder.Header().Get("X-Simulator-Azure-Operation") != "Microsoft.Resources/ResourceGroups_Get" {
		t.Fatalf("missing subject %d %v", recorder.Code, recorder.Header())
	}
}

func TestAuthorityParametersAndBodies(t *testing.T) {
	h := newHarness(t)
	h.group("rg1")
	vault := rg + "/providers/Microsoft.KeyVault/vaults/vault-one"
	expect(t, h.call("intruder", "GET", vault+kvAPI, nil), 403, "AuthorizationFailed")
	denied := expect(t, h.do("GET", "/subscriptions/11111111-1111-4111-8111-111111111111/resourceGroups/rg1/providers/Microsoft.KeyVault/vaults/vault-one"+kvAPI, nil), 403, "AuthorizationFailed")
	if !strings.Contains(denied.raw, "Microsoft.KeyVault/vaults/read") {
		t.Fatalf("denial names the descriptive action: %s", denied.raw)
	}
	expect(t, h.call("intruder", "PUT", "/subscriptions/"+sub+"/resourcegroups/x"+rgAPI, map[string]any{"location": "eastus"}), 403, "AuthorizationFailed")
	expect(t, h.do("GET", "/subscriptions/not-a-uuid/resourceGroups/rg1/providers/Microsoft.KeyVault/vaults/v"+kvAPI, nil), 400, "InvalidResourceName")
	expect(t, h.do("GET", rg+"/providers/Microsoft.OperationalInsights/workspaces/a?api-version=2025-07-01", nil), 400, "InvalidResourceName")
	expect(t, h.do("GET", "/subscriptions/"+sub+"/resourcegroups"+rgAPI+"&$filter=tagName%20eq%20%27a%27", nil), 400, "InvalidQueryParameterValue")
	expect(t, h.do("GET", "/subscriptions/"+sub+"/resourcegroups"+rgAPI+"&bogus=1", nil), 400, "InvalidQueryParameterValue")
	expect(t, h.do("GET", "/subscriptions/"+sub+"/resourcegroups"+rgAPI+"&$top=1&$top=2", nil), 400, "InvalidQueryParameterValue")
	expect(t, h.do("GET", "/subscriptions/"+sub+"/resourcegroups"+rgAPI+"&$top=x", nil), 400, "InvalidQueryParameterValue")
	expect(t, h.do("GET", "/subscriptions/"+sub+"/resourcegroups"+rgAPI+"&$top=0", nil), 400, "InvalidQueryParameterValue")
	expect(t, h.do("GET", "/subscriptions/"+sub+"/resourcegroups"+rgAPI+"&$skiptoken=!!!", nil), 400, "InvalidSkipToken")
	expect(t, h.do("GET", "/subscriptions/"+sub+"/resourcegroups"+rgAPI+"&$skiptoken=bm9wZQ", nil), 400, "InvalidSkipToken")
	expect(t, h.do("DELETE", "/subscriptions/"+sub+"/resourcegroups/rg1"+rgAPI+"&forceDeletionTypes=x", nil), 400, "InvalidQueryParameterValue")
	expect(t, h.do("GET", vault+kvAPI, map[string]any{"x": 1}), 400, "InvalidRequestContent")
	expect(t, h.do("PUT", vault+kvAPI, ""), 400, "InvalidRequestContent")
	expect(t, h.do("PUT", vault+kvAPI, "{bad"), 400, "InvalidRequestContent")
	expect(t, h.do("PUT", vault+kvAPI, `{"location":"eastus"}{}`), 400, "InvalidRequestContent")
	expect(t, h.do("PUT", vault+kvAPI, `[]`), 400, "InvalidRequestContent")
	expect(t, h.do("PUT", vault+kvAPI, `{"location":"eastus","padding":"`+strings.Repeat("x", maxRequestBytes)+`"}`), 413, "RequestEntityTooLarge")
	body := map[string]any{"location": "eastus", "properties": map[string]any{"tenantId": tenant, "sku": map[string]any{"family": "A", "name": "standard"}}}
	expect(t, h.do("PUT", vault+kvAPI, body), 201, "")
	expect(t, h.do("PUT", strings.Replace(vault, "vault-one", "VAULT-ONE", 1)+kvAPI, body), 200, "")
	got := expect(t, h.do("GET", strings.ToUpper(vault[:len(vault)-9])+"vault-one"+kvAPI, nil), 200, "")
	if got.body["name"] != "vault-one" {
		t.Fatalf("segments and names are case-insensitive and keep the created casing: %v", got.body)
	}
}

func TestDescriptorsCoverReviewedTypes(t *testing.T) {
	h := newHarness(t)
	d := h.engine.Describe()
	if len(d) != len(Providers()) {
		t.Fatalf("descriptors %d", len(d))
	}
	found := map[string]TypeDescriptor{}
	for _, p := range d {
		if p.Official < len(p.Operations) || p.Source.Commit == "" {
			t.Fatalf("provider %+v", p)
		}
		for _, td := range p.Types {
			found[td.Type] = td
		}
	}
	vault := found["Microsoft.KeyVault/vaults"]
	if !vault.Location || vault.Operations["listAll"].ID != "Vaults_ListBySubscription" || vault.Operations["list"].Items != "value" || vault.Defaults["properties.tenantId"] != tenant {
		t.Fatalf("vault descriptor %+v", vault)
	}
	if vault.Fields[0].Path != "properties.tenantId" || !vault.Fields[0].Required || vault.Fields[0].Default != tenant {
		t.Fatalf("vault fields %+v", vault.Fields)
	}
	subnet := found["Microsoft.Network/virtualNetworks/subnets"]
	if subnet.Parent != "Microsoft.Network/virtualNetworks" || subnet.Location || len(subnet.References) != 1 {
		t.Fatalf("subnet descriptor %+v", subnet)
	}
	vnet := found["Microsoft.Network/virtualNetworks"]
	if vnet.Fields[0].Type != "array:string" {
		t.Fatalf("vnet fields %+v", vnet.Fields)
	}
	groups := found["Microsoft.Resources/resourceGroups"]
	if groups.Operations["list"].ID != "ResourceGroups_List" || groups.Operations["head"].Method != "HEAD" {
		t.Fatalf("group descriptor %+v", groups)
	}
	pip := found["Microsoft.Network/publicIPAddresses"]
	if len(pip.Fields[0].Enum) == 0 {
		t.Fatalf("enum fields %+v", pip.Fields)
	}
}

func TestOwnerBindingIsPersistedAndNeverReassigned(t *testing.T) {
	h := newHarness(t)
	cfg := testConfig(h)
	cfg.Owner = "someone-else"
	if _, err := New(h.core.Database(), cfg, Providers()); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("owner drift must be refused: %v", err)
	}
	cfg = testConfig(h)
	if _, err := New(h.core.Database(), cfg, Providers()); err != nil {
		t.Fatalf("the same binding reopens: %v", err)
	}
	for _, broken := range []func(*Config){
		func(c *Config) { c.Environment = "" },
		func(c *Config) { c.Subscription = "x" },
		func(c *Config) { c.Tenant = "x" },
		func(c *Config) { c.Owner = "" },
		func(c *Config) { c.Locations = nil },
		func(c *Config) { c.Subject = nil },
		func(c *Config) { c.BasePath = "relative" },
	} {
		c := testConfig(h)
		broken(&c)
		if _, err := New(h.core.Database(), c, nil); err == nil {
			t.Fatal("incomplete configuration accepted")
		}
	}
	if _, err := newStore(nil, cfg); err == nil {
		t.Fatal("nil database accepted")
	}
	c := testConfig(h)
	c.Now = nil
	c.Environment = "other"
	engine, err := New(h.core.Database(), c, nil)
	if err != nil || engine.cfg.Now == nil {
		t.Fatalf("default clock %v", err)
	}
	if _, err := New(h.core.Database(), testConfig(h), []*Provider{{Namespace: "Microsoft.Missing"}}); err == nil {
		t.Fatal("a provider without an embedded model is refused")
	}
	if _, ok := h.engine.cfg.Subject(context.Background()); ok {
		t.Fatal("the test subject extractor needs a subject")
	}
}
