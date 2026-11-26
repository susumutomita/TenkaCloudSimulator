package entra

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestEntraVerificationFixtureAdmission(t *testing.T) {
	address := netip.MustParseAddrPort("127.0.0.1:43210")
	f, err := NewVerificationFixture(address)
	if err != nil {
		t.Fatal(err)
	}
	for _, opt := range []string{"0.0.0.0:43210", "127.0.0.1:0", "[::1]:43210", "192.0.2.1:43210"} {
		if _, err := NewVerificationFixture(netip.MustParseAddrPort(opt)); err == nil {
			t.Fatalf("invalid listen %s accepted", opt)
		}
	}
	for _, test := range []struct {
		name, method, path, peer, host, body, header string
		code                                         int
	}{
		{name: "metadata", path: MetadataPath, code: 200},
		{name: "keys", path: KeysPath, code: 200},
		{name: "mismatched local host", path: KeysPath, host: "127.0.0.1:43211", code: 404},
		{name: "remote peer", path: KeysPath, peer: "192.0.2.1:51814", code: 404},
		{name: "encoded route", path: "/%5fsimulator/azure/entra/discovery/v2.0/keys", code: 404},
		{name: "query", path: KeysPath + "?value=synthetic", code: 404},
		{name: "absolute URI", path: "http://127.0.0.1:43210" + KeysPath, code: 404},
		{name: "forwarded host", path: KeysPath, header: "X-Forwarded-Host", code: 404},
		{name: "credential", path: KeysPath, header: "Authorization", code: 404},
		{name: "cookie", path: KeysPath, header: "Cookie", code: 404},
		{name: "head", method: "HEAD", path: KeysPath, code: 405},
		{name: "body", path: KeysPath, body: "synthetic", code: 400},
		{name: "no OIDC discovery misadvertisement", path: issuerPath + "/.well-known/openid-configuration", code: 404},
		{name: "no login", path: issuerPath + "/authorize", code: 404},
		{name: "no issuer endpoint", method: "POST", path: issuerPath + "/token", body: "synthetic", code: 404},
		{name: "no SAML", path: issuerPath + "/saml2", code: 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			method := test.method
			if method == "" {
				method = "GET"
			}
			r := httptest.NewRequest(method, test.path, strings.NewReader(test.body))
			r.Host = address.String()
			r.RemoteAddr = "127.0.0.1:10100"
			if test.host != "" {
				r.Host = test.host
			}
			if test.peer != "" {
				r.RemoteAddr = test.peer
			}
			if test.header != "" {
				r.Header.Set(test.header, "synthetic")
			}
			w := httptest.NewRecorder()
			f.ServeHTTP(w, r)
			if w.Code != test.code || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d, header %v", w.Code, w.Header())
			}
			if test.code != 200 && strings.Contains(w.Body.String(), "fixture_profile") {
				t.Fatal("rejected request exposed metadata")
			}
		})
	}
}

func TestEntraSyntheticTokenHasOnlyFixedClaims(t *testing.T) {
	f, err := NewVerificationFixture(netip.MustParseAddrPort("127.0.0.1:43210"))
	if err != nil {
		t.Fatal(err)
	}
	for _, nonce := range []string{"", "small", strings.Repeat("x", 97), "not-a-url-safe-nonce?"} {
		if token, err := f.TestIDToken(nonce); token != "" || err == nil {
			t.Fatal("invalid nonce accepted")
		}
	}
	const nonce = "one-local-transaction-nonce"
	compact, err := f.TestIDToken(nonce)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		t.Fatal("invalid compact token shape")
	}
	fields := func(part string) map[string]any {
		b, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(b, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	header, claims := fields(parts[0]), fields(parts[1])
	if len(header) != 3 || header["alg"] != "RS256" || header["typ"] != "JWT" || header["kid"] != f.keyID {
		t.Fatal("unexpected signature metadata")
	}
	if len(claims) != 6 || claims["iss"] != f.Metadata().Issuer || claims["aud"] != testAudience || claims["sub"] != testSubject ||
		claims["nonce"] != nonce || claims["exp"].(float64)-claims["iat"].(float64) != 60 {
		t.Fatal("unexpected claims or lifetime")
	}
}
