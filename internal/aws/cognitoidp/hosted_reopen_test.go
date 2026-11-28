package cognitoidp

import (
	"bytes"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/crewjam/saml"
	"github.com/ncruces/go-sqlite3"
)

func TestHostedFirstStartRequiresWritableEnrolledStore(t *testing.T) {
	ctx := t.Context()
	signer := newHostedSignedIDP(t)
	path := filepath.Join(t.TempDir(), "enrolled.sqlite")
	_, initial := federationTestPool(t, path)
	store, err := NewStoreWithLocalFederationConfig(initial.Database())
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Account: "012345678901", Region: "us-east-1"}
	pool, err := store.create(ctx, scope, "canonical enrolled pool")
	if err != nil {
		t.Fatal(err)
	}
	publisher := federationPublisherFor(t, store, scope, federationAllow)
	if _, err := publisher.addProvider(ctx, pool.ID, signer.provider("Entra")); err != nil {
		t.Fatal(err)
	}
	client, err := publisher.addClient(ctx, pool.ID, federationClientInput{
		Name: "existing app", Callbacks: []string{hostedFixtureCallback}, Providers: []string{"Entra"}, Scopes: []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}

	enrollment := func(db *sql.DB) []byte {
		t.Helper()
		var raw []byte
		if err := db.QueryRowContext(ctx, `SELECT federation_state FROM aws_cognito_user_pools
			WHERE account=? AND region=? AND pool_id=?`, scope.Account, scope.Region, pool.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) <= federationRevisionBytes {
			t.Fatal("registered client and provider must exist before migration")
		}
		return raw
	}
	requireLegacySchema := func(db *sql.DB) {
		t.Helper()
		var scanned bool
		err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pragma_table_info('aws_cognito_user_pools') WHERE name='federation_hosted_state')").Scan(&scanned)
		if err != nil || scanned {
			t.Fatal("the hosted column should not exist before a writable first start", err)
		}
	}
	before := enrollment(initial.Database())
	requireLegacySchema(initial.Database())
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}

	// Use SQLite's read-only open mode, not filesystem permissions that the
	// runner may bypass. Native enrollment already exists, but the separate
	// Hosted migration is still required.
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_txlock=immediate"}).String()
	readonly, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readonly.Close() })
	mounted, err := NewStoreWithLocalFederationConfig(readonly)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mounted.openFederationStorage(ctx); err != nil {
		t.Fatal("an already-enrolled store should be readable before Hosted initialization", err)
	}
	if b, err := NewHostedBrowser(mounted, scope.Account, hostedFixtureOrigin); b != nil || !errors.Is(err, sqlite3.READONLY) {
		t.Fatal("a Hosted migration on a read-only enrolled database did not fail closed", err)
	}
	requireLegacySchema(readonly)
	if got := enrollment(readonly); !bytes.Equal(got, before) {
		t.Fatal("failed Hosted initialization changed the native trust or client revision")
	}
	if err := readonly.Close(); err != nil {
		t.Fatal(err)
	}

	_, writable := federationTestPool(t, path)
	store, err = NewStoreWithLocalFederationConfig(writable.Database())
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := NewHostedBrowser(store, scope.Account, hostedFixtureOrigin)
	if err != nil {
		t.Fatal("the same enrolled file could not initialize after a writable reopen", err)
	}
	b := enabled.(*hostedBrowser)
	l := beginHostedSignedLogin(t, b, pool.ID, client.ID, "Entra")
	subject := saml.NameID{Format: string(saml.PersistentNameIDFormat), Value: "fixture@example.invalid"}
	completed := signer.post(t, b, l, "Entra", subject, hostedObjectIDs(fixtureEntraAlice))
	callback, _ := assertHostedCompletion(t, l, completed)
	redeemHostedSignedLogin(t, b, l, completed)
	requireNoHostedResult(t, hostedServe(b, hostedExchangeRequest(l, callback.Query().Get("code"), hostedOrigin(callback))))
	if got := enrollment(writable.Database()); !bytes.Equal(got, before) {
		t.Fatal("successful Hosted migration and first login changed the existing native enrollment")
	}
}
