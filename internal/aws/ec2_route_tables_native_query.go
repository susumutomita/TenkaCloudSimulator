package aws

import (
	"context"
	"encoding/xml"
	"maps"
	"regexp"
	"slices"
	"strconv"
)

var ec2NativeRouteTableMutations = []string{"CreateRouteTable", "DeleteRouteTable"}

var (
	ec2NativeRouteTableID    = regexp.MustCompile(`^rtb-(?:[0-9a-f]{8}|[0-9a-f]{17})$`)
	ec2NativeRouteTableToken = regexp.MustCompile(`^[ -~]{1,64}$`)
)

type ec2NativeRouteTableQuery struct {
	vpc, id, clientToken string
	tags                 map[string]string

	// Bound to the admitted principal by the gateway, and used on both new
	// and replayed IDs. A wildcard permit cannot conceal a concrete denial.
	authorizeCreatedID func(string) error
}

func (q ec2NativeRouteTableQuery) permissions(action, region string) [][2]string {
	if action == "CreateRouteTable" {
		return append([][2]string{{"ec2:CreateRouteTable", networkARN(region, "vpc", q.vpc)}}, q.createPermissions(region, "*")...)
	}
	return [][2]string{{"ec2:DeleteRouteTable", networkARN(region, "route-table", q.id)}}
}

func (q ec2NativeRouteTableQuery) createPermissions(region, id string) [][2]string {
	arn := networkARN(region, "route-table", id)
	out := [][2]string{{"ec2:CreateRouteTable", arn}}
	if len(q.tags) != 0 {
		out = append(out, [2]string{"ec2:CreateTags", arn})
	}
	return out
}

func parseEC2NativeRouteTableMutation(action string, fields map[string][]string) (ec2NativeRouteTableQuery, *ec2Failure) {
	var q ec2NativeRouteTableQuery
	if !slices.Contains(ec2NativeRouteTableMutations, action) {
		return q, ec2Invalid("UnsupportedOperation", "Only detached native route table create and delete are supported here.")
	}
	members := map[int]map[string]any{}
	var tagSpec bool
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
				return q, ec2Invalid("UnsupportedOperation", "Only DryRun=false is supported for local route table mutations.")
			}
			continue
		}
		if action == "DeleteRouteTable" && key == "RouteTableId" {
			q.id = v
			continue
		}
		if action == "CreateRouteTable" {
			switch key {
			case "VpcId":
				q.vpc = v
				continue
			case "ClientToken":
				if !ec2NativeRouteTableToken.MatchString(v) {
					return q, ec2Invalid("InvalidParameterValue", "ClientToken must be 1–64 printable ASCII characters.")
				}
				q.clientToken = v
				continue
			case "TagSpecification.1.ResourceType":
				if v != "route-table" {
					return q, ec2Invalid("InvalidParameterValue", "TagSpecification.1.ResourceType must be route-table.")
				}
				tagSpec = true
				continue
			}
			if m := ec2TagSpecMember.FindStringSubmatch(key); m != nil && m[1] == "1" {
				if !ec2SecurityGroupXMLText(v) {
					return q, ec2Invalid("InvalidParameterValue", "Tags must be valid XML text.")
				}
				n, _ := strconv.Atoi(m[2])
				if members[n] == nil {
					members[n] = map[string]any{}
				}
				members[n][m[3]] = v
				continue
			}
		}
		return q, ec2Invalid("UnsupportedOperation", "Parameter "+key+" is not supported by local "+action+".")
	}
	if action == "DeleteRouteTable" {
		if q.id == "" {
			return q, ec2Invalid("MissingParameter", "RouteTableId is required.")
		}
		if !ec2NativeRouteTableID.MatchString(q.id) {
			return q, ec2Invalid("InvalidRouteTableID.Malformed", "RouteTableId must be an rtb- ID.")
		}
		return q, nil
	}
	if q.vpc == "" {
		return q, ec2Invalid("MissingParameter", "VpcId is required.")
	}
	if !ec2VPCIDPattern.MatchString(q.vpc) {
		return q, ec2Invalid("InvalidVpcID.Malformed", "VpcId must be a local vpc- ID.")
	}
	if len(members) != 0 && !tagSpec {
		return q, ec2Invalid("InvalidParameterValue", "Tags require TagSpecification.1.ResourceType=route-table.")
	}
	tags := make([]any, 0, len(members))
	for i := 1; i <= len(members); i++ {
		m := members[i]
		if m["Key"] == nil {
			return q, ec2Invalid("InvalidParameterValue", "Tags must be numbered from 1 without gaps and each requires a Key.")
		}
		if _, ok := m["Value"]; !ok {
			m["Value"] = ""
		}
		tags = append(tags, m)
	}
	parsed, err := networkTags(tags)
	if err != nil {
		return q, ec2Invalid("InvalidParameterValue", "Expected at most 50 unique non-reserved tags within local size limits.")
	}
	q.tags = parsed
	return q, nil
}

type ec2NativeRouteTableMutationResponse struct {
	XMLName     xml.Name
	XMLNS       string             `xml:"xmlns,attr"`
	RequestID   string             `xml:"requestId"`
	Table       *ec2RouteTableRead `xml:"routeTable,omitempty"`
	ClientToken string             `xml:"clientToken,omitempty"`
	Return      *bool              `xml:"return,omitempty"`
}

func ec2NativeRouteTableRecord(r EC2NetworkResource) ec2RouteTableRead {
	table := ec2RouteTableRead{ID: r.ID, VPC: r.VPCID, Owner: "000000000000", Tags: ec2ReadTags(r.Tags)}
	// This is saved local configuration, not a data-plane observation.
	if r.CIDR != "" {
		table.Routes = []ec2RouteRead{{CIDR: r.CIDR, Gateway: "local", Origin: "CreateRouteTable", State: "active"}}
	}
	if isNativeMainTable(r) {
		table.Associations = []ec2RouteAssociationRead{{ID: nativeMainAssociationID(r.Region, r.VPCID), Table: r.ID, Main: true,
			State: &ec2NativeRouteAssociationStatus{State: "associated"}}}
	}
	return table
}

func (s *StackStore) executeEC2NativeRouteTableMutation(ctx context.Context, action, region string, q ec2NativeRouteTableQuery) (*ec2NativeRouteTableMutationResponse, error) {
	out := &ec2NativeRouteTableMutationResponse{XMLName: xml.Name{Local: action + "Response"}}
	switch action {
	case "CreateRouteTable":
		r, err := s.createNativeRouteTable(ctx, region, q)
		if err != nil {
			return nil, err
		}
		table := ec2NativeRouteTableRecord(r)
		out.Table, out.ClientToken = &table, q.clientToken
	case "DeleteRouteTable":
		if err := s.deleteNativeRouteTable(ctx, region, q.id); err != nil {
			return nil, err
		}
		ok := true
		out.Return = &ok
	default:
		return nil, ec2Invalid("UnsupportedOperation", "Only detached native route table create and delete are supported here.")
	}
	return out, nil
}
