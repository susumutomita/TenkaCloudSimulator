package aws

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const lambdaServiceTrust = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"Service":"lambda.amazonaws.com"}}]}`
const nativeReadPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::bucket/*"}]}`

func iamCode(err error) string {
	var failure ssmFailure
	if errors.As(err, &failure) {
		return failure.code
	}
	return ""
}

func TestNativeIAMRoleTrustPolicyAndRetiredOwnership(t *testing.T) {
	ctx := context.Background()
	db, _ := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	identities := core.NewIdentities(db)
	role, err := roles.Create(ctx, "CaseRole", lambdaServiceTrust, "local role", 3600)
	if err != nil {
		t.Fatal(err)
	}
	if role.RoleName != "CaseRole" || role.Arn != "arn:aws:iam::000000000000:role/CaseRole" || !strings.HasPrefix(role.RoleID, "AROA") || role.Path != "/" || role.CreateDate.IsZero() {
		t.Fatal(role)
	}
	if _, err = roles.Create(ctx, "caserole", lambdaServiceTrust, "collision", 3600); iamCode(err) != "EntityAlreadyExists" {
		t.Fatal("case-only role collision was accepted", err)
	}
	read, err := roles.Get(ctx, "CASEROLE")
	if err != nil || read.RoleID != role.RoleID {
		t.Fatal(read, err)
	}
	owned, allowed, err := roles.LambdaTrust(ctx, role.Arn)
	if err != nil || !owned || !allowed {
		t.Fatal(owned, allowed, err)
	}
	if decision, err := identities.Decision(role.RoleName, "s3:GetObject", "arn:aws:s3:::bucket/key"); err != nil || decision != "implicitDeny" {
		t.Fatal("an empty role granted service permissions", decision, err)
	}
	if _, err = roles.Policy(ctx, "CaseRole", "missing"); iamCode(err) != "NoSuchEntity" {
		t.Fatal(err)
	}
	if err = roles.PutPolicy(ctx, "caserole", "reader", nativeReadPolicy); err != nil {
		t.Fatal(err)
	}
	if decision, err := identities.Decision(role.RoleName, "s3:GetObject", "arn:aws:s3:::bucket/key"); err != nil || decision != "allowed" {
		t.Fatal("native inline policy not shared with runtime identity", decision, err)
	}
	if err = roles.PutPolicy(ctx, "CaseRole", "deny", strings.Replace(nativeReadPolicy, "Allow", "Deny", 1)); err != nil {
		t.Fatal(err)
	}
	if decision, _ := identities.Decision(role.RoleName, "s3:GetObject", "arn:aws:s3:::bucket/key"); decision != "explicitDeny" {
		t.Fatal(decision)
	}
	if err = roles.Delete(ctx, "CaseRole"); iamCode(err) != "DeleteConflict" {
		t.Fatal("role deleted while inline policies remained", err)
	}
	if err = roles.DeletePolicy(ctx, "caserole", "deny"); err != nil {
		t.Fatal(err)
	}
	if err = roles.DeletePolicy(ctx, "CaseRole", "reader"); err != nil {
		t.Fatal(err)
	}
	if err = roles.DeletePolicy(ctx, "CaseRole", "reader"); iamCode(err) != "NoSuchEntity" {
		t.Fatal(err)
	}
	if err = roles.UpdateTrust(ctx, "CaseRole", strings.Replace(lambdaServiceTrust, "lambda.amazonaws.com", "ec2.amazonaws.com", 1)); err != nil {
		t.Fatal(err)
	}
	if owned, allowed, err = roles.LambdaTrust(ctx, role.Arn); err != nil || !owned || allowed {
		t.Fatal("wrong service trust allowed Lambda", owned, allowed, err)
	}
	if err = roles.UpdateTrust(ctx, "CaseRole", lambdaServiceTrust); err != nil {
		t.Fatal(err)
	}
	if err = roles.Delete(ctx, "CaseRole"); err != nil {
		t.Fatal(err)
	}
	if _, err = roles.Get(ctx, "caserole"); iamCode(err) != "NoSuchEntity" {
		t.Fatal(err)
	}
	if owned, err = roles.Owns(ctx, "CASEROLE"); err != nil || !owned {
		t.Fatal("retired native name became a legacy identity", owned, err)
	}
	if owned, allowed, err = roles.LambdaTrust(ctx, role.Arn); err != nil || !owned || allowed {
		t.Fatal("deleted native role fell back to legacy trust", owned, allowed, err)
	}
	recreated, err := roles.Create(ctx, "CaseRole", lambdaServiceTrust, "recreated", 3600)
	if err != nil || recreated.RoleID == role.RoleID {
		t.Fatal(recreated, err)
	}
	if err = roles.UpdateTrust(ctx, "missing", lambdaServiceTrust); iamCode(err) != "NoSuchEntity" {
		t.Fatal(err)
	}
	if owned, allowed, err = roles.LambdaTrust(ctx, "arn:aws:iam::000000000000:role/legacy"); err != nil || owned || allowed {
		t.Fatal(owned, allowed, err)
	}
}

