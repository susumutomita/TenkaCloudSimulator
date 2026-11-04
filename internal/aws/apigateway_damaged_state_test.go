package aws

import (
	"context"
	"strings"
	"testing"
)

func TestRESTDamagedPersistedDocumentsFailRatherThanInventState(t *testing.T) {
	ctx := context.Background()
	db, functions, _ := lambdaState(t)
	apis, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	api, err := apis.CreateAPI(ctx, "us-east-1", "damaged-state", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = apis.PutMethod(ctx, "us-east-1", api.ID, api.RootResourceID, "GET", nil); err != nil {
		t.Fatal(err)
	}
	uri := "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:handler/invocations"
	if _, err = apis.PutIntegration(ctx, "us-east-1", api.ID, api.RootResourceID, "GET", uri); err != nil {
		t.Fatal(err)
	}
	deployment, err := apis.CreateDeployment(ctx, "us-east-1", api.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = apis.CreateStage(ctx, "us-east-1", api.ID, "prod", deployment.ID, nil); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		table, key, where string
		actions           []func() error
	}{
		{"aws_apigateway_apis", "document", "id=?", []func() error{func() error { _, e := apis.API(ctx, "us-east-1", api.ID); return e }, func() error { _, e := apis.APIs(ctx, "us-east-1", "", 25); return e }}},
		{"aws_apigateway_resources", "document", "api_id=?", []func() error{func() error { _, e := apis.Resource(ctx, "us-east-1", api.ID, api.RootResourceID); return e }, func() error { _, e := apis.Resources(ctx, "us-east-1", api.ID, "", 25); return e }, func() error { _, e := apis.CreateDeployment(ctx, "us-east-1", api.ID, ""); return e }, func() error {
			_, e := apis.CreateResource(ctx, "us-east-1", api.ID, api.RootResourceID, "child")
			return e
		}}},
		{"aws_apigateway_deployments", "document", "api_id=?", []func() error{func() error { _, _, e := apis.Deployment(ctx, "us-east-1", api.ID, deployment.ID); return e }}},
		{"aws_apigateway_deployments", "snapshot", "api_id=?", []func() error{func() error { _, _, e := apis.Deployment(ctx, "us-east-1", api.ID, deployment.ID); return e }, func() error { _, e := apis.ResolveStage(ctx, "us-east-1", api.ID, "prod", "/", "GET"); return e }}},
		{"aws_apigateway_stages", "document", "api_id=?", []func() error{func() error { _, e := apis.Stage(ctx, "us-east-1", api.ID, "prod"); return e }, func() error { _, e := apis.ResolveStage(ctx, "us-east-1", api.ID, "prod", "/", "GET"); return e }}},
	}
	for _, test := range checks {
		var original string
		if err = db.Database().QueryRow("SELECT "+test.key+" FROM "+test.table+" WHERE "+test.where, api.ID).Scan(&original); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Database().Exec("UPDATE "+test.table+" SET "+test.key+"=? WHERE "+test.where, "invalid private-marker-json", api.ID); err != nil {
			t.Fatal(err)
		}
		for _, check := range test.actions {
			if err = check(); err == nil {
				t.Fatal("damaged document reported valid state", test.table, test.key)
			}
		}
		if _, err = db.Database().Exec("UPDATE "+test.table+" SET "+test.key+"=? WHERE "+test.where, original, api.ID); err != nil {
			t.Fatal(err)
		}
	}
	var original string
	if err = db.Database().QueryRow("SELECT document FROM aws_apigateway_apis WHERE id=?", api.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec("UPDATE aws_apigateway_apis SET document=? WHERE id=?", strings.Replace(original, api.ID, "wrong-id", 1), api.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = apis.API(ctx, "us-east-1", api.ID); err == nil {
		t.Fatal("mismatched persisted API identity accepted")
	}
	permissions, err := NewLambdaPermissionStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	if err = functions.save(&LambdaFunction{Name: "damaged-policy", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Code: []byte("dummy")}, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = permissions.Add(ctx, "us-east-1", "damaged-policy", "allow", "apigateway.amazonaws.com", "lambda:InvokeFunction", "arn:aws:execute-api:us-east-1:000000000000:"+api.ID+"/prod/*/*", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec("UPDATE aws_lambda_resource_policies SET document='invalid-private-policy'"); err != nil {
		t.Fatal(err)
	}
	if allowed, e := permissions.AllowsAPI(ctx, "us-east-1", "damaged-policy", "arn:aws:execute-api:us-east-1:000000000000:"+api.ID+"/prod/GET/"); e == nil || allowed {
		t.Fatal("damaged service policy allowed invocation", allowed, e)
	}
}
