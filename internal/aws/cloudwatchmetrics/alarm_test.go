package cloudwatchmetrics

import (
	"encoding/xml"
	"fmt"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func alarmWire(name string) url.Values {
	return url.Values{"AlarmName": {name}, "Namespace": {"Training"}, "MetricName": {"Latency"}, "Unit": {"Count"},
		"Statistic": {"Average"}, "Period": {"60"}, "EvaluationPeriods": {"1"}, "Threshold": {"4"}, "ComparisonOperator": {"GreaterThanThreshold"}}
}

func alarmServe(t *testing.T, s *Store, region, action string, f url.Values, allow func(string, string) bool, status int) string {
	t.Helper()
	f.Set("Action", action)
	f.Set("Version", "2010-08-01")
	w := httptest.NewRecorder()
	s.ServeQuery(w, request([]byte(f.Encode()), ""), region, func(action, resource string) (bool, error) { return allow(action, resource), nil })
	if w.Code != status {
		t.Fatalf("%s: status %d want %d: %s", action, w.Code, status, w.Body.String())
	}
	var root struct{ XMLName xml.Name }
	if xml.Unmarshal(w.Body.Bytes(), &root) != nil || root.XMLName.Space != xmlNamespace {
		t.Fatal("not native CloudWatch XML")
	}
	return w.Body.String()
}

func alarmState(t *testing.T, s *Store, name, want string) {
	t.Helper()
	body := alarmServe(t, s, "us-east-1", "DescribeAlarms", url.Values{"AlarmNames.member.1": {name}}, func(string, string) bool { return true }, 200)
	var result struct {
		Alarms []struct{ AlarmName, AlarmArn, StateValue, StateReason, StateReasonData, StateUpdatedTimestamp string } `xml:"DescribeAlarmsResult>MetricAlarms>member"`
	}
	if err := xml.Unmarshal([]byte(body), &result); err != nil || len(result.Alarms) != 1 {
		t.Fatal(body, err)
	}
	a := result.Alarms[0]
	if a.AlarmName != name || a.AlarmArn != "arn:aws:cloudwatch:us-east-1:000000000000:alarm:"+name || a.StateValue != want || a.StateReason == "" || a.StateReasonData == "" || a.StateUpdatedTimestamp == "" {
		t.Fatalf("wrong actual alarm: %+v", a)
	}
}

func TestAlarmActualSampleLifecycleAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarms.db")
	s, owner := testStore(t, path)
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	allow := func(string, string) bool { return true }
	alarmServe(t, s, "us-east-1", "PutMetricAlarm", alarmWire("latency"), allow, 200)
	alarmState(t, s, "latency", "INSUFFICIENT_DATA")
	point := observation(now.Add(-30*time.Second), 1)
	if err := s.put(t.Context(), "us-east-1", "Training", []sample{point}, now); err != nil {
		t.Fatal(err)
	}
	alarmState(t, s, "latency", "OK")
	point.value = 9
	if err := s.put(t.Context(), "us-east-1", "Training", []sample{point}, now); err != nil {
		t.Fatal(err)
	}
	alarmState(t, s, "latency", "ALARM") // Actual mean of 1 and 9 is five.
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	s, _ = testStore(t, path)
	s.now = func() time.Time { return now }
	alarmState(t, s, "latency", "ALARM")
	updated := alarmWire("latency")
	updated.Set("Threshold", "100")
	alarmServe(t, s, "us-east-1", "PutMetricAlarm", updated, allow, 200)
	alarmState(t, s, "latency", "OK")
	now = now.Add(time.Minute)
	alarmState(t, s, "latency", "INSUFFICIENT_DATA") // Older breaching data is outside the explicit local window.
	alarmServe(t, s, "us-east-1", "DeleteAlarms", url.Values{"AlarmNames.member.1": {"latency"}}, allow, 200)
	body := alarmServe(t, s, "us-east-1", "DescribeAlarms", url.Values{}, allow, 200)
	if strings.Contains(body, "<member>") {
		t.Fatal("deleted alarm still returned")
	}
}

