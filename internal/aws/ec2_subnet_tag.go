package aws

import (
	"context"
	"database/sql"
	"errors"
)

func readNativeSubnetTagTarget(ctx context.Context, tx *sql.Tx, region, id string) (EC2NetworkResource, string, error) {
	row, err := networkRead(ctx, tx, region, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.Kind != ec2NativeSubnetKind {
		return row, "", ec2Invalid("InvalidSubnetID.NotFound", "The subnet does not exist in this region.")
	}
	if err != nil {
		return row, "", err
	}
	if row.StackID != ec2NativeOwner {
		return row, "", ec2Invalid("UnsupportedOperation", "Native tag operations cannot change a CloudFormation-owned subnet.")
	}
	// This shared decoder checks saved shape and namespace-wide claims on
	// both ends, region, CIDR containment, generation, AZ and tag bounds.
	if _, err := nativeSubnetReadback(ctx, tx, row); err != nil {
		return row, "", err
	}
	_, declared, err := regionalGatewayStacks(ctx, tx, region)
	if err != nil {
		return row, "", err
	}
	if declared[id] || declared[row.VPCID] {
		return row, "", errNativeEC2TagState
	}
	parent, err := nativeRouteTableVPC(ctx, tx, region, row.VPCID, &declared)
	if err != nil {
		return row, "", err
	}
	if !validNativeDNSGeneration(parent) {
		return row, "", errNativeEC2TagState
	}
	return exactNativeTagDocument(ctx, tx, row)
}
