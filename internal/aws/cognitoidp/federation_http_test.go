package cognitoidp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalFederationNativeShapeAndScopeBeforeStorage(t *testing.T) {
	_, db := poolFixture(t)
	store, err := NewStoreWithLocalFederationConfig(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{"012345678901", "us-east-1"}
	peer := Scope{"012345678902", scope.Region}
	id := scope.Region + "_fixture"
	seedSameKey(t, store, scope, id)
	hits := 0
	authorize := func(_ context.Context, action, resource string) bool {
		hits++
		return resource == scope.arn(id)
	}
	actor := Actor{"native-test", "fixture"}
	own := NewHTTPHandler(store, scope, actor, authorize)
	call := func(h http.Handler, action, raw string, status int, code string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader(raw))
		r.RemoteAddr = "127.0.0.1:40552"
		r.Header.Set("Content-Type", JSONMediaType)
		r.Header.Set("X-Amz-Target", TargetPrefix+action)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status || w.Header().Get("X-Amzn-Errortype") != code || w.Header().Get("Content-Type") != JSONMediaType {
			t.Fatalf("%s: unexpected response %d/%s", action, w.Code, w.Header().Get("X-Amzn-Errortype"))
		}
		return w
	}
	jsonText := func(fields map[string]any) string {
		t.Helper()
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	makeProvider := func(raw string) string {
		return jsonText(map[string]any{"UserPoolId": id, "ProviderName": "Keycloak", "ProviderType": "SAML", "ProviderDetails": map[string]string{"MetadataFile": raw}})
	}
	xml := federationProviderFixture(t, "Keycloak").MetadataFile
	p := makeProvider(xml)
	c := jsonText(map[string]any{
		"UserPoolId": id, "ClientName": "fixture", "AllowedOAuthFlowsUserPoolClient": true, "AllowedOAuthFlows": []string{"code"},
		"AllowedOAuthScopes": []string{"openid"}, "SupportedIdentityProviders": []string{"Keycloak"}, "CallbackURLs": []string{fixtureCallback},
	})
	lookup := jsonText(map[string]any{"UserPoolId": id, "ProviderName": "Keycloak"})
	foreign := jsonText(map[string]any{"UserPoolId": id, "ProviderName": "Keycloak", "Account": peer.Account})
	for _, test := range []struct{ name, op, input string }{
		{"metadata endpoint not local", nativeCreateIdentityProvider, makeProvider(strings.Replace(xml, fixtureIdPURL, "http://192.0.2.2:18081/sso", 1))},
		{"metadata URL", nativeCreateIdentityProvider, strings.Replace(p, "MetadataFile", "MetadataURL", 1)},
		{"invalid provider type", nativeCreateIdentityProvider, strings.Replace(p, `"ProviderType":"SAML"`, `"ProviderType":"OIDC"`, 1)},
		{"unknown request key", nativeDescribeIdentityProvider, foreign},
		{"ambiguous top-level field", nativeCreateIdentityProvider, `{"UserPoolId":"` + id + `",` + p[1:]},
		{"ambiguous nested field", nativeCreateIdentityProvider, strings.Replace(p, `"ProviderDetails":{`, `"ProviderDetails":{"MetadataFile":"synthetic",`, 1)},
		{"missing required value", nativeCreateIdentityProvider, strings.Replace(p, `"ProviderDetails":{`, `"OtherDetails":{`, 1)},
		{"null optional mapping", nativeCreateIdentityProvider, `{"AttributeMapping":null,` + p[1:]},
		{"ambiguous mapping", nativeCreateIdentityProvider, `{"AttributeMapping":{"email":"a","email":"b"},` + p[1:]},
		{"enabled client secret", nativeCreateUserPoolClient, `{"GenerateSecret":true,` + c[1:]},
		{"null client secret selector", nativeCreateUserPoolClient, `{"GenerateSecret":null,` + c[1:]},
		{"missing exact permission fields", nativeCreateUserPoolClient, strings.Replace(c, `"AllowedOAuthFlowsUserPoolClient":true,`, "", 1)},
		{"unsupported OAuth flow", nativeCreateUserPoolClient, strings.Replace(c, `["code"]`, `["implicit"]`, 1)},
		{"invalid client selector", nativeDescribeUserPoolClient, `{"ClientId":"whatever","UserPoolId":"` + id + `"}`},
		{"case-mismatched selector", nativeDescribeIdentityProvider, strings.Replace(lookup, "ProviderName", "providerName", 1)},
		{"extra JSON", nativeDescribeIdentityProvider, lookup + ` {}`},
		{"oversize", nativeCreateIdentityProvider, makeProvider(strings.Repeat("a", RequestLimit))},
	} {
		t.Run(test.name, func(t *testing.T) {
			call(own, test.op, test.input, 400, "InvalidParameterException")
		})
	}
	var column bool
	if err := store.db.QueryRow("SELECT EXISTS(SELECT 1 FROM pragma_table_info('aws_cognito_user_pools') WHERE name='federation_state')").Scan(&column); err != nil || column || hits != 0 {
		t.Fatal("wire admission touched policy/storage on malformed input", err)
	}
	call(own, nativeCreateIdentityProvider, p, 200, "")
	call(own, nativeCreateUserPoolClient, c, 200, "")
	seedSameKey(t, store, peer, id)
	peerHandler := NewHTTPHandler(store, peer, actor, func(_ context.Context, action, resource string) bool { return resource == peer.arn(id) })
	call(peerHandler, nativeDescribeIdentityProvider, lookup, 400, "ResourceNotFoundException")
	// A valid peer may use the same provider label independently; it cannot
	// alias the first account's canonical data.
	call(peerHandler, nativeCreateIdentityProvider, p, 200, "")
	call(own, nativeDescribeIdentityProvider, lookup, 200, "")
}

func TestDirectFederationSocketIdentity(t *testing.T) {
	for _, row := range []struct {
		host, peer string
		ok         bool
	}{
		{"127.0.0.1", "127.0.0.1:14520", true},
		{"127.0.0.1:4566", "127.0.0.1:14520", true},
		{"[::1]", "[::1]:14520", true},
		{"[::1]:4566", "[::1]:14520", true},
		{"localhost:4566", "127.0.0.1:14520", false},
		{"192.0.2.1:4566", "127.0.0.1:14520", false},
		{"127.0.0.1:4566", "192.0.2.1:14520", false},
		{"[127.0.0.1]", "127.0.0.1:14520", false},
		{"127.0.0.1:0", "127.0.0.1:14520", false},
		{"127.0.0.1", "127.0.0.1:0", false},
		{"127.0.0.1", "loopback:14520", false},
		{"[::1%loop]:4566", "[::1]:14520", false},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", nil)
		r.Host, r.RemoteAddr = row.host, row.peer
		if got := directLocalFederationRequest(r); got != row.ok {
			t.Fatalf("unexpected direct peer admissibility (%s,%s)", row.host, row.peer)
		}
	}
}
