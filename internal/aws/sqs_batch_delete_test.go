package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func sqsBatchClaim(t *testing.T, s *SQSStore, q SQSQueue, body string) SQSMessage {
	t.Helper()
	if _, err := s.Send(q.Region, q, body, nil, 0); err != nil {
		t.Fatal(err)
	}
	messages, err := s.Receive(q.Region, q, 1, 3600)
	if err != nil || len(messages) != 1 || messages[0].Body != body {
		t.Fatal("claim fixture", messages, err)
	}
	return messages[0]
}

func sqsBatchBody(t *testing.T, queue string, entries []SQSDeleteMessageBatchEntry) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"QueueUrl": queue, "Entries": entries})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func sqsBatchExpired(t *testing.T, s *SQSStore, q SQSQueue) {
	t.Helper()
	if _, err := s.db.Exec("INSERT INTO aws_sqs_messages VALUES('expired',?,?,'expired','null',0,0,1,'expired-receipt')", q.Region, q.Name); err != nil {
		t.Fatal(err)
	}
}

func sqsBatchUnchanged(t *testing.T, s *SQSStore, q SQSQueue, before []sqsAdditionalRow) {
	t.Helper()
	afterQueue, err := s.GetQueue(q.Region, q.Name)
	if err != nil || afterQueue != q || !reflect.DeepEqual(before, sqsAdditionalRows(t, s)) {
		t.Fatal("batch failure changed the persisted queue or messages", afterQueue, err)
	}
}

func sqsBatchWireResult(t *testing.T, g *Gateway, body string) SQSDeleteMessageBatchResult {
	t.Helper()
	w := httptest.NewRecorder()
	g.ServeHTTP(w, sqsAdditionalRequest("POST", "/", "DeleteMessageBatch", body))
	var result SQSDeleteMessageBatchResult
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/x-amz-json-1.0" || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal("batch wire response", w.Code, w.Body.String())
	}
	if result.Successful == nil || result.Failed == nil {
		t.Fatal("batch response partitions must be arrays, never null/omitted", w.Body.String())
	}
	return result
}

func sqsBatchFailure(t *testing.T, g *Gateway, body string) {
	t.Helper()
	out := sqsAdditionalResponse(t, g, sqsAdditionalRequest("POST", "/", "DeleteMessageBatch", body), 500, "InternalError")
	if !reflect.DeepEqual(out, map[string]any{"__type": "InternalError", "Message": "Simulator queue state operation failed."}) {
		t.Fatal("infrastructure failure leaked internal data or partial results", out)
	}
}

