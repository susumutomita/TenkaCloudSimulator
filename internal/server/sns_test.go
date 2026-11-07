package server

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func snsForm(t *testing.T, s *httptest.Server, fields url.Values, want int, headers map[string]string) string {
	t.Helper()
	fields.Set("Version", "2010-03-31")
	h := map[string]string{"X-Simulator-Service": "sns", "Content-Type": "application/x-www-form-urlencoded"}
	for k, v := range headers {
		h[k] = v
	}
	return string(status(t, s, "POST", "/", fields.Encode(), want, h))
}

func TestSNSWireLifecycleThroughServer(t *testing.T) {
	s := instance(t)
	topicArn := "arn:aws:sns:us-east-1:000000000000:wire-alerts"
	body := snsForm(t, s, url.Values{"Action": {"CreateTopic"}, "Name": {"wire-alerts"}}, 200, nil)
	if !strings.Contains(body, "<TopicArn>"+topicArn+"</TopicArn>") {
		t.Fatal(body)
	}
	body = snsForm(t, s, url.Values{"Action": {"Subscribe"}, "TopicArn": {topicArn}, "Protocol": {"email"}, "Endpoint": {"ops@example.com"}}, 200, nil)
	if !strings.Contains(body, "<SubscriptionArn>"+topicArn+":") {
		t.Fatal(body)
	}
	body = snsForm(t, s, url.Values{"Action": {"Publish"}, "TopicArn": {topicArn}, "Message": {"via server"}}, 200, nil)
	if !strings.Contains(body, "<MessageId>") {
		t.Fatal(body)
	}
	// SQS fanout must be an explicit error, never a silent success.
	body = snsForm(t, s, url.Values{"Action": {"Subscribe"}, "TopicArn": {topicArn}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:us-east-1:000000000000:q"}}, 400, nil)
	if !strings.Contains(body, "not integrated") {
		t.Fatal(body)
	}
	caps := string(status(t, s, "GET", "/_simulator/v1/capabilities", "", 200, nil))
	if !strings.Contains(caps, `"sns"`) || !strings.Contains(caps, `"sqsFanout":false`) {
		t.Fatal("capabilities do not declare the SNS subset and its SQS limit")
	}
	// IAM: a principal allowed only ListTopics cannot publish.
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sns:ListTopics"],"Resource":"*"}]}`
	status(t, s, "PUT", "/_simulator/v1/iam/principals/reader/policy", policy, 200, nil)
	reader := map[string]string{"X-Simulator-Principal": "reader"}
	snsForm(t, s, url.Values{"Action": {"ListTopics"}}, 200, reader)
	snsForm(t, s, url.Values{"Action": {"Publish"}, "TopicArn": {topicArn}, "Message": {"nope"}}, 403, reader)
	snsForm(t, s, url.Values{"Action": {"DeleteTopic"}, "TopicArn": {topicArn}}, 200, nil)
	snsForm(t, s, url.Values{"Action": {"ListTopics"}}, 200, nil)
}
