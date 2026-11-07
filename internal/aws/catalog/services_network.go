package catalog

import "strings"

// Reviewed bounded specifications for networking and content delivery,
// front-end web and mobile, and location services. Each binds only official
// operations whose effect is configuration metadata: no traffic is routed, no
// DNS name is published or resolved, no connection, link or tunnel is
// established, no GraphQL or event request is served, no route or map tile is
// computed and no device runs a test.

// appMeshGenerated returns the official resource metadata of an App Mesh
// resource, repeated at the top level for its list reference shape.
func appMeshGenerated(extra map[string]any) map[string]any {
	generated := map[string]any{
		"metadata": map[string]any{"arn": "$arn", "createdAt": "$now", "lastUpdatedAt": "$now", "meshOwner": "$account",
			"resourceOwner": "$account", "uid": "$uuid", "version": 1},
		"status": map[string]any{"status": "ACTIVE"},
		"arn":    "$arn", "createdAt": "$now", "lastUpdatedAt": "$now", "meshOwner": "$account", "resourceOwner": "$account", "version": 1,
	}
	for key, value := range extra {
		generated[key] = value
	}
	return generated
}

// appMeshChild describes a mesh-scoped App Mesh resource kind.
func appMeshChild(kind, shape, summary, nameMember, arnKind string) *Resource {
	return &Resource{Kind: kind, Shape: shape, Summary: summary, Parent: "mesh", ParentMember: "meshName", ID: "name",
		ARN:        "arn:{partition}:appmesh:{region}:{account}:mesh/{parent}/" + arnKind + "/{name}",
		NameMember: nameMember, Unique: true, Limit: 50, Generate: appMeshGenerated(map[string]any{"meshName": "$parentName"})}
}

// appMeshChildOps binds the five lifecycle operations of one mesh child.
func appMeshChildOps(noun, plural, kind, nameMember string, check func(map[string]any) error) []Op {
	deleted := map[string]any{"status": map[string]any{"status": "DELETED"}}
	return []Op{
		{Name: "Create" + noun, Kind: Create, Resource: kind, Parent: "meshName", Check: check},
		{Name: "Describe" + noun, Kind: Read, Resource: kind, Parent: "meshName", Lookup: nameMember},
		{Name: "List" + plural, Kind: List, Resource: kind, Parent: "meshName"},
		{Name: "Update" + noun, Kind: Update, Resource: kind, Parent: "meshName", Lookup: nameMember, Fields: []string{"spec"}, Accept: []string{"clientToken"}, Check: check},
		{Name: "Delete" + noun, Kind: Delete, Resource: kind, Parent: "meshName", Lookup: nameMember, Set: deleted},
	}
}

