package aws

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

const (
	ec2NetworkACLAssociationKind = "AWS::EC2::SubnetNetworkAclAssociation"
	nativeACLDefaultProfile      = "ipv4-default-v1"
)

var errNativeNetworkACLAssociationState = errors.New("inconsistent native network ACL association state")

// These rows store control-plane configuration only. The default ACL is a
// durable system resource of a qualified native VPC, not a guess on Describe
// and not a second network backend. Neither table is read by a packet path.
func initEC2NativeNetworkACLAssociations(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS aws_ec2_native_acl_defaults(
		region TEXT NOT NULL,
		vpc_id TEXT NOT NULL,
		acl_id TEXT NOT NULL,
		profile TEXT NOT NULL CHECK(profile='ipv4-default-v1'),
		PRIMARY KEY(region,vpc_id),
		UNIQUE(region,acl_id),
		FOREIGN KEY(region,vpc_id) REFERENCES aws_ec2_network_resources(region,id) ON DELETE RESTRICT
	) WITHOUT ROWID;
	CREATE TABLE IF NOT EXISTS aws_ec2_native_acl_associations(
		region TEXT NOT NULL,
		subnet_id TEXT NOT NULL,
		vpc_id TEXT NOT NULL,
		custom_acl_id TEXT,
		association_id TEXT NOT NULL,
		previous_id TEXT,
		generation INTEGER NOT NULL CHECK(generation>0),
		CHECK((generation=1 AND previous_id IS NULL) OR
			(generation>1 AND previous_id IS NOT NULL AND previous_id<>association_id)),
		PRIMARY KEY(region,subnet_id),
		UNIQUE(region,association_id),
		UNIQUE(region,previous_id),
		FOREIGN KEY(region,vpc_id) REFERENCES aws_ec2_native_acl_defaults(region,vpc_id) ON DELETE RESTRICT,
		FOREIGN KEY(region,subnet_id) REFERENCES aws_ec2_network_resources(region,id) ON DELETE RESTRICT,
		FOREIGN KEY(region,custom_acl_id) REFERENCES aws_ec2_network_resources(region,id) ON DELETE RESTRICT
	) WITHOUT ROWID;
	CREATE INDEX IF NOT EXISTS aws_ec2_acl_associations_vpc ON aws_ec2_native_acl_associations(region,vpc_id);
	CREATE INDEX IF NOT EXISTS aws_ec2_acl_associations_custom ON aws_ec2_native_acl_associations(region,custom_acl_id);`); err != nil {
		return err
	}
	return tx.Commit()
}

type nativeACLDefault struct {
	region, vpcID, id, profile string
}

type nativeACLBinding struct {
	region, subnetID, vpcID, customACL, id, previous string
	generation                                       int64
}

func (b nativeACLBinding) aclID(d nativeACLDefault) string {
	if b.customACL != "" {
		return b.customACL
	}
	return d.id
}

func nativeACLID(prefix, region, owner string, generation int64) string {
	raw := "tenkacloud/ec2/native-acl/v1\x00" + prefix + "\x00" + region + "\x00" + owner + "\x00" + strconv.FormatInt(generation, 10)
	sum := sha256.Sum256([]byte(raw))
	return prefix + hex.EncodeToString(sum[:])[:17]
}

func nativeDefaultACLID(region, vpc string) string {
	return nativeACLID("acl-", region, vpc, 1)
}

func nativeACLBindingID(region, subnet string, generation int64) string {
	return nativeACLID("aclassoc-", region, subnet, generation)
}

func nativeACLDeclarations(ctx context.Context, tx *sql.Tx, region string) (map[string]bool, error) {
	stacks, declared, err := regionalGatewayStacks(ctx, tx, region)
	if err != nil {
		return nil, err
	}
	// ACL and subnet/ACL associations are not supported CFN resources, but
	// surviving declarations from a different version must never be adopted.
	for _, stack := range stacks {
		for _, member := range stack.Resources {
			if member.Type == ec2NetworkACLKind || member.Type == ec2NetworkACLAssociationKind {
				declared[member.PhysicalID] = true
			}
		}
	}
	return declared, nil
}

func nativeACLUnclaimed(ctx context.Context, tx *sql.Tx, region string, declared map[string]bool, ids ...string) error {
	for _, id := range ids {
		if id == "" {
			continue
		}
		if declared[id] {
			return errNativeNetworkACLAssociationState
		}
		var claimed bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_cfn_ownership WHERE physical_id=?
			AND (namespace=? OR (namespace=? AND kind IN (?,?))))`, id,
			stackResourceNamespace(region, ec2NativeSubnetKind), region, ec2NetworkACLKind, ec2NetworkACLAssociationKind).Scan(&claimed); err != nil {
			return err
		}
		if claimed {
			return errNativeNetworkACLAssociationState
		}
	}
	return nil
}

