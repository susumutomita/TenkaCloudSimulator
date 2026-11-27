package aws

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/netip"
	"strings"
)

const (
	ec2NativeRouteTableRowLimit    = 1000
	ec2NativeRouteTableReplayLimit = 4096
)

var errEC2NativeRouteTableState = errors.New("inconsistent detached native route table state")

// The table records only EC2-native create receipts. Capturing the generated
// ID also as a permanent tombstone prevents a delayed retry from re-creating
// a deleted route table; raw client tokens are not stored.
func initEC2NativeRouteTableReplay(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_ec2_native_route_table_replay(
		region TEXT NOT NULL,
		token_hash BLOB NOT NULL CHECK(length(token_hash)=32),
		request_hash BLOB NOT NULL CHECK(length(request_hash)=32),
		route_table_id TEXT NOT NULL,
		PRIMARY KEY(region,token_hash),
		UNIQUE(region,route_table_id)
	) WITHOUT ROWID`)
	return err
}

func nativeRouteTableUnclaimed(ctx context.Context, tx *sql.Tx, region, id string) error {
	var claimed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_cfn_ownership WHERE namespace=? AND physical_id=?)`,
		stackResourceNamespace(region, ec2RT), id).Scan(&claimed); err != nil {
		return err
	}
	if claimed {
		return errEC2NativeRouteTableState
	}
	return nil
}

func nativeRouteTableVPC(ctx context.Context, tx *sql.Tx, region, id string, declarations *map[string]bool) (EC2NetworkResource, error) {
	vpc, err := networkRead(ctx, tx, region, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && vpc.Kind != ec2RTVPC) {
		return EC2NetworkResource{}, ec2Invalid("InvalidVpcID.NotFound", "The parent VPC does not exist in this region.")
	}
	if err != nil {
		return EC2NetworkResource{}, err
	}
	if vpc.StackID != ec2NativeOwner {
		return EC2NetworkResource{}, ec2Invalid("UnsupportedOperation", "Native route tables require an EC2-managed VPC.")
	}
	cidr, err := netip.ParsePrefix(vpc.CIDR)
	if err != nil || !cidr.Addr().Is4() || cidr != cidr.Masked() || cidr.Bits() < 16 || cidr.Bits() > 28 {
		return EC2NetworkResource{}, errEC2NativeRouteTableState
	}
	if err := nativeRouteTableUnclaimed(ctx, tx, region, id); err != nil {
		return EC2NetworkResource{}, err
	}
	if err := verifyNativeVPCMainSlot(ctx, tx, vpc, declarations); err != nil {
		return EC2NetworkResource{}, err
	}
	return vpc, nil
}

func nativeRouteTableFields(r EC2NetworkResource) bool {
	return ec2NativeRouteTableID.MatchString(r.ID) && ec2VPCIDPattern.MatchString(r.VPCID) && (r.Revision == 1 || r.Revision == 2) &&
		r.MainRouteRepair == nil && r.MainRouteTableID == "" && r.SubnetID == "" && r.GatewayID == "" && r.RouteTableID == "" && r.AvailabilityZone == "" &&
		r.Description == "" && !r.DNSHostnames && !r.DNSSupport && !r.PublicIP && !r.RouteEnabled && len(r.Ingress) == 0 && len(r.Tags) <= 50
}

func readNativeRouteTable(ctx context.Context, tx *sql.Tx, region, id string) (EC2NetworkResource, error) {
	return readNativeRouteTableInSnapshot(ctx, tx, region, id, nil)
}

