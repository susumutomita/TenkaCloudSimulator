package metrics

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This frame includes foreign fixture tables and malformed startup schemas too.
// Values (including the private cursor key) are hashed, never printed.
func metricsStoreCoverageFrame(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name`)
	if err != nil {
		t.Fatal("schema frame unavailable")
	}
	tables := []string{}
	schemaRows := [][]any{}
	for rows.Next() {
		var kind, name, parent string
		var definition sql.NullString
		if err := rows.Scan(&kind, &name, &parent, &definition); err != nil {
			t.Fatal("schema frame scan failed")
		}
		schemaRows = append(schemaRows, []any{kind, name, parent, definition})
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal("schema frame iteration failed")
	}
	if err := rows.Close(); err != nil {
		t.Fatal("schema frame close failed")
	}
	state := map[string]any{"schema": schemaRows}
	for _, table := range tables {
		query := `SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`
		probe, err := db.Query(query + " LIMIT 0")
		if err != nil {
			t.Fatal("frame column probe failed")
		}
		columns, err := probe.Columns()
		if err != nil {
			t.Fatal("frame columns unavailable")
		}
		probe.Close()
		order := make([]string, len(columns))
		for i := range columns {
			order[i] = strconv.Itoa(i + 1)
		}
		data, err := db.Query(query + " ORDER BY " + strings.Join(order, ","))
		if err != nil {
			t.Fatal("row frame unavailable")
		}
		values := [][]any{}
		for data.Next() {
			row := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range row {
				dest[i] = &row[i]
			}
			if err := data.Scan(dest...); err != nil {
				t.Fatal("row frame scan failed")
			}
			values = append(values, row)
		}
		if err := data.Err(); err != nil {
			t.Fatal("row frame iteration failed")
		}
		if err := data.Close(); err != nil {
			t.Fatal("row frame close failed")
		}
		state[table] = values
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal("frame encoding failed")
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
func metricsStoreCoverageUnchanged(t *testing.T, db *sql.DB, before string) {
	t.Helper()
	if metricsStoreCoverageFrame(t, db) != before {
		t.Fatal("durable SQLite frame changed on refusal")
	}
}
func metricsStoreCoverageExec(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatal("controlled fixture SQL failed")
	}
}
func metricsStoreCoverageCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal("observable count unavailable")
	}
	return n
}
func metricsStoreCoverageInternal(t *testing.T, err error) {
	t.Helper()
	code(t, err, "InternalServiceError")
	if strings.Contains(err.Error(), "private-store-fixture") {
		t.Fatal("private SQL detail escaped")
	}
}
func metricsStoreCoverageInput(name string) PutInput {
	return PutInput{Namespace: "Practice/Store", Data: []Datum{{MetricName: name, Value: 7}}}
}
func metricsStoreCoverageRawDB(t *testing.T) *sql.DB {
	return openDB(t, filepath.Join(t.TempDir(), "store-contract.sqlite"))
}

func TestMetricsStoreCoverageConstructorAdmissionAndRecovery(t *testing.T) {
	t.Run("foreign-keys-required", func(t *testing.T) {
		db := metricsStoreCoverageRawDB(t)
		metricsStoreCoverageExec(t, db, "PRAGMA foreign_keys=OFF")
		before := metricsStoreCoverageFrame(t, db)
		got, err := NewStore(context.Background(), db, Config{})
		metricsStoreCoverageInternal(t, err)
		if got != nil {
			t.Fatal("refused startup returned a store")
		}
		metricsStoreCoverageUnchanged(t, db, before)
		metricsStoreCoverageExec(t, db, "PRAGMA foreign_keys=ON")
		got, err = NewStore(context.Background(), db, Config{})
		if err != nil || !got.UsesDatabase(db) {
			t.Fatal("repaired startup did not bind borrowed database")
		}
		if err := got.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Recovered")); err != nil {
			t.Fatal("default host clock could not accept current data")
		}
	})
	t.Run("invalid-trusted-budget-no-effects", func(t *testing.T) {
		_, db, _, _ := fixture(t, Limits{})
		before := metricsStoreCoverageFrame(t, db)
		cases := []Limits{{1001, 100000, 64 << 20, 64, 500}, {1000, 100001, 64 << 20, 64, 500}, {1000, 100000, (64 << 20) + 1, 64, 500}, {1000, 100000, 64 << 20, 65, 500}, {1000, 100000, 64 << 20, 64, 501}, {1000, 100000, 64 << 20, 64, 0}}
		for i, limits := range cases {
			t.Run(strconv.Itoa(i), func(t *testing.T) {
				got, err := NewStore(context.Background(), db, Config{Limits: limits})
				code(t, err, "InvalidParameterValue")
				if got != nil {
					t.Fatal("invalid configuration returned store")
				}
				metricsStoreCoverageUnchanged(t, db, before)
			})
		}
	})
	t.Run("cancelled-and-closed-database", func(t *testing.T) {
		db := metricsStoreCoverageRawDB(t)
		before := metricsStoreCoverageFrame(t, db)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, err := NewStore(ctx, db, Config{})
		metricsStoreCoverageInternal(t, err)
		if got != nil {
			t.Fatal("cancelled startup returned store")
		}
		metricsStoreCoverageUnchanged(t, db, before)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		got, err = NewStore(context.Background(), db, Config{})
		metricsStoreCoverageInternal(t, err)
		if got != nil {
			t.Fatal("closed database returned store")
		}
	})
}

func TestMetricsStoreCoverageMalformedSchemaRefusesAtomicallyAndRepairs(t *testing.T) {
	cases := []struct{ name, table, ddl string }{
		{"column-count", "aws_metrics_meta", `CREATE TABLE aws_metrics_meta(singleton INTEGER PRIMARY KEY,account TEXT)`},
		{"column-order", "aws_metrics_meta", `CREATE TABLE aws_metrics_meta(singleton INTEGER PRIMARY KEY,secret BLOB,account TEXT,configuration TEXT)`},
		{"missing-primary-key", "aws_metrics_regions", `CREATE TABLE aws_metrics_regions(region TEXT NOT NULL,revision INTEGER NOT NULL)`},
		{"wrong-primary-key-order", "aws_metrics_series", `CREATE TABLE aws_metrics_series(region TEXT,identity TEXT,namespace TEXT,name TEXT,document TEXT,last_ingested INTEGER,PRIMARY KEY(identity,region),FOREIGN KEY(region) REFERENCES aws_metrics_regions(region) ON DELETE RESTRICT)`},
		{"missing-foreign-key", "aws_metrics_points", `CREATE TABLE aws_metrics_points(id INTEGER PRIMARY KEY AUTOINCREMENT,region TEXT,series_id TEXT,stamp INTEGER,unit TEXT,document TEXT)`},
		{"cascade-is-not-restrict", "aws_metrics_series", `CREATE TABLE aws_metrics_series(region TEXT,identity TEXT,namespace TEXT,name TEXT,document TEXT,last_ingested INTEGER,PRIMARY KEY(region,identity),FOREIGN KEY(region) REFERENCES aws_metrics_regions(region) ON DELETE CASCADE)`},
		{"foreign-key-update-cascade", "aws_metrics_series", `CREATE TABLE aws_metrics_series(region TEXT,identity TEXT,namespace TEXT,name TEXT,document TEXT,last_ingested INTEGER,PRIMARY KEY(region,identity),FOREIGN KEY(region) REFERENCES aws_metrics_regions(region) ON UPDATE CASCADE ON DELETE RESTRICT)`},
		{"two-foreign-key-groups", "aws_metrics_series", `CREATE TABLE aws_metrics_series(region TEXT,identity TEXT,namespace TEXT,name TEXT,document TEXT,last_ingested INTEGER,PRIMARY KEY(region,identity),FOREIGN KEY(region) REFERENCES aws_metrics_regions(region) ON DELETE RESTRICT,FOREIGN KEY(namespace) REFERENCES store_coverage_peer(id) ON DELETE RESTRICT)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := metricsStoreCoverageRawDB(t)
			metricsStoreCoverageExec(t, db, `CREATE TABLE store_coverage_peer(id TEXT PRIMARY KEY);INSERT INTO store_coverage_peer VALUES('peer-preserved')`)
			metricsStoreCoverageExec(t, db, tc.ddl)
			before := metricsStoreCoverageFrame(t, db)
			got, err := NewStore(context.Background(), db, Config{})
			metricsStoreCoverageInternal(t, err)
			if got != nil {
				t.Fatal("incompatible schema was adopted")
			}
			metricsStoreCoverageUnchanged(t, db, before)
			metricsStoreCoverageExec(t, db, "DROP TABLE "+tc.table)
			got, err = NewStore(context.Background(), db, Config{})
			if err != nil {
				t.Fatal("explicit schema repair failed")
			}
			if metricsStoreCoverageCount(t, db, "SELECT count(*) FROM store_coverage_peer WHERE id='peer-preserved'") != 1 {
				t.Fatal("foreign table changed during schema repair")
			}
			if err := got.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("AfterRepair")); err != nil {
				t.Fatal("repaired schema was not operational")
			}
		})
	}
}

