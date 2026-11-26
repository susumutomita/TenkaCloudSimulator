package cognitoidp

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	fixtureIdPURL   = "http://127.0.0.1:18081/sso"
	fixtureCallback = "http://127.0.0.1:18080/callback?app=fixture"
	fixtureEntity   = "urn:test:synthetic-local-idp"
)

func federationCertFixture(t *testing.T, at time.Time, thumb int64) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(thumb), NotBefore: at.Add(-time.Hour),
		NotAfter: at.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func federationXMLFixture(at time.Time, cert []byte) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" xmlns:ds="http://www.w3.org/2000/09/xmldsig#" entityID="%s" validUntil="%s">
  <md:IDPSSODescriptor protocolSupportEnumeration="%s" validUntil="%s">
    <md:KeyDescriptor use="signing"><ds:KeyInfo><ds:X509Data><ds:X509Certificate>%s</ds:X509Certificate></ds:X509Data></ds:KeyInfo></md:KeyDescriptor>
    <md:SingleSignOnService Binding="%s" Location="%s"/>
    <md:SingleSignOnService Binding="%s" Location="http://127.0.0.1:18081/post"/>
  </md:IDPSSODescriptor>
  <md:Extensions><fixture:unused xmlns:fixture="urn:test">not part of the projection</fixture:unused></md:Extensions>
