package aws

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"reflect"
)

// The pairing is recorded in the native VPC JSON, not reconstructed on read.
// An empty slot marks a legacy VPC; do not create or adopt a table for it.
func nativeMainDerivedID(prefix, region, vpc string) string {
	h := sha256.Sum256([]byte("tenkacloud/ec2/native-vpc/main/v1\x00" + prefix + "\x00" + region + "\x00" + vpc))
	return prefix + hex.EncodeToString(h[:])[:17]
}

func nativeMainTableID(region, vpc string) string { return nativeMainDerivedID("rtb-", region, vpc) }
func nativeMainAssociationID(region, vpc string) string {
	return nativeMainDerivedID("rtbassoc-", region, vpc)
}

func nativeTableLocalCIDR(vpc EC2NetworkResource) string {
	if vpc.MainRouteTableID == "" {
		return ""
	}
	return vpc.CIDR
}

// Called only after verifying the typed table and its owning VPC slot.
func isNativeMainTable(r EC2NetworkResource) bool {
	return r.CIDR != "" && r.ID == nativeMainTableID(r.Region, r.VPCID)
}

func verifyNativeVPCMainSlot(ctx context.Context, tx *sql.Tx, vpc EC2NetworkResource, declarations *map[string]bool) error {
	if vpc.MainRouteTableID == "" {
		return nil
	}
	if vpc.Kind != ec2RTVPC || vpc.StackID != ec2NativeOwner || !ec2VPCIDPattern.MatchString(vpc.ID) ||
		vpc.MainRouteTableID != nativeMainTableID(vpc.Region, vpc.ID) ||
		(vpc.Revision != 1 || vpc.MainRouteRepair != nil) && !validNativeVPCMainRepair(vpc) {
		return errEC2NativeRouteTableState
	}
	prefix, ok := nativeSubnetPrefix(vpc.CIDR)
	if !ok || prefix.Masked() != prefix {
		return errEC2NativeRouteTableState
	}
	main, err := networkRead(ctx, tx, vpc.Region, vpc.MainRouteTableID)
	if err != nil || main.Kind != ec2RT || main.StackID != ec2NativeOwner || main.VPCID != vpc.ID ||
		!nativeRouteTableFields(main) || main.Revision != 1 || main.CIDR != vpc.CIDR || len(main.Tags) != 0 {
		return errEC2NativeRouteTableState
	}
	for _, id := range []string{vpc.ID, main.ID, nativeMainAssociationID(vpc.Region, vpc.ID)} {
		if err := nativeRouteTableUnclaimed(ctx, tx, vpc.Region, id); err != nil {
			return err
		}
	}
	var collision bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_network_resources WHERE region=? AND id=?)`,
		vpc.Region, nativeMainAssociationID(vpc.Region, vpc.ID)).Scan(&collision); err != nil {
		return err
	}
	if collision {
		return errEC2NativeRouteTableState
	}
	if declarations == nil {
		var own map[string]bool
		declarations = &own
	}
	if *declarations == nil {
		if _, seen, err := regionalGatewayStacks(ctx, tx, vpc.Region); err != nil {
			return err
		} else {
			*declarations = seen
		}
	}
	return checkNativeVPCMainDeclarations(vpc, *declarations)
}

// main declarations cannot be adopted even if their ownership index entry is
// missing. This is a separate typed regional witness from the claims table.
func checkNativeVPCMainDeclarations(vpc EC2NetworkResource, declared map[string]bool) error {
	if declared[vpc.ID] || declared[vpc.MainRouteTableID] || declared[nativeMainAssociationID(vpc.Region, vpc.ID)] {
		return errEC2NativeRouteTableState
	}
	return nil
}

func createNativeVPCMainTable(ctx context.Context, tx *sql.Tx, vpc EC2NetworkResource) error {
	_, declared, err := regionalGatewayStacks(ctx, tx, vpc.Region)
	if err != nil {
		return err
	}
	if err := checkNativeVPCMainDeclarations(vpc, declared); err != nil {
		return err
	}
	var retired bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_native_route_table_replay WHERE region=? AND route_table_id=?)`,
		vpc.Region, vpc.MainRouteTableID).Scan(&retired); err != nil {
		return err
	}
	if retired {
		return errEC2NativeRouteTableState
	}
	main := EC2NetworkResource{ID: vpc.MainRouteTableID, Kind: ec2RT, Region: vpc.Region, StackID: ec2NativeOwner,
		VPCID: vpc.ID, CIDR: vpc.CIDR, Revision: 1}
	if err := createEC2Network(ctx, tx, main); err != nil {
		return err
	}
	owner, err := networkRead(ctx, tx, vpc.Region, vpc.ID)
	if err != nil || !reflect.DeepEqual(owner, vpc) {
		return errEC2NativeRouteTableState
	}
	return verifyNativeVPCMainSlot(ctx, tx, owner, nil)
}

// The caller already validated the paired slot and checked every dependency
// other than this exact system child. Both deletes belong to one transaction.
func removeNativeVPCMainTable(ctx context.Context, tx *sql.Tx, vpc EC2NetworkResource) error {
	if vpc.MainRouteTableID == "" {
		return nil
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources WHERE region=? AND id=? AND stack_id=? AND kind=? AND revision=1`,
		vpc.Region, vpc.MainRouteTableID, ec2NativeOwner, ec2RT)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errEC2NativeRouteTableState
	}
	if _, err := networkRead(ctx, tx, vpc.Region, vpc.MainRouteTableID); !errors.Is(err, sql.ErrNoRows) {
		return errEC2NativeRouteTableState
	}
	return nil
}

// Verify both removals and absence of any late binding in the final snapshot,
// after parent deletion too. The derived main association is not a stored row.
func verifyNativeVPCMainRetired(ctx context.Context, tx *sql.Tx, vpc EC2NetworkResource) error {
	if vpc.MainRouteTableID == "" {
		return nil
	}
	for _, id := range []string{vpc.ID, vpc.MainRouteTableID, nativeMainAssociationID(vpc.Region, vpc.ID)} {
		if _, err := networkRead(ctx, tx, vpc.Region, id); !errors.Is(err, sql.ErrNoRows) {
			return errEC2NativeRouteTableState
		}
		if err := nativeRouteTableUnclaimed(ctx, tx, vpc.Region, id); err != nil {
			return err
		}
	}
	_, declared, err := regionalGatewayStacks(ctx, tx, vpc.Region)
	if err != nil {
		return err
	}
	if err := checkNativeVPCMainDeclarations(vpc, declared); err != nil {
		return err
	}
	referenced, err := nativeVPCRetainedDependents(ctx, tx, vpc, false)
	if err != nil {
		return err
	}
	if referenced {
		return errEC2NativeRouteTableState
	}
	return nil
}

// Check marked native VPCs even if their table row is completely missing.
// Legacy and CloudFormation VPCs do not receive route rows or a projection.
func checkNativeVPCMainRouteInventory(ctx context.Context, tx *sql.Tx, region string, declarations *map[string]bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM aws_ec2_network_resources WHERE region=? AND stack_id=? AND kind=? ORDER BY id LIMIT ?`,
		region, ec2NativeOwner, ec2RTVPC, ec2NativeRouteTableRowLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) > ec2NativeRouteTableRowLimit {
		return errEC2NativeRouteTableState
	}
	for _, id := range ids {
		vpc, err := networkRead(ctx, tx, region, id)
		if err != nil {
			return err
		}
		if err := verifyNativeVPCMainSlot(ctx, tx, vpc, declarations); err != nil {
			return err
		}
	}
	return nil
}
