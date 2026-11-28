package aws

import (
	"context"
	"database/sql"
	"errors"
	"slices"
)

var errELBPlacement = errors.New("inconsistent Application Load Balancer placement")

// resolveELBPlacement runs in the same transaction as the authorized create.
// The simulator has no default group, so a new ALB needs an explicit native
// group. An identical retry of a historical load balancer never enters here.
func resolveELBPlacement(ctx context.Context, tx *sql.Tx, lb *ELBLoadBalancer) error {
	if len(lb.Subnets) < 2 {
		return elbConfig("An application load balancer needs at least two subnets in distinct Availability Zones.")
	}
	lb.VPCID = ""
	lb.SubnetZones = make(map[string]string, len(lb.Subnets))
	zones := make(map[string]bool, len(lb.Subnets))
	for _, id := range lb.Subnets {
		r, err := networkRead(ctx, tx, lb.Region, id)
		if errors.Is(err, sql.ErrNoRows) || err == nil && r.Kind != ec2NativeSubnetKind {
			return elbErr("SubnetNotFound", "A subnet does not exist in this region.")
		}
		if err != nil {
			return err
		}
		if r.StackID != ec2NativeOwner {
			return elbErr("InvalidSubnet", "Placement requires EC2-managed subnets.")
		}
		subnet, err := nativeSubnetReadback(ctx, tx, r)
		if err != nil {
			return err
		}
		if lb.VPCID != "" && lb.VPCID != subnet.VPC || zones[subnet.AZ] {
			return elbErr("InvalidSubnet", "Use one subnet per Availability Zone in a single VPC.")
		}
		lb.VPCID = subnet.VPC
		lb.SubnetZones[id] = subnet.AZ
		zones[subnet.AZ] = true
	}
	if len(lb.SecurityGroups) == 0 {
		return elbErr("InvalidSecurityGroup", "Specify an EC2-managed security group. Local VPCs have no default security group.")
	}
	for _, id := range lb.SecurityGroups {
		group, err := readEC2NativeSecurityGroup(ctx, tx, lb.Region, id)
		var invalid *ec2Failure
		if errors.As(err, &invalid) && (invalid.code == "InvalidGroup.NotFound" || invalid.code == "UnsupportedOperation") || err == nil && group.VPCID != lb.VPCID {
			return elbErr("InvalidSecurityGroup", "A security group does not belong to the selected native VPC.")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Before native placement, ALBs stored only the submitted subnet/group strings.
// Such rows have no verified VPC or child references, even if those IDs later exist.
func checkELBPlacementRefs(ctx context.Context, tx *sql.Tx, lb ELBLoadBalancer) error {
	ids, err := elbStrings(ctx, tx, "SELECT resource_id FROM aws_elb_placement_refs WHERE alb=? AND region=? ORDER BY resource_id", lb.ARN, lb.Region)
	if err != nil {
		return err
	}
	if lb.VPCID == "" {
		if len(ids) != 0 || len(lb.SubnetZones) != 0 {
			return errELBPlacement
		}
		return nil
	}
	if !ec2VPCIDPattern.MatchString(lb.VPCID) || len(lb.SubnetZones) != len(lb.Subnets) || len(lb.Subnets) < 2 || len(lb.SecurityGroups) == 0 {
		return errELBPlacement
	}
	seen := make(map[string]bool, len(lb.Subnets))
	for _, id := range lb.Subnets {
		az := lb.SubnetZones[id]
		if !nativeSubnetAZ(lb.Region, az) || seen[az] {
			return errELBPlacement
		}
		seen[az] = true
	}
	want := append(slices.Clone(lb.Subnets), lb.SecurityGroups...)
	if !elbSameSet(ids, want) {
		return errELBPlacement
	}
	return nil
}
