package cloudwatchmetrics

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type alarmDimension struct{ Name, Value string }
type alarmConfig struct {
	AlarmName          string
	AlarmDescription   string
	ActionsEnabled     bool
	MetricName         string
	Namespace          string
	Statistic          string
	Dimensions         []alarmDimension `xml:"Dimensions>member"`
	Period             int
	Unit               string
	EvaluationPeriods  int
	DatapointsToAlarm  int
	Threshold          float64
	ComparisonOperator string
	TreatMissingData   string
	DimensionsJSON     string `xml:"-"`
}
type metricAlarm struct {
	alarmConfig
	AlarmArn                           string
	AlarmConfigurationUpdatedTimestamp string
	StateValue                         string
	StateReason                        string
	StateReasonData                    string
	StateUpdatedTimestamp              string
	StateTransitionedTimestamp         string
	Generation                         string `xml:"-"`
}

func alarmARN(region, name string) string {
	return "arn:aws:cloudwatch:" + region + ":000000000000:alarm:" + name
}
func (f form) alarmName(key string) (string, error) {
	value, present := f.take(key)
	if !present {
		return "", missing(key)
	}
	if !printable(value, 255) {
		return "", invalid("Use a nonempty printable alarm name of at most 255 bytes.")
	}
	return value, nil
}
func (f form) alarmConfig() (alarmConfig, error) {
	var c alarmConfig
	var err error
	c.AlarmName, err = f.alarmName("AlarmName")
	if err != nil {
		return c, err
	}
	c.AlarmDescription, _ = f.take("AlarmDescription")
	if len(c.AlarmDescription) > 1024 || !utf8.ValidString(c.AlarmDescription) || strings.ContainsFunc(c.AlarmDescription, func(r rune) bool { return r == 0xfffe || r == 0xffff || unicode.IsControl(r) && r != '\n' && r != '\t' }) {
		return c, invalid("Invalid alarm description.")
	}
	c.ActionsEnabled = true
	if value, present := f.take("ActionsEnabled"); present {
		if value != "true" && value != "false" {
			return c, invalid("ActionsEnabled must be true or false.")
		}
		c.ActionsEnabled = value == "true"
	}
	for _, key := range []string{"AlarmActions", "OKActions", "InsufficientDataActions"} {
		if value, present := f.take(key); present && value != "" {
			return c, invalid("Alarm actions are unsupported.")
		}
	}
	c.Namespace, err = f.namespace()
	if err != nil {
		return c, err
	}
	c.MetricName, _ = f.take("MetricName")
	if !metricName(c.MetricName) {
		return c, invalid("Supply a supported metric name.")
	}
	c.DimensionsJSON, err = f.dimensions("Dimensions")
	if err != nil {
		return c, err
	}
	var dims map[string]string
	if json.Unmarshal([]byte(c.DimensionsJSON), &dims) != nil {
		return c, invalid("Invalid dimensions.")
	}
	names := make([]string, 0, len(dims))
	for name := range dims {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		c.Dimensions = append(c.Dimensions, alarmDimension{name, dims[name]})
	}
	c.Unit, _ = f.take("Unit")
	if !slices.Contains(units, c.Unit) {
		return c, invalid("An explicit supported Unit is required.")
	}
	c.Statistic, _ = f.take("Statistic")
	if !slices.Contains(statistics, c.Statistic) {
		return c, invalid("Use a supported basic Statistic.")
	}
	period, _ := f.take("Period")
	evaluation, _ := f.take("EvaluationPeriods")
	points, present := f.take("DatapointsToAlarm")
	if period != "60" || evaluation != "1" || present && points != "1" {
		return c, invalid("Only Period=60 and one evaluation/datapoint are supported.")
	}
	c.Period, c.EvaluationPeriods, c.DatapointsToAlarm = 60, 1, 1
	threshold, _ := f.take("Threshold")
	c.Threshold, err = strconv.ParseFloat(threshold, 64)
	if err != nil || !decimal.MatchString(threshold) || math.IsNaN(c.Threshold) || math.IsInf(c.Threshold, 0) || math.Abs(c.Threshold) > math.Exp2(360) {
		return c, invalid("Supply a finite bounded threshold.")
	}
	c.ComparisonOperator, _ = f.take("ComparisonOperator")
	if !slices.Contains([]string{"GreaterThanThreshold", "GreaterThanOrEqualToThreshold", "LessThanThreshold", "LessThanOrEqualToThreshold"}, c.ComparisonOperator) {
		return c, invalid("Unsupported comparison operator.")
	}
	c.TreatMissingData, present = f.take("TreatMissingData")
	if !present {
		c.TreatMissingData = "missing"
	}
	if c.TreatMissingData != "missing" {
		return c, invalid("Only TreatMissingData=missing is supported.")
	}
	if len(f) != 0 {
		return c, invalid("Unknown or unsupported alarm field.")
	}
	return c, nil
}

type alarmDescribe struct {
	name, prefix, state, token string
	limit                      int
}