</md:EntityDescriptor>`, fixtureEntity, at.Add(50*time.Minute).Format(time.RFC3339), federationSAMLProtocol,
		at.Add(40*time.Minute).Format(time.RFC3339), base64.StdEncoding.EncodeToString(cert),
		federationSAMLRedirect, fixtureIdPURL, federationSAMLPost)
}

func federationAllow(context.Context, string, string) bool { return true }

func federationPublisherFor(t *testing.T, s *Store, scope Scope, auth Authorize) *federationPublisher {
	t.Helper()
	publisher, err := newFederationPublisher(s, scope, Actor{"test-fixture", "test-exercise"}, auth)
	if err != nil {
		t.Fatal(err)
	}
	return publisher
}

func federationInput() federationClientInput {
	return federationClientInput{Name: "internal fixture app", Callbacks: []string{fixtureCallback},
		Providers: []string{"Keycloak"}, Scopes: []string{"openid", "email", "profile"}}
}

func federationProviderFixture(t *testing.T, name string) federationProviderInput {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	return federationProviderInput{Name: name, MetadataFile: federationXMLFixture(now, federationCertFixture(t, now, 31)),
		Mapping: map[string]string{"email": "mail", "email_verified": "email_verified", "name": "name"}}
}

func TestFederationPublisherOptInManagementAndExactBinding(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "config.sqlite")
	s, db := federationTestPool(t, path)
	scope := Scope{"012345678901", "us-east-1"}
	pool, err := s.create(ctx, scope, "native fixture")
	if err != nil {
		t.Fatal(err)
	}
	actions := []string{}
	auth := func(_ context.Context, action, resource string) bool {
		actions = append(actions, action)
		return resource == scope.arn(pool.ID)
	}
	p := federationPublisherFor(t, s, scope, auth)
	keycloak, entra := federationProviderFixture(t, "Keycloak"), federationProviderFixture(t, "Entra")
	fixtures := []federationProviderInput{keycloak, entra}
	for _, input := range fixtures {
		got, err := p.addProvider(ctx, pool.ID, input)
		if err != nil || got.Name != input.Name || got.RedirectURL != fixtureIdPURL || len(got.SigningCertDER) == 0 {
			t.Fatal("provider did not project approved typed metadata", err)
		}
	}
	if _, err := p.addProvider(ctx, pool.ID, keycloak); !errors.Is(err, errFederationDuplicate) {
		t.Fatal("duplicate provider accepted", err)
	}
	flow := federationInput()
	client, err := p.addClient(ctx, pool.ID, flow)
	if err != nil || !validFederationClientID(client.ID) {
		t.Fatal("local public client not persisted", err)
	}
	entraInput := federationInput()
	entraInput.Providers = []string{"Entra"}
	otherClient, err := p.addClient(ctx, pool.ID, entraInput)
	if err != nil {
		t.Fatal("second provider client configuration failed", err)
	}
	entraRequest := federationBindingRequest{otherClient.ID, entra.Name, fixtureCallback, federationPKCE, []string{"openid", "profile"}}
	if chosen, err := p.inspectBinding(ctx, pool.ID, entraRequest, nil); err != nil || chosen.Provider.Name != "Entra" {
		t.Fatal("configured second provider was not selectable", err)
	}
	request := federationBindingRequest{client.ID, keycloak.Name, fixtureCallback, federationPKCE, []string{"openid", "email"}}
	selection, err := p.inspectBinding(ctx, pool.ID, request, nil)
	if err != nil || selection.ClientID != client.ID || selection.Callback != fixtureCallback ||
		selection.Owner != (federationOwner{scope, pool.ID}) || selection.Provider.EntityID != fixtureEntity {
		t.Fatal("binding not resolved exactly", err)
	}
	wantPrefix := []string{createFederationProvider, createFederationProvider, createFederationProvider,
		createFederationClient, createFederationClient, describeFederationClient, describeFederationProvider,
		describeFederationClient, describeFederationProvider}
	if strings.Join(actions, ",") != strings.Join(wantPrefix, ",") {
		t.Fatal("wrong exact action scopes for config management")
	}

	// Reported client/provider data must be read from committed storage, not aliases.
	flow.Callbacks[0] = "http://127.0.0.1:18080/alias"
	keycloak.Mapping["email"] = "renamed"
	selection.Provider.Mapping["email"] = "changed after read"
	recordStore := optIn(t, s)
	record, err := recordStore.transition(ctx, scope, pool.ID, nil)
	if err != nil || bytes.Contains(record.metadata, []byte("EntityDescriptor")) || bytes.Contains(record.metadata, []byte("not part of the projection")) {
		t.Fatal("raw metadata leaked into durable projection", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, _ = federationTestPool(t, path)
	p = federationPublisherFor(t, s, scope, auth)
	restored, err := p.inspectBinding(ctx, pool.ID, request, &selection.Revision)
	if err != nil || restored.Provider.Mapping["email"] != "mail" || restored.Callback != fixtureCallback {
		t.Fatal("cold reopen lost or altered exact binding", err)
	}

	for name, edit := range map[string]func(*federationBindingRequest){
		"unregistered callback": func(q *federationBindingRequest) { q.Callback = "http://127.0.0.1:18080/callback?app=another" },
		"callback host":         func(q *federationBindingRequest) { q.Callback = "http://127.0.0.2:18080/callback?app=fixture" },
		"callback port":         func(q *federationBindingRequest) { q.Callback = "http://127.0.0.1:9999/callback?app=fixture" },
		"wrong provider":        func(q *federationBindingRequest) { q.Provider = "Entra" },
		"wrong client":          func(q *federationBindingRequest) { q.ClientID = strings.Repeat("0", 24) },
	} {
		t.Run(name, func(t *testing.T) {
			q := request
			edit(&q)
			got, err := p.inspectBinding(ctx, pool.ID, q, &selection.Revision)
			if !errors.Is(err, errFederationNotFound) || got.ClientID != "" {
				t.Fatal("returned a reply for a different local tuple", err)
			}
		})
	}
	misfit := request
	misfit.PKCEMethod = "plain"
	if _, err := p.inspectBinding(ctx, pool.ID, misfit, nil); !errors.Is(err, errInvalid) {
		t.Fatal("admitted a non-S256 profile", err)
	}
	if err := p.removeProvider(ctx, pool.ID, "Keycloak"); !errors.Is(err, errFederationInUse) {
		t.Fatal("provider delete removed client without client permission", err)
	}
	if _, err := p.inspectBinding(ctx, pool.ID, request, &selection.Revision); err != nil {
		t.Fatal("failed delete changed config revision", err)
	}
	if err := p.removeClient(ctx, pool.ID, client.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.inspectBinding(ctx, pool.ID, request, &selection.Revision); !errors.Is(err, errFederationStale) {
		t.Fatal("old version remained valid after client deletion", err)
	}
	if err := p.removeProvider(ctx, pool.ID, "Keycloak"); err != nil {
		t.Fatal(err)
	}
	if err := p.removeClient(ctx, pool.ID, otherClient.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.removeProvider(ctx, pool.ID, "Entra"); err != nil {
		t.Fatal(err)
	}
	if err := s.delete(ctx, scope, pool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.addClient(ctx, pool.ID, federationInput()); !errors.Is(err, errMissing) {
		t.Fatal("mutation recreated deleted parent", err)
	}
	seedSameKey(t, s, scope, pool.ID)
	if _, err := p.inspectBinding(ctx, pool.ID, request, &selection.Revision); !errors.Is(err, errFederationStale) {
		t.Fatal("old version attached to replicated ID", err)
	}
}

func TestFederationPublisherAuthorityBeforeStorageAndScopeIsolation(t *testing.T) {
	ctx := t.Context()
	s, _ := poolFixture(t)
	scope := Scope{"012345678901", "us-east-1"}
	peer := Scope{"012345678902", scope.Region}
	west := Scope{scope.Account, "us-west-2"}
	poolID := scope.Region + "_shared"
	provider := federationProviderFixture(t, "Keycloak")
	seedSameKey(t, s, scope, poolID)

	for _, bad := range []Authorize{nil, func(context.Context, string, string) bool { return false }} {
		p, err := newFederationPublisher(s, scope, Actor{"test", "fixture"}, bad)
		if bad == nil {
			if !errors.Is(err, errFederationDenied) || p != nil {
				t.Fatal("nil authorizer enabled publisher")
			}
			continue
		}
		for _, target := range []string{poolID, scope.Region + "_missing"} {
			if got, err := p.addProvider(ctx, target, provider); !errors.Is(err, errFederationDenied) || got.Name != "" {
				t.Fatal("denied action discovered existence or produced configuration", err)
			}
		}
	}
	var installed bool
	if err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM pragma_table_info('aws_cognito_user_pools') WHERE name='federation_state')").Scan(&installed); err != nil || installed {
		t.Fatal("denial touched optional SQLite schema", err)
	}
	if p, err := newFederationPublisher(s, scope, Actor{}, federationAllow); !errors.Is(err, errFederationDenied) || p != nil {
		t.Fatal("missing trusted actor enabled publisher", err)
	}
	p := federationPublisherFor(t, s, scope, func(_ context.Context, action, arn string) bool {
		return arn == scope.arn(poolID) && action == createFederationProvider
	})
	if _, err := p.addProvider(ctx, poolID, provider); err != nil {
		t.Fatal(err)
	}
	if _, err := p.addClient(ctx, poolID, federationInput()); !errors.Is(err, errFederationDenied) {
		t.Fatal("provider create grant used to create client", err)
	}
	if err := p.removeProvider(ctx, poolID, "Keycloak"); !errors.Is(err, errFederationDenied) {
		t.Fatal("provider create grant used to remove", err)
	}
	signed := federationPublisherFor(t, s, scope, federationAllow)
	ownerClient, err := signed.addClient(ctx, poolID, federationInput())
	if err != nil {
		t.Fatal(err)
	}
	q := federationBindingRequest{ownerClient.ID, "Keycloak", fixtureCallback, "S256", []string{"openid"}}
	ownerBinding, err := signed.inspectBinding(ctx, poolID, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, onlyAction := range []string{describeFederationClient, describeFederationProvider} {
		p := federationPublisherFor(t, s, scope, func(_ context.Context, action, arn string) bool {
			return arn == scope.arn(poolID) && action == onlyAction
		})
		if got, err := p.inspectBinding(ctx, poolID, q, &ownerBinding.Revision); !errors.Is(err, errFederationDenied) || got.ClientID != "" {
			t.Fatal("revision or partial rights supplied describe authority", err)
		}
	}

	for _, item := range []struct {
		scope Scope
		id    string
	}{{peer, poolID}, {west, west.Region + "_shared"}, {scope, scope.Region + "_sibling"}} {
		seedSameKey(t, s, item.scope, item.id)
		other := federationPublisherFor(t, s, item.scope, federationAllow)
		if _, err := other.addClient(ctx, item.id, federationInput()); !errors.Is(err, errFederationNotFound) {
			t.Fatal("referenced peer provider for client", err)
		}
		store := optIn(t, s)
		empty, err := store.transition(ctx, item.scope, item.id, nil)
		if err != nil || empty.revision != (federationRevision{}) || len(empty.metadata) != 0 {
			t.Fatal("denied edit initialized empty generation", err)
		}
		if _, err := other.addProvider(ctx, item.id, provider); err != nil {
			t.Fatal(err)
		}
		if _, err := other.inspectBinding(ctx, item.id, q, nil); !errors.Is(err, errFederationNotFound) {
			t.Fatal("client ID leaked across scope", err)
		}
		if _, err := other.inspectBinding(ctx, item.id, q, &ownerBinding.Revision); !errors.Is(err, errFederationStale) {
			t.Fatal("revision was usable across scopes", err)
		}
	}
	if _, err := signed.inspectBinding(ctx, poolID, q, &ownerBinding.Revision); err != nil {
		t.Fatal("peer writes changed owner configuration", err)
	}
}

func TestFederationPublisherConcurrentFramesAndCorruptProjection(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "config.sqlite")
	a, _ := federationTestPool(t, path)
	b, _ := federationTestPool(t, path)
	scope := Scope{"012345678901", "us-east-1"}
	pool, err := a.create(ctx, scope, "typed fixture")
	if err != nil {
		t.Fatal(err)
	}
	left := federationPublisherFor(t, a, scope, federationAllow)
	right := federationPublisherFor(t, b, scope, federationAllow)
	if _, err := left.addProvider(ctx, pool.ID, federationProviderFixture(t, "Keycloak")); err != nil {
		t.Fatal(err)
	}
	type result struct {
		client federationClient
		err    error
	}
	start := make(chan struct{})
	done := make(chan result, 2)
	for _, p := range []*federationPublisher{left, right} {
		go func() {
			<-start
			c, err := p.addClient(ctx, pool.ID, federationInput())
			done <- result{c, err}
		}()
	}
	close(start)
	one, two := <-done, <-done
	if one.err != nil || two.err != nil || one.client.ID == two.client.ID {
		t.Fatal("concurrent client writes failed", one.err, two.err)
	}
	cfg, rev, err := left.configuration(ctx, pool.ID)
	if err != nil || len(cfg.Clients) != 2 || cfg.clientIndex(one.client.ID) < 0 || cfg.clientIndex(two.client.ID) < 0 {
		t.Fatal("lost approved concurrent client", err)
	}
	if _, err = a.db.Exec(`CREATE TRIGGER ignore_typed_commit BEFORE UPDATE OF federation_state ON aws_cognito_user_pools
		BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if ack, err := right.addClient(ctx, pool.ID, federationInput()); !errors.Is(err, errFederationState) || ack.ID != "" {
		t.Fatal("failed SQL returned a client ID", err)
	}
	if _, cfgRev, err := left.configuration(ctx, pool.ID); err != nil || cfgRev != rev {
		t.Fatal("failed SQL advanced the config revision", err)
	}
	if _, err = b.db.Exec("DROP TRIGGER ignore_typed_commit"); err != nil {
		t.Fatal(err)
	}
	cfg.Owner.Scope.Account = "012345678902" // Synthetic corruption of persisted typed owner.
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := optIn(t, a).replace(ctx, scope, pool.ID, rev, raw); err != nil {
		t.Fatal(err)
	}
	if err := left.removeClient(ctx, pool.ID, one.client.ID); !errors.Is(err, errFederationState) {
		t.Fatal("mutated slot with invalid persisted owner", err)
	}
	request := federationBindingRequest{one.client.ID, "Keycloak", fixtureCallback, "S256", []string{"openid"}}
	if got, err := right.inspectBinding(ctx, pool.ID, request, nil); !errors.Is(err, errFederationState) || got.ClientID != "" {
		t.Fatal("corrupt owner disclosed a binding", err)
	}
}

