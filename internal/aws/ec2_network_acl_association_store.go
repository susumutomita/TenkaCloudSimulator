package aws

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"
	"strings"
)

func nativeACLBindingIDFree(ctx context.Context, tx *sql.Tx, region, id string) error {
	if err := nativeACLCoreIDFree(ctx, tx, region, id); err != nil {
		return err
	}
	var found bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_native_acl_associations
		WHERE region=? AND (association_id=? OR previous_id=?))`, region, id, id).Scan(&found); err != nil {
		return err
	}
	if found {
		return errNativeNetworkACLAssociationState
	}
	return nil
}

// Use only inside the new native VPC's transaction, after creating and
// verifying its main route-table slot. Never called on read or for migration.
func createNativeNetworkACLDefault(ctx context.Context, tx *sql.Tx, vpc EC2NetworkResource) error {
	d := nativeACLDefault{vpc.Region, vpc.ID, nativeDefaultACLID(vpc.Region, vpc.ID), nativeACLDefaultProfile}
	declared, err := nativeACLDeclarations(ctx, tx, d.region)
	if err != nil {
		return err
	}
	if err := validateNativeACLDefault(ctx, tx, d, declared); err != nil {
		return err
	}
	before, err := nativeACLWriteCount(ctx, tx)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO aws_ec2_native_acl_defaults(region,vpc_id,acl_id,profile) VALUES(?,?,?,?)`,
		d.region, d.vpcID, d.id, d.profile)
	if err != nil {
		return err
	}
	if err := nativeACLConfirmWrite(ctx, tx, result, before); err != nil {
		return err
	}
	saved, bindings, err := nativeACLBindingsInVPC(ctx, tx, d.region, d.vpcID, declared)
	if err != nil {
		return err
	}
	if saved != d || len(bindings) != 0 {
		return errNativeNetworkACLAssociationState
	}
	return nil
}

// A new subnet under a marked VPC receives its durable default association
// in the caller's create transaction. Existing unmarked VPCs stay legacy.
func createNativeSubnetACLAssociation(ctx context.Context, tx *sql.Tx, subnet EC2NetworkResource) error {
	d, marked, err := rawNativeACLDefault(ctx, tx, subnet.Region, subnet.VPCID)
	if err != nil {
		return err
	}
	declared, err := nativeACLDeclarations(ctx, tx, subnet.Region)
	if err != nil {
		return err
	}
	if !marked {
		_, _, err := nativeACLBindingsInVPC(ctx, tx, subnet.Region, subnet.VPCID, declared)
		if err != nil {
			return err
		}
		return checkNativeACLSubnetBindingAbsent(ctx, tx, subnet)
	}
	if err := validateNativeACLDefault(ctx, tx, d, declared); err != nil {
		return err
	}
	if _, err := nativeACLReadSubnet(ctx, tx, d, subnet.ID, declared); err != nil {
		return err
	}
	id := nativeACLBindingID(d.region, subnet.ID, 1)
	if err := nativeACLUnclaimed(ctx, tx, d.region, declared, id); err != nil {
		return err
	}
	if err := nativeACLBindingIDFree(ctx, tx, d.region, id); err != nil {
		return err
	}
	before, err := nativeACLWriteCount(ctx, tx)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO aws_ec2_native_acl_associations
		(region,subnet_id,vpc_id,association_id,generation) VALUES(?,?,?,?,1)`, d.region, subnet.ID, d.vpcID, id)
	if err != nil {
		return err
	}
	if err := nativeACLConfirmWrite(ctx, tx, result, before); err != nil {
		return err
	}
	saved, bindings, err := nativeACLBindingsInVPC(ctx, tx, d.region, d.vpcID, declared)
	if err != nil {
		return err
	}
	if b := bindings[subnet.ID]; saved != d || b.id != id || b.generation != 1 || b.customACL != "" {
		return errNativeNetworkACLAssociationState
	}
	return nil
}

// The caller has already checked existing subnet dependencies and will delete
// the exact subnet next in this same transaction. Deleting an association here
// neither disassociates any peer nor changes effective route-table state.
func removeNativeSubnetACLAssociation(ctx context.Context, tx *sql.Tx, subnet EC2NetworkResource) error {
	declared, err := nativeACLDeclarations(ctx, tx, subnet.Region)
	if err != nil {
		return err
	}
	d, bindings, err := nativeACLBindingsInVPC(ctx, tx, subnet.Region, subnet.VPCID, declared)
	if err != nil {
		return err
	}
	if d.id == "" {
		return checkNativeACLSubnetBindingAbsent(ctx, tx, subnet)
	}
	b, ok := bindings[subnet.ID]
	if !ok {
		return errNativeNetworkACLAssociationState
	}
	before, err := nativeACLWriteCount(ctx, tx)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM aws_ec2_native_acl_associations
		WHERE region=? AND subnet_id=? AND vpc_id=? AND association_id=? AND generation=?`,
		b.region, b.subnetID, b.vpcID, b.id, b.generation)
	if err != nil {
		return err
	}
	if err := nativeACLConfirmWrite(ctx, tx, result, before); err != nil {
		return err
	}
	return checkNativeACLSubnetBindingAbsent(ctx, tx, subnet)
}

