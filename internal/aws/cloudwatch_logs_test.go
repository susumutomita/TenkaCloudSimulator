package aws

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func newLogsTest(t *testing.T) (*core.Store, *CloudWatchLogsStore) {
	t.Helper()
	db, err := core.New(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewCloudWatchLogsStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.UnixMilli(1800000000000) }
	return db, store
}

func TestLogsByteBoundedPagesConcurrentPutsAndPrivateLimits(t *testing.T) {
	ctx := context.Background()
	db, s := newLogsTest(t)
	seedLogs(t, s, "group", "stream")
	now := s.now().UnixMilli()
	large := strings.Repeat("x", 600000)
	_, err := s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now, large}})
	logsMust(t, err)
	_, err = s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now + 1, large}})
	logsMust(t, err)
	q := LogsEventsQuery{StartFromHead: true, Limit: 10000}
	page, err := s.Events(ctx, "us-east-1", "group", "stream", q)
	logsMust(t, err)
	if len(page.Events) != 1 {
		t.Fatal("page ignored byte ceiling", len(page.Events))
	}
	q.Token = page.Forward
	page, err = s.Events(ctx, "us-east-1", "group", "stream", q)
	logsMust(t, err)
	if len(page.Events) != 1 || page.Events[0].Timestamp != now+1 {
		t.Fatal("byte ceiling skipped an event", len(page.Events))
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now + 2, fmt.Sprintf("parallel-%d", i)}})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		logsMust(t, err)
	}
	q = LogsEventsQuery{StartFromHead: true, Limit: 10000, Token: page.Forward}
	page, err = s.Events(ctx, "us-east-1", "group", "stream", q)
	logsMust(t, err)
	if len(page.Events) != 4 {
		t.Fatal("parallel ingestion lost events", len(page.Events))
	}
	// Fill only resource metadata in one transaction to exercise the public quota
	// contract without allocating huge event bodies or changing production limits.
	tx, err := db.Database().BeginTx(ctx, nil)
	logsMust(t, err)
	for i := 1; i < 100; i++ {
		_, err = tx.ExecContext(ctx, "INSERT INTO aws_logs_groups VALUES(?,?,?,?)", "us-east-1", fmt.Sprintf("quota-%03d", i), fmt.Sprintf("generation-%d", i), now)
		logsMust(t, err)
	}
	logsMust(t, tx.Commit())
	if err = s.CreateGroup(ctx, "us-east-1", "over-quota"); logsCode(err) != "LimitExceededException" {
		t.Fatal(err)
	}
	if err = s.CreateGroup(ctx, "us-east-1", "group"); logsCode(err) != "ResourceAlreadyExistsException" {
		t.Fatal("quota hid existing resource", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = s.DeleteGroup(canceled, "us-east-1", "group"); err == nil {
		t.Fatal("canceled deletion executed")
	}
	page, err = s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{Limit: 1})
	logsMust(t, err)
	if len(page.Events) != 1 {
		t.Fatal("cancellation damaged existing state")
	}
}
func logsMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func logsCode(err error) string {
	var e logsFailure
	if errors.As(err, &e) {
		return e.code
	}
	return "infrastructure"
}
func seedLogs(t *testing.T, s *CloudWatchLogsStore, group, stream string) {
	t.Helper()
	logsMust(t, s.CreateGroup(context.Background(), "us-east-1", group))
	logsMust(t, s.CreateStream(context.Background(), "us-east-1", group, stream))
}

