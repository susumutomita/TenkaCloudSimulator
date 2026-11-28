package cloudwatchmetrics

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const alarmSchema = `CREATE TABLE IF NOT EXISTS aws_cloudwatch_alarms(region TEXT NOT NULL,name TEXT NOT NULL,generation TEXT NOT NULL,document TEXT NOT NULL,PRIMARY KEY(region,name));
CREATE TABLE IF NOT EXISTS aws_cloudwatch_alarm_settings(id INTEGER PRIMARY KEY CHECK(id=1),secret TEXT NOT NULL);`

func initializeAlarms(tx *sql.Tx) ([]byte, error) {
	if _, err := tx.Exec(alarmSchema); err != nil {
		return nil, err
	}
	var key string
	err := tx.QueryRow("SELECT secret FROM aws_cloudwatch_alarm_settings WHERE id=1").Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		var raw [32]byte
		if _, err = rand.Read(raw[:]); err != nil {
			return nil, err
		}
		key = hex.EncodeToString(raw[:])
		if _, err = tx.Exec("INSERT INTO aws_cloudwatch_alarm_settings VALUES(1,?)", key); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("invalid alarm cursor authority")
	}
	return raw, nil
}
func alarmWrite(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	var before, after int64
	if err := tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&after); err != nil {
		return err
	}
	if count != 1 || after-before != 1 {
		return errors.New("alarm mutation was not acknowledged exactly")
	}
	return nil
}
func readAlarm(ctx context.Context, tx *sql.Tx, region, name string) (metricAlarm, error) {
	var a metricAlarm
	var generation, data string
	err := tx.QueryRowContext(ctx, "SELECT generation,document FROM aws_cloudwatch_alarms WHERE region=? AND name=?", region, name).Scan(&generation, &data)
	if err != nil {
		return a, err
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&a) != nil || decoder.Decode(new(any)) != io.EOF || !validSavedAlarm(a) || a.AlarmName != name || a.AlarmArn != alarmARN(region, name) || a.Generation != generation || len(generation) != 32 {
		return a, errors.New("invalid saved alarm identity")
	}
	if raw, err := hex.DecodeString(generation); err != nil || len(raw) != 16 {
		return a, errors.New("invalid saved alarm generation")
	}
	return a, nil
}
func storeAlarm(ctx context.Context, tx *sql.Tx, region string, a metricAlarm, create bool) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	query := "UPDATE aws_cloudwatch_alarms SET document=? WHERE region=? AND name=? AND generation=?"
	args := []any{string(raw), region, a.AlarmName, a.Generation}
	if create {
		query = "INSERT INTO aws_cloudwatch_alarms(region,name,generation,document) VALUES(?,?,?,?)"
		args = []any{region, a.AlarmName, a.Generation, string(raw)}
	}
	if err = alarmWrite(ctx, tx, query, args...); err != nil {
		return err
	}
	var saved string
	if err = tx.QueryRowContext(ctx, "SELECT document FROM aws_cloudwatch_alarms WHERE region=? AND name=? AND generation=?", region, a.AlarmName, a.Generation).Scan(&saved); err != nil {
		return err
	}
	if saved != string(raw) {
		return errors.New("alarm write readback mismatch")
	}
	return nil
}
func (s *Store) saveAlarm(ctx context.Context, region string, c alarmConfig, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := readAlarm(ctx, tx, region, c.AlarmName)
	create := errors.Is(err, sql.ErrNoRows)
	if err != nil && !create {
		return err
	}
	if create {
		var count, total int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FILTER (WHERE region=?), count(*) FROM aws_cloudwatch_alarms", region).Scan(&count, &total); err != nil {
			return err
		}
		if count >= 100 || total >= 1000 {
			return requestError{"LimitExceeded", "Local alarm limits are 100 per Region and 1000 per team.", 400}
		}
		var raw [16]byte
		if _, err = rand.Read(raw[:]); err != nil {
			return err
		}
		a = metricAlarm{AlarmArn: alarmARN(region, c.AlarmName), StateValue: "INSUFFICIENT_DATA", StateReason: "No completed local minute has been evaluated.", StateReasonData: `{"version":"1.0","reason":"created"}`, StateUpdatedTimestamp: now.Format(time.RFC3339Nano), StateTransitionedTimestamp: now.Format(time.RFC3339Nano), Generation: hex.EncodeToString(raw[:])}
	}
	a.alarmConfig = c
	a.AlarmConfigurationUpdatedTimestamp = now.Format(time.RFC3339Nano)
	if err = storeAlarm(ctx, tx, region, a, create); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return tx.Commit()
}
func evaluateAlarm(ctx context.Context, tx *sql.Tx, region string, a metricAlarm, now time.Time) (metricAlarm, error) {
	end := now.Truncate(time.Minute)
	start := end.Add(-time.Minute)
	rows, err := tx.QueryContext(ctx, `SELECT value FROM aws_cloudwatch_samples WHERE region=? AND namespace=? AND metric=? AND dimensions=? AND unit=? AND at_ns>=? AND at_ns<? ORDER BY at_ns,id`, region, a.Namespace, a.MetricName, a.DimensionsJSON, a.Unit, start.UnixNano(), end.UnixNano())
	if err != nil {
		return a, err
	}
	var aggregate aggregate
	for rows.Next() {
		var value float64
		if err = rows.Scan(&value); err != nil {
			rows.Close()
			return a, err
		}
		aggregate.add(value)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return a, err
	}
	if closeErr != nil {
		return a, closeErr
	}
	state := "INSUFFICIENT_DATA"
	value := 0.0
	if aggregate.count > 0 {
		sum := aggregate.sum + aggregate.correction
		switch a.Statistic {
		case "Average":
			value = sum / float64(aggregate.count)
		case "Sum":
			value = sum
		case "SampleCount":
			value = float64(aggregate.count)
		case "Minimum":
			value = aggregate.min
		case "Maximum":
			value = aggregate.max
		default:
			return a, errors.New("unsupported saved alarm statistic")
		}
		breach := false
		switch a.ComparisonOperator {
		case "GreaterThanThreshold":
			breach = value > a.Threshold
		case "GreaterThanOrEqualToThreshold":
			breach = value >= a.Threshold
		case "LessThanThreshold":
			breach = value < a.Threshold
		case "LessThanOrEqualToThreshold":
			breach = value <= a.Threshold
		default:
			return a, errors.New("unsupported saved alarm comparison")
		}
		state = "OK"
		if breach {
			state = "ALARM"
		}
	}
	reason := "Local completed-minute evaluation: no eligible observations; no statistic was measured."
	var measured *float64
	if aggregate.count > 0 {
		measured = &value
		reason = fmt.Sprintf("Local completed-minute evaluation: %d samples, %s=%g, %s %g.", aggregate.count, a.Statistic, value, a.ComparisonOperator, a.Threshold)
	}
	data, err := json.Marshal(struct {
		Version, Start, End, Statistic string
		SampleCount                    int
		Value                          *float64
		Threshold                      float64
	}{"1.0", start.Format(time.RFC3339), end.Format(time.RFC3339), a.Statistic, aggregate.count, measured, a.Threshold})
	if err != nil {
		return a, err
	}
	if state != a.StateValue || reason != a.StateReason || string(data) != a.StateReasonData {
		if state != a.StateValue {
			a.StateTransitionedTimestamp = now.Format(time.RFC3339Nano)
		}
		a.StateValue, a.StateReason, a.StateReasonData = state, reason, string(data)
		a.StateUpdatedTimestamp = now.Format(time.RFC3339Nano)
		err = storeAlarm(ctx, tx, region, a, false)
	}
	return a, err
}
func (s *Store) evaluateAlarms(ctx context.Context, tx *sql.Tx, region, namespace string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, "SELECT name FROM aws_cloudwatch_alarms WHERE region=? ORDER BY name", region)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		a, err := readAlarm(ctx, tx, region, name)
		if err != nil {
			return err
		}
		if a.Namespace == namespace {
			if _, err = evaluateAlarm(ctx, tx, region, a, now); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) deleteAlarm(ctx context.Context, region, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := readAlarm(ctx, tx, region, name)
	if errors.Is(err, sql.ErrNoRows) {
		return requestError{"ResourceNotFound", "The named alarm does not exist.", 404}
	}
	if err != nil {
		return err
	}
	if err = alarmWrite(ctx, tx, "DELETE FROM aws_cloudwatch_alarms WHERE region=? AND name=? AND generation=?", region, name, a.Generation); err != nil {
		return err
	}
	var remains int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_cloudwatch_alarms WHERE region=? AND name=?", region, name).Scan(&remains); err != nil {
		return err
	}
	if remains != 0 {
		return errors.New("alarm deletion readback mismatch")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

type alarmCursor struct {
	Region, Name, Prefix, State, Last string
	Limit                             int
}

func (s *Store) cursor(q alarmCursor) string {
	raw, _ := json.Marshal(q)
	mac := hmac.New(sha256.New, s.alarmKey)
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(append(mac.Sum(nil), raw...))
}
func (s *Store) marker(region string, q alarmDescribe) (string, error) {
	if q.token == "" {
		return "", nil
	}
	bad := requestError{"InvalidNextToken", "Invalid or differently scoped alarm continuation.", 400}
	raw, err := base64.RawURLEncoding.DecodeString(q.token)
	if err != nil || len(raw) <= sha256.Size {
		return "", bad
	}
	mac := hmac.New(sha256.New, s.alarmKey)
	mac.Write(raw[sha256.Size:])
	if !hmac.Equal(raw[:sha256.Size], mac.Sum(nil)) {
		return "", bad
	}
	var c alarmCursor
	if json.Unmarshal(raw[sha256.Size:], &c) != nil || c.Region != region || c.Name != q.name || c.Prefix != q.prefix || c.State != q.state || c.Limit != q.limit || !printable(c.Last, 255) {
		return "", bad
	}
	return c.Last, nil
}
func (s *Store) describeAlarms(ctx context.Context, region string, q alarmDescribe, now time.Time) ([]metricAlarm, string, error) {
	marker, err := s.marker(region, q)
	if err != nil {
		return nil, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT name FROM aws_cloudwatch_alarms WHERE region=? ORDER BY name", region)
	if err != nil {
		return nil, "", err
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return nil, "", err
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	var result []metricAlarm
	for _, name := range names {
		if q.name != "" && name != q.name || !strings.HasPrefix(name, q.prefix) {
			continue
		}
		a, err := readAlarm(ctx, tx, region, name)
		if err != nil {
			return nil, "", err
		}
		a, err = evaluateAlarm(ctx, tx, region, a, now)
		if err != nil {
			return nil, "", err
		}
		if name > marker && (q.state == "" || a.StateValue == q.state) {
			result = append(result, a)
		}
	}
	token := ""
	if len(result) > q.limit {
		result = result[:q.limit]
		token = s.cursor(alarmCursor{region, q.name, q.prefix, q.state, result[len(result)-1].AlarmName, q.limit})
	}
	if err = ctx.Err(); err != nil {
		return nil, "", err
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return result, token, nil
}

func evaluateRestartedAlarms(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, "SELECT region,name FROM aws_cloudwatch_alarms ORDER BY region,name")
	if err != nil {
		return err
	}
	var keys [][2]string
	for rows.Next() {
		var key [2]string
		if err = rows.Scan(&key[0], &key[1]); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(keys) > 1000 {
		return errors.New("saved alarm quota exceeded")
	}
	for _, key := range keys {
		a, err := readAlarm(ctx, tx, key[0], key[1])
		if err != nil {
			return err
		}
		if _, err = evaluateAlarm(ctx, tx, key[0], a, now); err != nil {
			return err
		}
	}
	return nil
}
