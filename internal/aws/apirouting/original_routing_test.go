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

func TestOriginalRouteProgramCardinalityAndRawSegments(t *testing.T) {
	cases := []struct {
		template string
		segments []string
		want     map[string]string
		score    int
		ok       bool
	}{
		{"", nil, map[string]string{}, 0, true},
		{"///literal/{key}//{rest+}///", []string{"literal", "%2F", "", ".", "..", "tail"}, map[string]string{"key": "%2F", "rest": "./../tail"}, 2, true},
		{"/one/{key}", []string{"one", "value", "extra"}, nil, -1, false},
		{"/one/{key}/tail", []string{"one", "value"}, nil, -1, false},
		{"/one/{key}/tail", []string{"other", "value", "tail"}, nil, -1, false},
		{"/one/{rest+}", []string{"one"}, nil, -1, false},
		{"/one/{rest+}", []string{"one", "part", "", "last"}, map[string]string{"rest": "part//last"}, 1, true},
		{"/one/{rest+}/tail", []string{"one", "part", "tail"}, nil, -1, false},
		{"/{}/tail", []string{"value", "tail"}, nil, -1, false},
		{"/{+}", []string{"value"}, nil, -1, false},
		{"/{same}/{same}", []string{"first", "last"}, map[string]string{"same": "last"}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.template, func(t *testing.T) {
			before := append([]string(nil), tc.segments...)
			got, score, ok := MatchPath(tc.template, tc.segments)
			if ok != tc.ok || score != tc.score || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("capture/cardinality mismatch: got %v/%d/%v want %v/%d/%v", got, score, ok, tc.want, tc.score, tc.ok)
			}
			if !reflect.DeepEqual(before, tc.segments) {
				t.Fatal("matching mutated the native request segments")
			}
		})
	}
	for _, tc := range []struct {
		path string
		want []string
	}{
		{"", nil}, {"////", nil}, {"///a///b///", []string{"a", "", "", "b"}},
		{"/%2F/./../", []string{"%2F", ".", ".."}},
	} {
		if got := SplitPath(tc.path); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("path bytes normalized: %q -> %v", tc.path, got)
		}
	}
}

