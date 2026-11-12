package aws

import (
	"strings"
	"testing"
)

func nativeRoleProps() map[string]any {
	return map[string]any{
		"AssumeRolePolicyDocument": map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Principal": map[string]any{"Service": "ec2.amazonaws.com"}, "Action": "sts:AssumeRole"}}},
	}
}

func TestCompileStackNativeIAMDerivesBoundedNames(t *testing.T) {
	long := strings.Repeat("s", 120)
	name, state, err := compileStackNativeIAM(long, "stack-id", "Role", "AWS::IAM::Role", "CAPABILITY_IAM", nativeRoleProps())
	if err != nil || state == nil || len(name) != 64 || !iamRoleName.MatchString(name) {
		t.Fatalf("derived role name %q %v", name, err)
	}
	again, _, _ := compileStackNativeIAM(long, "stack-id", "Role", "AWS::IAM::Role", "CAPABILITY_IAM", nativeRoleProps())
	other, _, _ := compileStackNativeIAM(long, "stack-other", "Role", "AWS::IAM::Role", "CAPABILITY_IAM", nativeRoleProps())
	if again != name || other == name {
		t.Fatalf("derived names must be stable per stack incarnation: %q %q %q", name, again, other)
	}
	profile, pstate, err := compileStackNativeIAM(long, "stack-id", "Profile", "AWS::IAM::InstanceProfile", "CAPABILITY_IAM", map[string]any{"Roles": []any{"role-a"}})
	if err != nil || len(profile) != 128 || pstate.RoleName != "role-a" {
		t.Fatalf("derived profile %q %+v %v", profile, pstate, err)
	}
	single := nativeRoleProps()
	single["AssumeRolePolicyDocument"].(map[string]any)["Statement"] = map[string]any{"Effect": "Allow", "Principal": map[string]any{"Service": "ec2.amazonaws.com"}, "Action": "sts:AssumeRole"}
	single["MaxSessionDuration"] = "7200"
	single["Description"] = "worker"
	single["ManagedPolicyArns"] = []any{iamSSMCoreARN}
	single["Policies"] = []any{map[string]any{"PolicyName": "read", "PolicyDocument": map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": "s3:GetObject", "Resource": "*"}}}}}
	if _, _, err = compileStackNativeIAM("s", "stack-id", "Role", "AWS::IAM::Role", "CAPABILITY_IAM", single); err != nil {
		t.Fatal("single trust statement object refused:", err)
	}
}

func TestCompileStackNativeIAMRefusesUnsupportedProperties(t *testing.T) {
	big := strings.Repeat("a", 33<<10)
	role := func(edit func(map[string]any)) map[string]any { p := nativeRoleProps(); edit(p); return p }
	trust := func(stmt map[string]any) func(map[string]any) {
		return func(p map[string]any) {
			p["AssumeRolePolicyDocument"] = map[string]any{"Version": "2012-10-17", "Statement": stmt}
		}
	}
	for name, tc := range map[string]struct {
		kind, capability string
		props            map[string]any
	}{
		"no capability":       {"AWS::IAM::Role", "", nativeRoleProps()},
		"unknown property":    {"AWS::IAM::Role", "CAPABILITY_IAM", role(func(p map[string]any) { p["Bogus"] = 1 })},
		"non-root path":       {"AWS::IAM::Role", "CAPABILITY_IAM", role(func(p map[string]any) { p["Path"] = "/x/" })},
		"empty name":          {"AWS::IAM::Role", "CAPABILITY_NAMED_IAM", role(func(p map[string]any) { p["RoleName"] = "" })},
		"non-string name":     {"AWS::IAM::Role", "CAPABILITY_NAMED_IAM", role(func(p map[string]any) { p["RoleName"] = 7 })},
		"named without named": {"AWS::IAM::Role", "CAPABILITY_IAM", role(func(p map[string]any) { p["RoleName"] = "r" })},
		"reserved name":       {"AWS::IAM::Role", "CAPABILITY_NAMED_IAM", role(func(p map[string]any) { p["RoleName"] = "SIMULATOR-ADMIN" })},
		"tags":                {"AWS::IAM::Role", "CAPABILITY_IAM", role(func(p map[string]any) { p["Tags"] = []any{map[string]any{"Key": "k"}} })},
		"two managed":         {"AWS::IAM::Role", "CAPABILITY_IAM", role(func(p map[string]any) { p["ManagedPolicyArns"] = []any{iamSSMCoreARN, iamSSMCoreARN} })},
		"foreign managed":     {"AWS::IAM::Role", "CAPABILITY_IAM", role(func(p map[string]any) { p["ManagedPolicyArns"] = []any{"arn:aws:iam::aws:policy/AdministratorAccess"} })},
		"aws principal":       {"AWS::IAM::Role", "CAPABILITY_IAM", role(trust(map[string]any{"Effect": "Allow", "Principal": map[string]any{"AWS": "arn:aws:iam::000000000000:root"}, "Action": "sts:AssumeRole"}))},
		"conditioned trust":   {"AWS::IAM::Role", "CAPABILITY_IAM", role(trust(map[string]any{"Effect": "Allow", "Principal": map[string]any{"Service": "ec2.amazonaws.com"}, "Action": "sts:AssumeRole", "Condition": map[string]any{"Bool": map[string]any{"aws:SecureTransport": "true"}}}))},
		"non-string desc":     {"AWS::IAM::Role", "CAPABILITY_IAM", role(func(p map[string]any) { p["Description"] = 1 })},
		"short session":       {"AWS::IAM::Role", "CAPABILITY_IAM", role(func(p map[string]any) { p["MaxSessionDuration"] = "60" })},
		"policies not list":   {"AWS::IAM::Role", "CAPABILITY_IAM", role(func(p map[string]any) { p["Policies"] = "x" })},
		"duplicate policy": {"AWS::IAM::Role", "CAPABILITY_IAM", role(func(p map[string]any) {
			pol := map[string]any{"PolicyName": "a", "PolicyDocument": map[string]any{}}
			p["Policies"] = []any{pol, pol}
		})},
		"oversized policy": {"AWS::IAM::Role", "CAPABILITY_IAM", role(func(p map[string]any) {
			p["Policies"] = []any{map[string]any{"PolicyName": "a", "PolicyDocument": map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": "s3:GetObject", "Resource": big}}}}}
		})},
		"invalid policy": {"AWS::IAM::Role", "CAPABILITY_IAM", role(func(p map[string]any) {
			p["Policies"] = []any{map[string]any{"PolicyName": "a", "PolicyDocument": map[string]any{"Statement": "nope"}}}
		})},
		"invalid profile name": {"AWS::IAM::InstanceProfile", "CAPABILITY_NAMED_IAM", map[string]any{"InstanceProfileName": "bad name", "Roles": []any{}}},
		"two profile roles":    {"AWS::IAM::InstanceProfile", "CAPABILITY_IAM", map[string]any{"Roles": []any{"a", "b"}}},
		"invalid profile role": {"AWS::IAM::InstanceProfile", "CAPABILITY_IAM", map[string]any{"Roles": []any{"bad role"}}},
		"role prop on profile": {"AWS::IAM::InstanceProfile", "CAPABILITY_IAM", map[string]any{"RoleName": "a", "Roles": []any{}}},
	} {
		if physical, state, err := compileStackNativeIAM("stack", "stack-id", "Logical", tc.kind, tc.capability, tc.props); err == nil || physical != "" || state != nil {
			t.Errorf("%s accepted: %q %+v", name, physical, state)
		}
	}
}
