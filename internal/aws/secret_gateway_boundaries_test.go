package aws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestSecretGatewayProtocolSelectionAndAuthorityFailureFrames(t *testing.T) {
	db, secrets := secretTestStore(t)
	identities := core.NewIdentities(db)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"secretsmanager:*","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = identities.Set("secret-reader", policy); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Store: db, Secrets: secrets, Identities: identities}
	const body = `{"Name":"training/protocol","ClientRequestToken":"00000000-0000-0000-0000-000000000001","SecretString":"DUMMY-secret-protocol"}`
	request := func() *http.Request {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "secretsmanager.CreateSecret")
		return r
	}
	signed := func(r *http.Request, service, region string) {
		r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261104/"+region+"/"+service+"/aws4_request, SignedHeaders=host, Signature=dummy")
	}
	cases := []struct {
		name  string
		alter func(*http.Request)
		code  int
	}{
		{"signed missing target", func(r *http.Request) { r.Header.Del("X-Amz-Target"); signed(r, "secretsmanager", "us-east-1") }, 501},
		{"signed foreign target", func(r *http.Request) {
			r.Header.Set("X-Amz-Target", "AmazonSSM.PutParameter")
			signed(r, "secretsmanager", "us-east-1")
		}, 501},
		{"selector conflict", func(r *http.Request) { r.Header.Set("X-Simulator-Service", "s3") }, 400},
		{"signature service conflict", func(r *http.Request) { signed(r, "s3", "us-east-1") }, 403},
		{"signature region conflict", func(r *http.Request) {
			signed(r, "secretsmanager", "us-east-1")
			r.Header.Set("X-Simulator-Region", "us-west-2")
		}, 400},
		{"invalid region", func(r *http.Request) { r.Header.Set("X-Simulator-Region", "invalid-region") }, 400},
		{"missing identity", func(r *http.Request) { r.Header.Set("X-Simulator-Principal", "absent-secret-reader") }, 403},
		{"canceled identity lookup", func(r *http.Request) {
			r.Header.Set("X-Simulator-Principal", "secret-reader")
			ctx, cancel := context.WithCancel(r.Context())
			cancel()
			*r = *r.WithContext(ctx)
		}, 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := secretStateSnapshot(t, db)
			r := request()
			tc.alter(r)
			w := httptest.NewRecorder()
			gateway.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("want %d got %d", tc.code, w.Code)
			}
			if strings.Contains(w.Body.String(), "DUMMY-secret-") {
				t.Fatal("failure exposed value")
			}
			if before != secretStateSnapshot(t, db) {
				t.Fatal("rejected protocol/authority changed durable state")
			}
		})
	}
	// Real identity-table outage, not a mock authorizer: only the table name is
	// temporarily moved. Secret store stays live and its five tables must not move.
	before := secretStateSnapshot(t, db)
	if _, err = db.Database().Exec("ALTER TABLE identity_policies RENAME TO identity_policies_unavailable"); err != nil {
		t.Fatal(err)
	}
	r := request()
	r.Header.Set("X-Simulator-Principal", "secret-reader")
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, r)
	if _, err = db.Database().Exec("ALTER TABLE identity_policies_unavailable RENAME TO identity_policies"); err != nil {
		t.Fatal(err)
	}
	if w.Code != 500 || strings.Contains(w.Body.String(), "identity_policies") || strings.Contains(w.Body.String(), "DUMMY-secret-") {
		t.Fatal("authority outage did not return sanitized infrastructure error")
	}
	if before != secretStateSnapshot(t, db) {
		t.Fatal("authority outage mutated secret state")
	}
	// Same request succeeds after restoration: failures did not poison a token or
	// reserve a physical ARN. Native authentication remains an outer host concern.
	r = request()
	r.Header.Set("X-Simulator-Principal", "secret-reader")
	w = httptest.NewRecorder()
	gateway.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("restored identity authority did not permit clean retry", w.Code)
	}
}

func TestSecretHTTPRejectsDuplicateProtocolSelectors(t *testing.T) {
	db, s := secretTestStore(t)
	for _, name := range []string{"Content-Type", "X-Amz-Target", "X-Simulator-Service", "X-Simulator-Region", "Authorization", "X-Simulator-Principal", "X-Amz-Security-Token"} {
		t.Run(name, func(t *testing.T) {
			before := secretStateSnapshot(t, db)
			r := httptest.NewRequest("POST", "/", strings.NewReader(`{"Name":"duplicate-test","ClientRequestToken":"00000000-0000-0000-0000-000000000001","SecretString":"DUMMY-secret-duplicate"}`))
			r.Header.Set("Content-Type", "application/x-amz-json-1.1")
			r.Header.Set("X-Amz-Target", "secretsmanager.CreateSecret")
			value := r.Header.Get(name)
			if value == "" {
				value = "duplicate"
			}
			r.Header.Set(name, value)
			r.Header.Add(name, value)
			for _, throughGateway := range []bool{false, true} {
				w := httptest.NewRecorder()
				copy := r.Clone(r.Context())
				copy.Body = httptest.NewRequest("POST", "/", strings.NewReader(`{"Name":"duplicate-test","ClientRequestToken":"00000000-0000-0000-0000-000000000001","SecretString":"DUMMY-secret-duplicate"}`)).Body
				if throughGateway {
					serveSecretGateway(w, copy, s, core.NewIdentities(db))
				} else {
					s.ServeHTTP(w, copy, "us-east-1", func(string, string) error { return nil })
				}
				if w.Code != 400 {
					t.Fatal("ambiguous selector was accepted", name, w.Code)
				}
				if before != secretStateSnapshot(t, db) {
					t.Fatal("ambiguous selector changed state")
				}
			}
		})
	}
}
