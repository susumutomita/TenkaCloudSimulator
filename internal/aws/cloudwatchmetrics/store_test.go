package cloudwatchmetrics

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func testStore(t *testing.T, path string) (*Store, *core.Store) {
	t.Helper()
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	return s, db
}

func testQuery(start time.Time, period int64) query {
	return query{name: "Latency", dimensions: "{}", start: start, end: start.Add(2 * time.Minute), period: period, stats: map[string]bool{"SampleCount": true, "Sum": true, "Average": true, "Minimum": true, "Maximum": true}}
}
func observation(at time.Time, value float64) sample {
	return sample{"Latency", "{}", "Count", at, value}
}

func TestRawAggregatesPersistAcrossRestartAndScopes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, owner := testStore(t, path)
	now := time.Date(2026, 1, 7, 12, 0, 0, 0, time.UTC)
	start := now.Add(-2 * time.Minute)
	ctx := context.Background()
	batch := []sample{observation(start.Add(10*time.Second), 10), observation(start, 2), observation(start.Add(3*time.Second), 4), observation(start.Add(time.Minute), -2), observation(now, 99)}
	if err := s.put(ctx, "us-east-1", "Training", batch, now); err != nil {
		t.Fatal(err)
	}
	other := observation(start, 100)
	other.unit = "Bytes"
	for _, scope := range []struct {
		r, n  string
		point sample
	}{{"us-east-1", "Training", other}, {"us-east-1", "Other", batch[0]}, {"us-west-2", "Training", batch[0]}} {
		if err := s.put(ctx, scope.r, scope.n, []sample{scope.point}, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	s, _ = testStore(t, path)
	q := testQuery(start, 60)
	points, err := s.get(ctx, "us-east-1", "Training", q, now)
	if err != nil || len(points) != 3 {
		t.Fatalf("unit/time points %v, %v", points, err)
	}
	p := points[1]
	if points[0].Unit != "Bytes" || *points[0].Sum != 100 || p.Unit != "Count" || *p.Sum != 16 || *p.SampleCount != 3 || *p.Average != 16.0/3 || *p.Minimum != 2 || *p.Maximum != 10 || *points[2].Sum != -2 {
		t.Fatalf("incorrect raw statistics %+v", points)
	}
	q.period = 120
	q.unit = "Count"
	q.stats = map[string]bool{"Average": true}
	points, err = s.get(ctx, "us-east-1", "Training", q, now)
	if err != nil || len(points) != 1 || *points[0].Average != 3.5 || points[0].Sum != nil {
		t.Fatalf("weighted average %+v, %v", points, err)
	}
	q.unit = "Seconds"
	points, err = s.get(ctx, "us-east-1", "Training", q, now)
	if err != nil || len(points) != 0 {
		t.Fatal(points, err)
	}
}

func TestRetentionRollbackIgnoredInsertQuotaAndCancellation(t *testing.T) {
	s, _ := testStore(t, filepath.Join(t.TempDir(), "state.db"))
	ctx := context.Background()
	now := time.Now().UTC()
	old := observation(now.Add(-retention-time.Nanosecond), 1)
	for _, region := range []string{"us-east-1", "us-west-2"} {
		if err := s.put(ctx, region, "N", []sample{old}, old.at); err != nil {
			t.Fatal(err)
		}
	}
	count := func(region string) int {
		t.Helper()
		var n int
		err := s.db.QueryRow("SELECT count(*) FROM aws_cloudwatch_samples WHERE region=?", region).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	points, err := s.get(ctx, "us-east-1", "N", testQuery(old.at.Truncate(time.Minute), 60), now)
	if err != nil || len(points) != 0 || count("us-east-1") != 1 {
		t.Fatal("read modified or returned expired data", points, err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER ignore_second_metric BEFORE INSERT ON aws_cloudwatch_samples WHEN NEW.value=2 BEGIN SELECT RAISE(IGNORE); END;`); err != nil {
		t.Fatal(err)
	}
	batch := []sample{observation(now, 1), observation(now, 2)}
	if err = s.put(ctx, "us-east-1", "N", batch, now); err == nil || count("us-east-1") != 1 {
		t.Fatal("unacknowledged write partially committed", err)
	}
	var at int64
	if err = s.db.QueryRow("SELECT at_ns FROM aws_cloudwatch_samples WHERE region='us-east-1'").Scan(&at); err != nil || at != old.at.UnixNano() {
		t.Fatal("failed transaction pruned old sample", at, err)
	}
	if _, err = s.db.Exec("DROP TRIGGER ignore_second_metric"); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = s.put(canceled, "us-east-1", "N", batch, now); err == nil || count("us-east-1") != 1 {
		t.Fatal("canceled write changed state", err)
	}
	if err = s.put(ctx, "us-east-1", "N", batch, now); err != nil || count("us-east-1") != 2 || count("us-west-2") != 1 {
		t.Fatal("scoped retry", err)
	}
	if _, err = s.db.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<9998)
		INSERT INTO aws_cloudwatch_samples(region,namespace,metric,dimensions,unit,at_ns,value)
		SELECT 'us-east-1','N','Quota','{}','None',?,0 FROM n`, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err = s.put(ctx, "us-east-1", "N", batch, now); err == nil || count("us-east-1") != 10000 {
		t.Fatal("quota allowed partial upload", err)
	}
	if err = s.put(ctx, "us-east-1", "Other", batch, now); err != nil {
		t.Fatal(err)
	}
}

func TestReturnLimitAndCompensatedAverage(t *testing.T) {
	s, _ := testStore(t, filepath.Join(t.TempDir(), "state.db"))
	now := time.Now().UTC().Truncate(time.Minute)
	start := now.Add(-721 * time.Minute)
	ctx := context.Background()
	var batch []sample
	for i := 0; i < 720; i++ {
		a := observation(start.Add(time.Duration(i)*time.Minute), 2)
		b := a
		b.unit = "Seconds"
		batch = append(batch, a, b)
	}
	if err := s.put(ctx, "us-east-1", "N", batch[:1000], now); err != nil {
		t.Fatal(err)
	}
	if err := s.put(ctx, "us-east-1", "N", batch[1000:], now); err != nil {
		t.Fatal(err)
	}
	q := testQuery(start, 60)
	q.end = now
	p, err := s.get(ctx, "us-east-1", "N", q, now)
	if err != nil || len(p) != 1440 {
		t.Fatal(len(p), err)
	}
	a := observation(start, 1)
	a.unit = "Bytes"
	if err = s.put(ctx, "us-east-1", "N", []sample{a}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.get(ctx, "us-east-1", "N", q, now); err == nil {
		t.Fatal("truncated excess unit/time points")
	}
	q.unit = "Count"
	p, err = s.get(ctx, "us-east-1", "N", q, now)
	if err != nil || len(p) != 720 {
		t.Fatal(len(p), err)
	}
	var agg aggregate
	for _, v := range []float64{math.Ldexp(1, 360), 1, -math.Ldexp(1, 360)} {
		agg.add(v)
	}
	if agg.sum+agg.correction != 1 {
		t.Fatal(agg)
	}
}

func TestSQLFailuresAreReturned(t *testing.T) {
	if _, err := NewStore(nil); err == nil {
		t.Fatal("nil db")
	}
	s, owner := testStore(t, filepath.Join(t.TempDir(), "state.db"))
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(s.db); err == nil {
		t.Fatal("closed database")
	}
	if err := s.put(context.Background(), "region", "N", nil, time.Now()); err == nil {
		t.Fatal("closed database accepted write")
	}
	q := testQuery(time.Now().Add(-time.Minute), 60)
	if _, err := s.get(context.Background(), "region", "N", q, time.Now()); err == nil || err == sql.ErrNoRows {
		t.Fatal(err)
	}
}

func TestInflightCancellationPreservesRetentionAndEveryRow(t *testing.T) {
	for _, event := range []sqlite3.AuthorizerActionCode{sqlite3.AUTH_DELETE, sqlite3.AUTH_INSERT} {
		t.Run(fmt.Sprint(event), func(t *testing.T) {
			s, _ := testStore(t, filepath.Join(t.TempDir(), "cancel.db"))
			now := time.Now().UTC()
			bg := context.Background()
			stale := now.Add(-retention - time.Second)
			if err := s.put(bg, "us-east-1", "N", []sample{observation(stale, 23)}, stale); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(bg)
			defer cancel()
			conn, err := s.db.Conn(bg)
			if err != nil {
				t.Fatal(err)
			}
			err = conn.Raw(func(raw any) error {
				raw.(driver.Conn).Raw().UpdateHook(func(code sqlite3.AuthorizerActionCode, _, table string, _ int64) {
					if code == event && table == "aws_cloudwatch_samples" {
						cancel()
					}
				})
				return nil
			})
			conn.Close()
			if err != nil {
				t.Fatal(err)
			}
			if err = s.put(ctx, "us-east-1", "N", []sample{observation(now, 3), observation(now, 4)}, now); err == nil || ctx.Err() == nil {
				t.Fatal("cancellation did not interrupt transaction", err)
			}
			var n int
			var value float64
			if err = s.db.QueryRow("SELECT count(*),min(value) FROM aws_cloudwatch_samples").Scan(&n, &value); err != nil || n != 1 || value != 23 {
				t.Fatal("changed rows after cancellation", n, value, err)
			}
		})
	}
}

func TestScopePruningAcknowledgesDeletesAndKeepsInclusiveCutoff(t *testing.T) {
	s, _ := testStore(t, filepath.Join(t.TempDir(), "retention.db"))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Minute)
	cutoff := now.Add(-retention)
	samples := []sample{observation(cutoff, 24), observation(cutoff.Add(-time.Nanosecond), 13)}
	if err := s.put(ctx, "us-east-1", "N", samples, cutoff); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER suppress_prune BEFORE DELETE ON aws_cloudwatch_samples BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.put(ctx, "us-east-1", "N", []sample{observation(now, 7)}, now); err == nil {
		t.Fatal("unacknowledged retention cleanup returned success")
	}
	if _, err := s.db.Exec("DROP TRIGGER suppress_prune"); err != nil {
		t.Fatal(err)
	}
	if err := s.put(ctx, "us-east-1", "N", []sample{observation(now, 7)}, now); err != nil {
		t.Fatal(err)
	}
	points, err := s.get(ctx, "us-east-1", "N", testQuery(cutoff, 60), now)
	if err != nil || len(points) != 1 || *points[0].Sum != 24 {
		t.Fatal(points, err)
	}
}
