package aws

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Native EC2 VPC lifecycle. CreateVpc stores a VPC in the same
// aws_ec2_network_resources table as CloudFormation networks, under an owner
// that can never be a stack ARN. Neither side adopts or deletes the other's
// VPCs: CloudFormation children require a parent of their own stack, and
// DeleteVpc refuses CloudFormation-owned VPCs.
const (
	ec2NativeOwner             = "ec2:native"
	ec2ManagedByEC2            = "ec2"
	ec2ManagedByCloudFormation = "cloudformation"
)

var ec2Operations = slices.Concat(
	[]string{"DescribeInstances", "DescribeVpcs", "DescribeSubnets", "DescribeSecurityGroups", "DescribeRouteTables", "CreateVpc", "DeleteVpc", ec2DescribeVPCAttribute, ec2ModifyVPCAttribute, ec2CreateVPCTag, ec2DeleteVPCTag, "CreateSubnet", "DeleteSubnet", ec2ModifyNativeSubnet},
	ec2LaunchTemplateOperations, ec2NativeSecurityGroupMutations, ec2NetworkACLOperations, ec2NativeRouteTableMutations, ec2NativeRouteAssociationOperations, ec2InternetGatewayOperations, ec2NativeDefaultRouteOperations,
)

// EC2Operations lists registered native Query operations, not granted permissions.
func EC2Operations() []string { return slices.Clone(ec2Operations) }

var (
	ec2VPCIDPattern  = regexp.MustCompile(`^vpc-[0-9a-f]{8,17}$`)
	ec2TagSpecType   = regexp.MustCompile(`^TagSpecification\.([1-9][0-9]{0,2})\.ResourceType$`)
	ec2TagSpecMember = regexp.MustCompile(`^TagSpecification\.([1-9][0-9]{0,2})\.Tag\.([1-9][0-9]{0,2})\.(Key|Value)$`)
)

type ec2Failure struct {
	code, message string
	status        int
}

func (f *ec2Failure) Error() string { return f.code + ": " + f.message }

func ec2Invalid(code, message string) *ec2Failure { return &ec2Failure{code, message, 400} }

type ec2Request struct {
	cidr, vpcID        string
	tags               map[string]string
	subnet             ec2SubnetMutation
	vpcAttribute       ec2VPCAttributeQuery
	nativeTag          ec2NativeTagQuery
	securityGroup      *ec2NativeSecurityGroupQuery
	networkACL         *ec2NetworkACLRequest
	routeTable         *ec2NativeRouteTableQuery
	routeAssociation   *ec2NativeRouteAssociationQuery
	routeTableRead     ec2RouteTableQuery
	nativeDefaultRoute *ec2NativeDefaultRouteQuery
	internetGateway    *ec2InternetGatewayQuery
}

// permissions prechecks known resources. The route association lifecycle
// instead receives a bound decision to check its exact endpoints in the store.
// Tagging at creation also needs ec2:CreateTags.
func (q ec2Request) permissions(action, region string) [][2]string {
	switch {
	case q.securityGroup != nil:
		return q.securityGroup.permissions(action, region)
	case q.networkACL != nil:
		return q.networkACL.permissions(action, region)
	case q.routeTable != nil:
		return q.routeTable.permissions(action, region)
	case q.nativeDefaultRoute != nil:
		return q.nativeDefaultRoute.permissions(action, region)
	case q.routeAssociation != nil:
		return nil
	case q.internetGateway != nil:
		return q.internetGateway.permissions(action, region)
	}
	switch action {
	case "CreateSubnet", "DeleteSubnet", ec2ModifyNativeSubnet:
		return q.subnet.permissions(action, region)
	case "CreateVpc":
		out := [][2]string{{"ec2:CreateVpc", networkARN(region, "vpc", "*")}}
		if len(q.tags) > 0 {
			out = append(out, [2]string{"ec2:CreateTags", networkARN(region, "vpc", "*")})
		}
		return out
	case ec2CreateVPCTag, ec2DeleteVPCTag:
		return [][2]string{{"ec2:" + action, networkARN(region, q.nativeTag.resourceType, q.nativeTag.id)}}
	case "DeleteVpc", ec2DescribeVPCAttribute, ec2ModifyVPCAttribute:
		return [][2]string{{"ec2:" + action, networkARN(region, "vpc", q.vpcID)}}
	}
	return [][2]string{{"ec2:" + action, "*"}}
}

