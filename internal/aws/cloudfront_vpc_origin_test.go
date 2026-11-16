package aws

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type cfHarness struct {
	*elbHarness
	alb, config string
}

func cfHarnessFor(t *testing.T) *cfHarness {
	t.Helper()
	h := newELBHarness(t)
	s, err := NewCloudFrontStore(h.store.Database())
	if err != nil {
		t.Fatal(err)
	}
	h.gateway.CloudFront = s
	lb := elbDecode(t, h.ok("CreateLoadBalancer", "Name", "origin-target", "Scheme", "internal", "Subnets.member.1", "subnet-0123456789abcdef0", "Subnets.member.2", "subnet-89abcdef")).LoadBalancers[0].LoadBalancerArn
	c := "<VpcOriginEndpointConfig><Name>origin-a</Name><Arn>" + lb + "</Arn><HTTPPort>80</HTTPPort><HTTPSPort>443</HTTPSPort><OriginProtocolPolicy>http-only</OriginProtocolPolicy></VpcOriginEndpointConfig>"
	return &cfHarness{h, lb, c}
}
func cfCreateXML(c string) string {
	return "<CreateVpcOriginRequest xmlns=\"" + cloudFrontNS + "\">" + c + "<Tags><Items><Tag><Key>team</Key><Value>trial</Value></Tag></Items></Tags></CreateVpcOriginRequest>"
}
func (h *cfHarness) request(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Simulator-Service", "cloudfront")
	if h.principal != "" {
		r.Header.Set("X-Simulator-Principal", h.principal)
	}
	for i := 0; i < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.gateway.ServeHTTP(w, r)
	return w
}
func (h *cfHarness) origin(w *httptest.ResponseRecorder, code int) CFVpcOrigin {
	h.t.Helper()
	if w.Code != code {
		h.t.Fatalf("want %d, got %d: %s", code, w.Code, w.Body.String())
	}
	var o CFVpcOrigin
	if err := xml.Unmarshal(w.Body.Bytes(), &o); err != nil {
		h.t.Fatal(err)
	}
	o.ETag = w.Header().Get("ETag")
	return o
}
func cfExpectError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status || elbCode(w.Body.String()) != code {
		t.Fatalf("want %d %s, got %d %s", status, code, w.Code, w.Body.String())
	}
}

func TestCloudFrontVpcOriginNativeLifecycle(t *testing.T) {
	h := cfHarnessFor(t)
	created := h.request("POST", cloudFrontPath, cfCreateXML(h.config))
	o := h.origin(created, 202)
	loc := created.Header().Get("Location")
	if loc != cloudFrontPath+"/"+o.ID || !cloudFrontOriginID.MatchString(o.ID) || o.ARN != cloudFrontARNPrefix+o.ID || o.ETag == "" || o.Config.ARN != h.alb || o.Status != "Deployed" {
		t.Fatalf("bad create result %#v, location %q", o, loc)
	}
	cfExpectError(t, h.request("POST", cloudFrontPath, cfCreateXML(h.config)), 409, "EntityAlreadyExists")
	if read := h.origin(h.request("GET", loc, ""), 200); read.ETag != o.ETag || read.CreatedTime != o.CreatedTime {
		t.Fatalf("read mismatch %#v", read)
	}
	if durable, e := h.gateway.CloudFront.Get(t.Context(), o.ID); e != nil || len(durable.Tags) != 1 || durable.Tags[0].Value != "trial" {
		t.Fatal(durable, e)
	}
	cfExpectError(t, h.request("PUT", loc, h.config), 400, "InvalidIfMatchVersion")
	change := strings.Replace(h.config, "origin-a", "renamed", 1)
	updated := h.origin(h.request("PUT", loc, change, "If-Match", o.ETag), 202)
	if updated.ETag == o.ETag || updated.ID != o.ID || updated.Config.Name != "renamed" || updated.CreatedTime != o.CreatedTime {
		t.Fatal(updated)
	}
	cfExpectError(t, h.request("PUT", loc, h.config, "If-Match", o.ETag), 412, "PreconditionFailed")
	cfExpectError(t, h.request("DELETE", loc, "", "If-Match", o.ETag), 412, "PreconditionFailed")
	f := h.origin(h.request("DELETE", loc, "", "If-Match", updated.ETag), 202)
	if f.ID != o.ID || f.Status != "Deleting" {
		t.Fatal(f)
	}
	cfExpectError(t, h.request("GET", loc, ""), 404, "EntityNotFound")
	h.ok("DeleteLoadBalancer", "LoadBalancerArn", h.alb)
}