func checkNativeACLSubnetBindingAbsent(ctx context.Context, tx *sql.Tx, subnet EC2NetworkResource) error {
	var remaining bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_native_acl_associations WHERE region=? AND subnet_id=?)`,
		subnet.Region, subnet.ID).Scan(&remaining); err != nil {
		return err
	}
	if remaining {
		return errNativeNetworkACLAssociationState
	}
	return nil
}

// The exact system default is retired only with its VPC after all ordinary
// VPC dependencies have been checked by the caller.
func removeNativeNetworkACLDefault(ctx context.Context, tx *sql.Tx, vpc EC2NetworkResource) error {
	declared, err := nativeACLDeclarations(ctx, tx, vpc.Region)
	if err != nil {
		return err
	}
	d, bindings, err := nativeACLBindingsInVPC(ctx, tx, vpc.Region, vpc.ID, declared)
	if err != nil || d.id == "" {
		return err
	}
	if len(bindings) != 0 {
		return ec2Invalid("DependencyViolation", "The VPC still has subnets associated to its network ACLs.")
	}
	before, err := nativeACLWriteCount(ctx, tx)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM aws_ec2_native_acl_defaults
		WHERE region=? AND vpc_id=? AND acl_id=? AND profile=?`, d.region, d.vpcID, d.id, d.profile)
	if err != nil {
		return err
	}
	if err := nativeACLConfirmWrite(ctx, tx, result, before); err != nil {
		return err
	}
	if _, found, err := rawNativeACLDefault(ctx, tx, d.region, d.vpcID); found || err != nil {
		return errNativeNetworkACLAssociationState
	}
	return nil
}

// Resolve current or one-step replay privately. Unknown IDs, other regions
// and partial authorization are indistinguishable to an ordinary identity.
func resolveNativeACLBinding(ctx context.Context, tx *sql.Tx, region string, q ec2NetworkACLAssociationQuery, a ec2NetworkACLAssociationAuthority) (nativeACLBinding, error) {
	var b nativeACLBinding
	rows, err := tx.QueryContext(ctx, nativeACLBindingSelect+" WHERE region=? AND (association_id=? OR previous_id=?) LIMIT 2",
		region, q.association, q.association)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return b, err
		}
		return b, a.missingAssociation()
	}
	b, err = scanNativeACLBinding(region, rows)
	if err != nil || !nativeACLBindingShape(b) || rows.Next() {
		if a.revealMissingID {
			return nativeACLBinding{}, errNativeNetworkACLAssociationState
		}
		return nativeACLBinding{}, deniedNetworkACLAssociation()
	}
	if !a.allows(region, "subnet", b.subnetID) {
		return nativeACLBinding{}, deniedNetworkACLAssociation()
	}
	if err := rows.Err(); err != nil {
		return nativeACLBinding{}, err
	}
	return b, rows.Close()
}

