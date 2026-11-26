package aws

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
)

var errEC2NativeRouteAssociationState = errors.New("inconsistent native route table association state")

func nativeRouteAssociationEndpoints(ctx context.Context, tx *sql.Tx, region, table, subnet string) (string, error) {
	t, err := readNativeRouteTable(ctx, tx, region, table)
	if err != nil {
		return "", err
	}
	n, err := networkRead(ctx, tx, region, subnet)
	if errors.Is(err, sql.ErrNoRows) || err == nil && n.Kind != ec2NativeSubnetKind {
		return "", ec2Invalid("InvalidSubnetID.NotFound", "The subnet does not exist in this region.")
	}
	if err != nil {
		return "", err
	}
	if n.StackID != ec2NativeOwner {
		return "", ec2Invalid("UnsupportedOperation", "CloudFormation manages this subnet; its associations are not changed here.")
	}
	if _, err := nativeSubnetReadback(ctx, tx, n); err != nil {
		return "", errEC2NativeRouteAssociationState
	}
	if t.VPCID != n.VPCID {
		return "", ec2Invalid("InvalidParameterValue", "The native route table and subnet must be in the same VPC.")
	}
	return n.VPCID, nil
}

func nativeRouteAssociationShape(r EC2NetworkResource) bool {
	return r.Kind == ec2Assoc && r.StackID == ec2NativeOwner && r.Revision == 1 &&
		ec2NativeRouteAssociationID.MatchString(r.ID) && ec2VPCIDPattern.MatchString(r.VPCID) &&
		ec2NativeRouteTableID.MatchString(r.RouteTableID) && ec2SubnetIDPattern.MatchString(r.SubnetID) &&
		r.GatewayID == "" && r.CIDR == "" && r.AvailabilityZone == "" && r.Description == "" &&
		!r.DNSHostnames && !r.DNSSupport && !r.PublicIP && !r.RouteEnabled && len(r.Tags) == 0 && len(r.Ingress) == 0
}

func checkNativeRouteAssociation(ctx context.Context, tx *sql.Tx, r EC2NetworkResource) error {
	if !nativeRouteAssociationShape(r) {
		return errEC2NativeRouteAssociationState
	}
	if err := nativeRouteTableUnclaimed(ctx, tx, r.Region, r.ID); err != nil {
		return err
	}
	vpc, err := nativeRouteAssociationEndpoints(ctx, tx, r.Region, r.RouteTableID, r.SubnetID)
	if err != nil || vpc != r.VPCID {
		return errEC2NativeRouteAssociationState
	}
	return nil
}

