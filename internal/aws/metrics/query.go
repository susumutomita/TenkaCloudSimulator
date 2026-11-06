package metrics

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

type cursor struct {
	Version                         int `json:"version"`
	Account, Region, Options, After string
	Revision, AsOf                  int64
}

func (s *Store) seal(c cursor) (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func (s *Store) open(token, region, options string, revision int64, now time.Time) (cursor, error) {
	var c cursor
	if len(token) > 2048 {
		return c, invalid("NextToken is invalid.")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return c, invalid("NextToken is invalid.")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return c, invalid("NextToken is invalid.")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, invalid("NextToken is invalid.")
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(data)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return c, invalid("NextToken is invalid.")
	}
	if err = strictDocument(string(data), &c); err != nil {
		return c, invalid("NextToken is invalid.")
	}
	age := now.Sub(time.Unix(0, c.AsOf))
	id, err := hex.DecodeString(c.After)
	if c.Version != 1 || c.Account != AccountID || c.Region != region || c.Options != options || c.Revision != revision || c.AsOf <= 0 || age < 0 || age > cursorLife || err != nil || len(id) != 32 {
		return c, invalid("NextToken is stale or has a different scope.")
	}
	return c, nil
}
func revisionTx(ctx context.Context, tx *sql.Tx, region string) (int64, error) {
	var rev int64
	err := tx.QueryRowContext(ctx, "SELECT revision FROM aws_metrics_regions WHERE region=?", region).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil || rev < 0 {
		return 0, internal()
	}
	return rev, nil
}
func decodeMetric(id, namespace, name, document string) (Metric, error) {
	var metric Metric
	if err := strictDocument(document, &metric); err != nil {
		return metric, internal()
	}
	dims, err := canonicalDimensions(metric.Dimensions)
	if err != nil || !namespaceOK(metric.Namespace) || !nameOK(metric.Name, 255, false, false) {
		return metric, internal()
	}
	metric.Dimensions = dims
	actual, canonical, err := identity(metric)
	if err != nil || actual != id || canonical != document || namespace != metric.Namespace || name != metric.Name {
		return metric, internal()
	}
	return metric, nil
}
func matches(metric Metric, input ListInput) bool {
	if input.Namespace != nil && metric.Namespace != *input.Namespace || input.MetricName != nil && metric.Name != *input.MetricName {
		return false
	}
	for _, filter := range input.Dimensions {
		found := false
		for _, d := range metric.Dimensions {
			if d.Name == filter.Name && (filter.Value == nil || d.Value == *filter.Value) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// This upper bound includes XML escaping. It permits smaller valid pages instead
// of constructing an unbounded response or inventing a ListMetrics Limit field.
func metricCost(metric Metric) int {
	n := 256 + 6*(len(metric.Namespace)+len(metric.Name))
	for _, d := range metric.Dimensions {
		n += 96 + 6*(len(d.Name)+len(d.Value))
	}
	return n
}
func (s *Store) ListMetrics(ctx context.Context, scope Scope, input ListInput) (ListOutput, error) {
	var out ListOutput
	err := s.run(ctx, scope, "ListMetrics", func(tx *sql.Tx, now time.Time) error {
		var e error
		out, e = s.listTx(ctx, tx, scope.Region, input, now)
		return e
	})
	if err != nil {
		return ListOutput{}, err
	}
	return out, nil
}
func (s *Store) listTx(ctx context.Context, tx *sql.Tx, region string, input ListInput, now time.Time) (ListOutput, error) {
	out := ListOutput{Metrics: []Metric{}}
	input, err := normalizeList(input)
	if err != nil {
		return out, err
	}
	rev, err := revisionTx(ctx, tx, region)
	if err != nil {
		return out, err
	}
	filterBody, err := json.Marshal(struct {
		Namespace, Name *string
		Dimensions      []DimensionFilter
	}{input.Namespace, input.MetricName, input.Dimensions})
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(filterBody)
	options := hex.EncodeToString(sum[:])
	c := cursor{Version: 1, Account: AccountID, Region: region, Options: options, Revision: rev, AsOf: now.UnixNano()}
	if input.NextToken != "" {
		c, err = s.open(input.NextToken, region, options, rev, now)
		if err != nil {
			return out, err
		}
	}
	// No durable collection or regional initialization occurs on reads.
	rows, err := tx.QueryContext(ctx, `SELECT identity,namespace,name,document,last_ingested FROM aws_metrics_series s WHERE region=? AND last_ingested>=? AND EXISTS(SELECT 1 FROM aws_metrics_points p WHERE p.region=s.region AND p.series_id=s.identity AND p.stamp>=?) ORDER BY identity`, region, time.Unix(0, c.AsOf).Add(-listingAge).UnixNano(), time.Unix(0, c.AsOf).Add(-retention).UnixNano())
	if err != nil {
		return out, err
	}
	defer rows.Close()
	type entry struct {
		id     string
		metric Metric
	}
	all := []entry{}
	for rows.Next() {
		var id, namespace, name, document string
		var last int64
		if err = rows.Scan(&id, &namespace, &name, &document, &last); err != nil {
			return out, err
		}
		if last <= 0 {
			return out, internal()
		}
		metric, e := decodeMetric(id, namespace, name, document)
		if e != nil {
			return out, e
		}
		if matches(metric, input) {
			all = append(all, entry{id, metric})
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	start := 0
	if c.After != "" {
		found := false
		for i, e := range all {
			if e.id == c.After {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return out, invalid("NextToken position is unavailable.")
		}
	}
	used := 4096
	index := start
	for index < len(all) {
		cost := metricCost(all[index].metric)
		if len(out.Metrics) >= s.limits.PageSize || len(out.Metrics) > 0 && used+cost > 1<<20 {
			break
		}
		if used+cost > 1<<20 {
			return out, internal()
		}
		out.Metrics = append(out.Metrics, all[index].metric)
		used += cost
		index++
	}
	if index < len(all) {
		c.After = all[index-1].id
		out.NextToken, err = s.seal(c)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
func (s *Store) GetMetricStatistics(ctx context.Context, scope Scope, input StatisticsInput) (StatisticsOutput, error) {
	var out StatisticsOutput
	err := s.run(ctx, scope, "GetMetricStatistics", func(tx *sql.Tx, now time.Time) error {
		var e error
		out, e = s.statisticsTx(ctx, tx, scope.Region, input, now)
		return e
	})
	if err != nil {
		return StatisticsOutput{}, err
	}
	return out, nil
}
func (s *Store) statisticsTx(ctx context.Context, tx *sql.Tx, region string, input StatisticsInput, now time.Time) (StatisticsOutput, error) {
	out := StatisticsOutput{}
	input, err := normalizeStatistics(input, now)
	if err != nil {
		return out, err
	}
	out.Label = input.MetricName
	out.Statistics = append([]string{}, input.Statistics...)
	out.Datapoints = []Datapoint{}
	id, _, err := identity(Metric{input.Namespace, input.MetricName, input.Dimensions})
	if err != nil {
		return out, err
	}
	var document, namespace, name string
	err = tx.QueryRowContext(ctx, "SELECT namespace,name,document FROM aws_metrics_series WHERE region=? AND identity=?", region, id).Scan(&namespace, &name, &document)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if _, err = decodeMetric(id, namespace, name, document); err != nil {
		return out, err
	}
	start := input.Start.UnixNano()
	if cutoff := now.Add(-retention).UnixNano(); cutoff > start {
		start = cutoff
	}
	rows, err := tx.QueryContext(ctx, "SELECT stamp,unit,document FROM aws_metrics_points WHERE region=? AND series_id=? AND stamp>=? AND stamp<? ORDER BY id", region, id, start, input.End.UnixNano())
	if err != nil {
		return out, err
	}
	defer rows.Close()
	type bucket struct {
		stamp int64
		unit  string
	}
	groups := map[bucket]Datapoint{}
	period := int64(input.Period) * int64(time.Second)
	for rows.Next() {
		var stamp int64
		var unit, body string
		if err = rows.Scan(&stamp, &unit, &body); err != nil {
			return out, err
		}
		var point pointDocument
		if err = strictDocument(body, &point); err != nil {
			return out, err
		}
		canonical, e := json.Marshal(point)
		if e != nil || string(canonical) != body || point.Timestamp != stamp || point.Unit != unit || stamp <= 0 || !units[unit] || !finite(point.Value) {
			return out, internal()
		}
		if input.Unit != nil && unit != *input.Unit {
			continue
		}
		b := bucket{input.Start.UnixNano() + ((stamp-input.Start.UnixNano())/period)*period, unit}
		d, found := groups[b]
		if !found {
			d = Datapoint{Timestamp: time.Unix(0, b.stamp).UTC(), Unit: unit, Minimum: point.Value, Maximum: point.Value}
		}
		d.SampleCount++
		d.Sum += point.Value
		if point.Value < d.Minimum {
			d.Minimum = point.Value
		}
		if point.Value > d.Maximum {
			d.Maximum = point.Value
		}
		d.Average = d.Sum / d.SampleCount
		groups[b] = d
		if len(groups) > 1440 {
			return out, invalid("At most 1440 unit/time datapoints may be returned.")
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	for _, d := range groups {
		out.Datapoints = append(out.Datapoints, d)
	}
	sort.Slice(out.Datapoints, func(i, j int) bool {
		a, b := out.Datapoints[i], out.Datapoints[j]
		if a.Timestamp.Equal(b.Timestamp) {
			return a.Unit < b.Unit
		}
		return a.Timestamp.Before(b.Timestamp)
	})
	return out, nil
}
