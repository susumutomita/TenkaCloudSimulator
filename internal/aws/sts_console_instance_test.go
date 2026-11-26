package aws

import (
	"context"
	"net/http"
	"testing"
)

func TestConsoleInstanceBindingExactTargetAndNoChaining(t *testing.T) {
	r, a, id, key := sessionAuthFixture(t)
	binding, err := a.ConsoleInstanceBinding(r, "us-east-1", "i-own")
	if err != nil {
		t.Fatal(err)
	}
	browser, _ := http.NewRequest(http.MethodPost, "http://localhost/", nil)
	bound, actual, err := binding(browser)
	if err != nil || actual != id {
		t.Fatalf("binding: %v", err)
	}
	if err = checkTerminalBoundary(bound, "us-east-1", "i-own"); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"us-west-2", "i-own"}, {"us-east-1", "i-peer"}} {
		if checkTerminalBoundary(bound, pair[0], pair[1]) == nil {
			t.Fatal("terminal boundary escaped", pair)
		}
	}
	if boundary, ok := bound.Context().Value(consoleInstanceBoundaryKey{}).(consoleInstanceBoundary); !ok || boundary.instance != "i-own" || boundary.region != "us-east-1" {
		t.Fatal("console boundary missing")
	}
	if _, err = a.ConsoleInstanceBinding(bound, "us-east-1", "i-own"); err == nil {
		t.Fatal("console session minted another handoff")
	}
	parameter := r.WithContext(context.WithValue(r.Context(), consoleParameterBoundaryKey{}, consoleParameterBoundary{}))
	if _, err = a.ConsoleInstanceBinding(parameter, "us-east-1", "i-own"); err == nil {
		t.Fatal("parameter scope widened to an instance")
	}
	a.mu.Lock()
	delete(a.sessions, key)
	a.mu.Unlock()
	if _, _, err = binding(browser); err == nil {
		t.Fatal("revoked original credential accepted")
	}
}

func TestConsoleInstanceBoundaryDeniesGenericDecisions(t *testing.T) {
	g := &Gateway{}
	r, _ := http.NewRequest(http.MethodPost, "http://localhost/", nil)
	r = r.WithContext(context.WithValue(r.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{"us-east-1", "i-own"}))
	for _, action := range []string{"ssm:GetParameter", "s3:GetObject", "ec2:DescribeInstances"} {
		if d, err := g.sdkDecision(r, "viewer", action, "*"); err != nil || d != "implicitDeny" {
			t.Fatal("instance console session received a generic decision", action, d, err)
		}
	}
}

func TestOnlyInstanceKeepsTheBoundInstance(t *testing.T) {
	in := []ec2ReservationRead{
		{Owner: "1", Instances: []ec2InstanceRead{{ID: "i-own"}, {ID: "i-peer"}}},
		{Owner: "2", Instances: []ec2InstanceRead{{ID: "i-other"}}},
	}
	out := onlyInstance(in, "i-own")
	if len(out) != 1 || len(out[0].Instances) != 1 || out[0].Instances[0].ID != "i-own" {
		t.Fatalf("filtered %+v", out)
	}
	if got := onlyInstance(in, "i-none"); len(got) != 0 {
		t.Fatal("unknown instance kept resources")
	}
}
