package aws

import (
	"context"
	"encoding/xml"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSNSRejectedQueryCannotCreateOrPublish(t *testing.T) {
	tests := []struct {
		name   string
		fields url.Values
	}{
		{"DisplayName control", url.Values{"Action": {"CreateTopic"}, "Name": {"unintended"}, "Attributes.entry.1.key": {"DisplayName"}, "Attributes.entry.1.value": {"bad\x00name"}}},
		{"endpoint control", url.Values{"Action": {"Subscribe"}, "TopicArn": {snsARN("us-east-1", "base")}, "Protocol": {"email"}, "Endpoint": {"bad\x00endpoint"}}},
		{"duplicate action", url.Values{"Action": {"CreateTopic", "DeleteTopic"}, "Name": {"unintended"}}},
		{"duplicate version", url.Values{"Action": {"CreateTopic"}, "Version": {"2010-03-31", "invalid"}, "Name": {"unintended"}}},
		{"duplicate name", url.Values{"Action": {"CreateTopic"}, "Name": {"unintended", "other"}}},
		{"unsupported sparse map attribute", url.Values{"Action": {"CreateTopic"}, "Name": {"unintended"}, "Attributes.entry.2.key": {"Policy"}, "Attributes.entry.2.value": {"{}"}}},
		{"unknown map suffix", url.Values{"Action": {"CreateTopic"}, "Name": {"unintended"}, "Attributes.entry.1.extra": {"ignored"}}},
		{"unpaired sparse map value", url.Values{"Action": {"CreateTopic"}, "Name": {"unintended"}, "Attributes.entry.2.value": {"ignored"}}},
		{"noncanonical map index", url.Values{"Action": {"CreateTopic"}, "Name": {"unintended"}, "Attributes.entry.01.key": {"Policy"}, "Attributes.entry.01.value": {"{}"}}},
		{"duplicate message", url.Values{"Action": {"Publish"}, "TopicArn": {snsARN("us-east-1", "base")}, "Message": {"one", "two"}}},
		{"invalid subscription boolean", url.Values{"Action": {"Subscribe"}, "TopicArn": {snsARN("us-east-1", "base")}, "Protocol": {"email"}, "Endpoint": {"ops@example.com"}, "ReturnSubscriptionArn": {"perhaps"}}},
		{"subject newline", url.Values{"Action": {"Publish"}, "TopicArn": {snsARN("us-east-1", "base")}, "Message": {"test"}, "Subject": {"hello\nworld"}}},
		{"subject control", url.Values{"Action": {"Publish"}, "TopicArn": {snsARN("us-east-1", "base")}, "Message": {"test"}, "Subject": {"hello\x00world"}}},
		{"subject invalid utf8", url.Values{"Action": {"Publish"}, "TopicArn": {snsARN("us-east-1", "base")}, "Message": {"test"}, "Subject": {string([]byte{0xff})}}},
		{"subject hundred characters", url.Values{"Action": {"Publish"}, "TopicArn": {snsARN("us-east-1", "base")}, "Message": {"test"}, "Subject": {strings.Repeat("a", 100)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, _ := s3TestState(t)
			g := snsTestGateway(t, db)
			if _, _, err := g.SNS.CreateTopic("us-east-1", "base", "", false); err != nil {
				t.Fatal(err)
			}
			if !tt.fields.Has("Version") {
				tt.fields.Set("Version", "2010-03-31")
			}
			r := httptest.NewRequest("POST", "/", strings.NewReader(tt.fields.Encode()))
			r.Header.Set("X-Simulator-Service", "sns")
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Errorf("expected rejection, got %d %s", w.Code, w.Body.String())
			}
			for table, want := range map[string]int{"aws_sns_topics": 1, "aws_sns_subscriptions": 0, "aws_sns_messages": 0, "aws_sns_deliveries": 0} {
				var count int
				if err := db.Database().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != want {
					t.Errorf("rejected query changed %s: %d want%d: %v", table, count, want, err)
				}
			}
		})
	}
}

func TestSNSSparseSupportedAttributesAndUnicodeSubject(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	snsCall(t, g, "", url.Values{"Action": {"CreateTopic"}, "Name": {"sparse"}, "Attributes.entry.2.key": {"DisplayName"}, "Attributes.entry.2.value": {"Expected"}}, 200, "<TopicArn>")
	topic, err := g.SNS.GetTopic(snsARN("us-east-1", "sparse"))
	if err != nil || topic.DisplayName != "Expected" {
		t.Fatalf("supported sparse attribute was discarded: %+v %v", topic, err)
	}
	subject := strings.Repeat("日", 99)
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {topic.ARN}, "Message": {"test"}, "Subject": {subject}}, 200, "<MessageId>")
	messages, err := g.SNS.ListMessages(context.Background(), topic.ARN)
	if err != nil || len(messages) != 1 || messages[0].Subject != subject {
		t.Fatal(messages, err)
	}
}

