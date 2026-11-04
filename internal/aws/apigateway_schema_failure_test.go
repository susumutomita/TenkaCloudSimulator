package aws

import (
	"context"
	"testing"
)

// A damaged/mismatched SQLite schema must surface an error, rather than an
// empty successful inventory or a partly committed deployment/stage mutation.
func TestRESTMissingStateTablesFailClosed(t *testing.T) {
	for _, table := range []string{"aws_apigateway_apis", "aws_apigateway_resources", "aws_apigateway_deployments", "aws_apigateway_stages", "aws_lambda_resource_policies"} {
		t.Run(table, func(t *testing.T) {
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
			api, err := apis.CreateAPI(ctx, "us-east-1", "schema-state", "", nil)
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
			if err = functions.save(&LambdaFunction{Name: "schema-handler", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Code: []byte("dummy")}, true, ""); err != nil {
				t.Fatal(err)
			}
			var deployment RESTDeployment
			if table == "aws_apigateway_stages" {
				deployment, err = apis.CreateDeployment(ctx, "us-east-1", api.ID, "")
				if err != nil {
					t.Fatal(err)
				}
			}
			// Removing a parent with children may cascade rows under SQLite's DDL
			// semantics. These isolated disposable fixtures are never user databases.
			if _, err = db.Database().Exec("DROP TABLE " + table); err != nil {
				t.Fatal(err)
			}
			var checks []func() error
			switch table {
			case "aws_apigateway_apis":
				checks = []func() error{func() error { _, e := apis.CreateAPI(ctx, "us-east-1", "failed-create", "", nil); return e }, func() error { _, e := apis.APIs(ctx, "us-east-1", "", 25); return e }, func() error { _, e := apis.API(ctx, "us-east-1", api.ID); return e }, func() error { return apis.DeleteAPI(ctx, "us-east-1", api.ID) }, func() error {
					_, e := apis.CreateResource(ctx, "us-east-1", api.ID, api.RootResourceID, "child")
					return e
				}}
			case "aws_apigateway_resources":
				checks = []func() error{func() error { _, e := apis.Resources(ctx, "us-east-1", api.ID, "", 25); return e }, func() error { _, e := apis.CreateDeployment(ctx, "us-east-1", api.ID, ""); return e }, func() error {
					_, e := apis.CreateResource(ctx, "us-east-1", api.ID, api.RootResourceID, "child")
					return e
				}, func() error {
					_, e := apis.PutMethod(ctx, "us-east-1", api.ID, api.RootResourceID, "POST", nil)
					return e
				}, func() error {
					_, e := apis.PutIntegration(ctx, "us-east-1", api.ID, api.RootResourceID, "GET", uri)
					return e
				}, func() error { return apis.DeleteResource(ctx, "us-east-1", api.ID, api.RootResourceID) }}
			case "aws_apigateway_deployments":
				checks = []func() error{func() error { _, e := apis.CreateDeployment(ctx, "us-east-1", api.ID, ""); return e }, func() error { _, _, e := apis.Deployment(ctx, "us-east-1", api.ID, "missing"); return e }, func() error { _, e := apis.CreateStage(ctx, "us-east-1", api.ID, "prod", "missing", nil); return e }}
			case "aws_apigateway_stages":
				checks = []func() error{func() error { _, e := apis.CreateStage(ctx, "us-east-1", api.ID, "prod", deployment.ID, nil); return e }, func() error { _, e := apis.Stage(ctx, "us-east-1", api.ID, "prod"); return e }, func() error { return apis.DeleteStage(ctx, "us-east-1", api.ID, "prod") }, func() error { return apis.DeleteAPI(ctx, "us-east-1", api.ID) }}
			case "aws_lambda_resource_policies":
				checks = []func() error{func() error { _, _, e := permissions.Policy(ctx, "us-east-1", "schema-handler"); return e }, func() error {
					_, e := permissions.Add(ctx, "us-east-1", "schema-handler", "allow", "apigateway.amazonaws.com", "lambda:InvokeFunction", "arn:aws:execute-api:us-east-1:000000000000:"+api.ID+"/prod/*/*", "")
					return e
				}, func() error { return permissions.Remove(ctx, "us-east-1", "schema-handler", "allow", "") }}
			}
			for _, check := range checks {
				if err = check(); err == nil {
					t.Fatal("missing state table reported success", table)
				}
			}
		})
	}
}
