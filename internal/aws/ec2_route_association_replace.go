package aws

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

const replaceNativeAssociationIAM = "ec2:" + ec2ReplaceNativeRouteTable

// The old ID is resolved privately so an exercise identity cannot guess a
// subnet or check just the target's authorization. Read and write refer to
// the same incumbent and destination in one transaction.
func resolveNativeRouteReplacement(ctx context.Context, tx *sql.Tx, region string, q ec2NativeRouteAssociationQuery, a ec2NativeRouteAssociationAuthority) (EC2NetworkResource, error) {
	old, err := networkRead(ctx, tx, region, q.association)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (old.Kind != ec2Assoc || old.StackID != ec2NativeOwner) {
		return EC2NetworkResource{}, a.missingAssociation()
	}
	if err != nil {
		if !a.revealMissingID {
			return EC2NetworkResource{}, deniedNativeRouteAssociation()
		}
		return EC2NetworkResource{}, err
	}
	if !ec2NativeRouteTableID.MatchString(old.RouteTableID) || !ec2SubnetIDPattern.MatchString(old.SubnetID) ||
		!a.allowsPair(ec2ReplaceNativeRouteTable, region, old.RouteTableID, old.SubnetID) {
		return EC2NetworkResource{}, deniedNativeRouteAssociation()
	}
	return old, nil
}

// No survivor from either CloudFormation ownership witness may be adopted.
// Detect mutations other than the exact old/new native relationship, including
// changes to VPC/subnet/table documents or to configured routes. The digest
// frames indexed durable row fields, not a display projection of them.
func nativeRouteReplacementWitness(ctx context.Context, tx *sql.Tx, region string, old, next EC2NetworkResource) (digest [sha256.Size]byte, err error) {
	_, declared, err := regionalGatewayStacks(ctx, tx, region)
	if err != nil {
		return digest, err
	}
	for _, id := range []string{old.ID, next.ID, old.VPCID, old.SubnetID, old.RouteTableID, next.RouteTableID} {
		if declared[id] {
			return digest, errEC2NativeRouteAssociationState
		}
		if err := nativeRouteTableUnclaimed(ctx, tx, region, id); err != nil {
			return digest, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,stack_id,kind,CAST(revision AS text),document
		FROM aws_ec2_network_resources WHERE region=? AND id NOT IN (?,?) ORDER BY id LIMIT ?`,
		region, old.ID, next.ID, ec2NativeRouteTableRowLimit+1)
	if err != nil {
		return digest, err
	}
	defer rows.Close()
	pin := sha256.New()
	for count := 0; rows.Next(); count++ {
		var f [5]string
		if count >= ec2NativeRouteTableRowLimit {
			return digest, errEC2NativeRouteAssociationState
		}
		if err := rows.Scan(&f[0], &f[1], &f[2], &f[3], &f[4]); err != nil {
			return digest, err
		}
		for _, field := range f {
			var length [8]byte
			binary.BigEndian.PutUint64(length[:], uint64(len(field)))
			_, _ = pin.Write(length[:])
			_, _ = io.WriteString(pin, field)
		}
	}
	if err := rows.Err(); err != nil {
		return digest, err
	}
	_ = pin.Sum(digest[:0])
	return digest, nil
}

func verifyNativeReplacementDocument(ctx context.Context, tx *sql.Tx, row EC2NetworkResource) error {
	document, err := json.Marshal(row)
	if err != nil {
		return err
	}
	var canonical bool
	if err := tx.QueryRowContext(ctx, `SELECT document=? FROM aws_ec2_network_resources WHERE region=? AND id=?`,
		string(document), row.Region, row.ID).Scan(&canonical); err != nil {
		return err
	}
	if !canonical {
		return errEC2NativeRouteAssociationState
	}
	return nil
}

func deleteReplacedAssociation(ctx context.Context, tx *sql.Tx, old EC2NetworkResource) error {
	result, err := tx.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources
		WHERE region=? AND id=? AND stack_id=? AND kind=? AND revision=1`, old.Region, old.ID, ec2NativeOwner, ec2Assoc)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errEC2NativeRouteAssociationState
	}
	return nil
}

func (s *StackStore) replaceNativeRouteTableAssociation(ctx context.Context, region string, q ec2NativeRouteAssociationQuery, a ec2NativeRouteAssociationAuthority) (EC2NetworkResource, error) {
	var next EC2NetworkResource
	if a.check == nil || !a.check(replaceNativeAssociationIAM, networkARN(region, "route-table", q.table)) {
		return next, deniedNativeRouteAssociation()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return next, err
	}
	defer tx.Rollback()
	old, err := resolveNativeRouteReplacement(ctx, tx, region, q, a)
	if err != nil {
		return next, err
	}
	if q.dryRun {
		// The incumbent must exist to resolve the two dependent IAM resources.
		// No destination row, topology transition or write is verified in DryRun.
		return next, ec2Invalid("DryRunOperation", "The required permissions are present; the request was not executed.")
	}
	if old.RouteTableID == q.table {
		return next, ec2Invalid("InvalidParameterValue", "This subnet already uses the requested route table.")
	}
	if _, err := listNativeRouteAssociations(ctx, tx, region); err != nil {
		return next, err
	}
	if err := verifyNativeReplacementDocument(ctx, tx, old); err != nil {
		return next, err
	}
	vpc, err := nativeRouteAssociationEndpoints(ctx, tx, region, q.table, old.SubnetID)
	if err != nil {
		return next, err
	}
	if vpc != old.VPCID {
		return next, errEC2NativeRouteAssociationState
	}
	var entropy [9]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return next, err
	}
	next = EC2NetworkResource{ID: "rtbassoc-" + hex.EncodeToString(entropy[:])[:17],
		Kind: ec2Assoc, Region: region, StackID: ec2NativeOwner, VPCID: vpc,
		SubnetID: old.SubnetID, RouteTableID: q.table, Revision: 1}
	if next.ID == old.ID {
		return EC2NetworkResource{}, errEC2NativeRouteAssociationState
	}
	if _, err := networkRead(ctx, tx, region, next.ID); !errors.Is(err, sql.ErrNoRows) {
		return EC2NetworkResource{}, errEC2NativeRouteAssociationState
	}
	before, err := nativeRouteReplacementWitness(ctx, tx, region, old, next)
	if err != nil {
		return EC2NetworkResource{}, err
	}
	if err := deleteReplacedAssociation(ctx, tx, old); err != nil {
		return EC2NetworkResource{}, err
	}
	// The same transaction releases then reuses one resource slot, even
	// at the existing quota. Native readers never observe an interim gap.
	if err := createEC2Network(ctx, tx, next); err != nil {
		return EC2NetworkResource{}, err
	}
	if _, err := networkRead(ctx, tx, region, old.ID); !errors.Is(err, sql.ErrNoRows) {
		return EC2NetworkResource{}, errEC2NativeRouteAssociationState
	}
	if err := verifyNativeReplacementDocument(ctx, tx, next); err != nil {
		return EC2NetworkResource{}, err
	}
	if _, err := listNativeRouteAssociations(ctx, tx, region); err != nil {
		return EC2NetworkResource{}, err
	}
	after, err := nativeRouteReplacementWitness(ctx, tx, region, old, next)
	if err != nil {
		return EC2NetworkResource{}, err
	}
	if before != after {
		return EC2NetworkResource{}, errEC2NativeRouteAssociationState
	}
	if err := tx.Commit(); err != nil {
		return EC2NetworkResource{}, err
	}
	return next, nil
}
