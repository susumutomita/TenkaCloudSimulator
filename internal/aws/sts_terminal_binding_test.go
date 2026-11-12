package aws

import (
	"context"
	"net/http"
	"testing"
)

func TestTerminalBindingSeparateExactTargetAndCredential(t *testing.T) {
	r, a, id, key := sessionAuthFixture(t)
	binding, err := a.TerminalBinding(r, "us-east-1", "i-own")
	if err != nil {
		t.Fatal(err)
	}
	browser, _ := http.NewRequest(http.MethodPost, "http://localhost/_simulator/v1/terminal-control", nil)
	bound, actual, err := binding(browser)
	if err != nil || actual != id {
		t.Fatalf("binding: %v", err)
	}
	if err = checkTerminalBoundary(bound, "us-east-1", "i-own"); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"us-west-2", "i-own"}, {"us-east-1", "i-peer"}} {
		if checkTerminalBoundary(bound, pair[0], pair[1]) == nil {
			t.Fatal("boundary escaped")
		}
	}
	if _, err = a.TerminalBinding(bound, "us-east-1", "i-own"); err == nil {
		t.Fatal("chained handoff allowed")
	}
	parameter := r.WithContext(context.WithValue(r.Context(), consoleParameterBoundaryKey{}, consoleParameterBoundary{}))
	if _, err = a.TerminalBinding(parameter, "us-east-1", "i-own"); err == nil {
		t.Fatal("parameter scope widened")
	}
	a.mu.Lock()
	delete(a.sessions, key)
	a.mu.Unlock()
	if _, _, err = binding(browser); err == nil {
		t.Fatal("revoked original credential accepted")
	}
}
