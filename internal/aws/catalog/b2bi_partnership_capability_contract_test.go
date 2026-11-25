package catalog

import (
	"reflect"
	"testing"
)

// AWS B2B Data Interchange defines a partnership as a profile with one or
// more capabilities. Omission on update leaves the association unchanged.
func TestB2BIPartnershipCapabilityContract(t *testing.T) {
	h := businessHarness(t, b2bi)
	call := func(op string, in map[string]any) reply { return h.json1("b2bi", op, in) }
	profile := call("CreateProfile", map[string]any{
		"name": "test", "businessName": "Test", "phone": "+1 555 0100", "logging": "DISABLED",
	})
	expect(t, profile, 200, "")
	profileID := str(profile.body["profileId"])
	makePartnership := func(name string, capabilities []any) reply {
		return call("CreatePartnership", map[string]any{"profileId": profileID, "name": name, "email": "ops@example.com", "capabilities": capabilities})
	}
	kept := []any{"ca-fixture"}
	valid := makePartnership("valid", kept)
	expect(t, valid, 200, "")
	id := str(valid.body["partnershipId"])
	t.Run("create rejects empty without a new row", func(t *testing.T) {
		got := makePartnership("should-not-exist", []any{})
		if got.status != 400 {
			t.Errorf("CreatePartnership capabilities=[] returned HTTP %d, want 400", got.status)
		} else {
			expect(t, got, 400, "ValidationException")
		}
		listed := items(t, call("ListPartnerships", map[string]any{"profileId": profileID}), "partnerships")
		if len(listed) != 1 {
			t.Errorf("got %d persisted partnerships, want 1", len(listed))
		}
	})
	t.Run("update rejects empty but permits omission", func(t *testing.T) {
		renamed := call("UpdatePartnership", map[string]any{"partnershipId": id, "name": "renamed"})
		expect(t, renamed, 200, "")
		if !reflect.DeepEqual(renamed.body["capabilities"], kept) {
			t.Fatal("omitting capabilities changed the associated set")
		}
		empty := call("UpdatePartnership", map[string]any{"partnershipId": id, "name": "should-not-persist", "capabilities": []any{}})
		if empty.status != 400 {
			t.Errorf("UpdatePartnership capabilities=[] returned HTTP %d, want 400", empty.status)
		} else {
			expect(t, empty, 400, "ValidationException")
		}
		get := call("GetPartnership", map[string]any{"partnershipId": id})
		expect(t, get, 200, "")
		if get.body["name"] != "renamed" || !reflect.DeepEqual(get.body["capabilities"], kept) {
			t.Error("update with empty capabilities mutated the stored row")
		}
	})
	t.Run("accepts replacement with valid IDs", func(t *testing.T) {
		replacements := []any{"ca-new", "ca-second"}
		updated := call("UpdatePartnership", map[string]any{"partnershipId": id, "capabilities": replacements})
		expect(t, updated, 200, "")
		read := call("GetPartnership", map[string]any{"partnershipId": id})
		expect(t, read, 200, "")
		if !reflect.DeepEqual(read.body["capabilities"], replacements) {
			t.Fatal("nonempty replacement did not persist")
		}
	})
}