func (f form) alarmDescribe() (alarmDescribe, error) {
	q := alarmDescribe{limit: 100}
	var err error
	if literal, present := f.take("AlarmNames"); present {
		if literal != "" {
			return q, invalid("AlarmNames uses numbered members or an empty list.")
		}
	} else if _, present := f["AlarmNames.member.1"]; present {
		q.name, err = f.alarmName("AlarmNames.member.1")
		if err != nil {
			return q, err
		}
	}
	q.prefix, _ = f.take("AlarmNamePrefix")
	if q.prefix != "" && !printable(q.prefix, 255) {
		return q, invalid("Invalid alarm prefix.")
	}
	if q.name != "" && q.prefix != "" {
		return q, invalid("Do not combine an exact name and prefix.")
	}
	q.state, _ = f.take("StateValue")
	if q.state != "" && !slices.Contains([]string{"OK", "ALARM", "INSUFFICIENT_DATA"}, q.state) {
		return q, invalid("Invalid alarm state filter.")
	}
	if literal, present := f.take("AlarmTypes"); present {
		if literal != "" {
			return q, invalid("AlarmTypes uses numbered members or an empty list.")
		}
	} else if typ, present := f.take("AlarmTypes.member.1"); present && typ != "MetricAlarm" {
		return q, invalid("Only metric alarms are supported.")
	}
	if value, present := f.take("MaxRecords"); present {
		q.limit, err = strconv.Atoi(value)
		if err != nil || q.limit < 1 || q.limit > 100 {
			return q, invalid("MaxRecords must be 1 through 100.")
		}
	}
	q.token, _ = f.take("NextToken")
	if len(q.token) > 2048 {
		return q, invalid("Invalid alarm continuation.")
	}
	if len(f) != 0 {
		return q, invalid("Unknown or unsupported alarm query field.")
	}
	return q, nil
}
func alarmXML(action string, result any) any {
	return struct {
		XMLName  xml.Name
		Result   any `xml:",omitempty"`
		Metadata struct {
			RequestID string `xml:"RequestId"`
		} `xml:"ResponseMetadata"`
	}{XMLName: xml.Name{Space: xmlNamespace, Local: action + "Response"}, Result: result, Metadata: struct {
		RequestID string `xml:"RequestId"`
	}{"local-cloudwatch"}}
}

// Revalidate persisted configuration without repairing or adopting malformed rows.
func validSavedAlarm(a metricAlarm) bool {
	f := form{"AlarmName": {a.AlarmName}, "AlarmDescription": {a.AlarmDescription}, "ActionsEnabled": {strconv.FormatBool(a.ActionsEnabled)}, "Namespace": {a.Namespace}, "MetricName": {a.MetricName}, "Unit": {a.Unit}, "Statistic": {a.Statistic}, "Period": {strconv.Itoa(a.Period)}, "EvaluationPeriods": {strconv.Itoa(a.EvaluationPeriods)}, "DatapointsToAlarm": {strconv.Itoa(a.DatapointsToAlarm)}, "Threshold": {strconv.FormatFloat(a.Threshold, 'g', -1, 64)}, "ComparisonOperator": {a.ComparisonOperator}, "TreatMissingData": {a.TreatMissingData}}
	for i, d := range a.Dimensions {
		prefix := "Dimensions.member." + strconv.Itoa(i+1) + "."
		f[prefix+"Name"] = []string{d.Name}
		f[prefix+"Value"] = []string{d.Value}
	}
	c, err := f.alarmConfig()
	if err != nil {
		return false
	}
	old, _ := json.Marshal(a.alarmConfig)
	checked, _ := json.Marshal(c)
	if string(old) != string(checked) {
		return false
	}
	if !slices.Contains([]string{"OK", "ALARM", "INSUFFICIENT_DATA"}, a.StateValue) || a.StateReason == "" {
		return false
	}
	for _, timestamp := range []string{a.AlarmConfigurationUpdatedTimestamp, a.StateUpdatedTimestamp, a.StateTransitionedTimestamp} {
		if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
			return false
		}
	}
	return validAlarmReasonData(a)
}

// Reason data is provider-owned persisted evidence, not caller-supplied state.
func validAlarmReasonData(a metricAlarm) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(a.StateReasonData), &fields) != nil || len(fields) == 0 {
		return false
	}
	if len(fields) == 2 && string(fields["version"]) == `"1.0"` && string(fields["reason"]) == `"created"` {
		return a.StateValue == "INSUFFICIENT_DATA"
	}
	if len(fields) != 7 {
		return false
	}
	for _, key := range []string{"Version", "Start", "End", "Statistic", "SampleCount", "Value", "Threshold"} {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	var data struct {
		Version, Start, End, Statistic string
		SampleCount                    int
		Value                          *float64
		Threshold                      float64
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(a.StateReasonData)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&data) != nil || data.Version != "1.0" || !slices.Contains(statistics, data.Statistic) || data.SampleCount < 0 || data.SampleCount > maxNamespaceSamples || math.IsNaN(data.Threshold) || math.IsInf(data.Threshold, 0) || math.Abs(data.Threshold) > math.Exp2(360) {
		return false
	}
	start, err := time.Parse(time.RFC3339, data.Start)
	if err != nil || !start.Equal(start.Truncate(time.Minute)) {
		return false
	}
	end, err := time.Parse(time.RFC3339, data.End)
	if err != nil || end.Sub(start) != time.Minute {
		return false
	}
	if data.SampleCount == 0 {
		return data.Value == nil && a.StateValue == "INSUFFICIENT_DATA"
	}
	return data.Value != nil && !math.IsInf(*data.Value, 0) && !math.IsNaN(*data.Value) && a.StateValue != "INSUFFICIENT_DATA"
}
