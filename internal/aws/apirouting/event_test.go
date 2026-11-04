package apirouting

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func proxyRoute() ProxyRequest {
	return ProxyRequest{APIID: "local-api", Stage: "prod", ResourcePath: "/{proxy+}", Path: "/wp-json/batch/v1", RequestID: "local-request", PathParameters: map[string]string{"proxy": "wp-json/batch/v1"}}
}

func TestRESTProxyEventPreservesActualRouteAndApplicationData(t *testing.T) {
	r := httptest.NewRequest("POST", "/ignored-transport-prefix?query=one&query=two&empty=&encoded=a%2Fb", nil)
	r.Header.Add("X-Application", "first")
	r.Header.Add("X-Application", "last")
	r.Header.Set("Authorization", "Bearer application-only-dummy")
	r.Header.Set("Cookie", "wordpress_session=dummy; other_local_app=private-host-session; tcsim_0123456789ab=private-local-operator; application=keep; tcapi_local=private-api-session")
	r.Header.Set("X-Simulator-Token", "private-token")
	r.Header.Set("X-Simulator-Principal", "simulator-admin")
	r.Header.Set("X-Simulator-Access-Role", "operator")
	r.Header.Set("X-Amz-Security-Token", "private-session")
	r.Header.Set("Proxy-Authorization", "private-proxy")
	r.Header.Set("Connection", "keep-alive")
	encoded, err := BuildProxyEvent(r, proxyRoute(), []byte(`{"requests":[{"method":"POST"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("private-")) || bytes.Contains(encoded, []byte("simulator-admin")) {
		t.Fatal("transport authority leaked to the function")
	}
	var event ProxyEventV1
	if err = json.Unmarshal(encoded, &event); err != nil {
		t.Fatal(err)
	}
	if event.Resource != "/{proxy+}" || event.Path != "/wp-json/batch/v1" || event.RequestContext.Path != "/prod/wp-json/batch/v1" || event.RequestContext.ResourcePath != event.Resource || event.HTTPMethod != "POST" {
		t.Fatal("template was substituted for the actual request path", event)
	}
	if event.Headers["x-application"] != "last" || !reflect.DeepEqual(event.MultiValueHeaders["x-application"], []string{"first", "last"}) || event.Headers["authorization"] != "Bearer application-only-dummy" {
		t.Fatal(event.Headers, event.MultiValueHeaders)
	}
	if _, present := event.Headers["cookie"]; present {
		t.Fatal(event.Headers)
	}
	if event.QueryStringParameters["query"] != "two" || !reflect.DeepEqual(event.MultiValueQueryStringParameters["query"], []string{"one", "two"}) || event.QueryStringParameters["encoded"] != "a/b" || event.IsBase64Encoded {
		t.Fatal(event)
	}
	if event.RequestContext.AccountID != "000000000000" || event.RequestContext.RequestID != "local-request" {
		t.Fatal(event.RequestContext)
	}
}

func TestRESTProxyEventRejectsMalformedOrOversizedData(t *testing.T) {
	for name, tc := range map[string]struct {
		body  []byte
		query string
		route ProxyRequest
	}{
		"raw-body":        {bytes.Repeat([]byte("x"), MaxProxyEventBytes+1), "", proxyRoute()},
		"escaped-body":    {bytes.Repeat([]byte{1}, MaxProxyEventBytes/2), "", proxyRoute()},
		"utf8":            {[]byte{0xff}, "", proxyRoute()},
		"query":           {nil, "bad=%zz", proxyRoute()},
		"query-utf8":      {nil, "bad=%FF", proxyRoute()},
		"missing-context": {nil, "", ProxyRequest{}},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.URL.RawQuery = tc.query
			if _, err := BuildProxyEvent(r, tc.route, tc.body); err == nil {
				t.Fatal("invalid event silently changed into a successful request")
			}
		})
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/dummy")
	r.Header.Set("Cookie", "tcsim_only=private-value")
	encoded, err := BuildProxyEvent(r, proxyRoute(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var event ProxyEventV1
	if err = json.Unmarshal(encoded, &event); err != nil {
		t.Fatal(err)
	}
	if len(event.Headers) != 0 || event.QueryStringParameters != nil || event.MultiValueQueryStringParameters != nil || event.StageVariables != nil || strings.Contains(string(encoded), "private-value") {
		t.Fatal(string(encoded))
	}
}

func TestRESTProxyEventRejectsInvalidUTF8AcrossInputSources(t *testing.T) {
	for _, source := range []string{"query-name", "query-value", "header-name", "header-value", "path-parameter", "stage-variable"} {
		t.Run(source, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/", nil)
			route := proxyRoute()
			switch source {
			case "query-name":
				request.URL.RawQuery = "%FF=value"
			case "query-value":
				request.URL.RawQuery = "name=%FF"
			case "header-name":
				request.Header[string([]byte{0xff})] = []string{"value"}
			case "header-value":
				request.Header.Set("X-App", string([]byte{0xff}))
			case "path-parameter":
				route.PathParameters = map[string]string{"proxy": string([]byte{0xff})}
			case "stage-variable":
				route.StageVariables = map[string]string{"mode": string([]byte{0xff})}
			}
			if _, err := BuildProxyEvent(request, route, nil); err == nil {
				t.Fatal("invalid UTF-8 changed silently at the guest boundary")
			}
		})
	}
}