func TestAlarmExactAuthorizationAndRefusal(t *testing.T) {
	s, _ := testStore(t, "")
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	arn := "arn:aws:cloudwatch:us-east-1:000000000000:alarm:owned"
	policy, err := core.ParsePolicy([]byte(fmt.Sprintf(`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":["cloudwatch:PutMetricAlarm","cloudwatch:DescribeAlarms","cloudwatch:DeleteAlarms"],"Resource":%q}}`, arn)))
	if err != nil {
		t.Fatal(err)
	}
	allow := func(action, resource string) bool { return core.Evaluate(policy, action, resource) == "allowed" }
	alarmServe(t, s, "us-east-1", "PutMetricAlarm", alarmWire("owned"), allow, 200)
	alarmServe(t, s, "us-east-1", "DescribeAlarms", url.Values{"AlarmNames.member.1": {"owned"}}, allow, 200)
	alarmServe(t, s, "us-east-1", "DescribeAlarms", url.Values{}, allow, 403)
	alarmServe(t, s, "us-west-2", "DescribeAlarms", url.Values{"AlarmNames.member.1": {"owned"}}, allow, 403)
	alarmServe(t, s, "us-east-1", "DeleteAlarms", url.Values{"AlarmNames.member.1": {"absent"}}, allow, 403)
	for key, value := range map[string]string{"AlarmActions.member.1": "arn:aws:sns:us-east-1:000000000000:topic", "Period": "10", "EvaluationPeriods": "2", "TreatMissingData": "breaching", "Statistic": "p99", "Threshold": "NaN", "Unknown": "value"} {
		f := alarmWire("owned")
		f.Set(key, value)
		alarmServe(t, s, "us-east-1", "PutMetricAlarm", f, allow, 400)
	}
	alarmState(t, s, "owned", "INSUFFICIENT_DATA")
}

func TestAlarmStatisticsThresholdEqualityAndWindow(t *testing.T) {
	s, _ := testStore(t, "")
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	start := now.Add(-time.Minute)
	batch := []sample{observation(start, 2), observation(now.Add(-time.Nanosecond), 4), observation(start.Add(-time.Nanosecond), 99), observation(now, 99)}
	foreign := observation(start, 100)
	foreign.unit = "Bytes"
	batch = append(batch, foreign)
	if err := s.put(t.Context(), "us-east-1", "Training", batch, now); err != nil {
		t.Fatal(err)
	}
	for statistic, value := range map[string]string{"Average": "3", "Sum": "6", "SampleCount": "2", "Minimum": "2", "Maximum": "4"} {
		for comparison, state := range map[string]string{"GreaterThanThreshold": "OK", "GreaterThanOrEqualToThreshold": "ALARM", "LessThanThreshold": "OK", "LessThanOrEqualToThreshold": "ALARM"} {
			name := statistic + comparison
			f := alarmWire(name)
			f.Set("Statistic", statistic)
			f.Set("Threshold", value)
			f.Set("ComparisonOperator", comparison)
			alarmServe(t, s, "us-east-1", "PutMetricAlarm", f, func(string, string) bool { return true }, 200)
			alarmState(t, s, name, state)
		}
	}
}

func TestAlarmUnmeasuredValueAndDescriptionRoundtrip(t *testing.T) {
	s, _ := testStore(t, "")
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	allow := func(string, string) bool { return true }
	f := alarmWire("empty")
	f.Set("AlarmDescription", "説明\nsecond\tline")
	alarmServe(t, s, "us-east-1", "PutMetricAlarm", f, allow, 200)
	read := func() string {
		return alarmServe(t, s, "us-east-1", "DescribeAlarms", url.Values{"AlarmNames.member.1": {"empty"}}, allow, 200)
	}
	var response struct {
		Alarms []struct{ StateReason, StateReasonData, AlarmDescription string } `xml:"DescribeAlarmsResult>MetricAlarms>member"`
	}
	if err := xml.Unmarshal([]byte(read()), &response); err != nil || len(response.Alarms) != 1 {
		t.Fatal(err)
	}
	if !strings.Contains(response.Alarms[0].StateReasonData, `"Value":null`) || strings.Contains(response.Alarms[0].StateReason, "Average=0") || response.Alarms[0].AlarmDescription != f.Get("AlarmDescription") {
		t.Fatal("missing observation fabricated zero or changed valid XML text", response)
	}
	point := observation(now.Truncate(time.Minute).Add(-time.Second), 0)
	if err := s.put(t.Context(), "us-east-1", "Training", []sample{point}, now); err != nil {
		t.Fatal(err)
	}
	response.Alarms = nil
	if err := xml.Unmarshal([]byte(read()), &response); err != nil || len(response.Alarms) != 1 || !strings.Contains(response.Alarms[0].StateReasonData, `"Value":0`) {
		t.Fatal("actual zero not distinguished", err, response)
	}
	for _, description := range []string{"bad\ufffe", "bad\uffff", "bad\x01", string([]byte{0xff})} {
		f.Set("AlarmDescription", description)
		alarmServe(t, s, "us-east-1", "PutMetricAlarm", f, allow, 400)
	}
}

