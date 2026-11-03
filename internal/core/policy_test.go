package core

import "testing"

func TestPolicyEvaluation(t *testing.T) {
	p, err := ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:Get*","s3:ListBucket"],"Resource":["arn:aws:s3:::example","arn:aws:s3:::example/*"]},{"Effect":"Deny","Action":"s3:GetObject","Resource":"arn:aws:s3:::example/private/*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ action, resource, want string }{{"s3:GetObject", "arn:aws:s3:::example/public/file", "allowed"}, {"S3:GETOBJECT", "arn:aws:s3:::example/public/file", "allowed"}, {"s3:GetObject", "arn:aws:s3:::example/private/key", "explicitDeny"}, {"s3:PutObject", "arn:aws:s3:::example/public/file", "implicitDeny"}, {"s3:GetObject", "arn:aws:s3:::EXAMPLE/public/file", "implicitDeny"}, {"s3:ListBucket", "arn:aws:s3:::example", "allowed"}} {
		if got := Evaluate(p, tc.action, tc.resource); got != tc.want {
			t.Fatalf("%s %s: %s", tc.action, tc.resource, got)
		}
	}
	p.Statement[0], p.Statement[1] = p.Statement[1], p.Statement[0]
	if Evaluate(p, "s3:GetObject", "arn:aws:s3:::example/private/file") != "explicitDeny" {
		t.Fatal("deny depended on order")
	}
	if !wildcard("a?/*", "ab/with/slashes") || wildcard("a?", "abc") {
		t.Fatal("wildcard matching is incorrect")
	}
}
func TestUnsupportedPolicyNeverSilentlyAccepted(t *testing.T) {
	for _, document := range []string{
		`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"*","Resource":"*","Condition":{}}}`,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","NotAction":"*","Resource":"*"}]}`,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*","Principal":"*"}]}`,
		`{"Version":"2012-10-17","Statement":[]}`,
		`{"Version":"2012-10-17","Statement":{"Effect":"Maybe","Action":"*","Resource":"*"}}`,
		`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":[],"Resource":"*"}}`,
		`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"*","Resource":"*"}} {}`,
	} {
		if _, err := ParsePolicy([]byte(document)); err == nil {
			t.Errorf("accepted %s", document)
		}
	}
}
