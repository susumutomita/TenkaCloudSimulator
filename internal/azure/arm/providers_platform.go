package arm

import (
	"strconv"
	"strings"
)

// Reviewed bounded specifications for monitoring, configuration, registry,
// messaging, database and App Service plan metadata.

var operationalInsights = &Provider{
	Namespace: "Microsoft.OperationalInsights", APIVersion: "2025-07-01", Title: "Log Analytics", Issues: catalogIssue,
	Types: []*ResourceType{{
		Name: "workspaces", Title: "Log Analytics workspaces", Limit: 10,
		NamePattern: name(`^[A-Za-z0-9][A-Za-z0-9-]{2,61}[A-Za-z0-9]$`),
		Generate:    map[string]any{"properties.customerId": "$uuid", "properties.createdDate": "$created", "properties.modifiedDate": "$now"},
		Unsupported: map[string]string{
			"properties.defaultDataCollectionRuleResourceId": "data collection rules are not in the catalog.",
			"properties.replication":                         "workspace replication is not simulated.",
			"properties.failover":                            "workspace failover is not simulated.",
			"properties.features.clusterResourceId":          "dedicated clusters are not in the catalog.",
		},
		Check: func(c *Change) error {
			if text(c.Document, "properties.sku.name") == "" {
				assign(c.Document, "properties.sku.name", "PerGB2018")
			}
			return rangeCheck(c, "properties.retentionInDays", 30, 730)
		},
		Form: []string{"properties.sku.name", "properties.retentionInDays"}, Defaults: map[string]any{"properties.sku.name": "PerGB2018", "properties.retentionInDays": int64(30)},
	}},
	Bindings: standard("workspaces", "Workspaces_CreateOrUpdate", "Workspaces_Get", "Workspaces_Update", "Workspaces_Delete", "Workspaces_ListByResourceGroup", "Workspaces_List",
		map[string][]string{"Workspaces_Delete": {"force"}}),
	Bounds: []string{
		"Workspace metadata only: no log ingestion, query, table, agent or shared key exists.",
		"Deletion is always immediate and permanent (force is accepted and changes nothing); deleted-workspace recovery is not simulated.",
		"Retention must be 30-730 days; failover, replication, data collection rules and every other operation are not implemented (HTTP 501).",
	},
}

var appConfiguration = &Provider{
	Namespace: "Microsoft.AppConfiguration", APIVersion: "2024-06-01", Title: "App Configuration", Issues: catalogIssue,
	Types: []*ResourceType{{
		Name: "configurationStores", Title: "App Configuration stores", Limit: 10, GlobalName: true,
		NamePattern: name(`^[A-Za-z0-9_-]{5,50}$`),
		Generate:    map[string]any{"properties.creationDate": "$created"},
		Unsupported: map[string]string{"properties.encryption": "customer-managed keys are not simulated."},
		Check: func(c *Change) error {
			if !equalsAny(text(c.Document, "sku.name"), "Free", "Developer", "Standard", "Premium") {
				return refuse("sku.name must be Free, Developer, Standard or Premium.")
			}
			if strings.EqualFold(text(c.Document, "properties.createMode"), "Recover") {
				return refuse("createMode 'Recover' is not supported: the simulator does not retain deleted stores.")
			}
			return rangeCheck(c, "properties.softDeleteRetentionInDays", 1, 7)
		},
		Form: []string{"sku.name", "properties.disableLocalAuth"}, Defaults: map[string]any{"sku.name": "Standard"},
	}},
	Bindings: standard("configurationStores", "ConfigurationStores_Create", "ConfigurationStores_Get", "ConfigurationStores_Update", "ConfigurationStores_Delete",
		"ConfigurationStores_ListByResourceGroup", "ConfigurationStores_List", map[string][]string{"ConfigurationStores_ListByResourceGroup": {"$skipToken"}, "ConfigurationStores_List": {"$skipToken"}}),
	Bounds: []string{
		"Store metadata only: no key-value data plane, endpoint, access key or connection string exists.",
		"Deletion is immediate and permanent; createMode Recover, customer-managed keys, key listing and regeneration are refused or not implemented (HTTP 501).",
	},
}

