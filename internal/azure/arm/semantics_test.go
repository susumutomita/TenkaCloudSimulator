package arm

import (
	"strings"
	"testing"
)

const (
	vnetPath = rg + "/providers/Microsoft.Network/virtualNetworks/vnet"
	nsgPath  = rg + "/providers/Microsoft.Network/networkSecurityGroups/nsg"
	msiAPI   = "?api-version=2024-11-30"
	acrAPI   = "?api-version=2025-11-01"
)

func (h *harness) vnet(path, prefix string) {
	h.t.Helper()
	expect(h.t, h.do("PUT", path+netAPI, map[string]any{"location": "eastus", "properties": map[string]any{"addressSpace": map[string]any{"addressPrefixes": []any{prefix}}}}), 201, "")
}

func TestReferencesBlockDeletionAndValidateTargets(t *testing.T) {
	h := newHarness(t)
	h.group("rg1")
	h.group("rg2")
	h.vnet(vnetPath, "10.0.0.0/16")
	other := "/subscriptions/" + sub + "/resourceGroups/rg2/providers/Microsoft.Network/networkSecurityGroups/remote"
	expect(t, h.do("PUT", nsgPath+netAPI, map[string]any{"location": "eastus"}), 201, "")
	expect(t, h.do("PUT", other+netAPI, map[string]any{"location": "eastus"}), 201, "")
	subnet := func(name, prefix, group string) reply {
		props := map[string]any{"addressPrefix": prefix}
		if group != "" {
			props["networkSecurityGroup"] = map[string]any{"id": group}
		}
		return h.do("PUT", vnetPath+"/subnets/"+name+netAPI, map[string]any{"properties": props})
	}
	for _, bad := range []string{nsgPath + "-missing", vnetPath, "/subscriptions/11111111-1111-4111-8111-111111111111/resourceGroups/rg1/providers/Microsoft.Network/networkSecurityGroups/nsg", "not-an-id", rg + "/providers/Microsoft.Network"} {
		expect(t, subnet("bad", "10.0.9.0/24", bad), 400, "InvalidResourceReference")
	}
	expect(t, subnet("a", "10.0.1.0/24", strings.ToUpper(nsgPath)), 201, "")
	expect(t, subnet("b", "10.0.2.0/24", other), 201, "")
	expect(t, h.do("DELETE", nsgPath+netAPI, nil), 409, "InUseNetworkSecurityGroupCannotBeDeleted")
	// A cross-group reference blocks deleting the referenced group atomically.
	expect(t, h.do("DELETE", "/subscriptions/"+sub+"/resourcegroups/rg2"+rgAPI, nil), 409, "InUseNetworkSecurityGroupCannotBeDeleted")
	expect(t, h.do("GET", other+netAPI, nil), 200, "")
	// Updating the subnet without the group releases the reference.
	expect(t, subnet("a", "10.0.1.0/24", ""), 200, "")
	expect(t, h.do("DELETE", nsgPath+netAPI, nil), 200, "")
	// Deleting the source releases its references; the target then deletes.
	expect(t, h.do("DELETE", vnetPath+"/subnets/b"+netAPI, nil), 200, "")
	expect(t, h.do("DELETE", "/subscriptions/"+sub+"/resourcegroups/rg2"+rgAPI, nil), 200, "")
	// A group containing both ends of a reference deletes together.
	expect(t, h.do("PUT", nsgPath+netAPI, map[string]any{"location": "eastus"}), 201, "")
	expect(t, subnet("a", "10.0.1.0/24", nsgPath), 200, "")
	expect(t, h.do("DELETE", "/subscriptions/"+sub+"/resourcegroups/rg1"+rgAPI, nil), 200, "")
	var count int
	if err := h.engine.store.db.QueryRow("SELECT (SELECT count(*) FROM azure_arm_resources)+(SELECT count(*) FROM azure_arm_references)").Scan(&count); err != nil || count != 0 {
		t.Fatalf("cascade left %d rows (%v)", count, err)
	}
}