// parseEC2Request admits only supported members of each action. Unknown
// members are rejected rather than ignored.
func parseEC2Request(action string, fields map[string][]string) (ec2Request, *ec2Failure) {
	if action == ec2DescribeVPCAttribute || action == ec2ModifyVPCAttribute {
		return parseEC2VPCAttribute(action, fields)
	}
	if action == ec2CreateVPCTag || action == ec2DeleteVPCTag {
		return parseEC2NativeTag(action, fields)
	}
	if action == "DescribeRouteTables" {
		query, err := parseEC2RouteTableSelection(fields)
		return ec2Request{routeTableRead: query}, err
	}
	if action == "DescribeSecurityGroups" || slices.Contains(ec2NativeSecurityGroupMutations, action) {
		q, err := parseEC2NativeSGQuery(action, fields)
		return ec2Request{securityGroup: &q}, err
	}
	if slices.Contains(ec2NetworkACLOperations, action) {
		q, err := parseEC2NetworkACLRequest(action, fields)
		return ec2Request{networkACL: &q}, err
	}
	if slices.Contains(ec2NativeDefaultRouteOperations, action) {
		q, err := parseEC2NativeDefaultRoute(action, fields)
		return ec2Request{nativeDefaultRoute: &q}, err
	}
	if slices.Contains(ec2NativeRouteTableMutations, action) {
		q, err := parseEC2NativeRouteTableMutation(action, fields)
		return ec2Request{routeTable: &q}, err
	}
	if slices.Contains(ec2NativeRouteAssociationOperations, action) {
		q, err := parseEC2NativeRouteAssociation(action, fields)
		return ec2Request{routeAssociation: &q}, err
	}
	if slices.Contains(ec2InternetGatewayOperations, action) {
		q, err := parseEC2InternetGateway(action, fields)
		return ec2Request{internetGateway: &q}, err
	}
	q := ec2Request{}
	if action == "CreateSubnet" || action == "DeleteSubnet" || action == ec2ModifyNativeSubnet {
		sub, err := parseEC2SubnetMutation(action, fields)
		q.subnet = sub
		return q, err
	}
	type tagMember struct{ key, value *string }
	specs := map[int]string{}
	tags := map[int]map[int]*tagMember{}
	for key, values := range fields {
		if len(values) != 1 {
			return q, ec2Invalid("InvalidParameterValue", "Parameter "+key+" must be sent once.")
		}
		value := values[0]
		switch {
		case key == "Action" || key == "Version":
		case key == "DryRun" && (action == "CreateVpc" || action == "DeleteVpc"):
			if value != "false" {
				return q, ec2Invalid("UnsupportedOperation", "DryRun is not implemented locally; the request was not run.")
			}
		case key == "CidrBlock" && action == "CreateVpc":
			q.cidr = value
		case key == "InstanceTenancy" && action == "CreateVpc":
			if value != "default" {
				return q, ec2Invalid("UnsupportedOperation", "Only default instance tenancy is supported locally.")
			}
		case key == "VpcId" && action == "DeleteVpc":
			q.vpcID = value
		case action == "CreateVpc" && ec2TagSpecType.MatchString(key):
			n, _ := strconv.Atoi(ec2TagSpecType.FindStringSubmatch(key)[1])
			specs[n] = value
		case action == "CreateVpc" && ec2TagSpecMember.MatchString(key):
			m := ec2TagSpecMember.FindStringSubmatch(key)
			n, _ := strconv.Atoi(m[1])
			i, _ := strconv.Atoi(m[2])
			if tags[n] == nil {
				tags[n] = map[int]*tagMember{}
			}
			if tags[n][i] == nil {
				tags[n][i] = &tagMember{}
			}
			v := value
			if m[3] == "Key" {
				tags[n][i].key = &v
			} else {
				tags[n][i].value = &v
			}
		default:
			return q, ec2Invalid("UnsupportedOperation", "Parameter "+key+" is not supported by local "+action+"; filters, pagination, IPv6 and IPAM are not implemented.")
		}
	}
	switch action {
	case "CreateVpc":
		if q.cidr == "" {
			return q, ec2Invalid("MissingParameter", "The request must contain the parameter cidrBlock.")
		}
		prefix, e := netip.ParsePrefix(q.cidr)
		if e != nil || !prefix.Addr().Is4() {
			return q, ec2Invalid("InvalidParameterValue", "Value ("+q.cidr+") for parameter cidrBlock is invalid. This is not a valid IPv4 CIDR block.")
		}
		if prefix.Bits() < 16 || prefix.Bits() > 28 {
			return q, ec2Invalid("InvalidVpc.Range", "The CIDR '"+q.cidr+"' is invalid. Use a netmask from /16 to /28.")
		}
		// Host bits are cleared, as the CloudFormation path does.
		q.cidr = prefix.Masked().String()
		for n := range tags {
			if _, ok := specs[n]; !ok {
				return q, ec2Invalid("InvalidParameterValue", "Each tag specification requires ResourceType vpc.")
			}
		}
		if len(specs) > 1 {
			return q, ec2Invalid("UnsupportedOperation", "Only one tag specification is supported locally.")
		}
		raw := []any{}
		for n, kind := range specs {
			if n != 1 || kind != "vpc" {
				return q, ec2Invalid("InvalidParameterValue", "CreateVpc tag specifications must use TagSpecification.1 with ResourceType vpc.")
			}
			for i := 1; i <= len(tags[n]); i++ {
				member := tags[n][i]
				if member == nil || member.key == nil {
					return q, ec2Invalid("InvalidParameterValue", "Tags must be numbered from 1 without gaps and each tag requires a Key.")
				}
				value := ""
				if member.value != nil {
					value = *member.value
				}
				raw = append(raw, map[string]any{"Key": *member.key, "Value": value})
			}
		}
		parsed, e := networkTags(raw)
		if e != nil {
			return q, ec2Invalid("InvalidParameterValue", "Tags allow up to 50 unique keys of 1-128 characters, values up to 256 characters, and no aws: prefix.")
		}
		q.tags = parsed
	case "DeleteVpc":
		if q.vpcID == "" {
			return q, ec2Invalid("MissingParameter", "The request must contain the parameter vpcId.")
		}
		if !ec2VPCIDPattern.MatchString(q.vpcID) {
			return q, ec2Invalid("InvalidVpcID.Malformed", "Invalid id: \""+q.vpcID+"\" (expecting \"vpc-...\").")
		}
	}
	return q, nil
}

