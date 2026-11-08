package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func signedSTSRequest(key, secret, token string, at time.Time) *http.Request {
	body := "Action=GetCallerIdentity&Version=2011-06-15"
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Amz-Date", at.UTC().Format("20060102T150405Z"))
	headers := "content-type;host;x-amz-date"
	canonical := "content-type:application/x-www-form-urlencoded\nhost:127.0.0.1:4566\nx-amz-date:" + r.Header.Get("X-Amz-Date") + "\n"
	if token != "" {
		r.Header.Set("X-Amz-Security-Token", token)
		headers += ";x-amz-security-token"
		canonical += "x-amz-security-token:" + token + "\n"
	}
	digest := sha256.Sum256([]byte(body))
	request := "POST\n/\n\n" + canonical + "\n" + headers + "\n" + hex.EncodeToString(digest[:])
	hash := sha256.Sum256([]byte(request))
	date := at.UTC().Format("20060102")
	scope := date + "/us-east-1/sts/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + r.Header.Get("X-Amz-Date") + "\n" + scope + "\n" + hex.EncodeToString(hash[:])
	dateKey := sdkHMAC([]byte("AWS4"+secret), date)
	regionKey := sdkHMAC(dateKey, "us-east-1")
	serviceKey := sdkHMAC(regionKey, "sts")
	sig := sdkHMAC(sdkHMAC(serviceKey, "aws4_request"), toSign)
	r.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%x", key, scope, headers, sig))
	return r
}

func TestSTSCredentialSignatureAndBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	a := NewSTSAuthority("a-private-local-operator-token-1234567890")
	a.now = func() time.Time { return now }
	good := func() *http.Request { return signedSTSRequest("TCSIMOPERATOR", a.operator, "", now) }
	r, identity, err := a.Authenticate(good())
	if err != nil || identity.Account != "999999999999" || identity.Principal != "simulator-admin" {
		t.Fatal(identity, err)
	}
	if got, ok := SDKIdentityFromContext(r.Context()); !ok || got != identity {
		t.Fatal("missing identity context")
	}
	if got, ok := STSAuthorityFromContext(r.Context()); !ok || got != a {
		t.Fatal("missing authority context")
	}
	if body, _ := io.ReadAll(r.Body); string(body) != "Action=GetCallerIdentity&Version=2011-06-15" {
		t.Fatal("body lost during verification")
	}
	cases := map[string]func(*http.Request){
		"wrong-signature": func(r *http.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "Signature=", "Signature=00", 1))
		},
		"body-tamper":         func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("Action=AssumeRole")) },
		"host-tamper":         func(r *http.Request) { r.Host = "localhost:9999" },
		"path-tamper":         func(r *http.Request) { r.URL.Path = "/other" },
		"query":               func(r *http.Request) { r.URL.RawQuery = "Action=AssumeRole" },
		"principal-injection": func(r *http.Request) { r.Header.Set("X-Simulator-Principal", "another-role") },
		"target-injection":    func(r *http.Request) { r.Header.Set("X-Amz-Target", "AmazonSSM.GetParameter") },
		"service-mismatch":    func(r *http.Request) { r.Header.Set("X-Simulator-Service", "ssm") },
		"region-mismatch":     func(r *http.Request) { r.Header.Set("X-Simulator-Region", "us-west-2") },
		"session-on-operator": func(r *http.Request) { r.Header.Set("X-Amz-Security-Token", "untrusted") },
		"duplicate-auth":      func(r *http.Request) { r.Header.Add("Authorization", r.Header.Get("Authorization")) },
		"duplicate-date":      func(r *http.Request) { r.Header.Add("X-Amz-Date", r.Header.Get("X-Amz-Date")) },
		"unsigned-payload":    func(r *http.Request) { r.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD") },
		"payload-overflow":    func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", (256<<10)+1))) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := good()
			change(r)
			if _, _, err := a.Authenticate(r); err == nil {
				t.Fatal("invalid signature accepted")
			}
		})
	}
	for _, delta := range []time.Duration{-6 * time.Minute, 6 * time.Minute} {
		if _, _, err := a.Authenticate(signedSTSRequest("TCSIMOPERATOR", a.operator, "", now.Add(delta))); err == nil {
			t.Fatal("stale/future request accepted")
		}
	}
	if _, _, err := a.Authenticate(signedSTSRequest("TCSIMOPERATOR", "different-secret", "", now)); err == nil {
		t.Fatal("wrong secret accepted")
	}
}

