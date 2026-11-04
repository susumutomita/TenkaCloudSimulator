package aws

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogsRejectedTransportAndNativeContractsPreserveExistingGraph(t *testing.T) {
	ctx := context.Background()
	_, s := newLogsTest(t)
	seedLogs(t, s, "group", "stream")
	now := s.now().UnixMilli()
	_, err := s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now, "preserved"}})
	logsMust(t, err)
	allowed := func(string, string) error { return nil }
	for _, body := range []string{``, `{"logGroupName":`, `{"logGroupName":"group","logStreamName":"stream","logEvents":[null]}`, strings.Repeat("[", 34) + strings.Repeat("]", 34), string([]byte{'{', '"', 'x', '"', ':', '"', 255, '"', '}'})} {
		w := logsHTTP(s, "PutLogEvents", body, allowed)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, test := range []struct{ op, body string }{
		{"CreateLogGroup", `{"logGroupName":""}`}, {"CreateLogGroup", `{"logGroupName":"aws/reserved"}`}, {"CreateLogGroup", `{"logGroupName":"group","logGroupClass":""}`},
		{"CreateLogStream", `{"logGroupName":"group","logStreamName":"bad:*"}`}, {"CreateLogStream", `{"logGroupName":"missing","logStreamName":"stream"}`}, {"CreateLogStream", `{"logGroupName":"group","logStreamName":"stream"}`},
		{"DescribeLogGroups", `{"limit":0}`}, {"DescribeLogGroups", `{"nextToken":"malformed"}`}, {"DescribeLogGroups", `{"logGroupNamePrefix":"*"}`},
		{"DescribeLogStreams", `{"logGroupName":"missing"}`}, {"DescribeLogStreams", `{"logGroupName":"group","limit":51}`}, {"DescribeLogStreams", `{"logGroupName":"group","orderBy":"unknown"}`}, {"DescribeLogStreams", `{"logGroupName":"group","orderBy":"LastEventTime","logStreamNamePrefix":"s"}`}, {"DescribeLogStreams", `{"logGroupName":"group","nextToken":"a.b"}`},
		{"GetLogEvents", `{"logGroupName":"group","logStreamName":"stream","limit":10001}`}, {"GetLogEvents", `{"logGroupName":"group","logStreamName":"stream","startTime":5,"endTime":5}`}, {"GetLogEvents", `{"logGroupName":"group","logStreamName":"stream","nextToken":"a.!"}`}, {"PutLogEvents", `{"logGroupName":"group","logStreamName":"stream"}`},
	} {
		w := logsHTTP(s, test.op, test.body, allowed)
		if w.Code != 400 {
			t.Fatal(test.op, test.body, w.Code, w.Body.String())
		}
	}
	for _, variant := range []string{"method", "path", "query", "content-type", "region", "session"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"logGroupName":"group"}`))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "Logs_20140328.CreateLogGroup")
		region := "us-east-1"
		switch variant {
		case "method":
			r.Method = "GET"
		case "path":
			r.URL.Path = "/wrong"
		case "query":
			r.URL.RawQuery = "x=1"
		case "content-type":
			r.Header.Set("Content-Type", "text/plain")
		case "region":
			region = "unsupported"
		case "session":
			r.Header.Set("X-Amz-Security-Token", "dummy")
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r, region, allowed)
		if w.Code != 400 {
			t.Fatal(variant, w.Code, w.Body.String())
		}
	}
	page, err := s.Events(ctx, "us-east-1", "group", "stream", LogsEventsQuery{StartFromHead: true, Limit: 10})
	logsMust(t, err)
	if len(page.Events) != 1 || page.Events[0].Message != "preserved" {
		t.Fatal("rejected request mutated existing graph", page)
	}
}

func TestLogsHTTPPagesAndPartialRejectionsUseNativeResults(t *testing.T) {
	ctx := context.Background()
	_, s := newLogsTest(t)
	seedLogs(t, s, "group-a", "stream-a")
	logsMust(t, s.CreateGroup(ctx, "us-east-1", "group-b"))
	logsMust(t, s.CreateStream(ctx, "us-east-1", "group-a", "stream-b"))
	allowed := func(string, string) error { return nil }
	for _, opbody := range []struct{ op, body string }{{"DescribeLogGroups", `{"limit":1}`}, {"DescribeLogStreams", `{"logGroupName":"group-a","limit":1}`}} {
		w := logsHTTP(s, opbody.op, opbody.body, allowed)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"nextToken"`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	now := s.now().UnixMilli()
	w := logsHTTP(s, "PutLogEvents", fmt.Sprintf(`{"logGroupName":"group-a","logStreamName":"stream-a","logEvents":[{"timestamp":%d,"message":"future"}]}`, now+3*3600000), allowed)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"tooNewLogEventStartIndex":0`) {
		t.Fatal(w.Code, w.Body.String())
	}
	_, next, err := s.Streams(ctx, "us-east-1", "group-a", LogsStreamsQuery{Limit: 1})
	logsMust(t, err)
	logsMust(t, s.DeleteGroup(ctx, "us-east-1", "group-a"))
	seedLogs(t, s, "group-a", "stream-a")
	if _, _, err = s.Streams(ctx, "us-east-1", "group-a", LogsStreamsQuery{Limit: 1, Token: next}); logsCode(err) != "InvalidParameterException" {
		t.Fatal("listing cursor adopted recreated group", err)
	}
}

func TestLogsCursorAndNativeBoundaryGuardsLeaveNoPartialState(t *testing.T) {
	ctx := context.Background()
	_, s := newLogsTest(t)
	seedLogs(t, s, "group", "stream")
	if err := s.CreateGroup(ctx, "bad-region", "other"); logsCode(err) != "InvalidParameterException" {
		t.Fatal(err)
	}
	if err := s.DeleteGroup(ctx, "bad-region", "group"); logsCode(err) != "InvalidParameterException" {
		t.Fatal(err)
	}
	if err := s.DeleteGroup(ctx, "us-east-1", "bad:group"); logsCode(err) != "InvalidParameterException" {
		t.Fatal(err)
	}
	if err := s.CreateStream(ctx, "bad-region", "group", "other"); logsCode(err) != "InvalidParameterException" {
		t.Fatal(err)
	}
	if _, err := s.PutEvents(ctx, "bad-region", "group", "stream", []LogsInputEvent{{1, "message"}}); logsCode(err) != "InvalidParameterException" {
		t.Fatal(err)
	}
	if _, err := s.PutEvents(ctx, "us-east-1", "group", "bad:stream", []LogsInputEvent{{1, "message"}}); logsCode(err) != "InvalidParameterException" {
		t.Fatal(err)
	}
	if _, _, err := s.Groups(ctx, "bad-region", "", "", 1); logsCode(err) != "InvalidParameterException" {
		t.Fatal(err)
	}
	if _, _, err := s.Streams(ctx, "bad-region", "group", LogsStreamsQuery{Limit: 1}); logsCode(err) != "InvalidParameterException" {
		t.Fatal(err)
	}
	if _, _, err := s.Streams(ctx, "us-east-1", "bad:group", LogsStreamsQuery{Limit: 1}); logsCode(err) != "InvalidParameterException" {
		t.Fatal(err)
	}
	if _, err := s.Events(ctx, "bad-region", "group", "stream", LogsEventsQuery{Limit: 1}); logsCode(err) != "InvalidParameterException" {
		t.Fatal(err)
	}
	if _, err := s.Events(ctx, "us-east-1", "group", "bad:stream", LogsEventsQuery{Limit: 1}); logsCode(err) != "InvalidParameterException" {
		t.Fatal(err)
	}
	if _, _, err := s.Groups(ctx, "us-east-1", "", strings.Repeat("x", 16385), 1); logsCode(err) != "InvalidParameterException" {
		t.Fatal(err)
	}
	groups, _, err := s.Groups(ctx, "us-east-1", "", "", 50)
	logsMust(t, err)
	streams, _, err := s.Streams(ctx, "us-east-1", "group", LogsStreamsQuery{Limit: 50})
	logsMust(t, err)
	if len(groups) != 1 || len(streams) != 1 {
		t.Fatal("rejected native input changed graph", groups, streams)
	}
}
