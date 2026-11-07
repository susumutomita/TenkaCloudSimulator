package arm

import (
	"strings"
	"testing"
)

type hookCase struct {
	name   string
	method string
	path   string
	body   map[string]any
	status int
	code   string
}

func runCases(t *testing.T, h *harness, cases []hookCase) {
	t.Helper()
	for _, c := range cases {
		method := c.method
		if method == "" {
			method = "PUT"
		}
		r := h.do(method, c.path, c.body)
		if r.status != c.status || c.code != "" && r.code() != c.code {
			t.Fatalf("%s: want %d %s, got %d %s", c.name, c.status, c.code, r.status, r.raw)
		}
	}
}

func props(values map[string]any) map[string]any { return values }

func TestKeyVaultChecks(t *testing.T) {
	h := newHarness(t)
	h.group("rg1")
	h.vnet(vnetPath, "10.0.0.0/16")
	expect(t, h.do("PUT", vnetPath+"/subnets/kv"+netAPI, map[string]any{"properties": map[string]any{"addressPrefix": "10.0.1.0/24"}}), 201, "")
	vault := rg + "/providers/Microsoft.KeyVault/vaults/"
	body := func(extra map[string]any) map[string]any {
		p := map[string]any{"tenantId": tenant, "sku": map[string]any{"family": "A", "name": "standard"}}
		for k, v := range extra {
			p[k] = v
		}
		return map[string]any{"location": "eastus", "properties": p}
	}
	runCases(t, h, []hookCase{
		{"short name", "", vault + "ab" + kvAPI, body(nil), 400, "VaultNameNotValid"},
		{"bad name", "", vault + "1abc" + kvAPI, body(nil), 400, "InvalidResourceName"},
		{"double hyphen", "", vault + "a--bc" + kvAPI, body(nil), 400, "InvalidResourceName"},
		{"tenant", "", vault + "vault-a" + kvAPI, body(map[string]any{"tenantId": "11111111-1111-4111-8111-111111111111"}), 400, "InvalidRequestContent"},
		{"policy tenant", "", vault + "vault-a" + kvAPI, body(map[string]any{"accessPolicies": []any{map[string]any{"tenantId": "11111111-1111-4111-8111-111111111111", "objectId": "o", "permissions": map[string]any{}}}}), 400, "InvalidRequestContent"},
		{"recover", "", vault + "vault-a" + kvAPI, body(map[string]any{"createMode": "recover"}), 400, "InvalidRequestContent"},
		{"retention", "", vault + "vault-a" + kvAPI, body(map[string]any{"softDeleteRetentionInDays": 3}), 400, "InvalidRequestContent"},
		{"vnet rule", "", vault + "vault-a" + kvAPI, body(map[string]any{"accessPolicies": []any{map[string]any{"tenantId": tenant, "objectId": "o", "permissions": map[string]any{"secrets": []any{"get"}}}},
			"networkAcls": map[string]any{"virtualNetworkRules": []any{map[string]any{"id": vnetPath + "/subnets/kv"}}}}), 201, ""},
		{"missing subnet rule", "", vault + "vault-b" + kvAPI, body(map[string]any{"networkAcls": map[string]any{"virtualNetworkRules": []any{map[string]any{"id": vnetPath + "/subnets/none"}}}}), 400, "InvalidResourceReference"},
	})
	// A vault network rule does not block deleting its subnet.
	expect(t, h.do("DELETE", vnetPath+"/subnets/kv"+netAPI, nil), 200, "")
}