func TestSQSDeleteMessageBatchMixedReceiptsAndOrderedPartitions(t *testing.T) {
	g, s, q := sqsAdditionalFixture(t)
	first := sqsBatchClaim(t, s, q, "first")
	rotated := sqsBatchClaim(t, s, q, "rotated")
	if err := s.ChangeVisibility(q.Region, q.Name, rotated.ReceiptHandle, 0); err != nil {
		t.Fatal(err)
	}
	current, err := s.Receive(q.Region, q, 1, 3600)
	if err != nil || len(current) != 1 || current[0].ReceiptHandle == rotated.ReceiptHandle {
		t.Fatal(current, err)
	}
	peer, err := s.CreateQueue(q.Region, SQSQueue{Name: "peer", RetentionSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	peerMessage := sqsBatchClaim(t, s, peer, "peer sentinel")
	sqsBatchExpired(t, s, q)
	entries := []SQSDeleteMessageBatchEntry{
		{"stale", rotated.ReceiptHandle}, {"first", first.ReceiptHandle},
		{"duplicate-handle", first.ReceiptHandle}, {"peer", peerMessage.ReceiptHandle},
		{"second", current[0].ReceiptHandle}, {"expired", "expired-receipt"}, {"unknown", "unknown"},
	}
	result := sqsBatchWireResult(t, g, sqsBatchBody(t, q.Name, entries))
	if !reflect.DeepEqual(result.Successful, []SQSDeleteMessageBatchSuccess{{"first"}, {"second"}}) || len(result.Failed) != 5 {
		t.Fatal("wrong result partition", result)
	}
	for i, id := range []string{"stale", "duplicate-handle", "peer", "expired", "unknown"} {
		if result.Failed[i] != (SQSDeleteMessageBatchFailure{id, "ReceiptHandleIsInvalid", "The input receipt handle is invalid.", true}) {
			t.Fatal("wrong per-entry failure", result.Failed[i])
		}
	}
	remaining := sqsAdditionalRows(t, s)
	if len(remaining) != 1 || remaining[0].ID != peerMessage.ID || remaining[0].Receipt != peerMessage.ReceiptHandle {
		t.Fatal("wrong persisted survivors", remaining)
	}
	// The exact successful request is no longer successful on retry.
	retry := sqsBatchWireResult(t, g, sqsBatchBody(t, q.Name, entries))
	if len(retry.Successful) != 0 || len(retry.Failed) != len(entries) {
		t.Fatal("stale receipts acknowledged on retry", retry)
	}
}

func TestSQSDeleteMessageBatchBoundaryValidationPrecedesMutation(t *testing.T) {
	g, s, q := sqsAdditionalFixture(t)
	m := sqsBatchClaim(t, s, q, "preserved")
	sqsBatchExpired(t, s, q)
	before := sqsAdditionalRows(t, s)
	valid := fmt.Sprintf(`{"Id":"valid","ReceiptHandle":%q}`, m.ReceiptHandle)
	for _, tc := range []struct{ name, body, code string }{
		{"empty-object", `{}`, "InvalidParameterValue"},
		{"missing-queue", `{"Entries":[` + valid + `]}`, "InvalidParameterValue"},
		{"empty-queue", `{"QueueUrl":"","Entries":[` + valid + `]}`, "InvalidParameterValue"},
		{"invalid-queue", `{"QueueUrl":"bad name","Entries":[` + valid + `]}`, "InvalidParameterValue"},
		{"fifo-queue", `{"QueueUrl":"name.fifo","Entries":[` + valid + `]}`, "InvalidParameterValue"},
		{"missing-entries", `{"QueueUrl":"boundary"}`, "InvalidParameterValue"},
		{"empty-entries", `{"QueueUrl":"boundary","Entries":[]}`, "EmptyBatchRequest"},
		{"null-entries", `{"QueueUrl":"boundary","Entries":null}`, "InvalidParameterValue"},
		{"object-entries", `{"QueueUrl":"boundary","Entries":{}}`, "InvalidParameterValue"},
		{"string-entries", `{"QueueUrl":"boundary","Entries":"entries"}`, "InvalidParameterValue"},
		{"root-array", `[]`, "InvalidParameterValue"},
		{"root-null", `null`, "InvalidParameterValue"},
		{"root-number", `1`, "InvalidParameterValue"},
		{"malformed", `{"QueueUrl":`, "InvalidParameterValue"},
		{"duplicate-queue", `{"QueueUrl":"peer","QueueUrl":"boundary","Entries":[` + valid + `]}`, "InvalidParameterValue"},
		{"duplicate-entries", `{"QueueUrl":"boundary","Entries":[],"Entries":[` + valid + `]}`, "InvalidParameterValue"},
		{"escaped-duplicate", `{"QueueUrl":"boundary","Queue\u0055rl":"peer","Entries":[` + valid + `]}`, "InvalidParameterValue"},
		{"case-queue", `{"queueurl":"boundary","Entries":[` + valid + `]}`, "InvalidParameterValue"},
		{"unknown-root", `{"QueueUrl":"boundary","Entries":[` + valid + `],"Other":1}`, "InvalidParameterValue"},
		{"null-queue", `{"QueueUrl":null,"Entries":[` + valid + `]}`, "InvalidParameterValue"},
		{"number-queue", `{"QueueUrl":7,"Entries":[` + valid + `]}`, "InvalidParameterValue"},
		{"trailing-value", `{"QueueUrl":"boundary","Entries":[` + valid + `]} {}`, "InvalidParameterValue"},
		{"trailing-garbage", `{"QueueUrl":"boundary","Entries":[` + valid + `]} broken`, "InvalidParameterValue"},
		{"incomplete-object", `{"QueueUrl":"boundary","Entries":[` + valid + `]`, "InvalidParameterValue"},
		{"bad-queue-unicode", `{"QueueUrl":"\ud800","Entries":[` + valid + `]}`, "InvalidParameterValue"},
		{"invalid-utf8", "{\"QueueUrl\":\"\xff\",\"Entries\":[" + valid + "]}", "InvalidParameterValue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqsAdditionalResponse(t, g, sqsAdditionalRequest("POST", "/", "DeleteMessageBatch", tc.body), 400, tc.code)
			sqsBatchUnchanged(t, s, q, before)
		})
	}
	for _, tc := range []struct{ name, entry, code string }{
		{"null", `null`, "InvalidParameterValue"},
		{"array", `[]`, "InvalidParameterValue"},
		{"number", `1`, "InvalidParameterValue"},
		{"missing-id", `{"ReceiptHandle":"x"}`, "InvalidBatchEntryId"},
		{"empty-id", `{"Id":"","ReceiptHandle":"x"}`, "InvalidBatchEntryId"},
		{"long-id", `{"Id":"` + strings.Repeat("x", 81) + `","ReceiptHandle":"x"}`, "InvalidBatchEntryId"},
		{"punctuation-id", `{"Id":"bad.id","ReceiptHandle":"x"}`, "InvalidBatchEntryId"},
		{"unicode-id", `{"Id":"日本語","ReceiptHandle":"x"}`, "InvalidBatchEntryId"},
		{"space-id", `{"Id":" bad ","ReceiptHandle":"x"}`, "InvalidBatchEntryId"},
		{"newline-id", `{"Id":"bad\n","ReceiptHandle":"x"}`, "InvalidBatchEntryId"},
		{"null-id", `{"Id":null,"ReceiptHandle":"x"}`, "InvalidParameterValue"},
		{"number-id", `{"Id":7,"ReceiptHandle":"x"}`, "InvalidParameterValue"},
		{"bad-unicode-id", `{"Id":"\ud800","ReceiptHandle":"x"}`, "InvalidParameterValue"},
		{"duplicate-id", `{"Id":"one","Id":"two","ReceiptHandle":"x"}`, "InvalidParameterValue"},
		{"case-id", `{"id":"wrong","ReceiptHandle":"x"}`, "InvalidParameterValue"},
		{"missing-receipt", `{"Id":"one"}`, "InvalidParameterValue"},
		{"empty-receipt", `{"Id":"one","ReceiptHandle":""}`, "InvalidParameterValue"},
		{"null-receipt", `{"Id":"one","ReceiptHandle":null}`, "InvalidParameterValue"},
		{"number-receipt", `{"Id":"one","ReceiptHandle":1}`, "InvalidParameterValue"},
		{"bad-unicode-receipt", `{"Id":"one","ReceiptHandle":"\ud800"}`, "InvalidParameterValue"},
		{"duplicate-receipt", `{"Id":"one","ReceiptHandle":"x","ReceiptHandle":"y"}`, "InvalidParameterValue"},
		{"unknown-field", `{"Id":"one","ReceiptHandle":"x","Other":"y"}`, "InvalidParameterValue"},
		{"duplicate-entry-id", valid, "BatchEntryIdsNotDistinct"},
	} {
		t.Run("entry-"+tc.name, func(t *testing.T) {
			body := `{"QueueUrl":"boundary","Entries":[` + valid + `,` + tc.entry + `]}`
			sqsAdditionalResponse(t, g, sqsAdditionalRequest("POST", "/", "DeleteMessageBatch", body), 400, tc.code)
			sqsBatchUnchanged(t, s, q, before)
		})
	}
	entries := []SQSDeleteMessageBatchEntry{{"valid", m.ReceiptHandle}}
	for i := 1; i < 11; i++ {
		entries = append(entries, SQSDeleteMessageBatchEntry{fmt.Sprint(i), "unknown"})
	}
	sqsAdditionalResponse(t, g, sqsAdditionalRequest("POST", "/", "DeleteMessageBatch", sqsBatchBody(t, q.Name, entries)), 400, "TooManyEntriesInBatchRequest")
	sqsBatchUnchanged(t, s, q, before)
	// Exact limits: ten entries and an eighty-byte identifier. All allowed ID
	// character classes appear, and one live target commits the scoped purge.
	entries = entries[:10]
	entries[0].Id = strings.Repeat("aA09_-", 13) + "xY"
	result := sqsBatchWireResult(t, g, sqsBatchBody(t, q.Name, entries))
	if len(result.Successful) != 1 || result.Successful[0].Id != entries[0].Id || len(result.Failed) != 9 || sqsSQLCount(t, s, "aws_sqs_messages") != 0 {
		t.Fatal("boundary batch rejected or mutated incorrectly", result)
	}
}

func TestSQSDeleteMessageBatchNativeValidationAndAllInvalidExpiry(t *testing.T) {
	s, q := sqsQueueFixture(t)
	m := sqsBatchClaim(t, s, q, "live")
	sqsBatchExpired(t, s, q)
	before := sqsAdditionalRows(t, s)
	for _, tc := range []struct {
		queue   string
		entries []SQSDeleteMessageBatchEntry
		code    string
	}{
		{"", []SQSDeleteMessageBatchEntry{{"a", "x"}}, "InvalidParameterValue"},
		{q.Name, nil, "EmptyBatchRequest"},
		{q.Name, make([]SQSDeleteMessageBatchEntry, 11), "TooManyEntriesInBatchRequest"},
		{q.Name, []SQSDeleteMessageBatchEntry{{"ok", m.ReceiptHandle}, {"", "x"}}, "InvalidBatchEntryId"},
		{q.Name, []SQSDeleteMessageBatchEntry{{"same", m.ReceiptHandle}, {"same", "x"}}, "BatchEntryIdsNotDistinct"},
		{q.Name, []SQSDeleteMessageBatchEntry{{"ok", m.ReceiptHandle}, {"missing", ""}}, "InvalidParameterValue"},
	} {
		out, err := s.DeleteMessageBatch(context.Background(), q.Region, tc.queue, tc.entries)
		sqsFailureCode(t, err, tc.code)
		if out.Successful != nil || out.Failed != nil {
			t.Fatal("invalid batch returned partial results", out)
		}
		sqsBatchUnchanged(t, s, q, before)
	}
	entries := []SQSDeleteMessageBatchEntry{{"expired", "expired-receipt"}, {"unknown", "unknown"}, {"repeat", "unknown"}}
	out, err := s.DeleteMessageBatch(context.Background(), q.Region, q.Name, entries)
	if err != nil || len(out.Successful) != 0 || len(out.Failed) != 3 || sqsSQLCount(t, s, "aws_sqs_messages") != 1 {
		t.Fatal("semantic failures must commit successful scoped expiry cleanup", out, err)
	}
	// DeleteMessage also accepts a still-current receipt after visibility elapses;
	// the batch API must retain this local behavior until actual redelivery.
	if err = s.ChangeVisibility(q.Region, q.Name, m.ReceiptHandle, 0); err != nil {
		t.Fatal(err)
	}
	out, err = s.DeleteMessageBatch(context.Background(), q.Region, q.Name, []SQSDeleteMessageBatchEntry{{"released", m.ReceiptHandle}})
	if err != nil || len(out.Successful) != 1 || len(out.Failed) != 0 {
		t.Fatal("current receipt after visibility release", out, err)
	}
}

func TestSQSDeleteMessageBatchTenSuccessesAndConcurrentRetry(t *testing.T) {
	s, q := sqsQueueFixture(t)
	for i := range 10 {
		if _, err := s.Send(q.Region, q, fmt.Sprint(i), nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := s.Receive(q.Region, q, 10, 30)
	if err != nil || len(messages) != 10 {
		t.Fatal(messages, err)
	}
	entries := make([]SQSDeleteMessageBatchEntry, len(messages))
	for i, message := range messages {
		entries[i] = SQSDeleteMessageBatchEntry{fmt.Sprint(i), message.ReceiptHandle}
	}
	start := make(chan struct{})
	type outcome struct {
		result SQSDeleteMessageBatchResult
		err    error
	}
	completed := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			result, err := s.DeleteMessageBatch(context.Background(), q.Region, q.Name, entries)
			completed <- outcome{result, err}
		}()
	}
	close(start)
	totalSuccess := 0
	for range 2 {
		out := <-completed
		if out.err != nil || out.result.Successful == nil || out.result.Failed == nil {
			t.Fatal(out)
		}
		if len(out.result.Successful) != 0 && len(out.result.Successful) != 10 {
			t.Fatal("concurrent requests split atomic batches", out)
		}
		if len(out.result.Successful)+len(out.result.Failed) != 10 {
			t.Fatal("not every entry received exactly one result", out)
		}
		for i, success := range out.result.Successful {
			if success.Id != entries[i].Id {
				t.Fatal("success ordering changed", out)
			}
		}
		totalSuccess += len(out.result.Successful)
	}
	if totalSuccess != 10 || sqsSQLCount(t, s, "aws_sqs_messages") != 0 {
		t.Fatal("concurrent receipts acknowledged more or less than once", totalSuccess)
	}
}

func TestSQSDeleteMessageBatchUsesDeleteMessageIAMAction(t *testing.T) {
	g, s, q := sqsAdditionalFixture(t)
	m := sqsBatchClaim(t, s, q, "protected")
	sqsBatchExpired(t, s, q)
	before := sqsAdditionalRows(t, s)
	arn := "arn:aws:sqs:us-east-1:000000000000:" + q.Name
	body := sqsBatchBody(t, q.Name, []SQSDeleteMessageBatchEntry{{"protected", m.ReceiptHandle}})
	for _, tc := range []struct{ name, action, resource, deny string }{
		{"no-grant", "sqs:ReceiveMessage", arn, ""},
		{"wrong-action", "sqs:DeleteMessageBatch", arn, ""},
		{"wrong-queue", "sqs:DeleteMessage", arn + "-other", ""},
		{"wrong-region", "sqs:DeleteMessage", strings.Replace(arn, "us-east-1", "us-west-2", 1), ""},
		{"explicit-deny", "sqs:*", "*", "sqs:DeleteMessage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{tc.action}, Resource: core.StringList{tc.resource}}}}
			if tc.deny != "" {
				p.Statement = append(p.Statement, core.Statement{Effect: "Deny", Action: core.StringList{tc.deny}, Resource: core.StringList{"*"}})
			}
			if err := g.Identities.Set("batch-user", p); err != nil {
				t.Fatal(err)
			}
			r := sqsAdditionalRequest("POST", "/", "DeleteMessageBatch", body)
			r.Header.Set("X-Simulator-Principal", "batch-user")
			sqsAdditionalResponse(t, g, r, 403, "AccessDenied")
			sqsBatchUnchanged(t, s, q, before)
		})
	}
	policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{
		{Effect: "Allow", Action: core.StringList{"sqs:DeleteMessage"}, Resource: core.StringList{arn}},
		{Effect: "Deny", Action: core.StringList{"sqs:DeleteMessageBatch"}, Resource: core.StringList{"*"}},
	}}
	if err := g.Identities.Set("batch-user", policy); err != nil {
		t.Fatal(err)
	}
	r := sqsAdditionalRequest("POST", "/", "DeleteMessageBatch", body)
	r.Header.Set("X-Simulator-Principal", "batch-user")
	out := sqsAdditionalResponse(t, g, r, 200, "")
	if len(out["Successful"].([]any)) != 1 || sqsSQLCount(t, s, "aws_sqs_messages") != 0 {
		t.Fatal("single-message grant did not authorize batch", out)
	}
}

