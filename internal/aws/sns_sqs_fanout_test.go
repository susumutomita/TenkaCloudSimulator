package aws

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func snsNativeFanoutFixture(t *testing.T) (*Gateway, SNSTopic) {
	t.Helper()
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	var err error
	g.Queues, err = NewSQSStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	topic, _, err := g.SNS.CreateTopic("us-east-1", "native", "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		_, err = g.Queues.CreateQueue(topic.Region, SQSQueue{Name: name, VisibilityTimeout: 30, RetentionSeconds: 345600})
		if err != nil {
			t.Fatal(err)
		}
		snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {topic.ARN}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:us-east-1:000000000000:" + name}}, 200, "SubscriptionArn")
	}
	// Stable recipient order makes second-destination rollback tests deterministic.
	if _, err = g.SNS.db.Exec("UPDATE aws_sns_subscriptions SET arn=topic_arn || ':' || substr(endpoint,length(endpoint),1)"); err != nil {
		t.Fatal(err)
	}
	return g, topic
}

func snsFanoutCounts(t *testing.T, g *Gateway, messages, deliveries, queues int) {
	t.Helper()
	for table, want := range map[string]int{"aws_sns_messages": messages, "aws_sns_deliveries": deliveries, "aws_sqs_messages": queues} {
		var got int
		if err := g.SNS.db.QueryRow("SELECT count(*) FROM " + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count=%d want=%d: %v", table, got, want, err)
		}
	}
}

func TestSNSNativeFanoutLifecycle(t *testing.T) {
	g, topic := snsNativeFanoutFixture(t)
	// The composed native stores take precedence; this callback must never run.
	g.QueueFanout = snsCheckFailure{}
	snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {topic.ARN}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:us-east-1:000000000000:a"}}, 200, "SubscriptionArn")
	if _, err := g.SNS.Subscribe(topic, "email", "audit@example.com"); err != nil {
		t.Fatal(err)
	}
	q, err := g.Queues.CreateQueue("us-west-2", SQSQueue{Name: "a", VisibilityTimeout: 30, RetentionSeconds: 345600})
	if err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topic.ARN}, "Message": {"日本語 <payload>"}, "Subject": {"notice"}}, 200, "MessageId")
	snsFanoutCounts(t, g, 1, 3, 2)
	stored, err := g.SNS.ListMessages(context.Background(), topic.ARN)
	if err != nil || len(stored) != 1 {
		t.Fatal(stored, err)
	}
	for _, name := range []string{"a", "b"} {
		queue, err := g.Queues.GetQueue(topic.Region, name)
		if err != nil {
			t.Fatal(err)
		}
		messages, err := g.Queues.Receive(topic.Region, queue, 10, 30)
		if err != nil || len(messages) != 1 {
			t.Fatal(messages, err)
		}
		var envelope map[string]string
		if err = json.Unmarshal([]byte(messages[0].Body), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["Type"] != "Notification" || envelope["TopicArn"] != topic.ARN || envelope["MessageId"] != stored[0].ID || envelope["Message"] != stored[0].Message || envelope["Subject"] != "notice" || envelope["Timestamp"] == "" {
			t.Fatal(envelope)
		}
	}
	if messages, err := g.Queues.Receive("us-west-2", q, 10, 30); err != nil || len(messages) != 0 {
		t.Fatal("other region changed", messages, err)
	}
	if err := g.SNS.DeleteTopic(topic.ARN); err != nil {
		t.Fatal(err)
	}
	// Already delivered queue messages belong to SQS, not to later SNS cleanup.
	snsFanoutCounts(t, g, 0, 0, 2)
}

func TestSNSNativeFanoutPolicySnapshotDoesNotMixGrants(t *testing.T) {
	g, topic := snsNativeFanoutFixture(t)
	snsOnly := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"sns:*"}, Resource: core.StringList{"*"}}}}
	sqsOnly := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"sqs:SendMessage"}, Resource: core.StringList{"*"}}}}
	if err := g.Identities.Set("changing", snsOnly); err != nil {
		t.Fatal(err)
	}
	before, err := g.snsAuthorizationSnapshot(context.Background(), "changing")
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Identities.Set("changing", sqsOnly); err != nil {
		t.Fatal(err)
	}
	if before("sns:Publish", topic.ARN) != "allowed" {
		t.Fatal("SNS snapshot changed")
	}
	if _, err = g.SNS.publishQueues(context.Background(), g.Queues, topic, "mixed grants", "", snsQueueAuthorization(before)); err == nil {
		t.Fatal("mixed policy versions permitted fanout")
	}
	after, err := g.snsAuthorizationSnapshot(context.Background(), "changing")
	if err != nil {
		t.Fatal(err)
	}
	if after("sns:Publish", topic.ARN) == "allowed" {
		t.Fatal("SQS-only policy authorized SNS")
	}
	snsCall(t, g, "changing", url.Values{"Action": {"Publish"}, "TopicArn": {topic.ARN}, "Message": {"denied"}}, 403, "AuthorizationError")
	snsFanoutCounts(t, g, 0, 0, 0)
	if _, err = g.SNS.db.Exec("DROP TABLE identity_policies"); err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "changing", url.Values{"Action": {"Publish"}, "TopicArn": {topic.ARN}, "Message": {"unreadable policy"}}, 500, "InternalError")
}

