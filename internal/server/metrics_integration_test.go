package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/metrics"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestCloudWatchMetricsRegularServerAuthenticatesAndPersistsActualRegionalState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "metrics.sqlite")
	token := "METRICS_OPERATOR_A_12345678901234567890123456789"
	peerToken := "METRICS_OPERATOR_B_12345678901234567890123456789"
	start := func(path, key string) (*httptest.Server, func()) {
		handler, closeDB, err := New(path)
		if err != nil {
			t.Fatal(err)
		}
		access, err := NewAccess(handler, key, false)
		if err != nil {
			closeDB()
			t.Fatal(err)
		}
		server := httptest.NewServer(access)
		return server, func() {
			server.Close()
			if err := closeDB(); err != nil {
				t.Error(err)
			}
		}
	}
	srv, stop := start(state, token)
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	peer, closePeer := start(filepath.Join(t.TempDir(), "peer.sqlite"), peerToken)
	defer closePeer()
	call := func(target *httptest.Server, path, key, region, principal string, form url.Values, want int) []byte {
		t.Helper()
		r, err := http.NewRequest("POST", target.URL+path, strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-Simulator-Service", "monitoring")
		r.Header.Set("X-Simulator-Region", region)
		r.Header.Set("X-Simulator-Token", key)
		if principal != "" {
			r.Header.Set("X-Simulator-Principal", principal)
		}
		response, err := target.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("actual server metrics expected%d got%d", want, response.StatusCode)
		}
		return body
	}
	control := func(path, method string, input any) map[string]json.RawMessage {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		r, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-Simulator-Token", token)
		r.Header.Set("Content-Type", "application/json")
		response, err := srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal("actual identity/access API failed")
		}
		out := map[string]json.RawMessage{}
		if json.NewDecoder(response.Body).Decode(&out) != nil {
			t.Fatal("identity control returned invalid JSON")
		}
		return out
	}
	form := func(action string) url.Values { return url.Values{"Action": {action}, "Version": {"2010-08-01"}} }
	put := form("PutMetricData")
	put.Set("Namespace", "Practice/CLI")
	put.Set("MetricData.member.1.MetricName", "Shared")
	put.Set("MetricData.member.1.Value", "2")
	call(srv, "/", "", "us-east-1", "", put, 401)
	call(srv, "/", token, "us-east-1", "", put, 200)
	put.Set("MetricData.member.1.Value", "9")
	call(srv, "/", token, "us-west-2", "", put, 200)
	put.Set("MetricData.member.1.Value", "7")
	call(peer, "/", peerToken, "us-east-1", "", put, 200)
	read := form("GetMetricStatistics")
	read.Set("Namespace", "Practice/CLI")
	read.Set("MetricName", "Shared")
	read.Set("Period", "60")
	read.Set("Statistics.member.1", "Sum")
	read.Set("StartTime", time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano))
	read.Set("EndTime", time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano))
	sum := func(target *httptest.Server, path, key, region, principal string, want float64) {
		body := call(target, path, key, region, principal, read, 200)
		var out struct {
			Points []struct {
				Sum  float64
				Unit string
			} `xml:"GetMetricStatisticsResult>Datapoints>member"`
		}
		if xml.Unmarshal(body, &out) != nil || len(out.Points) != 1 || out.Points[0].Sum != want || out.Points[0].Unit != "None" {
			t.Fatal("actual server/region/DB returned incorrect persisted sample")
		}
	}
	sum(srv, "/", token, "us-east-1", "", 2)
	sum(srv, "/_simulator/providers/aws/", token, "us-west-2", "", 9)
	sum(peer, "/", peerToken, "us-east-1", "", 7)
	call(peer, "/", token, "us-east-1", "", read, 401)
	policy := map[string]any{"Version": "2012-10-17", "Statement": []map[string]any{{"Effect": "Allow", "Action": []string{"cloudwatch:ListMetrics", "cloudwatch:GetMetricStatistics"}, "Resource": "*"}}}
	control("/_simulator/v1/iam/principals/metrics-reader/policy", "PUT", policy)
	issued := control("/_simulator/v1/access", "POST", map[string]string{"Principal": "metrics-reader"})
	var participant string
	if json.Unmarshal(issued["AccessToken"], &participant) != nil || participant == "" {
		t.Fatal("actual access token missing")
	}
	sum(srv, "/", participant, "us-east-1", "", 2)
	put.Set("MetricData.member.1.Value", "99")
	call(srv, "/", participant, "us-east-1", "", put, 403)
	call(srv, "/", token, "us-east-1", "metrics-reader", put, 403)        // operator role is not permission.
	call(srv, "/", participant, "us-east-1", "simulator-admin", put, 403) // outer participant selector guard.
	sum(srv, "/", token, "us-east-1", "", 2)
	sum(peer, "/", peerToken, "us-east-1", "", 7)
	stop()
	stop = nil
	srv, stop = start(state, token)
	sum(srv, "/", token, "us-east-1", "", 2)
	sum(srv, "/", token, "us-west-2", "", 9)
	// Participant sessions are deliberately not persisted; no false restart auth.
	call(srv, "/", participant, "us-east-1", "", read, 401)
}

func TestCloudWatchMetricsRegularConstructorRefusesCorruptSchemaWithoutWipingIt(t *testing.T) {
	state := filepath.Join(t.TempDir(), "corrupt.sqlite")
	// Establish the actual versioned core first. An unversioned foreign DB is
	// correctly refused by core.New before any provider constructor is reached.
	seed, err := core.New(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = seed.Database().Exec("CREATE TABLE sentinel(value TEXT); INSERT INTO sentinel VALUES('keep'); CREATE TABLE aws_metrics_meta(singleton TEXT)"); err != nil {
		seed.Close()
		t.Fatal(err)
	}
	if err = seed.Close(); err != nil {
		t.Fatal(err)
	}
	h, closeDB, err := New(state)
	if err == nil || h != nil || closeDB != nil {
		if closeDB != nil {
			closeDB()
		}
		t.Fatal("default server accepted an incompatible metric schema")
	}
	var metricError *metrics.Error
	if !errors.As(err, &metricError) || metricError.Code != "InternalServiceError" {
		t.Fatal("refusal did not reach the actual metric constructor")
	}
	raw, err := sql.Open("sqlite3", state)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var value string
	var tables int
	if raw.QueryRow("SELECT value FROM sentinel").Scan(&value) != nil || value != "keep" {
		t.Fatal("failed startup discarded peer data")
	}
	if raw.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='aws_metrics_regions'").Scan(&tables) != nil || tables != 0 {
		t.Fatal("failed metric constructor partially committed schema")
	}
	if _, err = raw.Exec("DROP TABLE aws_metrics_meta"); err != nil {
		t.Fatal(err)
	}
	repaired, done, err := New(state)
	if err != nil || repaired == nil || done == nil {
		t.Fatal("repaired default server did not recover")
	}
	if err = done(); err != nil {
		t.Fatal(err)
	}
}
