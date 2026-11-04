// Copyright (c) 2025 sivchari
// SPDX-License-Identifier: MIT
// Wire event shapes and group/stream lifecycle adapted from Kumo ce37a8fb406ecb96b5619204f645c26302b651a5,
// internal/service/cloudwatchlogs/{types,storage}.go; see third_party/KUMO-LICENSE.txt.
// SQLite transactions, ingest validation and authenticated pagination are local.
package aws

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// CloudWatchLogsStore is regional provider-owned state. Team hosts must allocate
// separate SQLite databases and access-token authorities, as for other services.
type CloudWatchLogsStore struct {
	db  *sql.DB
	now func() time.Time
	key []byte
}
type LogsInputEvent struct {
	Timestamp int64  `json:"timestamp"`
	Message   string `json:"message"`
}
type LogsEvent struct {
	Timestamp     int64  `json:"timestamp"`
	Message       string `json:"message"`
	IngestionTime int64  `json:"ingestionTime"`
	id            int64
}
type LogsGroup struct {
	Name        string `json:"logGroupName"`
	Created     int64  `json:"creationTime"`
	ARN         string `json:"arn"`
	LogGroupARN string `json:"logGroupArn"`
	Class       string `json:"logGroupClass"`
	Bytes       int64  `json:"storedBytes"`
	Filters     int    `json:"metricFilterCount"`
}
type LogsStream struct {
	Name     string `json:"logStreamName"`
	Created  int64  `json:"creationTime"`
	ARN      string `json:"arn"`
	First    *int64 `json:"firstEventTimestamp,omitempty"`
	Last     *int64 `json:"lastEventTimestamp,omitempty"`
	Ingested *int64 `json:"lastIngestionTime,omitempty"`
	Bytes    int64  `json:"storedBytes"`
}
type LogsRejected struct {
	TooOld *int `json:"tooOldLogEventEndIndex,omitempty"`
	TooNew *int `json:"tooNewLogEventStartIndex,omitempty"`
}
type logsFailure struct{ code, message string }

func (e logsFailure) Error() string    { return e.message }
func logsInvalid(message string) error { return logsFailure{"InvalidParameterException", message} }
func logsMissing() error {
	return logsFailure{"ResourceNotFoundException", "The log group or stream does not exist in this region."}
}

var logsGroupName = regexp.MustCompile(`^[.\-_/#A-Za-z0-9]{1,512}$`)

func validLogsStream(name string) bool {
	return utf8.ValidString(name) && utf8.RuneCountInString(name) > 0 && utf8.RuneCountInString(name) <= 512 && !strings.ContainsAny(name, ":*\x00")
}
func logsGroupARN(region, name string) string {
	return fmt.Sprintf("arn:aws:logs:%s:000000000000:log-group:%s", region, name)
}
func logsStreamARN(region, group, stream string) string {
	return logsGroupARN(region, group) + ":log-stream:" + stream
}
func validLogsRegion(region string) error {
	if !slices.Contains(Regions, region) {
		return logsInvalid("Unsupported simulator region.")
	}
	return nil
}

