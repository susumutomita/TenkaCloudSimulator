package aws

import (
	"context"
	"encoding/xml"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

var ec2NetworkACLOperations = []string{
	"CreateNetworkAcl", "DescribeNetworkAcls", "DeleteNetworkAcl",
	ec2CreateNetworkACLEntry, ec2ReplaceNetworkACLEntry, ec2DeleteNetworkACLEntry, ec2ReplaceNetworkACLAssociation,
}

var (
	ec2NetworkACLIDPattern    = regexp.MustCompile(`^acl-(?:[0-9a-f]{8}|[0-9a-f]{17})$`)
	ec2NetworkACLTokenPattern = regexp.MustCompile(`^[ -~]{1,64}$`)
	ec2NetworkACLIDParam      = regexp.MustCompile(`^NetworkAclId\.([1-9][0-9]?)$`)
	ec2NetworkACLVPCParam     = regexp.MustCompile(`^Filter\.1\.Value\.([1-9][0-9]?)$`)
)

type ec2NetworkACLRequest struct {
	vpcID, aclID, clientToken string
	tags                      map[string]string
	ids, filterVPCs           []string
	filterSubnet              string
	entry                     ec2NetworkACLEntry
	// Bound by the gateway for a non-admin caller after the initial IAM check.
	// Recheck the actual generated/replayed resource before using its state.
	authorizeCreateID func(string) error
}

// These checks run before reading replay receipts or network state. Create
// needs authority over both the new ACL resource and the actual parent VPC.
func (q ec2NetworkACLRequest) permissions(action, region string) [][2]string {
	switch action {
	case "CreateNetworkAcl":
		vpc := [][2]string{{"ec2:CreateNetworkAcl", networkARN(region, "vpc", q.vpcID)}}
		return append(vpc, q.aclCreatePermissions(region, "*")...)
	case "DeleteNetworkAcl", ec2CreateNetworkACLEntry, ec2ReplaceNetworkACLEntry, ec2DeleteNetworkACLEntry:
		return [][2]string{{"ec2:" + action, networkARN(region, "network-acl", q.aclID)}}
	default:
		return [][2]string{{"ec2:DescribeNetworkAcls", "*"}}
	}
}

func (q ec2NetworkACLRequest) aclCreatePermissions(region, id string) [][2]string {
	acl := networkARN(region, "network-acl", id)
	out := [][2]string{{"ec2:CreateNetworkAcl", acl}}
	if len(q.tags) > 0 {
		out = append(out, [2]string{"ec2:CreateTags", acl})
	}
	return out
}

func parseEC2NetworkACLRequest(action string, fields map[string][]string) (ec2NetworkACLRequest, *ec2Failure) {
	q := ec2NetworkACLRequest{tags: map[string]string{}}
	ids, vpcs := map[int]string{}, map[int]string{}
	tags := map[int]map[string]any{}
	var tagSpec bool
	var filterName string
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		values := fields[key]
		if len(values) != 1 {
			return q, ec2Invalid("InvalidParameterValue", "Parameter "+key+" must be sent once.")
		}
		value := values[0]
		switch key {
		case "Action", "Version":
			continue
		case "DryRun":
			if value != "false" {
				return q, ec2Invalid("UnsupportedOperation", "Only DryRun=false is supported by local network ACLs.")
			}
			continue
		}
		switch action {
		case "CreateNetworkAcl":
			switch key {
			case "VpcId":
				q.vpcID = value
				continue
			case "ClientToken":
				if !ec2NetworkACLTokenPattern.MatchString(value) {
					return q, ec2Invalid("InvalidParameterValue", "ClientToken must be 1–64 printable ASCII characters.")
				}
				q.clientToken = value
				continue
			case "TagSpecification.1.ResourceType":
				if value != "network-acl" {
					return q, ec2Invalid("InvalidParameterValue", "TagSpecification.1.ResourceType must be network-acl.")
				}
				tagSpec = true
				continue
			}
			if m := ec2TagSpecMember.FindStringSubmatch(key); m != nil && m[1] == "1" {
				n, _ := strconv.Atoi(m[2])
				if tags[n] == nil {
					tags[n] = map[string]any{}
				}
				if !validNetworkACLTagText(value) {
					return q, ec2Invalid("InvalidParameterValue", "Tags must be valid XML text.")
				}
				tags[n][m[3]] = value
				continue
			}
		case "DescribeNetworkAcls":
			if key == "Filter.1.Name" {
				if value != "vpc-id" && value != "association.subnet-id" {
					return q, ec2Invalid("UnsupportedOperation", "Only the vpc-id and association.subnet-id network ACL filters are supported.")
				}
				filterName = value
				continue
			}
			if m := ec2NetworkACLIDParam.FindStringSubmatch(key); m != nil {
				n, _ := strconv.Atoi(m[1])
				ids[n] = value
				continue
			}
			if m := ec2NetworkACLVPCParam.FindStringSubmatch(key); m != nil {
				n, _ := strconv.Atoi(m[1])
				vpcs[n] = value
				continue
			}
		case "DeleteNetworkAcl", ec2CreateNetworkACLEntry, ec2ReplaceNetworkACLEntry, ec2DeleteNetworkACLEntry:
			if key == "NetworkAclId" {
				q.aclID = value
				continue
			}
			if networkACLEntryParameter(action, key) {
				continue
			}
		}
		return q, ec2Invalid("UnsupportedOperation", "Parameter "+key+" is not supported by local "+action+".")
	}
	switch action {
	case "CreateNetworkAcl":
		if q.vpcID == "" {
			return q, ec2Invalid("MissingParameter", "VpcId is required.")
		}
		if !ec2VPCIDPattern.MatchString(q.vpcID) {
			return q, ec2Invalid("InvalidVpcID.Malformed", "VpcId must be a local vpc- ID.")
		}
		if !tagSpec && len(tags) > 0 {
			return q, ec2Invalid("InvalidParameterValue", "Tags require TagSpecification.1.ResourceType=network-acl.")
		}
		raw := make([]any, 0, len(tags))
		for i := 1; i <= len(tags); i++ {
			tag := tags[i]
			if tag["Key"] == nil {
				return q, ec2Invalid("InvalidParameterValue", "Tags must be numbered from 1 without gaps and each requires a Key.")
			}
			if _, ok := tag["Value"]; !ok {
				tag["Value"] = ""
			}
			raw = append(raw, tag)
		}
		parsed, err := networkTags(raw)
		if err != nil {
			return q, ec2Invalid("InvalidParameterValue", "Expected at most 50 unique non-reserved tags within local size limits.")
		}
		q.tags = parsed
	case "DescribeNetworkAcls":
		parsed, f := networkACLIndexedValues(ids, ec2NetworkACLIDPattern, "NetworkAclId", "InvalidNetworkAclId.Malformed")
		if f != nil {
			return q, f
		}
		q.ids = parsed
		if (filterName != "") != (len(vpcs) > 0) {
			return q, ec2Invalid("InvalidParameterValue", "A network ACL filter requires a name and at least one value.")
		}
		if filterName == "association.subnet-id" {
			if len(vpcs) != 1 || !ec2SubnetIDPattern.MatchString(vpcs[1]) {
				return q, ec2Invalid("InvalidParameterValue", "The association.subnet-id filter requires one native subnet ID.")
			}
			q.filterSubnet = vpcs[1]
		} else {
			parsed, f = networkACLIndexedValues(vpcs, ec2VPCIDPattern, "vpc-id", "InvalidVpcID.Malformed")
			if f != nil {
				return q, f
			}
			q.filterVPCs = parsed
		}
	case "DeleteNetworkAcl", ec2CreateNetworkACLEntry, ec2ReplaceNetworkACLEntry, ec2DeleteNetworkACLEntry:
		if q.aclID == "" {
			return q, ec2Invalid("MissingParameter", "NetworkAclId is required.")
		}
		if !ec2NetworkACLIDPattern.MatchString(q.aclID) {
			return q, ec2Invalid("InvalidNetworkAclId.Malformed", "NetworkAclId must be an acl- ID.")
		}
		if action != "DeleteNetworkAcl" {
			entry, f := parseNetworkACLEntry(action, fields)
			q.entry = entry
			return q, f
		}
	default:
		return q, ec2Invalid("UnsupportedOperation", "Only detached network ACL and IPv4 entry configuration is supported.")
	}
	return q, nil
}

