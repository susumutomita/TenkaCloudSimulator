package metrics

import (
	"net/url"
	"reflect"
	"testing"
	"time"
)

func metricsRequestContractCopy(v url.Values) url.Values {
	out := url.Values{}
	for k, a := range v {
		out[k] = append([]string{}, a...)
	}
	return out
}
func metricsRequestContractStats() url.Values {
	return url.Values{"Action": {"GetMetricStatistics"}, "Version": {"2010-08-01"}, "Namespace": {"Practice/App"}, "MetricName": {"Metric"}, "StartTime": {"2026-01-02T11:59:00Z"}, "EndTime": {"2026-01-02T12:01:00Z"}, "Period": {"60"}, "Statistics.member.1": {"Sum"}}
}
func TestMetricsRequestContractPutPresenceMultiplicityAndTypes(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 30, 0, time.UTC)
	valid := rawPut(now)
	cases := []struct {
		name, key string
		values    []string
		want      string
	}{
		{"namespace_missing", "Namespace", nil, "MissingParameter"}, {"namespace_repeated", "Namespace", []string{"A", "B"}, "InvalidParameterValue"},
		{"metric_missing", "MetricData.member.1.MetricName", nil, "MissingParameter"}, {"value_missing", "MetricData.member.1.Value", nil, "MissingParameter"}, {"value_nonnumeric", "MetricData.member.1.Value", []string{"one"}, "InvalidParameterValue"},
		{"unit_repeated", "MetricData.member.1.Unit", []string{"Count", "None"}, "InvalidParameterValue"}, {"timestamp_repeated", "MetricData.member.1.Timestamp", []string{"a", "b"}, "InvalidParameterValue"}, {"timestamp_invalid", "MetricData.member.1.Timestamp", []string{"yesterday"}, "InvalidParameterValue"},
		{"resolution_repeated", "MetricData.member.1.StorageResolution", []string{"60", "60"}, "InvalidParameterValue"}, {"resolution_nonnumeric", "MetricData.member.1.StorageResolution", []string{"standard"}, "InvalidParameterValue"},
		{"dimension_name_missing", "MetricData.member.1.Dimensions.member.1.Value", []string{"blue"}, "MissingParameter"}, {"dimension_value_missing", "MetricData.member.1.Dimensions.member.1.Name", []string{"color"}, "MissingParameter"},
		{"dimension_gap", "MetricData.member.1.Dimensions.member.2.Name", []string{"color"}, "InvalidParameterValue"}, {"unknown", "Unrecognized", []string{"true"}, "InvalidParameterCombination"}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := metricsRequestContractCopy(valid)
			if c.values == nil {
				v.Del(c.key)
			} else {
				v[c.key] = c.values
			}
			_, e := decodePut(v)
			code(t, e, c.want)
		})
	}
	for _, index := range []string{"0", "01", "-1", "1001", "x"} {
		t.Run("index_"+index, func(t *testing.T) {
			v := metricsRequestContractCopy(valid)
			v.Set("MetricData.member."+index+".MetricName", "Other")
			_, e := decodePut(v)
			code(t, e, "InvalidParameterValue")
		})
	}
	empty := base("PutMetricData")
	empty.Set("Namespace", "Practice/App")
	_, e := decodePut(empty)
	code(t, e, "MissingParameter")
	valid.Set("MetricData.member.1.Unit", "Count")
	valid.Set("MetricData.member.1.StorageResolution", "60")
	valid.Set("MetricData.member.1.Value", "-2.5")
	valid.Set("MetricData.member.1.Dimensions.member.1.Name", "color")
	valid.Set("MetricData.member.1.Dimensions.member.1.Value", "blue")
	out, e := decodePut(valid)
	if e != nil || out.Namespace != "Practice/App" || len(out.Data) != 1 {
		t.Fatal("valid typed put failed")
	}
	d := out.Data[0]
	if d.MetricName != "Metric" || d.Value != -2.5 || d.Unit == nil || *d.Unit != "Count" || d.StorageResolution == nil || *d.StorageResolution != 60 || d.Timestamp == nil || !d.Timestamp.Equal(now) || !reflect.DeepEqual(d.Dimensions, []Dimension{{"color", "blue"}}) {
		t.Fatal("typed put lost literal values")
	}
}
func TestMetricsRequestContractListOptionalFilterSemantics(t *testing.T) {
	cases := []struct {
		name, key string
		values    []string
		want      string
	}{
		{"namespace_repeat", "Namespace", []string{"A", "B"}, "InvalidParameterValue"}, {"name_repeat", "MetricName", []string{"A", "B"}, "InvalidParameterValue"}, {"token_repeat", "NextToken", []string{"A", "B"}, "InvalidParameterValue"},
		{"gap", "Dimensions.member.2.Name", []string{"color"}, "InvalidParameterValue"}, {"name_missing", "Dimensions.member.1.Value", []string{"blue"}, "MissingParameter"}, {"value_repeat", "Dimensions.member.1.Value", []string{"blue", "red"}, "InvalidParameterValue"}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := base("ListMetrics")
			if c.name == "value_repeat" {
				v.Set("Dimensions.member.1.Name", "color")
			}
			v[c.key] = c.values
			_, e := decodeList(v)
			code(t, e, c.want)
		})
	}
	v := base("ListMetrics")
	v.Set("Namespace", "Practice/App")
	v.Set("MetricName", "Metric")
	v.Set("NextToken", "opaque-token")
	v.Set("Dimensions.member.1.Name", "color")
	v.Set("Dimensions.member.2.Name", "shape")
	v.Set("Dimensions.member.2.Value", "round")
	out, e := decodeList(v)
	if e != nil || out.Namespace == nil || *out.Namespace != "Practice/App" || out.MetricName == nil || *out.MetricName != "Metric" || out.NextToken != "opaque-token" || len(out.Dimensions) != 2 || out.Dimensions[0].Name != "color" || out.Dimensions[0].Value != nil || out.Dimensions[1].Value == nil || *out.Dimensions[1].Value != "round" {
		t.Fatal("list lost absent-versus-value filters")
	}
	v.Set("MaxRecords", "1")
	_, e = decodeList(v)
	code(t, e, "InvalidParameterCombination")
}
func TestMetricsRequestContractStatisticsRequiredTimesAndLists(t *testing.T) {
	valid := metricsRequestContractStats()
	for _, k := range []string{"Namespace", "MetricName", "StartTime", "EndTime", "Period"} {
		t.Run("missing_"+k, func(t *testing.T) {
			v := metricsRequestContractCopy(valid)
			v.Del(k)
			_, e := decodeStatistics(v)
			code(t, e, "MissingParameter")
		})
	}
	cases := []struct {
		name, key string
		values    []string
		want      string
	}{
		{"bad_start", "StartTime", []string{"bad"}, "InvalidParameterValue"}, {"bad_end", "EndTime", []string{"bad"}, "InvalidParameterValue"}, {"repeat_time", "StartTime", []string{"a", "b"}, "InvalidParameterValue"}, {"period_decimal", "Period", []string{"60.0"}, "InvalidParameterValue"},
		{"repeat_stat", "Statistics.member.1", []string{"Sum", "Average"}, "InvalidParameterValue"}, {"stat_gap", "Statistics.member.3", []string{"Average"}, "InvalidParameterValue"}, {"repeat_unit", "Unit", []string{"Count", "None"}, "InvalidParameterValue"}, {"dimension_missing_value", "Dimensions.member.1.Name", []string{"color"}, "MissingParameter"}, {"unknown", "ExtendedStatistics.member.1", []string{"p99"}, "InvalidParameterCombination"}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := metricsRequestContractCopy(valid)
			v[c.key] = c.values
			_, e := decodeStatistics(v)
			code(t, e, c.want)
		})
	}
	valid.Set("Statistics.member.2", "Average")
	valid.Set("Unit", "None")
	valid.Set("Dimensions.member.1.Name", "color")
	valid.Set("Dimensions.member.1.Value", "blue")
	out, e := decodeStatistics(valid)
	if e != nil || out.Namespace != "Practice/App" || out.MetricName != "Metric" || out.Start.Format(time.RFC3339) != "2026-01-02T11:59:00Z" || out.End.Format(time.RFC3339) != "2026-01-02T12:01:00Z" || out.Period != 60 || !reflect.DeepEqual(out.Statistics, []string{"Sum", "Average"}) || out.Unit == nil || *out.Unit != "None" || !reflect.DeepEqual(out.Dimensions, []Dimension{{"color", "blue"}}) {
		t.Fatal("statistics parser lost literal query")
	}
}