func TestLogsPersistenceRegionalOrderingAndStablePages(t *testing.T) {
	ctx := context.Background()
	db, s := newLogsTest(t)
	seedLogs(t, s, "/aws/demo", "alpha")
	now := s.now().UnixMilli()
	// Out-of-order requests and equal timestamps remain ordered by event time and
	// durable insertion identity, without rewriting original ingestion timestamps.
	_, err := s.PutEvents(ctx, "us-east-1", "/aws/demo", "alpha", []LogsInputEvent{{now, "third"}})
	logsMust(t, err)
	_, err = s.PutEvents(ctx, "us-east-1", "/aws/demo", "alpha", []LogsInputEvent{{now - 2, "first"}, {now - 2, "second"}, {now + 1, "fourth"}})
	logsMust(t, err)
	q := LogsEventsQuery{StartFromHead: true, Limit: 2}
	page, err := s.Events(ctx, "us-east-1", "/aws/demo", "alpha", q)
	logsMust(t, err)
	if len(page.Events) != 2 || page.Events[0].Message != "first" || page.Events[1].Message != "second" {
		t.Fatal(page)
	}
	firstToken := page.Forward
	// Reopen the actual file and verify cursor authority and saved ingest time.
	path := ""
	logsMust(t, db.Database().QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path))
	logsMust(t, db.Close())
	reopened, err := core.New(path)
	logsMust(t, err)
	defer reopened.Close()
	s, err = NewCloudWatchLogsStore(reopened.Database())
	logsMust(t, err)
	s.now = func() time.Time { return time.UnixMilli(now + 60000) }
	q.Token = firstToken
	page, err = s.Events(ctx, "us-east-1", "/aws/demo", "alpha", q)
	logsMust(t, err)
	if len(page.Events) != 2 || page.Events[0].Message != "third" || page.Events[1].Message != "fourth" || page.Events[0].IngestionTime != now {
		t.Fatal(page)
	}
	q.Token = page.Forward
	end, err := s.Events(ctx, "us-east-1", "/aws/demo", "alpha", q)
	logsMust(t, err)
	if len(end.Events) != 0 || end.Forward != q.Token {
		t.Fatal("forward end did not converge", end)
	}
	_, err = s.PutEvents(ctx, "us-east-1", "/aws/demo", "alpha", []LogsInputEvent{{now + 2, "appended"}})
	logsMust(t, err)
	again, err := s.Events(ctx, "us-east-1", "/aws/demo", "alpha", q)
	logsMust(t, err)
	if len(again.Events) != 1 || again.Events[0].Message != "appended" {
		t.Fatal(again)
	}
	back, err := s.Events(ctx, "us-east-1", "/aws/demo", "alpha", LogsEventsQuery{Limit: 2, Token: page.Backward})
	logsMust(t, err)
	if len(back.Events) != 2 || back.Events[0].Message != "first" {
		t.Fatal(back)
	}
	start, endTime := now, now+1
	filtered, err := s.Events(ctx, "us-east-1", "/aws/demo", "alpha", LogsEventsQuery{Start: &start, End: &endTime, StartFromHead: true, Limit: 10})
	logsMust(t, err)
	if len(filtered.Events) != 1 || filtered.Events[0].Message != "third" {
		t.Fatal("endTime must be exclusive", filtered)
	}
	if _, err = s.Events(ctx, "us-west-2", "/aws/demo", "alpha", q); logsCode(err) != "ResourceNotFoundException" {
		t.Fatal(err)
	}
}

func TestLogsTokensCannotCrossTeamQueryExpiryOrRecreatedResources(t *testing.T) {
	ctx := context.Background()
	_, s := newLogsTest(t)
	_, other := newLogsTest(t)
	seedLogs(t, s, "group", "stream")
	seedLogs(t, other, "group", "stream")
	now := s.now().UnixMilli()
	_, err := s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now, "one"}, {now + 1, "two"}})
	logsMust(t, err)
	q := LogsEventsQuery{StartFromHead: true, Limit: 1}
	page, err := s.Events(ctx, "us-east-1", "group", "stream", q)
	logsMust(t, err)
	q.Token = page.Forward
	if _, err = other.Events(ctx, "us-east-1", "group", "stream", q); logsCode(err) != "InvalidParameterException" {
		t.Fatal("cross-team cursor accepted", err)
	}
	changed := now
	modified := q
	modified.Start = &changed
	if _, err = s.Events(ctx, "us-east-1", "group", "stream", modified); logsCode(err) != "InvalidParameterException" {
		t.Fatal("query scope changed", err)
	}
	modified = q
	modified.StartFromHead = false
	if _, err = s.Events(ctx, "us-east-1", "group", "stream", modified); logsCode(err) != "InvalidParameterException" {
		t.Fatal("forward token used backwards", err)
	}
	s.now = func() time.Time { return time.UnixMilli(now).Add(24 * time.Hour) }
	if _, err = s.Events(ctx, "us-east-1", "group", "stream", q); logsCode(err) != "InvalidParameterException" {
		t.Fatal("expired token accepted", err)
	}
	s.now = func() time.Time { return time.UnixMilli(now) }
	logsMust(t, s.DeleteGroup(ctx, "us-east-1", "group"))
	seedLogs(t, s, "group", "stream")
	if _, err = s.Events(ctx, "us-east-1", "group", "stream", q); logsCode(err) != "InvalidParameterException" {
		t.Fatal("deleted generation reused", err)
	}
	empty, err := s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{Limit: 10})
	logsMust(t, err)
	if len(empty.Events) != 0 {
		t.Fatal(empty)
	}
	_, err = s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now, "after-empty"}})
	logsMust(t, err)
	after, err := s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{StartFromHead: true, Limit: 10, Token: empty.Forward})
	logsMust(t, err)
	if len(after.Events) != 1 {
		t.Fatal("empty latest-page forward token lost later writes", after)
	}
}