var containerRegistry = &Provider{
	Namespace: "Microsoft.ContainerRegistry", APIVersion: "2025-11-01", Title: "Container Registry", Issues: catalogIssue,
	Types: []*ResourceType{{
		Name: "registries", Title: "Container registries", Limit: 10, GlobalName: true,
		NamePattern: name(`^[A-Za-z0-9]{5,50}$`),
		Generate:    map[string]any{"properties.creationDate": "$created"},
		Unsupported: map[string]string{"properties.encryption": "customer-managed keys are not simulated."},
		Check: func(c *Change) error {
			sku := text(c.Document, "sku.name")
			if strings.EqualFold(sku, "Classic") {
				return refuse("The Classic registry SKU is retired; choose Basic, Standard or Premium.")
			}
			assign(c.Document, "sku.tier", sku)
			if present(c.Document, "properties.networkRuleSet") && !strings.EqualFold(sku, "Premium") {
				return refuse("Network rule sets require the Premium SKU.")
			}
			return nil
		},
		Form: []string{"sku.name", "properties.adminUserEnabled"}, Defaults: map[string]any{"sku.name": "Basic"},
	}},
	Bindings: standard("registries", "Registries_Create", "Registries_Get", "Registries_Update", "Registries_Delete", "Registries_ListByResourceGroup", "Registries_List", nil),
	Bounds: []string{
		"Registry metadata only: no image, repository, login server, token or admin credential exists, even when adminUserEnabled is true.",
		"Credential listing, imports, replications, webhooks, tasks and every other operation are not implemented (HTTP 501).",
	},
}

// messagingNamespace is the shared review of Event Hubs and Service Bus
// namespaces, which publish the same namespace shape.
func messagingNamespace(title string) *ResourceType {
	return &ResourceType{
		Name: "namespaces", Title: title, Limit: 10, GlobalName: true,
		NamePattern: name(`^[A-Za-z][A-Za-z0-9-]{4,48}[A-Za-z0-9]$`),
		Generate:    map[string]any{"properties.createdAt": "$created", "properties.updatedAt": "$now", "properties.status": "Active"},
		Unsupported: map[string]string{
			"properties.encryption":                 "customer-managed keys are not simulated.",
			"properties.clusterArmId":               "dedicated clusters are not in the catalog.",
			"properties.geoDataReplication":         "geo-replication is not simulated.",
			"properties.privateEndpointConnections": "private endpoints are not in the catalog.",
		},
		Check: func(c *Change) error {
			if strings.HasSuffix(strings.ToLower(c.Name), "-sb") || strings.HasSuffix(strings.ToLower(c.Name), "-mgmt") {
				return refuse("Namespace names cannot end with -sb or -mgmt.")
			}
			if tier := text(c.Document, "sku.tier"); tier != "" && !strings.EqualFold(tier, text(c.Document, "sku.name")) {
				return refuse("sku.tier must equal sku.name.")
			}
			assign(c.Document, "sku.tier", text(c.Document, "sku.name"))
			return nil
		},
		Form: []string{"sku.name"}, Defaults: map[string]any{"sku.name": "Standard"},
	}
}

