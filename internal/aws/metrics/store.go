package metrics

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

type Store struct {
	db     *sql.DB
	clock  func() time.Time
	limits Limits
	key    []byte
}

const schema = `
CREATE TABLE IF NOT EXISTS aws_metrics_meta(singleton INTEGER PRIMARY KEY CHECK(singleton=1),account TEXT NOT NULL,secret BLOB NOT NULL,configuration TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS aws_metrics_regions(region TEXT NOT NULL PRIMARY KEY,revision INTEGER NOT NULL CHECK(revision>=0));
CREATE TABLE IF NOT EXISTS aws_metrics_series(region TEXT NOT NULL,identity TEXT NOT NULL,namespace TEXT NOT NULL,name TEXT NOT NULL,document TEXT NOT NULL,last_ingested INTEGER NOT NULL,PRIMARY KEY(region,identity),FOREIGN KEY(region) REFERENCES aws_metrics_regions(region) ON DELETE RESTRICT);
CREATE TABLE IF NOT EXISTS aws_metrics_points(id INTEGER PRIMARY KEY AUTOINCREMENT,region TEXT NOT NULL,series_id TEXT NOT NULL,stamp INTEGER NOT NULL,unit TEXT NOT NULL,document TEXT NOT NULL,FOREIGN KEY(region,series_id) REFERENCES aws_metrics_series(region,identity) ON DELETE RESTRICT);
CREATE INDEX IF NOT EXISTS aws_metrics_points_window ON aws_metrics_points(region,series_id,stamp);`

