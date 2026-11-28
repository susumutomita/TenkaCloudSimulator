package cognitoidp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewjam/saml"
)

func requireNoHostedResult(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" ||
		w.Header().Get("Set-Cookie") != "" || w.Header().Get("Access-Control-Allow-Origin") != "" ||
		strings.Contains(w.Body.String(), "Continue") || strings.Contains(w.Body.String(), "id_token") {
		t.Fatal("a rejected hosted operation published a sign-in result", w.Code)
	}
}

func TestHostedConfigCommitFencesActiveSignedBrowserOperations(t *testing.T) {
	f := newHostedSignedIDP(t)
	b, p, _, pool, client := newHostedFixture(t, f.provider("Entra"))
	subject := saml.NameID{Format: string(saml.PersistentNameIDFormat), Value: "fixture@example.invalid"}
	assertion := func(l hostedLogin) *httptest.ResponseRecorder {
		return f.post(t, b, l, "Entra", subject, hostedObjectIDs(fixtureEntraAlice))
	}
	generation1 := beginHostedSignedLogin(t, b, pool, client, "Entra")
	replaceClient, err := p.addClient(t.Context(), pool, federationClientInput{
		Name: "replacement app", Callbacks: []string{hostedFixtureCallback}, Providers: []string{"Entra"}, Scopes: []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Even an additive native app-client configuration commit invalidates
	// an earlier browser snapshot. It does not change the selected IdP's identity.
	requireNoHostedResult(t, assertion(generation1))

	generation2 := beginHostedSignedLogin(t, b, pool, replaceClient.ID, "Entra")
	completed := assertion(generation2)
	callback, _ := assertHostedCompletion(t, generation2, completed)
	code := callback.Query().Get("code")
	if err := p.removeClient(t.Context(), pool, replaceClient.ID); err != nil {
		t.Fatal(err)
	}
	requireNoHostedResult(t, hostedServe(b, hostedExchangeRequest(generation2, code, "")))

	recovered := beginHostedSignedLogin(t, b, pool, client, "Entra")
	redeemHostedSignedLogin(t, b, recovered, assertion(recovered))
}

func TestHostedNoSuccessfulBrowserResultOnPersistentWriteRefusal(t *testing.T) {
	f := newHostedSignedIDP(t)
	b, _, _, pool, client := newHostedFixture(t, f.provider("Entra"))
	ctx := t.Context()
	subject := saml.NameID{Format: string(saml.PersistentNameIDFormat), Value: "fixture@example.invalid"}

	// This test owns this SQLite database. Exercise the actual HTTP handling
	// against a write refusal and a write that didn't update its expected row.
	// Neither interruption should publish a browser artifact before a commit.
	withFailedWrite := func(mode string, check func()) {
		t.Helper()
		statement := `CREATE TRIGGER hosted_write_failure BEFORE UPDATE OF federation_hosted_state ON aws_cognito_user_pools
			BEGIN SELECT RAISE(ABORT, 'fixture write refused'); END`
		if mode == "no update" {
			statement = `CREATE TRIGGER hosted_write_failure BEFORE UPDATE OF federation_hosted_state ON aws_cognito_user_pools
				BEGIN SELECT RAISE(IGNORE); END`
		}
		if _, err := b.store.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := b.store.db.ExecContext(ctx, "DROP TRIGGER hosted_write_failure"); err != nil {
				t.Error(err)
			}
		}()
		check()
	}

	withFailedWrite("abort", func() {
		q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {hostedFixtureCallback},
			"scope": {"openid"}, "state": {fixtureHostedState}, "nonce": {fixtureHostedNonce},
			"code_challenge": {hostedDigest(strings.Repeat("v", 43))}, "code_challenge_method": {"S256"}, "identity_provider": {"Entra"}}
		r := hostedFixtureRequest(http.MethodGet, hostedPrefix+pool+"/"+hostedAuthorize+"?"+q.Encode(), nil)
		requireNoHostedResult(t, hostedServe(b, r))
	})

	login := beginHostedSignedLogin(t, b, pool, client, "Entra")
	submit := func() *httptest.ResponseRecorder {
		return f.post(t, b, login, "Entra", subject, hostedObjectIDs(fixtureEntraAlice))
	}
	withFailedWrite("no update", func() { requireNoHostedResult(t, submit()) })
	completed := submit()
	callback, _ := assertHostedCompletion(t, login, completed)
	exchange := func() *httptest.ResponseRecorder {
		r := hostedExchangeRequest(login, callback.Query().Get("code"), hostedOrigin(callback))
		return hostedServe(b, r)
	}
	withFailedWrite("abort", func() { requireNoHostedResult(t, exchange()) })
	redeemHostedSignedLogin(t, b, login, completed)
	requireNoHostedResult(t, exchange())
}

