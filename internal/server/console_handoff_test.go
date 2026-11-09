package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func signedHandoff(c aws.STSCredentials, body string) *http.Request {
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/_simulator/v1/console-handoffs", strings.NewReader(body))
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
func handoffRequest(a *Access, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}
func handoffRedeem(a *Access, code string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/_simulator/login", strings.NewReader(url.Values{"handoff": {code}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return handoffRequest(a, r)
}
func newHandoffFixture(t *testing.T) (*Access, aws.STSCredentials, *bool, *core.Policy) {
	t.Helper()
	valid := true
	policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"ssm:GetParameter"}, Resource: core.StringList{"*"}}}}
	a, e := NewAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := aws.SDKIdentityFromContext(r.Context())
		if !ok || id.Principal != "viewer" {
			t.Error("missing STS browser identity")
		}
		p, ok := aws.SDKPolicyFromContext(r.Context())
		if !ok || len(p.Statement) != len(policy.Statement) {
			t.Error("missing fresh snapshot")
		}
		w.WriteHeader(204)
	}), strings.Repeat("a", 32), false)
	if e != nil {
		t.Fatal(e)
	}
	c, e := a.sdk.Issue(aws.STSSessionRequest{Principal: "viewer", RoleARN: "arn:aws:iam::000000000000:role/viewer", RoleID: "original-generation", SessionName: "browser-test", Duration: 15 * time.Minute, Validate: func(context.Context) error {
		if !valid {
			return fmt.Errorf("role deleted/replaced")
		}
		return nil
	}, Snapshot: func(context.Context) (core.Policy, error) { return policy, nil }})
	if e != nil {
		t.Fatal(e)
	}
	return a, c, &valid, &policy
}
func issueHandoffCode(t *testing.T, a *Access, c aws.STSCredentials) string {
	t.Helper()
	return issueHandoffCodeForParameter(t, a, c, "/own/value")
}
func issueHandoffCodeForParameter(t *testing.T, a *Access, c aws.STSCredentials, name string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"ParameterName": name, "Region": "us-east-1"})
	w := handoffRequest(a, signedHandoff(c, string(body)))
	if w.Code != 200 {
		t.Fatalf("issuance status %d: %s", w.Code, w.Body.String())
	}
	var data struct{ Code, ExpiresAt string }
	if json.Unmarshal(w.Body.Bytes(), &data) != nil || len(data.Code) != 64 || data.ExpiresAt == "" {
		t.Fatal("invalid handoff response")
	}
	return data.Code
}
func TestConsoleHandoffBindingReplayAndRevocation(t *testing.T) {
	a, c, valid, policy := newHandoffFixture(t)
	code := issueHandoffCode(t, a, c)
	w := handoffRedeem(a, code)
	if w.Code != 303 || w.Header().Get("Location") != "/_simulator/console/#/home?region=us-east-1" {
		t.Fatal("redemption destination", w.Code, w.Header())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("cookie attributes")
	}
	if handoffRedeem(a, code).Code != 401 {
		t.Fatal("handoff replay accepted")
	}
	send := func(path, principal string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://127.0.0.1:4566"+path, nil)
		r.AddCookie(cookies[0])
		if principal != "" {
			r.Header.Set("X-Simulator-Principal", principal)
		}
		return handoffRequest(a, r)
	}
	if send("/_simulator/v1/capabilities", "").Code != 204 {
		t.Fatal("browser rejected")
	}
	for _, path := range []string{"/_simulator/docs/", "/_simulator/docs/openapi.json", "/_simulator/docs/scalar.js", "/_simulator/docs/init.js"} {
		if send(path, "").Code != 204 {
			t.Fatal("static API reference rejected", path)
		}
		r := httptest.NewRequest("POST", "http://127.0.0.1:4566"+path, nil)
		r.AddCookie(cookies[0])
		if handoffRequest(a, r).Code != 403 {
			t.Fatal("reference path broadened mutation access", path)
		}
	}
	policy.Statement = nil
	if send("/_simulator/v1/capabilities", "").Code != 204 {
		t.Fatal("policy refresh lost")
	}
	if send("/_simulator/console/", "simulator-admin").Code != 403 {
		t.Fatal("admin selection accepted")
	}
	for _, path := range []string{"/_simulator/v1/iam/identities", "/_simulator/v1/access", "/_simulator/providers/gcp/storage/v1/b", "/_simulator/docs-admin/", "/"} {
		if send(path, "").Code != 403 {
			t.Fatal("broadened API access", path)
		}
	}
	*valid = false
	if send("/_simulator/v1/capabilities", "").Code != 401 {
		t.Fatal("role revocation ignored")
	}
	*valid = true
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/_simulator/logout", nil)
	r.AddCookie(cookies[0])
	if handoffRequest(a, r).Code != 204 || send("/_simulator/v1/capabilities", "").Code != 401 {
		t.Fatal("logout replay accepted")
	}
}
func TestConsoleHandoffAdmissionExpiryQuota(t *testing.T) {
	a, c, _, _ := newHandoffFixture(t)
	for _, body := range []string{`{}`, `{"ParameterName":"*","Region":"us-east-1"}`, `{"ParameterName":"/own","Region":"unknown"}`, `{"ParameterName":"/own","Region":"us-east-1","Policy":{}}`, `{"ParameterName":"/own","Region":"us-east-1"} {}`} {
		if handoffRequest(a, signedHandoff(c, body)).Code != 400 {
			t.Fatal("invalid body accepted", body)
		}
	}
	r := signedHandoff(c, `{"ParameterName":"/own","Region":"us-east-1"}`)
	r.Header.Set("Origin", "https://other.example")
	if handoffRequest(a, r).Code != 403 {
		t.Fatal("cross-origin issuance")
	}
	r = signedHandoff(c, `{"ParameterName":"/own","Region":"us-east-1"}`)
	r.URL.Path = "/_simulator/v1/access"
	if handoffRequest(a, r).Code != 403 {
		t.Fatal("signed route widening")
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1:4566/_simulator/v1/console-handoffs", strings.NewReader(`{"ParameterName":"/own","Region":"us-east-1"}`))
	r.Header.Set("X-Simulator-Token", strings.Repeat("a", 32))
	if handoffRequest(a, r).Code != 403 {
		t.Fatal("operator handoff accepted")
	}
	code := issueHandoffCode(t, a, c)
	start := time.Now()
	a.now = func() time.Time { return start.Add(61 * time.Second) }
	if handoffRedeem(a, code).Code != 401 {
		t.Fatal("expired code accepted")
	}
	a.now = time.Now
	for i := 0; i < 1000; i++ {
		a.handoffs[sha256.Sum256([]byte(fmt.Sprint(i)))] = consoleHandoff{expires: time.Now().Add(time.Minute)}
	}
	if handoffRequest(a, signedHandoff(c, `{"ParameterName":"/own","Region":"us-east-1"}`)).Code != 429 {
		t.Fatal("handoff quota ignored")
	}
}

func TestConsoleHandoffActualSSMIsolation(t *testing.T) {
	h, closeState, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer closeState()
	a, c, _, policy := newHandoffFixture(t)
	a.next = h
	send := func(op, name, region string, cookie *http.Cookie) *httptest.ResponseRecorder {
		data, _ := json.Marshal(map[string]any{"Name": name, "Value": "practice-value", "Type": "String", "Overwrite": true})
		if op == "GetParameter" {
			data, _ = json.Marshal(map[string]string{"Name": name})
		}
		r := httptest.NewRequest("POST", "http://127.0.0.1:4566/", strings.NewReader(string(data)))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "AmazonSSM."+op)
		r.Header.Set("X-Simulator-Region", region)
		if cookie == nil {
			r.Header.Set("X-Simulator-Token", strings.Repeat("a", 32))
		} else {
			r.AddCookie(cookie)
		}
		return handoffRequest(a, r)
	}
	for _, name := range []string{"/own/value", "own/value", "/other/value"} {
		if w := send("PutParameter", name, "us-east-1", nil); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	code := issueHandoffCode(t, a, c)
	login := handoffRedeem(a, code)
	cookie := login.Result().Cookies()[0]
	for _, path := range []string{"/_simulator/docs/", "/_simulator/docs/openapi.json", "/_simulator/docs/init.js", "/_simulator/docs/boot.js", "/_simulator/docs/scalar.js"} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:4566"+path, nil)
		r.AddCookie(cookie)
		w := handoffRequest(a, r)
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatal("embedded participant API reference unavailable", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "practice-value") {
			t.Fatal("static reference exposed a parameter value", path)
		}
	}
	if w := send("GetParameter", "/own/value", "us-east-1", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), "practice-value") {
		t.Fatal("own value unavailable", w.Code, w.Body.String())
	}
	for _, tc := range []struct{ op, name, region string }{{"GetParameter", "own/value", "us-east-1"}, {"GetParameter", "/other/value", "us-east-1"}, {"GetParameter", "/own/value", "us-west-2"}, {"PutParameter", "/own/value", "us-east-1"}} {
		if w := send(tc.op, tc.name, tc.region, cookie); w.Code != 403 {
			t.Fatal("role or boundary ignored", tc, w.Code, w.Body.String())
		}
	}
	policy.Statement[0].Action = core.StringList{"ssm:*"}
	if w := send("PutParameter", "/own/value", "us-east-1", cookie); w.Code != 200 {
		t.Fatal("allowed write denied", w.Code, w.Body.String())
	}
	for _, op := range []string{"GetParameter", "PutParameter"} {
		if w := send(op, "own/value", "us-east-1", cookie); w.Code != 403 {
			t.Fatal("leading-slash alias escaped exact name boundary", op, w.Code)
		}
	}
	if w := send("PutParameter", "/other/value", "us-east-1", cookie); w.Code != 403 {
		t.Fatal("broad role escaped boundary", w.Code)
	}
	if w := send("DescribeParameters", "/own/value", "us-east-1", cookie); w.Code != 403 {
		t.Fatal("enumeration allowed", w.Code)
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:4566/_simulator/v1/capabilities", nil)
	r.AddCookie(cookie)
	w := handoffRequest(a, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"parameterName":"/own/value"`) || !strings.Contains(w.Body.String(), `"identitySelection":false`) {
		t.Fatal("scope capability missing", w.Code)
	}
	// The inverse binding must also preserve the exact database key.
	aliasLogin := handoffRedeem(a, issueHandoffCodeForParameter(t, a, c, "own/value"))
	if aliasLogin.Code != 303 {
		t.Fatal("alias target login failed", aliasLogin.Code)
	}
	aliasCookie := aliasLogin.Result().Cookies()[0]
	for _, op := range []string{"GetParameter", "PutParameter"} {
		if w := send(op, "own/value", "us-east-1", aliasCookie); w.Code != 200 {
			t.Fatal("exact non-slash target denied", op, w.Code)
		}
		if w := send(op, "/own/value", "us-east-1", aliasCookie); w.Code != 403 {
			t.Fatal("slash-prefixed sibling escaped exact name boundary", op, w.Code)
		}
	}
}

func TestConsoleHandoffCrossSiteNavigationOnly(t *testing.T) {
	a, _, _, _ := newHandoffFixture(t)
	a.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_simulator/console/" || r.Header.Get("Sec-Fetch-Site") != "" {
			t.Error("unsafe login forwarding")
		}
		w.WriteHeader(204)
	})
	request := func(method, path, mode, dest string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:4566"+path, nil)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Sec-Fetch-Mode", mode)
		r.Header.Set("Sec-Fetch-Dest", dest)
		return handoffRequest(a, r)
	}
	if request("GET", "/_simulator/login", "navigate", "document").Code != 204 {
		t.Fatal("host console navigation blocked")
	}
	for _, tc := range []struct{ method, path, mode, dest string }{{"POST", "/_simulator/login", "navigate", "document"}, {"GET", "/_simulator/console/", "navigate", "document"}, {"GET", "/_simulator/login", "cors", "empty"}, {"GET", "/_simulator/login", "navigate", "iframe"}} {
		if request(tc.method, tc.path, tc.mode, tc.dest).Code != 403 {
			t.Fatal("cross-site request admitted", tc)
		}
	}
}

func TestConsoleHandoffRedemptionOriginQuotaAndSeparation(t *testing.T) {
	a, c, _, _ := newHandoffFixture(t)
	code := issueHandoffCode(t, a, c)
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/_simulator/login", strings.NewReader(url.Values{"handoff": {code}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://other.example")
	if handoffRequest(a, r).Code != 403 {
		t.Fatal("cross-origin redemption accepted")
	}
	// Rejected cross-origin requests cannot consume another tab's handoff.
	w := handoffRedeem(a, code)
	if w.Code != 303 {
		t.Fatal("origin refusal consumed code")
	}
	cookie := w.Result().Cookies()[0]
	header := httptest.NewRequest("GET", "http://127.0.0.1:4566/_simulator/v1/capabilities", nil)
	header.Header.Set("X-Simulator-Token", cookie.Value)
	if handoffRequest(a, header).Code != 401 {
		t.Fatal("browser token accepted as SDK token")
	}
	other, _, _, _ := newHandoffFixture(t)
	header.Header.Del("X-Simulator-Token")
	header.AddCookie(cookie)
	if handoffRequest(other, header).Code != 401 {
		t.Fatal("browser crossed process")
	}
	code = issueHandoffCode(t, a, c)
	for i := 0; i < 999; i++ {
		a.browsers[sha256.Sum256([]byte(fmt.Sprint(i)))] = browserSession{accessSession{"simulator-admin", time.Now().Add(time.Minute)}, a.operator}
	}
	if handoffRedeem(a, code).Code != 429 {
		t.Fatal("combined browser quota ignored")
	}
	if handoffRedeem(a, code).Code != 401 {
		t.Fatal("failed redemption replay accepted")
	}
	code = issueHandoffCode(t, a, c)
	r = httptest.NewRequest("POST", "http://127.0.0.1:4566/_simulator/login", strings.NewReader(url.Values{"handoff": {code}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept", "application/json")
	r.AddCookie(cookie)
	w = handoffRequest(a, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"Destination"`) {
		t.Fatal("replacement at quota failed", w.Code)
	}
	header = httptest.NewRequest("GET", "http://127.0.0.1:4566/_simulator/v1/capabilities", nil)
	header.AddCookie(cookie)
	if handoffRequest(a, header).Code != 401 {
		t.Fatal("replaced cookie replay")
	}
}

func TestConsoleHandoffConcurrentReplay(t *testing.T) {
	a, c, _, _ := newHandoffFixture(t)
	code := issueHandoffCode(t, a, c)
	statuses := make(chan int, 8)
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() { defer group.Done(); statuses <- handoffRedeem(a, code).Code }()
	}
	group.Wait()
	close(statuses)
	success := 0
	for status := range statuses {
		if status == 303 {
			success++
		} else if status != 401 {
			t.Fatal(status)
		}
	}
	if success != 1 {
		t.Fatal("concurrent redemption count", success)
	}
}

func TestConsoleHandoffRequiresAccessCapability(t *testing.T) {
	h, closeState, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer closeState()
	bareRequest := httptest.NewRequest("GET", "http://127.0.0.1:4566/_simulator/v1/capabilities", nil)
	bareRequest.Header.Set("X-Simulator-Authentication", "required")
	bareResponse := httptest.NewRecorder()
	h.ServeHTTP(bareResponse, bareRequest)
	if bareResponse.Code != 200 || strings.Contains(bareResponse.Body.String(), `"consoleHandoff"`) {
		t.Fatal("embedding without Access advertised console handoff", bareResponse.Code)
	}
}
