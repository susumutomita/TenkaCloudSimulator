package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

// EC2 Query/XML: discovery of CloudFormation-owned and EC2-owned local
// resources, plus the bounded native VPC lifecycle in ec2_vpc.go. No VM
// liveness is inferred from a saved plan or a successful stack creation.
func isEC2(r *http.Request) bool {
	if s := r.Header.Get("X-Simulator-Service"); s != "" {
		return s == "ec2"
	}
	return elbSigningService(r.Header.Get("Authorization")) == "ec2"
}
func ec2Error(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"Response"`
		Code    string   `xml:"Errors>Error>Code"`
		Message string   `xml:"Errors>Error>Message"`
	}{Code: code, Message: message})
}

// ec2QueryBytes bounds one Query request. It admits 50 maximum-length tags.
const ec2QueryBytes = 64 << 10

func (g *Gateway) serveEC2(w http.ResponseWriter, r *http.Request) {
	fail := func(code, msg string, status int) { ec2Error(w, code, msg, status) }
	failChild := func(err error, fallback string) {
		var failure *ec2Failure
		if errors.As(err, &failure) {
			fail(failure.code, failure.message, failure.status)
		} else {
			fail("InternalError", fallback, 500)
		}
	}
	for _, h := range []string{"Authorization", "X-Simulator-Service", "X-Simulator-Region", "X-Simulator-Principal", "X-Amz-Security-Token", "X-Amz-Target"} {
		if len(r.Header.Values(h)) > 1 {
			fail("InvalidParameterValue", "Duplicate headers are unsupported.", 400)
			return
		}
	}
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || r.Header.Get("X-Amz-Target") != "" {
		fail("InvalidRequest", "Use POST Query/XML on /.", 400)
		return
	}
	region := r.Header.Get("X-Simulator-Region")
	if r.Header.Get("X-Amz-Security-Token") != "" {
		fail("UnsupportedOperation", "Session credentials are unsupported by local EC2.", 400)
		return
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		_, scope, ok := strings.Cut(auth, "Credential=")
		credential, _, _ := strings.Cut(scope, ",")
		parts := strings.Split(credential, "/")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || !ok || len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[1] == "" || parts[3] != "ec2" || parts[4] != "aws4_request" {
			fail("UnauthorizedOperation", "Use dummy local EC2 credentials.", 403)
			return
		}
		if region != "" && region != parts[2] {
			fail("InvalidParameterValue", "Region scope mismatch.", 400)
			return
		}
		region = parts[2]
	}
	if region == "" {
		region = "us-east-1"
	}
	if !slices.Contains(Regions, region) {
		fail("InvalidParameterValue", "Unsupported region.", 400)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, ec2QueryBytes))
	if err != nil {
		fail("InvalidRequest", "Query exceeds local bound or could not be read.", 400)
		return
	}
	fields, err := url.ParseQuery(string(raw))
	if err != nil {
		fail("InvalidParameterValue", "Malformed Query.", 400)
		return
	}
	action := fields.Get("Action")
	if len(fields["Action"]) != 1 || len(fields["Version"]) != 1 || fields.Get("Version") != "2016-11-15" || !slices.Contains(ec2Operations, action) {
		fail("UnsupportedOperation", "Only "+strings.Join(ec2Operations, ", ")+" version 2016-11-15 are supported.", 400)
		return
	}
	request, failure := parseEC2Request(action, fields)
	if failure != nil {
		fail(failure.code, failure.message, failure.status)
		return
	}
	if g.Stacks == nil || g.Identities == nil {
		fail("InternalError", "EC2 stores are unavailable.", 500)
		return
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	boundary, instanceScoped := r.Context().Value(consoleInstanceBoundaryKey{}).(consoleInstanceBoundary)
	var routeAssociationAuthority ec2NativeRouteAssociationAuthority
	if instanceScoped {
		policy, ok := SDKPolicyFromContext(r.Context())
		if action != "DescribeInstances" || region != boundary.region || !ok || core.Evaluate(policy, "ec2:DescribeInstances", "*") != "allowed" {
			fail("UnauthorizedOperation", "This console session is limited to its own instance.", 403)
			return
		}
	} else if principal != core.Administrator {
		policy, found, e := g.Identities.PolicyContext(r.Context(), principal)
		if e != nil {
			fail("InternalError", "Identity state unavailable.", 500)
			return
		}
		if request.routeAssociation != nil {
			if !found {
				failChild(deniedNativeRouteAssociation(), "")
				return
			}
			routeAssociationAuthority.check = func(action, resource string) bool {
				return core.Evaluate(policy, action, resource) == "allowed"
			}
		}
		for _, check := range request.permissions(action, region) {
			if !found || core.Evaluate(policy, check[0], check[1]) != "allowed" {
				fail("UnauthorizedOperation", "You are not authorized to perform "+check[0]+" on "+check[1]+".", 403)
				return
			}
		}
		exactChildGrant := func(checks [][2]string, kind string) error {
			for _, check := range checks {
				if core.Evaluate(policy, check[0], check[1]) != "allowed" {
					return &ec2Failure{"UnauthorizedOperation", "You are not authorized to create this " + kind + ".", 403}
				}
			}
			return nil
		}
		switch action {
		case "CreateSubnet":
			request.subnet.authorizeCreatedID = func(id string) error {
				return exactChildGrant([][2]string{nativeSubnetCreatePermission(region, id)}, "subnet")
			}
		case "CreateSecurityGroup":
			request.securityGroup.authorizeCreatedID = func(id string) error {
				return exactChildGrant(request.securityGroup.createPermissions(region, id), "security group")
			}
		case "CreateNetworkAcl":
			request.networkACL.authorizeCreateID = func(id string) error {
				return exactChildGrant(request.networkACL.aclCreatePermissions(region, id), "network ACL")
			}
		case ec2CreateInternetGateway:
			request.internetGateway.authorizeCreatedID = func(id string) error {
				return exactChildGrant(request.internetGateway.createPermissions(region, id), "internet gateway")
			}
		case "CreateRouteTable":
			request.routeTable.authorizeCreatedID = func(id string) error {
				return exactChildGrant(request.routeTable.createPermissions(region, id), "route table")
			}
		}
	} else if request.routeAssociation != nil {
		// Only the matched effective local administrator (never a scoped
		// instance session) receives this authority and native-not-found detail.
		routeAssociationAuthority = ec2NativeRouteAssociationAuthority{check: func(string, string) bool { return true }, revealMissingID: true}
	}
	var out any
	const sgFailure = "Local security group configuration could not be read or changed."
	switch action {
	case "CreateSecurityGroup", "DeleteSecurityGroup":
		group, err := g.Stacks.executeEC2NativeSGMutation(r.Context(), action, region, *request.securityGroup)
		if err != nil {
			failChild(err, sgFailure)
			return
		}
		out = group
	case ec2AuthorizeNativeSGIngress, ec2AuthorizeNativeSGEgress, ec2RevokeNativeSGIngress, ec2RevokeNativeSGEgress:
		rule, err := g.Stacks.executeEC2NativeSGRuleMutation(r.Context(), action, region, *request.securityGroup)
		if err != nil {
			failChild(err, sgFailure)
			return
		}
		out = rule
	case "CreateNetworkAcl", "DescribeNetworkAcls", "DeleteNetworkAcl",
		ec2CreateNetworkACLEntry, ec2ReplaceNetworkACLEntry, ec2DeleteNetworkACLEntry:
		result, err := g.Stacks.executeEC2NetworkACL(r.Context(), action, region, *request.networkACL)
		if err != nil {
			failChild(err, "Local detached network ACL state could not be read or changed.")
			return
		}
		out = result
	case ec2CreateInternetGateway, ec2DescribeInternetGateway, ec2DeleteInternetGateway, ec2AttachInternetGateway, ec2DetachInternetGateway:
		if request.internetGateway.paginated() {
			caller, ok := provider.ActorFromContext(r.Context())
			if !ok || caller.ExerciseIdentity != principal {
				fail("UnauthorizedOperation", "A verified local actor is required for an internet gateway page.", 403)
				return
			}
			request.internetGateway.caller = caller
		}
		gateways, err := g.Stacks.executeEC2InternetGateway(r.Context(), action, region, *request.internetGateway)
		if err != nil {
			failChild(err, "Local internet gateway configuration could not be read or changed.")
			return
		}
		out = gateways
	case ec2CreateNativeDefaultRoute, ec2DeleteNativeDefaultRoute:
		route, err := g.Stacks.executeEC2NativeDefaultRoute(r.Context(), action, region, *request.nativeDefaultRoute)
		if err != nil {
			failChild(err, "Local native route state could not be changed; nothing was committed.")
			return
		}
		out = route
	case "CreateRouteTable", "DeleteRouteTable":
		table, err := g.Stacks.executeEC2NativeRouteTableMutation(r.Context(), action, region, *request.routeTable)
		if err != nil {
			failChild(err, "Local detached route table metadata could not be changed.")
			return
		}
		out = table
	case ec2AssociateNativeRouteTable, ec2DisassociateNativeRouteTable, ec2ReplaceNativeRouteTable:
		association, err := g.Stacks.executeEC2NativeRouteAssociation(r.Context(), action, region, *request.routeAssociation, routeAssociationAuthority)
		if err != nil {
			failChild(err, "Local explicit route table association could not be changed.")
			return
		}
		out = association
	case "DescribeRouteTables":
		query := request.routeTableRead
		var caller provider.Actor
		if query.paginated() {
			actor, ok := provider.ActorFromContext(r.Context())
			if !ok || actor.ExerciseIdentity != principal {
				fail("UnauthorizedOperation", "A verified local actor is required for a route table page.", 403)
				return
			}
			caller = actor
		}
		tables, err := g.Stacks.executeEC2RouteTableRead(r.Context(), region, query, caller)
		const failure = "Local route-table ownership or state is inconsistent or exceeds the local bound."
		if err != nil {
			failChild(err, failure)
			return
		}
		if query.paginated() || query.id != "" {
			wire, err := encodeEC2RouteTableBounded(tables, query.id != "")
			if err != nil {
				failChild(err, failure)
				return
			}
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write(wire)
			return
		}
		out = tables
	case "CreateSubnet":
		subnet, e := g.Stacks.createNativeSubnet(r.Context(), region, request.subnet)
		if e != nil {
			g.failEC2Mutation(w, e)
			return
		}
		out = &ec2CreateSubnetResponse{Subnet: subnet}
	case "DeleteSubnet":
		if e := g.Stacks.deleteNativeSubnet(r.Context(), region, request.subnet.id); e != nil {
			g.failEC2Mutation(w, e)
			return
		}
		out = &ec2DeleteSubnetResponse{Return: true}
	case ec2ModifyNativeSubnet:
		if e := g.Stacks.modifyNativeSubnetPublicIPv4(r.Context(), region, request.subnet.id, *request.subnet.publicIPv4); e != nil {
			g.failEC2Mutation(w, e)
			return
		}
		out = &ec2ModifySubnetResponse{Return: true}
	case ec2DescribeVPCAttribute, ec2ModifyVPCAttribute:
		if request.vpcAttribute.dryRun {
			fail("DryRunOperation", "The local request is authorized; no operation was performed.", http.StatusPreconditionFailed)
			return
		}
		result, err := g.Stacks.vpcAttribute(r.Context(), action, region, request.vpcID, request.vpcAttribute)
		if err != nil {
			failChild(err, "Local VPC settings are unavailable; no change was committed.")
			return
		}
		out = result
	case "CreateVpc":
		vpc, e := g.Stacks.createNativeVPC(r.Context(), region, request.cidr, request.tags)
		if e != nil {
			g.failEC2Mutation(w, e)
			return
		}
		out = &ec2CreateVpcResponse{VPC: ec2VPCRecord(vpc)}
	case "DeleteVpc":
		if e := g.Stacks.deleteNativeVPC(r.Context(), region, request.vpcID); e != nil {
			g.failEC2Mutation(w, e)
			return
		}
		out = &ec2DeleteVpcResponse{Return: true}
	default:
		read, e := g.Stacks.ec2Read(r.Context(), region, action)
		if e != nil {
			fail("InternalError", "Local discovery state is inconsistent or exceeds the 1000-resource bound.", 500)
			return
		}
		if action == "DescribeSubnets" {
			out = &ec2DescribeSubnetsResponse{Subnets: *read.Subnets}
			break
		}
		if action == "DescribeSecurityGroups" {
			groups, err := request.securityGroup.filterSecurityGroups(read.securityGroups)
			if err != nil {
				failChild(err, sgFailure)
				return
			}
			out = &ec2DescribeSecurityGroupsResponse{Groups: ec2SecurityGroupSet{Items: groups}}
			break
		}
		if instanceScoped {
			read.Reservations = onlyInstance(read.Reservations, boundary.instance)
		}
		read.XMLName = xml.Name{Local: action + "Response"}
		out = &read
	}
	stampEC2Response(out)
	w.Header().Set("Content-Type", "text/xml")
	_ = xml.NewEncoder(w).Encode(out)
}

type ec2Tag struct {
	Key   string `xml:"key"`
	Value string `xml:"value"`
}
type ec2Group struct {
	ID string `xml:"groupId"`
}
type ec2InstanceRead struct {
	ID      string     `xml:"instanceId"`
	Image   string     `xml:"imageId"`
	Type    string     `xml:"instanceType"`
	Subnet  string     `xml:"subnetId"`
	VPC     string     `xml:"vpcId"`
	Runtime string     `xml:"simulatorRuntimeState"`
	Groups  []ec2Group `xml:"groupSet>item"`
	Tags    []ec2Tag   `xml:"tagSet>item"`
}
type ec2ReservationRead struct {
	Owner     string            `xml:"ownerId"`
	Instances []ec2InstanceRead `xml:"instancesSet>item"`
}

// ManagedBy is a local extension: "ec2" VPCs come from CreateVpc and can be
// deleted with DeleteVpc; "cloudformation" VPCs are deleted with their stack.
type ec2VPCRead struct {
	ID        string   `xml:"vpcId"`
	CIDR      string   `xml:"cidrBlock"`
	Tags      []ec2Tag `xml:"tagSet>item"`
	ManagedBy string   `xml:"simulatorManagedBy"`
}
type ec2SubnetRead struct {
	ID         string   `xml:"subnetId"`
	VPC        string   `xml:"vpcId"`
	CIDR       string   `xml:"cidrBlock"`
	State      string   `xml:"state"`
	AZ         string   `xml:"availabilityZone,omitempty"`
	ManagedBy  string   `xml:"simulatorManagedBy,omitempty"`
	PublicIPv4 *bool    `xml:"mapPublicIpOnLaunch,omitempty"`
	Tags       []ec2Tag `xml:"tagSet>item"`
}
type ec2SubnetSet struct {
	Items []ec2SubnetRead `xml:"item"`
}
type ec2ReadResponse struct {
	Subnets      *ec2SubnetSet `xml:"subnetSet,omitempty"`
	XMLName      xml.Name
	XMLNS        string               `xml:"xmlns,attr"`
	RequestID    string               `xml:"requestId"`
	Reservations []ec2ReservationRead `xml:"reservationSet>item,omitempty"`
	VPCs         []ec2VPCRead         `xml:"vpcSet>item,omitempty"`

	securityGroups []ec2SecurityGroupRead // Only serialized by ec2DescribeSecurityGroupsResponse.
}

func ec2ReadTags(tags map[string]string) []ec2Tag {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []ec2Tag{}
	for _, k := range keys {
		out = append(out, ec2Tag{k, tags[k]})
	}
	return out
}
func (s *StackStore) ec2Read(ctx context.Context, region, action string) (ec2ReadResponse, error) {
	out := ec2ReadResponse{}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	kind := "AWS::EC2::VPC"
	if action == "DescribeInstances" {
		kind = "AWS::EC2::Instance"
	} else if action == "DescribeSubnets" {
		kind = "AWS::EC2::Subnet"
		out.Subnets = &ec2SubnetSet{}
	} else if action == "DescribeSecurityGroups" {
		kind = ec2SecurityGroupKind
	}
	rows, e := tx.QueryContext(ctx, `SELECT o.physical_id,o.stack_id,s.document FROM aws_cfn_ownership o JOIN aws_cfn_stacks s ON s.id=o.stack_id WHERE o.namespace=? AND o.kind=? AND s.region=? ORDER BY o.physical_id LIMIT 1001`, stackResourceNamespace(region, kind), kind, region)
	if e != nil {
		return out, e
	}
	type owned struct{ id, stack, document string }
	var all []owned
	for rows.Next() {
		var item owned
		if e = rows.Scan(&item.id, &item.stack, &item.document); e != nil {
			rows.Close()
			return out, e
		}
		all = append(all, item)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(all) > 1000 {
		return out, fmt.Errorf("resource bound")
	}
	for _, item := range all {
		var record StackRecord
		if json.Unmarshal([]byte(item.document), &record) != nil || record.ID != item.stack || record.Region != region {
			return out, fmt.Errorf("stack identity")
		}
		var resource *StackResource
		for i := range record.Resources {
			r := &record.Resources[i]
			if r.Type == kind && r.PhysicalID == item.id {
				if resource != nil {
					return out, fmt.Errorf("duplicate resource")
				}
				resource = r
			}
		}
		if resource == nil {
			return out, fmt.Errorf("missing resource")
		}
		if kind == ec2SecurityGroupKind {
			group, err := ec2OwnedSecurityGroup(ctx, tx, record, *resource)
			if err != nil {
				return out, err
			}
			out.securityGroups = append(out.securityGroups, group)
			continue
		}
		if kind == "AWS::EC2::VPC" || kind == "AWS::EC2::Subnet" {
			v, e := networkRead(ctx, tx, region, item.id)
			if e != nil || v.StackID != item.stack || v.Kind != kind {
				return out, fmt.Errorf("network identity")
			}
			if kind == "AWS::EC2::Subnet" {
				out.Subnets.Items = append(out.Subnets.Items, ec2SubnetRead{ID: v.ID, VPC: v.VPCID, CIDR: v.CIDR, State: "available", AZ: v.AvailabilityZone, Tags: ec2ReadTags(v.Tags)})
			} else {
				out.VPCs = append(out.VPCs, ec2VPCRead{v.ID, v.CIDR, ec2ReadTags(v.Tags), ec2ManagedByCloudFormation})
			}
			continue
		}
		props := resource.Properties
		subnet, e := networkRead(ctx, tx, region, stringValue(props["SubnetId"]))
		if e != nil || subnet.StackID != item.stack || subnet.Kind != "AWS::EC2::Subnet" {
			return out, fmt.Errorf("subnet identity")
		}
		tags, e := networkTags(props["Tags"])
		if e != nil {
			return out, e
		}
		instance := ec2InstanceRead{ID: item.id, Image: stringValue(props["ImageId"]), Type: stringValue(props["InstanceType"]), Subnet: subnet.ID, VPC: subnet.VPCID, Runtime: "unknown", Tags: ec2ReadTags(tags)}
		groups, ok := props["SecurityGroupIds"].([]any)
		if !ok {
			return out, fmt.Errorf("groups")
		}
		for _, id := range groups {
			group, e := networkRead(ctx, tx, region, stringValue(id))
			if e != nil || group.Kind != "AWS::EC2::SecurityGroup" || group.StackID != item.stack || group.VPCID != subnet.VPCID {
				return out, fmt.Errorf("group identity")
			}
			instance.Groups = append(instance.Groups, ec2Group{group.ID})
		}
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='aws_ec2_instance_runtime'`).Scan(&count); e != nil {
			return out, e
		}
		if count == 1 {
			var state string
			e = tx.QueryRowContext(ctx, `SELECT state FROM aws_ec2_instance_runtime WHERE region=? AND stack_id=? AND instance=?`, region, item.stack, item.id).Scan(&state)
			if e != nil && e != sql.ErrNoRows {
				return out, e
			}
			if e == nil {
				switch state {
				case "ready", "launch_pending", "seal_pending":
					instance.Runtime = state
				default:
					instance.Runtime = "unknown"
				}
			}
		}
		out.Reservations = append(out.Reservations, ec2ReservationRead{Owner: "000000000000", Instances: []ec2InstanceRead{instance}})
	}
	if kind == "AWS::EC2::VPC" {
		native, e := nativeVPCs(ctx, tx, region)
		if e != nil {
			return out, e
		}
		for _, v := range native {
			out.VPCs = append(out.VPCs, ec2VPCRecord(v))
		}
		if len(out.VPCs) > 1000 {
			return out, fmt.Errorf("resource bound")
		}
		sort.Slice(out.VPCs, func(i, j int) bool { return out.VPCs[i].ID < out.VPCs[j].ID })
	}
	if out.Subnets != nil {
		if err := addNativeSubnetReadback(ctx, tx, region, out.Subnets); err != nil {
			return out, err
		}
	}
	if kind == ec2SecurityGroupKind {
		groups, err := listEC2NativeSecurityGroups(ctx, tx, region)
		if err != nil || len(groups)+len(out.securityGroups) > ec2NativeSGNetworkLimit {
			return out, errEC2NativeSecurityGroupState
		}
		for _, g := range groups {
			out.securityGroups = append(out.securityGroups, g.readback())
		}
		sort.Slice(out.securityGroups, func(i, j int) bool { return out.securityGroups[i].ID < out.securityGroups[j].ID })
	}
	return out, tx.Commit()
}

// onlyInstance keeps the one bound instance; every other resource is dropped
// before serialization.
func onlyInstance(reservations []ec2ReservationRead, id string) []ec2ReservationRead {
	kept := []ec2ReservationRead{}
	for _, reservation := range reservations {
		instances := []ec2InstanceRead{}
		for _, instance := range reservation.Instances {
			if instance.ID == id {
				instances = append(instances, instance)
			}
		}
		if len(instances) > 0 {
			reservation.Instances = instances
			kept = append(kept, reservation)
		}
	}
	return kept
}