func nativeACLCoreIDFree(ctx context.Context, tx *sql.Tx, region, id string) error {
	var found bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_network_resources WHERE region=? AND id=?)`, region, id).Scan(&found); err != nil {
		return err
	}
	if found {
		return errNativeNetworkACLAssociationState
	}
	return nil
}

func validateNativeACLDefault(ctx context.Context, tx *sql.Tx, d nativeACLDefault, declared map[string]bool) error {
	if !slices.Contains(Regions, d.region) || !ec2VPCIDPattern.MatchString(d.vpcID) ||
		d.id != nativeDefaultACLID(d.region, d.vpcID) || d.profile != nativeACLDefaultProfile {
		return errNativeNetworkACLAssociationState
	}
	vpc, err := networkRead(ctx, tx, d.region, d.vpcID)
	if err != nil || vpc.Kind != ec2NetworkACLVPCType || vpc.StackID != ec2NativeOwner || vpc.MainRouteTableID == "" {
		return errNativeNetworkACLAssociationState
	}
	if err := verifyNativeVPCMainSlot(ctx, tx, vpc, &declared); err != nil {
		return err
	}
	if err := nativeACLUnclaimed(ctx, tx, d.region, declared, d.vpcID, d.id); err != nil {
		return err
	}
	return nativeACLCoreIDFree(ctx, tx, d.region, d.id)
}

func rawNativeACLDefault(ctx context.Context, tx *sql.Tx, region, vpc string) (nativeACLDefault, bool, error) {
	d := nativeACLDefault{region: region, vpcID: vpc}
	err := tx.QueryRowContext(ctx, `SELECT acl_id,profile FROM aws_ec2_native_acl_defaults WHERE region=? AND vpc_id=?`,
		region, vpc).Scan(&d.id, &d.profile)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	return d, err == nil, err
}

const nativeACLBindingSelect = `SELECT subnet_id,vpc_id,COALESCE(custom_acl_id,''),association_id,COALESCE(previous_id,''),generation
	FROM aws_ec2_native_acl_associations`

type nativeACLScanner interface{ Scan(...any) error }

func scanNativeACLBinding(region string, row nativeACLScanner) (nativeACLBinding, error) {
	b := nativeACLBinding{region: region}
	err := row.Scan(&b.subnetID, &b.vpcID, &b.customACL, &b.id, &b.previous, &b.generation)
	return b, err
}

func nativeACLBindingShape(b nativeACLBinding) bool {
	return b.generation > 0 && ec2VPCIDPattern.MatchString(b.vpcID) && ec2SubnetIDPattern.MatchString(b.subnetID) &&
		(b.customACL == "" || ec2NetworkACLIDPattern.MatchString(b.customACL)) &&
		b.id == nativeACLBindingID(b.region, b.subnetID, b.generation) &&
		(b.generation == 1 && b.previous == "" || b.generation > 1 && b.id != b.previous &&
			b.previous == nativeACLBindingID(b.region, b.subnetID, b.generation-1))
}

func nativeACLReadSubnet(ctx context.Context, tx *sql.Tx, d nativeACLDefault, subnet string, declared map[string]bool) (EC2NetworkResource, error) {
	row, err := networkRead(ctx, tx, d.region, subnet)
	if err != nil || row.Kind != ec2NativeSubnetKind || row.StackID != ec2NativeOwner || row.VPCID != d.vpcID {
		return EC2NetworkResource{}, errNativeNetworkACLAssociationState
	}
	if _, err := nativeSubnetReadback(ctx, tx, row); err != nil {
		return EC2NetworkResource{}, err
	}
	if err := nativeACLUnclaimed(ctx, tx, d.region, declared, subnet); err != nil {
		return EC2NetworkResource{}, err
	}
	return row, nil
}

func nativeACLReadCustom(ctx context.Context, tx *sql.Tx, d nativeACLDefault, id string, declared map[string]bool) (EC2NetworkResource, error) {
	r, err := readNativeNetworkACL(ctx, tx, d.region, id)
	if err != nil {
		return r, err
	}
	if r.VPCID != d.vpcID {
		return r, ec2Invalid("InvalidParameterValue", "The network ACL and subnet must be in the same VPC.")
	}
	if err := nativeACLUnclaimed(ctx, tx, d.region, declared, id); err != nil {
		return r, err
	}
	return r, nil
}

// Returns exact stored bindings in the selected VPC only. Missing metadata
// means legacy/unmanaged, not a default association inferred from an ID.
// A marked VPC with a missing or invalid binding is an error, not legacy.
func nativeACLBindingsInVPC(ctx context.Context, tx *sql.Tx, region, vpc string, declared map[string]bool) (nativeACLDefault, map[string]nativeACLBinding, error) {
	var coverage map[string][]string
	return nativeACLBindingsFromSnapshot(ctx, tx, region, vpc, declared, &coverage)
}

func nativeACLBindingsFromSnapshot(ctx context.Context, tx *sql.Tx, region, vpc string, declared map[string]bool, coverage *map[string][]string) (nativeACLDefault, map[string]nativeACLBinding, error) {
	d, marked, err := rawNativeACLDefault(ctx, tx, region, vpc)
	if err != nil {
		return d, nil, err
	}
	rows, err := tx.QueryContext(ctx, nativeACLBindingSelect+" WHERE region=? AND vpc_id=? ORDER BY subnet_id LIMIT ?",
		region, vpc, ec2NetworkACLRowLimit+1)
	if err != nil {
		return d, nil, err
	}
	defer rows.Close()
	bindings := map[string]nativeACLBinding{}
	ids := map[string]bool{}
	for rows.Next() {
		b, err := scanNativeACLBinding(region, rows)
		if err != nil {
			return d, nil, err
		}
		if !marked || len(bindings) >= ec2NetworkACLRowLimit || !nativeACLBindingShape(b) ||
			b.vpcID != vpc || bindings[b.subnetID].id != "" || ids[b.id] || b.previous != "" && ids[b.previous] {
			return d, nil, errNativeNetworkACLAssociationState
		}
		bindings[b.subnetID], ids[b.id] = b, true
		if b.previous != "" {
			ids[b.previous] = true
		}
	}
	if err := rows.Err(); err != nil {
		return d, nil, err
	}
	if err := rows.Close(); err != nil {
		return d, nil, err
	}
	if !marked {
		return d, bindings, nil
	}
	if err := validateNativeACLDefault(ctx, tx, d, declared); err != nil {
		return d, nil, err
	}
	readCustom := map[string]bool{}
	for _, b := range bindings {
		if _, err := nativeACLReadSubnet(ctx, tx, d, b.subnetID, declared); err != nil {
			return d, nil, err
		}
		if err := nativeACLUnclaimed(ctx, tx, region, declared, b.id, b.previous); err != nil {
			return d, nil, err
		}
		for _, assocID := range []string{b.id, b.previous} {
			if assocID == "" {
				continue
			}
			if err := nativeACLCoreIDFree(ctx, tx, region, assocID); err != nil {
				return d, nil, err
			}
		}
		if b.customACL != "" && !readCustom[b.customACL] {
			if _, err := nativeACLReadCustom(ctx, tx, d, b.customACL, declared); err != nil {
				return d, nil, fmt.Errorf("%w: bound ACL unavailable", errNativeNetworkACLAssociationState)
			}
			readCustom[b.customACL] = true
		}
	}
	if *coverage == nil {
		*coverage, err = nativeACLSubnetCoverage(ctx, tx, region)
		if err != nil {
			return d, nil, err
		}
	}
	for _, id := range (*coverage)[d.vpcID] {
		if bindings[id].id == "" {
			return d, nil, errNativeNetworkACLAssociationState
		}
	}
	return d, bindings, nil
}

func nativeACLSubnetCoverage(ctx context.Context, tx *sql.Tx, region string) (map[string][]string, error) {
	// Do not infer ownership from a case-sensitive JSON path. Scan only the
	// bounded, correctly typed native subnet namespace of this region.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM aws_ec2_network_resources WHERE region=? AND kind=? AND stack_id=? ORDER BY id LIMIT ?`,
		region, ec2NativeSubnetKind, ec2NativeOwner, ec2NetworkACLRowLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if len(ids) >= ec2NetworkACLRowLimit {
			return nil, errNativeNetworkACLAssociationState
		}
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
	result := map[string][]string{}
	for _, id := range ids {
		r, err := networkRead(ctx, tx, region, id)
		if err != nil {
			return nil, err
		}
		result[r.VPCID] = append(result[r.VPCID], id)
	}
	return result, nil
}

func nativeACLWriteCount(ctx context.Context, tx *sql.Tx) (int64, error) {
	var count int64
	err := tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&count)
	return count, err
}

// Require exactly the intended write and no additional trigger/cascade
// effects on subnet settings, route state, ownership, or neighbouring rows.
func nativeACLConfirmWrite(ctx context.Context, tx *sql.Tx, result sql.Result, before int64) error {
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return errNativeNetworkACLAssociationState
	}
	return nativeACLRequireWriteDelta(ctx, tx, before, 1)
}

func nativeACLRequireWriteDelta(ctx context.Context, tx *sql.Tx, before, expected int64) error {
	after, err := nativeACLWriteCount(ctx, tx)
	if err != nil {
		return err
	}
	if after-before != expected {
		return errNativeNetworkACLAssociationState
	}
	return nil
}
