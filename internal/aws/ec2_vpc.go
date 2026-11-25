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
)

// Native EC2 lifecycle. CreateVpc and CreateSubnet store rows in the same
// aws_ec2_network_resources table as CloudFormation networks, under an owner
// that can never be a stack ARN; RunInstances (ec2_instance.go) stores a
// launch record below an EC2-owned subnet. Neither side adopts or deletes the
// other's resources: CloudFormation children require a parent of their own
// stack, EC2-owned children require an EC2-owned parent, and the delete
// actions refuse CloudFormation-owned resources.
const (
	ec2NativeOwner             = "ec2:native"
	ec2ManagedByEC2            = "ec2"
	ec2ManagedByCloudFormation = "cloudformation"
)

// EC2Operations is every native action served; the capability document lists it.
var EC2Operations = []string{"DescribeInstances", "DescribeVpcs", "DescribeSubnets", "CreateVpc", "DeleteVpc", "CreateSubnet", "DeleteSubnet", "RunInstances", "TerminateInstances"}

// ec2Members lists the scalar members each mutation admits. DryRun=false and,
// for ec2TaggedResource actions, one tag specification are admitted as well.
var ec2Members = map[string][]string{
	"CreateVpc":          {"CidrBlock", "InstanceTenancy"},
	"DeleteVpc":          {"VpcId"},
	"CreateSubnet":       {"VpcId", "CidrBlock", "AvailabilityZone"},
	"DeleteSubnet":       {"SubnetId"},
	"RunInstances":       {"ImageId", "InstanceType", "MinCount", "MaxCount", "SubnetId", "ClientToken"},
	"TerminateInstances": {"InstanceId.1"},
}
var ec2TaggedResource = map[string]string{"CreateVpc": "vpc", "CreateSubnet": "subnet", "RunInstances": "instance"}

// ec2Unimplemented names what an action refuses, for the error message only.
var ec2Unimplemented = map[string]string{
	"CreateSubnet":       "IPv6, IPAM, Outposts and AvailabilityZoneId are not implemented.",
	"DeleteSubnet":       "only SubnetId is accepted.",
	"RunInstances":       "key pairs, security groups, user data, instance profiles, block devices, network interfaces, placement and launch templates are not implemented.",
	"TerminateInstances": "only InstanceId.1 is accepted, one instance per request.",
}

var (
	ec2VPCIDPattern       = regexp.MustCompile(`^vpc-[0-9a-f]{8,17}$`)
	ec2SubnetIDPattern    = regexp.MustCompile(`^subnet-[0-9a-f]{8,17}$`)
	ec2InstanceIDPattern  = regexp.MustCompile(`^i-(?:[0-9a-f]{8}|[0-9a-f]{17})$`)
	ec2ImageIDPattern     = regexp.MustCompile(`^ami-(?:[0-9a-f]{8}|[0-9a-f]{17})$`)
	ec2ClientTokenPattern = regexp.MustCompile(`^[\x21-\x7e]{1,64}$`)
	ec2TagSpecType        = regexp.MustCompile(`^TagSpecification\.([1-9][0-9]{0,2})\.ResourceType$`)
	ec2TagSpecMember      = regexp.MustCompile(`^TagSpecification\.([1-9][0-9]{0,2})\.Tag\.([1-9][0-9]{0,2})\.(Key|Value)$`)
)

type ec2Failure struct {
	code, message string
	status        int
}

func (f *ec2Failure) Error() string { return f.code + ": " + f.message }

func ec2Invalid(code, message string) *ec2Failure { return &ec2Failure{code, message, 400} }

type ec2Request struct {
	cidr, vpcID, subnetID, zone                    string
	imageID, instanceType, clientToken, instanceID string
	tags                                           map[string]string
}

