package aws

import (
	"context"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"testing"
	"time"
)

func TestSessionResumeCannotReopenShutdownAdmission(t *testing.T) {
	r, a, _, key := sessionAuthFixture(t)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:ResumeSession","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	session := a.sessions[key]
	session.snapshot = func(context.Context) (core.Policy, error) { return policy, nil }
	a.sessions[key] = session
	a.mu.Unlock()
	owner, err := bindSSMSessionCaller(r)
	if err != nil {
		t.Fatal(err)
	}
	marked := make(chan struct{})
	e := &ssmLiveSession{id: owner.identity.UserID + "-one", region: "us-east-1", owner: owner, state: "connected", channelCancel: func() { close(marked) }}
	s := &SSMSessionStore{entries: map[string]*ssmLiveSession{e.id: e}}
	e.channelGate.RLock()
	done := make(chan error, 1)
	go func() { _, err := s.Resume(r, e.region, e.id); done <- err }()
	select {
	case <-marked:
	case <-time.After(time.Second):
		e.channelGate.RUnlock()
		t.Fatal("resume did not revoke old consumer")
	}
	e.mu.Lock()
	e.state = "shutdown"
	e.mu.Unlock()
	e.channelGate.RUnlock()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("resume reopened shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("resume did not finish")
	}
	if e.state != "shutdown" {
		t.Fatal("shutdown state overwritten")
	}
}