var eventHub = &Provider{
	Namespace: "Microsoft.EventHub", APIVersion: "2026-01-01", Title: "Event Hubs", Issues: catalogIssue,
	Types: []*ResourceType{messagingNamespace("Event Hubs namespaces"), {
		Name: "namespaces/eventhubs", Title: "Event hubs", Limit: 40,
		NamePattern: name(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,254}[A-Za-z0-9])?$`),
		Generate:    map[string]any{"properties.createdAt": "$created", "properties.updatedAt": "$now", "properties.status": "Active"},
		Unsupported: map[string]string{"properties.captureDescription": "capture to storage is not simulated."},
		Check: func(c *Change) error {
			count, ok := number(c.Document, "properties.partitionCount")
			if !ok {
				count = 4
				assign(c.Document, "properties.partitionCount", count)
			}
			if count < 1 || count > 32 {
				return refuse("partitionCount must be between 1 and 32.")
			}
			if prior, ok := number(c.Previous, "properties.partitionCount"); ok && count < prior {
				return refuse("The partition count cannot be decreased.")
			}
			ids := make([]any, count)
			for i := range ids {
				ids[i] = strconv.Itoa(i)
			}
			assign(c.Document, "properties.partitionIds", ids)
			return rangeCheck(c, "properties.messageRetentionInDays", 1, 7)
		},
		Form: []string{"properties.partitionCount", "properties.messageRetentionInDays"}, Defaults: map[string]any{"properties.partitionCount": int64(2), "properties.messageRetentionInDays": int64(1)},
	}},
	Bindings: append(standard("namespaces", "Namespaces_CreateOrUpdate", "Namespaces_Get", "Namespaces_Update", "Namespaces_Delete", "Namespaces_ListByResourceGroup", "Namespaces_List", nil),
		Binding{Operation: "EventHubs_CreateOrUpdate", Kind: Put, Type: "namespaces/eventhubs"},
		Binding{Operation: "EventHubs_Get", Kind: Get, Type: "namespaces/eventhubs"},
		Binding{Operation: "EventHubs_Delete", Kind: Delete, Type: "namespaces/eventhubs"},
		Binding{Operation: "EventHubs_ListByNamespace", Kind: List, Type: "namespaces/eventhubs", Query: []string{"$skip", "$top"}}),
	Bounds: []string{
		"Namespace and event hub metadata only: no endpoint, AMQP or Kafka listener, event, consumer group, authorization rule, SAS key or connection string exists.",
		"Event hubs get generated partition IDs; the partition count is 1-32 and cannot decrease. Capture, geo-replication and customer-managed keys are refused.",
		"Deleting a namespace deletes its event hubs; every other operation is not implemented (HTTP 501).",
	},
}

var serviceBus = &Provider{
	Namespace: "Microsoft.ServiceBus", APIVersion: "2026-01-01", Title: "Service Bus", Issues: catalogIssue,
	Types: []*ResourceType{messagingNamespace("Service Bus namespaces"), {
		Name: "namespaces/queues", Title: "Queues", Limit: 100,
		NamePattern: name(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,258}[A-Za-z0-9])?$`),
		Generate: map[string]any{"properties.createdAt": "$created", "properties.updatedAt": "$now", "properties.accessedAt": "$created", "properties.status": "Active",
			"properties.messageCount": int64(0), "properties.sizeInBytes": int64(0)},
		Check: func(c *Change) error {
			for _, field := range []string{"properties.forwardTo", "properties.forwardDeadLetteredMessagesTo"} {
				target := text(c.Document, field)
				if target == "" {
					continue
				}
				found := false
				for _, sibling := range c.Siblings {
					found = found || strings.EqualFold(text(sibling, "name"), target)
				}
				if !found || strings.EqualFold(target, c.Name) {
					return refuse("Member '%s' must name another existing queue in this namespace.", field)
				}
			}
			if partitioned, _ := lookup(c.Document, "properties.enablePartitioning"); c.Previous != nil && partitioned != nil {
				if prior, _ := lookup(c.Previous, "properties.enablePartitioning"); prior != nil && prior != partitioned {
					return fail(400, "PropertyChangeNotAllowed", "Changing property 'properties.enablePartitioning' is not allowed.")
				}
			}
			return rangeCheck(c, "properties.maxDeliveryCount", 1, 2000)
		},
		Form: []string{"properties.maxDeliveryCount", "properties.requiresSession"}, Defaults: map[string]any{"properties.maxDeliveryCount": int64(10)},
	}},
	Bindings: append(standard("namespaces", "Namespaces_CreateOrUpdate", "Namespaces_Get", "Namespaces_Update", "Namespaces_Delete", "Namespaces_ListByResourceGroup", "Namespaces_List", nil),
		Binding{Operation: "Queues_CreateOrUpdate", Kind: Put, Type: "namespaces/queues"},
		Binding{Operation: "Queues_Get", Kind: Get, Type: "namespaces/queues"},
		Binding{Operation: "Queues_Delete", Kind: Delete, Type: "namespaces/queues"},
		Binding{Operation: "Queues_ListByNamespace", Kind: List, Type: "namespaces/queues", Query: []string{"$skip", "$top"}}),
	Bounds: []string{
		"Namespace and queue metadata only: no endpoint, message, session, topic, authorization rule, SAS key or connection string exists; message counts stay 0.",
		"Queue forwarding must name another existing queue in the same namespace; partitioning cannot change after creation.",
		"Deleting a namespace deletes its queues; every other operation is not implemented (HTTP 501).",
	},
}

