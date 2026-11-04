package aws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sqsAdditionalFixture(t *testing.T) (*Gateway, *SQSStore, SQSQueue) {
	t.Helper()
	db, _, identities := lambdaState(t)
	store, err := NewSQSStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	q, err := store.CreateQueue("us-east-1", SQSQueue{Name: "boundary", VisibilityTimeout: 30, RetentionSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	return &Gateway{Store: db, Identities: identities, Queues: store}, store, q
}

type sqsAdditionalRow struct {
	Region, Queue, ID, Body, Attributes, Receives, Receipt string
	Sent, Visible                                          float64
}

// Preserve persisted values even when the test deliberately corrupts an integer
// column. Reading it as text avoids normalizing the evidence during comparison.
func sqsAdditionalRows(t *testing.T, store *SQSStore) []sqsAdditionalRow {
	t.Helper()
	rows, err := store.db.Query("SELECT region,queue,id,body,attributes,CAST(receive_count AS TEXT),receipt,sent,visible FROM aws_sqs_messages ORDER BY region,queue,id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []sqsAdditionalRow
	for rows.Next() {
		var row sqsAdditionalRow
		if err = rows.Scan(&row.Region, &row.Queue, &row.ID, &row.Body, &row.Attributes, &row.Receives, &row.Receipt, &row.Sent, &row.Visible); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func sqsAdditionalResponse(t *testing.T, gateway *Gateway, request *http.Request, status int, code string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, request)
	if w.Code != status {
		t.Fatalf("got status %d want %d: %s", w.Code, status, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if code != "" && body["__type"] != code {
		t.Fatalf("got error %v want %s", body, code)
	}
	return body
}

func sqsAdditionalRequest(method, target, op, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("X-Amz-Target", "AmazonSQS."+op)
	r.Header.Set("Content-Type", "application/x-amz-json-1.0")
	return r
}

func TestSQSAdditionalTransportAdmissionPreservesState(t *testing.T) {
	g, store, q := sqsAdditionalFixture(t)
	if _, err := store.Send(q.Region, q, "preserved", nil, -1); err != nil {
		t.Fatal(err)
	}
	before := sqsAdditionalRows(t, store)
	for _, tc := range []struct {
		name, method, target, body string
		headers                    map[string]string
		status                     int
		code                       string
	}{
		{"method", "GET", "/", `{}`, nil, 400, "InvalidParameterValue"},
		{"path", "POST", "/queue-path", `{}`, nil, 400, "InvalidParameterValue"},
		{"query", "POST", "/?Action=SendMessage", `{}`, nil, 400, "InvalidParameterValue"},
		{"missing-credential", "POST", "/", `{}`, map[string]string{"Authorization": "Bearer not-an-SQS-credential"}, 403, "AccessDenied"},
		{"real-credential", "POST", "/", `{}`, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/20261104/us-east-1/sqs/aws4_request"}, 403, "AccessDenied"},
		{"session-token", "POST", "/", `{}`, map[string]string{"X-Amz-Security-Token": "unsupported-dummy-session"}, 400, "InvalidParameterValue"},
		{"region-conflict", "POST", "/", `{}`, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261104/us-west-2/sqs/aws4_request", "X-Simulator-Region": "us-east-1"}, 400, "InvalidParameterValue"},
		{"unsupported-region", "POST", "/", `{}`, map[string]string{"X-Simulator-Region": "unimplemented-region"}, 400, "InvalidParameterValue"},
		{"oversized-envelope", "POST", "/", strings.Repeat(" ", (320<<10)+1), nil, 400, "InvalidParameterValue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := sqsAdditionalRequest(tc.method, tc.target, "SendMessage", tc.body)
			for key, value := range tc.headers {
				r.Header.Set(key, value)
			}
			sqsAdditionalResponse(t, g, r, tc.status, tc.code)
			if !reflect.DeepEqual(before, sqsAdditionalRows(t, store)) || sqsSQLCount(t, store, "aws_sqs_queues") != 1 {
				t.Fatal("rejected transport mutated queue graph")
			}
		})
	}
	got, err := store.Receive(q.Region, q, 1, 30)
	if err != nil || len(got) != 1 || got[0].Body != "preserved" || got[0].ReceiveCount != 1 {
		t.Fatal("admission refusals consumed the original message", got, err)
	}
}

func TestSQSAdditionalCredentialRegionHTTPSAndBareName(t *testing.T) {
	g, store, _ := sqsAdditionalFixture(t)
	r := sqsAdditionalRequest("POST", "https://local.example/", "CreateQueue", `{"QueueName":"regional"}`)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261104/us-west-2/sqs/aws4_request")
	r.Header.Set("X-Simulator-Region", "us-west-2")
	created := sqsAdditionalResponse(t, g, r, 200, "")
	if created["QueueUrl"] != "https://local.example/000000000000/regional" {
		t.Fatal("TLS origin was not used in native queue URL", created)
	}
	q, err := store.GetQueue("us-west-2", "regional")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.GetQueue("us-east-1", "regional")
	sqsFailureCode(t, err, "AWS.SimpleQueueService.NonExistentQueue")
	r = sqsAdditionalRequest("POST", "/", "SendMessage", `{"QueueUrl":"regional","MessageBody":"west-only"}`)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261104/us-west-2/sqs/aws4_request")
	sqsAdditionalResponse(t, g, r, 200, "")
	got, err := store.Receive(q.Region, q, 1, 30)
	if err != nil || len(got) != 1 || got[0].Body != "west-only" {
		t.Fatal("credential region or bounded bare-name alias was lost", got, err)
	}
}

func TestSQSAdditionalMissingAndInvalidQueueRefusals(t *testing.T) {
	g, store, q := sqsAdditionalFixture(t)
	if _, err := store.Send(q.Region, q, "retained", nil, -1); err != nil {
		t.Fatal(err)
	}
	before := sqsAdditionalRows(t, store)
	for _, tc := range []struct {
		op, body string
	}{
		{"GetQueueUrl", `{"QueueName":"missing"}`},
		{"DeleteQueue", `{"QueueUrl":"missing"}`},
		{"SendMessage", `{"QueueUrl":"missing","MessageBody":"must-not-exist"}`},
		{"ReceiveMessage", `{"QueueUrl":"missing"}`},
		{"DeleteMessage", `{"QueueUrl":"missing","ReceiptHandle":"unissued"}`},
		{"ChangeMessageVisibility", `{"QueueUrl":"missing","ReceiptHandle":"unissued","VisibilityTimeout":1}`},
		{"GetQueueAttributes", `{"QueueUrl":"missing","AttributeNames":["All"]}`},
	} {
		t.Run(tc.op, func(t *testing.T) {
			sqsAdditionalResponse(t, g, sqsAdditionalRequest("POST", "/", tc.op, tc.body), 400, "AWS.SimpleQueueService.NonExistentQueue")
			if !reflect.DeepEqual(before, sqsAdditionalRows(t, store)) || sqsSQLCount(t, store, "aws_sqs_queues") != 1 {
				t.Fatal("missing queue request changed live graph")
			}
		})
	}
	for _, op := range []string{"DeleteQueue", "ReceiveMessage"} {
		sqsAdditionalResponse(t, g, sqsAdditionalRequest("POST", "/", op, `{"QueueUrl":"bad name"}`), 400, "InvalidParameterValue")
	}
	if !reflect.DeepEqual(before, sqsAdditionalRows(t, store)) {
		t.Fatal("invalid queue name changed live messages")
	}
}

func TestSQSAdditionalMissingStoreAndCorruptPolicyFailClosed(t *testing.T) {
	// Library composition without the native store is a server fault, not a
	// caller InvalidParameterValue/400 and never a successful empty response.
	sqsAdditionalResponse(t, &Gateway{}, sqsAdditionalRequest("POST", "/", "ListQueues", `{}`), 500, "InternalError")
	g, store, q := sqsAdditionalFixture(t)
	if _, err := store.Send(q.Region, q, "original", nil, -1); err != nil {
		t.Fatal(err)
	}
	before := sqsAdditionalRows(t, store)
	if _, err := store.db.Exec("INSERT INTO identity_policies(name,document) VALUES(?,?)", "corrupt-policy", "private-corrupt-policy"); err != nil {
		t.Fatal(err)
	}
	r := sqsAdditionalRequest("POST", "/", "SendMessage", `{"QueueUrl":"boundary","MessageBody":"must-not-exist"}`)
	r.Header.Set("X-Simulator-Principal", "corrupt-policy")
	out := sqsAdditionalResponse(t, g, r, 500, "InternalError")
	if strings.Contains(out["Message"].(string), "private-corrupt-policy") || !reflect.DeepEqual(before, sqsAdditionalRows(t, store)) {
		t.Fatal("authorization storage failure leaked or mutated queue", out)
	}
}

func TestSQSAdditionalCorruptPersistedBatchRollsBackExpiryAndClaims(t *testing.T) {
	for _, tc := range []struct{ name, column, value string }{
		{"malformed-json", "attributes", `{"private-corrupt-json"`},
		{"invalid-binary", "attributes", `{"private":{"DataType":"Binary","BinaryValue":"not-base64"}}`},
		{"invalid-receive-count", "receive_count", "private-corrupt-count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, store, q := sqsAdditionalFixture(t)
			var ids []string
			for _, body := range []string{"first", "second", "expired"} {
				m, err := store.Send(q.Region, q, body, nil, -1)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, m.ID)
			}
			now := sqsNow()
			if _, err := store.db.Exec("UPDATE aws_sqs_messages SET sent=CASE id WHEN ? THEN ? WHEN ? THEN ? ELSE ? END", ids[0], now-2, ids[1], now-1, now-120); err != nil {
				t.Fatal(err)
			}
			// Only test-owned constant column names are interpolated, never input.
			if _, err := store.db.Exec("UPDATE aws_sqs_messages SET "+tc.column+"=? WHERE id=?", tc.value, ids[1]); err != nil {
				t.Fatal(err)
			}
			before := sqsAdditionalRows(t, store)
			out := sqsAdditionalResponse(t, g, sqsAdditionalRequest("POST", "/", "ReceiveMessage", `{"QueueUrl":"boundary","MaxNumberOfMessages":2}`), 500, "InternalError")
			if strings.Contains(out["Message"].(string), "private") || !reflect.DeepEqual(before, sqsAdditionalRows(t, store)) {
				t.Fatal("corrupt batch leaked data or committed expiry/partial claims", out)
			}
			if _, err := store.db.Exec("UPDATE aws_sqs_messages SET attributes='null',receive_count=0 WHERE id=?", ids[1]); err != nil {
				t.Fatal(err)
			}
			got, err := store.Receive(q.Region, q, 2, 30)
			if err != nil || len(got) != 2 || got[0].Body != "first" || got[1].Body != "second" || got[0].ReceiveCount != 1 || got[1].ReceiveCount != 1 || sqsSQLCount(t, store, "aws_sqs_messages") != 2 {
				t.Fatal("repaired batch did not claim atomically and purge expiry", got, err)
			}
		})
	}
}

func TestSQSAdditionalCorruptQueueMetadataDoesNotBecomeMissingOrOverwrite(t *testing.T) {
	g, store, q := sqsAdditionalFixture(t)
	if _, err := store.db.Exec("UPDATE aws_sqs_queues SET created='private-invalid-clock' WHERE name=?", q.Name); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ op, body string }{
		{"CreateQueue", `{"QueueName":"boundary"}`},
		{"GetQueueUrl", `{"QueueName":"boundary"}`},
		{"ListQueues", `{}`},
	} {
		out := sqsAdditionalResponse(t, g, sqsAdditionalRequest("POST", "/", tc.op, tc.body), 500, "InternalError")
		if strings.Contains(out["Message"].(string), "private-invalid-clock") || sqsSQLCount(t, store, "aws_sqs_queues") != 1 {
			t.Fatal("corrupt metadata was disclosed, replaced or acknowledged", out)
		}
	}
	var persisted string
	if err := store.db.QueryRow("SELECT created FROM aws_sqs_queues WHERE name=?", q.Name).Scan(&persisted); err != nil || persisted != "private-invalid-clock" {
		t.Fatal("failed read/create rewrote corrupt metadata", persisted, err)
	}
	if _, err := store.db.Exec("UPDATE aws_sqs_queues SET created=? WHERE name=?", q.Created, q.Name); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetQueue(q.Region, q.Name); err != nil || got != q {
		t.Fatal("metadata repair did not restore original queue", got, err)
	}
}

func TestSQSAdditionalReceiptDeleteFaultPreservesCurrentReceipt(t *testing.T) {
	g, store, q := sqsAdditionalFixture(t)
	m, err := store.Send(q.Region, q, "retained", nil, -1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Receive(q.Region, q, 1, 30)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	before := sqsAdditionalRows(t, store)
	if _, err = store.db.Exec("CREATE TRIGGER sqs_additional_delete_fault BEFORE DELETE ON aws_sqs_messages BEGIN SELECT RAISE(ABORT,'private-delete-fault'); END"); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{"QueueUrl": q.Name, "ReceiptHandle": got[0].ReceiptHandle})
	if err != nil {
		t.Fatal(err)
	}
	out := sqsAdditionalResponse(t, g, sqsAdditionalRequest("POST", "/", "DeleteMessage", string(data)), 500, "InternalError")
	if strings.Contains(out["Message"].(string), "private-delete-fault") || !reflect.DeepEqual(before, sqsAdditionalRows(t, store)) {
		t.Fatal("delete failure changed/acknowledged current receipt", out)
	}
	if _, err = store.db.Exec("DROP TRIGGER sqs_additional_delete_fault"); err != nil {
		t.Fatal(err)
	}
	if err = store.DeleteMessage(q.Region, q.Name, got[0].ReceiptHandle); err != nil || sqsSQLCount(t, store, "aws_sqs_messages") != 0 {
		t.Fatal("saved receipt stopped working after failed deletion", m.ID, err)
	}
}

func TestSQSAdditionalParentDeleteSQLFaultPreservesGraph(t *testing.T) {
	g, store, q := sqsAdditionalFixture(t)
	if _, err := store.Send(q.Region, q, "retained child", nil, -1); err != nil {
		t.Fatal(err)
	}
	before := sqsAdditionalRows(t, store)
	if _, err := store.db.Exec("CREATE TRIGGER sqs_additional_parent_fault BEFORE DELETE ON aws_sqs_queues BEGIN SELECT RAISE(ABORT,'private-parent-fault'); END"); err != nil {
		t.Fatal(err)
	}
	out := sqsAdditionalResponse(t, g, sqsAdditionalRequest("POST", "/", "DeleteQueue", `{"QueueUrl":"boundary"}`), 500, "InternalError")
	if strings.Contains(out["Message"].(string), "private-parent-fault") || !reflect.DeepEqual(before, sqsAdditionalRows(t, store)) || sqsSQLCount(t, store, "aws_sqs_queues") != 1 {
		t.Fatal("parent SQL failure committed graph deletion", out)
	}
	if _, err := store.db.Exec("DROP TRIGGER sqs_additional_parent_fault"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteQueue(q.Region, q.Name); err != nil || sqsSQLCount(t, store, "aws_sqs_queues") != 0 || sqsSQLCount(t, store, "aws_sqs_messages") != 0 {
		t.Fatal("queue graph could not be deleted after fault repair", err)
	}
}

func TestSQSAdditionalSkippedVisibilityUpdatePreservesCurrentRow(t *testing.T) {
	g, store, q := sqsAdditionalFixture(t)
	if _, err := store.Send(q.Region, q, "current receipt", nil, -1); err != nil {
		t.Fatal(err)
	}
	got, err := store.Receive(q.Region, q, 1, 30)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	before := sqsAdditionalRows(t, store)
	// Pure IGNORE skips the UPDATE without undefined BEFORE-trigger target
	// mutation. The affected-row guard must reject an unpersisted visibility.
	if _, err = store.db.Exec("CREATE TRIGGER sqs_additional_skip_visibility BEFORE UPDATE ON aws_sqs_messages BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"QueueUrl": q.Name, "ReceiptHandle": got[0].ReceiptHandle, "VisibilityTimeout": 60})
	if err != nil {
		t.Fatal(err)
	}
	sqsAdditionalResponse(t, g, sqsAdditionalRequest("POST", "/", "ChangeMessageVisibility", string(body)), 500, "InternalError")
	if !reflect.DeepEqual(before, sqsAdditionalRows(t, store)) {
		t.Fatal("zero-row visibility update changed the current receipt row")
	}
	if _, err = store.db.Exec("DROP TRIGGER sqs_additional_skip_visibility"); err != nil {
		t.Fatal(err)
	}
	if err = store.ChangeVisibility(q.Region, q.Name, got[0].ReceiptHandle, 0); err != nil {
		t.Fatal(err)
	}
	again, err := store.Receive(q.Region, q, 1, 30)
	if err != nil || len(again) != 1 || again[0].Body != "current receipt" || again[0].ReceiveCount != 2 {
		t.Fatal("saved receipt/message was not preserved after fault rollback", again, err)
	}
}

func TestSQSAdditionalReceiveSchemaFailureDoesNotCommitExpiryCleanup(t *testing.T) {
	g, store, q := sqsAdditionalFixture(t)
	for _, body := range []string{"live", "expired"} {
		if _, err := store.Send(q.Region, q, body, nil, -1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec("UPDATE aws_sqs_messages SET sent=? WHERE body='expired'", sqsNow()-120); err != nil {
		t.Fatal(err)
	}
	before := sqsAdditionalRows(t, store)
	if _, err := store.db.Exec("ALTER TABLE aws_sqs_messages RENAME COLUMN body TO private_missing_body"); err != nil {
		t.Fatal(err)
	}
	out := sqsAdditionalResponse(t, g, sqsAdditionalRequest("POST", "/", "ReceiveMessage", `{"QueueUrl":"boundary"}`), 500, "InternalError")
	if strings.Contains(out["Message"].(string), "private_missing_body") {
		t.Fatal("native error disclosed private schema", out)
	}
	if _, err := store.db.Exec("ALTER TABLE aws_sqs_messages RENAME COLUMN private_missing_body TO body"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, sqsAdditionalRows(t, store)) {
		t.Fatal("failed receive query committed expiry removal")
	}
	got, err := store.Receive(q.Region, q, 1, 30)
	if err != nil || len(got) != 1 || got[0].Body != "live" || got[0].ReceiveCount != 1 || sqsSQLCount(t, store, "aws_sqs_messages") != 1 {
		t.Fatal("repaired schema did not restore live receive/expiry behavior", got, err)
	}
}

func TestSQSAdditionalEmptyLongPollAndCancellationDoNotMutate(t *testing.T) {
	g, store, _ := sqsAdditionalFixture(t)
	r := sqsAdditionalRequest("POST", "/", "ReceiveMessage", `{"QueueUrl":"boundary","WaitTimeSeconds":1}`)
	start := time.Now()
	out := sqsAdditionalResponse(t, g, r, 200, "")
	if out["Messages"] != nil || time.Since(start) < 900*time.Millisecond {
		t.Fatal("empty long poll skipped the requested wait", out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = sqsAdditionalRequest("POST", "/", "ReceiveMessage", `{"QueueUrl":"boundary","WaitTimeSeconds":20}`).WithContext(ctx)
	start = time.Now()
	out = sqsAdditionalResponse(t, g, r, 200, "")
	if out["Messages"] != nil || time.Since(start) > 2*time.Second || sqsSQLCount(t, store, "aws_sqs_messages") != 0 {
		t.Fatal("cancellation waited, fabricated or persisted a message", out)
	}
}

func TestSQSAdditionalConstructorRejectsReadOnlyOrIncompatibleSchema(t *testing.T) {
	for _, mode := range []string{"read-only", "incompatible-schema"} {
		t.Run(mode, func(t *testing.T) {
			db, _, _ := lambdaState(t)
			if mode == "read-only" {
				if _, err := db.Database().Exec("PRAGMA query_only=ON"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.Database().Exec("CREATE TABLE aws_sqs_queues(private_column TEXT)"); err != nil {
				t.Fatal(err)
			}
			if _, err := NewSQSStore(db.Database()); err == nil {
				t.Fatal("invalid native persistence configuration accepted")
			}
			if mode == "read-only" {
				var tables int
				if err := db.Database().QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'aws_sqs_%'").Scan(&tables); err != nil || tables != 0 {
					t.Fatal("read-only admission left partial SQS schema", tables, err)
				}
			}
		})
	}
}
