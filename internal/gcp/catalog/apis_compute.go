package catalog

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Reviewed bounded specification of Compute Engine (compute.v1): the flat
// API behind Virtual Private Cloud, Persistent Disk and the Compute Engine
// configuration resources. Every resource is configuration metadata; no VM,
// packet, block device or Google-allocated address exists.

var compute = &API{
	Key: "compute.v1", Title: "Compute Engine", Issues: []int{371, 322, 332, 355, 351, 356, 358, 359, 350, 342, 345, 347, 365}, Scope: "zonal",
	SelfLink: "https://www.googleapis.com/compute/v1/",
	Collections: []*Collection{
		{ID: "networks", Schema: "Network", IDField: "name", ShortName: true, Limit: 15, Permission: "compute.networks",
			Generate: map[string]any{"routingConfig": map[string]any{"routingMode": "REGIONAL"}, "mtu": 1460, "networkFirewallPolicyEnforcementOrder": "AFTER_CLASSIC_FIREWALL"},
			Mutable:  []string{"routingConfig"}},
		{ID: "subnetworks", Schema: "Subnetwork", IDField: "name", ShortName: true, Limit: 100, Permission: "compute.subnetworks",
			Generate:   map[string]any{"fingerprint": "$fingerprint", "state": "READY", "purpose": "PRIVATE", "stackType": "IPV4_ONLY", "privateIpGoogleAccess": false},
			Mutable:    []string{"privateIpGoogleAccess", "secondaryIpRanges", "logConfig", "enableFlowLogs", "role", "allowSubnetCidrRoutesOverlap"},
			References: map[string][]string{"network": {"networks"}}, Conflict: computeSubnetConflict},
		{ID: "firewalls", Schema: "Firewall", IDField: "name", ShortName: true, Limit: 100, Permission: "compute.firewalls",
			Generate: map[string]any{"direction": "INGRESS", "priority": 1000, "disabled": false},
			Mutable: []string{"description", "allowed", "denied", "sourceRanges", "destinationRanges", "sourceTags", "targetTags",
				"sourceServiceAccounts", "targetServiceAccounts", "priority", "disabled", "logConfig"},
			References: map[string][]string{"network": {"networks"}}},
		{ID: "routes", Schema: "Route", IDField: "name", ShortName: true, Limit: 250, Permission: "compute.routes",
			Generate:   map[string]any{"routeType": "STATIC"},
			References: map[string][]string{"network": {"networks"}}, Weak: map[string][]string{"nextHopVpnTunnel": {"vpnTunnels"}}},
		{ID: "disks", Schema: "Disk", IDField: "name", ShortName: true, Limit: 100, Permission: "compute.disks",
			Generate: map[string]any{"status": "READY", "labelFingerprint": "$fingerprint", "physicalBlockSizeBytes": "4096", "sizeGb": "500"},
			Weak:     map[string][]string{"sourceDisk": {"disks"}, "sourceSnapshot": {"snapshots"}}},
		{ID: "snapshots", Schema: "Snapshot", IDField: "name", ShortName: true, Limit: 100, Permission: "compute.snapshots",
			Generate: map[string]any{"status": "READY", "labelFingerprint": "$fingerprint", "storageBytes": "0", "storageBytesStatus": "UP_TO_DATE", "autoCreated": false},
			Weak:     map[string][]string{"sourceDisk": {"disks"}}},
		{ID: "instanceTemplates", Schema: "InstanceTemplate", IDField: "name", ShortName: true, Limit: 100, Permission: "compute.instanceTemplates",
			References: map[string][]string{"properties.networkInterfaces[].network": {"networks"}, "properties.networkInterfaces[].subnetwork": {"subnetworks"}}},
		{ID: "routers", Schema: "Router", IDField: "name", ShortName: true, Limit: 20, Permission: "compute.routers",
			Mutable: []string{"description", "bgp", "bgpPeers", "interfaces", "nats"},
			References: map[string][]string{"network": {"networks"}, "interfaces[].linkedVpnTunnel": {"vpnTunnels"}, "interfaces[].subnetwork": {"subnetworks"},
				"interfaces[].linkedInterconnectAttachment": {"interconnectAttachments"}, "nats[].subnetworks[].name": {"subnetworks"}}},
		{ID: "vpnGateways", Schema: "VpnGateway", IDField: "name", ShortName: true, Limit: 20, Permission: "compute.vpnGateways",
			Generate:   map[string]any{"labelFingerprint": "$fingerprint", "stackType": "IPV4_ONLY", "vpnInterfaces": []any{map[string]any{"id": 0}, map[string]any{"id": 1}}},
			References: map[string][]string{"network": {"networks"}}},
		{ID: "externalVpnGateways", Schema: "ExternalVpnGateway", IDField: "name", ShortName: true, Limit: 20, Permission: "compute.externalVpnGateways",
			Generate: map[string]any{"labelFingerprint": "$fingerprint"}},
		{ID: "vpnTunnels", Schema: "VpnTunnel", IDField: "name", ShortName: true, Limit: 40, Permission: "compute.vpnTunnels",
			Generate: map[string]any{"labelFingerprint": "$fingerprint", "ikeVersion": 2, "status": "WAITING_FOR_FULL_CONFIG",
				"detailedStatus": "The simulator stores no shared secret and runs no IKE session."},
			References: map[string][]string{"vpnGateway": {"vpnGateways"}, "peerGcpGateway": {"vpnGateways"}, "peerExternalGateway": {"externalVpnGateways"}, "router": {"routers"}}},
		{ID: "securityPolicies", Schema: "SecurityPolicy", IDField: "name", ShortName: true, Limit: 20, Permission: "compute.securityPolicies",
			Generate: map[string]any{"fingerprint": "$fingerprint", "labelFingerprint": "$fingerprint", "type": "CLOUD_ARMOR", "rules": []any{computeDefaultRule}},
			Mutable:  []string{"description", "adaptiveProtectionConfig", "advancedOptionsConfig", "ddosProtectionConfig", "recaptchaOptionsConfig"}},
		{ID: "healthChecks", Schema: "HealthCheck", IDField: "name", ShortName: true, Limit: 50, Permission: "compute.healthChecks",
			Generate: map[string]any{"checkIntervalSec": 5, "timeoutSec": 5, "healthyThreshold": 2, "unhealthyThreshold": 2},
			Mutable: []string{"description", "checkIntervalSec", "timeoutSec", "healthyThreshold", "unhealthyThreshold", "type", "tcpHealthCheck", "sslHealthCheck",
				"httpHealthCheck", "httpsHealthCheck", "http2HealthCheck", "grpcHealthCheck", "grpcTlsHealthCheck", "logConfig", "sourceRegions"}},
		{ID: "backendServices", Schema: "BackendService", IDField: "name", ShortName: true, Limit: 50, Permission: "compute.backendServices",
			Generate: map[string]any{"fingerprint": "$fingerprint", "protocol": "HTTP", "timeoutSec": 30, "port": 80, "portName": "http", "sessionAffinity": "NONE", "loadBalancingScheme": "EXTERNAL"},
			Mutable: []string{"description", "healthChecks", "timeoutSec", "port", "portName", "protocol", "sessionAffinity", "affinityCookieTtlSec", "enableCDN", "cdnPolicy",
				"connectionDraining", "logConfig", "customRequestHeaders", "customResponseHeaders", "compressionMode", "iap", "localityLbPolicy", "outlierDetection",
				"circuitBreakers", "consistentHash", "maxStreamDuration"},
			References: map[string][]string{"healthChecks[]": {"healthChecks"}, "securityPolicy": {"securityPolicies"}, "network": {"networks"}}},
		{ID: "backendBuckets", Schema: "BackendBucket", IDField: "name", ShortName: true, Limit: 50, Permission: "compute.backendBuckets",
			Generate: map[string]any{"enableCdn": false},
			Mutable:  []string{"description", "bucketName", "enableCdn", "cdnPolicy", "compressionMode", "customResponseHeaders"}},
		{ID: "urlMaps", Schema: "UrlMap", IDField: "name", ShortName: true, Limit: 50, Permission: "compute.urlMaps",
			Generate: map[string]any{"fingerprint": "$fingerprint"},
			Mutable:  []string{"description", "defaultService", "defaultUrlRedirect", "hostRules", "pathMatchers", "headerAction"},
			References: map[string][]string{"defaultService": {"backendServices", "backendBuckets"}, "pathMatchers[].defaultService": {"backendServices", "backendBuckets"},
				"pathMatchers[].pathRules[].service": {"backendServices", "backendBuckets"}}},
		{ID: "targetHttpProxies", Schema: "TargetHttpProxy", IDField: "name", ShortName: true, Limit: 50, Permission: "compute.targetHttpProxies",
			Generate:   map[string]any{"fingerprint": "$fingerprint"},
			Mutable:    []string{"description", "urlMap", "httpKeepAliveTimeoutSec"},
			References: map[string][]string{"urlMap": {"urlMaps"}}},
		{ID: "globalForwardingRules", Schema: "ForwardingRule", IDField: "name", ShortName: true, Limit: 50, Permission: "compute.globalForwardingRules",
			Generate: map[string]any{"fingerprint": "$fingerprint", "labelFingerprint": "$fingerprint", "IPProtocol": "TCP", "loadBalancingScheme": "EXTERNAL",
				"networkTier": "PREMIUM", "ipVersion": "IPV4", "portRange": "80-80"},
			Mutable:    []string{"networkTier"},
			References: map[string][]string{"target": {"targetHttpProxies"}, "network": {"networks"}}},
		{ID: "interconnectAttachments", Schema: "InterconnectAttachment", IDField: "name", ShortName: true, Limit: 20, Permission: "compute.interconnectAttachments",
			Generate:   map[string]any{"labelFingerprint": "$fingerprint", "state": "PENDING_PARTNER", "operationalStatus": "OS_UNPROVISIONED", "adminEnabled": false, "mtu": 1440, "stackType": "IPV4_ONLY"},
			Mutable:    []string{"description", "adminEnabled", "mtu"},
			References: map[string][]string{"router": {"routers"}}},
	},
	Bindings: []Binding{
		{Method: "networks.insert", Kind: Create, Collection: "networks", Check: computeNetwork},
		{Method: "networks.get", Kind: Get, Collection: "networks"},
		{Method: "networks.list", Kind: List, Collection: "networks"},
		{Method: "networks.patch", Kind: Patch, Collection: "networks", Check: computeSecrets},
		{Method: "networks.delete", Kind: Delete, Collection: "networks"},
		{Method: "subnetworks.insert", Kind: Create, Collection: "subnetworks", Check: computeSubnetwork},
		{Method: "subnetworks.get", Kind: Get, Collection: "subnetworks", Refuse: []string{"views"}},
		{Method: "subnetworks.list", Kind: List, Collection: "subnetworks", Refuse: []string{"views"}},
		{Method: "subnetworks.patch", Kind: Patch, Collection: "subnetworks", Fingerprint: "fingerprint", Check: computeNeeds("fingerprint"),
			Transition: computeMerged(computeSubnetRanges), Refuse: []string{"drainTimeoutSeconds"}},
		{Method: "subnetworks.expandIpCidrRange", Kind: Set, Collection: "subnetworks", Transition: computeExpand},
		{Method: "subnetworks.setPrivateIpGoogleAccess", Kind: Set, Collection: "subnetworks"},
		{Method: "subnetworks.delete", Kind: Delete, Collection: "subnetworks"},
		{Method: "firewalls.insert", Kind: Create, Collection: "firewalls", Check: computeAll(computeSecrets, computeNeeds("network"), computeFirewall)},
		{Method: "firewalls.get", Kind: Get, Collection: "firewalls"},
		{Method: "firewalls.list", Kind: List, Collection: "firewalls"},
		{Method: "firewalls.patch", Kind: Patch, Collection: "firewalls", Check: computeSecrets, Transition: computeMerged(computeFirewall)},
		{Method: "firewalls.delete", Kind: Delete, Collection: "firewalls"},
		{Method: "routes.insert", Kind: Create, Collection: "routes", Check: computeAll(computeSecrets, computeRoute)},
		{Method: "routes.get", Kind: Get, Collection: "routes"},
		{Method: "routes.list", Kind: List, Collection: "routes"},
		{Method: "routes.delete", Kind: Delete, Collection: "routes"},
		{Method: "disks.insert", Kind: Create, Collection: "disks", Check: computeDisk, Refuse: []string{"sourceImage"}},
		{Method: "disks.get", Kind: Get, Collection: "disks"},
		{Method: "disks.list", Kind: List, Collection: "disks"},
		{Method: "disks.setLabels", Kind: Set, Collection: "disks", Fingerprint: "labelFingerprint", Check: computeNeeds("labelFingerprint")},
		{Method: "disks.resize", Kind: Set, Collection: "disks", Check: computeNeeds("sizeGb"), Transition: computeGrow},
		{Method: "disks.delete", Kind: Delete, Collection: "disks"},
		{Method: "snapshots.insert", Kind: Create, Collection: "snapshots", Check: computeSnapshot},
		{Method: "snapshots.get", Kind: Get, Collection: "snapshots"},
		{Method: "snapshots.list", Kind: List, Collection: "snapshots"},
		{Method: "snapshots.setLabels", Kind: Set, Collection: "snapshots", Fingerprint: "labelFingerprint", Check: computeNeeds("labelFingerprint")},
		{Method: "snapshots.delete", Kind: Delete, Collection: "snapshots"},
		{Method: "instanceTemplates.insert", Kind: Create, Collection: "instanceTemplates", Check: computeInstanceTemplate},
		{Method: "instanceTemplates.get", Kind: Get, Collection: "instanceTemplates"},
		{Method: "instanceTemplates.list", Kind: List, Collection: "instanceTemplates"},
		{Method: "instanceTemplates.delete", Kind: Delete, Collection: "instanceTemplates"},
		{Method: "routers.insert", Kind: Create, Collection: "routers", Check: computeAll(computeSecrets, computeRouter)},
		{Method: "routers.get", Kind: Get, Collection: "routers"},
		{Method: "routers.list", Kind: List, Collection: "routers"},
		{Method: "routers.patch", Kind: Patch, Collection: "routers", Check: computeSecrets, Transition: computeMerged(computeRouter)},
		{Method: "routers.delete", Kind: Delete, Collection: "routers"},
		{Method: "vpnGateways.insert", Kind: Create, Collection: "vpnGateways", Check: computeVPNGateway},
		{Method: "vpnGateways.get", Kind: Get, Collection: "vpnGateways"},
		{Method: "vpnGateways.list", Kind: List, Collection: "vpnGateways"},
		{Method: "vpnGateways.setLabels", Kind: Set, Collection: "vpnGateways", Fingerprint: "labelFingerprint", Check: computeNeeds("labelFingerprint")},
		{Method: "vpnGateways.delete", Kind: Delete, Collection: "vpnGateways"},
		{Method: "externalVpnGateways.insert", Kind: Create, Collection: "externalVpnGateways", Check: computeExternalGateway},
		{Method: "externalVpnGateways.get", Kind: Get, Collection: "externalVpnGateways"},
		{Method: "externalVpnGateways.list", Kind: List, Collection: "externalVpnGateways"},
		{Method: "externalVpnGateways.setLabels", Kind: Set, Collection: "externalVpnGateways", Fingerprint: "labelFingerprint", Check: computeNeeds("labelFingerprint")},
		{Method: "externalVpnGateways.delete", Kind: Delete, Collection: "externalVpnGateways"},
		{Method: "vpnTunnels.insert", Kind: Create, Collection: "vpnTunnels", Check: computeVPNTunnel},
		{Method: "vpnTunnels.get", Kind: Get, Collection: "vpnTunnels"},
		{Method: "vpnTunnels.list", Kind: List, Collection: "vpnTunnels"},
		{Method: "vpnTunnels.setLabels", Kind: Set, Collection: "vpnTunnels", Fingerprint: "labelFingerprint", Check: computeNeeds("labelFingerprint")},
		{Method: "vpnTunnels.delete", Kind: Delete, Collection: "vpnTunnels"},
		{Method: "securityPolicies.insert", Kind: Create, Collection: "securityPolicies", Check: computeSecurityPolicy},
		{Method: "securityPolicies.get", Kind: Get, Collection: "securityPolicies"},
		{Method: "securityPolicies.list", Kind: List, Collection: "securityPolicies"},
		{Method: "securityPolicies.patch", Kind: Patch, Collection: "securityPolicies", Fingerprint: "fingerprint", Check: computeSecrets, Refuse: []string{"updateMask"}},
		{Method: "securityPolicies.setLabels", Kind: Set, Collection: "securityPolicies", Fingerprint: "labelFingerprint", Check: computeNeeds("labelFingerprint")},
		{Method: "securityPolicies.delete", Kind: Delete, Collection: "securityPolicies"},
		{Method: "healthChecks.insert", Kind: Create, Collection: "healthChecks", Check: computeAll(computeSecrets, computeHealthCheck)},
		{Method: "healthChecks.get", Kind: Get, Collection: "healthChecks"},
		{Method: "healthChecks.list", Kind: List, Collection: "healthChecks"},
		{Method: "healthChecks.patch", Kind: Patch, Collection: "healthChecks", Check: computeSecrets, Transition: computeMerged(computeHealthCheck)},
		{Method: "healthChecks.delete", Kind: Delete, Collection: "healthChecks"},
		{Method: "backendServices.insert", Kind: Create, Collection: "backendServices", Check: computeBackendService},
		{Method: "backendServices.get", Kind: Get, Collection: "backendServices"},
		{Method: "backendServices.list", Kind: List, Collection: "backendServices"},
		{Method: "backendServices.patch", Kind: Patch, Collection: "backendServices", Fingerprint: "fingerprint", Check: computeBackendService, Transition: computeMerged(computeCDNScheme)},
		{Method: "backendServices.setSecurityPolicy", Kind: Set, Collection: "backendServices"},
		{Method: "backendServices.delete", Kind: Delete, Collection: "backendServices"},
		{Method: "backendBuckets.insert", Kind: Create, Collection: "backendBuckets", Check: computeAll(computeSecrets, computeNeeds("bucketName"))},
		{Method: "backendBuckets.get", Kind: Get, Collection: "backendBuckets"},
		{Method: "backendBuckets.list", Kind: List, Collection: "backendBuckets"},
		{Method: "backendBuckets.patch", Kind: Patch, Collection: "backendBuckets", Check: computeSecrets},
		{Method: "backendBuckets.delete", Kind: Delete, Collection: "backendBuckets"},
		{Method: "urlMaps.insert", Kind: Create, Collection: "urlMaps", Check: computeAll(computeSecrets, computeURLMap)},
		{Method: "urlMaps.get", Kind: Get, Collection: "urlMaps"},
		{Method: "urlMaps.list", Kind: List, Collection: "urlMaps"},
		{Method: "urlMaps.patch", Kind: Patch, Collection: "urlMaps", Fingerprint: "fingerprint", Check: computeSecrets, Transition: computeMerged(computeURLMap)},
		{Method: "urlMaps.delete", Kind: Delete, Collection: "urlMaps"},
		{Method: "targetHttpProxies.insert", Kind: Create, Collection: "targetHttpProxies", Check: computeAll(computeSecrets, computeNeeds("urlMap"))},
		{Method: "targetHttpProxies.get", Kind: Get, Collection: "targetHttpProxies"},
		{Method: "targetHttpProxies.list", Kind: List, Collection: "targetHttpProxies"},
		{Method: "targetHttpProxies.patch", Kind: Patch, Collection: "targetHttpProxies", Fingerprint: "fingerprint", Check: computeSecrets},
		{Method: "targetHttpProxies.delete", Kind: Delete, Collection: "targetHttpProxies"},
		{Method: "globalForwardingRules.insert", Kind: Create, Collection: "globalForwardingRules", Check: computeForwardingRule},
		{Method: "globalForwardingRules.get", Kind: Get, Collection: "globalForwardingRules", Refuse: []string{"view"}},
		{Method: "globalForwardingRules.list", Kind: List, Collection: "globalForwardingRules"},
		{Method: "globalForwardingRules.patch", Kind: Patch, Collection: "globalForwardingRules", Check: computeAll(computeSecrets, computePremium)},
		{Method: "globalForwardingRules.setTarget", Kind: Set, Collection: "globalForwardingRules", Check: computeNeeds("target")},
		{Method: "globalForwardingRules.setLabels", Kind: Set, Collection: "globalForwardingRules", Fingerprint: "labelFingerprint", Check: computeNeeds("labelFingerprint")},
		{Method: "globalForwardingRules.delete", Kind: Delete, Collection: "globalForwardingRules"},
		{Method: "interconnectAttachments.insert", Kind: Create, Collection: "interconnectAttachments", Check: computeAttachment},
		{Method: "interconnectAttachments.get", Kind: Get, Collection: "interconnectAttachments"},
		{Method: "interconnectAttachments.list", Kind: List, Collection: "interconnectAttachments"},
		{Method: "interconnectAttachments.patch", Kind: Patch, Collection: "interconnectAttachments", Check: computeAll(computeSecrets, computeMTU)},
		{Method: "interconnectAttachments.setLabels", Kind: Set, Collection: "interconnectAttachments", Fingerprint: "labelFingerprint", Check: computeNeeds("labelFingerprint")},
		{Method: "interconnectAttachments.delete", Kind: Delete, Collection: "interconnectAttachments"},
		{Method: "globalOperations.get", Kind: OperationGet},
		{Method: "globalOperations.list", Kind: OperationList},
		{Method: "globalOperations.wait", Kind: OperationGet},
		{Method: "regionOperations.get", Kind: OperationGet},
		{Method: "regionOperations.list", Kind: OperationList},
		{Method: "regionOperations.wait", Kind: OperationGet},
		{Method: "zoneOperations.get", Kind: OperationGet},
		{Method: "zoneOperations.list", Kind: OperationList},
		{Method: "zoneOperations.wait", Kind: OperationGet},
	},
	Bounds: []string{
		"Networks, subnetworks, firewall rules, routes, disks, snapshots and instance templates are configuration metadata: no packet is routed or filtered, no VM runs and no block storage exists.",
		"Only custom mode VPC networks are accepted (autoCreateSubnetworks=false); no default network, default internet route or subnet route is created, and Network.subnetworks and peerings are not maintained.",
		"Disks and snapshots hold no data: sizes are recorded as given (blank disks default to 500 GB), a disk created from a source must state sizeGb, snapshots report storageBytes 0 and do not copy the source size, and customer-supplied encryption keys are refused.",
		"Instances are not simulated: instances.insert and every other instance method return UNIMPLEMENTED, because inserting an instance boots a VM; instance templates store the configuration only.",
		"References must name existing resources of this project; a resource still referenced (for example a network used by a subnetwork, a health check used by a backend service, or a VPN tunnel linked from a Cloud Router interface) cannot be deleted, and subnetwork ranges may not overlap within a network.",
		"Cloud Router and Cloud NAT are configuration only: no BGP session is established and no NAT address is allocated; MD5 keys, route policies and router appliances are refused, and NAT gateways need AUTO_ONLY allocation.",
		"HA VPN gateways, external VPN gateways and VPN tunnels are configuration only: shared secrets are refused and never stored, no IKE session runs, tunnels report WAITING_FOR_FULL_CONFIG, HA VPN interfaces carry no external IP address, and Classic VPN is not simulated.",
		"Cloud Armor security policies keep the rules given at creation (a default allow rule is added when none is given) because addRule, patchRule and removeRule are not simulated, and no request is ever evaluated; Cloud Armor Enterprise enrollment is not simulated, while policy settings such as adaptiveProtectionConfig are stored.",
		"Health checks, backend services, backend buckets, URL maps, target HTTP proxies and global forwarding rules are configuration only: no probe runs, no request is served, no address is allocated (IPAddress must be stated), backend services accept no instance groups or network endpoint groups, backend bucket names are not checked against Cloud Storage, and Cloud CDN settings are stored without any cache.",
		"Global forwarding rules use the Premium network tier; the project default network tier (projects.setDefaultNetworkTier) cannot be changed.",
		"Partner Interconnect VLAN attachments stay PENDING_PARTNER and OS_UNPROVISIONED without a pairing key because no partner exists; Dedicated, Cross-Cloud and Cross-Site Interconnect need physical links and are not simulated.",
		"Operations complete synchronously with status DONE and remain readable through zoneOperations, regionOperations and globalOperations get, list and wait.",
	},
}