func TestMetricsStoreCoverageMetadataRefusalAndExactRepair(t *testing.T) {
	for _, kind := range []string{"account", "key-length", "configuration", "missing-binding"} {
		t.Run(kind, func(t *testing.T) {
			s, db, now, _ := fixture(t, Limits{})
			put(t, s, "us-west-2", "Practice/Peer", "Peer", 3, nil, nil)
			var account, configuration string
			var secret []byte
			if err := db.QueryRow("SELECT account,secret,configuration FROM aws_metrics_meta WHERE singleton=1").Scan(&account, &secret, &configuration); err != nil {
				t.Fatal("saved metadata unavailable")
			}
			switch kind {
			case "account":
				metricsStoreCoverageExec(t, db, "UPDATE aws_metrics_meta SET account='private-store-fixture'")
			case "key-length":
				metricsStoreCoverageExec(t, db, "UPDATE aws_metrics_meta SET secret=x'00'")
			case "configuration":
				metricsStoreCoverageExec(t, db, "UPDATE aws_metrics_meta SET configuration='private-store-fixture'")
			case "missing-binding":
				metricsStoreCoverageExec(t, db, "DELETE FROM aws_metrics_meta")
			}
			before := metricsStoreCoverageFrame(t, db)
			got, err := NewStore(context.Background(), db, Config{Clock: func() time.Time { return *now }})
			metricsStoreCoverageInternal(t, err)
			if got != nil {
				t.Fatal("corrupt metadata was adopted")
			}
			metricsStoreCoverageUnchanged(t, db, before)
			metricsStoreCoverageInternal(t, s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Refused")))
			metricsStoreCoverageUnchanged(t, db, before)
			metricsStoreCoverageExec(t, db, "DELETE FROM aws_metrics_meta")
			metricsStoreCoverageExec(t, db, "INSERT INTO aws_metrics_meta(singleton,account,secret,configuration)VALUES(1,?,?,?)", account, secret, configuration)
			if _, err := NewStore(context.Background(), db, Config{Clock: func() time.Time { return *now }}); err != nil {
				t.Fatal("restored persistent metadata did not reopen")
			}
			if err := s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Recovered")); err != nil {
				t.Fatal("restored handle remained unusable")
			}
			if metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_points WHERE region='us-west-2'") != 1 {
				t.Fatal("peer region changed")
			}
		})
	}
}

