package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func logsDecodedObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var obj map[string]any
	if e := json.Unmarshal(data, &obj); e != nil {
		t.Fatal(e)
	}
	return obj
}
func logsAssertObjectKeys(t *testing.T, obj map[string]any, keys ...string) {
	t.Helper()
	if len(obj) != len(keys) {
		t.Fatalf("unexpected protocol fields: %v", obj)
	}
	for _, key := range keys {
		if _, ok := obj[key]; !ok {
			t.Fatal("missing protocol field", key)
		}
	}
}
func TestLogsOriginalProtocolProjectionUsesSavedDataAndOptionalZero(t *testing.T) {
	_, s := newLogsTest(t)
	s.now = func() time.Time { return time.UnixMilli(0) }
	seedLogs(t, s, "schema-group", "schema-stream")
	allow := func(string, string) error { return nil }
	groupResponse := logsHTTP(s, "DescribeLogGroups", `{}`, allow)
	if groupResponse.Code != 200 {
		t.Fatal(groupResponse.Code)
	}
	groups := logsDecodedObject(t, groupResponse.Body.Bytes())["logGroups"].([]any)
	g := groups[0].(map[string]any)
	logsAssertObjectKeys(t, g, "logGroupName", "creationTime", "arn", "logGroupArn", "logGroupClass", "storedBytes", "metricFilterCount")
	if g["logGroupName"] != "schema-group" || g["creationTime"] != float64(0) || g["arn"] != "arn:aws:logs:us-east-1:000000000000:log-group:schema-group:*" || g["logGroupArn"] != "arn:aws:logs:us-east-1:000000000000:log-group:schema-group" || g["logGroupClass"] != "STANDARD" || g["storedBytes"] != float64(0) || g["metricFilterCount"] != float64(0) {
		t.Fatal("group projection did not match saved zero/ARN data", g)
	}
	streamBody := `{"logGroupName":"schema-group"}`
	streamResponse := logsHTTP(s, "DescribeLogStreams", streamBody, allow)
	if streamResponse.Code != 200 {
		t.Fatal(streamResponse.Code)
	}
	stream := logsDecodedObject(t, streamResponse.Body.Bytes())["logStreams"].([]any)[0].(map[string]any)
	logsAssertObjectKeys(t, stream, "logStreamName", "creationTime", "arn", "storedBytes")
	// Epoch zero is a real accepted event timestamp, not an absent optional field.
	batch := `{"logGroupName":"schema-group","logStreamName":"schema-stream","sequenceToken":"obsolete","logEvents":[{"message":"epoch","timestamp":0}]}`
	if response := logsHTTP(s, "PutLogEvents", batch, allow); response.Code != 200 {
		t.Fatal(response.Code)
	}
	s.now = func() time.Time { return time.UnixMilli(1000) }
	streamResponse = logsHTTP(s, "DescribeLogStreams", streamBody, allow)
	stream = logsDecodedObject(t, streamResponse.Body.Bytes())["logStreams"].([]any)[0].(map[string]any)
	logsAssertObjectKeys(t, stream, "logStreamName", "creationTime", "arn", "storedBytes", "firstEventTimestamp", "lastEventTimestamp", "lastIngestionTime")
	for _, key := range []string{"firstEventTimestamp", "lastEventTimestamp", "lastIngestionTime"} {
		if stream[key] != float64(0) {
			t.Fatal("optional zero omitted or replaced", key, stream)
		}
	}
	// Stream bytes remain the documented deprecated zero, while group bytes are real.
	if stream["storedBytes"] != float64(0) {
		t.Fatal("invented stream-byte metadata")
	}
	eventResponse := logsHTTP(s, "GetLogEvents", `{"logGroupName":"schema-group","logStreamName":"schema-stream","startFromHead":true}`, allow)
	if eventResponse.Code != 200 {
		t.Fatal(eventResponse.Code)
	}
	event := logsDecodedObject(t, eventResponse.Body.Bytes())["events"].([]any)[0].(map[string]any)
	logsAssertObjectKeys(t, event, "timestamp", "message", "ingestionTime")
	if event["timestamp"] != float64(0) || event["ingestionTime"] != float64(0) || event["message"] != "epoch" {
		t.Fatal("read changed saved ingest timestamp", event)
	}
	groupResponse = logsHTTP(s, "DescribeLogGroups", `{}`, allow)
	g = logsDecodedObject(t, groupResponse.Body.Bytes())["logGroups"].([]any)[0].(map[string]any)
	if g["storedBytes"] != float64(5) {
		t.Fatal("group byte count not actual UTF8 payload", g)
	}
}
func TestLogsOriginalProtocolValuesRetainJSONRoundtrip(t *testing.T) {
	zero, index := int64(0), 0
	values := []any{LogsInputEvent{17, "literal +%/日本語"}, LogsEvent{17, "saved", 19, 0}, LogsGroup{"group", 0, "arn:*", "arn", "STANDARD", 0, 0}, LogsStream{"stream", 0, "arn", &zero, &zero, &zero, 0}, LogsStream{Name: "empty"}, LogsRejected{TooOld: &index, TooNew: &index}, LogsRejected{}}
	for _, value := range values {
		t.Run(fmt.Sprintf("%T-%v", value, reflect.ValueOf(value).NumField()), func(t *testing.T) {
			data, e := json.Marshal(value)
			logsMust(t, e)
			target := reflect.New(reflect.TypeOf(value))
			logsMust(t, json.Unmarshal(data, target.Interface()))
			if !reflect.DeepEqual(target.Elem().Interface(), value) {
				t.Fatalf("changed typed JSON projection: %T", value)
			}
		})
	}
	var input LogsInputEvent
	logsMust(t, json.Unmarshal([]byte(`{"timestamp":1,"TIMESTAMP":2,"message":"first","MESSAGE":"last","ignored":true}`), &input))
	if input.Timestamp != 2 || input.Message != "last" {
		t.Fatal("typed decoder changed existing last-member matching")
	}
	if e := json.Unmarshal([]byte(`{"timestamp":"not-a-number"}`), &input); e == nil {
		t.Fatal("typed decoder accepted invalid timestamp")
	}
	if e := json.Unmarshal([]byte(`[]`), &input); e == nil {
		t.Fatal("typed decoder accepted non-object")
	}
}
func TestLogsOriginalBatchWindowCutPointsRemainNativeAndAtomic(t *testing.T) {
	db, s := newLogsTest(t)
	ctx := context.Background()
	seedLogs(t, s, "target", "stream")
	seedLogs(t, s, "peer", "stream")
	now := s.now().UnixMilli()
	day := int64(24 * time.Hour / time.Millisecond)
	hour := int64(time.Hour / time.Millisecond)
	tests := []struct {
		name     string
		events   []LogsInputEvent
		old, new *int
		accepted []string
	}{
		{"old-cutoff-inclusive", []LogsInputEvent{{now - 14*day - 2, "old-a"}, {now - 14*day - 1, "old-b"}, {now - 14*day, "edge"}}, func() *int { v := 1; return &v }(), nil, []string{"edge"}},
		{"future-cutoff-inclusive", []LogsInputEvent{{now + 2*hour, "edge"}, {now + 2*hour + 1, "future-a"}, {now + 2*hour + 2, "future-b"}}, nil, func() *int { v := 1; return &v }(), []string{"edge"}},
		{"both-cut-points", []LogsInputEvent{{now - 15*day, "old-a"}, {now - 15*day, "old-b"}, {now - 1, "same-a"}, {now - 1, "same-b"}, {now + 2*hour + 1, "future"}}, func() *int { v := 1; return &v }(), func() *int { v := 4; return &v }(), []string{"same-a", "same-b"}},
		{"all-old", []LogsInputEvent{{now - 15*day, "old"}}, func() *int { v := 0; return &v }(), nil, []string{}},
		{"all-future", []LogsInputEvent{{now + 2*hour + 1, "future"}}, nil, func() *int { v := 0; return &v }(), []string{}},
		{"span-exact-24h", []LogsInputEvent{{now - day, "first"}, {now, "last"}}, nil, nil, []string{"first", "last"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logsMust(t, s.CreateStream(ctx, "us-east-1", "target", tc.name))
			original := append([]LogsInputEvent(nil), tc.events...)
			rejection, e := s.PutEvents(ctx, "us-east-1", "target", tc.name, tc.events)
			logsMust(t, e)
			if tc.old == nil && tc.new == nil {
				if rejection != nil {
					t.Fatal(rejection)
				}
			} else if rejection == nil || !reflect.DeepEqual(rejection.TooOld, tc.old) || !reflect.DeepEqual(rejection.TooNew, tc.new) {
				t.Fatal("incorrect source batch indices", rejection)
			}
			if !reflect.DeepEqual(tc.events, original) {
				t.Fatal("batch was sorted or mutated")
			}
			page, e := s.Events(ctx, "us-east-1", "target", tc.name, LogsEventsQuery{StartFromHead: true, Limit: 100})
			logsMust(t, e)
			actual := []string{}
			for _, event := range page.Events {
				actual = append(actual, event.Message)
				if event.IngestionTime != now {
					t.Fatal("unstable ingest timestamp")
				}
			}
			if !reflect.DeepEqual(actual, tc.accepted) {
				t.Fatal(actual, tc.accepted)
			}
		})
	}
	before := logsGraphSnapshot(t, db.Database())
	for _, batch := range [][]LogsInputEvent{
		{{now - day - 1, "first"}, {now, "last"}},                         // both in accepted window, span exceeds24h
		{{now - 15*day, strings.Repeat("x", 1048576-26)}, {now, "valid"}}, // rejected-old messages still count toward batch bytes
	} {
		if _, e := s.PutEvents(ctx, "us-east-1", "target", "stream", batch); logsCode(e) != "InvalidParameterException" {
			t.Fatal(e)
		}
		if logsGraphSnapshot(t, db.Database()) != before {
			t.Fatal("invalid batch partially committed")
		}
	}
}
func TestLogsOriginalBatchByteBudgetUsesUTF8AndOverhead(t *testing.T) {
	db, s := newLogsTest(t)
	ctx := context.Background()
	seedLogs(t, s, "group", "stream")
	now := s.now().UnixMilli()
	text := strings.Repeat("界", (1048576-26)/3) + "ab"
	if len(text)+26 != 1048576 {
		t.Fatal("fixture does not hit exact byte limit")
	}
	_, e := s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now, text}})
	logsMust(t, e)
	before := logsGraphSnapshot(t, db.Database())
	if _, e = s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now, text + "c"}}); logsCode(e) != "InvalidParameterException" {
		t.Fatal(e)
	}
	if logsGraphSnapshot(t, db.Database()) != before {
		t.Fatal("over-limit UTF8 batch changed persisted state")
	}
	page, e := s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{StartFromHead: true, Limit: 1})
	logsMust(t, e)
	if len(page.Events) != 1 || page.Events[0].Message != text {
		t.Fatal("boundary message was truncated")
	}
}
func TestLogsOriginalCatalogSnapshotErrorPriorityAndRegionalUniqueness(t *testing.T) {
	db, s := newLogsTest(t)
	ctx := context.Background()
	seedLogs(t, s, "retained", "stream")
	logsMust(t, s.CreateGroup(ctx, "us-west-2", "retained"))
	tx, e := db.Database().BeginTx(ctx, nil)
	logsMust(t, e)
	defer tx.Rollback()
	for i := 2; i < 100; i++ {
		logsMust(t, s.CreateGroupTx(ctx, tx, "us-east-1", fmt.Sprintf("group-%03d", i)))
	}
	logsMust(t, tx.Commit())
	before := logsGraphSnapshot(t, db.Database())
	if e = s.CreateGroup(ctx, "us-east-1", "retained"); logsCode(e) != "ResourceAlreadyExistsException" {
		t.Fatal("quota hid existing group", e)
	}
	if e = s.CreateGroup(ctx, "us-east-1", "over-limit"); logsCode(e) != "LimitExceededException" {
		t.Fatal(e)
	}
	if logsGraphSnapshot(t, db.Database()) != before {
		t.Fatal("group admission changed existing state")
	}
	// Controlled persisted fixture reaches the independent stream cap without
	// claiming1000SDK creations. Native requests then exercise snapshot priority.
	tx, e = db.Database().BeginTx(ctx, nil)
	logsMust(t, e)
	defer tx.Rollback()
	for i := 1; i < 1000; i++ {
		_, e = tx.ExecContext(ctx, "INSERT INTO aws_logs_streams VALUES(?,?,?,?,?)", "us-east-1", "retained", fmt.Sprintf("stream-%04d", i), fmt.Sprintf("fixture-%d", i), s.now().UnixMilli())
		logsMust(t, e)
	}
	logsMust(t, tx.Commit())
	before = logsGraphSnapshot(t, db.Database())
	for _, tc := range []struct{ group, stream, code string }{{"missing", "new", "ResourceNotFoundException"}, {"retained", "stream", "ResourceAlreadyExistsException"}, {"retained", "over-limit", "LimitExceededException"}} {
		if e = s.CreateStream(ctx, "us-east-1", tc.group, tc.stream); logsCode(e) != tc.code {
			t.Fatal(tc, e)
		}
		if logsGraphSnapshot(t, db.Database()) != before {
			t.Fatal("stream admission changed graph")
		}
	}
	logsMust(t, s.DeleteGroup(ctx, "us-east-1", "retained"))
	logsMust(t, s.CreateGroup(ctx, "us-east-1", "repaired"))
	logsMust(t, s.CreateStream(ctx, "us-east-1", "repaired", "new"))
	groups, _, e := s.Groups(ctx, "us-west-2", "retained", "", 50)
	logsMust(t, e)
	if len(groups) != 1 || groups[0].Name != "retained" {
		t.Fatal("regional peer lost during cleanup")
	}
}
