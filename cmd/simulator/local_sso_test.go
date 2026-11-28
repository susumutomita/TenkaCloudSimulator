package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLocalSSOEphemeralTrust(t *testing.T) {
	capem, pool, leaves, err := localSSOTLS(4, time.Now())
	if err != nil || len(leaves) != 4 {
		t.Fatal("ephemeral trust setup", err)
	}
	root, rest := pem.Decode(capem)
	if root == nil || root.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatal("only one public CA PEM block may be written")
	}
	seen := map[string]bool{}
	for _, leaf := range leaves {
		c, err := x509.ParseCertificate(leaf.Certificate[0])
		if err != nil || c.IsCA || seen[string(c.RawSubjectPublicKeyInfo)] {
			t.Fatal("each listener requires its own non-CA certificate")
		}
		seen[string(c.RawSubjectPublicKeyInfo)] = true
		if _, err = c.Verify(x509.VerifyOptions{Roots: pool, DNSName: "127.0.0.1"}); err != nil {
			t.Fatal("isolated trust does not verify exact loopback", err)
		}
		if _, err = c.Verify(x509.VerifyOptions{Roots: pool, DNSName: "example.invalid"}); err == nil {
			t.Fatal("per-run certificate valid for another host")
		}
	}
}

func TestLocalSSOSampleFixedOriginAndBrowserBinding(t *testing.T) {
	const app = "https://127.0.0.1:7318"
	const issuer = "https://127.0.0.1:7319/_simulator/cognito/us-east-1_example"
	p := newLocalSSORelyingParty(nil, issuer, "test-client", app)
	request := func(path string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = "127.0.0.1:7318"
		r.RemoteAddr = "127.0.0.1:6453"
		r.TLS = &tls.ConnectionState{}
		return r
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, request("/start"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("valid local start refused: %d", w.Code)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Scheme+"://"+u.Host+u.Path != issuer+"/oauth2/authorize" ||
		u.Query().Get("identity_provider") != "Entra" ||
		u.Query().Get("redirect_uri") != app+"/callback" ||
		u.Query().Get("code_challenge_method") != "S256" ||
		!localSSOOpaque(u.Query().Get("state")) || !localSSOOpaque(u.Query().Get("code_challenge")) ||
		!localSSOOpaque(u.Query().Get("nonce")) {
		t.Fatal("start redirect exceeded its exact local profile")
	}
	if cookies := w.Result().Cookies(); len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].Path != "/callback" {
		t.Fatal("browser binding cookie not scoped to the HTTPS callback")
	}
	for _, r := range []*http.Request{request("/callback"), request("/callback?state=" + u.Query().Get("state") + "&code=" + localSSOEntropy())} {
		w = httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "sign-in verified") {
			t.Fatal("unbound callback must refuse before exchange")
		}
	}
	r := request("/start")
	r.Host = "localhost:7318"
	w = httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("an alternate host must not enter the sample")
	}
}

func TestLocalSSOCleanupRetainsPrimaryAndSanitizesFailures(t *testing.T) {
	primary := errors.New("startup stage failed")
	sensitive := errors.New("temporary path or credential text")
	got := localSSOJoinCleanup(primary, sensitive, errLocalSSOCloseState)
	got = localSSOJoinCleanup(got, sensitive, errLocalSSORemoveState)
	for _, want := range []error{primary, errLocalSSOCloseState, errLocalSSORemoveState} {
		if !errors.Is(got, want) {
			t.Fatalf("lost public error: %v", want)
		}
	}
	if strings.Contains(got.Error(), sensitive.Error()) || errors.Is(got, sensitive) {
		t.Fatal("raw failure escaped the launcher boundary")
	}
	if localSSOJoinCleanup(primary, nil, errLocalSSOCloseState) != primary ||
		!errors.Is(localSSOJoinCleanup(nil, sensitive, errLocalSSORemoveState), errLocalSSORemoveState) {
		t.Fatal("incorrect success/no-primary teardown handling")
	}
}

func TestLocalSSOFailedStartReleasesRealBoundSocketsAndScratch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failFourth bool
		want       string
		count      int
	}{
		{"partial-bind", true, "cannot bind isolated local SSO listeners", 3},
		{"cancel-before-signed-bootstrap", false, "signed local Cognito CreateUserPool failed", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			t.Setenv("TMPDIR", parent)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var out bytes.Buffer
			type outcome struct {
				err       error
				addresses []string
			}
			result := make(chan outcome, 1)
			go func() {
				var addresses []string
				listen := func(network, address string) (net.Listener, error) {
					if network != "tcp4" || address != "127.0.0.1:0" {
						return nil, errors.New("nonlocal or non-ephemeral listener selection")
					}
					if tc.failFourth && len(addresses) == 3 {
						return nil, errors.New("forced fixture bind failure")
					}
					l, err := net.Listen(network, address)
					if err == nil {
						addresses = append(addresses, l.Addr().String())
						if !tc.failFourth && len(addresses) == 4 {
							cancel()
						}
					}
					return l, err
				}
				err := runLocalSSOWithListener(ctx, nil, &out, listen)
				result <- outcome{err, addresses}
			}()
			select {
			case got := <-result:
				if got.err == nil || got.err.Error() != tc.want || out.Len() != 0 || len(got.addresses) != tc.count {
					t.Fatalf("failed start did not terminate without readiness; error=%v, bound=%d", got.err, len(got.addresses))
				}
				for _, address := range got.addresses {
					l, err := net.Listen("tcp4", address)
					if err != nil {
						t.Fatal("failed start left a bound listener")
					}
					if err = l.Close(); err != nil {
						t.Fatal("fixture listener did not close", err)
					}
				}
				files, err := os.ReadDir(parent)
				if err != nil || len(files) != 0 {
					t.Fatal("failed start left private scratch")
				}
			case <-time.After(12 * time.Second):
				t.Fatal("failed startup did not finish within isolated test bound")
			}
		})
	}
}
