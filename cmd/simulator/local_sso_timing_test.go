package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/azure/entraidp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
)

type localSSOBrowserOutcome int

const (
	localSSOAcceptBrowser localSSOBrowserOutcome = iota
	localSSOExpireBrowser
	localSSOReopenCognito
)

func TestLocalSSOSlowEntraEntryAndExactAppExpiry(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		outcome localSSOBrowserOutcome
	}{
		{"accept code entered at 105 seconds", localSSOAcceptBrowser},
		{"refuse otherwise usable code at exact app deadline", localSSOExpireBrowser},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				checkLocalSSOBrowserTiming(t, scenario.outcome)
			})
		})
	}
}

func TestLocalSSOReopenedCognitoRequiresCurrentSigningKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		checkLocalSSOBrowserTiming(t, localSSOReopenCognito)
	})
}

// The routes and signatures are production handlers. Only the transport is
// in-process, so the test can cross the real deadline with synctest's clock.
// Browser TLS, cookie policy and CSP need the separate real-browser witness.
func checkLocalSSOBrowserTiming(t *testing.T, outcome localSSOBrowserOutcome) {
	t.Helper()
	const (
		private = "https://127.0.0.1:18451"
		cognito = "https://127.0.0.1:18452"
		entra   = "https://127.0.0.1:18453"
		app     = "https://127.0.0.1:18454"
	)
	ctx := t.Context()
	options := server.DefaultProviderConfig()
	options.CognitoLocalFederationConfig = true
	statePath := filepath.Join(t.TempDir(), "state.sqlite")
	pair, closeState, err := server.NewWithLocalCognitoHosted(statePath, options, cognito)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeState != nil {
			if err := closeState(); err != nil {
				t.Error("could not release synthetic SQLite state", err)
			}
		}
	})
	secret := localSSOEntropy() + localSSOEntropy()
	sdk, err := server.NewAccess(pair.AWS, secret, false)
	if err != nil {
		t.Fatal(err)
	}
	tokenPosts := 0
	var heldID string
	transport := func(handlers map[string]http.Handler) http.RoundTripper {
		return callbackContractRoundTrip(func(r *http.Request) (*http.Response, error) {
			target := r.URL.Scheme + "://" + r.URL.Host
			h := handlers[target]
			if h == nil || r.URL.User != nil || r.URL.Fragment != "" {
				return nil, errors.New("test refused an unregistered destination")
			}
			originForm := r.Clone(r.Context())
			originForm.URL.Scheme, originForm.URL.Host = "", ""
			originForm.Host, originForm.RemoteAddr = r.URL.Host, "127.0.0.1:23111"
			originForm.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
			tokenPost := target == cognito && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/oauth2/token")
			if tokenPost {
				tokenPosts++
			}
			out := httptest.NewRecorder()
			h.ServeHTTP(out, originForm)
			if outcome == localSSOReopenCognito && tokenPost && out.Code == http.StatusOK {
				if heldID != "" {
					return nil, errors.New("unexpected second successful token exchange")
				}
				var issued struct {
					ID string `json:"id_token"`
				}
				if err := json.Unmarshal(out.Body.Bytes(), &issued); err != nil || issued.ID == "" {
					return nil, errors.New("normal Cognito response did not include an identity token")
				}
				heldID = issued.ID
				// The app already has its real, newly issued token. Reopen
				// before its next JWKS read, without changing either response.
				stopOld := closeState
				closeState = nil
				if err := stopOld(); err != nil {
					return nil, errors.New("could not close original Cognito store")
				}
				reopened, closeNew, err := server.NewWithLocalCognitoHosted(statePath, options, cognito)
				if err != nil {
					return nil, errors.New("could not reopen the same Cognito store")
				}
				closeState = closeNew
				handlers[target] = reopened.Browser
			}
			return out.Result(), nil
		})
	}
	limitedRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	setup := &http.Client{Transport: transport(map[string]http.Handler{private: sdk}), CheckRedirect: limitedRedirect}
	native := func(operation string, args map[string]any, response any) {
		t.Helper()
		if err := localSSOAWS(ctx, setup, private, secret, operation, args, response); err != nil {
			t.Fatal(err)
		}
	}
	var p struct{ UserPool struct{ ID string } }
	native("CreateUserPool", map[string]any{"PoolName": "local timing fixture"}, &p)
	sp, err := pair.Browser.ServiceProvider(ctx, p.UserPool.ID)
	if err != nil {
		t.Fatal(err)
	}
	idp, code, err := entraidp.New(entraidp.Config{Origin: entra, ServiceProvider: entraidp.ServiceProvider{
		EntityID: sp.EntityID, ACS: sp.ACS, SigningCertificateDER: sp.SigningCertificateDER,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(idp.Close)
	handlers := map[string]http.Handler{cognito: pair.Browser, entra: idp}
	local := &http.Client{Transport: transport(handlers), CheckRedirect: limitedRedirect}
	metadata, err := localSSOGet(ctx, local, idp.Public().MetadataURL)
	if err != nil {
		t.Fatal(err)
	}
	native("CreateIdentityProvider", map[string]any{
		"UserPoolId": p.UserPool.ID, "ProviderName": "Entra", "ProviderType": "SAML",
		"ProviderDetails": map[string]string{"MetadataFile": string(metadata)},
	}, nil)
	var enrollment struct{ UserPoolClient struct{ ClientID string } }
	native("CreateUserPoolClient", map[string]any{
		"UserPoolId": p.UserPool.ID, "ClientName": "timing sample", "AllowedOAuthFlowsUserPoolClient": true,
		"AllowedOAuthFlows": []string{"code"}, "AllowedOAuthScopes": []string{"openid"},
		"CallbackURLs": []string{app + "/callback"}, "SupportedIdentityProviders": []string{"Entra"},
	}, &enrollment)
	issuer := cognito + "/_simulator/cognito/" + p.UserPool.ID
	appClient := enrollment.UserPoolClient.ClientID
	handlers[app] = newLocalSSORelyingParty(local, issuer, appClient, app)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Transport: transport(handlers), CheckRedirect: limitedRedirect, Jar: jar}
	call := func(method, target string, fields url.Values, want int) (string, http.Header) {
		t.Helper()
		var body io.Reader
		if fields != nil {
			body = strings.NewReader(fields.Encode())
		}
		r, err := http.NewRequestWithContext(ctx, method, target, body)
		if err != nil {
			t.Fatal("could not create fixture browser request")
		}
		if fields != nil {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", entra)
		}
		reply, err := browser.Do(r)
		if err != nil {
			t.Fatal("could not complete fixture browser request", err)
		}
		defer reply.Body.Close()
		wire, err := io.ReadAll(io.LimitReader(reply.Body, localSSOLimit+1))
		if err != nil || len(wire) > localSSOLimit || reply.StatusCode != want {
			t.Fatalf("unexpected fixture browser status %d, wanted %d", reply.StatusCode, want)
		}
		return string(wire), reply.Header
	}
	markup := func(page string, find func(string) []string) string {
		t.Helper()
		match := find(page)
		if len(match) != 2 {
			t.Fatal("expected form or callback link is absent")
		}
		return html.UnescapeString(match[1])
	}
	formFields := func(page string, names ...string) url.Values {
		t.Helper()
		fields := url.Values{}
		for _, m := range localSSOTestInput.FindAllStringSubmatch(page, -1) {
			fields.Add(m[1], html.UnescapeString(m[2]))
		}
		if len(fields) != len(names) {
			t.Fatal("unexpected browser form fields")
		}
		for _, name := range names {
			if len(fields[name]) != 1 || fields.Get(name) == "" {
				t.Fatal("missing or duplicate required browser form field")
			}
		}
		return fields
	}

	started := time.Now()
	_, start := call(http.MethodGet, app+"/start", nil, http.StatusSeeOther)
	cookies := (&http.Response{Header: start}).Cookies()
	if len(cookies) != 1 || cookies[0].Path != "/callback" || !cookies[0].Secure || !cookies[0].HttpOnly ||
		cookies[0].MaxAge != int(localSSOAttemptLifetime.Seconds()) {
		t.Fatal("application binding cookie does not match the guarded attempt window")
	}
	// Account for a nonzero app-to-signed-request handoff as well as the
	// person's time at the prompt. These timers are entirely virtual.
	<-time.After(2 * time.Second)
	_, authorize := call(http.MethodGet, start.Get("Location"), nil, http.StatusFound)
	prompt, _ := call(http.MethodGet, authorize.Get("Location"), nil, http.StatusOK)
	fields := formFields(prompt, "flow")
	fields.Set("code", code)
	<-time.After(time.Until(started.Add(105 * time.Second)))
	signed, _ := call(http.MethodPost, markup(prompt, localSSOTestForm.FindStringSubmatch), fields, http.StatusOK)
	completion, _ := call(http.MethodPost, markup(signed, localSSOTestForm.FindStringSubmatch), formFields(signed, "SAMLResponse", "RelayState"), http.StatusOK)
	callback := markup(completion, localSSOTestContinue.FindStringSubmatch)
	if outcome == localSSOReopenCognito {
		keysURL := issuer + "/.well-known/jwks.json"
		previousKeys, err := localSSOGet(ctx, local, keysURL)
		if err != nil {
			t.Fatal("could not read original published keys", err)
		}
		finished, _ := call(http.MethodGet, callback, nil, http.StatusBadRequest)
		currentKeys, err := localSSOGet(ctx, local, keysURL)
		var current struct{ Keys []struct{ Kid string } }
		auth, parseErr := url.Parse(start.Get("Location"))
		if err != nil || parseErr != nil || json.Unmarshal(currentKeys, &current) != nil || len(current.Keys) != 2 ||
			!localSSOOpaque(current.Keys[0].Kid) || !localSSOOpaque(current.Keys[1].Kid) ||
			current.Keys[0].Kid == current.Keys[1].Kid || bytes.Equal(previousKeys, currentKeys) {
			t.Fatal("same restored pool did not publish a distinct complete current key set")
		}
		nonce := auth.Query().Get("nonce")
		if tokenPosts != 1 || !localSSOVerifyJWT(previousKeys, heldID, issuer, appClient, nonce) ||
			localSSOVerifyJWT(currentKeys, heldID, issuer, appClient, nonce) ||
			strings.Contains(finished, "Local sign-in verified") || !strings.Contains(finished, "restart local-sso") {
			t.Fatal("prior signed token was not verified before reopen or was accepted by the restarted issuer")
		}
		checkLocalSSOUnusableKeySets(t, previousKeys, heldID, issuer, appClient, nonce)
		return
	}
	if outcome == localSSOAcceptBrowser {
		<-time.After(3 * time.Second)
		finished, _ := call(http.MethodGet, callback, nil, http.StatusOK)
		if tokenPosts != 1 || !strings.Contains(finished, "Local sign-in verified") {
			t.Fatal("slow, still-eligible browser login did not complete")
		}
		return
	}

	// Hold the genuine callback past the exact app deadline while Cognito's
	// code is still live (150 - 105 < 60s). Carry the originally issued
	// cookie to check the server's deadline independent of browser expiry.
	<-time.After(time.Until(started.Add(localSSOAttemptLifetime)))
	queued, err := http.NewRequestWithContext(context.Background(), http.MethodGet, callback, nil)
	if err != nil {
		t.Fatal("could not create the exact queued callback")
	}
	queued.AddCookie(cookies[0])
	refused, err := local.Do(queued)
	if err != nil {
		t.Fatal("could not check queued callback", err)
	}
	defer refused.Body.Close()
	body, err := io.ReadAll(io.LimitReader(refused.Body, 4096))
	if err != nil || refused.StatusCode != http.StatusBadRequest || tokenPosts != 0 ||
		strings.Contains(string(body), "Local sign-in verified") || !strings.Contains(string(body), "restart local-sso") {
		t.Fatal("expired application callback attempted redemption or gave no recovery direction")
	}
}

// Use the issuer's original keys and valid token. The relying party must
// reject a broken publication instead of trusting only part of the set.
func checkLocalSSOUnusableKeySets(t *testing.T, original []byte, id, issuer, clientID, nonce string) {
	t.Helper()
	parts := strings.Split(id, ".")
	if len(parts) != 3 {
		t.Fatal("fixture did not return a JWT")
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	var signed struct {
		KeyID string `json:"kid"`
	}
	if err != nil || json.Unmarshal(header, &signed) != nil || signed.KeyID == "" {
		t.Fatal("fixture JWT has no signing key")
	}
	for _, kind := range []string{"truncated document", "missing key", "invalid modulus", "invalid exponent", "duplicate key"} {
		var set struct {
			Keys []map[string]string `json:"keys"`
		}
		if err := json.Unmarshal(original, &set); err != nil || len(set.Keys) != 2 {
			t.Fatal("fixture did not issue a complete key set")
		}
		if set.Keys[1]["kid"] == signed.KeyID {
			set.Keys[0], set.Keys[1] = set.Keys[1], set.Keys[0]
		}
		if set.Keys[0]["kid"] != signed.KeyID {
			t.Fatal("fixture signing key was not published")
		}
		// Keep the genuine signing key intact in each incomplete-key case.
		switch kind {
		case "missing key":
			set.Keys = set.Keys[:1]
		case "invalid modulus":
			set.Keys[1]["n"] = "%"
		case "invalid exponent":
			set.Keys[1]["e"] = "%"
		case "duplicate key":
			set.Keys[1] = set.Keys[0]
		}
		wire, err := json.Marshal(set)
		if err != nil {
			t.Fatal("could not serialize fixture JWKS")
		}
		if kind == "truncated document" {
			wire = wire[:len(wire)-1]
		}
		if localSSOVerifyJWT(wire, id, issuer, clientID, nonce) {
			t.Errorf("app verified a token against an unusable key set (%s)", kind)
		}
	}
}
