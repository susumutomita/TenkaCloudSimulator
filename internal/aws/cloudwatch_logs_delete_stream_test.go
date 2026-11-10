package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestLogsDeleteStreamPreservesPeersAndRotatesCursorAfterRestart(t *testing.T) {
	ctx := context.Background()
	db, s := newLogsTest(t)
	seedLogs(t, s, "group", "target")
	logsMust(t, s.CreateStream(ctx, "us-east-1", "group", "peer"))
	seedLogs(t, s, "other", "target")
	logsMust(t, s.CreateGroup(ctx, "us-west-2", "group"))
	logsMust(t, s.CreateStream(ctx, "us-west-2", "group", "target"))
	for _, entry := range []struct{ region, group, stream string }{{"us-east-1", "group", "target"}, {"us-east-1", "group", "peer"}, {"us-east-1", "other", "target"}, {"us-west-2", "group", "target"}} {
		_, err := s.PutEvents(ctx, entry.region, entry.group, entry.stream, []LogsInputEvent{{s.now().UnixMilli(), "first"}, {s.now().UnixMilli() + 1, "second"}})
		logsMust(t, err)
	}
	original, err := s.Events(ctx, "us-east-1", "group", "target", LogsEventsQuery{StartFromHead: true, Limit: 1})
	logsMust(t, err)
	logsMust(t, s.DeleteStream(ctx, "us-east-1", "group", "target"))
	groups, _, err := s.Groups(ctx, "us-east-1", "", "", 50)
	logsMust(t, err)
	if len(groups) != 2 || groups[0].Bytes != int64(len("firstsecond")) {
		t.Fatal("stream deletion removed group or retained byte usage", groups)
	}
	streams, _, err := s.Streams(ctx, "us-east-1", "group", LogsStreamsQuery{Limit: 50})
	logsMust(t, err)
	if len(streams) != 1 || streams[0].Name != "peer" {
		t.Fatal("sibling changed", streams)
	}
	if err = s.DeleteStream(ctx, "us-east-1", "group", "target"); logsCode(err) != "ResourceNotFoundException" {
		t.Fatal("repeat delete", err)
	}
	if _, err = s.Events(ctx, "us-east-1", "group", "target", LogsEventsQuery{Limit: 1}); logsCode(err) != "ResourceNotFoundException" {
		t.Fatal("deleted events readable", err)
	}
	var path string
	logsMust(t, db.Database().QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path))
	now := s.now()
	logsMust(t, db.Close())
	reopened, err := core.New(path)
	logsMust(t, err)
	defer reopened.Close()
	s, err = NewCloudWatchLogsStore(reopened.Database())
	logsMust(t, err)
	s.now = func() time.Time { return now }
	if err = s.DeleteStream(ctx, "us-east-1", "group", "target"); logsCode(err) != "ResourceNotFoundException" {
		t.Fatal("delete not durable", err)
	}
	for _, entry := range []struct{ region, group, stream string }{{"us-east-1", "group", "peer"}, {"us-east-1", "other", "target"}, {"us-west-2", "group", "target"}} {
		page, err := s.Events(ctx, entry.region, entry.group, entry.stream, LogsEventsQuery{StartFromHead: true, Limit: 10})
		logsMust(t, err)
		if len(page.Events) != 2 || page.Events[0].Message != "first" {
			t.Fatal("peer lost events", entry, page)
		}
	}
	logsMust(t, s.CreateStream(ctx, "us-east-1", "group", "target"))
	if _, err = s.Events(ctx, "us-east-1", "group", "target", LogsEventsQuery{StartFromHead: true, Limit: 1, Token: original.Forward}); logsCode(err) != "InvalidParameterException" {
		t.Fatal("old stream cursor accepted", err)
	}
	page, err := s.Events(ctx, "us-east-1", "group", "target", LogsEventsQuery{Limit: 10})
	logsMust(t, err)
	if len(page.Events) != 0 {
		t.Fatal("old events adopted", page)
	}
}

