package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/cognitoidp"
)

const (
	federationTarget  = "http://127.0.0.1:4566"
	federationRegion  = "us-east-1"
	federationService = "cognito-idp"
	fixtureCallback   = "http://127.0.0.1:18080/oauth/callback"
	fixtureSSO        = "http://127.0.0.1:18081/saml"
)

func nativeFederationMetadata(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(31), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="urn:tcsim:synthetic">
<IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol"><KeyDescriptor use="signing">
<KeyInfo xmlns="http://www.w3.org/2000/09/xmldsig#"><X509Data><X509Certificate>%s</X509Certificate></X509Data></KeyInfo>
</KeyDescriptor><SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="%s"/>
</IDPSSODescriptor></EntityDescriptor>`, base64.StdEncoding.EncodeToString(der), fixtureSSO)
}

func localFederationFixture(t *testing.T, database string, on bool) *Access {
	t.Helper()
	cfg := DefaultProviderConfig()
	cfg.CognitoLocalFederationConfig = on
	handler, done, err := NewWithProviders(database, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = done() })
	protected, err := NewAccess(handler, federationTestKey, false)
	if err != nil {
		t.Fatal(err)
	}
	return protected
}

const federationTestKey = "fixture-local-signature-only-not-an-aws-credential-18161321"

func federationSignedRequest(action, body, principal, region, service, remote string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, federationTarget+"/", strings.NewReader(body))
	r.RemoteAddr = remote
	r.Header.Set("Content-Type", cognitoidp.JSONMediaType)
	r.Header.Set("X-Amz-Target", cognitoidp.TargetPrefix+action)
	if principal != "" {
		r.Header.Set("X-Simulator-Principal", principal)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	if service != "" {
		signPoolFixture(federationTestKey, r, body, service, region)
	} else {
		r.Header.Set("X-Simulator-Token", federationTestKey)
	}
	return r
}

func federationWireBody(t *testing.T, fields map[string]any) string {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func federationAssertResponse(t *testing.T, h http.Handler, r *http.Request, status int, code string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status || w.Header().Get("X-Amzn-Errortype") != code ||
		w.Header().Get("Content-Type") != cognitoidp.JSONMediaType || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%s wanted %d/%s, got %d/%s (%s)", r.Header.Get("X-Amz-Target"), status, code,
			w.Code, w.Header().Get("X-Amzn-Errortype"), w.Header().Get("Content-Type"))
	}
	return w
}

func federationFixtureIAM(t *testing.T, a http.Handler, principal string, actions []string, arn string) {
	t.Helper()
	doc := federationWireBody(t, map[string]any{"Version": "2012-10-17", "Statement": []map[string]any{{"Effect": "Allow", "Action": actions, "Resource": arn}}})
	r := httptest.NewRequest(http.MethodPut, federationTarget+"/_simulator/v1/iam/principals/"+principal+"/policy", strings.NewReader(doc))
	r.Header.Set("X-Simulator-Token", federationTestKey)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("fixture IAM setup returned HTTP%d", w.Code)
	}
}

func TestCognitoFederationConfigSignedNativeLifecycle(t *testing.T) {
	const peer = "127.0.0.1:34017"
	database := filepath.Join(t.TempDir(), "signed.sqlite")
	native := localFederationFixture(t, database, true)
	call := func(h http.Handler, op, body, principal string, status int, code string) *httptest.ResponseRecorder {
		t.Helper()
		return federationAssertResponse(t, h, federationSignedRequest(op, body, principal, federationRegion, federationService, peer, nil), status, code)
	}
	pool := func(name string) (id, arn string) {
		t.Helper()
		got := call(native, "CreateUserPool", federationWireBody(t, map[string]any{"PoolName": name}), "", 200, "")
		var result struct{ UserPool struct{ ID, ARN string } }
		if json.Unmarshal(got.Body.Bytes(), &result) != nil || result.UserPool.ID == "" || result.UserPool.ARN == "" {
			t.Fatal("native pool not created")
		}
		return result.UserPool.ID, result.UserPool.ARN
	}
	id, arn := pool("native bounded fixture")
	other, _ := pool("sibling")
	metadata := nativeFederationMetadata(t)
	providerFields := map[string]any{"UserPoolId": id, "ProviderName": "Keycloak", "ProviderType": "SAML",
		"ProviderDetails": map[string]string{"MetadataFile": metadata}, "AttributeMapping": map[string]string{"email": "mail"}}
	registered := federationWireBody(t, providerFields)
	if len(registered) > cognitoidp.RequestLimit {
		t.Fatal("synthetic fixture exceeded unchanged native signature limit")
	}
	providerSelector := federationWireBody(t, map[string]any{"UserPoolId": id, "ProviderName": "Keycloak"})
	clientFields := map[string]any{"UserPoolId": id, "ClientName": "fixture web", "AllowedOAuthFlowsUserPoolClient": true,
		"AllowedOAuthFlows": []string{"code"}, "AllowedOAuthScopes": []string{"openid", "email"},
		"SupportedIdentityProviders": []string{"Keycloak"}, "CallbackURLs": []string{fixtureCallback}, "GenerateSecret": false}
	clientBody := federationWireBody(t, clientFields)

	capability := func(h http.Handler, enable bool) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, federationTarget+"/_simulator/v1/capabilities", nil)
		r.Header.Set("X-Simulator-Token", federationTestKey)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var manifest struct {
			Services map[string]struct {
				Operations                            []string
				LocalFederationConfiguration          map[string]any
				Console, Scalar, SignIn, MetadataOnly bool
			}
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &manifest) != nil {
			t.Fatal("invalid manifest response")
		}
		got := manifest.Services[federationService]
		want := []string{"CreateUserPool", "DescribeUserPool", "ListUserPools", "DeleteUserPool"}
		if enable {
			want = append(want, "CreateIdentityProvider", "DescribeIdentityProvider", "CreateUserPoolClient", "DescribeUserPoolClient")
		}
		if got.Console || got.Scalar || got.SignIn || !got.MetadataOnly || !slices.Equal(got.Operations, want) ||
			(got.LocalFederationConfiguration != nil) != enable {
			t.Fatal("capabilities claimed absent functionality or lied about opt-in")
		}
		if enable && got.LocalFederationConfiguration["requestBytes"] != float64(cognitoidp.RequestLimit) {
			t.Fatal("capabilities drifted from the verified request bound")
		}
	}
	capability(native, true)
	call(native, "CreateUserPoolClient", clientBody, "", 400, "ResourceNotFoundException")

	// Actual gateway checks the local exercise principal's exact IAM action and
	// ARN rather than granting the operator's authority to a selected exercise.
	federationFixtureIAM(t, native, "idp-owner", []string{"cognito-idp:CreateIdentityProvider", "cognito-idp:DescribeIdentityProvider"}, arn)
	federationFixtureIAM(t, native, "app-owner", []string{"cognito-idp:CreateUserPoolClient", "cognito-idp:DescribeUserPoolClient"}, arn)
	federationFixtureIAM(t, native, "wrong-owner", []string{"cognito-idp:DescribeIdentityProvider"}, strings.Replace(arn, ":000000000000:", ":012345678901:", 1))
	for _, unauthenticated := range []*http.Request{
		federationSignedRequest("CreateIdentityProvider", registered, "", federationRegion, "", peer, nil),
		federationSignedRequest("CreateIdentityProvider", registered, "", federationRegion, federationService, "192.0.2.15:34117", nil),
		federationSignedRequest("CreateIdentityProvider", registered, "", federationRegion, federationService, peer, map[string]string{"Origin": federationTarget}),
		federationSignedRequest("CreateIdentityProvider", registered, "", federationRegion, federationService, peer, map[string]string{"X-Forwarded-For": "127.0.0.1"}),
		federationSignedRequest("CreateIdentityProvider", registered+strings.Repeat(" ", cognitoidp.RequestLimit), "", federationRegion, federationService, peer, nil),
	} {
		w := federationAssertResponse(t, native, unauthenticated, 403, "UnrecognizedClientException")
		if w.Header().Get("X-Simulator-IAM-Decision") != "" {
			t.Fatal("mismatched transport reached policy evaluation")
		}
	}
	call(native, "CreateIdentityProvider", registered, "app-owner", 403, "AccessDeniedException")
	call(native, "DescribeIdentityProvider", providerSelector, "", 400, "ResourceNotFoundException")

	created := call(native, "CreateIdentityProvider", registered, "idp-owner", 200, "")
	if !strings.Contains(created.Body.String(), `"SSORedirectBindingURI":"`+fixtureSSO+`"`) ||
		strings.Contains(created.Body.String(), "X509Certificate") || strings.Contains(created.Body.String(), "MetadataFile") ||
		strings.Contains(created.Body.String(), "ClientId") {
		t.Fatal("provider projection was incomplete or exceeded selected resource")
	}
	call(native, "DescribeIdentityProvider", providerSelector, "wrong-owner", 403, "AccessDeniedException")
	call(native, "DescribeIdentityProvider", providerSelector, "app-owner", 403, "AccessDeniedException")
	if got := call(native, "DescribeIdentityProvider", providerSelector, "idp-owner", 200, ""); got.Body.String() != created.Body.String() {
		t.Fatal("provider did not round trip")
	}
	call(native, "CreateIdentityProvider", registered, "idp-owner", 400, "DuplicateProviderException")

	c := call(native, "CreateUserPoolClient", clientBody, "app-owner", 200, "")
	var result struct {
		UserPoolClient struct {
			ClientId, ClientName, UserPoolId string
			CallbackURLs                     []string
		}
	}
	if json.Unmarshal(c.Body.Bytes(), &result) != nil || len(result.UserPoolClient.ClientId) != 24 ||
		result.UserPoolClient.UserPoolId != id || !slices.Equal(result.UserPoolClient.CallbackURLs, []string{fixtureCallback}) ||
		strings.Contains(c.Body.String(), "ClientSecret") || strings.Contains(c.Body.String(), "IdentityProvider\":") {
		t.Fatal("unexpected client projection")
	}
	selector := federationWireBody(t, map[string]any{"UserPoolId": id, "ClientId": result.UserPoolClient.ClientId})
	call(native, "DescribeUserPoolClient", selector, "idp-owner", 403, "AccessDeniedException")
	if got := call(native, "DescribeUserPoolClient", selector, "app-owner", 200, ""); got.Body.String() != c.Body.String() {
		t.Fatal("client did not round trip")
	}
	call(native, "DescribeUserPoolClient", strings.Replace(selector, id, other, 1), "app-owner", 403, "AccessDeniedException")
	call(native, "DescribeUserPoolClient", strings.Replace(selector, id, other, 1), "", 400, "ResourceNotFoundException")
	badRegion := federationSignedRequest("DescribeUserPoolClient", selector, "", "us-west-2", federationService, peer, nil)
	federationAssertResponse(t, native, badRegion, 400, "InvalidParameterException")

	// Reopening the same file never implicitly enables the feature.
	disabled := localFederationFixture(t, database, false)
	capability(disabled, false)
	for _, op := range []string{"CreateIdentityProvider", "DescribeIdentityProvider", "CreateUserPoolClient", "DescribeUserPoolClient"} {
		call(disabled, op, `{}`, "", 400, "UnknownOperationException")
	}
	fresh := localFederationFixture(t, database, true)
	call(fresh, "DescribeIdentityProvider", providerSelector, "", 200, "")
	call(fresh, "DescribeUserPoolClient", selector, "", 200, "")
	body := federationWireBody(t, map[string]any{"UserPoolId": id})
	call(fresh, "DeleteUserPool", body, "", 200, "")
	call(fresh, "DescribeIdentityProvider", providerSelector, "", 400, "ResourceNotFoundException")
	call(fresh, "DescribeUserPoolClient", selector, "", 400, "ResourceNotFoundException")
	for _, op := range []string{"DeleteIdentityProvider", "ListUserPoolClients", "UpdateUserPoolClient", "InitiateAuth", "CreateUserPoolDomain"} {
		call(fresh, op, `{}`, "", 400, "UnknownOperationException")
	}
}
