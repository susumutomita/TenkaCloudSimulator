package entraidp

import (
	"net/mail"
	"strings"
	"testing"
)

func TestSyntheticEmailCanonicalAndBoundToDirectoryIdentity(t *testing.T) {
	const tenant = "02a94351-0532-4f1f-b1bd-d7362471dd30"
	const object = "ae64c6ed-b921-462c-98c9-d620c2b3e7cd"
	const exact = "user-" + object + "@tenant-" + tenant + ".entra.invalid"
	address, err := syntheticEmail(tenant, object)
	parsed, parseErr := mail.ParseAddress(address)
	if err != nil || address != exact || parseErr != nil || parsed.Address != exact || parsed.Name != "" || len(address) > 254 {
		t.Fatal("invalid synthetic mailbox shape")
	}
	for _, invalid := range []string{"", strings.ToUpper(tenant), tenant + "\n", "00000000-0000-0000-0000-000000000000", "ae64c6ed-b921-462c-78c9-d620c2b3e7cd"} {
		if got, err := syntheticEmail(invalid, object); err == nil || got != "" {
			t.Fatal("accepted an unsupported tenant ID shape")
		}
		if got, err := syntheticEmail(tenant, invalid); err == nil || got != "" {
			t.Fatal("accepted an unsupported object ID shape")
		}
	}
	first, err := newDirectory()
	if err != nil {
		t.Fatal("synthetic directory provisioning failed", err)
	}
	second, err := newDirectory()
	if err != nil || first.Email() == second.Email() || !strings.HasSuffix(first.Email(), ".invalid") {
		t.Fatal("independent synthetic accounts must not share a deliverable or duplicate mailbox")
	}
}
