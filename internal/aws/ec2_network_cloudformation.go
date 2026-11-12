package aws

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

var networkCFNFields = map[string][]string{
	"AWS::EC2::VPC":                         {"CidrBlock", "EnableDnsHostnames", "EnableDnsSupport", "Tags"},
	"AWS::EC2::Subnet":                      {"VpcId", "CidrBlock", "AvailabilityZone", "MapPublicIpOnLaunch", "Tags"},
	"AWS::EC2::InternetGateway":             {"Tags"},
	"AWS::EC2::VPCGatewayAttachment":        {"VpcId", "InternetGatewayId"},
	"AWS::EC2::RouteTable":                  {"VpcId", "Tags"},
	"AWS::EC2::Route":                       {"RouteTableId", "DestinationCidrBlock", "GatewayId"},
	"AWS::EC2::SubnetRouteTableAssociation": {"RouteTableId", "SubnetId"},
	"AWS::EC2::SecurityGroup":               {"VpcId", "GroupDescription", "SecurityGroupIngress", "Tags"},
}

func stackNetworkKind(kind string) bool { _, ok := networkCFNFields[kind]; return ok }

// Bounded documented CFN GetAtt fields. Unimplemented attributes stay rejected.
func stackNetworkAttribute(kind, field string) bool {
	fields := map[string][]string{
		"AWS::EC2::VPC":                         {"VpcId", "CidrBlock"},
		"AWS::EC2::Subnet":                      {"SubnetId", "VpcId", "AvailabilityZone"},
		"AWS::EC2::InternetGateway":             {"InternetGatewayId"},
		"AWS::EC2::RouteTable":                  {"RouteTableId"},
		"AWS::EC2::SecurityGroup":               {"GroupId", "VpcId"},
		"AWS::EC2::SubnetRouteTableAssociation": {"Id"},
	}
	for _, candidate := range fields[kind] {
		if candidate == field {
			return true
		}
	}
	return false
}
func networkPrefix(raw any, min, max int) (string, error) {
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("CIDR must be a string")
	}
	p, e := netip.ParsePrefix(s)
	if e != nil || !p.Addr().Is4() || p.Bits() < min || p.Bits() > max {
		return "", fmt.Errorf("unsupported IPv4 CIDR")
	}
	return p.Masked().String(), nil
}
func networkBool(p map[string]any, key string, def bool) (bool, error) {
	v, ok := p[key]
	if !ok {
		return def, nil
	}
	if b, ok := v.(bool); ok {
		return b, nil
	}
	if v == "true" {
		return true, nil
	}
	if v == "false" {
		return false, nil
	}
	return false, fmt.Errorf("%s must be Boolean", key)
}
func networkTags(raw any) (map[string]string, error) {
	out := map[string]string{}
	if raw == nil {
		return out, nil
	}
	items, ok := raw.([]any)
	if !ok || len(items) > 50 {
		return nil, fmt.Errorf("invalid network tags")
	}
	for _, item := range items {
		m := object(item)
		key, kok := m["Key"].(string)
		value, vok := m["Value"].(string)
		if len(m) != 2 || !kok || !vok || len(key) < 1 || len(key) > 128 || len(value) > 256 || strings.HasPrefix(strings.ToLower(key), "aws:") {
			return nil, fmt.Errorf("invalid network tag")
		}
		if _, ok = out[key]; ok {
			return nil, fmt.Errorf("duplicate tag")
		}
		out[key] = value
	}
	return out, nil
}
func networkRules(raw any) ([]EC2IngressRule, error) {
	out := []EC2IngressRule{}
	if raw == nil {
		return out, nil
	}
	items, ok := raw.([]any)
	if !ok || len(items) > 60 {
		return nil, fmt.Errorf("invalid ingress rules")
	}
	for _, item := range items {
		p := object(item)
		for k := range p {
			if k != "IpProtocol" && k != "FromPort" && k != "ToPort" && k != "CidrIp" && k != "Description" {
				return nil, fmt.Errorf("unsupported ingress field %s", k)
			}
		}
		if p["IpProtocol"] != "tcp" && p["IpProtocol"] != "6" {
			return nil, fmt.Errorf("only bounded TCP ingress is supported")
		}
		from, e := strconv.Atoi(fmt.Sprint(p["FromPort"]))
		if e != nil {
			return nil, e
		}
		to, e := strconv.Atoi(fmt.Sprint(p["ToPort"]))
		if e != nil || from < 1 || to > 65535 || to < from {
			return nil, fmt.Errorf("invalid TCP port range")
		}
		cidr, e := networkPrefix(p["CidrIp"], 0, 32)
		if e != nil {
			return nil, e
		}
		if desc, exists := p["Description"]; exists {
			str, ok := desc.(string)
			if !ok || len(str) > 255 {
				return nil, fmt.Errorf("invalid ingress description")
			}
		}
		out = append(out, EC2IngressRule{"tcp", from, to, cidr})
	}
	return out, nil
}
func compileStackNetwork(region, stack, logical, kind string, p map[string]any, attrs map[string]string) (string, error) {
	r, e := compileNetworkResource(region, stack, logical, kind, p)
	if e != nil {
		return "", e
	}
	switch kind {
	case "AWS::EC2::VPC":
		attrs[logical+".VpcId"] = r.ID
		attrs[logical+".CidrBlock"] = r.CIDR
	case "AWS::EC2::Subnet":
		attrs[logical+".SubnetId"] = r.ID
		attrs[logical+".VpcId"] = r.VPCID
		attrs[logical+".AvailabilityZone"] = r.AvailabilityZone
	case "AWS::EC2::InternetGateway":
		attrs[logical+".InternetGatewayId"] = r.ID
	case "AWS::EC2::RouteTable":
		attrs[logical+".RouteTableId"] = r.ID
	case "AWS::EC2::SecurityGroup":
		attrs[logical+".GroupId"] = r.ID
		attrs[logical+".VpcId"] = r.VPCID
	case "AWS::EC2::SubnetRouteTableAssociation":
		attrs[logical+".Id"] = r.ID
	}
	return r.ID, nil
}
func compileNetworkResource(region, stack, logical, kind string, p map[string]any) (EC2NetworkResource, error) {
	r := EC2NetworkResource{Kind: kind, Region: region, StackID: stack, Revision: 1}
	fields, ok := networkCFNFields[kind]
	if !ok {
		return r, fmt.Errorf("unsupported network resource")
	}
	for key := range p {
		found := false
		for _, f := range fields {
			if key == f {
				found = true
			}
		}
		if !found {
			return r, fmt.Errorf("unsupported %s property %s", kind, key)
		}
	}
	prefixes := map[string]string{"AWS::EC2::VPC": "vpc", "AWS::EC2::Subnet": "subnet", "AWS::EC2::InternetGateway": "igw", "AWS::EC2::VPCGatewayAttachment": "vpc-gateway-attachment", "AWS::EC2::RouteTable": "rtb", "AWS::EC2::Route": "route", "AWS::EC2::SubnetRouteTableAssociation": "rtbassoc", "AWS::EC2::SecurityGroup": "sg"}
	sum := sha256.Sum256([]byte(stack + "\x00" + logical))
	r.ID = prefixes[kind] + "-" + hex.EncodeToString(sum[:])[:17]
	var e error
	r.Tags, e = networkTags(p["Tags"])
	if e != nil {
		return r, e
	}
	need := func(key string) (string, error) {
		s, ok := p[key].(string)
		if !ok || len(s) == 0 || len(s) > 128 {
			return "", fmt.Errorf("%s requires a bounded ID", key)
		}
		return s, nil
	}
	switch kind {
	case "AWS::EC2::VPC":
		r.CIDR, e = networkPrefix(p["CidrBlock"], 16, 28)
		if e != nil {
			return r, e
		}
		r.DNSHostnames, e = networkBool(p, "EnableDnsHostnames", false)
		if e != nil {
			return r, e
		}
		r.DNSSupport, e = networkBool(p, "EnableDnsSupport", true)
		if e != nil {
			return r, e
		}
		if r.DNSHostnames && !r.DNSSupport {
			return r, fmt.Errorf("DNS hostnames require DNS support")
		}
	case "AWS::EC2::Subnet":
		r.VPCID, e = need("VpcId")
		if e != nil {
			return r, e
		}
		r.CIDR, e = networkPrefix(p["CidrBlock"], 16, 28)
		if e != nil {
			return r, e
		}
		r.PublicIP, e = networkBool(p, "MapPublicIpOnLaunch", false)
		if e != nil {
			return r, e
		}
		r.AvailabilityZone = region + "a"
		if az, ok := p["AvailabilityZone"]; ok {
			str, ok := az.(string)
			if !ok || (str != region+"a" && str != region+"b" && str != region+"c") {
				return r, fmt.Errorf("unsupported local availability zone")
			}
			r.AvailabilityZone = str
		}
	case "AWS::EC2::VPCGatewayAttachment":
		r.VPCID, e = need("VpcId")
		if e != nil {
			return r, e
		}
		r.GatewayID, e = need("InternetGatewayId")
	case "AWS::EC2::RouteTable":
		r.VPCID, e = need("VpcId")
	case "AWS::EC2::Route":
		r.RouteTableID, e = need("RouteTableId")
		if e != nil {
			return r, e
		}
		r.GatewayID, e = need("GatewayId")
		if e != nil {
			return r, e
		}
		r.CIDR, e = networkPrefix(p["DestinationCidrBlock"], 0, 32)
		if e != nil {
			return r, e
		}
		if r.CIDR != "0.0.0.0/0" {
			return r, fmt.Errorf("only explicit default internet route is supported")
		}
		r.RouteEnabled = true
	case "AWS::EC2::SubnetRouteTableAssociation":
		r.SubnetID, e = need("SubnetId")
		if e != nil {
			return r, e
		}
		r.RouteTableID, e = need("RouteTableId")
	case "AWS::EC2::SecurityGroup":
		r.VPCID, e = need("VpcId")
		if e != nil {
			return r, e
		}
		r.Description, ok = p["GroupDescription"].(string)
		if !ok || r.Description == "" || len(r.Description) > 255 {
			return r, fmt.Errorf("invalid security group description")
		}
		r.Ingress, e = networkRules(p["SecurityGroupIngress"])
	}
	return r, e
}
func createStackNetwork(ctx context.Context, tx *sql.Tx, record StackRecord, r StackResource) error {
	state, e := compileNetworkResource(record.Region, record.ID, r.LogicalID, r.Type, r.Properties)
	if e != nil {
		return e
	}
	if state.ID != r.PhysicalID {
		return fmt.Errorf("network physical identity changed")
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace(record.Region, r.Type), r.Type, r.PhysicalID, record.ID); e != nil {
		return e
	}
	return createEC2Network(ctx, tx, state)
}
func deleteStackNetwork(ctx context.Context, tx *sql.Tx, record StackRecord, r StackResource) error {
	state, e := networkRead(ctx, tx, record.Region, r.PhysicalID)
	if e != nil {
		return e
	}
	if state.StackID != record.ID || state.Kind != r.Type {
		return fmt.Errorf("network ownership changed")
	}
	all, e := networkList(ctx, tx, record.Region, record.ID)
	if e != nil {
		return e
	}
	for _, child := range all {
		if child.ID == state.ID {
			continue
		}
		if child.VPCID == state.ID || child.SubnetID == state.ID || child.RouteTableID == state.ID || child.GatewayID == state.ID {
			return fmt.Errorf("network dependent resource remains")
		}
	}
	result, e := tx.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources WHERE region=? AND id=? AND stack_id=?`, record.Region, r.PhysicalID, record.ID)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return fmt.Errorf("network delete was not acknowledged")
	}
	var remaining int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_network_resources WHERE region=? AND id=?`, record.Region, r.PhysicalID).Scan(&remaining); e != nil {
		return e
	}
	if remaining != 0 {
		return fmt.Errorf("network resource remains")
	}
	return nil
}

