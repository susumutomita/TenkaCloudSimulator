package aws

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/apirouting"
)

func TestPrivateRESTInvokesActualSavedRolePythonAfterServicePermission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db, functions, identities := lambdaState(t)
	var code bytes.Buffer
	archive := zip.NewWriter(&code)
	file, err := archive.Create("app.py")
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.Write([]byte("import json\ndef handler(event,context):\n if event.get('body')=='bad': return {'unexpected':1}\n if event.get('body')=='crash': raise RuntimeError('private-guest-message')\n return {'statusCode': 201, 'body': json.dumps({'path':event['path'],'method':event['httpMethod'],'proxy':event['pathParameters']['proxy'],'role':context.function_name}), 'headers': {'X-App-Result':'executed'}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	function := LambdaFunction{Name: "rest-executor", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Handler: "app.handler", Timeout: 10, Code: code.Bytes()}
	if err = functions.save(&function, true, ""); err != nil {
		t.Fatal(err)
	}
	permissions, err := NewLambdaPermissionStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := api.CreateAPI(ctx, function.Region, "actual-proxy", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := api.CreateResource(ctx, function.Region, metadata.ID, metadata.RootResourceID, "{proxy+}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.PutMethod(ctx, function.Region, metadata.ID, proxy.ID, "ANY", nil); err != nil {
		t.Fatal(err)
	}
	uri := "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/" + lambdaARN(function.Region, function.Name) + "/invocations"
	if _, err = api.PutIntegration(ctx, function.Region, metadata.ID, proxy.ID, "ANY", uri); err != nil {
		t.Fatal(err)
	}
	deployment, err := api.CreateDeployment(ctx, function.Region, metadata.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.CreateStage(ctx, function.Region, metadata.ID, "prod", deployment.ID, nil); err != nil {
		t.Fatal(err)
	}
	route, err := api.ResolveStage(ctx, function.Region, metadata.ID, "prod", "/wp-json/batch/v1", "POST")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/transport", nil)
	event, err := apirouting.BuildProxyEvent(request, apirouting.ProxyRequest{APIID: metadata.ID, Stage: "prod", ResourcePath: proxy.Path, Path: "/wp-json/batch/v1", RequestID: "dummy-local", PathParameters: route.PathParameters}, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Store: db, Identities: identities, Lambdas: functions}
	if _, err = gateway.invokeREST(ctx, function.Region, route, "/wp-json/batch/v1", "POST", event); iamCode(err) != "BadGatewayException" {
		t.Fatal("missing service permission executed", err)
	}
	source := "arn:aws:execute-api:us-east-1:000000000000:" + metadata.ID + "/prod/POST/*"
	if _, err = permissions.Add(ctx, function.Region, function.Name, "allow-proxy", "apigateway.amazonaws.com", "lambda:InvokeFunction", source, ""); err != nil {
		t.Fatal(err)
	}
	// Runtime compilation is separately bounded and tested; warm it before this
	// focused test of the API integration's 29-second execution deadline.
	if _, err = functions.runner(ctx); err != nil {
		t.Fatal(err)
	}
	response, err := gateway.invokeREST(ctx, function.Region, route, "/wp-json/batch/v1", "POST", event)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	if err = json.Unmarshal([]byte(response.Body), &body); err != nil || response.StatusCode != 201 || body["path"] != "/wp-json/batch/v1" || body["method"] != "POST" || body["proxy"] != "wp-json/batch/v1" || body["role"] != function.Name {
		t.Fatal(response, body, err)
	}
	gateway.APIGateway = api
	gateway.LambdaPermissions = permissions
	for _, test := range []struct {
		body, query string
		status      int
	}{{"bad", "", 502}, {"crash", "", 502}, {"{}", "", 201}, {"{}", "?bad=%zz", 400}, {string([]byte{0xff}), "", 400}, {strings.Repeat("x", apirouting.MaxProxyEventBytes+1), "", 400}} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/_execute-api/us-east-1/"+metadata.ID+"/prod/wp-json/batch/v1"+test.query, strings.NewReader(test.body))
		request.Header.Set("Authorization", "Bearer application-only")
		gateway.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Fatal("native execute-api transport", test.status, recorder.Code, recorder.Body.String())
		}
		if test.status == 201 && (recorder.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(recorder.Header().Get("Content-Disposition"), "attachment;")) {
			t.Fatal("native transport allowed active response", recorder.Header())
		}
	}
	if _, err = gateway.invokeREST(ctx, function.Region, route, "/wp-json/batch/v1", "GET", event); iamCode(err) != "BadGatewayException" {
		t.Fatal("source method restriction ignored", err)
	}
	if err = permissions.Remove(ctx, function.Region, function.Name, "allow-proxy", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = gateway.invokeREST(ctx, function.Region, route, "/wp-json/batch/v1", "POST", event); iamCode(err) != "BadGatewayException" {
		t.Fatal("revoked service grant still executed", err)
	}
}
