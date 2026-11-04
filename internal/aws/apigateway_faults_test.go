package aws

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestRESTPersistenceFailuresDoNotReportSuccess(t *testing.T) {
	ctx := context.Background()
	db, functions, _ := lambdaState(t)
	apis, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := NewLambdaPermissionStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	function := LambdaFunction{Name: "fault-target", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Code: []byte("dummy")}
	if err = functions.save(&function, true, ""); err != nil {
		t.Fatal(err)
	}
	api, err := apis.CreateAPI(ctx, "us-east-1", "fault-api", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = apis.PutMethod(ctx, "us-east-1", api.ID, api.RootResourceID, "GET", nil); err != nil {
		t.Fatal(err)
	}
	uri := "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/" + lambdaARN("us-east-1", function.Name) + "/invocations"
	if _, err = apis.PutIntegration(ctx, "us-east-1", api.ID, api.RootResourceID, "GET", uri); err != nil {
		t.Fatal(err)
	}
	inject := func(table, event string) {
		t.Helper()
		if _, e := db.Database().Exec("CREATE TRIGGER reject_rest BEFORE " + event + " ON " + table + " BEGIN SELECT RAISE(ABORT,'test REST persistence failure'); END"); e != nil {
			t.Fatal(e)
		}
	}
	clear := func() {
		t.Helper()
		if _, e := db.Database().Exec("DROP TRIGGER reject_rest"); e != nil {
			t.Fatal(e)
		}
	}
	inject("aws_apigateway_resources", "INSERT")
	if _, err = apis.CreateResource(ctx, "us-east-1", api.ID, api.RootResourceID, "must-not-exist"); err == nil {
		t.Fatal("failed resource insert succeeded")
	}
	clear()
	resources, err := apis.Resources(ctx, "us-east-1", api.ID, "", 100)
	if err != nil || len(resources) != 1 {
		t.Fatal("partial resource survived", resources, err)
	}
	inject("aws_apigateway_resources", "UPDATE")
	if _, err = apis.PutIntegration(ctx, "us-east-1", api.ID, api.RootResourceID, "GET", strings.Replace(uri, "fault-target", "replacement", 1)); err == nil {
		t.Fatal("failed integration update succeeded")
	}
	clear()
	root, err := apis.Resource(ctx, "us-east-1", api.ID, api.RootResourceID)
	if err != nil || root.ResourceMethods["GET"].MethodIntegration.URI != uri {
		t.Fatal("failed update changed integration", root, err)
	}
	inject("aws_apigateway_deployments", "INSERT")
	if _, err = apis.CreateDeployment(ctx, "us-east-1", api.ID, ""); err == nil {
		t.Fatal("failed snapshot insert succeeded")
	}
	clear()
	var count int
	if err = db.Database().QueryRow("SELECT count(*) FROM aws_apigateway_deployments").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	deployment, err := apis.CreateDeployment(ctx, "us-east-1", api.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	inject("aws_apigateway_stages", "INSERT")
	if _, err = apis.CreateStage(ctx, "us-east-1", api.ID, "prod", deployment.ID, nil); err == nil {
		t.Fatal("failed stage insert succeeded")
	}
	clear()
	if _, err = apis.Stage(ctx, "us-east-1", api.ID, "prod"); iamCode(err) != "NotFoundException" {
		t.Fatal("failed stage persisted", err)
	}
	if _, err = apis.CreateStage(ctx, "us-east-1", api.ID, "prod", deployment.ID, nil); err != nil {
		t.Fatal(err)
	}
	inject("aws_apigateway_apis", "DELETE")
	if err = apis.DeleteAPI(ctx, "us-east-1", api.ID); err == nil {
		t.Fatal("failed API deletion succeeded")
	}
	clear()
	if _, err = apis.Stage(ctx, "us-east-1", api.ID, "prod"); err != nil {
		t.Fatal("API deletion did not restore stages on rollback", err)
	}
	inject("aws_lambda_resource_policies", "INSERT")
	if _, err = permissions.Add(ctx, "us-east-1", function.Name, "fault-grant", "apigateway.amazonaws.com", "lambda:InvokeFunction", "arn:aws:execute-api:us-east-1:000000000000:"+api.ID+"/prod/*/*", ""); err == nil {
		t.Fatal("failed service grant succeeded")
	}
	clear()
	policy, _, err := permissions.Policy(ctx, "us-east-1", function.Name)
	if err != nil || len(policy.Statement) != 0 {
		t.Fatal("failed policy persisted", policy, err)
	}
}
func TestRESTQuotasAndServicePermissionSizeAreEnforced(t *testing.T) {
	ctx := context.Background()
	db, functions, _ := lambdaState(t)
	apis, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	api, err := apis.CreateAPI(ctx, "us-east-1", "quota-api", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 99; i++ {
		if _, err = apis.CreateResource(ctx, "us-east-1", api.ID, api.RootResourceID, fmt.Sprint("child-", i)); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err = apis.CreateResource(ctx, "us-east-1", api.ID, api.RootResourceID, "over-limit"); iamCode(err) != "LimitExceededException" {
		t.Fatal("resource quota not enforced", err)
	}
	if _, err = apis.PutMethod(ctx, "us-east-1", api.ID, api.RootResourceID, "GET", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = apis.PutIntegration(ctx, "us-east-1", api.ID, api.RootResourceID, "GET", "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:handler/invocations"); err != nil {
		t.Fatal(err)
	}
	deployment, err := apis.CreateDeployment(ctx, "us-east-1", api.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err = apis.CreateStage(ctx, "us-east-1", api.ID, fmt.Sprint("stage-", i), deployment.ID, nil); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err = apis.CreateStage(ctx, "us-east-1", api.ID, "overflow", deployment.ID, nil); iamCode(err) != "LimitExceededException" {
		t.Fatal("stage quota not enforced", err)
	}
	for i := 1; i < 100; i++ {
		if _, err = apis.CreateDeployment(ctx, "us-east-1", api.ID, ""); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err = apis.CreateDeployment(ctx, "us-east-1", api.ID, ""); iamCode(err) != "LimitExceededException" {
		t.Fatal("snapshot quota not enforced", err)
	}
	for i := 1; i < 100; i++ {
		if _, err = apis.CreateAPI(ctx, "us-east-1", fmt.Sprint("api-", i), "", nil); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err = apis.CreateAPI(ctx, "us-west-2", "overflow", "", nil); iamCode(err) != "LimitExceededException" {
		t.Fatal("global API quota not enforced", err)
	}
	permissions, err := NewLambdaPermissionStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"count-policy", "size-policy"} {
		if err = functions.save(&LambdaFunction{Name: name, Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Code: []byte("dummy")}, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	source := "arn:aws:execute-api:us-east-1:000000000000:" + api.ID + "/prod/*/*"
	for i := 0; i < 20; i++ {
		if _, err = permissions.Add(ctx, "us-east-1", "count-policy", fmt.Sprint("grant-", i), "apigateway.amazonaws.com", "lambda:InvokeFunction", source, ""); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err = permissions.Add(ctx, "us-east-1", "count-policy", "overflow", "apigateway.amazonaws.com", "lambda:InvokeFunction", source, ""); iamCode(err) != "PolicyLengthExceededException" {
		t.Fatal("resource policy count quota not enforced", err)
	}
	for i := 0; i < 20; i++ {
		_, err = permissions.Add(ctx, "us-east-1", "size-policy", fmt.Sprint("grant-", i), "apigateway.amazonaws.com", "lambda:InvokeFunction", strings.TrimSuffix(source, "*")+strings.Repeat("a", 850), "")
		if err != nil {
			if iamCode(err) != "PolicyLengthExceededException" {
				t.Fatal(err)
			}
			policy, _, e := permissions.Policy(ctx, "us-east-1", "size-policy")
			if e != nil || len(policy.Statement) != i {
				t.Fatal("oversized grant changed policy", policy, e)
			}
			return
		}
	}
	t.Fatal("resource policy byte quota never triggered")
}

func TestRESTClosedStateFailsWithoutSuccess(t *testing.T) {
	ctx := context.Background()
	db, _, _ := lambdaState(t)
	apis, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := NewLambdaPermissionStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	checks := map[string]func() error{
		"createAPI":      func() error { _, e := apis.CreateAPI(ctx, "us-east-1", "closed", "", nil); return e },
		"readAPI":        func() error { _, e := apis.API(ctx, "us-east-1", "0123456789"); return e },
		"listAPIs":       func() error { _, e := apis.APIs(ctx, "us-east-1", "", 25); return e },
		"deleteAPI":      func() error { return apis.DeleteAPI(ctx, "us-east-1", "0123456789") },
		"readResource":   func() error { _, e := apis.Resource(ctx, "us-east-1", "0123456789", "root"); return e },
		"listResources":  func() error { _, e := apis.Resources(ctx, "us-east-1", "0123456789", "", 25); return e },
		"createResource": func() error { _, e := apis.CreateResource(ctx, "us-east-1", "0123456789", "root", "child"); return e },
		"deleteResource": func() error { return apis.DeleteResource(ctx, "us-east-1", "0123456789", "child") },
		"putMethod":      func() error { _, e := apis.PutMethod(ctx, "us-east-1", "0123456789", "root", "GET", nil); return e },
		"putIntegration": func() error {
			_, e := apis.PutIntegration(ctx, "us-east-1", "0123456789", "root", "GET", "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:handler/invocations")
			return e
		},
		"createDeployment": func() error { _, e := apis.CreateDeployment(ctx, "us-east-1", "0123456789", ""); return e },
		"readDeployment":   func() error { _, _, e := apis.Deployment(ctx, "us-east-1", "0123456789", "deployment"); return e },
		"createStage": func() error {
			_, e := apis.CreateStage(ctx, "us-east-1", "0123456789", "prod", "deployment", nil)
			return e
		},
		"readStage":   func() error { _, e := apis.Stage(ctx, "us-east-1", "0123456789", "prod"); return e },
		"deleteStage": func() error { return apis.DeleteStage(ctx, "us-east-1", "0123456789", "prod") },
		"readPolicy":  func() error { _, _, e := permissions.Policy(ctx, "us-east-1", "handler"); return e },
		"addPermission": func() error {
			_, e := permissions.Add(ctx, "us-east-1", "handler", "allow", "apigateway.amazonaws.com", "lambda:InvokeFunction", "arn:aws:execute-api:us-east-1:000000000000:0123456789/prod/*/*", "")
			return e
		},
		"removePermission": func() error { return permissions.Remove(ctx, "us-east-1", "handler", "allow", "") },
		"servicePermission": func() error {
			allowed, e := permissions.AllowsAPI(ctx, "us-east-1", "handler", "arn:aws:execute-api:us-east-1:000000000000:0123456789/prod/GET/path")
			if allowed {
				t.Fatal("failed database granted invocation")
			}
			return e
		},
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(); err == nil {
				t.Fatal("unavailable database returned success")
			}
		})
	}
	if _, err = NewAPIGatewayStore(db.Database()); err == nil {
		t.Fatal("closed database initialized API tables")
	}
	if _, err = NewLambdaPermissionStore(db.Database()); err == nil {
		t.Fatal("closed database initialized policy tables")
	}
}
