package aws

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func elbZIP(t *testing.T, source string) []byte {
	t.Helper()
	var code bytes.Buffer
	archive := zip.NewWriter(&code)
	file, err := archive.Create("app.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte(source)); err != nil {
		t.Fatal(err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	return code.Bytes()
}

const elbApp = `import base64, json, time
def handler(event, context):
    path = event['path']
    if path == '/crash':
        raise RuntimeError('private guest failure')
    if path == '/bad':
        return {'unexpected': 1}
    if path == '/binary':
        return {'statusCode': 200, 'isBase64Encoded': True, 'headers': {'X-App-Kind': 'binary'}, 'body': base64.b64encode(b'\x00\x01').decode()}
    if path == '/health':
        return {'statusCode': HEALTH, 'isBase64Encoded': False, 'headers': {}}
    if path == '/slow':
        time.sleep(3)
    return {'statusCode': 201, 'statusDescription': '201 Created', 'isBase64Encoded': False, 'headers': {'X-App-Result': 'executed', 'Content-Type': 'text/html', 'Set-Cookie': 'a=b'}, 'body': json.dumps(event)}
`

func elbSave(t *testing.T, h *elbHarness, name, health string, update bool) LambdaFunction {
	t.Helper()
	source := strings.Replace(elbApp, "HEALTH", health, 1)
	if health == "slow" {
		source = strings.Replace(elbApp, "if path == '/health':\n        return {'statusCode': HEALTH, 'isBase64Encoded': False, 'headers': {}}", "if path == '/health':\n        time.sleep(3)", 1)
	}
	if health == "crash" {
		source = strings.Replace(elbApp, "return {'statusCode': HEALTH, 'isBase64Encoded': False, 'headers': {}}", "raise RuntimeError('unhealthy')", 1)
	}
	function := LambdaFunction{Name: name, Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Handler: "app.handler", Timeout: 10, Code: elbZIP(t, source)}
	if err := h.gateway.Lambdas.save(&function, !update, ""); err != nil {
		t.Fatal(err)
	}
	return function
}

func elbAt(at time.Time) func() time.Time { return func() time.Time { return at } }

func (h *elbHarness) traffic(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	h.gateway.ServeHTTP(w, r)
	return w
}

func (h *elbHarness) health(arn string, targets ...string) []elbHealthWire {
	h.t.Helper()
	fields := []string{"TargetGroupArn", arn}
	for i, target := range targets {
		fields = append(fields, "Targets.member."+string(rune('1'+i))+".Id", target)
	}
	return elbDecode(h.t, h.ok("DescribeTargetHealth", fields...)).Health
}
func elbState(t *testing.T, got []elbHealthWire, state, reason string) {
	t.Helper()
	if len(got) != 1 || got[0].TargetHealth.State != state || got[0].TargetHealth.Reason != reason {
		t.Fatalf("want %s/%s, got %+v", state, reason, got)
	}
}

func TestELBRoutesToSavedLambdaAndTracksHealthTransitions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	h := newELBHarness(t)
	function := elbSave(t, h, "alb-app", "200", false)
	arn := lambdaARN("us-east-1", function.Name)
	lb := h.loadBalancer("web")
	// The health timeout exceeds the function timeout, so slow sandboxes (for
	// example under the race detector) are bounded by the function, not judged
	// as health timeouts. Timeouts are checked separately with a short limit.
	tg := h.targetGroup("app", "HealthCheckEnabled", "true", "HealthCheckPath", "/health", "HealthCheckIntervalSeconds", "30", "HealthCheckTimeoutSeconds", "25", "HealthyThresholdCount", "2", "UnhealthyThresholdCount", "2")
	h.forward(lb.LoadBalancerArn, tg.TargetGroupArn, "80")
	h.ok("CreateListener", "LoadBalancerArn", lb.LoadBalancerArn, "Protocol", "HTTP", "Port", "81", "DefaultActions.member.1.Type", "fixed-response", "DefaultActions.member.1.FixedResponseConfig.StatusCode", "418", "DefaultActions.member.1.FixedResponseConfig.ContentType", "text/html", "DefaultActions.member.1.FixedResponseConfig.MessageBody", "<b>teapot</b>")

	if w := h.traffic("GET", "/_elb/us-east-1/web/80/hello", "", nil); w.Code != 503 {
		t.Fatal("unregistered target group did not return 503", w.Code)
	}
	h.fail("AccessDenied", 403, "RegisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", arn)
	if _, err := h.gateway.LambdaPermissions.Add(ctx, "us-east-1", function.Name, "other-group", lambdaELBPrincipal, "lambda:InvokeFunction", strings.Replace(tg.TargetGroupArn, "/app/", "/other/", 1), ""); err != nil {
		t.Fatal(err)
	}
	h.fail("AccessDenied", 403, "RegisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", arn)
	if _, err := h.gateway.LambdaPermissions.Add(ctx, "us-east-1", function.Name, "elb", lambdaELBPrincipal, "lambda:InvokeFunction", "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/app/*", ""); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"arn:aws:elasticloadbalancing:us-west-2:000000000000:targetgroup/app/0123456789abcdef", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/0123456789abcdef", "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/"} {
		if _, err := h.gateway.LambdaPermissions.Add(ctx, "us-east-1", function.Name, "bad", lambdaELBPrincipal, "lambda:InvokeFunction", bad, ""); iamCode(err) != "InvalidParameterValueException" {
			t.Fatal("accepted non-target-group ELB source", bad, err)
		}
	}
	h.ok("RegisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", arn, "Targets.member.1.AvailabilityZone", "all")
	h.ok("RegisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", arn)
	elbSave(t, h, "second-app", "200", false)
	h.fail("TooManyTargets", 400, "RegisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-east-1", "second-app"))
	elbState(t, h.health(tg.TargetGroupArn), "initial", "Elb.InitialHealthChecking")
	unregistered := h.health(tg.TargetGroupArn, lambdaARN("us-east-1", "second-app"), arn)
	if len(unregistered) != 2 || unregistered[0].TargetHealth.State != "unused" || unregistered[0].TargetHealth.Reason != "Target.NotRegistered" || unregistered[1].TargetHealth.State != "initial" {
		t.Fatalf("%+v", unregistered)
	}

	// The event carries application data only; simulator authority is excluded.
	w := h.traffic("POST", "/_elb/us-east-1/web/80/hello/world?a=1&a=2&b=%20x&flag", "payload", map[string]string{"Cookie": "console=secret", "X-Simulator-Token": "secret", "Authorization": "AWS4-HMAC-SHA256 Credential=TCSIM/x", "X-Forwarded-For": "198.51.100.7", "X-Custom": "kept"})
	if w.Code != 201 || w.Header().Get("X-App-Result") != "executed" || w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("Set-Cookie") != "" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	var event struct {
		RequestContext struct {
			ELB struct {
				TargetGroupArn string `json:"targetGroupArn"`
			} `json:"elb"`
		} `json:"requestContext"`
		HTTPMethod, Path, Body string
		Query                  map[string]string `json:"queryStringParameters"`
		Headers                map[string]string `json:"headers"`
		IsBase64Encoded        bool              `json:"isBase64Encoded"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &event); err != nil {
		t.Fatal(err, w.Body.String())
	}
	if event.RequestContext.ELB.TargetGroupArn != tg.TargetGroupArn || event.HTTPMethod != "POST" || event.Path != "/hello/world" || event.Body != "payload" || event.IsBase64Encoded {
		t.Fatalf("%+v", event)
	}
	if event.Query["a"] != "2" || event.Query["b"] != "%20x" || event.Query["flag"] != "" || len(event.Query) != 3 {
		t.Fatalf("query was decoded or lost: %+v", event.Query)
	}
	for _, excluded := range []string{"cookie", "x-simulator-token", "authorization"} {
		if _, ok := event.Headers[excluded]; ok {
			t.Fatal("authority header reached guest:", excluded)
		}
	}
	if event.Headers["x-custom"] != "kept" || event.Headers["host"] != "127.0.0.1" || event.Headers["x-forwarded-for"] != "198.51.100.7, 192.0.2.1" || event.Headers["x-forwarded-port"] != "80" || event.Headers["x-forwarded-proto"] != "http" {
		t.Fatalf("%+v", event.Headers)
	}
	binary := h.traffic("PUT", "/_elb/us-east-1/web/80", "\xff\xfe", nil)
	if binary.Code != 201 || !strings.Contains(binary.Body.String(), `"isBase64Encoded": true`) || !strings.Contains(binary.Body.String(), `"path": "/"`) {
		t.Fatal(binary.Code, binary.Body.String())
	}
	if b := h.traffic("GET", "/_elb/us-east-1/web/80/binary", "", nil); b.Code != 200 || !bytes.Equal(b.Body.Bytes(), []byte{0, 1}) || b.Header().Get("X-App-Kind") != "binary" {
		t.Fatal(b.Code, b.Body.Bytes())
	}
	for _, path := range []string{"/crash", "/bad"} {
		if failed := h.traffic("GET", "/_elb/us-east-1/web/80"+path, "", nil); failed.Code != 502 || strings.Contains(failed.Body.String(), "private") {
			t.Fatal(path, failed.Code, failed.Body.String())
		}
	}
	fixed := h.traffic("GET", "/_elb/us-east-1/web/81/anything", "", nil)
	if fixed.Code != 418 || fixed.Body.String() != "<b>teapot</b>" || fixed.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatal(fixed.Code, fixed.Header(), fixed.Body.String())
	}
	if head := h.traffic("HEAD", "/_elb/us-east-1/web/81/", "", nil); head.Code != 418 || head.Body.Len() != 0 {
		t.Fatal(head.Code, head.Body.String())
	}
	for path, status := range map[string]int{"/_elb/us-east-1/missing/80/": 404, "/_elb/us-east-1/web/82/": 404, "/_elb/us-east-1/web/080/": 404, "/_elb/us-east-1/web": 404, "/_elb/mars-1/web/80/": 404, "/_elb/us-east-1/-bad/80/": 404} {
		if got := h.traffic("GET", path, "", nil); got.Code != status {
			t.Fatal(path, got.Code, got.Body.String())
		}
	}
	if got := h.traffic("TRACE", "/_elb/us-east-1/web/80/", "", nil); got.Code != 405 {
		t.Fatal(got.Code)
	}
	if got := h.traffic("GET", "/_elb/us-east-1/web/80/", "", map[string]string{"X-Simulator-Region": "us-west-2"}); got.Code != 404 {
		t.Fatal(got.Code)
	}
	if got := h.traffic("POST", "/_elb/us-east-1/web/80/", strings.Repeat("a", elbEventBytes+1), nil); got.Code != 413 {
		t.Fatal(got.Code)
	}
	if got := h.traffic("POST", "/_elb/us-east-1/web/80/", strings.Repeat("a", elbEventBytes-100), nil); got.Code != 400 {
		t.Fatal("oversized event was not refused", got.Code)
	}

	// Health: thresholds of two consecutive results move between states.
	later := time.Now().Add(time.Hour)
	run := func(at time.Time) {
		t.Helper()
		if n, err := h.gateway.RunELBHealthChecks(ctx, elbAt(at), 4); err != nil || n != 1 {
			t.Fatal(n, err)
		}
	}
	step := func(seconds int) {
		t.Helper()
		later = later.Add(time.Duration(seconds) * time.Second)
		run(later)
	}
	run(later)
	elbState(t, h.health(tg.TargetGroupArn), "initial", "Elb.InitialHealthChecking")
	if n, err := h.gateway.RunELBHealthChecks(ctx, elbAt(later), 4); err != nil || n != 0 {
		t.Fatal("a check ran again before its interval", n, err)
	}
	step(30)
	elbState(t, h.health(tg.TargetGroupArn), "healthy", "")
	// Re-applying identical settings is not a change and keeps the health.
	h.ok("ModifyTargetGroup", "TargetGroupArn", tg.TargetGroupArn)
	h.ok("ModifyTargetGroup", "TargetGroupArn", tg.TargetGroupArn, "HealthCheckIntervalSeconds", "30", "Matcher.HttpCode", "200")
	elbState(t, h.health(tg.TargetGroupArn), "healthy", "")
	elbSave(t, h, "alb-app", "500", true)
	step(30)
	elbState(t, h.health(tg.TargetGroupArn), "healthy", "")
	step(30)
	got := h.health(tg.TargetGroupArn)
	elbState(t, got, "unhealthy", "Target.ResponseCodeMismatch")
	if got[0].TargetHealth.Description != "Health checks failed with these codes: [500]" {
		t.Fatal(got[0].TargetHealth.Description)
	}
	// An unhealthy single target still receives traffic (fail open).
	if open := h.traffic("GET", "/_elb/us-east-1/web/80/open", "", nil); open.Code != 201 {
		t.Fatal(open.Code)
	}
	elbSave(t, h, "alb-app", "crash", true)
	step(30)
	elbState(t, h.health(tg.TargetGroupArn), "unhealthy", "Target.FailedHealthChecks")
	elbSave(t, h, "alb-app", "200", true)
	step(30)
	elbState(t, h.health(tg.TargetGroupArn), "unhealthy", "Target.FailedHealthChecks")
	// A handler slower than a short health timeout is a health timeout.
	h.ok("ModifyTargetGroup", "TargetGroupArn", tg.TargetGroupArn, "HealthCheckIntervalSeconds", "5", "HealthCheckTimeoutSeconds", "2")
	elbState(t, h.health(tg.TargetGroupArn), "initial", "Elb.InitialHealthChecking")
	elbSave(t, h, "alb-app", "slow", true)
	step(5)
	elbState(t, h.health(tg.TargetGroupArn), "initial", "Elb.InitialHealthChecking")
	step(5)
	elbState(t, h.health(tg.TargetGroupArn), "unhealthy", "Target.Timeout")

	// A result computed before a configuration change is discarded.
	checks, err := h.gateway.ELB.dueChecks(ctx, later.Add(time.Hour), 4)
	if err != nil || len(checks) != 1 {
		t.Fatal(checks, err)
	}
	h.ok("ModifyTargetGroup", "TargetGroupArn", tg.TargetGroupArn, "HealthyThresholdCount", "3")
	if saved, err := h.gateway.ELB.recordCheck(ctx, checks[0], later, "", ""); err != nil || saved {
		t.Fatal("stale health result was recorded", saved, err)
	}
	elbState(t, h.health(tg.TargetGroupArn), "initial", "Elb.InitialHealthChecking")

	h.ok("ModifyTargetGroup", "TargetGroupArn", tg.TargetGroupArn, "HealthCheckEnabled", "false")
	elbState(t, h.health(tg.TargetGroupArn), "unavailable", "Target.HealthCheckDisabled")
	if n, err := h.gateway.RunELBHealthChecks(ctx, elbAt(later.Add(time.Hour)), 4); err != nil || n != 0 {
		t.Fatal("disabled health checks invoked the target", n, err)
	}
	if err := h.gateway.LambdaPermissions.Remove(ctx, "us-east-1", function.Name, "elb", ""); err != nil {
		t.Fatal(err)
	}
	if denied := h.traffic("GET", "/_elb/us-east-1/web/80/", "", nil); denied.Code != 502 {
		t.Fatal("removed resource permission still invoked the function", denied.Code)
	}
	h.ok("DeregisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", arn)
	h.ok("DeregisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", arn)
	if empty := h.health(tg.TargetGroupArn); len(empty) != 0 {
		t.Fatalf("%+v", empty)
	}
	if none := h.traffic("GET", "/_elb/us-east-1/web/80/", "", nil); none.Code != 503 {
		t.Fatal(none.Code)
	}
	h.ok("DeleteLoadBalancer", "LoadBalancerArn", lb.LoadBalancerArn)
	if gone := h.traffic("GET", "/_elb/us-east-1/web/80/", "", nil); gone.Code != 404 {
		t.Fatal(gone.Code)
	}
}

func TestELBHealthReportsUnusedTargetsAndSkipsUnusedGroups(t *testing.T) {
	ctx := context.Background()
	h := newELBHarness(t)
	function := elbSave(t, h, "idle", "200", false)
	tg := h.targetGroup("idle", "HealthCheckEnabled", "true", "HealthCheckIntervalSeconds", "5", "HealthCheckTimeoutSeconds", "2")
	if _, err := h.gateway.LambdaPermissions.Add(ctx, "us-east-1", function.Name, "elb", lambdaELBPrincipal, "lambda:InvokeFunction", tg.TargetGroupArn, ""); err != nil {
		t.Fatal(err)
	}
	h.ok("RegisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-east-1", function.Name))
	elbState(t, h.health(tg.TargetGroupArn), "unused", "Target.NotInUse")
	if n, err := h.gateway.RunELBHealthChecks(ctx, elbAt(time.Now().Add(time.Hour)), 4); err != nil || n != 0 {
		t.Fatal("unused target group was health checked", n, err)
	}
	lb := h.loadBalancer("idle")
	listener := h.forward(lb.LoadBalancerArn, tg.TargetGroupArn, "80")
	elbState(t, h.health(tg.TargetGroupArn), "initial", "Elb.InitialHealthChecking")
	h.ok("DeleteListener", "ListenerArn", listener.ListenerArn)
	elbState(t, h.health(tg.TargetGroupArn), "unused", "Target.NotInUse")
	h.ok("DeleteTargetGroup", "TargetGroupArn", tg.TargetGroupArn)
	var rows int
	if err := h.store.Database().QueryRow("SELECT count(*) FROM aws_elb_targets").Scan(&rows); err != nil || rows != 0 {
		t.Fatal("target registration outlived its target group", rows, err)
	}
	var nilGateway Gateway
	if n, err := nilGateway.RunELBHealthChecks(ctx, elbAt(time.Now()), 1); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	loop, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); h.gateway.ELBHealthLoop(loop, time.Millisecond) }()
	time.Sleep(5 * time.Millisecond)
	stop()
	<-done
}

func TestELBResponseAndEventProjectionBoundaries(t *testing.T) {
	for _, payload := range []string{`{"statusCode":}`, `{"statusCode":200,"headers":{"":"x"}}`, `{"StatusCode":200}`, `{"statusCode":200,"statusCode":201}`, `{"statusCode":200,"BODY":"x"}`, `[]`, `{"statusCode":99}`, `{"statusCode":200,"multiValueHeaders":{}}`, `{"body":"x"}`, `{"statusCode":200,"headers":{"Bad Name":"x"}}`, `{"statusCode":200,"headers":{"X":"a\nb"}}`, `{"statusCode":200,"isBase64Encoded":true,"body":"%%"}`, `{"statusCode":200} {}`, "\xff"} {
		if _, _, _, err := elbParseResponse([]byte(payload)); err == nil {
			t.Fatal("malformed response accepted:", payload)
		}
	}
	status, headers, body, err := elbParseResponse([]byte(`{"statusCode":204,"statusDescription":"204 No Content","headers":{"ETag":"\"1\""}}`))
	if err != nil || status != 204 || headers["ETag"] != `"1"` || len(body) != 0 {
		t.Fatal(status, headers, body, err)
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1/?x=%ff", nil)
	r.URL.RawQuery = "x=\xff"
	if _, err := elbBuildEvent(r, "tg", "/", 80, nil); err == nil {
		t.Fatal("invalid query text accepted")
	}
	r = httptest.NewRequest("GET", "http://127.0.0.1/", nil)
	r.Header["Bad"] = []string{"\xff"}
	if _, err := elbBuildEvent(r, "tg", "/", 80, nil); err == nil {
		t.Fatal("invalid header text accepted")
	}
	r = httptest.NewRequest("GET", "http://127.0.0.1/?&a=1", nil)
	r.RemoteAddr = "local"
	data, err := elbBuildEvent(r, "tg", "/", 80, nil)
	if err != nil || !strings.Contains(string(data), `"x-forwarded-for":"local"`) {
		t.Fatal(string(data), err)
	}
	var event elbEvent
	if err = json.Unmarshal(elbHealthEvent("tg", "/ping"), &event); err != nil || event.Headers["user-agent"] != "ELB-HealthChecker/2.0" || event.Path != "/ping" || event.HTTPMethod != "GET" {
		t.Fatalf("%+v %v", event, err)
	}
	if !elbMatches("200,202-204", 203) || elbMatches("200,202-204", 201) || elbMatches("bad", 200) {
		t.Fatal("matcher ranges")
	}
	g := &Gateway{}
	if _, err := g.invokeELBTarget(context.Background(), "us-east-1", "tg", "not-an-arn", []byte(`{}`), 0); err != errELBTargetFailed {
		t.Fatal(err)
	}
	if _, err := g.invokeELBTarget(context.Background(), "us-east-1", "tg", lambdaARN("us-east-1", "x"), []byte(`{}`), 0); err != errELBInternal {
		t.Fatal(err)
	}
	if w := httptest.NewRecorder(); true {
		g.serveELBTraffic(w, httptest.NewRequest("GET", "http://127.0.0.1/_elb/us-east-1/web/80/", io.NopCloser(strings.NewReader(""))))
		if w.Code != 503 {
			t.Fatal(w.Code)
		}
	}
}

// A caller's SDK session policy on the data-plane request must not become the
// guest's authority: SDK calls inside the target use only its execution role.
func TestELBTargetGuestNeverInheritsCallerSessionAuthority(t *testing.T) {
	ctx := context.Background()
	h := newELBHarness(t)
	parameters, err := NewParameterStore(h.store.Database())
	if err != nil {
		t.Fatal(err)
	}
	h.gateway.Parameters = parameters
	if _, err = parameters.Put("us-east-1", "/elb/private", "String", "private-dummy", nil, false); err != nil {
		t.Fatal(err)
	}
	none, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::unrelated/*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = h.gateway.Identities.Set("no-ssm", none); err != nil {
		t.Fatal(err)
	}
	source := "import boto3\ndef handler(event, context):\n    try:\n        value = boto3.client('ssm').get_parameter(Name='/elb/private')['Parameter']['Value']\n    except Exception as exc:\n        value = 'denied:' + type(exc).__name__\n    return {'statusCode': 200, 'body': value}\n"
	function := LambdaFunction{Name: "probe", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/no-ssm", Handler: "app.handler", Timeout: 10, Code: elbZIP(t, source)}
	if err = h.gateway.Lambdas.save(&function, true, ""); err != nil {
		t.Fatal(err)
	}
	lb := h.loadBalancer("probe")
	tg := h.targetGroup("probe")
	h.forward(lb.LoadBalancerArn, tg.TargetGroupArn, "80")
	if _, err = h.gateway.LambdaPermissions.Add(ctx, "us-east-1", "probe", "elb", lambdaELBPrincipal, "lambda:InvokeFunction", tg.TargetGroupArn, ""); err != nil {
		t.Fatal(err)
	}
	h.ok("RegisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-east-1", "probe"))
	everything, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1/_elb/us-east-1/probe/80/", nil)
	r = r.WithContext(context.WithValue(r.Context(), sdkPolicyContextKey{}, everything))
	w := httptest.NewRecorder()
	h.gateway.ServeHTTP(w, r)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "denied:") || strings.Contains(w.Body.String(), "private-dummy") {
		t.Fatal("guest used the caller's session authority:", w.Code, w.Body.String())
	}
	// Simulator-side failures are internal errors, not target health or 502.
	if _, err = h.store.Database().Exec("UPDATE aws_lambda_functions SET document='{'"); err != nil {
		t.Fatal(err)
	}
	if got := h.traffic("GET", "/_elb/us-east-1/probe/80/", "", nil); got.Code != 500 {
		t.Fatal(got.Code, got.Body.String())
	}
	h.ok("ModifyTargetGroup", "TargetGroupArn", tg.TargetGroupArn, "HealthCheckEnabled", "true", "HealthCheckIntervalSeconds", "5", "HealthCheckTimeoutSeconds", "2", "UnhealthyThresholdCount", "2")
	at := time.Now().Add(time.Hour)
	for i := 0; i < 2; i++ {
		if n, err := h.gateway.RunELBHealthChecks(ctx, elbAt(at.Add(time.Duration(i)*5*time.Second)), 4); err != nil || n != 1 {
			t.Fatal(n, err)
		}
	}
	elbState(t, h.health(tg.TargetGroupArn), "unhealthy", "Elb.InternalError")
}
