package cognitoidp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFederationStoredConfigRejectsInconsistentProjection(t *testing.T) {
	owner := federationOwner{Scope{"012345678901", "us-east-1"}, "us-east-1_fixture"}
	provider, err := compileFederationProvider(federationProviderFixture(t, "Keycloak"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	original := federationConfig{Version: federationConfigVersion, Owner: owner,
		Providers: []federationProvider{provider}, Clients: []federationClient{{strings.Repeat("a", 24), federationInput()}}}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := readFederationConfig(encoded, owner); err != nil || len(got.Clients) != 1 || len(got.Providers) != 1 {
		t.Fatal("fixture did not admit its canonical projection", err)
	}
	if _, err := readFederationConfig(append([]byte(" "), encoded...), owner); !errors.Is(err, errFederationState) {
		t.Fatal("accepted noncanonical persisted data", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*federationConfig)
	}{
		{"duplicate provider", func(c *federationConfig) { c.Providers = append(c.Providers, c.Providers[0]) }},
		{"duplicate client", func(c *federationConfig) { c.Clients = append(c.Clients, c.Clients[0]) }},
		{"orphaned client", func(c *federationConfig) { c.Providers = nil }},
		{"nonlocal saved destination", func(c *federationConfig) { c.Providers[0].RedirectURL = "http://localhost:18081/sso" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := readFederationConfig(encoded, owner)
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(&cfg)
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := readFederationConfig(raw, owner); !errors.Is(err, errFederationState) || got.Version != 0 {
				t.Fatal("inconsistent slot returned a typed projection", err)
			}
		})
	}
}

func TestFederationPublisherDeclinesUnsupportedSelectionAndClaims(t *testing.T) {
	s, _ := poolFixture(t)
	scope := Scope{"012345678901", "us-east-1"}
	ctx := t.Context()
	row, err := s.create(ctx, scope, "boundary fixture")
	if err != nil {
		t.Fatal(err)
	}
	id := row.ID
	p := federationPublisherFor(t, s, scope, federationAllow)
	provider := federationProviderFixture(t, "Keycloak")
	provider.Mapping = nil
	if _, err := p.addProvider(ctx, id, provider); err != nil {
		t.Fatal(err)
	}
	if err := p.removeProvider(ctx, id, "Entra"); !errors.Is(err, errFederationNotFound) {
		t.Fatal("missing provider removal did not return not found", err)
	}
	if err := p.removeProvider(ctx, id, "unrecognized"); !errors.Is(err, errInvalid) {
		t.Fatal("invalid provider removal accepted", err)
	}
	if err := p.removeClient(ctx, id, strings.Repeat("a", 24)); !errors.Is(err, errFederationNotFound) {
		t.Fatal("missing client removal did not return not found", err)
	}
	if err := p.removeClient(ctx, id, strings.Repeat("z", 24)); !errors.Is(err, errInvalid) {
		t.Fatal("nonhexadecimal client selector accepted", err)
	}

	client, err := p.addClient(ctx, id, federationInput())
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range []federationSelection{{provider: "Keycloak", client: client.ID}, {client: "invalid"}} {
		if got, rev, err := p.readConfiguration(ctx, id, selection); !errors.Is(err, errInvalid) || got.Version != 0 || rev != (federationRevision{}) {
			t.Fatal("unsupported selector produced configuration", err)
		}
	}
	request := federationBindingRequest{client.ID, "Keycloak", fixtureCallback, federationPKCE, []string{"openid"}}
	accepted, err := p.inspectBinding(ctx, id, request, nil)
	if err != nil {
		t.Fatal("subject-only binding did not succeed", err)
	}
	for _, scope := range []string{"email", "profile"} {
		withClaim := request
		withClaim.Scopes = []string{"openid", scope}
		if got, err := p.inspectBinding(ctx, id, withClaim, &accepted.Revision); !errors.Is(err, errFederationNotFound) || got.ClientID != "" {
			t.Fatal("provider without the required claim mapping admitted extra scope", err)
		}
	}
	badRequest := request
	badRequest.Scopes = []string{"openid", "email", "profile", "unavailable"}
	if _, err := p.inspectBinding(ctx, id, badRequest, nil); !errors.Is(err, errInvalid) {
		t.Fatal("scope count was not bounded during selection", err)
	}
	badRequest = request
	badRequest.Callback = "http://127.0.0.1:18080/test//callback"
	if _, err := p.inspectBinding(ctx, id, badRequest, nil); !errors.Is(err, errInvalid) {
		t.Fatal("noncanonical callback appeared usable", err)
	}
	if _, err := p.inspectBinding(ctx, id, request, &accepted.Revision); err != nil {
		t.Fatal("rejected requests disturbed the accepted binding", err)
	}

	if _, err := p.addProvider(ctx, id, federationProviderFixture(t, "Entra")); err != nil {
		t.Fatal(err)
	}
	small := federationInput()
	small.Providers = []string{"Entra"}
	small.Scopes = []string{"openid"}
	scoped, err := p.addClient(ctx, id, small)
	if err != nil {
		t.Fatal(err)
	}
	extra := federationBindingRequest{scoped.ID, "Entra", fixtureCallback, federationPKCE, []string{"openid", "email"}}
	if got, err := p.inspectBinding(ctx, id, extra, nil); !errors.Is(err, errFederationNotFound) || got.ClientID != "" {
		t.Fatal("provider mapping expanded the client scope", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*federationClientInput)
	}{
		{"oversize callback list", func(c *federationClientInput) { c.Callbacks = make([]string, maxFederationCallbacks+1) }},
		{"unknown provider name", func(c *federationClientInput) { c.Providers = []string{"unrecognized"} }},
		{"invalid client callback", func(c *federationClientInput) { c.Callbacks = []string{"http://localhost:18080/callback"} }},
		{"unknown scope", func(c *federationClientInput) { c.Scopes = []string{"openid", "unavailable"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := federationInput()
			tc.edit(&input)
			if got, err := p.addClient(t.Context(), id, input); !errors.Is(err, errInvalid) || got.ID != "" {
				t.Fatal("invalid client returned a creation receipt", err)
			}
		})
	}
	if cfg, _, err := p.configuration(ctx, id); err != nil || len(cfg.Clients) != 2 {
		t.Fatal("rejected clients changed storage", err)
	}
}

func TestFederationPublisherRejectsUnreadyAuthorityAndStorage(t *testing.T) {
	s, db := poolFixture(t)
	ctx := t.Context()
	scope := Scope{"012345678901", "us-east-1"}
	id := scope.Region + "_fixture"
	seedSameKey(t, s, scope, id)
	p := federationPublisherFor(t, s, scope, federationAllow)
	provider := federationProviderFixture(t, "Keycloak")
	if got, err := (*federationPublisher)(nil).addProvider(ctx, id, provider); !errors.Is(err, errFederationDenied) || got.Name != "" {
		t.Fatal("absent publisher returned a provider", err)
	}
	if got, err := p.addProvider(ctx, "us-west-2_fixture", provider); !errors.Is(err, errInvalid) || got.Name != "" {
		t.Fatal("mistyped pool region reached configuration", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := p.addProvider(canceled, id, provider); !errors.Is(err, context.Canceled) || got.Name != "" {
		t.Fatal("canceled publication returned a provider", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := p.addProvider(ctx, id, provider); err == nil || got.Name != "" {
		t.Fatal("no live store returned a provider", err)
	}
	if got, rev, err := p.configuration(ctx, id); err == nil || got.Version != 0 || rev != (federationRevision{}) {
		t.Fatal("no live store returned configuration", err)
	}
}

func TestFederationStorageNoAcknowledgmentAtSQLWriteReadbackOrCommitFailure(t *testing.T) {
	s, _ := poolFixture(t)
	ctx := t.Context()
	scope := Scope{"012345678901", "us-east-1"}
	row, err := s.create(ctx, scope, "fault fixture")
	if err != nil {
		t.Fatal(err)
	}
	f := optIn(t, s)
	snap, err := f.snapshot(ctx, scope, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.replace(ctx, scope, row.ID, snap.revision, []byte("last committed"))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE TABLE federation_fault_parent(id INTEGER PRIMARY KEY)")
	exec("CREATE TABLE federation_fault_child(id INTEGER REFERENCES federation_fault_parent(id) DEFERRABLE INITIALLY DEFERRED)")
	for _, tc := range []struct {
		name, trigger string
		want          error
	}{
		{"write", "BEFORE UPDATE OF federation_state ON aws_cognito_user_pools BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END", nil},
		{"readback", "AFTER UPDATE OF federation_state ON aws_cognito_user_pools BEGIN DELETE FROM aws_cognito_user_pools WHERE account=NEW.account AND region=NEW.region AND pool_id=NEW.pool_id; END", sql.ErrNoRows},
		{"commit", "AFTER UPDATE OF federation_state ON aws_cognito_user_pools BEGIN INSERT INTO federation_fault_child VALUES (1); END", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec("CREATE TRIGGER federation_fixture_fault " + tc.trigger)
			got, err := f.replace(t.Context(), scope, row.ID, before.revision, []byte("unacknowledged edit"))
			if err == nil || tc.want != nil && !errors.Is(err, tc.want) || got.revision != (federationRevision{}) || got.metadata != nil {
				t.Fatal("failed transaction returned a revision or projection", err)
			}
			exec("DROP TRIGGER federation_fixture_fault")
			got, err = f.snapshot(t.Context(), scope, row.ID)
			if err != nil || got.revision != before.revision || string(got.metadata) != "last committed" {
				t.Fatal("failed transaction changed the pool or last committed metadata", err)
			}
		})
	}
	tooLarge := func(federationSnapshot, bool) ([]byte, bool, error) {
		return make([]byte, maxFederationMetadata+1), true, nil
	}
	if got, err := f.transition(ctx, scope, row.ID, tooLarge); !errors.Is(err, errInvalid) || got.metadata != nil {
		t.Fatal("unbounded internal transition returned state", err)
	}
	after, err := f.replace(ctx, scope, row.ID, before.revision, []byte("recovered"))
	if err != nil || after.revision == before.revision || string(after.metadata) != "recovered" {
		t.Fatal("rollback did not permit a fresh acknowledged revision", err)
	}
}

func TestFederationWireReportsCapacityAndRejectsMalformedFields(t *testing.T) {
	_, db := poolFixture(t)
	s, err := NewStoreWithLocalFederationConfig(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{"012345678901", "us-east-1"}
	id := scope.Region + "_fixture"
	seedSameKey(t, s, scope, id)
	p := federationPublisherFor(t, s, scope, federationAllow)
	source := federationProviderFixture(t, "Keycloak")
	if _, err := p.addProvider(t.Context(), id, source); err != nil {
		t.Fatal(err)
	}
	calls := 0
	h := NewHTTPHandler(s, scope, Actor{"capacity-test", "fixture"}, func(_ context.Context, action, arn string) bool {
		calls++
		return action == createFederationClient && arn == scope.arn(id)
	})
	call := func(op string, fields map[string]any, status int, errorType string) {
		t.Helper()
		body, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader(string(body)))
		r.RemoteAddr = "127.0.0.1:25447"
		r.Header.Set("Content-Type", JSONMediaType)
		r.Header.Set("X-Amz-Target", TargetPrefix+op)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status || w.Header().Get("X-Amzn-Errortype") != errorType {
			t.Fatalf("%s: want %d/%s, got %d/%s", op, status, errorType, w.Code, w.Header().Get("X-Amzn-Errortype"))
		}
	}
	call(nativeCreateIdentityProvider, map[string]any{"UserPoolId": id, "ProviderName": "Keycloak",
		"ProviderType": "SAML", "ProviderDetails": map[string]any{"MetadataFile": source.MetadataFile},
		"AttributeMapping": map[string]any{"email": 1}}, 400, "InvalidParameterException")
	wire := map[string]any{"UserPoolId": id, "ClientName": "fixture", "AllowedOAuthFlowsUserPoolClient": true,
		"AllowedOAuthFlows": []string{"code"}, "AllowedOAuthScopes": []string{"openid"},
		"SupportedIdentityProviders": []string{"Keycloak"}, "CallbackURLs": fixtureCallback}
	call(nativeCreateUserPoolClient, wire, 400, "InvalidParameterException")
	if calls != 0 {
		t.Fatal("malformed configuration reached policy evaluation")
	}
	wire["CallbackURLs"] = []string{fixtureCallback}
	for range maxFederationClients {
		call(nativeCreateUserPoolClient, wire, 200, "")
	}
	before, rev, err := p.configuration(t.Context(), id)
	if err != nil || len(before.Clients) != maxFederationClients {
		t.Fatal("baseline configuration capacity not reached", err)
	}
	call(nativeCreateUserPoolClient, wire, 400, "LimitExceededException")
	if after, final, err := p.configuration(t.Context(), id); err != nil || final != rev || len(after.Clients) != len(before.Clients) {
		t.Fatal("capacity error changed the acknowledged configuration", err)
	}
}