func TestNativeIAMRejectsUnknownTrustAndRollsBackAggregateLimit(t *testing.T) {
	ctx := context.Background()
	db, _ := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"{}", strings.Replace(lambdaServiceTrust, "\"Effect\":\"Allow\"", "\"Effect\":\"Allow\",\"Condition\":{}", 1), strings.Replace(lambdaServiceTrust, "sts:AssumeRole", "*", 1), strings.Replace(lambdaServiceTrust, "lambda.amazonaws.com", "*", 1)} {
		if _, err = roles.Create(ctx, "invalid", bad, "", 3600); iamCode(err) != "MalformedPolicyDocument" {
			t.Fatal(bad, err)
		}
	}
	for _, name := range []string{"simulator-admin", "SIMULATOR-ADMIN", "bad/name", ""} {
		if _, err = roles.Create(ctx, name, lambdaServiceTrust, "", 3600); iamCode(err) != "ValidationError" {
			t.Fatal(name, err)
		}
	}
	role, err := roles.Create(ctx, "limited", lambdaServiceTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	var trust map[string]any
	if err = json.Unmarshal([]byte(lambdaServiceTrust), &trust); err != nil {
		t.Fatal(err)
	}
	deny := map[string]any{"Effect": "Deny", "Action": "sts:AssumeRole", "Principal": map[string]any{"Service": "lambda.amazonaws.com"}}
	trust["Statement"] = append(trust["Statement"].([]any), deny)
	data, _ := json.Marshal(trust)
	if err = roles.UpdateTrust(ctx, "limited", string(data)); err != nil {
		t.Fatal(err)
	}
	if _, allow, err := roles.LambdaTrust(ctx, role.Arn); err != nil || allow {
		t.Fatal("trust Deny did not win", err)
	}
	if err = roles.UpdateTrust(ctx, "limited", "{}"); iamCode(err) != "MalformedPolicyDocument" {
		t.Fatal(err)
	}
	for n := 0; n < 10; n++ {
		name := string(rune('a' + n))
		if err = roles.PutPolicy(ctx, "limited", name, nativeReadPolicy); err != nil {
			t.Fatal(err)
		}
	}
	if err = roles.PutPolicy(ctx, "limited", "overflow", nativeReadPolicy); iamCode(err) != "LimitExceeded" {
		t.Fatal(err)
	}
	if _, err = roles.Policy(ctx, "limited", "overflow"); iamCode(err) != "NoSuchEntity" {
		t.Fatal("rejected aggregate left an inline policy", err)
	}
	if err = roles.PutPolicy(ctx, "limited", "a", nativeReadPolicy); err != nil {
		t.Fatal("replacement counted as a new policy", err)
	}
	if err = roles.PutPolicy(ctx, "limited", "bad/name", nativeReadPolicy); iamCode(err) != "MalformedPolicyDocument" {
		t.Fatal(err)
	}
	if err = roles.PutPolicy(ctx, "limited", "invalid", "{}"); iamCode(err) != "MalformedPolicyDocument" {
		t.Fatal(err)
	}
	if err = roles.PutPolicy(ctx, "missing", "a", nativeReadPolicy); iamCode(err) != "NoSuchEntity" {
		t.Fatal(err)
	}
	_, err = db.Database().Exec(`CREATE TRIGGER deny_native_aggregate_update BEFORE UPDATE ON identity_policies BEGIN SELECT RAISE(ABORT,'test aggregate fault'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = roles.PutPolicy(ctx, "limited", "a", strings.Replace(nativeReadPolicy, "Allow", "Deny", 1)); err == nil {
		t.Fatal("failed aggregate update reported success")
	}
	stored, err := roles.Policy(ctx, "limited", "a")
	if err != nil || strings.Contains(stored, "Deny") {
		t.Fatal("aggregate failure changed native inline policy", stored, err)
	}
	_, err = db.Database().Exec(`CREATE TRIGGER deny_native_role_insert BEFORE INSERT ON aws_iam_roles BEGIN SELECT RAISE(ABORT,'test insert fault'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = roles.Create(ctx, "rolled-back", lambdaServiceTrust, "", 3600); err == nil {
		t.Fatal("failed native role write reported success")
	}
	if owned, err := roles.Owns(ctx, "rolled-back"); err != nil || owned {
		t.Fatal("failed role creation retained ownership", owned, err)
	}
}
