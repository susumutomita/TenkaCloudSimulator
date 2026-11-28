package aws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Exercise a stack-authored endpoint through its native API. The dedicated
// origin assumes the new load balancer reference only after the update commits.
func TestVpcOriginContractStackConfigurationAndTargetTransfer(t *testing.T) {
	h := cfHarnessFor(t)
	stacks, err := NewStackStore(h.store.Database())
	if err != nil {
		t.Fatal(err)
	}
	h.gateway.Stacks = stacks
	create := func(name, extra string) *httptest.ResponseRecorder {
		t.Helper()
		template := strings.Replace(vpcOriginTemplate, "OriginProtocolPolicy: http-only", "OriginProtocolPolicy: http-only\n"+extra, 1)
		params := url.Values{"Action": {"CreateStack"}, "Version": {"2010-05-15"}, "StackName": {name}, "TemplateBody": {template},
			"Parameters.member.1.ParameterKey": {"EndpointArn"}, "Parameters.member.1.ParameterValue": {h.alb}}
		return h.raw(map[string]string{"X-Simulator-Service": "cloudformation"}, params.Encode())
	}
	for _, tc := range []struct{ field, message string }{
		{"HTTPPort: none", "HTTPPort must be a TCP port"},
		{"OriginSSLProtocols: TLSv1.2", "OriginSSLProtocols must be an array"},
		{"OriginSSLProtocols: [TLSv1.1]", "Only TLSv1.2"},
	} {
		if w := create("invalid-port-or-protocol", "        "+tc.field); w.Code != 400 || elbCode(w.Body.String()) != "ValidationError" || !strings.Contains(w.Body.String(), tc.message) {
			t.Fatalf("invalid endpoint settings %q: %d %s", tc.field, w.Code, w.Body.String())
		}
	}
	if w := create("custom-ports", "        HTTPPort: 8080\n        HTTPSPort: 8443\n        OriginSSLProtocols: [TLSv1.2]"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	rows, err := stacks.Records("us-east-1", "custom-ports")
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	path := cloudFrontPath + "/" + rows[0].Outputs["Id"]
	original := h.origin(h.request("GET", path, ""), 200)
	protocols := original.Config.OriginSslProtocols
	if original.Config.HTTPPort != 8080 || original.Config.HTTPSPort != 8443 || protocols == nil || protocols.Quantity != 1 || len(protocols.Items) != 1 || protocols.Items[0] != "TLSv1.2" {
		t.Fatal(original.Config)
	}
	newLB := elbDecode(t, h.ok("CreateLoadBalancer", "Name", "fresh-target", "Scheme", "internal", "Subnets.member.1", h.network.a, "Subnets.member.2", h.network.b, "SecurityGroups.member.1", h.network.group)).LoadBalancers[0].LoadBalancerArn
	config := strings.Replace(h.config, h.alb, newLB, 1)
	changed := h.origin(h.request("PUT", path, config, "If-Match", original.ETag), 202)
	if changed.Config.ARN != newLB || changed.ID != original.ID || changed.ETag == original.ETag {
		t.Fatal(changed)
	}
	h.ok("DeleteLoadBalancer", "LoadBalancerArn", h.alb)
	h.fail("ResourceInUse", 400, "DeleteLoadBalancer", "LoadBalancerArn", newLB)
}

func TestVpcOriginContractProtocolAndWireBoundaries(t *testing.T) {
	h := cfHarnessFor(t)
	template := cfCreateXML(h.config)
	for _, tc := range []struct{ name, body string }{
		{"blank name", strings.Replace(template, "origin-a", "  ", 1)},
		{"non-numeric port", strings.Replace(template, ">80<", ">none<", 1)},
		{"foreign namespace", strings.Replace(template, cloudFrontNS, "urn:example:other", 1)},
		{"attribute on scalar", strings.Replace(template, "<Name>", "<Name extra=\"yes\">", 1)},
		{"text in configuration", strings.Replace(template, "<Name>", "text<Name>", 1)},
		{"processing instruction in configuration", strings.Replace(template, "<Name>", "<?work example?><Name>", 1)},
		{"unsupported SSL policy", strings.Replace(template, "</OriginProtocolPolicy>", "</OriginProtocolPolicy><OriginSslProtocols><Quantity>1</Quantity><Items><SslProtocol>TLSv1.1</SslProtocol></Items></OriginSslProtocols>", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfExpectError(t, h.request("POST", cloudFrontPath, tc.body), 400, "InvalidArgument")
		})
	}
	valid := "<?xml version=\"1.0\"?><!-- integration -->" + template
	o := h.origin(h.request("POST", cloudFrontPath, valid), 202)
	path := cloudFrontPath + "/" + o.ID
	for _, tc := range []struct{ name, method, path, body string }{
		{"query on item", "GET", path + "?page=1", ""},
		{"wrong resource identifier", "GET", cloudFrontPath + "/unrecognized", ""},
		{"unsupported item verb", "PATCH", path, ""},
		{"Get with body", "GET", path, "<Options/>"},
		{"Delete with body", "DELETE", path, "<Options/>"},
	} {
		t.Run(tc.name, func(t *testing.T) { cfExpectError(t, h.request(tc.method, tc.path, tc.body), 400, "InvalidArgument") })
	}
	cfExpectError(t, h.request("GET", path, "", "X-Simulator-Region", "eu-west-1"), 400, "InvalidArgument")
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIMTEST/20261117/us-east-1/cloudfront/aws4_request, SignedHeaders=host, Signature=local")
	for _, duplicate := range []bool{false, true} {
		if duplicate {
			req.Header.Add("If-Match", o.ETag)
			req.Header.Add("If-Match", o.ETag)
		}
		w := httptest.NewRecorder()
		h.gateway.ServeHTTP(w, req)
		if duplicate {
			cfExpectError(t, w, 400, "InvalidArgument")
		} else if got := h.origin(w, 200); got.ID != o.ID || got.ETag != o.ETag {
			t.Fatal(got)
		}
	}
}

func TestVpcOriginContractCapacityAndRetry(t *testing.T) {
	h := cfHarnessFor(t)
	body := func(i int) string {
		return cfCreateXML(strings.Replace(h.config, "origin-a", fmt.Sprintf("origin-%d", i), 1))
	}
	var first CFVpcOrigin
	for i := 0; i < 50; i++ {
		got := h.origin(h.request("POST", cloudFrontPath, body(i)), 202)
		if i == 0 {
			first = got
		}
	}
	cfExpectError(t, h.request("POST", cloudFrontPath, body(50)), 400, "EntityLimitExceeded")
	path := cloudFrontPath + "/" + first.ID
	modified := h.origin(h.request("PUT", path, h.config, "If-Match", first.ETag), 202)
	h.origin(h.request("DELETE", path, "", "If-Match", modified.ETag), 202)
	h.origin(h.request("POST", cloudFrontPath, body(50)), 202)
	var tags strings.Builder
	for i := 0; i < 51; i++ {
		fmt.Fprintf(&tags, "<Tag><Key>key-%d</Key><Value>local</Value></Tag>", i)
	}
	tagged := "<CreateVpcOriginRequest>" + h.config + "<Tags><Items>" + tags.String() + "</Items></Tags></CreateVpcOriginRequest>"
	cfExpectError(t, h.request("POST", cloudFrontPath, tagged), 400, "InvalidArgument")
	cfExpectError(t, h.request("PUT", path, h.config, "If-Match", modified.ETag), 404, "EntityNotFound")
}

func TestVpcOriginContractStorageFailureRollsBack(t *testing.T) {
	h := cfHarnessFor(t)
	db := h.store.Database()
	for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
		t.Run(operation, func(t *testing.T) {
			if _, err := db.Exec("CREATE TRIGGER origin_write_failure BEFORE " + operation + " ON aws_cloudfront_vpc_origins BEGIN SELECT RAISE(ABORT, 'private storage detail'); END"); err != nil {
				t.Fatal(err)
			}
			var original CFVpcOrigin
			if operation != "INSERT" {
				original = h.origin(h.request("POST", cloudFrontPath, cfCreateXML(h.config)), 202)
			}
			path := cloudFrontPath + "/" + original.ID
			var w *httptest.ResponseRecorder
			switch operation {
			case "INSERT":
				w = h.request("POST", cloudFrontPath, cfCreateXML(h.config))
			case "UPDATE":
				w = h.request("PUT", path, strings.Replace(h.config, "origin-a", "replacement", 1), "If-Match", original.ETag)
			case "DELETE":
				w = h.request("DELETE", path, "", "If-Match", original.ETag)
			}
			cfExpectError(t, w, 500, "InternalError")
			if strings.Contains(w.Body.String(), "private storage detail") || w.Header().Get("ETag") != "" || w.Header().Get("Location") != "" {
				t.Fatal(w.Body.String(), w.Header())
			}
			if _, err := db.Exec("DROP TRIGGER origin_write_failure"); err != nil {
				t.Fatal(err)
			}
			if operation == "INSERT" {
				original = h.origin(h.request("POST", cloudFrontPath, cfCreateXML(h.config)), 202)
				path = cloudFrontPath + "/" + original.ID
			} else if got := h.origin(h.request("GET", path, ""), 200); got.ETag != original.ETag || got.Config.Name != original.Config.Name {
				t.Fatal(got)
			}
			h.origin(h.request("DELETE", path, "", "If-Match", original.ETag), 202)
		})
	}
}

