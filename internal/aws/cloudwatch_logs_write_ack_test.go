package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Host-owned DB fault fixtures, not an HTTP-accessible SQL feature. Pure BEFORE
// INSERT RAISE(IGNORE) suppresses only the target insert: no target-row deletion
// and no undefined BEFORE UPDATE/DELETE behavior is involved.
func logsGraphSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	graph := map[string][][]any{}
	for _, table := range []string{"aws_logs_groups", "aws_logs_streams", "aws_logs_events"} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY 1,2,3")
		logsMust(t, err)
		columns, err := rows.Columns()
		logsMust(t, err)
		values := [][]any{}
		for rows.Next() {
			row := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range row {
				pointers[i] = &row[i]
			}
			logsMust(t, rows.Scan(pointers...))
			values = append(values, row)
		}
		logsMust(t, rows.Err())
		logsMust(t, rows.Close())
		graph[table] = values
	}
	data, err := json.Marshal(graph)
	logsMust(t, err)
	return string(data)
}

func TestLogsNativeIgnoredWritesRejectFakeSuccessAndPreserveGraph(t *testing.T) {
	for _, kind := range []string{"group", "stream", "second-event"} {
		t.Run(kind, func(t *testing.T) {
			db, store := newLogsTest(t)
			ctx := context.Background()
			now := store.now().UnixMilli()
			seedLogs(t, store, "kept", "kept-stream")
			_, err := store.PutEvents(ctx, "us-east-1", "kept", "kept-stream", []LogsInputEvent{{now - 1, "kept-value"}})
			logsMust(t, err)
			foreign := logsGraphSnapshot(t, db.Database())
			op, payload, trigger := "CreateLogGroup", `{"logGroupName":"target"}`, `CREATE TRIGGER ignore_logs_write BEFORE INSERT ON aws_logs_groups WHEN NEW.name='target' BEGIN SELECT RAISE(IGNORE); END`
			if kind != "group" {
				logsMust(t, store.CreateGroup(ctx, "us-east-1", "target"))
			}
			if kind == "stream" {
				op, payload = "CreateLogStream", `{"logGroupName":"target","logStreamName":"target-stream"}`
				trigger = `CREATE TRIGGER ignore_logs_write BEFORE INSERT ON aws_logs_streams WHEN NEW.group_name='target' AND NEW.name='target-stream' BEGIN SELECT RAISE(IGNORE); END`
			}
			if kind == "second-event" {
				logsMust(t, store.CreateStream(ctx, "us-east-1", "target", "target-stream"))
				_, err = store.PutEvents(ctx, "us-east-1", "target", "target-stream", []LogsInputEvent{{now - 1, "target-before"}})
				logsMust(t, err)
				op = "PutLogEvents"
				payload = fmt.Sprintf(`{"logGroupName":"target","logStreamName":"target-stream","logEvents":[{"timestamp":%d,"message":"must-roll-back"},{"timestamp":%d,"message":"ignore-second"}]}`, now, now+1)
				trigger = `CREATE TRIGGER ignore_logs_write BEFORE INSERT ON aws_logs_events WHEN NEW.group_name='target' AND NEW.message='ignore-second' BEGIN SELECT RAISE(IGNORE); END`
			}
			before := logsGraphSnapshot(t, db.Database())
			_, err = db.Database().Exec(trigger)
			logsMust(t, err)
			allow := func(string, string) error { return nil }
			response := logsHTTP(store, op, payload, allow)
			var body map[string]any
			parseErr := json.Unmarshal(response.Body.Bytes(), &body)
			if response.Code != http.StatusInternalServerError || parseErr != nil || body["__type"] != "ServiceUnavailableException" {
				t.Errorf("ignored %s write produced fake/incorrect response: status=%d", kind, response.Code)
			}
			if strings.Contains(response.Body.String(), "ignore_logs_write") || strings.Contains(response.Body.String(), "must-roll-back") {
				t.Error("infrastructure failure disclosed DB fixture or event data")
			}
			if after := logsGraphSnapshot(t, db.Database()); after != before {
				t.Errorf("ignored %s write retained partial rows or changed existing graph", kind)
			}
			_, err = db.Database().Exec("DROP TRIGGER ignore_logs_write")
			logsMust(t, err)
			retry := logsHTTP(store, op, payload, allow)
			if retry.Code != 200 {
				t.Fatalf("repaired %s retry failed: status=%d", kind, retry.Code)
			}
			if kind == "second-event" {
				page, err := store.Events(ctx, "us-east-1", "target", "target-stream", LogsEventsQuery{StartFromHead: true, Limit: 100})
				logsMust(t, err)
				if len(page.Events) != 3 || page.Events[0].Message != "target-before" || page.Events[1].Message != "must-roll-back" || page.Events[2].Message != "ignore-second" {
					t.Error("repaired batch did not contain exactly the original row plus two new rows")
				}
			} else if logsGraphSnapshot(t, db.Database()) == before {
				t.Error("repaired acknowledged creation did not create a real row")
			}
			logsMust(t, store.DeleteGroup(ctx, "us-east-1", "target"))
			if after := logsGraphSnapshot(t, db.Database()); after != foreign {
				t.Error("owned cleanup changed the kept group/stream/event graph")
			}
		})
	}
}

func TestLogsConstructorRequiresDatabase(t *testing.T) {
	store, err := NewCloudWatchLogsStore(nil)
	if store != nil || err == nil {
		t.Fatal("missing DB must return no store and a readable configuration error")
	}
}