func TestNetworkChecks(t *testing.T) {
	h := newHarness(t)
	h.group("rg1")
	vnet := func(prefixes ...any) map[string]any {
		if prefixes == nil {
			prefixes = []any{}
		}
		return map[string]any{"location": "eastus", "properties": map[string]any{"addressSpace": map[string]any{"addressPrefixes": prefixes}}}
	}
	withProps := func(body map[string]any, key string, value any) map[string]any {
		body["properties"].(map[string]any)[key] = value
		return body
	}
	subnet := func(p map[string]any) map[string]any { return map[string]any{"properties": p} }
	runCases(t, h, []hookCase{
		{"no prefixes", "", vnetPath + netAPI, vnet(), 400, "InvalidAddressSpace"},
		{"bad prefix", "", vnetPath + netAPI, vnet("10.0.0.0/33"), 400, "InvalidAddressPrefixFormat"},
		{"host bits", "", vnetPath + netAPI, vnet("10.0.0.1/16"), 400, "InvalidAddressPrefixFormat"},
		{"overlap", "", vnetPath + netAPI, vnet("10.0.0.0/16", "10.0.128.0/17"), 400, "NetcfgInvalidAddressSpace"},
		{"ddos", "", vnetPath + netAPI, withProps(vnet("10.0.0.0/16"), "enableDdosProtection", true), 400, "InvalidRequestContent"},
		{"dns", "", vnetPath + netAPI, withProps(vnet("10.0.0.0/16"), "dhcpOptions", map[string]any{"dnsServers": []any{"dns.example"}}), 400, "InvalidRequestContent"},
		{"vnet", "", vnetPath + netAPI, withProps(vnet("10.0.0.0/16", "10.1.0.0/16"), "dhcpOptions", map[string]any{"dnsServers": []any{"10.0.0.4"}}), 201, ""},
		{"no subnet prefix", "", vnetPath + "/subnets/a" + netAPI, subnet(map[string]any{}), 400, "InvalidRequestFormat"},
		{"both prefixes", "", vnetPath + "/subnets/a" + netAPI, subnet(map[string]any{"addressPrefix": "10.0.1.0/24", "addressPrefixes": []any{"10.0.2.0/24"}}), 400, "InvalidRequestFormat"},
		{"bad subnet prefix", "", vnetPath + "/subnets/a" + netAPI, subnet(map[string]any{"addressPrefix": "nope"}), 400, "InvalidAddressPrefixFormat"},
		{"bad subnet list", "", vnetPath + "/subnets/a" + netAPI, subnet(map[string]any{"addressPrefixes": []any{"nope"}}), 400, "InvalidAddressPrefixFormat"},
		{"outside", "", vnetPath + "/subnets/a" + netAPI, subnet(map[string]any{"addressPrefix": "10.2.0.0/24"}), 400, "NetcfgSubnetRangeOutsideVnet"},
		{"self overlap", "", vnetPath + "/subnets/a" + netAPI, subnet(map[string]any{"addressPrefixes": []any{"10.0.1.0/24", "10.0.1.0/25"}}), 400, "NetcfgInvalidSubnet"},
		{"two prefixes", "", vnetPath + "/subnets/a" + netAPI, subnet(map[string]any{"addressPrefixes": []any{"10.0.1.0/24", "10.1.1.0/24"}}), 201, ""},
		{"sibling overlap", "", vnetPath + "/subnets/b" + netAPI, subnet(map[string]any{"addressPrefix": "10.1.1.128/25"}), 400, "NetcfgSubnetRangesOverlap"},
		{"group object", "", vnetPath + "/subnets/b" + netAPI, subnet(map[string]any{"addressPrefix": "10.0.2.0/24", "networkSecurityGroup": map[string]any{"location": "eastus"}}), 400, "InvalidRequestContent"},
		{"unsupported route table", "", vnetPath + "/subnets/b" + netAPI, subnet(map[string]any{"addressPrefix": "10.0.2.0/24", "routeTable": map[string]any{"id": "x"}}), 400, "InvalidRequestContent"},
		{"shrink below subnet", "", vnetPath + netAPI, vnet("10.0.0.0/16"), 400, "NetcfgSubnetRangeOutsideVnet"},
		{"grow", "", vnetPath + netAPI, vnet("10.0.0.0/16", "10.1.0.0/16", "10.2.0.0/16"), 200, ""},
	})
	rule := func(name string, priority any, direction string) map[string]any {
		return map[string]any{"name": name, "properties": map[string]any{"protocol": "Tcp", "access": "Allow", "direction": direction, "priority": priority,
			"sourceAddressPrefix": "*", "sourcePortRange": "*", "destinationAddressPrefix": "*", "destinationPortRange": "80"}}
	}
	nsg := func(rules ...any) map[string]any {
		return map[string]any{"location": "eastus", "properties": map[string]any{"securityRules": rules}}
	}
	asg := rule("asg", 300, "Inbound")
	asg["properties"].(map[string]any)["sourceApplicationSecurityGroups"] = []any{map[string]any{"id": "x"}}
	runCases(t, h, []hookCase{
		{"duplicate name", "", nsgPath + netAPI, nsg(rule("web", 100, "Inbound"), rule("WEB", 200, "Inbound")), 400, "SecurityRuleNameInvalid"},
		{"bad name", "", nsgPath + netAPI, nsg(rule("-bad", 100, "Inbound")), 400, "SecurityRuleNameInvalid"},
		{"priority range", "", nsgPath + netAPI, nsg(rule("web", 99, "Inbound")), 400, "SecurityRuleInvalidPriority"},
		{"priority conflict", "", nsgPath + netAPI, nsg(rule("a", 100, "Inbound"), rule("b", 100, "Inbound")), 400, "SecurityRuleConflict"},
		{"application groups", "", nsgPath + netAPI, nsg(asg), 400, "InvalidRequestContent"},
		{"directions", "", nsgPath + netAPI, nsg(rule("a", 100, "Inbound"), rule("b", 100, "Outbound")), 201, ""},
	})
	pip := rg + "/providers/Microsoft.Network/publicIPAddresses/"
	basic := expect(t, h.do("PUT", pip+"basic"+netAPI, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Basic"}}), 201, "").body
	if get(basic, "properties.publicIPAllocationMethod") != "Dynamic" || get(basic, "properties.publicIPAddressVersion") != "IPv4" || get(basic, "sku.tier") != "Regional" {
		t.Fatalf("basic defaults %v", basic)
	}
	standard := expect(t, h.do("PUT", pip+"standard"+netAPI, map[string]any{"location": "eastus"}), 201, "").body
	if get(standard, "sku.name") != "Standard" || get(standard, "properties.publicIPAllocationMethod") != "Static" {
		t.Fatalf("standard defaults %v", standard)
	}
	runCases(t, h, []hookCase{
		{"dynamic standard", "", pip + "x" + netAPI, map[string]any{"location": "eastus", "properties": map[string]any{"publicIPAllocationMethod": "Dynamic"}}, 400, "StandardSkuPublicIPAddressCannotBeDynamic"},
		{"idle timeout", "", pip + "x" + netAPI, map[string]any{"location": "eastus", "properties": map[string]any{"idleTimeoutInMinutes": 31}}, 400, "InvalidRequestContent"},
		{"ip address", "", pip + "x" + netAPI, map[string]any{"location": "eastus", "properties": map[string]any{"ipAddress": "203.0.113.4"}}, 400, "InvalidRequestContent"},
		{"name", "", pip + "-x" + netAPI, map[string]any{"location": "eastus"}, 400, "InvalidResourceName"},
	})
}

func TestPlatformChecks(t *testing.T) {
	h := newHarness(t)
	h.group("rg1")
	p := rg + "/providers/"
	la := p + "Microsoft.OperationalInsights/workspaces/logs-one?api-version=2025-07-01"
	ac := p + "Microsoft.AppConfiguration/configurationStores/"
	acAPI := "?api-version=2024-06-01"
	cr := p + "Microsoft.ContainerRegistry/registries/"
	eh := p + "Microsoft.EventHub/namespaces/"
	sb := p + "Microsoft.ServiceBus/namespaces/"
	msgAPI := "?api-version=2026-01-01"
	runCases(t, h, []hookCase{
		{"retention", "", la, map[string]any{"location": "eastus", "properties": map[string]any{"retentionInDays": 731}}, 400, "InvalidRequestContent"},
		{"workspace", "", la, map[string]any{"location": "eastus"}, 201, ""},
		{"workspace patch", "PATCH", la, map[string]any{"properties": map[string]any{"retentionInDays": 90}}, 200, ""},
		{"store sku", "", ac + "store-one" + acAPI, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Gold"}}, 400, "InvalidRequestContent"},
		{"store recover", "", ac + "store-one" + acAPI, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Free"}, "properties": map[string]any{"createMode": "Recover"}}, 400, "InvalidRequestContent"},
		{"store retention", "", ac + "store-one" + acAPI, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Free"}, "properties": map[string]any{"softDeleteRetentionInDays": 8}}, 400, "InvalidRequestContent"},
		{"store cmk", "", ac + "store-one" + acAPI, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Free"}, "properties": map[string]any{"encryption": map[string]any{}}}, 400, "InvalidRequestContent"},
		{"store", "", ac + "store-one" + acAPI, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Free"}, "properties": map[string]any{"createMode": "Default"}}, 201, ""},
		{"store page", "GET", p + "Microsoft.AppConfiguration/configurationStores" + acAPI + "&$skipToken=", nil, 200, ""},
		{"classic", "", cr + "registryone" + acrAPI, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Classic"}}, 400, "InvalidRequestContent"},
		{"network rules", "", cr + "registryone" + acrAPI, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Basic"}, "properties": map[string]any{"networkRuleSet": map[string]any{"defaultAction": "Deny"}}}, 400, "InvalidRequestContent"},
		{"premium rules", "", cr + "registryone" + acrAPI, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Premium"}, "properties": map[string]any{"networkRuleSet": map[string]any{"defaultAction": "Deny"}}}, 201, ""},
		{"registry name", "", cr + "reg-one" + acrAPI, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Basic"}}, 400, "InvalidResourceName"},
		{"reserved suffix", "", eh + "events-sb" + msgAPI, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Standard"}}, 400, "InvalidRequestContent"},
		{"tier", "", eh + "events-one" + msgAPI, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Standard", "tier": "Premium"}}, 400, "InvalidRequestContent"},
		{"namespace", "", eh + "events-one" + msgAPI, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Standard"}}, 201, ""},
		{"partitions", "", eh + "events-one/eventhubs/hub" + msgAPI, map[string]any{"properties": map[string]any{"partitionCount": 33}}, 400, "InvalidRequestContent"},
		{"capture", "", eh + "events-one/eventhubs/hub" + msgAPI, map[string]any{"properties": map[string]any{"captureDescription": map[string]any{}}}, 400, "InvalidRequestContent"},
		{"hub", "", eh + "events-one/eventhubs/hub" + msgAPI, map[string]any{"properties": map[string]any{"partitionCount": 8, "messageRetentionInDays": 2}}, 200, ""},
		{"decrease", "", eh + "events-one/eventhubs/hub" + msgAPI, map[string]any{"properties": map[string]any{"partitionCount": 4}}, 400, "InvalidRequestContent"},
		{"hub retention", "", eh + "events-one/eventhubs/hub" + msgAPI, map[string]any{"properties": map[string]any{"partitionCount": 8, "messageRetentionInDays": 9}}, 400, "InvalidRequestContent"},
		{"bus", "", sb + "queues-one" + msgAPI, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Standard"}}, 201, ""},
		{"forward missing", "", sb + "queues-one/queues/a" + msgAPI, map[string]any{"properties": map[string]any{"forwardTo": "b"}}, 400, "InvalidRequestContent"},
		{"forward self", "", sb + "queues-one/queues/a" + msgAPI, map[string]any{"properties": map[string]any{"forwardTo": "a"}}, 400, "InvalidRequestContent"},
		{"queue b", "", sb + "queues-one/queues/b" + msgAPI, map[string]any{"properties": map[string]any{"enablePartitioning": false}}, 200, ""},
		{"forward", "", sb + "queues-one/queues/a" + msgAPI, map[string]any{"properties": map[string]any{"forwardTo": "B", "forwardDeadLetteredMessagesTo": "b"}}, 200, ""},
		{"partitioning", "", sb + "queues-one/queues/b" + msgAPI, map[string]any{"properties": map[string]any{"enablePartitioning": true}}, 400, "PropertyChangeNotAllowed"},
		{"delivery count", "", sb + "queues-one/queues/b" + msgAPI, map[string]any{"properties": map[string]any{"maxDeliveryCount": 0}}, 400, "InvalidRequestContent"},
	})
	hub := expect(t, h.do("GET", eh+"events-one/eventhubs/hub"+msgAPI, nil), 200, "").body
	if ids := get(hub, "properties.partitionIds").([]any); len(ids) != 8 || ids[7] != "7" || hub["location"] != "eastus" {
		t.Fatalf("event hub %v", hub)
	}
	defaulted := expect(t, h.do("PUT", eh+"events-one/eventhubs/default"+msgAPI, map[string]any{}), 200, "").body
	if get(defaulted, "properties.partitionCount") != float64(4) && get(defaulted, "properties.partitionCount") != int64(4) {
		t.Fatalf("default partitions %v", defaulted)
	}
	queue := expect(t, h.do("GET", sb+"queues-one/queues/a"+msgAPI, nil), 200, "").body
	if get(queue, "properties.messageCount") != float64(0) || get(queue, "properties.status") != "Active" {
		t.Fatalf("queue %v", queue)
	}
	cosmos := p + "Microsoft.DocumentDB/databaseAccounts/acct?api-version=2026-03-15"
	account := func(locations ...any) map[string]any {
		return map[string]any{"location": "eastus", "properties": map[string]any{"databaseAccountOfferType": "Standard", "locations": locations}}
	}
	loc := func(name string, priority int) map[string]any {
		return map[string]any{"locationName": name, "failoverPriority": priority}
	}
	restore := account(loc("eastus", 0))
	restore["properties"].(map[string]any)["createMode"] = "Restore"
	backup := account(loc("eastus", 0))
	backup["properties"].(map[string]any)["backupPolicy"] = map[string]any{"type": "Hourly"}
	runCases(t, h, []hookCase{
		{"restore", "", cosmos, restore, 400, "InvalidRequestContent"},
		{"no locations", "", cosmos, account(), 400, "InvalidRequestContent"},
		{"unknown region", "", cosmos, account(loc("mars", 0)), 400, "InvalidRequestContent"},
		{"duplicate region", "", cosmos, account(loc("eastus", 0), loc("East US", 1)), 400, "InvalidRequestContent"},
		{"priority", "", cosmos, account(loc("eastus", 1)), 400, "InvalidRequestContent"},
		{"discriminator", "", cosmos, backup, 400, "InvalidRequestContent"},
		{"two regions", "", cosmos, account(loc("eastus", 0), loc("westeurope", 1)), 200, ""},
		{"plan sku", "", p + "Microsoft.Web/serverfarms/plan?api-version=2025-05-01", map[string]any{"location": "eastus"}, 400, "InvalidRequestContent"},
		{"plan ase", "", p + "Microsoft.Web/serverfarms/plan?api-version=2025-05-01", map[string]any{"location": "eastus", "sku": map[string]any{"name": "B1"}, "properties": map[string]any{"hostingEnvironmentProfile": map[string]any{"id": "x"}}}, 400, "InvalidRequestContent"},
	})
	if !strings.Contains(h.do("GET", cosmos, nil).raw, "westeurope") {
		t.Fatal("cosmos locations persist")
	}
}
