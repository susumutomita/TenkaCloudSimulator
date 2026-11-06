package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestNativeIAMReferenceExamplesAndLegacyOwnershipBoundary(t *testing.T) {
	s := instance(t)
	document, err := assets.ReadFile("apidocs/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var paths struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err = json.Unmarshal(document, &paths); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/", "/_simulator/providers/aws/"} {
		var post struct {
			RequestBody struct {
				Content map[string]struct {
					Examples map[string]struct {
						Value map[string]any `json:"value"`
					} `json:"examples"`
				} `json:"content"`
			} `json:"requestBody"`
		}
		if err = json.Unmarshal(paths.Paths[endpoint]["post"], &post); err != nil {
			t.Fatal(err)
		}
		examples := post.RequestBody.Content["application/x-www-form-urlencoded"].Examples
		headers := map[string]string{"X-Simulator-Service": "iam", "Content-Type": "application/x-www-form-urlencoded"}
		executed := map[string]bool{}
		for _, action := range []string{"CreateRole", "GetRole", "ListRoles", "PutRolePolicy", "GetRolePolicy", "ListRolePolicies", "UpdateAssumeRolePolicy", "CreatePolicy", "GetPolicy", "ListPolicies", "AttachRolePolicy", "ListAttachedRolePolicies", "CreatePolicyVersion", "GetPolicyVersion", "ListPolicyVersions", "SetDefaultPolicyVersion", "DeletePolicyVersion", "DetachRolePolicy", "DeletePolicy", "DeleteRolePolicy", "DeleteRole"} {
			if action == "SetDefaultPolicyVersion" || action == "DeletePolicyVersion" {
				status(t, s, "POST", endpoint, "Action=SetDefaultPolicyVersion&Version=2010-05-08&PolicyArn=arn%3Aaws%3Aiam%3A%3A000000000000%3Apolicy%2FReadObjects&VersionId=v1", 200, headers)
			}
			fields, exists := examples[action]
			if !exists {
				t.Fatal("Missing advertised IAM example", action)
			}
			input := url.Values{}
			for name, value := range fields.Value {
				input.Set(name, fmt.Sprint(value))
			}
			body := status(t, s, "POST", endpoint, input.Encode(), 200, headers)
			executed[action] = true
			if !strings.Contains(string(body), "<"+action+"Response") {
				t.Fatal(action, string(body))
			}
			if action == "CreateRole" {
				for _, method := range []string{"PUT", "DELETE"} {
					status(t, s, method, "/_simulator/v1/iam/principals/PRACTICE-EXECUTOR/policy", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`, 409, nil)
				}
			}
			if action == "PutRolePolicy" {
				status(t, s, "POST", endpoint, "Action=DeleteRole&Version=2010-05-08&RoleName=practice-executor", 409, headers)
			}
		}
		status(t, s, "PUT", "/_simulator/v1/iam/principals/practice-executor/policy", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`, 409, nil)
		status(t, s, "POST", endpoint, "Action=GetRole&Version=2010-05-08&RoleName=practice-executor", 404, headers)
		var capabilities struct {
			Services map[string]struct {
				Operations []string `json:"operations"`
			} `json:"services"`
		}
		if err = json.Unmarshal(status(t, s, "GET", "/_simulator/v1/capabilities", "", 200, nil), &capabilities); err != nil {
			t.Fatal(err)
		}
		if len(capabilities.Services["iam"].Operations) != 21 {
			t.Fatal(capabilities)
		}
		for _, action := range capabilities.Services["iam"].Operations {
			if !executed[action] {
				t.Fatal("Capability lacks executed reference example", action)
			}
		}
	}
}
