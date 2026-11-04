package aws

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestLogsGatewayDispatchPreservesServiceRegionAndIAMAuthority(t *testing.T) {
	db, s := newLogsTest(t)
	g := &Gateway{CloudWatchLogs: s, Identities: core.NewIdentities(db)}
	call := func(target, service, region, credentials, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", target)
		if service != "" {
			r.Header.Set("X-Simulator-Service", service)
		}
		if region != "" {
			r.Header.Set("X-Simulator-Region", region)
		}
		if credentials != "" {
			r.Header.Set("Authorization", credentials)
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		return w
	}
	credential := "AWS4-HMAC-SHA256 Credential=TCSIMTEST/20261104/us-west-2/logs/aws4_request, SignedHeaders=host, Signature=dummy"
	w := call("Logs_20140328.CreateLogGroup", "", "", credential, `{"logGroupName":"regional"}`)
	if w.Code != 200 || w.Header().Get("X-Simulator-IAM-Decision") != "allowed" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("Logs_20140328.DescribeLogGroups", "logs", "us-west-2", "", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "regional") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("Logs_20140328.DescribeLogGroups", "logs", "us-east-1", "", `{}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "regional") {
		t.Fatal("regional state crossed scope", w.Code, w.Body.String())
	}
	w = call("Logs_20140328.DescribeLogGroups", "logs", "us-east-1", credential, `{}`)
	if w.Code != 400 {
		t.Fatal("signed region mismatch", w.Code, w.Body.String())
	}
	w = call("Logs_20140328.DescribeLogGroups", "ssm", "", "", `{}`)
	if w.Code != 400 {
		t.Fatal("conflicting service accepted", w.Code, w.Body.String())
	}
	for _, target := range []string{"Logs_20140328.PutRetentionPolicy", "", "AmazonSSM.DescribeParameters"} {
		w = call(target, "logs", "", "", `{}`)
		if w.Code != 501 || strings.Contains(w.Body.String(), "<Error>") {
			t.Fatal("unsupported Logs escaped to another protocol", target, w.Code, w.Body.String())
		}
	}
	g.CloudWatchLogs = nil
	w = call("Logs_20140328.DescribeLogGroups", "logs", "", "", `{}`)
	if w.Code != 501 {
		t.Fatal(w.Code, w.Body.String())
	}
	g.CloudWatchLogs = s
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"logGroupName":"regional"}`))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "Logs_20140328.DeleteLogGroup")
	r.Header.Set("X-Simulator-Principal", "unknown-reader")
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 403 || w.Header().Get("X-Simulator-IAM-Decision") != "implicitDeny" {
		t.Fatal("unknown identity mutated Logs", w.Code, w.Body.String())
	}
	g.Identities = nil
	w = call("Logs_20140328.DescribeLogGroups", "logs", "", "", `{}`)
	if w.Code != 500 {
		t.Fatal("missing authority was reported as success", w.Code, w.Body.String())
	}
	g.Identities = core.NewIdentities(db)
	logsMust(t, db.Close())
	fresh := httptest.NewRequest("POST", "/", strings.NewReader(`{"logGroupName":"regional"}`))
	fresh.Header = r.Header.Clone()
	r = fresh
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 500 || strings.Contains(w.Body.String(), "closed") {
		t.Fatal("failed policy lookup became denial or leaked internals", w.Code, w.Body.String())
	}
	// A Logs signing scope alone still routes to a Logs operation error rather
	// than returning a superficially successful S3/list-bucket response.
	q := httptest.NewRequest("POST", "/", nil)
	q.Header.Set("Authorization", credential)
	if !isCloudWatchLogs(q) {
		t.Fatal("signed Logs scope was not recognized")
	}
}
