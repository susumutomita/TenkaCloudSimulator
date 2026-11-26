package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type entraReadinessOutput chan []byte

func (w entraReadinessOutput) Write(b []byte) (int, error) {
	w <- bytes.Clone(b)
	return len(b), nil
}

type entraFixtureReadiness struct {
	Profile     string `json:"fixture_profile"`
	Issuer      string `json:"issuer"`
	Audience    string `json:"expected_audience"`
	MetadataURL string `json:"verification_metadata_url"`
	JWKSURI     string `json:"jwks_uri"`
	Token       string `json:"test_id_token"`
}

func startEntraFixtureCommand(t *testing.T, args ...string) (entraFixtureReadiness, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	output := make(entraReadinessOutput, 1)
	exited := make(chan struct{})
	var runErr error
	go func() {
		defer close(exited)
		runErr = run(ctx, append([]string{entraFixtureCommand}, args...), output)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-exited:
				if runErr != nil {
					t.Errorf("fixture shutdown: %v", runErr)
				}
			case <-time.After(5 * time.Second):
				t.Error("fixture did not stop after cancellation")
			}
		})
	}
	t.Cleanup(stop)
	var ready entraFixtureReadiness
	select {
	case data := <-output:
		if err := json.Unmarshal(data, &ready); err != nil || ready.Profile == "" {
			t.Fatal("missing verified fixture readiness")
		}
	case <-exited:
		t.Fatalf("fixture exited before readiness: %v", runErr)
	case <-time.After(8 * time.Second):
		t.Fatal("fixture failed to start before deadline")
	}
	return ready, stop
}

type entraFixtureKey struct {
	KID string `json:"kid"`
	KTY string `json:"kty"`
	ALG string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func TestEntraFixtureCommandRoundTrip(t *testing.T) {
	const nonce = "one-local-transaction-nonce"
	ready, stop := startEntraFixtureCommand(t, "--test-id-token-nonce", nonce)
	issuer, err := url.Parse(ready.Issuer)
	if err != nil || issuer.Scheme != "http" || issuer.Hostname() != "127.0.0.1" || issuer.Port() == "" ||
		issuer.Path != "/_simulator/azure/entra/v2.0" ||
		ready.Audience != "urn:tenkacloud:simulator:test:entra" {
		t.Fatal("incorrect fixed synthetic issuer or audience")
	}
	origin := issuer.Scheme + "://" + issuer.Host
	if ready.MetadataURL != ready.Issuer+"/verification-metadata.json" ||
		ready.JWKSURI != origin+"/_simulator/azure/entra/discovery/v2.0/keys" {
		t.Fatal("advertised routes are not exact numeric loopback")
	}
	transport := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	get := func(method, path string, status int, dst any) {
		t.Helper()
		req, err := http.NewRequest(method, path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != status || res.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("response status=%d want %d", res.StatusCode, status)
		}
		if dst != nil {
			if err := json.NewDecoder(io.LimitReader(res.Body, 8192)).Decode(dst); err != nil {
				t.Fatal("invalid verification JSON")
			}
		}
	}
	var metadata entraFixtureReadiness
	get(http.MethodGet, ready.MetadataURL, http.StatusOK, &metadata)
	if metadata.Profile != ready.Profile || metadata.Issuer != ready.Issuer ||
		metadata.Audience != ready.Audience || metadata.JWKSURI != ready.JWKSURI || metadata.Token != "" {
		t.Fatal("wire metadata did not preserve fixed local trust or leaked a token")
	}
	readKey := func() entraFixtureKey {
		t.Helper()
		var set struct {
			Keys []entraFixtureKey `json:"keys"`
		}
		get(http.MethodGet, ready.JWKSURI, http.StatusOK, &set)
		if len(set.Keys) != 1 || set.Keys[0].KTY != "RSA" || set.Keys[0].ALG != "RS256" || set.Keys[0].Use != "sig" {
			t.Fatal("JWKS did not return exactly one public RS256 signing key")
		}
		return set.Keys[0]
	}
	decode := func(s string) []byte {
		t.Helper()
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal("expected base64url")
		}
		return b
	}
	first := readKey()
	parts := strings.Split(ready.Token, ".")
	if len(parts) != 3 {
		t.Fatal("explicit nonce did not produce compact synthetic token")
	}
	var header struct{ ALG, KID, TYP string }
	var claims struct {
		ISS, AUD, SUB, Nonce string
		IAT, EXP             int64
	}
	if json.Unmarshal(decode(parts[0]), &header) != nil || json.Unmarshal(decode(parts[1]), &claims) != nil ||
		header.ALG != "RS256" || header.TYP != "JWT" || header.KID != first.KID ||
		claims.ISS != ready.Issuer || claims.AUD != ready.Audience || claims.SUB != "synthetic-fixture-user" ||
		claims.Nonce != nonce || claims.EXP-claims.IAT != 60 || claims.EXP <= time.Now().Unix() {
		t.Fatal("unexpected fixed signature or claims")
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	verify := func(key entraFixtureKey) error {
		t.Helper()
		e := new(big.Int).SetBytes(decode(key.E))
		n := new(big.Int).SetBytes(decode(key.N))
		if e.Int64() != 65537 || n.BitLen() != 2048 {
			t.Fatal("unexpected public RSA key parameters")
		}
		return rsa.VerifyPKCS1v15(&rsa.PublicKey{N: n, E: int(e.Int64())}, crypto.SHA256, hash[:], decode(parts[2]))
	}
	if err := verify(first); err != nil {
		t.Fatal("local public key did not verify startup sample")
	}
	get(http.MethodPost, ready.Issuer+"/token", http.StatusNotFound, nil)
	get(http.MethodGet, origin+"/_simulator/health", http.StatusNotFound, nil)
	stop()

	// Rebind exactly the same authority after the real lifecycle shuts down.
	// The new process-scoped fixture must still rotate trust, even at one port.
	second, _ := startEntraFixtureCommand(t, "--listen", issuer.Host)
	if second.Issuer != ready.Issuer || second.Token != "" {
		t.Fatal("restart changed trust address or emitted a token without opt-in")
	}
	secondKey := readKey()
	if secondKey.KID == first.KID || verify(secondKey) == nil {
		t.Fatal("restart retained the old signing key")
	}
}

func TestEntraFixtureCommandRefusesUnsafeStartup(t *testing.T) {
	busy, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	for _, test := range []struct {
		name, reason string
		args         []string
	}{
		{"unsafe binding", "requires numeric IPv4 loopback", []string{"--listen", "0.0.0.0:0"}},
		{"unknown port name", "requires numeric IPv4 loopback", []string{"--listen", "localhost:0"}},
		{"ordinary gateway flag", "flag provided but not defined", []string{"--allow-remote"}},
		{"real issuer cannot be entered", "flag provided but not defined", []string{"--issuer", "example.invalid"}},
		{"positional target", "unexpected verification fixture arguments", []string{"example.invalid"}},
		{"explicit invalid nonce", "16–96 URL-safe", []string{"--test-id-token-nonce", ""}},
		{"owned occupied port", "address already in use", []string{"--listen", busy.Addr().String()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := run(ctx, append([]string{entraFixtureCommand}, test.args...), &out)
			if err == nil || !strings.Contains(err.Error(), test.reason) || strings.Contains(out.String(), "fixture_profile") {
				t.Fatalf("startup refusal not as expected: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := run(ctx, []string{entraFixtureCommand}, &out); err != context.Canceled || out.Len() != 0 {
		t.Fatal("cancelled startup was not a no-op")
	}
}
