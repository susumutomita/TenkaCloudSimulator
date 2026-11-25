package aws

import (
	"encoding/xml"
	"fmt"
	"maps"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const snsReadAttributes = "GetSubscriptionAttributes"

func newSNSReadbackGateway(t *testing.T, db *core.Store) *Gateway {
	t.Helper()
	g := snsTestGateway(t, db)
	queues, err := NewSQSStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	g.Queues = queues
	return g
}

func snsReadbackDB(t *testing.T) *core.Store {
	t.Helper()
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func getSNSAttributes(t *testing.T, g *Gateway, arn, principal, region string, status int) *httptest.ResponseRecorder {
	t.Helper()
	fields := url.Values{"Action": {snsReadAttributes}, "Version": {"2010-03-31"}, "SubscriptionArn": {arn}}
	return snsWire(t, g, "POST", "/", fields.Encode(), map[string]string{
		"X-Simulator-Principal": principal,
		"X-Simulator-Region":    region,
		"Content-Type":          "application/x-www-form-urlencoded",
	}, status)
}

func checkSNSAttributes(t *testing.T, w *httptest.ResponseRecorder, want map[string]string) {
	t.Helper()
	var response struct {
		XMLName    xml.Name
		Attributes []snsAttributeEntry `xml:"GetSubscriptionAttributesResult>Attributes>entry"`
		RequestID  string              `xml:"ResponseMetadata>RequestId"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.XMLName != (xml.Name{Space: snsNamespace, Local: "GetSubscriptionAttributesResponse"}) ||
		!strings.HasPrefix(response.RequestID, "sns-local-") ||
		w.Header().Get("Content-Type") != "text/xml" ||
		w.Header().Get("X-Simulator-IAM-Decision") != "allowed" {
		t.Fatalf("wrong GetSubscriptionAttributes response shape: %+v", response)
	}
	got := make(map[string]string, len(response.Attributes))
	for _, e := range response.Attributes {
		if _, dup := got[e.Key]; dup {
			t.Fatalf("duplicate key %q", e.Key)
		}
		got[e.Key] = e.Value
	}
	if !maps.Equal(got, want) {
		t.Fatalf("attributes = %+v, want %+v", got, want)
	}
}

func requireSNSAttributesError(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body struct {
		XMLName xml.Name
		Code    string `xml:"Error>Code"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Code != want ||
		body.XMLName != (xml.Name{Space: snsNamespace, Local: "ErrorResponse"}) ||
		w.Header().Get("Content-Type") != "application/xml" {
		t.Fatalf("wrong %s response: %s (%v)", want, w.Body.String(), err)
	}
}

func TestSNSGetSubscriptionAttributesReadbackLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	g := newSNSReadbackGateway(t, db)
	arn := snsARN("us-east-1", "readback")
	const endpoint = "https://127.0.0.1:18443/events?one=1&two=2"
	snsCall(t, g, "", url.Values{"Action": {"CreateTopic"}, "Name": {"readback"}}, 200, arn)
	reply := snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {arn}, "Protocol": {"https"}, "Endpoint": {endpoint}, "ReturnSubscriptionArn": {"true"}}, 200, "SubscribeResult")
	var response struct {
		ARN string `xml:"SubscribeResult>SubscriptionArn"`
	}
	if err = xml.Unmarshal([]byte(reply), &response); err != nil || !strings.HasPrefix(response.ARN, arn+":") {
		t.Fatalf("invalid Subscribe response %q: %v", reply, err)
	}
	want := map[string]string{
		"SubscriptionArn": response.ARN, "TopicArn": arn, "Owner": "000000000000",
		"Protocol": "https", "Endpoint": endpoint, "ConfirmationWasAuthenticated": "false",
		"PendingConfirmation": "false", "RawMessageDelivery": "false",
	}
	checkSNSAttributes(t, getSNSAttributes(t, g, response.ARN, "", "us-east-1", 200), want)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	g = newSNSReadbackGateway(t, reopened)
	checkSNSAttributes(t, getSNSAttributes(t, g, response.ARN, "", "us-east-1", 200), want)
	snsCall(t, g, "", url.Values{"Action": {"Publish"}, "TopicArn": {arn}, "Message": {"local only"}}, 200, "MessageId")
	snsCall(t, g, "", url.Values{"Action": {"Unsubscribe"}, "SubscriptionArn": {response.ARN}}, 200, "UnsubscribeResponse")
	if retained, err := g.SNS.DeliveryCount(response.ARN); err != nil || retained != 1 {
		t.Fatalf("precondition: delivery history should still exist, got %d, %v", retained, err)
	}
	requireSNSAttributesError(t, getSNSAttributes(t, g, response.ARN, "", "us-east-1", 404), "NotFound")

	reply = snsCall(t, g, "", url.Values{"Action": {"Subscribe"}, "TopicArn": {arn}, "Protocol": {"https"}, "Endpoint": {endpoint}}, 200, "SubscribeResult")
	old := response.ARN
	if err = xml.Unmarshal([]byte(reply), &response); err != nil || response.ARN == old {
		t.Fatalf("recreated subscription must have a fresh ARN: %s %v", reply, err)
	}
	snsCall(t, g, "", url.Values{"Action": {"DeleteTopic"}, "TopicArn": {arn}}, 200, "DeleteTopicResponse")
	requireSNSAttributesError(t, getSNSAttributes(t, g, response.ARN, "", "us-east-1", 404), "NotFound")
}

