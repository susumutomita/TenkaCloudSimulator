package apirouting

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

// REST v1 wire names follow the AWS proxy input specification. This implementation
// projects a native request through explicit validation and authority exclusion,
// then encodes an owned event within the simulator's fixed event budget.
// https://docs.aws.amazon.com/apigateway/latest/developerguide/set-up-lambda-proxy-integrations.html
const MaxProxyEventBytes = 256 << 10

// ProxyRequest is trusted provider metadata, not an authority selected by a guest.
type ProxyRequest struct {
	APIID, Stage, ResourcePath, Path, RequestID string
	PathParameters, StageVariables              map[string]string
}

// ProxyEventV1 and ProxyContextV1 describe the supported public wire fields.
// Identity/authorizer/domain/time fields and binary media are not implemented.
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

var errEventBudget = errors.New("encoded REST proxy event exceeds the local Lambda event limit")

// The encoder's terminal newline is allowed separately from the event budget.
// This destination refuses excess encoded output. It is not a total allocation
// bound on encoding/json, the native request, or the provider process.
type eventSink struct{ bytes.Buffer }

func (sink *eventSink) Write(part []byte) (int, error) {
	if len(part) > MaxProxyEventBytes+1-sink.Len() {
		return 0, errEventBudget
	}
	return sink.Buffer.Write(part)
}

func validEventText(values ...string) bool {
	for _, text := range values {
		if !utf8.ValidString(text) {
			return false
		}
	}
	return true
}

func snapshotParameters(input map[string]string) (map[string]string, error) {
	if len(input) == 0 {
		return nil, nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		if !validEventText(key, value) {
			return nil, errors.New("invalid UTF-8 routing parameter")
		}
		output[key] = value
	}
	return output, nil
}

type applicationHeader struct {
	original, folded, value string
	ordinal                 int
}

func excludedTransportHeader(name, value string) bool {
	switch name {
	case "cookie", "connection", "proxy-authorization", "x-amz-security-token":
		return true
	case "authorization":
		scheme, _, _ := strings.Cut(value, " ")
		return strings.EqualFold(scheme, "AWS4-HMAC-SHA256")
	default:
		return strings.HasPrefix(name, "x-simulator-")
	}
}

// Header spellings may coexist in a directly constructed native request. Sort
// accepted values by original spelling and ordinal, retaining the established
// deterministic last-value projection without mutating the caller's slices.
func applicationHeaders(input http.Header) (map[string]string, map[string][]string, error) {
	var accepted []applicationHeader
	for original, values := range input {
		if !validEventText(original) {
			return nil, nil, errors.New("invalid UTF-8 header name")
		}
		for ordinal, value := range values {
			if !validEventText(value) {
				return nil, nil, errors.New("invalid UTF-8 header value")
			}
			folded := strings.ToLower(original)
			if !excludedTransportHeader(folded, value) {
				accepted = append(accepted, applicationHeader{original, folded, value, ordinal})
			}
		}
	}
	sort.Slice(accepted, func(a, b int) bool {
		if accepted[a].original == accepted[b].original {
			return accepted[a].ordinal < accepted[b].ordinal
		}
		return accepted[a].original < accepted[b].original
	})
	last, all := make(map[string]string), make(map[string][]string)
	for _, header := range accepted {
		last[header.folded] = header.value
		all[header.folded] = append(all[header.folded], header.value)
	}
	return last, all, nil
}

func applicationQuery(raw string) (map[string]string, url.Values, error) {
	all, err := url.ParseQuery(raw)
	if err != nil {
		return nil, nil, err // Never silently discard an invalid escaped pair.
	}
	if len(all) == 0 {
		return nil, nil, nil
	}
	last := make(map[string]string, len(all))
	for key, values := range all {
		if !validEventText(key) {
			return nil, nil, errors.New("invalid UTF-8 query name")
		}
		for _, value := range values {
			if !validEventText(value) {
				return nil, nil, errors.New("invalid UTF-8 query value")
			}
			last[key] = value
		}
	}
	return last, all, nil
}

// BuildProxyEvent supplies application data only. Host authentication precedes
// this projection; all cookies are excluded because local ports share cookies.
// An application Bearer value remains data and cannot grant simulator authority.
func BuildProxyEvent(request *http.Request, route ProxyRequest, body []byte) ([]byte, error) {
	if len(body) > MaxProxyEventBytes || !utf8.Valid(body) {
		return nil, errors.New("the local REST proxy supports bounded UTF-8 bodies only")
	}
	if request == nil || request.URL == nil || route.APIID == "" || route.Stage == "" || route.RequestID == "" || !strings.HasPrefix(route.Path, "/") || !strings.HasPrefix(route.ResourcePath, "/") || !validEventText(request.Method, route.APIID, route.Stage, route.RequestID, route.Path, route.ResourcePath) {
		return nil, errors.New("missing trusted REST routing context")
	}
	pathValues, err := snapshotParameters(route.PathParameters)
	if err != nil {
		return nil, err
	}
	stageValues, err := snapshotParameters(route.StageVariables)
	if err != nil {
		return nil, err
	}
	headers, multiHeaders, err := applicationHeaders(request.Header)
	if err != nil {
		return nil, err
	}
	query, multiQuery, err := applicationQuery(request.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	// These explicit, owned fields form a closed projection: the original
	// http.Request, identity, cookies and arbitrary caller objects never enter it.
	wire := map[string]any{
		"resource": route.ResourcePath, "path": route.Path, "httpMethod": request.Method,
		"headers": headers, "multiValueHeaders": multiHeaders,
		"queryStringParameters": query, "multiValueQueryStringParameters": multiQuery,
		"pathParameters": pathValues, "stageVariables": stageValues,
		"body": string(body), "isBase64Encoded": false,
		"requestContext": map[string]string{
			"accountId": "000000000000", "apiId": route.APIID, "stage": route.Stage,
			"requestId": route.RequestID, "resourcePath": route.ResourcePath,
			"httpMethod": request.Method, "path": "/" + route.Stage + route.Path,
		},
	}
	var sink eventSink
	if err := json.NewEncoder(&sink).Encode(wire); err != nil {
		return nil, err
	}
	encoded := sink.Bytes()
	encoded = encoded[:len(encoded)-1] // Encoder adds exactly one terminal newline.
	if len(encoded) > MaxProxyEventBytes {
		return nil, errEventBudget
	}
	return encoded, nil
}