func TestFederationPublisherCapacityAndDefaultSurface(t *testing.T) {
	s, _ := poolFixture(t)
	ctx := t.Context()
	scope := Scope{"012345678901", "us-east-1"}
	id := scope.Region + "_budget"
	seedSameKey(t, s, scope, id)
	p := federationPublisherFor(t, s, scope, federationAllow)
	h := NewHTTPHandler(s, scope, Actor{"test", "fixture"}, federationAllow)
	rejectManagement := func() {
		t.Helper()
		for _, op := range []string{"CreateIdentityProvider", "DescribeIdentityProvider", "DeleteIdentityProvider", "ListIdentityProviders", "CreateUserPoolClient", "DescribeUserPoolClient", "DeleteUserPoolClient", "InitiateAuth"} {
			poolRequest(t, h, op, `{}`, 400, "UnknownOperationException")
		}
	}
	rejectManagement()
	if _, err := p.addProvider(ctx, id, federationProviderFixture(t, "Keycloak")); err != nil {
		t.Fatal(err)
	}
	rejectManagement() // Config presence never opts in the default HTTP endpoint.
	for range maxFederationClients {
		if _, err := p.addClient(ctx, id, federationInput()); err != nil {
			t.Fatal("could not reach declared client capacity", err)
		}
	}
	old, rev, err := p.configuration(ctx, id)
	if err != nil || len(old.Clients) != maxFederationClients {
		t.Fatal("bad client count", err)
	}
	if got, err := p.addClient(ctx, id, federationInput()); !errors.Is(err, errFederationLimit) || got.ID != "" {
		t.Fatal("client count limit not enforced", err)
	}
	for _, existed := range old.Clients {
		if err := p.removeClient(ctx, id, existed.ID); err != nil {
			t.Fatal(err)
		}
	}
	large := federationInput()
	large.Callbacks = nil
	for i := range maxFederationCallbacks {
		large.Callbacks = append(large.Callbacks, fmt.Sprintf("http://127.0.0.1:18080/big/%d?value=%s", i, strings.Repeat("<", 720)))
	}
	for range 2 {
		if _, err := p.addClient(ctx, id, large); err != nil {
			t.Fatal("typed payload below aggregate budget was refused", err)
		}
	}
	before, largeRev, err := p.configuration(ctx, id)
	if err != nil || rev == largeRev {
		t.Fatal("successful edits did not advance revision", err)
	}
	for i := 0; ; i++ {
		got, err := p.addClient(ctx, id, large)
		if errors.Is(err, errFederationLimit) {
			if got.ID != "" || i > 1 {
				t.Fatal("aggregate overflow did not fail closed")
			}
			break
		}
		if err != nil || i >= 1 {
			t.Fatal("aggregate byte limit not enforced", err)
		}
		before, largeRev, err = p.configuration(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	got, revised, err := p.configuration(ctx, id)
	if err != nil || revised != largeRev || len(got.Clients) != len(before.Clients) {
		t.Fatal("failed aggregate write changed revision or client set", err)
	}
}

func TestFederationRawConfigurationRequiresBothDescribeGrantsBeforeStorage(t *testing.T) {
	s, _ := poolFixture(t)
	ctx := t.Context()
	scope := Scope{"012345678901", "us-east-1"}
	id, absent := scope.Region+"_owner", scope.Region+"_absent"
	seedSameKey(t, s, scope, id)
	for _, single := range []string{createFederationClient, describeFederationClient, describeFederationProvider} {
		p := federationPublisherFor(t, s, scope, func(_ context.Context, act, arn string) bool {
			return act == single && (arn == scope.arn(id) || arn == scope.arn(absent))
		})
		for _, target := range []string{id, absent} {
			cfg, rev, err := p.configuration(ctx, target)
			if !errors.Is(err, errFederationDenied) || cfg.Version != 0 || rev != (federationRevision{}) {
				t.Fatal("raw read without both describe decisions", err)
			}
		}
	}
	var migrated bool
	if err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM pragma_table_info('aws_cognito_user_pools') WHERE name='federation_state')").Scan(&migrated); err != nil || migrated {
		t.Fatal("denied raw read touched storage", err)
	}
	setup := federationPublisherFor(t, s, scope, federationAllow)
	if _, err := setup.addProvider(ctx, id, federationProviderFixture(t, "Keycloak")); err != nil {
		t.Fatal(err)
	}
	seen := []string{}
	both := federationPublisherFor(t, s, scope, func(_ context.Context, act, arn string) bool {
		seen = append(seen, act)
		return (act == describeFederationClient || act == describeFederationProvider) && arn == scope.arn(id)
	})
	if cfg, _, err := both.configuration(ctx, id); err != nil || len(cfg.Providers) != 1 || cfg.Owner != (federationOwner{scope, id}) {
		t.Fatal("approved exact raw configuration unavailable", err)
	}
	if strings.Join(seen, ",") != describeFederationClient+","+describeFederationProvider {
		t.Fatal("raw admission did not evaluate both exact actions")
	}
	if _, _, err := both.configuration(ctx, absent); !errors.Is(err, errFederationDenied) {
		t.Fatal("describes for a different pool admitted", err)
	}
}

