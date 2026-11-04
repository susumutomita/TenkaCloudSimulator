package aws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrivateRESTControlProtocolAndAuthorization(t *testing.T) {
	db, _ := s3TestState(t)
	store, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	allowed := true
	calls := []string{}
	request := func(method, path, body string, status int) map[string]any {
		t.Helper()
		recorder := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		store.serveControl(recorder, r, "us-east-1", func(action, resource string) bool { calls = append(calls, action+" "+resource); return allowed })
		if recorder.Code != status {
			t.Fatalf("%s %s: status%d want%d: %s", method, path, recorder.Code, status, recorder.Body.String())
		}
		result := map[string]any{}
		if recorder.Body.Len() > 0 {
			if e := json.Unmarshal(recorder.Body.Bytes(), &result); e != nil {
				t.Fatal(e)
			}
		}
		return result
	}
	api := request("POST", "/restapis", `{"name":"protocol","endpointConfiguration":{"types":["REGIONAL"]},"tags":{"Team":"dummy"}}`, 201)
	id := api["id"].(string)
	root := api["rootResourceId"].(string)
	prefix := "/restapis/" + id
	if request("GET", prefix, "", 200)["name"] != "protocol" {
		t.Fatal("API readback differs")
	}
	resources := request("GET", prefix+"/resources", "", 200)["item"].([]any)
	if len(resources) != 1 || resources[0].(map[string]any)["path"] != "/" {
		t.Fatal(resources)
	}
	proxy := request("POST", prefix+"/resources/"+root, `{"pathPart":"{proxy+}"}`, 201)["id"].(string)
	methodPath := prefix + "/resources/" + proxy + "/methods/ANY"
	request("PUT", methodPath, `{"authorizationType":"NONE","requestParameters":{"method.request.path.proxy":true}}`, 201)
	request("PUT", methodPath+"/integration", `{"type":"HTTP_PROXY","httpMethod":"GET","uri":"https://example.invalid"}`, 400)
	request("PUT", methodPath+"/integration", `{"type":"AWS_PROXY","httpMethod":"POST","uri":"arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:handler/invocations"}`, 201)
	if request("GET", methodPath, "", 200)["methodIntegration"].(map[string]any)["type"] != "AWS_PROXY" {
		t.Fatal("integration omitted")
	}
	deployment := request("POST", prefix+"/deployments", `{"description":"frozen"}`, 201)["id"].(string)
	request("POST", prefix+"/stages", `{"stageName":"prod","deploymentId":"`+deployment+`","variables":{"mode":"dummy"}}`, 201)
	request("GET", prefix+"/deployments/"+deployment, "", 200)
	if request("GET", prefix+"/stages/prod", "", 200)["deploymentId"] != deployment {
		t.Fatal("stage readback changed")
	}
	allowed = false
	request("DELETE", prefix, "", 403)
	allowed = true
	request("GET", prefix, "", 200)
	request("DELETE", prefix+"/stages/prod", "", 202)
	request("GET", prefix+"/stages/prod", "", 404)
	request("DELETE", prefix, "", 202)
	request("GET", prefix, "", 404)
	if len(calls) < 1 || calls[0] != "apigateway:POST arn:aws:apigateway:us-east-1::/restapis" {
		t.Fatal(calls)
	}
	for _, test := range []struct{ method, path, body string }{{"POST", "/restapis", `{"Name":"case","endpointConfiguration":{"types":["REGIONAL"]}}`}, {"POST", "/restapis", `{"name":"bad","endpointConfiguration":{"types":["EDGE"]}}`}, {"POST", "/restapis", `{"name":"bad","endpointConfiguration":{"types":["REGIONAL"]},"policy":"ignored"}`}, {"GET", "/restapis?limit=0", ""}, {"GET", "/restapis?limit=1&limit=2", ""}, {"GET", "/restapis?unknown=1", ""}, {"GET", "/restapis?position=%zz", ""}} {
		request(test.method, test.path, test.body, 400)
	}
	request("PATCH", "/restapis/unknown", `{}`, 501)
	recorder := httptest.NewRecorder()
	store.serveControl(recorder, httptest.NewRequest(http.MethodPost, "/restapis", strings.NewReader(`{}`)), "us-east-1", nil)
	if recorder.Code != 403 {
		t.Fatal("nil authorizer allowed management", recorder.Code)
	}
}

