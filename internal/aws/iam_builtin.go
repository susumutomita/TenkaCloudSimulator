// Copyright (c) 2026 TenkaCloudSimulator contributors
// SPDX-License-Identifier: MIT
package aws

import (
	"context"
	"database/sql"
)

// Pinned from the official AWS reference on 2026-11-09; see docs/iam-ssm-managed-policy.md.
// These permissions are not implementations of SSM agent or session APIs.
const iamSSMCoreARN = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
const iamSSMCoreName = "AmazonSSMManagedInstanceCore"

// Slash cannot occur in a customer policy name. Keep both namespaces distinct,
// including customer policies that have the same display name as this builtin.
const iamSSMCoreKey = "/aws/AmazonSSMManagedInstanceCore"
const iamSSMCoreID = "ANPA0000000000000001" // Stable synthetic simulator ID, not an AWS policy ID.
const iamSSMCoreCreated = "2019-03-15T17:22:00Z"
const iamSSMCoreUpdated = "2019-05-23T16:54:00Z"
const iamSSMCoreDescription = "The policy for Amazon EC2 Role to enable AWS Systems Manager service core functionality."
const iamSSMCoreDocument = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ssm:DescribeAssociation","ssm:GetDeployablePatchSnapshotForInstance","ssm:GetDocument","ssm:DescribeDocument","ssm:GetManifest","ssm:GetParameter","ssm:GetParameters","ssm:ListAssociations","ssm:ListInstanceAssociations","ssm:PutInventory","ssm:PutComplianceItems","ssm:PutConfigurePackageResult","ssm:UpdateAssociationStatus","ssm:UpdateInstanceAssociationStatus","ssm:UpdateInstanceInformation"],"Resource":"*"},{"Effect":"Allow","Action":["ssmmessages:CreateControlChannel","ssmmessages:CreateDataChannel","ssmmessages:OpenControlChannel","ssmmessages:OpenDataChannel"],"Resource":"*"},{"Effect":"Allow","Action":["ec2messages:AcknowledgeMessage","ec2messages:DeleteMessage","ec2messages:FailMessage","ec2messages:GetEndpoint","ec2messages:GetMessages","ec2messages:SendReply"],"Resource":"*"}]}`

func iamManagedKey(p iamManagedRecord) string {
	if p.Arn == iamSSMCoreARN {
		return iamSSMCoreKey
	}
	return p.PolicyName
}
func iamManagedKeyARN(key string) string {
	if key == iamSSMCoreKey {
		return iamSSMCoreARN
	}
	return iamManagedPrefix + key
}
func iamManagedReadOnly(arn string) error {
	if arn == iamSSMCoreARN {
		return ssmFailure{"UnmodifiableEntity", "The pinned AWS-managed policy is read-only."}
	}
	return nil
}

// Initialize once, never repair or overwrite existing builtin state. Reads
// validate its exact pin; persisted corruption must fail rather than grant.
func iamBuiltinSchema(db *sql.DB) error {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM aws_iam_managed_policies WHERE name=?", iamSSMCoreKey).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if err = iamExactlyOne(tx.Exec(`INSERT INTO aws_iam_managed_policies VALUES(?,?,?,?,?,?,2,3,0)`, iamSSMCoreKey, iamSSMCoreID, "/", iamSSMCoreDescription, iamSSMCoreCreated, iamSSMCoreUpdated)); err != nil {
			return err
		}
		if err = iamExactlyOne(tx.Exec(`INSERT INTO aws_iam_managed_policy_versions VALUES(?,?,2,?,?)`, iamSSMCoreKey, iamSSMCoreID, iamSSMCoreDocument, iamSSMCoreUpdated)); err != nil {
			return err
		}
	}
	if _, err = iamManagedRead(ctx, tx, iamSSMCoreARN); err != nil {
		return err
	}
	return tx.Commit()
}
