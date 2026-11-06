package aws

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/metrics"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

func metricsGatewayFixture(t *testing.T) (*Gateway, *core.Store) {
	t.Helper()
	s, err := core.New(filepath.Join(t.TempDir(), "gateway.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	metricStore, err := metrics.NewStore(context.Background(), s.Database(), metrics.Config{})
	if err != nil {
		t.Fatal(err)
	}
	native, err := NewMetricsGateway(metricStore, s.Database())
	if err != nil {
		t.Fatal(err)
	}
	objects, err := NewS3Store(s.Database())
	if err != nil {
		t.Fatal(err)
	}
	return &Gateway{Store: s, S3: objects, CloudWatchMetrics: native, Identities: core.NewIdentities(s)}, s
}
func metricsGatewayFrame(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	result := map[string]string{}
	tables := []string{"aws_metrics_meta", "aws_metrics_regions", "aws_metrics_series", "aws_metrics_points", "identity_policies", "sqlite_sequence"}
	for _, name := range tables {
		rows, err := db.Query("SELECT * FROM " + name + " ORDER BY 1,2")
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		data := [][]any{}
		for rows.Next() {
			values := make([]any, len(cols))
			dest := make([]any, len(cols))
			for i := range values {
				dest[i] = &values[i]
			}
			if err = rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			data = append(data, values)
		}
		err = rows.Err()
		closed := rows.Close()
		if err != nil || closed != nil {
			t.Fatal("frame read failed")
		}
		b, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(b)
		result[name] = hex.EncodeToString(hash[:])
	}
	return result
}
func metricsGatewayActor(subject string) provider.Actor {
	return provider.Actor{CredentialIdentity: core.Administrator, ExerciseIdentity: subject, CredentialRole: provider.CredentialOperator}
}
func metricsGatewayForm(action string) url.Values {
	return url.Values{"Action": {action}, "Version": {"2010-08-01"}}
}
func metricsGatewayCall(g *Gateway, actor *provider.Actor, method string, form url.Values, headers map[string][]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for name, values := range headers {
		r.Header[name] = values
	}
	if actor != nil {
		r = r.WithContext(provider.WithActor(r.Context(), *actor))
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}
func metricsGatewayError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Code string `xml:"Error>Code"`
	}
	if w.Code != status || xml.Unmarshal(w.Body.Bytes(), &body) != nil || body.Code != code {
		t.Fatalf("wrong native metrics refusal: status%d expected%d/code%s", w.Code, status, code)
	}
}
func TestCloudWatchMetricsGatewayUsesTrustedActorAndCurrentCallerTxPolicy(t *testing.T) {
	g, s := metricsGatewayFixture(t)
	db := s.Database()
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["cloudwatch:ListMetrics","cloudwatch:GetMetricStatistics"],"Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Identities.Set("reader", policy); err != nil {
		t.Fatal(err)
	}
	admin, reader := metricsGatewayActor(core.Administrator), metricsGatewayActor("reader")
	headers := map[string][]string{"X-Simulator-Service": {"monitoring"}, "X-Simulator-Region": {"us-east-1"}}
	put := metricsGatewayForm("PutMetricData")
	put.Set("Namespace", "Practice/App")
	put.Set("MetricData.member.1.MetricName", "Metric")
	put.Set("MetricData.member.1.Value", "0")
	if w := metricsGatewayCall(g, &admin, "POST", put, headers); w.Code != 200 {
		t.Fatal("explicit authenticated administrator could not seed actual metric")
	}
	west := map[string][]string{"X-Simulator-Service": {"monitoring"}, "X-Simulator-Region": {"us-west-2"}}
	if w := metricsGatewayCall(g, &admin, "POST", put, west); w.Code != 200 {
		t.Fatal("peer region seed failed")
	}
	before := metricsGatewayFrame(t, db)
	// An operator credential selecting reader does not retain data-plane admin.
	spoof := map[string][]string{"X-Simulator-Service": {"monitoring"}, "X-Simulator-Region": {"ap-northeast-1"}, "X-Simulator-Principal": {core.Administrator}}
	metricsGatewayError(t, metricsGatewayCall(g, &reader, "POST", put, spoof), 403, "AccessDenied")
	metricsGatewayError(t, metricsGatewayCall(g, nil, "POST", put, spoof), 401, "NotAuthorized")
	invalid := provider.Actor{CredentialIdentity: "reader", ExerciseIdentity: core.Administrator, CredentialRole: provider.CredentialParticipant}
	metricsGatewayError(t, metricsGatewayCall(g, &invalid, "POST", put, spoof), 401, "NotAuthorized")
	if w := metricsGatewayCall(g, &reader, "POST", metricsGatewayForm("ListMetrics"), headers); w.Code != 200 || w.Header().Get("X-Simulator-IAM-Decision") != "allowed" {
		t.Fatal("reader could not use current same-Tx projection")
	}
	if !reflect.DeepEqual(before, metricsGatewayFrame(t, db)) {
		t.Fatal("refusal/read mutated whole region/peer frame")
	}
	// Real malformed persisted policy is an infrastructure500, never403/allow.
	if _, err = db.Exec("UPDATE identity_policies SET document='not-json' WHERE name='reader'"); err != nil {
		t.Fatal(err)
	}
	broken := metricsGatewayFrame(t, db)
	metricsGatewayError(t, metricsGatewayCall(g, &reader, "POST", put, headers), 500, "InternalServiceError")
	if !reflect.DeepEqual(broken, metricsGatewayFrame(t, db)) {
		t.Fatal("policy fault changed metric authority/state")
	}
	if err = g.Identities.Set("reader", policy); err != nil {
		t.Fatal(err)
	}
	if w := metricsGatewayCall(g, &reader, "POST", metricsGatewayForm("ListMetrics"), headers); w.Code != 200 {
		t.Fatal("policy repair did not recover real read")
	}
}
func TestCloudWatchMetricsMetadataCannotFallThroughToAnotherService(t *testing.T) {
	g, s := metricsGatewayFixture(t)
	actor := metricsGatewayActor(core.Administrator)
	auth := "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261106/us-east-1/monitoring/aws4_request, SignedHeaders=host;x-amz-date, Signature=dummy"
	before := metricsGatewayFrame(t, s.Database())
	cases := []struct {
		name, method string
		headers      map[string][]string
		status       int
		code         string
	}{
		{"signedmissingaction", "POST", map[string][]string{"Authorization": {auth}}, 400, "MissingParameter"},
		{"signedget", "GET", map[string][]string{"Authorization": {auth}}, 404, "UnknownOperationException"},
		{"conflictingservice", "POST", map[string][]string{"Authorization": {auth}, "X-Simulator-Service": {"kms"}}, 400, "InvalidParameterValue"},
		{"emptysignedregion", "POST", map[string][]string{"Authorization": {strings.Replace(auth, "/us-east-1/monitoring/", "//monitoring/", 1)}}, 400, "InvalidParameterValue"},
		{"conflictingregion", "POST", map[string][]string{"Authorization": {auth}, "X-Simulator-Region": {"us-west-2"}}, 400, "InvalidParameterValue"},
		{"duplicateselector", "POST", map[string][]string{"X-Simulator-Service": {"monitoring", "monitoring"}}, 400, "InvalidParameterValue"},
		{"commaselector", "POST", map[string][]string{"X-Simulator-Service": {"monitoring,s3"}}, 400, "InvalidParameterValue"},
		{"realauthoritykey", "POST", map[string][]string{"Authorization": {strings.Replace(auth, "TCSIMLOCAL", "AKIAFAKE", 1)}}, 403, "AccessDenied"},
		{"session", "POST", map[string][]string{"Authorization": {auth}, "X-Amz-Security-Token": {"dummy"}}, 400, "InvalidParameterCombination"},
		{"unsupportedregion", "POST", map[string][]string{"X-Simulator-Service": {"monitoring"}, "X-Simulator-Region": {"moon-1"}}, 400, "InvalidParameterValue"},
		{"targetconflict", "POST", map[string][]string{"Authorization": {auth}, "X-Amz-Target": {"TrentService.CreateKey"}}, 400, "InvalidParameterCombination"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			w := metricsGatewayCall(g, &actor, item.method, metricsGatewayForm(""), item.headers)
			metricsGatewayError(t, w, item.status, item.code)
			if !reflect.DeepEqual(before, metricsGatewayFrame(t, s.Database())) {
				t.Fatal("metadata refusal changed whole state")
			}
		})
	}
	normal := metricsGatewayCall(g, &actor, "GET", url.Values{}, nil)
	if normal.Code != 200 || !bytes.Contains(normal.Body.Bytes(), []byte("ListAllMyBucketsResult")) {
		t.Fatal("ordinary S3 fallback changed")
	}
	missing := &Gateway{Store: g.Store, S3: g.S3}
	metricsGatewayError(t, metricsGatewayCall(missing, &actor, "GET", url.Values{}, map[string][]string{"Authorization": {auth}}), 501, "UnknownOperationException")
}
func TestCloudWatchMetricsFactoryRefusesDifferentDatabaseAndMissingActorBeforeBody(t *testing.T) {
	g, s := metricsGatewayFixture(t)
	other, err := core.New(filepath.Join(t.TempDir(), "other.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	different, err := metrics.NewStore(context.Background(), other.Database(), metrics.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if h, err := NewMetricsGateway(different, s.Database()); err == nil || h != nil {
		t.Fatal("cross-database identity/metrics composition accepted")
	}
	body := &metricsNoReadBody{}
	r := httptest.NewRequest("POST", "http://127.0.0.1/", nil)
	r.Body = body
	r.Header.Set("X-Simulator-Service", "monitoring")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Simulator-Principal", core.Administrator)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	metricsGatewayError(t, w, 401, "NotAuthorized")
	if body.reads != 0 {
		t.Fatal("missing trusted actor caused body consumption")
	}
}

type metricsNoReadBody struct{ reads int }

func (b *metricsNoReadBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (*metricsNoReadBody) Close() error               { return nil }
