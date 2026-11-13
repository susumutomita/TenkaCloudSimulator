package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// signedLambdaRequest signs a Lambda REST request the way the AWS SDK does:
// method and path select the operation, there is no X-Amz-Target and the
// credential scope names the lambda service.
func signedLambdaRequest(key, secret, token, method, target, contentType string, body []byte, at time.Time) *http.Request {
	r := httptest.NewRequest(method, "http://127.0.0.1:4566"+target, strings.NewReader(string(body)))
	stamp := at.UTC().Format("20060102T150405Z")
	r.Header.Set("X-Amz-Date", stamp)
	headers := "host;x-amz-date"
	canonical := "host:127.0.0.1:4566\nx-amz-date:" + stamp + "\n"
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
		headers = "content-type;" + headers
		canonical = "content-type:" + contentType + "\n" + canonical
	}
	if token != "" {
		r.Header.Set("X-Amz-Security-Token", token)
		headers += ";x-amz-security-token"
		canonical += "x-amz-security-token:" + token + "\n"
	}
	digest := sha256.Sum256(body)
	request := method + "\n" + (&url.URL{Path: r.URL.Path}).EscapedPath() + "\n\n" + canonical + "\n" + headers + "\n" + hex.EncodeToString(digest[:])
	hash := sha256.Sum256([]byte(request))
	date := at.UTC().Format("20060102")
	scope := date + "/us-east-1/lambda/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(hash[:])
	signing := sdkHMAC(sdkHMAC(sdkHMAC(sdkHMAC([]byte("AWS4"+secret), date), "us-east-1"), "lambda"), "aws4_request")
	r.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%x", key, scope, headers, sdkHMAC(signing, toSign)))
	return r
}

func TestSDKSignatureAdmitsOnlyLambdaFunctionRESTOperations(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	a := NewSTSAuthority(strings.Repeat("l", 40))
	a.now = func() time.Time { return now }
	sign := func(method, path, contentType string, body []byte) *http.Request {
		return signedLambdaRequest("TCSIMOPERATOR", a.operator, "", method, path, contentType, body, now)
	}
	const root = "/2015-03-31/functions"
	for _, ok := range []struct{ method, path, contentType string }{
		{"GET", root, ""}, {"GET", root + "/", ""}, {"POST", root, "application/json"},
		{"GET", root + "/settings-viewer/configuration", ""}, {"GET", root + "/settings-viewer/configuration/", ""},
		{"POST", root + "/settings-viewer/invocations", ""}, {"POST", root + "/settings-viewer/invocations", "application/octet-stream"},
		{"POST", root + "/settings-viewer/invocations", "binary/octet-stream"}, {"PUT", root + "/settings-viewer/code", "application/json"},
		{"DELETE", root + "/settings-viewer", ""}, {"GET", root + "/" + strings.Repeat("a", 64) + "/configuration", ""},
	} {
		r, identity, err := a.Authenticate(sign(ok.method, ok.path, ok.contentType, []byte(`{}`)))
		if err != nil || identity.Principal != "simulator-admin" {
			t.Fatal("documented Lambda operation rejected", ok, err)
		}
		if body, _ := io.ReadAll(r.Body); string(body) != `{}` {
			t.Fatal("body lost during verification", ok)
		}
	}
	for name, bad := range map[string]struct{ method, path, contentType string }{
		"arn-name":            {"GET", root + "/arn:aws:lambda:us-east-1:000000000000:function:settings-viewer/configuration", ""},
		"long-name":           {"GET", root + "/" + strings.Repeat("a", 65) + "/configuration", ""},
		"empty-name":          {"GET", root + "//configuration", ""},
		"unlisted-operation":  {"GET", root + "/settings-viewer/policy", ""},
		"unsupported-get":     {"GET", root + "/settings-viewer", ""},
		"nested-operation":    {"PUT", root + "/settings-viewer/code/extra", "application/json"},
		"wrong-method":        {"GET", root + "/settings-viewer/invocations", ""},
		"configuration-write": {"PUT", root + "/settings-viewer/configuration", "application/json"},
		"other-version":       {"GET", "/2018-10-31/layers", ""},
		"root-post":           {"POST", "/", "application/x-www-form-urlencoded"},
		"form-content":        {"POST", root + "/settings-viewer/invocations", "application/x-www-form-urlencoded"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := a.Authenticate(sign(bad.method, bad.path, bad.contentType, []byte(`{}`))); err == nil {
				t.Fatal("unlisted Lambda request accepted")
			}
		})
	}
	escaped := sign("GET", root+"/settings%2Fviewer/configuration", "", nil)
	if escaped.URL.RawPath == "" {
		t.Fatal("test request lost its escaped path")
	}
	if _, _, err := a.Authenticate(escaped); err == nil {
		t.Fatal("escaped function name accepted")
	}
	target := sign("POST", root+"/settings-viewer/invocations", "", []byte(`{}`))
	target.Header.Set("X-Amz-Target", "AmazonSSM.GetParameter")
	if _, _, err := a.Authenticate(target); err == nil {
		t.Fatal("RPC target accepted on a REST Lambda path")
	}
	query := sign("POST", root+"/settings-viewer/invocations", "", []byte(`{}`))
	query.URL.RawQuery = "Qualifier=1"
	if _, _, err := a.Authenticate(query); err == nil {
		t.Fatal("unsigned query accepted")
	}
	moved := sign("POST", root+"/settings-viewer/invocations", "", []byte(`{}`))
	moved.URL.Path = root + "/other-function/invocations"
	if _, _, err := a.Authenticate(moved); err == nil {
		t.Fatal("signature reused for another function")
	}
	// UpdateFunctionCode carries a base64 ZIP; the Lambda limit is wider than the
	// 256 KiB control-plane limit, but it is still a limit.
	if _, _, err := a.Authenticate(sign("PUT", root+"/settings-viewer/code", "application/json", []byte(strings.Repeat("x", 384<<10)))); err != nil {
		t.Fatal("Lambda body at its limit rejected", err)
	}
	if _, _, err := a.Authenticate(sign("PUT", root+"/settings-viewer/code", "application/json", []byte(strings.Repeat("x", (384<<10)+1)))); err == nil {
		t.Fatal("oversized Lambda body accepted")
	}
}

