package aws

import (
	"context"
	"encoding/xml"
	"maps"
	"net/netip"
	"slices"
	"strings"
)

const (
	ec2CreateNativeDefaultRoute = "CreateRoute"
	ec2DeleteNativeDefaultRoute = "DeleteRoute"
	ec2NativeDefaultCIDR        = "0.0.0.0/0"
)

var ec2NativeDefaultRouteOperations = []string{ec2CreateNativeDefaultRoute, ec2DeleteNativeDefaultRoute}

type ec2NativeDefaultRouteQuery struct {
	table, gateway string
	dryRun         bool
}

func (q ec2NativeDefaultRouteQuery) permissions(action, region string) [][2]string {
	return [][2]string{{"ec2:" + action, networkARN(region, "route-table", q.table)}}
}

func parseEC2NativeDefaultRoute(action string, fields map[string][]string) (ec2NativeDefaultRouteQuery, *ec2Failure) {
	var q ec2NativeDefaultRouteQuery
	if !slices.Contains(ec2NativeDefaultRouteOperations, action) {
		return q, ec2Invalid("UnsupportedOperation", "Only CreateRoute and DeleteRoute are supported.")
	}
	var destination string
	for _, field := range slices.Sorted(maps.Keys(fields)) {
		values := fields[field]
		if len(values) != 1 {
			return q, ec2Invalid("InvalidParameterValue", "Each route parameter must be sent once.")
		}
		value := values[0]
		switch field {
		case "Action", "Version":
		case "RouteTableId":
			q.table = value
		case "DestinationCidrBlock":
			destination = value
		case "GatewayId":
			if action != ec2CreateNativeDefaultRoute {
				return q, ec2Invalid("UnsupportedOperation", "DeleteRoute uses only the exact destination and route table.")
			}
			q.gateway = value
		case "DryRun":
			if value != "false" && value != "true" {
				return q, ec2Invalid("InvalidParameterValue", "DryRun must be true or false.")
			}
			q.dryRun = value == "true"
		default:
			return q, ec2Invalid("UnsupportedOperation", "Only a native IPv4 default route to a native internet gateway is supported.")
		}
	}
	if q.table == "" || destination == "" || action == ec2CreateNativeDefaultRoute && q.gateway == "" {
		return q, ec2Invalid("MissingParameter", "RouteTableId and DestinationCidrBlock are required. CreateRoute also requires GatewayId.")
	}
	if !ec2NativeRouteTableID.MatchString(q.table) {
		return q, ec2Invalid("InvalidRouteTableId.Malformed", "RouteTableId must be an rtb- ID.")
	}
	prefix, err := netip.ParsePrefix(destination)
	if err != nil || !prefix.Addr().Is4() {
		return q, ec2Invalid("InvalidParameterValue", "DestinationCidrBlock must be an IPv4 CIDR.")
	}
	if prefix.Bits() != 0 {
		return q, ec2Invalid("UnsupportedOperation", "Only the IPv4 default destination is supported.")
	}
	if action == ec2DeleteNativeDefaultRoute && destination != ec2NativeDefaultCIDR {
		return q, ec2Invalid("InvalidRoute.NotFound", "DeleteRoute requires the exact saved destination, 0.0.0.0/0.")
	}
	if action == ec2CreateNativeDefaultRoute && !ec2InternetGatewayID.MatchString(q.gateway) {
		if !strings.HasPrefix(q.gateway, "igw-") {
			return q, ec2Invalid("UnsupportedOperation", "This target is not a native internet gateway.")
		}
		return q, ec2Invalid("InvalidInternetGatewayId.Malformed", "GatewayId must be an igw- ID.")
	}
	return q, nil
}

type ec2NativeDefaultRouteResponse struct {
	XMLName   xml.Name
	XMLNS     string `xml:"xmlns,attr"`
	RequestID string `xml:"requestId"`
	Return    bool   `xml:"return"`
}

func (s *StackStore) executeEC2NativeDefaultRoute(ctx context.Context, action, region string, q ec2NativeDefaultRouteQuery) (*ec2NativeDefaultRouteResponse, error) {
	if q.dryRun {
		return nil, ec2Invalid("DryRunOperation", "The caller has the requested permission; DryRun changed nothing.")
	}
	if err := s.changeNativeDefaultRoute(ctx, region, q, action == ec2CreateNativeDefaultRoute); err != nil {
		return nil, err
	}
	return &ec2NativeDefaultRouteResponse{XMLName: xml.Name{Local: action + "Response"}, Return: true}, nil
}
