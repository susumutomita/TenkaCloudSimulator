// Copyright (c) 2025 sivchari
// SPDX-License-Identifier: MIT
// Adapted from Kumo ce37a8fb406ecb96b5619204f645c26302b651a5,
// internal/service/execapi/event.go. Original notice: third_party/KUMO-LICENSE.txt.
// Local changes: REST v1 only, actual path separate from resource template,
// bounded event size, no UUID dependency and simulator credential redaction.
package apirouting

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

const MaxProxyEventBytes = 256 << 10

// ProxyRequest contains provider-selected routing state, never guest authority.
type ProxyRequest struct {
	APIID, Stage, ResourcePath, Path, RequestID string
	PathParameters, StageVariables              map[string]string
}

type ProxyEventV1 struct {
	Resource                        string              `json:"resource"`
	Path                            string              `json:"path"`
	HTTPMethod                      string              `json:"httpMethod"`
	Headers                         map[string]string   `json:"headers"`
	MultiValueHeaders               map[string][]string `json:"multiValueHeaders"`
	QueryStringParameters           map[string]string   `json:"queryStringParameters"`
	MultiValueQueryStringParameters map[string][]string `json:"multiValueQueryStringParameters"`
	PathParameters                  map[string]string   `json:"pathParameters"`
	StageVariables                  map[string]string   `json:"stageVariables"`
	RequestContext                  ProxyContextV1      `json:"requestContext"`
	Body                            string              `json:"body"`
	IsBase64Encoded                 bool                `json:"isBase64Encoded"`
}

type ProxyContextV1 struct {
	ResourcePath string `json:"resourcePath"`
	HTTPMethod   string `json:"httpMethod"`
	Path         string `json:"path"`
	APIID        string `json:"apiId"`
	Stage        string `json:"stage"`
	RequestID    string `json:"requestId"`
	AccountID    string `json:"accountId"`
}

// BuildProxyEvent preserves application data while sealing the simulator's
// transport credentials. Binary media configuration is not implemented here;
// invalid UTF-8 is rejected instead of silently replacing body bytes.
func BuildProxyEvent(r *http.Request, route ProxyRequest, body []byte) ([]byte, error) {
	if len(body) > MaxProxyEventBytes || !utf8.Valid(body) {
		return nil, errors.New("the local REST proxy supports bounded UTF-8 bodies only")
	}
	if !utf8.ValidString(route.Path) || !utf8.ValidString(route.ResourcePath) || route.APIID == "" || route.Stage == "" || route.RequestID == "" || !strings.HasPrefix(route.Path, "/") || !strings.HasPrefix(route.ResourcePath, "/") {
		return nil, errors.New("missing trusted REST routing context")
	}
	headers, multi := proxyHeaders(r)
	// URL.Query silently drops malformed pairs; do not turn malformed client
	// input into a different successful application request.
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	for name, values := range query {
		if !utf8.ValidString(name) {
			return nil, errors.New("invalid UTF-8 query name")
		}
		for _, value := range values {
			if !utf8.ValidString(value) {
				return nil, errors.New("invalid UTF-8 query value")
			}
		}
	}
	for name, values := range r.Header {
		if !utf8.ValidString(name) {
			return nil, errors.New("invalid UTF-8 header name")
		}
		for _, value := range values {
			if !utf8.ValidString(value) {
				return nil, errors.New("invalid UTF-8 header value")
			}
		}
	}
	for _, values := range []map[string]string{route.PathParameters, route.StageVariables} {
		for name, value := range values {
			if !utf8.ValidString(name) || !utf8.ValidString(value) {
				return nil, errors.New("invalid UTF-8 routing parameter")
			}
		}
	}
	singleQuery := map[string]string{}
	for name, values := range query {
		if len(values) > 0 {
			singleQuery[name] = values[len(values)-1]
		}
	}
	event := ProxyEventV1{Resource: route.ResourcePath, Path: route.Path, HTTPMethod: r.Method,
		Headers: headers, MultiValueHeaders: multi, QueryStringParameters: nilIfEmpty(singleQuery), MultiValueQueryStringParameters: nilMultiIfEmpty(query),
		PathParameters: nilIfEmpty(route.PathParameters), StageVariables: nilIfEmpty(route.StageVariables), Body: string(body),
		RequestContext: ProxyContextV1{ResourcePath: route.ResourcePath, HTTPMethod: r.Method, Path: "/" + route.Stage + route.Path, APIID: route.APIID, Stage: route.Stage, RequestID: route.RequestID, AccountID: "000000000000"}}
	// This concrete DTO has only string/bool/struct/slice/map-of-string fields;
	// it cannot contain a JSON-unsupported type, cycle or non-finite number.
	encoded, _ := json.Marshal(event)
	if len(encoded) > MaxProxyEventBytes {
		return nil, errors.New("encoded REST proxy event exceeds the local Lambda event limit")
	}
	return encoded, nil
}

func proxyHeaders(r *http.Request) (map[string]string, map[string][]string) {
	single, multi := map[string]string{}, map[string][]string{}
	keys := make([]string, 0, len(r.Header))
	for name := range r.Header {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, original := range keys {
		name := strings.ToLower(original)
		if strings.HasPrefix(name, "x-simulator-") || name == "x-amz-security-token" || name == "cookie" || name == "connection" || name == "proxy-authorization" {
			continue
		}
		for _, value := range r.Header[original] {
			if name == "authorization" && strings.EqualFold(strings.SplitN(value, " ", 2)[0], "AWS4-HMAC-SHA256") {
				continue
			}
			multi[name] = append(multi[name], value)
			single[name] = value
		}
	}
	// Cookies are automatically shared across ports on one host. This API-only
	// transport has no isolated application origin, so forwarding even a cookie
	// without our prefix could disclose another local application's authority.
	// Host authentication already happened; no Cookie header enters the guest.
	return single, multi
}

func nilIfEmpty(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	return values
}
func nilMultiIfEmpty(values map[string][]string) map[string][]string {
	if len(values) == 0 {
		return nil
	}
	return values
}
