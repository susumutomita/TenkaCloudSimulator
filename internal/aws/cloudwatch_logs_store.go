// SPDX-License-Identifier: MIT
// Provider-owned Logs state, native projections and transactional admission.
// Historical notice retained for earlier, replaced Kumo adaptations:
// Copyright (c) 2025 sivchari
// Original permission and historical provenance: third_party/KUMO-LICENSE.txt
// and the corresponding third_party/kumo-*.json record.
package aws

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
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

// These domain values have no serialization layout. The explicit protocol
// projection below is defined by AWS InputLogEvent/OutputLogEvent/LogGroup/
// LogStream/RejectedLogEventsInfo, not by an emulator's persistence structs.
type LogsInputEvent struct {
	Timestamp int64
	Message   string
}
type LogsEvent struct {
	Timestamp     int64
	Message       string
	IngestionTime int64
	id            int64
}
type LogsGroup struct {
	Name                    string
	Created                 int64
	ARN, LogGroupARN, Class string
	Bytes                   int64
	Filters                 int
}
type LogsStream struct {
	Name                  string
	Created               int64
	ARN                   string
	First, Last, Ingested *int64
	Bytes                 int64
}
type LogsRejected struct{ TooOld, TooNew *int }

// Output fields are selected deliberately: resource generations and SQL row IDs
// are never wire fields. Optional timestamps/rejection indices preserve zero.
func (v LogsInputEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"timestamp": v.Timestamp, "message": v.Message})
}
func (v LogsEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"timestamp": v.Timestamp, "message": v.Message, "ingestionTime": v.IngestionTime})
}
func (v LogsGroup) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"logGroupName": v.Name, "creationTime": v.Created, "arn": v.ARN, "logGroupArn": v.LogGroupARN, "logGroupClass": v.Class, "storedBytes": v.Bytes, "metricFilterCount": v.Filters})
}
func (v LogsStream) MarshalJSON() ([]byte, error) {
	fields := map[string]any{"logStreamName": v.Name, "creationTime": v.Created, "arn": v.ARN, "storedBytes": v.Bytes}
	if v.First != nil {
		fields["firstEventTimestamp"] = *v.First
	}
	if v.Last != nil {
		fields["lastEventTimestamp"] = *v.Last
	}
	if v.Ingested != nil {
		fields["lastIngestionTime"] = *v.Ingested
	}
	return json.Marshal(fields)
}
func (v LogsRejected) MarshalJSON() ([]byte, error) {
	fields := map[string]any{}
	if v.TooOld != nil {
		fields["tooOldLogEventEndIndex"] = *v.TooOld
	}
	if v.TooNew != nil {
		fields["tooNewLogEventStartIndex"] = *v.TooNew
	}
	return json.Marshal(fields)
}

// Preserve the former exported values' JSON decoding contract, including the
// standard case-insensitive field matching and last-member-wins behavior. Native
// HTTP admission separately rejects unknown/duplicate/noncanonical keys first.
func logsDecodeFields(data []byte, fields map[string]any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return nil
	}
	if token != json.Delim('{') {
		return errors.New("expected Logs JSON object")
	}
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("expected Logs JSON member")
		}
		var raw json.RawMessage
		if err = dec.Decode(&raw); err != nil {
			return err
		}
		target, found := fields[name]
		if !found {
			for candidate, pointer := range fields {
				if strings.EqualFold(name, candidate) {
					target, found = pointer, true
					break
				}
			}
		}
		if found {
			if err = json.Unmarshal(raw, target); err != nil {
				return err
			}
		}
	}
	if _, err = dec.Token(); err != nil {
		return err
	}
	if _, err = dec.Token(); err != io.EOF {
		return errors.New("expected one Logs JSON object")
	}
	return nil
}
func (v *LogsInputEvent) UnmarshalJSON(data []byte) error {
	return logsDecodeFields(data, map[string]any{"timestamp": &v.Timestamp, "message": &v.Message})
}
func (v *LogsEvent) UnmarshalJSON(data []byte) error {
	return logsDecodeFields(data, map[string]any{"timestamp": &v.Timestamp, "message": &v.Message, "ingestionTime": &v.IngestionTime})
}
func (v *LogsGroup) UnmarshalJSON(data []byte) error {
	return logsDecodeFields(data, map[string]any{"logGroupName": &v.Name, "creationTime": &v.Created, "arn": &v.ARN, "logGroupArn": &v.LogGroupARN, "logGroupClass": &v.Class, "storedBytes": &v.Bytes, "metricFilterCount": &v.Filters})
}
func (v *LogsStream) UnmarshalJSON(data []byte) error {
	return logsDecodeFields(data, map[string]any{"logStreamName": &v.Name, "creationTime": &v.Created, "arn": &v.ARN, "firstEventTimestamp": &v.First, "lastEventTimestamp": &v.Last, "lastIngestionTime": &v.Ingested, "storedBytes": &v.Bytes})
}
func (v *LogsRejected) UnmarshalJSON(data []byte) error {
	return logsDecodeFields(data, map[string]any{"tooOldLogEventEndIndex": &v.TooOld, "tooNewLogEventStartIndex": &v.TooNew})
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
	return s.createCatalogEntry(ctx, tx, region, name, nil)
}

