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
	"fmt"
	"maps"
)

const (
	ec2NetworkACLKind        = "AWS::EC2::NetworkAcl"
	ec2NetworkACLVPCType     = "AWS::EC2::VPC"
	ec2NetworkACLRowLimit    = 1000 // shared EC2 network resource bound
	ec2NetworkACLReplayLimit = 4096 // including retired ACLs; never evict into a fresh create
)

var errNetworkACLState = errors.New("inconsistent detached network ACL state")

// Initialize in the shared StackStore constructor. ACLs themselves live in
// aws_ec2_network_resources, so the existing native VPC deletion guard sees
// them. This receipt table keeps retirements only to prevent late retries
// from silently creating a second resource; only digests are retained.
func initEC2NetworkACLReplay(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_ec2_network_acl_replay(
		region TEXT NOT NULL,
		token_hash BLOB NOT NULL CHECK(length(token_hash)=32),
		request_hash BLOB NOT NULL CHECK(length(request_hash)=32),
		acl_id TEXT NOT NULL,
		PRIMARY KEY(region,token_hash)
	) WITHOUT ROWID`)
	return err
}

func nativeNetworkACLParent(ctx context.Context, tx *sql.Tx, region, id string) error {
	v, err := networkRead(ctx, tx, region, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && v.Kind != ec2NetworkACLVPCType) {
		return ec2Invalid("InvalidVpcID.NotFound", "The parent VPC does not exist in this region.")
	}
	if err != nil {
		return err
	}
	if v.StackID != ec2NativeOwner {
		return ec2Invalid("UnsupportedOperation", "Only EC2-managed VPCs can own detached native network ACLs.")
	}
	claims, err := cloudFormationClaims(ctx, tx, region, id)
	if err != nil {
		return err
	}
	if claims != 0 {
		return errNetworkACLState
	}
	return nil
}

func nativeNetworkACLClaims(ctx context.Context, tx *sql.Tx, region, id string) error {
	var claimed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=?)`,
		stackResourceNamespace(region, ec2NetworkACLKind), ec2NetworkACLKind, id).Scan(&claimed)
	if err != nil {
		return err
	}
	if claimed {
		return errNetworkACLState
	}
	return nil
}

func readNativeNetworkACL(ctx context.Context, tx *sql.Tx, region, id string) (EC2NetworkResource, error) {
	r, err := networkRead(ctx, tx, region, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (r.Kind != ec2NetworkACLKind || r.StackID != ec2NativeOwner)) {
		return EC2NetworkResource{}, ec2Invalid("InvalidNetworkAclID.NotFound", "The detached network ACL does not exist in this region.")
	}
	if err != nil {
		return EC2NetworkResource{}, err
	}
	// Native entries are checked by networkRead. An old ACL document has
	// no entry state and is valid only at its original generation.
	// Associations and other network-kind fields are not used by ACLs.
	if !ec2NetworkACLIDPattern.MatchString(id) || !ec2VPCIDPattern.MatchString(r.VPCID) ||
		r.SubnetID != "" || r.GatewayID != "" || r.RouteTableID != "" || r.CIDR != "" || r.AvailabilityZone != "" ||
		r.Description != "" || r.DNSHostnames || r.DNSSupport || r.PublicIP || r.RouteEnabled || len(r.Ingress) != 0 ||
		(r.NativeACLRules == nil && r.Revision != 1) {
		return EC2NetworkResource{}, errNetworkACLState
	}
	if len(r.Tags) > 50 {
		return EC2NetworkResource{}, errNetworkACLState
	}
	rawTags := make([]any, 0, len(r.Tags))
	for key, value := range r.Tags {
		if !validNetworkACLTagText(key) || !validNetworkACLTagText(value) {
			return EC2NetworkResource{}, errNetworkACLState
		}
		rawTags = append(rawTags, map[string]any{"Key": key, "Value": value})
	}
	if _, err := networkTags(rawTags); err != nil {
		return EC2NetworkResource{}, errNetworkACLState
	}
	if err := nativeNetworkACLParent(ctx, tx, region, r.VPCID); err != nil {
		return EC2NetworkResource{}, fmt.Errorf("%w: parent unavailable", errNetworkACLState)
	}
	if err := nativeNetworkACLClaims(ctx, tx, region, id); err != nil {
		return EC2NetworkResource{}, err
	}
	return r, nil
}

func nativeNetworkACLRequestHash(q ec2NetworkACLRequest) ([32]byte, error) {
	data, err := json.Marshal(struct {
		VPC  string
		Tags map[string]string
	}{q.vpcID, q.tags})
	return sha256.Sum256(data), err
}

type nativeNetworkACLCreateResult struct {
	EC2NetworkResource
	record ec2NetworkACLRead
}

