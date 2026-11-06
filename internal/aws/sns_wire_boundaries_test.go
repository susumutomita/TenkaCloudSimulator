package aws

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func snsWire(t *testing.T, g *Gateway, method, path, body string, headers map[string]string, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Simulator-Service", "sns")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: %d want%d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func TestSNSGlobalTopicAndSubscriptionCapsPermitIdempotentRetries(t *testing.T) {
	t.Run("topics", func(t *testing.T) {
		db, _ := s3TestState(t)
		g := snsTestGateway(t, db)
		_, err := db.Database().Exec("WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO aws_sns_topics SELECT 'us-east-1',printf('limit-%04d',x),printf('arn:aws:sns:us-east-1:000000000000:limit-%04d',x),'',0,0 FROM n")
		if err != nil {
			t.Fatal(err)
		}
		snsWire(t, g, "POST", "/", "Action=CreateTopic&Version=2010-03-31&Name=new", map[string]string{"X-Simulator-Region": "us-west-2"}, 409)
		snsWire(t, g, "POST", "/", "Action=CreateTopic&Version=2010-03-31&Name=limit-0001", nil, 200)
		if _, err = db.Database().Exec("INSERT INTO aws_sns_topics VALUES('us-west-2','overflow','arn:aws:sns:us-west-2:000000000000:overflow','',0,0)"); err != nil {
			t.Fatal(err)
		}
		if _, err = NewSNSStore(db.Database()); err == nil {
			t.Fatal("over-budget persisted state opened")
		}
	})
	t.Run("subscriptions", func(t *testing.T) {
		db, _ := s3TestState(t)
		g := snsTestGateway(t, db)
		topic, _, err := g.SNS.CreateTopic("us-east-1", "base", "", false)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Database().Exec("WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO aws_sns_subscriptions SELECT printf('sub%05d',x),?,'email',printf('mail%d@example.com',x),0 FROM n", topic.ARN)
		if err != nil {
			t.Fatal(err)
		}
		q := url.Values{"Action": {"Subscribe"}, "Version": {"2010-03-31"}, "TopicArn": {topic.ARN}, "Protocol": {"email"}, "Endpoint": {"new@example.com"}}
		snsWire(t, g, "POST", "/", q.Encode(), nil, 409)
		q.Set("Endpoint", "mail1@example.com")
		snsWire(t, g, "POST", "/", q.Encode(), nil, 200)
	})
}

func TestSNSIdentityReadFailureCannotBecomeAnAllow(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	p, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sns:ListTopics","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Identities.Set("reader", p); err != nil {
		t.Fatal(err)
	}
	snsSetAuthorizer(t, db.Database(), func(action sqlite3.AuthorizerActionCode, table, _, _, _ string) sqlite3.AuthorizerReturnCode {
		if action == sqlite3.AUTH_READ && table == "identity_policies" {
			return sqlite3.AUTH_DENY
		}
		return sqlite3.AUTH_OK
	})
	w := snsWire(t, g, "POST", "/", "Action=ListTopics&Version=2010-03-31", map[string]string{"X-Simulator-Principal": "reader"}, 500)
	snsSetAuthorizer(t, db.Database(), nil)
	if strings.Contains(w.Body.String(), "identity_policies") {
		t.Fatal("private database error leaked")
	}
}

type snsCheckFailure struct{}

func (snsCheckFailure) QueueExists(string, string) (bool, error) {
	return false, errors.New("fixture queue read failure")
}
func (snsCheckFailure) DeliverToQueue(string, string, string) error {
	return errors.New("unexpected delivery")
}