func TestSNSNativeFanoutHTTPReadsPolicyOnlyOnce(t *testing.T) {
	g, topic := snsNativeFanoutFixture(t)
	p, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sns:Publish","sqs:SendMessage"],"Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Identities.Set("snapshot", p); err != nil {
		t.Fatal(err)
	}
	reads := 0
	snsSetAuthorizer(t, g.SNS.db, func(action sqlite3.AuthorizerActionCode, table, column, _, _ string) sqlite3.AuthorizerReturnCode {
		if action == sqlite3.AUTH_READ && table == "identity_policies" && column == "document" {
			reads++
			if reads > 1 {
				return sqlite3.AUTH_DENY
			}
		}
		return sqlite3.AUTH_OK
	})
	snsCall(t, g, "snapshot", url.Values{"Action": {"Publish"}, "TopicArn": {topic.ARN}, "Message": {"one authority snapshot"}}, 200, "MessageId")
	snsSetAuthorizer(t, g.SNS.db, nil)
	if reads != 1 {
		t.Fatalf("handler read %d policy snapshots; want exactly one", reads)
	}
	snsFanoutCounts(t, g, 1, 2, 2)
}

func TestSNSNativeFanoutDelayExpiryAndQueueRecreation(t *testing.T) {
	g, topic := snsNativeFanoutFixture(t)
	if _, err := g.SNS.db.Exec("UPDATE aws_sqs_queues SET delay_seconds=7 WHERE name='a'"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.SNS.db.Exec("INSERT INTO aws_sqs_messages VALUES('expired','us-east-1','a','old','null',0,0,0,'')"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.SNS.db.Exec("CREATE TRIGGER fault BEFORE INSERT ON aws_sqs_messages WHEN NEW.queue='b' BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topic.ARN}, "Message": {"rollback"}}, 500, "InternalError")
	// Even scoped retention cleanup is undone if a destination cannot commit.
	snsFanoutCounts(t, g, 0, 0, 1)
	if _, err := g.SNS.db.Exec("DROP TRIGGER fault"); err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topic.ARN}, "Message": {"delayed"}}, 200, "MessageId")
	snsFanoutCounts(t, g, 1, 2, 2)
	q, err := g.Queues.GetQueue(topic.Region, "a")
	if err != nil {
		t.Fatal(err)
	}
	if messages, err := g.Queues.Receive(topic.Region, q, 10, 30); err != nil || len(messages) != 0 {
		t.Fatal("queue delay ignored", messages, err)
	}
	if err = g.Queues.DeleteQueue(topic.Region, "b"); err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topic.ARN}, "Message": {"missing"}}, 404, "NotFound")
	if _, err = g.Queues.CreateQueue(topic.Region, SQSQueue{Name: "b", VisibilityTimeout: 30, RetentionSeconds: 345600}); err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topic.ARN}, "Message": {"recreated"}}, 200, "MessageId")
	snsFanoutCounts(t, g, 2, 4, 3)
	subs, err := g.SNS.ListSubscriptionsByTopic(context.Background(), topic.ARN)
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range subs {
		if err = g.SNS.Unsubscribe(sub.ARN); err != nil {
			t.Fatal(err)
		}
	}
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topic.ARN}, "Message": {"unsubscribed"}}, 200, "MessageId")
	snsFanoutCounts(t, g, 3, 4, 3)
}

