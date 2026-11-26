package aws

import (
	"context"
	"encoding/xml"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

const (
	ec2CreateInternetGateway   = "CreateInternetGateway"
	ec2DescribeInternetGateway = "DescribeInternetGateways"
	ec2DeleteInternetGateway   = "DeleteInternetGateway"
	ec2InternetGatewayARNKind  = "internet-gateway"
	ec2GatewaySelectorLimit    = 8
)

var ec2InternetGatewayOperations = []string{ec2CreateInternetGateway, ec2DescribeInternetGateway, ec2DeleteInternetGateway}

var (
	ec2InternetGatewayID    = regexp.MustCompile(`^igw-(?:[0-9a-f]{8}|[0-9a-f]{17})$`)
	ec2InternetGatewayParam = regexp.MustCompile(`^InternetGatewayId\.([1-9][0-9]?)$`)
)

type ec2InternetGatewayQuery struct {
	id                 string
	ids                []string
	tags               map[string]string
	authorizeCreatedID func(string) error
	maxResults         int
	nextToken          string
	caller             provider.Actor // trusted request binding for pages only
}

func (q ec2InternetGatewayQuery) paginated() bool { return q.maxResults != 0 || q.nextToken != "" }

func (q ec2InternetGatewayQuery) permissions(action, region string) [][2]string {
	switch action {
	case ec2CreateInternetGateway:
		return q.createPermissions(region, "*")
	case ec2DeleteInternetGateway:
		return [][2]string{{"ec2:DeleteInternetGateway", networkARN(region, ec2InternetGatewayARNKind, q.id)}}
	default:
		return [][2]string{{"ec2:DescribeInternetGateways", "*"}}
	}
}

func (q ec2InternetGatewayQuery) createPermissions(region, id string) [][2]string {
	resource := networkARN(region, ec2InternetGatewayARNKind, id)
	out := [][2]string{{"ec2:CreateInternetGateway", resource}}
	if len(q.tags) > 0 {
		out = append(out, [2]string{"ec2:CreateTags", resource})
	}
	return out
}

func parseEC2InternetGateway(action string, fields map[string][]string) (ec2InternetGatewayQuery, *ec2Failure) {
	q := ec2InternetGatewayQuery{}
	ids := map[int]string{}
	tags := map[int]map[string]any{}
	var tagSpec bool
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
				return q, ec2Invalid("UnsupportedOperation", "Only DryRun=false is supported by local internet gateways.")
			}
			continue
		}
		switch action {
		case ec2CreateInternetGateway:
			if key == "TagSpecification.1.ResourceType" {
				if value != ec2InternetGatewayARNKind {
					return q, ec2Invalid("InvalidParameterValue", "TagSpecification.1.ResourceType must be internet-gateway.")
				}
				tagSpec = true
				continue
			}
			if m := ec2TagSpecMember.FindStringSubmatch(key); m != nil && m[1] == "1" {
				if !ec2SecurityGroupXMLText(value) {
					return q, ec2Invalid("InvalidParameterValue", "Tags must be valid XML text.")
				}
				n, _ := strconv.Atoi(m[2])
				if tags[n] == nil {
					tags[n] = map[string]any{}
				}
				tags[n][m[3]] = value
				continue
			}
		case ec2DescribeInternetGateway:
			switch key {
			case "MaxResults":
				size, failure := parseEC2GatewayMaxResults(value)
				if failure != nil {
					return q, failure
				}
				q.maxResults = size
				continue
			case "NextToken":
				if len(value) != ec2GatewayPageWireLen {
					return q, invalidEC2GatewayPage()
				}
				q.nextToken = value
				continue
			}
			if m := ec2InternetGatewayParam.FindStringSubmatch(key); m != nil {
				n, _ := strconv.Atoi(m[1])
				ids[n] = value
				continue
			}
		case ec2DeleteInternetGateway:
			if key == "InternetGatewayId" {
				q.id = value
				continue
			}
		}
		return q, ec2Invalid("UnsupportedOperation", "Parameter "+key+" is not supported by local "+action+".")
	}

	switch action {
	case ec2CreateInternetGateway:
		if len(tags) > 50 || len(tags) != 0 && !tagSpec {
			return q, ec2Invalid("InvalidParameterValue", "At most 50 tags with ResourceType=internet-gateway are supported.")
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
			return q, ec2Invalid("InvalidParameterValue", "Expected unique non-reserved tags within local size limits.")
		}
		q.tags = parsed
	case ec2DescribeInternetGateway:
		if len(ids) != 0 && q.paginated() {
			return q, ec2Invalid("InvalidParameterCombination", "InternetGatewayId cannot be combined with MaxResults or NextToken.")
		}
		if len(ids) > ec2GatewaySelectorLimit {
			return q, ec2Invalid("InvalidParameterValue", "At most eight internet gateway IDs are supported.")
		}
		q.ids = make([]string, 0, len(ids))
		for i := 1; i <= len(ids); i++ {
			id, found := ids[i]
			if !found || slices.Contains(q.ids, id) {
				return q, ec2Invalid("InvalidParameterValue", "InternetGatewayId must be numbered from 1 without gaps or duplicate IDs.")
			}
			if !ec2InternetGatewayID.MatchString(id) {
				return q, malformedInternetGatewayID()
			}
			q.ids = append(q.ids, id)
		}
	case ec2DeleteInternetGateway:
		if q.id == "" {
			return q, ec2Invalid("MissingParameter", "InternetGatewayId is required.")
		}
		if !ec2InternetGatewayID.MatchString(q.id) {
			return q, malformedInternetGatewayID()
		}
	default:
		return q, ec2Invalid("UnsupportedOperation", "Only creation, discovery and deletion of detached native internet gateways are supported.")
	}
	return q, nil
}

