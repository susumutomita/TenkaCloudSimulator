package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Independent test signer exercises the actual Access boundary, not a prefilled
// SDK identity context. Secrets are synthetic process-local test values.
func accessSDKRequest(key, secret, token, service, principal string) *http.Request {
	body := "Action=GetCallerIdentity&Version=2011-06-15"
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if service == "ssm" {
		body = `{"Name":"/test"}`
		r = httptest.NewRequest("POST", "http://127.0.0.1:4566/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "AmazonSSM.GetParameter")
	}
	now := time.Now().UTC()
	r.Header.Set("X-Amz-Date", now.Format("20060102T150405Z"))
	if token != "" {
		r.Header.Set("X-Amz-Security-Token", token)
	}
	if principal != "" {
		r.Header.Set("X-Simulator-Principal", principal)
	}
	names := []string{"host"}
	for name := range r.Header {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	var canonical strings.Builder
	for _, name := range names {
		value := r.Header.Get(name)
		if name == "host" {
			value = r.Host
		}
		canonical.WriteString(name + ":" + value + "\n")
	}
	signed := strings.Join(names, ";")
	payload := sha256.Sum256([]byte(body))
	request := r.Method + "\n/\n\n" + canonical.String() + "\n" + signed + "\n" + hex.EncodeToString(payload[:])
	digest := sha256.Sum256([]byte(request))
	date := now.Format("20060102")
	scope := date + "/us-east-1/" + service + "/aws4_request"
	mac := func(key []byte, value string) []byte {
		h := hmac.New(sha256.New, key)
		_, _ = h.Write([]byte(value))
		return h.Sum(nil)
	}
	signing := mac(mac(mac(mac([]byte("AWS4"+secret), date), "us-east-1"), service), "aws4_request")
	signature := mac(signing, "AWS4-HMAC-SHA256\n"+r.Header.Get("X-Amz-Date")+"\n"+scope+"\n"+hex.EncodeToString(digest[:]))
	r.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%x", key, scope, signed, signature))
	return r
}

func TestAccessSDKWrapperIdentityAndIsolation(t *testing.T) {
	operator := strings.Repeat("a", 64)
	calls := 0
	var got aws.SDKIdentity
	var selected, role, sessionToken string
	var policy core.Policy
	var hasPolicy bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var ok bool
		got, ok = aws.SDKIdentityFromContext(r.Context())
		if !ok {
			t.Error("authenticated SDK identity missing")
		}
		selected = r.Header.Get("X-Simulator-Principal")
		role = r.Header.Get("X-Simulator-Access-Role")
		sessionToken = r.Header.Get("X-Amz-Security-Token")
		policy, hasPolicy = aws.SDKPolicyFromContext(r.Context())
		w.WriteHeader(204)
	})
	a, err := NewAccess(handler, operator, false)
	if err != nil {
		t.Fatal(err)
	}
	send := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	w := send(accessSDKRequest("TCSIMOPERATOR", operator, "", "sts", ""))
	if w.Code != 204 || calls != 1 || got.Account != "999999999999" || role != "operator" || hasPolicy {
		t.Fatal("operator identity boundary", w.Code, got, role, hasPolicy)
	}
	allowed := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"ssm:GetParameter"}, Resource: core.StringList{"arn:aws:ssm:us-east-1:000000000000:parameter/test"}}}}
	c, err := a.sdk.Issue(aws.STSSessionRequest{Principal: "reader", RoleARN: "arn:aws:iam::000000000000:role/reader", RoleID: "test-generation", SessionName: "test-session", Duration: time.Hour, Validate: func(context.Context) error { return nil }, Snapshot: func(context.Context) (core.Policy, error) { return allowed, nil }})
	if err != nil {
		t.Fatal(err)
	}
	w = send(accessSDKRequest(c.AccessKeyID, c.SecretAccessKey, c.SessionToken, "ssm", ""))
	if w.Code != 204 || calls != 2 || got.Principal != "reader" || selected != "reader" || role != "participant" || sessionToken != "" || !hasPolicy || core.Evaluate(policy, "ssm:PutParameter", "*") == "allowed" {
		t.Fatal("session boundary", w.Code, got, selected, role, hasPolicy)
	}
	w = send(accessSDKRequest(c.AccessKeyID, c.SecretAccessKey, c.SessionToken, "sts", core.Administrator))
	if w.Code != 403 || calls != 2 {
		t.Fatal("signed identity escalation accepted", w.Code, calls)
	}
	other, err := NewAccess(handler, strings.Repeat("b", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	other.ServeHTTP(w, accessSDKRequest(c.AccessKeyID, c.SecretAccessKey, c.SessionToken, "sts", ""))
	if w.Code != 403 || calls != 2 {
		t.Fatal("cross-process session accepted")
	}
}

func TestAccessSDKWrapperFailuresAreTypedAndDoNotFallBack(t *testing.T) {
	operator := strings.Repeat("c", 64)
	calls := 0
	a, err := NewAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(204) }), operator, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{"sts", "ssm"} {
		for _, kind := range []string{"wrong-secret", "header-token", "browser-cookie", "unknown-session", "tampered-body"} {
			t.Run(service+"/"+kind, func(t *testing.T) {
				r := accessSDKRequest("TCSIMOPERATOR", operator, "", service, "")
				switch kind {
				case "wrong-secret":
					r = accessSDKRequest("TCSIMOPERATOR", "wrong-private-secret", "", service, "")
				case "header-token":
					r.Header.Set("X-Simulator-Token", operator)
				case "browser-cookie":
					r.AddCookie(&http.Cookie{Name: a.cookie, Value: "even-an-invalid-browser-cookie"})
				case "unknown-session":
					r = accessSDKRequest("TCSIMASIAunknown", "wrong-private-secret", "unknown-token", service, "")
				case "tampered-body":
					r.Body = http.NoBody
				}
				w := httptest.NewRecorder()
				a.ServeHTTP(w, r)
				if w.Code != 403 || calls != 0 {
					t.Fatal("failed SDK request reached handler", w.Code, calls)
				}
				if service == "ssm" {
					var payload map[string]string
					if json.Unmarshal(w.Body.Bytes(), &payload) != nil || payload["__type"] != "UnrecognizedClientException" || w.Header().Get("Content-Type") != "application/x-amz-json-1.1" {
						t.Fatal("invalid SDK JSON error", w.Body.String())
					}
				} else {
					var payload struct {
						XMLName xml.Name
						Error   struct{ Code, Type, Message string }
					}
					if xml.Unmarshal(w.Body.Bytes(), &payload) != nil || payload.XMLName.Local != "ErrorResponse" || payload.Error.Code != "InvalidClientTokenId" || payload.Error.Type != "Sender" {
						t.Fatal("invalid SDK XML error", w.Body.String())
					}
				}
				if strings.Contains(w.Body.String(), operator) || strings.Contains(w.Body.String(), "wrong-private-secret") || strings.Contains(w.Body.String(), "unknown-token") {
					t.Fatal("credential data in error")
				}
			})
		}
	}
	// An unrelated legacy header remains on the old authenticated path, rather
	// than accidentally activating the native SDK branch.
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/", nil)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/test")
	r.Header.Set("X-Simulator-Token", operator)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 204 || calls != 1 {
		t.Fatal("legacy authentication regressed", w.Code, calls)
	}
}
