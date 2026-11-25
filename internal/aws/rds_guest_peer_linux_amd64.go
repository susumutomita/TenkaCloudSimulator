//go:build linux && amd64

package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/netip"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
)

// VerifyPrivatePostgresPeer checks whether this retained guest and the one
// locally owned database can be paired under the current provider policy.
// A nil error is an observation, not a connection or later-I/O capability.
// No address, port, SQL, credentials, sockets or packet backend are exposed.
func (s *StackStore) VerifyPrivatePostgresPeer(ctx context.Context, guest *EC2VMProbeLaunch, db *catalog.OwnedPrivatePostgres) error {
	if db == nil {
		return errGuestLease
	}
	return s.verifyCompletedGuest(ctx, guest, func(owner EC2GuestOwner, net EC2NetworkSnapshot, ip netip.Addr) error {
		return db.ProbeGuestPeer(ctx, s.db, owner.Account, owner.Region, func(ctx context.Context, tx *sql.Tx, refs catalog.RDSPrivatePostgresRefs) error {
			return privatePostgresPeerIngress(ctx, tx, net, ip, refs)
		})
	})
}

// Called in the catalog owner's transaction, after its same-generation native
// subnet-group, private VPC and destination SG reference checks. In particular
// inbound rules on the guest's own SG never authorize a database connection.
func privatePostgresPeerIngress(ctx context.Context, tx *sql.Tx, net EC2NetworkSnapshot, ip netip.Addr, refs catalog.RDSPrivatePostgresRefs) error {
	if ctx == nil || ctx.Err() != nil || tx == nil || net.Region != refs.Region || net.PublicRoute || !ip.Is4() || !ip.IsGlobalUnicast() ||
		net.StackID == "" || net.vpcID == "" || len(refs.SubnetIDs) < 2 || len(refs.SubnetIDs) > 20 ||
		len(refs.SecurityGroupIDs) < 1 || len(refs.SecurityGroupIDs) > 5 {
		return errGuestLease
	}
	// The configured catalog validator may add local policy, but a provider
	// pairing must enforce native isolation itself even if that hook is lax.
	if err := ValidateRDSPrivatePostgresTopology(ctx, tx, refs); err != nil {
		return errGuestLease
	}
	var raw []byte
	var stack StackRecord
	err := tx.QueryRowContext(ctx, "SELECT document FROM aws_cfn_stacks WHERE region=? AND id=?", net.Region, net.StackID).Scan(&raw)
	if err != nil || json.Unmarshal(raw, &stack) != nil || stack.Status != "CREATE_COMPLETE" || stack.ID != net.StackID || stack.Region != net.Region {
		return errGuestLease
	}
	// A native row with a plausible StackID is not a typed CFN reservation.
	type key struct{ kind, id string }
	declared := make(map[key]StackResource, len(stack.Resources))
	for _, resource := range stack.Resources {
		k := key{resource.Type, resource.PhysicalID}
		if _, duplicate := declared[k]; duplicate {
			return errGuestLease
		}
		declared[k] = resource
	}
	owned := func(kind, id string) bool {
		_, exists := declared[key{kind, id}]
		var stackID string
		return exists && tx.QueryRowContext(ctx,
			"SELECT stack_id FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=?",
			stackResourceNamespace(net.Region, kind), kind, id).Scan(&stackID) == nil && stackID == stack.ID
	}
	const vpcKind, subnetKind = "AWS::EC2::VPC", "AWS::EC2::Subnet"
	subnet, err := networkRead(ctx, tx, net.Region, net.SubnetID)
	block, parseErr := netip.ParsePrefix(net.SubnetCIDR)
	if err != nil || parseErr != nil || !block.Addr().Is4() || block.Masked().String() != net.SubnetCIDR || !block.Contains(ip) ||
		subnet.Kind != subnetKind || subnet.StackID != net.StackID || subnet.VPCID != net.vpcID || subnet.CIDR != net.SubnetCIDR ||
		!owned(vpcKind, net.vpcID) || !owned(subnetKind, net.SubnetID) {
		return errGuestLease
	}
	for _, id := range refs.SubnetIDs {
		resource, err := networkRead(ctx, tx, net.Region, id)
		if err != nil || resource.StackID != net.StackID || resource.VPCID != net.vpcID || resource.Kind != subnetKind ||
			!owned(subnetKind, id) {
			return errGuestLease
		}
	}
	allowed := false
	for _, id := range refs.SecurityGroupIDs {
		if !owned(ec2SecurityGroupKind, id) {
			return errGuestLease
		}
		resource := declared[key{ec2SecurityGroupKind, id}]
		// Reuse the same bounded, strict native rule reader as EC2 Query.
		g, err := ec2OwnedSecurityGroup(ctx, tx, stack, resource)
		if err != nil || g.VPC != net.vpcID {
			return errGuestLease
		}
		for _, rule := range g.Ingress {
			for _, r := range rule.Ranges {
				prefix, err := netip.ParsePrefix(r.CIDR)
				if err != nil {
					return errGuestLease
				}
				if rule.Protocol == "tcp" && rule.FromPort <= 5432 && rule.ToPort >= 5432 && prefix.Contains(ip) {
					allowed = true
				}
			}
		}
	}
	if !allowed {
		return errGuestLease
	}
	return ctx.Err()
}
