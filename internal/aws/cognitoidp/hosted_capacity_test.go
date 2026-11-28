package cognitoidp

import (
	"bytes"
	"net/http"
	"testing"
	"testing/synctest"

	"github.com/crewjam/saml"
)

func TestHostedUnredeemedGrantCapacityRecoversAfterExchange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, store := newCognitoRecoveryFixture(t)
		f.addProvider(t, store, http.StatusOK)
		client := f.addClient(t, store, "workshop app", http.StatusOK)
		browser := f.browser(t, store)
		var firstLogin hostedLogin
		var firstCode string
		for i := range hostedCodeLimit {
			login, accepted := f.complete(t, browser, client)
			callback, _ := assertHostedCompletion(t, login, accepted)
			if i == 0 {
				firstLogin, firstCode = login, callback.Query().Get("code")
			}
		}
		waiting := beginHostedSignedLogin(t, browser, f.poolID, client, "Entra")
		subject := saml.NameID{Format: string(saml.PersistentNameIDFormat), Value: "workshop@example.invalid"}
		before := f.slots(t, store.db)
		requireNoHostedResult(t, f.idp.post(t, browser, waiting, "Entra", subject, hostedObjectIDs(fixtureEntraAlice)))
		if after := f.slots(t, store.db); !bytes.Equal(before[0], after[0]) || !bytes.Equal(before[1], after[1]) {
			t.Fatal("capacity refusal mutated the pending login or existing grants")
		}
		first := hostedServe(browser, hostedExchangeRequest(firstLogin, firstCode, ""))
		if first.Code != http.StatusOK {
			t.Fatal("preexisting grant unusable at capacity", first.Code)
		}
		accepted := f.idp.post(t, browser, waiting, "Entra", subject, hostedObjectIDs(fixtureEntraAlice))
		redeemHostedSignedLogin(t, browser, waiting, accepted)
	})
}
