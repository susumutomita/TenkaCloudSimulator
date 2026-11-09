package server

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// This is a documentation contract check, not an IAM runtime or SDK test.
func TestIAMSSMBuiltinOpenAPIContract(t *testing.T) {
	raw, err := os.ReadFile("apidocs/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]struct {
			Post struct {
				Description string
				RequestBody struct {
					Content map[string]struct {
						Schema struct {
							OneOf []struct {
								Properties map[string]struct {
									Const       string
									Pattern     string
									Description string
									Enum        []string
								}
							}
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	op := spec.Paths["/_simulator/providers/aws/"].Post
	for _, phrase := range []string{"default version v2", "UnmodifiableEntity400", "Other AWS policy ARNs fail ARN validation with InvalidInput400", "ListPolicies Scope=Local excludes it", "legacy CFN metadata references grant nothing", "does not implement EC2, SSM agent, commands or sessions"} {
		if !strings.Contains(op.Description, phrase) {
			t.Errorf("missing boundary %q", phrase)
		}
	}
	count := 0
	for _, branch := range op.RequestBody.Content["application/x-www-form-urlencoded"].Schema.OneOf {
		action := branch.Properties["Action"].Const
		if action == "ListPolicies" {
			scope := branch.Properties["Scope"].Enum
			if len(scope) != 1 || scope[0] != "Local" {
				t.Fatal("ListPolicies catalog expanded", scope)
			}
		}
		arn, ok := branch.Properties["PolicyArn"]
		if !ok {
			continue
		}
		count++
		pattern, err := regexp.Compile(arn.Pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{"arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore", "arn:aws:iam::000000000000:policy/customer", "arn:aws:iam::000000000000:policy/AmazonSSMManagedInstanceCore"} {
			if !pattern.MatchString(value) {
				t.Errorf("%s rejects %s", action, value)
			}
		}
		for _, value := range []string{"arn:aws:iam::aws:policy/AdministratorAccess", "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore/extra", "arn:aws:iam::123456789012:policy/customer", "arn:aws:iam::aws:policy/amazonssmmanagedinstancecore", "arn:aws:iam::000000000000:policy/path/customer"} {
			if pattern.MatchString(value) {
				t.Errorf("%s accepts unsupported %s", action, value)
			}
		}
		if !strings.Contains(arn.Description, "mutations return UnmodifiableEntity400") {
			t.Errorf("%s omits immutable error", action)
		}
	}
	if count != 9 {
		t.Fatalf("expected nine exact ARN fields, got %d", count)
	}
}
