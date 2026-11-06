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
	Key: "compute.v1", Title: "Compute Engine", Issues: []int{371, 322, 332}, Scope: "zonal",
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
			References: map[string][]string{"network": {"networks"}}},
		{ID: "disks", Schema: "Disk", IDField: "name", ShortName: true, Limit: 100, Permission: "compute.disks",
			Generate: map[string]any{"status": "READY", "labelFingerprint": "$fingerprint", "physicalBlockSizeBytes": "4096", "sizeGb": "500"},
			Weak:     map[string][]string{"sourceDisk": {"disks"}, "sourceSnapshot": {"snapshots"}}},
		{ID: "snapshots", Schema: "Snapshot", IDField: "name", ShortName: true, Limit: 100, Permission: "compute.snapshots",
			Generate: map[string]any{"status": "READY", "labelFingerprint": "$fingerprint", "storageBytes": "0", "storageBytesStatus": "UP_TO_DATE", "autoCreated": false},
			Weak:     map[string][]string{"sourceDisk": {"disks"}}},
		{ID: "instanceTemplates", Schema: "InstanceTemplate", IDField: "name", ShortName: true, Limit: 100, Permission: "compute.instanceTemplates",
			References: map[string][]string{"properties.networkInterfaces[].network": {"networks"}, "properties.networkInterfaces[].subnetwork": {"subnetworks"}}},
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
		"Only custom mode VPC networks are accepted (autoCreateSubnetworks=false); no default computeNetwork, default internet route or subnet route is created, and Network.subnetworks and peerings are not maintained.",
		"Disks and snapshots hold no data: sizes are recorded as given (blank disks default to 500 GB), a disk created from a source must state sizeGb, snapshots report storageBytes 0 and do not copy the source size, and customer-supplied encryption keys are refused.",
		"Instances are not simulated: instances.insert and every other instance method return UNIMPLEMENTED, because inserting an instance boots a VM; instance templates store the configuration only.",
		"References must name existing resources of this project; a network still used by a computeSubnetwork, firewall rule, route or instance template cannot be deleted, and subnetwork ranges may not overlap within a network.",
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
	if err := computeRefuse("instances, internal load balancers, VPN tunnels and peer networks are not simulated as next hops.", "nextHopInstance", "nextHopIlb", "nextHopVpnTunnel", "nextHopNetwork")(body); err != nil {
		return err
	}
	hops := 0
	for _, field := range []string{"nextHopGateway", "nextHopIp"} {
		if computeText(body, field) != "" {
			hops++
		}
	}
	if hops != 1 {
		return invalid("Exactly one of nextHopGateway or nextHopIp must be specified.")
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

// computeGrow only enlarges a computeDisk, as disks.resize requires.
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
