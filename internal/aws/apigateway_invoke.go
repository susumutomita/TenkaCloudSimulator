package aws

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/apirouting"
	pythonruntime "github.com/susumutomita/TenkaCloudSimulator/internal/workload/python"
)

// invokeREST is private and not an HTTP entrypoint. The host must independently
// authenticate its local client before selecting trusted regional routing state.
// Function code, service permission and execution-role trust are captured in one
// SQLite read transaction so deletion/recreation cannot borrow a previous grant.
func (g *Gateway) invokeREST(ctx context.Context, region string, route RESTRoute, path, method string, event []byte) (apirouting.ProxyResponse, error) {
	fail := func() (apirouting.ProxyResponse, error) {
		return apirouting.ProxyResponse{}, apiFailure("BadGatewayException", "The local Lambda proxy integration failed.")
	}
	if g.Lambdas == nil || route.Method.MethodIntegration == nil || !json.Valid(event) || len(event) > apirouting.MaxProxyEventBytes {
		return fail()
	}
	integration := route.Method.MethodIntegration
	prefix := "arn:aws:apigateway:" + region + ":lambda:path/2015-03-31/functions/" + "arn:aws:lambda:" + region + ":000000000000:function:"
	if integration.Type != "AWS_PROXY" || integration.HTTPMethod != "POST" || !strings.HasPrefix(integration.URI, prefix) || !strings.HasSuffix(integration.URI, "/invocations") {
		return fail()
	}
	name := strings.TrimSuffix(strings.TrimPrefix(integration.URI, prefix), "/invocations")
	if !lambdaName.MatchString(name) {
		return fail()
	}
	source := "arn:aws:execute-api:" + region + ":000000000000:" + route.API.ID + "/" + route.Stage.StageName + "/" + method + "/" + strings.TrimPrefix(path, "/")
	tx, err := g.Lambdas.db.BeginTx(ctx, nil)
	if err != nil {
		return fail()
	}
	defer tx.Rollback()
	var data string
	if err = tx.QueryRowContext(ctx, "SELECT document FROM aws_lambda_functions WHERE region=? AND name=?", region, name).Scan(&data); err != nil {
		return fail()
	}
	var function LambdaFunction
	if err = json.Unmarshal([]byte(data), &function); err != nil || function.Name != name || function.Region != region {
		return fail()
	}
	policy, _, err := lambdaPolicyRead(ctx, tx, region, name)
	if err != nil || !lambdaPolicyAllowsAPI(policy, region, name, source) {
		return fail()
	}
	if err = iamExecutionRole(ctx, tx, function.Role); err != nil {
		return fail()
	}
	if err = tx.Commit(); err != nil {
		return fail()
	}
	deadline, cancel := context.WithTimeout(ctx, 29*time.Second)
	defer cancel()
	runner, err := g.Lambdas.runner(deadline)
	if err != nil {
		return fail()
	}
	result, err := runner.Run(deadline, pythonruntime.Invocation{Exchange: g.lambdaExchange(function, region), Modules: lambdaSDKFiles(), Code: function.Code, Handler: function.Handler, Event: event, Environment: function.Environment, Context: map[string]string{"function_name": function.Name, "function_version": "$LATEST", "invoked_function_arn": lambdaARN(region, name)}, Timeout: time.Duration(function.Timeout) * time.Second})
	if err != nil || result.FunctionError {
		return fail()
	}
	response, err := apirouting.ParseProxyResponse(result.Payload)
	if err != nil {
		return fail()
	}
	return response, nil
}