// NewStore initializes only provider-owned schema at trusted host startup. A
// request must never call this constructor as an authorization shortcut.
func NewStore(ctx context.Context, db *sql.DB, cfg Config) (*Store, error) {
	if db == nil {
		return nil, internal()
	}
	limits := cfg.Limits
	if limits == (Limits{}) {
		limits = defaultLimits
	}
	if limits.Series < 1 || limits.Series > defaultLimits.Series || limits.Points < 1 || limits.Points > defaultLimits.Points || limits.Bytes < 1 || limits.Bytes > defaultLimits.Bytes || limits.Regions < 1 || limits.Regions > defaultLimits.Regions || limits.PageSize < 1 || limits.PageSize > 500 {
		return nil, invalid("Trusted local metric limits are invalid.")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	configuration, err := json.Marshal(struct {
		Version int
		Limits  Limits
	}{1, limits})
	if err != nil {
		return nil, internal()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, internal()
	}
	defer tx.Rollback()
	var fk int
	if tx.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk) != nil || fk != 1 {
		return nil, internal()
	}
	if _, err = tx.ExecContext(ctx, schema); err != nil {
		return nil, internal()
	}
	// Column and primary-key probes reject incompatible existing tables without
	// deleting them. Referential ownership is also checked before startup commits.
	for table, cols := range map[string][]string{
		"aws_metrics_meta":    {"singleton", "account", "secret", "configuration"},
		"aws_metrics_regions": {"region", "revision"},
		"aws_metrics_series":  {"region", "identity", "namespace", "name", "document", "last_ingested"},
		"aws_metrics_points":  {"id", "region", "series_id", "stamp", "unit", "document"},
	} {
		rows, e := tx.QueryContext(ctx, "SELECT * FROM "+table+" LIMIT 0")
		if e != nil {
			return nil, internal()
		}
		got, e := rows.Columns()
		closeErr := rows.Close()
		if e != nil || closeErr != nil || len(got) != len(cols) {
			return nil, internal()
		}
		for i := range cols {
			if got[i] != cols[i] {
				return nil, internal()
			}
		}
	}
	for table, expected := range map[string]map[string]int{
		"aws_metrics_meta": {"singleton": 1}, "aws_metrics_regions": {"region": 1}, "aws_metrics_series": {"region": 1, "identity": 2}, "aws_metrics_points": {"id": 1},
	} {
		rows, e := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if e != nil {
			return nil, internal()
		}
		actual := map[string]int{}
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var fallback any
			if e = rows.Scan(&cid, &name, &kind, &notnull, &fallback, &pk); e != nil {
				rows.Close()
				return nil, internal()
			}
			if pk > 0 {
				actual[name] = pk
			}
		}
		e = rows.Err()
		closeErr := rows.Close()
		if e != nil || closeErr != nil || len(actual) != len(expected) {
			return nil, internal()
		}
		for name, pk := range expected {
			if actual[name] != pk {
				return nil, internal()
			}
		}
	}
	for table, expected := range map[string]map[string]bool{
		"aws_metrics_series": {"aws_metrics_regions|region|region|0|RESTRICT": true},
		"aws_metrics_points": {"aws_metrics_series|region|region|0|RESTRICT": true, "aws_metrics_series|series_id|identity|1|RESTRICT": true},
	} {
		rows, e := tx.QueryContext(ctx, "PRAGMA foreign_key_list("+table+")")
		if e != nil {
			return nil, internal()
		}
		seen := map[string]bool{}
		group := -1
		for rows.Next() {
			var id, seq int
			var parent, from, to, onUpdate, onDelete, match string
			if e = rows.Scan(&id, &seq, &parent, &from, &to, &onUpdate, &onDelete, &match); e != nil {
				rows.Close()
				return nil, internal()
			}
			if group < 0 {
				group = id
			}
			if id != group || onUpdate != "NO ACTION" || match != "NONE" {
				rows.Close()
				return nil, internal()
			}
			seen[fmt.Sprintf("%s|%s|%s|%d|%s", parent, from, to, seq, onDelete)] = true
		}
		e = rows.Err()
		closeErr := rows.Close()
		if e != nil || closeErr != nil || len(seen) != len(expected) {
			return nil, internal()
		}
		for key := range expected {
			if !seen[key] {
				return nil, internal()
			}
		}
	}
	var metaRows, badMeta int
	if err = tx.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(singleton<>1),0) FROM aws_metrics_meta").Scan(&metaRows, &badMeta); err != nil || metaRows > 1 || badMeta != 0 {
		return nil, internal()
	}
	var account, saved string
	var key []byte
	err = tx.QueryRowContext(ctx, "SELECT account,secret,configuration FROM aws_metrics_meta WHERE singleton=1").Scan(&account, &key, &saved)
	if errors.Is(err, sql.ErrNoRows) {
		var count int
		if tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM aws_metrics_regions)+(SELECT count(*) FROM aws_metrics_series)+(SELECT count(*) FROM aws_metrics_points)").Scan(&count) != nil || count != 0 {
			return nil, internal()
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, internal()
		}
		if err = one(tx.ExecContext(ctx, "INSERT INTO aws_metrics_meta(singleton,account,secret,configuration) VALUES(1,?,?,?)", AccountID, key, string(configuration))); err != nil {
			return nil, internal()
		}
	} else if err != nil || account != AccountID || len(key) != 32 || saved != string(configuration) {
		return nil, internal()
	}
	var bad int
	if tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_metrics_series s LEFT JOIN aws_metrics_regions r ON r.region=s.region WHERE r.region IS NULL").Scan(&bad) != nil || bad != 0 {
		return nil, internal()
	}
	if tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_metrics_points p LEFT JOIN aws_metrics_series s ON s.region=p.region AND s.identity=p.series_id WHERE s.identity IS NULL").Scan(&bad) != nil || bad != 0 {
		return nil, internal()
	}
	if err = tx.Commit(); err != nil {
		return nil, internal()
	}
	return &Store{db, clock, limits, append([]byte{}, key...)}, nil
}
func (s *Store) UsesDatabase(db *sql.DB) bool { return s != nil && s.db == db }
func one(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return internal()
	}
	return nil
}
func exact(result sql.Result, err error, want int64) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != want {
		return internal()
	}
	return nil
}
func (s *Store) run(ctx context.Context, scope Scope, action string, work func(*sql.Tx, time.Time) error) error {
	if s == nil || s.db == nil || scope.Authorize == nil {
		return internal()
	}
	if !regionOK(scope.Region) {
		return invalid("A valid local Region is required.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return internal()
	}
	defer tx.Rollback()
	allowed, err := scope.Authorize(ctx, tx, "cloudwatch:"+action, "*")
	if err != nil {
		return internal()
	}
	if !allowed {
		return denied()
	}
	var account, configuration string
	var key []byte
	if err = tx.QueryRowContext(ctx, "SELECT account,secret,configuration FROM aws_metrics_meta WHERE singleton=1").Scan(&account, &key, &configuration); err != nil {
		return internal()
	}
	want, err := json.Marshal(struct {
		Version int
		Limits  Limits
	}{1, s.limits})
	if err != nil {
		return internal()
	}
	if account != AccountID || !hmac.Equal(key, s.key) || configuration != string(want) {
		return internal()
	}
	now := s.clock().UTC()
	if _, err = nano(now); err != nil {
		return internal()
	}
	if err = work(tx, now); err != nil {
		var api *Error
		if errors.As(err, &api) {
			return err
		}
		return internal()
	}
	if err = tx.Commit(); err != nil {
		return internal()
	}
	return nil
}
func identity(metric Metric) (string, string, error) {
	body, err := json.Marshal(metric)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), string(body), nil
}
func (s *Store) PutMetricData(ctx context.Context, scope Scope, input PutInput) error {
	return s.run(ctx, scope, "PutMetricData", func(tx *sql.Tx, now time.Time) error { return s.putTx(ctx, tx, scope.Region, input, now) })
}
func (s *Store) ensureRegion(ctx context.Context, tx *sql.Tx, region string) (int64, error) {
	var revision int64
	err := tx.QueryRowContext(ctx, "SELECT revision FROM aws_metrics_regions WHERE region=?", region).Scan(&revision)
	if err == nil {
		if revision < 0 || revision == math.MaxInt64 {
			return 0, internal()
		}
		return revision, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var n int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_metrics_regions").Scan(&n); err != nil {
		return 0, err
	}
	if n >= s.limits.Regions {
		return 0, quota()
	}
	if err = one(tx.ExecContext(ctx, "INSERT INTO aws_metrics_regions(region,revision) VALUES(?,0)", region)); err != nil {
		return 0, err
	}
	return 0, nil
}
func (s *Store) putTx(ctx context.Context, tx *sql.Tx, region string, input PutInput, now time.Time) error {
	points, err := normalizePut(input, now)
	if err != nil {
		return err
	}
	revision, err := s.ensureRegion(ctx, tx, region)
	if err != nil {
		return err
	}
	// Only this authorized successful Put may collect expired rows. Reads never
	// mutate durable state; expired rows in every other scope still charge quota.
	cutoff := now.Add(-retention).UnixNano()
	var expired int64
	where := `region=? AND stamp<? AND series_id IN (SELECT identity FROM aws_metrics_series WHERE region=? AND namespace=?)`
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_metrics_points WHERE "+where, region, cutoff, region, input.Namespace).Scan(&expired); err != nil {
		return err
	}
	if expired > 0 {
		result, e := tx.ExecContext(ctx, "DELETE FROM aws_metrics_points WHERE "+where, region, cutoff, region, input.Namespace)
		if err = exact(result, e, expired); err != nil {
			return err
		}
		var remaining int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_metrics_points WHERE "+where, region, cutoff, region, input.Namespace).Scan(&remaining); err != nil || remaining != 0 {
			return internal()
		}
	}
	var empty int64
	emptyWhere := `region=? AND namespace=? AND last_ingested<? AND NOT EXISTS(SELECT 1 FROM aws_metrics_points p WHERE p.region=aws_metrics_series.region AND p.series_id=aws_metrics_series.identity)`
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_metrics_series WHERE "+emptyWhere, region, input.Namespace, now.Add(-listingAge).UnixNano()).Scan(&empty); err != nil {
		return err
	}
	if empty > 0 {
		result, e := tx.ExecContext(ctx, "DELETE FROM aws_metrics_series WHERE "+emptyWhere, region, input.Namespace, now.Add(-listingAge).UnixNano())
		if err = exact(result, e, empty); err != nil {
			return err
		}
	}
	for _, entry := range points {
		id, document, e := identity(entry.Metric)
		if e != nil {
			return e
		}
		var saved string
		e = tx.QueryRowContext(ctx, "SELECT document FROM aws_metrics_series WHERE region=? AND identity=?", region, id).Scan(&saved)
		if errors.Is(e, sql.ErrNoRows) {
			if e = one(tx.ExecContext(ctx, "INSERT INTO aws_metrics_series(region,identity,namespace,name,document,last_ingested) VALUES(?,?,?,?,?,?)", region, id, entry.Metric.Namespace, entry.Metric.Name, document, now.UnixNano())); e != nil {
				return e
			}
		} else if e != nil {
			return e
		} else {
			if saved != document {
				return internal()
			}
			if e = one(tx.ExecContext(ctx, "UPDATE aws_metrics_series SET last_ingested=? WHERE region=? AND identity=?", now.UnixNano(), region, id)); e != nil {
				return e
			}
		}
		body, e := json.Marshal(entry.Point)
		if e != nil {
			return e
		}
		if e = one(tx.ExecContext(ctx, "INSERT INTO aws_metrics_points(region,series_id,stamp,unit,document) VALUES(?,?,?,?,?)", region, id, entry.Point.Timestamp, entry.Point.Unit, string(body))); e != nil {
			return e
		}
	}
	var seriesCount, pointCount, bytes int64
	if err = tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM aws_metrics_series),(SELECT count(*) FROM aws_metrics_points),(SELECT coalesce(sum(length(CAST(document AS BLOB))),0) FROM aws_metrics_series)+(SELECT coalesce(sum(length(CAST(document AS BLOB))),0) FROM aws_metrics_points)").Scan(&seriesCount, &pointCount, &bytes); err != nil {
		return err
	}
	if seriesCount > int64(s.limits.Series) || pointCount > int64(s.limits.Points) || bytes > int64(s.limits.Bytes) {
		return quota()
	}
	if err = one(tx.ExecContext(ctx, "UPDATE aws_metrics_regions SET revision=revision+1 WHERE region=? AND revision=?", region, revision)); err != nil {
		return err
	}
	var after int64
	if err = tx.QueryRowContext(ctx, "SELECT revision FROM aws_metrics_regions WHERE region=?", region).Scan(&after); err != nil || after != revision+1 {
		return internal()
	}
	return nil
}