var documentDB = &Provider{
	Namespace: "Microsoft.DocumentDB", APIVersion: "2026-03-15", Title: "Azure Cosmos DB", Issues: catalogIssue,
	Types: []*ResourceType{{
		Name: "databaseAccounts", Title: "Azure Cosmos DB accounts", Limit: 10, GlobalName: true,
		NamePattern: name(`^[a-z0-9][a-z0-9-]{1,42}[a-z0-9]$`),
		Refs:        []Ref{{Path: "properties.virtualNetworkRules[].id", Type: "Microsoft.Network/virtualNetworks/subnets"}},
		Unsupported: map[string]string{
			"properties.restoreParameters":           "point-in-time restore is not simulated.",
			"properties.keyVaultKeyUri":              "customer-managed keys are not simulated.",
			"properties.networkAclBypassResourceIds": "network ACL bypass resources are not in the catalog.",
		},
		Check:    cosmosCheck,
		Form:     []string{"kind", "properties.databaseAccountOfferType", "properties.consistencyPolicy.defaultConsistencyLevel"},
		Defaults: map[string]any{"kind": "GlobalDocumentDB", "properties.databaseAccountOfferType": "Standard", "properties.locations": "$location-list", "properties.consistencyPolicy.defaultConsistencyLevel": "Session"},
	}},
	Bindings: standard("databaseAccounts", "DatabaseAccounts_CreateOrUpdate", "DatabaseAccounts_Get", "DatabaseAccounts_Update", "DatabaseAccounts_Delete",
		"DatabaseAccounts_ListByResourceGroup", "DatabaseAccounts_List", nil),
	Bounds: []string{
		"Account metadata only: no database engine, document endpoint, container, key, connection string or failover exists.",
		"Locations must be distinct simulator regions with failover priorities 0..n-1; createMode Restore, restore parameters and customer-managed keys are refused.",
		"Databases, containers, keys, regional failover and every other operation are not implemented (HTTP 501).",
	},
}

func cosmosCheck(c *Change) error {
	if strings.EqualFold(text(c.Document, "properties.createMode"), "Restore") {
		return refuse("createMode 'Restore' is not supported: the simulator keeps no backups.")
	}
	value, _ := lookup(c.Document, "properties.locations")
	locations, _ := value.([]any)
	if len(locations) == 0 {
		return refuse("properties.locations needs at least one location.")
	}
	seen, priorities := map[string]bool{}, map[int64]bool{}
	for _, item := range locations {
		location, _ := item.(map[string]any)
		region := normalizeLocation(text(location, "locationName"))
		known := false
		for _, l := range c.Locations {
			known = known || normalizeLocation(l) == region
		}
		priority, _ := number(location, "failoverPriority")
		if !known || seen[region] || priorities[priority] || priority < 0 || priority >= int64(len(locations)) {
			return refuse("Each location needs a distinct simulator region and a distinct failoverPriority from 0 to %d.", len(locations)-1)
		}
		seen[region], priorities[priority] = true, true
	}
	return nil
}

var web = &Provider{
	Namespace: "Microsoft.Web", APIVersion: "2025-05-01", Title: "App Service", Issues: catalogIssue,
	Types: []*ResourceType{{
		Name: "serverfarms", Title: "App Service plans", Limit: 20,
		NamePattern: name(`^[A-Za-z0-9-]{1,60}$`),
		Generate:    map[string]any{"properties.status": "Ready", "properties.numberOfSites": int64(0), "properties.numberOfWorkers": int64(1), "properties.maximumNumberOfWorkers": int64(1)},
		Unsupported: map[string]string{
			"properties.hostingEnvironmentProfile": "App Service Environments are not in the catalog.",
			"properties.kubeEnvironmentProfile":    "Kubernetes environments are not in the catalog.",
			"properties.registryAdapters":          "registry adapters reference Key Vault secrets, which are not simulated.",
			"properties.storageMounts":             "storage mounts reference credentials, which are not simulated.",
			"properties.installScripts":            "install scripts are not executed.",
			"properties.network":                   "plan networking is not simulated.",
			"extendedLocation":                     "extended locations are not simulated.",
		},
		Check: func(c *Change) error {
			if text(c.Document, "sku.name") == "" {
				return refuse("sku.name is required, for example F1, B1 or P1v3.")
			}
			id := text(c.Document, "id")
			parts := strings.Split(id, "/")
			if len(parts) > 4 {
				assign(c.Document, "properties.subscription", parts[2])
				assign(c.Document, "properties.resourceGroup", parts[4])
			}
			return nil
		},
		Form: []string{"sku.name", "kind", "properties.reserved"}, Defaults: map[string]any{"sku.name": "B1", "kind": "linux", "properties.reserved": true},
	}},
	Bindings: standard("serverfarms", "AppServicePlans_CreateOrUpdate", "AppServicePlans_Get", "AppServicePlans_Update", "AppServicePlans_Delete",
		"AppServicePlans_ListByResourceGroup", "AppServicePlans_List", map[string][]string{"AppServicePlans_List": {"detailed"}}),
	Bounds: []string{
		"Plan metadata only: no worker, site, deployment, certificate or hybrid connection exists; numberOfSites stays 0.",
		"The official plan PATCH definition has no tags; change tags with a full create-or-update. detailed is accepted because every stored property is always returned.",
		"Sites, App Service Environments, Kubernetes environments, registry adapters, storage mounts and every other operation are not implemented (HTTP 501).",
	},
}
