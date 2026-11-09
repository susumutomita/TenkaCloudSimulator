package aws

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const cfnProfileTemplate = `Resources:
  Role:
    Type: AWS::IAM::Role
    Properties:
      RoleName: NativeRole
      AssumeRolePolicyDocument:
        Version: '2012-10-17'
        Statement: [{Effect: Allow, Action: 'sts:AssumeRole', Principal: {Service: ec2.amazonaws.com}}]
  Profile:
    Type: AWS::IAM::InstanceProfile
    Properties:
      InstanceProfileName: NativeProfile
      Roles: [!Ref Role]
Outputs:
  Role: {Value: !Ref Role}
  Profile: {Value: !Ref Profile}
  Arn: {Value: !GetAtt Profile.Arn}
`

func cfnIAMCompile(t *testing.T, body string) StackRecord {
	t.Helper()
	r, err := compileStack("us-east-1", "native-iam", body, nil, "CAPABILITY_NAMED_IAM")
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func cfnIAMQuery(g *Gateway, action, name, body, principal string) *httptest.ResponseRecorder {
	form := url.Values{"Action": {action}, "Version": {"2010-05-15"}, "StackName": {name}}
	if action == "CreateStack" {
		form.Set("TemplateBody", body)
		form.Set("Capabilities.member.1", "CAPABILITY_NAMED_IAM")
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Simulator-Principal", principal)
	out := httptest.NewRecorder()
	g.ServeHTTP(out, req)
	return out
}
func TestCFNNativeIAMLifecyclePersistenceAndNoImplicitGrants(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/state.sqlite"
	open := func() (*core.Store, *StackStore, *IAMRoleStore) {
		t.Helper()
		db, err := core.New(path)
		if err != nil {
			t.Fatal(err)
		}
		stacks, err := NewStackStore(db.Database())
		if err != nil {
			t.Fatal(err)
		}
		return db, stacks, &IAMRoleStore{db.Database()}
	}
	db, stacks, roles := open()
	record := cfnIAMCompile(t, cfnProfileTemplate)
	if record.Outputs["Profile"] != "NativeProfile" || record.Outputs["Arn"] != iamInstanceProfileARN+"NativeProfile" {
		t.Fatal(record.Outputs)
	}
	if err := stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	p, err := roles.GetInstanceProfile(ctx, "nativeprofile")
	if err != nil || len(p.Roles) != 1 || p.Roles[0].RoleName != "NativeRole" {
		t.Fatal(p, err)
	}
	rid, pid := p.Roles[0].RoleID, p.InstanceProfileID
	if decision, err := core.NewIdentities(db).Decision("NativeRole", "ssm:SendCommand", "*"); err != nil || decision != "implicitDeny" {
		t.Fatal("empty role granted authority", decision, err)
	}
	policy, _ := core.ParsePolicy([]byte(nativeReadPolicy))
	if err := roles.MutateIdentityPolicy(ctx, "nativerole", &policy, false); core.Code(err) != "Conflict" {
		t.Fatal("legacy control adopted native role", err)
	}
	if err := roles.Delete(ctx, "NativeRole", rid); iamCode(err) != "DeleteConflict" {
		t.Fatal("profile dependency bypassed", err)
	}
	saved, err := stacks.Records(record.Region, record.ID)
	if err != nil || len(saved) != 1 || saved[0].Resources[0].NativeIAM.Generation != rid {
		t.Fatal(saved, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, stacks, roles = open()
	defer db.Close()
	p, err = roles.GetInstanceProfile(ctx, "NativeProfile")
	if err != nil || p.InstanceProfileID != pid || p.Roles[0].RoleID != rid {
		t.Fatal("restart lost generations", p, err)
	}
	if err = stacks.Delete(record.Region, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = roles.Get(ctx, "NativeRole"); iamCode(err) != "NoSuchEntity" {
		t.Fatal(err)
	}
	if _, err = roles.GetInstanceProfile(ctx, "NativeProfile"); iamCode(err) != "NoSuchEntity" {
		t.Fatal(err)
	}
	// Successful cleanup releases CFN claims, not native namespace tombstones.
	if err = roles.MutateIdentityPolicy(ctx, "NativeRole", &policy, false); core.Code(err) != "Conflict" {
		t.Fatal(err)
	}
	fresh := cfnIAMCompile(t, cfnProfileTemplate)
	if err = stacks.Create(fresh); err != nil {
		t.Fatal(err)
	}
	p, err = roles.GetInstanceProfile(ctx, "NativeProfile")
	if err != nil || p.InstanceProfileID == pid || p.Roles[0].RoleID == rid {
		t.Fatal("recreation reused generation", p, err)
	}
	if err = stacks.Delete(record.Region, record.ID); err == nil {
		t.Fatal("stale stack id accepted")
	}
	if err = stacks.Delete(fresh.Region, fresh.ID); err != nil {
		t.Fatal(err)
	}
}
func TestCFNNativeIAMCompilerFailClosed(t *testing.T) {
	role := func() map[string]any {
		return map[string]any{"AssumeRolePolicyDocument": map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": "sts:AssumeRole", "Principal": map[string]any{"Service": "ec2.amazonaws.com"}}}}}
	}
	encode := func(p map[string]any, kind string) string {
		resources := map[string]any{"Resource": map[string]any{"Type": kind, "Properties": p}}
		if kind == "AWS::IAM::Role" {
			resources["Profile"] = map[string]any{"Type": "AWS::IAM::InstanceProfile", "Properties": map[string]any{"Roles": []any{map[string]any{"Ref": "Resource"}}}}
		}
		b, _ := json.Marshal(map[string]any{"Resources": resources})
		return string(b)
	}
	for _, change := range []func(map[string]any){
		func(p map[string]any) {
			p["ManagedPolicyArns"] = []any{"arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCoreUnknown"}
		},
		func(p map[string]any) {
			p["ManagedPolicyArns"] = []any{"arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"}
		},
		func(p map[string]any) { p["Tags"] = []any{map[string]any{"Key": "x", "Value": "y"}} },
		func(p map[string]any) { p["Path"] = "/unsupported/" }, func(p map[string]any) { p["RoleName"] = "SIMULATOR-ADMIN" },
		func(p map[string]any) { p["MaxSessionDuration"] = "3599" }, func(p map[string]any) { p["Description"] = true }, func(p map[string]any) { p["Description"] = "\u0000" },
		func(p map[string]any) { p["AssumeRolePolicyDocument"] = nil },
		func(p map[string]any) {
			object(p["AssumeRolePolicyDocument"])["Statement"] = []any{map[string]any{"Effect": "Allow", "Action": "sts:AssumeRole", "Principal": map[string]any{"AWS": "arn:aws:iam::000000000000:root"}}}
		},
		func(p map[string]any) {
			s := object(p["AssumeRolePolicyDocument"])["Statement"].([]any)[0]
			object(s)["Condition"] = map[string]any{"StringEquals": map[string]any{"sts:ExternalId": "not-accepted"}}
		},
		func(p map[string]any) {
			p["Policies"] = []any{map[string]any{"PolicyName": "x", "PolicyDocument": map[string]any{"Version": "2012-10-17", "Statement": map[string]any{"Effect": "Allow", "Action": "ssm:GetParameter", "Resource": "*", "Condition": map[string]any{"StringEquals": map[string]any{"x": "y"}}}}}}
		},
		func(p map[string]any) {
			p["Policies"] = []any{map[string]any{"PolicyName": "x", "PolicyDocument": json.RawMessage(nativeReadPolicy)}, map[string]any{"PolicyName": "x", "PolicyDocument": json.RawMessage(nativeReadPolicy)}}
		},
	} {
		p := role()
		change(p)
		if _, err := compileStack("us-east-1", "rejected", encode(p, "AWS::IAM::Role"), nil, "CAPABILITY_NAMED_IAM"); err == nil {
			t.Fatalf("accepted unsupported role: %#v", p)
		}
	}
	for _, p := range []map[string]any{{}, {"Roles": "role"}, {"Roles": []any{"one", "two"}}, {"Roles": []any{"arn:aws:iam::000000000000:role/Role"}}, {"Roles": []any{}, "Path": "/not-root/"}, {"Roles": []any{}, "Unknown": true}} {
		if _, err := compileStack("us-east-1", "rejected", encode(p, "AWS::IAM::InstanceProfile"), nil, "CAPABILITY_IAM"); err == nil {
			t.Fatal("accepted profile", p)
		}
	}
	if _, err := compileStack("us-east-1", "rejected", cfnProfileTemplate, nil, "CAPABILITY_IAM"); err == nil {
		t.Fatal("named IAM not acknowledged")
	}
	// Zero policies and ten distinct policies are a native role contract, not a legacy shortcut.
	p := role()
	p["Policies"] = []any{}
	if _, err := compileStack("us-east-1", "empty", encode(p, "AWS::IAM::Role"), nil, "CAPABILITY_IAM"); err != nil {
		t.Fatal(err)
	}
	policies := []any{}
	for i := 0; i < 10; i++ {
		policies = append(policies, map[string]any{"PolicyName": fmt.Sprint(i), "PolicyDocument": json.RawMessage(nativeReadPolicy)})
	}
	p["Policies"] = policies
	if _, err := compileStack("us-east-1", "ten", encode(p, "AWS::IAM::Role"), nil, "CAPABILITY_IAM"); err != nil {
		t.Fatal(err)
	}
	p["Policies"] = append(policies, policies[0])
	if _, err := compileStack("us-east-1", "eleven", encode(p, "AWS::IAM::Role"), nil, "CAPABILITY_IAM"); err == nil {
		t.Fatal("inline quota ignored")
	}
}
func TestCFNNativeIAMInlinePoliciesAndLegacySeparation(t *testing.T) {
	ctx := context.Background()
	db, _, stacks, _ := s3StackState(t)
	roles := &IAMRoleStore{db.Database()}
	body := strings.Replace(cfnProfileTemplate, "      RoleName: NativeRole", "      RoleName: NativeRole\n      Policies:\n        - PolicyName: Read\n          PolicyDocument: "+nativeReadPolicy, 1)
	record := cfnIAMCompile(t, body)
	if err := stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	role, err := roles.Get(ctx, "NativeRole")
	if err != nil {
		t.Fatal(err)
	}
	if names, err := roles.PolicyNames(ctx, role.RoleName, role.RoleID); err != nil || len(names) != 1 || names[0] != "Read" {
		t.Fatal(names, err)
	}
	if err = stacks.Delete(record.Region, record.ID); err != nil {
		t.Fatal(err)
	}
	// Simulate an old stored stack, not a new role migrated by observation.
	legacy := StackRecord{Name: "old-stack", ID: "old-stack-id", Region: "us-east-1", Created: "2026-01-01T00:00:00Z", Resources: []StackResource{{LogicalID: "Role", Type: "AWS::IAM::Role", PhysicalID: "Legacy", Properties: map[string]any{"SimulatorPolicy": nativeReadPolicy, "AssumeRolePolicyDocument": map[string]any{"historical": "metadata"}}}}}
	if err = stacks.Create(legacy); err != nil {
		t.Fatal(err)
	}
	external := cfnIAMCompile(t, `Resources: {Profile: {Type: 'AWS::IAM::InstanceProfile', Properties: {InstanceProfileName: LegacyProfile, Roles: [Legacy]}}}`)
	before := cleanupACKFrame(t, db.Database())
	if err = stacks.Create(external); err == nil || cleanupACKFrame(t, db.Database()) != before {
		t.Fatal("legacy profile adoption", err)
	}
	if _, err = roles.Create(ctx, "legacy", lambdaServiceTrust, "", 3600); iamCode(err) != "EntityAlreadyExists" {
		t.Fatal(err)
	}
	if err = stacks.Delete(legacy.Region, legacy.ID); err != nil {
		t.Fatal(err)
	}
}
func TestCFNNativeIAMCanonicalPreflightAndExternalRole(t *testing.T) {
	db, _, stacks, _ := s3StackState(t)
	roles := &IAMRoleStore{db.Database()}
	role := profileRole(t, roles, "ExternalRole")
	body := `Resources: {Profile: {Type: 'AWS::IAM::InstanceProfile', Properties: {InstanceProfileName: ExternalProfile, Roles: [externalrole]}}}`
	record := cfnIAMCompile(t, body)
	seen := map[string]string{}
	deny := func(action, arn string) error {
		seen[action] = arn
		if action == "iam:PassRole" {
			return ssmFailure{"AccessDenied", "denied canonical role"}
		}
		return nil
	}
	before := cleanupACKFrame(t, db.Database())
	if err := stacks.createChecked(record, deny); iamCode(err) != "AccessDenied" || cleanupACKFrame(t, db.Database()) != before {
		t.Fatal(err)
	}
	if seen["iam:PassRole"] != role.Arn {
		t.Fatal("case alias authorization", seen)
	}
	allow := func(action, arn string) error { seen[action] = arn; return nil }
	if err := stacks.createChecked(record, allow); err != nil {
		t.Fatal(err)
	}
	if seen["iam:CreateInstanceProfile"] != iamInstanceProfileARN+"ExternalProfile" || seen["iam:AddRoleToInstanceProfile"] != iamInstanceProfileARN+"ExternalProfile" {
		t.Fatal(seen)
	}
	if err := stacks.Delete(record.Region, record.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := roles.Get(context.Background(), role.RoleName); err != nil || got.RoleID != role.RoleID {
		t.Fatal("deleted referenced external role", err)
	}
}
func TestCFNNativeIAMQueryPermissionsAndSharedNativeViews(t *testing.T) {
	p := newManagedQueryProbe(t)
	stacks, err := NewStackStore(p.gateway.Store.Database())
	if err != nil {
		t.Fatal(err)
	}
	p.gateway.Stacks = stacks
	for _, action := range []string{"iam:CreateRole", "iam:CreateInstanceProfile", "iam:AddRoleToInstanceProfile", "iam:PassRole"} {
		policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"},{"Effect":"Deny","Action":"` + action + `","Resource":"*"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err = p.gateway.Identities.Set("caller", policy); err != nil {
			t.Fatal(err)
		}
		before := cleanupACKFrame(t, stacks.db)
		w := cfnIAMQuery(p.gateway, "CreateStack", "denied", cfnProfileTemplate, "caller")
		if w.Code != 403 || cleanupACKFrame(t, stacks.db) != before {
			t.Fatal(action, w.Code, w.Body.String())
		}
	}
	w := cfnIAMQuery(p.gateway, "CreateStack", "query", cfnProfileTemplate, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	p.call("GetRole", "", url.Values{"RoleName": {"NativeRole"}}, 200, "ec2.amazonaws.com")
	p.call("GetInstanceProfile", "", url.Values{"InstanceProfileName": {"NativeProfile"}}, 200, "<RoleName>NativeRole</RoleName>")
	for _, action := range []string{"iam:RemoveRoleFromInstanceProfile", "iam:DeleteInstanceProfile", "iam:DeleteRole"} {
		policy, _ := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"},{"Effect":"Deny","Action":"` + action + `","Resource":"*"}]}`))
		if err = p.gateway.Identities.Set("caller", policy); err != nil {
			t.Fatal(err)
		}
		before := cleanupACKFrame(t, stacks.db)
		w = cfnIAMQuery(p.gateway, "DeleteStack", "query", "", "caller")
		if w.Code != 403 || cleanupACKFrame(t, stacks.db) != before {
			t.Fatal(action, w.Code, w.Body.String())
		}
	}
	w = cfnIAMQuery(p.gateway, "DeleteStack", "query", "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestCFNNativeIAMCompatibilitySelection(t *testing.T) {
	// The immutable fixture is exactly the current original hello-world template,
	// not the Battle and not a projected role-only substitute.
	fixture, err := os.ReadFile("../../test/fixtures/hello-world.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(fixture)) != "ad8b4fcf3f1d42475c5c2e8617d3194536810cc044a6ea166a2699cac719c8bb" {
		t.Fatal("original fixture changed")
	}
	db, _, stacks, _ := s3StackState(t)
	record, err := compileStack("us-east-1", "original", string(fixture), map[string]string{"NamePrefix": "tc-hello-world-test", "TenkaCloudAccountId": "000000000000", "ExternalId": "local-test-job-20261109", "FlagSeed": "DummySeed123"}, "CAPABILITY_IAM")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range record.Resources {
		if r.NativeIAM != nil {
			t.Fatal("original legacy role was promoted")
		}
	}
	if err = stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	if err = stacks.Delete(record.Region, record.ID); err != nil {
		t.Fatal(err)
	}
	var native int
	if err = db.Database().QueryRow("SELECT count(*) FROM aws_iam_roles").Scan(&native); err != nil || native != 0 {
		t.Fatal(native, err)
	}
	roleTemplate := `Resources:
  Role:
    Type: AWS::IAM::Role
    Properties:
      RoleName: LegacyLambda
      AssumeRolePolicyDocument: ` + lambdaServiceTrust + `
      ManagedPolicyArns: ['arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole']
      Policies: [{PolicyName: Read, PolicyDocument: ` + nativeReadPolicy + `}]
`

	record = cfnIAMCompile(t, roleTemplate)
	if record.Resources[0].NativeIAM != nil {
		t.Fatal("established Lambda role promoted")
	}
	if err = stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	if err = stacks.Delete(record.Region, record.ID); err != nil {
		t.Fatal(err)
	}
	disabled := `Conditions: {Off: {Fn::Equals: [a, b]}}
` + roleTemplate + `  Profile:
    Condition: Off
    Type: AWS::IAM::InstanceProfile
    Properties: {Roles: [!Ref Role]}
`
	record = cfnIAMCompile(t, disabled)
	if len(record.Resources) != 1 || record.Resources[0].NativeIAM != nil {
		t.Fatal("disabled profile reinterpreted legacy role", record)
	}
	active := strings.Replace(disabled, "Condition: Off", "Condition: On", 1)
	active = strings.Replace(active, "Off: {Fn::Equals: [a, b]}", "On: {Fn::Equals: [a, a]}", 1)
	if _, err = compileStack("us-east-1", "native", active, nil, "CAPABILITY_NAMED_IAM"); err == nil {
		t.Fatal("profile silently accepted unsupported managed policy")
	}
	// Literal exact names and intrinsics select the same native contract.
	plain := strings.Replace(roleTemplate, "      ManagedPolicyArns: ['arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole']\n", "", 1)
	for _, roles := range []string{"[LegacyLambda]", "[!Sub '${Role}']", "[!If [On, !Ref Role, unused]]"} {
		text := `Conditions: {On: {Fn::Equals: [a, a]}}
` + plain + "  Profile:\n    Type: AWS::IAM::InstanceProfile\n    DependsOn: Role\n    Properties: {Roles: " + roles + "}\n"
		record = cfnIAMCompile(t, text)
		for _, r := range record.Resources {
			if r.NativeIAM == nil {
				t.Fatal("active composition not native")
			}
		}
	}
	wrongCase := plain + "  Profile:\n    Type: AWS::IAM::InstanceProfile\n    Properties: {Roles: [legacylambda]}\n"
	if _, err = compileStack("us-east-1", "case", wrongCase, nil, "CAPABILITY_NAMED_IAM"); err == nil {
		t.Fatal("case alias adopted declared identity")
	}
	// Mixed service/AWS and malformed service trust cannot hide in the native path.
	for _, trust := range []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"Service":"ec2.amazonaws.com","AWS":"arn:aws:iam::000000000000:root"}}]}`, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"Service":"*"}}]}`} {
		text := strings.Replace(plain, lambdaServiceTrust, trust, 1) + "  Profile:\n    Type: AWS::IAM::InstanceProfile\n    Properties: {Roles: [!Ref Role]}\n"
		if _, err = compileStack("us-east-1", "mixed", text, nil, "CAPABILITY_NAMED_IAM"); err == nil {
			t.Fatal("unsupported native trust accepted")
		}
	}
}

func TestCFNNativeIAMExternalDependenciesDriftAndRetiredCleanup(t *testing.T) {
	for _, change := range []string{"external-profile", "external-policy", "trust", "membership", "role-generation", "profile-generation", "retired"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			db, _, stacks, _ := s3StackState(t)
			roles := &IAMRoleStore{db.Database()}
			record := cfnIAMCompile(t, cfnProfileTemplate)
			if err := stacks.Create(record); err != nil {
				t.Fatal(err)
			}
			p, err := roles.GetInstanceProfile(ctx, "NativeProfile")
			if err != nil {
				t.Fatal(err)
			}
			r := p.Roles[0]
			switch change {
			case "external-profile":
				other := profileCreate(t, roles, "ExternalProfile")
				if err = roles.ChangeInstanceProfileRole(ctx, other.InstanceProfileName, other.InstanceProfileID, r.RoleName, r.RoleID, true); err != nil {
					t.Fatal(err)
				}
			case "external-policy":
				policy := managedPolicy(t, roles, "ExternalPolicy", nativeReadPolicy)
				if err = roles.AttachRolePolicy(ctx, r.RoleName, policy.Arn, r.RoleID); err != nil {
					t.Fatal(err)
				}
			case "trust":
				if err = roles.UpdateTrust(ctx, r.RoleName, lambdaServiceTrust, r.RoleID); err != nil {
					t.Fatal(err)
				}
			case "membership":
				if err = roles.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r.RoleName, r.RoleID, false); err != nil {
					t.Fatal(err)
				}
			case "role-generation", "profile-generation", "retired":
				if err = roles.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r.RoleName, r.RoleID, false); err != nil {
					t.Fatal(err)
				}
				if err = roles.DeleteInstanceProfile(ctx, p.InstanceProfileName, p.InstanceProfileID); err != nil {
					t.Fatal(err)
				}
				if err = roles.Delete(ctx, r.RoleName, r.RoleID); err != nil {
					t.Fatal(err)
				}
				if _, err = roles.Create(ctx, strings.ToLower(r.RoleName), lambdaServiceTrust, "", 3600); iamCode(err) != "EntityAlreadyExists" {
					t.Fatal("drifted role name was reusable", err)
				}
				if _, err = roles.CreateInstanceProfile(ctx, strings.ToLower(p.InstanceProfileName)); iamCode(err) != "EntityAlreadyExists" {
					t.Fatal("drifted profile name was reusable", err)
				}
				if change == "role-generation" {
					profileSQL(t, roles.db, "UPDATE aws_iam_roles SET role_id='AROA0000000000000000' WHERE name='NativeRole'")
				}
				if change == "profile-generation" {
					profileSQL(t, roles.db, "UPDATE aws_iam_instance_profiles SET profile_id='AIPA0000000000000000' WHERE name='NativeProfile'")
				}
			}
			before := cleanupACKFrame(t, roles.db)
			err = stacks.Delete(record.Region, record.ID)
			if change == "retired" {
				if err != nil {
					t.Fatal("same retired generations did not clean up", err)
				}
				return
			}
			if err == nil || cleanupACKFrame(t, roles.db) != before {
				t.Fatal("drift/dependency cleanup was acknowledged or partially changed state", err)
			}
		})
	}
}