func TestMetricsStoreCoverageConstructorCommitFailureAndIgnoredBootstrap(t *testing.T) {
	for _, kind := range []string{"ignored-meta-insert", "deferred-foreign-key"} {
		t.Run(kind, func(t *testing.T) {
			_, db, now, _ := fixture(t, Limits{})
			metricsStoreCoverageExec(t, db, "DELETE FROM aws_metrics_meta")
			if kind == "ignored-meta-insert" {
				metricsStoreCoverageExec(t, db, `CREATE TRIGGER store_coverage_start_fault BEFORE INSERT ON aws_metrics_meta BEGIN SELECT RAISE(IGNORE); END`)
			} else {
				metricsStoreCoverageExec(t, db, `CREATE TABLE store_coverage_parent(id TEXT PRIMARY KEY);CREATE TABLE store_coverage_child(id TEXT REFERENCES store_coverage_parent(id) DEFERRABLE INITIALLY DEFERRED);CREATE TRIGGER store_coverage_start_fault AFTER INSERT ON aws_metrics_meta BEGIN INSERT INTO store_coverage_child VALUES('private-store-fixture'); END`)
			}
			before := metricsStoreCoverageFrame(t, db)
			got, err := NewStore(context.Background(), db, Config{Clock: func() time.Time { return *now }})
			metricsStoreCoverageInternal(t, err)
			if got != nil {
				t.Fatal("failed bootstrap acknowledged a store")
			}
			metricsStoreCoverageUnchanged(t, db, before)
			metricsStoreCoverageExec(t, db, "DROP TRIGGER store_coverage_start_fault")
			got, err = NewStore(context.Background(), db, Config{Clock: func() time.Time { return *now }})
			if err != nil {
				t.Fatal("bootstrap repair failed")
			}
			if metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_meta WHERE singleton=1 AND length(secret)=32") != 1 {
				t.Fatal("repair did not save one complete binding")
			}
			if err := got.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Ready")); err != nil {
				t.Fatal("repaired store is not operational")
			}
		})
	}
}