// permissions are evaluated against the caller's identity policy before any
// state is read or written. Tagging at creation also needs ec2:CreateTags.
func (q ec2Request) permissions(action, region string) [][2]string {
	tagging := func(out [][2]string, kind string) [][2]string {
		if len(q.tags) > 0 {
			out = append(out, [2]string{"ec2:CreateTags", networkARN(region, kind, "*")})
		}
		return out
	}
	switch action {
	case "CreateVpc":
		return tagging([][2]string{{"ec2:CreateVpc", networkARN(region, "vpc", "*")}}, "vpc")
	case "DeleteVpc":
		return [][2]string{{"ec2:DeleteVpc", networkARN(region, "vpc", q.vpcID)}}
	case "CreateSubnet":
		return tagging([][2]string{{"ec2:CreateSubnet", networkARN(region, "vpc", q.vpcID)}, {"ec2:CreateSubnet", networkARN(region, "subnet", "*")}}, "subnet")
	case "DeleteSubnet":
		return [][2]string{{"ec2:DeleteSubnet", networkARN(region, "subnet", q.subnetID)}}
	case "RunInstances":
		// No network interface, volume or security group is created, so none is checked.
		return tagging([][2]string{{"ec2:RunInstances", networkARN(region, "instance", "*")}, {"ec2:RunInstances", "arn:aws:ec2:" + region + "::image/" + q.imageID}, {"ec2:RunInstances", networkARN(region, "subnet", q.subnetID)}}, "instance")
	case "TerminateInstances":
		return [][2]string{{"ec2:TerminateInstances", networkARN(region, "instance", q.instanceID)}}
	}
	return [][2]string{{"ec2:" + action, "*"}}
}

type ec2TagMember struct{ key, value *string }

