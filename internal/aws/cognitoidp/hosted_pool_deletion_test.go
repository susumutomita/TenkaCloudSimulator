package cognitoidp

import (
	"context"
	"net/http"
	"testing"

	"github.com/crewjam/saml"
)

func TestHostedNativePoolDeletionEndsOnlyItsOwnInFlightLogins(t *testing.T) {
	ctx := t.Context()
	signer := newHostedSignedIDP(t)
	b, publisher, scope, deletedID, deletedClient := newHostedFixture(t, signer.provider("Entra"))
	peer, err := b.store.create(ctx, scope, "unrelated app pool")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.addProvider(ctx, peer.ID, signer.provider("Entra")); err != nil {
		t.Fatal(err)
	}
	peerClient, err := publisher.addClient(ctx, peer.ID, federationClientInput{
		Name: "peer app client", Callbacks: []string{hostedFixtureCallback}, Providers: []string{"Entra"}, Scopes: []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	subject := saml.NameID{Format: string(saml.PersistentNameIDFormat), Value: "fixture@example.invalid"}
	identity := hostedObjectIDs(fixtureEntraAlice)

	// All three attempts start normally. The selected pool has both a signed
	// assertion still at the directory and a different code not yet exchanged.
	pending := beginHostedSignedLogin(t, b, deletedID, deletedClient, "Entra")
	form := signer.signedAssertionForm(t, pending, "Entra", subject, nil, identity)
	issued := beginHostedSignedLogin(t, b, deletedID, deletedClient, "Entra")
	callback, _ := assertHostedCompletion(t, issued, signer.post(t, b, issued, "Entra", subject, identity))
	surviving := beginHostedSignedLogin(t, b, peer.ID, peerClient.ID, "Entra")
	peerForm := signer.signedAssertionForm(t, surviving, "Entra", subject, nil, identity)

	// Exercise the native provider operation with the authority of the owner
	// for exactly the selected ARN; no direct row deletion or injected grant.
	decisions := 0
	management := NewHTTPHandler(b.store, scope, Actor{"fixture-operator", "test-exercise"}, func(_ context.Context, action, arn string) bool {
		decisions++
		return action == "cognito-idp:DeleteUserPool" && arn == scope.arn(deletedID)
	})
	poolRequest(t, management, "DeleteUserPool", `{"UserPoolId":"`+deletedID+`"}`, http.StatusOK, "")
	if decisions != 1 {
		t.Fatalf("native deletion did not check one scoped management decision: %d", decisions)
	}
	requireNoHostedResult(t, hostedServe(b, hostedACSRequest(pending, form)))
	requireNoHostedResult(t, hostedServe(b, hostedExchangeRequest(issued, callback.Query().Get("code"), hostedOrigin(callback))))

	var deletedRows int
	err = b.store.db.QueryRowContext(ctx, `SELECT count(*) FROM aws_cognito_user_pools
		WHERE account=? AND region=? AND pool_id=?`, scope.Account, scope.Region, deletedID).Scan(&deletedRows)
	if err != nil || deletedRows != 0 {
		t.Fatal("processing a stale in-flight login recreated the deleted canonical pool", err)
	}
	redeemHostedSignedLogin(t, b, surviving, hostedServe(b, hostedACSRequest(surviving, peerForm)))
}
