//go:build linux

package server

import (
	"context"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBattleClockRejectsQueryBeforeDispatch(t *testing.T) {
	r, e := NewBattleCapabilityRegistry("http://127.0.0.1:4566", guesttcp.HTTPProbeCommandClock{Domain: "inert", ValidFromMs: 1, ValidUntilMs: 2})
	if e != nil {
		t.Fatal(e)
	}
	for _, suffix := range []string{"/clock", "/availability", "/resolve", "/seal", "/unknown/api/start", "/unknown/api/read"} {
		req := httptest.NewRequest("POST", battleCapabilitiesPath+suffix+"?modified=1", strings.NewReader(`{"version":1}`))
		req = req.WithContext(provider.WithActor(context.Background(), provider.Actor{CredentialIdentity: "operator", ExerciseIdentity: "exercise", CredentialRole: provider.CredentialOperator}))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("query accepted %s status%d", suffix, w.Code)
		}
	}
	if len(r.entries) != 0 || len(r.providers) != 0 {
		t.Fatal("query mutated registry")
	}
}
