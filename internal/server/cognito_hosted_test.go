package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/cognitoidp"
)

func TestCognitoHostedSeparateListenersShareCanonicalPool(t *testing.T) {
	tlsListener := httptest.NewUnstartedServer(nil)
	defer tlsListener.Close()
	origin := "https://" + tlsListener.Listener.Addr().String()
	options := DefaultProviderConfig()
	options.CognitoLocalFederationConfig = true
	pair, closeState, err := NewWithLocalCognitoHosted(filepath.Join(t.TempDir(), "pool.sqlite"), options, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeState(); err != nil {
			t.Error(err)
		}
	}()
	protected, err := NewAccess(pair.AWS, federationTestKey, false)
	if err != nil {
		t.Fatal(err)
	}
	primary := httptest.NewServer(protected)
	defer primary.Close()
	tlsListener.Config.Handler = pair.Browser
	tlsListener.StartTLS()
	defer tlsListener.Close() // shut down both listeners before SQLite
	if tlsListener.URL != origin || primary.URL == origin || !strings.HasPrefix(primary.URL, "http://") {
		t.Fatal("primary and hosted browser listener must have separate origins and transports")
	}
	primary.Client().Timeout = 3 * time.Second
	tlsListener.Client().Timeout = 3 * time.Second

	call := func(client *http.Client, req *http.Request, status int) []byte {
		t.Helper()
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != status {
			t.Fatalf("%s %s wanted %d, got %d (%s)", req.Method, req.URL.Path, status, resp.StatusCode, raw)
		}
		return raw
	}
	request := func(method, url, body string) *http.Request {
		t.Helper()
		req, err := http.NewRequest(method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	signed := func(origin, op, body string) *http.Request {
		t.Helper()
		req := request(http.MethodPost, origin+"/", body)
		req.Header.Set("Content-Type", cognitoidp.JSONMediaType)
		req.Header.Set("X-Amz-Target", cognitoidp.TargetPrefix+op)
		signPoolFixture(federationTestKey, req, body, federationService, federationRegion)
		return req
	}
	raw := call(primary.Client(), signed(primary.URL, "CreateUserPool", `{"PoolName":"local hosted fixture"}`), http.StatusOK)
	var pool struct{ UserPool struct{ ID, ARN string } }
	if err := json.Unmarshal(raw, &pool); err != nil || pool.UserPool.ID == "" ||
		pool.UserPool.ARN != "arn:aws:cognito-idp:"+federationRegion+":"+localCognitoAccount+":userpool/"+pool.UserPool.ID {
		t.Fatal("native AWS endpoint did not return the canonical pool")
	}
	id := pool.UserPool.ID
	ctx := context.Background()
	sp, err := pair.Browser.ServiceProvider(ctx, id)
	if err != nil || sp.EntityID != "urn:amazon:cognito:sp:"+id || sp.ACS != origin+"/_simulator/cognito/"+id+"/saml2/idpresponse" ||
		sp.MetadataURL != origin+"/_simulator/cognito/"+id+"/saml2/metadata" || len(sp.SigningCertificateDER) == 0 {
		t.Fatal("trusted bootstrap did not describe that exact live SAML SP", err)
	}
	meta := call(tlsListener.Client(), request(http.MethodGet, sp.MetadataURL, ""), http.StatusOK)
	var doc struct {
		EntityID   string `xml:"entityID,attr"`
		Descriptor struct {
			Signed bool `xml:"AuthnRequestsSigned,attr"`
			ACS    struct {
				Location string `xml:"Location,attr"`
			} `xml:"AssertionConsumerService"`
			Keys []struct {
				Use  string `xml:"use,attr"`
				Cert string `xml:"KeyInfo>X509Data>X509Certificate"`
			} `xml:"KeyDescriptor"`
		} `xml:"SPSSODescriptor"`
	}
	if err := xml.Unmarshal(meta, &doc); err != nil || doc.EntityID != sp.EntityID || doc.Descriptor.ACS.Location != sp.ACS ||
		!doc.Descriptor.Signed || len(doc.Descriptor.Keys) != 1 || doc.Descriptor.Keys[0].Use != "signing" {
		t.Fatal("wire SP metadata does not match bootstrap projection", err)
	}
	der, err := base64.StdEncoding.DecodeString(doc.Descriptor.Keys[0].Cert)
	if err != nil || !bytes.Equal(der, sp.SigningCertificateDER) {
		t.Fatal("SP metadata and bootstrap use different signing certificates")
	}
	sp.SigningCertificateDER[0] ^= 0xff
	again, err := pair.Browser.ServiceProvider(ctx, id)
	if err != nil || !bytes.Equal(der, again.SigningCertificateDER) {
		t.Fatal("public certificate projection aliases the private handler state")
	}

	// Nothing on the ordinary HTTP listener routes to the browser handler.
	browserOnPrimary := request(http.MethodGet, primary.URL+strings.TrimPrefix(sp.MetadataURL, origin), "")
	browserOnPrimary.Header.Set("X-Simulator-Token", federationTestKey)
	call(primary.Client(), browserOnPrimary, http.StatusNotFound)
	// Nothing on the browser-only TLS listener routes to SDK, console or IAM.
	call(tlsListener.Client(), signed(origin, "DescribeUserPool", `{"UserPoolId":"`+id+`"}`), http.StatusNotFound)
	for _, path := range []string{"/_simulator/console/", "/_simulator/v1/iam/principals", "/oauth2/authorize"} {
		call(tlsListener.Client(), request(http.MethodGet, origin+path, ""), http.StatusNotFound)
	}

	// The browser TLS origin is not an exception to the primary Access guards.
	iamURL := primary.URL + "/_simulator/v1/iam/principals"
	iam := request(http.MethodGet, iamURL, "")
	iam.Header.Set("X-Simulator-Token", federationTestKey)
	call(primary.Client(), iam, http.StatusOK)
	iam = request(http.MethodGet, iamURL, "")
	iam.Header.Set("X-Simulator-Token", federationTestKey)
	iam.Header.Set("Origin", origin)
	call(primary.Client(), iam, http.StatusForbidden)

	call(primary.Client(), signed(primary.URL, "DeleteUserPool", `{"UserPoolId":"`+id+`"}`), http.StatusOK)
	call(tlsListener.Client(), request(http.MethodGet, sp.MetadataURL, ""), http.StatusBadRequest)
	if _, err := pair.Browser.ServiceProvider(ctx, id); err == nil {
		t.Fatal("deleted native pool remains available for browser bootstrap")
	}
}

func TestCognitoHostedRejectsInvalidConfigurationBeforeOpeningState(t *testing.T) {
	for _, tc := range []struct {
		name, origin string
		enabled      bool
	}{
		{"disabled", "https://127.0.0.1:1443", false},
		{"http", "http://127.0.0.1:1443", true},
		{"hostname", "https://localhost:1443", true},
		{"public", "https://192.0.2.1:1443", true},
		{"noport", "https://127.0.0.1", true},
		{"path", "https://127.0.0.1:1443/saml2", true},
		{"userinfo", "https://example@127.0.0.1:1443", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "should-not-open.sqlite")
			options := DefaultProviderConfig()
			options.CognitoLocalFederationConfig = tc.enabled
			pair, done, err := NewWithLocalCognitoHosted(path, options, tc.origin)
			if err == nil || done != nil || pair.AWS != nil || pair.Browser != nil {
				t.Fatal("invalid local profile returned an active listener handler")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("invalid local profile touched native state")
			}
		})
	}
}
