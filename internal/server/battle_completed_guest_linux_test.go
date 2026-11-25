//go:build linux

package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

func TestBattleResolveRejectsUnverifiedCompletedGenerationWithoutBlockingSeal(t *testing.T) {
	r, err := NewBattleCapabilityRegistry("http://127.0.0.1:4566", battleClock())
	if err != nil {
		t.Fatal(err)
	}
	r.driver = &aws.EC2InstanceRuntime{}
	owner := BattleDeployment{EventID: "event", TeamID: "owner", JobID: "job", UnitHash: strings.Repeat("1", 64),
		DeployedAt: 1, StackID: "exact-stack-generation", InstanceID: "local-instance"}
	key := battleKey(owner)
	present := &battleMemoryLifetime{joined: true}
	r.entries[key] = &battleEntry{source: present, receipt: battleReceipt{Binding: battleBinding{Owner: owner}}}
	payload, err := json.Marshal(map[string]any{"version": 1, "owner": owner, "key": key})
	if err != nil {
		t.Fatal(err)
	}
	call := func(suffix string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", battleCapabilitiesPath+suffix, strings.NewReader(string(payload)))
		req = req.WithContext(provider.WithActor(req.Context(), provider.Actor{
			CredentialIdentity: "operator", ExerciseIdentity: "exercise", CredentialRole: provider.CredentialOperator,
		}))
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}
	if out := call("/resolve"); out.Code != 409 || !strings.Contains(out.Body.String(), "not ready") {
		t.Fatal("old receipt without canonical completed generation accepted", out.Code)
	}
	if out := call("/seal"); out.Code != 200 || !present.sealed || !strings.Contains(out.Body.String(), `"joined":true`) {
		t.Fatal("guard blocked cleanup for an incomplete generation", out.Code)
	}
}