func TestIdentitiesAreGeneratedAndResolved(t *testing.T) {
	h := newHarness(t)
	h.group("rg1")
	msi := rg + "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/pull"
	created := expect(t, h.do("PUT", msi+msiAPI, map[string]any{"location": "eastus"}), 201, "").body
	registry := rg + "/providers/Microsoft.ContainerRegistry/registries/registryone"
	body := func(identity map[string]any) map[string]any {
		return map[string]any{"location": "eastus", "sku": map[string]any{"name": "Basic"}, "identity": identity}
	}
	first := expect(t, h.do("PUT", registry+acrAPI, body(map[string]any{"type": "SystemAssigned"})), 201, "").body
	principal := get(first, "identity.principalId")
	if principal == nil || get(first, "identity.tenantId") != tenant {
		t.Fatalf("system identity %v", first)
	}
	again := expect(t, h.do("PUT", registry+acrAPI, body(map[string]any{"type": "SystemAssigned, UserAssigned", "userAssignedIdentities": map[string]any{msi: map[string]any{}}})), 200, "").body
	assigned := get(again, "identity.userAssignedIdentities").(map[string]any)[msi].(map[string]any)
	if get(again, "identity.principalId") != principal || assigned["principalId"] != get(created, "properties.principalId") || assigned["clientId"] != get(created, "properties.clientId") {
		t.Fatalf("identity resolution %v", again)
	}
	none := expect(t, h.do("PUT", registry+acrAPI, body(map[string]any{"type": "None"})), 200, "").body
	if get(none, "identity.principalId") != nil {
		t.Fatalf("None clears principals %v", none)
	}
	expect(t, h.do("PUT", registry+acrAPI, body(map[string]any{"type": "UserAssigned"})), 400, "InvalidIdentity")
	expect(t, h.do("PUT", registry+acrAPI, body(map[string]any{"type": "SystemAssigned", "userAssignedIdentities": map[string]any{msi: map[string]any{}}})), 400, "InvalidIdentity")
	for _, id := range []string{msi + "-missing", "/subscriptions/" + sub + "/resourceGroups/rg1/providers/Microsoft.KeyVault/vaults/x", "nope"} {
		expect(t, h.do("PUT", registry+acrAPI, body(map[string]any{"type": "UserAssigned", "userAssignedIdentities": map[string]any{id: map[string]any{}}})), 400, "InvalidIdentity")
	}
	// Deleting an assigned identity is allowed, as in Azure.
	expect(t, h.do("PUT", registry+acrAPI, body(map[string]any{"type": "UserAssigned", "userAssignedIdentities": map[string]any{msi: map[string]any{}}})), 200, "")
	expect(t, h.do("DELETE", msi+msiAPI, nil), 200, "")
	expect(t, h.do("DELETE", msi+msiAPI, nil), 204, "")
}

