package aws

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func sqsWire(t *testing.T, g *Gateway, op string, input any, code int) map[string]any {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
	r.Header.Set("X-Amz-Target", "AmazonSQS."+op)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != code {
		t.Fatalf("%s: got %d want %d: %s", op, w.Code, code, w.Body.String())
	}
	var out map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSQSWireStorageFailureNeverAcknowledgesMutation(t *testing.T) {
	for _, op := range []string{"CreateQueue", "SendMessage", "ReceiveMessage", "DeleteQueue", "ChangeMessageVisibility", "GetQueueAttributes"} {
		t.Run(op, func(t *testing.T) {
			db, _, identities := lambdaState(t)
			store, err := NewSQSStore(db.Database())
			if err != nil {
				t.Fatal(err)
			}
			g := &Gateway{Store: db, Identities: identities, Queues: store}
			url := sqsWire(t, g, "CreateQueue", map[string]any{"QueueName": "wire-fault"}, 200)["QueueUrl"].(string)
			sqsWire(t, g, "SendMessage", map[string]any{"QueueUrl": url, "MessageBody": "preserved"}, 200)
			input := map[string]any{"QueueUrl": url}
			trigger := "BEFORE UPDATE ON aws_sqs_messages"
			switch op {
			case "CreateQueue":
				input = map[string]any{"QueueName": "must-not-exist"}
				trigger = "BEFORE INSERT ON aws_sqs_queues"
			case "SendMessage":
				input["MessageBody"] = "must-not-exist"
				trigger = "BEFORE INSERT ON aws_sqs_messages"
			case "DeleteQueue":
				trigger = "BEFORE DELETE ON aws_sqs_messages"
			case "ChangeMessageVisibility":
				out := sqsWire(t, g, "ReceiveMessage", input, 200)
				input["ReceiptHandle"] = out["Messages"].([]any)[0].(map[string]any)["ReceiptHandle"]
				input["VisibilityTimeout"] = 60
			case "GetQueueAttributes":
				input["AttributeNames"] = []string{"All"}
				if _, err = store.db.Exec("UPDATE aws_sqs_messages SET sent=0"); err != nil {
					t.Fatal(err)
				}
				trigger = "BEFORE DELETE ON aws_sqs_messages"
			}
			var beforeVisible float64
			var beforeReceipt string
			var beforeReceives int
			if err = store.db.QueryRow("SELECT visible,receipt,receive_count FROM aws_sqs_messages").Scan(&beforeVisible, &beforeReceipt, &beforeReceives); err != nil {
				t.Fatal(err)
			}
			if _, err = store.db.Exec("CREATE TRIGGER wire_fault " + trigger + " BEGIN SELECT RAISE(ABORT,'private injected database fault'); END"); err != nil {
				t.Fatal(err)
			}
			out := sqsWire(t, g, op, input, 500)
			if out["__type"] != "InternalError" || strings.Contains(fmt.Sprint(out), "private injected") {
				t.Fatal("storage failure leaked or acknowledged", out)
			}
			var visible float64
			var receipt string
			var receives int
			if err = store.db.QueryRow("SELECT visible,receipt,receive_count FROM aws_sqs_messages").Scan(&visible, &receipt, &receives); err != nil {
				t.Fatal(err)
			}
			if visible != beforeVisible || receipt != beforeReceipt || receives != beforeReceives || sqsSQLCount(t, store, "aws_sqs_messages") != 1 || sqsSQLCount(t, store, "aws_sqs_queues") != 1 {
				t.Fatal("wire storage failure committed partial state", visible, receipt, receives)
			}
		})
	}
}

func TestSQSNativeValidationDoesNotMutateQueue(t *testing.T) {
	s, q := sqsQueueFixture(t)
	for _, test := range []struct {
		body  string
		delay int
	}{
		{"", -1}, {"\x00", -1}, {"ok", -2}, {"ok", 901},
	} {
		if _, err := s.Send(q.Region, q, test.body, nil, test.delay); err == nil {
			t.Fatal("invalid native send accepted", test)
		}
	}
	m, err := s.Send(q.Region, q, "unchanged", nil, -1)
	if err != nil {
		t.Fatal(err)
	}
	for _, bounds := range [][2]int{{0, 30}, {11, 30}, {1, -1}, {1, 43201}} {
		if _, err = s.Receive(q.Region, q, bounds[0], bounds[1]); err == nil {
			t.Fatal("invalid native receive accepted", bounds)
		}
	}
	for _, timeout := range []int{-1, 43201} {
		sqsFailureCode(t, s.ChangeVisibility(q.Region, q.Name, "unissued", timeout), "InvalidParameterValue")
	}
	got, err := s.Receive(q.Region, q, 1, 30)
	if err != nil || len(got) != 1 || got[0].ID != m.ID || got[0].ReceiveCount != 1 {
		t.Fatal("invalid requests changed message", got, err)
	}
}

func TestSQSQueueCapacityPreservesIdempotency(t *testing.T) {
	s, q := sqsQueueFixture(t)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 1; i < maxQueues; i++ {
		if _, err = tx.Exec("INSERT INTO aws_sqs_queues VALUES(?,?,?,?,?,?)", q.Region, fmt.Sprintf("capacity-%04d", i), q.Created, 30, 0, 60); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateQueue(q.Region, SQSQueue{Name: "overflow", VisibilityTimeout: 30, RetentionSeconds: 60})
	sqsFailureCode(t, err, "QueueLimitExceeded")
	again, err := s.CreateQueue(q.Region, q)
	if err != nil || again != q {
		t.Fatal("existing identical queue lost at capacity", again, err)
	}
	changed := q
	changed.DelaySeconds = 1
	_, err = s.CreateQueue(q.Region, changed)
	sqsFailureCode(t, err, "QueueNameExists")
	if sqsSQLCount(t, s, "aws_sqs_queues") != maxQueues {
		t.Fatal("capacity refusal changed queue set")
	}
}

func TestSQSCountsSeparateDelayedFromDeliveredInflight(t *testing.T) {
	s, q := sqsQueueFixture(t)
	if _, err := s.Send(q.Region, q, "not delivered", nil, 900); err != nil {
		t.Fatal(err)
	}
	if visible, inflight, err := s.Counts(q.Region, q); err != nil || visible != 0 || inflight != 0 {
		t.Fatal("delayed message counted as inflight", visible, inflight, err)
	}
	m, err := s.Send(q.Region, q, "deliver now", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if visible, inflight, err := s.Counts(q.Region, q); err != nil || visible != 1 || inflight != 0 {
		t.Fatal("available message counters", visible, inflight, err)
	}
	got, err := s.Receive(q.Region, q, 1, 900)
	if err != nil || len(got) != 1 || got[0].ID != m.ID {
		t.Fatal(got, err)
	}
	if visible, inflight, err := s.Counts(q.Region, q); err != nil || visible != 0 || inflight != 1 {
		t.Fatal("delivered message counters", visible, inflight, err)
	}
	if err = s.ChangeVisibility(q.Region, q.Name, got[0].ReceiptHandle, 0); err != nil {
		t.Fatal(err)
	}
	if visible, inflight, err := s.Counts(q.Region, q); err != nil || visible != 1 || inflight != 0 {
		t.Fatal("visibility release counters", visible, inflight, err)
	}
}