func listNativeRouteAssociations(ctx context.Context, tx *sql.Tx, region string) ([]EC2NetworkResource, error) {
	// Check the ID namespace as well: wrong-kind corruption must not erase an
	// existing binding from the native view or allow it to be replaced.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM aws_ec2_network_resources
		WHERE region=? AND stack_id=? AND (kind=? OR id LIKE 'rtbassoc-%') ORDER BY id LIMIT ?`,
		region, ec2NativeOwner, ec2Assoc, ec2NativeRouteTableRowLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(ids) > ec2NativeRouteTableRowLimit {
		return nil, errEC2NativeRouteAssociationState
	}
	out := make([]EC2NetworkResource, 0, len(ids))
	subnets := make(map[string]bool, len(ids))
	for _, id := range ids {
		r, err := networkRead(ctx, tx, region, id)
		if err != nil {
			return nil, err
		}
		if err := checkNativeRouteAssociation(ctx, tx, r); err != nil {
			return nil, err
		}
		if subnets[r.SubnetID] {
			return nil, errEC2NativeRouteAssociationState
		}
		subnets[r.SubnetID] = true
		out = append(out, r)
	}
	return out, nil
}

// The regional Describe path must obtain these rows only after its existing
// ec2:DescribeRouteTables check and in the same transaction as its table read.
func nativeRouteAssociationProjection(ctx context.Context, tx *sql.Tx, region string) (map[string][]ec2RouteAssociationRead, int, error) {
	rows, err := listNativeRouteAssociations(ctx, tx, region)
	if err != nil {
		return nil, 0, err
	}
	byTable := map[string][]ec2RouteAssociationRead{}
	for _, r := range rows {
		byTable[r.RouteTableID] = append(byTable[r.RouteTableID], ec2RouteAssociationRead{
			ID: r.ID, Table: r.RouteTableID, Subnet: r.SubnetID, Main: false,
		})
	}
	return byTable, len(rows), nil
}

func (s *StackStore) associateNativeRouteTable(ctx context.Context, region string, q ec2NativeRouteAssociationQuery, a ec2NativeRouteAssociationAuthority) (EC2NetworkResource, error) {
	if !a.allowsPair(ec2AssociateNativeRouteTable, region, q.table, q.subnet) {
		return EC2NetworkResource{}, deniedNativeRouteAssociation()
	}
	// The shared SQLite store uses immediate transactions. Both uniqueness
	// lookup and write must be inside this one transaction.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EC2NetworkResource{}, err
	}
	defer tx.Rollback()
	vpc, err := nativeRouteAssociationEndpoints(ctx, tx, region, q.table, q.subnet)
	if err != nil {
		return EC2NetworkResource{}, err
	}
	existing, err := listNativeRouteAssociations(ctx, tx, region)
	if err != nil {
		return EC2NetworkResource{}, err
	}
	for _, r := range existing {
		if r.SubnetID != q.subnet {
			continue
		}
		if r.RouteTableID != q.table {
			return EC2NetworkResource{}, ec2Invalid("Resource.AlreadyAssociated", "This subnet already has a different explicit route table association.")
		}
		// Retrying the same local relationship returns its persisted ID.
		return r, tx.Commit()
	}
	var entropy [9]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return EC2NetworkResource{}, err
	}
	r := EC2NetworkResource{ID: "rtbassoc-" + hex.EncodeToString(entropy[:])[:17],
		Kind: ec2Assoc, Region: region, StackID: ec2NativeOwner, VPCID: vpc, SubnetID: q.subnet, RouteTableID: q.table, Revision: 1}
	if err := nativeRouteTableUnclaimed(ctx, tx, region, r.ID); err != nil {
		return EC2NetworkResource{}, err
	}
	if err := createEC2Network(ctx, tx, r); err != nil {
		if errors.Is(err, errEC2NetworkQuota) {
			return EC2NetworkResource{}, ec2Invalid("ResourceLimitExceeded", "The local network resource bound has been reached.")
		}
		return EC2NetworkResource{}, err
	}
	return r, tx.Commit()
}

func (s *StackStore) disassociateNativeRouteTable(ctx context.Context, region, id string, a ec2NativeRouteAssociationAuthority) error {
	if a.check == nil {
		return deniedNativeRouteAssociation()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := networkRead(ctx, tx, region, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (r.Kind != ec2Assoc || r.StackID != ec2NativeOwner) {
		return a.missingAssociation()
	}
	if err != nil {
		// A failed identity read cannot establish the pair's authorization.
		if !a.revealMissingID {
			return deniedNativeRouteAssociation()
		}
		return err
	}
	// Resolve the pair privately from immutable transaction state. Do not grant
	// access with a guessed ID, partial pair, or the ID of a CFN association.
	if !ec2NativeRouteTableID.MatchString(r.RouteTableID) || !ec2SubnetIDPattern.MatchString(r.SubnetID) ||
		!a.allowsPair(ec2DisassociateNativeRouteTable, region, r.RouteTableID, r.SubnetID) {
		return deniedNativeRouteAssociation()
	}
	if _, err := listNativeRouteAssociations(ctx, tx, region); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources
		WHERE region=? AND id=? AND stack_id=? AND kind=? AND revision=?`, region, id, ec2NativeOwner, ec2Assoc, r.Revision)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return errEC2NativeRouteAssociationState
	}
	if _, err := networkRead(ctx, tx, region, id); !errors.Is(err, sql.ErrNoRows) {
		return errEC2NativeRouteAssociationState
	}
	// No implicit main table or active forwarding is synthesized on removal.
	return tx.Commit()
}
