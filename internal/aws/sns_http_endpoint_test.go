package aws

import (
	"net/url"
	"testing"
)

func TestSNSHTTPSubscriptionEndpointAdmission(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	const topic = "arn:aws:sns:us-east-1:000000000000:webhooks"
	snsCall(t, g, "", url.Values{"Action": {"CreateTopic"}, "Name": {"webhooks"}}, 200, topic)

	subscribe := func(t *testing.T, protocol, endpoint string, status int, expected string) {
		t.Helper()
		snsCall(t, g, "", url.Values{
			"Action": {"Subscribe"}, "TopicArn": {topic},
			"Protocol": {protocol}, "Endpoint": {endpoint},
		}, status, expected)
	}

	for _, tc := range []struct{ name, protocol, endpoint string }{
		{"relative", "https", "/notifications"},
		{"http-for-https", "https", "http://127.0.0.1/notifications"},
		{"https-for-http", "http", "https://127.0.0.1/notifications"},
		{"missing-host", "https", "https:///notifications"},
		{"port-without-host", "http", "http://:8080/notifications"},
		{"malformed-escape", "http", "http://127.0.0.1/%zz"},
		{"unescaped-space", "https", "https://127.0.0.1/my notifications"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subscribe(t, tc.protocol, tc.endpoint, 400, "<Code>InvalidParameter</Code>")
		})
	}
	subs, err := g.SNS.ListSubscriptionsByTopic(t.Context(), topic)
	if err != nil || len(subs) != 0 {
		t.Fatalf("rejected endpoints must not create subscriptions: %+v, %v", subs, err)
	}

	// The endpoints are only stored. No listener, DNS, or outbound delivery is needed.
	want := map[string]string{
		"http":  "http://127.0.0.1:8080/my%20notifications?team=blue",
		"https": "https://[::1]:8443/notifications",
	}
	for protocol, endpoint := range want {
		subscribe(t, protocol, endpoint, 200, "<SubscriptionArn>")
		subscribe(t, protocol, endpoint, 200, "<SubscriptionArn>")
	}
	subs, err = g.SNS.ListSubscriptionsByTopic(t.Context(), topic)
	if err != nil || len(subs) != len(want) {
		t.Fatalf("expected one subscription per distinct endpoint: %+v, %v", subs, err)
	}
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topic}, "Message": {"local only"}}, 200, "<MessageId>")
	for _, sub := range subs {
		if endpoint, ok := want[sub.Protocol]; !ok || sub.Endpoint != endpoint {
			t.Fatalf("unexpected persisted endpoint: %+v", sub)
		}
		delete(want, sub.Protocol)
		if n, err := g.SNS.DeliveryCount(sub.ARN); err != nil || n != 1 {
			t.Fatalf("expected exactly one local delivery record, got %d, %v", n, err)
		}
		snsCall(t, g, "", url.Values{"Action": {"Unsubscribe"}, "SubscriptionArn": {sub.ARN}}, 200, "<UnsubscribeResponse")
	}
	snsCall(t, g, "", url.Values{"Action": {"DeleteTopic"}, "TopicArn": {topic}}, 200, "<DeleteTopicResponse")
	snsCall(t, g, "", url.Values{"Action": {"GetTopicAttributes"}, "TopicArn": {topic}}, 404, "<Code>NotFound</Code>")
}
