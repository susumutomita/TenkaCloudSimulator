package aws

import (
	"context"
	"encoding/xml"
	"maps"
	"regexp"
	"slices"
)

const (
	ec2AssociateNativeRouteTable    = "AssociateRouteTable"
	ec2DisassociateNativeRouteTable = "DisassociateRouteTable"
)

var (
	ec2NativeRouteAssociationOperations = []string{ec2AssociateNativeRouteTable, ec2DisassociateNativeRouteTable}
	ec2NativeRouteAssociationID         = regexp.MustCompile(`^rtbassoc-(?:[0-9a-f]{8}|[0-9a-f]{17})$`)
)

type ec2NativeRouteAssociationQuery struct {
	table, subnet, association string
}

// An existing gateway must supply an authorization decision bound to its
// authenticated, effective identity. Missing-decision does not mean admin.
type ec2NativeRouteAssociationAuthority struct {
	check           func(action, resource string) bool
	revealMissingID bool // only for the established local administrator
}

func (a ec2NativeRouteAssociationAuthority) allowsPair(action, region, table, subnet string) bool {
	return a.check != nil &&
		a.check("ec2:"+action, networkARN(region, "route-table", table)) &&
		a.check("ec2:"+action, networkARN(region, "subnet", subnet))
}

func deniedNativeRouteAssociation() *ec2Failure {
	return &ec2Failure{"UnauthorizedOperation", "The selected identity cannot change this route table association.", 403}
}

func (a ec2NativeRouteAssociationAuthority) missingAssociation() *ec2Failure {
	// No synthetic association-resource ARN exists. An ordinary identity has
	// no resolvable exact pair to check for absence, so reveal no distinction.
	if a.check != nil && a.revealMissingID {
		return ec2Invalid("InvalidAssociationID.NotFound", "No native route table association with this ID exists in this region.")
	}
	return deniedNativeRouteAssociation()
}

func parseEC2NativeRouteAssociation(action string, fields map[string][]string) (ec2NativeRouteAssociationQuery, *ec2Failure) {
	var q ec2NativeRouteAssociationQuery
	if !slices.Contains(ec2NativeRouteAssociationOperations, action) {
		return q, ec2Invalid("UnsupportedOperation", "Only explicit native subnet route table association and removal are supported here.")
	}
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		values := fields[key]
		if len(values) != 1 {
			return q, ec2Invalid("InvalidParameterValue", "Each association parameter must be sent once.")
		}
		value := values[0]
		switch {
		case key == "Action" || key == "Version":
			// The common Query gateway already validated the action and version.
		case key == "DryRun" && value == "false":
		case action == ec2AssociateNativeRouteTable && key == "RouteTableId":
			q.table = value
		case action == ec2AssociateNativeRouteTable && key == "SubnetId":
			q.subnet = value
		case action == ec2DisassociateNativeRouteTable && key == "AssociationId":
			q.association = value
		default:
			return q, ec2Invalid("UnsupportedOperation", "Unsupported parameter for this explicit subnet route table association.")
		}
	}
	return q, q.validate(action)
}

func (q ec2NativeRouteAssociationQuery) validate(action string) *ec2Failure {
	switch action {
	case ec2AssociateNativeRouteTable:
		if q.table == "" || q.subnet == "" {
			return ec2Invalid("MissingParameter", "RouteTableId and SubnetId are required.")
		}
		if !ec2NativeRouteTableID.MatchString(q.table) {
			return ec2Invalid("InvalidRouteTableID.Malformed", "RouteTableId must be an rtb- ID.")
		}
		if !ec2SubnetIDPattern.MatchString(q.subnet) || q.association != "" {
			return ec2Invalid("InvalidParameterValue", "Only one explicit SubnetId is accepted as an association target.")
		}
	case ec2DisassociateNativeRouteTable:
		if q.association == "" {
			return ec2Invalid("MissingParameter", "AssociationId is required.")
		}
		if !ec2NativeRouteAssociationID.MatchString(q.association) || q.table != "" || q.subnet != "" {
			return ec2Invalid("InvalidParameterValue", "AssociationId must be an rtbassoc- ID.")
		}
	default:
		return ec2Invalid("UnsupportedOperation", "Unsupported native route table association operation.")
	}
	return nil
}

type ec2NativeRouteAssociationResponse struct {
	XMLName          xml.Name
	XMLNS            string                           `xml:"xmlns,attr"`
	RequestID        string                           `xml:"requestId"`
	AssociationID    string                           `xml:"associationId,omitempty"`
	AssociationState *ec2NativeRouteAssociationStatus `xml:"associationState,omitempty"`
	Return           *bool                            `xml:"return,omitempty"`
}

type ec2NativeRouteAssociationStatus struct {
	State string `xml:"state"`
}

func (s *StackStore) executeEC2NativeRouteAssociation(ctx context.Context, action, region string, q ec2NativeRouteAssociationQuery, a ec2NativeRouteAssociationAuthority) (*ec2NativeRouteAssociationResponse, error) {
	if failure := q.validate(action); failure != nil {
		return nil, failure
	}
	if !slices.Contains(Regions, region) {
		return nil, ec2Invalid("InvalidParameterValue", "Unsupported region.")
	}
	out := &ec2NativeRouteAssociationResponse{XMLName: xml.Name{Local: action + "Response"}}
	if action == ec2AssociateNativeRouteTable {
		r, err := s.associateNativeRouteTable(ctx, region, q, a)
		if err != nil {
			return nil, err
		}
		out.AssociationID = r.ID
		out.AssociationState = &ec2NativeRouteAssociationStatus{State: "associated"}
	} else {
		if err := s.disassociateNativeRouteTable(ctx, region, q.association, a); err != nil {
			return nil, err
		}
		ok := true
		out.Return = &ok
	}
	return out, nil
}
