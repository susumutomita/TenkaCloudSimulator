package metrics

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

func allow(context.Context, *sql.Tx, string, string) (bool, error) { return true, nil }
func pointer[T any](v T) *T                                        { return &v }
func fixture(t *testing.T, limits Limits) (*Store, *sql.DB, *time.Time, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "metrics.sqlite")
	db := openDB(t, path)
	now := time.Date(2026, 1, 2, 12, 0, 30, 0, time.UTC)
	store, err := NewStore(context.Background(), db, Config{Clock: func() time.Time { return now }, Limits: limits})
	if err != nil {
		t.Fatal("metric constructor failed", err)
	}
	return store, db, &now, path
}
func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func code(t *testing.T, err error, want string) {
	t.Helper()
	var api *Error
	if !errors.As(err, &api) || api.Code != want {
		t.Fatalf("error code mismatch: want %s, got %T", want, err)
	}
}

// Hash complete owned rows/schema rather than printing private cursor material.
func frame(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"sqlite_master", "aws_metrics_meta", "aws_metrics_regions", "aws_metrics_series", "aws_metrics_points", "sqlite_sequence"} {
		query := "SELECT * FROM " + table + " ORDER BY 1,2"
		if table == "sqlite_master" {
			query = "SELECT name,sql FROM sqlite_master WHERE name LIKE 'aws_metrics_%' OR name='sqlite_sequence' ORDER BY name"
		}
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal("frame query failed")
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		saved := [][]any{}
		for rows.Next() {
			values := make([]any, len(cols))
			p := make([]any, len(cols))
			for i := range p {
				p[i] = &values[i]
			}
			if err = rows.Scan(p...); err != nil {
				t.Fatal(err)
			}
			saved = append(saved, values)
		}
		err = rows.Err()
		closed := rows.Close()
		if err != nil || closed != nil {
			t.Fatal("frame read failed")
		}
		data, err := json.Marshal(saved)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		out[table] = hex.EncodeToString(sum[:])
	}
	return out
}
func unchanged(t *testing.T, db *sql.DB, before map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before, frame(t, db)) {
		t.Fatal("owned durable frame changed on refusal or read")
	}
}
func put(t *testing.T, s *Store, region, namespace, name string, value float64, stamp *time.Time, unit *string) {
	t.Helper()
	if err := s.PutMetricData(context.Background(), Scope{region, allow}, PutInput{namespace, []Datum{{MetricName: name, Value: value, Timestamp: stamp, Unit: unit}}}); err != nil {
		t.Fatal(err)
	}
}
func statInput(now time.Time, name string) StatisticsInput {
	return StatisticsInput{Namespace: "Practice/App", MetricName: name, Start: now.Truncate(time.Minute).Add(10 * time.Second), End: now.Truncate(time.Minute).Add(2 * time.Minute), Period: 60, Statistics: []string{"SampleCount", "Sum", "Minimum", "Maximum", "Average"}}
}

