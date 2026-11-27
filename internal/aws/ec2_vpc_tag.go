package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"strings"
)

const (
	ec2CreateVPCTag = "CreateTags"
	ec2DeleteVPCTag = "DeleteTags"
)

var errNativeEC2TagState = errors.New("inconsistent local native EC2 tag state")

type ec2NativeTagQuery struct {
	id, resourceType string
	key, value       string
	hasValue         bool
}

type ec2VPCTagResponse struct {
	XMLName   xml.Name
	XMLNS     string `xml:"xmlns,attr"`
	RequestID string `xml:"requestId"`
	Return    bool   `xml:"return"`
}

func parseEC2NativeTag(action string, fields map[string][]string) (ec2Request, *ec2Failure) {
	var q ec2Request
	for key, values := range fields {
		if len(values) != 1 {
			return q, ec2Invalid("InvalidParameterValue", "Tag query parameters must be sent once.")
		}
		value := values[0]
		switch key {
		case "Action", "Version":
		case "ResourceId.1":
			q.nativeTag.id = value
		case "Tag.1.Key":
			q.nativeTag.key = value
		case "Tag.1.Value":
			q.nativeTag.value, q.nativeTag.hasValue = value, true
		case "DryRun":
			if value != "false" {
				return q, ec2Invalid("UnsupportedOperation", "Only DryRun=false is supported for local native tags.")
			}
		default:
			return q, ec2Invalid("UnsupportedOperation", "Only one EC2-managed VPC or subnet and one tag per request are supported.")
		}
	}
	if q.nativeTag.id == "" || len(fields["Tag.1.Key"]) == 0 || action == ec2CreateVPCTag && !q.nativeTag.hasValue {
		return q, ec2Invalid("MissingParameter", "Supply one VPC or subnet ID and one tag key; CreateTags also requires an explicit value.")
	}
	switch {
	case ec2VPCIDPattern.MatchString(q.nativeTag.id):
		q.nativeTag.resourceType = "vpc"
	case ec2SubnetIDPattern.MatchString(q.nativeTag.id):
		q.nativeTag.resourceType = "subnet"
	case strings.HasPrefix(q.nativeTag.id, "subnet-"):
		return q, ec2Invalid("InvalidSubnetID.Malformed", "ResourceId.1 is not a valid subnet ID.")
	default:
		return q, ec2Invalid("InvalidVpcID.Malformed", "Only a single VPC or subnet ID is supported for local tag changes.")
	}
	if !validNativeEC2Tags(map[string]string{q.nativeTag.key: q.nativeTag.value}) {
		return q, ec2Invalid("InvalidParameterValue", "Tags must be valid XML text; keys are 1–128 UTF-8 bytes, values are at most 256 bytes, and aws: keys are reserved.")
	}
	return q, nil
}

func validNativeEC2Tags(tags map[string]string) bool {
	if len(tags) > 50 {
		return false
	}
	members := make([]any, 0, len(tags))
	for key, value := range tags {
		if !validNetworkACLTagText(key) || !validNetworkACLTagText(value) {
			return false
		}
		members = append(members, map[string]any{"Key": key, "Value": value})
	}
	_, err := networkTags(members)
	return err == nil
}

func readNativeVPCTagTarget(ctx context.Context, tx *sql.Tx, region, id string) (EC2NetworkResource, string, error) {
	var declared map[string]bool
	vpc, err := nativeRouteTableVPC(ctx, tx, region, id, &declared)
	if err != nil {
		var f *ec2Failure
		if errors.As(err, &f) && f.code == "UnsupportedOperation" {
			return vpc, "", ec2Invalid("UnsupportedOperation", "Native tag operations cannot change a CloudFormation-owned VPC.")
		}
		return vpc, "", err
	}
	if declared == nil { // A supported legacy VPC need not have a main route table.
		_, declared, err = regionalGatewayStacks(ctx, tx, region)
		if err != nil {
			return vpc, "", err
		}
	}
	if declared[id] || !validNativeDNSGeneration(vpc) || !validNativeEC2Tags(vpc.Tags) {
		return vpc, "", errNativeEC2TagState
	}
	return exactNativeTagDocument(ctx, tx, vpc)
}

// Refuse unknown fields, duplicate fields, or other byte formats that a
// metadata-only rewrite would otherwise normalize or silently discard.
func exactNativeTagDocument(ctx context.Context, tx *sql.Tx, row EC2NetworkResource) (EC2NetworkResource, string, error) {
	var prior string
	encoded, err := json.Marshal(row)
	if err != nil {
		return row, "", err
	}
	if err := tx.QueryRowContext(ctx, `SELECT document FROM aws_ec2_network_resources WHERE region=? AND id=?`, row.Region, row.ID).Scan(&prior); err != nil {
		return row, "", err
	}
	if prior != string(encoded) {
		return row, "", errNativeEC2TagState
	}
	return row, prior, nil
}

func (s *StackStore) mutateNativeEC2Tag(ctx context.Context, action, region string, q ec2NativeTagQuery) error {
	load := readNativeVPCTagTarget
	switch q.resourceType {
	case "vpc":
	case "subnet":
		load = readNativeSubnetTagTarget
	default:
		return ec2Invalid("UnsupportedOperation", "Only native VPC and subnet tag changes are supported.")
	}
	// core.New takes an immediate SQLite lock before either reading or writing.
	// DNS uses the same transaction boundary; the offline route repair instead
	// takes an exclusive lock on separately witnessed stopped DB state.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, prior, err := load(ctx, tx, region, q.id)
	if err != nil {
		return err
	}
	old, exists := row.Tags[q.key]
	switch action {
	case ec2CreateVPCTag:
		if exists && old == q.value {
			return tx.Commit()
		}
		if row.Tags == nil {
			row.Tags = map[string]string{}
		}
		row.Tags[q.key] = q.value
		if !validNativeEC2Tags(row.Tags) {
			// Both the old map and the new pair have passed the same validator.
			return ec2Invalid("TagLimitExceeded", "Local resources allow up to 50 tags.")
		}
	case ec2DeleteVPCTag:
		if !exists || q.hasValue && old != q.value {
			return tx.Commit()
		}
		delete(row.Tags, q.key)
	default:
		return ec2Invalid("UnsupportedOperation", "Only native VPC and subnet tag changes are supported.")
	}
	next, err := json.Marshal(row)
	if err != nil {
		return err
	}
	// Tags are metadata, not a topology or DNS revision. Match the exact old
	// blob as well as the generation and exact native resource identity. No
	// sibling row, parent link, generation, ownership or other tag is rewritten.
	var changedBefore int64
	if q.resourceType == "subnet" {
		// Reject extra writes, including unexpected trigger effects on the
		// VPC, siblings or routes, without changing the prepared document.
		if err := tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&changedBefore); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE aws_ec2_network_resources SET document=?
		WHERE region=? AND id=? AND kind=? AND stack_id=? AND revision=? AND document=?`,
		string(next), region, q.id, row.Kind, ec2NativeOwner, row.Revision, prior)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errNativeEC2TagState
	}
	if q.resourceType == "subnet" {
		var changedAfter int64
		if err := tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&changedAfter); err != nil || changedAfter-changedBefore != 1 {
			return errNativeEC2TagState
		}
	}
	if _, observed, err := load(ctx, tx, region, q.id); err != nil || observed != string(next) {
		return errNativeEC2TagState
	}
	return tx.Commit()
}
