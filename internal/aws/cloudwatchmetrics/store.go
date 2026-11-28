package cloudwatchmetrics

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"
	"time"
)

const (
	maxNamespaceSamples = 10000
	maxTeamSamples      = 100000
)

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("CloudWatch metrics requires SQLite")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS aws_cloudwatch_samples(
		id INTEGER PRIMARY KEY, region TEXT NOT NULL, namespace TEXT NOT NULL, metric TEXT NOT NULL,
		dimensions TEXT NOT NULL, unit TEXT NOT NULL, at_ns INTEGER NOT NULL, value REAL NOT NULL);
		CREATE INDEX IF NOT EXISTS aws_cloudwatch_samples_identity ON aws_cloudwatch_samples(region,namespace,metric,dimensions,at_ns);
		CREATE INDEX IF NOT EXISTS aws_cloudwatch_samples_age ON aws_cloudwatch_samples(region,namespace,at_ns);`)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &Store{db: db, now: time.Now}, nil
}

func (s *Store) put(ctx context.Context, region, namespace string, samples []sample, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The first statement acquires SQLite's writer lock before the quota count.
	cutoff := now.Add(-retention).UnixNano()
	if _, err = tx.ExecContext(ctx, `DELETE FROM aws_cloudwatch_samples WHERE region=? AND namespace=? AND at_ns<?`, region, namespace, cutoff); err != nil {
		return err
	}
	var scoped, expired, team int
	if err = tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER (WHERE at_ns<?) FROM aws_cloudwatch_samples WHERE region=? AND namespace=?`, cutoff, region, namespace).Scan(&scoped, &expired); err != nil {
		return err
	}
	if expired != 0 {
		return errors.New("expired metric sample deletion was not acknowledged")
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_cloudwatch_samples`).Scan(&team); err != nil {
		return err
	}
	if len(samples) > maxNamespaceSamples-scoped || len(samples) > maxTeamSamples-team {
		return invalid("Local retained metric sample quota exceeded.")
	}
	insert, err := tx.PrepareContext(ctx, `INSERT INTO aws_cloudwatch_samples(region,namespace,metric,dimensions,unit,at_ns,value) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer insert.Close()
	for _, point := range samples {
		result, err := insert.ExecContext(ctx, region, namespace, point.name, point.dimensions, point.unit, point.at.UnixNano(), point.value)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return errors.New("metric sample insertion was not acknowledged")
		}
	}
	return tx.Commit()
}

type bucket struct {
	seconds int64
	unit    string
}
type aggregate struct {
	min, max, sum, correction float64
	count                     int
}

func (a *aggregate) add(v float64) {
	if a.count == 0 {
		a.min, a.max = v, v
	} else {
		a.min = math.Min(a.min, v)
		a.max = math.Max(a.max, v)
	}
	// Preserve small samples when larger positive and negative samples cancel.
	total := a.sum + v
	if math.Abs(a.sum) >= math.Abs(v) {
		a.correction += (a.sum - total) + v
	} else {
		a.correction += (v - total) + a.sum
	}
	a.sum = total
	a.count++
}

type datapoint struct {
	Timestamp   string
	SampleCount *float64 `xml:",omitempty"`
	Average     *float64 `xml:",omitempty"`
	Sum         *float64 `xml:",omitempty"`
	Minimum     *float64 `xml:",omitempty"`
	Maximum     *float64 `xml:",omitempty"`
	Unit        string
}

func (s *Store) get(ctx context.Context, region, namespace string, q query, now time.Time) ([]datapoint, error) {
	start := q.start
	if cutoff := now.Add(-retention); start.Before(cutoff) {
		start = cutoff
	}
	end := q.end
	// A Query can span dates beyond int64 nanoseconds. Clamp the upper
	// boundary before converting it, independent of the current write horizon.
	if latest := time.Unix(0, math.MaxInt64); end.After(latest) {
		end = latest
	}
	if !start.Before(end) {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT at_ns,unit,value FROM aws_cloudwatch_samples
		WHERE region=? AND namespace=? AND metric=? AND dimensions=? AND at_ns>=? AND at_ns<?
		AND (?='' OR unit=?) ORDER BY at_ns,id`, region, namespace, q.name, q.dimensions, start.UnixNano(), end.UnixNano(), q.unit, q.unit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	buckets := map[bucket]*aggregate{}
	var keys []bucket
	for rows.Next() {
		var ns int64
		var unit string
		var value float64
		if err = rows.Scan(&ns, &unit, &value); err != nil {
			return nil, err
		}
		elapsed := time.Unix(0, ns).Unix() - q.start.Unix()
		key := bucket{q.start.Unix() + elapsed/q.period*q.period, unit}
		a := buckets[key]
		if a == nil {
			if len(keys) == maxPoints {
				return nil, invalid("More than 1440 unit/time datapoints match.")
			}
			a = &aggregate{}
			buckets[key] = a
			keys = append(keys, key)
		}
		a.add(value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(keys, func(a, b bucket) int {
		if order := cmp.Compare(a.seconds, b.seconds); order != 0 {
			return order
		}
		return cmp.Compare(a.unit, b.unit)
	})
	points := make([]datapoint, 0, len(keys))
	for _, key := range keys {
		a := buckets[key]
		sum, count := a.sum+a.correction, float64(a.count)
		d := datapoint{Timestamp: time.Unix(key.seconds, 0).UTC().Format(time.RFC3339), Unit: key.unit}
		if q.stats["Sum"] {
			d.Sum = &sum
		}
		if q.stats["Average"] {
			average := sum / count
			d.Average = &average
		}
		if q.stats["SampleCount"] {
			d.SampleCount = &count
		}
		if q.stats["Minimum"] {
			d.Minimum = &a.min
		}
		if q.stats["Maximum"] {
			d.Maximum = &a.max
		}
		points = append(points, d)
	}
	return points, nil
}
