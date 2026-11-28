package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	callbackContractApp    = "https://127.0.0.1:7318"
	callbackContractIssuer = "https://127.0.0.1:7319/_simulator/cognito/us-east-1_callback_fixture"
	callbackContractClient = "test-callback-client"
)

type callbackContractRoundTrip func(*http.Request) (*http.Response, error)

func (f callbackContractRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func callbackContractRequest(path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "127.0.0.1:7318"
	req.RemoteAddr = "127.0.0.1:23111"
	req.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
	return req
}

func TestLocalSSOCallbackKeepsAttemptAfterIncompleteBrowserReturn(t *testing.T) {
	calls := 0
	p := newLocalSSORelyingParty(&http.Client{Transport: callbackContractRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("fictional unavailable"))}, nil
	})}, callbackContractIssuer, callbackContractClient, callbackContractApp)
	start := httptest.NewRecorder()
	p.ServeHTTP(start, callbackContractRequest("/start"))
	location, err := start.Result().Location()
	cookies := start.Result().Cookies()
	if err != nil || start.Code != http.StatusSeeOther || len(cookies) != 1 {
		t.Fatal("valid sample start did not create a bound attempt")
	}
	state := location.Query().Get("state")
	providerReply := url.Values{"state": {state}, "error": {"access_denied"}}
	boundCode := url.Values{"state": {state}, "code": {localSSOEntropy()}}
	for _, tc := range []struct {
		name       string
		query      url.Values
		withCookie bool
	}{
		{"ordinary provider denial rather than a code", providerReply, true},
		{"browser omitted its callback cookie", boundCode, false},
		{"provider returned a truncated code", url.Values{"state": {state}, "code": {"truncated"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := callbackContractRequest("/callback?" + tc.query.Encode())
			if tc.withCookie {
				req.AddCookie(cookies[0])
			}
			out := httptest.NewRecorder()
			p.ServeHTTP(out, req)
			if out.Code != http.StatusBadRequest || calls != 0 || len(p.pending) != 1 ||
				out.Header().Get("Set-Cookie") != "" || strings.Contains(out.Body.String(), "sign-in verified") {
				t.Fatal("incomplete return reached the provider, consumed its attempt, or appeared successful")
			}
		})
	}
	// A complete browser-bound return consumes the attempt once, even when the
	// upstream token endpoint is temporarily unavailable. Retrying needs a new start.
	for _, wantCalls := range []int{1, 1} {
		req := callbackContractRequest("/callback?" + boundCode.Encode())
		req.AddCookie(cookies[0])
		out := httptest.NewRecorder()
		p.ServeHTTP(out, req)
		if out.Code != http.StatusBadRequest || calls != wantCalls || len(p.pending) != 0 ||
			strings.Contains(out.Body.String(), "sign-in verified") {
			t.Fatal("complete callback state consumption or duplicate-return handling was incorrect")
		}
	}
}