func TestVpcOriginContractCancelledWritesLeaveRevisionAvailable(t *testing.T) {
	h := cfHarnessFor(t)
	store := h.gateway.CloudFront
	o := h.origin(h.request("POST", cloudFrontPath, cfCreateXML(h.config)), 202)
	path := cloudFrontPath + "/" + o.ID
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Update(ctx, o.ID, o.ETag, o.Config); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.Delete(ctx, o.ID, o.ETag); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, o.ID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(ctx, "GET", path, nil)
	req.Header.Set("X-Simulator-Service", "cloudfront")
	req.Header.Set("X-Simulator-Principal", "reader")
	w := httptest.NewRecorder()
	h.gateway.ServeHTTP(w, req)
	cfExpectError(t, w, 500, "InternalError")
	if got := h.origin(h.request("GET", path, ""), 200); got.ETag != o.ETag {
		t.Fatal(got)
	}
	h.origin(h.request("DELETE", path, "", "If-Match", o.ETag), 202)
}

func TestVpcOriginContractCorruptMetadataCanBeRestoredWithoutLosingRevision(t *testing.T) {
	h := cfHarnessFor(t)
	db := h.store.Database()
	o := h.origin(h.request("POST", cloudFrontPath, cfCreateXML(h.config)), 202)
	path := cloudFrontPath + "/" + o.ID
	var raw string
	if err := db.QueryRow("SELECT document FROM aws_cloudfront_vpc_origins WHERE id=?", o.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE aws_cloudfront_vpc_origins SET document='broken' WHERE id=?", o.ID); err != nil {
		t.Fatal(err)
	}
	cfExpectError(t, h.request("GET", path, ""), 500, "InternalError")
	cfExpectError(t, h.request("PUT", path, h.config, "If-Match", o.ETag), 500, "InternalError")
	cfExpectError(t, h.request("DELETE", path, "", "If-Match", o.ETag), 500, "InternalError")
	if _, err := db.Exec("UPDATE aws_cloudfront_vpc_origins SET document=? WHERE id=?", raw, o.ID); err != nil {
		t.Fatal(err)
	}
	var albRaw string
	if err := db.QueryRow("SELECT document FROM aws_elb_load_balancers WHERE arn=?", h.alb).Scan(&albRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE aws_elb_load_balancers SET document='broken' WHERE arn=?", h.alb); err != nil {
		t.Fatal(err)
	}
	cfExpectError(t, h.request("PUT", path, h.config, "If-Match", o.ETag), 500, "InternalError")
	if _, err := db.Exec("UPDATE aws_elb_load_balancers SET document=? WHERE arn=?", albRaw, h.alb); err != nil {
		t.Fatal(err)
	}
	if got := h.origin(h.request("GET", path, ""), 200); got.ETag != o.ETag || got.Config.ARN != h.alb {
		t.Fatal(got)
	}
	h.origin(h.request("DELETE", path, "", "If-Match", o.ETag), 202)
	h.ok("DeleteLoadBalancer", "LoadBalancerArn", h.alb)
}