func TestSNSDisabledAdapterSeamFailsClosedOnBadStoredEndpoint(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	topic, _, err := g.SNS.CreateTopic("us-east-1", "base", "", false)
	if err != nil {
		t.Fatal(err)
	}
	g.QueueFanout = snsCheckFailure{}
	q := url.Values{"Action": {"Subscribe"}, "Version": {"2010-03-31"}, "TopicArn": {topic.ARN}, "Protocol": {"sqs"}, "Endpoint": {"bad-arn"}}
	snsWire(t, g, "POST", "/", q.Encode(), nil, 400)
	q.Set("Endpoint", "arn:aws:sqs:us-east-1:000000000000:q")
	snsWire(t, g, "POST", "/", q.Encode(), nil, 500)
	for _, endpoint := range []string{"bad-arn", "arn:aws:sqs:us-west-2:000000000000:q"} {
		sub, err := g.SNS.Subscribe(topic, "sqs", endpoint)
		if err != nil {
			t.Fatal(err)
		}
		publish := url.Values{"Action": {"Publish"}, "Version": {"2010-03-31"}, "TopicArn": {topic.ARN}, "Message": {"new"}}
		snsWire(t, g, "POST", "/", publish.Encode(), nil, 500)
		if err = g.SNS.Unsubscribe(sub.ARN); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := g.SNS.ListMessages(context.Background(), topic.ARN)
	if err != nil || len(msgs) != 0 {
		t.Fatal("failed seam recorded success", msgs, err)
	}
	g.QueueFanout = &stubQueueFanout{queues: map[string]bool{"us-east-1/q": true}, delivered: map[string][]string{}}
	if _, err = g.SNS.Subscribe(topic, "email", "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	q.Set("Endpoint", "arn:aws:sqs:us-east-1:000000000000:q")
	snsWire(t, g, "POST", "/", q.Encode(), nil, 200)
	snsWire(t, g, "POST", "/", url.Values{"Action": {"Publish"}, "Version": {"2010-03-31"}, "TopicArn": {topic.ARN}, "Message": {"no subject"}}.Encode(), nil, 200)
	fifo, _, err := g.SNS.CreateTopic("us-east-1", "metadata.fifo", "", true)
	if err != nil {
		t.Fatal(err)
	}
	snsWire(t, g, "POST", "/", url.Values{"Action": {"GetTopicAttributes"}, "Version": {"2010-03-31"}, "TopicArn": {fifo.ARN}}.Encode(), nil, 200)
}

func TestSNSProtocolBoundaryFailuresLeaveStateUnchanged(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	base := "Action=CreateTopic&Version=2010-03-31&Name=unintended"
	for _, tt := range []struct {
		name, method, path, body string
		headers                  map[string]string
		status                   int
	}{
		{"wrong path", "POST", "/object", base, nil, 400},
		{"query", "POST", "/?Version=2010-03-31", base, nil, 400},
		{"method", "GET", "/", base, nil, 400},
		{"JSON target", "POST", "/", base, map[string]string{"X-Amz-Target": "Other.Service"}, 400},
		{"session", "POST", "/", base, map[string]string{"X-Amz-Security-Token": "dummy"}, 403},
		{"real credential", "POST", "/", base, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=REAL/20261106/us-east-1/sns/aws4_request, SignedHeaders=host, Signature=dummy"}, 403},
		{"region", "POST", "/", base, map[string]string{"X-Simulator-Region": "unavailable"}, 400},
		{"region mismatch", "POST", "/", base, map[string]string{"X-Simulator-Region": "us-west-2", "Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261106/us-east-1/sns/aws4_request, SignedHeaders=host, Signature=dummy"}, 400},
		{"oversized ingress", "POST", "/", strings.Repeat("x", 1<<20+1), nil, 413},
		{"malformed form", "POST", "/", base + "&broken=%xx", nil, 400},
		{"wrong version", "POST", "/", "Action=CreateTopic&Version=invalid&Name=unintended", nil, 400},
		{"unknown action", "POST", "/", "Action=Unknown&Version=2010-03-31", nil, 400},
		{"unsupported field", "POST", "/", base + "&Unknown=ignored", nil, 400},
		{"attributes on publish", "POST", "/", "Action=Publish&Version=2010-03-31&Attributes.entry.1.key=DisplayName&Attributes.entry.1.value=ignored", nil, 400},
		{"duplicate attribute", "POST", "/", base + "&Attributes.entry.1.key=DisplayName&Attributes.entry.1.value=a&Attributes.entry.2.key=DisplayName&Attributes.entry.2.value=b", nil, 400},
		{"invalid FIFO boolean", "POST", "/", base + "&Attributes.entry.1.key=FifoTopic&Attributes.entry.1.value=perhaps", nil, 400},
		{"long DisplayName", "POST", "/", base + "&Attributes.entry.1.key=DisplayName&Attributes.entry.1.value=" + strings.Repeat("x", 101), nil, 400},
	} {
		t.Run(tt.name, func(t *testing.T) { snsWire(t, g, tt.method, tt.path, tt.body, tt.headers, tt.status) })
	}
	topics, err := g.SNS.ListTopics(context.Background(), "us-east-1")
	if err != nil || len(topics) != 0 {
		t.Fatal("rejected request changed topic state", topics, err)
	}
	for _, missing := range []*Gateway{{SNS: g.SNS}, {Identities: g.Identities}} {
		snsWire(t, missing, "POST", "/", base, nil, 500)
	}
}

type snsReadFailure struct{}

func (snsReadFailure) Read([]byte) (int, error) { return 0, errors.New("private transport detail") }
func (snsReadFailure) Close() error             { return nil }
func TestSNSBrokenBodyDoesNotClaimOversizedOrSuccess(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	r := httptest.NewRequest("POST", "/", nil)
	r.Body = snsReadFailure{}
	r.Header.Set("X-Simulator-Service", "sns")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 400 || strings.Contains(w.Body.String(), "private transport detail") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestSNSNativeScopeAndExplicitS3Routing(t *testing.T) {
	db, s3 := s3TestState(t)
	g := snsTestGateway(t, db)
	g.S3 = s3
	auth := "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261106/us-east-1/sns/aws4_request, SignedHeaders=host, Signature=dummy"
	snsWire(t, g, "POST", "/", "Action=ListTopics&Version=2010-03-31", map[string]string{"X-Simulator-Service": "", "Authorization": auth}, 200)
	r := httptest.NewRequest("PUT", "/versions-test/literal-sns", strings.NewReader("original"))
	r.Header.Set("X-Simulator-Service", "s3")
	r.Header.Set("Authorization", auth)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "/versions-test/literal-sns", nil)
	r.Header.Set("X-Simulator-Service", "s3")
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "original" {
		t.Fatal(w.Code, w.Body.String())
	}
	topics, err := g.SNS.ListTopics(context.Background(), "us-east-1")
	if err != nil || len(topics) != 0 {
		t.Fatal(topics, err)
	}
}

func TestSNSPagingIsScopedAndSurvivesRemovedEarlierRows(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	for i := 0; i < 101; i++ {
		if _, _, err := g.SNS.CreateTopic("us-east-1", fmt.Sprintf("page-%03d", i), "", false); err != nil {
			t.Fatal(err)
		}
	}
	query := url.Values{"Action": {"ListTopics"}, "Version": {"2010-03-31"}}
	var first struct {
		Result snsTopicsResult `xml:"ListTopicsResult"`
	}
	if err := xml.Unmarshal(snsWire(t, g, "POST", "/", query.Encode(), nil, 200).Body.Bytes(), &first); err != nil || len(first.Result.Topics) != 100 || first.Result.NextToken == "" {
		t.Fatal(first, err)
	}
	query.Set("NextToken", first.Result.NextToken)
	snsWire(t, g, "POST", "/", query.Encode(), map[string]string{"X-Simulator-Region": "us-west-2"}, 400)
	for _, topic := range first.Result.Topics {
		if err := g.SNS.DeleteTopic(topic.TopicArn); err != nil {
			t.Fatal(err)
		}
	}
	var second struct {
		Result snsTopicsResult `xml:"ListTopicsResult"`
	}
	if err := xml.Unmarshal(snsWire(t, g, "POST", "/", query.Encode(), nil, 200).Body.Bytes(), &second); err != nil || len(second.Result.Topics) != 1 || second.Result.NextToken != "" {
		t.Fatal(second, err)
	}
	for _, token := range []string{"!", "SNS1.!", "SNS1." + base64.RawURLEncoding.EncodeToString([]byte("missing-separator")), "SNS1." + base64.RawURLEncoding.EncodeToString([]byte("ListTopics:us-east-1\x00")), strings.Repeat("x", 2049)} {
		query.Set("NextToken", token)
		snsWire(t, g, "POST", "/", query.Encode(), nil, 400)
	}
	topic, err := g.SNS.GetTopic(snsARN("us-east-1", "page-100"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 101; i++ {
		if _, err = g.SNS.Subscribe(topic, "email", fmt.Sprintf("mail%d@example.com", i)); err != nil {
			t.Fatal(err)
		}
	}
	subQuery := url.Values{"Action": {"ListSubscriptionsByTopic"}, "Version": {"2010-03-31"}, "TopicArn": {topic.ARN}}
	var page struct {
		Result snsSubscriptionsResult `xml:"ListSubscriptionsByTopicResult"`
	}
	if err = xml.Unmarshal(snsWire(t, g, "POST", "/", subQuery.Encode(), nil, 200).Body.Bytes(), &page); err != nil || len(page.Result.Subscriptions) != 100 || page.Result.NextToken == "" {
		t.Fatal(page, err)
	}
	subQuery.Set("NextToken", page.Result.NextToken)
	subQuery.Set("Action", "ListSubscriptions")
	subQuery.Del("TopicArn")
	snsWire(t, g, "POST", "/", subQuery.Encode(), nil, 400)
	subQuery.Set("Action", "ListSubscriptionsByTopic")
	subQuery.Set("TopicArn", topic.ARN)
	snsWire(t, g, "POST", "/", subQuery.Encode(), nil, 200)
	for _, operation := range []string{"ListTopics", "ListSubscriptions", "ListSubscriptionsByTopic"} {
		q := url.Values{"Action": {operation}, "Version": {"2010-03-31"}, "NextToken": {"!"}}
		if operation == "ListSubscriptionsByTopic" {
			q.Set("TopicArn", topic.ARN)
		}
		snsWire(t, g, "POST", "/", q.Encode(), nil, 400)
	}
}

func TestSNSMissingTopicAndSubscriptionInputsAreRefused(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	topic, _, err := g.SNS.CreateTopic("us-east-1", "base", "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, fields := range []url.Values{
		{"Action": {"Subscribe"}, "TopicArn": {topic.ARN}, "Protocol": {"unsupported"}, "Endpoint": {"bad"}},
		{"Action": {"Subscribe"}, "TopicArn": {topic.ARN}, "Protocol": {"email"}, "Endpoint": {""}},
		{"Action": {"Subscribe"}, "TopicArn": {topic.ARN}, "Protocol": {"lambda"}, "Endpoint": {"not-an-arn"}},
		{"Action": {"Publish"}, "TopicArn": {topic.ARN}, "Message": {strings.Repeat("x", 262145)}},
	} {
		fields.Set("Version", "2010-03-31")
		snsWire(t, g, "POST", "/", fields.Encode(), nil, 400)
	}
	for _, op := range []string{"Publish", "Subscribe", "DeleteTopic", "GetTopicAttributes", "ListSubscriptionsByTopic"} {
		fields := url.Values{"Action": {op}, "Version": {"2010-03-31"}, "TopicArn": {"arn:aws:sns:us-east-1:000000000000:missing"}}
		snsWire(t, g, "POST", "/", fields.Encode(), nil, 404)
	}
	snsWire(t, g, "POST", "/", "Action=Unsubscribe&Version=2010-03-31&SubscriptionArn=missing", nil, 404)
	snsWire(t, g, "POST", "/", "Action=GetTopicAttributes&Version=2010-03-31&TopicArn="+url.QueryEscape(topic.ARN), map[string]string{"X-Simulator-Region": "us-west-2"}, 404)
}
