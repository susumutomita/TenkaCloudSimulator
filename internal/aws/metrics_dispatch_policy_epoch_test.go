package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

// This exercises the real Gateway/SQLite policy port with a trusted in-process
// Actor. It is not an HTTP token-authentication or arbitrary scheduling proof.
// Unlike the KMS policy snapshot contract, Metrics deliberately reads policy in
// the operation's caller transaction AFTER bounded request-body consumption.
func TestCloudWatchMetricsBodyReadPolicyRevocationUsesCurrentTransaction(t *testing.T) {
	metricsPolicyEpoch(t, "Allow", "Deny", 403, "explicitDeny")
}

func TestCloudWatchMetricsBodyReadPolicyGrantUsesCurrentTransaction(t *testing.T) {
	metricsPolicyEpoch(t, "Deny", "Allow", 200, "allowed")
}

type metricsPolicyEpochBody struct {
	reader  io.Reader
	ctx     context.Context
	entered chan struct{}
	release chan struct{}
	first   sync.Once
	resume  sync.Once
}

func (b *metricsPolicyEpochBody) Read(p []byte) (int, error) {
	b.first.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return b.reader.Read(p)
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
}

func (b *metricsPolicyEpochBody) Close() error {
	b.resume.Do(func() { close(b.release) })
	return nil
}

