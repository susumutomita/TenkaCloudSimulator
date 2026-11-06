package catalog

import (
	"strings"
	"testing"
)

// The insert transaction rechecks a parent read before authorization.
func TestParentIsRecheckedInsideTheInsertTransaction(t *testing.T) {
	h := newHarness(t)
	expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=c1", map[string]any{"network": "n"}), 200, "")
	tx, err := h.engine.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	name := "projects/tenkacloud-local/locations/us-central1/clusters/c1"
	if err := liveParent(tx, name); err != nil {
		t.Fatalf("a live parent must pass: %v", err)
	}
	if err := liveParent(tx, name+"-gone"); err == nil || err.(*status).code != "NOT_FOUND" {
		t.Fatalf("a deleted parent must be refused: %v", err)
	}
}

// Patches are charged against the aggregate budget and roll back.
func TestPatchesRespectTheAggregateMetadataBudget(t *testing.T) {
	h := newHarness(t)
	expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=c1", map[string]any{"network": "n"}), 200, "")
	db := h.engine.store.db
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	total, err := storedBytes(tx)
	if err != nil {
		t.Fatal(err)
	}
	filler := `{"x":"` + strings.Repeat("f", int(maxStoredBytes-total)-64) + `"}`
	if _, err := tx.Exec("INSERT INTO gcp_catalog_resources VALUES('filler','projects/filler/f','projects/filler','filler','f',?,999999,1)", filler); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	expect(t, h.call("owner", "PATCH", loc+"/clusters/c1?updateMask=displayName", map[string]any{"displayName": strings.Repeat("d", 200)}), 429, "RESOURCE_EXHAUSTED")
	if got := h.call("owner", "GET", loc+"/clusters/c1", nil).body["displayName"]; got != nil {
		t.Fatalf("a refused patch must roll back, got %v", got)
	}
	expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=c2", map[string]any{"network": strings.Repeat("n", 200)}), 429, "RESOURCE_EXHAUSTED")
}