func TestSQSDeleteMessageBatchIsolationRecreationAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "batch.db")
	db, s := sqsState(t, path)
	q, err := s.CreateQueue("us-east-1", SQSQueue{Name: "durable", RetentionSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	m := sqsBatchClaim(t, s, q, "durable")
	west, err := s.CreateQueue("us-west-2", SQSQueue{Name: q.Name, RetentionSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	wm := sqsBatchClaim(t, s, west, "west sentinel")
	sqsBatchExpired(t, s, west)
	peerDB, peer := sqsState(t, filepath.Join(t.TempDir(), "peer.db"))
	defer peerDB.Close()
	peerQ, err := peer.CreateQueue(q.Region, q)
	if err != nil {
		t.Fatal(err)
	}
	pm := sqsBatchClaim(t, peer, peerQ, "team sentinel")
	peerBefore := sqsAdditionalRows(t, peer)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, s = sqsState(t, path)
	defer db.Close()
	g := &Gateway{Store: db, Identities: core.NewIdentities(db), Queues: s}
	entries := []SQSDeleteMessageBatchEntry{{"west", wm.ReceiptHandle}, {"peer", pm.ReceiptHandle}, {"local", m.ReceiptHandle}}
	// QueueUrl host/region continue to be inert; the authenticated request region
	// selects the local queue and never causes an outbound request.
	result := sqsBatchWireResult(t, g, sqsBatchBody(t, "https://sqs.us-west-2.amazonaws.com/000000000000/"+q.Name, entries))
	if len(result.Successful) != 1 || result.Successful[0].Id != "local" || len(result.Failed) != 2 {
		t.Fatal("cross-scope receipt or restart behavior", result)
	}
	sqsBatchUnchanged(t, peer, peerQ, peerBefore)
	if sqsSQLCount(t, s, "aws_sqs_messages") != 2 {
		t.Fatal("batch cleaned a foreign region's live or expired rows")
	}
	if err = s.DeleteQueue(q.Region, q.Name); err != nil {
		t.Fatal(err)
	}
	body := sqsBatchBody(t, q.Name, []SQSDeleteMessageBatchEntry{{"old", m.ReceiptHandle}})
	sqsAdditionalResponse(t, g, sqsAdditionalRequest("POST", "/", "DeleteMessageBatch", body), 400, "AWS.SimpleQueueService.NonExistentQueue")
	replacement, err := s.CreateQueue(q.Region, q)
	if err != nil {
		t.Fatal(err)
	}
	rm := sqsBatchClaim(t, s, replacement, "replacement sentinel")
	before := sqsAdditionalRows(t, s)
	result = sqsBatchWireResult(t, g, body)
	if len(result.Successful) != 0 || len(result.Failed) != 1 {
		t.Fatal("old queue receipt affected replacement", result)
	}
	sqsBatchUnchanged(t, s, replacement, before)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, s = sqsState(t, path)
	defer db.Close()
	// Committed deletion is still absent after the second process restart.
	rows := sqsAdditionalRows(t, s)
	if len(rows) != 3 {
		t.Fatal(rows)
	}
	for _, row := range rows {
		if row.ID == m.ID || (row.Queue == q.Name && row.Region == q.Region && row.ID != rm.ID) {
			t.Fatal("deleted generation reappeared", row)
		}
	}
}

func TestSQSDeleteMessageBatchRechecksQueueAfterDelayedBegin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recreate.db")
	firstDB, first := sqsState(t, path)
	defer firstDB.Close()
	secondDB, second := sqsState(t, path)
	defer secondDB.Close()
	q, err := first.CreateQueue("us-east-1", SQSQueue{Name: "generation", RetentionSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	old := sqsBatchClaim(t, first, q, "old")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	snsSetAuthorizer(t, first.db, func(action sqlite3.AuthorizerActionCode, operation, _, _, _ string) sqlite3.AuthorizerReturnCode {
		if action == sqlite3.AUTH_TRANSACTION && strings.EqualFold(operation, "BEGIN") {
			once.Do(func() { close(entered); <-release })
		}
		return sqlite3.AUTH_OK
	})
	type outcome struct {
		result SQSDeleteMessageBatchResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		out, err := first.DeleteMessageBatch(context.Background(), q.Region, q.Name, []SQSDeleteMessageBatchEntry{{"old", old.ReceiptHandle}})
		done <- outcome{out, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not reach its transaction")
	}
	if err = second.DeleteQueue(q.Region, q.Name); err != nil {
		t.Fatal(err)
	}
	replacement, err := second.CreateQueue(q.Region, q)
	if err != nil {
		t.Fatal(err)
	}
	current := sqsBatchClaim(t, second, replacement, "replacement")
	before := sqsAdditionalRows(t, second)
	unblock()
	select {
	case out := <-done:
		if out.err != nil || len(out.result.Successful) != 0 || len(out.result.Failed) != 1 {
			t.Fatal("delayed stale receipt affected replacement", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not finish")
	}
	snsSetAuthorizer(t, first.db, nil)
	sqsBatchUnchanged(t, second, replacement, before)
	// This is a current-name operation, not an old HTTP queue-generation lease.
	out, err := first.DeleteMessageBatch(context.Background(), q.Region, q.Name, []SQSDeleteMessageBatchEntry{{"current", current.ReceiptHandle}})
	if err != nil || len(out.Successful) != 1 || len(out.Failed) != 0 {
		t.Fatal("current recreated-queue receipt was rejected", out, err)
	}
}

func TestSQSDeleteMessageBatchSQLiteFailuresRollBackEntireBatch(t *testing.T) {
	for _, mode := range []string{"purge-abort", "purge-ignore", "second-abort", "second-ignore", "duplicate-receipt", "resurrect-rotated", "resurrect-earlier", "delete-later", "queue-generation", "queue-missing", "late-expiry"} {
		t.Run(mode, func(t *testing.T) {
			g, s, q := sqsAdditionalFixture(t)
			first := sqsBatchClaim(t, s, q, "first")
			second := sqsBatchClaim(t, s, q, "second")
			sqsBatchExpired(t, s, q)
			setup := ""
			switch mode {
			case "purge-abort":
				setup = `CREATE TRIGGER batch_fault BEFORE DELETE ON aws_sqs_messages WHEN old.body='expired' BEGIN SELECT RAISE(ABORT,'private purge fault'); END`
			case "purge-ignore":
				setup = `CREATE TRIGGER batch_fault BEFORE DELETE ON aws_sqs_messages WHEN old.body='expired' BEGIN SELECT RAISE(IGNORE); END`
			case "second-abort":
				setup = `CREATE TRIGGER batch_fault BEFORE DELETE ON aws_sqs_messages WHEN old.body='second' BEGIN SELECT RAISE(ABORT,'private delete fault'); END`
			case "second-ignore":
				setup = `CREATE TRIGGER batch_fault BEFORE DELETE ON aws_sqs_messages WHEN old.body='second' BEGIN SELECT RAISE(IGNORE); END`
			case "duplicate-receipt":
				if _, err := s.db.Exec("UPDATE aws_sqs_messages SET receipt=? WHERE id=?", first.ReceiptHandle, second.ID); err != nil {
					t.Fatal(err)
				}
			case "resurrect-rotated":
				setup = `CREATE TRIGGER batch_fault AFTER DELETE ON aws_sqs_messages WHEN old.body='first' BEGIN INSERT INTO aws_sqs_messages VALUES(old.id,old.region,old.queue,old.body,old.attributes,old.sent,old.visible,old.receive_count,'rotated-by-trigger'); END`
			case "resurrect-earlier":
				setup = `CREATE TABLE batch_saved AS SELECT * FROM aws_sqs_messages WHERE body='first'; CREATE TRIGGER batch_fault AFTER DELETE ON aws_sqs_messages WHEN old.body='second' BEGIN INSERT INTO aws_sqs_messages SELECT * FROM batch_saved; END`
			case "delete-later":
				setup = `CREATE TRIGGER batch_fault AFTER DELETE ON aws_sqs_messages WHEN old.body='first' BEGIN DELETE FROM aws_sqs_messages WHERE body='second'; END`
			case "queue-generation":
				setup = `CREATE TRIGGER batch_fault AFTER DELETE ON aws_sqs_messages WHEN old.body='second' BEGIN UPDATE aws_sqs_queues SET created=created+1 WHERE region=old.region AND name=old.queue; END`
			case "queue-missing":
				setup = `CREATE TRIGGER batch_fault AFTER DELETE ON aws_sqs_messages WHEN old.body='second' BEGIN DELETE FROM aws_sqs_queues WHERE region=old.region AND name=old.queue; END`
			case "late-expiry":
				setup = `CREATE TRIGGER batch_fault AFTER DELETE ON aws_sqs_messages WHEN old.body='second' BEGIN INSERT INTO aws_sqs_messages VALUES('late-expired',old.region,old.queue,'late-expired','null',0,0,0,''); END`
			}
			if setup != "" {
				if _, err := s.db.Exec(setup); err != nil {
					t.Fatal(err)
				}
			}
			before := sqsAdditionalRows(t, s)
			entries := []SQSDeleteMessageBatchEntry{{"first", first.ReceiptHandle}, {"unknown", "unknown"}, {"second", second.ReceiptHandle}}
			sqsBatchFailure(t, g, sqsBatchBody(t, q.Name, entries))
			sqsBatchUnchanged(t, s, q, before)
			if mode == "duplicate-receipt" {
				if _, err := s.db.Exec("UPDATE aws_sqs_messages SET receipt=? WHERE id=?", second.ReceiptHandle, second.ID); err != nil {
					t.Fatal(err)
				}
			} else if _, err := s.db.Exec("DROP TRIGGER batch_fault"); err != nil {
				t.Fatal(err)
			}
			out := sqsBatchWireResult(t, g, sqsBatchBody(t, q.Name, entries))
			if len(out.Successful) != 2 || len(out.Failed) != 1 || sqsSQLCount(t, s, "aws_sqs_messages") != 0 {
				t.Fatal("repair did not restore atomic deletion", out)
			}
		})
	}
}

func TestSQSDeleteMessageBatchDeferredCommitFailure(t *testing.T) {
	g, s, q := sqsAdditionalFixture(t)
	first := sqsBatchClaim(t, s, q, "first")
	second := sqsBatchClaim(t, s, q, "second")
	sqsBatchExpired(t, s, q)
	if _, err := s.db.Exec(`CREATE TABLE batch_parent(id INTEGER PRIMARY KEY);
CREATE TABLE batch_child(parent INTEGER REFERENCES batch_parent(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TRIGGER batch_commit_fault AFTER DELETE ON aws_sqs_messages WHEN old.body='second' BEGIN INSERT INTO batch_child VALUES(1); END`); err != nil {
		t.Fatal(err)
	}
	before := sqsAdditionalRows(t, s)
	// Demonstrate that this fixture fails at COMMIT, not at the DELETE statement.
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("DELETE FROM aws_sqs_messages WHERE id=?", second.ID); err != nil {
		tx.Rollback()
		t.Fatal("fixture failed before commit", err)
	}
	if err = tx.Commit(); err == nil {
		t.Fatal("fixture did not fail at commit")
	}
	_ = tx.Rollback()
	sqsBatchUnchanged(t, s, q, before)
	body := sqsBatchBody(t, q.Name, []SQSDeleteMessageBatchEntry{{"first", first.ReceiptHandle}, {"second", second.ReceiptHandle}})
	sqsBatchFailure(t, g, body)
	sqsBatchUnchanged(t, s, q, before)
	if sqsSQLCount(t, s, "batch_child") != 0 {
		t.Fatal("deferred commit side effect escaped rollback")
	}
	if _, err = s.db.Exec("DROP TRIGGER batch_commit_fault"); err != nil {
		t.Fatal(err)
	}
	if out := sqsBatchWireResult(t, g, body); len(out.Successful) != 2 || len(out.Failed) != 0 {
		t.Fatal("commit repair", out)
	}
}

func TestSQSDeleteMessageBatchReadFaultsRemainInfrastructureFailures(t *testing.T) {
	// Enumerate each actual SQLite read authorization point, including the final
	// absence/queue/expiry checks after writes. An undenied probe closes the range.
	denials := 0
	for cutoff := 1; cutoff <= 80; cutoff++ {
		denied := false
		t.Run(fmt.Sprint(cutoff), func(t *testing.T) {
			g, s, q := sqsAdditionalFixture(t)
			m := sqsBatchClaim(t, s, q, "retained")
			sqsBatchExpired(t, s, q)
			before := sqsAdditionalRows(t, s)
			reads := 0
			snsSetAuthorizer(t, s.db, func(action sqlite3.AuthorizerActionCode, table, _, _, _ string) sqlite3.AuthorizerReturnCode {
				if action == sqlite3.AUTH_READ && strings.HasPrefix(table, "aws_sqs_") {
					reads++
					if reads == cutoff {
						denied = true
						return sqlite3.AUTH_DENY
					}
				}
				return sqlite3.AUTH_OK
			})
			w := httptest.NewRecorder()
			g.ServeHTTP(w, sqsAdditionalRequest("POST", "/", "DeleteMessageBatch", sqsBatchBody(t, q.Name, []SQSDeleteMessageBatchEntry{{"valid", m.ReceiptHandle}})))
			snsSetAuthorizer(t, s.db, nil)
			if denied {
				denials++
				if w.Code != 500 || strings.Contains(w.Body.String(), "Successful") || strings.Contains(w.Body.String(), "not authorized") {
					t.Fatal("read fault misreported", w.Code, w.Body.String())
				}
				sqsBatchUnchanged(t, s, q, before)
			} else if w.Code != 200 || sqsSQLCount(t, s, "aws_sqs_messages") != 0 {
				t.Fatal("control without read denial failed", w.Code, w.Body.String())
			}
		})
		if !denied {
			break
		}
	}
	if denials < 10 || denials == 80 {
		t.Fatal("read-fault sweep did not reach all real statements and its success control", denials)
	}
}

func TestSQSDeleteMessageBatchCancellationRollsBack(t *testing.T) {
	for _, phase := range []string{"before-begin", "after-purge", "after-first-delete"} {
		t.Run(phase, func(t *testing.T) {
			db, s := sqsState(t, filepath.Join(t.TempDir(), "cancel.db"))
			defer db.Close()
			q, err := s.CreateQueue("us-east-1", SQSQueue{Name: "cancel", RetentionSeconds: 60})
			if err != nil {
				t.Fatal(err)
			}
			first := sqsBatchClaim(t, s, q, "first")
			second := sqsBatchClaim(t, s, q, "second")
			sqsBatchExpired(t, s, q)
			before := sqsAdditionalRows(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deletes := 0
			if phase == "before-begin" {
				cancel()
			} else {
				conn, err := s.db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				err = conn.Raw(func(raw any) error {
					raw.(driver.Conn).Raw().UpdateHook(func(action sqlite3.AuthorizerActionCode, _, table string, _ int64) {
						if action == sqlite3.AUTH_DELETE && table == "aws_sqs_messages" {
							deletes++
							if phase == "after-purge" || deletes == 2 {
								cancel()
							}
						}
					})
					return nil
				})
				conn.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			out, err := s.DeleteMessageBatch(ctx, q.Region, q.Name, []SQSDeleteMessageBatchEntry{{"first", first.ReceiptHandle}, {"second", second.ReceiptHandle}})
			if err == nil || out.Successful != nil || out.Failed != nil || (phase != "before-begin" && deletes == 0) {
				t.Fatal("canceled batch acknowledged or fixture did not run", out, err, deletes)
			}
			var protocol sqsFailure
			if errors.As(err, &protocol) {
				t.Fatal("cancellation became a semantic receipt failure", err)
			}
			sqsBatchUnchanged(t, s, q, before)
			if phase == "before-begin" {
				g := &Gateway{Store: db, Identities: core.NewIdentities(db), Queues: s}
				r := sqsAdditionalRequest("POST", "/", "DeleteMessageBatch", sqsBatchBody(t, q.Name, []SQSDeleteMessageBatchEntry{{"first", first.ReceiptHandle}})).WithContext(ctx)
				sqsAdditionalResponse(t, g, r, 500, "InternalError")
				sqsBatchUnchanged(t, s, q, before)
			}
		})
	}
}

func TestSQSDeleteMessageBatchInterruptsInFlightPurge(t *testing.T) {
	db, s := sqsState(t, filepath.Join(t.TempDir(), "interrupt.db"))
	defer db.Close()
	q, err := s.CreateQueue("us-east-1", SQSQueue{Name: "interrupt", RetentionSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	m := sqsBatchClaim(t, s, q, "preserved")
	sqsBatchExpired(t, s, q)
	before := sqsAdditionalRows(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	active, entered, completed := false, false, false
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = conn.Raw(func(raw any) error {
		sqlite := raw.(driver.Conn).Raw()
		if err := sqlite.CreateFunction("batch_cancel", 0, sqlite3.INNOCUOUS, func(call sqlite3.Context, _ ...sqlite3.Value) {
			entered = true
			if active {
				cancel()
			}
			call.ResultInt(0)
		}); err != nil {
			return err
		}
		return sqlite.CreateFunction("batch_completed", 0, sqlite3.INNOCUOUS, func(call sqlite3.Context, _ ...sqlite3.Value) {
			completed = true
			call.ResultInt(0)
		})
	})
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Cancel inside the running DELETE, then execute enough bounded SQLite work
	// to reach its progress callback. The final marker must not run. A Background
	// Exec would reach it before the transaction's eventual cancellation rollback.
	if _, err = s.db.Exec(`CREATE TRIGGER batch_interrupt BEFORE DELETE ON aws_sqs_messages WHEN old.body='expired' BEGIN
SELECT batch_cancel();
SELECT sum(n) FROM (WITH RECURSIVE work(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM work WHERE n<10000) SELECT n FROM work);
SELECT batch_completed();
END`); err != nil {
		t.Fatal(err)
	}
	// First prove the same trigger completes when cancellation is not activated.
	probe, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = purgeExpiredTx(probe, q, sqsNow())
	_ = probe.Rollback()
	if err != nil || !entered || !completed {
		t.Fatal("in-flight purge fixture did not complete its control", entered, completed, err)
	}
	sqsBatchUnchanged(t, s, q, before)
	active, entered, completed = true, false, false
	out, err := s.DeleteMessageBatch(ctx, q.Region, q.Name, []SQSDeleteMessageBatchEntry{{"live", m.ReceiptHandle}})
	if err == nil || !entered || completed || ctx.Err() == nil || out.Successful != nil || out.Failed != nil {
		t.Fatal("cancellation did not interrupt the running purge", entered, completed, out, err)
	}
	sqsBatchUnchanged(t, s, q, before)
}

func TestSQSDeleteMessageBatchReleasesPhysicalStorageBudgets(t *testing.T) {
	for _, budget := range []string{"rows", "payload"} {
		t.Run(budget, func(t *testing.T) {
			s, q := sqsQueueFixture(t)
			if budget == "rows" {
				seedSQSBudgetRows(t, s, q, maxSQSMessages, "seed")
			} else {
				seedSQSBudgetRows(t, s, q, 256, strings.Repeat("x", 262144-4))
			}
			_, err := s.Send(q.Region, q, "blocked", nil, 0)
			sqsFailureCode(t, err, "SimulatorQuotaExceeded")
			messages, err := s.Receive(q.Region, q, 1, 30)
			if err != nil || len(messages) != 1 {
				t.Fatal(messages, err)
			}
			out, err := s.DeleteMessageBatch(context.Background(), q.Region, q.Name, []SQSDeleteMessageBatchEntry{{"release", messages[0].ReceiptHandle}})
			if err != nil || len(out.Successful) != 1 || len(out.Failed) != 0 {
				t.Fatal(out, err)
			}
			if _, err = s.Send(q.Region, q, "fits after batch delete", nil, 0); err != nil {
				t.Fatal("batch did not release actual storage usage", err)
			}
		})
	}
}