func TestSNSGetSubscriptionAttributesScopedAuthorization(t *testing.T) {
	db := snsReadbackDB(t)
	g := newSNSReadbackGateway(t, db)
	makeSubscription := func(name string) SNSSubscription {
		t.Helper()
		topic, _, err := g.SNS.CreateTopic("us-east-1", name, "", false)
		if err != nil {
			t.Fatal(err)
		}
		sub, err := g.SNS.Subscribe(topic, "https", "https://127.0.0.1:18443/"+name)
		if err != nil {
			t.Fatal(err)
		}
		return sub
	}
	owned, peer := makeSubscription("owned"), makeSubscription("peer")
	grant := func(name, policy string) {
		t.Helper()
		p, err := core.ParsePolicy([]byte(policy))
		if err != nil {
			t.Fatal(err)
		}
		if err = g.Identities.Set(name, p); err != nil {
			t.Fatal(err)
		}
	}
	grant("reader", fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sns:GetSubscriptionAttributes","Resource":%q}]}`, owned.TopicARN))
	grant("wrong-resource", fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sns:GetSubscriptionAttributes","Resource":%q}]}`, owned.ARN))
	grant("denied", fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sns:GetSubscriptionAttributes","Resource":"*"},{"Effect":"Deny","Action":"sns:GetSubscriptionAttributes","Resource":%q}]}`, owned.TopicARN))
	grant("list-only", fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sns:ListSubscriptionsByTopic","Resource":%q}]}`, owned.TopicARN))

	getSNSAttributes(t, g, owned.ARN, "reader", "us-east-1", 200)
	requireSNSAttributesError(t, getSNSAttributes(t, g, owned.ARN, "reader", "us-west-2", 404), "NotFound")
	for _, principal := range []string{"wrong-resource", "denied", "list-only"} {
		requireSNSAttributesError(t, getSNSAttributes(t, g, owned.ARN, principal, "us-east-1", 403), "AuthorizationError")
	}
	unknownUUID := "80289ba6-0fd4-4079-afb4-ce8c8260f0ca"
	requireSNSAttributesError(t, getSNSAttributes(t, g, owned.TopicARN+":"+unknownUUID, "reader", "us-east-1", 404), "NotFound")
	for _, arn := range []string{peer.ARN, peer.TopicARN + ":" + unknownUUID} {
		w := getSNSAttributes(t, g, arn, "reader", "us-east-1", 403)
		requireSNSAttributesError(t, w, "AuthorizationError")
		if strings.Contains(w.Body.String(), peer.Endpoint) {
			t.Fatal("unauthorized subscription values were exposed")
		}
	}
	isolated := snsReadbackDB(t)
	requireSNSAttributesError(t, getSNSAttributes(t, newSNSReadbackGateway(t, isolated), owned.ARN, "", "us-east-1", 404), "NotFound")
}

func TestSNSGetSubscriptionAttributesInputAndStorageFailures(t *testing.T) {
	db := snsReadbackDB(t)
	g := newSNSReadbackGateway(t, db)
	topic, _, err := g.SNS.CreateTopic("us-east-1", "valid", "", false)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := g.SNS.Subscribe(topic, "https", "https://127.0.0.1:18443/read")
	if err != nil {
		t.Fatal(err)
	}
	for _, arn := range []string{"", topic.ARN, topic.ARN + ":bad", sub.ARN + ":extra", strings.Replace(sub.ARN, "000000000000", "123456789012", 1), strings.Repeat("a", 513)} {
		requireSNSAttributesError(t, getSNSAttributes(t, g, arn, "", "us-east-1", 400), "InvalidParameter")
	}
	for _, extra := range []url.Values{
		{"SubscriptionArn": {sub.ARN, sub.ARN}},
		{"SubscriptionArn": {sub.ARN}, "TopicArn": {topic.ARN}},
		{"SubscriptionArn": {sub.ARN}, "Attributes.entry.1.key": {"RawMessageDelivery"}, "Attributes.entry.1.value": {"true"}},
	} {
		extra.Set("Action", snsReadAttributes)
		extra.Set("Version", "2010-03-31")
		requireSNSAttributesError(t, snsWire(t, g, "POST", "/", extra.Encode(), nil, 400), "ValidationError")
	}
	set := url.Values{"Action": {"SetSubscriptionAttributes"}, "Version": {"2010-03-31"}, "SubscriptionArn": {sub.ARN}, "AttributeName": {"RawMessageDelivery"}, "AttributeValue": {"true"}}
	requireSNSAttributesError(t, snsWire(t, g, "POST", "/", set.Encode(), nil, 400), "InvalidAction")
	snsSetAuthorizer(t, db.Database(), func(op sqlite3.AuthorizerActionCode, table, _, _, _ string) sqlite3.AuthorizerReturnCode {
		if op == sqlite3.AUTH_READ && table == "aws_sns_subscriptions" {
			return sqlite3.AUTH_DENY
		}
		return sqlite3.AUTH_OK
	})
	w := getSNSAttributes(t, g, sub.ARN, "", "us-east-1", 500)
	snsSetAuthorizer(t, db.Database(), nil)
	requireSNSAttributesError(t, w, "InternalError")
	if strings.Contains(w.Body.String(), "aws_sns_subscriptions") {
		t.Fatal("raw SQLite failure was exposed")
	}
	getSNSAttributes(t, g, sub.ARN, "", "us-east-1", 200)
}