func TestRESTControlPaginationAndUnsupportedOptions(t *testing.T) {
	db, _ := s3TestState(t)
	store, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, status int) map[string]any {
		t.Helper()
		recorder := httptest.NewRecorder()
		store.serveControl(recorder, httptest.NewRequest(method, path, strings.NewReader(body)), "us-east-1", func(string, string) bool { return true })
		if recorder.Code != status {
			t.Fatal(method, path, recorder.Code, recorder.Body.String())
		}
		value := map[string]any{}
		if recorder.Body.Len() > 0 {
			if e := json.Unmarshal(recorder.Body.Bytes(), &value); e != nil {
				t.Fatal(e)
			}
		}
		return value
	}
	for i := 0; i < 3; i++ {
		call("POST", "/restapis", `{"name":"paged","endpointConfiguration":{"types":["REGIONAL"]}}`, 201)
	}
	seen := map[string]bool{}
	position := ""
	for {
		result := call("GET", "/restapis?limit=1&position="+position, "", 200)
		items := result["item"].([]any)
		if len(items) != 1 {
			t.Fatal(result)
		}
		id := items[0].(map[string]any)["id"].(string)
		if seen[id] {
			t.Fatal("pagination duplicated an API")
		}
		seen[id] = true
		next, ok := result["position"].(string)
		if !ok {
			break
		}
		position = next
	}
	if len(seen) != 3 {
		t.Fatal("pagination omitted an API", seen)
	}
	var id string
	for current := range seen {
		id = current
		break
	}
	api := call("GET", "/restapis/"+id, "", 200)
	root := api["rootResourceId"].(string)
	prefix := "/restapis/" + id + "/resources/" + root
	child := call("POST", prefix, `{"pathPart":"child"}`, 201)["id"].(string)
	call("POST", prefix, `{"pathPart":"other"}`, 201)
	if call("GET", "/restapis/"+id+"/resources/"+child, "", 200)["path"] != "/child" {
		t.Fatal("resource readback changed")
	}
	position = ""
	resources := map[string]bool{}
	for {
		result := call("GET", "/restapis/"+id+"/resources?limit=1&position="+position, "", 200)
		items := result["item"].([]any)
		if len(items) != 1 {
			t.Fatal(result)
		}
		key := items[0].(map[string]any)["id"].(string)
		if resources[key] {
			t.Fatal("pagination duplicated a resource")
		}
		resources[key] = true
		next, ok := result["position"].(string)
		if !ok {
			break
		}
		position = next
	}
	if len(resources) != 3 {
		t.Fatal(resources)
	}
	call("GET", "/restapis/"+id+"?limit=1", "", 400)
	call("GET", "/restapis?limit=501", "", 400)
	call("GET", "/outside", "", 501)
	call("PUT", prefix+"/methods/GET", `{"authorizationType":"AWS_IAM"}`, 400)
	call("PUT", prefix+"/methods/GET", `{"authorizationType":"NONE","apiKeyRequired":true}`, 400)
	call("PUT", prefix+"/methods/TRACE", `{"authorizationType":"NONE"}`, 400)
	call("GET", prefix+"/methods/GET", "", 404)
	call("PUT", prefix+"/methods/GET/integration", `{"type":"AWS_PROXY","httpMethod":"POST","uri":"arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:missing/invocations"}`, 404)
	call("DELETE", "/restapis/"+id+"/resources/"+child, "", 202)
	call("POST", "/restapis", string([]byte{'{', '"', 'n', 'a', 'm', 'e', '"', ':', '"', 0xff, '"', '}'}), 400)
}
