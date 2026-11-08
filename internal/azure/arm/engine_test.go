package arm

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type subjectKey struct{}

const (
	sub    = "00000000-0000-4000-8000-000000000001"
	tenant = "00000000-0000-4000-8000-000000000000"
	rg     = "/subscriptions/" + sub + "/resourceGroups/rg1"
)

type harness struct {
	t      *testing.T
	engine *Engine
	core   *core.Store
	path   string
	now    time.Time
}

func testConfig(h *harness) Config {
	return Config{Environment: "local", Subscription: sub, Tenant: tenant, Owner: "owner", Locations: []string{"eastus", "westeurope", "japaneast"}, BasePath: "/_simulator/providers/azure-arm",
		Subject: func(ctx context.Context) (string, bool) { s, ok := ctx.Value(subjectKey{}).(string); return s, ok }, Now: func() time.Time { return h.now }}
}

func newHarness(t *testing.T, providers ...*Provider) *harness {
	t.Helper()
	h := &harness{t: t, now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), path: filepath.Join(t.TempDir(), "state.db")}
	store, err := core.New(h.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if len(providers) == 0 {
		providers = Providers()
	}
	engine, err := New(store.Database(), testConfig(h), providers)
	if err != nil {
		t.Fatal(err)
	}
	h.engine, h.core = engine, store
	return h
}

type reply struct {
	status int
	body   map[string]any
	raw    string
}