func networkARN(region, kind, id string) string {
	return "arn:aws:ec2:" + region + ":000000000000:" + kind + "/" + id
}
func stackNetworkPermissions(record StackRecord, r StackResource, deleting bool, authorize func(string, string) error) error {
	actions := map[string][2]string{"AWS::EC2::VPC": {"ec2:CreateVpc", "ec2:DeleteVpc"}, "AWS::EC2::Subnet": {"ec2:CreateSubnet", "ec2:DeleteSubnet"}, "AWS::EC2::InternetGateway": {"ec2:CreateInternetGateway", "ec2:DeleteInternetGateway"}, "AWS::EC2::VPCGatewayAttachment": {"ec2:AttachInternetGateway", "ec2:DetachInternetGateway"}, "AWS::EC2::RouteTable": {"ec2:CreateRouteTable", "ec2:DeleteRouteTable"}, "AWS::EC2::Route": {"ec2:CreateRoute", "ec2:DeleteRoute"}, "AWS::EC2::SubnetRouteTableAssociation": {"ec2:AssociateRouteTable", "ec2:DisassociateRouteTable"}, "AWS::EC2::SecurityGroup": {"ec2:CreateSecurityGroup", "ec2:DeleteSecurityGroup"}}
	kind := map[string]string{"AWS::EC2::VPC": "vpc", "AWS::EC2::Subnet": "subnet", "AWS::EC2::InternetGateway": "internet-gateway", "AWS::EC2::RouteTable": "route-table", "AWS::EC2::SecurityGroup": "security-group"}[r.Type]
	resource := networkARN(record.Region, kind, r.PhysicalID)
	if r.Type == "AWS::EC2::Route" || r.Type == "AWS::EC2::SubnetRouteTableAssociation" {
		resource = networkARN(record.Region, "route-table", stringValue(r.Properties["RouteTableId"]))
	}
	if r.Type == "AWS::EC2::VPCGatewayAttachment" {
		resource = networkARN(record.Region, "internet-gateway", stringValue(r.Properties["InternetGatewayId"]))
	}
	index := 0
	if deleting {
		index = 1
	}
	if e := authorize(actions[r.Type][index], resource); e != nil {
		return e
	}
	// Explicit dependencies are part of resource-level admission, not new grants.
	if vpc := stringValue(r.Properties["VpcId"]); vpc != "" && (!deleting || r.Type == "AWS::EC2::VPCGatewayAttachment") {
		if e := authorize(actions[r.Type][index], networkARN(record.Region, "vpc", vpc)); e != nil {
			return e
		}
	}
	if r.Type == "AWS::EC2::SubnetRouteTableAssociation" {
		if e := authorize(actions[r.Type][index], networkARN(record.Region, "subnet", stringValue(r.Properties["SubnetId"]))); e != nil {
			return e
		}
	}
	if !deleting {
		if r.Type == "AWS::EC2::SecurityGroup" {
			if rules, _ := r.Properties["SecurityGroupIngress"].([]any); len(rules) > 0 {
				if e := authorize("ec2:AuthorizeSecurityGroupIngress", resource); e != nil {
					return e
				}
			}
		}
		if r.Type == "AWS::EC2::VPC" {
			if e := authorize("ec2:ModifyVpcAttribute", resource); e != nil {
				return e
			}
		}
		if r.Type == "AWS::EC2::Subnet" {
			if e := authorize("ec2:ModifySubnetAttribute", resource); e != nil {
				return e
			}
		}
		if tags, _ := r.Properties["Tags"].([]any); len(tags) > 0 {
			if e := authorize("ec2:CreateTags", resource); e != nil {
				return e
			}
		}
	}
	return nil
}