func TestSNSUnsubscribeCannotCrossSelectedRegion(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	topic, _, err := g.SNS.CreateTopic("us-east-1", "regional", "", false)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := g.SNS.Subscribe(topic, "email", "ops@example.com")
	if err != nil {
		t.Fatal(err)
	}
	fields := url.Values{"Action": {"Unsubscribe"}, "Version": {"2010-03-31"}, "SubscriptionArn": {sub.ARN}}
	r := httptest.NewRequest("POST", "/", strings.NewReader(fields.Encode()))
	r.Header.Set("X-Simulator-Service", "sns")
	r.Header.Set("X-Simulator-Region", "us-west-2")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Errorf("cross-region unsubscribe returned %d %s", w.Code, w.Body.String())
	}
	if _, err = g.SNS.GetSubscription(sub.ARN); err != nil {
		t.Fatalf("cross-region request removed subscription: %v", err)
	}
}

func TestSNSCredentialScopeMustMatchService(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	// Exercise the SNS handler contract directly: the older shared IAM dispatcher
	// also recognizes an IAM signing scope before consulting the SNS selector.
	for _, scope := range []string{"TCSIMLOCAL/20261106/us-east-1/iam/aws4_request", "TCSIMLOCAL/20261106/us-east-1/sns/invalid", "TCSIMLOCAL/20261106/us-east-1/sns/aws4_request/extra", "TCSIMLOCAL"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader("Action=CreateTopic&Version=2010-03-31&Name=unintended"))
		r.Header.Set("X-Simulator-Service", "sns")
		r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+scope+", SignedHeaders=host, Signature=dummy")
		w := httptest.NewRecorder()
		g.serveSNS(w, r)
		if w.Code != 403 {
			t.Errorf("wrong scope %q: %d %s", scope, w.Code, w.Body.String())
		}
	}
	topics, err := g.SNS.ListTopics(context.Background(), "us-east-1")
	if err != nil || len(topics) != 0 {
		t.Fatalf("invalid credential scope mutated topics: %+v %v", topics, err)
	}
}

func TestSNSFIFOCannotPublishWithoutImplementedSemantics(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	topic, _, err := g.SNS.CreateTopic("us-east-1", "events.fifo", "", true)
	if err != nil {
		t.Fatal(err)
	}
	fields := url.Values{"Action": {"Publish"}, "Version": {"2010-03-31"}, "TopicArn": {topic.ARN}, "Message": {"test"}}
	r := httptest.NewRequest("POST", "/", strings.NewReader(fields.Encode()))
	r.Header.Set("X-Simulator-Service", "sns")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	var result struct {
		Code string `xml:"Error>Code"`
	}
	if w.Code != 400 || xml.Unmarshal(w.Body.Bytes(), &result) != nil || result.Code != "InvalidParameterException" {
		t.Errorf("FIFO publish must explicitly refuse unsupported semantics: %d %s", w.Code, w.Body.String())
	}
	messages, err := g.SNS.ListMessages(context.Background(), topic.ARN)
	if err != nil || len(messages) != 0 {
		t.Fatalf("unsupported FIFO publish persisted success: %+v %v", messages, err)
	}
}
