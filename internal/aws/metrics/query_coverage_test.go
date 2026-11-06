package metrics

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func metricsQueryContractPut(t *testing.T, s *Store, region, namespace, name string, dims []Dimension, now time.Time) {
	t.Helper()
	if e := s.PutMetricData(context.Background(), Scope{region, allow}, PutInput{namespace, []Datum{{MetricName: name, Value: 3, Dimensions: dims, Timestamp: &now}}}); e != nil {
		t.Fatal("native fixture put failed", e)
	}
}
func TestMetricsQueryContractExactAndNameOnlyDimensionFilters(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	ctx := context.Background()
	scope := Scope{"us-east-1", allow}
	metricsQueryContractPut(t, s, scope.Region, "Practice/App", "Metric", []Dimension{{"color", "blue"}, {"shape", "round"}}, *now)
	metricsQueryContractPut(t, s, scope.Region, "Practice/App", "Metric", []Dimension{{"color", "red"}}, *now)
	metricsQueryContractPut(t, s, scope.Region, "Other/App", "Other", nil, *now)
	metricsQueryContractPut(t, s, "us-west-2", "Practice/App", "Peer", nil, *now)
	before := frame(t, db)
	cases := []struct {
		name              string
		in                ListInput
		want              int
		namespace, metric string
	}{
		{"namespace", ListInput{Namespace: pointer("Practice/App")}, 2, "Practice/App", "Metric"},
		{"metric", ListInput{MetricName: pointer("Other")}, 1, "Other/App", "Other"},
		{"name_only", ListInput{Dimensions: []DimensionFilter{{Name: "color"}}}, 2, "Practice/App", "Metric"},
		{"exact", ListInput{Dimensions: []DimensionFilter{{Name: "color", Value: pointer("blue")}, {Name: "shape"}}}, 1, "Practice/App", "Metric"},
		{"wrong_value", ListInput{Dimensions: []DimensionFilter{{Name: "color", Value: pointer("green")}}}, 0, "", ""},
		{"missing_name", ListInput{Dimensions: []DimensionFilter{{Name: "absent"}}}, 0, "", ""},
		{"missing_namespace", ListInput{Namespace: pointer("Missing/App")}, 0, "", ""}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, e := s.ListMetrics(ctx, scope, c.in)
			if e != nil || len(out.Metrics) != c.want || out.NextToken != "" {
				t.Fatal("list filter cardinality/token changed")
			}
			for _, m := range out.Metrics {
				if m.Namespace != c.namespace || m.Name != c.metric {
					t.Fatal("filter exposed wrong metric")
				}
			}
			if c.name == "exact" && !reflect.DeepEqual(out.Metrics[0].Dimensions, []Dimension{{"color", "blue"}, {"shape", "round"}}) {
				t.Fatal("exact dimensions changed")
			}
			unchanged(t, db, before)
		})
	}
}
func TestMetricsQueryContractContinuationShapesScopeAndExpiry(t *testing.T) {
	limits := defaultLimits
	limits.PageSize = 1
	s, db, now, _ := fixture(t, limits)
	ctx := context.Background()
	scope := Scope{"us-east-1", allow}
	for _, name := range []string{"First", "Second"} {
		metricsQueryContractPut(t, s, scope.Region, "Practice/App", name, nil, *now)
	}
	first, e := s.ListMetrics(ctx, scope, ListInput{})
	if e != nil || len(first.Metrics) != 1 || first.NextToken == "" {
		t.Fatal("real continuation missing")
	}
	before := frame(t, db)
	bad := []string{strings.Repeat("x", 2049), "one", "a.b.c", "%.AA", "e30.%", "e30.AA", first.NextToken + "x"}
	for i, token := range bad {
		t.Run(fmt.Sprint("shape", i), func(t *testing.T) {
			out, e := s.ListMetrics(ctx, scope, ListInput{NextToken: token})
			code(t, e, "InvalidParameterValue")
			if len(out.Metrics) != 0 || out.NextToken != "" {
				t.Fatal("invalid token published data")
			}
			unchanged(t, db, before)
		})
	}
	_, e = s.ListMetrics(ctx, Scope{"us-west-2", allow}, ListInput{NextToken: first.NextToken})
	code(t, e, "InvalidParameterValue")
	unchanged(t, db, before)
	_, e = s.ListMetrics(ctx, scope, ListInput{NextToken: first.NextToken, Namespace: pointer("Practice/App")})
	code(t, e, "InvalidParameterValue")
	unchanged(t, db, before)
	*now = now.Add(-time.Nanosecond)
	_, e = s.ListMetrics(ctx, scope, ListInput{NextToken: first.NextToken})
	code(t, e, "InvalidParameterValue")
	unchanged(t, db, before)
	*now = now.Add(time.Hour + time.Nanosecond)
	second, e := s.ListMetrics(ctx, scope, ListInput{NextToken: first.NextToken})
	if e != nil || len(second.Metrics) != 1 || second.NextToken != "" || second.Metrics[0].Name == first.Metrics[0].Name {
		t.Fatal("inclusive one-hour cursor boundary changed")
	}
	unchanged(t, db, before)
	*now = now.Add(time.Nanosecond)
	_, e = s.ListMetrics(ctx, scope, ListInput{NextToken: first.NextToken})
	code(t, e, "InvalidParameterValue")
	unchanged(t, db, before)
	// Internal cursor-metadata admission port only: these MAC-valid variants are
	// not producible by public ListMetrics and do not demonstrate a MAC forgery.
	body, e := base64.RawURLEncoding.DecodeString(strings.Split(first.NextToken, ".")[0])
	if e != nil {
		t.Fatal(e)
	}
	var captured cursor
	if json.Unmarshal(body, &captured) != nil {
		t.Fatal("issued cursor decode failed")
	}
	*now = time.Unix(0, captured.AsOf)
	mutations := []struct {
		name   string
		change func(*cursor)
	}{{"version", func(c *cursor) { c.Version = 2 }}, {"account", func(c *cursor) { c.Account = "111111111111" }}, {"after_nothex", func(c *cursor) { c.After = "nothex" }}, {"after_short", func(c *cursor) { c.After = "aa" }}, {"absent_position", func(c *cursor) { c.After = strings.Repeat("0", 64) }}, {"nonpositive_asof", func(c *cursor) { c.AsOf = 0 }}}
	for _, m := range mutations {
		t.Run("internal_metadata_"+m.name, func(t *testing.T) {
			c := captured
			m.change(&c)
			token, e := s.seal(c)
			if e != nil {
				t.Fatal(e)
			}
			out, e := s.ListMetrics(ctx, scope, ListInput{NextToken: token})
			code(t, e, "InvalidParameterValue")
			if len(out.Metrics) != 0 {
				t.Fatal("invalid signed metadata exposed page")
			}
			unchanged(t, db, before)
		})
	}
}
func TestMetricsQueryContractResponseBudgetPagesRetainAllDimensions(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	scope := Scope{"us-east-1", allow}
	dims := make([]Dimension, 30)
	for i := range dims {
		dims[i] = Dimension{fmt.Sprintf("d%02d", i), strings.Repeat("x", 1024)}
	}
	expected := map[string]bool{}
	for i := 0; i < 7; i++ {
		name := fmt.Sprintf("Big%d", i)
		expected[name] = true
		metricsQueryContractPut(t, s, scope.Region, "Practice/App", name, dims, *now)
	}
	before := frame(t, db)
	out, e := s.ListMetrics(context.Background(), scope, ListInput{})
	if e != nil || len(out.Metrics) != 5 || out.NextToken == "" {
		t.Fatal("escaped-response budget did not page five bounded records")
	}
	seen := map[string]bool{}
	for _, m := range out.Metrics {
		if !expected[m.Name] || seen[m.Name] || !reflect.DeepEqual(m.Dimensions, dims) {
			t.Fatal("first budget page altered or duplicated dimensions")
		}
		seen[m.Name] = true
	}
	unchanged(t, db, before)
	tail, e := s.ListMetrics(context.Background(), scope, ListInput{NextToken: out.NextToken})
	if e != nil || len(tail.Metrics) != 2 || tail.NextToken != "" {
		t.Fatal("response-budget continuation did not finish exact remaining records")
	}
	for _, m := range tail.Metrics {
		if !expected[m.Name] || seen[m.Name] || !reflect.DeepEqual(m.Dimensions, dims) {
			t.Fatal("tail page altered/duplicated dimensions")
		}
		seen[m.Name] = true
	}
	if !reflect.DeepEqual(seen, expected) {
		t.Fatal("response-budget pages lost records")
	}
	unchanged(t, db, before)
}
func TestMetricsQueryContractPersistedReadFaultsFrameAndRepair(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	scope := Scope{"us-east-1", allow}
	ctx := context.Background()
	metricsQueryContractPut(t, s, scope.Region, "Practice/App", "Metric", nil, *now)
	metricsQueryContractPut(t, s, "us-west-2", "Peer/App", "Peer", nil, *now)
	stats := StatisticsInput{Namespace: "Practice/App", MetricName: "Metric", Start: now.Truncate(time.Minute), End: now.Add(time.Minute), Period: 60, Statistics: []string{"Sum"}}
	var metricDoc, pointDoc string
	if e := db.QueryRow("SELECT document FROM aws_metrics_series WHERE region='us-east-1'").Scan(&metricDoc); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow("SELECT document FROM aws_metrics_points WHERE region='us-east-1'").Scan(&pointDoc); e != nil {
		t.Fatal(e)
	}
	faults := []struct {
		name, table, bad, restore string
		both                      bool
	}{
		{"metric_malformed", "aws_metrics_series", "{", metricDoc, true},
		{"metric_unknown_field", "aws_metrics_series", strings.TrimSuffix(metricDoc, "}") + `,"extra":1}`, metricDoc, true},
		{"metric_noncanonical", "aws_metrics_series", metricDoc + " ", metricDoc, true},
		{"metric_bad_dimensions", "aws_metrics_series", `{"namespace":"Practice/App","name":"Metric","dimensions":[{"name":"a","value":"x"},{"name":"a","value":"y"}]}`, metricDoc, true},
		{"metric_bad_identity", "aws_metrics_series", `{"namespace":"Other/App","name":"Metric","dimensions":[]}`, metricDoc, true},
		{"point_malformed", "aws_metrics_points", "{", pointDoc, false},
		{"point_noncanonical", "aws_metrics_points", pointDoc + " ", pointDoc, false},
		{"point_wrong_stamp", "aws_metrics_points", `{"timestamp":1,"unit":"None","value":3}`, pointDoc, false},
		{"point_wrong_unit", "aws_metrics_points", strings.Replace(pointDoc, `"None"`, `"Count"`, 1), pointDoc, false}}
	for _, f := range faults {
		t.Run(f.name, func(t *testing.T) {
			if _, e := db.Exec("UPDATE "+f.table+" SET document=? WHERE region='us-east-1'", f.bad); e != nil {
				t.Fatal(e)
			}
			before := frame(t, db)
			out, e := s.GetMetricStatistics(ctx, scope, stats)
			code(t, e, "InternalServiceError")
			if out.Label != "" || len(out.Datapoints) != 0 {
				t.Fatal("failed statistics read published partial data")
			}
			unchanged(t, db, before)
			if f.both {
				list, e := s.ListMetrics(ctx, scope, ListInput{})
				code(t, e, "InternalServiceError")
				if len(list.Metrics) != 0 || list.NextToken != "" {
					t.Fatal("failed list published partial page")
				}
				unchanged(t, db, before)
			}
			if _, e := db.Exec("UPDATE "+f.table+" SET document=? WHERE region='us-east-1'", f.restore); e != nil {
				t.Fatal(e)
			}
			repaired := frame(t, db)
			good, e := s.GetMetricStatistics(ctx, scope, stats)
			if e != nil || len(good.Datapoints) != 1 || good.Datapoints[0].Sum != 3 {
				t.Fatal("repaired persisted read failed")
			}
			unchanged(t, db, repaired)
		})
	}
	for _, fault := range []struct{ table, column string }{{"aws_metrics_series", "document"}, {"aws_metrics_points", "stamp"}} {
		t.Run("missing_query_column_"+fault.table, func(t *testing.T) {
			if _, e := db.Exec("ALTER TABLE " + fault.table + " RENAME COLUMN " + fault.column + " TO unavailable"); e != nil {
				t.Fatal(e)
			}
			before := frame(t, db)
			out, e := s.GetMetricStatistics(ctx, scope, stats)
			code(t, e, "InternalServiceError")
			if out.Label != "" || len(out.Datapoints) != 0 {
				t.Fatal("query failure leaked partial result")
			}
			unchanged(t, db, before)
			list, e := s.ListMetrics(ctx, scope, ListInput{})
			code(t, e, "InternalServiceError")
			if len(list.Metrics) != 0 {
				t.Fatal("query failure leaked list")
			}
			unchanged(t, db, before)
			if _, e := db.Exec("ALTER TABLE " + fault.table + " RENAME COLUMN unavailable TO " + fault.column); e != nil {
				t.Fatal(e)
			}
			repaired := frame(t, db)
			good, e := s.ListMetrics(ctx, scope, ListInput{})
			if e != nil || len(good.Metrics) != 1 {
				t.Fatal("schema repair failed")
			}
			unchanged(t, db, repaired)
		})
	}
}