func computeAll(checks ...func(map[string]any) error) func(map[string]any) error {
	return func(body map[string]any) error {
		for _, check := range checks {
			if err := check(body); err != nil {
				return err
			}
		}
		return nil
	}
}

// computeNeeds requires top-level request fields the official schema leaves optional.
func computeNeeds(fields ...string) func(map[string]any) error {
	return func(body map[string]any) error {
		for _, field := range fields {
			if value, ok := body[field]; !ok || value == "" {
				return invalid("Field '%s' is required.", field)
			}
		}
		return nil
	}
}

// computeRefuse rejects top-level fields whose effect the simulator does not have.
func computeRefuse(reason string, fields ...string) func(map[string]any) error {
	return func(body map[string]any) error {
		for _, field := range fields {
			if _, ok := body[field]; ok {
				return invalid("Field '%s' is not supported by the simulator: %s", field, reason)
			}
		}
		return nil
	}
}

// computeSecrets refuses key material anywhere in a request: customer-supplied
// encryption keys are never stored, and input-only params bind tags the
// simulator does not have.
func computeSecrets(body map[string]any) error {
	var walk func(any) error
	walk = func(value any) error {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				if key == "rawKey" || key == "rsaEncryptedKey" {
					return invalid("Field '%s' is refused: customer-supplied encryption keys are never stored by the simulator; use kmsKeyName.", key)
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range v {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(body); err != nil {
		return err
	}
	return computeRefuse("resource manager tags are not simulated.", "params")(body)
}

// computeMerged validates a patch or custom method against the resulting top-level
// fields.
func computeMerged(check func(map[string]any) error) func(current, body map[string]any) error {
	return func(current, body map[string]any) error {
		out := map[string]any{}
		for k, v := range current {
			out[k] = v
		}
		for k, v := range body {
			out[k] = v
		}
		return check(out)
	}
}

func computeText(body map[string]any, field string) string {
	value, _ := body[field].(string)
	return value
}

func computeList(body map[string]any, field string) []any {
	value, _ := body[field].([]any)
	return value
}

// computeCIDR parses a canonical IPv4 or IPv6 prefix, or a bare address.
func computeCIDR(field, value string, bare bool) (netip.Prefix, error) {
	if bare && !strings.Contains(value, "/") {
		if addr, err := netip.ParseAddr(value); err == nil {
			return netip.PrefixFrom(addr, addr.BitLen()), nil
		}
	}
	p, err := netip.ParsePrefix(value)
	if err != nil || p.Masked() != p {
		return netip.Prefix{}, invalid("Invalid value for field '%s': '%s'. Expected a CIDR range such as 10.0.0.0/24.", field, value)
	}
	return p, nil
}

func computeNumber(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil
	case fmt.Stringer:
		n, err := strconv.ParseInt(v.String(), 10, 64)
		return n, err == nil
	}
	return 0, false
}

func computeNetwork(body map[string]any) error {
	if err := computeSecrets(body); err != nil {
		return err
	}
	if auto, ok := body["autoCreateSubnetworks"].(bool); !ok || auto {
		return invalid("Field 'autoCreateSubnetworks' must be false: only custom mode VPC networks are simulated, and their subnetworks are created explicitly.")
	}
	if err := computeRefuse("IPv6 ranges and network profiles are not allocated.", "IPv4Range", "internalIpv6Range", "networkProfile")(body); err != nil {
		return err
	}
	if body["enableUlaInternalIpv6"] == true {
		return invalid("Field 'enableUlaInternalIpv6' is not supported by the simulator: no IPv6 range is allocated.")
	}
	if mtu, ok := computeNumber(body["mtu"]); ok && (mtu < 1300 || mtu > 8896) {
		return invalid("Invalid value for field 'mtu': %d. The MTU must be between 1300 and 8896.", mtu)
	}
	return nil
}

func computeSubnetwork(body map[string]any) error {
	if err := computeAll(computeSecrets, computeNeeds("network", "ipCidrRange"), computeRefuse("IPv6 and BYOIP ranges are not allocated.",
		"ipCollection", "reservedInternalRange", "externalIpv6Prefix", "internalIpv6Prefix", "ipv6AccessType"))(body); err != nil {
		return err
	}
	if stack := computeText(body, "stackType"); stack != "" && stack != "IPV4_ONLY" {
		return invalid("Field 'stackType' must be IPV4_ONLY: no IPv6 range is allocated by the simulator.")
	}
	return computeSubnetRanges(body)
}

// computeRanges lists a subnetwork's primary and secondary ranges.
func computeRanges(body map[string]any) ([]netip.Prefix, error) {
	primary, err := computeCIDR("ipCidrRange", computeText(body, "ipCidrRange"), false)
	if err != nil {
		return nil, err
	}
	if !primary.Addr().Is4() || primary.Bits() < 8 || primary.Bits() > 29 {
		return nil, invalid("Invalid value for field 'ipCidrRange': '%s'. Subnetwork ranges must be IPv4 prefixes from /8 to /29.", primary)
	}
	out := []netip.Prefix{primary}
	for i, item := range computeList(body, "secondaryIpRanges") {
		secondary, _ := item.(map[string]any)
		if _, ok := secondary["reservedInternalRange"]; ok {
			return nil, invalid("Field 'secondaryIpRanges[%d].reservedInternalRange' is not supported by the simulator.", i)
		}
		if computeText(secondary, "rangeName") == "" {
			return nil, invalid("Field 'secondaryIpRanges[%d].rangeName' is required.", i)
		}
		p, err := computeCIDR(fmt.Sprintf("secondaryIpRanges[%d].ipCidrRange", i), computeText(secondary, "ipCidrRange"), false)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func computeSubnetRanges(body map[string]any) error {
	prefixes, err := computeRanges(body)
	if err != nil {
		return err
	}
	for i := range prefixes {
		for j := i + 1; j < len(prefixes); j++ {
			if prefixes[i].Overlaps(prefixes[j]) {
				return invalid("Invalid IPCidrRange: %s conflicts with %s in the same subnetwork.", prefixes[j], prefixes[i])
			}
		}
	}
	return nil
}

// computeSubnetConflict refuses ranges that overlap another subnetwork of the
// same network.
func computeSubnetConflict(candidate, other map[string]any) error {
	if candidate["network"] != other["network"] {
		return nil
	}
	mine, err := computeRanges(candidate)
	if err != nil {
		return err
	}
	theirs, _ := computeRanges(other)
	for _, a := range mine {
		for _, b := range theirs {
			if a.Overlaps(b) {
				return invalid("Invalid IPCidrRange: %s conflicts with existing subnetwork '%s' (%s).", a, other["name"], b)
			}
		}
	}
	return nil
}

// computeExpand only widens a subnetwork's primary range.
func computeExpand(current, body map[string]any) error {
	next, err := computeCIDR("ipCidrRange", computeText(body, "ipCidrRange"), false)
	if err != nil {
		return err
	}
	previous, _ := netip.ParsePrefix(computeText(current, "ipCidrRange"))
	if next.Bits() >= previous.Bits() || !next.Contains(previous.Addr()) || next.Bits() < 8 {
		return invalid("The new range %s must contain the current range %s and be larger.", next, previous)
	}
	return nil
}

var computeProtocols = map[string]bool{"tcp": true, "udp": true, "icmp": true, "esp": true, "ah": true, "sctp": true, "ipip": true, "all": true}

func computeFirewall(body map[string]any) error {
	allowed, denied := computeList(body, "allowed"), computeList(body, "denied")
	if len(allowed) == 0 == (len(denied) == 0) {
		return invalid("Exactly one of 'allowed' or 'denied' must be specified.")
	}
	for i, item := range append(allowed, denied...) {
		rule, _ := item.(map[string]any)
		protocol := strings.ToLower(computeText(rule, "IPProtocol"))
		if n, err := strconv.Atoi(protocol); !computeProtocols[protocol] && (err != nil || n < 0 || n > 255) {
			return invalid("Invalid value for field 'rules[%d].IPProtocol': '%s'.", i, computeText(rule, "IPProtocol"))
		}
		if len(computeList(rule, "ports")) > 0 && protocol != "tcp" && protocol != "udp" && protocol != "sctp" {
			return invalid("Ports may be specified only for tcp, udp or sctp rules.")
		}
	}
	if priority, ok := computeNumber(body["priority"]); ok && (priority < 0 || priority > 65535) {
		return invalid("Invalid value for field 'priority': %d. Must be between 0 and 65535.", priority)
	}
	for _, field := range []string{"sourceRanges", "destinationRanges"} {
		for _, value := range computeList(body, field) {
			if _, err := computeCIDR(field, fmt.Sprint(value), true); err != nil {
				return err
			}
		}
	}
	if len(computeList(body, "sourceTags")) > 0 && len(computeList(body, "sourceServiceAccounts")) > 0 || len(computeList(body, "targetTags")) > 0 && len(computeList(body, "targetServiceAccounts")) > 0 {
		return invalid("Tags and service accounts cannot be combined in one firewall rule.")
	}
	if computeText(body, "direction") == "EGRESS" {
		if len(computeList(body, "sourceTags")) > 0 || len(computeList(body, "sourceServiceAccounts")) > 0 {
			return invalid("Egress firewall rules cannot use source tags or source service accounts.")
		}
	} else if len(computeList(body, "sourceRanges"))+len(computeList(body, "sourceTags"))+len(computeList(body, "sourceServiceAccounts")) == 0 {
		return invalid("Ingress firewall rules need sourceRanges, sourceTags or sourceServiceAccounts.")
	}
	return nil
}

func computeRoute(body map[string]any) error {
	if _, err := computeCIDR("destRange", computeText(body, "destRange"), false); err != nil {
		return err
	}
	if priority, ok := computeNumber(body["priority"]); !ok || priority < 0 || priority > 65535 {
		return invalid("Invalid value for field 'priority': must be between 0 and 65535.")
	}
	if err := computeRefuse("instances, internal load balancers and peer networks are not simulated as next hops.", "nextHopInstance", "nextHopIlb", "nextHopNetwork")(body); err != nil {
		return err
	}
	hops := 0
	for _, field := range []string{"nextHopGateway", "nextHopIp", "nextHopVpnTunnel"} {
		if computeText(body, field) != "" {
			hops++
		}
	}
	if hops != 1 {
		return invalid("Exactly one of nextHopGateway, nextHopIp or nextHopVpnTunnel must be specified.")
	}
	if gateway := computeText(body, "nextHopGateway"); gateway != "" && !strings.HasSuffix(gateway, "global/gateways/default-internet-gateway") {
		return invalid("Field 'nextHopGateway' may name only global/gateways/default-internet-gateway.")
	}
	if ip := computeText(body, "nextHopIp"); ip != "" {
		if _, err := netip.ParseAddr(ip); err != nil {
			return invalid("Invalid value for field 'nextHopIp': '%s'.", ip)
		}
	}
	return nil
}

// computeDiskTypes are the published Persistent Disk and Hyperdisk types.
var computeDiskTypes = map[string]bool{"pd-standard": true, "pd-balanced": true, "pd-ssd": true, "pd-extreme": true,
	"hyperdisk-balanced": true, "hyperdisk-throughput": true, "hyperdisk-extreme": true, "hyperdisk-ml": true}

func computeDisk(body map[string]any) error {
	if err := computeAll(computeSecrets, computeRefuse("the disk holds no data, replica, pool or import in the simulator.",
		"sourceStorageObject", "asyncPrimaryDisk", "storagePool", "replicaZones", "sourceInstantSnapshot"))(body); err != nil {
		return err
	}
	if kind := computeText(body, "type"); kind != "" {
		_, name, found := strings.Cut(kind, "diskTypes/")
		if !found || !computeDiskTypes[name] {
			return invalid("Invalid value for field 'type': '%s'. Use zones/{zone}/diskTypes/{type}, for example pd-balanced.", kind)
		}
	}
	sources := 0
	for _, field := range []string{"sourceImage", "sourceSnapshot", "sourceDisk"} {
		if computeText(body, field) != "" {
			sources++
		}
	}
	if sources > 1 {
		return invalid("At most one of sourceImage, sourceSnapshot or sourceDisk may be specified.")
	}
	size, sized := computeNumber(body["sizeGb"])
	if sources == 1 && !sized {
		return invalid("Field 'sizeGb' is required with a source: the simulator does not know source sizes.")
	}
	if sized && (size < 1 || size > 65536) {
		return invalid("Invalid value for field 'sizeGb': %d. Must be between 1 and 65536.", size)
	}
	return nil
}

// computeGrow only enlarges a disk, as disks.resize requires.
func computeGrow(current, body map[string]any) error {
	next, _ := computeNumber(body["sizeGb"])
	previous, _ := computeNumber(current["sizeGb"])
	if next <= previous || next > 65536 {
		return invalid("Invalid value for field 'sizeGb': %d. The new size must be larger than %d GB and at most 65536 GB.", next, previous)
	}
	return nil
}

func computeSnapshot(body map[string]any) error {
	if err := computeAll(computeSecrets, computeNeeds("sourceDisk"), computeRefuse("only snapshots of a disk are simulated.", "sourceInstantSnapshot", "sourceDiskForRecoveryCheckpoint"))(body); err != nil {
		return err
	}
	if body["guestFlush"] == true {
		return invalid("Field 'guestFlush' is not supported by the simulator: no guest operating system runs.")
	}
	return nil
}

func computeInstanceTemplate(body map[string]any) error {
	if err := computeAll(computeSecrets, computeNeeds("properties"), computeRefuse("instances are not simulated.", "sourceInstance", "sourceInstanceParams"))(body); err != nil {
		return err
	}
	properties, _ := body["properties"].(map[string]any)
	if computeText(properties, "machineType") == "" {
		return invalid("Field 'properties.machineType' is required.")
	}
	if _, ok := properties["resourceManagerTags"]; ok {
		return invalid("Field 'properties.resourceManagerTags' is not supported by the simulator: resource manager tags are not simulated.")
	}
	for i, item := range computeList(properties, "networkInterfaces") {
		nic, _ := item.(map[string]any)
		if computeText(nic, "network") == "" && computeText(nic, "subnetwork") == "" {
			return invalid("Field 'properties.networkInterfaces[%d]' needs a network or subnetwork: the simulator has no default network.", i)
		}
		if _, ok := nic["networkAttachment"]; ok {
			return invalid("Field 'properties.networkInterfaces[%d].networkAttachment' is not supported by the simulator.", i)
		}
	}
	return nil
}

// computeDefaultRule is the rule Cloud Armor adds to a policy without rules.
var computeDefaultRule = map[string]any{"priority": 2147483647, "action": "allow", "preview": false, "description": "Default rule, higher priority overrides it",
	"match": map[string]any{"versionedExpr": "SRC_IPS_V1", "config": map[string]any{"srcIpRanges": []any{"*"}}}}

func computeObject(body map[string]any, field string) map[string]any {
	value, _ := body[field].(map[string]any)
	return value
}

// computeNames requires unique non-empty names in a list of objects.
func computeNames(field string, items []any) (map[string]bool, error) {
	names := map[string]bool{}
	for i, item := range items {
		name := computeText(item.(map[string]any), "name")
		if name == "" || names[name] {
			return nil, invalid("Field '%s[%d].name' must be present and unique.", field, i)
		}
		names[name] = true
	}
	return names, nil
}

func computeRouter(body map[string]any) error {
	if err := computeRefuse("MD5 keys are secrets and Network Connectivity Center gateways are not simulated.", "md5AuthenticationKeys", "nccGateway")(body); err != nil {
		return err
	}
	if bgp := computeObject(body, "bgp"); bgp != nil {
		asn, _ := computeNumber(bgp["asn"])
		if asn != 16550 && (asn < 64512 || asn > 65534) && (asn < 4200000000 || asn > 4294967294) {
			return invalid("Invalid value for field 'bgp.asn': %d. Use a private ASN (64512-65534 or 4200000000-4294967294) or 16550.", asn)
		}
	}
	interfaces, err := computeNames("interfaces", computeList(body, "interfaces"))
	if err != nil {
		return err
	}
	for i, item := range computeList(body, "interfaces") {
		iface := item.(map[string]any)
		if computeText(iface, "linkedVpnTunnel") != "" && computeText(iface, "linkedInterconnectAttachment") != "" {
			return invalid("Field 'interfaces[%d]' may link one VPN tunnel or one VLAN attachment.", i)
		}
		if r := computeText(iface, "ipRange"); r != "" {
			if _, err := netip.ParsePrefix(r); err != nil {
				return invalid("Invalid value for field 'interfaces[%d].ipRange': '%s'.", i, r)
			}
		}
	}
	if _, err := computeNames("bgpPeers", computeList(body, "bgpPeers")); err != nil {
		return err
	}
	for i, item := range computeList(body, "bgpPeers") {
		peer := item.(map[string]any)
		for _, field := range []string{"md5AuthenticationKeyName", "routerApplianceInstance", "importPolicies", "exportPolicies"} {
			if _, ok := peer[field]; ok {
				return invalid("Field 'bgpPeers[%d].%s' is not supported by the simulator: MD5 keys, router appliances and route policies are not simulated.", i, field)
			}
		}
		if !interfaces[computeText(peer, "interfaceName")] {
			return invalid("Field 'bgpPeers[%d].interfaceName' must name an interface of this router.", i)
		}
		if _, ok := computeNumber(peer["peerAsn"]); !ok {
			return invalid("Field 'bgpPeers[%d].peerAsn' is required.", i)
		}
		if ip := computeText(peer, "peerIpAddress"); ip != "" {
			if _, err := netip.ParseAddr(ip); err != nil {
				return invalid("Invalid value for field 'bgpPeers[%d].peerIpAddress': '%s'.", i, ip)
			}
		}
	}
	if _, err := computeNames("nats", computeList(body, "nats")); err != nil {
		return err
	}
	for i, item := range computeList(body, "nats") {
		nat := item.(map[string]any)
		for _, field := range []string{"natIps", "drainNatIps", "nat64Subnetworks", "sourceSubnetworkIpRangesToNat64", "rules"} {
			if _, ok := nat[field]; ok {
				return invalid("Field 'nats[%d].%s' is not supported by the simulator: static NAT addresses, NAT64 and NAT rules are not simulated.", i, field)
			}
		}
		if computeText(nat, "natIpAllocateOption") != "AUTO_ONLY" {
			return invalid("Field 'nats[%d].natIpAllocateOption' must be AUTO_ONLY.", i)
		}
		if nat["type"] == "PRIVATE" {
			return invalid("Field 'nats[%d].type' must be PUBLIC: Private NAT is not simulated.", i)
		}
		mode, subnets := computeText(nat, "sourceSubnetworkIpRangesToNat"), len(computeList(nat, "subnetworks"))
		if mode == "" || mode == "LIST_OF_SUBNETWORKS" != (subnets > 0) {
			return invalid("Field 'nats[%d].sourceSubnetworkIpRangesToNat' is required, and LIST_OF_SUBNETWORKS needs subnetworks.", i)
		}
	}
	return nil
}

func computeVPNGateway(body map[string]any) error {
	if err := computeAll(computeSecrets, computeRefuse("HA VPN over Cloud Interconnect is not simulated.", "vpnInterfaces"))(body); err != nil {
		return err
	}
	if stack := computeText(body, "stackType"); stack != "" && stack != "IPV4_ONLY" || computeText(body, "gatewayIpVersion") == "IPV6" {
		return invalid("HA VPN gateways must be IPv4 only: no IPv6 address is allocated by the simulator.")
	}
	return nil
}

var computeRedundancy = map[string]int{"SINGLE_IP_INTERNALLY_REDUNDANT": 1, "TWO_IPS_REDUNDANCY": 2, "FOUR_IPS_REDUNDANCY": 4}

func computeExternalGateway(body map[string]any) error {
	if err := computeSecrets(body); err != nil {
		return err
	}
	interfaces := computeList(body, "interfaces")
	if computeRedundancy[computeText(body, "redundancyType")] != len(interfaces) {
		return invalid("Field 'interfaces' must list 1, 2 or 4 interfaces as redundancyType states.")
	}
	seen := map[int64]bool{}
	for i, item := range interfaces {
		iface := item.(map[string]any)
		id, _ := computeNumber(iface["id"])
		if seen[id] || id < 0 || id >= int64(len(interfaces)) {
			return invalid("Field 'interfaces[%d].id' must be unique and below %d.", i, len(interfaces))
		}
		seen[id] = true
		if addr, err := netip.ParseAddr(computeText(iface, "ipAddress")); err != nil || !addr.Is4() {
			return invalid("Field 'interfaces[%d].ipAddress' must be an IPv4 address.", i)
		}
	}
	return nil
}

func computeVPNTunnel(body map[string]any) error {
	if err := computeAll(computeSecrets,
		computeRefuse("shared secrets are never stored; the simulator runs no IKE session.", "sharedSecret", "sharedSecretHash"),
		computeRefuse("Classic VPN and traffic selectors are not simulated.", "targetVpnGateway", "peerIp", "localTrafficSelector", "remoteTrafficSelector"),
		computeNeeds("vpnGateway", "router"))(body); err != nil {
		return err
	}
	if n, ok := computeNumber(body["vpnGatewayInterface"]); !ok || n < 0 || n > 1 {
		return invalid("Field 'vpnGatewayInterface' must be 0 or 1.")
	}
	external, peer := computeText(body, "peerExternalGateway") != "", computeText(body, "peerGcpGateway") != ""
	if external == peer {
		return invalid("Exactly one of peerExternalGateway or peerGcpGateway must be specified.")
	}
	if _, ok := computeNumber(body["peerExternalGatewayInterface"]); external != ok {
		return invalid("Field 'peerExternalGatewayInterface' is required with peerExternalGateway only.")
	}
	if v, ok := computeNumber(body["ikeVersion"]); ok && v != 1 && v != 2 {
		return invalid("Field 'ikeVersion' must be 1 or 2.")
	}
	return nil
}

func computeSecurityPolicy(body map[string]any) error {
	if err := computeAll(computeSecrets, computeRefuse("network edge and organization policies are not simulated.", "userDefinedFields", "associations", "shortName"))(body); err != nil {
		return err
	}
	if kind := computeText(body, "type"); kind != "" && kind != "CLOUD_ARMOR" && kind != "CLOUD_ARMOR_EDGE" {
		return invalid("Field 'type' must be CLOUD_ARMOR or CLOUD_ARMOR_EDGE.")
	}
	rules, ok := body["rules"].([]any)
	if !ok {
		return nil
	}
	priorities, defaulted := map[int64]bool{}, false
	for i, item := range rules {
		rule := item.(map[string]any)
		if _, ok := rule["networkMatch"]; ok {
			return invalid("Field 'rules[%d].networkMatch' is not supported by the simulator: network edge policies are not simulated.", i)
		}
		priority, ok := computeNumber(rule["priority"])
		if !ok || priority < 0 || priority > 2147483647 || priorities[priority] {
			return invalid("Field 'rules[%d].priority' must be unique and between 0 and 2147483647.", i)
		}
		priorities[priority], defaulted = true, defaulted || priority == 2147483647
		if computeText(rule, "action") == "" {
			return invalid("Field 'rules[%d].action' is required.", i)
		}
		match := computeObject(rule, "match")
		if match == nil || (match["versionedExpr"] == nil) == (match["expr"] == nil) {
			return invalid("Field 'rules[%d].match' needs exactly one of versionedExpr or expr.", i)
		}
	}
	if !defaulted {
		return invalid("Field 'rules' must include the default rule with priority 2147483647.")
	}
	return nil
}

var computeProbes = map[string]string{"TCP": "tcpHealthCheck", "SSL": "sslHealthCheck", "HTTP": "httpHealthCheck", "HTTPS": "httpsHealthCheck",
	"HTTP2": "http2HealthCheck", "GRPC": "grpcHealthCheck", "GRPC_WITH_TLS": "grpcTlsHealthCheck"}

func computeHealthCheck(body map[string]any) error {
	probe := computeProbes[computeText(body, "type")]
	if probe == "" || body[probe] == nil {
		return invalid("Field 'type' is required and needs its matching configuration, for example HTTP with httpHealthCheck.")
	}
	interval, ok := computeNumber(body["checkIntervalSec"])
	if !ok {
		interval = 5
	}
	if timeout, ok := computeNumber(body["timeoutSec"]); ok && timeout > interval {
		return invalid("Field 'timeoutSec' must not exceed checkIntervalSec.")
	}
	return nil
}

func computeBackendService(body map[string]any) error {
	if err := computeAll(computeSecrets, computeRefuse("instance groups, network endpoint groups, mTLS policies and service bindings are not simulated.",
		"backends", "securitySettings", "serviceBindings", "serviceLbPolicy", "tlsSettings", "haPolicy", "failoverPolicy"))(body); err != nil {
		return err
	}
	if iap := computeObject(body, "iap"); iap != nil {
		if _, ok := iap["oauth2ClientSecret"]; ok {
			return invalid("Field 'iap.oauth2ClientSecret' is refused: client secrets are never stored by the simulator.")
		}
	}
	if computeText(body, "loadBalancingScheme") == "INTERNAL" {
		return invalid("Field 'loadBalancingScheme' INTERNAL needs a regional backend service, which is not simulated.")
	}
	if len(computeList(body, "healthChecks")) > 1 {
		return invalid("Field 'healthChecks' may name at most one health check.")
	}
	return computeCDNScheme(body)
}

// computeCDNScheme allows Cloud CDN only for external Application Load Balancers.
func computeCDNScheme(body map[string]any) error {
	if scheme := computeText(body, "loadBalancingScheme"); body["enableCDN"] == true && scheme != "" && scheme != "EXTERNAL" && scheme != "EXTERNAL_MANAGED" {
		return invalid("Field 'enableCDN' is available only for EXTERNAL or EXTERNAL_MANAGED backend services.")
	}
	return nil
}

func computeURLMap(body map[string]any) error {
	if err := computeRefuse("route actions and URL map tests need request evaluation, which is not simulated.", "defaultRouteAction", "tests", "defaultCustomErrorResponsePolicy")(body); err != nil {
		return err
	}
	if (computeText(body, "defaultService") == "") == (body["defaultUrlRedirect"] == nil) {
		return invalid("Exactly one of defaultService or defaultUrlRedirect must be specified.")
	}
	matchers, err := computeNames("pathMatchers", computeList(body, "pathMatchers"))
	if err != nil {
		return err
	}
	for i, item := range computeList(body, "pathMatchers") {
		matcher := item.(map[string]any)
		for _, field := range []string{"defaultRouteAction", "routeRules", "defaultCustomErrorResponsePolicy"} {
			if _, ok := matcher[field]; ok {
				return invalid("Field 'pathMatchers[%d].%s' is not supported by the simulator.", i, field)
			}
		}
		if (computeText(matcher, "defaultService") == "") == (matcher["defaultUrlRedirect"] == nil) {
			return invalid("Field 'pathMatchers[%d]' needs exactly one of defaultService or defaultUrlRedirect.", i)
		}
		for j, rule := range computeList(matcher, "pathRules") {
			if _, ok := rule.(map[string]any)["routeAction"]; ok {
				return invalid("Field 'pathMatchers[%d].pathRules[%d].routeAction' is not supported by the simulator.", i, j)
			}
		}
	}
	for i, item := range computeList(body, "hostRules") {
		if !matchers[computeText(item.(map[string]any), "pathMatcher")] {
			return invalid("Field 'hostRules[%d].pathMatcher' must name a path matcher of this URL map.", i)
		}
	}
	return nil
}

func computePremium(body map[string]any) error {
	if tier := computeText(body, "networkTier"); tier != "" && tier != "PREMIUM" {
		return invalid("Field 'networkTier' must be PREMIUM: global forwarding rules use the Premium network tier.")
	}
	return nil
}

func computeForwardingRule(body map[string]any) error {
	if err := computeAll(computeSecrets, computePremium, computeNeeds("target", "IPAddress"), computeRefuse("only global external Application Load Balancer forwarding rules are simulated.",
		"backendService", "subnetwork", "ipCollection", "serviceDirectoryRegistrations", "allowGlobalAccess", "allowPscGlobalAccess", "noAutomateDnsZone",
		"isMirroringCollector", "ports", "allPorts", "sourceIpRanges"))(body); err != nil {
		return err
	}
	if addr, err := netip.ParseAddr(computeText(body, "IPAddress")); err != nil || !addr.Is4() {
		return invalid("Field 'IPAddress' must be an IPv4 address: the simulator allocates no address.")
	}
	if ports := computeText(body, "portRange"); ports != "" {
		low, high, _ := strings.Cut(ports, "-")
		if high == "" {
			high = low
		}
		a, errA := strconv.Atoi(low)
		b, errB := strconv.Atoi(high)
		if errA != nil || errB != nil || a < 1 || a > b || b > 65535 {
			return invalid("Invalid value for field 'portRange': '%s'.", ports)
		}
	}
	if protocol := computeText(body, "IPProtocol"); protocol != "" && protocol != "TCP" {
		return invalid("Field 'IPProtocol' must be TCP for a target HTTP proxy.")
	}
	return nil
}

func computeMTU(body map[string]any) error {
	if mtu, ok := computeNumber(body["mtu"]); ok && mtu != 1440 && mtu != 1460 && mtu != 1500 && mtu != 8896 {
		return invalid("Field 'mtu' must be 1440, 1460, 1500 or 8896.")
	}
	return nil
}

func computeAttachment(body map[string]any) error {
	if err := computeAll(computeSecrets, computeMTU, computeNeeds("router", "edgeAvailabilityDomain"), computeRefuse("only Partner Interconnect VLAN attachments are simulated; no partner, physical link, address or pairing key exists.",
		"interconnect", "pairingKey", "partnerAsn", "partnerMetadata", "ipsecInternalAddresses", "l2Forwarding", "vlanTag8021q", "bandwidth",
		"candidateSubnets", "candidateIpv6Subnets", "candidateCloudRouterIpAddress", "candidateCustomerRouterIpAddress",
		"candidateCloudRouterIpv6Address", "candidateCustomerRouterIpv6Address", "subnetLength", "cloudRouterIpv6InterfaceId", "customerRouterIpv6InterfaceId"))(body); err != nil {
		return err
	}
	if computeText(body, "type") != "PARTNER" {
		return invalid("Field 'type' must be PARTNER: Dedicated, Cross-Cloud and Cross-Site Interconnect need physical links.")
	}
	if encryption := computeText(body, "encryption"); encryption != "" && encryption != "NONE" || computeText(body, "stackType") == "IPV4_IPV6" {
		return invalid("HA VPN over Cloud Interconnect encryption and IPv6 are not simulated.")
	}
	return nil
}
