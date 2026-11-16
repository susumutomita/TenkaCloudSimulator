package aws

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// NativeSDKServices are the signing services a TCSIMOPERATOR or assumed-role
// session may use. Hosts read the same list from the capability document.
var NativeSDKServices = []string{"sts", "iam", "cloudformation", "ssm", "lambda"}

type sdkSignature struct {
	key, date, region, service, scope, headers, signature string
}

func parseSDKSignature(r *http.Request) (sdkSignature, error) {
	fail := func() (sdkSignature, error) { return sdkSignature{}, fmt.Errorf("invalid local SigV4 authentication") }
	if len(r.Header.Values("Authorization")) != 1 || r.URL.RawQuery != "" {
		return fail()
	}
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 ") || len(authorization) > 4096 {
		return fail()
	}
	fields := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(authorization, "AWS4-HMAC-SHA256 "), ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || fields[key] != "" || value == "" {
			return fail()
		}
		fields[key] = value
	}
	if len(fields) != 3 || fields["Credential"] == "" || fields["SignedHeaders"] == "" || fields["Signature"] == "" {
		return fail()
	}
	parts := strings.Split(fields["Credential"], "/")
	if len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || len(parts[1]) != 8 || parts[4] != "aws4_request" {
		return fail()
	}
	regionSupported := false
	for _, region := range Regions {
		regionSupported = regionSupported || region == parts[2]
	}
	if !regionSupported {
		return fail()
	}
	// This native credential surface is the exact subset needed by unchanged
	// CloudFormation/SSM problems and by participants using a problem's own Lambda
	// function (REST paths below /2015-03-31/functions only). Other services fail closed.
	serviceSupported := false
	for _, service := range NativeSDKServices {
		serviceSupported = serviceSupported || service == parts[3]
	}
	if !serviceSupported {
		return fail()
	}
	decoded, err := hex.DecodeString(fields["Signature"])
	if err != nil || len(decoded) != 32 {
		return fail()
	}
	return sdkSignature{parts[0], parts[1], parts[2], parts[3], strings.Join(parts[1:], "/"), fields["SignedHeaders"], fields["Signature"]}, nil
}

func sdkHMAC(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(value))
	return h.Sum(nil)
}