func readNativeRouteTableInSnapshot(ctx context.Context, tx *sql.Tx, region, id string, declarations *map[string]bool) (EC2NetworkResource, error) {
	r, err := networkRead(ctx, tx, region, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && r.Kind != ec2RT) {
		return EC2NetworkResource{}, ec2Invalid("InvalidRouteTableID.NotFound", "The route table does not exist in this region.")
	}
	if err != nil {
		return EC2NetworkResource{}, err
	}
	if r.StackID != ec2NativeOwner {
		return EC2NetworkResource{}, ec2Invalid("UnsupportedOperation", "This route table is CloudFormation-managed; remove it through its stack.")
	}
	if !nativeRouteTableFields(r) {
		return EC2NetworkResource{}, errEC2NativeRouteTableState
	}
	for key, val := range r.Tags {
		if len(key) < 1 || len(key) > 128 || len(val) > 256 || strings.HasPrefix(strings.ToLower(key), "aws:") ||
			!ec2SecurityGroupXMLText(key) || !ec2SecurityGroupXMLText(val) {
			return EC2NetworkResource{}, errEC2NativeRouteTableState
		}
	}
	vpc, err := nativeRouteTableVPC(ctx, tx, region, r.VPCID, declarations)
	if err != nil || r.CIDR != nativeTableLocalCIDR(vpc) || r.Revision == 2 && !validNativeVPCMainRepair(vpc) ||
		nativeMainTableID(region, r.VPCID) == r.ID && (vpc.MainRouteTableID != r.ID || len(r.Tags) != 0) {
		return EC2NetworkResource{}, errEC2NativeRouteTableState
	}
	if err := nativeRouteTableUnclaimed(ctx, tx, region, id); err != nil {
		return EC2NetworkResource{}, err
	}
	return r, nil
}

