package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCloudWatchMetricsUsesExistingAccessAndSharedQueryRoot(t *testing.T) {
	h, closeState, err := New(filepath.Join(t.TempDir(), "team.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeState()
	const token = "owner-token-for-local-metrics-testing"
	access, err := NewAccess(h, token, false)
	if err != nil {
		t.Fatal(err)
	}
	call := func(path string, fields url.Values, headers map[string]string) (int, string) {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+path, strings.NewReader(fields.Encode()))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		access.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	plain := map[string]string{"X-Simulator-Token": token, "X-Simulator-Service": "monitoring"}
	put := url.Values{"Action": {"PutMetricData"}, "Version": {"2010-08-01"}, "Namespace": {"Custom/Service"}, "MetricData.member.1.MetricName": {"Count"}, "MetricData.member.1.Value": {"4"}}
	if status, body := call("/", put, plain); status != 200 || !strings.Contains(body, "PutMetricDataResponse") {
		t.Fatal(status, body)
	}
	other := map[string]string{"X-Simulator-Token": token, "Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMTEST/20261128/us-east-1/monitoring/aws4_request, SignedHeaders=host, Signature=00"}
	now := time.Now().UTC()
	params := url.Values{"Action": {"GetMetricStatistics"}, "Version": {"2010-08-01"}, "Namespace": {"Custom/Service"}, "MetricName": {"Count"}, "Period": {"60"}, "StartTime": {now.Add(-2 * time.Minute).Format(time.RFC3339)}, "EndTime": {now.Add(2 * time.Minute).Format(time.RFC3339)}, "Statistics.member.1": {"Sum"}}
	for _, path := range []string{"/", "/_simulator/providers/aws/"} {
		if status, body := call(path, params, other); status != 200 || !strings.Contains(body, "<Sum>4</Sum>") {
			t.Fatal(path, status, body)
		}
	}
	other["X-Simulator-Region"] = "us-west-2"
	if status, _ := call("/", put, other); status != 400 {
		t.Fatal(status)
	}
	if status, _ := call("/", put, map[string]string{"X-Simulator-Service": "monitoring"}); status != 401 {
		t.Fatal(status)
	}
	if status, body := call("/", put, map[string]string{"X-Simulator-Token": token, "X-Simulator-Service": "cloudformation"}); strings.Contains(body, "PutMetricDataResponse") || status == 200 {
		t.Fatal("wrong Query service", status, body)
	}
	plain["X-Amz-Target"] = "Logs_20140328.DescribeLogGroups"
	if status, _ := call("/", put, plain); status != 400 {
		t.Fatal("mixed protocol target", status)
	}
}
