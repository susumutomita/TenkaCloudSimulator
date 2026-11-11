package aws

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"
	"time"
)

// Exercise a real connection: ResponseRecorder does not enforce WriteTimeout.
func TestELBTrafficOutlivesHTTPWriteTimeout(t *testing.T) {
	h := newELBHarness(t)
	fn := elbSave(t, h, "deadline", "200", false)
	lb := h.loadBalancer("deadline")
	tg := h.targetGroup("deadline")
	if _, err := h.gateway.LambdaPermissions.Add(context.Background(), "us-east-1", fn.Name, "elb", lambdaELBPrincipal, "lambda:InvokeFunction", tg.TargetGroupArn, ""); err != nil {
		t.Fatal(err)
	}
	h.ok("RegisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-east-1", fn.Name))
	h.forward(lb.LoadBalancerArn, tg.TargetGroupArn, "80")
	server := httptest.NewUnstartedServer(h.gateway)
	server.Config.WriteTimeout = 50 * time.Millisecond
	server.Start()
	defer server.Close()
	client := server.Client()
	client.Timeout = 3 * time.Minute
	for _, timeout := range []int{10, 1} {
		if timeout == 1 {
			h.ok("DeregisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-east-1", fn.Name))
			fn.Name, fn.Timeout = "deadline-short", 1
			if err := h.gateway.Lambdas.save(&fn, true, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := h.gateway.LambdaPermissions.Add(context.Background(), "us-east-1", fn.Name, "elb", lambdaELBPrincipal, "lambda:InvokeFunction", tg.TargetGroupArn, ""); err != nil {
				t.Fatal(err)
			}
			h.ok("RegisterTargets", "TargetGroupArn", tg.TargetGroupArn, "Targets.member.1.Id", lambdaARN("us-east-1", fn.Name))
		}
		response, err := client.Get(server.URL + "/_elb/us-east-1/deadline/80/slow")
		if err != nil {
			t.Fatalf("timeout %d: response lost at HTTP write deadline: %v", timeout, err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		want := 201
		if timeout == 1 {
			want = 502
		}
		if err != nil || response.StatusCode != want {
			t.Fatalf("timeout %d: want %d, got %d %s (%v)", timeout, want, response.StatusCode, body, err)
		}
	}
}