func TestCloudFrontVpcOriginRejectsInvalidOrUnownedTargets(t *testing.T) {
	h := cfHarnessFor(t)
	public := h.loadBalancer("public").LoadBalancerArn
	for _, v := range []struct{ before, after, code string }{
		{h.alb, "http://127.0.0.1:80/", "InvalidArgument"},
		{h.alb, public, "InvalidArgument"},
		{h.alb, strings.Replace(h.alb, "origin-target", "missing", 1), "InvalidArgument"},
		{"http-only", "https-only", "UnsupportedOperation"},
		{"<HTTPPort>80</HTTPPort>", "<HTTPPort>0</HTTPPort>", "InvalidArgument"},
		{"<HTTPPort>80</HTTPPort>", "<HTTPPort>80</HTTPPort><HTTPPort>81</HTTPPort>", "InvalidArgument"},
		{"<HTTPPort>80</HTTPPort>", "<HTTPPort>80</HTTPPort><VpcTarget>something</VpcTarget>", "InvalidArgument"},
		{"</OriginProtocolPolicy>", "</OriginProtocolPolicy><OriginSslProtocols><Quantity>2</Quantity><Items><SslProtocol>TLSv1.2</SslProtocol></Items></OriginSslProtocols>", "InconsistentQuantities"},
		{"<Key>team</Key>", "<Key>aws:service</Key>", "InvalidArgument"},
		{"</Items></Tags>", "<Tag><Key>team</Key></Tag></Items></Tags>", "InvalidArgument"},
	} {
		cfExpectError(t, h.request("POST", cloudFrontPath, strings.Replace(cfCreateXML(h.config), v.before, v.after, 1)), 400, v.code)
	}
	for _, body := range []string{"", cfCreateXML(h.config) + "<extra/>", "<!DOCTYPE x>" + cfCreateXML(h.config), strings.Repeat("x", 33000)} {
		cfExpectError(t, h.request("POST", cloudFrontPath, body), 400, "InvalidArgument")
	}
	h.origin(h.request("POST", cloudFrontPath, cfCreateXML(h.config)), 202)
	if r := h.call("DeleteLoadBalancer", "LoadBalancerArn", h.alb); r.Code != 400 || elbCode(r.Body.String()) != "ResourceInUse" {
		t.Fatalf("deleting a referenced ALB: %d %s", r.Code, r.Body.String())
	}
}

func TestCloudFrontVpcOriginAuthorizesExactARNAndFailsClosed(t *testing.T) {
	h := cfHarnessFor(t)
	o := h.origin(h.request("POST", cloudFrontPath, cfCreateXML(h.config)), 202)
	policy, err := core.ParsePolicy([]byte(fmt.Sprintf("{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"cloudfront:GetVpcOrigin\",\"Resource\":%q}]}", o.ARN)))
	if err != nil {
		t.Fatal(err)
	}
	if err = h.gateway.Identities.Set("reader", policy); err != nil {
		t.Fatal(err)
	}
	h.principal = "reader"
	h.origin(h.request("GET", cloudFrontPath+"/"+o.ID, ""), 200)
	cfExpectError(t, h.request("GET", cloudFrontPath+"/vo_0000000000000000", ""), 403, "AccessDenied")
	cfExpectError(t, h.request("DELETE", cloudFrontPath+"/"+o.ID, "", "If-Match", o.ETag), 403, "AccessDenied")
	cfExpectError(t, h.request("POST", cloudFrontPath, cfCreateXML(h.config)), 403, "AccessDenied")
	cfExpectError(t, h.request("GET", cloudFrontPath+"/"+o.ID, "", "X-Amz-Security-Token", "dummy"), 403, "AccessDenied")
	for _, scope := range []string{"wrong", "AWS4-HMAC-SHA256 Credential=TCSIMTEST/20250101/us-west-2/cloudfront/aws4_request, SignedHeaders=host, Signature=local"} {
		cfExpectError(t, h.request("GET", cloudFrontPath+"/"+o.ID, "", "Authorization", scope), 403, "AccessDenied")
	}
	r := httptest.NewRequest(http.MethodGet, cloudFrontPath+"/"+o.ID, nil)
	r.Header.Set("X-Simulator-Service", "s3")
	if isCloudFrontVpcOrigin(r) {
		t.Fatal("S3 key routed to CloudFront")
	}
}

func TestCloudFrontVpcOriginTransactionsRollBack(t *testing.T) {
	h := cfHarnessFor(t)
	c, tags, err := parseCFOriginRequest([]byte(cfCreateXML(h.config)), true)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := h.store.Database().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	o, err := saveCFOriginTx(t.Context(), tx, "vo_0000000000000001", "", true, c, tags)
	if err != nil {
		t.Fatal(err)
	}
	check, err := getCFOrigin(t.Context(), tx, o.ID)
	if err != nil || check.ETag != o.ETag {
		t.Fatal(check, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	cfExpectError(t, h.request("GET", cloudFrontPath+"/"+o.ID, ""), 404, "EntityNotFound")
}
