package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestNativeIAMCancellationFailsWithoutWrites(t *testing.T) {
	db, _ := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	role, err := roles.Create(ctx, "cancel-role", lambdaServiceTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	if err = roles.PutPolicy(ctx, role.RoleName, "reader", nativeReadPolicy); err != nil {
		t.Fatal(err)
	}
	p, err := core.ParsePolicy([]byte(nativeReadPolicy))
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for name, action := range map[string]func() error{
		"create":         func() error { _, e := roles.Create(canceled, "late-role", lambdaServiceTrust, "", 3600); return e },
		"get":            func() error { _, e := roles.Get(canceled, role.RoleName); return e },
		"list":           func() error { _, e := roles.List(canceled, "", 100); return e },
		"policy":         func() error { _, e := roles.Policy(canceled, role.RoleName, "reader"); return e },
		"names":          func() error { _, e := roles.PolicyNames(canceled, role.RoleName); return e },
		"put":            func() error { return roles.PutPolicy(canceled, role.RoleName, "late", nativeReadPolicy) },
		"delete-policy":  func() error { return roles.DeletePolicy(canceled, role.RoleName, "reader") },
		"delete-role":    func() error { return roles.Delete(canceled, role.RoleName) },
		"trust":          func() error { return roles.UpdateTrust(canceled, role.RoleName, lambdaServiceTrust) },
		"legacy-put":     func() error { return roles.MutateIdentityPolicy(canceled, "late-identity", &p, false) },
		"legacy-delete":  func() error { return roles.MutateIdentityPolicy(canceled, "late-identity", nil, false) },
		"execution-role": func() error { return iamExecutionRole(canceled, roles.db, role.Arn) },
	} {
		t.Run(name, func(t *testing.T) {
			if e := action(); !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
		})
	}
	if _, err = roles.Get(ctx, role.RoleName); err != nil {
		t.Fatal(err)
	}
	if names, e := roles.PolicyNames(ctx, role.RoleName); e != nil || len(names) != 1 || names[0] != "reader" {
		t.Fatal(names, e)
	}
	if owned, e := roles.Owns(ctx, "late-role"); e != nil || owned {
		t.Fatal(owned, e)
	}
	if _, exists, e := core.NewIdentities(db).Policy("late-identity"); e != nil || exists {
		t.Fatal(exists, e)
	}
}

func TestNativeIAMQuotasAndCorruptStateRollback(t *testing.T) {
	ctx := context.Background()
	db, _ := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	_, err = roles.Create(ctx, "limited", lambdaServiceTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	p, err := core.ParsePolicy([]byte(nativeReadPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO identity_policies(name,document) SELECT printf('legacy-%04d',x),? FROM n`, nativeReadPolicy); err != nil {
		t.Fatal(err)
	}
	if err = roles.PutPolicy(ctx, "limited", "reader", nativeReadPolicy); iamCode(err) != "LimitExceeded" {
		t.Fatal("aggregate identity quota was bypassed", err)
	}
	if names, e := roles.PolicyNames(ctx, "limited"); e != nil || len(names) != 0 {
		t.Fatal("failed quota wrote native policy", names, e)
	}
	if err = roles.MutateIdentityPolicy(ctx, "overflow", &p, true); core.Code(err) != "ServiceUnavailable" {
		t.Fatal(err)
	}
	if err = roles.MutateIdentityPolicy(ctx, "legacy-0001", &p, false); err != nil {
		t.Fatal("replacement at quota failed", err)
	}
	if err = roles.MutateIdentityPolicy(ctx, "legacy-0001", nil, false); err != nil {
		t.Fatal(err)
	}
	if err = roles.PutPolicy(ctx, "limited", "reader", nativeReadPolicy); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec(`UPDATE aws_iam_role_policies SET document='{}' WHERE role_name='limited'`); err != nil {
		t.Fatal(err)
	}
	if err = roles.PutPolicy(ctx, "limited", "new-policy", nativeReadPolicy); err == nil {
		t.Fatal("corrupt persisted policy silently granted permissions")
	}
	if _, err = roles.Policy(ctx, "limited", "new-policy"); iamCode(err) != "NoSuchEntity" {
		t.Fatal("corrupt-state update did not roll back", err)
	}
	if _, err = db.Database().Exec(`UPDATE aws_iam_role_policies SET document=? WHERE role_name='limited'`, nativeReadPolicy); err != nil {
		t.Fatal(err)
	}
	// An individually valid policy can exceed the normalized aggregate byte limit.
	largePolicy, e := core.ParsePolicy([]byte(nativeReadPolicy))
	if e != nil {
		t.Fatal(e)
	}
	largePolicy.Statement[0].Resource = core.StringList{}
	for n := 0; n < 20; n++ {
		largePolicy.Statement[0].Resource = append(largePolicy.Statement[0].Resource, strings.Repeat("r", 1000))
	}
	largeData, e := json.Marshal(largePolicy)
	if e != nil {
		t.Fatal(e)
	}
	large := string(largeData)
	if err = roles.PutPolicy(ctx, "limited", "large-one", large); err != nil {
		t.Fatal(err)
	}
	if err = roles.PutPolicy(ctx, "limited", "large-two", large); iamCode(err) != "LimitExceeded" {
		t.Fatal("aggregate size quota bypassed", err)
	}
	if _, err = roles.Policy(ctx, "limited", "large-two"); iamCode(err) != "NoSuchEntity" {
		t.Fatal(err)
	}
	if err = roles.DeletePolicy(ctx, "limited", "large-one"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<999) INSERT INTO aws_iam_roles(name,role_id,trust,created,description,duration,deleted) SELECT printf('retired-%04d',x),'dummy',?,'2026-01-01T00:00:00Z','',3600,1 FROM n`, lambdaServiceTrust); err != nil {
		t.Fatal(err)
	}
	if _, err = roles.Create(ctx, "overflow", lambdaServiceTrust, "", 3600); iamCode(err) != "LimitExceeded" {
		t.Fatal("retired claim quota bypassed", err)
	}
	if _, err = roles.Create(ctx, "retired-0001", lambdaServiceTrust, "", 3600); err != nil {
		t.Fatal("native reactivation at quota rejected", err)
	}
}

func TestNativeIAMStorageFailuresDoNotCommitPartialChanges(t *testing.T) {
	ctx := context.Background()
	for _, operation := range []string{"policy-insert", "policy-delete", "aggregate-delete", "role-delete", "trust-update", "legacy-put", "legacy-delete"} {
		t.Run(operation, func(t *testing.T) {
			db, _ := s3TestState(t)
			roles, err := NewIAMRoleStore(db.Database())
			if err != nil {
				t.Fatal(err)
			}
			role, err := roles.Create(ctx, "fault-role", lambdaServiceTrust, "", 3600)
			if err != nil {
				t.Fatal(err)
			}
			p, err := core.ParsePolicy([]byte(nativeReadPolicy))
			if err != nil {
				t.Fatal(err)
			}
			if err = roles.MutateIdentityPolicy(ctx, "legacy", &p, false); err != nil {
				t.Fatal(err)
			}
			if operation == "policy-delete" || operation == "aggregate-delete" {
				if err = roles.PutPolicy(ctx, role.RoleName, "reader", nativeReadPolicy); err != nil {
					t.Fatal(err)
				}
			}
			target, event := "aws_iam_role_policies", "INSERT"
			switch operation {
			case "policy-delete":
				event = "DELETE"
			case "aggregate-delete":
				target, event = "identity_policies", "DELETE"
			case "role-delete", "trust-update":
				target, event = "aws_iam_roles", "UPDATE"
			case "legacy-put":
				target, event = "identity_policies", "UPDATE"
			case "legacy-delete":
				target, event = "identity_policies", "DELETE"
			}
			if _, err = db.Database().Exec(fmt.Sprintf("CREATE TRIGGER native_fault BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'private test storage fault'); END", event, target)); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "policy-insert":
				err = roles.PutPolicy(ctx, role.RoleName, "reader", nativeReadPolicy)
			case "policy-delete", "aggregate-delete":
				err = roles.DeletePolicy(ctx, role.RoleName, "reader")
			case "role-delete":
				err = roles.Delete(ctx, role.RoleName)
			case "trust-update":
				err = roles.UpdateTrust(ctx, role.RoleName, strings.Replace(lambdaServiceTrust, "lambda.amazonaws.com", "ec2.amazonaws.com", 1))
			case "legacy-put":
				err = roles.MutateIdentityPolicy(ctx, "legacy", &p, false)
			case "legacy-delete":
				err = roles.MutateIdentityPolicy(ctx, "legacy", nil, false)
			}
			if err == nil {
				t.Fatal("storage failure reported successful mutation")
			}
			stored, e := roles.Get(ctx, role.RoleName)
			if e != nil || stored.AssumeRolePolicyDocument != lambdaServiceTrust {
				t.Fatal("failure changed role", stored, e)
			}
			if _, exists, e := core.NewIdentities(db).Policy("legacy"); e != nil || !exists {
				t.Fatal("failure deleted legacy identity", e)
			}
			if operation == "policy-delete" || operation == "aggregate-delete" {
				if _, e = roles.Policy(ctx, role.RoleName, "reader"); e != nil {
					t.Fatal("failure deleted inline policy", e)
				}
			}
		})
	}
}

func TestNativeIAMPostInsertFailureAndMissingRolesFailClosed(t *testing.T) {
	ctx := context.Background()
	db, _ := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec(`CREATE TRIGGER corrupt_new_role AFTER INSERT ON aws_iam_roles BEGIN UPDATE aws_iam_roles SET created='invalid timestamp fault' WHERE name=NEW.name; END`); err != nil {
		t.Fatal(err)
	}
	if _, err = roles.Create(ctx, "rollback-claim", lambdaServiceTrust, "", 3600); err == nil {
		t.Fatal("failed metadata readback published a role")
	}
	if owned, e := roles.Owns(ctx, "rollback-claim"); e != nil || owned {
		t.Fatal("post-insert failure retained native claim", owned, e)
	}
	if _, err = db.Database().Exec(`DROP TRIGGER corrupt_new_role`); err != nil {
		t.Fatal(err)
	}
	if err = roles.Delete(ctx, "missing"); iamCode(err) != "NoSuchEntity" {
		t.Fatal(err)
	}
	if err = roles.DeletePolicy(ctx, "missing", "reader"); iamCode(err) != "NoSuchEntity" {
		t.Fatal(err)
	}
	if _, err = roles.PolicyNames(ctx, "missing"); iamCode(err) != "NoSuchEntity" {
		t.Fatal(err)
	}
	if _, err = roles.Policy(ctx, "missing", "reader"); iamCode(err) != "NoSuchEntity" {
		t.Fatal(err)
	}
	if _, err = roles.Create(ctx, "malformed-trust", lambdaServiceTrust, "", 3600); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec(`UPDATE aws_iam_roles SET trust='{}' WHERE name='malformed-trust'`); err != nil {
		t.Fatal(err)
	}
	if err = iamExecutionRole(ctx, roles.db, "arn:aws:iam::000000000000:role/malformed-trust"); err == nil {
		t.Fatal("damaged trust granted execution")
	}
	// Ownership lookup failure must not fall through to a legacy write.
	if _, err = db.Database().Exec(`DROP TABLE aws_iam_roles`); err != nil {
		t.Fatal(err)
	}
	p, err := core.ParsePolicy([]byte(nativeReadPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if err = roles.MutateIdentityPolicy(ctx, "must-not-exist", &p, false); err == nil {
		t.Fatal("failed ownership lookup granted legacy access")
	}
	if _, exists, e := core.NewIdentities(db).Policy("must-not-exist"); e != nil || exists {
		t.Fatal(exists, e)
	}
}

func TestNativeIAMStrictTrustAndDamagedMetadata(t *testing.T) {
	ctx := context.Background()
	db, _ := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	single := strings.Replace(strings.Replace(lambdaServiceTrust, `"Statement":[`, `"Statement":`, 1), `}]}`, `}}`, 1)
	if _, err = roles.Create(ctx, "single-trust", single, "", 3600); err != nil {
		t.Fatal(single, err)
	}
	for _, document := range []string{single + ` {}`, strings.Replace(single, `"Service":"lambda.amazonaws.com"`, `"AWS":"*"`, 1), `{"Version":"2012-10-17","Statement":{}}`, `{"Version":"2012-10-17","Statement":false}`} {
		if _, err = roles.Create(ctx, "invalid-trust", document, "", 3600); iamCode(err) != "MalformedPolicyDocument" {
			t.Fatal(document, err)
		}
	}
	if _, err = db.Database().Exec(`UPDATE aws_iam_roles SET created='damaged timestamp' WHERE name='single-trust'`); err != nil {
		t.Fatal(err)
	}
	if _, err = roles.Get(ctx, "single-trust"); err == nil {
		t.Fatal("damaged persisted timestamp accepted")
	}
	if _, err = roles.List(ctx, "", 100); err == nil {
		t.Fatal("damaged persisted list accepted")
	}
	if err = iamExecutionRole(ctx, roles.db, "arn:aws:iam::000000000000:role/single-trust"); err == nil {
		t.Fatal("damaged role was executable")
	}
}

func TestNativeIAMAndCloudFormationCannotAdoptEachOthersNames(t *testing.T) {
	ctx := context.Background()
	db, _ := s3TestState(t)
	stacks, err := NewStackStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	compile := func(stack, name string) StackRecord {
		t.Helper()
		source := `{"Resources":{"Role":{"Type":"AWS::IAM::Role","Properties":{"RoleName":"` + name + `","Policies":[{"PolicyName":"reader","PolicyDocument":` + nativeReadPolicy + `}]}}}}`
		result, e := compileStack("us-east-1", stack, source, nil, "CAPABILITY_NAMED_IAM")
		if e != nil {
			t.Fatal(e)
		}
		return result
	}
	if _, err = roles.Create(ctx, "OwnedRole", lambdaServiceTrust, "native sentinel", 3600); err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(compile("cannot-adopt-native", "ownedrole")); err == nil {
		t.Fatal("stack adopted native case alias")
	}
	if records, e := stacks.Records("us-east-1", ""); e != nil || len(records) != 0 {
		t.Fatal("failed stack persisted", records, e)
	}
	if err = roles.Delete(ctx, "OwnedRole"); err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(compile("cannot-adopt-retired", "OwnedRole")); err == nil {
		t.Fatal("stack adopted retired native name")
	}
	legacy := compile("legacy-owned", "StackRole")
	if err = stacks.Create(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err = roles.Create(ctx, "stackrole", lambdaServiceTrust, "", 3600); iamCode(err) != "EntityAlreadyExists" {
		t.Fatal("native adopted legacy identity", err)
	}
	if err = core.NewIdentities(db).Delete("StackRole"); err != nil {
		t.Fatal(err)
	}
	if _, err = roles.Create(ctx, "STACKROLE", lambdaServiceTrust, "", 3600); iamCode(err) != "EntityAlreadyExists" {
		t.Fatal("native adopted drifted stack ownership", err)
	}
	if err = stacks.Delete("us-east-1", legacy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = roles.Create(ctx, "StackRole", lambdaServiceTrust, "new native owner", 3600); err != nil {
		t.Fatal("released stack name could not be reused", err)
	}
}
