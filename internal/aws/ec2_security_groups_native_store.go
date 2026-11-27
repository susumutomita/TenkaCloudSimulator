package aws

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"strings"
)

const ec2NativeSGNetworkLimit = 1000 // same DB-wide network-resource bound as native VPCs

var errEC2NativeSecurityGroupState = errors.New("inconsistent native security group metadata")

type ec2NativeSecurityGroup struct {
	EC2NetworkResource
	Name       string
	parentCIDR string
	rules      []ec2NativeSGRule
}

func initEC2NativeSecurityGroups(db *sql.DB) error {
	// The existing shared network row owns VPC dependency and tags. The typed
	// name row binds the same ID, vpc parent and case-insensitive name without
	// changing the representation or wire fields of CloudFormation groups.
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_ec2_native_security_groups(
		region TEXT NOT NULL,
		id TEXT NOT NULL,
		vpc_id TEXT NOT NULL,
		name TEXT NOT NULL COLLATE NOCASE CHECK(length(name) BETWEEN 1 AND 255),
		PRIMARY KEY(region,id),
		UNIQUE(region,vpc_id,name),
		FOREIGN KEY(region,id) REFERENCES aws_ec2_network_resources(region,id) ON DELETE RESTRICT,
		FOREIGN KEY(region,vpc_id) REFERENCES aws_ec2_network_resources(region,id) ON DELETE RESTRICT,
		CHECK(id<>vpc_id)
	) WITHOUT ROWID`)
	if err != nil {
		return err
	}
	return initEC2NativeSGRules(db)
}

func checkEC2NativeSGParent(ctx context.Context, tx *sql.Tx, region, id string) (EC2NetworkResource, error) {
	vpc, err := networkRead(ctx, tx, region, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && vpc.Kind != "AWS::EC2::VPC") {
		return EC2NetworkResource{}, ec2Invalid("InvalidVpcID.NotFound", "The parent VPC does not exist in this region.")
	}
	if err != nil {
		return EC2NetworkResource{}, err
	}
	if vpc.StackID != ec2NativeOwner {
		return EC2NetworkResource{}, ec2Invalid("UnsupportedOperation", "Only EC2-managed VPCs can own a native security group.")
	}
	prefix, err := netip.ParsePrefix(vpc.CIDR)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() < 16 || prefix.Bits() > 28 || prefix.Masked().String() != vpc.CIDR {
		return EC2NetworkResource{}, errEC2NativeSecurityGroupState
	}
	claims, err := cloudFormationClaims(ctx, tx, region, id)
	if err != nil {
		return EC2NetworkResource{}, err
	}
	if claims != 0 {
		return EC2NetworkResource{}, errEC2NativeSecurityGroupState
	}
	return vpc, nil
}

func readEC2NativeSecurityGroup(ctx context.Context, tx *sql.Tx, region, id string) (ec2NativeSecurityGroup, error) {
	var out ec2NativeSecurityGroup
	r, netErr := networkRead(ctx, tx, region, id)
	var name, vpc string
	nameErr := tx.QueryRowContext(ctx, `SELECT vpc_id,name FROM aws_ec2_native_security_groups WHERE region=? AND id=?`, region, id).Scan(&vpc, &name)
	if netErr != nil && !errors.Is(netErr, sql.ErrNoRows) {
		return out, netErr
	}
	if nameErr != nil && !errors.Is(nameErr, sql.ErrNoRows) {
		return out, nameErr
	}
	if netErr != nil || r.Kind != ec2SecurityGroupKind || r.StackID != ec2NativeOwner {
		if nameErr == nil {
			return out, errEC2NativeSecurityGroupState
		}
		if netErr == nil && r.Kind == ec2SecurityGroupKind {
			return out, ec2Invalid("UnsupportedOperation", "This security group is managed by CloudFormation; make changes through its stack.")
		}
		return out, ec2Invalid("InvalidGroup.NotFound", "The security group does not exist in this region.")
	}
	if nameErr != nil || !ec2NativeSecurityGroupID.MatchString(id) || vpc != r.VPCID ||
		!ec2VPCIDPattern.MatchString(vpc) || invalidNativeSGName(name) != nil || name != strings.TrimRight(name, " ") ||
		!validNativeSGText(r.Description) || r.SubnetID != "" || r.GatewayID != "" || r.RouteTableID != "" ||
		r.CIDR != "" || r.AvailabilityZone != "" || r.DNSHostnames || r.DNSSupport || r.PublicIP ||
		r.RouteEnabled || len(r.Ingress) != 0 || r.Revision != 1 {
		return out, errEC2NativeSecurityGroupState
	}
	if len(r.Tags) > 50 {
		return out, errEC2NativeSecurityGroupState
	}
	for key, val := range r.Tags {
		if len(key) < 1 || len(key) > 128 || len(val) > 256 || strings.HasPrefix(strings.ToLower(key), "aws:") ||
			!ec2SecurityGroupXMLText(key) || !ec2SecurityGroupXMLText(val) {
			return out, errEC2NativeSecurityGroupState
		}
	}
	parent, err := checkEC2NativeSGParent(ctx, tx, region, vpc)
	if err != nil {
		return out, fmt.Errorf("%w: parent unavailable", errEC2NativeSecurityGroupState)
	}
	var claimed bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=?)`,
		stackResourceNamespace(region, ec2SecurityGroupKind), ec2SecurityGroupKind, id).Scan(&claimed)
	if err != nil {
		return out, err
	}
	if claimed {
		return out, errEC2NativeSecurityGroupState
	}
	rules, err := readEC2NativeSGRules(ctx, tx, region, id, parent.CIDR)
	if err != nil {
		return out, err
	}
	return ec2NativeSecurityGroup{EC2NetworkResource: r, Name: name, parentCIDR: parent.CIDR, rules: rules}, nil
}