func TestLogsAdvancingPagesIssueNewExpiryWhileExhaustionPreservesToken(t *testing.T) {
	ctx := context.Background()
	_, s := newLogsTest(t)
	seedLogs(t, s, "group", "stream")
	now := s.now().UnixMilli()
	_, err := s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now, "first"}, {now + 1, "second"}, {now + 2, "third"}})
	logsMust(t, err)
	q := LogsEventsQuery{StartFromHead: true, Limit: 1}
	first, err := s.Events(ctx, "us-east-1", "group", "stream", q)
	logsMust(t, err)
	s.now = func() time.Time { return time.UnixMilli(now).Add(23 * time.Hour) }
	q.Token = first.Forward
	second, err := s.Events(ctx, "us-east-1", "group", "stream", q)
	logsMust(t, err)
	if len(second.Events) != 1 || second.Events[0].Message != "second" {
		t.Fatal(second)
	}
	s.now = func() time.Time { return time.UnixMilli(now).Add(25 * time.Hour) }
	if _, err = s.Events(ctx, "us-east-1", "group", "stream", q); logsCode(err) != "InvalidParameterException" {
		t.Fatal("old token did not expire", err)
	}
	q.Token = second.Forward
	third, err := s.Events(ctx, "us-east-1", "group", "stream", q)
	logsMust(t, err)
	if len(third.Events) != 1 || third.Events[0].Message != "third" {
		t.Fatal("progress inherited stale expiry", third)
	}
	s.now = func() time.Time { return time.UnixMilli(now).Add(26 * time.Hour) }
	q.Token = third.Forward
	end, err := s.Events(ctx, "us-east-1", "group", "stream", q)
	logsMust(t, err)
	if len(end.Events) != 0 || end.Forward != q.Token {
		t.Fatal("empty boundary refreshed token forever", end)
	}
}

func TestLogsReversingAtEmptyPageDoesNotSkipBoundaryEvents(t *testing.T) {
	ctx := context.Background()
	_, s := newLogsTest(t)
	seedLogs(t, s, "group", "stream")
	now := s.now().UnixMilli()
	_, err := s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now, "first"}, {now + 1, "middle"}, {now + 2, "last"}})
	logsMust(t, err)
	first, err := s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{StartFromHead: true, Limit: 1})
	logsMust(t, err)
	before, err := s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{Limit: 1, Token: first.Backward})
	logsMust(t, err)
	if len(before.Events) != 0 || before.Backward != first.Backward {
		t.Fatal("backward boundary failed to converge", before)
	}
	again, err := s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{StartFromHead: true, Limit: 1, Token: before.Forward})
	logsMust(t, err)
	if len(again.Events) != 1 || again.Events[0].Message != "first" {
		t.Fatal("rewinding skipped first event", again)
	}
	last, err := s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{Limit: 1})
	logsMust(t, err)
	after, err := s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{StartFromHead: true, Limit: 1, Token: last.Forward})
	logsMust(t, err)
	if len(after.Events) != 0 || after.Forward != last.Forward {
		t.Fatal("forward boundary failed to converge", after)
	}
	again, err = s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{Limit: 1, Token: after.Backward})
	logsMust(t, err)
	if len(again.Events) != 1 || again.Events[0].Message != "last" {
		t.Fatal("reversing skipped last event", again)
	}
}

