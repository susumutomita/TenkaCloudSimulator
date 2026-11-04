package server

import (
	"fmt"
	"strings"
	"testing"
)

// Exercises the native authorization + ParameterStore seams corresponding to
// SSMParameterState. It is runtime evidence, not a Go-to-TLA refinement proof.
func TestSSMFormalLatestRowIsolationAndExplicitDeny(t *testing.T) {
	server := instance(t)
	type cell struct {
		region, name, value string
		version             int
	}
	expected := []cell{
		{"us-east-1", "/formal/alpha", "east-alpha-2", 2},
		{"us-east-1", "/formal/beta", "east-beta-1", 1},
		{"ap-northeast-1", "/formal/alpha", "tokyo-alpha-3", 3},
		{"ap-northeast-1", "/formal/beta", "tokyo-beta-4", 4},
	}
	for _, item := range expected {
		for version := 1; version <= item.version; version++ {
			value := fmt.Sprintf("seed-%s-%s-%d", item.region, item.name, version)
			if version == item.version {
				value = item.value
			}
			out := ssmCall(t, server, "PutParameter", map[string]any{"Name": item.name, "Type": "String", "Value": value, "Overwrite": version > 1}, 200, map[string]string{"X-Simulator-Region": item.region})
			if out["Version"] != float64(version) {
				t.Fatalf("seed version at %s/%s: %v", item.region, item.name, out)
			}
		}
	}
	assertState := func() {
		t.Helper()
		for _, item := range expected {
			out := ssmCall(t, server, "GetParameter", map[string]any{"Name": item.name}, 200, map[string]string{"X-Simulator-Region": item.region})
			parameter, ok := out["Parameter"].(map[string]any)
			arn := "arn:aws:ssm:" + item.region + ":000000000000:parameter/" + strings.TrimPrefix(item.name, "/")
			if !ok || parameter["Name"] != item.name || parameter["Value"] != item.value || parameter["Version"] != float64(item.version) || parameter["ARN"] != arn {
				t.Fatalf("wrong latest row at %s/%s: %v", item.region, item.name, out)
			}
		}
	}
	assertState()
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ssm:PutParameter","ssm:DeleteParameter","ssm:GetParameter"],"Resource":"*"},{"Effect":"Deny","Action":["ssm:PutParameter","ssm:DeleteParameter","ssm:GetParameter"],"Resource":"arn:aws:ssm:*:000000000000:parameter/formal/*"}]}`
	status(t, server, "PUT", "/_simulator/v1/iam/principals/formal-explicit-deny/policy", policy, 200, nil)
	for _, item := range expected {
		headers := map[string]string{"X-Simulator-Region": item.region, "X-Simulator-Principal": "formal-explicit-deny"}
		for _, op := range []string{"PutParameter", "DeleteParameter", "GetParameter"} {
			input := map[string]any{"Name": item.name}
			if op == "PutParameter" {
				input["Type"] = "String"
				input["Value"] = "denied replacement must not persist"
				input["Overwrite"] = true
			}
			out := ssmCall(t, server, op, input, 403, headers)
			message, _ := out["message"].(string)
			if out["__type"] != "AccessDeniedException" || !strings.Contains(message, "explicitDeny") || out["Parameter"] != nil {
				t.Fatalf("expected explicit Deny without read data for %s: %v", op, out)
			}
			// Check every address after each rejected operation, not only its HTTP code.
			assertState()
		}
		out := ssmCall(t, server, "PutParameter", map[string]any{"Name": item.name, "Type": "String", "Value": "unapproved no-overwrite replacement", "Overwrite": false}, 400, map[string]string{"X-Simulator-Region": item.region})
		if out["__type"] != "ParameterAlreadyExists" {
			t.Fatal("wrong no-overwrite refusal", out)
		}
		assertState()
	}
	// An accepted overwrite advances only this exact regional/name address.
	target := &expected[0]
	target.value = "east-alpha-accepted"
	target.version++
	out := ssmCall(t, server, "PutParameter", map[string]any{"Name": target.name, "Type": "String", "Value": target.value, "Overwrite": true}, 200, map[string]string{"X-Simulator-Region": target.region})
	if out["Version"] != float64(target.version) {
		t.Fatal("accepted overwrite did not advance latest version", out)
	}
	assertState()
	// Native deletion has no AWS recreation delay. Recreate starts at one; other
	// addresses retain their own different values/versions, including versions >2.
	deleted := expected[len(expected)-1]
	expected = expected[:len(expected)-1]
	headers := map[string]string{"X-Simulator-Region": deleted.region}
	ssmCall(t, server, "DeleteParameter", map[string]any{"Name": deleted.name}, 200, headers)
	missing := ssmCall(t, server, "GetParameter", map[string]any{"Name": deleted.name}, 400, headers)
	if missing["__type"] != "ParameterNotFound" {
		t.Fatal("deleted row still readable", missing)
	}
	assertState()
	deleted.value = "tokyo-beta-recreated"
	deleted.version = 1
	recreated := ssmCall(t, server, "PutParameter", map[string]any{"Name": deleted.name, "Type": "String", "Value": deleted.value}, 200, headers)
	if recreated["Version"] != float64(1) {
		t.Fatal("recreation did not reset latest-row version", recreated)
	}
	expected = append(expected, deleted)
	assertState()
}
