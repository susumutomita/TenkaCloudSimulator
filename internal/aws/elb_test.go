package aws

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type elbHarness struct {
	t         *testing.T
	gateway   *Gateway
	store     *core.Store
	principal string
}

func newELBHarness(t *testing.T) *elbHarness {
	t.Helper()
	db, functions, identities := lambdaState(t)
	permissions, err := NewLambdaPermissionStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	elb, err := NewELBStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	return &elbHarness{t: t, store: db, gateway: &Gateway{ELB: elb, Store: db, Identities: identities, Lambdas: functions, LambdaPermissions: permissions}}
}

func (h *elbHarness) raw(headers map[string]string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Simulator-Service", "elasticloadbalancing")
	if h.principal != "" {
		r.Header.Set("X-Simulator-Principal", h.principal)
	}
	for name, value := range headers {
		if value == "" {
			r.Header.Del(name)
		} else {
			r.Header.Set(name, value)
		}
	}
	w := httptest.NewRecorder()
	h.gateway.ServeHTTP(w, r)
	return w
}

// call sends one ELBv2 Query action with alternating field names and values.
func (h *elbHarness) call(action string, fields ...string) *httptest.ResponseRecorder {
	form := url.Values{"Action": {action}, "Version": {"2015-12-01"}}
	for i := 0; i+1 < len(fields); i += 2 {
		form.Set(fields[i], fields[i+1])
	}
	return h.raw(nil, form.Encode())
}
func (h *elbHarness) ok(action string, fields ...string) string {
	h.t.Helper()
	w := h.call(action, fields...)
	if w.Code != 200 {
		h.t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
	}
	return w.Body.String()
}
func (h *elbHarness) fail(code string, status int, action string, fields ...string) string {
	h.t.Helper()
	w := h.call(action, fields...)
	if w.Code != status || elbCode(w.Body.String()) != code {
		h.t.Fatalf("%s: want %d %s, got %d %s", action, status, code, w.Code, w.Body.String())
	}
	return w.Body.String()
}
func elbCode(body string) string {
	var response struct {
		Error struct{ Code string } `xml:"Error"`
	}
	_ = xml.Unmarshal([]byte(body), &response)
	return response.Error.Code
}

type elbDecoded struct {
	LoadBalancers []elbLBWire       `xml:"LoadBalancers>member"`
	TargetGroups  []elbTGWire       `xml:"TargetGroups>member"`
	Listeners     []elbListenerWire `xml:"Listeners>member"`
	Health        []elbHealthWire   `xml:"TargetHealthDescriptions>member"`
	NextMarker    string
}

func elbDecode(t *testing.T, body string) elbDecoded {
	t.Helper()
	var response struct {
		Result elbDecoded `xml:",any"`
	}
	if err := xml.Unmarshal([]byte(body), &response); err != nil {
		t.Fatal(err, body)
	}
	return response.Result
}

func (h *elbHarness) loadBalancer(name string) elbLBWire {
	h.t.Helper()
	return elbDecode(h.t, h.ok("CreateLoadBalancer", "Name", name, "Subnets.member.1", "subnet-0123456789abcdef0", "Subnets.member.2", "subnet-89abcdef")).LoadBalancers[0]
}
func (h *elbHarness) targetGroup(name string, fields ...string) elbTGWire {
	h.t.Helper()
	return elbDecode(h.t, h.ok("CreateTargetGroup", append([]string{"Name", name, "TargetType", "lambda"}, fields...)...)).TargetGroups[0]
}
func (h *elbHarness) forward(lb, tg string, port string) elbListenerWire {
	h.t.Helper()
	return elbDecode(h.t, h.ok("CreateListener", "LoadBalancerArn", lb, "Protocol", "HTTP", "Port", port, "DefaultActions.member.1.Type", "forward", "DefaultActions.member.1.TargetGroupArn", tg)).Listeners[0]
}