func TestAlarmPaginationScopesAndDeniedReadFrame(t *testing.T) {
	s, _ := testStore(t, "")
	peer, _ := testStore(t, "")
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	peer.now = s.now
	allow := func(string, string) bool { return true }
	for _, store := range []*Store{s, peer} {
		for _, name := range []string{"a", "b", "c"} {
			alarmServe(t, store, "us-east-1", "PutMetricAlarm", alarmWire(name), allow, 200)
		}
	}
	parse := func(body string) ([]string, string) {
		var r struct {
			Names []string `xml:"DescribeAlarmsResult>MetricAlarms>member>AlarmName"`
			Token string   `xml:"DescribeAlarmsResult>NextToken"`
		}
		if err := xml.Unmarshal([]byte(body), &r); err != nil {
			t.Fatal(err)
		}
		return r.Names, r.Token
	}
	names, token := parse(alarmServe(t, s, "us-east-1", "DescribeAlarms", url.Values{"MaxRecords": {"1"}}, allow, 200))
	if len(names) != 1 || names[0] != "a" || token == "" {
		t.Fatal(names, token)
	}
	before, peerBefore := alarmFrame(t, s.db), alarmFrame(t, peer.db)
	alarmServe(t, s, "us-east-1", "DescribeAlarms", url.Values{"NextToken": {token}, "MaxRecords": {"1"}}, func(string, string) bool { return false }, 403)
	if !reflect.DeepEqual(before, alarmFrame(t, s.db)) {
		t.Fatal("denied read evaluated or changed saved state")
	}
	for _, query := range []url.Values{{"NextToken": {token}, "MaxRecords": {"2"}}, {"NextToken": {token}, "MaxRecords": {"1"}, "StateValue": {"ALARM"}}, {"NextToken": {token + "x"}, "MaxRecords": {"1"}}} {
		alarmServe(t, s, "us-east-1", "DescribeAlarms", query, allow, 400)
	}
	alarmServe(t, peer, "us-east-1", "DescribeAlarms", url.Values{"NextToken": {token}, "MaxRecords": {"1"}}, allow, 400)
	alarmServe(t, s, "us-west-2", "DescribeAlarms", url.Values{"NextToken": {token}, "MaxRecords": {"1"}}, allow, 400)
	names, token = parse(alarmServe(t, s, "us-east-1", "DescribeAlarms", url.Values{"NextToken": {token}, "MaxRecords": {"1"}}, allow, 200))
	if len(names) != 1 || names[0] != "b" || token == "" {
		t.Fatal(names, token)
	}
	names, token = parse(alarmServe(t, s, "us-east-1", "DescribeAlarms", url.Values{"NextToken": {token}, "MaxRecords": {"1"}}, allow, 200))
	if len(names) != 1 || names[0] != "c" || token != "" {
		t.Fatal(names, token)
	}
	alarmServe(t, s, "us-east-1", "DescribeAlarms", url.Values{"AlarmNames": {""}, "AlarmTypes": {""}}, allow, 200)
	alarmServe(t, s, "us-east-1", "DescribeAlarms", url.Values{"AlarmNames": {""}, "AlarmNames.member.1": {"a"}}, allow, 400)
	if !reflect.DeepEqual(peerBefore, alarmFrame(t, peer.db)) {
		t.Fatal("host queries changed populated peer")
	}
}
