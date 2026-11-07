package aws

import (
	"context"
	"encoding/json"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"os"
	"strings"
	"testing"
)

const stsRootTrust = `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Condition":{"StringEquals":{"sts:ExternalId":"team-external"}}}}`

func TestSTSTrustBoundaries(t *testing.T) {
	caller := SDKIdentity{Principal: "admin", ARN: "arn:aws:iam::000000000000:root", Account: "000000000000"}
	for _, tc := range []struct {
		external, account string
		want              bool
	}{{"team-external", "000000000000", true}, {"wrong", "000000000000", false}, {"", "000000000000", false}, {"team-external", "111111111111", false}} {
		c := caller
		c.Account = tc.account
		c.ARN = "arn:aws:iam::" + tc.account + ":root"
		got, err := stsTrustAllows(stsRootTrust, c, tc.external)
		if err != nil || got != tc.want {
			t.Fatalf("trust %v: %v %v", tc, got, err)
		}
	}
	for _, doc := range []string{
		`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":"*"}}}`,
		`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":"000000000000"},"Condition":{"StringLike":{"sts:ExternalId":"*"}}}}`,
		`{"Version":"2012-10-17","Statement":{"Effect":"Deny","Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":"000000000000"}}}`,
		`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":"000000000000"},"NotAction":"s3:*"}}`,
	} {
		if _, err := iamTrust(doc); err == nil {
			t.Fatalf("accepted unsupported trust %s", doc)
		}
	}
	if lambda, err := iamTrust(stsRootTrust); err != nil || lambda {
		t.Fatalf("AWS trust grants Lambda: %v %v", lambda, err)
	}
}
func TestSTSRoleNativeGenerationAndCloudFormation(t *testing.T) {
	ctx := context.Background()
	db, _ := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	stacks, err := NewStackStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Roles: roles, Stacks: stacks, Identities: core.NewIdentities(db)}
	role, err := roles.Create(ctx, "target", stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	found, err := g.stsRole(ctx, role.Arn)
	if err != nil || found.RoleID != role.RoleID {
		t.Fatal(found, err)
	}
	if err = roles.Delete(ctx, "target"); err != nil {
		t.Fatal(err)
	}
	if _, err = g.stsRole(ctx, role.Arn); err == nil {
		t.Fatal("deleted role resolved")
	}
	recreated, err := roles.Create(ctx, "target", stsRootTrust, "", 3600)
	if err != nil || recreated.RoleID == role.RoleID {
		t.Fatal("generation reused", err)
	}
	if _, err = g.stsRole(ctx, "arn:aws:iam::000000000000:role/admin"); err == nil {
		t.Fatal("arbitrary admin resolved")
	}
	var trust any
	_ = json.Unmarshal([]byte(stsRootTrust), &trust)
	record := StackRecord{ID: "stack-generation-one", Resources: []StackResource{{Type: "AWS::IAM::Role", LogicalID: "Role", PhysicalID: "cfn-target", Properties: map[string]any{"AssumeRolePolicyDocument": trust}}}}
	raw, _ := json.Marshal(record)
	for _, query := range []string{`INSERT INTO aws_cfn_stacks VALUES('us-east-1','stack','stack-generation-one',?)`, `INSERT INTO aws_cfn_ownership VALUES('global','AWS::IAM::Role','cfn-target','stack-generation-one')`, `INSERT INTO identity_policies VALUES('cfn-target','{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::bucket/*"}]}')`} {
		var err error
		if query[len(query)-2] == '?' {
			_, err = db.Database().Exec(query, string(raw))
		} else {
			_, err = db.Database().Exec(query)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	cfn, err := g.stsRole(ctx, "arn:aws:iam::000000000000:role/cfn-target")
	if err != nil || cfn.MaxSessionDuration != 3600 {
		t.Fatal(cfn, err)
	}

	cfnPolicy, err := g.stsSessionPolicy(ctx, cfn)
	if err != nil || core.Evaluate(cfnPolicy, "s3:GetObject", "arn:aws:s3:::bucket/key") != "allowed" {
		t.Fatal("CFN snapshot", err)
	}
	for _, badDuration := range []any{"junk", "3600.0", " 3600", "+3600", "03600", "3599", "43201", 899.0, 43201.0, 3600.5, nil} {
		record.Resources[0].Properties["MaxSessionDuration"] = badDuration
		raw, _ = json.Marshal(record)
		if _, err = db.Database().Exec("UPDATE aws_cfn_stacks SET document=? WHERE id=?", string(raw), record.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = g.stsRole(ctx, cfn.Arn); err == nil {
			t.Fatalf("accepted malformed CFN duration %v", badDuration)
		}
	}
	delete(record.Resources[0].Properties, "MaxSessionDuration")
	record.Resources[0].Properties["AssumeRolePolicyDocument"] = map[string]any{"unsupported": true}
	raw, _ = json.Marshal(record)
	if _, err = db.Database().Exec("UPDATE aws_cfn_stacks SET document=? WHERE id=?", string(raw), record.ID); err != nil {
		t.Fatal(err)
	}
	malformed, err := g.stsRole(ctx, cfn.Arn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stsTrustAllows(malformed.AssumeRolePolicyDocument, SDKIdentity{Account: "000000000000"}, "team-external"); err == nil {
		t.Fatal("malformed CFN trust accepted")
	}
	record.Resources[0].Properties["AssumeRolePolicyDocument"] = trust
	record.ID = "stack-generation-two"
	raw, _ = json.Marshal(record)
	// Delete old claims first so the generation transition retains FK integrity.
	for _, query := range []string{"DELETE FROM aws_cfn_ownership WHERE physical_id='cfn-target'", "DELETE FROM aws_cfn_stacks WHERE id='stack-generation-one'"} {
		if _, err = db.Database().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Database().Exec("INSERT INTO aws_cfn_stacks VALUES('us-east-1','stack','stack-generation-two',?)", string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec("INSERT INTO aws_cfn_ownership VALUES('global','AWS::IAM::Role','cfn-target','stack-generation-two')"); err != nil {
		t.Fatal(err)
	}
	next, err := g.stsRole(ctx, cfn.Arn)
	if err != nil || next.RoleID == cfn.RoleID {
		t.Fatal("CFN generation reused", err)
	}

	if _, err = g.stsSessionPolicy(ctx, cfn); err == nil {
		t.Fatal("old CFN generation acquired replacement snapshot")
	}
	if _, err = g.stsSessionPolicy(ctx, next); err != nil {
		t.Fatal("new CFN generation rejected", err)
	}
	if _, err = db.Database().Exec(`DELETE FROM identity_policies WHERE name='cfn-target'`); err != nil {
		t.Fatal(err)
	}
	if _, err = g.stsRole(ctx, cfn.Arn); err == nil {
		t.Fatal("drifted CFN role resolved")
	}
}

func TestSTSExplicitRoleTrustAndDeny(t *testing.T) {
	caller := SDKIdentity{Principal: "source", ARN: "arn:aws:sts::000000000000:assumed-role/source/session", Account: "000000000000"}
	allow := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":"arn:aws:iam::000000000000:role/source"}}]}`
	ok, err := stsTrustAllows(allow, caller, "")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	untrusted := caller
	untrusted.Principal = "other"
	untrusted.ARN = "arn:aws:sts::000000000000:assumed-role/other/session"
	if ok, err := stsTrustAllows(allow, untrusted, ""); err != nil || ok {
		t.Fatal(ok, err)
	}
	deny := strings.Replace(allow, `}]}`, `},{"Effect":"Deny","Action":"sts:AssumeRole","Principal":{"AWS":"000000000000"}}]}`, 1)
	if ok, err := stsTrustAllows(deny, caller, ""); err != nil || ok {
		t.Fatal(ok, err)
	}
}

func TestSTSSessionPolicySnapshotGeneration(t *testing.T) {
	ctx := context.Background()
	db, _ := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Roles: roles}
	role, err := roles.Create(ctx, "snapshot-target", stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := g.stsSessionPolicy(ctx, role)
	if err != nil || core.Evaluate(empty, "s3:GetObject", "arn:aws:s3:::bucket/key") == "allowed" {
		t.Fatal("empty native role must authenticate with deny", err)
	}
	if err = roles.PutPolicy(ctx, role.RoleName, "read", nativeReadPolicy); err != nil {
		t.Fatal(err)
	}
	before, err := g.stsSessionPolicy(ctx, role)
	if err != nil || core.Evaluate(before, "s3:GetObject", "arn:aws:s3:::bucket/key") != "allowed" {
		t.Fatal("snapshot absent", err)
	}
	if err = roles.DeletePolicy(ctx, role.RoleName, "read"); err != nil {
		t.Fatal(err)
	}
	if err = roles.Delete(ctx, role.RoleName); err != nil {
		t.Fatal(err)
	}
	if _, err = g.stsSessionPolicy(ctx, role); err == nil {
		t.Fatal("deleted role snapshot accepted")
	}
	replacement, err := roles.Create(ctx, role.RoleName, stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	if err = roles.PutPolicy(ctx, replacement.RoleName, "new", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err = g.stsSessionPolicy(ctx, role); err == nil {
		t.Fatal("old session acquired replacement permissions")
	}
	if core.Evaluate(before, "iam:CreateRole", "*") == "allowed" {
		t.Fatal("old immutable policy changed")
	}
	if _, err = db.Database().Exec(`UPDATE identity_policies SET document='invalid' WHERE name='snapshot-target'`); err != nil {
		t.Fatal(err)
	}
	if _, err = g.stsSessionPolicy(ctx, replacement); err == nil {
		t.Fatal("corrupt policy accepted")
	}
}

func TestSTSOriginalTemplatesCompiledRoleDuration(t *testing.T) {
	for _, name := range []string{"hello-world"} {
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile("testdata/sts-original/" + name + ".yaml")
			if err != nil {
				t.Fatal(err)
			}
			record, err := compileStack("us-east-1", "original-"+name, string(body), map[string]string{"NamePrefix": "tc-" + name + "-team", "TenkaCloudAccountId": "999999999999", "ExternalId": "original-job-external", "FlagSeed": "FixtureOnly123"}, "CAPABILITY_IAM")
			if err != nil {
				t.Fatal(err)
			}
			db, _ := s3TestState(t)
			if _, err = NewParameterStore(db.Database()); err != nil {
				t.Fatal(err)
			}
			stacks, err := NewStackStore(db.Database())
			if err != nil {
				t.Fatal(err)
			}
			if err = stacks.Create(record); err != nil {
				t.Fatal(err)
			}
			g := &Gateway{Stacks: stacks}
			ctx := context.Background()
			arn := record.Outputs["ParticipantViewerRoleArn"]
			role, err := g.stsRole(ctx, arn)
			if err != nil || role.MaxSessionDuration != 3600 {
				t.Fatalf("compiled original role duration: %d %v", role.MaxSessionDuration, err)
			}
			for _, r := range record.Resources {
				if r.Type == "AWS::IAM::Role" {
					if r.Properties["MaxSessionDuration"] != "3600" {
						t.Fatalf("unexpected compiler representation: %#v", r.Properties["MaxSessionDuration"])
					}
				}
			}
			trusted, err := stsTrustAllows(role.AssumeRolePolicyDocument, SDKIdentity{Account: "999999999999", ARN: "arn:aws:iam::999999999999:root"}, "original-job-external")
			if err != nil || !trusted {
				t.Fatal("original trust failed", err)
			}
			policy, err := g.stsSessionPolicy(ctx, role)
			if err != nil {
				t.Fatal(err)
			}
			if core.Evaluate(policy, "ssm:GetParameter", "arn:aws:ssm:us-east-1:000000000000:parameter/tc-"+name+"-team/hello") != "allowed" {
				t.Fatal("original scoped policy was lost")
			}
		})
	}
}