var appMesh = &Service{
	Model: "appmesh", Title: "AWS App Mesh", IAMPrefix: "appmesh", Issues: []int{236},
	Resources: []*Resource{
		{Kind: "mesh", Shape: "MeshData", Summary: "MeshRef", ID: "name", ARN: "arn:{partition}:appmesh:{region}:{account}:mesh/{name}",
			NameMember: "meshName", Unique: true, Limit: 15, Generate: appMeshGenerated(map[string]any{"spec": map[string]any{"egressFilter": map[string]any{"type": "DROP_ALL"}}})},
		appMeshChild("virtualNode", "VirtualNodeData", "VirtualNodeRef", "virtualNodeName", "virtualNode"),
		appMeshChild("virtualRouter", "VirtualRouterData", "VirtualRouterRef", "virtualRouterName", "virtualRouter"),
		appMeshChild("virtualService", "VirtualServiceData", "VirtualServiceRef", "virtualServiceName", "virtualService"),
		appMeshChild("virtualGateway", "VirtualGatewayData", "VirtualGatewayRef", "virtualGatewayName", "virtualGateway"),
	},
	Ops: append(append(append(append([]Op{
		{Name: "CreateMesh", Kind: Create, Resource: "mesh"},
		{Name: "DescribeMesh", Kind: Read, Resource: "mesh", Lookup: "meshName"},
		{Name: "ListMeshes", Kind: List, Resource: "mesh"},
		{Name: "UpdateMesh", Kind: Update, Resource: "mesh", Lookup: "meshName", Fields: []string{"spec"}, Accept: []string{"clientToken"}},
		{Name: "DeleteMesh", Kind: Delete, Resource: "mesh", Lookup: "meshName", Set: map[string]any{"status": map[string]any{"status": "DELETED"}}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	}, appMeshChildOps("VirtualNode", "VirtualNodes", "virtualNode", "virtualNodeName", nil)...),
		appMeshChildOps("VirtualRouter", "VirtualRouters", "virtualRouter", "virtualRouterName", nil)...),
		appMeshChildOps("VirtualService", "VirtualServices", "virtualService", "virtualServiceName", appMeshVirtualService)...),
		appMeshChildOps("VirtualGateway", "VirtualGateways", "virtualGateway", "virtualGatewayName", nil)...),
	AccountMembers: []string{"meshOwner"},
	Bounds: []string{
		"Meshes, virtual nodes, virtual routers, virtual services and virtual gateways are configuration metadata: no Envoy proxy configuration is served, no traffic is routed, and service discovery, backend, listener and TLS settings are stored without resolving DNS names, AWS Cloud Map services, certificates or files.",
		"Routes and gateway routes are not implemented because they are nested two levels below a mesh; CreateRoute, CreateGatewayRoute and their other operations return UnknownOperationException with HTTP 501. Virtual services therefore refuse a provider, which would name a virtual node or router.",
		"Each resource keeps metadata version 1 and its creation time as lastUpdatedAt after updates. Meshes are owned by the simulator account: meshOwner must be that account, so shared meshes are not available. Deleting a mesh that still has virtual resources is refused.",
		"Update client tokens are accepted but not recorded. ListTagsForResource returns every tag and refuses limit and nextToken.",
	},
}

// appMeshVirtualService refuses providers: virtual nodes and routers are
// stored, but the simulator does not check a provider's reference.
func appMeshVirtualService(input map[string]any) error {
	if spec, _ := input["spec"].(map[string]any); spec["provider"] != nil {
		return Failure("BadRequestException", "The simulator does not support virtual service providers; create the virtual service without spec.provider.")
	}
	return nil
}

var cloudMap = &Service{
	Model: "servicediscovery", Title: "AWS Cloud Map", IAMPrefix: "servicediscovery", Issues: []int{237},
	Resources: []*Resource{
		{Kind: "namespace", Shape: "Namespace", Summary: "NamespaceSummary", ID: "alnum:16:ns-",
			ARN:       "arn:{partition}:servicediscovery:{region}:{account}:namespace/{id}",
			ARNMember: "Arn", NameMember: "Name", IDMember: "Id", Unique: true, Limit: 50,
			Generate: map[string]any{"Type": "HTTP", "ServiceCount": 0, "CreateDate": "$now", "ResourceOwner": "$account",
				"Properties": map[string]any{"HttpProperties": map[string]any{"HttpName": "$name"}}}},
	},
	Ops: []Op{
		{Name: "CreateHttpNamespace", Kind: Create, Resource: "namespace", Set: map[string]any{"OperationId": "$uuid"}},
		{Name: "GetNamespace", Kind: Read, Resource: "namespace", Lookup: "Id"},
		{Name: "ListNamespaces", Kind: List, Resource: "namespace"},
		{Name: "UpdateHttpNamespace", Kind: Update, Resource: "namespace", Lookup: "Id", From: "Namespace", Fields: []string{"Description"},
			Accept: []string{"UpdaterRequestId"}, Set: map[string]any{"OperationId": "$uuid"}},
		{Name: "DeleteNamespace", Kind: Delete, Resource: "namespace", Lookup: "Id", Set: map[string]any{"OperationId": "$uuid"}},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Only HTTP namespaces are stored: no Route 53 hosted zone, DNS record, health check, service or instance exists, and CreatePrivateDnsNamespace, CreatePublicDnsNamespace, CreateService, RegisterInstance and DiscoverInstances return UnknownOperationException with HTTP 501.",
		"Namespace creation, update and deletion finish before the response. Each returns a new OperationId that names no stored operation; GetOperation and ListOperations return UnknownOperationException with HTTP 501, so use ListNamespaces to find a new namespace's Id.",
		"ServiceCount stays 0. ListNamespaces refuses Filters. Update requester IDs are accepted but not recorded. Validation failures detected by the simulator are reported as ValidationException, not the official InvalidInput code.",
	},
}

var recoveryControl = &Service{
	Model: "route53-recovery-control-config", Title: "Amazon Application Recovery Controller - Recovery Control Configuration",
	IAMPrefix: "route53-recovery-control-config", Issues: []int{242}, Global: true,
	Resources: []*Resource{
		{Kind: "cluster", Shape: "Cluster", ID: "uuid", ARN: "arn:{partition}:route53-recovery-control::{account}:cluster/{id}",
			ARNMember: "ClusterArn", NameMember: "Name", Limit: 10, Aliases: map[string]string{"ClusterName": "Name"},
			Generate: map[string]any{"Status": "DEPLOYED", "Owner": "$account", "NetworkType": "IPV4", "ClusterEndpoints": []any{}}},
	},
	Ops: []Op{
		{Name: "CreateCluster", Kind: Create, Resource: "cluster"},
		{Name: "DescribeCluster", Kind: Read, Resource: "cluster", Lookup: "ClusterArn"},
		{Name: "ListClusters", Kind: List, Resource: "cluster"},
		{Name: "UpdateCluster", Kind: Update, Resource: "cluster", Lookup: "ClusterArn", Fields: []string{"NetworkType"}},
		{Name: "DeleteCluster", Kind: Delete, Resource: "cluster", Lookup: "ClusterArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Clusters are global configuration metadata reported DEPLOYED with no ClusterEndpoints: no Regional data-plane cluster is provisioned, and the Route 53 ARC cluster API that reads and changes routing control states is not served.",
		"Control panels, routing controls and safety rules are not implemented because they are read and changed by their own ARN without naming their parent; CreateControlPanel, CreateRoutingControl, CreateSafetyRule, their other operations and resource policies return UnknownOperationException with HTTP 501. The default control panel of a cluster is therefore not created.",
	},
}

var globalAccelerator = &Service{
	Model: "globalaccelerator", Title: "AWS Global Accelerator", IAMPrefix: "globalaccelerator", Issues: []int{243}, Global: true,
	Resources: []*Resource{
		{Kind: "accelerator", Shape: "Accelerator", ID: "uuid", ARN: "arn:{partition}:globalaccelerator::{account}:accelerator/{id}",
			ARNMember: "AcceleratorArn", NameMember: "Name", Limit: 20,
			Generate: map[string]any{"IpAddressType": "IPV4", "Status": "DEPLOYED", "IpSets": []any{}, "Events": []any{}, "CreatedTime": "$now", "LastModifiedTime": "$now"}},
		{Kind: "customRoutingAccelerator", Shape: "CustomRoutingAccelerator", ID: "uuid", ARN: "arn:{partition}:globalaccelerator::{account}:accelerator/{id}",
			ARNMember: "AcceleratorArn", NameMember: "Name", Limit: 20,
			Generate: map[string]any{"IpAddressType": "IPV4", "Status": "DEPLOYED", "IpSets": []any{}, "CreatedTime": "$now", "LastModifiedTime": "$now"}},
		{Kind: "attachment", Shape: "Attachment", ID: "uuid", ARN: "arn:{partition}:globalaccelerator::{account}:attachment/{id}",
			ARNMember: "AttachmentArn", NameMember: "Name", Limit: 20,
			Generate: map[string]any{"Principals": []any{}, "Resources": []any{}, "CreatedTime": "$now", "LastModifiedTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateAccelerator", Kind: Create, Resource: "accelerator", Check: acceleratorCreate},
		{Name: "DescribeAccelerator", Kind: Read, Resource: "accelerator", Lookup: "AcceleratorArn"},
		{Name: "ListAccelerators", Kind: List, Resource: "accelerator"},
		{Name: "UpdateAccelerator", Kind: Update, Resource: "accelerator", Lookup: "AcceleratorArn", Fields: []string{"Name", "IpAddressType", "Enabled"},
			Touch: []string{"LastModifiedTime"}, Check: acceleratorUpdate},
		{Name: "DeleteAccelerator", Kind: Delete, Resource: "accelerator", Lookup: "AcceleratorArn"},
		{Name: "CreateCustomRoutingAccelerator", Kind: Create, Resource: "customRoutingAccelerator", Check: acceleratorCreate},
		{Name: "DescribeCustomRoutingAccelerator", Kind: Read, Resource: "customRoutingAccelerator", Lookup: "AcceleratorArn"},
		{Name: "ListCustomRoutingAccelerators", Kind: List, Resource: "customRoutingAccelerator"},
		{Name: "UpdateCustomRoutingAccelerator", Kind: Update, Resource: "customRoutingAccelerator", Lookup: "AcceleratorArn",
			Fields: []string{"Name", "IpAddressType", "Enabled"}, Touch: []string{"LastModifiedTime"}, Check: acceleratorUpdate},
		{Name: "DeleteCustomRoutingAccelerator", Kind: Delete, Resource: "customRoutingAccelerator", Lookup: "AcceleratorArn"},
		{Name: "CreateCrossAccountAttachment", Kind: Create, Resource: "attachment"},
		{Name: "DescribeCrossAccountAttachment", Kind: Read, Resource: "attachment", Lookup: "AttachmentArn"},
		{Name: "ListCrossAccountAttachments", Kind: List, Resource: "attachment"},
		{Name: "UpdateCrossAccountAttachment", Kind: Update, Resource: "attachment", Lookup: "AttachmentArn", Fields: []string{"Name"}, Touch: []string{"LastModifiedTime"}},
		{Name: "DeleteCrossAccountAttachment", Kind: Delete, Resource: "attachment", Lookup: "AttachmentArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Standard and custom routing accelerators are global configuration metadata reported DEPLOYED with no IpSets, DnsName or DualStackDnsName: no anycast address is allocated and no traffic is accepted or routed. Accelerators must stay disabled, so Enabled must be false at creation and cannot become true; IpAddresses (bring your own IP) is refused.",
		"Listeners, endpoint groups, endpoints, accelerator attributes (flow logs), custom routing traffic rules and BYOIP address ranges are not implemented; CreateListener, CreateEndpointGroup, AddEndpoints, ProvisionByoipCidr and their other operations return UnknownOperationException with HTTP 501, so no accelerator has a listener when it is deleted.",
		"Cross-account attachments store their principals and resources without sharing anything with another account or checking the named endpoints and CIDR ranges; UpdateCrossAccountAttachment can only rename an attachment and refuses AddPrincipals, RemovePrincipals, AddResources and RemoveResources.",
	},
}

// acceleratorCreate requires a disabled accelerator without bring-your-own
// addresses; Enabled defaults to true in the official API.
func acceleratorCreate(input map[string]any) error {
	if enabled, ok := input["Enabled"].(bool); !ok || enabled {
		return Failure("InvalidArgumentException", "The simulator routes no traffic, so accelerators must be created with Enabled set to false.")
	}
	return acceleratorUpdate(input)
}

// acceleratorUpdate keeps accelerators disabled and refuses IpAddresses,
// which would name addresses from a provisioned BYOIP range.
func acceleratorUpdate(input map[string]any) error {
	if enabled, _ := input["Enabled"].(bool); enabled {
		return Failure("InvalidArgumentException", "The simulator routes no traffic, so accelerators cannot be enabled.")
	}
	if _, ok := input["IpAddresses"]; ok {
		return Failure("InvalidArgumentException", "The simulator has no BYOIP address ranges, so IpAddresses is not supported.")
	}
	return nil
}

var rtbFabric = &Service{
	Model: "rtbfabric", Title: "AWS RTB Fabric", IAMPrefix: "rtbfabric", Issues: []int{246},
	Resources: []*Resource{
		{Kind: "requesterGateway", Shape: "GetRequesterGatewayResponse", ID: "alnum:16:rtb-gw-",
			ARN: "arn:{partition}:rtbfabric:{region}:{account}:gateway/{id}", IDMember: "gatewayId", Limit: 20,
			Generate: map[string]any{"status": "ACTIVE", "createdAt": "$now", "updatedAt": "$now", "activeLinksCount": 0, "totalLinksCount": 0}},
		{Kind: "responderGateway", Shape: "GetResponderGatewayResponse", ID: "alnum:16:rtb-gw-",
			ARN: "arn:{partition}:rtbfabric:{region}:{account}:gateway/{id}", IDMember: "gatewayId", Limit: 20,
			Generate: map[string]any{"status": "ACTIVE", "createdAt": "$now", "updatedAt": "$now", "activeLinksCount": 0, "totalLinksCount": 0, "inboundLinksCount": 0}},
	},
	Ops: []Op{
		{Name: "CreateRequesterGateway", Kind: Create, Resource: "requesterGateway"},
		{Name: "GetRequesterGateway", Kind: Read, Resource: "requesterGateway", Lookup: "gatewayId"},
		{Name: "ListRequesterGateways", Kind: List, Resource: "requesterGateway", ListValue: "gatewayId"},
		{Name: "UpdateRequesterGateway", Kind: Update, Resource: "requesterGateway", Lookup: "gatewayId", Fields: []string{"description"},
			Accept: []string{"clientToken"}, Touch: []string{"updatedAt"}},
		{Name: "DeleteRequesterGateway", Kind: Delete, Resource: "requesterGateway", Lookup: "gatewayId", Set: map[string]any{"status": "DELETED"}},
		{Name: "CreateResponderGateway", Kind: Create, Resource: "responderGateway"},
		{Name: "GetResponderGateway", Kind: Read, Resource: "responderGateway", Lookup: "gatewayId"},
		{Name: "ListResponderGateways", Kind: List, Resource: "responderGateway", ListValue: "gatewayId"},
		{Name: "UpdateResponderGateway", Kind: Update, Resource: "responderGateway", Lookup: "gatewayId",
			Fields: []string{"domainName", "port", "protocol", "trustStoreConfiguration", "managedEndpointConfiguration", "description"},
			Accept: []string{"clientToken"}, Touch: []string{"updatedAt"}},
		{Name: "DeleteResponderGateway", Kind: Delete, Resource: "responderGateway", Lookup: "gatewayId", Set: map[string]any{"status": "DELETED"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Requester and responder gateways are ACTIVE configuration metadata: VPC, subnet, security group, Auto Scaling group, EKS endpoint and trust store settings are stored without checking them, no network interface or endpoint is created, no domain name is assigned to a requester gateway and no bid request is accepted or forwarded.",
		"Links, inbound and outbound external links and link module flows are not implemented, so link counts stay 0; CreateLink, AcceptLink, CreateInboundExternalLink and their other operations return UnknownOperationException with HTTP 501. Update client tokens are accepted but not recorded.",
	},
}

func latticeTimes(extra map[string]any) map[string]any {
	generated := map[string]any{"createdAt": "$now", "lastUpdatedAt": "$now"}
	for key, value := range extra {
		generated[key] = value
	}
	return generated
}

var vpcLattice = &Service{
	Model: "vpc-lattice", Title: "Amazon VPC Lattice", IAMPrefix: "vpc-lattice", Issues: []int{249},
	Resources: []*Resource{
		{Kind: "serviceNetwork", Shape: "GetServiceNetworkResponse", Summary: "ServiceNetworkSummary", ID: "alnum:17:sn-",
			ARN: "arn:{partition}:vpc-lattice:{region}:{account}:servicenetwork/{id}", ARNMember: "arn", NameMember: "name", IDMember: "id", Unique: true, Limit: 20,
			Generate: latticeTimes(map[string]any{"authType": "NONE", "numberOfAssociatedVPCs": 0, "numberOfAssociatedServices": 0, "numberOfAssociatedResourceConfigurations": 0})},
		{Kind: "service", Shape: "GetServiceResponse", Summary: "ServiceSummary", ID: "alnum:17:svc-",
			ARN: "arn:{partition}:vpc-lattice:{region}:{account}:service/{id}", ARNMember: "arn", NameMember: "name", IDMember: "id", Unique: true, Limit: 50,
			Cascade: true, Generate: latticeTimes(map[string]any{"authType": "NONE", "status": "ACTIVE"})},
		{Kind: "listener", Shape: "GetListenerResponse", Summary: "ListenerSummary", Parent: "service", ParentMember: "serviceId", ID: "alnum:17:listener-",
			ARN: "arn:{partition}:vpc-lattice:{region}:{account}:service/{parent}/listener/{id}", ARNMember: "arn", NameMember: "name", IDMember: "id", Unique: true, Limit: 2,
			Generate: latticeTimes(map[string]any{"serviceArn": "$parentArn", "serviceId": "$parentId"})},
		{Kind: "targetGroup", Shape: "GetTargetGroupResponse", Summary: "TargetGroupSummary", ID: "alnum:17:tg-",
			ARN: "arn:{partition}:vpc-lattice:{region}:{account}:targetgroup/{id}", ARNMember: "arn", NameMember: "name", IDMember: "id", Unique: true, Limit: 50,
			Generate: latticeTimes(map[string]any{"status": "ACTIVE", "serviceArns": []any{}})},
	},
	Ops: []Op{
		{Name: "CreateServiceNetwork", Kind: Create, Resource: "serviceNetwork"},
		{Name: "GetServiceNetwork", Kind: Read, Resource: "serviceNetwork", Lookup: "serviceNetworkIdentifier"},
		{Name: "ListServiceNetworks", Kind: List, Resource: "serviceNetwork"},
		{Name: "UpdateServiceNetwork", Kind: Update, Resource: "serviceNetwork", Lookup: "serviceNetworkIdentifier", Fields: []string{"authType"}, Touch: []string{"lastUpdatedAt"}},
		{Name: "DeleteServiceNetwork", Kind: Delete, Resource: "serviceNetwork", Lookup: "serviceNetworkIdentifier"},
		{Name: "CreateService", Kind: Create, Resource: "service"},
		{Name: "GetService", Kind: Read, Resource: "service", Lookup: "serviceIdentifier"},
		{Name: "ListServices", Kind: List, Resource: "service"},
		{Name: "UpdateService", Kind: Update, Resource: "service", Lookup: "serviceIdentifier", Fields: []string{"certificateArn", "authType"}, Touch: []string{"lastUpdatedAt"}},
		{Name: "DeleteService", Kind: Delete, Resource: "service", Lookup: "serviceIdentifier", Set: map[string]any{"status": "DELETE_IN_PROGRESS"}},
		{Name: "CreateListener", Kind: Create, Resource: "listener", Parent: "serviceIdentifier", Check: latticeListener},
		{Name: "GetListener", Kind: Read, Resource: "listener", Parent: "serviceIdentifier", Lookup: "listenerIdentifier"},
		{Name: "ListListeners", Kind: List, Resource: "listener", Parent: "serviceIdentifier"},
		{Name: "UpdateListener", Kind: Update, Resource: "listener", Parent: "serviceIdentifier", Lookup: "listenerIdentifier", Fields: []string{"defaultAction"},
			Touch: []string{"lastUpdatedAt"}, Check: latticeAction},
		{Name: "DeleteListener", Kind: Delete, Resource: "listener", Parent: "serviceIdentifier", Lookup: "listenerIdentifier"},
		{Name: "CreateTargetGroup", Kind: Create, Resource: "targetGroup", Check: latticeTargetGroup},
		{Name: "GetTargetGroup", Kind: Read, Resource: "targetGroup", Lookup: "targetGroupIdentifier"},
		{Name: "ListTargetGroups", Kind: List, Resource: "targetGroup", Filters: map[string]string{"targetGroupType": "type"}},
		{Name: "DeleteTargetGroup", Kind: Delete, Resource: "targetGroup", Lookup: "targetGroupIdentifier", Set: map[string]any{"status": "DELETE_IN_PROGRESS"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Service networks, services, listeners and target groups are configuration metadata: no DNS entry is assigned, no request is accepted, routed or authenticated, custom domain names and certificates are stored without checks, and no target is registered or health-checked.",
		"Listeners are HTTP or HTTPS listeners whose default action is a fixed 404 or 500 response; forward actions and TLS_PASSTHROUGH listeners are refused because target group references are not tracked. A missing port defaults to 80 for HTTP and 443 for HTTPS. Listener rules, auth and resource policies, access log subscriptions, targets and associations with VPCs, services and resource configurations return UnknownOperationException with HTTP 501, so association counts stay 0.",
		"Deleting a service also deletes its listeners. Target groups cannot be updated, and ListTargetGroups refuses vpcIdentifier and returns summaries without port, protocol, ipAddressType and vpcIdentifier, which GetTargetGroup reports inside config.",
	},
}

// latticeListener applies the documented default ports and bounds listeners
// to HTTP and HTTPS with fixed-response default actions.
func latticeListener(input map[string]any) error {
	protocol, _ := input["protocol"].(string)
	if protocol != "HTTP" && protocol != "HTTPS" {
		return Failure("ValidationException", "The simulator supports HTTP and HTTPS listeners with fixed-response default actions only.")
	}
	if _, ok := input["port"]; !ok {
		input["port"] = map[string]int64{"HTTP": 80, "HTTPS": 443}[protocol]
	}
	return latticeAction(input)
}

// latticeAction allows only the documented fixed-response status codes and
// refuses forward actions, whose target group references are not tracked.
func latticeAction(input map[string]any) error {
	action, _ := input["defaultAction"].(map[string]any)
	fixed, ok := action["fixedResponse"].(map[string]any)
	if !ok {
		return Failure("ValidationException", "The simulator supports only fixedResponse default actions; forward actions are not supported.")
	}
	if code, _ := fixed["statusCode"].(int64); code != 404 && code != 500 {
		return Failure("ValidationException", "Fixed responses support only the 404 and 500 status codes.")
	}
	return nil
}

// latticeTargetGroup applies the documented per-type configuration rules.
func latticeTargetGroup(input map[string]any) error {
	kind, _ := input["type"].(string)
	config, _ := input["config"].(map[string]any)
	allowed := map[string][]string{
		"LAMBDA":   {"lambdaEventStructureVersion"},
		"ALB":      {"port", "protocol", "protocolVersion", "vpcIdentifier"},
		"INSTANCE": {"port", "protocol", "protocolVersion", "vpcIdentifier", "healthCheck"},
		"IP":       {"port", "protocol", "protocolVersion", "vpcIdentifier", "healthCheck", "ipAddressType"},
	}[kind]
	for member := range config {
		supported := false
		for _, name := range allowed {
			supported = supported || name == member
		}
		if !supported {
			return Failure("ValidationException", "config."+member+" is not supported for "+kind+" target groups.")
		}
	}
	if _, ok := config["vpcIdentifier"]; kind != "LAMBDA" && !ok {
		return Failure("ValidationException", kind+" target groups need config.vpcIdentifier.")
	}
	return nil
}

var networkManager = &Service{
	Model: "networkmanager", Title: "AWS Network Manager", IAMPrefix: "networkmanager", Issues: []int{296}, Global: true,
	Resources: []*Resource{
		{Kind: "globalNetwork", Shape: "GlobalNetwork", ID: "hex:17:global-network-", ARN: "arn:{partition}:networkmanager::{account}:global-network/{id}",
			ARNMember: "GlobalNetworkArn", IDMember: "GlobalNetworkId", Limit: 5, Generate: map[string]any{"State": "AVAILABLE", "CreatedAt": "$now"}},
		{Kind: "site", Shape: "Site", Parent: "globalNetwork", ParentMember: "GlobalNetworkId", ID: "hex:17:site-",
			ARN: "arn:{partition}:networkmanager::{account}:site/{parent}/{id}", ARNMember: "SiteArn", IDMember: "SiteId", Limit: 50,
			Generate: map[string]any{"GlobalNetworkId": "$parentId", "State": "AVAILABLE", "CreatedAt": "$now"}},
		{Kind: "device", Shape: "Device", Parent: "globalNetwork", ParentMember: "GlobalNetworkId", ID: "hex:17:device-",
			ARN: "arn:{partition}:networkmanager::{account}:device/{parent}/{id}", ARNMember: "DeviceArn", IDMember: "DeviceId", Limit: 50,
			Generate: map[string]any{"GlobalNetworkId": "$parentId", "State": "AVAILABLE", "CreatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateGlobalNetwork", Kind: Create, Resource: "globalNetwork"},
		{Name: "DescribeGlobalNetworks", Kind: List, Resource: "globalNetwork", Names: "GlobalNetworkIds"},
		{Name: "UpdateGlobalNetwork", Kind: Update, Resource: "globalNetwork", Lookup: "GlobalNetworkId", Fields: []string{"Description"}},
		{Name: "DeleteGlobalNetwork", Kind: Delete, Resource: "globalNetwork", Lookup: "GlobalNetworkId", Set: map[string]any{"State": "DELETING"}},
		{Name: "CreateSite", Kind: Create, Resource: "site", Parent: "GlobalNetworkId"},
		{Name: "GetSites", Kind: List, Resource: "site", Parent: "GlobalNetworkId", Names: "SiteIds"},
		{Name: "UpdateSite", Kind: Update, Resource: "site", Parent: "GlobalNetworkId", Lookup: "SiteId", Fields: []string{"Description", "Location"}},
		{Name: "DeleteSite", Kind: Delete, Resource: "site", Parent: "GlobalNetworkId", Lookup: "SiteId", Set: map[string]any{"State": "DELETING"}},
		{Name: "CreateDevice", Kind: Create, Resource: "device", Parent: "GlobalNetworkId", Check: networkDevice},
		{Name: "GetDevices", Kind: List, Resource: "device", Parent: "GlobalNetworkId", Names: "DeviceIds", Filters: map[string]string{"SiteId": "SiteId"}},
		{Name: "UpdateDevice", Kind: Update, Resource: "device", Parent: "GlobalNetworkId", Lookup: "DeviceId", Check: networkDevice,
			Fields: []string{"AWSLocation", "Description", "Type", "Vendor", "Model", "SerialNumber", "Location"}},
		{Name: "DeleteDevice", Kind: Delete, Resource: "device", Parent: "GlobalNetworkId", Lookup: "DeviceId", Set: map[string]any{"State": "DELETING"}},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Global networks, sites and devices are AVAILABLE global inventory metadata: no transit gateway is registered, no route, topology, event or monitoring data is collected, and AWS locations and subnet ARNs are stored without checks. Unknown identifiers in GlobalNetworkIds, SiteIds or DeviceIds are refused with ResourceNotFoundException.",
		"Devices cannot be placed at a site (SiteId is refused), because the simulator does not track site references; links, link and connection associations, connect peers, transit gateway registrations and Cloud WAN core networks, policies and attachments return UnknownOperationException with HTTP 501.",
		"Deleting a global network that still has sites or devices is refused.",
	},
}

// networkDevice refuses SiteId: devices are stored without a site placement.
func networkDevice(input map[string]any) error {
	if _, ok := input["SiteId"]; ok {
		return Failure("ValidationException", "The simulator does not place devices at sites; omit SiteId.")
	}
	return nil
}

var appSync = &Service{
	Model: "appsync", Title: "AWS AppSync", IAMPrefix: "appsync", Issues: []int{148},
	Resources: []*Resource{
		{Kind: "graphqlApi", Shape: "GraphqlApi", ID: "alnum:26", ARN: "arn:{partition}:appsync:{region}:{account}:apis/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "apiId", Limit: 25, Cascade: true,
			Generate: map[string]any{"owner": "$account", "apiType": "GRAPHQL", "visibility": "GLOBAL", "xrayEnabled": false,
				"introspectionConfig": "ENABLED", "queryDepthLimit": 0, "resolverCountLimit": 0}},
		{Kind: "dataSource", Shape: "DataSource", Parent: "graphqlApi", ID: "name", ARN: "arn:{partition}:appsync:{region}:{account}:apis/{parent}/datasources/{name}",
			ARNMember: "dataSourceArn", NameMember: "name", Unique: true, Limit: 50},
		{Kind: "api", Shape: "Api", ID: "alnum:26", ARN: "arn:{partition}:appsync:{region}:{account}:apis/{id}",
			ARNMember: "apiArn", NameMember: "name", IDMember: "apiId", Limit: 25, Cascade: true, Generate: map[string]any{"created": "$now", "xrayEnabled": false}},
		{Kind: "channelNamespace", Shape: "ChannelNamespace", Parent: "api", ParentMember: "apiId", ID: "name",
			ARN:       "arn:{partition}:appsync:{region}:{account}:apis/{parent}/channelNamespace/{name}",
			ARNMember: "channelNamespaceArn", NameMember: "name", Unique: true, Limit: 50,
			Generate: map[string]any{"apiId": "$parentId", "created": "$now", "lastModified": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateGraphqlApi", Kind: Create, Resource: "graphqlApi", Check: appSyncAuthorization},
		{Name: "GetGraphqlApi", Kind: Read, Resource: "graphqlApi", Lookup: "apiId"},
		{Name: "ListGraphqlApis", Kind: List, Resource: "graphqlApi", Filters: map[string]string{"apiType": "apiType"}},
		{Name: "UpdateGraphqlApi", Kind: Update, Resource: "graphqlApi", Lookup: "apiId", Check: appSyncAuthorization,
			Fields: []string{"name", "logConfig", "authenticationType", "userPoolConfig", "openIDConnectConfig", "additionalAuthenticationProviders", "xrayEnabled",
				"lambdaAuthorizerConfig", "mergedApiExecutionRoleArn", "ownerContact", "introspectionConfig", "queryDepthLimit", "resolverCountLimit", "enhancedMetricsConfig"}},
		{Name: "DeleteGraphqlApi", Kind: Delete, Resource: "graphqlApi", Lookup: "apiId"},
		{Name: "CreateDataSource", Kind: Create, Resource: "dataSource", Parent: "apiId", Check: appSyncDataSource},
		{Name: "GetDataSource", Kind: Read, Resource: "dataSource", Parent: "apiId", Lookup: "name"},
		{Name: "ListDataSources", Kind: List, Resource: "dataSource", Parent: "apiId"},
		{Name: "DeleteDataSource", Kind: Delete, Resource: "dataSource", Parent: "apiId", Lookup: "name"},
		{Name: "CreateApi", Kind: Create, Resource: "api"},
		{Name: "GetApi", Kind: Read, Resource: "api", Lookup: "apiId"},
		{Name: "ListApis", Kind: List, Resource: "api"},
		{Name: "UpdateApi", Kind: Update, Resource: "api", Lookup: "apiId", Fields: []string{"name", "ownerContact", "eventConfig"}},
		{Name: "DeleteApi", Kind: Delete, Resource: "api", Lookup: "apiId"},
		{Name: "CreateChannelNamespace", Kind: Create, Resource: "channelNamespace", Parent: "apiId"},
		{Name: "GetChannelNamespace", Kind: Read, Resource: "channelNamespace", Parent: "apiId", Lookup: "name"},
		{Name: "ListChannelNamespaces", Kind: List, Resource: "channelNamespace", Parent: "apiId"},
		{Name: "UpdateChannelNamespace", Kind: Update, Resource: "channelNamespace", Parent: "apiId", Lookup: "name",
			Fields: []string{"subscribeAuthModes", "publishAuthModes", "codeHandlers", "handlerConfigs"}, Touch: []string{"lastModified"}},
		{Name: "DeleteChannelNamespace", Kind: Delete, Resource: "channelNamespace", Parent: "apiId", Lookup: "name"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"GraphQL APIs, data sources, Event APIs and channel namespaces are configuration metadata: no GraphQL, real-time or event endpoint is served (uris and dns are not returned), no query, mutation, subscription, publish or handler code runs, and Cognito user pools, OIDC issuers, Lambda authorizers, roles, log groups and data source targets are stored without contacting or checking them.",
		"Schemas, types, resolvers, functions, API keys, caches, custom domain names, environment variables, merged API associations and data source introspection return UnknownOperationException with HTTP 501. Data sources cannot be updated; delete and re-create them.",
		"Each authentication type needs its own configuration (user pool, OpenID Connect or Lambda authorizer), merged APIs need mergedApiExecutionRoleArn, and each data source type needs exactly its own configuration. UpdateGraphqlApi changes only the supplied members. Deleting a GraphQL API deletes its data sources, and deleting an Event API deletes its channel namespaces. ListGraphqlApis refuses owner.",
	},
}

var appSyncAuthConfig = map[string]string{"AMAZON_COGNITO_USER_POOLS": "userPoolConfig", "OPENID_CONNECT": "openIDConnectConfig", "AWS_LAMBDA": "lambdaAuthorizerConfig"}

// appSyncAuthorization requires the configuration that each documented
// authentication type depends on, for the primary and additional providers.
func appSyncAuthorization(input map[string]any) error {
	providers := []map[string]any{input}
	extra, _ := input["additionalAuthenticationProviders"].([]any)
	for _, item := range extra {
		provider, _ := item.(map[string]any)
		providers = append(providers, provider)
	}
	for _, provider := range providers {
		kind, _ := provider["authenticationType"].(string)
		if member := appSyncAuthConfig[kind]; member != "" && provider[member] == nil {
			return Failure("BadRequestException", kind+" authentication needs "+member+".")
		}
	}
	if input["apiType"] == "MERGED" && input["mergedApiExecutionRoleArn"] == nil {
		return Failure("BadRequestException", "Merged APIs need mergedApiExecutionRoleArn.")
	}
	return nil
}

var appSyncDataSourceConfig = map[string]string{
	"AWS_LAMBDA": "lambdaConfig", "AMAZON_DYNAMODB": "dynamodbConfig", "AMAZON_ELASTICSEARCH": "elasticsearchConfig",
	"AMAZON_OPENSEARCH_SERVICE": "openSearchServiceConfig", "HTTP": "httpConfig", "RELATIONAL_DATABASE": "relationalDatabaseConfig",
	"AMAZON_EVENTBRIDGE": "eventBridgeConfig",
}

// appSyncDataSource requires exactly the configuration of the data source
// type: none for NONE and AMAZON_BEDROCK_RUNTIME, its own for the others.
func appSyncDataSource(input map[string]any) error {
	kind, _ := input["type"].(string)
	want := appSyncDataSourceConfig[kind]
	for _, member := range appSyncDataSourceConfig {
		if _, ok := input[member]; ok && member != want {
			return Failure("BadRequestException", member+" does not apply to "+kind+" data sources.")
		}
	}
	if _, ok := input[want]; want != "" && !ok {
		return Failure("BadRequestException", kind+" data sources need "+want+".")
	}
	return nil
}

// locationResource describes one named Amazon Location Service resource.
func locationResource(kind, shape, summary, arnKind, nameMember, arnMember string, extra map[string]any) *Resource {
	generated := map[string]any{"CreateTime": "$now", "UpdateTime": "$now", "Description": "", "PricingPlan": "RequestBasedUsage"}
	for key, value := range extra {
		generated[key] = value
	}
	return &Resource{Kind: kind, Shape: shape, Summary: summary, ID: "name", ARN: "arn:{partition}:geo:{region}:{account}:" + arnKind + "/{name}",
		ARNMember: arnMember, NameMember: nameMember, Unique: true, Limit: 50, Generate: generated}
}

var location = &Service{
	Model: "location", Title: "Amazon Location Service", IAMPrefix: "geo", Issues: []int{150},
	Resources: []*Resource{
		locationResource("map", "DescribeMapResponse", "ListMapsResponseEntry", "map", "MapName", "MapArn", nil),
		locationResource("placeIndex", "DescribePlaceIndexResponse", "ListPlaceIndexesResponseEntry", "place-index", "IndexName", "IndexArn",
			map[string]any{"DataSourceConfiguration": map[string]any{"IntendedUse": "SingleUse"}}),
		locationResource("routeCalculator", "DescribeRouteCalculatorResponse", "ListRouteCalculatorsResponseEntry", "route-calculator", "CalculatorName", "CalculatorArn", nil),
		locationResource("tracker", "DescribeTrackerResponse", "ListTrackersResponseEntry", "tracker", "TrackerName", "TrackerArn",
			map[string]any{"PositionFiltering": "TimeBased", "EventBridgeEnabled": false}),
		locationResource("geofenceCollection", "DescribeGeofenceCollectionResponse", "ListGeofenceCollectionsResponseEntry", "geofence-collection", "CollectionName", "CollectionArn",
			map[string]any{"GeofenceCount": 0}),
	},
	Ops: []Op{
		{Name: "CreateMap", Kind: Create, Resource: "map", Check: locationMapStyle},
		{Name: "DescribeMap", Kind: Read, Resource: "map", Lookup: "MapName"},
		{Name: "ListMaps", Kind: List, Resource: "map"},
		{Name: "UpdateMap", Kind: Update, Resource: "map", Lookup: "MapName", Fields: []string{"PricingPlan", "Description"}, Touch: []string{"UpdateTime"}, Check: locationPricing},
		{Name: "DeleteMap", Kind: Delete, Resource: "map", Lookup: "MapName"},
		{Name: "CreatePlaceIndex", Kind: Create, Resource: "placeIndex", Check: locationProvider},
		{Name: "DescribePlaceIndex", Kind: Read, Resource: "placeIndex", Lookup: "IndexName"},
		{Name: "ListPlaceIndexes", Kind: List, Resource: "placeIndex"},
		{Name: "UpdatePlaceIndex", Kind: Update, Resource: "placeIndex", Lookup: "IndexName", Fields: []string{"PricingPlan", "Description", "DataSourceConfiguration"},
			Touch: []string{"UpdateTime"}, Check: locationPricing},
		{Name: "DeletePlaceIndex", Kind: Delete, Resource: "placeIndex", Lookup: "IndexName"},
		{Name: "CreateRouteCalculator", Kind: Create, Resource: "routeCalculator", Check: locationProvider},
		{Name: "DescribeRouteCalculator", Kind: Read, Resource: "routeCalculator", Lookup: "CalculatorName"},
		{Name: "ListRouteCalculators", Kind: List, Resource: "routeCalculator"},
		{Name: "UpdateRouteCalculator", Kind: Update, Resource: "routeCalculator", Lookup: "CalculatorName", Fields: []string{"PricingPlan", "Description"}, Touch: []string{"UpdateTime"},
			Check: locationPricing},
		{Name: "DeleteRouteCalculator", Kind: Delete, Resource: "routeCalculator", Lookup: "CalculatorName"},
		{Name: "CreateTracker", Kind: Create, Resource: "tracker", Check: locationPricing},
		{Name: "DescribeTracker", Kind: Read, Resource: "tracker", Lookup: "TrackerName"},
		{Name: "ListTrackers", Kind: List, Resource: "tracker"},
		{Name: "UpdateTracker", Kind: Update, Resource: "tracker", Lookup: "TrackerName", Touch: []string{"UpdateTime"}, Check: locationPricing,
			Fields: []string{"PricingPlan", "PricingPlanDataSource", "Description", "PositionFiltering", "EventBridgeEnabled", "KmsKeyEnableGeospatialQueries"}},
		{Name: "DeleteTracker", Kind: Delete, Resource: "tracker", Lookup: "TrackerName"},
		{Name: "CreateGeofenceCollection", Kind: Create, Resource: "geofenceCollection", Check: locationPricing},
		{Name: "DescribeGeofenceCollection", Kind: Read, Resource: "geofenceCollection", Lookup: "CollectionName"},
		{Name: "ListGeofenceCollections", Kind: List, Resource: "geofenceCollection"},
		{Name: "UpdateGeofenceCollection", Kind: Update, Resource: "geofenceCollection", Lookup: "CollectionName",
			Fields: []string{"PricingPlan", "PricingPlanDataSource", "Description"}, Touch: []string{"UpdateTime"}, Check: locationPricing},
		{Name: "DeleteGeofenceCollection", Kind: Delete, Resource: "geofenceCollection", Lookup: "CollectionName"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Maps, place indexes, route calculators, trackers and geofence collections are configuration metadata: no map tile, style, glyph, sprite, geocode, place, route, matrix, device position or geofence evaluation is produced, no data provider is contacted, and KMS keys are stored without checks. GetMapTile, SearchPlaceIndexForText, CalculateRoute, BatchUpdateDevicePosition, PutGeofence and the other data-plane APIs return UnknownOperationException with HTTP 501.",
		"Map styles must name the Esri, HERE, Grab or Open Data provider, which is reported as DataSource; place indexes and route calculators accept the Esri, Grab and Here data sources. PricingPlan is no longer used: it is reported as RequestBasedUsage, the only value accepted. UpdateMap refuses ConfigurationUpdate. Geofence collections hold no geofences, so GeofenceCount stays 0.",
		"API keys and tracker consumers are not implemented. SDK clients must disable host-prefix injection (botocore inject_host_prefix=False) because the official cp.maps., cp.places., cp.routes., cp.tracking. and cp.geofencing. host prefixes cannot address the loopback endpoint.",
	},
}

// locationStyleProviders maps the provider name embedded in documented map
// style names onto the reported data source.
var locationStyleProviders = []string{"Esri", "Here", "Grab", "OpenData"}

// locationPricing accepts only RequestBasedUsage, the one value the
// official API still allows for its unused pricing plan.
func locationPricing(input map[string]any) error {
	if plan, ok := input["PricingPlan"]; ok && plan != "RequestBasedUsage" {
		return Failure("ValidationException", "PricingPlan is no longer used; the only allowed value is RequestBasedUsage.")
	}
	return nil
}

// locationMapStyle derives the official DataSource of a map from its style
// and refuses styles of no documented provider.
func locationMapStyle(input map[string]any) error {
	if err := locationPricing(input); err != nil {
		return err
	}
	configuration, _ := input["Configuration"].(map[string]any)
	style, _ := configuration["Style"].(string)
	for _, provider := range locationStyleProviders {
		if strings.Contains(style, provider) {
			input["DataSource"] = provider
			return nil
		}
	}
	return Failure("ValidationException", "Map styles must name the Esri, Here, Grab or OpenData data provider.")
}

// locationProvider accepts the documented place and route data providers.
func locationProvider(input map[string]any) error {
	if err := locationPricing(input); err != nil {
		return err
	}
	switch input["DataSource"] {
	case "Esri", "Grab", "Here":
		return nil
	}
	return Failure("ValidationException", "DataSource must be Esri, Grab or Here.")
}

var deviceFarm = &Service{
	Model: "devicefarm", Title: "AWS Device Farm", IAMPrefix: "devicefarm", Issues: []int{149},
	Resources: []*Resource{
		{Kind: "project", Shape: "Project", ID: "uuid", ARN: "arn:{partition}:devicefarm:{region}:{account}:project:{id}",
			ARNMember: "arn", NameMember: "name", Limit: 50, Generate: map[string]any{"defaultJobTimeoutMinutes": 150, "created": "$now"}},
		{Kind: "instanceProfile", Shape: "InstanceProfile", ID: "uuid", ARN: "arn:{partition}:devicefarm:{region}:{account}:instanceprofile:{id}",
			ARNMember: "arn", NameMember: "name", Limit: 50, Generate: map[string]any{"packageCleanup": false, "rebootAfterUse": true, "excludeAppPackagesFromCleanup": []any{}}},
		{Kind: "testGridProject", Shape: "TestGridProject", ID: "uuid", ARN: "arn:{partition}:devicefarm:{region}:{account}:testgrid-project:{id}",
			ARNMember: "arn", NameMember: "name", Limit: 50, Generate: map[string]any{"created": "$now"}},
		{Kind: "vpceConfiguration", Shape: "VPCEConfiguration", ID: "uuid", ARN: "arn:{partition}:devicefarm:{region}:{account}:vpceconfiguration:{id}",
			ARNMember: "arn", NameMember: "vpceConfigurationName", Limit: 5},
	},
	Ops: []Op{
		{Name: "CreateProject", Kind: Create, Resource: "project"},
		{Name: "GetProject", Kind: Read, Resource: "project", Lookup: "arn"},
		{Name: "ListProjects", Kind: List, Resource: "project", Filters: map[string]string{"arn": "arn"}},
		{Name: "UpdateProject", Kind: Update, Resource: "project", Lookup: "arn",
			Fields: []string{"name", "defaultJobTimeoutMinutes", "vpcConfig", "environmentVariables", "executionRoleArn"}},
		{Name: "DeleteProject", Kind: Delete, Resource: "project", Lookup: "arn"},
		{Name: "CreateInstanceProfile", Kind: Create, Resource: "instanceProfile"},
		{Name: "GetInstanceProfile", Kind: Read, Resource: "instanceProfile", Lookup: "arn"},
		{Name: "ListInstanceProfiles", Kind: List, Resource: "instanceProfile"},
		{Name: "UpdateInstanceProfile", Kind: Update, Resource: "instanceProfile", Lookup: "arn",
			Fields: []string{"name", "description", "packageCleanup", "excludeAppPackagesFromCleanup", "rebootAfterUse"}},
		{Name: "DeleteInstanceProfile", Kind: Delete, Resource: "instanceProfile", Lookup: "arn"},
		{Name: "CreateTestGridProject", Kind: Create, Resource: "testGridProject"},
		{Name: "GetTestGridProject", Kind: Read, Resource: "testGridProject", Lookup: "projectArn"},
		{Name: "ListTestGridProjects", Kind: List, Resource: "testGridProject"},
		{Name: "UpdateTestGridProject", Kind: Update, Resource: "testGridProject", Lookup: "projectArn", Fields: []string{"name", "description", "vpcConfig"}},
		{Name: "DeleteTestGridProject", Kind: Delete, Resource: "testGridProject", Lookup: "projectArn"},
		{Name: "CreateVPCEConfiguration", Kind: Create, Resource: "vpceConfiguration"},
		{Name: "GetVPCEConfiguration", Kind: Read, Resource: "vpceConfiguration", Lookup: "arn"},
		{Name: "ListVPCEConfigurations", Kind: List, Resource: "vpceConfiguration"},
		{Name: "UpdateVPCEConfiguration", Kind: Update, Resource: "vpceConfiguration", Lookup: "arn",
			Fields: []string{"vpceConfigurationName", "vpceServiceName", "serviceDnsName", "vpceConfigurationDescription"}},
		{Name: "DeleteVPCEConfiguration", Kind: Delete, Resource: "vpceConfiguration", Lookup: "arn"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Projects, instance profiles, Selenium test grid projects and VPC endpoint configurations are configuration metadata: no device, device slot, upload, run, job, remote access session or test grid session exists, no test runs, no Selenium endpoint is served and no VPC endpoint service is contacted. ScheduleRun, CreateUpload, CreateTestGridUrl and the other run APIs return UnknownOperationException with HTTP 501.",
		"Device pools and network profiles are not implemented because they are read and changed by their own ARN without naming their project; CreateDevicePool, CreateNetworkProfile, their other operations, ListDevices, GetAccountSettings and the offering APIs return UnknownOperationException with HTTP 501.",
		"VPC, subnet, security group and role identifiers are stored without checks. ListTestGridProjects refuses maxResult. Validation failures detected by the simulator are reported as ValidationException, not the official ArgumentException code.",
	},
}