func TestELBLoadBalancerTargetGroupAndListenerLifecycle(t *testing.T) {
	h := newELBHarness(t)
	lb := h.loadBalancer("web")
	if lb.State.Code != "active" || lb.Type != "application" || lb.Scheme != "internet-facing" || len(lb.AvailabilityZones) != 2 || lb.IPAddressType != "ipv4" {
		t.Fatalf("%+v", lb)
	}
	if !elbLBARNPattern.MatchString(lb.LoadBalancerArn) || !strings.HasSuffix(lb.DNSName, ".us-east-1.elb.simulator.invalid") || !strings.HasPrefix(lb.DNSName, "web-") {
		t.Fatal(lb.LoadBalancerArn, lb.DNSName)
	}
	// Identical settings, including reordered subnets, return the same resource.
	again := elbDecode(t, h.ok("CreateLoadBalancer", "Name", "web", "Subnets.member.1", "subnet-89abcdef", "Subnets.member.2", "subnet-0123456789abcdef0")).LoadBalancers[0]
	if again.LoadBalancerArn != lb.LoadBalancerArn {
		t.Fatal("idempotent create allocated a second load balancer")
	}
	h.fail("DuplicateLoadBalancerName", 400, "CreateLoadBalancer", "Name", "web", "Scheme", "internal", "Subnets.member.1", "subnet-89abcdef", "Subnets.member.2", "subnet-0123456789abcdef0")
	internal := elbDecode(t, h.ok("CreateLoadBalancer", "Name", "Inside", "Scheme", "internal", "Type", "application", "IpAddressType", "ipv4", "Subnets.member.1", "subnet-11111111", "Subnets.member.2", "subnet-22222222", "SecurityGroups.member.1", "sg-12345678")).LoadBalancers[0]
	if !strings.HasPrefix(internal.DNSName, "internal-inside-") || len(internal.SecurityGroups) != 1 {
		t.Fatalf("%+v", internal)
	}

	tg := h.targetGroup("fn")
	if tg.HealthCheckEnabled || tg.HealthCheckIntervalSeconds != 35 || tg.HealthCheckTimeoutSeconds != 30 || tg.HealthyThresholdCount != 5 || tg.UnhealthyThresholdCount != 5 || tg.HealthCheckPath != "/" || tg.Matcher.HTTPCode != "200" || tg.TargetType != "lambda" || len(tg.LoadBalancerArns) != 0 {
		t.Fatalf("documented lambda defaults changed: %+v", tg)
	}
	if h.targetGroup("fn").TargetGroupArn != tg.TargetGroupArn {
		t.Fatal("idempotent target group create allocated a second resource")
	}
	h.fail("DuplicateTargetGroupName", 400, "CreateTargetGroup", "Name", "fn", "TargetType", "lambda", "HealthCheckEnabled", "true")

	fixed := elbDecode(t, h.ok("CreateListener", "LoadBalancerArn", lb.LoadBalancerArn, "Protocol", "HTTP", "Port", "8080", "DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "418", "DefaultActions.member.1.FixedResponseConfig.ContentType", "text/plain", "DefaultActions.member.1.FixedResponseConfig.MessageBody", "teapot")).Listeners[0]
	if fixed.DefaultActions[0].FixedResponseConfig.StatusCode != "418" || fixed.DefaultActions[0].FixedResponseConfig.MessageBody != "teapot" || !elbLsARNPattern.MatchString(fixed.ListenerArn) {
		t.Fatalf("%+v", fixed)
	}
	listener := h.forward(lb.LoadBalancerArn, tg.TargetGroupArn, "80")
	action := listener.DefaultActions[0]
	if action.TargetGroupArn != tg.TargetGroupArn || action.ForwardConfig == nil || action.ForwardConfig.TargetGroups[0].Weight != 1 || action.ForwardConfig.TargetGroupStickinessConfig.Enabled {
		t.Fatalf("%+v", action)
	}
	if h.forward(lb.LoadBalancerArn, tg.TargetGroupArn, "80").ListenerArn != listener.ListenerArn {
		t.Fatal("idempotent listener create allocated a second listener")
	}
	h.fail("DuplicateListener", 400, "CreateListener", "LoadBalancerArn", lb.LoadBalancerArn, "Protocol", "HTTP", "Port", "80", "DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "200")
	h.fail("TargetGroupAssociationLimit", 400, "CreateListener", "LoadBalancerArn", internal.LoadBalancerArn, "Protocol", "HTTP", "Port", "80", "DefaultActions.member.1.Type", "forward", "DefaultActions.member.1.ForwardConfig.TargetGroups.member.1.TargetGroupArn", tg.TargetGroupArn)
	second := h.forward(lb.LoadBalancerArn, tg.TargetGroupArn, "81")

	described := elbDecode(t, h.ok("DescribeTargetGroups", "LoadBalancerArn", lb.LoadBalancerArn)).TargetGroups
	if len(described) != 1 || len(described[0].LoadBalancerArns) != 1 || described[0].LoadBalancerArns[0] != lb.LoadBalancerArn {
		t.Fatalf("%+v", described)
	}
	listeners := elbDecode(t, h.ok("DescribeListeners", "LoadBalancerArn", lb.LoadBalancerArn)).Listeners
	if len(listeners) != 3 || listeners[0].Port != 80 || listeners[1].Port != 81 || listeners[2].Port != 8080 {
		t.Fatalf("%+v", listeners)
	}
	if got := elbDecode(t, h.ok("DescribeListeners", "ListenerArns.member.1", second.ListenerArn)).Listeners; len(got) != 1 || got[0].Port != 81 {
		t.Fatalf("%+v", got)
	}
	h.fail("ResourceInUse", 400, "DeleteTargetGroup", "TargetGroupArn", tg.TargetGroupArn)
	h.ok("DeleteListener", "ListenerArn", second.ListenerArn)
	h.fail("ListenerNotFound", 400, "DeleteListener", "ListenerArn", second.ListenerArn)
	h.fail("ListenerNotFound", 400, "DescribeListeners", "ListenerArns.member.1", second.ListenerArn)

	// Deleting the load balancer deletes its listeners and releases the group.
	h.ok("DeleteLoadBalancer", "LoadBalancerArn", lb.LoadBalancerArn)
	h.ok("DeleteLoadBalancer", "LoadBalancerArn", lb.LoadBalancerArn)
	h.fail("LoadBalancerNotFound", 400, "DescribeListeners", "LoadBalancerArn", lb.LoadBalancerArn)
	h.fail("ListenerNotFound", 400, "DescribeListeners", "ListenerArns.member.1", listener.ListenerArn)
	h.fail("LoadBalancerNotFound", 400, "DescribeLoadBalancers", "LoadBalancerArns.member.1", lb.LoadBalancerArn)
	h.fail("LoadBalancerNotFound", 400, "DescribeTargetGroups", "LoadBalancerArn", lb.LoadBalancerArn)
	if got := elbDecode(t, h.ok("DescribeTargetGroups", "Names.member.1", "fn")).TargetGroups; len(got) != 1 || len(got[0].LoadBalancerArns) != 0 {
		t.Fatalf("%+v", got)
	}
	h.ok("DeleteTargetGroup", "TargetGroupArn", tg.TargetGroupArn)
	h.ok("DeleteTargetGroup", "TargetGroupArn", tg.TargetGroupArn)
	h.fail("TargetGroupNotFound", 400, "DescribeTargetGroups", "TargetGroupArns.member.1", tg.TargetGroupArn)
	h.fail("TargetGroupNotFound", 400, "DescribeTargetHealth", "TargetGroupArn", tg.TargetGroupArn)
	h.fail("LoadBalancerNotFound", 400, "CreateListener", "LoadBalancerArn", lb.LoadBalancerArn, "Protocol", "HTTP", "Port", "80", "DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "200")
	h.fail("TargetGroupNotFound", 400, "CreateListener", "LoadBalancerArn", internal.LoadBalancerArn, "Protocol", "HTTP", "Port", "80", "DefaultActions.member.1.Type", "forward", "DefaultActions.member.1.TargetGroupArn", tg.TargetGroupArn)
	if all := elbDecode(t, h.ok("DescribeLoadBalancers")).LoadBalancers; len(all) != 1 || all[0].LoadBalancerName != "Inside" {
		t.Fatalf("%+v", all)
	}
	// Other regions are separate scopes and foreign-region ARNs are refused.
	other := h.raw(map[string]string{"X-Simulator-Region": "eu-west-1"}, "Action=DescribeLoadBalancers&Version=2015-12-01")
	if other.Code != 200 || len(elbDecode(t, other.Body.String()).LoadBalancers) != 0 {
		t.Fatal(other.Body.String())
	}
	foreign := h.raw(map[string]string{"X-Simulator-Region": "eu-west-1"}, url.Values{"Action": {"DeleteLoadBalancer"}, "Version": {"2015-12-01"}, "LoadBalancerArn": {internal.LoadBalancerArn}}.Encode())
	if elbCode(foreign.Body.String()) != "ValidationError" {
		t.Fatal(foreign.Body.String())
	}
}

func TestELBDescribePagesAreBoundToTheirRequest(t *testing.T) {
	h := newELBHarness(t)
	for _, name := range []string{"c", "a", "b"} {
		h.loadBalancer(name)
		h.targetGroup("tg-" + name)
	}
	first := elbDecode(t, h.ok("DescribeLoadBalancers", "PageSize", "2"))
	if len(first.LoadBalancers) != 2 || first.LoadBalancers[0].LoadBalancerName != "a" || first.NextMarker == "" {
		t.Fatalf("%+v", first)
	}
	rest := elbDecode(t, h.ok("DescribeLoadBalancers", "PageSize", "2", "Marker", first.NextMarker))
	if len(rest.LoadBalancers) != 1 || rest.LoadBalancers[0].LoadBalancerName != "c" || rest.NextMarker != "" {
		t.Fatalf("%+v", rest)
	}
	h.fail("ValidationError", 400, "DescribeTargetGroups", "Marker", first.NextMarker)
	h.fail("ValidationError", 400, "DescribeLoadBalancers", "Marker", "ELB1.not-base64!")
	h.fail("ValidationError", 400, "DescribeLoadBalancers", "PageSize", "401")
	groups := elbDecode(t, h.ok("DescribeTargetGroups", "PageSize", "1"))
	if len(groups.TargetGroups) != 1 || groups.TargetGroups[0].TargetGroupName != "tg-a" {
		t.Fatalf("%+v", groups)
	}
	named := elbDecode(t, h.ok("DescribeLoadBalancers", "Names.member.1", "b", "Names.member.2", "a", "Names.member.3", "b")).LoadBalancers
	if len(named) != 2 || named[0].LoadBalancerName != "a" {
		t.Fatalf("%+v", named)
	}
	h.fail("LoadBalancerNotFound", 400, "DescribeLoadBalancers", "Names.member.1", "missing")
	h.fail("TargetGroupNotFound", 400, "DescribeTargetGroups", "Names.member.1", "missing")
	lb := named[0].LoadBalancerArn
	for i := 1; i <= 3; i++ {
		h.ok("CreateListener", "LoadBalancerArn", lb, "Protocol", "HTTP", "Port", []string{"", "9000", "80", "443"}[i], "DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "204")
	}
	page := elbDecode(t, h.ok("DescribeListeners", "LoadBalancerArn", lb, "PageSize", "2"))
	if len(page.Listeners) != 2 || page.Listeners[0].Port != 80 || page.Listeners[1].Port != 443 {
		t.Fatalf("%+v", page)
	}
	last := elbDecode(t, h.ok("DescribeListeners", "LoadBalancerArn", lb, "PageSize", "2", "Marker", page.NextMarker))
	if len(last.Listeners) != 1 || last.Listeners[0].Port != 9000 {
		t.Fatalf("%+v", last)
	}
}

func TestELBRefusesUnsupportedOrInvalidInputWithoutChangingState(t *testing.T) {
	h := newELBHarness(t)
	lb := h.loadBalancer("valid")
	tg := h.targetGroup("valid")
	two := []string{"Subnets.member.1", "subnet-11111111", "Subnets.member.2", "subnet-22222222"}
	listen := func(extra ...string) []string {
		return append([]string{"LoadBalancerArn", lb.LoadBalancerArn, "Protocol", "HTTP", "Port", "80"}, extra...)
	}
	forward := func(extra ...string) []string {
		return listen(append([]string{"DefaultActions.member.1.Type", "forward"}, extra...)...)
	}
	for _, c := range []struct {
		name, code, action string
		fields             []string
	}{
		{"name too long", "ValidationError", "CreateLoadBalancer", append([]string{"Name", strings.Repeat("a", 33)}, two...)},
		{"name hyphen", "ValidationError", "CreateLoadBalancer", append([]string{"Name", "-a"}, two...)},
		{"internal prefix", "ValidationError", "CreateLoadBalancer", append([]string{"Name", "internal-a"}, two...)},
		{"network type", "InvalidConfigurationRequest", "CreateLoadBalancer", append([]string{"Name", "n", "Type", "network"}, two...)},
		{"scheme", "InvalidScheme", "CreateLoadBalancer", append([]string{"Name", "n", "Scheme", "public"}, two...)},
		{"dualstack", "InvalidConfigurationRequest", "CreateLoadBalancer", append([]string{"Name", "n", "IpAddressType", "dualstack"}, two...)},
		{"one subnet", "InvalidConfigurationRequest", "CreateLoadBalancer", []string{"Name", "n", "Subnets.member.1", "subnet-11111111"}},
		{"duplicate subnet", "ValidationError", "CreateLoadBalancer", []string{"Name", "n", "Subnets.member.1", "subnet-11111111", "Subnets.member.2", "subnet-11111111"}},
		{"malformed subnet", "ValidationError", "CreateLoadBalancer", []string{"Name", "n", "Subnets.member.1", "subnet-1", "Subnets.member.2", "subnet-22222222"}},
		{"gap in list", "ValidationError", "CreateLoadBalancer", []string{"Name", "n", "Subnets.member.1", "subnet-11111111", "Subnets.member.3", "subnet-22222222"}},
		{"bad group", "ValidationError", "CreateLoadBalancer", append([]string{"Name", "n", "SecurityGroups.member.1", "group"}, two...)},
		{"subnet mappings", "ValidationError", "CreateLoadBalancer", append([]string{"Name", "n", "SubnetMappings.member.1.SubnetId", "subnet-33333333"}, two...)},
		{"tags", "ValidationError", "CreateLoadBalancer", append([]string{"Name", "n", "Tags.member.1.Key", "k"}, two...)},
		{"instance targets", "InvalidConfigurationRequest", "CreateTargetGroup", []string{"Name", "n"}},
		{"ip targets", "InvalidConfigurationRequest", "CreateTargetGroup", []string{"Name", "n", "TargetType", "ip"}},
		{"lambda protocol", "InvalidConfigurationRequest", "CreateTargetGroup", []string{"Name", "n", "TargetType", "lambda", "Protocol", "HTTP"}},
		{"timeout interval", "InvalidConfigurationRequest", "CreateTargetGroup", []string{"Name", "n", "TargetType", "lambda", "HealthCheckIntervalSeconds", "10", "HealthCheckTimeoutSeconds", "10"}},
		{"interval range", "ValidationError", "CreateTargetGroup", []string{"Name", "n", "TargetType", "lambda", "HealthCheckIntervalSeconds", "4"}},
		{"threshold", "ValidationError", "CreateTargetGroup", []string{"Name", "n", "TargetType", "lambda", "HealthyThresholdCount", "11"}},
		{"matcher", "ValidationError", "CreateTargetGroup", []string{"Name", "n", "TargetType", "lambda", "Matcher.HttpCode", "200-599"}},
		{"matcher order", "ValidationError", "CreateTargetGroup", []string{"Name", "n", "TargetType", "lambda", "Matcher.HttpCode", "300-200"}},
		{"grpc matcher", "ValidationError", "CreateTargetGroup", []string{"Name", "n", "TargetType", "lambda", "Matcher.GrpcCode", "12"}},
		{"health path", "ValidationError", "CreateTargetGroup", []string{"Name", "n", "TargetType", "lambda", "HealthCheckPath", "health"}},
		{"health path space", "ValidationError", "CreateTargetGroup", []string{"Name", "n", "TargetType", "lambda", "HealthCheckPath", "/a b"}},
		{"health bool", "ValidationError", "CreateTargetGroup", []string{"Name", "n", "TargetType", "lambda", "HealthCheckEnabled", "yes"}},
		{"https", "UnsupportedProtocol", "CreateListener", []string{"LoadBalancerArn", lb.LoadBalancerArn, "Protocol", "HTTPS", "Port", "443", "DefaultActions.member.1.Type", "forward", "DefaultActions.member.1.TargetGroupArn", tg.TargetGroupArn}},
		{"protocol", "ValidationError", "CreateListener", []string{"LoadBalancerArn", lb.LoadBalancerArn, "Protocol", "http", "Port", "80"}},
		{"no port", "ValidationError", "CreateListener", []string{"LoadBalancerArn", lb.LoadBalancerArn, "Protocol", "HTTP"}},
		{"port range", "ValidationError", "CreateListener", []string{"LoadBalancerArn", lb.LoadBalancerArn, "Protocol", "HTTP", "Port", "65536"}},
		{"port spelling", "ValidationError", "CreateListener", []string{"LoadBalancerArn", lb.LoadBalancerArn, "Protocol", "HTTP", "Port", "080"}},
		{"no action", "InvalidLoadBalancerAction", "CreateListener", listen()},
		{"two actions", "InvalidLoadBalancerAction", "CreateListener", forward("DefaultActions.member.1.TargetGroupArn", tg.TargetGroupArn, "DefaultActions.member.2.Type", "forward")},
		{"redirect", "InvalidLoadBalancerAction", "CreateListener", listen("DefaultActions.member.1.Type", "redirect")},
		{"unknown action", "ValidationError", "CreateListener", listen("DefaultActions.member.1.Type", "drop")},
		{"no group", "InvalidLoadBalancerAction", "CreateListener", forward()},
		{"forward with fixed", "InvalidLoadBalancerAction", "CreateListener", forward("DefaultActions.member.1.TargetGroupArn", tg.TargetGroupArn, "DefaultActions.member.1.FixedResponseConfig.StatusCode", "200")},
		{"two groups", "InvalidLoadBalancerAction", "CreateListener", forward("DefaultActions.member.1.ForwardConfig.TargetGroups.member.1.TargetGroupArn", tg.TargetGroupArn, "DefaultActions.member.1.ForwardConfig.TargetGroups.member.2.TargetGroupArn", tg.TargetGroupArn)},
		{"different groups", "InvalidLoadBalancerAction", "CreateListener", forward("DefaultActions.member.1.TargetGroupArn", tg.TargetGroupArn, "DefaultActions.member.1.ForwardConfig.TargetGroups.member.1.TargetGroupArn", strings.Replace(tg.TargetGroupArn, "/valid/", "/other/", 1))},
		{"zero weight", "InvalidLoadBalancerAction", "CreateListener", forward("DefaultActions.member.1.ForwardConfig.TargetGroups.member.1.TargetGroupArn", tg.TargetGroupArn, "DefaultActions.member.1.ForwardConfig.TargetGroups.member.1.Weight", "0")},
		{"stickiness", "InvalidLoadBalancerAction", "CreateListener", forward("DefaultActions.member.1.ForwardConfig.TargetGroups.member.1.TargetGroupArn", tg.TargetGroupArn, "DefaultActions.member.1.ForwardConfig.TargetGroupStickinessConfig.Enabled", "true")},
		{"order range", "ValidationError", "CreateListener", forward("DefaultActions.member.1.TargetGroupArn", tg.TargetGroupArn, "DefaultActions.member.1.Order", "0")},
		{"fixed status", "ValidationError", "CreateListener", listen("DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "302")},
		{"fixed content", "ValidationError", "CreateListener", listen("DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "200", "DefaultActions.member.1.FixedResponseConfig.ContentType", "image/png")},
		{"fixed body", "ValidationError", "CreateListener", listen("DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "200", "DefaultActions.member.1.FixedResponseConfig.MessageBody", strings.Repeat("x", 1025))},
		{"certificates", "ValidationError", "CreateListener", listen("DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "200", "SslPolicy", "ELBSecurityPolicy-2016-08")},
		{"bad lb arn", "ValidationError", "CreateListener", []string{"LoadBalancerArn", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/net/x/0123456789abcdef", "Protocol", "HTTP", "Port", "80", "DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "200"}},
		{"bad tg arn", "ValidationError", "CreateListener", forward("DefaultActions.member.1.TargetGroupArn", "arn:aws:elasticloadbalancing:us-east-1:111111111111:targetgroup/x/0123456789abcdef")},
		{"both filters", "ValidationError", "DescribeLoadBalancers", []string{"Names.member.1", "a", "LoadBalancerArns.member.1", lb.LoadBalancerArn}},
		{"listener filters", "ValidationError", "DescribeListeners", nil},
		{"tg filters", "ValidationError", "DescribeTargetGroups", []string{"Names.member.1", "a", "LoadBalancerArn", lb.LoadBalancerArn}},
		{"bare list value", "ValidationError", "DescribeLoadBalancers", []string{"Names", "a"}},
		{"too many names", "ValidationError", "DescribeLoadBalancers", strings.Split(strings.Repeat("Names.member.1,a,", 1)+"Names.member.2,b,Names.member.3,c,Names.member.4,d,Names.member.5,e,Names.member.6,f,Names.member.7,g,Names.member.8,h,Names.member.9,i,Names.member.10,j,Names.member.11,k,Names.member.12,l,Names.member.13,m,Names.member.14,n,Names.member.15,o,Names.member.16,p,Names.member.17,q,Names.member.18,r,Names.member.19,s,Names.member.20,t,Names.member.21,u", ",")},
		{"include", "ValidationError", "DescribeTargetHealth", []string{"TargetGroupArn", tg.TargetGroupArn, "Include.member.1", "AnomalyDetection"}},
		{"target port", "InvalidTarget", "RegisterTargets", []string{"TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-east-1", "fn"), "Targets.member.1.Port", "80"}},
		{"target zone", "InvalidTarget", "RegisterTargets", []string{"TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-east-1", "fn"), "Targets.member.1.AvailabilityZone", "us-east-1a"}},
		{"two targets", "TooManyTargets", "RegisterTargets", []string{"TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-east-1", "a"), "Targets.member.2.Id", lambdaARN("us-east-1", "b")}},
		{"no targets", "ValidationError", "RegisterTargets", []string{"TargetGroupArn", tg.TargetGroupArn}},
		{"instance id", "InvalidTarget", "RegisterTargets", []string{"TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", "i-0123456789abcdef0"}},
		{"qualified function", "InvalidTarget", "RegisterTargets", []string{"TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-east-1", "fn") + ":live"}},
		{"other region function", "InvalidTarget", "DeregisterTargets", []string{"TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-west-2", "fn")}},
		{"deregister none", "ValidationError", "DeregisterTargets", []string{"TargetGroupArn", tg.TargetGroupArn}},
		{"health target", "InvalidTarget", "DescribeTargetHealth", []string{"TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", "10.0.0.1"}},
		{"modify protocol", "InvalidConfigurationRequest", "ModifyTargetGroup", []string{"TargetGroupArn", tg.TargetGroupArn, "HealthCheckProtocol", "HTTP"}},
		{"modify invalid", "InvalidConfigurationRequest", "ModifyTargetGroup", []string{"TargetGroupArn", tg.TargetGroupArn, "HealthCheckTimeoutSeconds", "40"}},
		{"modify unknown", "ValidationError", "ModifyTargetGroup", []string{"TargetGroupArn", tg.TargetGroupArn, "Port", "80"}},
		{"modify missing", "TargetGroupNotFound", "ModifyTargetGroup", []string{"TargetGroupArn", strings.Replace(tg.TargetGroupArn, "/valid/", "/gone/", 1)}},
		{"register missing", "TargetGroupNotFound", "RegisterTargets", []string{"TargetGroupArn", strings.Replace(tg.TargetGroupArn, "/valid/", "/gone/", 1), "Targets.member.1.Id", lambdaARN("us-east-1", "fn")}},
		{"deregister missing", "TargetGroupNotFound", "DeregisterTargets", []string{"TargetGroupArn", strings.Replace(tg.TargetGroupArn, "/valid/", "/gone/", 1), "Targets.member.1.Id", lambdaARN("us-east-1", "fn")}},
		{"function missing", "InvalidTarget", "RegisterTargets", []string{"TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-east-1", "absent")}},
		{"unsupported operation", "InvalidAction", "CreateRule", nil},
		{"delete lb arn", "ValidationError", "DeleteLoadBalancer", []string{"LoadBalancerArn", "web"}},
		{"delete listener arn", "ValidationError", "DeleteListener", []string{"ListenerArn", lb.LoadBalancerArn}},
		{"delete tg arn", "ValidationError", "DeleteTargetGroup", []string{"TargetGroupArn", lb.LoadBalancerArn}},
		{"delete extra", "ValidationError", "DeleteTargetGroup", []string{"TargetGroupArn", tg.TargetGroupArn, "Force", "true"}},
	} {
		t.Run(c.name, func(t *testing.T) { h.fail(c.code, 400, c.action, c.fields...) })
	}
	if got := elbDecode(t, h.ok("DescribeLoadBalancers")).LoadBalancers; len(got) != 1 {
		t.Fatalf("refused requests changed load balancers: %+v", got)
	}
	if got := elbDecode(t, h.ok("DescribeTargetGroups")).TargetGroups; len(got) != 1 || got[0].HealthCheckTimeoutSeconds != 30 {
		t.Fatalf("refused requests changed target groups: %+v", got)
	}
	if got := elbDecode(t, h.ok("DescribeListeners", "LoadBalancerArn", lb.LoadBalancerArn)).Listeners; len(got) != 0 {
		t.Fatalf("refused requests created listeners: %+v", got)
	}
}

func TestELBQueryTransportBoundaries(t *testing.T) {
	h := newELBHarness(t)
	valid := "Action=DescribeLoadBalancers&Version=2015-12-01"
	for _, c := range []struct {
		name    string
		headers map[string]string
		body    string
		status  int
		code    string
	}{
		{"classic", nil, "Action=DescribeLoadBalancers&Version=2012-06-01", 400, "InvalidAction"},
		{"version", nil, "Action=DescribeLoadBalancers", 400, "ValidationError"},
		{"json target", map[string]string{"X-Amz-Target": "ElasticLoadBalancing.DescribeLoadBalancers"}, valid, 400, "ValidationError"},
		{"content type", map[string]string{"Content-Type": "application/json"}, valid, 400, "ValidationError"},
		{"session", map[string]string{"X-Amz-Security-Token": "token"}, valid, 400, "ValidationError"},
		{"real key", map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/20261111/us-east-1/elasticloadbalancing/aws4_request, SignedHeaders=host, Signature=x"}, valid, 403, "AccessDenied"},
		{"scope service", map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261111/us-east-1/sns/aws4_request, SignedHeaders=host, Signature=x"}, valid, 403, "AccessDenied"},
		{"region disagree", map[string]string{"X-Simulator-Region": "us-west-2", "Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261111/us-east-1/elasticloadbalancing/aws4_request, SignedHeaders=host, Signature=x"}, valid, 400, "ValidationError"},
		{"region", map[string]string{"X-Simulator-Region": "mars-1"}, valid, 400, "ValidationError"},
		{"duplicate field", nil, valid + "&PageSize=1&PageSize=2", 400, "ValidationError"},
		{"malformed", nil, valid + "&%zz", 400, "ValidationError"},
		{"too large", nil, valid + "&Marker=" + strings.Repeat("a", 65<<10), 413, "RequestEntityTooLarge"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := h.raw(c.headers, c.body)
			if w.Code != c.status || elbCode(w.Body.String()) != c.code {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	signed := h.raw(map[string]string{"X-Simulator-Service": "", "Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261111/eu-west-1/elasticloadbalancing/aws4_request, SignedHeaders=host, Signature=x"}, valid)
	if signed.Code != 200 || !strings.Contains(signed.Body.String(), "<DescribeLoadBalancersResponse xmlns=\""+elbNamespace+"\">") {
		t.Fatal(signed.Code, signed.Body.String())
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1/?Action=DescribeLoadBalancers", nil)
	r.Header.Set("X-Simulator-Service", "elasticloadbalancing")
	w := httptest.NewRecorder()
	h.gateway.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader(valid))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Add("X-Simulator-Service", "elasticloadbalancing")
	r.Header.Add("X-Simulator-Service", "elasticloadbalancing")
	w = httptest.NewRecorder()
	h.gateway.ServeHTTP(w, r)
	if w.Code != 400 || elbCode(w.Body.String()) != "ValidationError" {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, g := range []*Gateway{{Identities: h.gateway.Identities}, {ELB: h.gateway.ELB}} {
		w = httptest.NewRecorder()
		r = httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader(valid))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-Simulator-Service", "elasticloadbalancing")
		g.ServeHTTP(w, r)
		if w.Code != 500 && w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	// Query routing does not capture other form services or other selections.
	if isELB(httptest.NewRequest("POST", "http://127.0.0.1/", nil)) {
		t.Fatal("unsigned form POST routed to ELB")
	}
	s3 := httptest.NewRequest("GET", "http://127.0.0.1/_elb/x", nil)
	s3.Header.Set("X-Simulator-Service", "s3")
	if isELB(s3) {
		t.Fatal("explicit service selection was overridden")
	}
}

func TestELBAuthorizationUsesDocumentedActionsAndResources(t *testing.T) {
	h := newELBHarness(t)
	admin := h.loadBalancer("admin-lb")
	tg := h.targetGroup("admin-tg")
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[
{"Effect":"Allow","Action":["elasticloadbalancing:CreateLoadBalancer","elasticloadbalancing:DeleteLoadBalancer"],"Resource":"arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/team-*"},
{"Effect":"Allow","Action":"elasticloadbalancing:DescribeLoadBalancers","Resource":"*"},
{"Effect":"Allow","Action":["elasticloadbalancing:CreateTargetGroup","elasticloadbalancing:ModifyTargetGroup"],"Resource":"*"},
{"Effect":"Deny","Action":"elasticloadbalancing:ModifyTargetGroup","Resource":"arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/admin-*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = h.gateway.Identities.Set("operator-a", policy); err != nil {
		t.Fatal(err)
	}
	h.principal = "operator-a"
	team := h.loadBalancer("team-web")
	h.fail("AccessDenied", 403, "CreateLoadBalancer", "Name", "admin-lb", "Subnets.member.1", "subnet-0123456789abcdef0", "Subnets.member.2", "subnet-89abcdef")
	h.fail("AccessDenied", 403, "CreateLoadBalancer", "Name", "other", "Subnets.member.1", "subnet-0123456789abcdef0", "Subnets.member.2", "subnet-89abcdef")
	h.fail("AccessDenied", 403, "DeleteLoadBalancer", "LoadBalancerArn", admin.LoadBalancerArn)
	h.fail("AccessDenied", 403, "DescribeTargetGroups")
	h.fail("AccessDenied", 403, "DescribeListeners", "LoadBalancerArn", admin.LoadBalancerArn)
	h.fail("AccessDenied", 403, "DescribeTargetHealth", "TargetGroupArn", tg.TargetGroupArn)
	h.fail("AccessDenied", 403, "ModifyTargetGroup", "TargetGroupArn", tg.TargetGroupArn, "HealthCheckEnabled", "true")
	h.fail("AccessDenied", 403, "CreateListener", "LoadBalancerArn", team.LoadBalancerArn, "Protocol", "HTTP", "Port", "80", "DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "200")
	h.fail("AccessDenied", 403, "DeleteTargetGroup", "TargetGroupArn", tg.TargetGroupArn)
	h.fail("AccessDenied", 403, "RegisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-east-1", "fn"))
	h.fail("AccessDenied", 403, "DeregisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-east-1", "fn"))
	h.fail("AccessDenied", 403, "DeleteListener", "ListenerArn", strings.Replace(admin.LoadBalancerArn, "loadbalancer/app/", "listener/app/", 1)+"/0123456789abcdef")
	mine := h.targetGroup("team-tg")
	if modified := elbDecode(t, h.ok("ModifyTargetGroup", "TargetGroupArn", mine.TargetGroupArn, "HealthCheckEnabled", "true", "HealthCheckIntervalSeconds", "10", "HealthCheckTimeoutSeconds", "5", "Matcher.HttpCode", "200-299")).TargetGroups[0]; !modified.HealthCheckEnabled || modified.Matcher.HTTPCode != "200-299" || modified.HealthCheckIntervalSeconds != 10 {
		t.Fatalf("%+v", modified)
	}
	h.ok("DeleteLoadBalancer", "LoadBalancerArn", team.LoadBalancerArn)
	h.principal = "unknown-principal"
	h.fail("AccessDenied", 403, "DescribeLoadBalancers")
	h.principal = ""
	if got := elbDecode(t, h.ok("DescribeLoadBalancers")).LoadBalancers; len(got) != 1 || got[0].LoadBalancerName != "admin-lb" {
		t.Fatalf("denied or deleted requests changed state: %+v", got)
	}
	if got := elbDecode(t, h.ok("DescribeTargetGroups", "Names.member.1", "admin-tg")).TargetGroups; got[0].HealthCheckEnabled {
		t.Fatal("explicit Deny did not prevent modification")
	}
}

func TestELBQuotaBoundsAreExplicit(t *testing.T) {
	h := newELBHarness(t)
	var lb elbLBWire
	for i := 0; i < elbLoadBalancerLimit; i++ {
		lb = h.loadBalancer("lb-" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	h.fail("TooManyLoadBalancers", 400, "CreateLoadBalancer", "Name", "one-more", "Subnets.member.1", "subnet-0123456789abcdef0", "Subnets.member.2", "subnet-89abcdef")
	for i := 0; i < elbTargetGroupLimit; i++ {
		h.targetGroup("tg-" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	h.fail("TooManyTargetGroups", 400, "CreateTargetGroup", "Name", "one-more", "TargetType", "lambda")
	for port := 1; port <= elbListenerLimit; port++ {
		h.ok("CreateListener", "LoadBalancerArn", lb.LoadBalancerArn, "Protocol", "HTTP", "Port", strings.TrimLeft(string(rune('0'+port/10))+string(rune('0'+port%10)), "0"), "DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "200")
	}
	h.fail("TooManyListeners", 400, "CreateListener", "LoadBalancerArn", lb.LoadBalancerArn, "Protocol", "HTTP", "Port", "99", "DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "200")
}

func TestELBInternalFailuresAreNotReportedAsSuccess(t *testing.T) {
	h := newELBHarness(t)
	lb := h.loadBalancer("web")
	tg := h.targetGroup("fn")
	if _, err := h.store.Database().Exec("DROP TABLE aws_elb_targets"); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][]string{
		{"DescribeTargetHealth", "TargetGroupArn", tg.TargetGroupArn},
		{"DeregisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-east-1", "fn")},
		{"ModifyTargetGroup", "TargetGroupArn", tg.TargetGroupArn},
	} {
		h.fail("InternalFailure", 500, c[0], c[1:]...)
	}
	if _, err := h.store.Database().Exec("DROP TABLE aws_elb_listeners"); err != nil {
		t.Fatal(err)
	}
	h.fail("InternalFailure", 500, "DeleteLoadBalancer", "LoadBalancerArn", lb.LoadBalancerArn)
	h.fail("InternalFailure", 500, "DescribeTargetGroups")
	h.fail("InternalFailure", 500, "DeleteTargetGroup", "TargetGroupArn", tg.TargetGroupArn)
	if w := h.call("DescribeLoadBalancers"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	req := httptest.NewRequest("GET", "http://127.0.0.1/_elb/us-east-1/web/80/", nil)
	w := httptest.NewRecorder()
	h.gateway.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestELBStorageFailuresNeverReportSuccess(t *testing.T) {
	h := newELBHarness(t)
	lb := h.loadBalancer("web")
	tg := h.targetGroup("fn")
	listener := elbDecode(t, h.ok("CreateListener", "LoadBalancerArn", lb.LoadBalancerArn, "Protocol", "HTTP", "Port", "80", "DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "200")).Listeners[0]
	fn := lambdaARN("us-east-1", "fn")
	requests := [][]string{
		{"CreateLoadBalancer", "Name", "next", "Subnets.member.1", "subnet-11111111", "Subnets.member.2", "subnet-22222222"},
		{"DescribeLoadBalancers"},
		{"DescribeLoadBalancers", "Names.member.1", "web"},
		{"DeleteLoadBalancer", "LoadBalancerArn", lb.LoadBalancerArn},
		{"CreateTargetGroup", "Name", "next", "TargetType", "lambda"},
		{"DescribeTargetGroups"},
		{"DescribeTargetGroups", "LoadBalancerArn", lb.LoadBalancerArn},
		{"ModifyTargetGroup", "TargetGroupArn", tg.TargetGroupArn},
		{"DeleteTargetGroup", "TargetGroupArn", tg.TargetGroupArn},
		{"RegisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", fn},
		{"DeregisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", fn},
		{"DescribeTargetHealth", "TargetGroupArn", tg.TargetGroupArn},
		{"CreateListener", "LoadBalancerArn", lb.LoadBalancerArn, "Protocol", "HTTP", "Port", "81", "DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "200"},
		{"DescribeListeners", "LoadBalancerArn", lb.LoadBalancerArn},
		{"DeleteListener", "ListenerArn", listener.ListenerArn},
	}
	// Corrupted stored documents are reported as internal failures.
	for _, table := range []string{"aws_elb_load_balancers", "aws_elb_target_groups", "aws_elb_listeners"} {
		if _, err := h.store.Database().Exec("UPDATE " + table + " SET document='{'"); err != nil {
			t.Fatal(err)
		}
	}
	// Every request that reads a corrupted row fails; only new-name creates,
	// which read no stored document, may proceed.
	for _, request := range requests {
		if (request[0] == "CreateLoadBalancer" || request[0] == "CreateTargetGroup") && request[2] == "next" {
			continue
		}
		h.fail("InternalFailure", 500, request[0], request[1:]...)
	}
	h.fail("InternalFailure", 500, "CreateLoadBalancer", "Name", "web", "Subnets.member.1", "subnet-11111111", "Subnets.member.2", "subnet-22222222")
	h.fail("InternalFailure", 500, "CreateTargetGroup", "Name", "fn", "TargetType", "lambda")
	if _, err := (&ELBStore{h.store.Database()}).resolveELBRoute(context.Background(), "us-east-1", "web", 80); err == nil {
		t.Fatal("corrupted route resolved")
	}
	if _, err := h.gateway.ELB.dueChecks(context.Background(), time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Database().Exec("INSERT INTO aws_elb_targets VALUES(?,?,0,'initial','','',0,0,0)", tg.TargetGroupArn, fn); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Database().Exec("UPDATE aws_elb_target_groups SET checks=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Database().Exec("UPDATE aws_elb_listeners SET target_group=?", tg.TargetGroupArn); err != nil {
		t.Fatal(err)
	}
	if _, err := h.gateway.RunELBHealthChecks(context.Background(), time.Now(), 1); err == nil {
		t.Fatal("corrupted health configuration was checked")
	}
	// A closed database fails every operation and the health pass.
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		h.fail("InternalFailure", 500, request[0], request[1:]...)
	}
	if _, err := h.gateway.RunELBHealthChecks(context.Background(), time.Now(), 1); err == nil {
		t.Fatal("closed database reported a successful health pass")
	}
	if _, err := h.gateway.ELB.recordCheck(context.Background(), elbCheck{group: tg.TargetGroupArn}, time.Now(), "", ""); err == nil {
		t.Fatal("closed database recorded a health result")
	}
	if _, err := NewELBStore(h.store.Database()); err == nil {
		t.Fatal("schema creation on a closed database succeeded")
	}
	if w := h.traffic("GET", "/_elb/us-east-1/web/80/", "", nil); w.Code != 500 {
		t.Fatal(w.Code)
	}
	if got := (elbFailure{"Code", "message", 400}).Error(); got != "message" || elbQuote(strings.Repeat("a", 300)) != "<invalid>" {
		t.Fatal(got)
	}
}
