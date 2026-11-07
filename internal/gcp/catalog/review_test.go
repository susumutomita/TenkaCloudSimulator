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
	if err := liveParent(tx, "alloydb", name); err != nil {
		t.Fatalf("a live parent must pass: %v", err)
	}
	if err := liveParent(tx, "alloydb", name+"-gone"); err == nil || err.(*status).code != "NOT_FOUND" {
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

// An AlloyDB cluster's initial user password is refused like a user's, so
// it is never stored or echoed.
func TestAlloyDBClusterInitialUserPasswordIsRefused(t *testing.T) {
	network := map[string]any{"networkConfig": map[string]any{"network": "projects/p/global/networks/default"}}
	withPassword := map[string]any{"initialUser": map[string]any{"user": "postgres", "password": "secret"}, "networkConfig": network["networkConfig"]}
	if err := clusterNetwork(withPassword); err == nil {
		t.Fatal("initialUser.password was accepted")
	}
	withoutPassword := map[string]any{"initialUser": map[string]any{"user": "postgres"}, "networkConfig": network["networkConfig"]}
	if err := clusterNetwork(withoutPassword); err != nil {
		t.Fatalf("initialUser without password: %v", err)
	}
}

// A Compute patch is checked against the recursively merged document it
// would store, without changing the current resource.
func TestComputeTransitionsValidateTheRecursivelyMergedDocument(t *testing.T) {
	current := map[string]any{"name": "r", "bgp": map[string]any{"asn": int64(64512)}}
	body := map[string]any{"bgp": map[string]any{"advertiseMode": "CUSTOM"}}
	if err := computeMerged(computeRouter)(current, body); err != nil {
		t.Fatalf("advertiseMode-only patch: %v", err)
	}
	if _, ok := current["bgp"].(map[string]any)["advertiseMode"]; ok {
		t.Fatal("the current resource was changed")
	}
	if err := computeMerged(computeRouter)(current, map[string]any{"bgp": map[string]any{"asn": int64(1)}}); err == nil {
		t.Fatal("a public ASN was accepted")
	}
	list := []any{"a"}
	if copied := copyJSON(list).([]any); &copied[0] == &list[0] {
		t.Fatal("arrays are not copied")
	}
}
