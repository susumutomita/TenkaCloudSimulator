package aws

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/apirouting"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestWAFNativeDispatchAuthorityAndRegion(t *testing.T) {
	store, _ := wafFixture(t)
	gateway := &Gateway{WAF: store}
	for _, c := range []struct {
		service, auth, region, session, path, target string
		status                                       int
	}{
		{"wafv2", "", "", "", "/", "AWSWAF_20190729.ListWebACLs", 200},
		{"", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/date/ap-northeast-1/wafv2/aws4_request", "", "", "/", "AWSWAF_20190729.ListWebACLs", 200},
		{"s3", "", "", "", "/", "AWSWAF_20190729.ListWebACLs", 400},
		{"wafv2", "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/date/us-east-1/wafv2/aws4_request", "", "", "/", "AWSWAF_20190729.ListWebACLs", 403},
		{"wafv2", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/date/us-east-1/ssm/aws4_request", "", "", "/", "AWSWAF_20190729.ListWebACLs", 403},
		{"wafv2", "Bearer application", "", "", "/", "AWSWAF_20190729.ListWebACLs", 403},
		{"wafv2", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/date/us-west-2/wafv2/aws4_request", "us-east-1", "", "/", "AWSWAF_20190729.ListWebACLs", 400},
		{"wafv2", "", "invalid", "", "/", "AWSWAF_20190729.ListWebACLs", 400},
		{"wafv2", "", "", "session", "/", "AWSWAF_20190729.ListWebACLs", 400},
		{"wafv2", "", "", "", "/?X-Amz-Credential=TCSIM", "AWSWAF_20190729.ListWebACLs", 400},
		{"wafv2", "", "", "", "/", "AmazonSSM.GetParameter", 400},
		{"", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/date/us-east-1/wafv2/aws4_request", "", "", "/", "", 400},
	} {
		r := httptest.NewRequest("POST", c.path, strings.NewReader(`{"Scope":"REGIONAL"}`))
		r.Header.Set("X-Simulator-Service", c.service)
		r.Header.Set("Authorization", c.auth)
		r.Header.Set("X-Simulator-Region", c.region)
		r.Header.Set("X-Amz-Security-Token", c.session)
		r.Header.Set("X-Amz-Target", c.target)
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, r)
		if w.Code != c.status {
			t.Fatal(c, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"Scope":"REGIONAL"}`))
	r.Header.Set("X-Amz-Target", "AWSWAF_20190729.ListWebACLs")
	w := httptest.NewRecorder()
	(&Gateway{}).ServeHTTP(w, r)
	if w.Code != 501 {
		t.Fatal(w.Code)
	}
	r.Header.Set("X-Simulator-Principal", "untrusted")
	w = httptest.NewRecorder()
	gateway.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestWAFDispatchUsesIdentityPolicyAndPreservesExplicitDeny(t *testing.T) {
	db, _, identities := lambdaState(t)
	if _, e := NewAPIGatewayStore(db.Database()); e != nil {
		t.Fatal(e)
	}
	store, e := NewWAFStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	summary, e := store.Create(context.Background(), wafTestRegion, wafTestACL())
	if e != nil {
		t.Fatal(e)
	}
	policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"wafv2:GetWebACL"}, Resource: core.StringList{summary.ARN}}}}
	if e = identities.Set("acl-reader", policy); e != nil {
		t.Fatal(e)
	}
	gateway := &Gateway{WAF: store, Identities: identities}
	call := func(op, body string, status int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("X-Simulator-Principal", "acl-reader")
		r.Header.Set("X-Amz-Target", "AWSWAF_20190729."+op)
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatal(op, w.Code, w.Body.String())
		}
	}
	body := wafJSON(t, map[string]string{"Name": summary.Name, "Id": summary.ID, "Scope": "REGIONAL"})
	call("GetWebACL", body, 200)
	call("ListWebACLs", `{"Scope":"REGIONAL"}`, 403)
	policy.Statement = append(policy.Statement, core.Statement{Effect: "Deny", Action: core.StringList{"wafv2:*"}, Resource: core.StringList{"*"}})
	if e = identities.Set("acl-reader", policy); e != nil {
		t.Fatal(e)
	}
	call("GetWebACL", body, 403)
}
func TestWAFInvocationBlocksBeforeLambdaLookupAndRejectsUntrustedEvent(t *testing.T) {
	store, stage := wafFixture(t)
	ctx := context.Background()
	acl := wafTestACL()
	acl.Rules[0].Action = WAFAction{Block: &WAFEmpty{}}
	summary, e := store.Create(ctx, wafTestRegion, acl)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Associate(ctx, wafTestRegion, stage, summary.ARN); e != nil {
		t.Fatal(e)
	}
	apiID, stageName, e := wafStageParts(wafTestRegion, stage)
	if e != nil {
		t.Fatal(e)
	}
	// A non-existent Lambda URI deliberately demonstrates that blocking never
	// resolves a function/grant or starts the guest VM. Safe data must proceed.
	route := RESTRoute{API: RESTAPI{ID: apiID}, Stage: RESTStage{StageName: stageName}, Method: RESTMethod{MethodIntegration: &RESTIntegration{Type: "AWS_PROXY", HTTPMethod: "POST", URI: "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:missing/invocations"}}}
	functions, e := NewLambdaStore(store.db)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = functions.Close() })
	gateway := &Gateway{WAF: store, Lambdas: functions}
	event := apirouting.ProxyEventV1{Path: "/wp-json/batch/v1", HTTPMethod: "POST", Body: `{"requests":[{"body":{"category":"x' UNION SELECT id, marker, note FROM vault -- "}}]}`, RequestContext: apirouting.ProxyContextV1{APIID: apiID, Stage: stageName}}
	data, _ := json.Marshal(event)
	response, e := gateway.invokeREST(ctx, wafTestRegion, route, event.Path, event.HTTPMethod, data)
	if e != nil || response.StatusCode != 403 {
		t.Fatal(response, e)
	}
	event.Body = `{"category":"news"}`
	data, _ = json.Marshal(event)
	if _, e = gateway.invokeREST(ctx, wafTestRegion, route, event.Path, event.HTTPMethod, data); iamCode(e) != "BadGatewayException" {
		t.Fatal("safe input bypassed missing backend", e)
	}
	event.Body = "1 OR 1=1 -- "
	event.IsBase64Encoded = true
	data, _ = json.Marshal(event)
	if _, e = gateway.invokeREST(ctx, wafTestRegion, route, event.Path, event.HTTPMethod, data); iamCode(e) != "BadGatewayException" {
		t.Fatal("unsupported binary event accepted", e)
	}
	event.IsBase64Encoded = false
	event.RequestContext.APIID = "9876543210"
	data, _ = json.Marshal(event)
	if _, e = gateway.invokeREST(ctx, wafTestRegion, route, event.Path, event.HTTPMethod, data); iamCode(e) != "BadGatewayException" {
		t.Fatal("event could choose stage association", e)
	}
	apis, e := NewAPIGatewayStore(store.db)
	if e != nil {
		t.Fatal(e)
	}
	if e = apis.DeleteStage(ctx, wafTestRegion, apiID, stageName); iamCode(e) != "ConflictException" {
		t.Fatal("stage deletion conflict missing", e)
	}
	if e = apis.DeleteAPI(ctx, wafTestRegion, apiID); iamCode(e) != "ConflictException" {
		t.Fatal("API deletion conflict missing", e)
	}
}