func metricsPolicyEpochPeerRows(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, table := range []string{"aws_metrics_regions", "aws_metrics_series", "aws_metrics_points"} {
		rows, err := db.Query("SELECT * FROM " + table + " WHERE region='us-west-2' ORDER BY 1,2")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var values [][]any
		for rows.Next() {
			row := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range row {
				dest[i] = &row[i]
			}
			if err = rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			values = append(values, row)
		}
		err = rows.Err()
		closed := rows.Close()
		if err != nil || closed != nil || len(values) != 1 {
			t.Fatal("peer fixture must contain one real regional row in each metric table")
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		result[table] = string(encoded)
	}
	return result
}

func metricsPolicyEpoch(t *testing.T, initial, replacement string, status int, decision string) {
	t.Helper()
	g, store := metricsGatewayFixture(t)
	db := store.Database()
	admin, reader := metricsGatewayActor(core.Administrator), metricsGatewayActor("epoch-reader")
	headers := map[string][]string{"X-Simulator-Service": {"monitoring"}, "X-Simulator-Region": {"us-east-1"}}
	put := metricsGatewayForm("PutMetricData")
	put.Set("Namespace", "Epoch/App")
	put.Set("MetricData.member.1.MetricName", "Seed")
	put.Set("MetricData.member.1.Value", "2")
	if w := metricsGatewayCall(g, &admin, "POST", put, headers); w.Code != 200 {
		t.Fatal("actual own-region seed failed")
	}
	peerHeaders := map[string][]string{"X-Simulator-Service": {"monitoring"}, "X-Simulator-Region": {"us-west-2"}}
	put.Set("Namespace", "Peer/App")
	put.Set("MetricData.member.1.MetricName", "Peer")
	put.Set("MetricData.member.1.Value", "23")
	if w := metricsGatewayCall(g, &admin, "POST", put, peerHeaders); w.Code != 200 {
		t.Fatal("actual peer-region seed failed")
	}
	policy := func(effect string) core.Policy {
		p, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"` + effect + `","Action":"cloudwatch:PutMetricData","Resource":"*"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if err := g.Identities.Set("epoch-reader", policy(initial)); err != nil {
		t.Fatal(err)
	}
	beforePolicyChange := metricsGatewayFrame(t, db)
	peerBefore := metricsPolicyEpochPeerRows(t, db)
	put.Set("Namespace", "Epoch/App")
	put.Set("MetricData.member.1.MetricName", "CreatedAfterBody")
	put.Set("MetricData.member.1.Value", "7")
	ctx, cancel := context.WithCancel(context.Background())
	body := &metricsPolicyEpochBody{reader: strings.NewReader(put.Encode()), ctx: ctx, entered: make(chan struct{}), release: make(chan struct{})}
	r := httptest.NewRequest("POST", "http://127.0.0.1/", body)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Simulator-Service", "monitoring")
	r.Header.Set("X-Simulator-Region", "us-east-1")
	r = r.WithContext(provider.WithActor(ctx, reader))
	w := httptest.NewRecorder()
	done, mutationDone := make(chan struct{}), make(chan struct{})
	mutationError := make(chan error, 1)
	mutationStarted := false
	wait := func(ch <-chan struct{}, stage string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal(stage)
		}
	}
	defer func() {
		body.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("owned policy-epoch handler did not terminate")
		}
		if mutationStarted {
			select {
			case <-mutationDone:
			case <-time.After(5 * time.Second):
				t.Error("owned identity-policy update did not terminate")
			}
		}
	}()
	go func() { defer close(done); g.ServeHTTP(w, r) }()
	wait(body.entered, "request did not reach the actual body read")
	// Set is the existing persisted core identity API, not a substituted
	// authorizer. It must COMMIT while the request body is still held, before
	// Metrics opens its operation transaction on this single-connection DB.
	replacementPolicy := policy(replacement)
	mutationStarted = true
	go func() {
		defer close(mutationDone)
		mutationError <- g.Identities.Set("epoch-reader", replacementPolicy)
	}()
	wait(mutationDone, "identity policy could not commit before body release")
	if err := <-mutationError; err != nil {
		t.Fatal(err)
	}
	afterPolicyChange := metricsGatewayFrame(t, db)
	if beforePolicyChange["identity_policies"] == afterPolicyChange["identity_policies"] {
		t.Fatal("real persisted policy did not change")
	}
	for table, hash := range beforePolicyChange {
		if table != "identity_policies" && afterPolicyChange[table] != hash {
			t.Fatal("policy update changed metric state before request release")
		}
	}
	body.Close()
	wait(done, "request did not finish after body release")
	if w.Code != status || w.Header().Get("X-Simulator-IAM-Decision") != decision {
		t.Fatalf("request did not use the post-body policy: status%d decision%s", w.Code, w.Header().Get("X-Simulator-IAM-Decision"))
	}
	after := metricsGatewayFrame(t, db)
	if !reflect.DeepEqual(peerBefore, metricsPolicyEpochPeerRows(t, db)) {
		t.Fatal("policy transition or current request changed peer rows")
	}
	assertOwnRows := func(points, revision, created int) {
		t.Helper()
		for _, check := range []struct {
			query string
			want  int
		}{
			{"SELECT count(*) FROM aws_metrics_series WHERE region='us-east-1'", 2},
			{"SELECT count(*) FROM aws_metrics_points WHERE region='us-east-1'", points},
			{"SELECT revision FROM aws_metrics_regions WHERE region='us-east-1'", revision},
			{"SELECT count(*) FROM aws_metrics_points p JOIN aws_metrics_series s ON s.region=p.region AND s.identity=p.series_id WHERE s.region='us-east-1' AND s.namespace='Epoch/App' AND s.name='CreatedAfterBody' AND json_extract(p.document,'$.unit')='None' AND json_extract(p.document,'$.value')=7", created},
			{"SELECT count(*) FROM aws_metrics_points p JOIN aws_metrics_series s ON s.region=p.region AND s.identity=p.series_id WHERE s.region='us-east-1' AND s.namespace='Epoch/App' AND s.name='Seed' AND json_extract(p.document,'$.unit')='None' AND json_extract(p.document,'$.value')=2", 1},
		} {
			var got int
			if db.QueryRow(check.query).Scan(&got) != nil || got != check.want {
				t.Fatal("granted request did not preserve the exact literal point/revision frame")
			}
		}
	}
	if status == 403 {
		metricsGatewayError(t, w, 403, "AccessDenied")
		if !reflect.DeepEqual(afterPolicyChange, after) {
			t.Fatal("revoked request changed the full post-policy state frame")
		}
	} else {
		var response struct {
			XMLName xml.Name
			ID      string `xml:"ResponseMetadata>RequestId"`
		}
		if xml.Unmarshal(w.Body.Bytes(), &response) != nil || response.XMLName.Local != "PutMetricDataResponse" || response.XMLName.Space != "http://monitoring.amazonaws.com/doc/2010-08-01/" || response.ID == "" {
			t.Fatal("granted request did not return the native success envelope")
		}
		for _, table := range []string{"aws_metrics_meta", "identity_policies"} {
			if after[table] != afterPolicyChange[table] {
				t.Fatal("granted write changed configuration or authority")
			}
		}
		assertOwnRows(2, 2, 1)
	}
	// A subsequent request must observe the same new policy. The read-only
	// denied follow-up also retains the COMPLETE frame, including policy rows.
	follow := metricsGatewayCall(g, &reader, "POST", put, headers)
	if follow.Code != status || follow.Header().Get("X-Simulator-IAM-Decision") != decision {
		t.Fatal("next request did not retain the replacement policy")
	}
	if status == 403 && !reflect.DeepEqual(after, metricsGatewayFrame(t, db)) {
		t.Fatal("denied follow-up changed the full state frame")
	}
	if status == 200 {
		assertOwnRows(3, 3, 2)
		followFrame := metricsGatewayFrame(t, db)
		if followFrame["aws_metrics_meta"] != after["aws_metrics_meta"] || followFrame["identity_policies"] != after["identity_policies"] || !reflect.DeepEqual(peerBefore, metricsPolicyEpochPeerRows(t, db)) {
			t.Fatal("allowed follow-up changed authority, configuration or peer rows")
		}
	}
}