func (g *Gateway) failEC2Mutation(w http.ResponseWriter, e error) {
	var failure *ec2Failure
	if errors.As(e, &failure) {
		ec2Error(w, failure.code, failure.message, failure.status)
		return
	}
	ec2Error(w, "InternalError", "Local EC2 network state could not be changed; nothing was committed.", 500)
}

type ec2CreateVpcResponse struct {
	XMLName   xml.Name   `xml:"CreateVpcResponse"`
	XMLNS     string     `xml:"xmlns,attr"`
	RequestID string     `xml:"requestId"`
	VPC       ec2VPCRead `xml:"vpc"`
}
type ec2DeleteVpcResponse struct {
	XMLName   xml.Name `xml:"DeleteVpcResponse"`
	XMLNS     string   `xml:"xmlns,attr"`
	RequestID string   `xml:"requestId"`
	Return    bool     `xml:"return"`
}

func stampEC2Response(out any) {
	id := fmt.Sprintf("ec2-local-%016x", sequence.Add(1))
	const ns = "http://ec2.amazonaws.com/doc/2016-11-15/"
	switch v := out.(type) {
	case *ec2ReadResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2DescribeRouteTablesResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2DescribeSecurityGroupsResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2NativeSecurityGroupMutationResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2NativeSGRuleMutationResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2InternetGatewayResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2NetworkACLResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2NativeDefaultRouteResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2NativeRouteTableMutationResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2NativeRouteAssociationResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2VPCAttributeResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2VPCTagResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2CreateVpcResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2DeleteVpcResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2CreateSubnetResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2DeleteSubnetResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2ModifySubnetResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2DescribeSubnetsResponse:
		v.XMLNS, v.RequestID = ns, id
	}
}

func ec2VPCRecord(v EC2NetworkResource) ec2VPCRead {
	return ec2VPCRead{v.ID, v.CIDR, ec2ReadTags(v.Tags), ec2ManagedByEC2}
}

