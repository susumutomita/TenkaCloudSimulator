package aws

import (
	"context"
	"testing"
)

func TestPrivateLambdaServicePermissionIsSourceBoundAndRevisionChecked(t *testing.T) {
	ctx := context.Background()
	db, functions, _ := lambdaState(t)
	function := LambdaFunction{Name: "permission-target", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Code: []byte("dummy")}
	if err := functions.save(&function, true, ""); err != nil {
		t.Fatal(err)
	}
	store, err := NewLambdaPermissionStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	source := "arn:aws:execute-api:us-east-1:000000000000:0123456789/prod/POST/wp-json/batch/v1"
	if allowed, e := store.AllowsAPI(ctx, function.Region, function.Name, source); e != nil || allowed {
		t.Fatal("missing resource grant allowed", allowed, e)
	}
	pattern := "arn:aws:execute-api:us-east-1:000000000000:0123456789/*/*/*"
	for _, bad := range []struct{ principal, action, source string }{{"*", "lambda:InvokeFunction", pattern}, {"apigateway.amazonaws.com", "*", pattern}, {"apigateway.amazonaws.com", "lambda:InvokeFunction", "arn:aws:execute-api:us-east-1:111111111111:0123456789/*/*/*"}, {"apigateway.amazonaws.com", "lambda:InvokeFunction", "arn:aws:execute-api:us-west-2:000000000000:0123456789/*/*/*"}, {"apigateway.amazonaws.com", "lambda:InvokeFunction", "arn:aws:execute-api:us-east-1:000000000000:*/*/*/*"}} {
		if _, err = store.Add(ctx, function.Region, function.Name, "invalid", bad.principal, bad.action, bad.source, ""); iamCode(err) != "InvalidParameterValueException" {
			t.Fatal("unbounded permission accepted", bad, err)
		}
	}
	grant, err := store.Add(ctx, function.Region, function.Name, "allow-api", "apigateway.amazonaws.com", "lambda:InvokeFunction", pattern, "")
	if err != nil || grant.Principal["Service"] != "apigateway.amazonaws.com" {
		t.Fatal(grant, err)
	}
	_, revision, err := store.Policy(ctx, function.Region, function.Name)
	if err != nil || revision == "" {
		t.Fatal(revision, err)
	}
	for _, test := range []struct {
		source string
		want   bool
	}{{source, true}, {"arn:aws:execute-api:us-east-1:000000000000:9999999999/prod/POST/wp-json/batch/v1", false}, {"arn:aws:execute-api:us-west-2:000000000000:0123456789/prod/POST/wp-json/batch/v1", false}, {pattern, false}} {
		if allowed, e := store.AllowsAPI(ctx, function.Region, function.Name, test.source); e != nil || allowed != test.want {
			t.Fatal(test, allowed, e)
		}
	}
	if _, err = store.Add(ctx, function.Region, function.Name, "allow-api", "apigateway.amazonaws.com", "lambda:InvokeFunction", pattern, revision); iamCode(err) != "ResourceConflictException" {
		t.Fatal("duplicate replaced policy", err)
	}
	if err = store.Remove(ctx, function.Region, function.Name, "allow-api", "stale"); iamCode(err) != "PreconditionFailedException" {
		t.Fatal("stale delete accepted", err)
	}
	if err = store.Remove(ctx, function.Region, function.Name, "allow-api", revision); err != nil {
		t.Fatal(err)
	}
	if allowed, e := store.AllowsAPI(ctx, function.Region, function.Name, source); e != nil || allowed {
		t.Fatal("revoked policy still grants", allowed, e)
	}
	if _, err = store.Add(ctx, function.Region, function.Name, "new-grant", "apigateway.amazonaws.com", "lambda:InvokeFunction", pattern, revision); iamCode(err) != "PreconditionFailedException" {
		t.Fatal("stale add accepted", err)
	}
	if _, err = store.Add(ctx, function.Region, function.Name, "new-grant", "apigateway.amazonaws.com", "lambda:InvokeFunction", pattern, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec("DELETE FROM aws_lambda_functions WHERE region=? AND name=?", function.Region, function.Name); err != nil {
		t.Fatal(err)
	}
	if err = functions.save(&function, true, ""); err != nil {
		t.Fatal(err)
	}
	if allowed, e := store.AllowsAPI(ctx, function.Region, function.Name, source); e != nil || allowed {
		t.Fatal("recreated function inherited old grant", allowed, e)
	}
}

func TestPrivateLambdaPermissionRollbackCancellationAndTeamIsolation(t *testing.T) {
	ctx := context.Background()
	first, functions, _ := lambdaState(t)
	second, otherFunctions, _ := lambdaState(t)
	function := LambdaFunction{Name: "same-function", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Code: []byte("dummy")}
	for _, store := range []*LambdaStore{functions, otherFunctions} {
		if err := store.save(&function, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	one, err := NewLambdaPermissionStore(first.Database())
	if err != nil {
		t.Fatal(err)
	}
	two, err := NewLambdaPermissionStore(second.Database())
	if err != nil {
		t.Fatal(err)
	}
	pattern := "arn:aws:execute-api:us-east-1:000000000000:0123456789/prod/POST/*"
	source := "arn:aws:execute-api:us-east-1:000000000000:0123456789/prod/POST/resource"
	if _, err = one.Add(ctx, function.Region, function.Name, "first-team", "apigateway.amazonaws.com", "lambda:InvokeFunction", pattern, ""); err != nil {
		t.Fatal(err)
	}
	if allowed, e := two.AllowsAPI(ctx, function.Region, function.Name, source); e != nil || allowed {
		t.Fatal("service policy leaked to other database", allowed, e)
	}
	if _, err = first.Database().Exec(`CREATE TRIGGER reject_resource_policy BEFORE UPDATE ON aws_lambda_resource_policies BEGIN SELECT RAISE(ABORT,'test policy persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = one.Remove(ctx, function.Region, function.Name, "first-team", ""); err == nil {
		t.Fatal("failed deletion reported success")
	}
	if allowed, e := one.AllowsAPI(ctx, function.Region, function.Name, source); e != nil || !allowed {
		t.Fatal("failed deletion changed existing grant", allowed, e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = two.Add(canceled, function.Region, function.Name, "late", "apigateway.amazonaws.com", "lambda:InvokeFunction", pattern, ""); err == nil {
		t.Fatal("canceled grant persisted")
	}
	policy, _, err := two.Policy(ctx, function.Region, function.Name)
	if err != nil || len(policy.Statement) != 0 {
		t.Fatal(policy, err)
	}
}

func TestLambdaServicePermissionNeverAcceptsOtherPrincipalsOrDamagedConditions(t *testing.T) {
	source := "arn:aws:execute-api:us-east-1:000000000000:0123456789/prod/GET/path"
	valid := LambdaResourceStatement{Effect: "Allow", Principal: map[string]string{"Service": "apigateway.amazonaws.com"}, Action: "lambda:InvokeFunction", Resource: lambdaARN("us-east-1", "target"), Condition: map[string]map[string]string{"ArnLike": {"AWS:SourceArn": "arn:aws:execute-api:us-east-1:000000000000:0123456789/prod/*/*"}}}
	for _, kind := range []string{"deny", "other-principal", "other-action", "other-resource", "missing-condition", "unbounded-source"} {
		statement := valid
		switch kind {
		case "deny":
			statement.Effect = "Deny"
		case "other-principal":
			statement.Principal = map[string]string{"Service": "s3.amazonaws.com"}
		case "other-action":
			statement.Action = "lambda:*"
		case "other-resource":
			statement.Resource = lambdaARN("us-east-1", "different")
		case "missing-condition":
			statement.Condition = nil
		case "unbounded-source":
			statement.Condition = map[string]map[string]string{"ArnLike": {"AWS:SourceArn": "*"}}
		}
		if lambdaPolicyAllowsAPI(LambdaResourcePolicy{Statement: []LambdaResourceStatement{statement}}, "us-east-1", "target", source) {
			t.Fatal("damaged or unsupported grant permitted invocation", kind)
		}
	}
	if localAPISource("us-east-1", source+"\n") {
		t.Fatal("control byte in service SourceArn accepted")
	}
}
