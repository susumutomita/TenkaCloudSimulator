package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// These fixtures represent host-owned persisted-state damage, not an API by
// which an exercise participant can modify SQL. Reads must fail closed, retain
// the damaged graph for repair, and avoid exposing driver details or log data.
func TestLogsDamagedReadStateRefusesWithoutDiscardingData(t *testing.T) {
	for _, fixture := range []struct{ name, damage, repair, op, body string }{
		{"group-time", `UPDATE aws_logs_groups SET created='private-corrupt-clock' WHERE name='group'`, `UPDATE aws_logs_groups SET created=%d WHERE name='group'`, "DescribeLogGroups", `{}`},
		{"stream-time", `UPDATE aws_logs_streams SET created='private-corrupt-clock' WHERE group_name='group'`, `UPDATE aws_logs_streams SET created=%d WHERE group_name='group'`, "DescribeLogStreams", `{"logGroupName":"group"}`},
		{"event-time", `UPDATE aws_logs_events SET ingested='private-corrupt-clock' WHERE group_name='group'`, `UPDATE aws_logs_events SET ingested=%d WHERE group_name='group'`, "GetLogEvents", `{"logGroupName":"group","logStreamName":"stream","startFromHead":true}`},
		{"group-aggregate-column", `ALTER TABLE aws_logs_events RENAME COLUMN message TO private_missing_message`, `ALTER TABLE aws_logs_events RENAME COLUMN private_missing_message TO message`, "DescribeLogGroups", `{}`},
		{"stream-aggregate-column", `ALTER TABLE aws_logs_events RENAME COLUMN ingested TO private_missing_ingested`, `ALTER TABLE aws_logs_events RENAME COLUMN private_missing_ingested TO ingested`, "DescribeLogStreams", `{"logGroupName":"group"}`},
		{"event-column", `ALTER TABLE aws_logs_events RENAME COLUMN message TO private_missing_message`, `ALTER TABLE aws_logs_events RENAME COLUMN private_missing_message TO message`, "GetLogEvents", `{"logGroupName":"group","logStreamName":"stream","startFromHead":true}`},
		{"group-generation-column", `ALTER TABLE aws_logs_groups RENAME COLUMN generation TO private_missing_generation`, `ALTER TABLE aws_logs_groups RENAME COLUMN private_missing_generation TO generation`, "DescribeLogStreams", `{"logGroupName":"group"}`},
		{"stream-generation-column", `ALTER TABLE aws_logs_streams RENAME COLUMN generation TO private_missing_generation`, `ALTER TABLE aws_logs_streams RENAME COLUMN private_missing_generation TO generation`, "GetLogEvents", `{"logGroupName":"group","logStreamName":"stream","startFromHead":true}`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			db, s := newLogsTest(t)
			ctx := context.Background()
			seedLogs(t, s, "group", "stream")
			seedLogs(t, s, "peer", "peer-stream")
			now := s.now().UnixMilli()
			_, err := s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now, "private-log-value"}})
			logsMust(t, err)
			_, err = s.PutEvents(ctx, "us-east-1", "peer", "peer-stream", []LogsInputEvent{{now, "peer-preserved"}})
			logsMust(t, err)
			original := logsGraphSnapshot(t, db.Database())
			_, err = db.Database().Exec(fixture.damage)
			logsMust(t, err)
			damaged := logsGraphSnapshot(t, db.Database())
			response := logsHTTP(s, fixture.op, fixture.body, func(string, string) error { return nil })
			var body map[string]any
			logsMust(t, json.Unmarshal(response.Body.Bytes(), &body))
			if response.Code != 500 || body["__type"] != "ServiceUnavailableException" {
				t.Fatalf("damaged read acknowledged: status %d", response.Code)
			}
			for _, private := range []string{"private-", "private_", "SQL", "Scan"} {
				if strings.Contains(response.Body.String(), private) {
					t.Fatal("driver or persisted data disclosed")
				}
			}
			if logsGraphSnapshot(t, db.Database()) != damaged {
				t.Fatal("failed read changed damaged persisted graph")
			}
			repair := fixture.repair
			if strings.Contains(repair, "%d") {
				repair = fmt.Sprintf(repair, now)
			}
			_, err = db.Database().Exec(repair)
			logsMust(t, err)
			if logsGraphSnapshot(t, db.Database()) != original {
				t.Fatal("repair did not restore exact graph")
			}
			response = logsHTTP(s, fixture.op, fixture.body, func(string, string) error { return nil })
			if response.Code != 200 {
				t.Fatal("repaired read failed", response.Code)
			}
			page, err := s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{StartFromHead: true, Limit: 10})
			logsMust(t, err)
			if len(page.Events) != 1 || page.Events[0].Message != "private-log-value" {
				t.Fatal("read failure discarded log data")
			}
			logsMust(t, s.DeleteGroup(ctx, "us-east-1", "group"))
			peer, err := s.Events(ctx, "us-east-1", "peer", "peer-stream", LogsEventsQuery{StartFromHead: true, Limit: 10})
			logsMust(t, err)
			if len(peer.Events) != 1 || peer.Events[0].Message != "peer-preserved" {
				t.Fatal("target cleanup changed peer")
			}
		})
	}
}