func malformedInternetGatewayID() *ec2Failure {
	return ec2Invalid("InvalidInternetGatewayId.Malformed", "InternetGatewayId must be an igw- ID.")
}

func missingInternetGatewayID() *ec2Failure {
	return ec2Invalid("InvalidInternetGatewayID.NotFound", "No internet gateway with that ID exists in this region.")
}

type ec2InternetGatewayResponse struct {
	XMLName   xml.Name
	XMLNS     string                 `xml:"xmlns,attr"`
	RequestID string                 `xml:"requestId"`
	Gateway   *ec2InternetGateway    `xml:"internetGateway,omitempty"`
	Gateways  *ec2InternetGatewaySet `xml:"internetGatewaySet,omitempty"`
	NextToken string                 `xml:"nextToken,omitempty"`
	Return    *bool                  `xml:"return,omitempty"`
}

type ec2InternetGatewaySet struct {
	Items []ec2InternetGateway `xml:"item"`
}

type ec2InternetGateway struct {
	ID          string                 `xml:"internetGatewayId"`
	OwnerID     string                 `xml:"ownerId"`
	Attachments []ec2GatewayAttachment `xml:"attachmentSet>item"`
	Tags        []ec2Tag               `xml:"tagSet>item"`
	ManagedBy   string                 `xml:"simulatorManagedBy"`
}

type ec2GatewayAttachment struct {
	VPCID string `xml:"vpcId"`
	State string `xml:"state"`
}

func internetGatewayProjection(r EC2NetworkResource, vpcID string) ec2InternetGateway {
	gw := ec2InternetGateway{ID: r.ID, OwnerID: "000000000000", Tags: ec2ReadTags(r.Tags), ManagedBy: ec2ManagedByCloudFormation}
	if r.StackID == ec2NativeOwner {
		gw.ManagedBy = ec2ManagedByEC2
	}
	if vpcID != "" {
		gw.Attachments = []ec2GatewayAttachment{{VPCID: vpcID, State: "available"}}
	}
	return gw
}

func validInternetGatewayRow(r EC2NetworkResource) bool {
	if !ec2InternetGatewayID.MatchString(r.ID) || r.Kind != ec2RTGateway || r.Revision != 1 ||
		r.VPCID != "" || r.SubnetID != "" || r.GatewayID != "" || r.RouteTableID != "" ||
		r.CIDR != "" || r.AvailabilityZone != "" || r.Description != "" || r.DNSHostnames ||
		r.DNSSupport || r.PublicIP || r.RouteEnabled || len(r.Ingress) != 0 || len(r.Tags) > 50 {
		return false
	}
	for key, value := range r.Tags {
		if len(key) < 1 || len(key) > 128 || len(value) > 256 || strings.HasPrefix(strings.ToLower(key), "aws:") ||
			!ec2SecurityGroupXMLText(key) || !ec2SecurityGroupXMLText(value) {
			return false
		}
	}
	return true
}

func (s *StackStore) executeEC2InternetGateway(ctx context.Context, action, region string, q ec2InternetGatewayQuery) (*ec2InternetGatewayResponse, error) {
	out := &ec2InternetGatewayResponse{XMLName: xml.Name{Local: action + "Response"}}
	switch action {
	case ec2CreateInternetGateway:
		row, err := s.createNativeInternetGateway(ctx, region, q)
		if err != nil {
			return nil, err
		}
		gw := internetGatewayProjection(row, "")
		out.Gateway = &gw
	case ec2DescribeInternetGateway:
		gateways, err := s.describeInternetGateways(ctx, region, q.ids)
		if err != nil {
			return nil, err
		}
		page, next, err := s.internetGatewayPages.page(region, q, gateways)
		if err != nil {
			return nil, err
		}
		out.Gateways, out.NextToken = &ec2InternetGatewaySet{Items: page}, next
	case ec2DeleteInternetGateway:
		if err := s.deleteNativeInternetGateway(ctx, region, q.id); err != nil {
			return nil, err
		}
		ok := true
		out.Return = &ok
	default:
		return nil, ec2Invalid("UnsupportedOperation", "Native gateway attachments and routing are not supported.")
	}
	return out, nil
}
