package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

type observedActorRequest struct {
	actor           provider.Actor
	valid           bool
	principal, role string
}

func TestAccessActorHTTPAuthorityAndCredentialSource(t *testing.T) {
	seen := make(chan observedActorRequest, 32)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := provider.ActorFromContext(r.Context())
		seen <- observedActorRequest{actor, ok, r.Header.Get("X-Simulator-Principal"), r.Header.Get("X-Simulator-Access-Role")}
		w.WriteHeader(http.StatusNoContent)
	})
	a, err := NewAccess(next, strings.Repeat("a", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewAccess(next, strings.Repeat("b", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewServer(a)
	defer first.Close()
	second := httptest.NewServer(b)
	defer second.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	request := func(origin, method, path, token, body string, headers map[string]string, cookie *http.Cookie) *http.Response {
		t.Helper()
		r, err := http.NewRequest(method, origin+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			r.Header.Set("X-Simulator-Token", token)
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	closeResponse := func(r *http.Response, status int) {
		t.Helper()
		defer r.Body.Close()
		_, _ = io.Copy(io.Discard, r.Body)
		if r.StatusCode != status {
			t.Fatalf("status %d, want %d", r.StatusCode, status)
		}
	}
	issue := request(first.URL, "POST", "/_simulator/v1/access", strings.Repeat("a", 64), `{"Principal":"reader"}`, nil, nil)
	var issued struct{ AccessToken string }
	if issue.StatusCode != 200 || json.NewDecoder(issue.Body).Decode(&issued) != nil || issued.AccessToken == "" {
		t.Fatal("participant issuance failed")
	}
	issue.Body.Close()
	observe := func(expected provider.Actor, principal, role string) {
		t.Helper()
		select {
		case got := <-seen:
			if !got.valid || got.actor != expected || got.principal != principal || got.role != role {
				t.Fatalf("incorrect actor or changed AWS headers: %#v", got)
			}
		default:
			t.Fatal("authenticated handler was not reached")
		}
	}
	operator := provider.Actor{CredentialIdentity: "simulator-admin", ExerciseIdentity: "simulator-admin", CredentialRole: provider.CredentialOperator}
	closeResponse(request(first.URL, "GET", "/", strings.Repeat("a", 64), "", nil, nil), 204)
	observe(operator, "", "operator") // Default wire principal remains empty, as before.
	operator.ExerciseIdentity = "reader"
	closeResponse(request(first.URL, "GET", "/", strings.Repeat("a", 64), "", map[string]string{"X-Simulator-Principal": "reader", "X-Simulator-Access-Role": "participant"}, nil), 204)
	observe(operator, "reader", "operator")
	participant := provider.Actor{CredentialIdentity: "reader", ExerciseIdentity: "reader", CredentialRole: provider.CredentialParticipant}
	closeResponse(request(first.URL, "GET", "/", issued.AccessToken, "", map[string]string{"X-Simulator-Access-Role": "operator", "X-Simulator-Actor": "simulator-admin"}, nil), 204)
	observe(participant, "reader", "participant")
	closeResponse(request(first.URL, "GET", "/", issued.AccessToken, "", map[string]string{"X-Simulator-Principal": "reader"}, nil), 204)
	observe(participant, "reader", "participant")
	closeResponse(request(first.URL, "GET", "/_simulator/v1/capabilities", issued.AccessToken, "", map[string]string{"X-Simulator-Principal": "simulator-admin"}, nil), 204)
	observe(participant, "reader", "participant") // Existing capabilities exception still sanitizes identity.
	for _, selected := range []string{"simulator-admin", "other"} {
		closeResponse(request(first.URL, "GET", "/", issued.AccessToken, "", map[string]string{"X-Simulator-Principal": selected}, nil), 403)
	}
	for _, token := range []string{"", "invalid", strings.Repeat("b", 64)} {
		closeResponse(request(first.URL, "GET", "/", token, "", map[string]string{"X-Simulator-Access-Role": "operator", "X-Simulator-Principal": "simulator-admin"}, nil), 401)
	}
	for _, token := range []string{strings.Repeat("a", 64), issued.AccessToken} {
		closeResponse(request(second.URL, "GET", "/", token, "", nil, nil), 401)
	}
	// Browser and SDK credential sources follow the existing Access implementation.
	login := request(first.URL, "POST", "/_simulator/login", "", url.Values{"token": {issued.AccessToken}}.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, nil)
	cookies := login.Cookies()
	closeResponse(login, 303)
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Value == issued.AccessToken {
		t.Fatal("missing private browser cookie")
	}
	closeResponse(request(first.URL, "GET", "/", "", "", nil, cookies[0]), 204)
	observe(participant, "reader", "participant")
	closeResponse(request(second.URL, "GET", "/", "", "", nil, cookies[0]), 401)
	closeResponse(request(first.URL, "GET", "/", "invalid", "", nil, cookies[0]), 401) // Invalid explicit SDK token never falls back to a valid cookie.
	closeResponse(request(first.URL, "GET", "/", issued.AccessToken, "", nil, nil), 204)
	observe(participant, "reader", "participant")
	// Current main mints independent, hash-stored browser credentials. Neither
	// source may be replayed through the other credential channel.
	closeResponse(request(first.URL, "GET", "/", cookies[0].Value, "", nil, nil), 401)
	rawSDKCookie := *cookies[0]
	rawSDKCookie.Value = issued.AccessToken
	closeResponse(request(first.URL, "GET", "/", "", "", nil, &rawSDKCookie), 401)
	secondLogin := request(first.URL, "POST", "/_simulator/login", "", url.Values{"token": {issued.AccessToken}}.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, nil)
	otherCookies := secondLogin.Cookies()
	closeResponse(secondLogin, 303)
	if len(otherCookies) != 1 || otherCookies[0].Value == cookies[0].Value {
		t.Fatal("browser credentials not independent")
	}
	closeResponse(request(first.URL, "POST", "/_simulator/logout", "", "", map[string]string{"Origin": first.URL}, cookies[0]), 204)
	closeResponse(request(first.URL, "GET", "/", "", "", nil, cookies[0]), 401)
	closeResponse(request(first.URL, "GET", "/", "", "", nil, otherCookies[0]), 204)
	observe(participant, "reader", "participant")
	closeResponse(request(first.URL, "GET", "/", issued.AccessToken, "", nil, nil), 204)
	observe(participant, "reader", "participant")
	// Anonymous bootstrap remains public but receives no actor; new providers must deny it.
	closeResponse(request(first.URL, "GET", "/_simulator/console/app.js", "", "", map[string]string{"X-Simulator-Actor": "operator"}, nil), 204)
	anonymous := <-seen
	if anonymous.valid || anonymous.actor != (provider.Actor{}) {
		t.Fatal("bootstrap authenticated raw headers")
	}
	select {
	case <-seen:
		t.Fatal("rejected request reached provider")
	default:
	}
}

func TestAccessActorPreservesNativeAWSStateAndIAM(t *testing.T) {
	native, closeState, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeState(); err != nil {
			t.Error(err)
		}
	}()
	operatorToken := strings.Repeat("c", 64)
	access, err := NewAccess(native, operatorToken, false)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(access)
	defer srv.Close()
	op := map[string]string{"X-Simulator-Token": operatorToken}
	status(t, srv, "PUT", "/actor-boundary", "", 200, op)
	status(t, srv, "PUT", "/actor-boundary/value", "preserved", 200, op)
	status(t, srv, "PUT", "/_simulator/v1/iam/principals/reader/policy", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::actor-boundary/*"}]}`, 200, op)
	issue := status(t, srv, "POST", "/_simulator/v1/access", `{"Principal":"reader"}`, 200, op)
	var session struct{ AccessToken string }
	if json.Unmarshal(issue, &session) != nil || session.AccessToken == "" {
		t.Fatal("missing participant token")
	}
	for _, headers := range []map[string]string{
		{"X-Simulator-Token": operatorToken, "X-Simulator-Principal": "reader"},
		{"X-Simulator-Token": session.AccessToken, "X-Simulator-Access-Role": "operator"},
	} {
		if got := status(t, srv, "GET", "/actor-boundary/value", "", 200, headers); string(got) != "preserved" {
			t.Fatal("native value changed")
		}
		status(t, srv, "PUT", "/actor-boundary/value", "unauthorized", 403, headers)
	}
	status(t, srv, "PUT", "/actor-boundary/value", "spoofed", 403, map[string]string{"X-Simulator-Token": session.AccessToken, "X-Simulator-Principal": "simulator-admin"})
	if got := status(t, srv, "GET", "/actor-boundary/value", "", 200, op); string(got) != "preserved" {
		t.Fatal("denied write changed native state")
	}
	status(t, srv, "DELETE", "/actor-boundary/value", "", 204, op)
	status(t, srv, "DELETE", "/actor-boundary", "", 204, op)
}
