package catalog

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type pcsHistoricalRow struct {
	Service, Region, Kind, Parent, ID, Name, ARN, Document, Tags string
	Seq, Generation                                              int64
}

// These bytes were captured from an actual clean pre-canonical candidate,
// not synthesized by the new ARN formatter and not described as released main.
func pcsHistoricalHarness(t *testing.T) (*harness, string, string, string) {
	t.Helper()
	data, err := os.ReadFile("../../../test/fixtures/pcs-flat-88c85d9.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != "91e5d62fe7c40441b41ea4169c8acc126e3cc599349967ba61980446c52a92fd" {
		t.Fatal("historical fixture bytes changed")
	}
	var fixture struct {
		Version      int
		Producer     map[string]string
		Schema       []string
		Resources    []pcsHistoricalRow
		NextSequence int64
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || fixture.Producer["sourceSha"] != "88c85d9dcf36417a78081c59310911707207d670" || fixture.Producer["binarySha256"] != "dab3f5016d96de9550fdfd3ce8097090d2485e14ed166292640fe682143f41c9" || fixture.Producer["epoch"] != "pre-canonical candidate, not released main" || len(fixture.Resources) != 3 || len(fixture.Schema) != 5 {
		t.Fatal("unexpected historical source epoch")
	}
	path := filepath.Join(t.TempDir(), "historical.db")
	original, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = original.Close() })
	db := original.Database()
	for _, ddl := range fixture.Schema {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]string{}
	for _, r := range fixture.Resources {
		if r.Service != "pcs" || r.Region != "us-east-1" || r.Generation != 1 {
			t.Fatal("nonfixture row")
		}
		ids[r.Kind] = r.ID
		if _, err := db.Exec("INSERT INTO aws_catalog_resources("+columns+") VALUES(?,?,?,?,?,?,?,?,?,?,?)", r.Service, r.Region, r.Kind, r.Parent, r.ID, r.Name, r.ARN, r.Document, r.Tags, r.Seq, r.Generation); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO aws_catalog_sequence VALUES(1,?)", fixture.NextSequence); err != nil {
		t.Fatal(err)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	h := &harness{t: t, db: reopened.Database(), policy: &policy{deny: map[string]bool{}}, now: time.Date(2026, 11, 29, 0, 0, 0, 0, time.UTC)}
	h.engine, err = New(h.db, Config{Regions: []string{"us-east-1", "ap-northeast-1"}, Account: "000000000000", DefaultPrincipal: "simulator-admin", Authorize: h.policy.authorize, Now: func() time.Time { return h.now }}, []*Service{pcs})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pcsRawRows(t, h.db), fixture.Resources) {
		t.Fatal("opening migrated or duplicated historical rows")
	}
	var next int64
	if err := h.db.QueryRow("SELECT next FROM aws_catalog_sequence WHERE id=1").Scan(&next); err != nil || next != fixture.NextSequence {
		t.Fatal("opening changed historical sequence", err)
	}
	h.engine.cfg.AuthorizeSnapshot = pcsPolicySnapshot(h.policy)
	return h, ids["cluster"], ids["computeNodeGroup"], ids["queue"]
}

func pcsRawRows(t *testing.T, db *sql.DB) []pcsHistoricalRow {
	t.Helper()
	rows, err := db.Query("SELECT " + columns + " FROM aws_catalog_resources ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []pcsHistoricalRow
	for rows.Next() {
		var r pcsHistoricalRow
		if err := rows.Scan(&r.Service, &r.Region, &r.Kind, &r.Parent, &r.ID, &r.Name, &r.ARN, &r.Document, &r.Tags, &r.Seq, &r.Generation); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
