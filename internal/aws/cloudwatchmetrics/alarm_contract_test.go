package cloudwatchmetrics

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

func TestAlarmStrictNativeFieldShapesAndUntouchedFrame(t *testing.T) {
	s, _ := testStore(t, "")
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	allow := func(string, string) bool { return true }
	alarmServe(t, s, "us-east-1", "PutMetricAlarm", alarmWire("saved"), allow, 200)
	before := alarmFrame(t, s.db)
	for key, value := range map[string]string{"AlarmName": "\x01", "MetricName": "", "Unit": "Unknown", "ComparisonOperator": "EqualToThreshold", "DatapointsToAlarm": "2", "ActionsEnabled": "False", "AlarmActions": "outside", "OKActions": "outside", "InsufficientDataActions": "outside", "Dimensions.member.1.Name": ""} {
		f := alarmWire("candidate")
		f.Set(key, value)
		alarmServe(t, s, "us-east-1", "PutMetricAlarm", f, allow, 400)
	}
	f := alarmWire("candidate")
	f.Del("AlarmName")
	alarmServe(t, s, "us-east-1", "PutMetricAlarm", f, allow, 400)
	f = alarmWire("candidate")
	f.Del("Namespace")
	alarmServe(t, s, "us-east-1", "PutMetricAlarm", f, allow, 400)
	for _, f := range []url.Values{{"AlarmNamePrefix": {"\x01"}}, {"AlarmNames.member.1": {"saved"}, "AlarmNamePrefix": {"s"}}, {"StateValue": {"UNKNOWN"}}, {"AlarmTypes.member.1": {"CompositeAlarm"}}, {"MaxRecords": {"0"}}, {"MaxRecords": {"101"}}, {"MaxRecords": {"NaN"}}, {"NextToken": {strings.Repeat("x", 2049)}}, {"AlarmNames": {"not-a-list"}}, {"AlarmTypes": {"not-a-list"}}, {"AlarmTypes": {""}, "AlarmTypes.member.1": {"MetricAlarm"}}, {"Unreviewed": {"value"}}} {
		alarmServe(t, s, "us-east-1", "DescribeAlarms", f, allow, 400)
	}
	alarmServe(t, s, "us-east-1", "DeleteAlarms", url.Values{"AlarmNames.member.1": {"saved"}, "AlarmNames.member.2": {"other"}}, allow, 400)
	alarmServe(t, s, "us-east-1", "DeleteAlarms", url.Values{"AlarmNames.member.1": {"absent"}}, allow, 404)
	if !reflect.DeepEqual(before, alarmFrame(t, s.db)) {
		t.Fatal("native field refusal changed persisted prior state")
	}
	f = alarmWire("no-actions")
	f.Set("ActionsEnabled", "false")
	f["AlarmActions"] = []string{""}
	f["OKActions"] = []string{""}
	f["InsufficientDataActions"] = []string{""}
	f["Dimensions"] = []string{""}
	alarmServe(t, s, "us-east-1", "PutMetricAlarm", f, allow, 200)
}

