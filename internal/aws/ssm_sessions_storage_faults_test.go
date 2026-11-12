package aws

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Every persisted session transition (reserve, connect, resume, terminate)
// reports a storage failure instead of handing out a session or a
// termination receipt the state file does not back.
func TestSessionLifecycleReportsStorageFaults(t *testing.T) {
	sweepStorageFaults(t, false, func(t *testing.T, db *sql.DB) func(context.Context) error {
		r, a, _, key := sessionAuthFixture(t)
		p, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ssm:StartSession","ssm:ResumeSession","ssm:TerminateSession","ssmmessages:OpenDataChannel"],"Resource":"*"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		a.mu.Lock()
		v := a.sessions[key]
		v.snapshot = func(context.Context) (core.Policy, error) { return p, nil }
		a.sessions[key] = v
		a.mu.Unlock()
		b := &memorySessionBackend{target: SSMSessionTarget{Region: "us-east-1", StackID: "stack-one", InstanceID: "i-own"}}
		s, err := NewSSMSessionStore(db, b, "ws://localhost/_simulator/v1/terminal-channel")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, e := range s.entries {
				if e.driver != nil {
					_ = e.driver.Close()
				}
				if e.cancelLifetime != nil {
					e.cancelLifetime()
				}
			}
			for _, peer := range b.peers {
				_ = peer.Close()
			}
		})
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		t.Cleanup(cancel)
		r = r.WithContext(ctx)
		return func(context.Context) error {
			response, err := s.Start(r, "us-east-1", "i-own", "")
			if err != nil {
				s.mu.Lock()
				defer s.mu.Unlock()
				for id, e := range s.entries {
					e.mu.Lock()
					state := e.state
					e.mu.Unlock()
					if state == "connected" {
						t.Fatalf("failed start left connected session %s", id)
					}
				}
				return err
			}
			if response.SessionID == "" || len(response.TokenValue) != 64 {
				t.Fatalf("incomplete start receipt %+v", response)
			}
			if _, err = s.Resume(r, "us-east-1", response.SessionID); err != nil {
				return err
			}
			if err = s.Terminate(r, "us-east-1", response.SessionID); err != nil {
				return err
			}
			// Terminate only reports success after the terminated transition
			// committed, so the live entry is only marked terminated after it.
			s.mu.Lock()
			e := s.entries[response.SessionID]
			s.mu.Unlock()
			if e == nil || e.state != "terminated" {
				t.Fatal("terminate receipt without terminated session")
			}
			return nil
		}
	})
}