// A catalog command is admitted from one relational snapshot, not a mutable map
// or separately cached stream metadata. Parent/duplicate errors precede quotas.
// Existing SQL persistence and generated incarnation IDs remain authoritative.
func (s *CloudWatchLogsStore) createCatalogEntry(ctx context.Context, tx *sql.Tx, region, group string, stream *string) error {
	if err := validLogsRegion(region); err != nil {
		return err
	}
	if stream == nil {
		if !logsGroupName.MatchString(group) || strings.HasPrefix(group, "aws/") {
			return logsInvalid("Use a 1–512 character log group name; aws/ is reserved.")
		}
	} else if !logsGroupName.MatchString(group) || !validLogsStream(*stream) {
		return logsInvalid("Invalid log group or stream name.")
	}
	streamName := ""
	if stream != nil {
		streamName = *stream
	}
	var parentExists, entryExists bool
	var groupCount, streamCount int
	err := tx.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM aws_logs_groups WHERE region=? AND name=?),
 EXISTS(SELECT 1 FROM aws_logs_streams WHERE region=? AND group_name=? AND name=?),
 (SELECT count(*) FROM aws_logs_groups), (SELECT count(*) FROM aws_logs_streams)`, region, group, region, group, streamName).Scan(&parentExists, &entryExists, &groupCount, &streamCount)
	if err != nil {
		return err
	}
	if stream == nil {
		if parentExists {
			return logsFailure{"ResourceAlreadyExistsException", "Log group already exists."}
		}
		if groupCount >= 100 {
			return logsFailure{"LimitExceededException", "Simulator limit: 100 log groups per database."}
		}
		return logsExactlyOne(tx.ExecContext(ctx, "INSERT INTO aws_logs_groups VALUES(?,?,?,?)", region, group, logsGeneration(), s.now().UnixMilli()))
	}
	if !parentExists {
		return logsMissing()
	}
	if entryExists {
		return logsFailure{"ResourceAlreadyExistsException", "Log stream already exists."}
	}
	if streamCount >= 1000 {
		return logsFailure{"LimitExceededException", "Simulator limit: 1000 log streams per database."}
	}
	return logsExactlyOne(tx.ExecContext(ctx, "INSERT INTO aws_logs_streams VALUES(?,?,?,?,?)", region, group, *stream, logsGeneration(), s.now().UnixMilli()))
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
	// Validate before opening a transaction, preserving native admission errors.
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
	if err = s.createCatalogEntry(ctx, tx, region, group, &name); err != nil {
		return err
	}
	return tx.Commit()
}

// logsIngestPlan is a deterministic admission result for one supplied batch.
// AWS requires chronological input; after validation, binary-search cut points
// identify the contiguous accepted window without sorting caller data; accepted
// values are copied so the plan does not borrow the caller's mutable slice.
type logsIngestPlan struct {
	accepted []LogsInputEvent
	bytes    int
	rejected *LogsRejected
}

func planLogsIngest(events []LogsInputEvent, now int64) (logsIngestPlan, error) {
	var plan logsIngestPlan
	if len(events) == 0 || len(events) > 10000 {
		return plan, logsInvalid("A batch requires 1–10000 events.")
	}
	payload := 0
	for i, event := range events {
		if event.Timestamp < 0 || !utf8.ValidString(event.Message) || len(event.Message) == 0 {
			return plan, logsInvalid("Events require a nonnegative timestamp and nonempty UTF-8 message.")
		}
		payload += len(event.Message) + 26
		if payload > 1048576 {
			return plan, logsInvalid("Batch exceeds 1048576 bytes including 26 bytes per event.")
		}
		if i > 0 && event.Timestamp < events[i-1].Timestamp {
			return plan, logsInvalid("Batch events must be in chronological order.")
		}
	}
	oldest := now - int64(14*24*time.Hour/time.Millisecond)
	newest := now + int64(2*time.Hour/time.Millisecond)
	first := sort.Search(len(events), func(i int) bool { return events[i].Timestamp >= oldest })
	end := first + sort.Search(len(events)-first, func(i int) bool { return events[first+i].Timestamp > newest })
	plan.accepted = slices.Clone(events[first:end])
	if first > 0 {
		lastOld := first - 1
		plan.rejected = &LogsRejected{TooOld: &lastOld}
	}
	if end < len(events) {
		if plan.rejected == nil {
			plan.rejected = &LogsRejected{}
		}
		firstNew := end
		plan.rejected.TooNew = &firstNew
	}
	if len(plan.accepted) > 0 && plan.accepted[len(plan.accepted)-1].Timestamp-plan.accepted[0].Timestamp > int64(24*time.Hour/time.Millisecond) {
		return logsIngestPlan{}, logsInvalid("Accepted events may span at most 24 hours.")
	}
	for _, event := range plan.accepted {
		plan.bytes += len(event.Message)
	}
	return plan, nil
}
func (s *CloudWatchLogsStore) PutEvents(ctx context.Context, region, group, stream string, events []LogsInputEvent) (*LogsRejected, error) {
	if err := validLogsRegion(region); err != nil {
		return nil, err
	}
	if !logsGroupName.MatchString(group) || !validLogsStream(stream) {
		return nil, logsInvalid("Invalid log group or stream name.")
	}
	now := s.now().UnixMilli()
	plan, err := planLogsIngest(events, now)
	if err != nil {
		return nil, err
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
	if count+int64(len(plan.accepted)) > 100000 || stored+int64(plan.bytes) > 64<<20 {
		return nil, logsFailure{"LimitExceededException", "Simulator event storage limit: 100000 events or 64 MiB per database."}
	}
	for _, event := range plan.accepted {
		if err = logsExactlyOne(tx.ExecContext(ctx, "INSERT INTO aws_logs_events(region,group_name,stream_name,timestamp,ingested,message) VALUES(?,?,?,?,?,?)", region, group, stream, event.Timestamp, now, event.Message)); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return plan.rejected, nil
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