func TestLogsDeleteStreamNativeIAMAndStrictWire(t *testing.T) {
	db, s := newLogsTest(t)
	seedLogs(t, s, "group", "target")
	ids := core.NewIdentities(db)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"logs:DeleteLogStream","Resource":"arn:aws:logs:us-east-1:000000000000:log-group:group:log-stream:target"}]}`))
	logsMust(t, err)
	logsMust(t, ids.Set("deleter", policy))
	calls := 0
	auth := func(action, resource string) error {
		calls++
		if action != "logs:DeleteLogStream" {
			t.Fatal(action)
		}
		decision, err := ids.DecisionContext(context.Background(), "deleter", action, resource)
		if err != nil {
			return err
		}
		if decision != "allowed" {
			return ssmFailure{"AccessDeniedException", decision}
		}
		return nil
	}
	for _, body := range []string{`{}`, `{"logGroupName":"group"}`, `{"logGroupName":"group","logStreamName":""}`, `{"logGroupName":"group","logStreamName":"bad:*"}`, `{"logGroupName":"group","logStreamName":null}`, `{"logGroupName":"group","logStreamName":1}`, `{"logGroupName":"group","logStreamName":"target","extra":true}`, `{"logGroupName":"group","logStreamName":"target","logStreamName":"peer"}`} {
		w := logsHTTP(s, "DeleteLogStream", body, auth)
		if w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	if calls != 0 {
		t.Fatal("malformed request authorized", calls)
	}
	w := logsHTTP(s, "DeleteLogStream", `{"logGroupName":"group","logStreamName":"peer"}`, auth)
	if w.Code != 403 {
		t.Fatal("wrong ARN not denied before existence", w.Code, w.Body.String())
	}
	snapshot := logsGraphSnapshot(t, db.Database())
	w = logsHTTP(s, "DeleteLogStream", `{"logGroupName":"group","logStreamName":"target"}`, func(string, string) error { return logsFailure{"AccessDeniedException", "explicitDeny"} })
	if w.Code != 403 || logsGraphSnapshot(t, db.Database()) != snapshot {
		t.Fatal("denied mutation", w.Code)
	}
	w = logsHTTP(s, "DeleteLogStream", `{"logGroupName":"group","logStreamName":"target"}`, auth)
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "application/x-amz-json-1.1" {
		t.Fatal("native delete success", w.Code, w.Body.String())
	}
	w = logsHTTP(s, "DeleteLogStream", `{"logGroupName":"group","logStreamName":"target"}`, auth)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "ResourceNotFoundException") {
		t.Fatal("missing stream", w.Code, w.Body.String())
	}
}

func TestLogsDeleteStreamFailureRollbackAndSanitization(t *testing.T) {
	for _, fixture := range []struct{ name, damage, repair string }{
		{"ignored-stream", `CREATE TRIGGER block_delete BEFORE DELETE ON aws_logs_streams BEGIN SELECT RAISE(IGNORE); END`, `DROP TRIGGER block_delete`},
		{"recreated-stream", `CREATE TRIGGER block_delete AFTER DELETE ON aws_logs_streams BEGIN INSERT INTO aws_logs_streams VALUES(OLD.region,OLD.group_name,OLD.name,'new-generation',OLD.created); END`, `DROP TRIGGER block_delete`},
		{"missing-event-table", `ALTER TABLE aws_logs_events RENAME TO private_events`, `ALTER TABLE private_events RENAME TO aws_logs_events`},
		{"aborted-event", `CREATE TRIGGER block_delete BEFORE DELETE ON aws_logs_events BEGIN SELECT RAISE(ABORT,'private-fault'); END`, `DROP TRIGGER block_delete`},
		{"ignored-event", `CREATE TRIGGER block_delete BEFORE DELETE ON aws_logs_events BEGIN SELECT RAISE(IGNORE); END`, `DROP TRIGGER block_delete`},
		{"cascade-disabled", `PRAGMA foreign_keys=OFF`, `PRAGMA foreign_keys=ON`},
		{"missing-stream-table", `ALTER TABLE aws_logs_streams RENAME TO private_streams`, `ALTER TABLE private_streams RENAME TO aws_logs_streams`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			db, s := newLogsTest(t)
			seedLogs(t, s, "group", "target")
			seedLogs(t, s, "peer", "peer")
			_, err := s.PutEvents(context.Background(), "us-east-1", "group", "target", []LogsInputEvent{{s.now().UnixMilli(), "private-event"}})
			logsMust(t, err)
			original := logsGraphSnapshot(t, db.Database())
			_, err = db.Database().Exec(fixture.damage)
			logsMust(t, err)
			w := logsHTTP(s, "DeleteLogStream", `{"logGroupName":"group","logStreamName":"target"}`, func(string, string) error { return nil })
			var body map[string]any
			logsMust(t, json.Unmarshal(w.Body.Bytes(), &body))
			if w.Code != 500 || body["__type"] != "ServiceUnavailableException" || strings.Contains(w.Body.String(), "private") {
				t.Fatal("failed delete acknowledged or disclosed", w.Code, w.Body.String())
			}
			_, err = db.Database().Exec(fixture.repair)
			logsMust(t, err)
			if logsGraphSnapshot(t, db.Database()) != original {
				t.Fatal("failure changed original graph")
			}
			logsMust(t, s.DeleteStream(context.Background(), "us-east-1", "group", "target"))
		})
	}
}

func TestLogsDeleteStreamValidationCancellationAndQuotaRecovery(t *testing.T) {
	db, s := newLogsTest(t)
	seedLogs(t, s, "group", "target")
	ctx := context.Background()
	for _, entry := range []struct{ region, group, stream string }{{"moon-1", "group", "target"}, {"us-east-1", "bad space", "target"}, {"us-east-1", "group", strings.Repeat("x", 513)}, {"us-east-1", "group", "\x00"}} {
		if err := s.DeleteStream(ctx, entry.region, entry.group, entry.stream); logsCode(err) != "InvalidParameterException" {
			t.Fatal("invalid input", err)
		}
	}
	if err := s.DeleteStream(ctx, "us-east-1", "missing", "target"); logsCode(err) != "ResourceNotFoundException" {
		t.Fatal("missing parent", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	original := logsGraphSnapshot(t, db.Database())
	if err := s.DeleteStream(canceled, "us-east-1", "group", "target"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled delete", err)
	}
	if logsGraphSnapshot(t, db.Database()) != original {
		t.Fatal("canceled delete changed state")
	}
	tx, err := db.Database().BeginTx(ctx, nil)
	logsMust(t, err)
	for i := 1; i < 1000; i++ {
		_, err = tx.Exec("INSERT INTO aws_logs_streams VALUES(?,?,?,?,?)", "us-east-1", "group", fmt.Sprintf("quota-%d", i), fmt.Sprintf("gen-%d", i), s.now().UnixMilli())
		logsMust(t, err)
	}
	logsMust(t, tx.Commit())
	if err = s.CreateStream(ctx, "us-east-1", "group", "over-limit"); logsCode(err) != "LimitExceededException" {
		t.Fatal(err)
	}
	logsMust(t, s.DeleteStream(ctx, "us-east-1", "group", "target"))
	logsMust(t, s.CreateStream(ctx, "us-east-1", "group", "reclaimed"))
	logsMust(t, db.Close())
	if err = s.DeleteStream(ctx, "us-east-1", "group", "reclaimed"); err == nil {
		t.Fatal("closed database accepted delete")
	}
}
