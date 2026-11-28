package cognitoidp

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestHostedDamagedSQLiteRestoreRefusesLoginUntilKnownGoodSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name         string
		slot         int
		nativeStatus int
	}{
		{"native registration document", 0, http.StatusInternalServerError},
		{"browser state document", 1, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := newCognitoRecoveryFixture(t)
			f.addProvider(t, s, http.StatusOK)
			client := f.addClient(t, s, "restored app", http.StatusOK)
			b := f.browser(t, s)
			l, completed := f.complete(t, b, client)
			assertHostedCompletion(t, l, completed)
			snapshot := f.slots(t, s.db)
			f.dismiss(t, s.db)

			// The fixture owns this file. Model a restored SQLite copy with
			// one truncated application document, not an HTTP-supplied value.
			// Preserve the real pool, the other slot, and the native revision.
			install := func(slots [2][]byte) {
				t.Helper()
				offline := f.openWritable(t)
				result, err := offline.db.ExecContext(t.Context(), `UPDATE aws_cognito_user_pools
					SET federation_state=?, federation_hosted_state=?
					WHERE account=? AND region=? AND pool_id=?`,
					slots[0], slots[1], f.scope.Account, f.scope.Region, f.poolID)
				if err != nil {
					t.Fatal(err)
				}
				if count, err := result.RowsAffected(); err != nil || count != 1 {
					t.Fatal("snapshot repair did not select exactly the existing owned pool", err)
				}
				f.dismiss(t, offline.db)
			}
			damaged := snapshot
			damaged[tc.slot] = damaged[tc.slot][:len(damaged[tc.slot])-1]
			install(damaged)

			s = f.openWritable(t)
			b = f.browser(t, s)
			f.native(t, s, "DescribeUserPool", nil, http.StatusOK)
			f.native(t, s, nativeDescribeUserPoolClient, map[string]any{"ClientId": client}, tc.nativeStatus)
			q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {hostedFixtureCallback},
				"scope": {"openid"}, "state": {fixtureHostedState}, "nonce": {fixtureHostedNonce},
				"code_challenge": {hostedDigest(strings.Repeat("v", 43))}, "code_challenge_method": {"S256"}, "identity_provider": {"Entra"}}
			r := hostedFixtureRequest(http.MethodGet, hostedPrefix+f.poolID+"/"+hostedAuthorize+"?"+q.Encode(), nil)
			requireNoHostedResult(t, hostedServe(b, r))
			if seen := f.slots(t, s.db); !bytes.Equal(seen[0], damaged[0]) || !bytes.Equal(seen[1], damaged[1]) {
				t.Fatal("refused sign-in silently rewrote a damaged snapshot")
			}
			f.dismiss(t, s.db)

			install(snapshot)
			s = f.openWritable(t)
			if seen := f.slots(t, s.db); !bytes.Equal(seen[0], snapshot[0]) || !bytes.Equal(seen[1], snapshot[1]) {
				t.Fatal("writable reopen did not recover the same provider and browser records")
			}
			b = f.browser(t, s)
			l, completed = f.complete(t, b, client)
			redeemHostedSignedLogin(t, b, l, completed)
		})
	}
}

func TestHostedSQLiteAcknowledgedExchangeRequiresMatchingReadback(t *testing.T) {
	f, s := newCognitoRecoveryFixture(t)
	f.addProvider(t, s, http.StatusOK)
	client := f.addClient(t, s, "readback app", http.StatusOK)
	b := f.browser(t, s)
	l, completed := f.complete(t, b, client)
	cb, _ := assertHostedCompletion(t, l, completed)
	code := cb.Query().Get("code")
	before := f.slots(t, s.db)

	// A retained adapter in this disposable database restores the old
	// document after SQLite accepts the row update. The outer UPDATE still
	// affects one row. This is distinct from an abort or ignored write.
	if _, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER legacy_hosted_copy
		AFTER UPDATE OF federation_hosted_state ON aws_cognito_user_pools
		BEGIN
			UPDATE aws_cognito_user_pools SET federation_hosted_state=OLD.federation_hosted_state
			WHERE account=NEW.account AND region=NEW.region AND pool_id=NEW.pool_id;
		END`); err != nil {
		t.Fatal(err)
	}
	exchange := func() *http.Request { return hostedExchangeRequest(l, code, hostedOrigin(cb)) }
	requireNoHostedResult(t, hostedServe(b, exchange()))
	if seen := f.slots(t, s.db); !bytes.Equal(seen[0], before[0]) || !bytes.Equal(seen[1], before[1]) {
		t.Fatal("an unconfirmed token exchange changed the owned pool or used its code")
	}
	if _, err := s.db.ExecContext(t.Context(), "DROP TRIGGER legacy_hosted_copy"); err != nil {
		t.Fatal(err)
	}

	redeemHostedSignedLogin(t, b, l, completed)
	requireNoHostedResult(t, hostedServe(b, exchange()))
}