func TestSTSCredentialsIsolationExpiryAndRevocation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	a := NewSTSAuthority(strings.Repeat("a", 32))
	a.now = func() time.Time { return now }
	valid := true
	input := STSSessionRequest{Principal: "reader", RoleARN: "arn:aws:iam::000000000000:role/reader", RoleID: "AROA-generation", SessionName: "native-sdk", Duration: 15 * time.Minute, Snapshot: func(context.Context) (core.Policy, error) { return core.Policy{}, nil }, Validate: func(context.Context) error {
		if !valid {
			return fmt.Errorf("role gone")
		}
		return nil
	}}
	credentials, err := a.Issue(input)
	if err != nil {
		t.Fatal(err)
	}
	request := func() *http.Request {
		return signedSTSRequest(credentials.AccessKeyID, credentials.SecretAccessKey, credentials.SessionToken, now)
	}
	r, id, err := a.Authenticate(request())
	if err != nil || id.Principal != "reader" || id.Account != "000000000000" || id.UserID != "AROA-generation:native-sdk" || r.Header.Get("X-Amz-Security-Token") != "" {
		t.Fatal(id, err)
	}
	other := NewSTSAuthority(strings.Repeat("b", 32))
	if _, _, err := other.Authenticate(request()); err == nil {
		t.Fatal("session crossed process authority")
	}
	missing := signedSTSRequest(credentials.AccessKeyID, credentials.SecretAccessKey, "", now)
	if _, _, err := a.Authenticate(missing); err == nil {
		t.Fatal("missing token accepted")
	}
	valid = false
	if _, _, err := a.Authenticate(request()); err == nil {
		t.Fatal("revoked generation accepted")
	}
	valid = true
	now = now.Add(15 * time.Minute)
	if _, _, err := a.Authenticate(request()); err == nil {
		t.Fatal("expired session accepted")
	}
	if len(a.sessions) != 0 {
		t.Fatal("expired session not removed")
	}
	for _, bad := range []STSSessionRequest{{}, {Principal: "simulator-admin"}, {Principal: "reader", RoleID: "role", RoleARN: "arn", SessionName: "name", Duration: 15 * time.Minute}} {
		if _, err := a.Issue(bad); err == nil {
			t.Fatal("invalid issuance accepted")
		}
	}
}

type advancingSDKBody struct {
	io.Reader
	advance func()
}

func (b *advancingSDKBody) Read(p []byte) (int, error) {
	if b.advance != nil {
		b.advance()
		b.advance = nil
	}
	return b.Reader.Read(p)
}
func (b *advancingSDKBody) Close() error { return nil }

func TestSTSSlowBodyExpiryAndHeaderPreservation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	a := NewSTSAuthority(strings.Repeat("x", 32))
	a.now = func() time.Time { return now }
	r := signedSTSRequest("TCSIMOPERATOR", a.operator, "", now)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded\t")
	verified, _, err := a.Authenticate(r)
	if err != nil || verified.Header.Get("Content-Type") != "application/x-www-form-urlencoded\t" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded\t" {
		t.Fatal("canonicalization changed the original header", err)
	}
	r = signedSTSRequest("TCSIMOPERATOR", a.operator, "", now)
	r.Body = &advancingSDKBody{Reader: r.Body, advance: func() { now = now.Add(6 * time.Minute) }}
	if _, _, err := a.Authenticate(r); err == nil {
		t.Fatal("signature expired during body read was accepted")
	}
	request := STSSessionRequest{Principal: "reader", RoleARN: "arn:aws:iam::000000000000:role/reader", RoleID: "role-one", SessionName: "reader-sdk", Duration: 15 * time.Minute, Validate: func(context.Context) error { return nil }, Snapshot: func(context.Context) (core.Policy, error) { return core.Policy{}, nil }}
	credentials, err := a.Issue(request)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(14 * time.Minute)
	r = signedSTSRequest(credentials.AccessKeyID, credentials.SecretAccessKey, credentials.SessionToken, now)
	r.Body = &advancingSDKBody{Reader: r.Body, advance: func() { now = now.Add(2 * time.Minute) }}
	if _, _, err := a.Authenticate(r); err == nil {
		t.Fatal("session expired during body read was accepted")
	}
}