func TestOriginalEventProjectionFieldSetAndInputFrame(t *testing.T) {
	r := httptest.NewRequest(http.MethodPatch, "/transport?item=one&item=two&empty=", nil)
	r.Header = http.Header{
		"X-App": {"first", "second"}, "x-app": {"last"},
		"Authorization": {"AWS4-HMAC-SHA256 Credential=host-only", "Bearer app-only"},
		"Cookie":        {"unrelated_local_session=host-only"}, "x-SIMULATOR-Other": {"host-only"},
		"X-Amz-Security-Token": {"host-only"}, "Connection": {"close"}, "Proxy-Authorization": {"host-only"},
	}
	route := ProxyRequest{APIID: "api", Stage: "prod", RequestID: "request", ResourcePath: "/{id}", Path: "/actual", PathParameters: map[string]string{"id": "actual"}, StageVariables: map[string]string{"label": "dummy"}}
	beforeHeaders := r.Header.Clone()
	beforePath := map[string]string{"id": "actual"}
	beforeStage := map[string]string{"label": "dummy"}
	data, err := BuildProxyEvent(r, route, []byte("<application>\n日本語"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("host-only")) || bytes.HasSuffix(data, []byte("\n")) {
		t.Fatal("transport authority or serializer terminator entered the event")
	}
	event, err := observeProxyJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if event.HTTPMethod != http.MethodPatch || event.Body != "<application>\n日本語" || event.Path != "/actual" || event.Resource != "/{id}" || event.RequestContext.Path != "/prod/actual" {
		t.Fatal("native method/body/path projection changed", event)
	}
	if event.Headers["x-app"] != "last" || !reflect.DeepEqual(event.MultiValueHeaders["x-app"], []string{"first", "second", "last"}) || event.Headers["authorization"] != "Bearer app-only" {
		t.Fatal("duplicate header data changed", event.Headers, event.MultiValueHeaders)
	}
	if event.QueryStringParameters["item"] != "two" || !reflect.DeepEqual(event.MultiValueQueryStringParameters["item"], []string{"one", "two"}) || event.QueryStringParameters["empty"] != "" {
		t.Fatal("duplicate/empty query data changed")
	}
	if !reflect.DeepEqual(r.Header, beforeHeaders) || !reflect.DeepEqual(route.PathParameters, beforePath) || !reflect.DeepEqual(route.StageVariables, beforeStage) || r.URL.RawQuery != "item=one&item=two&empty=" {
		t.Fatal("projection changed caller-owned input")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 12 {
		t.Fatal("unsupported event fields added", fields)
	}
	for _, key := range []string{"resource", "path", "httpMethod", "headers", "multiValueHeaders", "queryStringParameters", "multiValueQueryStringParameters", "pathParameters", "stageVariables", "body", "isBase64Encoded", "requestContext"} {
		if _, exists := fields[key]; !exists {
			t.Fatal("required event field omitted", key)
		}
	}
	var observedPathParameters map[string]string
	if err := json.Unmarshal(fields["pathParameters"], &observedPathParameters); err != nil || !reflect.DeepEqual(observedPathParameters, beforePath) || !reflect.DeepEqual(event.StageVariables, beforeStage) {
		t.Fatal("owned routing maps changed in the event", observedPathParameters, event.StageVariables, err)
	}
	var context map[string]json.RawMessage
	if err := json.Unmarshal(fields["requestContext"], &context); err != nil || len(context) != 7 {
		t.Fatal("unsupported context fields added", context, err)
	}
	for _, key := range []string{"accountId", "apiId", "stage", "requestId", "resourcePath", "httpMethod", "path"} {
		if _, exists := context[key]; !exists {
			t.Fatal("required context field omitted", key)
		}
	}
}

func TestOriginalEventExactEncodedBudgetAndNullCollections(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	route := ProxyRequest{APIID: "api", Stage: "prod", RequestID: "request", ResourcePath: "/", Path: "/"}
	base, err := BuildProxyEvent(r, route, nil)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(base, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"queryStringParameters", "multiValueQueryStringParameters", "pathParameters", "stageVariables"} {
		if string(fields[key]) != "null" {
			t.Fatalf("empty %s must remain null: %s", key, fields[key])
		}
	}
	if string(fields["headers"]) != "{}" || string(fields["multiValueHeaders"]) != "{}" {
		t.Fatal("empty header projections changed")
	}
	body := bytes.Repeat([]byte("x"), MaxProxyEventBytes-len(base))
	exact, err := BuildProxyEvent(r, route, body)
	if err != nil || len(exact) != MaxProxyEventBytes {
		t.Fatal("exact encoded budget refused", len(exact), err)
	}
	if output, err := BuildProxyEvent(r, route, append(body, 'x')); err == nil || output != nil {
		t.Fatal("encoded overflow admitted")
	}
	if output, err := BuildProxyEvent(r, route, []byte(strings.Repeat("<", len(body)/2))); err == nil || output != nil {
		t.Fatal("JSON escaping bypassed the encoded budget")
	}
}

func TestOriginalEventRejectsInvalidTrustedTextAndNilRequest(t *testing.T) {
	for _, field := range []string{"method", "api", "stage", "request", "nil-request", "nil-url"} {
		t.Run(field, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			route := ProxyRequest{APIID: "api", Stage: "prod", RequestID: "request", ResourcePath: "/", Path: "/"}
			bad := string([]byte{0xff})
			switch field {
			case "method":
				r.Method = bad
			case "api":
				route.APIID = bad
			case "stage":
				route.Stage = bad
			case "request":
				route.RequestID = bad
			case "nil-request":
				r = nil
			case "nil-url":
				r.URL = nil
			}
			if _, err := BuildProxyEvent(r, route, nil); err == nil {
				t.Fatal("invalid native context admitted")
			}
		})
	}
	// Authority exclusion must not hide malformed input by filtering it first.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Cookie", string([]byte{0xff}))
	if _, err := BuildProxyEvent(r, proxyRoute(), nil); err == nil {
		t.Fatal("invalid excluded transport header admitted")
	}
}