func TestCreateOnlyLocationAndIdentityMembers(t *testing.T) {
	h := newHarness(t)
	h.group("rg1")
	plan := rg + "/providers/Microsoft.Web/serverfarms/plan?api-version=2025-05-01"
	expect(t, h.do("PUT", plan, map[string]any{"location": "Japan East", "sku": map[string]any{"name": "B1"}, "properties": map[string]any{"reserved": true}}), 200, "")
	if got := h.do("GET", plan, nil).body; got["location"] != "japaneast" {
		t.Fatalf("display names normalize %v", got)
	}
	expect(t, h.do("PATCH", plan, map[string]any{"properties": map[string]any{"reserved": false}}), 400, "PropertyChangeNotAllowed")
	kept := expect(t, h.do("PUT", plan, map[string]any{"location": "japaneast", "sku": map[string]any{"name": "B2"}}), 200, "").body
	if get(kept, "properties.reserved") != true {
		t.Fatalf("an omitted create-only member keeps its value %v", kept)
	}
	expect(t, h.do("PUT", plan, map[string]any{"location": "eastus", "sku": map[string]any{"name": "B2"}}), 409, "InvalidResourceLocation")
	expect(t, h.do("PUT", plan, map[string]any{"location": "Japan East", "sku": map[string]any{"name": "B2"}}), 200, "")
	cosmos := rg + "/providers/Microsoft.DocumentDB/databaseAccounts/acct?api-version=2026-03-15"
	body := map[string]any{"location": "eastus", "properties": map[string]any{"databaseAccountOfferType": "Standard", "locations": []any{map[string]any{"locationName": "eastus", "failoverPriority": 0}}}}
	expect(t, h.do("PUT", cosmos, body), 200, "")
	expect(t, h.do("PATCH", cosmos, map[string]any{"location": "westeurope"}), 400, "PropertyChangeNotAllowed")
	expect(t, h.do("PATCH", cosmos, map[string]any{"location": "East US", "tags": map[string]any{"a": "b"}}), 200, "")
	expect(t, h.do("PUT", rg+"/providers/Microsoft.Web/serverfarms/nolocation?api-version=2025-05-01", map[string]any{"sku": map[string]any{"name": "B1"}}), 400, "InvalidRequestContent")
	expect(t, h.do("PUT", rg+"/providers/Microsoft.Network/publicIPAddresses/ip"+netAPI, map[string]any{}), 400, "LocationRequired")
	expect(t, h.do("PUT", rg+"/providers/Microsoft.Network/publicIPAddresses/ip"+netAPI, map[string]any{"location": "mars"}), 400, "LocationNotAvailableForResourceType")
	expect(t, h.do("PUT", rg+"/providers/Microsoft.Network/publicIPAddresses/ip"+netAPI, map[string]any{"location": "eastus", "id": rg + "/other"}), 400, "InvalidRequestContent")
	expect(t, h.do("PUT", rg+"/providers/Microsoft.Network/publicIPAddresses/ip"+netAPI, map[string]any{"location": "eastus", "id": rg + "/providers/Microsoft.Network/publicIPAddresses/IP"}), 201, "")
	expect(t, h.do("PUT", vnetPath+"/subnets/s"+netAPI, map[string]any{"name": "other", "properties": map[string]any{"addressPrefix": "10.0.0.0/24"}}), 400, "InvalidRequestContent")
}

func TestMissingScopesAndDeletionStatuses(t *testing.T) {
	h := newHarness(t)
	vault := rg + "/providers/Microsoft.KeyVault/vaults/vault-one" + kvAPI
	expect(t, h.do("GET", vault, nil), 404, "ResourceGroupNotFound")
	expect(t, h.do("DELETE", vault, nil), 404, "ResourceGroupNotFound")
	expect(t, h.do("PUT", vault, map[string]any{"location": "eastus", "properties": map[string]any{"tenantId": tenant, "sku": map[string]any{"family": "A", "name": "standard"}}}), 404, "ResourceGroupNotFound")
	expect(t, h.do("GET", rg+"/providers/Microsoft.KeyVault/vaults"+kvAPI, nil), 404, "ResourceGroupNotFound")
	expect(t, h.do("PATCH", vault, map[string]any{"tags": map[string]any{}}), 404, "ResourceGroupNotFound")
	h.group("rg1")
	expect(t, h.do("DELETE", vault, nil), 204, "")
	expect(t, h.do("PATCH", vault, map[string]any{"tags": map[string]any{}}), 404, "ResourceNotFound")
	expect(t, h.do("GET", vnetPath+"/subnets/s"+netAPI, nil), 404, "ParentResourceNotFound")
	expect(t, h.do("DELETE", vnetPath+"/subnets/s"+netAPI, nil), 404, "ParentResourceNotFound")
	expect(t, h.do("PUT", vnetPath+"/subnets/s"+netAPI, map[string]any{"properties": map[string]any{"addressPrefix": "10.0.0.0/24"}}), 404, "ParentResourceNotFound")
	h.vnet(vnetPath, "10.0.0.0/16")
	expect(t, h.do("GET", vnetPath+"/subnets/s"+netAPI, nil), 404, "ResourceNotFound")
	expect(t, h.do("DELETE", vnetPath+"/subnets/s"+netAPI, nil), 204, "")
	cosmos := rg + "/providers/Microsoft.DocumentDB/databaseAccounts/acct?api-version=2026-03-15"
	expect(t, h.do("PUT", cosmos, map[string]any{"location": "eastus", "properties": map[string]any{"databaseAccountOfferType": "Standard", "locations": []any{map[string]any{"locationName": "eastus", "failoverPriority": 0}}}}), 200, "")
	expect(t, h.do("DELETE", cosmos, nil), 204, "")
	expect(t, h.do("DELETE", rg+"/providers/Microsoft.OperationalInsights/workspaces/logs-one?api-version=2025-07-01&force=true", nil), 204, "")
	expect(t, h.do("DELETE", rg+"/providers/Microsoft.OperationalInsights/workspaces/logs-one?api-version=2025-07-01&force=maybe", nil), 400, "InvalidQueryParameterValue")
}

