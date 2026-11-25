package aws

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
)

// Native EC2 instance lifecycle. RunInstances saves a launch record below an
// EC2-owned subnet and TerminateInstances removes it. No guest, address, disk
// or metadata service exists for a record: it is never reported as pending or
// running, only as simulatorRuntimeState not_launched. CloudFormation-owned
// instances keep their own path and are refused here.
const (
	ec2RuntimeNotLaunched  = "not_launched"
	ec2NativeInstanceLimit = 100
)

// ec2NativeInstance is the stored document. RequestHash binds a ClientToken to
// the request that first used it.
type ec2NativeInstance struct {
	ID, Region, SubnetID, VPCID, ImageID, InstanceType string
	ClientToken, RequestHash                           string `json:",omitempty"`
	Tags                                               map[string]string
}

const ec2NativeInstanceSchema = `CREATE TABLE IF NOT EXISTS aws_ec2_native_instances(region TEXT NOT NULL,id TEXT NOT NULL,subnet_id TEXT NOT NULL,client_token TEXT,document TEXT NOT NULL,PRIMARY KEY(region,id),UNIQUE(region,client_token),FOREIGN KEY(region,subnet_id) REFERENCES aws_ec2_network_resources(region,id) ON DELETE RESTRICT);`

type ec2RunInstancesResponse struct {
	XMLName   xml.Name          `xml:"RunInstancesResponse"`
	XMLNS     string            `xml:"xmlns,attr"`
	RequestID string            `xml:"requestId"`
	Owner     string            `xml:"ownerId"`
	Instances []ec2InstanceRead `xml:"instancesSet>item"`
}
type ec2InstanceState struct {
	Code int    `xml:"code"`
	Name string `xml:"name"`
}

// previousState is absent: the simulator never recorded a runtime state.
type ec2InstanceStateChange struct {
	ID      string           `xml:"instanceId"`
	Current ec2InstanceState `xml:"currentState"`
}
type ec2TerminateInstancesResponse struct {
	XMLName   xml.Name                 `xml:"TerminateInstancesResponse"`
	XMLNS     string                   `xml:"xmlns,attr"`
	RequestID string                   `xml:"requestId"`
	Instances []ec2InstanceStateChange `xml:"instancesSet>item"`
}

func ec2NativeInstanceRecord(v ec2NativeInstance) ec2InstanceRead {
	return ec2InstanceRead{ID: v.ID, Image: v.ImageID, Type: v.InstanceType, Subnet: v.SubnetID, VPC: v.VPCID, Runtime: ec2RuntimeNotLaunched, Tags: ec2ReadTags(v.Tags), ManagedBy: ec2ManagedByEC2}
}