func TestMetricsLiteralStatisticsUnitsDimensionIdentityAndRestart(t *testing.T) {
	s, db, now, path := fixture(t, Limits{})
	ctx := context.Background()
	scope := Scope{"us-east-1", allow}
	stamp := now.Truncate(time.Minute)
	dims := []Dimension{{"Zone", "a"}, {"Task", "b"}}
	data := []Datum{}
	for _, v := range []float64{2, 0, -1, 2} {
		data = append(data, Datum{MetricName: "Latency", Dimensions: dims, Timestamp: &stamp, Value: v})
	}
	if err := s.PutMetricData(ctx, scope, PutInput{"Practice/App", data}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMetricData(ctx, scope, PutInput{"Practice/App", []Datum{{MetricName: "Latency", Dimensions: []Dimension{{"Task", "b"}, {"Zone", "a"}}, Timestamp: &stamp, Value: 7, Unit: pointer("Count")}, {MetricName: "Latency", Dimensions: dims, Timestamp: pointer(stamp.Add(61 * time.Second)), Value: 4}}}); err != nil {
		t.Fatal(err)
	}
	query := statInput(*now, "Latency")
	query.Dimensions = dims
	before := frame(t, db)
	result, err := s.GetMetricStatistics(ctx, scope, query)
	if err != nil {
		t.Fatal(err)
	}
	want := []Datapoint{{stamp, "Count", 1, 7, 7, 7, 7}, {stamp, "None", 4, 3, -1, 2, .75}, {stamp.Add(time.Minute), "None", 1, 4, 4, 4, 4}}
	if !reflect.DeepEqual(result.Datapoints, want) {
		t.Fatalf("literal aggregate mismatch: %+v", result.Datapoints)
	}
	query.Unit = pointer("None")
	none, err := s.GetMetricStatistics(ctx, scope, query)
	if err != nil || len(none.Datapoints) != 2 || none.Datapoints[0].SampleCount != 4 {
		t.Fatal("explicit None filter did not preserve the recorded stream")
	}
	query.Unit = pointer("Seconds")
	empty, err := s.GetMetricStatistics(ctx, scope, query)
	if err != nil || len(empty.Datapoints) != 0 {
		t.Fatal("unit conversion or invented datapoint")
	}
	query.Unit = nil
	query.Dimensions = dims[:1]
	empty, err = s.GetMetricStatistics(ctx, scope, query)
	if err != nil || len(empty.Datapoints) != 0 {
		t.Fatal("incomplete dimensions selected a different metric")
	}
	listed, err := s.ListMetrics(ctx, scope, ListInput{Dimensions: []DimensionFilter{{Name: "Task"}}})
	if err != nil || len(listed.Metrics) != 1 || len(listed.Metrics[0].Dimensions) != 2 {
		t.Fatal("dimension subset listing failed")
	}
	unchanged(t, db, before)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openDB(t, path)
	reopened, err := NewStore(ctx, db, Config{Clock: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	query.Dimensions = dims
	result, err = reopened.GetMetricStatistics(ctx, scope, query)
	if err != nil || !reflect.DeepEqual(result.Datapoints, want) {
		t.Fatal("restart changed raw sample statistics")
	}
}

func TestMetricsColdDenialAuthorizationErrorAndInvalidBatchFrames(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	ctx := context.Background()
	before := frame(t, db)
	input := PutInput{"Practice/App", []Datum{{MetricName: "M", Value: 1}}}
	calls := 0
	deny := func(ctx context.Context, tx *sql.Tx, action, resource string) (bool, error) {
		calls++
		if tx == nil || action != "cloudwatch:PutMetricData" || resource != "*" {
			t.Fatal("wrong literal authorization contract")
		}
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_metrics_meta").Scan(&n); err != nil || n != 1 {
			t.Fatal("authorizer did not receive the caller transaction")
		}
		return false, nil
	}
	code(t, s.PutMetricData(ctx, Scope{"us-west-2", deny}, PutInput{"AWS/Fake", []Datum{{MetricName: "", Value: math.NaN()}}}), "AccessDenied")
	if calls != 1 {
		t.Fatal("authorization was not captured once")
	}
	unchanged(t, db, before)
	code(t, s.PutMetricData(ctx, Scope{"us-west-2", nil}, input), "InternalServiceError")
	unchanged(t, db, before)
	failure := func(context.Context, *sql.Tx, string, string) (bool, error) {
		return false, errors.New("private identity database failure")
	}
	err := s.PutMetricData(ctx, Scope{"us-west-2", failure}, input)
	code(t, err, "InternalServiceError")
	if strings.Contains(err.Error(), "private") {
		t.Fatal("infrastructure error leaked")
	}
	unchanged(t, db, before)
	bad := []PutInput{{"AWS/Fake", input.Data}, {"Practice/App", nil}, {"Practice/App", []Datum{{MetricName: "M", Value: math.Inf(1)}}}, {"Practice/App", []Datum{{MetricName: "M", Value: math.Ldexp(1, 361)}}}, {"Practice/App", []Datum{{MetricName: "M", Value: 1, Unit: pointer("")}}}, {"Practice/App", []Datum{{MetricName: "M", Value: 1, Timestamp: pointer(now.Add(-14*24*time.Hour - time.Nanosecond))}}}, {"Practice/App", []Datum{{MetricName: "M", Value: 1, Timestamp: pointer(now.Add(2*time.Hour + time.Nanosecond))}}}, {"Practice/App", []Datum{{MetricName: "M", Value: 1, Dimensions: []Dimension{{"x", "1"}, {"x", "2"}}}}}, {"Practice/App", []Datum{{MetricName: "M", Value: 1, StorageResolution: pointer(1)}}}}
	for i, in := range bad {
		if err := s.PutMetricData(ctx, Scope{"us-east-1", allow}, in); err == nil {
			t.Fatalf("invalid batch %d accepted", i)
		}
		unchanged(t, db, before)
	}
}

func TestMetricsOnlyPutCollectsOwnExpiryAndIgnoredSecondInsertRollsBack(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	ctx := context.Background()
	old := now.Add(-14 * 24 * time.Hour)
	put(t, s, "us-east-1", "Practice/App", "Old", 3, &old, nil)
	put(t, s, "us-east-1", "Peer/App", "Peer", 4, &old, nil)
	put(t, s, "us-west-2", "Practice/App", "PeerRegion", 5, &old, nil)
	*now = now.Add(2 * 24 * time.Hour)
	before := frame(t, db)
	list, err := s.ListMetrics(ctx, Scope{"us-east-1", allow}, ListInput{})
	if err != nil || len(list.Metrics) != 0 {
		t.Fatal("expired-only metrics were disclosed")
	}
	query := StatisticsInput{Namespace: "Practice/App", MetricName: "Old", Start: now.Add(-15 * 24 * time.Hour), End: *now, Period: 3600, Statistics: []string{"Sum"}}
	got, err := s.GetMetricStatistics(ctx, Scope{"us-east-1", allow}, query)
	if err != nil || len(got.Datapoints) != 0 {
		t.Fatal("expired raw data was returned")
	}
	unchanged(t, db, before)
	if _, err = db.Exec(`CREATE TRIGGER metrics_ignore_second BEFORE INSERT ON aws_metrics_points WHEN json_extract(NEW.document,'$.value')=9 BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	before = frame(t, db)
	err = s.PutMetricData(ctx, Scope{"us-east-1", allow}, PutInput{"Practice/App", []Datum{{MetricName: "New", Value: 8}, {MetricName: "New", Value: 9}}})
	code(t, err, "InternalServiceError")
	unchanged(t, db, before)
	if _, err = db.Exec("DROP TRIGGER metrics_ignore_second"); err != nil {
		t.Fatal(err)
	}
	put(t, s, "us-east-1", "Practice/App", "New", 8, nil, nil)
	for _, x := range []struct {
		region, namespace string
		want              int
	}{{"us-east-1", "Practice/App", 1}, {"us-east-1", "Peer/App", 1}, {"us-west-2", "Practice/App", 1}} {
		var n int
		err = db.QueryRow(`SELECT count(*) FROM aws_metrics_points p JOIN aws_metrics_series s ON s.region=p.region AND s.identity=p.series_id WHERE s.region=? AND s.namespace=?`, x.region, x.namespace).Scan(&n)
		if err != nil || n != x.want {
			t.Fatal("scoped collection changed another scope or retained old owned point")
		}
	}
}

func TestMetricsExpiryEqualityAndQuotaRefusalPreserveReadOnlyFrames(t *testing.T) {
	limits := defaultLimits
	limits.Points = 2
	s, db, now, _ := fixture(t, limits)
	old := now.Add(-14 * 24 * time.Hour)
	put(t, s, "us-east-1", "Practice/App", "M", 1, &old, nil)
	*now = now.Add(24 * time.Hour)
	put(t, s, "us-east-1", "Practice/App", "M", 2, nil, nil)
	var n int
	if err := db.QueryRow("SELECT count(*) FROM aws_metrics_points WHERE stamp=?", old.UnixNano()).Scan(&n); err != nil || n != 1 {
		t.Fatal("equal-cutoff point was collected")
	}
	before := frame(t, db)
	_, err := s.ListMetrics(context.Background(), Scope{"us-east-1", allow}, ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	unchanged(t, db, before)
	*now = now.Add(time.Nanosecond)
	put(t, s, "us-east-1", "Practice/App", "M", 3, nil, nil)
	if err = db.QueryRow("SELECT count(*) FROM aws_metrics_points WHERE stamp=?", old.UnixNano()).Scan(&n); err != nil || n != 0 {
		t.Fatal("strictly expired owned point was not collected")
	}
	before = frame(t, db)
	code(t, s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, PutInput{"Practice/App", []Datum{{MetricName: "M", Value: 4}}}), "SimulatorQuotaExceeded")
	unchanged(t, db, before)
}

func TestMetricsConcurrentLastSlotAndForeignExpiredPhysicalBudget(t *testing.T) {
	limits := defaultLimits
	limits.Points = 2
	s, db, now, _ := fixture(t, limits)
	old := now.Add(-14 * 24 * time.Hour)
	put(t, s, "us-east-1", "Foreign/App", "Old", 1, &old, nil)
	*now = now.Add(2 * 24 * time.Hour)
	before := frame(t, db)
	if _, err := s.ListMetrics(context.Background(), Scope{"us-east-1", allow}, ListInput{}); err != nil {
		t.Fatal(err)
	}
	unchanged(t, db, before)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"A", "B"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			results <- s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, PutInput{"Practice/App", []Datum{{MetricName: name, Value: 1}}})
		}(name)
	}
	wg.Wait()
	close(results)
	success, refused := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			code(t, err, "SimulatorQuotaExceeded")
			refused++
		}
	}
	if success != 1 || refused != 1 {
		t.Fatal("concurrent admission exceeded the last physical slot")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM aws_metrics_points").Scan(&count); err != nil || count != 2 {
		t.Fatal("foreign expired row was not charged")
	}
}

func TestMetricsList500PlusOneCursorRestartScopeAndReadFrame(t *testing.T) {
	s, db, now, path := fixture(t, Limits{})
	scope := Scope{"us-east-1", allow}
	batch := PutInput{Namespace: "Practice/App"}
	for i := 0; i < 501; i++ {
		batch.Data = append(batch.Data, Datum{MetricName: fmt.Sprintf("Metric-%03d", i), Value: float64(i)})
	}
	if err := s.PutMetricData(context.Background(), scope, batch); err != nil {
		t.Fatal(err)
	}
	before := frame(t, db)
	first, err := s.ListMetrics(context.Background(), scope, ListInput{})
	if err != nil || len(first.Metrics) != 500 || first.NextToken == "" {
		t.Fatal("actual500 boundary did not produce continuation")
	}
	unchanged(t, db, before)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openDB(t, path)
	s, err = NewStore(context.Background(), db, Config{Clock: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	last, err := s.ListMetrics(context.Background(), scope, ListInput{NextToken: first.NextToken})
	if err != nil || len(last.Metrics) != 1 || last.NextToken != "" {
		t.Fatal("restart lost cursor authority or exact continuation")
	}
	seen := map[string]bool{}
	for _, m := range append(first.Metrics, last.Metrics...) {
		if seen[m.Name] {
			t.Fatal("duplicate metric acrosspages")
		}
		seen[m.Name] = true
	}
	if len(seen) != 501 {
		t.Fatal("missing metric acrosspages")
	}
	before = frame(t, db)
	tampered := first.NextToken
	replacement := "A"
	if tampered[0] == 'A' {
		replacement = "B"
	}
	tampered = replacement + tampered[1:]
	for _, query := range []ListInput{{NextToken: tampered}, {NextToken: first.NextToken, Namespace: pointer("Different/App")}} {
		_, err = s.ListMetrics(context.Background(), scope, query)
		code(t, err, "InvalidParameterValue")
		unchanged(t, db, before)
	}
	_, err = s.ListMetrics(context.Background(), Scope{"us-west-2", allow}, ListInput{NextToken: first.NextToken})
	code(t, err, "InvalidParameterValue")
	unchanged(t, db, before)
	*now = now.Add(time.Hour + time.Nanosecond)
	_, err = s.ListMetrics(context.Background(), scope, ListInput{NextToken: first.NextToken})
	code(t, err, "InvalidParameterValue")
	unchanged(t, db, before)
	put(t, s, "us-east-1", "Practice/App", "Fresh", 1, nil, nil)
	before = frame(t, db)
	_, err = s.ListMetrics(context.Background(), scope, ListInput{NextToken: first.NextToken})
	code(t, err, "InvalidParameterValue")
	unchanged(t, db, before)
}

func TestMetricsLogicalBytesConstructorMismatchAndPersistedFaultRepair(t *testing.T) {
	limits := defaultLimits
	limits.Bytes = 200
	s, db, now, path := fixture(t, limits)
	put(t, s, "us-east-1", "Practice/App", "M", 0, nil, nil)
	before := frame(t, db)
	err := s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, PutInput{"Practice/App", []Datum{{MetricName: "Large", Dimensions: []Dimension{{"Payload", strings.Repeat("x", 200)}}, Value: 1}}})
	code(t, err, "SimulatorQuotaExceeded")
	unchanged(t, db, before)
	if _, err = NewStore(context.Background(), db, Config{Clock: func() time.Time { return *now }}); err == nil {
		t.Fatal("different persisted configuration was adopted")
	}
	unchanged(t, db, before)
	if _, err = db.Exec(`UPDATE aws_metrics_points SET document='not-json'`); err != nil {
		t.Fatal(err)
	}
	bad := frame(t, db)
	query := statInput(*now, "M")
	_, err = s.GetMetricStatistics(context.Background(), Scope{"us-east-1", allow}, query)
	code(t, err, "InternalServiceError")
	unchanged(t, db, bad)
	if _, err = db.Exec(`UPDATE aws_metrics_points SET document=json_object('timestamp',stamp,'unit',unit,'value',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetMetricStatistics(context.Background(), Scope{"us-east-1", allow}, query); err != nil {
		t.Fatal("repaired persisted data remainedunreadable", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openDB(t, path)
	if _, err = NewStore(context.Background(), db, Config{Clock: func() time.Time { return *now }, Limits: limits}); err != nil {
		t.Fatal(err)
	}
}

func TestMetricsIgnoredCatalogAndExpiryWritesRollbackAndRepair(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	ctx := context.Background()
	before := frame(t, db)
	if _, err := db.Exec(`CREATE TRIGGER metrics_ignore_catalog BEFORE INSERT ON aws_metrics_series BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	err := s.PutMetricData(ctx, Scope{"us-west-2", allow}, PutInput{"Practice/App", []Datum{{MetricName: "M", Value: 1}}})
	code(t, err, "InternalServiceError")
	unchanged(t, db, before)
	if _, err = db.Exec("DROP TRIGGER metrics_ignore_catalog"); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-14 * 24 * time.Hour)
	put(t, s, "us-west-2", "Practice/App", "M", 1, &old, nil)
	*now = now.Add(2 * 24 * time.Hour)
	if _, err = db.Exec(`CREATE TRIGGER metrics_ignore_expiry BEFORE DELETE ON aws_metrics_points BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	before = frame(t, db)
	err = s.PutMetricData(ctx, Scope{"us-west-2", allow}, PutInput{"Practice/App", []Datum{{MetricName: "M", Value: 2}}})
	code(t, err, "InternalServiceError")
	unchanged(t, db, before)
	if _, err = db.Exec("DROP TRIGGER metrics_ignore_expiry"); err != nil {
		t.Fatal(err)
	}
	put(t, s, "us-west-2", "Practice/App", "M", 2, nil, nil)
	var n int
	if err = db.QueryRow("SELECT count(*) FROM aws_metrics_points").Scan(&n); err != nil || n != 1 {
		t.Fatal("repaired request did not commit exactly the live point")
	}
}

func TestMetricsStatisticsBoundsFuturePointAndReadExpiryNeverCollect(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	future := now.Add(2 * time.Hour)
	put(t, s, "us-east-1", "Practice/App", "M", 5, &future, nil)
	scope := Scope{"us-east-1", allow}
	before := frame(t, db)
	in := StatisticsInput{Namespace: "Practice/App", MetricName: "M", Start: now.Truncate(time.Minute), End: now.Add(3 * time.Hour), Period: 60, Statistics: []string{"Sum"}}
	out, err := s.GetMetricStatistics(context.Background(), scope, in)
	if err != nil || len(out.Datapoints) != 1 || out.Datapoints[0].Sum != 5 {
		t.Fatal("inclusive future ingestion limit lost its sample")
	}
	in.End = in.Start.Add(1440 * time.Minute)
	if _, err = s.GetMetricStatistics(context.Background(), scope, in); err != nil {
		t.Fatal("1440 requested buckets rejected")
	}
	in.End = in.Start.Add(1440*time.Minute + time.Nanosecond)
	_, err = s.GetMetricStatistics(context.Background(), scope, in)
	code(t, err, "InvalidParameterValue")
	in.End = now.Add(time.Hour)
	in.Start = now.Add(-15*24*time.Hour - time.Nanosecond)
	_, err = s.GetMetricStatistics(context.Background(), scope, in)
	code(t, err, "InvalidParameterCombination")
	unchanged(t, db, before)
	*now = now.Add(14 * 24 * time.Hour)
	outList, err := s.ListMetrics(context.Background(), scope, ListInput{})
	if err != nil || len(outList.Metrics) != 1 {
		t.Fatal("exact listing-age boundary rejected")
	}
	*now = now.Add(time.Nanosecond)
	outList, err = s.ListMetrics(context.Background(), scope, ListInput{})
	if err != nil || len(outList.Metrics) != 0 {
		t.Fatal("old ingestion metadata remained visible")
	}
	unchanged(t, db, before)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.PutMetricData(ctx, scope, PutInput{"Practice/App", []Datum{{MetricName: "M", Value: 6}}}); err == nil {
		t.Fatal("canceled request acknowledged a write")
	}
	unchanged(t, db, before)
}

func TestMetricsConstructorRefusesUnboundLegacySchemaWithoutDeletingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db := openDB(t, path)
	if _, err := db.Exec(`CREATE TABLE aws_metrics_regions(region TEXT PRIMARY KEY); INSERT INTO aws_metrics_regions(region) VALUES('us-east-1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(context.Background(), db, Config{}); err == nil {
		t.Fatal("incompatible legacy table was adopted")
	}
	var name string
	if err := db.QueryRow("SELECT region FROM aws_metrics_regions").Scan(&name); err != nil || name != "us-east-1" {
		t.Fatal("constructor destroyed legacy data")
	}
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name LIKE 'aws_metrics_%'").Scan(&tables); err != nil || tables != 1 {
		t.Fatal("failed constructor committed partial schema")
	}
	if _, err := NewStore(context.Background(), nil, Config{}); err == nil {
		t.Fatal("nil database accepted")
	}
}

func TestMetricsCursorCannotCrossDatabaseAndRegionalStateIsBounded(t *testing.T) {
	limits := defaultLimits
	limits.PageSize = 1
	limits.Regions = 1
	a, adb, _, _ := fixture(t, limits)
	b, bdb, _, _ := fixture(t, limits)
	for _, s := range []*Store{a, b} {
		if err := s.PutMetricData(context.Background(), Scope{"us-east-1", allow}, PutInput{"Practice/App", []Datum{{MetricName: "A", Value: 1}, {MetricName: "B", Value: 2}}}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := a.ListMetrics(context.Background(), Scope{"us-east-1", allow}, ListInput{})
	if err != nil || first.NextToken == "" {
		t.Fatal("cursor fixture did not paginate")
	}
	before := frame(t, bdb)
	_, err = b.ListMetrics(context.Background(), Scope{"us-east-1", allow}, ListInput{NextToken: first.NextToken})
	code(t, err, "InvalidParameterValue")
	unchanged(t, bdb, before)
	before = frame(t, adb)
	err = a.PutMetricData(context.Background(), Scope{"us-west-2", allow}, PutInput{"Practice/App", []Datum{{MetricName: "A", Value: 1}}})
	code(t, err, "SimulatorQuotaExceeded")
	unchanged(t, adb, before)
}

func TestMetricsContinuationRetainsCapturedRawVisibilityWithinTTL(t *testing.T) {
	limits := defaultLimits
	limits.PageSize = 1
	s, db, now, _ := fixture(t, limits)
	scope := Scope{"us-east-1", allow}
	stamp := now.Add(-14 * 24 * time.Hour)
	for _, name := range []string{"BoundaryOne", "BoundaryTwo"} {
		put(t, s, scope.Region, "Practice/App", name, 1, &stamp, nil)
	}
	// Both raw points are exactly15days old, while catalog ingestion is only
	// one day old. There are no writes or revision changes between the pages.
	*now = now.Add(24 * time.Hour)
	before := frame(t, db)
	first, err := s.ListMetrics(context.Background(), scope, ListInput{})
	if err != nil || len(first.Metrics) != 1 || first.NextToken == "" {
		t.Fatal("visible cutoff fixture did not produce a first page")
	}
	unchanged(t, db, before)
	*now = now.Add(time.Minute) // Within the one-hour token lifetime.
	live, err := s.ListMetrics(context.Background(), scope, ListInput{})
	if err != nil || len(live.Metrics) != 0 || live.NextToken != "" {
		t.Fatal("fresh read did not cross the raw visibility cutoff")
	}
	unchanged(t, db, before)
	last, err := s.ListMetrics(context.Background(), scope, ListInput{NextToken: first.NextToken})
	if err != nil || len(last.Metrics) != 1 || last.NextToken != "" {
		t.Fatal("live continuation lost its captured pre-cutoff visibility")
	}
	want := map[string]bool{"BoundaryOne": true, "BoundaryTwo": true}
	for _, metric := range append(first.Metrics, last.Metrics...) {
		if metric.Namespace != "Practice/App" || len(metric.Dimensions) != 0 || !want[metric.Name] {
			t.Fatal("captured continuation repeated or substituted a metric")
		}
		delete(want, metric.Name)
	}
	if len(want) != 0 {
		t.Fatal("captured continuation omitted a boundary metric")
	}
	unchanged(t, db, before)
}

func TestMetricsSameClockWriteInvalidatesOtherwiseLiveCursor(t *testing.T) {
	limits := defaultLimits
	limits.PageSize = 1
	s, db, now, _ := fixture(t, limits)
	scope := Scope{"us-east-1", allow}
	for _, name := range []string{"RevisionOne", "RevisionTwo"} {
		put(t, s, scope.Region, "Practice/App", name, 1, nil, nil)
	}
	first, err := s.ListMetrics(context.Background(), scope, ListInput{})
	if err != nil || len(first.Metrics) != 1 || first.NextToken == "" {
		t.Fatal("live revision fixture did not produce a first page")
	}
	before := frame(t, db)
	last, err := s.ListMetrics(context.Background(), scope, ListInput{NextToken: first.NextToken})
	if err != nil || len(last.Metrics) != 1 || last.Metrics[0].Name == first.Metrics[0].Name {
		t.Fatal("cursor was not usable before the same-clock write")
	}
	unchanged(t, db, before)
	capturedClock := *now
	// Append to an existing identity: neither listing membership nor time
	// changes. Only the durable same-region revision invalidates the token.
	put(t, s, scope.Region, "Practice/App", "RevisionOne", 2, nil, nil)
	if !now.Equal(capturedClock) {
		t.Fatal("revision refusal fixture accidentally advanced its clock")
	}
	afterWrite := frame(t, db)
	_, err = s.ListMetrics(context.Background(), scope, ListInput{NextToken: first.NextToken})
	code(t, err, "InvalidParameterValue")
	unchanged(t, db, afterWrite)
	fresh, err := s.ListMetrics(context.Background(), scope, ListInput{})
	if err != nil || len(fresh.Metrics) != 1 || fresh.NextToken == "" {
		t.Fatal("write made a new first page unavailable")
	}
	freshLast, err := s.ListMetrics(context.Background(), scope, ListInput{NextToken: fresh.NextToken})
	if err != nil || len(freshLast.Metrics) != 1 || freshLast.NextToken != "" {
		t.Fatal("fresh same-clock cursor did not recover after revision change")
	}
	want := map[string]bool{"RevisionOne": true, "RevisionTwo": true}
	for _, metric := range append(fresh.Metrics, freshLast.Metrics...) {
		if metric.Namespace != "Practice/App" || len(metric.Dimensions) != 0 || !want[metric.Name] {
			t.Fatal("fresh pages repeated or substituted a metric")
		}
		delete(want, metric.Name)
	}
	if len(want) != 0 {
		t.Fatal("fresh pages omitted an unchanged metric identity")
	}
	unchanged(t, db, afterWrite)
}
