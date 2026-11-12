package aws

import (
	"context"
	"testing"
)

func TestSessionShutdownEmptyIsIdempotent(t *testing.T) {
	s := &SSMSessionStore{entries: map[string]*ssmLiveSession{}}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.closed {
		t.Fatal("admission still open")
	}
}
func TestSessionShutdownRetainsUnknownGuestDebt(t *testing.T) {
	canceled := false
	e := &ssmLiveSession{id: "s1", state: "starting", cancelLifetime: func() { canceled = true }}
	s := &SSMSessionStore{entries: map[string]*ssmLiveSession{"s1": e}}
	if err := s.Shutdown(context.Background()); err == nil {
		t.Fatal("missing guest receipt became successful shutdown")
	}
	if !s.closed || !canceled || !e.tokenUsed || e.state != "shutdown" || s.entries["s1"] != e {
		t.Fatal("unknown debt/admission guard lost")
	}
}