func (h *harness) call(subject, method, path string, body any) reply {
	h.t.Helper()
	var data []byte
	switch v := body.(type) {
	case nil:
	case map[string]any:
		if v != nil {
			data, _ = json.Marshal(v)
		}
	case string:
		data = []byte(v)
	default:
		data, _ = json.Marshal(v)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Host = "127.0.0.1:4566"
	if subject != "" {
		req = req.WithContext(context.WithValue(req.Context(), subjectKey{}, subject))
	}
	recorder := httptest.NewRecorder()
	h.engine.ServeHTTP(recorder, req)
	out := reply{status: recorder.Code, raw: recorder.Body.String()}
	_ = json.Unmarshal(recorder.Body.Bytes(), &out.body)
	return out
}

func (h *harness) do(method, path string, body any) reply {
	h.t.Helper()
	return h.call("owner", method, path, body)
}

func (r reply) code() string {
	if e, ok := r.body["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func expect(t *testing.T, r reply, status int, code string) reply {
	t.Helper()
	if r.status != status || code != "" && r.code() != code {
		t.Fatalf("want %d %s, got %d %s: %s", status, code, r.status, r.code(), r.raw)
	}
	return r
}

func v(api string) string { return "?api-version=" + api }

func get(document map[string]any, dotted string) any {
	value, _ := lookup(document, dotted)
	return value
}

func (h *harness) group(name string) {
	h.t.Helper()
	expect(h.t, h.do("PUT", "/subscriptions/"+sub+"/resourcegroups/"+name+v("2025-04-01"), map[string]any{"location": "eastus"}), 201, "")
}

func TestResourceGroupLifecycle(t *testing.T) {
	h := newHarness(t)
	path := "/subscriptions/" + sub + "/resourcegroups/rg1" + v("2025-04-01")
	created := expect(t, h.do("PUT", path, map[string]any{"location": "East US", "tags": map[string]any{"env": "dev"}, "id": "ignored", "properties": map[string]any{"provisioningState": "Failed"}}), 201, "")
	if created.body["id"] != rg || created.body["name"] != "rg1" || created.body["location"] != "eastus" || get(created.body, "properties.provisioningState") != "Succeeded" || created.body["type"] != "Microsoft.Resources/resourceGroups" {
		t.Fatalf("created %v", created.body)
	}
	if get(created.body, "systemData.createdBy") != "owner" {
		t.Fatalf("systemData %v", created.body)
	}
	updated := expect(t, h.do("PUT", "/subscriptions/"+strings.ToUpper(sub)+"/resourceGroups/RG1"+v("2025-04-01"), map[string]any{"location": "eastus", "tags": map[string]any{"env": "prod"}}), 200, "")
	if updated.body["name"] != "rg1" || get(updated.body, "tags.env") != "prod" {
		t.Fatalf("update keeps the original name: %v", updated.body)
	}
	expect(t, h.do("PUT", path, map[string]any{"location": "westeurope"}), 409, "InvalidResourceGroupLocation")
	expect(t, h.do("HEAD", path, nil), 204, "")
	expect(t, h.do("HEAD", "/subscriptions/"+sub+"/resourcegroups/none"+v("2025-04-01"), nil), 404, "")
	expect(t, h.do("PATCH", path, map[string]any{"tags": map[string]any{"a": "b"}}), 200, "")
	got := expect(t, h.do("GET", path, nil), 200, "")
	if get(got.body, "tags.a") != "b" || get(got.body, "tags.env") != nil {
		t.Fatalf("patch replaces tags: %v", got.body)
	}
	expect(t, h.do("PATCH", path, map[string]any{"name": "other"}), 400, "InvalidRequestContent")
	h.group("rg2")
	page := expect(t, h.do("GET", "/subscriptions/"+sub+"/resourcegroups"+v("2025-04-01")+"&$top=1", nil), 200, "")
	if len(page.body["value"].([]any)) != 1 || !strings.HasPrefix(page.body["nextLink"].(string), "http://127.0.0.1:4566/_simulator/providers/azure-arm/subscriptions/") {
		t.Fatalf("page %v", page.body)
	}
	next := strings.TrimPrefix(page.body["nextLink"].(string), "http://127.0.0.1:4566/_simulator/providers/azure-arm")
	second := expect(t, h.do("GET", next, nil), 200, "")
	if items := second.body["value"].([]any); len(items) != 1 || items[0].(map[string]any)["name"] != "rg2" || second.body["nextLink"] != nil {
		t.Fatalf("second page %v", second.body)
	}
	expect(t, h.do("DELETE", path, nil), 200, "")
	expect(t, h.do("GET", path, nil), 404, "ResourceGroupNotFound")
	expect(t, h.do("DELETE", path, nil), 404, "ResourceGroupNotFound")
}

type product struct {
	api, path, list, listAll string
	body, patch              map[string]any
	check                    func(t *testing.T, created map[string]any)
}

func products() []product {
	p := rg + "/providers/"
	all := "/subscriptions/" + sub + "/providers/"
	return []product{
		{"2026-05-15", p + "Microsoft.KeyVault/vaults/vault-one", p + "Microsoft.KeyVault/vaults", all + "Microsoft.KeyVault/vaults",
			map[string]any{"location": "eastus", "properties": map[string]any{"tenantId": tenant, "sku": map[string]any{"family": "A", "name": "Standard"}, "createMode": "default"}},
			map[string]any{"tags": map[string]any{"k": "v"}, "properties": map[string]any{"enableRbacAuthorization": true}},
			func(t *testing.T, c map[string]any) {
				if get(c, "properties.sku.name") != "standard" || get(c, "properties.createMode") != nil || get(c, "properties.provisioningState") != "Succeeded" {
					t.Fatalf("vault %v", c)
				}
			}},
		{"2024-11-30", p + "Microsoft.ManagedIdentity/userAssignedIdentities/id-one", p + "Microsoft.ManagedIdentity/userAssignedIdentities", all + "Microsoft.ManagedIdentity/userAssignedIdentities",
			map[string]any{"location": "eastus"}, map[string]any{"tags": map[string]any{"k": "v"}},
			func(t *testing.T, c map[string]any) {
				if get(c, "properties.tenantId") != tenant || len(get(c, "properties.principalId").(string)) != 36 {
					t.Fatalf("identity %v", c)
				}
			}},
		{"2026-03-01", p + "Microsoft.Network/virtualNetworks/vnet-one", p + "Microsoft.Network/virtualNetworks", all + "Microsoft.Network/virtualNetworks",
			map[string]any{"location": "eastus", "properties": map[string]any{"addressSpace": map[string]any{"addressPrefixes": []any{"10.0.0.0/16"}}}}, map[string]any{"tags": map[string]any{"k": "v"}},
			func(t *testing.T, c map[string]any) {
				if !strings.HasPrefix(c["etag"].(string), `W/"`) || get(c, "properties.resourceGuid") == nil {
					t.Fatalf("vnet %v", c)
				}
			}},
		{"2026-03-01", p + "Microsoft.Network/networkSecurityGroups/nsg-one", p + "Microsoft.Network/networkSecurityGroups", all + "Microsoft.Network/networkSecurityGroups",
			map[string]any{"location": "eastus", "properties": map[string]any{"securityRules": []any{map[string]any{"name": "ssh", "properties": map[string]any{"protocol": "Tcp", "access": "Allow", "direction": "Inbound", "priority": 100, "sourceAddressPrefix": "*", "sourcePortRange": "*", "destinationAddressPrefix": "*", "destinationPortRange": "22"}}}}},
			map[string]any{"tags": map[string]any{"k": "v"}},
			func(t *testing.T, c map[string]any) {
				rule := get(c, "properties.securityRules").([]any)[0].(map[string]any)
				if !strings.HasSuffix(rule["id"].(string), "/securityRules/ssh") {
					t.Fatalf("nsg %v", c)
				}
			}},
		{"2026-03-01", p + "Microsoft.Network/publicIPAddresses/pip-one", p + "Microsoft.Network/publicIPAddresses", all + "Microsoft.Network/publicIPAddresses",
			map[string]any{"location": "eastus", "sku": map[string]any{"name": "Standard"}}, map[string]any{"tags": map[string]any{"k": "v"}},
			func(t *testing.T, c map[string]any) {
				if get(c, "properties.publicIPAllocationMethod") != "Static" || get(c, "properties.ipAddress") != nil {
					t.Fatalf("pip %v", c)
				}
			}},
		{"2025-07-01", p + "Microsoft.OperationalInsights/workspaces/logs-one", p + "Microsoft.OperationalInsights/workspaces", all + "Microsoft.OperationalInsights/workspaces",
			map[string]any{"location": "eastus", "properties": map[string]any{"retentionInDays": 30}}, map[string]any{"tags": map[string]any{"k": "v"}},
			func(t *testing.T, c map[string]any) {
				if get(c, "properties.customerId") == nil || get(c, "properties.sku.name") != "PerGB2018" {
					t.Fatalf("workspace %v", c)
				}
			}},
		{"2024-06-01", p + "Microsoft.AppConfiguration/configurationStores/config-one", p + "Microsoft.AppConfiguration/configurationStores", all + "Microsoft.AppConfiguration/configurationStores",
			map[string]any{"location": "eastus", "sku": map[string]any{"name": "Standard"}}, map[string]any{"tags": map[string]any{"k": "v"}}, nil},
		{"2025-11-01", p + "Microsoft.ContainerRegistry/registries/registryone", p + "Microsoft.ContainerRegistry/registries", all + "Microsoft.ContainerRegistry/registries",
			map[string]any{"location": "eastus", "sku": map[string]any{"name": "Basic"}}, map[string]any{"tags": map[string]any{"k": "v"}},
			func(t *testing.T, c map[string]any) {
				if get(c, "sku.tier") != "Basic" || get(c, "properties.loginServer") != nil {
					t.Fatalf("registry %v", c)
				}
			}},
		{"2026-01-01", p + "Microsoft.EventHub/namespaces/events-one", p + "Microsoft.EventHub/namespaces", all + "Microsoft.EventHub/namespaces",
			map[string]any{"location": "eastus", "sku": map[string]any{"name": "Standard"}}, map[string]any{"tags": map[string]any{"k": "v"}}, nil},
		{"2026-01-01", p + "Microsoft.ServiceBus/namespaces/queues-one", p + "Microsoft.ServiceBus/namespaces", all + "Microsoft.ServiceBus/namespaces",
			map[string]any{"location": "eastus", "sku": map[string]any{"name": "Standard"}}, map[string]any{"tags": map[string]any{"k": "v"}}, nil},
		{"2026-03-15", p + "Microsoft.DocumentDB/databaseAccounts/cosmos-one", p + "Microsoft.DocumentDB/databaseAccounts", all + "Microsoft.DocumentDB/databaseAccounts",
			map[string]any{"location": "eastus", "kind": "GlobalDocumentDB", "properties": map[string]any{"databaseAccountOfferType": "Standard", "locations": []any{map[string]any{"locationName": "East US", "failoverPriority": 0}},
				"backupPolicy": map[string]any{"type": "Periodic", "periodicModeProperties": map[string]any{"backupIntervalInMinutes": 240}}}},
			map[string]any{"tags": map[string]any{"k": "v"}}, nil},
		{"2025-05-01", p + "Microsoft.Web/serverfarms/plan-one", p + "Microsoft.Web/serverfarms", all + "Microsoft.Web/serverfarms",
			map[string]any{"location": "eastus", "kind": "linux", "sku": map[string]any{"name": "B1", "tier": "Basic"}, "properties": map[string]any{"reserved": true}},
			map[string]any{"properties": map[string]any{"perSiteScaling": true}},
			func(t *testing.T, c map[string]any) {
				if get(c, "properties.resourceGroup") != "rg1" || get(c, "properties.status") != "Ready" {
					t.Fatalf("plan %v", c)
				}
			}},
	}
}

func TestEveryProductLifecycle(t *testing.T) {
	h := newHarness(t)
	h.group("rg1")
	for _, p := range products() {
		t.Run(p.path[strings.Index(p.path, "providers/")+10:], func(t *testing.T) {
			ver := v(p.api)
			created := expect(t, h.do("PUT", p.path+ver, p.body), map[bool]int{true: 201, false: 200}[h.engine.typeFor(t, p.path).kinds[Put][0].op.Responses["201"] != ""], "")
			if !strings.EqualFold(created.body["id"].(string), p.path) {
				t.Fatalf("id %v", created.body["id"])
			}
			if p.check != nil {
				p.check(t, created.body)
			}
			expect(t, h.do("PUT", p.path+ver, p.body), 200, "")
			got := expect(t, h.do("GET", p.path+ver, nil), 200, "")
			if got.body["name"] != created.body["name"] {
				t.Fatalf("get %v", got.body)
			}
			patched := expect(t, h.do("PATCH", p.path+ver, p.patch), 200, "")
			if p.patch["tags"] != nil && get(patched.body, "tags.k") != "v" {
				t.Fatalf("patch %v", patched.body)
			}
			for _, list := range []string{p.list, p.listAll} {
				page := expect(t, h.do("GET", list+ver, nil), 200, "")
				if len(page.body["value"].([]any)) != 1 {
					t.Fatalf("list %s %v", list, page.body)
				}
			}
			expect(t, h.do("GET", p.path+"?api-version=2015-01-01", nil), 400, "InvalidApiVersionParameter")
			expect(t, h.do("GET", p.path, nil), 400, "MissingApiVersionParameter")
			expect(t, h.call("intruder", "GET", p.path+ver, nil), 403, "AuthorizationFailed")
			deleted := h.do("DELETE", p.path+ver, nil)
			if deleted.status != 200 && deleted.status != 204 {
				t.Fatalf("delete %d %s", deleted.status, deleted.raw)
			}
			expect(t, h.do("GET", p.path+ver, nil), 404, "ResourceNotFound")
		})
	}
}

func (e *Engine) typeFor(t *testing.T, path string) *boundType {
	parts := splitPath(path)
	for i, part := range parts {
		if part == "providers" {
			return e.types[strings.ToLower(parts[i+1]+"/"+parts[i+2])]
		}
	}
	t.Fatal("no type")
	return nil
}
