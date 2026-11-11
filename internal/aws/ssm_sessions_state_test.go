package aws

import (
	"context"
	"fmt"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestsession"
	"testing"
	"time"
)

type noSessionBackend struct{}

func (noSessionBackend) ResolveSessionTarget(context.Context, string, string) (SSMSessionTarget, error) {
	return SSMSessionTarget{}, fmt.Errorf("unconfigured")
}
func (noSessionBackend) OpenSessionChannel(context.Context, SSMSessionTarget) (*guestsession.Driver, error) {
	return nil, fmt.Errorf("unconfigured")
}
func TestSessionStateTransitionReadbackAndRollback(t *testing.T) {
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSSMSessionStore(db.Database(), noSessionBackend{}, "ws://localhost/_simulator/v1/terminal-channel")
	if err != nil {
		t.Fatal(err)
	}
	request, _, _, _ := sessionAuthFixture(t)
	owner, err := bindSSMSessionCaller(request)
	if err != nil {
		t.Fatal(err)
	}
	e := &ssmLiveSession{id: "ROLE:alice-one", region: "us-east-1", target: SSMSessionTarget{Region: "us-east-1", StackID: "stack-1", InstanceID: "i-own"}, owner: owner, deadline: time.Now().Add(time.Minute)}
	if err = store.reserve(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec(`CREATE TRIGGER mutate_session AFTER UPDATE ON aws_ssm_sessions BEGIN UPDATE aws_ssm_sessions SET instance_id='i-peer' WHERE id=NEW.id; END`); err != nil {
		t.Fatal(err)
	}
	if store.transition(context.Background(), e, "starting", "connected") == nil {
		t.Fatal("mutated receipt accepted")
	}
	var state, instance string
	if err = db.Database().QueryRow(`SELECT state,instance_id FROM aws_ssm_sessions WHERE id=?`, e.id).Scan(&state, &instance); err != nil {
		t.Fatal(err)
	}
	if state != "starting" || instance != "i-own" {
		t.Fatal("failed transition escaped rollback")
	}
	if _, err = db.Database().Exec(`DROP TRIGGER mutate_session`); err != nil {
		t.Fatal(err)
	}
	if err = store.transition(context.Background(), e, "starting", "connected"); err != nil {
		t.Fatal(err)
	}
	if store.transition(context.Background(), e, "starting", "connected") == nil {
		t.Fatal("duplicate transition accepted")
	}
}