// cloudFormationClaims counts stack ownership claims on a physical VPC ID. An
// EC2-owned VPC with a claim is inconsistent state and fails closed.
func cloudFormationClaims(ctx context.Context, tx *sql.Tx, region, id string) (int, error) {
	var claims int
	e := tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=?`, stackResourceNamespace(region, "AWS::EC2::VPC"), "AWS::EC2::VPC", id).Scan(&claims)
	return claims, e
}

func nativeVPCs(ctx context.Context, tx *sql.Tx, region string) ([]EC2NetworkResource, error) {
	all, e := networkList(ctx, tx, region, ec2NativeOwner)
	if e != nil {
		return nil, e
	}
	out := []EC2NetworkResource{}
	associatedSubnets := map[string]bool{}
	var hasGateways bool
	var declared map[string]bool
	for _, v := range all {
		if v.Kind == ec2RTGateway || v.Kind == ec2RTAttachment || v.Kind == ec2Route ||
			strings.HasPrefix(v.ID, nativeInternetGatewayLinkPrefix) || strings.HasPrefix(v.ID, nativeDefaultRoutePrefix) {
			hasGateways = true
		}
	}
	if hasGateways {
		_, declared, e = regionalGatewayStacks(ctx, tx, region)
		if e != nil {
			return nil, e
		}
		if err := checkNativeDefaultRouteReservation(ctx, tx, region); err != nil {
			return nil, err
		}
		links, err := readNativeInternetGatewayLinks(ctx, tx, region, all, declared)
		if err != nil {
			return nil, err
		}
		if _, err := nativeIPv4Routes(ctx, tx, region, all, declared, links); err != nil {
			return nil, err
		}
	}
	for _, v := range all {
		switch v.Kind {
		case "AWS::EC2::VPC":
			claims, err := cloudFormationClaims(ctx, tx, region, v.ID)
			if err != nil {
				return nil, err
			}
			if claims != 0 {
				return nil, fmt.Errorf("EC2-owned VPC has a stack claim")
			}
			if err := verifyNativeVPCMainSlot(ctx, tx, v, &declared); err != nil {
				return nil, err
			}
			out = append(out, v)
		case ec2NativeSubnetKind:
			if _, err := nativeSubnetReadback(ctx, tx, v); err != nil {
				return nil, err
			}
		case ec2SecurityGroupKind:
			if _, err := readEC2NativeSecurityGroup(ctx, tx, region, v.ID); err != nil {
				return nil, err
			}
		case ec2NetworkACLKind:
			if _, err := readNativeNetworkACL(ctx, tx, region, v.ID); err != nil {
				return nil, err
			}
		case ec2RT:
			if _, err := readNativeRouteTableInSnapshot(ctx, tx, region, v.ID, &declared); err != nil {
				return nil, err
			}
		case ec2RTGateway, ec2RTAttachment, ec2Route:
			// Validated as one owned graph before this inventory is exposed.
		case ec2Assoc:
			if associatedSubnets[v.SubnetID] {
				return nil, errEC2NativeRouteAssociationState
			}
			if err := checkNativeRouteAssociation(ctx, tx, v); err != nil {
				return nil, err
			}
			associatedSubnets[v.SubnetID] = true
		default:
			return nil, fmt.Errorf("unexpected EC2-owned network kind")
		}
	}
	return out, nil
}

func newVPCID() (string, error) {
	raw := make([]byte, 9)
	if _, e := rand.Read(raw); e != nil {
		return "", e
	}
	return "vpc-" + hex.EncodeToString(raw)[:17], nil
}

func (s *StackStore) createNativeVPC(ctx context.Context, region, cidr string, tags map[string]string) (EC2NetworkResource, error) {
	id, e := newVPCID()
	if e != nil {
		return EC2NetworkResource{}, e
	}
	r := EC2NetworkResource{ID: id, Kind: "AWS::EC2::VPC", Region: region, StackID: ec2NativeOwner, CIDR: cidr, DNSSupport: true, Tags: tags, Revision: 1,
		MainRouteTableID: nativeMainTableID(region, id)}
	if r.Tags == nil {
		r.Tags = map[string]string{}
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return r, e
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_network_resources`).Scan(&count); e != nil {
		return r, e
	}
	if count > ec2NativeRouteTableRowLimit-2 {
		return r, ec2Invalid("VpcLimitExceeded", "The local limit of 1000 network resources has been reached.")
	}
	if e = createEC2Network(ctx, tx, r); e != nil {
		return r, e
	}
	if claims, e := cloudFormationClaims(ctx, tx, region, id); e != nil || claims != 0 {
		return r, fmt.Errorf("new VPC identity is already claimed")
	}
	if e = createNativeVPCMainTable(ctx, tx, r); e != nil {
		return r, e
	}
	return r, tx.Commit()
}

