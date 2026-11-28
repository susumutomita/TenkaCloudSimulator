package cognitoidp

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHostedPreflightRequiresResponseDocument(t *testing.T) {
	metadata := federationXMLFixture(time.Now().UTC(), federationCertFixture(t, time.Now().UTC(), 51))
	const response = `<p:Response xmlns:p="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:s="urn:oasis:names:tc:SAML:2.0:assertion"><s:Assertion><s:Subject><s:NameID>training-user</s:NameID></s:Subject></s:Assertion></p:Response>`
	if err := xmlGuard([]byte(response), map[string]bool{}); err != nil {
		t.Fatal("supported response structure rejected by preflight", err)
	}
	for _, tc := range []struct{ name, document string }{
		{"metadata accidentally posted instead of a response", metadata},
		{"response followed by gateway diagnostic text", response + "Upstream returned no signed result"},
		{"two response documents concatenated", response + response},
		{"provider extension exceeds structural nesting limit", strings.Replace(response, "</s:Assertion>", strings.Repeat("<s:Advice>", 32)+strings.Repeat("</s:Advice>", 32)+"</s:Assertion>", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := xmlGuard([]byte(tc.document), map[string]bool{}); !errors.Is(err, errInvalid) {
				t.Fatal("unsupported document shape passed local preflight", err)
			}
		})
	}
}