func TestMetricsStoreCoverageConstructorOrphanFramesAndRepair(t *testing.T) {
	for _, kind := range []string{"series-without-region", "points-without-series"} {
		t.Run(kind, func(t *testing.T) {
			s, db, now, _ := fixture(t, Limits{})
			put(t, s, "us-east-1", "Practice/Store", "Owned", 1, nil, nil)
			put(t, s, "us-west-2", "Practice/Peer", "Peer", 2, nil, nil)
			var revision int64
			if err := db.QueryRow("SELECT revision FROM aws_metrics_regions WHERE region='us-east-1'").Scan(&revision); err != nil {
				t.Fatal(err)
			}
			var id, namespace, name, document string
			var ingested int64
			if err := db.QueryRow("SELECT identity,namespace,name,document,last_ingested FROM aws_metrics_series WHERE region='us-east-1'").Scan(&id, &namespace, &name, &document, &ingested); err != nil {
				t.Fatal(err)
			}
			// Explicitly corrupt persisted rows outside API admission, then re-enable FK
			// enforcement before testing startup. Normal APIs cannot create this seed.
			metricsStoreCoverageExec(t, db, "PRAGMA foreign_keys=OFF")
			if kind == "series-without-region" {
				metricsStoreCoverageExec(t, db, "DELETE FROM aws_metrics_regions WHERE region='us-east-1'")
			} else {
				metricsStoreCoverageExec(t, db, "DELETE FROM aws_metrics_series WHERE region='us-east-1'")
			}
			metricsStoreCoverageExec(t, db, "PRAGMA foreign_keys=ON")
			before := metricsStoreCoverageFrame(t, db)
			got, err := NewStore(context.Background(), db, Config{Clock: func() time.Time { return *now }})
			metricsStoreCoverageInternal(t, err)
			if got != nil {
				t.Fatal("orphan state was adopted")
			}
			metricsStoreCoverageUnchanged(t, db, before)
			if kind == "series-without-region" {
				metricsStoreCoverageExec(t, db, "INSERT INTO aws_metrics_regions(region,revision)VALUES('us-east-1',?)", revision)
			} else {
				metricsStoreCoverageExec(t, db, "INSERT INTO aws_metrics_series(region,identity,namespace,name,document,last_ingested)VALUES('us-east-1',?,?,?,?,?)", id, namespace, name, document, ingested)
			}
			got, err = NewStore(context.Background(), db, Config{Clock: func() time.Time { return *now }})
			if err != nil {
				t.Fatal("explicit orphan repair did not reopen")
			}
			if err := got.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Recovered")); err != nil {
				t.Fatal("repaired store unusable")
			}
			if metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_points WHERE region='us-west-2'") != 1 {
				t.Fatal("peer points changed")
			}
		})
	}
}

