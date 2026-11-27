//go:build cognito_hosted

package server

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestCognitoHostedMountLeavesNativeAndGenericAccessGuards(t *testing.T) {
	const origin = "https://127.0.0.1:4566"
	const peer = "127.0.0.1:34017"
	cfg := DefaultProviderConfig()
	cfg.CognitoLocalFederationConfig = true
	cfg.CognitoLocalHostedOrigin = origin
	h, close, err := NewWithProviders(filepath.Join(t.TempDir(), "hosted.sqlite"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = close() })
	if _, err := NewAccess(h, federationTestKey, true); err == nil {
		t.Fatal("remote hosted composition unexpectedly allowed")
	}
	a, err := NewAccess(h, federationTestKey, false)
	if err != nil {
		t.Fatal(err)
	}
	create := federationSignedRequest("CreateUserPool", `{"PoolName":"signed wire fixture"}`, "", federationRegion, federationService, peer, nil)
	var pooled struct{ UserPool struct{ ID string } }
	w := federationAssertResponse(t, a, create, 200, "")
	if json.Unmarshal(w.Body.Bytes(), &pooled) != nil || pooled.UserPool.ID == "" {
		t.Fatal("canonical native pool not created")
	}
	path := "/_simulator/cognito/" + pooled.UserPool.ID + "/saml2/metadata"
	call := func(h http.Handler, method, path, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Host = "127.0.0.1:4566"
		r.RemoteAddr = peer
		r.TLS = &tls.ConnectionState{}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if r := call(a, http.MethodGet, path, ""); r.Code != 200 || !strings.Contains(r.Body.String(), "urn:amazon:cognito:sp:"+pooled.UserPool.ID) {
		t.Fatalf("same canonical wire pool did not serve HTTPS metadata: %d", r.Code)
	}
	if r := call(h, http.MethodGet, path, ""); r.Code == 200 {
		t.Fatal("unwrapped composition exposed hosted endpoints")
	}
	for _, q := range []string{path + "/", path + "/extra", "/oauth2/authorize", "/_simulator/v1/iam/principals"} {
		if r := call(a, http.MethodGet, q, "https://127.0.0.1:4968"); r.Code != 403 {
			t.Fatalf("generic Origin protections changed for %s: %d", q, r.Code)
		}
	}
	if r := call(a, http.MethodPost, path, "https://127.0.0.1:4968"); r.Code != 403 {
		t.Fatal("wrong method crossed general Origin guard")
	}
	delete := federationSignedRequest("DeleteUserPool", `{"UserPoolId":"`+pooled.UserPool.ID+`"}`, "", federationRegion, federationService, peer, nil)
	federationAssertResponse(t, a, delete, 200, "")
	if r := call(a, http.MethodGet, path, ""); r.Code == 200 {
		t.Fatal("native DeleteUserPool left hosted metadata alive")
	}
}
