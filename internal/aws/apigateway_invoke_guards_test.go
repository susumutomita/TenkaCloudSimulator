package aws

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRESTInvocationRejectsDamagedTargetsRolesAndUnavailableRuntime(t *testing.T) {
	ctx := context.Background()
	db, functions, identities := lambdaState(t)
	permissions, err := NewLambdaPermissionStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	function := LambdaFunction{Name: "guard-target", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Code: []byte("dummy")}
	if err = functions.save(&function, true, ""); err != nil {
		t.Fatal(err)
	}
	uri := "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/" + lambdaARN("us-east-1", function.Name) + "/invocations"
	route := RESTRoute{API: RESTAPI{ID: "0123456789"}, Stage: RESTStage{StageName: "prod"}, Method: RESTMethod{MethodIntegration: &RESTIntegration{Type: "AWS_PROXY", HTTPMethod: "POST", URI: uri}}}
	gateway := &Gateway{Store: db, Identities: identities, Lambdas: functions, LambdaPermissions: permissions}
	denied := func(target *Gateway, value RESTRoute, event []byte) {
		t.Helper()
		if _, e := target.invokeREST(ctx, "us-east-1", value, "/", "POST", event); iamCode(e) != "BadGatewayException" {
			t.Fatal("invalid invocation did not fail closed", e)
		}
	}
	denied(&Gateway{}, route, []byte(`{}`))
	denied(gateway, RESTRoute{}, []byte(`{}`))
	denied(gateway, route, []byte(`invalid private-input`))
	invalid := route
	invalid.Method.MethodIntegration = &RESTIntegration{Type: "HTTP_PROXY", HTTPMethod: "POST", URI: "https://example.invalid"}
	denied(gateway, invalid, []byte(`{}`))
	invalid.Method.MethodIntegration = &RESTIntegration{Type: "AWS_PROXY", HTTPMethod: "POST", URI: uri[:len(uri)-len("/invocations")] + ":alias/invocations"}
	denied(gateway, invalid, []byte(`{}`))
	invalid.Method.MethodIntegration = &RESTIntegration{Type: "AWS_PROXY", HTTPMethod: "POST", URI: "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:absent/invocations"}
	denied(gateway, invalid, []byte(`{}`))
	if _, err = permissions.Add(ctx, "us-east-1", function.Name, "allow", "apigateway.amazonaws.com", "lambda:InvokeFunction", "arn:aws:execute-api:us-east-1:000000000000:0123456789/prod/POST/*", ""); err != nil {
		t.Fatal(err)
	}
	var original string
	if err = db.Database().QueryRow("SELECT document FROM aws_lambda_functions WHERE name=?", function.Name).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec("UPDATE aws_lambda_functions SET document='damaged-private-json' WHERE name=?", function.Name); err != nil {
		t.Fatal(err)
	}
	denied(gateway, route, []byte(`{}`))
	if _, err = db.Database().Exec("UPDATE aws_lambda_functions SET document=? WHERE name=?", original, function.Name); err != nil {
		t.Fatal(err)
	}
	changed := function
	changed.Name = "wrong-stored-name"
	encoded, _ := json.Marshal(changed)
	if _, err = db.Database().Exec("UPDATE aws_lambda_functions SET document=? WHERE name=?", string(encoded), function.Name); err != nil {
		t.Fatal(err)
	}
	denied(gateway, route, []byte(`{}`))
	if _, err = db.Database().Exec("UPDATE aws_lambda_functions SET document=? WHERE name=?", original, function.Name); err != nil {
		t.Fatal(err)
	}
	if err = identities.Delete("executor"); err != nil {
		t.Fatal(err)
	}
	denied(gateway, route, []byte(`{}`))
	if _, err = db.Database().Exec("INSERT INTO identity_policies(name,document) VALUES('executor',?)", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:GetParameter","Resource":"*"}]}`); err != nil {
		t.Fatal(err)
	}
	if err = functions.Close(); err != nil {
		t.Fatal(err)
	}
	denied(gateway, route, []byte(`{}`))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = gateway.invokeREST(canceled, "us-east-1", route, "/", "POST", []byte(`{}`)); iamCode(err) != "BadGatewayException" {
		t.Fatal(err)
	}
}
