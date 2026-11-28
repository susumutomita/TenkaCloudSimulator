package cognitoidp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestHostedEnrollmentRequiresCurrentMetadataAtPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, s := newCognitoRecoveryFixture(t)
		ctx := t.Context()
		reader := federationPublisherFor(t, s, f.scope, federationAllow)
		_, revision, err := reader.configuration(ctx, f.poolID)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		fresh := f.idp.provider("Entra")
		parsed, err := compileFederationProvider(fresh, now)
		if err != nil {
			t.Fatal("fresh metadata not usable", err)
		}
		expires := now.Add(2 * time.Second).Truncate(time.Second)
		short := strings.Replace(fresh.MetadataFile, parsed.MetadataExpires.Format(time.RFC3339), expires.Format(time.RFC3339), 1)
		parsed, err = compileFederationProvider(federationProviderInput{Name: "Entra", MetadataFile: short}, now)
		if err != nil || !parsed.MetadataExpires.Equal(expires) {
			t.Fatal("fixture does not offer a currently usable, short metadata window", err)
		}

		approvals := 0
		handler := NewHTTPHandler(s, f.scope, Actor{"fixture-operator", "test-exercise"}, func(_ context.Context, action, arn string) bool {
			approvals++
			if approvals == 1 {
				if !time.Now().Before(expires) {
					t.Fatal("metadata had already expired before authorization")
				}
				// An operator's authorized approval is still valid; the
				// independent publication deadline can pass while waiting.
				<-time.After(time.Until(expires))
			}
			return action == createFederationProvider && arn == f.scope.arn(f.poolID)
		})
		enroll := func(metadata string, status int, errType string) {
			t.Helper()
			wire, err := json.Marshal(map[string]any{"UserPoolId": f.poolID, "ProviderName": "Entra",
				"ProviderType": "SAML", "ProviderDetails": map[string]string{"MetadataFile": metadata}})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:4566/", bytes.NewReader(wire))
			req.RemoteAddr = "127.0.0.1:4567"
			req.Header.Set("Content-Type", JSONMediaType)
			req.Header.Set("X-Amz-Target", TargetPrefix+nativeCreateIdentityProvider)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != status || w.Header().Get("X-Amzn-Errortype") != errType {
				t.Fatalf("enrollment status = %d/%s, want %d/%s", w.Code, w.Header().Get("X-Amzn-Errortype"), status, errType)
			}
		}
		enroll(short, http.StatusBadRequest, "InvalidParameterException")
		after, unchanged, err := reader.configuration(ctx, f.poolID)
		if err != nil || approvals != 1 || unchanged != revision || len(after.Providers) != 0 {
			t.Fatal("metadata that expired pending approval was published", err)
		}

		enroll(fresh.MetadataFile, http.StatusOK, "")
		f.native(t, s, nativeDescribeIdentityProvider, map[string]any{"ProviderName": "Entra"}, http.StatusOK)
		client := f.addClient(t, s, "renewed app", http.StatusOK)
		browser := f.browser(t, s)
		login, completed := f.complete(t, browser, client)
		redeemHostedSignedLogin(t, browser, login, completed)
	})
}

func TestHostedOlderSQLiteSnapshotRequiresReenrollingMissingApp(t *testing.T) {
	f, s := newCognitoRecoveryFixture(t)
	f.addProvider(t, s, http.StatusOK)
	retained := f.addClient(t, s, "app in the backup", http.StatusOK)
	browser := f.browser(t, s)
	login, completed := f.complete(t, browser, retained)
	redeemHostedSignedLogin(t, browser, login, completed)
	before := f.slots(t, s.db)
	f.dismiss(t, s.db)
	snapshot, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal("could not take an offline copy of the owned SQLite fixture", err)
	}

	s = f.openWritable(t)
	missing := f.addClient(t, s, "app added after backup", http.StatusOK)
	browser = f.browser(t, s)
	params := url.Values{"response_type": {"code"}, "client_id": {missing}, "redirect_uri": {hostedFixtureCallback},
		"scope": {"openid"}, "state": {fixtureHostedState}, "nonce": {fixtureHostedNonce}, "identity_provider": {"Entra"},
		"code_challenge": {hostedDigest(strings.Repeat("v", 43))}, "code_challenge_method": {"S256"}}
	address := hostedPrefix + f.poolID + "/" + hostedAuthorize + "?" + params.Encode()
	authorize := func(b *hostedBrowser) *httptest.ResponseRecorder {
		return hostedServe(b, hostedFixtureRequest(http.MethodGet, address, nil))
	}
	if w := authorize(browser); w.Code != http.StatusFound || len(w.Result().Cookies()) != 1 {
		t.Fatal("later registration did not yield a usable bookmark")
	}
	f.dismiss(t, s.db)

	// Both handles are closed. Restore the whole known-good SQLite image,
	// rather than modifying records in a running provider.
	if err := os.WriteFile(f.path, snapshot, 0600); err != nil {
		t.Fatal("could not restore the owned SQLite fixture", err)
	}
	s = f.openWritable(t)
	browser = f.browser(t, s)
	f.native(t, s, nativeDescribeUserPoolClient, map[string]any{"ClientId": retained}, http.StatusOK)
	w := f.native(t, s, nativeDescribeUserPoolClient, map[string]any{"ClientId": missing}, http.StatusBadRequest)
	if w.Header().Get("X-Amzn-Errortype") != "ResourceNotFoundException" {
		t.Fatal("older snapshot did not distinguish the missing app registration")
	}
	requireNoHostedResult(t, authorize(browser))
	if after := f.slots(t, s.db); !bytes.Equal(after[0], before[0]) || !bytes.Equal(after[1], before[1]) {
		t.Fatal("using an old bookmark changed the restored pool")
	}

	replacement := f.addClient(t, s, "app added after backup", http.StatusOK)
	if replacement == missing {
		t.Fatal("re-enrollment reused the discarded app ID")
	}
	login, completed = f.complete(t, browser, replacement)
	redeemHostedSignedLogin(t, browser, login, completed)
	requireNoHostedResult(t, authorize(browser))
}