func TestHostedSignedAssertionsRespectProfileClockLimits(t *testing.T) {
	f := newHostedSignedIDP(t)
	b, _, _, pool, client := newHostedFixture(t, f.provider("Entra"))
	subject := saml.NameID{Format: string(saml.PersistentNameIDFormat), Value: "fixture@example.invalid"}
	identity := hostedObjectIDs(fixtureEntraAlice)
	for _, tc := range []struct {
		name string
		edit func(*saml.Assertion)
	}{
		{"directory issued assertion in its future", func(a *saml.Assertion) {
			a.IssueInstant = a.IssueInstant.Add(30 * time.Second)
		}},
		{"directory recorded authentication in its future", func(a *saml.Assertion) {
			a.AuthnStatements[0].AuthnInstant = a.IssueInstant.Add(30 * time.Second)
		}},
		{"directory enabled an unsupported long validity window", func(a *saml.Assertion) {
			a.Conditions.NotOnOrAfter = a.IssueInstant.Add(6 * time.Minute)
		}},
		{"directory gave a not-before time in its future", func(a *saml.Assertion) {
			a.Conditions.NotBefore = a.IssueInstant.Add(30 * time.Second)
		}},
		{"directory omitted authentication event", func(a *saml.Assertion) {
			a.AuthnStatements = nil
		}},
		{"directory delayed bearer validity", func(a *saml.Assertion) {
			a.Subject.SubjectConfirmations[0].SubjectConfirmationData.NotBefore = a.IssueInstant.Add(30 * time.Second)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			login := beginHostedSignedLogin(t, b, pool, client, "Entra")
			invalid := f.postWithAssertion(t, b, login, "Entra", subject, tc.edit, identity)
			requireNoHostedResult(t, invalid)
			recovered := f.post(t, b, login, "Entra", subject, identity)
			redeemHostedSignedLogin(t, b, login, recovered)
		})
	}
}

func TestHostedManagementSelectsOneClientWithoutExposingOtherConfiguration(t *testing.T) {
	b, operator, scope, pool, id := newHostedFixture(t)
	ctx := t.Context()
	other, err := operator.addClient(ctx, pool, federationClientInput{
		Name: "sibling app", Callbacks: []string{hostedFixtureCallback}, Providers: []string{"Entra"}, Scopes: []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}

	reads := 0
	clientReader := federationPublisherFor(t, b.store, scope, func(_ context.Context, action, arn string) bool {
		if arn != scope.arn(pool) {
			t.Errorf("management selection used a different pool ARN: %s", arn)
			return false
		}
		if action == describeFederationClient {
			reads++
			return true
		}
		return false
	})
	selection := federationSelection{client: id}
	projected, revision, err := clientReader.readConfiguration(ctx, pool, selection)
	if err != nil || reads != 1 || projected.Owner != (federationOwner{Scope: scope, ID: pool}) ||
		len(projected.Clients) != 1 || projected.Clients[0].ID != id || len(projected.Providers) != 0 ||
		revision != (federationRevision{}) {
		t.Fatal("single authorized client query leaked a wider trust snapshot", err)
	}
	if cfg, _, err := clientReader.configuration(ctx, pool); !errors.Is(err, errFederationDenied) ||
		len(cfg.Clients) != 0 || len(cfg.Providers) != 0 {
		t.Fatal("a client reader could read the unrestricted provider configuration", err)
	}
	if err := operator.removeClient(ctx, pool, id); err != nil {
		t.Fatal(err)
	}
	removed, _, err := clientReader.readConfiguration(ctx, pool, selection)
	if !errors.Is(err, errFederationNotFound) || len(removed.Clients) != 0 || len(removed.Providers) != 0 {
		t.Fatal("deleted client selector returned a sibling or provider", err)
	}
	if selected, _, err := clientReader.readConfiguration(ctx, pool, federationSelection{client: other.ID}); err != nil ||
		len(selected.Clients) != 1 || selected.Clients[0].ID != other.ID {
		t.Fatal("the remaining selected client disappeared with its sibling", err)
	}
}
