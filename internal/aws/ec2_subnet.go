package aws

import (
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"net/netip"
)

// Native EC2 subnet lifecycle. A subnet is control-plane configuration below
// an EC2-owned VPC: no packet path, route or address pool is created.
type ec2CreateSubnetResponse struct {
	XMLName   xml.Name      `xml:"CreateSubnetResponse"`
	XMLNS     string        `xml:"xmlns,attr"`
	RequestID string        `xml:"requestId"`
	Subnet    ec2SubnetRead `xml:"subnet"`
}

func ec2SubnetRecord(v EC2NetworkResource, managedBy string) ec2SubnetRead {
	return ec2SubnetRead{ID: v.ID, VPC: v.VPCID, CIDR: v.CIDR, State: "available", Zone: v.AvailabilityZone, Tags: ec2ReadTags(v.Tags), ManagedBy: managedBy}
}

// nativeParent reads an EC2-owned resource of one kind in the request region.
// A CloudFormation-owned resource is refused, never adopted.
func nativeParent(ctx context.Context, tx *sql.Tx, region, id, kind, label, notFound, refusal string) (EC2NetworkResource, error) {
	v, e := networkRead(ctx, tx, region, id)
	if errors.Is(e, sql.ErrNoRows) || (e == nil && v.Kind != kind) {
		return v, ec2Invalid(notFound, "The "+label+" ID '"+id+"' does not exist")
	}
	if e != nil {
		return v, e
	}
	if v.StackID != ec2NativeOwner {
		return v, ec2Invalid("UnsupportedOperation", "The "+label+" '"+id+"' is managed by CloudFormation. "+refusal)
	}
	claims, e := cloudFormationClaims(ctx, tx, region, kind, id)
	if e != nil {
		return v, e
	}
	if claims != 0 {
		return v, fmt.Errorf("EC2-owned %s has a stack claim", label)
	}
	return v, nil
}

func (s *StackStore) createNativeSubnet(ctx context.Context, region string, q ec2Request) (EC2NetworkResource, error) {
	zone := q.zone
	if zone == "" {
		zone = region + "a"
	}
	if zone != region+"a" && zone != region+"b" && zone != region+"c" {
		return EC2NetworkResource{}, ec2Invalid("InvalidParameterValue", "Value ("+q.zone+") for parameter availabilityZone is invalid. Subnets can currently only be created in the following availability zones: "+region+"a, "+region+"b, "+region+"c.")
	}
	id, e := newEC2ID("subnet-")
	if e != nil {
		return EC2NetworkResource{}, e
	}
	r := EC2NetworkResource{ID: id, Kind: "AWS::EC2::Subnet", Region: region, StackID: ec2NativeOwner, VPCID: q.vpcID, CIDR: q.cidr, AvailabilityZone: zone, Tags: q.tags, Revision: 1}
	if r.Tags == nil {
		r.Tags = map[string]string{}
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return r, e
	}
	defer tx.Rollback()
	vpc, e := nativeParent(ctx, tx, region, q.vpcID, "AWS::EC2::VPC", "vpc", "InvalidVpcID.NotFound", "Add subnets in its stack instead.")
	if e != nil {
		return r, e
	}
	// Both prefixes were validated and masked when they were stored or parsed.
	outer, _ := netip.ParsePrefix(vpc.CIDR)
	inner, _ := netip.ParsePrefix(r.CIDR)
	if !outer.Contains(inner.Addr()) || inner.Bits() < outer.Bits() {
		return r, ec2Invalid("InvalidSubnet.Range", "The CIDR '"+r.CIDR+"' is invalid. It must lie inside the VPC CIDR "+vpc.CIDR+".")
	}
	_, siblings, e := nativeNetworks(ctx, tx, region)
	if e != nil {
		return r, e
	}
	for _, other := range siblings {
		taken, _ := netip.ParsePrefix(other.CIDR)
		if other.VPCID == r.VPCID && inner.Overlaps(taken) {
			return r, ec2Invalid("InvalidSubnet.Conflict", "The CIDR '"+r.CIDR+"' conflicts with another subnet")
		}
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_network_resources`).Scan(&count); e != nil {
		return r, e
	}
	if count >= 1000 {
		return r, ec2Invalid("SubnetLimitExceeded", "The local limit of 1000 network resources has been reached.")
	}
	if e = createEC2Network(ctx, tx, r); e != nil {
		return r, e
	}
	if claims, e := cloudFormationClaims(ctx, tx, region, r.Kind, id); e != nil || claims != 0 {
		return r, fmt.Errorf("new subnet identity is already claimed")
	}
	return r, tx.Commit()
}

func (s *StackStore) deleteNativeSubnet(ctx context.Context, region, id string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	subnet, e := nativeParent(ctx, tx, region, id, "AWS::EC2::Subnet", "subnet", "InvalidSubnetID.NotFound", "Delete its stack instead.")
	if e != nil {
		return e
	}
	var instances int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_native_instances WHERE region=? AND subnet_id=?`, region, id).Scan(&instances); e != nil {
		return e
	}
	if instances != 0 {
		return ec2Invalid("DependencyViolation", "The subnet '"+id+"' has dependencies and cannot be deleted.")
	}
	result, e := tx.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources WHERE region=? AND id=? AND stack_id=? AND revision=?`, region, id, ec2NativeOwner, subnet.Revision)
	if e != nil {
		return e
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		return fmt.Errorf("subnet delete was not acknowledged")
	}
	return tx.Commit()
}