func (s *StackStore) createEC2NativeSecurityGroup(ctx context.Context, region string, q ec2NativeSecurityGroupQuery) (ec2NativeSecurityGroup, error) {
	var out ec2NativeSecurityGroup
	var entropy [9]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return out, err
	}
	id := "sg-" + hex.EncodeToString(entropy[:])[:17]
	if q.authorizeCreatedID != nil {
		if err := q.authorizeCreatedID(id); err != nil {
			return out, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := checkEC2NativeSGParent(ctx, tx, region, q.vpc); err != nil {
		return out, err
	}
	var duplicate bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_native_security_groups WHERE region=? AND vpc_id=? AND name=?)`,
		region, q.vpc, q.name).Scan(&duplicate); err != nil {
		return out, err
	}
	if duplicate {
		return out, ec2Invalid("InvalidGroup.Duplicate", "A security group with this name already exists in the parent VPC.")
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_network_resources`).Scan(&count); err != nil {
		return out, err
	}
	if count >= ec2NativeSGNetworkLimit {
		return out, ec2Invalid("SecurityGroupLimitExceeded", "The local network resource bound has been reached.")
	}
	r := EC2NetworkResource{ID: id, Kind: ec2SecurityGroupKind, Region: region, StackID: ec2NativeOwner, VPCID: q.vpc,
		Description: q.description, Tags: q.tags, Revision: 1}
	if err := networkWrite(ctx, tx, r, true); err != nil {
		return out, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO aws_ec2_native_security_groups(region,id,vpc_id,name) VALUES(?,?,?,?)`, region, id, q.vpc, q.name)
	if err != nil {
		return out, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return out, errEC2NativeSecurityGroupState
	}
	out, err = readEC2NativeSecurityGroup(ctx, tx, region, id)
	if err != nil {
		return ec2NativeSecurityGroup{}, err
	}
	if out.Name != q.name || out.VPCID != q.vpc || out.Description != q.description || !maps.Equal(out.Tags, q.tags) || len(out.rules) != 0 {
		return ec2NativeSecurityGroup{}, errEC2NativeSecurityGroupState
	}
	return out, tx.Commit()
}

// Read alongside the existing CloudFormation results in the caller's one
// SQLite snapshot. The union detects a missing row on either side.
func listEC2NativeSecurityGroups(ctx context.Context, tx *sql.Tx, region string) ([]ec2NativeSecurityGroup, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM aws_ec2_network_resources WHERE region=? AND stack_id=? AND kind=?
		UNION SELECT id FROM aws_ec2_native_security_groups WHERE region=? ORDER BY id LIMIT ?`,
		region, ec2NativeOwner, ec2SecurityGroupKind, region, ec2NativeSGNetworkLimit+1)
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
	if len(ids) > ec2NativeSGNetworkLimit {
		return nil, errEC2NativeSecurityGroupState
	}
	out := make([]ec2NativeSecurityGroup, 0, len(ids))
	for _, id := range ids {
		g, err := readEC2NativeSecurityGroup(ctx, tx, region, id)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

func (s *StackStore) deleteEC2NativeSecurityGroup(ctx context.Context, region, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	g, err := readEC2NativeSecurityGroup(ctx, tx, region, id)
	if err != nil {
		return err
	}
	// The FK atomically removes detached rules only. Interfaces and instances
	// remain unsupported; a future attachment operation needs its own guard.
	result, err := tx.ExecContext(ctx, `DELETE FROM aws_ec2_native_security_groups WHERE region=? AND id=? AND vpc_id=?`, region, id, g.VPCID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errEC2NativeSecurityGroupState
	}
	result, err = tx.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources WHERE region=? AND id=? AND stack_id=? AND kind=? AND revision=?`,
		region, id, ec2NativeOwner, ec2SecurityGroupKind, g.Revision)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errEC2NativeSecurityGroupState
	}
	if _, err := networkRead(ctx, tx, region, id); !errors.Is(err, sql.ErrNoRows) {
		return errEC2NativeSecurityGroupState
	}
	var stillThere bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_native_security_groups WHERE region=? AND id=?
		UNION ALL SELECT 1 FROM aws_ec2_native_security_group_rules WHERE region=? AND group_id=?)`, region, id, region, id).Scan(&stillThere)
	if err != nil || stillThere {
		return errEC2NativeSecurityGroupState
	}
	return tx.Commit()
}
