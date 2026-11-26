package aws

import (
	"context"
	"encoding/xml"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var ec2NativeSecurityGroupMutations = []string{"CreateSecurityGroup", "DeleteSecurityGroup"}

var (
	ec2NativeSecurityGroupID = regexp.MustCompile(`^sg-(?:[0-9a-f]{8}|[0-9a-f]{17})$`)
	ec2NativeSGQueryID       = regexp.MustCompile(`^GroupId\.([1-9][0-9]?)$`)
	ec2NativeSGQueryVPC      = regexp.MustCompile(`^Filter\.1\.Value\.([1-9][0-9]?)$`)
)

type ec2NativeSecurityGroupQuery struct {
	name, description, vpc, id string
	tags                       map[string]string
	ids, vpcs                  []string
	// The gateway binds trusted identity-policy checks before SQL. A create
	// rechecks the assigned ID so a wildcard Allow cannot hide a concrete Deny.
	authorizeCreatedID func(string) error
}

func (q ec2NativeSecurityGroupQuery) permissions(action, region string) [][2]string {
	switch action {
	case "CreateSecurityGroup":
		vpc := [][2]string{{"ec2:CreateSecurityGroup", networkARN(region, "vpc", q.vpc)}}
		return append(vpc, q.createPermissions(region, "*")...)
	case "DeleteSecurityGroup":
		return [][2]string{{"ec2:DeleteSecurityGroup", networkARN(region, "security-group", q.id)}}
	default:
		return [][2]string{{"ec2:DescribeSecurityGroups", "*"}}
	}
}

func (q ec2NativeSecurityGroupQuery) createPermissions(region, id string) [][2]string {
	arn := networkARN(region, "security-group", id)
	out := [][2]string{{"ec2:CreateSecurityGroup", arn}}
	if len(q.tags) != 0 {
		out = append(out, [2]string{"ec2:CreateTags", arn})
	}
	return out
}

func validNativeSGText(s string) bool {
	if len(s) < 1 || len(s) > 255 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			strings.ContainsRune(" ._-:/()#,@[]+=&;{}!$*", r) {
			continue
		}
		return false
	}
	return true
}

func invalidNativeSGName(name string) *ec2Failure {
	if strings.EqualFold(name, "default") {
		return ec2Invalid("InvalidGroup.Reserved", "The default security group name is reserved.")
	}
	if !validNativeSGText(name) || strings.HasPrefix(strings.ToLower(name), "sg-") {
		return ec2Invalid("InvalidParameterValue", "GroupName must be a valid nonempty EC2 group name, at most 255 characters and not starting with sg-.")
	}
	return nil
}

func parseEC2NativeSGQuery(action string, fields map[string][]string) (ec2NativeSecurityGroupQuery, *ec2Failure) {
	var q ec2NativeSecurityGroupQuery
	ids, vpcs := map[int]string{}, map[int]string{}
	tagMembers := map[int]map[string]any{}
	var tagSpec, hasVPCFilter bool
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		values := fields[key]
		if len(values) != 1 {
			return q, ec2Invalid("InvalidParameterValue", "Parameter "+key+" must be sent once.")
		}
		v := values[0]
		switch key {
		case "Action", "Version":
			continue
		case "DryRun":
			if v != "false" {
				return q, ec2Invalid("UnsupportedOperation", "Only DryRun=false is supported for local security group operations.")
			}
			continue
		}
		switch action {
		case "CreateSecurityGroup":
			switch key {
			case "GroupName":
				if len(v) > 255 {
					return q, ec2Invalid("InvalidParameterValue", "GroupName cannot exceed 255 characters.")
				}
				q.name = strings.TrimRight(v, " ")
				continue
			case "GroupDescription":
				q.description = v
				continue
			case "VpcId":
				q.vpc = v
				continue
			case "TagSpecification.1.ResourceType":
				if v != "security-group" {
					return q, ec2Invalid("InvalidParameterValue", "TagSpecification.1.ResourceType must be security-group.")
				}
				tagSpec = true
				continue
			}
			if m := ec2TagSpecMember.FindStringSubmatch(key); m != nil && m[1] == "1" {
				n, _ := strconv.Atoi(m[2])
				if tagMembers[n] == nil {
					tagMembers[n] = map[string]any{}
				}
				if !ec2SecurityGroupXMLText(v) {
					return q, ec2Invalid("InvalidParameterValue", "Tags must be valid XML text.")
				}
				tagMembers[n][m[3]] = v
				continue
			}
		case "DeleteSecurityGroup":
			if key == "GroupId" {
				q.id = v
				continue
			}
		case "DescribeSecurityGroups":
			if key == "Filter.1.Name" {
				if v != "vpc-id" {
					return q, ec2Invalid("UnsupportedOperation", "Only the vpc-id security group filter is supported.")
				}
				hasVPCFilter = true
				continue
			}
			if m := ec2NativeSGQueryID.FindStringSubmatch(key); m != nil {
				n, _ := strconv.Atoi(m[1])
				ids[n] = v
				continue
			}
			if m := ec2NativeSGQueryVPC.FindStringSubmatch(key); m != nil {
				n, _ := strconv.Atoi(m[1])
				vpcs[n] = v
				continue
			}
		}
		return q, ec2Invalid("UnsupportedOperation", "Parameter "+key+" is not supported by local "+action+".")
	}
	switch action {
	case "CreateSecurityGroup":
		if q.name == "" || q.description == "" || q.vpc == "" {
			return q, ec2Invalid("MissingParameter", "GroupName, GroupDescription and an explicit VpcId are required.")
		}
		if e := invalidNativeSGName(q.name); e != nil {
			return q, e
		}
		if !validNativeSGText(q.description) {
			return q, ec2Invalid("InvalidParameterValue", "GroupDescription must be valid EC2 text of 1–255 characters.")
		}
		if !ec2VPCIDPattern.MatchString(q.vpc) {
			return q, ec2Invalid("InvalidVpcID.Malformed", "VpcId must be a local vpc- ID.")
		}
		if !tagSpec && len(tagMembers) != 0 {
			return q, ec2Invalid("InvalidParameterValue", "Tags require TagSpecification.1.ResourceType=security-group.")
		}
		raw := make([]any, 0, len(tagMembers))
		for n := 1; n <= len(tagMembers); n++ {
			member := tagMembers[n]
			if member["Key"] == nil {
				return q, ec2Invalid("InvalidParameterValue", "Tags must be numbered from 1 without gaps and each requires a Key.")
			}
			if _, ok := member["Value"]; !ok {
				member["Value"] = ""
			}
			raw = append(raw, member)
		}
		tags, err := networkTags(raw)
		if err != nil {
			return q, ec2Invalid("InvalidParameterValue", "Expected at most 50 unique non-reserved tags within local size limits.")
		}
		q.tags = tags
	case "DeleteSecurityGroup":
		if q.id == "" {
			return q, ec2Invalid("MissingParameter", "GroupId is required; deletion by name is unsupported.")
		}
		if !ec2NativeSecurityGroupID.MatchString(q.id) {
			return q, ec2Invalid("InvalidGroupId.Malformed", "GroupId must be an sg- ID.")
		}
	case "DescribeSecurityGroups":
		groupIDs, failure := nativeSGIndexedQueryValues(ids, ec2NativeSecurityGroupID, "GroupId", "InvalidGroupId.Malformed")
		if failure != nil {
			return q, failure
		}
		q.ids = groupIDs
		if hasVPCFilter != (len(vpcs) > 0) {
			return q, ec2Invalid("InvalidParameterValue", "The vpc-id filter requires a name and at least one value.")
		}
		filter, failure := nativeSGIndexedQueryValues(vpcs, ec2VPCIDPattern, "vpc-id", "InvalidVpcID.Malformed")
		if failure != nil {
			return q, failure
		}
		q.vpcs = filter
	default:
		return q, ec2Invalid("UnsupportedOperation", "Only bounded security group create, describe and delete are supported.")
	}
	return q, nil
}