func listNativeRouteTables(ctx context.Context, tx *sql.Tx, region string) ([]EC2NetworkResource, error) {
	var declarations map[string]bool
	if err := checkNativeVPCMainRouteInventory(ctx, tx, region, &declarations); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM aws_ec2_network_resources
		WHERE region=? AND stack_id=? AND kind=? ORDER BY id LIMIT ?`,
		region, ec2NativeOwner, ec2RT, ec2NativeRouteTableRowLimit+1)
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
		return nil, errEC2NativeRouteTableState
	}
	out := make([]EC2NetworkResource, 0, len(ids))
	for _, id := range ids {
		r, err := readNativeRouteTableInSnapshot(ctx, tx, region, id, &declarations)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func nativeRouteTableRequestHash(q ec2NativeRouteTableQuery) ([32]byte, error) {
	data, err := json.Marshal(struct {
		VPC  string
		Tags map[string]string
	}{q.vpc, q.tags})
	return sha256.Sum256(data), err
}

func (s *StackStore) createNativeRouteTable(ctx context.Context, region string, q ec2NativeRouteTableQuery) (EC2NetworkResource, error) {
	var r EC2NetworkResource
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()

	var tokenHash, requestHash [32]byte
	if q.clientToken != "" {
		tokenHash = sha256.Sum256([]byte(q.clientToken))
		requestHash, err = nativeRouteTableRequestHash(q)
		if err != nil {
			return r, err
		}
		var storedHash []byte
		var id string
		err = tx.QueryRowContext(ctx, `SELECT request_hash,route_table_id FROM aws_ec2_native_route_table_replay WHERE region=? AND token_hash=?`,
			region, tokenHash[:]).Scan(&storedHash, &id)
		if err == nil {
			if len(storedHash) != sha256.Size || !ec2NativeRouteTableID.MatchString(id) {
				return r, errEC2NativeRouteTableState
			}
			if q.authorizeCreatedID != nil {
				if err := q.authorizeCreatedID(id); err != nil {
					return r, err
				}
			}
			if !bytes.Equal(storedHash, requestHash[:]) {
				return r, ec2Invalid("IdempotentParameterMismatch", "ClientToken already identifies a different route table request in this region.")
			}
			r, err = readNativeRouteTable(ctx, tx, region, id)
			if err != nil {
				return r, err
			}
			if isNativeMainTable(r) || r.VPCID != q.vpc || !maps.Equal(r.Tags, q.tags) {
				return EC2NetworkResource{}, errEC2NativeRouteTableState
			}
			return r, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
		var receipts int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_native_route_table_replay`).Scan(&receipts); err != nil {
			return r, err
		}
		if receipts >= ec2NativeRouteTableReplayLimit {
			return r, ec2Invalid("RouteTableLimitExceeded", "The local route table create-replay bound has been reached.")
		}
	}

	var entropy [9]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return r, err
	}
	id := "rtb-" + hex.EncodeToString(entropy[:])[:17]
	if q.authorizeCreatedID != nil {
		if err := q.authorizeCreatedID(id); err != nil {
			return r, err
		}
	}
	vpc, err := nativeRouteTableVPC(ctx, tx, region, q.vpc, nil)
	if err != nil {
		return r, err
	}
	if id == nativeMainTableID(region, q.vpc) {
		return r, errEC2NativeRouteTableState
	}
	var retired bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_native_route_table_replay WHERE region=? AND route_table_id=?)`,
		region, id).Scan(&retired); err != nil {
		return r, err
	}
	if retired {
		return r, errEC2NativeRouteTableState
	}
	r = EC2NetworkResource{ID: id, Kind: ec2RT, Region: region, StackID: ec2NativeOwner, VPCID: q.vpc, CIDR: nativeTableLocalCIDR(vpc), Tags: q.tags, Revision: 1}
	if err := createEC2Network(ctx, tx, r); err != nil {
		if errors.Is(err, errEC2NetworkQuota) {
			return EC2NetworkResource{}, ec2Invalid("RouteTableLimitExceeded", "The local network resource bound has been reached.")
		}
		return EC2NetworkResource{}, err
	}
	r, err = readNativeRouteTable(ctx, tx, region, id)
	if err != nil {
		return r, err
	}
	if r.VPCID != q.vpc || !maps.Equal(r.Tags, q.tags) {
		return EC2NetworkResource{}, errEC2NativeRouteTableState
	}
	if q.clientToken != "" {
		res, err := tx.ExecContext(ctx, `INSERT INTO aws_ec2_native_route_table_replay(region,token_hash,request_hash,route_table_id) VALUES(?,?,?,?)`,
			region, tokenHash[:], requestHash[:], id)
		if err != nil {
			return EC2NetworkResource{}, err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return EC2NetworkResource{}, errEC2NativeRouteTableState
		}
		var storedHash []byte
		var storedID string
		err = tx.QueryRowContext(ctx, `SELECT request_hash,route_table_id FROM aws_ec2_native_route_table_replay WHERE region=? AND token_hash=?`,
			region, tokenHash[:]).Scan(&storedHash, &storedID)
		if err != nil || storedID != id || !bytes.Equal(storedHash, requestHash[:]) {
			return EC2NetworkResource{}, errEC2NativeRouteTableState
		}
	}
	return r, tx.Commit()
}

func (s *StackStore) deleteNativeRouteTable(ctx context.Context, region, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := readNativeRouteTable(ctx, tx, region, id)
	if err != nil {
		return err
	}
	if isNativeMainTable(r) {
		return ec2Invalid("DependencyViolation", "The main route table is removed only with its VPC.")
	}
	routes, err := nativeIPv4RoutesInRegion(ctx, tx, region)
	if err != nil {
		return err
	}
	if nativeIPv4RoutesForTable(routes, id) > 0 {
		return ec2Invalid("DependencyViolation", "Delete this route table's saved IPv4 routes first.")
	}
	var referenced bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_network_resources
		WHERE region=? AND id<>? AND json_extract(document,'$.RouteTableID')=?)`, region, id, id).Scan(&referenced); err != nil {
		return err
	}
	if referenced {
		return ec2Invalid("DependencyViolation", "The route table has a network dependency and cannot be removed.")
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources WHERE region=? AND id=? AND stack_id=? AND kind=? AND revision=?`,
		region, id, ec2NativeOwner, ec2RT, r.Revision)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errEC2NativeRouteTableState
	}
	if _, err := networkRead(ctx, tx, region, id); !errors.Is(err, sql.ErrNoRows) {
		return errEC2NativeRouteTableState
	}
	currentRoutes, err := nativeIPv4RoutesInRegion(ctx, tx, region)
	if err != nil || !maps.Equal(routes, currentRoutes) {
		return errEC2NativeRouteTableState
	}
	return tx.Commit()
}