func (s *StackStore) replaceNativeNetworkACLAssociation(ctx context.Context, region string, q ec2NetworkACLAssociationQuery, a ec2NetworkACLAssociationAuthority) (string, error) {
	if failure := q.validate(); failure != nil {
		return "", failure
	}
	if !slices.Contains(Regions, region) {
		return "", ec2Invalid("InvalidParameterValue", "Unsupported region.")
	}
	if !a.allows(region, "network-acl", q.acl) {
		return "", deniedNetworkACLAssociation()
	}
	// StackStore uses the common immediate SQLite transaction; the authorized
	// incumbent cannot drift between ID lookup, replacement, and readback.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	current, err := resolveNativeACLBinding(ctx, tx, region, q, a)
	if err != nil {
		return "", err
	}
	declared, err := nativeACLDeclarations(ctx, tx, region)
	if err != nil {
		return "", err
	}
	d, bindings, err := nativeACLBindingsInVPC(ctx, tx, region, current.vpcID, declared)
	if err != nil {
		return "", err
	}
	if d.id == "" || bindings[current.subnetID] != current {
		return "", errNativeNetworkACLAssociationState
	}
	if current.id != q.association && (current.previous != q.association || current.aclID(d) != q.acl) {
		return "", a.missingAssociation()
	}
	// Establish the typed target and same-VPC scope before checking its
	// relationships against this VPC's cohort. A valid attached ACL in a
	// different VPC is a scope error, not corruption of our saved graph.
	target := current.customACL
	if current.aclID(d) != q.acl {
		target, err = nativeACLReplacementTarget(ctx, tx, d, q.acl, declared)
		if err != nil {
			return "", err
		}
	}
	for _, id := range []string{current.customACL, target} {
		if id == "" {
			continue
		}
		count := 0
		for _, b := range bindings {
			if b.customACL == id {
				count++
			}
		}
		if err := verifyNativeACLCustomReferences(ctx, tx, region, id, count); err != nil {
			return "", err
		}
	}
	if current.aclID(d) == q.acl {
		return current.id, tx.Commit() // current no-op or exact immediate retry
	}
	var custom any
	next := current
	next.customACL = target
	if target != "" {
		custom = target
	}
	if current.generation == math.MaxInt64 {
		return "", errNativeNetworkACLAssociationState
	}
	next.generation++
	next.previous = current.id
	next.id = nativeACLBindingID(region, next.subnetID, next.generation)
	if err := nativeACLUnclaimed(ctx, tx, region, declared, next.id); err != nil {
		return "", err
	}
	if err := nativeACLBindingIDFree(ctx, tx, region, next.id); err != nil {
		return "", err
	}
	before, err := nativeACLWriteCount(ctx, tx)
	if err != nil {
		return "", err
	}
	result, err := tx.ExecContext(ctx, `UPDATE aws_ec2_native_acl_associations
		SET custom_acl_id=?,association_id=?,previous_id=?,generation=?
		WHERE region=? AND subnet_id=? AND vpc_id=? AND association_id=? AND generation=?
			AND COALESCE(custom_acl_id,'')=? AND COALESCE(previous_id,'')=?`,
		custom, next.id, next.previous, next.generation, region, current.subnetID, current.vpcID, current.id,
		current.generation, current.customACL, current.previous)
	if err != nil {
		return "", err
	}
	if err := nativeACLConfirmWrite(ctx, tx, result, before); err != nil {
		return "", err
	}
	saved, verified, err := nativeACLBindingsInVPC(ctx, tx, region, current.vpcID, declared)
	if err != nil {
		return "", err
	}
	if saved != d || verified[current.subnetID] != next || len(verified) != len(bindings) {
		return "", errNativeNetworkACLAssociationState
	}
	for id, before := range bindings {
		if id != current.subnetID && verified[id] != before {
			return "", errNativeNetworkACLAssociationState
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return next.id, nil
}

// A blank custom target represents the verified default of this same VPC;
// a non-blank target has been verified as an existing native custom ACL.
func nativeACLReplacementTarget(ctx context.Context, tx *sql.Tx, d nativeACLDefault, id string, declared map[string]bool) (string, error) {
	if id == d.id {
		return "", nil
	}
	// System defaults live outside the custom ACL table. Classify a real
	// stored default in another VPC as a scope error, too.
	var vpc string
	err := tx.QueryRowContext(ctx, `SELECT vpc_id FROM aws_ec2_native_acl_defaults WHERE region=? AND acl_id=?`, d.region, id).Scan(&vpc)
	if err == nil {
		other, found, err := rawNativeACLDefault(ctx, tx, d.region, vpc)
		if err != nil {
			return "", err
		}
		if !found || other.id != id || vpc == d.vpcID {
			return "", errNativeNetworkACLAssociationState
		}
		if err := validateNativeACLDefault(ctx, tx, other, declared); err != nil {
			return "", err
		}
		return "", ec2Invalid("InvalidParameterValue", "The network ACL and subnet must be in the same VPC.")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if _, err := nativeACLReadCustom(ctx, tx, d, id, declared); err != nil {
		return "", err
	}
	return id, nil
}

func (s *StackStore) executeEC2NetworkACLAssociation(ctx context.Context, region string, q ec2NetworkACLAssociationQuery, a ec2NetworkACLAssociationAuthority) (*ec2ReplaceNetworkACLAssociationResponse, error) {
	id, err := s.replaceNativeNetworkACLAssociation(ctx, region, q, a)
	if err != nil {
		return nil, err
	}
	return &ec2ReplaceNetworkACLAssociationResponse{NewAssociationID: id}, nil
}

// For the existing DescribeNetworkAcls transaction. This is a verified
// projection of stored rows, never an API that performs a repair or enrolment.
func nativeNetworkACLAssociationProjection(ctx context.Context, tx *sql.Tx, region, vpc string, declared map[string]bool) (nativeACLDefault, map[string][]ec2NetworkACLAssociationRead, error) {
	var coverage map[string][]string
	return nativeACLProjectionFromSnapshot(ctx, tx, region, vpc, declared, &coverage)
}

func nativeACLProjectionFromSnapshot(ctx context.Context, tx *sql.Tx, region, vpc string, declared map[string]bool, coverage *map[string][]string) (nativeACLDefault, map[string][]ec2NetworkACLAssociationRead, error) {
	d, bindings, err := nativeACLBindingsFromSnapshot(ctx, tx, region, vpc, declared, coverage)
	if err != nil {
		return d, nil, err
	}
	out := map[string][]ec2NetworkACLAssociationRead{}
	for _, b := range bindings {
		id := b.aclID(d)
		out[id] = append(out[id], ec2NetworkACLAssociationRead{b.id, id, b.subnetID})
	}
	for _, list := range out {
		slices.SortFunc(list, func(a, b ec2NetworkACLAssociationRead) int {
			return strings.Compare(a.SubnetID, b.SubnetID)
		})
	}
	return d, out, nil
}

func nativeDefaultNetworkACLEntries() []ec2NetworkACLEntry {
	in := ec2NetworkACLEntry{Rule: 100, Protocol: "-1", Action: "allow", CIDR: "0.0.0.0/0"}
	out := in
	out.Egress = true
	return networkACLRecordEntries(&nativeNetworkACLRuleState{Entries: []ec2NetworkACLEntry{in, out}})
}

// Deleting an attached custom ACL or editing an attached/default rule set
// is outside this bounded configuration profile. Restore the target subnet to
// its durable default before changing a custom ACL's detached rule set.
func rejectAttachedNativeACL(ctx context.Context, tx *sql.Tx, region, id string) error {
	var found bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_native_acl_associations WHERE region=? AND custom_acl_id=?)`,
		region, id).Scan(&found); err != nil {
		return err
	}
	if found {
		return ec2Invalid("DependencyViolation", "The network ACL still has a subnet association.")
	}
	return nil
}

// New custom ACL IDs may not collide with a stored default. The default is
// reserved even while it has zero associated subnets.
func rejectNativeDefaultACLID(ctx context.Context, tx *sql.Tx, region, id string) error {
	var found bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_native_acl_defaults WHERE region=? AND acl_id=?)`,
		region, id).Scan(&found)
	if err != nil {
		return err
	}
	if found {
		return ec2Invalid("UnsupportedOperation", "The system default network ACL is read-only in this profile.")
	}
	return nil
}
