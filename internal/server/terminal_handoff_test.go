package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func signedTerminalHandoff(c aws.STSCredentials, body string) *http.Request {
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/_simulator/v1/terminal-handoffs", strings.NewReader(body))
	date := time.Now().UTC()
	stamp := date.Format("20060102T150405Z")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Amz-Date", stamp)
	r.Header.Set("X-Amz-Security-Token", c.SessionToken)
	names := "content-type;host;x-amz-date;x-amz-security-token"
	canonical := "content-type:application/json\nhost:" + r.Host + "\nx-amz-date:" + stamp + "\nx-amz-security-token:" + c.SessionToken + "\n"
	payload := sha256.Sum256([]byte(body))
	digest := sha256.Sum256([]byte("POST\n" + r.URL.Path + "\n\n" + canonical + "\n" + names + "\n" + hex.EncodeToString(payload[:])))
	scope := date.Format("20060102") + "/us-east-1/sts/aws4_request"
	mac := func(k []byte, s string) []byte {
		h := hmac.New(sha256.New, k)
		_, _ = h.Write([]byte(s))
		return h.Sum(nil)
	}
	key := mac(mac(mac(mac([]byte("AWS4"+c.SecretAccessKey), date.Format("20060102")), "us-east-1"), "sts"), "aws4_request")
	sig := mac(key, "AWS4-HMAC-SHA256\n"+stamp+"\n"+scope+"\n"+hex.EncodeToString(digest[:]))
	r.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%x", c.AccessKeyID, scope, names, sig))
	return r
}

func terminalFixture(t *testing.T) (*Access, aws.STSCredentials, *bool) {
	t.Helper()
	valid := true
	a, e := NewAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Value(terminalScopeKey{}).(terminalScope); !ok {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(204)
	}), strings.Repeat("a", 32), false)
	if e != nil {
		t.Fatal(e)
	}
	c, e := a.sdk.Issue(aws.STSSessionRequest{Principal: "viewer", RoleARN: "arn:aws:iam::000000000000:role/viewer", RoleID: "original", SessionName: "terminal", Duration: 15 * time.Minute, Validate: func(context.Context) error {
		if !valid {
			return fmt.Errorf("revoked")
		}
		return nil
	}, Snapshot: func(context.Context) (core.Policy, error) {
		return core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"ssm:*", "ssmmessages:*"}, Resource: core.StringList{"*"}}}}, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	return a, c, &valid
}
func terminalCode(t *testing.T, a *Access, c aws.STSCredentials) string {
	t.Helper()
	w := handoffRequest(a, signedTerminalHandoff(c, `{"Target":"i-12345678","Region":"us-east-1"}`))
	if w.Code != 200 {
		t.Fatalf("issuance status %d", w.Code)
	}
	var body struct{ Code string }
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Code) != 64 {
		t.Fatal("invalid code")
	}
	return body.Code
}
func terminalRedeem(a *Access, code string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/_simulator/terminal-redeem", strings.NewReader(url.Values{"Code": {code}}.Encode()))
	r.Header.Set("Origin", "http://127.0.0.1:4566")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return handoffRequest(a, r)
}
func TestTerminalHandoffOneUseScopedAndRevoked(t *testing.T) {
	a, c, valid := terminalFixture(t)
	code := terminalCode(t, a, c)
	w := terminalRedeem(a, code)
	if w.Code != 303 || strings.Contains(w.Header().Get("Location"), code) {
		t.Fatal("redemption")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatal("cookie")
	}
	if terminalRedeem(a, code).Code != 403 {
		t.Fatal("replay")
	}
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:4566"+path, nil)
		r.AddCookie(cookies[0])
		return handoffRequest(a, r)
	}
	if request("/_simulator/v1/terminal-control").Code != 204 {
		t.Fatal("scoped request")
	}
	if request("/").Code == 204 {
		t.Fatal("generic authority widened")
	}
	*valid = false
	if request("/_simulator/v1/terminal-control").Code != 403 {
		t.Fatal("role revocation ignored")
	}
}
func TestTerminalHandoffRejectsOperatorAndSpoofedContext(t *testing.T) {
	a, _, _ := terminalFixture(t)
	for _, token := range []string{"", strings.Repeat("a", 32)} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:4566/_simulator/v1/terminal-handoffs", strings.NewReader(`{"Target":"i-12345678","Region":"us-east-1"}`))
		r.Header.Set("X-Simulator-Token", token)
		r.Header.Set("X-Simulator-Principal", "viewer")
		if w := handoffRequest(a, r); w.Code == 200 {
			t.Fatal("forged participant")
		}
	}
}
func TestTerminalNavigationExceptionDoesNotBroadenPOST(t *testing.T) {
	a, _, _ := terminalFixture(t)
	r := httptest.NewRequest("GET", "http://127.0.0.1:4566/_simulator/terminal", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Sec-Fetch-Dest", "document")
	if handoffRequest(a, r).Code != 200 {
		t.Fatal("public navigation")
	}
	r.Method = "POST"
	if handoffRequest(a, r).Code != 403 {
		t.Fatal("cross-site POST")
	}
	r.Method = "GET"
	r.URL.RawQuery = "Code=secret"
	if handoffRequest(a, r).Code != 403 {
		t.Fatal("query navigation")
	}
}
func TestTerminalLogoutRevokesCookieAndLifetime(t *testing.T) {
	a, c, _ := terminalFixture(t)
	cookie := terminalRedeem(a, terminalCode(t, a, c)).Result().Cookies()[0]
	key := sha256.Sum256([]byte(cookie.Value))
	a.mu.Lock()
	life := a.terminalBrowsers[key].ctx
	a.mu.Unlock()
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/_simulator/logout", nil)
	r.AddCookie(cookie)
	if handoffRequest(a, r).Code != 204 {
		t.Fatal("logout")
	}
	select {
	case <-life.Done():
	default:
		t.Fatal("channel lifetime not canceled")
	}
	r = httptest.NewRequest("GET", "http://127.0.0.1:4566/_simulator/v1/terminal-context", nil)
	r.AddCookie(cookie)
	if handoffRequest(a, r).Code != 401 {
		t.Fatal("cookie survived logout")
	}
}
func TestTerminalHandoffExpiryAndInvalidOrigin(t *testing.T) {
	a, c, _ := terminalFixture(t)
	code := terminalCode(t, a, c)
	a.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if terminalRedeem(a, code).Code != 403 {
		t.Fatal("expired code")
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/_simulator/terminal-redeem", strings.NewReader("Code="+code))
	r.Header.Set("Origin", "https://evil.invalid")
	if handoffRequest(a, r).Code != 403 {
		t.Fatal("foreign origin")
	}
}