func (s *StackStore) createNativeNetworkACL(ctx context.Context, region string, q ec2NetworkACLRequest) (nativeNetworkACLCreateResult, error) {
	var result nativeNetworkACLCreateResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var tokenHash, requestHash [32]byte
	if q.clientToken != "" {
		tokenHash = sha256.Sum256([]byte(q.clientToken))
		requestHash, err = nativeNetworkACLRequestHash(q)
		if err != nil {
			return result, err
		}
		var storedHash []byte
		var id string
		err = tx.QueryRowContext(ctx, `SELECT request_hash,acl_id FROM aws_ec2_network_acl_replay WHERE region=? AND token_hash=?`, region, tokenHash[:]).Scan(&storedHash, &id)
		if err == nil {
			if len(storedHash) != sha256.Size || !ec2NetworkACLIDPattern.MatchString(id) {
				return result, errNetworkACLState
			}
			if q.authorizeCreateID != nil {
				if err := q.authorizeCreateID(id); err != nil {
					return result, err
				}
			}
			if !bytes.Equal(storedHash, requestHash[:]) {
				return result, ec2Invalid("IdempotentParameterMismatch", "ClientToken was already used with different parameters in this region.")
			}
			r, err := readNativeNetworkACL(ctx, tx, region, id)
			if err != nil {
				return result, err // a retired ACL never provisions a new ACL on retry
			}
			if r.VPCID != q.vpcID || !maps.Equal(r.Tags, q.tags) {
				return result, errNetworkACLState
			}
			// The same token and request only read the established ACL. An
			// attachment never retires it or turns the retry into a write.
			view, err := newNativeACLReadSnapshot(ctx, tx, region)
			if err != nil {
				return result, err
			}
			record, err := view.record(id)
			if err != nil {
				return result, err
			}
			if record.Default || record.ID != id || record.VPCID != r.VPCID {
				return result, errNetworkACLState
			}
			return nativeNetworkACLCreateResult{r, record}, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return result, err
		}
		var receipts int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_network_acl_replay`).Scan(&receipts); err != nil {
			return result, err
		}
		if receipts >= ec2NetworkACLReplayLimit {
			return result, ec2Invalid("NetworkAclLimitExceeded", "The local network ACL create-replay bound has been reached.")
		}
	}
	if err = nativeNetworkACLParent(ctx, tx, region, q.vpcID); err != nil {
		return result, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_network_resources`).Scan(&count); err != nil {
		return result, err
	}
	if count >= ec2NetworkACLRowLimit {
		return result, ec2Invalid("NetworkAclLimitExceeded", "The local network resource bound has been reached.")
	}
	var entropy [9]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return result, err
	}
	id := "acl-" + hex.EncodeToString(entropy[:])[:17]
	if err := rejectNativeDefaultACLID(ctx, tx, region, id); err != nil {
		return result, err
	}
	if q.authorizeCreateID != nil {
		if err := q.authorizeCreateID(id); err != nil {
			return result, err
		}
	}
	r := EC2NetworkResource{ID: id, Kind: ec2NetworkACLKind, Region: region, StackID: ec2NativeOwner, VPCID: q.vpcID, Tags: q.tags, Revision: 1}
	if err = networkWrite(ctx, tx, r, true); err != nil {
		return result, err
	}
	if err = nativeNetworkACLClaims(ctx, tx, region, id); err != nil {
		return result, err
	}
	if q.clientToken != "" {
		insert, err := tx.ExecContext(ctx, `INSERT INTO aws_ec2_network_acl_replay(region,token_hash,request_hash,acl_id) VALUES(?,?,?,?)`, region, tokenHash[:], requestHash[:], id)
		if err != nil {
			return result, err
		}
		if n, err := insert.RowsAffected(); err != nil || n != 1 {
			return result, errNetworkACLState
		}
		var storedHash []byte
		var storedID string
		if err := tx.QueryRowContext(ctx, `SELECT request_hash,acl_id FROM aws_ec2_network_acl_replay WHERE region=? AND token_hash=?`, region, tokenHash[:]).Scan(&storedHash, &storedID); err != nil {
			return result, err
		}
		if storedID != id || !bytes.Equal(storedHash, requestHash[:]) {
			return result, errNetworkACLState
		}
	}
	if err := validateDetachedNativeACLForWrite(ctx, tx, r); err != nil {
		return result, err
	}
	return nativeNetworkACLCreateResult{r, ec2NetworkACLRecord(r)}, tx.Commit()
}

func listNativeNetworkACLIDs(ctx context.Context, tx *sql.Tx, region string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM aws_ec2_network_resources WHERE region=? AND stack_id=? AND kind=? ORDER BY id LIMIT ?`,
		region, ec2NativeOwner, ec2NetworkACLKind, ec2NetworkACLRowLimit+1)
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
	if len(ids) > ec2NetworkACLRowLimit {
		return nil, errNetworkACLState
	}
	return ids, nil
}

func (s *StackStore) deleteNativeNetworkACL(ctx context.Context, region, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := rejectNativeDefaultACLID(ctx, tx, region, id); err != nil {
		return err
	}
	r, err := readNativeNetworkACL(ctx, tx, region, id)
	if err != nil {
		return err
	}
	if err := validateDetachedNativeACLForWrite(ctx, tx, r); err != nil {
		return err
	}
	before, err := nativeACLWriteCount(ctx, tx)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources WHERE region=? AND id=? AND kind=? AND stack_id=? AND revision=?`,
		region, id, ec2NetworkACLKind, ec2NativeOwner, r.Revision)
	if err != nil {
		return err
	}
	if err := nativeACLConfirmWrite(ctx, tx, res, before); err != nil {
		return fmt.Errorf("%w: %w", errNetworkACLState, err)
	}
	if _, err := networkRead(ctx, tx, region, id); !errors.Is(err, sql.ErrNoRows) {
		return errNetworkACLState
	}
	return tx.Commit()
}
