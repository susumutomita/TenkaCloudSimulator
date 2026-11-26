package aws

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
)

// ValidateRDSPrivatePostgresTopology verifies references in the authorized RDS
// write transaction. It reads only the current provider's native EC2 state. It
// does not grant ingress, reserve network/DB resources, or create a listener.
func ValidateRDSPrivatePostgresTopology(ctx context.Context, tx *sql.Tx, refs catalog.RDSPrivatePostgresRefs) error {
	if ctx == nil || tx == nil || ctx.Err() != nil || len(refs.SubnetIDs) < 2 || len(refs.SubnetIDs) > 20 ||
		len(refs.SecurityGroupIDs) < 1 || len(refs.SecurityGroupIDs) > 5 {
		return privatePostgresNetworkFault()
	}
	subnets := make([]EC2NetworkResource, 0, len(refs.SubnetIDs))
	zones := make(map[string]bool, len(refs.SubnetIDs))
	seen := make(map[string]bool, len(refs.SubnetIDs)+len(refs.SecurityGroupIDs))
	var owner, vpcID string
	for _, id := range refs.SubnetIDs {
		subnet, err := networkRead(ctx, tx, refs.Region, id)
		if err != nil {
			return privatePostgresReadFault(err)
		}
		if subnet.Kind != "AWS::EC2::Subnet" || subnet.StackID == "" || subnet.VPCID == "" || subnet.PublicIP ||
			subnet.Revision <= 0 || seen[id] || len(subnet.AvailabilityZone) != len(refs.Region)+1 ||
			!strings.HasPrefix(subnet.AvailabilityZone, refs.Region) || subnet.AvailabilityZone[len(refs.Region)] < 'a' || subnet.AvailabilityZone[len(refs.Region)] > 'z' {
			return privatePostgresNetworkFault()
		}
		if owner == "" {
			owner, vpcID = subnet.StackID, subnet.VPCID
		}
		if owner != subnet.StackID || vpcID != subnet.VPCID {
			return privatePostgresNetworkFault()
		}
		seen[id], zones[subnet.AvailabilityZone] = true, true
		subnets = append(subnets, subnet)
	}
	if len(zones) < 2 {
		return privatePostgresNetworkFault()
	}
	vpc, err := networkRead(ctx, tx, refs.Region, vpcID)
	if err != nil {
		return privatePostgresReadFault(err)
	}
	space, err := netip.ParsePrefix(vpc.CIDR)
	if err != nil || vpc.Kind != "AWS::EC2::VPC" || vpc.StackID != owner || vpc.Revision <= 0 || !space.Addr().Is4() || space != space.Masked() {
		return privatePostgresNetworkFault()
	}
	for _, subnet := range subnets {
		block, err := netip.ParsePrefix(subnet.CIDR)
		if err != nil || !block.Addr().Is4() || block != block.Masked() || block.Bits() < space.Bits() || !space.Contains(block.Addr()) {
			return privatePostgresNetworkFault()
		}
	}
	for _, id := range refs.SecurityGroupIDs {
		group, err := networkRead(ctx, tx, refs.Region, id)
		if err != nil {
			return privatePostgresReadFault(err)
		}
		if group.Kind != "AWS::EC2::SecurityGroup" || group.StackID != owner || group.VPCID != vpcID || group.Revision <= 0 || seen[id] {
			return privatePostgresNetworkFault()
		}
		seen[id] = true
	}
	// This first private slice intentionally admits only an isolated VPC. The
	// subnet's no-public-IP setting alone doesn't rule out a public route.
	rows, err := networkList(ctx, tx, refs.Region, owner)
	if err != nil {
		return err
	}
	for _, resource := range rows {
		if resource.VPCID == vpcID && (resource.Kind == "AWS::EC2::VPCGatewayAttachment" ||
			resource.Kind == "AWS::EC2::Route" && resource.RouteEnabled) {
			return privatePostgresNetworkFault()
		}
	}
	return ctx.Err()
}

func privatePostgresReadFault(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return privatePostgresNetworkFault()
	}
	return err // The catalog's wire error mapper hides internal read details.
}

func privatePostgresNetworkFault() error {
	return catalog.Failure("InvalidVPCNetworkStateFault", "Private PostgreSQL metadata requires native subnets in at least two Availability Zones and security groups in one isolated VPC of the same owner.")
}