func TestLocalSSOCallbackValidatesNativeTokenContract(t *testing.T) {
	signer, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("could not generate synthetic Cognito signing key")
	}
	nextSigner, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("could not generate synthetic rotation key")
	}
	id, nextID := localSSOEntropy(), localSSOEntropy()
	encode := base64.RawURLEncoding.EncodeToString
	jwk := func(id string, key *rsa.PrivateKey) map[string]string {
		return map[string]string{"kid": id, "alg": "RS256", "kty": "RSA", "use": "sig",
			"n": encode(key.N.Bytes()), "e": encode(big.NewInt(int64(key.E)).Bytes())}
	}
	jsonBody := func(v any) string {
		body, err := json.Marshal(v)
		if err != nil {
			t.Fatal("invalid fixture encoding")
		}
		return string(body)
	}
	usualJWK := jwk(id, signer)
	nextJWK := jwk(nextID, nextSigner)
	rotationKeys := jsonBody(map[string]any{"keys": []any{usualJWK, nextJWK}})
	for _, tc := range []struct {
		name       string
		change     func(jwt.MapClaims)
		token      string
		keys       string
		keyStatus  int
		wantStatus int
		wantCalls  int
	}{
		{"normally signed ID token", nil, "", rotationKeys, 200, 200, 2},
		{"expired ID token", func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, "", rotationKeys, 200, 400, 2},
		{"valid token for another sample app", func(c jwt.MapClaims) { c["aud"] = "another-synthetic-client" }, "", rotationKeys, 200, 400, 2},
		{"valid token for a previous nonce", func(c jwt.MapClaims) { c["nonce"] = localSSOEntropy() }, "", rotationKeys, 200, 400, 2},
		{"ordinary access token is not the identity token", func(c jwt.MapClaims) { c["token_use"] = "access" }, "", rotationKeys, 200, 400, 2},
		{"token reply missing identity field", nil, "{}", rotationKeys, 200, 400, 1},
		{"token response outside size budget", nil, strings.Repeat("x", localSSOLimit+1), rotationKeys, 200, 400, 1},
		{"JWKS endpoint unavailable", nil, "", "unavailable", 503, 400, 2},
		{"JWKS incomplete during rotation", nil, "", jsonBody(map[string]any{"keys": []any{nextJWK}}), 200, 400, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cookie *http.Cookie
			var state, nonce, challenge string
			code := localSSOEntropy()
			now := time.Now()
			calls := 0
			token := ""
			client := &http.Client{Transport: callbackContractRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				status, body := tc.keyStatus, tc.keys
				switch calls {
				case 1:
					if r.URL.String() != callbackContractIssuer+"/oauth2/token" || r.Method != http.MethodPost ||
						r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.ParseForm() != nil {
						t.Fatal("callback did not use the fixed token exchange")
					}
					hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
					if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("redirect_uri") != callbackContractApp+"/callback" ||
						r.Form.Get("client_id") != callbackContractClient || r.Form.Get("code") != code || encode(hash[:]) != challenge {
						t.Fatal("callback dropped or changed browser-flow exchange constraints")
					}
					status, body = http.StatusOK, tc.token
					if body == "" {
						c := jwt.MapClaims{"iss": callbackContractIssuer, "aud": callbackContractClient, "sub": "synthetic-subject",
							"nonce": nonce, "token_use": "id", "iat": now.Add(-2 * time.Minute).Unix(), "exp": now.Add(2 * time.Minute).Unix()}
						if tc.change != nil {
							tc.change(c)
						}
						tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
						tok.Header["kid"] = id
						var err error
						token, err = tok.SignedString(signer)
						if err != nil {
							t.Fatal("could not sign normal synthetic fixture token")
						}
						body = jsonBody(map[string]string{"id_token": token})
					}
				case 2:
					if r.Method != http.MethodGet || r.URL.String() != callbackContractIssuer+"/.well-known/jwks.json" {
						t.Fatal("callback did not use the fixed public keys endpoint")
					}
				default:
					t.Fatal("callback made an extra issuer request")
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			p := newLocalSSORelyingParty(client, callbackContractIssuer, callbackContractClient, callbackContractApp)
			first := httptest.NewRecorder()
			p.ServeHTTP(first, callbackContractRequest("/start"))
			authorize, err := first.Result().Location()
			cookies := first.Result().Cookies()
			if err != nil || first.Code != http.StatusSeeOther || len(cookies) != 1 {
				t.Fatal("start did not issue a browser-bound attempt")
			}
			cookie = cookies[0]
			q := authorize.Query()
			state, nonce, challenge = q.Get("state"), q.Get("nonce"), q.Get("code_challenge")
			req := callbackContractRequest("/callback?" + (url.Values{"state": {state}, "code": {code}}).Encode())
			req.AddCookie(cookie)
			out := httptest.NewRecorder()
			p.ServeHTTP(out, req)
			body := out.Body.String()
			clear := out.Result().Cookies()
			if out.Code != tc.wantStatus || calls != tc.wantCalls || len(p.pending) != 0 ||
				len(clear) != 1 || clear[0].Name != cookie.Name || clear[0].MaxAge >= 0 || clear[0].Path != "/callback" ||
				out.Header().Get("Cache-Control") != "no-store" || out.Header().Get("Location") != "" ||
				strings.Contains(body, "Local sign-in verified") != (tc.wantStatus == http.StatusOK) ||
				strings.Contains(body, "synthetic-subject") || strings.Contains(body, code) || (token != "" && strings.Contains(body, token)) {
				t.Fatalf("callback response crossed its contract (status %d, issuer calls %d)", out.Code, calls)
			}
		})
	}
}
