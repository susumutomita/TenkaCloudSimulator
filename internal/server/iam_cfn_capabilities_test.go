package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCFNNativeIAMCapabilitiesStayBounded(t *testing.T) {
	s := instance(t)
	raw := status(t, s, "GET", "/_simulator/v1/capabilities", "", 200, nil)
	var capabilities struct {
		Services map[string]map[string]json.RawMessage
	}
	if err := json.Unmarshal(raw, &capabilities); err != nil {
		t.Fatal(err)
	}
	iam := capabilities.Services["iam"]
	if string(iam["instanceProfileCloudFormation"]) != "true" || string(iam["instanceProfileCredentials"]) != "false" || string(iam["instanceProfileConsole"]) != "false" || string(iam["awsManagedPolicies"]) != "true" || string(iam["awsManagedPolicyCatalog"]) != "false" {
		t.Fatal("profile capability exceeds implemented boundary", iam)
	}
	if string(iam["awsManagedPolicyBuiltins"]) != `[{"arn":"arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore","defaultVersionId":"v2","readOnly":true}]` {
		t.Fatal("builtin allowlist or version drift", iam)
	}
	if !strings.Contains(string(iam["managedPolicyScope"]), "excludes the builtin") {
		t.Fatal("ListPolicies scope overclaims builtin catalog", iam)
	}
	var types []string
	if err := json.Unmarshal(capabilities.Services["cloudformation"]["resourceTypes"], &types); err != nil {
		t.Fatal(err)
	}
	profile := 0
	for _, kind := range types {
		if kind == "AWS::IAM::InstanceProfile" {
			profile++
		}
		if strings.HasPrefix(kind, "AWS::EC2::") {
			t.Fatal("CFN profile advertised EC2 runtime", kind)
		}
	}
	if profile != 1 {
		t.Fatal(types)
	}
	var operations []string
	if err := json.Unmarshal(capabilities.Services["ssm"]["operations"], &operations); err != nil {
		t.Fatal(err)
	}
	for _, action := range operations {
		if action == "StartSession" || action == "SendCommand" || action == "UpdateInstanceInformation" {
			t.Fatal("profile advertised SSM instance execution", action)
		}
	}
	if strings.Contains(string(raw), "hello-world-battle") {
		t.Fatal("profile advertised original Battle")
	}
}