func TestMetricsQueryContractScanAndRevisionFailureFrames(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	ctx := context.Background()
	scope := Scope{"us-east-1", allow}
	metricsQueryContractPut(t, s, scope.Region, "Practice/App", "Metric", nil, *now)
	metricsQueryContractPut(t, s, "us-west-2", "Peer/App", "Peer", nil, *now)
	for _, c := range []struct{ name, column, bad string }{{"last_scan", "last_ingested", "bad-integer"}, {"name_mismatch", "name", "Wrong"}, {"namespace_mismatch", "namespace", "Wrong/App"}} {
		t.Run(c.name, func(t *testing.T) {
			var old any
			if e := db.QueryRow("SELECT " + c.column + " FROM aws_metrics_series WHERE region='us-east-1'").Scan(&old); e != nil {
				t.Fatal(e)
			}
			if _, e := db.Exec("UPDATE aws_metrics_series SET "+c.column+"=? WHERE region='us-east-1'", c.bad); e != nil {
				t.Fatal(e)
			}
			before := frame(t, db)
			out, e := s.ListMetrics(ctx, scope, ListInput{})
			code(t, e, "InternalServiceError")
			if len(out.Metrics) != 0 || out.NextToken != "" {
				t.Fatal("scan/metadata failure published page")
			}
			unchanged(t, db, before)
			if _, e := db.Exec("UPDATE aws_metrics_series SET "+c.column+"=? WHERE region='us-east-1'", old); e != nil {
				t.Fatal(e)
			}
			repaired := frame(t, db)
			out, e = s.ListMetrics(ctx, scope, ListInput{})
			if e != nil || len(out.Metrics) != 1 || out.Metrics[0].Name != "Metric" {
				t.Fatal("scan/metadata repair failed")
			}
			unchanged(t, db, repaired)
		})
	}
	if _, e := db.Exec("ALTER TABLE aws_metrics_regions RENAME COLUMN revision TO unavailable"); e != nil {
		t.Fatal(e)
	}
	before := frame(t, db)
	out, e := s.ListMetrics(ctx, scope, ListInput{})
	code(t, e, "InternalServiceError")
	if len(out.Metrics) != 0 {
		t.Fatal("revision lookup failure leaked page")
	}
	unchanged(t, db, before)
	if _, e := db.Exec("ALTER TABLE aws_metrics_regions RENAME COLUMN unavailable TO revision"); e != nil {
		t.Fatal(e)
	}
	repaired := frame(t, db)
	out, e = s.ListMetrics(ctx, scope, ListInput{})
	if e != nil || len(out.Metrics) != 1 {
		t.Fatal("revision lookup repair failed")
	}
	unchanged(t, db, repaired)
}