func NewCloudWatchLogsStore(db *sql.DB) (*CloudWatchLogsStore, error) {
	if db == nil {
		return nil, errors.New("CloudWatch Logs database is required")
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_logs_settings(id INTEGER PRIMARY KEY CHECK(id=1),secret BLOB NOT NULL CHECK(length(secret)=32));
CREATE TABLE IF NOT EXISTS aws_logs_groups(region TEXT NOT NULL,name TEXT NOT NULL,generation TEXT NOT NULL,created INTEGER NOT NULL,PRIMARY KEY(region,name));
CREATE TABLE IF NOT EXISTS aws_logs_streams(region TEXT NOT NULL,group_name TEXT NOT NULL,name TEXT NOT NULL,generation TEXT NOT NULL,created INTEGER NOT NULL,PRIMARY KEY(region,group_name,name),FOREIGN KEY(region,group_name) REFERENCES aws_logs_groups(region,name) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS aws_logs_events(id INTEGER PRIMARY KEY AUTOINCREMENT,region TEXT NOT NULL,group_name TEXT NOT NULL,stream_name TEXT NOT NULL,timestamp INTEGER NOT NULL,ingested INTEGER NOT NULL,message TEXT NOT NULL,FOREIGN KEY(region,group_name,stream_name) REFERENCES aws_logs_streams(region,group_name,name) ON DELETE CASCADE);
CREATE INDEX IF NOT EXISTS aws_logs_event_order ON aws_logs_events(region,group_name,stream_name,timestamp,id);`)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	if _, err = db.Exec("INSERT OR IGNORE INTO aws_logs_settings VALUES(1,?)", key); err != nil {
		return nil, err
	}
	if err = db.QueryRow("SELECT secret FROM aws_logs_settings WHERE id=1").Scan(&key); err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid persisted Logs cursor key")
	}
	return &CloudWatchLogsStore{db: db, now: time.Now, key: key}, nil
}
func logsGeneration() string { return rand.Text() }

// A nil Exec error does not prove a write happened: a host-owned SQLite
// BEFORE INSERT RAISE(IGNORE) can suppress it. Reject that outcome so callers
// roll back the entire operation instead of acknowledging missing/partial data.
func logsExactlyOne(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("CloudWatch Logs write did not affect exactly one row")
	}
	return nil
}

func (s *CloudWatchLogsStore) CreateGroup(ctx context.Context, region, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.CreateGroupTx(ctx, tx, region, name); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateGroupTx lets a trusted CloudFormation adapter share its short transaction.
// It does not confer ownership; that adapter must maintain its own claims.
func (s *CloudWatchLogsStore) CreateGroupTx(ctx context.Context, tx *sql.Tx, region, name string) error {
	if err := validLogsRegion(region); err != nil {
		return err
	}
	if !logsGroupName.MatchString(name) || strings.HasPrefix(name, "aws/") {
		return logsInvalid("Use a 1–512 character log group name; aws/ is reserved.")
	}
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_logs_groups WHERE region=? AND name=?", region, name).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return logsFailure{"ResourceAlreadyExistsException", "Log group already exists."}
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_logs_groups").Scan(&n); err != nil {
		return err
	}
	if n >= 100 {
		return logsFailure{"LimitExceededException", "Simulator limit: 100 log groups per database."}
	}
	return logsExactlyOne(tx.ExecContext(ctx, "INSERT INTO aws_logs_groups VALUES(?,?,?,?)", region, name, logsGeneration(), s.now().UnixMilli()))
}
func (s *CloudWatchLogsStore) DeleteGroup(ctx context.Context, region, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.DeleteGroupTx(ctx, tx, region, name); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *CloudWatchLogsStore) DeleteGroupTx(ctx context.Context, tx *sql.Tx, region, name string) error {
	if err := validLogsRegion(region); err != nil {
		return err
	}
	if !logsGroupName.MatchString(name) {
		return logsInvalid("Invalid log group name.")
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM aws_logs_groups WHERE region=? AND name=?", region, name)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return logsMissing()
	}
	return nil
}
func (s *CloudWatchLogsStore) CreateStream(ctx context.Context, region, group, name string) error {
	if err := validLogsRegion(region); err != nil {
		return err
	}
	if !logsGroupName.MatchString(group) || !validLogsStream(name) {
		return logsInvalid("Invalid log group or stream name.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_logs_groups WHERE region=? AND name=?", region, group).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return logsMissing()
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_logs_streams WHERE region=? AND group_name=? AND name=?", region, group, name).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return logsFailure{"ResourceAlreadyExistsException", "Log stream already exists."}
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_logs_streams").Scan(&n); err != nil {
		return err
	}
	if n >= 1000 {
		return logsFailure{"LimitExceededException", "Simulator limit: 1000 log streams per database."}
	}
	if err = logsExactlyOne(tx.ExecContext(ctx, "INSERT INTO aws_logs_streams VALUES(?,?,?,?,?)", region, group, name, logsGeneration(), s.now().UnixMilli())); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *CloudWatchLogsStore) PutEvents(ctx context.Context, region, group, stream string, events []LogsInputEvent) (*LogsRejected, error) {
	if err := validLogsRegion(region); err != nil {
		return nil, err
	}
	if !logsGroupName.MatchString(group) || !validLogsStream(stream) {
		return nil, logsInvalid("Invalid log group or stream name.")
	}
	if len(events) == 0 || len(events) > 10000 {
		return nil, logsInvalid("A batch requires 1–10000 events.")
	}
	now := s.now().UnixMilli()
	size := 0
	valid := make([]LogsInputEvent, 0, len(events))
	rejected := &LogsRejected{}
	for i, e := range events {
		if e.Timestamp < 0 || !utf8.ValidString(e.Message) || len(e.Message) == 0 {
			return nil, logsInvalid("Events require a nonnegative timestamp and nonempty UTF-8 message.")
		}
		size += len(e.Message) + 26
		if size > 1048576 {
			return nil, logsInvalid("Batch exceeds 1048576 bytes including 26 bytes per event.")
		}
		if i > 0 && e.Timestamp < events[i-1].Timestamp {
			return nil, logsInvalid("Batch events must be in chronological order.")
		}
		if e.Timestamp < now-int64(14*24*time.Hour/time.Millisecond) {
			index := i
			rejected.TooOld = &index
			continue
		}
		if e.Timestamp > now+int64(2*time.Hour/time.Millisecond) {
			if rejected.TooNew == nil {
				index := i
				rejected.TooNew = &index
			}
			continue
		}
		valid = append(valid, e)
	}
	if len(valid) > 0 && valid[len(valid)-1].Timestamp-valid[0].Timestamp > int64(24*time.Hour/time.Millisecond) {
		return nil, logsInvalid("Accepted events may span at most 24 hours.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var generation string
	if err = tx.QueryRowContext(ctx, "SELECT generation FROM aws_logs_streams WHERE region=? AND group_name=? AND name=?", region, group, stream).Scan(&generation); errors.Is(err, sql.ErrNoRows) {
		return nil, logsMissing()
	}
	if err != nil {
		return nil, err
	}
	var count, stored int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(length(CAST(message AS BLOB))),0) FROM aws_logs_events").Scan(&count, &stored); err != nil {
		return nil, err
	}
	bytes := 0
	for _, e := range valid {
		bytes += len(e.Message)
	}
	if count+int64(len(valid)) > 100000 || stored+int64(bytes) > 64<<20 {
		return nil, logsFailure{"LimitExceededException", "Simulator event storage limit: 100000 events or 64 MiB per database."}
	}
	for _, e := range valid {
		if err = logsExactlyOne(tx.ExecContext(ctx, "INSERT INTO aws_logs_events(region,group_name,stream_name,timestamp,ingested,message) VALUES(?,?,?,?,?,?)", region, group, stream, e.Timestamp, now, e.Message)); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if rejected.TooOld == nil && rejected.TooNew == nil {
		return nil, nil
	}
	return rejected, nil
}

type logsCursor struct {
	Scope      string `json:"s"`
	Generation string `json:"g,omitempty"`
	Direction  string `json:"d,omitempty"`
	Timestamp  int64  `json:"t,omitempty"`
	ID         int64  `json:"i,omitempty"`
	Name       string `json:"n,omitempty"`
	Expires    int64  `json:"e"`
}

func (s *CloudWatchLogsStore) encodeCursor(c logsCursor) string {
	b, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, s.key)
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *CloudWatchLogsStore) decodeCursor(token, scope string) (logsCursor, error) {
	var c logsCursor
	// Legal 512-character Unicode/control-escaped stream names are nested once
	// in the query scope and again in JSON before base64 encoding.
	if len(token) > 16384 {
		return c, logsInvalid("Invalid pagination token.")
	}
	a, b, ok := strings.Cut(token, ".")
	if !ok {
		return c, logsInvalid("Invalid pagination token.")
	}
	data, err := base64.RawURLEncoding.DecodeString(a)
	sig, e := base64.RawURLEncoding.DecodeString(b)
	if err != nil || e != nil {
		return c, logsInvalid("Invalid pagination token.")
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(data)
	if !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(data, &c) != nil || c.Scope != scope || c.Expires <= s.now().UnixMilli() {
		return c, logsInvalid("Invalid, expired, or differently scoped pagination token.")
	}
	return c, nil
}
func logsScope(parts ...any) string { b, _ := json.Marshal(parts); return string(b) }