func ec2LaunchHash(q ec2Request) string {
	// Map keys are marshalled in sorted order, so equal requests hash equally.
	raw, _ := json.Marshal([]any{q.imageID, q.instanceType, q.subnetID, q.tags})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// nativeInstanceScan validates one stored row against its key columns and its
// EC2-owned subnet. Any mismatch or stack claim is inconsistent state.
func nativeInstanceScan(ctx context.Context, tx *sql.Tx, region, id, subnet string, token sql.NullString, raw string) (ec2NativeInstance, error) {
	var v ec2NativeInstance
	if json.Unmarshal([]byte(raw), &v) != nil || v.ID != id || v.Region != region || v.SubnetID != subnet || v.ClientToken != token.String || (v.ClientToken != "") != token.Valid {
		return v, fmt.Errorf("native instance identity invariant failed")
	}
	parent, e := networkRead(ctx, tx, region, subnet)
	if e != nil || parent.Kind != "AWS::EC2::Subnet" || parent.StackID != ec2NativeOwner || parent.VPCID != v.VPCID {
		return v, fmt.Errorf("native instance subnet invariant failed")
	}
	claims, e := cloudFormationClaims(ctx, tx, region, "AWS::EC2::Instance", id)
	if e != nil {
		return v, e
	}
	if claims != 0 {
		return v, fmt.Errorf("EC2-owned instance has a stack claim")
	}
	return v, nil
}

func nativeInstances(ctx context.Context, tx *sql.Tx, region string) ([]ec2NativeInstance, error) {
	rows, e := tx.QueryContext(ctx, `SELECT id,subnet_id,client_token,document FROM aws_ec2_native_instances WHERE region=? ORDER BY id LIMIT 1001`, region)
	if e != nil {
		return nil, e
	}
	type stored struct {
		id, subnet, raw string
		token           sql.NullString
	}
	var all []stored
	for rows.Next() {
		var item stored
		if e = rows.Scan(&item.id, &item.subnet, &item.token, &item.raw); e != nil {
			rows.Close()
			return nil, e
		}
		all = append(all, item)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []ec2NativeInstance{}
	for _, item := range all {
		v, e := nativeInstanceScan(ctx, tx, region, item.id, item.subnet, item.token, item.raw)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}

// nativeInstanceRead finds one instance by ID, or by ClientToken when byToken.
func nativeInstanceRead(ctx context.Context, tx *sql.Tx, region, value string, byToken bool) (ec2NativeInstance, error) {
	query := `SELECT id,subnet_id,client_token,document FROM aws_ec2_native_instances WHERE region=? AND id=?`
	if byToken {
		query = `SELECT id,subnet_id,client_token,document FROM aws_ec2_native_instances WHERE region=? AND client_token=?`
	}
	var id, subnet, raw string
	var token sql.NullString
	if e := tx.QueryRowContext(ctx, query, region, value).Scan(&id, &subnet, &token, &raw); e != nil {
		return ec2NativeInstance{}, e
	}
	return nativeInstanceScan(ctx, tx, region, id, subnet, token, raw)
}

// runNativeInstance records one launch. A repeated ClientToken returns the
// instance it first created when the request is the same, and fails otherwise.
func (s *StackStore) runNativeInstance(ctx context.Context, region string, q ec2Request) (ec2NativeInstance, error) {
	id, e := newEC2ID("i-")
	if e != nil {
		return ec2NativeInstance{}, e
	}
	v := ec2NativeInstance{ID: id, Region: region, SubnetID: q.subnetID, ImageID: q.imageID, InstanceType: q.instanceType, ClientToken: q.clientToken, Tags: q.tags}
	if v.Tags == nil {
		v.Tags = map[string]string{}
	}
	token := sql.NullString{String: q.clientToken, Valid: q.clientToken != ""}
	if token.Valid {
		v.RequestHash = ec2LaunchHash(q)
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return v, e
	}
	defer tx.Rollback()
	if token.Valid {
		existing, e := nativeInstanceRead(ctx, tx, region, q.clientToken, true)
		if e == nil {
			if existing.RequestHash != v.RequestHash {
				return v, ec2Invalid("IdempotentParameterMismatch", "The client token was already used with different parameters.")
			}
			return existing, tx.Commit()
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return v, e
		}
	}
	subnet, e := nativeParent(ctx, tx, region, q.subnetID, "AWS::EC2::Subnet", "subnet", "InvalidSubnetID.NotFound", "Launch its instances from the stack instead.")
	if e != nil {
		return v, e
	}
	if vpc, e := networkRead(ctx, tx, region, subnet.VPCID); e != nil || vpc.Kind != "AWS::EC2::VPC" || vpc.StackID != ec2NativeOwner {
		return v, fmt.Errorf("EC2-owned subnet has no EC2-owned VPC")
	}
	v.VPCID = subnet.VPCID
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_native_instances`).Scan(&count); e != nil {
		return v, e
	}
	if count >= ec2NativeInstanceLimit {
		return v, ec2Invalid("InstanceLimitExceeded", fmt.Sprintf("The local limit of %d EC2-owned instance records has been reached.", ec2NativeInstanceLimit))
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return v, e
	}
	// A duplicate ID violates the primary key and commits nothing.
	result, e := tx.ExecContext(ctx, `INSERT INTO aws_ec2_native_instances VALUES(?,?,?,?,?)`, region, v.ID, v.SubnetID, token, string(raw))
	if e != nil {
		return v, e
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		return v, fmt.Errorf("instance write was not acknowledged")
	}
	stored, e := nativeInstanceRead(ctx, tx, region, v.ID, false)
	if e != nil {
		return v, e
	}
	if actual, _ := json.Marshal(stored); string(actual) != string(raw) {
		return v, fmt.Errorf("instance write readback differs")
	}
	return v, tx.Commit()
}

func (s *StackStore) terminateNativeInstance(ctx context.Context, region, id string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = nativeInstanceRead(ctx, tx, region, id, false)
	if errors.Is(e, sql.ErrNoRows) {
		claims, e := cloudFormationClaims(ctx, tx, region, "AWS::EC2::Instance", id)
		if e != nil {
			return e
		}
		if claims != 0 {
			return ec2Invalid("UnsupportedOperation", "The instance '"+id+"' is managed by CloudFormation. Delete its stack instead.")
		}
		return ec2Invalid("InvalidInstanceID.NotFound", "The instance ID '"+id+"' does not exist")
	}
	if e != nil {
		return e
	}
	result, e := tx.ExecContext(ctx, `DELETE FROM aws_ec2_native_instances WHERE region=? AND id=?`, region, id)
	if e != nil {
		return e
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		return fmt.Errorf("instance delete was not acknowledged")
	}
	return tx.Commit()
}
