package cognitoidp

import (
	"encoding/xml"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/crewjam/saml"
)

func TestHostedStillLiveCodeDoesNotOutliveSelectedProviderMetadata(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		expiring, current := newHostedSignedIDP(t), newHostedSignedIDP(t)
		entra := expiring.provider("Entra")
		var metadata samlMetadataEntity
		if err := xml.Unmarshal([]byte(entra.MetadataFile), &metadata); err != nil || len(metadata.Roles) != 1 || metadata.Roles[0].Expires == nil {
			t.Fatal("fixture does not have a real role expiration", err)
		}
		validUntil := time.Now().UTC().Truncate(time.Second).Add(15 * time.Second)
		old := `validUntil="` + metadata.Roles[0].Expires.Format(time.RFC3339) + `"`
		if !strings.Contains(entra.MetadataFile, old) {
			t.Fatal("fixture role expiration was not encoded")
		}
		entra.MetadataFile = strings.Replace(entra.MetadataFile, old, `validUntil="`+validUntil.Format(time.RFC3339)+`"`, 1)
		b, publisher, scope, pool, client := newHostedFixture(t, entra, current.provider("Keycloak"))
		cfg, revision, err := publisher.configuration(t.Context(), pool)
		if err != nil || cfg.providerIndex("Entra") < 0 ||
			!cfg.Providers[cfg.providerIndex("Entra")].MetadataExpires.Equal(validUntil) {
			t.Fatal("the selected provider did not commit the intended metadata validity", err)
		}

		// Start and finish at the actual signed application endpoints while the
		// operator's selected IdP is current. The app can still hold this code
		// when the directory publication expires.
		l := beginHostedSignedLogin(t, b, pool, client, "Entra")
		subject := saml.NameID{Format: string(saml.PersistentNameIDFormat), Value: "fixture@example.invalid"}
		reply := expiring.post(t, b, l, "Entra", subject, hostedObjectIDs(fixtureEntraAlice))
		callback, _ := assertHostedCompletion(t, l, reply)
		code := callback.Query().Get("code")

		// Advance only the synctest virtual clock. The code's ordinary TTL
		// is one minute, so it remains live at this earlier directory cutoff.
		<-time.After(time.Until(validUntil))
		if now := time.Now().UTC(); !now.Equal(validUntil) {
			t.Fatal("test did not reach the exact exclusive metadata deadline")
		}
		err = b.transition(t.Context(), scope, pool, &revision, func(_ federationConfig, _ federationRevision, state *hostedState) (bool, error) {
			g, ok := state.Codes[hostedDigest(code)]
			if !ok || g.Provider != "Entra" || g.Client != client || !validUntil.Before(g.Until) {
				t.Fatal("the rejected code was not still live under the unchanged configured registration")
			}
			return false, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		requireNoHostedResult(t, hostedServe(b, hostedExchangeRequest(l, code, hostedOrigin(callback))))

		// The same client still works with its other already enrolled and
		// independently signed IdP. No edit, restart or replacement is needed.
		healthy := beginHostedSignedLogin(t, b, pool, client, "Keycloak")
		redeemHostedSignedLogin(t, b, healthy, current.post(t, b, healthy, "Keycloak", subject))
	})
}