func TestListsPageWithinTheirScope(t *testing.T) {
	h := newHarness(t)
	h.group("rg1")
	ns := rg + "/providers/Microsoft.EventHub/namespaces/events-one"
	api := "?api-version=2026-01-01"
	expect(t, h.do("PUT", ns+api, map[string]any{"location": "eastus", "sku": map[string]any{"name": "Standard"}}), 201, "")
	for _, name := range []string{"a", "b", "c"} {
		expect(t, h.do("PUT", ns+"/eventhubs/"+name+api, map[string]any{}), 200, "")
	}
	page := expect(t, h.do("GET", ns+"/eventhubs"+api+"&$top=2", nil), 200, "").body
	if len(page["value"].([]any)) != 2 || page["nextLink"] == nil {
		t.Fatalf("page %v", page)
	}
	next := strings.TrimPrefix(page["nextLink"].(string), "http://127.0.0.1:4566/_simulator/providers/azure-arm")
	if !strings.Contains(next, "%24top=2") {
		t.Fatalf("nextLink keeps $top: %s", next)
	}
	rest := expect(t, h.do("GET", next, nil), 200, "").body
	if len(rest["value"].([]any)) != 1 {
		t.Fatalf("rest %v", rest)
	}
	skipped := expect(t, h.do("GET", ns+"/eventhubs"+api+"&$skip=2", nil), 200, "").body
	if items := skipped["value"].([]any); len(items) != 1 || items[0].(map[string]any)["name"] != "c" {
		t.Fatalf("skip %v", skipped)
	}
	token := next[strings.Index(next, "%24skiptoken=")+len("%24skiptoken="):]
	token = token[:strings.Index(token, "&")]
	expect(t, h.do("GET", "/subscriptions/"+sub+"/providers/Microsoft.EventHub/namespaces"+api+"&$skiptoken="+token, nil), 400, "InvalidSkipToken")
	expect(t, h.do("GET", rg+"/providers/Microsoft.EventHub/namespaces/missing-space/eventhubs"+api, nil), 404, "ParentResourceNotFound")
	all := expect(t, h.do("GET", "/subscriptions/"+sub+"/providers/Microsoft.EventHub/namespaces"+api, nil), 200, "").body
	if len(all["value"].([]any)) != 1 {
		t.Fatalf("subscription list %v", all)
	}
	// Deleting the namespace deletes its event hubs.
	expect(t, h.do("DELETE", ns+api, nil), 200, "")
	expect(t, h.do("GET", ns+"/eventhubs/a"+api, nil), 404, "ParentResourceNotFound")
}