func TestMetricsStoreCoveragePutMandatoryAcknowledgementsAndRepair(t *testing.T) {
	cases := []struct{ name, seed, trigger string }{
		{"region-insert-ignore", "", `CREATE TRIGGER store_coverage_write_fault BEFORE INSERT ON aws_metrics_regions WHEN NEW.region='us-east-1' BEGIN SELECT RAISE(IGNORE);END`},
		{"region-insert-abort", "", `CREATE TRIGGER store_coverage_write_fault BEFORE INSERT ON aws_metrics_regions WHEN NEW.region='us-east-1' BEGIN SELECT RAISE(ABORT,'private-store-fixture');END`},
		{"existing-catalog-update-ignore", "seed", `CREATE TRIGGER store_coverage_write_fault BEFORE UPDATE ON aws_metrics_series WHEN OLD.region='us-east-1' BEGIN SELECT RAISE(IGNORE);END`},
		{"revision-update-ignore", "seed", `CREATE TRIGGER store_coverage_write_fault BEFORE UPDATE ON aws_metrics_regions WHEN OLD.region='us-east-1' BEGIN SELECT RAISE(IGNORE);END`},
		{"revision-postwrite-rewind", "seed", `CREATE TRIGGER store_coverage_write_fault AFTER UPDATE ON aws_metrics_regions WHEN NEW.region='us-east-1' AND NEW.revision=OLD.revision+1 BEGIN UPDATE aws_metrics_regions SET revision=OLD.revision WHERE region=NEW.region;END`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, db, now, _ := fixture(t, Limits{})
			put(t, s, "us-west-2", "Practice/Peer", "Peer", 3, nil, nil)
			if tc.seed != "" {
				put(t, s, "us-east-1", "Practice/Store", "Owned", 1, nil, nil)
			}
			beforeCount := metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_points WHERE region='us-east-1'")
			*now = now.Add(time.Minute)
			metricsStoreCoverageExec(t, db, tc.trigger)
			before := metricsStoreCoverageFrame(t, db)
			metricsStoreCoverageInternal(t, s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Owned")))
			metricsStoreCoverageUnchanged(t, db, before)
			metricsStoreCoverageExec(t, db, "DROP TRIGGER store_coverage_write_fault")
			if err := s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Owned")); err != nil {
				t.Fatal("repaired mandatory write failed")
			}
			if metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_points WHERE region='us-east-1'") != beforeCount+1 || metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_points WHERE region='us-west-2'") != 1 {
				t.Fatal("retry or peer row count mismatch")
			}
		})
	}
}

func TestMetricsStoreCoveragePutCommitFailureRollsBackWholeFrame(t *testing.T) {
	s, db, _, _ := fixture(t, Limits{})
	put(t, s, "us-west-2", "Practice/Peer", "Peer", 3, nil, nil)
	metricsStoreCoverageExec(t, db, `CREATE TABLE store_coverage_parent(id TEXT PRIMARY KEY);CREATE TABLE store_coverage_child(id TEXT REFERENCES store_coverage_parent(id) DEFERRABLE INITIALLY DEFERRED);CREATE TRIGGER store_coverage_put_commit AFTER INSERT ON aws_metrics_points WHEN NEW.region='us-east-1' BEGIN INSERT INTO store_coverage_child VALUES('private-store-fixture');END`)
	before := metricsStoreCoverageFrame(t, db)
	metricsStoreCoverageInternal(t, s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Owned")))
	metricsStoreCoverageUnchanged(t, db, before)
	metricsStoreCoverageExec(t, db, "DROP TRIGGER store_coverage_put_commit")
	if err := s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Owned")); err != nil {
		t.Fatal("commit fault repair failed")
	}
	if metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_points WHERE region='us-east-1'") != 1 || metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_points WHERE region='us-west-2'") != 1 || metricsStoreCoverageCount(t, db, "SELECT count(*) FROM store_coverage_child") != 0 {
		t.Fatal("commit retry was not exact or peer changed")
	}
}

func TestMetricsStoreCoverageOwnExpiryPostconditionsAndRepair(t *testing.T) {
	cases := []struct{ name, trigger string }{
		{"expired-point-reinsert", `CREATE TRIGGER store_coverage_expiry_fault AFTER DELETE ON aws_metrics_points WHEN OLD.region='us-east-1' BEGIN INSERT INTO aws_metrics_points(id,region,series_id,stamp,unit,document)VALUES(OLD.id,OLD.region,OLD.series_id,OLD.stamp,OLD.unit,OLD.document);END`},
		{"empty-catalog-delete-ignore", `CREATE TRIGGER store_coverage_expiry_fault BEFORE DELETE ON aws_metrics_series WHEN OLD.region='us-east-1' BEGIN SELECT RAISE(IGNORE);END`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, db, now, _ := fixture(t, Limits{})
			put(t, s, "us-east-1", "Practice/Store", "Expired", 1, nil, nil)
			put(t, s, "us-west-2", "Practice/Peer", "Peer", 2, nil, nil)
			*now = now.Add(16 * 24 * time.Hour)
			metricsStoreCoverageExec(t, db, tc.trigger)
			before := metricsStoreCoverageFrame(t, db)
			metricsStoreCoverageInternal(t, s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Fresh")))
			metricsStoreCoverageUnchanged(t, db, before)
			metricsStoreCoverageExec(t, db, "DROP TRIGGER store_coverage_expiry_fault")
			if err := s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Fresh")); err != nil {
				t.Fatal("repaired expiry path failed")
			}
			if metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_points WHERE region='us-east-1'") != 1 || metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_series WHERE region='us-east-1' AND name='Expired'") != 0 || metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_points WHERE region='us-west-2'") != 1 {
				t.Fatal("own collection or peer preservation mismatch")
			}
		})
	}
}