func TestSNSNativeQueueSubscriptionRollback(t *testing.T) {
	g, topic := snsNativeFanoutFixture(t)
	if _, err := g.Queues.CreateQueue(topic.Region, SQSQueue{Name: "c", VisibilityTimeout: 30, RetentionSeconds: 345600}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.SNS.db.Exec("CREATE TRIGGER fault AFTER INSERT ON aws_sns_subscriptions BEGIN DELETE FROM aws_sqs_queues WHERE name='c'; END"); err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {topic.ARN}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:us-east-1:000000000000:c"}}, 404, "NotFound")
	if _, err := g.Queues.GetQueue(topic.Region, "c"); err != nil {
		t.Fatal("subscription rollback lost queue", err)
	}
	subs, err := g.SNS.ListSubscriptionsByTopic(context.Background(), topic.ARN)
	if err != nil || len(subs) != 2 {
		t.Fatal(subs, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = g.SNS.subscribeQueue(ctx, g.Queues, topic, "arn:aws:sqs:us-east-1:000000000000:c"); err == nil {
		t.Fatal("cancelled subscription succeeded")
	}
}

func TestSNSNativeFanoutIAMAndAdmission(t *testing.T) {
	g, topic := snsNativeFanoutFixture(t)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sns:*","Resource":"*"},{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"arn:aws:sqs:us-east-1:000000000000:a"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Identities.Set("publisher", policy); err != nil {
		t.Fatal(err)
	}
	fields := url.Values{"Action": {"Publish"}, "Version": {"2010-03-31"}, "TopicArn": {topic.ARN}, "Message": {"denied"}}
	w := snsWire(t, g, "POST", "/", fields.Encode(), map[string]string{"X-Simulator-Principal": "publisher"}, 403)
	if w.Header().Get("X-Simulator-IAM-Decision") != "implicitDeny" {
		t.Fatal(w.Header())
	}
	snsCall(t, g, "publisher", url.Values{"Action": {"Subscribe"}, "TopicArn": {topic.ARN}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:us-east-1:000000000000:b"}}, 403, "AuthorizationError")
	snsFanoutCounts(t, g, 0, 0, 0)
	policy.Statement = append(policy.Statement, core.Statement{Effect: "Allow", Action: core.StringList{"sqs:SendMessage"}, Resource: core.StringList{"*"}})
	if err = g.Identities.Set("publisher", policy); err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "publisher", fields, 200, "MessageId")
	snsFanoutCounts(t, g, 1, 2, 2)
	policy.Statement = append(policy.Statement, core.Statement{Effect: "Deny", Action: core.StringList{"sqs:SendMessage"}, Resource: core.StringList{"*"}})
	if err = g.Identities.Set("publisher", policy); err != nil {
		t.Fatal(err)
	}
	w = snsWire(t, g, "POST", "/", fields.Encode(), map[string]string{"X-Simulator-Principal": "publisher"}, 403)
	if w.Header().Get("X-Simulator-IAM-Decision") != "explicitDeny" {
		t.Fatal(w.Header())
	}
	snsFanoutCounts(t, g, 1, 2, 2)
	for _, endpoint := range []string{"arn:aws:sqs:us-west-2:000000000000:a", "arn:aws:sqs:us-east-1:111111111111:a", "arn:aws:sqs:us-east-1:000000000000:bad/name", "arn:aws:sqs:us-east-1:000000000000:a.fifo"} {
		snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {topic.ARN}, "Protocol": {"sqs"}, "Endpoint": {endpoint}}, 400, "InvalidParameter")
	}
	if err = g.Queues.DeleteQueue(topic.Region, "b"); err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "", fields, 404, "NotFound")
	snsFanoutCounts(t, g, 1, 2, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = g.SNS.publishQueues(ctx, g.Queues, topic, "cancelled", "", func(string) error { return nil }); err == nil {
		t.Fatal("cancelled publish succeeded")
	}
}

func TestSNSNativeFanoutStorageFaultsRollbackEveryDestination(t *testing.T) {
	triggers := map[string]string{
		"ignored enqueue":             "CREATE TRIGGER fault BEFORE INSERT ON aws_sqs_messages BEGIN SELECT RAISE(IGNORE); END",
		"vanished enqueue":            "CREATE TRIGGER fault AFTER INSERT ON aws_sqs_messages BEGIN DELETE FROM aws_sqs_messages WHERE id=NEW.id; END",
		"mutated envelope":            "CREATE TRIGGER fault AFTER INSERT ON aws_sqs_messages BEGIN UPDATE aws_sqs_messages SET body='corrupt' WHERE id=NEW.id; END",
		"second queue discards first": "CREATE TRIGGER fault AFTER INSERT ON aws_sqs_messages WHEN (SELECT count(*) FROM aws_sqs_messages)=2 BEGIN DELETE FROM aws_sqs_messages WHERE id!=NEW.id; END",
		"queue discards SNS audit":    "CREATE TRIGGER fault AFTER INSERT ON aws_sqs_messages BEGIN DELETE FROM aws_sns_messages; END",
		"queue discards subscription": "CREATE TRIGGER fault AFTER INSERT ON aws_sqs_messages BEGIN DELETE FROM aws_sns_subscriptions; END",
		"queue discards delivery":     "CREATE TRIGGER fault AFTER INSERT ON aws_sqs_messages BEGIN DELETE FROM aws_sns_deliveries; END",
		"queue changes admission":     "CREATE TRIGGER fault AFTER INSERT ON aws_sqs_messages BEGIN UPDATE aws_sqs_queues SET delay_seconds=1 WHERE name=NEW.queue AND region=NEW.region; END",
		"SNS delivery ignored":        "CREATE TRIGGER fault BEFORE INSERT ON aws_sns_deliveries BEGIN SELECT RAISE(IGNORE); END",
		"SNS message ignored":         "CREATE TRIGGER fault BEFORE INSERT ON aws_sns_messages BEGIN SELECT RAISE(IGNORE); END",
	}
	for name, sql := range triggers {
		t.Run(name, func(t *testing.T) {
			g, topic := snsNativeFanoutFixture(t)
			if _, err := g.SNS.db.Exec(sql); err != nil {
				t.Fatal(err)
			}
			w := snsWire(t, g, "POST", "/", url.Values{"Action": {"Publish"}, "Version": {"2010-03-31"}, "TopicArn": {topic.ARN}, "Message": {"rollback"}}.Encode(), nil, 500)
			if strings.Contains(w.Body.String(), "aws_sqs") || strings.Contains(w.Body.String(), "trigger") {
				t.Fatal("storage detail disclosed", w.Body.String())
			}
			snsFanoutCounts(t, g, 0, 0, 0)
			for _, q := range []string{"a", "b"} {
				queue, err := g.Queues.GetQueue(topic.Region, q)
				if err != nil || queue.DelaySeconds != 0 {
					t.Fatal(queue, err)
				}
			}
			subs, err := g.SNS.ListSubscriptionsByTopic(context.Background(), topic.ARN)
			if err != nil || len(subs) != 2 {
				t.Fatal(subs, err)
			}
			if _, err = g.SNS.db.Exec("DROP TRIGGER fault"); err != nil {
				t.Fatal(err)
			}
			snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topic.ARN}, "Message": {"retry"}}, 200, "MessageId")
			snsFanoutCounts(t, g, 1, 2, 2)
		})
	}
}

