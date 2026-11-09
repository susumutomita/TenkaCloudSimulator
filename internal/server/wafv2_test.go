package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWAFNativeServerCompositionAndRegionalState(t *testing.T) {
	s := instance(t)
	call := func(op, body string, want int, headers map[string]string) map[string]any {
		t.Helper()
		h := map[string]string{"X-Amz-Target": "AWSWAF_20190729." + op, "Content-Type": "application/x-amz-json-1.1"}
		for k, v := range headers {
			h[k] = v
		}
		data := status(t, s, "POST", "/", body, want, h)
		var out map[string]any
		if e := json.Unmarshal(data, &out); e != nil {
			t.Fatal(string(data), e)
		}
		return out
	}
	created := call("CreateWebACL", `{"Name":"server-waf","Scope":"REGIONAL","DefaultAction":{"Allow":{}},"VisibilityConfig":{"SampledRequestsEnabled":false,"CloudWatchMetricsEnabled":false,"MetricName":"server-waf"}}`, 200, nil)["Summary"].(map[string]any)
	body, _ := json.Marshal(map[string]string{"Name": "server-waf", "Id": created["Id"].(string), "Scope": "REGIONAL"})
	call("GetWebACL", string(body), 200, nil)
	call("GetWebACL", string(body), 400, map[string]string{"X-Simulator-Region": "ap-northeast-1"})
	call("ListWebACLs", `{"Scope":"REGIONAL"}`, 403, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/date/us-east-1/wafv2/aws4_request"})
	call("ListWebACLs", `{"Scope":"REGIONAL"}`, 400, map[string]string{"X-Amz-Security-Token": "session"})
	caps := status(t, s, "GET", "/_simulator/v1/capabilities", "", 200, nil)
	var capability map[string]any
	if e := json.Unmarshal(caps, &capability); e != nil {
		t.Fatal(e)
	}
	waf := capability["services"].(map[string]any)["wafv2"].(map[string]any)
	if len(waf["operations"].([]any)) != 10 || waf["bodyInspectionBytes"] != float64(16384) || waf["cloudWatchDelivery"] != false || waf["cloudFormation"] != false {
		t.Fatal(waf)
	}
}

func TestWAFParticipantCannotBorrowOperatorOrOtherTeamAuthority(t *testing.T) {
	handler, closeHandler, e := New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = closeHandler() })
	operator := strings.Repeat("w", 64)
	access, e := NewAccess(handler, operator, false)
	if e != nil {
		t.Fatal(e)
	}
	call := func(h http.Handler, method, path, body, token string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1:4566"+path, strings.NewReader(body))
		r.Header.Set("X-Simulator-Token", token)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"wafv2:ListWebACLs","Resource":"*"}]}`
	w := call(access, "PUT", "/_simulator/v1/iam/principals/waf-viewer/policy", policy, operator, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(access, "POST", "/_simulator/v1/access", `{"Principal":"waf-viewer"}`, operator, nil)
	var issued struct{ AccessToken string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &issued) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	headers := map[string]string{"X-Amz-Target": "AWSWAF_20190729.ListWebACLs"}
	w = call(access, "POST", "/", `{"Scope":"REGIONAL"}`, issued.AccessToken, headers)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	headers["X-Simulator-Principal"] = "simulator-admin"
	w = call(access, "POST", "/", `{"Scope":"REGIONAL"}`, issued.AccessToken, headers)
	if w.Code != 403 {
		t.Fatal("participant could select admin", w.Code)
	}
	delete(headers, "X-Simulator-Principal")
	headers["X-Amz-Target"] = "AWSWAF_20190729.CreateWebACL"
	w = call(access, "POST", "/", `{"Name":"forbidden","Scope":"REGIONAL","DefaultAction":{"Allow":{}},"VisibilityConfig":{"MetricName":"forbidden","CloudWatchMetricsEnabled":false,"SampledRequestsEnabled":false}}`, issued.AccessToken, headers)
	if w.Code != 403 {
		t.Fatal("viewer could mutate WAF", w.Code, w.Body.String())
	}
	other, closeOther, e := New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = closeOther() })
	otherAccess, e := NewAccess(other, strings.Repeat("z", 64), false)
	if e != nil {
		t.Fatal(e)
	}
	headers["X-Amz-Target"] = "AWSWAF_20190729.ListWebACLs"
	w = call(otherAccess, "POST", "/", `{"Scope":"REGIONAL"}`, issued.AccessToken, headers)
	if w.Code != 401 {
		t.Fatal("cross-team token accepted", w.Code, w.Body.String())
	}
}
