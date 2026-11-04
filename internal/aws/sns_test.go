package aws

import (
	"context"
	"encoding/xml"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func snsTestGateway(t *testing.T, db *core.Store) *Gateway {
	t.Helper()
	sns, err := NewSNSStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	return &Gateway{Store: db, SNS: sns, Identities: core.NewIdentities(db)}
}

func snsCall(t *testing.T, g *Gateway, principal string, fields url.Values, status int, contains string) string {
	t.Helper()
	fields.Set("Version", "2010-03-31")
	r := httptest.NewRequest("POST", "/", strings.NewReader(fields.Encode()))
	r.Header.Set("X-Simulator-Service", "sns")
	if principal != "" {
		r.Header.Set("X-Simulator-Principal", principal)
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != status || !strings.Contains(w.Body.String(), contains) {
		t.Fatalf("%s: want %d containing %q, got %d %s", fields.Get("Action"), status, contains, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestSNSLifecycleFanoutAndErrors(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	topicArn := "arn:aws:sns:us-east-1:000000000000:alerts"
	create := url.Values{"Action": {"CreateTopic"}, "Name": {"alerts"}, "Attributes.entry.1.key": {"DisplayName"}, "Attributes.entry.1.value": {"Alerts"}}
	snsCall(t, g, "", create, 200, "<TopicArn>"+topicArn+"</TopicArn>")
	// CreateTopic is idempotent in real SNS.
	snsCall(t, g, "", create, 200, "<TopicArn>"+topicArn+"</TopicArn>")
	snsCall(t, g, "", url.Values{"Action": {"CreateTopic"}, "Name": {"bad name!"}}, 400, "InvalidParameterException")
	snsCall(t, g, "", url.Values{"Action": {"CreateTopic"}, "Name": {"fifo-without-suffix"}, "Attributes.entry.1.key": {"FifoTopic"}, "Attributes.entry.1.value": {"true"}}, 400, "InvalidParameterException")
	snsCall(t, g, "", url.Values{"Action": {"CreateTopic"}, "Name": {"events.fifo"}, "Attributes.entry.1.key": {"FifoTopic"}, "Attributes.entry.1.value": {"true"}}, 200, "events.fifo")
	listed := snsCall(t, g, "", url.Values{"Action": {"ListTopics"}}, 200, topicArn)
	var listDoc struct {
		Result struct {
			Topics []struct{ TopicArn string } `xml:"Topics>member"`
		} `xml:"ListTopicsResult"`
	}
	if err := xml.Unmarshal([]byte(listed), &listDoc); err != nil || len(listDoc.Result.Topics) != 2 {
		t.Fatalf("list: %v %+v", err, listDoc)
	}
	attrs := snsCall(t, g, "", url.Values{"Action": {"GetTopicAttributes"}, "TopicArn": {topicArn}}, 200, "<key>DisplayName</key>")
	if !strings.Contains(attrs, "<value>Alerts</value>") || !strings.Contains(attrs, "<key>SubscriptionsConfirmed</key>") {
		t.Fatal(attrs)
	}
	snsCall(t, g, "", url.Values{"Action": {"GetTopicAttributes"}, "TopicArn": {"arn:aws:sns:us-east-1:000000000000:missing"}}, 404, "NotFoundException")
	// SQS fanout is deliberately not integrated in this build.
	snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {topicArn}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:us-east-1:000000000000:q"}}, 400, "not integrated")
	subBody := snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {topicArn}, "Protocol": {"email"}, "Endpoint": {"team@example.com"}}, 200, "<SubscriptionArn>")
	var subDoc struct {
		Result struct{ SubscriptionArn string } `xml:"SubscribeResult"`
	}
	if err := xml.Unmarshal([]byte(subBody), &subDoc); err != nil || !strings.HasPrefix(subDoc.Result.SubscriptionArn, topicArn+":") {
		t.Fatalf("subscribe: %v %s", err, subBody)
	}
	subArn := subDoc.Result.SubscriptionArn
	// Duplicate subscribe returns the same subscription (real SNS behavior).
	again := snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {topicArn}, "Protocol": {"email"}, "Endpoint": {"team@example.com"}}, 200, subArn)
	_ = again
	snsCall(t, g, "", url.Values{"Action": {"ListSubscriptionsByTopic"}, "TopicArn": {topicArn}}, 200, subArn)
	// Publish fans out to a local delivery record per subscription.
	pub := snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topicArn}, "Message": {"hello"}, "Subject": {"Hi"}}, 200, "<MessageId>")
	_ = pub
	if n, err := g.SNS.DeliveryCount(subArn); err != nil || n != 1 {
		t.Fatalf("deliveries: %d %v", n, err)
	}
	msgs, err := g.SNS.ListMessages(context.Background(), topicArn)
	if err != nil || len(msgs) != 1 || msgs[0].Message != "hello" || msgs[0].Subject != "Hi" {
		t.Fatalf("messages: %v %+v", err, msgs)
	}
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topicArn}}, 400, "InvalidParameterException")
	snsCall(t, g, "", url.Values{"Action": {"Unsubscribe"}, "SubscriptionArn": {subArn}}, 200, "")
	snsCall(t, g, "", url.Values{"Action": {"Unsubscribe"}, "SubscriptionArn": {subArn}}, 404, "NotFoundException")
	// After unsubscribing, publish records no new delivery.
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topicArn}, "Message": {"second"}}, 200, "<MessageId>")
	if n, _ := g.SNS.DeliveryCount(subArn); n != 1 {
		t.Fatalf("deliveries after unsubscribe: %d", n)
	}
	// DeleteTopic cascades subscriptions.
	snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {topicArn}, "Protocol": {"https"}, "Endpoint": {"https://example.com/hook"}}, 200, "<SubscriptionArn>")
	snsCall(t, g, "", url.Values{"Action": {"DeleteTopic"}, "TopicArn": {topicArn}}, 200, "")
	snsCall(t, g, "", url.Values{"Action": {"ListSubscriptionsByTopic"}, "TopicArn": {topicArn}}, 404, "NotFoundException")
	snsCall(t, g, "", url.Values{"Action": {"DeleteTopic"}, "TopicArn": {topicArn}}, 404, "NotFoundException")
	snsCall(t, g, "", url.Values{"Action": {"SetTopicAttributes"}, "TopicArn": {topicArn}}, 400, "InvalidAction")
}