// ec2RequestTags admits at most TagSpecification.1 for the action's own
// resource type, with tags numbered from 1 without gaps.
func ec2RequestTags(action, kind string, specs map[int]string, tags map[int]map[int]*ec2TagMember) (map[string]string, *ec2Failure) {
	for n := range tags {
		if _, ok := specs[n]; !ok {
			return nil, ec2Invalid("InvalidParameterValue", "Each tag specification requires ResourceType "+kind+".")
		}
	}
	if len(specs) > 1 {
		return nil, ec2Invalid("UnsupportedOperation", "Only one tag specification is supported locally.")
	}
	raw := []any{}
	for n, specified := range specs {
		if n != 1 || specified != kind {
			return nil, ec2Invalid("InvalidParameterValue", action+" tag specifications must use TagSpecification.1 with ResourceType "+kind+".")
		}
		for i := 1; i <= len(tags[n]); i++ {
			member := tags[n][i]
			if member == nil || member.key == nil {
				return nil, ec2Invalid("InvalidParameterValue", "Tags must be numbered from 1 without gaps and each tag requires a Key.")
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
		return nil, ec2Invalid("InvalidParameterValue", "Tags allow up to 50 unique keys of 1-128 characters, values up to 256 characters, and no aws: prefix.")
	}
	return parsed, nil
}

// ec2CIDR parses an IPv4 block and clears host bits, as the CloudFormation
// path does. A netmask outside /16 to /28 fails with rangeCode.
func ec2CIDR(value, rangeCode string) (string, *ec2Failure) {
	if value == "" {
		return "", ec2Invalid("MissingParameter", "The request must contain the parameter cidrBlock.")
	}
	prefix, e := netip.ParsePrefix(value)
	if e != nil || !prefix.Addr().Is4() {
		return "", ec2Invalid("InvalidParameterValue", "Value ("+value+") for parameter cidrBlock is invalid. This is not a valid IPv4 CIDR block.")
	}
	if prefix.Bits() < 16 || prefix.Bits() > 28 {
		return "", ec2Invalid(rangeCode, "The CIDR '"+value+"' is invalid. Use a netmask from /16 to /28.")
	}
	return prefix.Masked().String(), nil
}

// ec2ResourceID requires one well-formed ID. Existence is checked in the store.
func ec2ResourceID(value, parameter, prefix, code string, pattern *regexp.Regexp) *ec2Failure {
	if value == "" {
		return ec2Invalid("MissingParameter", "The request must contain the parameter "+parameter+".")
	}
	if !pattern.MatchString(value) {
		return ec2Invalid(code, "Invalid id: \""+value+"\" (expecting \""+prefix+"-...\").")
	}
	return nil
}

// parseEC2Request admits only documented members of each action. An unknown
// member, including a filter, page token or IPAM/IPv6 option, is rejected
// rather than ignored.
func parseEC2Request(action string, fields map[string][]string) (ec2Request, *ec2Failure) {
	q := ec2Request{}
	members, mutation := ec2Members[action]
	kind := ec2TaggedResource[action]
	scalar := map[string]string{}
	specs := map[int]string{}
	tags := map[int]map[int]*ec2TagMember{}
	for key, values := range fields {
		if len(values) != 1 {
			return q, ec2Invalid("InvalidParameterValue", "Parameter "+key+" must be sent once.")
		}
		value := values[0]
		switch {
		case key == "Action" || key == "Version":
		case key == "DryRun" && mutation:
			if value != "false" {
				return q, ec2Invalid("UnsupportedOperation", "DryRun is not implemented locally; the request was not run.")
			}
		case slices.Contains(members, key):
			scalar[key] = value
		case kind != "" && ec2TagSpecType.MatchString(key):
			n, _ := strconv.Atoi(ec2TagSpecType.FindStringSubmatch(key)[1])
			specs[n] = value
		case kind != "" && ec2TagSpecMember.MatchString(key):
			m := ec2TagSpecMember.FindStringSubmatch(key)
			n, _ := strconv.Atoi(m[1])
			i, _ := strconv.Atoi(m[2])
			if tags[n] == nil {
				tags[n] = map[int]*ec2TagMember{}
			}
			if tags[n][i] == nil {
				tags[n][i] = &ec2TagMember{}
			}
			v := value
			if m[3] == "Key" {
				tags[n][i].key = &v
			} else {
				tags[n][i].value = &v
			}
		default:
			hint := ec2Unimplemented[action]
			if hint == "" {
				hint = "filters, pagination, IPv6 and IPAM are not implemented."
			}
			return q, ec2Invalid("UnsupportedOperation", "Parameter "+key+" is not supported by local "+action+"; "+hint)
		}
	}
	var failure *ec2Failure
	switch action {
	case "CreateVpc":
		if tenancy, ok := scalar["InstanceTenancy"]; ok && tenancy != "default" {
			return q, ec2Invalid("UnsupportedOperation", "Only default instance tenancy is supported locally.")
		}
		if q.cidr, failure = ec2CIDR(scalar["CidrBlock"], "InvalidVpc.Range"); failure != nil {
			return q, failure
		}
	case "DeleteVpc":
		q.vpcID = scalar["VpcId"]
		return q, ec2ResourceID(q.vpcID, "vpcId", "vpc", "InvalidVpcID.Malformed", ec2VPCIDPattern)
	case "CreateSubnet":
		q.vpcID, q.zone = scalar["VpcId"], scalar["AvailabilityZone"]
		if _, sent := scalar["AvailabilityZone"]; sent && q.zone == "" {
			return q, ec2Invalid("InvalidParameterValue", "Value () for parameter availabilityZone is invalid. Omit the parameter to use the default zone.")
		}
		if failure = ec2ResourceID(q.vpcID, "vpcId", "vpc", "InvalidVpcID.Malformed", ec2VPCIDPattern); failure != nil {
			return q, failure
		}
		if q.cidr, failure = ec2CIDR(scalar["CidrBlock"], "InvalidSubnet.Range"); failure != nil {
			return q, failure
		}
	case "DeleteSubnet":
		q.subnetID = scalar["SubnetId"]
		return q, ec2ResourceID(q.subnetID, "subnetId", "subnet", "InvalidSubnetID.Malformed", ec2SubnetIDPattern)
	case "RunInstances":
		q.imageID, q.instanceType, q.subnetID, q.clientToken = scalar["ImageId"], scalar["InstanceType"], scalar["SubnetId"], scalar["ClientToken"]
		if failure = ec2ResourceID(q.imageID, "ImageId", "ami", "InvalidAMIID.Malformed", ec2ImageIDPattern); failure != nil {
			return q, failure
		}
		for _, count := range []string{"MinCount", "MaxCount"} {
			value, ok := scalar[count]
			if !ok {
				return q, ec2Invalid("MissingParameter", "The request must contain the parameter "+count+".")
			}
			if value != "1" {
				return q, ec2Invalid("UnsupportedOperation", "Local RunInstances records exactly one instance; send MinCount=1 and MaxCount=1.")
			}
		}
		if q.instanceType == "" {
			return q, ec2Invalid("MissingParameter", "The request must contain the parameter InstanceType; the AWS default type is not available locally.")
		}
		if q.instanceType != "t3.micro" && q.instanceType != "t3.small" {
			return q, ec2Invalid("InvalidParameterValue", "Invalid value '"+q.instanceType+"' for InstanceType. Only t3.micro and t3.small are recorded locally.")
		}
		if failure = ec2ResourceID(q.subnetID, "SubnetId", "subnet", "InvalidSubnetID.Malformed", ec2SubnetIDPattern); failure != nil {
			return q, failure
		}
		if _, ok := scalar["ClientToken"]; ok && !ec2ClientTokenPattern.MatchString(q.clientToken) {
			return q, ec2Invalid("InvalidParameterValue", "ClientToken must be 1-64 printable ASCII characters without spaces.")
		}
	case "TerminateInstances":
		q.instanceID = scalar["InstanceId.1"]
		return q, ec2ResourceID(q.instanceID, "InstanceId", "i", "InvalidInstanceID.Malformed", ec2InstanceIDPattern)
	}
	if kind != "" {
		q.tags, failure = ec2RequestTags(action, kind, specs, tags)
	}
	if failure != nil {
		return q, failure
	}
	return q, nil
}

func (g *Gateway) failEC2Mutation(w http.ResponseWriter, e error) {
	var failure *ec2Failure
	if errors.As(e, &failure) {
		ec2Error(w, failure.code, failure.message, failure.status)
		return
	}
	ec2Error(w, "InternalError", "Local EC2 state could not be changed; nothing was committed.", 500)
}

type ec2CreateVpcResponse struct {
	XMLName   xml.Name   `xml:"CreateVpcResponse"`
	XMLNS     string     `xml:"xmlns,attr"`
	RequestID string     `xml:"requestId"`
	VPC       ec2VPCRead `xml:"vpc"`
}

// ec2ReturnResponse is the DeleteVpc and DeleteSubnet result.
type ec2ReturnResponse struct {
	XMLName   xml.Name
	XMLNS     string `xml:"xmlns,attr"`
	RequestID string `xml:"requestId"`
	Return    bool   `xml:"return"`
}

func stampEC2Response(out any) {
	id := fmt.Sprintf("ec2-local-%016x", sequence.Add(1))
	const ns = "http://ec2.amazonaws.com/doc/2016-11-15/"
	switch v := out.(type) {
	case *ec2ReadResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2CreateVpcResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2ReturnResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2CreateSubnetResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2RunInstancesResponse:
		v.XMLNS, v.RequestID = ns, id
	case *ec2TerminateInstancesResponse:
		v.XMLNS, v.RequestID = ns, id
	}
}

func ec2VPCRecord(v EC2NetworkResource) ec2VPCRead {
	return ec2VPCRead{v.ID, v.CIDR, ec2ReadTags(v.Tags), ec2ManagedByEC2}
}

// cloudFormationClaims counts stack ownership claims on a physical ID of one
// resource type. An EC2-owned resource with a claim is inconsistent state and
// fails closed.
func cloudFormationClaims(ctx context.Context, tx *sql.Tx, region, kind, id string) (int, error) {
	var claims int
	e := tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=?`, stackResourceNamespace(region, kind), kind, id).Scan(&claims)
	return claims, e
}

// nativeNetworks returns the region's EC2-owned VPCs and subnets. Any other
// EC2-owned kind, a stack claim, or a subnet outside an EC2-owned VPC is
// inconsistent state and fails closed.
func nativeNetworks(ctx context.Context, tx *sql.Tx, region string) (vpcs, subnets []EC2NetworkResource, e error) {
	all, e := networkList(ctx, tx, region, ec2NativeOwner)
	if e != nil {
		return nil, nil, e
	}
	parents := map[string]bool{}
	for _, v := range all {
		if v.Kind != "AWS::EC2::VPC" && v.Kind != "AWS::EC2::Subnet" {
			return nil, nil, fmt.Errorf("unexpected EC2-owned network kind")
		}
		claims, e := cloudFormationClaims(ctx, tx, region, v.Kind, v.ID)
		if e != nil {
			return nil, nil, e
		}
		if claims != 0 {
			return nil, nil, fmt.Errorf("EC2-owned network resource has a stack claim")
		}
		if v.Kind == "AWS::EC2::VPC" {
			parents[v.ID] = true
			vpcs = append(vpcs, v)
		} else {
			subnets = append(subnets, v)
		}
	}
	for _, subnet := range subnets {
		if !parents[subnet.VPCID] {
			return nil, nil, fmt.Errorf("EC2-owned subnet has no EC2-owned VPC")
		}
	}
	return vpcs, subnets, nil
}

// newEC2ID returns prefix plus 17 random hex digits, the current AWS ID length.
func newEC2ID(prefix string) (string, error) {
	raw := make([]byte, 9)
	if _, e := rand.Read(raw); e != nil {
		return "", e
	}
	return prefix + hex.EncodeToString(raw)[:17], nil
}

func (s *StackStore) createNativeVPC(ctx context.Context, region, cidr string, tags map[string]string) (EC2NetworkResource, error) {
	id, e := newEC2ID("vpc-")
	if e != nil {
		return EC2NetworkResource{}, e
	}
	r := EC2NetworkResource{ID: id, Kind: "AWS::EC2::VPC", Region: region, StackID: ec2NativeOwner, CIDR: cidr, DNSSupport: true, Tags: tags, Revision: 1}
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
	if count >= 1000 {
		return r, ec2Invalid("VpcLimitExceeded", "The local limit of 1000 network resources has been reached.")
	}
	if e = createEC2Network(ctx, tx, r); e != nil {
		return r, e
	}
	if claims, e := cloudFormationClaims(ctx, tx, region, r.Kind, id); e != nil || claims != 0 {
		return r, fmt.Errorf("new VPC identity is already claimed")
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
	claims, e := cloudFormationClaims(ctx, tx, region, v.Kind, id)
	if e != nil {
		return e
	}
	if claims != 0 {
		return fmt.Errorf("EC2-owned VPC has a stack claim")
	}
	rows, e := tx.QueryContext(ctx, `SELECT document FROM aws_ec2_network_resources WHERE region=? AND id<>? ORDER BY id LIMIT 1001`, region, id)
	if e != nil {
		return e
	}
	dependents := 0
	for rows.Next() {
		var raw string
		var other EC2NetworkResource
		if e = rows.Scan(&raw); e != nil {
			rows.Close()
			return e
		}
		if e = json.Unmarshal([]byte(raw), &other); e != nil {
			rows.Close()
			return e
		}
		if other.VPCID == id {
			dependents++
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if dependents != 0 {
		return ec2Invalid("DependencyViolation", "The vpc '"+id+"' has dependencies and cannot be deleted.")
	}
	result, e := tx.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources WHERE region=? AND id=? AND stack_id=? AND revision=?`, region, id, ec2NativeOwner, v.Revision)
	if e != nil {
		return e
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		return fmt.Errorf("VPC delete was not acknowledged")
	}
	return tx.Commit()
}