func TestLimitsGlobalNamesAndBudget(t *testing.T) {
	h := newHarness(t)
	h.group("rg1")
	h.group("rg2")
	body := map[string]any{"location": "eastus", "properties": map[string]any{"tenantId": tenant, "sku": map[string]any{"family": "A", "name": "standard"}}}
	expect(t, h.do("PUT", rg+"/providers/Microsoft.KeyVault/vaults/shared"+kvAPI, body), 201, "")
	expect(t, h.do("PUT", "/subscriptions/"+sub+"/resourceGroups/rg2/providers/Microsoft.KeyVault/vaults/SHARED"+kvAPI, body), 409, "Conflict")
	for i := 0; i < 24; i++ {
		expect(t, h.do("PUT", rg+"/providers/Microsoft.KeyVault/vaults/vault-"+string(rune('a'+i))+kvAPI, body), 201, "")
	}
	expect(t, h.do("PUT", rg+"/providers/Microsoft.KeyVault/vaults/vault-z1"+kvAPI, body), 409, "QuotaExceeded")
	db := h.engine.store.db
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	total, err := storedBytes(tx)
	if err != nil {
		t.Fatal(err)
	}
	filler := `{"x":"` + strings.Repeat("f", int(maxStoredBytes-total)-64) + `"}`
	if _, err := tx.Exec("INSERT INTO azure_arm_resources VALUES('/filler','/filler',?,'/x','filler/filler',?,999999,1)", sub, filler); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	expect(t, h.do("PATCH", rg+"/providers/Microsoft.KeyVault/vaults/shared"+kvAPI, map[string]any{"tags": map[string]any{"padding": strings.Repeat("p", 200)}}), 429, "QuotaExceeded")
	if got := h.do("GET", rg+"/providers/Microsoft.KeyVault/vaults/shared"+kvAPI, nil).body; get(got, "tags.padding") != nil {
		t.Fatal("a refused write must roll back")
	}
	if _, err := db.Exec("DELETE FROM azure_arm_resources WHERE key='/filler'"); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.Begin()
	for i := 0; i < maxResources; i++ {
		if _, err := tx.Exec("INSERT INTO azure_arm_resources VALUES(?,?,?,'/many','many/items','{}',?,1)", "/many/"+string(rune(i+0x4e00)), "/many", sub, 2000000+i); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	expect(t, h.do("PUT", "/subscriptions/"+sub+"/resourcegroups/rg3"+rgAPI, map[string]any{"location": "eastus"}), 409, "QuotaExceeded")
}

func TestTagsUnsupportedMembersAndSecrets(t *testing.T) {
	h := newHarness(t)
	h.group("rg1")
	path := "/subscriptions/" + sub + "/resourcegroups/rg1" + rgAPI
	many := map[string]any{}
	for i := 0; i < 51; i++ {
		many["t"+string(rune('A'+i))] = "v"
	}
	expect(t, h.do("PATCH", path, map[string]any{"tags": many}), 400, "InvalidTagCount")
	expect(t, h.do("PATCH", path, map[string]any{"tags": map[string]any{"a/b": "v"}}), 400, "InvalidTagNameCharacters")
	expect(t, h.do("PATCH", path, map[string]any{"tags": map[string]any{"a": strings.Repeat("v", 257)}}), 400, "InvalidTagNameCharacters")
	expect(t, h.do("PUT", vnetPath+netAPI, map[string]any{"location": "eastus", "properties": map[string]any{"subnets": []any{}}}), 400, "InvalidRequestContent")
	// A reviewed secret member is refused before anything is stored.
	saved := network.Types[0].Secrets
	network.Types[0].Secrets = []string{"properties.dhcpOptions"}
	defer func() { network.Types[0].Secrets = saved }()
	secret := expect(t, h.do("PUT", vnetPath+netAPI, map[string]any{"location": "eastus", "properties": map[string]any{"dhcpOptions": map[string]any{"dnsServers": []any{}}}}), 400, "InvalidRequestContent")
	if !strings.Contains(secret.raw, "secret") {
		t.Fatalf("secret refusal %s", secret.raw)
	}
}

func TestStorageFaultsAreInternalAndLeaveNoPartialState(t *testing.T) {
	h := newHarness(t)
	h.group("rg1")
	db := h.engine.store.db
	exec := func(statement string) {
		t.Helper()
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	vault := rg + "/providers/Microsoft.KeyVault/vaults/vault-one" + kvAPI
	body := map[string]any{"location": "eastus", "properties": map[string]any{"tenantId": tenant, "sku": map[string]any{"family": "A", "name": "standard"}}}
	exec("CREATE TRIGGER f BEFORE INSERT ON azure_arm_resources BEGIN SELECT RAISE(ABORT, 'x'); END")
	expect(t, h.do("PUT", vault, body), 500, "InternalServerError")
	exec("DROP TRIGGER f")
	expect(t, h.do("PUT", vault, body), 201, "")
	exec("CREATE TRIGGER g BEFORE UPDATE ON azure_arm_resources BEGIN SELECT RAISE(IGNORE); END")
	expect(t, h.do("PUT", vault, body), 409, "Conflict")
	exec("DROP TRIGGER g")
	exec("CREATE TRIGGER r BEFORE INSERT ON azure_arm_references BEGIN SELECT RAISE(ABORT, 'x'); END")
	h.vnet(vnetPath, "10.0.0.0/16")
	expect(t, h.do("PUT", nsgPath+netAPI, map[string]any{"location": "eastus"}), 201, "")
	expect(t, h.do("PUT", vnetPath+"/subnets/s"+netAPI, map[string]any{"properties": map[string]any{"addressPrefix": "10.0.0.0/24", "networkSecurityGroup": map[string]any{"id": nsgPath}}}), 500, "")
	expect(t, h.do("GET", vnetPath+"/subnets/s"+netAPI, nil), 404, "ResourceNotFound")
	exec("DROP TRIGGER r")
	exec("CREATE TRIGGER d BEFORE DELETE ON azure_arm_resources BEGIN SELECT RAISE(IGNORE); END")
	expect(t, h.do("DELETE", vault, nil), 409, "Conflict")
	exec("DROP TRIGGER d")
	exec("UPDATE azure_arm_resources SET document='nope' WHERE type='microsoft.keyvault/vaults'")
	expect(t, h.do("GET", vault, nil), 500, "InternalServerError")
	expect(t, h.do("PUT", vault, body), 500, "")
	expect(t, h.do("GET", rg+"/providers/Microsoft.KeyVault/vaults"+kvAPI, nil), 500, "")
	exec("DROP TABLE azure_arm_references")
	expect(t, h.do("DELETE", nsgPath+netAPI, nil), 500, "")
	exec("DROP TABLE azure_arm_sequence")
	expect(t, h.do("PUT", rg+"/providers/Microsoft.KeyVault/vaults/vault-two"+kvAPI, body), 500, "")
	exec("DROP TABLE azure_arm_resources")
	for _, call := range [][2]string{{"GET", vault}, {"HEAD", "/subscriptions/" + sub + "/resourcegroups/rg1" + rgAPI}, {"DELETE", vault}, {"PATCH", vault}, {"GET", rg + "/providers/Microsoft.KeyVault/vaults" + kvAPI}} {
		var payload any
		if call[0] == "PATCH" {
			payload = map[string]any{"tags": map[string]any{}}
		}
		if r := h.do(call[0], call[1], payload); r.status != 500 {
			t.Fatalf("%s %s: %d %s", call[0], call[1], r.status, r.raw)
		}
	}
	if _, err := New(db, testConfig(h), Providers()); err != nil {
		t.Fatalf("tables are recreated on startup: %v", err)
	}
	exec("DROP TABLE azure_arm_scopes")
	exec("CREATE VIEW azure_arm_scopes AS SELECT 1 AS environment")
	if _, err := New(db, testConfig(h), Providers()); err == nil {
		t.Fatal("an unreadable scope table must fail startup")
	}
}