func TestAlarmSavedConfigurationAndCursorAuthorityFailClosed(t *testing.T) {
	for _, damage := range []string{"invalid-json", "unknown-field", "unit", "canonical-dimensions", "state", "reason", "generation", "generation-nonhex", "timestamp", "empty-reason-object", "cursor-secret"} {
		t.Run(damage, func(t *testing.T) {
			s, _ := testStore(t, "")
			now := time.Now().UTC()
			s.now = func() time.Time { return now }
			allow := func(string, string) bool { return true }
			alarmServe(t, s, "us-east-1", "PutMetricAlarm", alarmWire("saved"), allow, 200)
			alarmState(t, s, "saved", "INSUFFICIENT_DATA")
			var document string
			if err := s.db.QueryRow("SELECT document FROM aws_cloudwatch_alarms").Scan(&document); err != nil {
				t.Fatal(err)
			}
			damaged := document
			var data map[string]any
			if err := json.Unmarshal([]byte(document), &data); err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "invalid-json":
				damaged = "{"
			case "unknown-field":
				data["Unsupported"] = true
			case "unit":
				data["Unit"] = "Unknown"
			case "canonical-dimensions":
				data["DimensionsJSON"] = `{"invented":"dimension"}`
			case "state":
				data["StateValue"] = "fictional"
			case "reason":
				data["StateReasonData"] = "{"
			case "generation":
				data["Generation"] = "another"
			case "generation-nonhex":
				data["Generation"] = strings.Repeat("z", 32)
			case "timestamp":
				data["AlarmConfigurationUpdatedTimestamp"] = "not-a-time"
			case "empty-reason-object":
				data["StateReasonData"] = "null"
			case "cursor-secret":
			}
			if damage != "invalid-json" {
				raw, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				damaged = string(raw)
			}
			if damage == "generation-nonhex" {
				if _, err := s.db.Exec("UPDATE aws_cloudwatch_alarms SET generation=?", strings.Repeat("z", 32)); err != nil {
					t.Fatal(err)
				}
			}
			if damage == "cursor-secret" {
				if _, err := s.db.Exec("UPDATE aws_cloudwatch_alarm_settings SET secret='invalid'"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := s.db.Exec("UPDATE aws_cloudwatch_alarms SET document=?", damaged); err != nil {
				t.Fatal(err)
			}
			before := alarmFrame(t, s.db)
			if value, err := NewStore(s.db); err == nil || value != nil {
				t.Fatal("malformed saved state opened successfully")
			}
			if !reflect.DeepEqual(before, alarmFrame(t, s.db)) {
				t.Fatal("constructor silently repaired or changed malformed rows")
			}
		})
	}
}

func TestAlarmNativeStorageRefusalAtomicFrameAndRetry(t *testing.T) {
	type fault struct {
		op                   string
		action               sqlite3.AuthorizerActionCode
		table, column, after string
	}
	for _, f := range []fault{{"create", sqlite3.AUTH_FUNCTION, "", "count", ""}, {"create", sqlite3.AUTH_INSERT, "aws_cloudwatch_alarms", "", ""}, {"create", sqlite3.AUTH_READ, "aws_cloudwatch_alarms", "document", "aws_cloudwatch_alarms"}, {"update", sqlite3.AUTH_READ, "aws_cloudwatch_alarms", "document", ""}, {"update", sqlite3.AUTH_UPDATE, "aws_cloudwatch_alarms", "document", ""}, {"delete", sqlite3.AUTH_DELETE, "aws_cloudwatch_alarms", "", ""}, {"delete", sqlite3.AUTH_FUNCTION, "", "count", "aws_cloudwatch_alarms"}, {"describe", sqlite3.AUTH_READ, "aws_cloudwatch_alarms", "name", ""}, {"describe", sqlite3.AUTH_READ, "aws_cloudwatch_samples", "value", ""}, {"metric", sqlite3.AUTH_UPDATE, "aws_cloudwatch_alarms", "document", ""}, {"constructor", sqlite3.AUTH_READ, "aws_cloudwatch_alarm_settings", "secret", ""}} {
		t.Run(f.op+"/"+f.table+"/"+f.column+"/"+f.after, func(t *testing.T) {
			s, _ := testStore(t, "")
			now := time.Now().UTC()
			s.now = func() time.Time { return now }
			allow := func(string, string) bool { return true }
			alarmServe(t, s, "us-east-1", "PutMetricAlarm", alarmWire("sibling"), allow, 200)
			alarmState(t, s, "sibling", "INSUFFICIENT_DATA")
			if f.op != "create" {
				alarmServe(t, s, "us-east-1", "PutMetricAlarm", alarmWire("target"), allow, 200)
				alarmState(t, s, "target", "INSUFFICIENT_DATA")
			}
			before := alarmFrame(t, s.db)
			fired, armed := false, f.after == ""
			configure := func(clear bool) {
				t.Helper()
				conn, err := s.db.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				err = conn.Raw(func(raw any) error {
					db := raw.(driver.Conn).Raw()
					if clear {
						db.UpdateHook(nil)
						return db.SetAuthorizer(nil)
					}
					db.UpdateHook(func(_ sqlite3.AuthorizerActionCode, _, table string, _ int64) {
						if table == f.after {
							armed = true
						}
					})
					return db.SetAuthorizer(func(action sqlite3.AuthorizerActionCode, table, column, _, _ string) sqlite3.AuthorizerReturnCode {
						if armed && action == f.action && table == f.table && column == f.column {
							fired = true
							return sqlite3.AUTH_DENY
						}
						return sqlite3.AUTH_OK
					})
				})
				conn.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			run := func(status int) {
				t.Helper()
				switch f.op {
				case "create", "update":
					alarmServe(t, s, "us-east-1", "PutMetricAlarm", alarmWire("target"), allow, status)
				case "delete":
					alarmServe(t, s, "us-east-1", "DeleteAlarms", url.Values{"AlarmNames.member.1": {"target"}}, allow, status)
				case "describe":
					alarmServe(t, s, "us-east-1", "DescribeAlarms", url.Values{}, allow, status)
				case "metric":
					err := s.put(t.Context(), "us-east-1", "Training", []sample{observation(now.Truncate(time.Minute).Add(-time.Second), 9)}, now)
					if status == 500 && err == nil || status == 200 && err != nil {
						t.Fatal("wrong sample transaction result", err)
					}
				case "constructor":
					value, err := NewStore(s.db)
					if status == 500 && (err == nil || value != nil) || status == 200 && (err != nil || value == nil) {
						t.Fatal("wrong constructor result", err)
					}
				}
			}
			configure(false)
			run(500)
			configure(true)
			if !fired {
				t.Fatal("intended native storage read/write fault did not fire")
			}
			if !reflect.DeepEqual(before, alarmFrame(t, s.db)) {
				t.Fatal("storage refusal changed complete previous alarm/sample state")
			}
			run(200)
		})
	}
}
