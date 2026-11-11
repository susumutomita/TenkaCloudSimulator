//go:build linux

package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBattleRegistryRejectsMetadataAuthority(t *testing.T) {
	r, e := NewBattleCapabilityRegistry("http://127.0.0.1:4566", guesttcp.HTTPProbeCommandClock{Domain: "qualified", ValidFromMs: 1, ValidUntilMs: time.Now().Add(time.Hour).UnixMilli()})
	if e != nil {
		t.Fatal(e)
	}
	owner := BattleDeployment{EventID: "event", TeamID: "team", JobID: "job", UnitHash: strings.Repeat("a", 64), DeployedAt: 1, StackID: "stack", InstanceID: "instance"}
	for _, receipt := range []*aws.EC2VMProbeLaunch{nil, {}} {
		if r.Register(owner, receipt, map[string]string{"frontend": "web", "api": "health"}) == nil {
			t.Fatal("metadata minted ready authority")
		}
	}
	if len(r.entries) != 0 {
		t.Fatal("failed registration retained fake authority")
	}
}
func TestBattleRegistryAuthenticatesBeforeLookup(t *testing.T) {
	r, _ := NewBattleCapabilityRegistry("http://127.0.0.1:4566", guesttcp.HTTPProbeCommandClock{Domain: "qualified", ValidFromMs: 1, ValidUntilMs: time.Now().Add(time.Hour).UnixMilli()})
	for _, suffix := range []string{"/availability", "/resolve", "/seal", "/unknown/api/read"} {
		req := httptest.NewRequest("POST", battleCapabilitiesPath+suffix, strings.NewReader(`{}`))
		req.Header.Set("X-Simulator-Access-Role", "operator")
		req.Header.Set("X-Simulator-Principal", "simulator-admin")
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if out.Code != 403 {
			t.Fatalf("untrusted header accepted: %d", out.Code)
		}
		req = req.WithContext(provider.WithActor(context.Background(), provider.Actor{CredentialIdentity: "participant", ExerciseIdentity: "participant", CredentialRole: provider.CredentialParticipant}))
		out = httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if out.Code != 403 {
			t.Fatal("participant enumerated registry")
		}
	}
	req := httptest.NewRequest("POST", battleCapabilitiesPath+"/availability", strings.NewReader(`{"version":1}`))
	req = req.WithContext(provider.WithActor(req.Context(), provider.Actor{CredentialIdentity: "simulator-admin", ExerciseIdentity: "exercise", CredentialRole: provider.CredentialOperator}))
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"configured":false`) {
		t.Fatal("empty registry advertised readiness")
	}
}

func TestBattleDeploymentKeyGoldenAndStrictBody(t *testing.T) {
	raw, e := os.ReadFile("testdata/battle-deployment-key-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Owner BattleDeployment `json:"owner"`
		Key   string           `json:"key"`
	}
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("bad golden")
	}
	for _, c := range cases {
		if battleKey(c.Owner) != c.Key || !validBattleOwner(c.Owner) {
			t.Fatal("cross-language key mismatch")
		}
	}
	for _, raw := range []string{`{"version":1,"version":2}`, `{"owner":{"teamId":"a","team\u0049d":"b"}}`, `{} {}`, `[`} {
		if uniqueBattleJSON([]byte(raw)) {
			t.Fatalf("ambiguous JSON accepted: %s", raw)
		}
	}
	if !uniqueBattleJSON([]byte(`{"owner":{"teamId":"a"},"version":1}`)) {
		t.Fatal("valid JSON rejected")
	}
}

type battleMemoryLifetime struct {
	sealed bool
	joined bool
}

func (f *battleMemoryLifetime) Ready() bool                { return !f.sealed }
func (f *battleMemoryLifetime) Seal(context.Context) error { f.sealed = true; return nil }
func (f *battleMemoryLifetime) WaitCleanup(context.Context) error {
	if !f.joined {
		return errors.New("cleanup debt")
	}
	return nil
}
func TestBattleHTTPRetainsReadAndCleanupDebt(t *testing.T) {
	r, _ := NewBattleCapabilityRegistry("http://127.0.0.1:4566", guesttcp.HTTPProbeCommandClock{Domain: "qualified", ValidFromMs: 1, ValidUntilMs: time.Now().Add(time.Hour).UnixMilli()})
	owner := BattleDeployment{EventID: "e", TeamID: "t", JobID: "j", UnitHash: strings.Repeat("a", 64), DeployedAt: 1, StackID: "stack", InstanceID: "i"}
	lifetime := &battleMemoryLifetime{}
	handle := strings.Repeat("b", 64)
	calls := 0
	// Private handler fixture only: public registration still requires the actual
	// canonical AWS receipt, separately tested to reject nil/unready values.
	entry := &battleEntry{source: lifetime, receipt: battleReceipt{Version: 1, Handle: handle, Binding: battleBinding{Owner: owner}}, dispatch: map[string]battleDispatch{"frontend": func(raw []byte) (*guesttcp.HTTPProbeWireSnapshot, error) { calls++; return nil, nil }}}
	r.entries[battleKey(owner)] = entry
	r.byHandle[handle] = entry
	request := func(suffix, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", battleCapabilitiesPath+suffix, strings.NewReader(body))
		req = req.WithContext(provider.WithActor(req.Context(), provider.Actor{CredentialIdentity: "operator", ExerciseIdentity: "exercise", CredentialRole: provider.CredentialOperator}))
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "owner": owner, "key": battleKey(owner)})
	out := request("/seal", string(raw))
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"joined":false`) {
		t.Fatal("unknown cleanup became settled")
	}
	binding := guesttcp.HTTPProbeWireBinding{Owner: strings.Repeat("1", 64), Deployment: strings.Repeat("2", 64), Lease: strings.Repeat("3", 64), BackendIncarnation: strings.Repeat("4", 64)}
	command := map[string]any{"version": 1, "command": "read", "binding": binding, "service": "web", "key": strings.Repeat("1", 32)}
	data, _ := json.Marshal(command)
	out = request("/"+handle+"/frontend/read", string(data))
	if out.Code != 200 || strings.TrimSpace(out.Body.String()) != "null" || calls != 1 {
		t.Fatal("sealed read lost retained state")
	}
	command["command"] = "start"
	command["deadline"] = map[string]any{"domain": "qualified", "epochMs": time.Now().Add(time.Second).UnixMilli()}
	data, _ = json.Marshal(command)
	out = request("/"+handle+"/frontend/start", string(data))
	if out.Code != 409 || calls != 1 {
		t.Fatal("sealed admission dispatched")
	}
	lifetime.joined = true
	out = request("/seal", string(raw))
	if !strings.Contains(out.Body.String(), `"joined":true`) {
		t.Fatal("positive join not reported")
	}
}

func TestBattleShutdownDebtRetainsDatabase(t *testing.T) {
	var database *sql.DB
	blocked := true
	wanted := errors.New("unjoined physical debt")
	options := DefaultProviderConfig()
	options.configureInstances = func(db *sql.DB, _ *aws.StackStore) error { database = db; return nil }
	options.closeInstances = func() error {
		if blocked {
			return wanted
		}
		return nil
	}
	_, closeState, e := NewWithProviders(":memory:", options)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { blocked = false; _ = closeState() }()
	if !errors.Is(closeState(), wanted) {
		t.Fatal("shutdown debt suppressed")
	}
	if database == nil || database.Ping() != nil {
		t.Fatal("database closed before join")
	}
	blocked = false
	if e = closeState(); e != nil {
		t.Fatal(e)
	}
	if database.Ping() == nil {
		t.Fatal("database left open after positive cleanup")
	}
}
