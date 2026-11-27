package main

import (
	"context"
	"crypto/tls"
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

func TestLocalSSOSlowEntraEntryAndExactAppExpiry(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		expired bool
	}{
		{"accept code entered at 105 seconds", false},
		{"refuse otherwise usable code at exact app deadline", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				checkLocalSSOBrowserTiming(t, scenario.expired)
			})
		})
	}
}

// The routes and signatures are production handlers. Only the transport is
// in-process, so the test can cross the real deadline with synctest's clock.
// Browser TLS, cookie policy and CSP need the separate real-browser witness.
func checkLocalSSOBrowserTiming(t *testing.T, expired bool) {
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
	pair, closeState, err := server.NewWithLocalCognitoHosted(filepath.Join(t.TempDir(), "state.sqlite"), options, cognito)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeState(); err != nil {
			t.Error("could not release synthetic SQLite state", err)
		}
	})
	secret := localSSOEntropy() + localSSOEntropy()
	sdk, err := server.NewAccess(pair.AWS, secret, false)
	if err != nil {
		t.Fatal(err)
	}
	tokenPosts := 0
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
			if target == cognito && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/oauth2/token") {
				tokenPosts++
			}
			out := httptest.NewRecorder()
			h.ServeHTTP(out, originForm)
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
	handlers[app] = newLocalSSORelyingParty(local, cognito+"/_simulator/cognito/"+p.UserPool.ID, enrollment.UserPoolClient.ClientID, app)
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
	if !expired {
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