func verifySDKSignature(r *http.Request, s sdkSignature, secret string, now time.Time) error {
	fail := func() error { return fmt.Errorf("local SigV4 signature verification failed") }
	handoff := (r.URL.Path == "/_simulator/v1/console-handoffs" || r.URL.Path == "/_simulator/v1/terminal-handoffs") && s.service == "sts"
	lambdaREST := s.service == "lambda"
	if secret == "" || r.URL.RawPath != "" || r.URL.User != nil {
		return fail()
	}
	switch {
	case lambdaREST:
		// The REST method and path decide the operation; there is no target header.
		if !lambdaSDKPath(r.Method, r.URL.Path) || r.Header.Get("X-Amz-Target") != "" {
			return fail()
		}
		switch r.Header.Get("Content-Type") {
		case "", "application/json", "application/octet-stream", "binary/octet-stream":
		default:
			return fail()
		}
	case (r.URL.Path != "/" && !handoff) || r.Method != "POST":
		return fail()
	case handoff:
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Amz-Target") != "" {
			return fail()
		}
	case s.service == "ssm":
		if !strings.HasPrefix(r.Header.Get("X-Amz-Target"), "AmazonSSM.") || r.Header.Get("Content-Type") != "application/x-amz-json-1.1" {
			return fail()
		}
	case r.Header.Get("X-Amz-Target") != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded"):
		return fail()
	}
	for _, name := range []string{"Content-Type", "X-Amz-Target", "X-Amz-Date", "X-Amz-Security-Token", "X-Amz-Content-Sha256", "X-Simulator-Principal", "X-Simulator-Service", "X-Simulator-Region"} {
		if len(r.Header.Values(name)) > 1 {
			return fail()
		}
	}
	if region := r.Header.Get("X-Simulator-Region"); region != "" && region != s.region {
		return fail()
	}
	if service := r.Header.Get("X-Simulator-Service"); service != "" && service != s.service {
		return fail()
	}
	stamp := r.Header.Get("X-Amz-Date")
	date, err := time.Parse("20060102T150405Z", stamp)
	if err != nil || date.Format("20060102") != s.date || date.Before(now.Add(-5*time.Minute)) || date.After(now.Add(5*time.Minute)) {
		return fail()
	}
	names := strings.Split(s.headers, ";")
	if len(names) < 2 || len(names) > 64 || !sort.StringsAreSorted(names) {
		return fail()
	}
	var canonical strings.Builder
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || name != strings.ToLower(name) || seen[name] {
			return fail()
		}
		for _, char := range name {
			if char != '-' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
				return fail()
			}
		}
		seen[name] = true
		value := r.Host
		if name != "host" {
			values := append([]string(nil), r.Header.Values(name)...)
			if len(values) == 0 {
				return fail()
			}
			for i := range values {
				values[i] = strings.Join(strings.Fields(values[i]), " ")
			}
			value = strings.Join(values, ",")
		}
		canonical.WriteString(name + ":" + value + "\n")
	}
	if !seen["host"] || !seen["x-amz-date"] || r.Header.Get("X-Amz-Security-Token") != "" && !seen["x-amz-security-token"] {
		return fail()
	}
	// Signed dispatch and identity selectors cannot be injected after signing.
	for _, name := range []string{"x-amz-target", "x-simulator-principal", "x-simulator-region", "x-simulator-service"} {
		if r.Header.Get(name) != "" && !seen[name] {
			return fail()
		}
	}
	// UpdateFunctionCode carries a base64 ZIP; Lambda itself caps that body at 384 KiB.
	limit := int64(256 << 10)
	if lambdaREST {
		limit = 384 << 10
	}
	var body []byte
	if r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, limit+1))
		_ = r.Body.Close()
		if err != nil || int64(len(body)) > limit {
			return fail()
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	payload := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(payload[:])
	if supplied := r.Header.Get("X-Amz-Content-Sha256"); supplied != "" && supplied != payloadHash {
		return fail()
	}
	// Only root control-plane POSTs, the exact STS console-handoff POST and the
	// Lambda function REST paths are in scope. Canonical escaping stays explicit;
	// raw paths are refused, and Lambda paths admit only unescaped function names.
	path := (&url.URL{Path: r.URL.Path}).EscapedPath()
	request := r.Method + "\n" + path + "\n\n" + canonical.String() + "\n" + s.headers + "\n" + payloadHash
	digest := sha256.Sum256([]byte(request))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + s.scope + "\n" + hex.EncodeToString(digest[:])
	dateKey := sdkHMAC([]byte("AWS4"+secret), s.date)
	regionKey := sdkHMAC(dateKey, s.region)
	serviceKey := sdkHMAC(regionKey, s.service)
	expected := sdkHMAC(sdkHMAC(serviceKey, "aws4_request"), toSign)
	provided, _ := hex.DecodeString(s.signature)
	if !hmac.Equal(provided, expected) {
		return fail()
	}
	return nil
}

// lambdaSDKPath admits the Lambda REST operations a signed SDK session may call:
// ListFunctions, GetFunctionConfiguration, Invoke, UpdateFunctionCode, CreateFunction
// and DeleteFunction. Function names must be plain names (no ARN, no escaping), so
// the canonical path equals the request path.
func lambdaSDKPath(method, path string) bool {
	const prefix = "/2015-03-31/functions"
	if path == prefix || path == prefix+"/" {
		return method == "GET" || method == "POST"
	}
	rest, ok := strings.CutPrefix(path, prefix+"/")
	if !ok {
		return false
	}
	name, operation, _ := strings.Cut(rest, "/")
	if name == "" || len(name) > 64 || strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
		return false
	}
	switch operation {
	case "":
		return method == "DELETE"
	case "configuration", "configuration/":
		return method == "GET"
	case "invocations", "invocations/":
		return method == "POST"
	case "code", "code/":
		return method == "PUT"
	}
	return false
}