func TestCFNNativeIAMQuotasAndCaseOwnership(t *testing.T) {
	for _, limit := range []string{"roles", "profiles", "aggregate", "legacy-case", "native-case"} {
		t.Run(limit, func(t *testing.T) {
			db, _, stacks, _ := s3StackState(t)
			roles := &IAMRoleStore{db.Database()}
			body := cfnProfileTemplate
			switch limit {
			case "roles":
				profileSQL(t, roles.db, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO aws_iam_roles SELECT 'quota-'||x,'AROA0000000000000000',?,'2026-01-01T00:00:00Z','',3600,1 FROM n`, lambdaServiceTrust)
			case "profiles":
				profileSQL(t, roles.db, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO aws_iam_instance_profiles SELECT 'quota-'||x,'AIPA0000000000000000','2026-01-01T00:00:00Z',1 FROM n`)
			case "aggregate":
				profileSQL(t, roles.db, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO identity_policies SELECT 'quota-'||x,? FROM n`, nativeReadPolicy)
				body = strings.Replace(body, "      RoleName: NativeRole", "      RoleName: NativeRole\n      Policies: [{PolicyName: Read, PolicyDocument: "+nativeReadPolicy+"}]", 1)
			case "legacy-case":
				policy, _ := core.ParsePolicy([]byte(nativeReadPolicy))
				if err := roles.MutateIdentityPolicy(context.Background(), "nativerole", &policy, true); err != nil {
					t.Fatal(err)
				}
			case "native-case":
				profileRole(t, roles, "nativerole")
			}
			before := cleanupACKFrame(t, roles.db)
			err := stacks.Create(cfnIAMCompile(t, body))
			if err == nil || cleanupACKFrame(t, roles.db) != before {
				t.Fatal("quota/ownership bypass or partial writes", err)
			}
		})
	}
}

func TestCFNNativeIAMMissingMarkerNeverDowngradesOwnership(t *testing.T) {
	db, _, stacks, _ := s3StackState(t)
	record := cfnIAMCompile(t, cfnProfileTemplate)
	if err := stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	saved, err := stacks.Records(record.Region, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range saved[0].Resources {
		if saved[0].Resources[i].Type == "AWS::IAM::Role" {
			saved[0].Resources[i].NativeIAM = nil
		}
	}
	data, _ := json.Marshal(saved[0])
	profileSQL(t, db.Database(), "UPDATE aws_cfn_stacks SET document=? WHERE id=?", string(data), record.ID)
	before := cleanupACKFrame(t, db.Database())
	if err = stacks.Delete(record.Region, record.ID); err == nil || cleanupACKFrame(t, db.Database()) != before {
		t.Fatal("missing native marker adopted a native name as legacy", err)
	}
}
