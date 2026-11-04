package apirouting

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// ProxyResponse is the bounded REST v1 Lambda response. Binary media are not
// configured by this initial slice and therefore fail explicitly.
type ProxyResponse struct {
	StatusCode        int                 `json:"statusCode"`
	Headers           map[string]string   `json:"headers"`
	MultiValueHeaders map[string][]string `json:"multiValueHeaders"`
	Body              string              `json:"body"`
	IsBase64Encoded   bool                `json:"isBase64Encoded"`
}

func ParseProxyResponse(payload []byte) (ProxyResponse, error) {
	var response ProxyResponse
	if len(payload) > MaxProxyEventBytes || !utf8.Valid(payload) {
		return response, errors.New("malformed or oversized Lambda proxy response")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return response, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return response, errors.New("trailing Lambda proxy response")
	}
	if response.StatusCode < 200 || response.StatusCode > 599 || response.IsBase64Encoded {
		return response, errors.New("unsupported Lambda proxy status or binary media")
	}
	size := len(response.Body)
	for name, value := range response.Headers {
		if !validHeader(name, value) {
			return response, errors.New("invalid Lambda proxy header")
		}
		size += len(name) + len(value)
	}
	for name, values := range response.MultiValueHeaders {
		if !validHeader(name, "") {
			return response, errors.New("invalid Lambda proxy header name")
		}
		for _, value := range values {
			if !validHeader(name, value) {
				return response, errors.New("invalid Lambda proxy header")
			}
			size += len(name) + len(value)
		}
	}
	if size > MaxProxyEventBytes {
		return response, errors.New("oversized Lambda proxy response")
	}
	return response, nil
}
func validHeader(name, value string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	for _, r := range value {
		if r == '\r' || r == '\n' || r == 0 || r < 32 && r != '\t' || r == 127 {
			return false
		}
	}
	return true
}

// DownloadHeaders deliberately differs from live API Gateway: untrusted guest
// output cannot execute on the operator console origin or change its cookies,
// navigation, CSP or CORS. App hosting remains unavailable until isolated-origin
// transport exists. The body and valid HTTP status remain inspectable by API clients.
func DownloadHeaders(response ProxyResponse) http.Header {
	result := http.Header{}
	allowed := func(name string) bool {
		lower := strings.ToLower(name)
		return lower == "content-language" || lower == "etag" || strings.HasPrefix(lower, "x-app-")
	}
	for name, value := range response.Headers {
		if allowed(name) {
			result.Set(name, value)
		}
	}
	// Multi-value entries win for the same case-insensitive header name.
	for name, values := range response.MultiValueHeaders {
		if allowed(name) {
			result.Del(name)
			for _, value := range values {
				result.Add(name, value)
			}
		}
	}
	result.Set("Content-Type", "application/octet-stream")
	result.Set("Content-Disposition", `attachment; filename="response.bin"`)
	result.Set("Content-Security-Policy", "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	result.Set("X-Content-Type-Options", "nosniff")
	result.Set("Cache-Control", "no-store")
	return result
}