func validNetworkACLTagText(s string) bool {
	return utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool {
		return (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == 0xfffe || r == 0xffff
	}) == -1
}

func networkACLIndexedValues(items map[int]string, shape *regexp.Regexp, name, malformed string) ([]string, *ec2Failure) {
	if len(items) > 50 {
		return nil, ec2Invalid("InvalidParameterValue", name+" allows at most 50 values.")
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for i := 1; i <= len(items); i++ {
		value, ok := items[i]
		if !ok || seen[value] {
			return nil, ec2Invalid("InvalidParameterValue", name+" must be numbered from 1 without gaps or duplicate values.")
		}
		if !shape.MatchString(value) {
			return nil, ec2Invalid(malformed, name+" contains a malformed ID.")
		}
		seen[value] = true
		out = append(out, value)
	}
	slices.Sort(out)
	return out, nil
}

type ec2NetworkACLResponse struct {
	XMLName     xml.Name
	XMLNS       string             `xml:"xmlns,attr"`
	RequestID   string             `xml:"requestId"`
	NetworkACL  *ec2NetworkACLRead `xml:"networkAcl,omitempty"`
	NetworkACLs *ec2NetworkACLSet  `xml:"networkAclSet,omitempty"`
	ClientToken string             `xml:"clientToken,omitempty"`
	Return      *bool              `xml:"return,omitempty"`
}

type ec2NetworkACLSet struct {
	Items []ec2NetworkACLRead `xml:"item"`
}

type ec2NetworkACLRead struct {
	ID           string                      `xml:"networkAclId"`
	VPCID        string                      `xml:"vpcId"`
	OwnerID      string                      `xml:"ownerId"`
	Default      bool                        `xml:"default"`
	Entries      []ec2NetworkACLEntry        `xml:"entrySet>item"`
	Associations ec2NetworkACLAssociationSet `xml:"associationSet"`
	Tags         []ec2Tag                    `xml:"tagSet>item"`
	Mode         string                      `xml:"simulatorNetworkAclMode"`
}

type ec2NetworkACLAssociationSet struct {
	Items []ec2NetworkACLAssociationRead `xml:"item"`
}

type ec2NetworkACLEntry struct {
	Rule      int                     `xml:"ruleNumber"`
	Protocol  string                  `xml:"protocol"`
	Action    string                  `xml:"ruleAction"`
	Egress    bool                    `xml:"egress"`
	CIDR      string                  `xml:"cidrBlock"`
	PortRange *ec2NetworkACLPortRange `xml:"portRange,omitempty" json:",omitempty"`
}

func ec2NetworkACLRecord(r EC2NetworkResource) ec2NetworkACLRead {
	// Verified read snapshots add the current association projection.
	// Neither a detached nor an attached configuration filters packets.
	return ec2NetworkACLRead{
		ID: r.ID, VPCID: r.VPCID, OwnerID: "000000000000", Mode: "detached-config-only",
		Entries: networkACLRecordEntries(r.NativeACLRules),
		Tags:    ec2ReadTags(r.Tags),
	}
}

func (s *StackStore) executeEC2NetworkACL(ctx context.Context, action, region string, q ec2NetworkACLRequest) (*ec2NetworkACLResponse, error) {
	out := &ec2NetworkACLResponse{XMLName: xml.Name{Local: action + "Response"}}
	switch action {
	case "CreateNetworkAcl":
		r, err := s.createNativeNetworkACL(ctx, region, q)
		if err != nil {
			return nil, err
		}
		out.NetworkACL, out.ClientToken = &r.record, q.clientToken
	case "DescribeNetworkAcls":
		rows, err := s.describeNativeNetworkACLs(ctx, region, q)
		if err != nil {
			return nil, err
		}
		out.NetworkACLs = &ec2NetworkACLSet{Items: rows}
	case "DeleteNetworkAcl":
		if err := s.deleteNativeNetworkACL(ctx, region, q.aclID); err != nil {
			return nil, err
		}
		ok := true
		out.Return = &ok
	case ec2CreateNetworkACLEntry, ec2ReplaceNetworkACLEntry, ec2DeleteNetworkACLEntry:
		if err := s.mutateNativeNetworkACLEntry(ctx, action, region, q.aclID, q.entry); err != nil {
			return nil, err
		}
		ok := true
		out.Return = &ok
	default:
		return nil, ec2Invalid("UnsupportedOperation", "Only detached network ACL and IPv4 entry configuration is supported.")
	}
	return out, nil
}
