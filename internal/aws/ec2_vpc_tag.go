package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
)

const (
	ec2CreateVPCTag = "CreateTags"
	ec2DeleteVPCTag = "DeleteTags"
)

var errVPCTagState = errors.New("inconsistent local native VPC tag state")

type ec2VPCTagQuery struct {
	key, value string
	hasValue   bool
}

type ec2VPCTagResponse struct {
	XMLName   xml.Name
	XMLNS     string `xml:"xmlns,attr"`
	RequestID string `xml:"requestId"`
	Return    bool   `xml:"return"`
}

func parseEC2VPCTag(action string, fields map[string][]string) (ec2Request, *ec2Failure) {
	var q ec2Request
	for key, values := range fields {
		if len(values) != 1 {
			return q, ec2Invalid("InvalidParameterValue", "Tag query parameters must be sent once.")
		}
		value := values[0]
		switch key {
		case "Action", "Version":
		case "ResourceId.1":
			q.vpcID = value
		case "Tag.1.Key":
			q.vpcTag.key = value
		case "Tag.1.Value":
			q.vpcTag.value, q.vpcTag.hasValue = value, true
		case "DryRun":
			if value != "false" {
				return q, ec2Invalid("UnsupportedOperation", "Only DryRun=false is supported for local VPC tags.")
			}
		default:
			return q, ec2Invalid("UnsupportedOperation", "Only one EC2-managed VPC and one tag per request are supported.")
		}
	}
	if q.vpcID == "" || len(fields["Tag.1.Key"]) == 0 || action == ec2CreateVPCTag && !q.vpcTag.hasValue {
		return q, ec2Invalid("MissingParameter", "Supply one VPC ID and one tag key; CreateTags also requires an explicit value.")
	}
	if !ec2VPCIDPattern.MatchString(q.vpcID) {
		return q, ec2Invalid("InvalidVpcID.Malformed", "Only a single VPC ID is supported for local tag changes.")
	}
	if !validNativeVPCTags(map[string]string{q.vpcTag.key: q.vpcTag.value}) {
		return q, ec2Invalid("InvalidParameterValue", "Tags must be valid XML text; keys are 1–128 UTF-8 bytes, values are at most 256 bytes, and aws: keys are reserved.")
	}
	return q, nil
}

func validNativeVPCTags(tags map[string]string) bool {
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
	if declared[id] || !validNativeDNSGeneration(vpc) || !validNativeVPCTags(vpc.Tags) {
		return vpc, "", errVPCTagState
	}
	var prior string
	encoded, err := json.Marshal(vpc)
	if err != nil {
		return vpc, "", err
	}
	// Refuse unknown fields, duplicate fields, or other byte formats that a
	// metadata-only rewrite would otherwise normalize or silently discard.
	if err := tx.QueryRowContext(ctx, `SELECT document FROM aws_ec2_network_resources WHERE region=? AND id=?`, region, id).Scan(&prior); err != nil {
		return vpc, "", err
	}
	if prior != string(encoded) {
		return vpc, "", errVPCTagState
	}
	return vpc, prior, nil
}

func (s *StackStore) mutateNativeVPCTag(ctx context.Context, action, region, id string, q ec2VPCTagQuery) error {
	// core.New takes an immediate SQLite lock before either reading or writing.
	// DNS uses the same transaction boundary; the offline route repair instead
	// takes an exclusive lock on separately witnessed stopped DB state.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	vpc, prior, err := readNativeVPCTagTarget(ctx, tx, region, id)
	if err != nil {
		return err
	}
	old, exists := vpc.Tags[q.key]
	switch action {
	case ec2CreateVPCTag:
		if exists && old == q.value {
			return tx.Commit()
		}
		if vpc.Tags == nil {
			vpc.Tags = map[string]string{}
		}
		vpc.Tags[q.key] = q.value
		if !validNativeVPCTags(vpc.Tags) {
			// Both the old map and the new pair have passed the same validator.
			return ec2Invalid("TagLimitExceeded", "Local VPCs allow up to 50 tags.")
		}
	case ec2DeleteVPCTag:
		if !exists || q.hasValue && old != q.value {
			return tx.Commit()
		}
		delete(vpc.Tags, q.key)
	default:
		return ec2Invalid("UnsupportedOperation", "Only native VPC tag changes are supported.")
	}
	next, err := json.Marshal(vpc)
	if err != nil {
		return err
	}
	// Tags are metadata, not a topology or DNS revision. Match the exact old
	// blob as well as the generation and native VPC identity. No sibling row,
	// parent link, generation, ownership or other tag is rewritten.
	result, err := tx.ExecContext(ctx, `UPDATE aws_ec2_network_resources SET document=?
		WHERE region=? AND id=? AND kind=? AND stack_id=? AND revision=? AND document=?`,
		string(next), region, id, ec2RTVPC, ec2NativeOwner, vpc.Revision, prior)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errVPCTagState
	}
	if _, observed, err := readNativeVPCTagTarget(ctx, tx, region, id); err != nil || observed != string(next) {
		return errVPCTagState
	}
	return tx.Commit()
}