func TestLogsIngestPartialTimeRejectionValidationAndAtomicFailures(t *testing.T) {
	ctx := context.Background()
	db, s := newLogsTest(t)
	seedLogs(t, s, "group", "stream")
	now := s.now().UnixMilli()
	day := int64(24 * time.Hour / time.Millisecond)
	rejected, err := s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now - 14*day - 1, "old"}, {now, "accepted"}, {now + int64(2*time.Hour/time.Millisecond) + 1, "future"}})
	logsMust(t, err)
	if rejected == nil || rejected.TooOld == nil || *rejected.TooOld != 0 || rejected.TooNew == nil || *rejected.TooNew != 2 {
		t.Fatal(rejected)
	}
	for name, batch := range map[string][]LogsInputEvent{"empty": {}, "order": {{now, "a"}, {now - 1, "b"}}, "span": {{now - 2*day, "a"}, {now, "b"}}, "negative": {{-1, "a"}}, "empty-message": {{now, ""}}, "size": {{now, strings.Repeat("x", 1048576)}}, "invalid-utf8": {{now, string([]byte{255})}}} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.PutEvents(ctx, "us-east-1", "group", "stream", batch); logsCode(err) != "InvalidParameterException" {
				t.Fatal(err)
			}
		})
	}
	logsMust(t, func() error {
		_, e := db.Database().Exec(`CREATE TRIGGER reject_second_logs_event BEFORE INSERT ON aws_logs_events WHEN NEW.message='fail' BEGIN SELECT RAISE(ABORT,'test event failure'); END`)
		return e
	}())
	if _, err = s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now, "must-rollback"}, {now, "fail"}}); err == nil {
		t.Fatal("partial ingestion reported success")
	}
	page, err := s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{StartFromHead: true, Limit: 100})
	logsMust(t, err)
	if len(page.Events) != 1 || page.Events[0].Message != "accepted" {
		t.Fatal("invalid writes modified state", page)
	}
	_, err = db.Database().Exec(`CREATE TRIGGER reject_logs_cascade BEFORE DELETE ON aws_logs_events BEGIN SELECT RAISE(ABORT,'test cascade failure'); END`)
	logsMust(t, err)
	if err = s.DeleteGroup(ctx, "us-east-1", "group"); err == nil {
		t.Fatal("cascade failure masked")
	}
	groups, _, err := s.Groups(ctx, "us-east-1", "", "", 50)
	logsMust(t, err)
	if len(groups) != 1 {
		t.Fatal("failed delete lost owner", groups)
	}
	_, err = db.Database().Exec("DROP TRIGGER reject_logs_cascade")
	logsMust(t, err)
	logsMust(t, s.DeleteGroup(ctx, "us-east-1", "group"))
	for _, table := range []string{"aws_logs_groups", "aws_logs_streams", "aws_logs_events"} {
		var n int
		logsMust(t, db.Database().QueryRow("SELECT count(*) FROM "+table).Scan(&n))
		if n != 0 {
			t.Fatal("orphan state", table, n)
		}
	}
}

func TestLogsGroupAndStreamPaginationMetadataAndRollbackSeam(t *testing.T) {
	ctx := context.Background()
	db, s := newLogsTest(t)
	for _, g := range []string{"prefix-b", "prefix-a", "other"} {
		logsMust(t, s.CreateGroup(ctx, "us-east-1", g))
	}
	groups, next, err := s.Groups(ctx, "us-east-1", "prefix-", "", 1)
	logsMust(t, err)
	if len(groups) != 1 || groups[0].Name != "prefix-a" || next == "" {
		t.Fatal(groups, next)
	}
	groups, next, err = s.Groups(ctx, "us-east-1", "prefix-", next, 1)
	logsMust(t, err)
	if len(groups) != 1 || groups[0].Name != "prefix-b" || next != "" {
		t.Fatal(groups, next)
	}
	for _, name := range []string{"z", "a", "b"} {
		logsMust(t, s.CreateStream(ctx, "us-east-1", "prefix-a", name))
	}
	now := s.now().UnixMilli()
	_, err = s.PutEvents(ctx, "us-east-1", "prefix-a", "b", []LogsInputEvent{{now, "message"}})
	logsMust(t, err)
	streams, next, err := s.Streams(ctx, "us-east-1", "prefix-a", LogsStreamsQuery{OrderBy: "LastEventTime", Descending: true, Limit: 1})
	logsMust(t, err)
	if len(streams) != 1 || streams[0].Name != "b" || streams[0].First == nil || *streams[0].First != now || next == "" {
		t.Fatal(streams, next)
	}
	streams, next, err = s.Streams(ctx, "us-east-1", "prefix-a", LogsStreamsQuery{OrderBy: "LastEventTime", Descending: true, Limit: 2, Token: next})
	logsMust(t, err)
	if len(streams) != 2 || streams[0].Name != "z" || streams[1].Name != "a" || next != "" {
		t.Fatal(streams, next)
	}
	tx, err := db.Database().BeginTx(ctx, nil)
	logsMust(t, err)
	logsMust(t, s.CreateGroupTx(ctx, tx, "us-east-1", "not-committed"))
	logsMust(t, s.DeleteGroupTx(ctx, tx, "us-east-1", "prefix-a"))
	logsMust(t, tx.Rollback())
	groups, _, err = s.Groups(ctx, "us-east-1", "", "", 50)
	logsMust(t, err)
	if len(groups) != 3 {
		t.Fatal("CFN transaction seam escaped rollback", groups)
	}
	page, err := s.Events(ctx, "us-east-1", "prefix-a", "b", LogsEventsQuery{Limit: 10})
	logsMust(t, err)
	if len(page.Events) != 1 {
		t.Fatal("rolled back teardown lost events", page)
	}
}