func TestSNSAuthorization(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	snsCall(t, g, "", url.Values{"Action": {"CreateTopic"}, "Name": {"alerts"}}, 200, "<TopicArn>")
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sns:ListTopics"],"Resource":"*"}]}`
	p, err := core.ParsePolicy([]byte(policy))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Identities.Set("reader", p); err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "reader", url.Values{"Action": {"ListTopics"}}, 200, "<ListTopicsResponse")
	snsCall(t, g, "reader", url.Values{"Action": {"CreateTopic"}, "Name": {"other"}}, 403, "AuthorizationErrorException")
	snsCall(t, g, "reader", url.Values{"Action": {"Publish"}, "TopicArn": {"arn:aws:sns:us-east-1:000000000000:alerts"}, "Message": {"x"}}, 403, "AuthorizationErrorException")
}

type stubQueueFanout struct {
	queues    map[string]bool
	delivered map[string][]string
	fail      bool
}

func (s *stubQueueFanout) QueueExists(region, queueName string) (bool, error) {
	return s.queues[region+"/"+queueName], nil
}
func (s *stubQueueFanout) DeliverToQueue(region, queueName, body string) error {
	if s.fail {
		return ssmFailure{"InternalError", "stub delivery failure"}
	}
	if !s.queues[region+"/"+queueName] {
		return ssmFailure{"NotFoundException", "Queue does not exist: " + queueName}
	}
	s.delivered[queueName] = append(s.delivered[queueName], body)
	return nil
}

func TestSNStoSQSFanoutContract(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	fanout := &stubQueueFanout{queues: map[string]bool{"us-east-1/jobs": true}, delivered: map[string][]string{}}
	g.QueueFanout = fanout
	topicArn := "arn:aws:sns:us-east-1:000000000000:jobs-topic"
	snsCall(t, g, "", url.Values{"Action": {"CreateTopic"}, "Name": {"jobs-topic"}}, 200, topicArn)
	// Queue ARN in another region and a missing queue are rejected.
	snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {topicArn}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:eu-west-1:000000000000:jobs"}}, 400, "InvalidParameterException")
	snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {topicArn}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:us-east-1:000000000000:missing"}}, 404, "NotFoundException")
	snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {topicArn}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:us-east-1:000000000000:jobs"}}, 200, "<SubscriptionArn>")
	pub := snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topicArn}, "Message": {"run job"}, "Subject": {"Job"}}, 200, "<MessageId>")
	var pubDoc struct {
		Result struct{ MessageId string } `xml:"PublishResult"`
	}
	if err := xml.Unmarshal([]byte(pub), &pubDoc); err != nil {
		t.Fatal(err)
	}
	bodies := fanout.delivered["jobs"]
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"Type":"Notification"`) || !strings.Contains(bodies[0], `"Message":"run job"`) || !strings.Contains(bodies[0], `"MessageId":"`+pubDoc.Result.MessageId+`"`) || !strings.Contains(bodies[0], `"TopicArn":"`+topicArn+`"`) {
		t.Fatalf("envelope: %v", bodies)
	}
	// A failing queue delivery fails the Publish and records nothing.
	fanout.fail = true
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topicArn}, "Message": {"lost"}}, 500, "InternalError")
	msgs, err := g.SNS.ListMessages(context.Background(), topicArn)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("messages after failed publish: %v %+v", err, msgs)
	}
}

func TestSNSPersistenceAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sns-state.db")
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	g := snsTestGateway(t, db)
	topicArn := "arn:aws:sns:us-east-1:000000000000:persisted"
	snsCall(t, g, "", url.Values{"Action": {"CreateTopic"}, "Name": {"persisted"}}, 200, topicArn)
	sub := snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {topicArn}, "Protocol": {"email"}, "Endpoint": {"a@example.com"}}, 200, "<SubscriptionArn>")
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topicArn}, "Message": {"durable"}}, 200, "<MessageId>")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	g2 := snsTestGateway(t, db2)
	snsCall(t, g2, "", url.Values{"Action": {"ListTopics"}}, 200, topicArn)
	snsCall(t, g2, "", url.Values{"Action": {"ListSubscriptionsByTopic"}, "TopicArn": {topicArn}}, 200, "a@example.com")
	var subDoc struct {
		Result struct{ SubscriptionArn string } `xml:"SubscribeResult"`
	}
	if err := xml.Unmarshal([]byte(sub), &subDoc); err != nil {
		t.Fatal(err)
	}
	if n, err := g2.SNS.DeliveryCount(subDoc.Result.SubscriptionArn); err != nil || n != 1 {
		t.Fatalf("deliveries after restart: %d %v", n, err)
	}
	msgs, err := g2.SNS.ListMessages(context.Background(), topicArn)
	if err != nil || len(msgs) != 1 || msgs[0].Message != "durable" {
		t.Fatalf("messages after restart: %v %+v", err, msgs)
	}
}
