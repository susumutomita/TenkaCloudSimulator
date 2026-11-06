package metrics

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMetricsTypesContractNamesDimensionsAndRegionBoundary(t *testing.T) {
	for _, v := range []string{"", " ", ":hidden", "AWS/EC2", "雪", "a\x00b", strings.Repeat("a", 256)} {
		if namespaceOK(v) {
			t.Fatal("invalid custom namespace admitted")
		}
	}
	if !namespaceOK("Practice/App") || !nameOK("雪/%2F", 255, false, false) {
		t.Fatal("valid custom namespace or Unicode metric refused")
	}
	for _, v := range []string{"abc", "usa-east-1", "us--1", "us-East-1", "us-east-x", "us-e-1-extra-1-too", "us-east-"} {
		if regionOK(v) {
			t.Fatal("invalid region shape admitted")
		}
	}
	for _, v := range []string{"us-east-1", "us-gov-west-1", "cn-north-1"} {
		if !regionOK(v) {
			t.Fatal("bounded region shape refused")
		}
	}
	original := []Dimension{{"z", "last"}, {"a", "first"}}
	out, e := canonicalDimensions(original)
	if e != nil || !reflect.DeepEqual(out, []Dimension{{"a", "first"}, {"z", "last"}}) || original[0].Name != "z" {
		t.Fatal("dimension canonicalization reordered caller slice")
	}
	out[0].Value = "changed"
	if original[1].Value != "first" {
		t.Fatal("canonical dimensions alias caller storage")
	}
	for _, dims := range [][]Dimension{{{"same", "a"}, {"same", "b"}}, {{"雪", "v"}}, {{"ok", string([]byte{0xff})}}, {{":reserved", "v"}}, {{"ok", strings.Repeat("x", 1025)}}, make([]Dimension, 31)} {
		_, e := canonicalDimensions(dims)
		code(t, e, "InvalidParameterValue")
	}
}
func TestMetricsTypesContractPutAtomicAdmissionAndLiteralDefaults(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 30, 0, time.UTC)
	valid := PutInput{"Practice/App", []Datum{{MetricName: "Metric", Value: 0, Dimensions: []Dimension{{"z", "last"}, {"a", "first"}}}}}
	out, e := normalizePut(valid, now)
	if e != nil || len(out) != 1 || out[0].Point.Unit != "None" || out[0].Point.Value != 0 || out[0].Point.Timestamp != now.UnixNano() || out[0].Metric.Dimensions[0].Name != "a" || valid.Data[0].Dimensions[0].Name != "z" {
		t.Fatal("literal defaults/copy were lost")
	}
	for _, v := range []float64{math.Ldexp(1, 360), -math.Ldexp(1, 360)} {
		in := PutInput{"Practice/App", []Datum{{MetricName: "Metric", Value: v}}}
		out, e := normalizePut(in, now)
		if e != nil || out[0].Point.Value != v {
			t.Fatal("finite inclusive magnitude changed")
		}
	}
	cases := []struct {
		name   string
		mutate func(*PutInput)
		want   string
	}{
		{"bad_namespace", func(v *PutInput) { v.Namespace = "AWS/Reserved" }, "InvalidParameterValue"}, {"empty", func(v *PutInput) { v.Data = nil }, "InvalidParameterValue"}, {"over1000", func(v *PutInput) { v.Data = make([]Datum, 1001) }, "InvalidParameterValue"},
		{"bad_metric", func(v *PutInput) { v.Data[0].MetricName = "\x00" }, "InvalidParameterValue"}, {"nan", func(v *PutInput) { v.Data[0].Value = math.NaN() }, "InvalidParameterValue"}, {"infinity", func(v *PutInput) { v.Data[0].Value = math.Inf(1) }, "InvalidParameterValue"}, {"magnitude", func(v *PutInput) { v.Data[0].Value = math.Ldexp(1, 361) }, "InvalidParameterValue"},
		{"bad_dimensions", func(v *PutInput) { v.Data[0].Dimensions = []Dimension{{"a", "x"}, {"a", "y"}} }, "InvalidParameterValue"}, {"high_resolution", func(v *PutInput) { v.Data[0].StorageResolution = pointer(1) }, "InvalidParameterCombination"}, {"bad_unit", func(v *PutInput) { v.Data[0].Unit = pointer("count") }, "InvalidParameterValue"},
		{"too_old", func(v *PutInput) { v.Data[0].Timestamp = pointer(now.Add(-14*24*time.Hour - time.Nanosecond)) }, "InvalidParameterValue"}, {"too_future", func(v *PutInput) { v.Data[0].Timestamp = pointer(now.Add(2*time.Hour + time.Nanosecond)) }, "InvalidParameterValue"},
		{"invalid_later_record", func(v *PutInput) { v.Data = append(v.Data, Datum{MetricName: "Later", Value: math.Inf(-1)}) }, "InvalidParameterValue"}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := PutInput{"Practice/App", []Datum{{MetricName: "Metric", Value: 1}}}
			c.mutate(&in)
			out, e := normalizePut(in, now)
			code(t, e, c.want)
			if out != nil {
				t.Fatal("failed batch published partial normalized input")
			}
		})
	}
	for _, stamp := range []time.Time{now.Add(-14 * 24 * time.Hour), now.Add(2 * time.Hour)} {
		out, e := normalizePut(PutInput{"Practice/App", []Datum{{MetricName: "Metric", Timestamp: &stamp, StorageResolution: pointer(60)}}}, now)
		if e != nil || out[0].Point.Timestamp != stamp.UnixNano() {
			t.Fatal("inclusive ingestion edge refused")
		}
	}
	_, e = normalizePut(PutInput{"Practice/App", []Datum{{MetricName: "Metric", Timestamp: pointer(time.Unix(0, 0))}}}, time.Unix(3600, 0))
	code(t, e, "InvalidParameterValue")
	_, e = normalizePut(valid, time.Unix(0, 0))
	code(t, e, "InvalidParameterValue")
}
func TestMetricsTypesContractListAndStatisticsRefusalsAndCopies(t *testing.T) {
	list := ListInput{Dimensions: []DimensionFilter{{Name: "z"}, {Name: "a", Value: pointer("blue")}}}
	norm, e := normalizeList(list)
	if e != nil || norm.Dimensions[0].Name != "a" || list.Dimensions[0].Name != "z" {
		t.Fatal("list filter normalization mutated caller")
	}
	for _, in := range []ListInput{{Namespace: pointer("AWS/X")}, {MetricName: pointer("")}, {Dimensions: make([]DimensionFilter, 11)}, {Dimensions: []DimensionFilter{{Name: "a"}, {Name: "a"}}}, {Dimensions: []DimensionFilter{{Name: "color", Value: pointer("\x00")}}}} {
		_, e := normalizeList(in)
		code(t, e, "InvalidParameterValue")
	}
	now := time.Date(2026, 1, 2, 12, 0, 30, 0, time.UTC)
	fresh := func() StatisticsInput {
		return StatisticsInput{Namespace: "Practice/App", MetricName: "Metric", Start: now.Add(-time.Minute), End: now, Period: 60, Statistics: []string{"Sum", "Average"}, Dimensions: []Dimension{{"z", "v"}, {"a", "w"}}}
	}
	cases := []struct {
		name   string
		mutate func(*StatisticsInput)
		want   string
	}{
		{"namespace", func(v *StatisticsInput) { v.Namespace = "AWS/X" }, "InvalidParameterValue"}, {"dimension", func(v *StatisticsInput) { v.Dimensions = []Dimension{{"a", "x"}, {"a", "y"}} }, "InvalidParameterValue"},
		{"period0", func(v *StatisticsInput) { v.Period = 0 }, "InvalidParameterCombination"}, {"period61", func(v *StatisticsInput) { v.Period = 61 }, "InvalidParameterCombination"}, {"period_over15days", func(v *StatisticsInput) { v.Period = 1296060 }, "InvalidParameterCombination"},
		{"start_range", func(v *StatisticsInput) { v.Start = time.Unix(0, 0) }, "InvalidParameterValue"}, {"end_range", func(v *StatisticsInput) { v.End = time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC) }, "InvalidParameterValue"}, {"end_before_start", func(v *StatisticsInput) { v.End = v.Start }, "InvalidParameterValue"},
		{"older15days", func(v *StatisticsInput) { v.Start = now.Add(-15*24*time.Hour - time.Nanosecond) }, "InvalidParameterCombination"}, {"over1440buckets", func(v *StatisticsInput) { v.Start = now.Add(-1441 * time.Minute) }, "InvalidParameterValue"},
		{"no_stats", func(v *StatisticsInput) { v.Statistics = nil }, "MissingParameter"}, {"six_stats", func(v *StatisticsInput) { v.Statistics = []string{"Sum", "Sum", "Sum", "Sum", "Sum", "Sum"} }, "MissingParameter"}, {"unknown_stat", func(v *StatisticsInput) { v.Statistics = []string{"p99"} }, "InvalidParameterValue"}, {"duplicate_stat", func(v *StatisticsInput) { v.Statistics = []string{"Sum", "Sum"} }, "InvalidParameterValue"}, {"bad_unit", func(v *StatisticsInput) { v.Unit = pointer("count") }, "InvalidParameterValue"}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := fresh()
			c.mutate(&in)
			_, e := normalizeStatistics(in, now)
			code(t, e, c.want)
		})
	}
	in := fresh()
	out, e := normalizeStatistics(in, now)
	if e != nil || out.Start.Format(time.RFC3339) != "2026-01-02T11:59:00Z" || out.Dimensions[0].Name != "a" || in.Dimensions[0].Name != "z" {
		t.Fatal("statistics canonical identity/start changed")
	}
	out.Statistics[0] = "Minimum"
	if in.Statistics[0] != "Sum" {
		t.Fatal("normalized statistics alias caller slice")
	}
}
func TestMetricsTypesContractStoredDocumentSingleValueAndUnknownFields(t *testing.T) {
	var p pointDocument
	if e := strictDocument(`{"timestamp":1,"unit":"None","value":0} `, &p); e != nil || p.Timestamp != 1 || p.Unit != "None" || p.Value != 0 {
		t.Fatal("valid stored zero document lost")
	}
	for _, raw := range []string{`{"timestamp":1,"unit":"None","value":0,"unknown":1}`, `{"timestamp":1} {"timestamp":2}`, `{"timestamp":1} trailing`, `{`} {
		if e := strictDocument(raw, &p); e == nil {
			t.Fatal("malformed/extra stored JSON admitted")
		}
	}
}
