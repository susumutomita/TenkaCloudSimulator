package aws

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestRESTGatewayDispatchScopeIAMAndTransportValidation(t *testing.T) {
	db, _, identities := lambdaState(t)
	apis, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Store: db, Identities: identities, APIGateway: apis}
	policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"apigateway:GET"}, Resource: core.StringList{"arn:aws:apigateway:us-east-1::/restapis"}}}}
	if err = identities.Set("api-reader", policy); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, path, service, auth, region, principal, session, body string
		status                                                        int
	}{
		{"GET", "/restapis", "apigateway", "", "", "", "", "", 200},
		{"GET", "/restapis", "", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/date/us-east-1/apigateway/aws4_request", "", "", "", "", 200},
		{"GET", "/restapis", "apigateway", "", "", "api-reader", "", "", 200},
		{"POST", "/restapis", "apigateway", "", "", "api-reader", "", `{"name":"denied","endpointConfiguration":{"types":["REGIONAL"]}}`, 403},
		{"GET", "/restapis", "apigateway", "AWS4-HMAC-SHA256 Credential=REALKEY/date/us-east-1/apigateway/aws4_request", "", "", "", "", 403},
		{"GET", "/restapis", "apigateway", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/date/us-west-2/apigateway/aws4_request", "us-east-1", "", "", "", 400},
		{"GET", "/restapis", "apigateway", "", "unknown", "", "", "", 400},
		{"GET", "/restapis", "apigateway", "", "", "", "session", "", 400},
		{"POST", "/restapis", "apigateway", "", "", "", "", strings.Repeat("x", 128<<10+1), 400},
		{"GET", "/_execute-api/us-east-1/not-an-id/prod/", "execute-api", "", "", "", "", "", 400},
		{"GET", "/_execute-api/us-east-1/0123456789/prod/?X-Amz-Credential=dummy", "execute-api", "", "", "", "", "", 400},
		{"GET", "/_execute-api/us-east-1/0123456789/prod/", "execute-api", "", "us-west-2", "", "", "", 400},
		{"GET", "/_execute-api/us-east-1/0123456789/prod/", "execute-api", "Bearer application-only", "", "", "", "", 404},
		{"GET", "/_execute-api/us-east-1/0123456789/prod/", "apigateway", "", "", "", "", "", 400},
	} {
		r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		r.Header.Set("X-Simulator-Service", test.service)
		r.Header.Set("Authorization", test.auth)
		r.Header.Set("X-Simulator-Region", test.region)
		r.Header.Set("X-Simulator-Principal", test.principal)
		r.Header.Set("X-Amz-Security-Token", test.session)
		recorder := httptest.NewRecorder()
		gateway.ServeHTTP(recorder, r)
		if recorder.Code != test.status {
			t.Fatal(test, recorder.Code, recorder.Body.String())
		}
	}
	missing := &Gateway{}
	r := httptest.NewRequest("GET", "/restapis", nil)
	r.Header.Set("X-Simulator-Service", "apigateway")
	recorder := httptest.NewRecorder()
	missing.ServeHTTP(recorder, r)
	if recorder.Code != 501 {
		t.Fatal(recorder.Code)
	}
}
func TestLambdaResourcePolicyNativeHTTPActionsAndRevisions(t *testing.T) {
	db, functions, identities := lambdaState(t)
	permissions, err := NewLambdaPermissionStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	function := LambdaFunction{Name: "native-policy", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Code: []byte("dummy")}
	if err = functions.save(&function, true, ""); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Store: db, Identities: identities, Lambdas: functions, LambdaPermissions: permissions}
	request := func(method, path, body string, status int) map[string]string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Simulator-Service", "lambda")
		recorder := httptest.NewRecorder()
		gateway.ServeHTTP(recorder, r)
		if recorder.Code != status {
			t.Fatal(method, path, recorder.Code, recorder.Body.String())
		}
		result := map[string]string{}
		if recorder.Body.Len() > 0 {
			if e := json.Unmarshal(recorder.Body.Bytes(), &result); e != nil {
				t.Fatal(e)
			}
		}
		return result
	}
	path := "/2015-03-31/functions/native-policy/policy"
	request("GET", path, "", 404)
	body := `{"StatementId":"native-grant","Principal":"apigateway.amazonaws.com","Action":"lambda:InvokeFunction","SourceArn":"arn:aws:execute-api:us-east-1:000000000000:0123456789/prod/*/*"}`
	request("POST", path, body, 201)
	policy := request("GET", path, "", 200)
	if policy["Policy"] == "" || policy["RevisionId"] == "" {
		t.Fatal(policy)
	}
	request("POST", path, body, 409)
	request("POST", path, `{"Unknown":"ignored"}`, 400)
	request("POST", path, strings.Repeat("x", 32<<10+1), 400)
	request("DELETE", path+"/native-grant?RevisionId=stale", "", 412)
	request("DELETE", path+"/native-grant?RevisionId="+policy["RevisionId"], "", 204)
	request("GET", path, "", 404)
	request("DELETE", path+"/native-grant", "", 404)
	request("PUT", path, `{}`, 501)
	request("GET", path+"?RevisionId=unexpected", "", 400)
	request("GET", path+"?Qualifier=$LATEST", "", 501)
}

func TestRESTAndLambdaPolicyRequireConfiguredParticipantAuthority(t *testing.T) {
	db, functions, identities := lambdaState(t)
	apis, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := NewLambdaPermissionStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	function := LambdaFunction{Name: "permission-only", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Code: []byte("dummy")}
	if err = functions.save(&function, true, ""); err != nil {
		t.Fatal(err)
	}
	if err = identities.Set("policy-reader", core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"lambda:GetPolicy"}, Resource: core.StringList{lambdaARN("us-east-1", function.Name)}}}}); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Store: db, Identities: identities, Lambdas: functions, LambdaPermissions: permissions, APIGateway: apis}
	send := func(target *Gateway, method, path, service, principal, body string, status int) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Simulator-Service", service)
		r.Header.Set("X-Simulator-Principal", principal)
		recorder := httptest.NewRecorder()
		target.ServeHTTP(recorder, r)
		if recorder.Code != status {
			t.Fatal(method, path, principal, recorder.Code, recorder.Body.String())
		}
	}
	path := "/2015-03-31/functions/" + function.Name + "/policy"
	send(gateway, "POST", path, "lambda", "policy-reader", `{}`, 403)
	send(gateway, "DELETE", path+"/statement", "lambda", "policy-reader", "", 403)
	send(gateway, "GET", path, "lambda", "executor", "", 403)
	send(gateway, "GET", "/2015-03-31/functions/missing/policy", "lambda", "", "", 404)
	gateway.LambdaPermissions = nil
	send(gateway, "GET", path, "lambda", "", "", 501)
	gateway.LambdaPermissions = permissions
	gateway.Identities = nil
	send(gateway, "GET", "/restapis", "apigateway", "participant", "", 403)
	gateway.Identities = identities
	send(gateway, "TRACE", "/_execute-api/us-east-1/0123456789/prod/", "execute-api", "", "", 400)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	send(gateway, "GET", "/restapis", "apigateway", "policy-reader", "", 500)
	send(gateway, "GET", path, "lambda", "", "", 500)
}