func TestMetricsStoreCoverageCorruptRevisionAndCatalogRefuseThenRepair(t *testing.T) {
	for _, kind := range []string{"revision-overflow", "catalog-document-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			s, db, _, _ := fixture(t, Limits{})
			put(t, s, "us-east-1", "Practice/Store", "Owned", 1, nil, nil)
			var revision int64
			var saved string
			if err := db.QueryRow("SELECT revision FROM aws_metrics_regions WHERE region='us-east-1'").Scan(&revision); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("SELECT document FROM aws_metrics_series WHERE region='us-east-1'").Scan(&saved); err != nil {
				t.Fatal(err)
			}
			if kind == "revision-overflow" {
				metricsStoreCoverageExec(t, db, "UPDATE aws_metrics_regions SET revision=? WHERE region='us-east-1'", int64(math.MaxInt64))
			} else {
				metricsStoreCoverageExec(t, db, "UPDATE aws_metrics_series SET document='{}' WHERE region='us-east-1'")
			}
			before := metricsStoreCoverageFrame(t, db)
			metricsStoreCoverageInternal(t, s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Owned")))
			metricsStoreCoverageUnchanged(t, db, before)
			metricsStoreCoverageExec(t, db, "UPDATE aws_metrics_regions SET revision=? WHERE region='us-east-1'", revision)
			metricsStoreCoverageExec(t, db, "UPDATE aws_metrics_series SET document=? WHERE region='us-east-1'", saved)
			if err := s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Owned")); err != nil {
				t.Fatal("corruption repair failed")
			}
			if metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_points") != 2 {
				t.Fatal("refused put left a ghost sample")
			}
		})
	}
}

func TestMetricsStoreCoverageMissingTablesFailClosedThenStartupRepairs(t *testing.T) {
	for _, table := range []string{"aws_metrics_regions", "aws_metrics_points"} {
		t.Run(table, func(t *testing.T) {
			s, db, now, _ := fixture(t, Limits{})
			metricsStoreCoverageExec(t, db, "DROP TABLE "+table)
			before := metricsStoreCoverageFrame(t, db)
			metricsStoreCoverageInternal(t, s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Owned")))
			metricsStoreCoverageUnchanged(t, db, before)
			got, err := NewStore(context.Background(), db, Config{Clock: func() time.Time { return *now }})
			if err != nil {
				t.Fatal("empty missing-table repair failed")
			}
			if err := got.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Owned")); err != nil {
				t.Fatal("repaired table not operational")
			}
			if metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_regions") != 1 || metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_series") != 1 || metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_points") != 1 {
				t.Fatal("repair duplicated or omitted owned state")
			}
		})
	}
}

func TestMetricsStoreCoverageInvalidHostClockAndCancelledPutHaveNoEffects(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	before := metricsStoreCoverageFrame(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	metricsStoreCoverageInternal(t, s.PutMetricData(ctx, Scope{"us-east-1", allow}, metricsStoreCoverageInput("Owned")))
	metricsStoreCoverageUnchanged(t, db, before)
	for _, bad := range []time.Time{{}, time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC)} {
		*now = bad
		metricsStoreCoverageInternal(t, s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Owned")))
		metricsStoreCoverageUnchanged(t, db, before)
	}
	*now = time.Date(2026, 1, 2, 12, 0, 30, 0, time.UTC)
	if err := s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Owned")); err != nil {
		t.Fatal("repaired clock did not permit valid sample")
	}
	var absent *Store
	metricsStoreCoverageInternal(t, absent.PutMetricData(context.Background(), Scope{"us-east-1", allow}, metricsStoreCoverageInput("Owned")))
	if metricsStoreCoverageCount(t, db, "SELECT count(*) FROM aws_metrics_points") != 1 {
		t.Fatal("positive repair did not save exactly one sample")
	}
}