func TestSDKSessionReachesLambdaWithItsPolicySnapshot(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	a := NewSTSAuthority(strings.Repeat("m", 40))
	a.now = func() time.Time { return now }
	snapshot := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"lambda:InvokeFunction"}, Resource: core.StringList{lambdaARN("us-east-1", "settings-viewer")}}}}
	credentials, err := a.Issue(STSSessionRequest{Principal: "viewer", RoleARN: "arn:aws:iam::000000000000:role/viewer", RoleID: "AROA-viewer", SessionName: "participant", Duration: 15 * time.Minute,
		Validate: func(context.Context) error { return nil }, Snapshot: func(context.Context) (core.Policy, error) { return snapshot, nil }})
	if err != nil {
		t.Fatal(err)
	}
	path := "/2015-03-31/functions/settings-viewer/invocations"
	r, identity, err := a.Authenticate(signedLambdaRequest(credentials.AccessKeyID, credentials.SecretAccessKey, credentials.SessionToken, "POST", path, "", []byte(`{}`), now))
	if err != nil || identity.Principal != "viewer" || r.Header.Get("X-Amz-Security-Token") != "" {
		t.Fatal("session Lambda request", identity, err)
	}
	policy, ok := SDKPolicyFromContext(r.Context())
	if !ok || core.Evaluate(policy, "lambda:InvokeFunction", lambdaARN("us-east-1", "settings-viewer")) != "allowed" || core.Evaluate(policy, "lambda:InvokeFunction", lambdaARN("us-east-1", "other")) == "allowed" {
		t.Fatal("session policy snapshot missing from Lambda request", policy, ok)
	}
	if _, _, err = a.Authenticate(signedLambdaRequest(credentials.AccessKeyID, credentials.SecretAccessKey, "", "POST", path, "", []byte(`{}`), now)); err == nil {
		t.Fatal("session key without its token accepted")
	}
	if _, _, err = a.Authenticate(signedLambdaRequest("TCSIMOPERATOR", a.operator, credentials.SessionToken, "POST", path, "", []byte(`{}`), now)); err == nil {
		t.Fatal("operator key carrying a session token accepted")
	}
}