func TestLogsCursorKeyDamageRefusesReopenWithoutRotationOrDataLoss(t *testing.T) {
	db, s := newLogsTest(t)
	ctx := context.Background()
	seedLogs(t, s, "group", "stream")
	now := s.now().UnixMilli()
	_, err := s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now, "first"}, {now + 1, "second"}})
	logsMust(t, err)
	page, err := s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{StartFromHead: true, Limit: 1})
	logsMust(t, err)
	original := logsGraphSnapshot(t, db.Database())
	var key []byte
	logsMust(t, db.Database().QueryRow("SELECT secret FROM aws_logs_settings WHERE id=1").Scan(&key))
	_, err = db.Database().Exec("PRAGMA ignore_check_constraints=ON")
	logsMust(t, err)
	_, err = db.Database().Exec("UPDATE aws_logs_settings SET secret=x'0102' WHERE id=1")
	logsMust(t, err)
	reopened, err := NewCloudWatchLogsStore(db.Database())
	if err == nil || reopened != nil {
		t.Fatal("corrupt persisted cursor authority accepted")
	}
	var retained []byte
	logsMust(t, db.Database().QueryRow("SELECT secret FROM aws_logs_settings WHERE id=1").Scan(&retained))
	if len(retained) != 2 || logsGraphSnapshot(t, db.Database()) != original {
		t.Fatal("constructor silently replaced key or discarded graph")
	}
	_, err = db.Database().Exec("UPDATE aws_logs_settings SET secret=? WHERE id=1", key)
	logsMust(t, err)
	_, err = db.Database().Exec("PRAGMA ignore_check_constraints=OFF")
	logsMust(t, err)
	reopened, err = NewCloudWatchLogsStore(db.Database())
	logsMust(t, err)
	reopened.now = s.now
	next, err := reopened.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{StartFromHead: true, Limit: 1, Token: page.Forward})
	logsMust(t, err)
	if len(next.Events) != 1 || next.Events[0].Message != "second" {
		t.Fatal("repair lost original cursor authority or order")
	}
}

func TestLogsGlobalStreamQuotaPreservesReadableGraphAndAllowsCleanup(t *testing.T) {
	db, s := newLogsTest(t)
	ctx := context.Background()
	seedLogs(t, s, "group", "stream")
	seedLogs(t, s, "peer", "peer-stream")
	now := s.now().UnixMilli()
	_, err := s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now, "retained"}})
	logsMust(t, err)
	tx, err := db.Database().BeginTx(ctx, nil)
	logsMust(t, err)
	for i := 0; i < 998; i++ {
		_, err = tx.ExecContext(ctx, "INSERT INTO aws_logs_streams VALUES(?,?,?,?,?)", "us-east-1", "peer", fmt.Sprintf("quota-%04d", i), fmt.Sprintf("generation-%d", i), now)
		logsMust(t, err)
	}
	logsMust(t, tx.Commit())
	before := logsGraphSnapshot(t, db.Database())
	response := logsHTTP(s, "CreateLogStream", `{"logGroupName":"group","logStreamName":"rejected"}`, func(string, string) error { return nil })
	if response.Code != 400 || !strings.Contains(response.Body.String(), "LimitExceededException") {
		t.Fatal("stream cap not enforced", response.Code)
	}
	if logsGraphSnapshot(t, db.Database()) != before {
		t.Fatal("quota failure changed persisted graph")
	}
	page, err := s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{StartFromHead: true, Limit: 1})
	logsMust(t, err)
	if len(page.Events) != 1 || page.Events[0].Message != "retained" {
		t.Fatal("quota blocked or discarded existing events")
	}
	logsMust(t, s.DeleteGroup(ctx, "us-east-1", "peer"))
	logsMust(t, s.CreateStream(ctx, "us-east-1", "group", "rejected"))
	streams, _, err := s.Streams(ctx, "us-east-1", "group", LogsStreamsQuery{OrderBy: "LastEventTime", Limit: 50})
	logsMust(t, err)
	if len(streams) != 2 || streams[0].Name != "rejected" || streams[1].Name != "stream" {
		t.Fatal("last-event ordering or repaired admission incorrect", streams)
	}
	logsMust(t, s.DeleteGroup(ctx, "us-east-1", "group"))
	groups, _, err := s.Groups(ctx, "us-east-1", "", "", 50)
	logsMust(t, err)
	if len(groups) != 0 {
		t.Fatal("cleanup left quota graph")
	}
}

func TestLogsOversizeTransportAndMissingStreamDoNotMutateExistingGraph(t *testing.T) {
	db, s := newLogsTest(t)
	ctx := context.Background()
	seedLogs(t, s, "group", "stream")
	now := s.now().UnixMilli()
	_, err := s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now, "retained"}})
	logsMust(t, err)
	before := logsGraphSnapshot(t, db.Database())
	allow := func(string, string) error { return nil }
	oversized := `{"logGroupName":"` + strings.Repeat("x", 8<<20) + `"}`
	response := logsHTTP(s, "CreateLogGroup", oversized, allow)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "InvalidParameterException") {
		t.Fatal("oversize transport accepted", response.Code)
	}
	response = logsHTTP(s, "PutLogEvents", fmt.Sprintf(`{"logGroupName":"group","logStreamName":"absent","logEvents":[{"timestamp":%d,"message":"not-stored"}]}`, now), allow)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "ResourceNotFoundException") {
		t.Fatal("absent stream implicitly created", response.Code)
	}
	if logsGraphSnapshot(t, db.Database()) != before {
		t.Fatal("transport/missing-stream rejection changed graph")
	}
	logsMust(t, s.CreateStream(ctx, "us-east-1", "group", "absent"))
	response = logsHTTP(s, "PutLogEvents", fmt.Sprintf(`{"logGroupName":"group","logStreamName":"absent","logEvents":[{"timestamp":%d,"message":"not-stored"}]}`, now), allow)
	if response.Code != 200 {
		t.Fatal("explicit stream creation did not repair request", response.Code)
	}
	page, err := s.Events(ctx, "us-east-1", "group", "absent", LogsEventsQuery{StartFromHead: true, Limit: 1})
	logsMust(t, err)
	if len(page.Events) != 1 || page.Events[0].Message != "not-stored" {
		t.Fatal("repaired request failed actual persistence")
	}
}