func TestFederationPublisherFreezesBoundedInputsBeforePolicyCallbacks(t *testing.T) {
	newFixture := func(t *testing.T) (*Store, Scope, string) {
		t.Helper()
		s, _ := poolFixture(t)
		scope := Scope{"012345678901", "us-east-1"}
		id := scope.Region + "_snapshot"
		seedSameKey(t, s, scope, id)
		return s, scope, id
	}
	t.Run("mapping", func(t *testing.T) {
		s, scope, id := newFixture(t)
		input := federationProviderFixture(t, "Keycloak")
		p := federationPublisherFor(t, s, scope, func(_ context.Context, action, arn string) bool {
			input.Mapping["email"] = "replacement"
			return action == createFederationProvider && arn == scope.arn(id)
		})
		provider, err := p.addProvider(t.Context(), id, input)
		if err != nil || provider.Mapping["email"] != "mail" || input.Mapping["email"] != "replacement" {
			t.Fatal("registration did not use the originally validated attribute mapping", err)
		}
		reader := federationPublisherFor(t, s, scope, federationAllow)
		cfg, _, err := reader.configuration(t.Context(), id)
		if err != nil || len(cfg.Providers) != 1 || cfg.Providers[0].Mapping["email"] != "mail" {
			t.Fatal("registered mapping differed from the validated copy", err)
		}
	})
	t.Run("client slices", func(t *testing.T) {
		s, scope, id := newFixture(t)
		admin := federationPublisherFor(t, s, scope, federationAllow)
		for _, name := range []string{"Keycloak", "Entra"} {
			if _, err := admin.addProvider(t.Context(), id, federationProviderFixture(t, name)); err != nil {
				t.Fatal(err)
			}
		}
		input := federationInput()
		p := federationPublisherFor(t, s, scope, func(_ context.Context, action, arn string) bool {
			input.Callbacks[0] = "http://127.0.0.1:18080/another-callback"
			input.Providers[0] = "Entra"
			input.Scopes[1] = "unconfigured"
			return action == createFederationClient && arn == scope.arn(id)
		})
		client, err := p.addClient(t.Context(), id, input)
		if err != nil || client.Callbacks[0] != fixtureCallback || client.Providers[0] != "Keycloak" ||
			client.Scopes[1] != "email" || input.Providers[0] != "Entra" {
			t.Fatal("client registration did not use the originally validated slices", err)
		}
		cfg, _, err := admin.configuration(t.Context(), id)
		if err != nil || len(cfg.Clients) != 1 || cfg.Clients[0].ID != client.ID || cfg.Clients[0].Scopes[1] != "email" {
			t.Fatal("registered client state differed from the validated copy", err)
		}
	})
	t.Run("binding inputs", func(t *testing.T) {
		s, scope, id := newFixture(t)
		ctx := t.Context()
		admin := federationPublisherFor(t, s, scope, federationAllow)
		if _, err := admin.addProvider(ctx, id, federationProviderFixture(t, "Keycloak")); err != nil {
			t.Fatal(err)
		}
		client, err := admin.addClient(ctx, id, federationInput())
		if err != nil {
			t.Fatal(err)
		}
		_, live, err := admin.configuration(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		retired := live
		retired[0] ^= 1
		q := func() federationBindingRequest {
			return federationBindingRequest{client.ID, "Keycloak", fixtureCallback, federationPKCE, []string{"openid", "email"}}
		}
		allowed := func(action, arn string) bool {
			return (action == describeFederationClient || action == describeFederationProvider) && arn == scope.arn(id)
		}
		t.Run("scope", func(t *testing.T) {
			request := q()
			p := federationPublisherFor(t, s, scope, func(_ context.Context, action, arn string) bool {
				request.Scopes[1] = "profile"
				return allowed(action, arn)
			})
			got, err := p.inspectBinding(t.Context(), id, request, &live)
			if err != nil || got.Revision != live || len(got.Scopes) != 2 || got.Scopes[1] != "email" || request.Scopes[1] != "profile" {
				t.Fatal("binding did not use the originally requested scope", err)
			}
		})
		t.Run("current revision stays current", func(t *testing.T) {
			expected := live
			p := federationPublisherFor(t, s, scope, func(_ context.Context, action, arn string) bool {
				expected = retired
				return allowed(action, arn)
			})
			got, err := p.inspectBinding(t.Context(), id, q(), &expected)
			if err != nil || got.Revision != live || expected != retired {
				t.Fatal("binding did not preserve the original expected revision", err)
			}
		})
		t.Run("retired revision stays retired", func(t *testing.T) {
			expected := retired
			p := federationPublisherFor(t, s, scope, func(_ context.Context, action, arn string) bool {
				expected = live
				return allowed(action, arn)
			})
			if got, err := p.inspectBinding(t.Context(), id, q(), &expected); !errors.Is(err, errFederationStale) || got.ClientID != "" || expected != live {
				t.Fatal("a retired revision was replaced by policy evaluation", err)
			}
		})
	})
}
