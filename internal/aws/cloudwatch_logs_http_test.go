package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func logsHTTP(s *CloudWatchLogsStore, op, body string, auth func(string, string) error) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "Logs_20140328."+op)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r, "us-east-1", auth)
	return w
}

func TestLogsNativeIAMAllowExplicitDenyAndContinuationReauthorization(t *testing.T) {
	db, s := newLogsTest(t)
	seedLogs(t, s, "group", "stream")
	now := s.now().UnixMilli()
	_, err := s.PutEvents(context.Background(), "us-east-1", "group", "stream", []LogsInputEvent{{now, "one"}, {now + 1, "two"}})
	logsMust(t, err)
	ids := core.NewIdentities(db)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"logs:GetLogEvents","Resource":"arn:aws:logs:us-east-1:000000000000:log-group:group:log-stream:stream"}]}`))
	logsMust(t, err)
	logsMust(t, ids.Set("reader", policy))
	auth := func(action, resource string) error {
		decision, err := ids.DecisionContext(context.Background(), "reader", action, resource)
		if err != nil {
			return err
		}
		if decision != "allowed" {
			return ssmFailure{"AccessDeniedException", decision}
		}
		return nil
	}
	w := logsHTTP(s, "GetLogEvents", `{"logGroupName":"group","logStreamName":"stream","startFromHead":true,"limit":1}`, auth)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var page LogsEventsPage
	logsMust(t, json.Unmarshal(w.Body.Bytes(), &page))
	policy, err = core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"logs:*","Resource":"*"},{"Effect":"Deny","Action":"logs:GetLogEvents","Resource":"*"}]}`))
	logsMust(t, err)
	logsMust(t, ids.Set("reader", policy))
	w = logsHTTP(s, "GetLogEvents", fmt.Sprintf(`{"logGroupName":"group","logStreamName":"stream","startFromHead":true,"nextToken":%q}`, page.Forward), auth)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "explicitDeny") {
		t.Fatal("cursor bypassed changed IAM", w.Code, w.Body.String())
	}
}
func TestLogsNativeHTTPContractAndSequenceTokenIgnored(t *testing.T) {
	_, s := newLogsTest(t)
	var actions []string
	auth := func(action, resource string) error { actions = append(actions, action+" "+resource); return nil }
	for _, test := range []struct{ op, body string }{{"CreateLogGroup", `{"logGroupName":"group"}`}, {"CreateLogStream", `{"logGroupName":"group","logStreamName":"stream"}`}, {"PutLogEvents", fmt.Sprintf(`{"logGroupName":"group","logStreamName":"stream","sequenceToken":"intentionally-invalid","logEvents":[{"timestamp":%d,"message":"saved"}]}`, s.now().UnixMilli())}} {
		w := logsHTTP(s, test.op, test.body, auth)
		if w.Code != 200 {
			t.Fatal(test.op, w.Code, w.Body.String())
		}
	}
	w := logsHTTP(s, "GetLogEvents", `{"logGroupName":"group","logStreamName":"stream","startFromHead":true}`, auth)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var page LogsEventsPage
	logsMust(t, json.Unmarshal(w.Body.Bytes(), &page))
	if len(page.Events) != 1 || page.Events[0].Message != "saved" {
		t.Fatal(page)
	}
	w = logsHTTP(s, "DescribeLogGroups", `{}`, auth)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"logGroupName":"group"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = logsHTTP(s, "DescribeLogStreams", `{"logGroupName":"group"}`, auth)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"logStreamName":"stream"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if actions[0] != "logs:CreateLogGroup arn:aws:logs:us-east-1:000000000000:log-group:group" || actions[1] != "logs:CreateLogStream arn:aws:logs:us-east-1:000000000000:log-group:group:log-stream:stream" || actions[4] != "logs:DescribeLogGroups *" {
		t.Fatal(actions)
	}
	w = logsHTTP(s, "DeleteLogGroup", `{"logGroupName":"group"}`, auth)
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = logsHTTP(s, "GetLogEvents", `{"logGroupName":"group","logStreamName":"stream"}`, auth)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "ResourceNotFoundException") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestLogsHTTPRejectsPartialRepresentationAndMalformedRequests(t *testing.T) {
	_, s := newLogsTest(t)
	calls := 0
	auth := func(string, string) error { calls++; return nil }
	cases := []struct{ op, body string }{
		{"CreateLogGroup", `{"logGroupName":"group","tags":{}}`}, {"CreateLogGroup", `{"logGroupName":"group","kmsKeyId":"key"}`}, {"CreateLogGroup", `{"logGroupName":"group","logGroupClass":"DELIVERY"}`},
		{"CreateLogGroup", `{"logGroupName":"one","logGroupName":"two"}`}, {"CreateLogGroup", `{"logGroupName":null}`}, {"CreateLogGroup", `{"LogGroupName":"wrong-case"}`}, {"CreateLogGroup", `[]`}, {"CreateLogGroup", `{} {}`}, {"CreateLogGroup", `{"logGroupName":true}`},
		{"GetLogEvents", `{"logGroupName":"group","logStreamName":"stream","unmask":true}`}, {"GetLogEvents", `{"logGroupIdentifier":"arn:aws:logs:us-east-1:000000000000:log-group:group","logStreamName":"stream"}`},
		{"PutLogEvents", `{"logGroupName":"group","logStreamName":"stream","entity":{},"logEvents":[]}`}, {"PutLogEvents", `{"logGroupName":"group","logStreamName":"stream","logEvents":[{"message":"timestamp-missing"}]}`}, {"PutLogEvents", `{"logGroupName":"group","logStreamName":"stream","logEvents":[{"timestamp":1,"message":"extra","unknown":1}]}`},
	}
	for _, test := range cases {
		w := logsHTTP(s, test.op, test.body, auth)
		if w.Code != 400 {
			t.Fatal(test.op, test.body, w.Code, w.Body.String())
		}
	}
	if calls != 0 {
		t.Fatal("malformed requests reached state authorization", calls)
	}
	w := logsHTTP(s, "PutRetentionPolicy", `{}`, auth)
	if w.Code != 501 {
		t.Fatal(w.Code, w.Body.String())
	}
	groups, _, err := s.Groups(context.Background(), "us-east-1", "", "", 50)
	logsMust(t, err)
	if len(groups) != 0 {
		t.Fatal("invalid request provisioned state", groups)
	}
}
func TestLogsHTTPAuthorityAndInfrastructureFailuresRemainDistinct(t *testing.T) {
	db, s := newLogsTest(t)
	for _, test := range []struct {
		name   string
		auth   func(string, string) error
		status int
	}{
		{"explicit-deny", func(string, string) error { return ssmFailure{"AccessDeniedException", "IAM evaluation: explicitDeny"} }, 403},
		{"policy-database", func(string, string) error { return errors.New("sensitive database path") }, 500},
		{"unconfigured", nil, 500},
	} {
		w := logsHTTP(s, "CreateLogGroup", `{"logGroupName":"group"}`, test.auth)
		if w.Code != test.status || strings.Contains(w.Body.String(), "sensitive") {
			t.Fatal(test.name, w.Code, w.Body.String())
		}
	}
	for _, credential := range []string{"AWS4-HMAC-SHA256 Credential=REAL/20261104/us-east-1/logs/aws4_request, Signature=x", "AWS4-HMAC-SHA256 Credential=TCSIM/20261104/us-east-1/ssm/aws4_request, Signature=x", "AWS4-HMAC-SHA256 Credential=TCSIM/20261104/us-west-2/logs/aws4_request, Signature=x"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"logGroupName":"group"}`))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "Logs_20140328.CreateLogGroup")
		r.Header.Set("Authorization", credential)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r, "us-east-1", func(string, string) error { return nil })
		if w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	logsMust(t, db.Close())
	w := logsHTTP(s, "CreateLogGroup", `{"logGroupName":"group"}`, func(string, string) error { return nil })
	if w.Code != 500 || strings.Contains(w.Body.String(), "closed") {
		t.Fatal("state failure masked as denial or leaked details", w.Code, w.Body.String())
	}
}
