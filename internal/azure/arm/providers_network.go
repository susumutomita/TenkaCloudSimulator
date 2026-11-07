package arm

import (
	"net/netip"
	"strconv"
	"strings"
)

// Reviewed bounded specification of Microsoft.Network address metadata.

var (
	// 2-64 letters, digits, underscores, periods and hyphens; starts with a
	// letter or digit and ends with a letter, digit or underscore.
	vnetName = name(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}[A-Za-z0-9_]$`)
	// 1-80 characters with the same rules (subnets, groups, addresses).
	networkName = name(`^[A-Za-z0-9]([A-Za-z0-9_.-]{0,78}[A-Za-z0-9_])?$`)
)

var network = &Provider{
	Namespace: "Microsoft.Network", APIVersion: "2026-03-01", Title: "Virtual networking", Issues: catalogIssue,
	Types: []*ResourceType{
		{Name: "virtualNetworks", Title: "Virtual networks", Limit: 50, NamePattern: vnetName,
			Generate: map[string]any{"properties.resourceGuid": "$uuid"},
			Unsupported: map[string]string{
				"properties.subnets":                                "create subnets through Subnets_CreateOrUpdate; virtual network documents do not embed them.",
				"properties.virtualNetworkPeerings":                 "peering is not simulated.",
				"properties.ddosProtectionPlan":                     "DDoS protection plans are not in the catalog.",
				"properties.ipAllocations":                          "IP allocations are not in the catalog.",
				"properties.addressSpace.ipamPoolPrefixAllocations": "IPAM pools are not in the catalog.",
				"properties.bgpCommunities":                         "BGP is not simulated.",
				"extendedLocation":                                  "edge zones are not simulated.",
			},
			Check: vnetCheck, Form: []string{"properties.addressSpace.addressPrefixes"}, Defaults: map[string]any{"properties.addressSpace.addressPrefixes": []any{"10.0.0.0/16"}}},
		{Name: "virtualNetworks/subnets", Title: "Subnets", Limit: 50, NamePattern: networkName,
			Unsupported: map[string]string{
				"properties.natGateway":                         "NAT gateways are not in the catalog.",
				"properties.routeTable":                         "route tables are not in the catalog.",
				"properties.serviceEndpointPolicies":            "service endpoint policies are not in the catalog.",
				"properties.ipAllocations":                      "IP allocations are not in the catalog.",
				"properties.ipamPoolPrefixAllocations":          "IPAM pools are not in the catalog.",
				"properties.applicationGatewayIPConfigurations": "application gateways are not in the catalog.",
				"properties.serviceGateway":                     "service gateways are not in the catalog.",
			},
			Refs:  []Ref{{Path: "properties.networkSecurityGroup.id", Type: "Microsoft.Network/networkSecurityGroups", InUse: "InUseNetworkSecurityGroupCannotBeDeleted"}},
			Check: subnetCheck, Form: []string{"properties.addressPrefix"}, Defaults: map[string]any{"properties.addressPrefix": "10.0.0.0/24"}},
		{Name: "networkSecurityGroups", Title: "Network security groups", Limit: 50, NamePattern: networkName,
			Generate: map[string]any{"properties.resourceGuid": "$uuid"}, Check: securityGroupCheck, Form: []string{}},
		{Name: "publicIPAddresses", Title: "Public IP addresses", Limit: 50, NamePattern: networkName,
			Generate: map[string]any{"properties.resourceGuid": "$uuid"},
			Unsupported: map[string]string{
				"properties.ipAddress":                       "no address is allocated or routed by the simulator.",
				"properties.publicIPPrefix":                  "public IP prefixes are not in the catalog.",
				"properties.natGateway":                      "NAT gateways are not in the catalog.",
				"properties.linkedPublicIPAddress":           "linked addresses are not simulated.",
				"properties.servicePublicIPAddress":          "service addresses are not simulated.",
				"properties.ddosSettings.ddosProtectionPlan": "DDoS protection plans are not in the catalog.",
				"extendedLocation":                           "edge zones are not simulated.",
			},
			Check: publicAddressCheck, Form: []string{"sku.name", "properties.publicIPAllocationMethod", "properties.publicIPAddressVersion"},
			Defaults: map[string]any{"sku.name": "Standard", "properties.publicIPAllocationMethod": "Static"}},
	},
	Bindings: append(append(append(
		standard("virtualNetworks", "VirtualNetworks_CreateOrUpdate", "VirtualNetworks_Get", "VirtualNetworks_UpdateTags", "VirtualNetworks_Delete", "VirtualNetworks_List", "VirtualNetworks_ListAll", nil),
		Binding{Operation: "Subnets_CreateOrUpdate", Kind: Put, Type: "virtualNetworks/subnets"},
		Binding{Operation: "Subnets_Get", Kind: Get, Type: "virtualNetworks/subnets"},
		Binding{Operation: "Subnets_Delete", Kind: Delete, Type: "virtualNetworks/subnets"},
		Binding{Operation: "Subnets_List", Kind: List, Type: "virtualNetworks/subnets"}),
		standard("networkSecurityGroups", "NetworkSecurityGroups_CreateOrUpdate", "NetworkSecurityGroups_Get", "NetworkSecurityGroups_UpdateTags", "NetworkSecurityGroups_Delete", "NetworkSecurityGroups_List", "NetworkSecurityGroups_ListAll", nil)...),
		standard("publicIPAddresses", "PublicIPAddresses_CreateOrUpdate", "PublicIPAddresses_Get", "PublicIPAddresses_UpdateTags", "PublicIPAddresses_Delete", "PublicIPAddresses_List", "PublicIPAddresses_ListAll", nil)...),
	Bounds: []string{
		"Address metadata only: no packet is routed, filtered or translated and no public address is allocated (properties.ipAddress stays absent).",
		"Virtual networks need at least one valid, non-overlapping CIDR prefix; subnets must lie inside it and must not overlap one another. Subnets are separate child resources and are not embedded in the virtual network document.",
		"Security rules are stored inline in their network security group with unique names and priorities; default rules, effective rules, flow logs and $expand are not simulated.",
		"A network security group referenced by a subnet cannot be deleted; peering, gateways, NAT, route tables and every other Microsoft.Network operation are not implemented (HTTP 501).",
	},
}

func prefixes(c *Change, dotted string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, text := range strings_(c.Document, dotted) {
		p, err := netip.ParsePrefix(text)
		if err != nil || p.Masked() != p {
			return nil, fail(400, "InvalidAddressPrefixFormat", "Address prefix '%s' is not a valid CIDR network prefix.", text)
		}
		out = append(out, p)
	}
	return out, nil
}

func overlapping(list []netip.Prefix) (netip.Prefix, netip.Prefix, bool) {
	for i := range list {
		for j := i + 1; j < len(list); j++ {
			if list[i].Overlaps(list[j]) {
				return list[i], list[j], true
			}
		}
	}
	return netip.Prefix{}, netip.Prefix{}, false
}

func contained(outer []netip.Prefix, inner netip.Prefix) bool {
	for _, o := range outer {
		if o.Bits() <= inner.Bits() && o.Contains(inner.Addr()) {
			return true
		}
	}
	return false
}

func subnetPrefixes(document map[string]any) ([]netip.Prefix, error) {
	c := &Change{Document: document}
	list, err := prefixes(c, "properties.addressPrefixes")
	if err != nil {
		return nil, err
	}
	if single := text(document, "properties.addressPrefix"); single != "" {
		p, err := netip.ParsePrefix(single)
		if err != nil || p.Masked() != p {
			return nil, fail(400, "InvalidAddressPrefixFormat", "Address prefix '%s' is not a valid CIDR network prefix.", single)
		}
		list = append(list, p)
	}
	return list, nil
}

func vnetCheck(c *Change) error {
	space, err := prefixes(c, "properties.addressSpace.addressPrefixes")
	if err != nil {
		return err
	}
	if len(space) == 0 {
		return fail(400, "InvalidAddressSpace", "The virtual network needs at least one address prefix in properties.addressSpace.addressPrefixes.")
	}
	if a, b, ok := overlapping(space); ok {
		return fail(400, "NetcfgInvalidAddressSpace", "Address prefixes %s and %s overlap.", a, b)
	}
	if enabled, _ := lookup(c.Document, "properties.enableDdosProtection"); enabled == true {
		return refuse("enableDdosProtection requires a DDoS protection plan, which is not in the catalog.")
	}
	for _, dns := range strings_(c.Document, "properties.dhcpOptions.dnsServers") {
		if _, err := netip.ParseAddr(dns); err != nil {
			return refuse("DNS server '%s' is not an IP address.", dns)
		}
	}
	for _, subnet := range c.Children["virtualNetworks/subnets"] {
		list, _ := subnetPrefixes(subnet)
		for _, p := range list {
			if !contained(space, p) {
				return fail(400, "NetcfgSubnetRangeOutsideVnet", "Subnet '%s' prefix %s would lie outside the new address space.", subnet["name"], p)
			}
		}
	}
	return nil
}

func subnetCheck(c *Change) error {
	single := text(c.Document, "properties.addressPrefix")
	list, err := subnetPrefixes(c.Document)
	if err != nil {
		return err
	}
	if len(list) == 0 || single != "" && len(strings_(c.Document, "properties.addressPrefixes")) > 0 {
		return fail(400, "InvalidRequestFormat", "Specify exactly one of properties.addressPrefix or properties.addressPrefixes.")
	}
	space, _ := prefixes(&Change{Document: c.Parent}, "properties.addressSpace.addressPrefixes")
	for _, p := range list {
		if !contained(space, p) {
			return fail(400, "NetcfgSubnetRangeOutsideVnet", "Subnet prefix %s is not within the virtual network address space.", p)
		}
	}
	if a, b, ok := overlapping(list); ok {
		return fail(400, "NetcfgInvalidSubnet", "Subnet prefixes %s and %s overlap.", a, b)
	}
	for _, sibling := range c.Siblings {
		others, _ := subnetPrefixes(sibling)
		for _, p := range list {
			for _, o := range others {
				if p.Overlaps(o) {
					return fail(400, "NetcfgSubnetRangesOverlap", "Subnet prefix %s overlaps subnet '%s' (%s).", p, sibling["name"], o)
				}
			}
		}
	}
	if group, ok := lookup(c.Document, "properties.networkSecurityGroup"); ok {
		object, _ := group.(map[string]any)
		if len(object) != 1 || object["id"] == nil {
			return refuse("properties.networkSecurityGroup must contain only the id of an existing network security group.")
		}
	}
	return nil
}

func securityGroupCheck(c *Change) error {
	value, _ := lookup(c.Document, "properties.securityRules")
	rules, _ := value.([]any)
	names, priorities := map[string]bool{}, map[string]bool{}
	for _, item := range rules {
		rule, _ := item.(map[string]any)
		ruleName := text(rule, "name")
		if ruleName == "" || !networkName.MatchString(ruleName) || names[strings.ToLower(ruleName)] {
			return fail(400, "SecurityRuleNameInvalid", "Each security rule needs a unique valid name.")
		}
		names[strings.ToLower(ruleName)] = true
		for _, field := range []string{"properties.sourceApplicationSecurityGroups", "properties.destinationApplicationSecurityGroups"} {
			if present(rule, field) {
				return refuse("Security rule member '%s' is not supported: application security groups are not in the catalog.", field)
			}
		}
		priority, ok := number(rule, "properties.priority")
		if !ok || priority < 100 || priority > 4096 {
			return fail(400, "SecurityRuleInvalidPriority", "Security rule '%s' needs a priority between 100 and 4096.", ruleName)
		}
		slot := strings.ToLower(text(rule, "properties.direction")) + "/" + strconv.FormatInt(priority, 10)
		if priorities[slot] {
			return fail(400, "SecurityRuleConflict", "Security rule '%s' reuses priority %d in its direction.", ruleName, priority)
		}
		priorities[slot] = true
		rule["id"] = c.Document["id"].(string) + "/securityRules/" + ruleName
		if props, ok := rule["properties"].(map[string]any); ok {
			props["provisioningState"] = "Succeeded"
		}
	}
	return nil
}

func publicAddressCheck(c *Change) error {
	sku := text(c.Document, "sku.name")
	if sku == "" {
		sku = "Standard"
		assign(c.Document, "sku.name", sku)
	}
	method := text(c.Document, "properties.publicIPAllocationMethod")
	if method == "" {
		method = "Static"
		if strings.EqualFold(sku, "Basic") {
			method = "Dynamic"
		}
		assign(c.Document, "properties.publicIPAllocationMethod", method)
	}
	if !strings.EqualFold(sku, "Basic") && !strings.EqualFold(method, "Static") {
		return fail(400, "StandardSkuPublicIPAddressCannotBeDynamic", "Standard SKU public IP addresses must use Static allocation.")
	}
	if text(c.Document, "properties.publicIPAddressVersion") == "" {
		assign(c.Document, "properties.publicIPAddressVersion", "IPv4")
	}
	if text(c.Document, "sku.tier") == "" {
		assign(c.Document, "sku.tier", "Regional")
	}
	return rangeCheck(c, "properties.idleTimeoutInMinutes", 4, 30)
}
