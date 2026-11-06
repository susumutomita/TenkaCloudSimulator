package aws

import (
	"encoding/xml"
	"io"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

type metricsAuthorityBody struct {
	reader io.Reader
	reads  int
	bytes  int
}

func (b *metricsAuthorityBody) Read(p []byte) (int, error) {
	b.reads++
	n, err := b.reader.Read(p)
	b.bytes += n
	return n, err
}

func (*metricsAuthorityBody) Close() error { return nil }

// These literal native cases bridge finite TrustedScope fixtures14/29/42.
// WithActor is the existing trusted in-process fixture, not wire authentication;
// no token validation, SigV4 cryptography or broad scheduling proof is claimed.
func TestCloudWatchMetricsNativeAuthorityPrecedesMetadataAndBody(t *testing.T) {
	g, store := metricsGatewayFixture(t)
	db := store.Database()
	admin := metricsGatewayActor(core.Administrator)
	headers := map[string][]string{"X-Simulator-Service": {"monitoring"}, "X-Simulator-Region": {"us-east-1"}}
	put := metricsGatewayForm("PutMetricData")
	put.Set("Namespace", "Authority/Order")
	put.Set("MetricData.member.1.MetricName", "Visible")
	put.Set("MetricData.member.1.Value", "5")
	if w := metricsGatewayCall(g, &admin, "POST", put, headers); w.Code != 200 {
		t.Fatal("actual own-region authority fixture failed")
	}
	put.Set("Namespace", "Peer/Authority")
	put.Set("MetricData.member.1.MetricName", "Peer")
	put.Set("MetricData.member.1.Value", "9")
	if w := metricsGatewayCall(g, &admin, "POST", put, map[string][]string{"X-Simulator-Service": {"monitoring"}, "X-Simulator-Region": {"us-west-2"}}); w.Code != 200 {
		t.Fatal("actual nonempty peer-region fixture failed")
	}
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"cloudwatch:ListMetrics","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Identities.Set("authority-reader", policy); err != nil {
		t.Fatal(err)
	}
	reader := metricsGatewayActor("authority-reader")
	dummy := "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261106/us-east-1/monitoring/aws4_request, SignedHeaders=host, Signature=dummy"
	malformed := "BAD Credential=TCSIMLOCAL/20261106/us-east-1/monitoring/aws4_request, SignedHeaders=host, Signature=dummy"
	before := metricsGatewayFrame(t, db)
	cases := []struct {
		name, authorization, service string
		actor                        *provider.Actor
		status                       int
		code                         string
	}{
		// No explicit selector: malformed Authorization still carries monitoring
		// intent and must reach Metrics refusal, rather than CFN/S3 fallback.
		{"fixture14-trusted-actor-bad-authorization-prefix", malformed, "", &reader, 403, "AccessDenied"},
		// The wrong service would independently be400 for an authenticated
		// actor. Missing private authority must win first with401, not400.
		{"fixture29-missing-actor-before-wrong-service", dummy, "kms", nil, 401, "NotAuthorized"},
		{"fixture42-dummy-signature-is-not-private-actor", dummy, "monitoring", nil, 401, "NotAuthorized"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			body := &metricsAuthorityBody{reader: strings.NewReader("Action=ListMetrics&Version=2010-08-01")}
			r := httptest.NewRequest("POST", "http://127.0.0.1/", body)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Authorization", item.authorization)
			r.Header.Set("X-Simulator-Region", "us-east-1")
			r.Header.Set("X-Simulator-Principal", core.Administrator)
			if item.service != "" {
				r.Header.Set("X-Simulator-Service", item.service)
			}
			if item.actor != nil {
				r = r.WithContext(provider.WithActor(r.Context(), *item.actor))
			}
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			metricsGatewayError(t, w, item.status, item.code)
			var failure struct {
				XMLName xml.Name
				ID      string `xml:"RequestId"`
			}
			if xml.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.XMLName.Local != "ErrorResponse" || failure.XMLName.Space != "http://monitoring.amazonaws.com/doc/2010-08-01/" || failure.ID == "" {
				t.Fatal("refusal did not come from the native Metrics XML boundary")
			}
			if body.reads != 0 || body.bytes != 0 {
				t.Fatal("authority or metadata refusal consumed request body")
			}
			if w.Header().Get("X-Simulator-IAM-Decision") != "" {
				t.Fatal("pre-body refusal reached metric action authorization")
			}
			if !reflect.DeepEqual(before, metricsGatewayFrame(t, db)) {
				t.Fatal("authority refusal changed the complete six-table own/peer frame")
			}
		})
	}
	// Repair the trusted actor and canonical metadata together. A genuinely
	// consumed body and literal nonempty result prevent an always-unread or
	// always-denied fixture from satisfying the negative cases vacuously.
	body := &metricsAuthorityBody{reader: strings.NewReader("Action=ListMetrics&Version=2010-08-01")}
	r := httptest.NewRequest("POST", "http://127.0.0.1/", body)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization", dummy)
	r.Header.Set("X-Simulator-Service", "monitoring")
	r.Header.Set("X-Simulator-Region", "us-east-1")
	r.Header.Set("X-Simulator-Principal", core.Administrator)
	r = r.WithContext(provider.WithActor(r.Context(), reader))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	var listed struct {
		XMLName xml.Name
		Metrics []struct{ Namespace, MetricName string } `xml:"ListMetricsResult>Metrics>member"`
	}
	if w.Code != 200 || w.Header().Get("X-Simulator-IAM-Decision") != "allowed" || body.reads == 0 || body.bytes == 0 || xml.Unmarshal(w.Body.Bytes(), &listed) != nil || listed.XMLName.Local != "ListMetricsResponse" || listed.XMLName.Space != "http://monitoring.amazonaws.com/doc/2010-08-01/" || len(listed.Metrics) != 1 || listed.Metrics[0].Namespace != "Authority/Order" || listed.Metrics[0].MetricName != "Visible" {
		t.Fatal("repaired trusted reader did not consume the body and return the exact native metric")
	}
	if !reflect.DeepEqual(before, metricsGatewayFrame(t, db)) {
		t.Fatal("successful read changed the complete six-table own/peer frame")
	}
}
