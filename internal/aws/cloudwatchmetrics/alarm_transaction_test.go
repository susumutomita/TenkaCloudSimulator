package cloudwatchmetrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func alarmFrame(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var out []string
	for _, q := range []string{"SELECT json_array(id,region,namespace,metric,dimensions,unit,at_ns,value) FROM aws_cloudwatch_samples ORDER BY id", "SELECT json_array(region,name,generation,document) FROM aws_cloudwatch_alarms ORDER BY region,name", "SELECT json_array(id,secret) FROM aws_cloudwatch_alarm_settings ORDER BY id"} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var value string
			if err = rows.Scan(&value); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out = append(out, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}
func TestAlarmStagedCancellationDeadlineAndRetry(t *testing.T) {
	for _, operation := range []string{"create", "update", "delete", "metric"} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deadline-%t", operation, deadline), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "staged.db")
				s, owner := testStore(t, path)
				now := time.Now().UTC()
				s.now = func() time.Time { return now }
				allow := func(string, string) bool { return true }
				alarmServe(t, s, "us-east-1", "PutMetricAlarm", alarmWire("preserved"), allow, 200)
				if operation != "create" {
					alarmServe(t, s, "us-east-1", "PutMetricAlarm", alarmWire("target"), allow, 200)
				}
				if operation == "metric" {
					alarmState(t, s, "target", "INSUFFICIENT_DATA")
				}
				before := alarmFrame(t, s.db)
				ctx, cancel := context.WithCancel(t.Context())
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(t.Context(), time.Second)
				}
				defer cancel()
				conn, err := s.db.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				fired := false
				event := sqlite3.AUTH_INSERT
				if operation == "update" || operation == "metric" {
					event = sqlite3.AUTH_UPDATE
				}
				if operation == "delete" {
					event = sqlite3.AUTH_DELETE
				}
				if err = conn.Raw(func(raw any) error {
					raw.(driver.Conn).Raw().UpdateHook(func(code sqlite3.AuthorizerActionCode, _, table string, _ int64) {
						if !fired && table == "aws_cloudwatch_alarms" && code == event {
							fired = true
							if deadline {
								<-ctx.Done()
							} else {
								cancel()
							}
						}
					})
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				conn.Close()
				config, err := form(alarmWire("target")).alarmConfig()
				if err != nil {
					t.Fatal(err)
				}
				config.Threshold = 100
				run := func(ctx context.Context) error {
					switch operation {
					case "create", "update":
						return s.saveAlarm(ctx, "us-east-1", config, now)
					case "delete":
						return s.deleteAlarm(ctx, "us-east-1", "target")
					default:
						return s.put(ctx, "us-east-1", "Training", []sample{observation(now.Truncate(time.Minute).Add(-30*time.Second), 9)}, now)
					}
				}
				err = run(ctx)
				cause := context.Canceled
				if deadline {
					cause = context.DeadlineExceeded
				}
				if !fired || err == nil || !errors.Is(ctx.Err(), cause) {
					t.Fatal("physical stage/cause not reached", fired, err, ctx.Err())
				}
				conn, clearErr := s.db.Conn(t.Context())
				if clearErr != nil {
					t.Fatal(clearErr)
				}
				clearErr = conn.Raw(func(raw any) error { raw.(driver.Conn).Raw().UpdateHook(nil); return nil })
				conn.Close()
				if clearErr != nil {
					t.Fatal(clearErr)
				}
				if !reflect.DeepEqual(before, alarmFrame(t, s.db)) {
					t.Fatal("canceled mutation changed complete prior sample/alarm state")
				}
				if err = owner.Close(); err != nil {
					t.Fatal(err)
				}
				disk, openErr := core.New(path)
				if openErr != nil {
					t.Fatal(openErr)
				}
				if !reflect.DeepEqual(before, alarmFrame(t, disk.Database())) {
					disk.Close()
					t.Fatal("canceled rows survived a clean durable reopen")
				}
				if openErr = disk.Close(); openErr != nil {
					t.Fatal(openErr)
				}
				s, _ = testStore(t, path)
				s.now = func() time.Time { return now }
				// Startup performs real evaluation, so compare semantic untouched rows before
				// replay using a current admitted read rather than demanding unchanged reasons.
				if operation == "metric" {
					var count int
					if err = s.db.QueryRow("SELECT count(*) FROM aws_cloudwatch_samples").Scan(&count); err != nil || count != 0 {
						t.Fatal("canceled sample survived reopen", count, err)
					}
				}
				if err = run(t.Context()); err != nil {
					t.Fatal("healthy retry failed", err)
				}
				if operation == "delete" {
					body := alarmServe(t, s, "us-east-1", "DescribeAlarms", url.Values{"AlarmNames.member.1": {"target"}}, allow, 200)
					if len(body) == 0 {
						t.Fatal("empty native retry")
					}
				} else if operation == "metric" {
					alarmState(t, s, "target", "ALARM")
				} else {
					alarmState(t, s, "target", "INSUFFICIENT_DATA")
				}
			})
		}
	}
}
func TestAlarmFailedEvaluationRollsBackActualSamples(t *testing.T) {
	s, _ := testStore(t, "")
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	allow := func(string, string) bool { return true }
	alarmServe(t, s, "us-east-1", "PutMetricAlarm", alarmWire("target"), allow, 200)
	// These rows were valid at their original write time. A later failed
	// derived alarm update must also restore the physically pruned old row.
	originalTime := now.Add(-retention - time.Second)
	if err := s.put(t.Context(), "us-east-1", "Training", []sample{observation(originalTime, 23)}, originalTime); err != nil {
		t.Fatal(err)
	}
	if err := s.put(t.Context(), "us-east-1", "OtherNamespace", []sample{observation(originalTime, 71)}, originalTime); err != nil {
		t.Fatal(err)
	}
	if err := s.put(t.Context(), "us-west-2", "Training", []sample{observation(originalTime, 17)}, originalTime); err != nil {
		t.Fatal(err)
	}
	alarmState(t, s, "target", "INSUFFICIENT_DATA")
	before := alarmFrame(t, s.db)
	if _, err := s.db.Exec("CREATE TRIGGER ignored_alarm_update BEFORE UPDATE ON aws_cloudwatch_alarms BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	point := observation(now.Truncate(time.Minute).Add(-30*time.Second), 9)
	if err := s.put(t.Context(), "us-east-1", "Training", []sample{point}, now); err == nil {
		t.Fatal("unacknowledged derived state returned success")
	}
	if !reflect.DeepEqual(before, alarmFrame(t, s.db)) {
		t.Fatal("failed derived alarm update retained samples")
	}
	if _, err := s.db.Exec("DROP TRIGGER ignored_alarm_update"); err != nil {
		t.Fatal(err)
	}
	if err := s.put(t.Context(), "us-east-1", "Training", []sample{point}, now); err != nil {
		t.Fatal(err)
	}
	alarmState(t, s, "target", "ALARM")
	for _, scope := range []struct {
		region, namespace string
		value             float64
	}{{"us-east-1", "Training", 9}, {"us-east-1", "OtherNamespace", 71}, {"us-west-2", "Training", 17}} {
		var count int
		var value float64
		if err := s.db.QueryRow("SELECT count(*),min(value) FROM aws_cloudwatch_samples WHERE region=? AND namespace=?", scope.region, scope.namespace).Scan(&count, &value); err != nil || count != 1 || value != scope.value {
			t.Fatal("successful retry pruned the wrong scope", scope, count, value, err)
		}
	}
}