func TestSNSNativeFanoutQuotasAndUnsupportedComposition(t *testing.T) {
	g, topic := snsNativeFanoutFixture(t)
	_, err := g.SNS.db.Exec("WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<9999) INSERT INTO aws_sqs_messages SELECT printf('existing-%05d',x),'us-east-1','a','x','null',?, ?,0,'' FROM n", sqsNow(), sqsNow())
	if err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topic.ARN}, "Message": {"full"}}, 409, "LimitExceededException")
	snsFanoutCounts(t, g, 0, 0, 9999)
	if _, err = g.SNS.db.Exec("DELETE FROM aws_sqs_messages"); err != nil {
		t.Fatal(err)
	}
	// JSON escaping can make a valid SNS message exceed this bounded SQS body.
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topic.ARN}, "Message": {strings.Repeat("<", 262144)}}, 400, "InvalidParameter")
	snsFanoutCounts(t, g, 0, 0, 0)
	peer, _ := s3TestState(t)
	other, err := NewSQSStore(peer.Database())
	if err != nil {
		t.Fatal(err)
	}
	allow := func(string) error { return nil }
	if _, err = g.SNS.publishQueues(context.Background(), other, topic, "cross db", "", allow); err == nil {
		t.Fatal("cross database publish accepted")
	}
	if _, err = g.SNS.publishQueues(context.Background(), g.Queues, topic, "no auth", "", nil); err == nil {
		t.Fatal("missing authority accepted")
	}
	if _, err = g.SNS.subscribeQueue(context.Background(), other, topic, "arn:aws:sqs:us-east-1:000000000000:a"); err == nil {
		t.Fatal("cross database subscription accepted")
	}
	fifo, _, err := g.SNS.CreateTopic(topic.Region, "unsupported.fifo", "", true)
	if err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {fifo.ARN}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:us-east-1:000000000000:a"}}, 400, "Standard")
}