func TestVpcOriginContractStackOwnershipFailureCanBeRetried(t *testing.T) {
	h := cfHarnessFor(t)
	db := h.store.Database()
	stacks, err := NewStackStore(db)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := compileStack("us-east-1", "retry-stack", vpcOriginTemplate, map[string]string{"EndpointArn": h.alb}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER ownership_write_failure BEFORE INSERT ON aws_cfn_ownership BEGIN SELECT RAISE(ABORT, 'test refusal'); END"); err != nil {
		t.Fatal(err)
	}
	if err := stacks.Create(plan); err == nil {
		t.Fatal("expected ownership write failure")
	}
	if _, err := stacks.Records("us-east-1", "retry-stack"); err == nil {
		t.Fatal("stack persisted after ownership write failure")
	}
	path := cloudFrontPath + "/" + plan.Outputs["Id"]
	cfExpectError(t, h.request("GET", path, ""), 404, "EntityNotFound")
	if _, err := db.Exec("DROP TRIGGER ownership_write_failure"); err != nil {
		t.Fatal(err)
	}
	if err := stacks.Create(plan); err != nil {
		t.Fatal(err)
	}
	h.origin(h.request("GET", path, ""), 200)
	if err := stacks.Delete("us-east-1", "retry-stack"); err != nil {
		t.Fatal(err)
	}
	cfExpectError(t, h.request("GET", path, ""), 404, "EntityNotFound")
}
