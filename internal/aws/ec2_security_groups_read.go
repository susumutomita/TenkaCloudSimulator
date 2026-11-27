package aws

import (
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"net/netip"
	"unicode/utf8"
)

const ec2SecurityGroupKind = "AWS::EC2::SecurityGroup"

type ec2SecurityGroupIPv4Range struct {
	CIDR string `xml:"cidrIp"`
}

type ec2SecurityGroupPermission struct {
	Protocol string                      `xml:"ipProtocol"`
	FromPort int                         `xml:"fromPort"`
	ToPort   int                         `xml:"toPort"`
	Ranges   []ec2SecurityGroupIPv4Range `xml:"ipRanges>item"`
}

type ec2SecurityGroupPermissionSet struct {
	Items []ec2SecurityGroupPermission `xml:"item"`
}

type ec2SecurityGroupRead struct {
	Owner       string                         `xml:"ownerId"`
	ID          string                         `xml:"groupId"`
	Name        string                         `xml:"groupName,omitempty"`
	Description string                         `xml:"groupDescription"`
	VPC         string                         `xml:"vpcId"`
	Ingress     []ec2SecurityGroupPermission   `xml:"ipPermissions>item"`
	Egress      *ec2SecurityGroupPermissionSet `xml:"ipPermissionsEgress,omitempty"`
	Tags        []ec2Tag                       `xml:"tagSet>item,omitempty"`
	Mode        string                         `xml:"simulatorSecurityGroupMode,omitempty"`
}

type ec2SecurityGroupSet struct {
	Items []ec2SecurityGroupRead `xml:"item"`
}

// Grouped element tags in the shared legacy envelope can emit empty sibling
// collections even for nil slices. The Query operation has only this set.
type ec2DescribeSecurityGroupsResponse struct {
	XMLName   xml.Name            `xml:"DescribeSecurityGroupsResponse"`
	XMLNS     string              `xml:"xmlns,attr"`
	RequestID string              `xml:"requestId"`
	Groups    ec2SecurityGroupSet `xml:"securityGroupInfo"`
}

// encoding/xml silently replaces characters outside the XML 1.0 Char
// production. Reject these local values instead of publishing changed text.
// Tab, LF and CR are allowed; encoding/xml writes their numeric references.
func ec2SecurityGroupXMLText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' ||
			r >= 0x20 && r <= 0xd7ff || r >= 0xe000 && r <= 0xfffd || r >= 0x10000 {
			continue
		}
		return false
	}
	return true
}

// ec2OwnedSecurityGroup runs in the same snapshot as the typed ownership
// lookup. Only the persisted ingress is returned because the trusted provider
// can update it without rewriting the original CloudFormation template.
func ec2OwnedSecurityGroup(ctx context.Context, tx *sql.Tx, stack StackRecord, resource StackResource) (ec2SecurityGroupRead, error) {
	const vpcKind = "AWS::EC2::VPC"
	var out ec2SecurityGroupRead
	g, err := networkRead(ctx, tx, stack.Region, resource.PhysicalID)
	if err != nil || g.Kind != ec2SecurityGroupKind || g.StackID != stack.ID ||
		g.VPCID != stringValue(resource.Properties["VpcId"]) ||
		g.Description != stringValue(resource.Properties["GroupDescription"]) || len(g.Description) == 0 || len(g.Description) > 255 {
		return out, fmt.Errorf("security group identity")
	}
	if _, err := networkParent(ctx, tx, g, g.VPCID, vpcKind); err != nil {
		return out, fmt.Errorf("security group VPC identity")
	}
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT stack_id FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=?`,
		stackResourceNamespace(stack.Region, vpcKind), vpcKind, g.VPCID).Scan(&owner)
	if err != nil || owner != stack.ID {
		return out, fmt.Errorf("security group VPC ownership")
	}
	declarations := 0
	for _, member := range stack.Resources {
		if member.Type == vpcKind && member.PhysicalID == g.VPCID {
			declarations++
		}
	}
	if declarations != 1 || len(g.Ingress) > 60 || len(g.Tags) > 50 {
		return out, fmt.Errorf("security group bounds or parent declaration")
	}
	if !ec2SecurityGroupXMLText(g.ID) || !ec2SecurityGroupXMLText(g.VPCID) || !ec2SecurityGroupXMLText(g.Description) {
		return out, fmt.Errorf("security group data cannot be represented in XML")
	}
	for k, v := range g.Tags {
		if !ec2SecurityGroupXMLText(k) || !ec2SecurityGroupXMLText(v) {
			return out, fmt.Errorf("security group tags cannot be represented in XML")
		}
	}
	out = ec2SecurityGroupRead{
		Owner: "000000000000", ID: g.ID, VPC: g.VPCID, Description: g.Description, Tags: ec2ReadTags(g.Tags),
	}
	for _, rule := range g.Ingress {
		prefix, err := netip.ParsePrefix(rule.CIDR)
		if err != nil || !prefix.Addr().Is4() || prefix.Masked().String() != rule.CIDR ||
			rule.Protocol != "tcp" || rule.FromPort < 1 || rule.ToPort < rule.FromPort || rule.ToPort > 65535 {
			return ec2SecurityGroupRead{}, fmt.Errorf("invalid saved security group ingress")
		}
		out.Ingress = append(out.Ingress, ec2SecurityGroupPermission{
			Protocol: rule.Protocol, FromPort: rule.FromPort, ToPort: rule.ToPort,
			Ranges: []ec2SecurityGroupIPv4Range{{CIDR: rule.CIDR}},
		})
	}
	return out, nil
}