func TestSTSConsoleBindingBoundaryAndFreshAuthority(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	a := NewSTSAuthority(strings.Repeat("a", 32))
	a.now = func() time.Time { return now }
	valid := true
	policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"*"}, Resource: core.StringList{"*"}}}}
	c, err := a.Issue(STSSessionRequest{Principal: "viewer", RoleARN: "arn:aws:iam::000000000000:role/viewer", RoleID: "generation", SessionName: "console-test", Duration: 15 * time.Minute, Validate: func(context.Context) error {
		if !valid {
			return fmt.Errorf("generation changed")
		}
		return nil
	}, Snapshot: func(context.Context) (core.Policy, error) { return policy, nil }})
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := a.Authenticate(signedSTSRequest(c.AccessKeyID, c.SecretAccessKey, c.SessionToken, now))
	if err != nil {
		t.Fatal(err)
	}
	bind, err := a.ConsoleBinding(request, "us-east-1", "/own/value")
	if err != nil {
		t.Fatal(err)
	}
	browser, _, err := bind(httptest.NewRequest("POST", "http://127.0.0.1/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := STSAuthorityFromContext(browser.Context()); ok {
		t.Fatal("browser can mint native sessions")
	}
	g := &Gateway{}
	arn := "arn:aws:ssm:us-east-1:000000000000:parameter/own/value"
	for _, tc := range []struct{ action, resource, want string }{{"ssm:GetParameter", arn, "allowed"}, {"ssm:PutParameter", arn, "allowed"}, {"ssm:GetParameter", arn + "-other", "implicitDeny"}, {"ssm:GetParameter", strings.Replace(arn, "us-east-1", "us-west-2", 1), "implicitDeny"}, {"ssm:DescribeParameters", "*", "implicitDeny"}, {"ssm:DeleteParameter", arn, "implicitDeny"}, {"iam:CreateRole", "*", "implicitDeny"}} {
		decision, err := g.sdkDecision(browser, "viewer", tc.action, tc.resource)
		if err != nil || decision != tc.want {
			t.Fatalf("%s %s => %s %v", tc.action, tc.resource, decision, err)
		}
	}
	policy.Statement = nil
	browser, _, err = bind(browser)
	if err != nil {
		t.Fatal(err)
	}
	if decision, _ := g.sdkDecision(browser, "viewer", "ssm:GetParameter", arn); decision == "allowed" {
		t.Fatal("stale role policy snapshot")
	}
	valid = false
	if _, _, err := bind(browser); err == nil {
		t.Fatal("deleted/recreated role accepted")
	}
	valid = true
	now = now.Add(15 * time.Minute)
	if _, _, err := bind(browser); err == nil {
		t.Fatal("STS expiry ignored")
	}
	now = now.Add(-15 * time.Minute)
	delete(a.sessions, c.AccessKeyID)
	if _, _, err := bind(browser); err == nil {
		t.Fatal("removed STS session accepted")
	}
	op, _, err := a.Authenticate(signedSTSRequest("TCSIMOPERATOR", a.operator, "", now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConsoleBinding(op, "us-east-1", "/own"); err == nil {
		t.Fatal("operator made participant binding")
	}
	if _, err := a.ConsoleBinding(httptest.NewRequest("GET", "http://127.0.0.1/", nil), "us-east-1", "/own"); err == nil {
		t.Fatal("unsigned made binding")
	}
}
