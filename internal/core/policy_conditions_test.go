package core

import (
	"encoding/json"
	"testing"
)

func TestPolicyConditionOfficialStringContract(t *testing.T) {
	// AWS IAM condition-operators documentation: positive string predicates are
	// false when a context key is absent; StringEquals is case-sensitive;
	// StringLike alone interprets * and ?. Multiple keys/operators AND, values OR.
	raw := `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"ec2:Describe*","Resource":"*","Condition":{"StringEquals":{"aws:ResourceTag/TenkaCloud:NamePrefix":["tc-team-a","tc-team-b"]},"StringLike":{"aws:userid":"role:alice?"}}}}`
	p, err := ParsePolicy([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		ctx  PolicyContext
		want string
	}{
		{"missing", nil, "implicitDeny"},
		{"one-key", PolicyContext{"aws:userid": "role:alice1"}, "implicitDeny"},
		{"match", PolicyContext{"aws:userid": "role:alice1", "aws:ResourceTag/TenkaCloud:NamePrefix": "tc-team-b"}, "allowed"},
		{"wrong-case-value", PolicyContext{"aws:userid": "role:alice1", "aws:ResourceTag/TenkaCloud:NamePrefix": "TC-TEAM-B"}, "implicitDeny"},
		{"key-case", PolicyContext{"AWS:USERID": "role:alice1", "AWS:RESOURCETAG/TenkaCloud:NamePrefix": "tc-team-b"}, "allowed"},
		{"ambiguous-context", PolicyContext{"aws:userid": "role:alice1", "AWS:USERID": "role:alice1", "aws:ResourceTag/TenkaCloud:NamePrefix": "tc-team-b"}, "implicitDeny"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EvaluateWithContext(p, "ec2:DescribeInstances", "*", c.ctx); got != c.want {
				t.Fatalf("got %s want %s", got, c.want)
			}
		})
	}
	if Evaluate(p, "ec2:DescribeInstances", "*") != "implicitDeny" {
		t.Fatal("legacy evaluator silently ignored conditions")
	}
	encoded, _ := json.Marshal(p)
	if _, err := ParsePolicy(encoded); err != nil {
		t.Fatal(err)
	}
}
func TestPolicyConditionSessionVariable(t *testing.T) {
	p, err := ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"ssm:TerminateSession","Resource":"arn:aws:ssm:r:0:session/${aws:userid}-*","Condition":{"StringLike":{"ssm:resourceTag/aws:ssmmessages:session-id":["${aws:userid}*"]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ id, tag, resource, want string }{
		{"role:alice", "role:alice-123", "arn:aws:ssm:r:0:session/role:alice-123", "allowed"},
		{"role:alice", "role:bob-123", "arn:aws:ssm:r:0:session/role:alice-123", "implicitDeny"},
		{"role:alice", "role:alice-123", "arn:aws:ssm:r:0:session/role:bob-123", "implicitDeny"},
		{"role:*", "role:bob-123", "arn:aws:ssm:r:0:session/role:bob-123", "implicitDeny"},
		{"role:*", "role:*-123", "arn:aws:ssm:r:0:session/role:*-123", "allowed"},
		{"", "anything", "arn:aws:ssm:r:0:session/-123", "implicitDeny"},
	} {
		ctx := PolicyContext{"aws:userid": c.id, "ssm:resourceTag/aws:ssmmessages:session-id": c.tag}
		if got := EvaluateWithContext(p, "ssm:TerminateSession", c.resource, ctx); got != c.want {
			t.Fatalf("%+v got %s", c, got)
		}
	}
	if Evaluate(p, "ssm:TerminateSession", "arn:aws:ssm:r:0:session/role:alice-123") != "implicitDeny" {
		t.Fatal("missing userid granted")
	}
}
func TestPolicyConditionInvalidInputs(t *testing.T) {
	cases := []string{`null`, `{}`, `{"Bool":{"aws:userid":"true"}}`, `{"StringEquals":{}}`, `{"StringEquals":{"unknown:key":"a"}}`, `{"StringEquals":{"aws:userid":null}}`, `{"StringEquals":{"aws:userid":[]}}`, `{"StringEquals":{"aws:userid":"${aws:username}"}}`, `{"StringEquals":{"aws:userid":"a","AWS:USERID":"b"}}`, `{"StringEquals":{"aws:userid":"a"},"StringEquals":{"aws:userid":"b"}}`, `{"StringLike":{"aws:userid":123}}`}
	for _, condition := range cases {
		raw := `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"*","Resource":"*","Condition":` + condition + `}}`
		if _, err := ParsePolicy([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", condition)
		}
	}
}
func TestPolicyConditionExplicitDenyAndLiteralEquals(t *testing.T) {
	p, err := ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"},{"Effect":"Deny","Action":"*","Resource":"*","Condition":{"StringEquals":{"aws:userid":"alice*"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ id, want string }{{"alice1", "allowed"}, {"alice*", "explicitDeny"}} {
		if got := EvaluateWithContext(p, "x:y", "resource", PolicyContext{"aws:userid": c.id}); got != c.want {
			t.Fatalf("%s got %s", c.id, got)
		}
	}
}

func TestPolicyConditionInvalidTypedDenyIsNotDropped(t *testing.T) {
	p := Policy{Version: "2012-10-17", Statement: Statements{
		{Effect: "Allow", Action: StringList{"*"}, Resource: StringList{"*"}},
		{Effect: "Deny", Action: StringList{"*"}, Resource: StringList{"*"}, Condition: PolicyConditions{"Bool": {"aws:userid": StringList{"true"}}}},
	}}
	if got := EvaluateWithContext(p, "x:y", "*", nil); got != "implicitDeny" {
		t.Fatalf("invalid deny disappeared: %s", got)
	}
}

func TestPolicyConditionDuplicateStatementAndVariables(t *testing.T) {
	for _, fields := range []string{
		`"Condition":{"StringEquals":{"aws:userid":"alice"}},"Condition":{"StringEquals":{"aws:userid":"bob"}}`,
		`"Condition":{"StringEquals":{"aws:userid":"alice"}},"condition":{"StringEquals":{"aws:userid":"bob"}}`,
		`"Action":"ssm:TerminateSession","action":"*"`,
	} {
		raw := `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"ssm:TerminateSession","Resource":"*",` + fields + `}}`
		if _, err := ParsePolicy([]byte(raw)); err == nil {
			t.Fatalf("accepted repeated clause: %s", raw)
		}
	}
	for _, resource := range []string{"arn:x:y:z:a:${aws:username}", "arn:x:y:z:a:${aws:userid"} {
		raw := `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"*","Resource":"` + resource + `"}}`
		if _, err := ParsePolicy([]byte(raw)); err == nil {
			t.Fatalf("accepted unsupported variable: %s", resource)
		}
	}
	if _, err := ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"${aws:userid}","Resource":"*"}}`)); err == nil {
		t.Fatal("Action variable accepted")
	}
}

func TestPolicyConditionTypedUnknownVariableDeny(t *testing.T) {
	for _, bad := range []Statement{
		{Effect: "Deny", Action: StringList{"*"}, Resource: StringList{"arn:x:y:z:a:${unknown:key}"}},
		{Effect: "Deny", Action: StringList{"${aws:userid}"}, Resource: StringList{"*"}},
	} {
		p := Policy{Version: "2012-10-17", Statement: Statements{{Effect: "Allow", Action: StringList{"*"}, Resource: StringList{"*"}}, bad}}
		if got := EvaluateWithContext(p, "x:y", "resource", nil); got != "implicitDeny" {
			t.Fatalf("invalid typed denial disappeared: %s", got)
		}
	}
}

func TestPolicyConditionUnimplementedProviderCannotDropDeny(t *testing.T) {
	p, err := ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"},{"Effect":"Deny","Action":"*","Resource":"*","Condition":{"StringEquals":{"aws:ResourceTag/classification":"private"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := Evaluate(p, "s3:GetObject", "arn:aws:s3:::bucket/key"); got != "implicitDeny" {
		t.Fatalf("legacy provider dropped deny: %s", got)
	}
	if got := EvaluateWithContext(p, "s3:GetObject", "arn:aws:s3:::bucket/key", nil); got != "implicitDeny" {
		t.Fatalf("nil context dropped deny: %s", got)
	}
	if got := EvaluateWithContext(p, "s3:GetObject", "arn:aws:s3:::bucket/key", PolicyContext{}); got != "allowed" {
		t.Fatalf("provider-confirmed absent key is not IAM absence: %s", got)
	}
	if got := EvaluateWithContext(p, "s3:GetObject", "arn:aws:s3:::bucket/key", PolicyContext{"aws:ResourceTag/classification": "private"}); got != "explicitDeny" {
		t.Fatalf("actual private resource not denied: %s", got)
	}
}