func (s *StackStore) deleteNativeVPC(ctx context.Context, region, id string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v, e := networkRead(ctx, tx, region, id)
	if errors.Is(e, sql.ErrNoRows) {
		return ec2Invalid("InvalidVpcID.NotFound", "The vpc ID '"+id+"' does not exist")
	}
	if e != nil {
		return e
	}
	if v.Kind != "AWS::EC2::VPC" {
		return ec2Invalid("InvalidVpcID.NotFound", "The vpc ID '"+id+"' does not exist")
	}
	if v.StackID != ec2NativeOwner {
		return ec2Invalid("UnsupportedOperation", "The vpc '"+id+"' is managed by CloudFormation. Delete its stack instead.")
	}
	claims, e := cloudFormationClaims(ctx, tx, region, id)
	if e != nil {
		return e
	}
	if claims != 0 {
		return fmt.Errorf("EC2-owned VPC has a stack claim")
	}
	if e := verifyNativeVPCMainSlot(ctx, tx, v, nil); e != nil {
		return e
	}
	hasDependents, e := nativeVPCRetainedDependents(ctx, tx, v, true)
	if e != nil {
		return e
	}
	if hasDependents {
		return ec2Invalid("DependencyViolation", "The vpc '"+id+"' has dependencies and cannot be deleted.")
	}
	if e := removeNativeVPCMainTable(ctx, tx, v); e != nil {
		return e
	}
	result, e := tx.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources WHERE region=? AND id=? AND stack_id=? AND revision=?`, region, id, ec2NativeOwner, v.Revision)
	if e != nil {
		return e
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		return fmt.Errorf("VPC delete was not acknowledged")
	}
	if _, e := networkRead(ctx, tx, region, id); !errors.Is(e, sql.ErrNoRows) {
		return fmt.Errorf("VPC delete readback was not acknowledged")
	}
	if e := verifyNativeVPCMainRetired(ctx, tx, v); e != nil {
		return e
	}
	return tx.Commit()
}

// Use the same bounded typed snapshot before delete and for final readback.
// A raw JSON path lookup would recognize fewer stored field spellings than
// the Go resource readers. Validate the whole snapshot even on a match.
func nativeVPCRetainedDependents(ctx context.Context, tx *sql.Tx, v EC2NetworkResource, ignoreExactMain bool) (bool, error) {
	rows, e := tx.QueryContext(ctx, `SELECT id,document,kind,stack_id,revision FROM aws_ec2_network_resources
		WHERE region=? AND id<>? ORDER BY id LIMIT ?`, v.Region, v.ID, ec2NativeRouteTableRowLimit+1)
	if e != nil {
		return false, e
	}
	defer rows.Close()
	var hasDependents bool
	count := 0
	for rows.Next() {
		var child, raw, kind, owner string
		var revision int64
		var other EC2NetworkResource
		if e = rows.Scan(&child, &raw, &kind, &owner, &revision); e != nil {
			return false, e
		}
		count++
		if count > ec2NativeRouteTableRowLimit || json.Unmarshal([]byte(raw), &other) != nil || other.ID != child ||
			other.Kind != kind || other.Region != v.Region || other.StackID != owner || other.Revision != revision ||
			(other.MainRouteTableID != "" && (kind != ec2RTVPC || owner != ec2NativeOwner)) ||
			(other.MainRouteRepair != nil && !validNativeVPCMainRepair(other)) ||
			(other.NativeDNSUpdates != 0 && !validNativeDNSGeneration(other)) ||
			(strings.HasPrefix(child, nativeInternetGatewayLinkPrefix) || owner == ec2NativeOwner && kind == ec2RTAttachment) &&
				!validNativeInternetGatewayLink(other) ||
			(strings.HasPrefix(child, nativeDefaultRoutePrefix) || owner == ec2NativeOwner && kind == ec2Route) && !validNativeIPv4Route(other) {
			return false, errEC2InternetGatewayState
		}
		if !ignoreExactMain || child != v.MainRouteTableID {
			hasDependents = hasDependents || other.VPCID == v.ID ||
				v.MainRouteTableID != "" && other.RouteTableID == v.MainRouteTableID
		}
	}
	return hasDependents, rows.Err()
}
