package catalog

import (
	"net"
	"regexp"
	"strings"
)

// Reviewed bounded specifications for Google Cloud networking, network
// security and operations (logging and monitoring) configuration APIs.

var (
	// netopsShortID is the "letters, numbers, hyphens and underscores, not
	// starting with a number" rule of Network Security resource IDs.
	netopsShortID = regexp.MustCompile(`^[A-Za-z_-][A-Za-z0-9_-]{0,62}$`)
	// netopsLogID is the Cloud Logging rule for sink, exclusion and bucket IDs.
	netopsLogID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
)

// netopsStamp adds the creation and update timestamps every resource here
// reports to further server-assigned fields.
func netopsStamp(fields map[string]any) map[string]any {
	out := map[string]any{"createTime": "$now", "updateTime": "$now"}
	for k, v := range fields {
		out[k] = v
	}
	return out
}

var serviceDirectory = &API{
	Key: "servicedirectory.v1", Title: "Service Directory", Issues: []int{368}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.servicedirectory.v1.",
	Collections: []*Collection{
		{ID: "namespaces", Schema: "Namespace", IDParam: "namespaceId", Limit: 50, Permission: "servicedirectory.namespaces", Recursive: true,
			Generate: map[string]any{"uid": "$uid"}, Mutable: []string{"labels"}},
		{ID: "services", Schema: "Service", Parent: "namespaces", IDParam: "serviceId", Limit: 100, Permission: "servicedirectory.services", Recursive: true,
			Generate: map[string]any{"uid": "$uid"}, Mutable: []string{"annotations"}},
		{ID: "endpoints", Schema: "Endpoint", Parent: "services", IDParam: "endpointId", Limit: 100, Permission: "servicedirectory.endpoints",
			Generate: map[string]any{"uid": "$uid"}, Mutable: []string{"address", "port", "annotations"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.namespaces.create", Kind: Create, Collection: "namespaces"},
		{Method: "projects.locations.namespaces.get", Kind: Get, Collection: "namespaces"},
		{Method: "projects.locations.namespaces.list", Kind: List, Collection: "namespaces"},
		{Method: "projects.locations.namespaces.patch", Kind: Patch, Collection: "namespaces"},
		{Method: "projects.locations.namespaces.delete", Kind: Delete, Collection: "namespaces"},
		{Method: "projects.locations.namespaces.services.create", Kind: Create, Collection: "services", Check: annotationsWithin(2000)},
		{Method: "projects.locations.namespaces.services.get", Kind: Get, Collection: "services"},
		{Method: "projects.locations.namespaces.services.list", Kind: List, Collection: "services"},
		{Method: "projects.locations.namespaces.services.patch", Kind: Patch, Collection: "services", Check: annotationsWithin(2000)},
		{Method: "projects.locations.namespaces.services.delete", Kind: Delete, Collection: "services"},
		{Method: "projects.locations.namespaces.services.endpoints.create", Kind: Create, Collection: "endpoints", Check: directoryEndpoint},
		{Method: "projects.locations.namespaces.services.endpoints.get", Kind: Get, Collection: "endpoints"},
		{Method: "projects.locations.namespaces.services.endpoints.list", Kind: List, Collection: "endpoints"},
		{Method: "projects.locations.namespaces.services.endpoints.patch", Kind: Patch, Collection: "endpoints", Check: directoryEndpoint},
		{Method: "projects.locations.namespaces.services.endpoints.delete", Kind: Delete, Collection: "endpoints"},
	},
	Bounds: []string{
		"Namespaces, services and endpoints are registry metadata: no DNS zone, load balancer or health check is created, and services.resolve is not implemented.",
		"Endpoint addresses must be literal IPv4 or IPv6 addresses and ports must be within 0-65535; the network reference is recorded as given and not resolved.",
		"Deleting a namespace or service deletes its services and endpoints, as the official API does.",
	},
}

// annotationsWithin enforces Service Directory's total annotation size limit.
func annotationsWithin(limit int) func(map[string]any) error {
	return func(body map[string]any) error {
		annotations, _ := body["annotations"].(map[string]any)
		total := 0
		for k, v := range annotations {
			text, _ := v.(string)
			total += len(k) + len(text)
		}
		if total > limit {
			return invalid("Annotations may contain at most %d characters in total.", limit)
		}
		return nil
	}
}

// directoryEndpoint applies the official address, port and annotation rules.
func directoryEndpoint(body map[string]any) error {
	if address, ok := body["address"].(string); ok && address != "" && net.ParseIP(address) == nil {
		return invalid("Endpoint address %q must be an IPv4 or IPv6 address.", address)
	}
	if port, ok := body["port"].(int64); ok && (port < 0 || port > 65535) {
		return invalid("Endpoint port must be within [0, 65535].")
	}
	return annotationsWithin(512)(body)
}

var networkConnectivity = &API{
	Key: "networkconnectivity.v1", Title: "Network Connectivity Center", Issues: []int{361, 362}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.networkconnectivity.v1.",
	Collections: []*Collection{
		{ID: "hubs", Schema: "Hub", IDParam: "hubId", Limit: 20, Permission: "networkconnectivity.hubs",
			Generate: netopsStamp(map[string]any{"uniqueId": "$uid", "state": "ACTIVE"}),
			Mutable:  []string{"description", "labels", "exportPsc"}},
		{ID: "spokes", Schema: "Spoke", IDParam: "spokeId", Limit: 100, Permission: "networkconnectivity.spokes",
			Generate: netopsStamp(map[string]any{"uniqueId": "$uid", "state": "ACTIVE", "etag": "$etag"}),
			Mutable:  []string{"description", "labels"}},
		{ID: "gatewayAdvertisedRoutes", Schema: "GatewayAdvertisedRoute", Parent: "spokes", IDParam: "gatewayAdvertisedRouteId", Limit: 100, Permission: "networkconnectivity.gatewayAdvertisedRoutes",
			Generate: netopsStamp(map[string]any{"uniqueId": "$uid", "state": "ACTIVE"}),
			Mutable:  []string{"description", "labels", "priority", "recipient"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.global.hubs.create", Kind: Create, Collection: "hubs"},
		{Method: "projects.locations.global.hubs.get", Kind: Get, Collection: "hubs"},
		{Method: "projects.locations.global.hubs.list", Kind: List, Collection: "hubs"},
		{Method: "projects.locations.global.hubs.patch", Kind: Patch, Collection: "hubs"},
		{Method: "projects.locations.global.hubs.delete", Kind: Delete, Collection: "hubs"},
		{Method: "projects.locations.spokes.create", Kind: Create, Collection: "spokes", Check: hubSpoke},
		{Method: "projects.locations.spokes.get", Kind: Get, Collection: "spokes"},
		{Method: "projects.locations.spokes.list", Kind: List, Collection: "spokes"},
		{Method: "projects.locations.spokes.patch", Kind: Patch, Collection: "spokes"},
		{Method: "projects.locations.spokes.delete", Kind: Delete, Collection: "spokes"},
		{Method: "projects.locations.spokes.gatewayAdvertisedRoutes.create", Kind: Create, Collection: "gatewayAdvertisedRoutes", Check: advertisedRoute(true)},
		{Method: "projects.locations.spokes.gatewayAdvertisedRoutes.get", Kind: Get, Collection: "gatewayAdvertisedRoutes"},
		{Method: "projects.locations.spokes.gatewayAdvertisedRoutes.list", Kind: List, Collection: "gatewayAdvertisedRoutes"},
		{Method: "projects.locations.spokes.gatewayAdvertisedRoutes.patch", Kind: Patch, Collection: "gatewayAdvertisedRoutes", Check: advertisedRoute(false)},
		{Method: "projects.locations.spokes.gatewayAdvertisedRoutes.delete", Kind: Delete, Collection: "gatewayAdvertisedRoutes"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Hubs, spokes and gateway advertised routes are configuration metadata: no route is exchanged, no VPC, VPN, Interconnect, router appliance or gateway capacity is connected, and every resource reports ACTIVE immediately.",
		"A spoke names its hub and exactly one linked resource or gateway; the simulator derives spokeType from it but does not resolve the hub or linked resources, check route-advertisement parents, or block deleting a hub with spokes.",
		"Spoke acceptance and rejection, hub groups, route tables, listSpokes and queryStatus are not implemented. Long-running operations complete synchronously.",
	},
}

// spokeLinks maps each spoke's mutually exclusive linked field to its type.
var spokeLinks = map[string]string{
	"linkedVpnTunnels": "VPN_TUNNEL", "linkedInterconnectAttachments": "INTERCONNECT_ATTACHMENT",
	"linkedRouterApplianceInstances": "ROUTER_APPLIANCE", "linkedVpcNetwork": "VPC_NETWORK",
	"linkedProducerVpcNetwork": "PRODUCER_VPC_NETWORK", "gateway": "GATEWAY",
}

// hubSpoke requires a hub and exactly one linked resource, and records the
// output-only spokeType the official API derives from it.
func hubSpoke(body map[string]any) error {
	if hub, _ := body["hub"].(string); hub == "" {
		return invalid("Field 'hub' is required.")
	}
	spokeType := ""
	for field, kind := range spokeLinks {
		if _, ok := body[field]; ok {
			if spokeType != "" {
				return invalid("A spoke links exactly one resource type.")
			}
			spokeType = kind
		}
	}
	if spokeType == "" {
		return invalid("A spoke must link a VPN tunnel, Interconnect attachment, router appliance, VPC network, producer VPC network or gateway.")
	}
	body["spokeType"] = spokeType
	return nil
}

// advertisedRoute validates the CIDR range and priority of a gateway route.
func advertisedRoute(create bool) func(map[string]any) error {
	return func(body map[string]any) error {
		ipRange, ok := body["ipRange"].(string)
		if create && !ok {
			return invalid("Field 'ipRange' is required.")
		}
		if ok && !validRange(ipRange) {
			return invalid("ipRange %q must be a CIDR prefix or IP address.", ipRange)
		}
		if priority, ok := body["priority"].(int64); ok && (priority < 0 || priority > 65335) {
			return invalid("priority must be within [0, 65335].")
		}
		return nil
	}
}

func validRange(text string) bool {
	if _, _, err := net.ParseCIDR(text); err == nil {
		return true
	}
	return net.ParseIP(text) != nil
}

var networkManagement = &API{
	Key: "networkmanagement.v1", Title: "Network Intelligence Center (Network Management)", Issues: []int{363}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.networkmanagement.v1.",
	Collections: []*Collection{
		{ID: "connectivityTests", Schema: "ConnectivityTest", IDParam: "testId", Limit: 50, Permission: "networkmanagement.connectivitytests",
			IDPattern: regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`), Generate: netopsStamp(nil),
			Mutable: []string{"description", "labels", "source", "destination", "protocol", "relatedProjects", "bypassFirewallChecks", "roundTrip"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.global.connectivityTests.create", Kind: Create, Collection: "connectivityTests"},
		{Method: "projects.locations.global.connectivityTests.get", Kind: Get, Collection: "connectivityTests"},
		{Method: "projects.locations.global.connectivityTests.list", Kind: List, Collection: "connectivityTests"},
		{Method: "projects.locations.global.connectivityTests.patch", Kind: Patch, Collection: "connectivityTests"},
		{Method: "projects.locations.global.connectivityTests.delete", Kind: Delete, Collection: "connectivityTests"},
		{Method: "projects.locations.global.operations.get", Kind: OperationGet},
		{Method: "projects.locations.global.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Connectivity tests are stored test definitions only: no reachability analysis or probing runs, so reachabilityDetails, returnReachabilityDetails and probingDetails stay absent and rerun is not implemented.",
		"Source and destination endpoints are recorded as given and not resolved. VPC Flow Logs configurations and network monitoring providers are not implemented.",
		"Long-running operations complete synchronously and remain readable through operations.get/list.",
	},
}

var cloudIDS = &API{
	Key: "ids.v1", Title: "Cloud IDS", Issues: []int{344}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.ids.v1.",
	Collections: []*Collection{
		{ID: "endpoints", Schema: "Endpoint", IDParam: "endpointId", Limit: 20, Permission: "ids.endpoints", Zonal: true,
			Generate: netopsStamp(map[string]any{"state": "READY"}),
			Mutable:  []string{"description", "labels", "threatExceptions", "trafficLogs"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.endpoints.create", Kind: Create, Collection: "endpoints", Check: specified("severity")},
		{Method: "projects.locations.endpoints.get", Kind: Get, Collection: "endpoints"},
		{Method: "projects.locations.endpoints.list", Kind: List, Collection: "endpoints"},
		{Method: "projects.locations.endpoints.patch", Kind: Patch, Collection: "endpoints"},
		{Method: "projects.locations.endpoints.delete", Kind: Delete, Collection: "endpoints"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"IDS endpoints are zonal configuration metadata (for example us-central1-a): no Palo Alto Networks engine, internal load balancer, packet mirroring or threat detection runs, endpointIp and endpointForwardingRule stay absent, and endpoints report READY immediately.",
		"The attached network is recorded as given and not resolved; no threat or traffic log is ever produced.",
		"Long-running operations complete synchronously and remain readable through operations.get/list.",
	},
}

// specified requires enum fields whose only valid values are not *_UNSPECIFIED.
func specified(fields ...string) func(map[string]any) error {
	return func(body map[string]any) error {
		for _, field := range fields {
			if value, _ := body[field].(string); value == "" || strings.HasSuffix(value, "_UNSPECIFIED") {
				return invalid("Field '%s' is required.", field)
			}
		}
		return nil
	}
}

// netopsSecurityCollection describes one Network Security resource with the
// common server-assigned fields.
func netopsSecurityCollection(id, schema, idParam string, generate map[string]any, mutable ...string) *Collection {
	return &Collection{ID: id, Schema: schema, IDParam: idParam, Limit: 50, Permission: "networksecurity." + id,
		Generate: netopsStamp(generate), Mutable: mutable}
}

func netopsCRUD(prefix, collection string, create, patch func(map[string]any) error) []Binding {
	return []Binding{
		{Method: prefix + ".create", Kind: Create, Collection: collection, Check: create},
		{Method: prefix + ".get", Kind: Get, Collection: collection},
		{Method: prefix + ".list", Kind: List, Collection: collection},
		{Method: prefix + ".patch", Kind: Patch, Collection: collection, Check: patch},
		{Method: prefix + ".delete", Kind: Delete, Collection: collection},
	}
}

var networkSecurity = func() *API {
	active := map[string]any{"state": "ACTIVE", "reconciling": false}
	zonal := func(c *Collection) *Collection { c.Zonal = true; return c }
	pattern := func(c *Collection, p *regexp.Regexp) *Collection { c.IDPattern = p; return c }
	rules := netopsSecurityCollection("rules", "GatewaySecurityPolicyRule", "gatewaySecurityPolicyRuleId", nil,
		"description", "enabled", "priority", "sessionMatcher", "applicationMatcher", "basicProfile", "tlsInspectionEnabled")
	rules.Parent, rules.Permission, rules.IDPattern = "gatewaySecurityPolicies", "networksecurity.gatewaySecurityPolicyRules", regexp.MustCompile(`^[a-z-]{4,63}$`)
	api := &API{
		Key: "networksecurity.v1", Title: "Network Security (Cloud NGFW, Network Security Integration, Secure Web Proxy policies)", Issues: []int{353, 354, 364, 367}, Scope: "location",
		TypeURL: "type.googleapis.com/google.cloud.networksecurity.v1.",
		Collections: []*Collection{
			pattern(netopsSecurityCollection("securityProfiles", "SecurityProfile", "securityProfileId", map[string]any{"etag": "$etag"},
				"description", "labels", "threatPreventionProfile", "urlFilteringProfile", "customMirroringProfile", "customInterceptProfile"), netopsShortID),
			pattern(netopsSecurityCollection("securityProfileGroups", "SecurityProfileGroup", "securityProfileGroupId", map[string]any{"etag": "$etag"},
				"description", "labels", "threatPreventionProfile", "urlFilteringProfile", "customMirroringProfile", "customInterceptProfile"), netopsShortID),
			zonal(netopsSecurityCollection("firewallEndpoints", "FirewallEndpoint", "firewallEndpointId", active, "description", "labels")),
			zonal(netopsSecurityCollection("firewallEndpointAssociations", "FirewallEndpointAssociation", "firewallEndpointAssociationId", active, "labels", "disabled", "tlsInspectionPolicy")),
			netopsSecurityCollection("mirroringDeploymentGroups", "MirroringDeploymentGroup", "mirroringDeploymentGroupId", active, "description", "labels"),
			zonal(netopsSecurityCollection("mirroringDeployments", "MirroringDeployment", "mirroringDeploymentId", active, "description", "labels")),
			netopsSecurityCollection("mirroringEndpointGroups", "MirroringEndpointGroup", "mirroringEndpointGroupId", active, "description", "labels"),
			netopsSecurityCollection("mirroringEndpointGroupAssociations", "MirroringEndpointGroupAssociation", "mirroringEndpointGroupAssociationId", active, "labels"),
			netopsSecurityCollection("interceptDeploymentGroups", "InterceptDeploymentGroup", "interceptDeploymentGroupId", active, "description", "labels"),
			zonal(netopsSecurityCollection("interceptDeployments", "InterceptDeployment", "interceptDeploymentId", active, "description", "labels")),
			netopsSecurityCollection("interceptEndpointGroups", "InterceptEndpointGroup", "interceptEndpointGroupId", active, "description", "labels"),
			netopsSecurityCollection("interceptEndpointGroupAssociations", "InterceptEndpointGroupAssociation", "interceptEndpointGroupAssociationId", active, "labels"),
			pattern(netopsSecurityCollection("gatewaySecurityPolicies", "GatewaySecurityPolicy", "gatewaySecurityPolicyId", nil, "description", "tlsInspectionPolicy"), netopsShortID),
			rules,
			pattern(netopsSecurityCollection("urlLists", "UrlList", "urlListId", nil, "description", "values"), netopsShortID),
			pattern(netopsSecurityCollection("tlsInspectionPolicies", "TlsInspectionPolicy", "tlsInspectionPolicyId", nil,
				"description", "caPool", "trustConfig", "excludePublicCaSet", "minTlsVersion", "tlsFeatureProfile", "customTlsFeatures"), netopsShortID),
			pattern(netopsSecurityCollection("addressGroups", "AddressGroup", "addressGroupId", nil, "description", "labels", "items"), netopsShortID),
		},
		Bounds: []string{
			"Security profiles, profile groups, firewall endpoints and associations, mirroring and intercept deployment groups, deployments, endpoint groups and associations, gateway security policies and rules, URL lists, TLS inspection policies and address groups are configuration metadata.",
			"No firewall engine, threat signature, packet mirroring, interception, TLS decryption or proxy data plane runs; no traffic is inspected, and referenced networks, forwarding rules, CA pools and profiles are recorded as given and not resolved.",
			"Firewall endpoints, their associations and mirroring and intercept deployments are zonal (for example us-central1-a); other resources use the supported regions or global. Organization-level resources are not implemented.",
			"A security profile configures exactly the profile matching its type at creation; patches do not re-check that match. Address group items must be IP addresses or ranges within the declared capacity.",
			"Long-running operations complete synchronously and remain readable through operations.get/list. The authorization, TLS, DNS threat detector and Secure Access Connect resources are not implemented.",
		},
	}
	for _, c := range api.Collections {
		prefix := "projects.locations." + c.ID
		if c.ID == "rules" {
			prefix = "projects.locations.gatewaySecurityPolicies.rules"
		}
		var create, patch func(map[string]any) error
		switch c.ID {
		case "securityProfiles":
			create, patch = securityProfile, singleProfile
		case "rules":
			create = specified("basicProfile")
		case "addressGroups":
			create, patch = addressGroup(true), addressGroup(false)
		}
		api.Bindings = append(api.Bindings, netopsCRUD(prefix, c.ID, create, patch)...)
	}
	api.Bindings = append(api.Bindings,
		Binding{Method: "projects.locations.operations.get", Kind: OperationGet},
		Binding{Method: "projects.locations.operations.list", Kind: OperationList})
	return api
}()

// profileFields maps each security profile type to its configuration field.
var profileFields = map[string]string{
	"THREAT_PREVENTION": "threatPreventionProfile", "URL_FILTERING": "urlFilteringProfile",
	"CUSTOM_MIRRORING": "customMirroringProfile", "CUSTOM_INTERCEPT": "customInterceptProfile",
}

// securityProfile requires a type and allows only its configuration field.
func securityProfile(body map[string]any) error {
	if err := specified("type")(body); err != nil {
		return err
	}
	kind := body["type"].(string)
	for other, field := range profileFields {
		if _, ok := body[field]; ok && other != kind {
			return invalid("A %s security profile cannot set '%s'.", kind, field)
		}
	}
	return nil
}

// singleProfile refuses patches that configure more than one profile type.
func singleProfile(body map[string]any) error {
	set := 0
	for _, field := range profileFields {
		if _, ok := body[field]; ok {
			set++
		}
	}
	if set > 1 {
		return invalid("A security profile configures exactly one profile type.")
	}
	return nil
}

// addressGroup validates the declared type, items and capacity.
func addressGroup(create bool) func(map[string]any) error {
	return func(body map[string]any) error {
		if create {
			if err := specified("type")(body); err != nil {
				return err
			}
		}
		items, _ := body["items"].([]any)
		if capacity, ok := body["capacity"].(int64); ok && int64(len(items)) > capacity {
			return invalid("Address group items exceed its capacity.")
		}
		for _, item := range items {
			text, _ := item.(string)
			if !validRange(text) {
				return invalid("Address group item %q must be an IP address or range.", text)
			}
		}
		return nil
	}
}

var networkServices = &API{
	Key: "networkservices.v1", Title: "Network Services (Secure Web Proxy gateways)", Issues: []int{367}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.networkservices.v1.",
	Collections: []*Collection{
		{ID: "gateways", Schema: "Gateway", IDParam: "gatewayId", Limit: 20, Permission: "networkservices.gateways", IDPattern: netopsShortID,
			Generate: netopsStamp(nil),
			Mutable:  []string{"description", "labels", "ports", "gatewaySecurityPolicy", "certificateUrls", "serverTlsPolicy"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.gateways.create", Kind: Create, Collection: "gateways", Check: gatewaySpec(true)},
		{Method: "projects.locations.gateways.get", Kind: Get, Collection: "gateways"},
		{Method: "projects.locations.gateways.list", Kind: List, Collection: "gateways"},
		{Method: "projects.locations.gateways.patch", Kind: Patch, Collection: "gateways", Check: gatewaySpec(false)},
		{Method: "projects.locations.gateways.delete", Kind: Delete, Collection: "gateways"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Gateways are configuration metadata: no Secure Web Proxy or mesh proxy is deployed, no address is allocated from the subnetwork, and no traffic is proxied or filtered.",
		"Referenced networks, subnetworks, certificates and security or TLS policies are recorded as given and not resolved. Route views, meshes, routes and Media CDN resources are not implemented.",
		"Long-running operations complete synchronously and remain readable through operations.get/list.",
	},
}

// gatewaySpec requires a gateway type and valid ports.
func gatewaySpec(create bool) func(map[string]any) error {
	return func(body map[string]any) error {
		if create {
			if err := specified("type")(body); err != nil {
				return err
			}
		}
		ports, _ := body["ports"].([]any)
		for _, p := range ports {
			if port, _ := p.(int64); port < 1 || port > 65535 {
				return invalid("Gateway ports must be within [1, 65535].")
			}
		}
		if body["allPorts"] == true && len(ports) > 0 {
			return invalid("allPorts and ports are mutually exclusive.")
		}
		return nil
	}
}

var cloudLogging = &API{
	Key: "logging.v2", Title: "Cloud Logging", Issues: []int{372}, Scope: "project",
	TypeURL: "type.googleapis.com/google.logging.v2.",
	Collections: []*Collection{
		{ID: "sinks", Schema: "LogSink", IDField: "name", ShortName: true, IDPattern: netopsLogID, Limit: 50, Permission: "logging.sinks",
			Generate: netopsStamp(nil), Mutable: []string{"destination", "filter", "description", "disabled", "exclusions", "includeChildren", "interceptChildren", "bigqueryOptions"}},
		{ID: "exclusions", Schema: "LogExclusion", IDField: "name", ShortName: true, IDPattern: netopsLogID, Limit: 50, Permission: "logging.exclusions",
			Generate: netopsStamp(nil), Mutable: []string{"filter", "description", "disabled"}},
		{ID: "metrics", Schema: "LogMetric", IDField: "name", ShortName: true, Limit: 100, Permission: "logging.logMetrics",
			IDPattern: regexp.MustCompile(`^[A-Za-z0-9_.,+!*'()%-]{1,100}$`), Generate: netopsStamp(nil)},
		{ID: "buckets", Schema: "LogBucket", IDParam: "bucketId", IDPattern: netopsLogID, Limit: 20, Permission: "logging.buckets",
			Generate: netopsStamp(map[string]any{"lifecycleState": "ACTIVE"}),
			Mutable:  []string{"description", "retentionDays", "analyticsEnabled", "indexConfigs", "restrictedFields", "cmekSettings"}},
		{ID: "views", Schema: "LogView", Parent: "buckets", IDParam: "viewId", IDPattern: regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`), Limit: 30, Permission: "logging.views",
			Generate: netopsStamp(nil), Mutable: []string{"description", "filter"}},
	},
	Bindings: []Binding{
		{Method: "projects.sinks.create", Kind: Create, Collection: "sinks"},
		{Method: "projects.sinks.get", Kind: Get, Collection: "sinks"},
		{Method: "projects.sinks.list", Kind: List, Collection: "sinks"},
		{Method: "projects.sinks.patch", Kind: Patch, Collection: "sinks"},
		{Method: "projects.sinks.delete", Kind: Delete, Collection: "sinks"},
		{Method: "projects.exclusions.create", Kind: Create, Collection: "exclusions"},
		{Method: "projects.exclusions.get", Kind: Get, Collection: "exclusions"},
		{Method: "projects.exclusions.list", Kind: List, Collection: "exclusions"},
		{Method: "projects.exclusions.patch", Kind: Patch, Collection: "exclusions"},
		{Method: "projects.exclusions.delete", Kind: Delete, Collection: "exclusions"},
		{Method: "projects.metrics.create", Kind: Create, Collection: "metrics"},
		{Method: "projects.metrics.get", Kind: Get, Collection: "metrics"},
		{Method: "projects.metrics.list", Kind: List, Collection: "metrics"},
		{Method: "projects.metrics.delete", Kind: Delete, Collection: "metrics"},
		{Method: "projects.locations.buckets.create", Kind: Create, Collection: "buckets", Check: unlockedBucket},
		{Method: "projects.locations.buckets.get", Kind: Get, Collection: "buckets"},
		{Method: "projects.locations.buckets.list", Kind: List, Collection: "buckets"},
		{Method: "projects.locations.buckets.patch", Kind: Patch, Collection: "buckets", Check: unlockedBucket},
		{Method: "projects.locations.buckets.delete", Kind: Delete, Collection: "buckets"},
		{Method: "projects.locations.buckets.views.create", Kind: Create, Collection: "views"},
		{Method: "projects.locations.buckets.views.get", Kind: Get, Collection: "views"},
		{Method: "projects.locations.buckets.views.list", Kind: List, Collection: "views"},
		{Method: "projects.locations.buckets.views.patch", Kind: Patch, Collection: "views"},
		{Method: "projects.locations.buckets.views.delete", Kind: Delete, Collection: "views"},
	},
	Bounds: []string{
		"Sinks, exclusions, log-based metrics, log buckets and log views are configuration metadata only: no log entry is written, stored, read, routed, excluded, counted or exported, and entries, logs and tail methods are not implemented.",
		"Sinks receive no writerIdentity; uniqueWriterIdentity and customWriterIdentity create or use no service account. Filters and destinations are recorded as given and not evaluated or resolved.",
		"Log-based metrics support create, get, list and delete; metrics.update and metric names with '/' hierarchy are not implemented. The project's _Default and _Required sinks and buckets are not pre-created.",
		"Locking a bucket is refused. Deleted buckets are removed immediately rather than kept 7 days in DELETE_REQUESTED, undelete and asynchronous bucket methods are not implemented, and a bucket with views cannot be deleted.",
		"Folder, organization and billing-account parents are not implemented.",
	},
}

// unlockedBucket refuses bucket locking: the retention lock it promises would
// have no effect on a simulator that stores no log entries.
func unlockedBucket(body map[string]any) error {
	if body["locked"] == true {
		return invalid("Locking log buckets is not supported by the simulator.")
	}
	return nil
}

var cloudMonitoring = &API{
	Key: "monitoring.v3", Title: "Cloud Monitoring", Issues: []int{374}, Scope: "project",
	TypeURL: "type.googleapis.com/google.monitoring.v3.",
	Collections: []*Collection{
		{ID: "alertPolicies", Schema: "AlertPolicy", Limit: 100, Permission: "monitoring.alertPolicies",
			Generate: map[string]any{"enabled": true},
			Mutable:  []string{"displayName", "documentation", "userLabels", "conditions", "combiner", "enabled", "notificationChannels", "alertStrategy", "severity"}},
		{ID: "notificationChannels", Schema: "NotificationChannel", Limit: 100, Permission: "monitoring.notificationChannels",
			Generate: map[string]any{"enabled": true},
			Mutable:  []string{"displayName", "description", "labels", "userLabels", "enabled"}},
		{ID: "uptimeCheckConfigs", Schema: "UptimeCheckConfig", Limit: 100, Permission: "monitoring.uptimeCheckConfigs",
			Mutable: []string{"displayName", "httpCheck", "tcpCheck", "timeout", "period", "contentMatchers", "selectedRegions", "userLabels", "disabled", "logCheckFailures", "checkerType"}},
	},
	Bindings: []Binding{
		{Method: "projects.alertPolicies.create", Kind: Create, Collection: "alertPolicies", Check: serverRecords("creationRecord", "mutationRecord", "validity")},
		{Method: "projects.alertPolicies.get", Kind: Get, Collection: "alertPolicies"},
		{Method: "projects.alertPolicies.list", Kind: List, Collection: "alertPolicies"},
		{Method: "projects.alertPolicies.patch", Kind: Patch, Collection: "alertPolicies", Check: serverRecords("creationRecord", "mutationRecord", "validity")},
		{Method: "projects.alertPolicies.delete", Kind: Delete, Collection: "alertPolicies"},
		{Method: "projects.notificationChannels.create", Kind: Create, Collection: "notificationChannels", Check: notificationChannel(true)},
		{Method: "projects.notificationChannels.get", Kind: Get, Collection: "notificationChannels"},
		{Method: "projects.notificationChannels.list", Kind: List, Collection: "notificationChannels"},
		{Method: "projects.notificationChannels.patch", Kind: Patch, Collection: "notificationChannels", Check: notificationChannel(false)},
		{Method: "projects.notificationChannels.delete", Kind: Delete, Collection: "notificationChannels"},
		{Method: "projects.uptimeCheckConfigs.create", Kind: Create, Collection: "uptimeCheckConfigs", Check: uptimeCheck(true)},
		{Method: "projects.uptimeCheckConfigs.get", Kind: Get, Collection: "uptimeCheckConfigs"},
		{Method: "projects.uptimeCheckConfigs.list", Kind: List, Collection: "uptimeCheckConfigs"},
		{Method: "projects.uptimeCheckConfigs.patch", Kind: Patch, Collection: "uptimeCheckConfigs", Check: uptimeCheck(false)},
		{Method: "projects.uptimeCheckConfigs.delete", Kind: Delete, Collection: "uptimeCheckConfigs"},
	},
	Bounds: []string{
		"Alert policies, notification channels and uptime check configurations are configuration metadata: no metric, time series, incident or uptime probe exists, conditions are never evaluated and no notification is sent.",
		"Resource IDs are server-generated. Condition names, creation and mutation records and verification status are not assigned; client-sent records and verification status are dropped as the official API ignores them.",
		"A patch without updateMask updates only the supplied fields rather than replacing the resource, and updateMask paths use JSON field names such as displayName. Deleting a notification channel does not check alert policies that reference it.",
		"Notification channel credentials (tokens, passwords, service keys) and uptime check passwords or secret headers are refused and never stored; channel verification methods are not implemented.",
		"Groups, snoozes, services, SLOs, metric descriptors and time series are not implemented.",
	},
}

// serverRecords drops fields the official API documents as read-only and
// ignored on writes, although the discovery document does not mark them
// output-only.
func serverRecords(fields ...string) func(map[string]any) error {
	return func(body map[string]any) error {
		for _, field := range fields {
			delete(body, field)
		}
		return nil
	}
}

// secretName reports whether a label or header key names a credential.
func secretName(key string) bool {
	key = strings.ToLower(key)
	for _, word := range []string{"token", "password", "secret", "service_key", "authorization", "cookie", "api-key", "api_key", "apikey"} {
		if strings.Contains(key, word) {
			return true
		}
	}
	return false
}

// notificationChannel requires a channel type and refuses credentials in
// labels: the simulator delivers no notifications and stores no secrets.
func notificationChannel(create bool) func(map[string]any) error {
	drop := serverRecords("creationRecord", "mutationRecords", "verificationStatus")
	return func(body map[string]any) error {
		_ = drop(body)
		if kind, _ := body["type"].(string); create && kind == "" {
			return invalid("Field 'type' is required.")
		}
		labels, _ := body["labels"].(map[string]any)
		for key := range labels {
			if secretName(key) {
				return invalid("Notification channel credential label %q is not stored by the simulator.", key)
			}
		}
		return nil
	}
}

// uptimeCheck applies the official target and protocol choices and refuses
// probe credentials.
func uptimeCheck(create bool) func(map[string]any) error {
	return func(body map[string]any) error {
		targets := 0
		for _, field := range []string{"monitoredResource", "resourceGroup", "syntheticMonitor"} {
			if _, ok := body[field]; ok {
				targets++
			}
		}
		if create && targets != 1 || targets > 1 {
			return invalid("An uptime check names exactly one of monitoredResource, resourceGroup or syntheticMonitor.")
		}
		_, http := body["httpCheck"]
		_, tcp := body["tcpCheck"]
		if http && tcp {
			return invalid("httpCheck and tcpCheck are mutually exclusive.")
		}
		check, _ := body["httpCheck"].(map[string]any)
		if auth, ok := check["authInfo"].(map[string]any); ok && auth["password"] != nil {
			return invalid("Uptime check passwords are not stored by the simulator.")
		}
		if check["maskHeaders"] == true {
			return invalid("Masked (secret) uptime check headers are not stored by the simulator.")
		}
		headers, _ := check["headers"].(map[string]any)
		for key := range headers {
			if secretName(key) {
				return invalid("Uptime check header %q carries a credential and is not stored by the simulator.", key)
			}
		}
		return nil
	}
}
