package arm

import "strings"

// Reviewed bounded specifications for resource groups, Key Vault and
// managed identities.

var resources = &Provider{
	Namespace: "Microsoft.Resources", APIVersion: "2025-04-01", Title: "Resource groups", Issues: catalogIssue,
	Types: []*ResourceType{{
		Name: "resourceGroups", Title: "Resource groups", Limit: 100,
		// 1-90 letters, digits, underscores, hyphens, periods and parentheses; no trailing period.
		NamePattern: name(`^[-\p{L}\p{Mn}\p{Nd}\p{Pc}._()]{0,89}[-\p{L}\p{Mn}\p{Nd}\p{Pc}()]$`),
		Form:        []string{},
	}},
	Bindings: []Binding{
		{Operation: "ResourceGroups_CreateOrUpdate", Kind: Put, Type: "resourceGroups"},
		{Operation: "ResourceGroups_Get", Kind: Get, Type: "resourceGroups"},
		{Operation: "ResourceGroups_CheckExistence", Kind: Head, Type: "resourceGroups"},
		{Operation: "ResourceGroups_Update", Kind: Patch, Type: "resourceGroups"},
		{Operation: "ResourceGroups_Delete", Kind: Delete, Type: "resourceGroups"},
		{Operation: "ResourceGroups_List", Kind: List, Type: "resourceGroups", Query: []string{"$top"}},
	},
	Bounds: []string{
		"One synthetic subscription per environment; resource groups hold only the catalog resources below.",
		"Deleting a resource group synchronously deletes every catalog resource in it, unless another resource outside it still references one; forceDeletionTypes and $filter are refused.",
		"Export template, move and the generic Resources_* operations are not implemented (HTTP 501).",
	},
}

var keyVault = &Provider{
	Namespace: "Microsoft.KeyVault", APIVersion: "2026-05-15", Title: "Key Vault", Issues: catalogIssue,
	Types: []*ResourceType{{
		Name: "vaults", Title: "Key vaults", Limit: 25, GlobalName: true,
		// 3-24 letters, digits and hyphens; starts with a letter, ends with a letter or digit, no consecutive hyphens.
		NamePattern: name(`^[A-Za-z](-?[A-Za-z0-9])+$`),
		Refs:        []Ref{{Path: "properties.networkAcls.virtualNetworkRules[].id", Type: "Microsoft.Network/virtualNetworks/subnets"}},
		Check:       vaultCheck,
		Form:        []string{"properties.tenantId", "properties.sku.family", "properties.sku.name", "properties.enableRbacAuthorization"},
		Defaults:    map[string]any{"properties.tenantId": "$tenant", "properties.sku.family": "A", "properties.sku.name": "standard"},
	}},
	Bindings: standard("vaults", "Vaults_CreateOrUpdate", "Vaults_Get", "Vaults_Update", "Vaults_Delete", "Vaults_ListByResourceGroup", "Vaults_ListBySubscription",
		map[string][]string{"Vaults_ListByResourceGroup": {"$top"}, "Vaults_ListBySubscription": {"$top"}}),
	Bounds: []string{
		"Vault metadata only: no vault data plane, keys, secrets, certificates or vaultUri endpoint exists.",
		"tenantId and access-policy tenants must be the simulator tenant; createMode recover is refused because deleted vaults are not retained.",
		"Deletion is immediate and permanent; soft-delete, purge, deleted-vault and access-policy operations are not implemented (HTTP 501).",
	},
}

func vaultCheck(c *Change) error {
	if n := len(c.Name); n < 3 || n > 24 {
		return fail(400, "VaultNameNotValid", "The vault name '%s' must be 3-24 characters.", c.Name)
	}
	if err := sameTenant(c, "properties.tenantId"); err != nil {
		return err
	}
	policies, _ := lookup(c.Document, "properties.accessPolicies")
	entries, _ := policies.([]any)
	for _, entry := range entries {
		policy, _ := entry.(map[string]any)
		if tenant := text(policy, "tenantId"); !strings.EqualFold(tenant, c.Tenant) {
			return refuse("Access policy tenantId must be the simulator tenant %s.", c.Tenant)
		}
	}
	if strings.EqualFold(text(c.Document, "properties.createMode"), "recover") {
		return refuse("createMode 'recover' is not supported: the simulator does not retain deleted vaults.")
	}
	return rangeCheck(c, "properties.softDeleteRetentionInDays", 7, 90)
}

var managedIdentity = &Provider{
	Namespace: "Microsoft.ManagedIdentity", APIVersion: "2024-11-30", Title: "Managed identities", Issues: catalogIssue,
	Types: []*ResourceType{{
		Name: "userAssignedIdentities", Title: "User-assigned managed identities", Limit: 50,
		// 3-128 letters, digits, hyphens and underscores; starts with a letter or digit.
		NamePattern: name(`^[A-Za-z0-9][A-Za-z0-9_-]{2,127}$`),
		Generate:    map[string]any{"properties.tenantId": "$tenant", "properties.principalId": "$uuid", "properties.clientId": "$uuid"},
		Form:        []string{"properties.isolationScope"},
	}},
	Bindings: standard("userAssignedIdentities", "UserAssignedIdentities_CreateOrUpdate", "UserAssignedIdentities_Get", "UserAssignedIdentities_Update", "UserAssignedIdentities_Delete",
		"UserAssignedIdentities_ListByResourceGroup", "UserAssignedIdentities_ListBySubscription", nil),
	Bounds: []string{
		"Identities are metadata with generated principalId and clientId: no Entra service principal, token or credential is issued.",
		"Other catalog resources may assign them through identity.userAssignedIdentities; deleting an assigned identity is allowed, as in Azure.",
		"Federated identity credentials and system-assigned identity scopes are not implemented (HTTP 501).",
	},
}