func nativeSGIndexedQueryValues(input map[int]string, re *regexp.Regexp, member, malformed string) ([]string, *ec2Failure) {
	if len(input) > 50 {
		return nil, ec2Invalid("InvalidParameterValue", member+" accepts at most 50 values.")
	}
	values := make([]string, 0, len(input))
	seen := make(map[string]bool, len(input))
	for n := 1; n <= len(input); n++ {
		v, ok := input[n]
		if !ok || seen[v] {
			return nil, ec2Invalid("InvalidParameterValue", member+" indices must be dense without duplicate values.")
		}
		if !re.MatchString(v) {
			return nil, ec2Invalid(malformed, member+" contains a malformed ID.")
		}
		seen[v] = true
		values = append(values, v)
	}
	slices.Sort(values)
	return values, nil
}

func (q ec2NativeSecurityGroupQuery) filterSecurityGroups(rows []ec2SecurityGroupRead) ([]ec2SecurityGroupRead, error) {
	out := make([]ec2SecurityGroupRead, 0, len(rows))
	found := 0
	for _, row := range rows {
		if len(q.ids) != 0 && !slices.Contains(q.ids, row.ID) {
			continue
		}
		found++
		if len(q.vpcs) == 0 || slices.Contains(q.vpcs, row.VPC) {
			out = append(out, row)
		}
	}
	if len(q.ids) != 0 && found != len(q.ids) {
		return nil, ec2Invalid("InvalidGroup.NotFound", "One or more security groups do not exist in this region.")
	}
	return out, nil
}

type ec2NativeSecurityGroupMutationResponse struct {
	XMLName   xml.Name
	XMLNS     string   `xml:"xmlns,attr"`
	RequestID string   `xml:"requestId"`
	GroupID   string   `xml:"groupId"`
	ARN       string   `xml:"securityGroupArn,omitempty"`
	Return    bool     `xml:"return"`
	Tags      []ec2Tag `xml:"tagSet>item,omitempty"`
}

func (s *StackStore) executeEC2NativeSGMutation(ctx context.Context, action, region string, q ec2NativeSecurityGroupQuery) (*ec2NativeSecurityGroupMutationResponse, error) {
	out := &ec2NativeSecurityGroupMutationResponse{XMLName: xml.Name{Local: action + "Response"}, Return: true}
	switch action {
	case "CreateSecurityGroup":
		g, err := s.createEC2NativeSecurityGroup(ctx, region, q)
		if err != nil {
			return nil, err
		}
		out.GroupID, out.ARN, out.Tags = g.ID, networkARN(region, "security-group", g.ID), ec2ReadTags(g.Tags)
	case "DeleteSecurityGroup":
		if err := s.deleteEC2NativeSecurityGroup(ctx, region, q.id); err != nil {
			return nil, err
		}
		out.GroupID = q.id
	default:
		return nil, ec2Invalid("UnsupportedOperation", "Only bounded security group create and delete are supported.")
	}
	return out, nil
}
