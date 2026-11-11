package aws

import (
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoute53GatewaySelectorsAndS3Isolation(t *testing.T) {
	db, s := r53test(t)
	s3, e := NewS3Store(db.Database())
	r53must(t, e)
	g := &Gateway{Store: db, S3: s3, Route53: s, Identities: core.NewIdentities(db), CatalogOwns: func(*http.Request) bool { return true }, Catalog: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("Route53 fell through catalog") })}
	credential := "AWS4-HMAC-SHA256 Credential=TCSIMTEST/20261110/us-east-1/route53/aws4_request, SignedHeaders=host, Signature=dummy"
	for _, headers := range []map[string]string{{"Authorization": credential}, {"X-Simulator-Service": "route53"}, {}} {
		r := httptest.NewRequest("GET", "/2013-04-01/hostedzone", nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(headers, w.Code, w.Body.String())
		}
	}
	for _, headers := range []map[string]string{{"X-Simulator-Service": "logs"}, {"X-Amz-Target": "Logs_20140328.DescribeLogGroups"}, {"X-Simulator-Region": "eu-west-1"}, {"Authorization": credential, "X-Simulator-Service": "s3"}, {"Authorization": strings.Replace(credential, "/route53/", "/ssm/", 1)}, {"X-Simulator-Service": "route53", "X-Amz-Target": "DynamoDB_20120810.ListTables"}} {
		r := httptest.NewRequest("GET", "/2013-04-01/hostedzone", nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code < 400 {
			t.Fatal(headers, w.Code, w.Body.String())
		}
	}
	for _, key := range []string{"X-Simulator-Service", "Authorization", "X-Simulator-Principal", "X-Amz-Target", "X-Simulator-Region", "X-Amz-Security-Token"} {
		r := httptest.NewRequest("GET", "/2013-04-01/hostedzone", nil)
		r.Header.Add(key, "one")
		r.Header.Add(key, "two")
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(key, w.Code)
		}
	}
	g.CatalogOwns = nil
	r53must(t, db.Create("2013-04-01", "us-east-1"))
	_, e = db.Put("2013-04-01", "hostedzone", "text/plain", []byte("s3-peer"), "", "")
	r53must(t, e)
	for _, signed := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/2013-04-01/hostedzone", nil)
		if signed {
			r.Header.Set("Authorization", strings.Replace(credential, "/route53/", "/s3/", 1))
		} else {
			r.Header.Set("X-Simulator-Service", "s3")
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.String() != "s3-peer" {
			t.Fatal("explicit S3 hijacked", w.Code, w.Body.String())
		}
	}
	// Trusted capability bridge goes directly to S3, irrespective of REST-looking keys.
	r := httptest.NewRequest("GET", "/2013-04-01/hostedzone", nil)
	w := httptest.NewRecorder()
	g.serveS3(w, r)
	if w.Code != 200 || w.Body.String() != "s3-peer" {
		t.Fatal("trusted S3 path changed", w.Code, w.Body.String())
	}
}

type r53ChangingBody struct {
	reader  io.Reader
	change  func()
	changed bool
}

func (b *r53ChangingBody) Read(p []byte) (int, error) {
	if !b.changed {
		b.changed = true
		b.change()
	}
	return b.reader.Read(p)
}
func TestRoute53GatewayImmutablePolicyAndExplicitDeny(t *testing.T) {
	db, s := r53test(t)
	ids := core.NewIdentities(db)
	g := &Gateway{Route53: s, Identities: ids}
	allow := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"route53:CreateHostedZone", "ec2:DescribeVpcs"}, Resource: core.StringList{"*"}}}}
	deny := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Deny", Action: core.StringList{"*"}, Resource: core.StringList{"*"}}}}
	r53must(t, ids.Set("dns-user", allow))
	body := &r53ChangingBody{reader: strings.NewReader(r53createXML("snapshot")), change: func() { r53must(t, ids.Set("dns-user", deny)) }}
	r := httptest.NewRequest("POST", "/2013-04-01/hostedzone", body)
	r.Header.Set("X-Simulator-Principal", "dns-user")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	r = httptest.NewRequest("POST", "/2013-04-01/hostedzone", strings.NewReader(r53createXML("next")))
	r.Header.Set("X-Simulator-Principal", "dns-user")
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("new request reused prior policy", w.Code)
	}
	allow.Statement = append(allow.Statement, core.Statement{Effect: "Deny", Action: core.StringList{"ec2:DescribeVpcs"}, Resource: core.StringList{"*"}})
	r53must(t, ids.Set("dns-user", allow))
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("prerequisite deny ignored")
	}
	for _, gateway := range []*Gateway{{Route53: s}, {Identities: ids}} {
		w = httptest.NewRecorder()
		gateway.serveRoute53(w, httptest.NewRequest("GET", "/2013-04-01/hostedzone", nil))
		if w.Code < 400 {
			t.Fatal("missing dependency allowed")
		}
	}
}
