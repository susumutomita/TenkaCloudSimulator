package aws

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestsession"
)

// A codec peer on net.Pipe, not a guest or VM. It verifies actual framed
// lifecycle and native store state while keeping physical qualification separate.
type memorySessionBackend struct {
	target     SSMSessionTarget
	resolveErr error
	nilDriver  bool
	opens      int
	peers      []net.Conn
}

func (b *memorySessionBackend) ResolveSessionTarget(context.Context, string, string) (SSMSessionTarget, error) {
	return b.target, b.resolveErr
}
func (b *memorySessionBackend) OpenSessionChannel(ctx context.Context, _ SSMSessionTarget) (*guestsession.Driver, error) {
	b.opens++
	if b.nilDriver {
		return nil, nil
	}
	host, guest := net.Pipe()
	b.peers = append(b.peers, guest)
	const uuid = "11111111-1111-1111-1111-111111111111"
	go func() {
		defer guest.Close()
		codec := guestsession.NewCodec(guest)
		if codec.Write(guestsession.Frame{Kind: guestsession.Hello, Payload: []byte(uuid)}) != nil {
			return
		}
		first, err := codec.Read()
		if err != nil {
			return
		}
		if codec.Write(guestsession.Frame{Kind: guestsession.Ready, Session: first.Session}) != nil {
			return
		}
		for {
			f, err := codec.Read()
			if err != nil {
				return
			}
			switch f.Kind {
			case guestsession.Input:
				err = codec.Write(guestsession.Frame{Kind: guestsession.Output, Session: f.Session, Payload: f.Payload})
			case guestsession.Resize:
				err = codec.Write(guestsession.Frame{Kind: guestsession.Output, Session: f.Session, Payload: []byte("resized")})
			case guestsession.Close:
				_ = codec.Write(guestsession.Frame{Kind: guestsession.Exit, Session: f.Session, Payload: make([]byte, 4)})
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return guestsession.New(ctx, host, uuid)
}
func memorySessionFixture(t *testing.T) (*SSMSessionStore, *http.Request, *memorySessionBackend) {
	t.Helper()
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
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
	s, err := NewSSMSessionStore(db.Database(), b, "ws://localhost/_simulator/v1/terminal-channel")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
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
	return s, r.WithContext(ctx), b
}
func TestSessionMemoryLifecycleRetainsDriverAndRejectsTokens(t *testing.T) {
	s, r, b := memorySessionFixture(t)
	response, err := s.Start(r, "us-east-1", "i-own", "")
	if err != nil {
		t.Fatal(err)
	}
	e := s.entries[response.SessionID]
	driver := e.driver
	if b.opens != 1 || len(response.TokenValue) != 64 || !strings.HasPrefix(response.StreamURL, s.base+"/") {
		t.Fatal("invalid start receipt")
	}
	for _, token := range []string{"", strings.Repeat("0", 64), response.TokenValue + "x"} {
		if _, err = s.OpenDataChannel(r, response.SessionID, token); err == nil {
			t.Fatal("invalid token admitted")
		}
	}
	e.mu.Lock()
	expiry := e.tokenExpires
	e.tokenExpires = time.Now().Add(-time.Second)
	e.mu.Unlock()
	if _, err = s.OpenDataChannel(r, response.SessionID, response.TokenValue); err == nil {
		t.Fatal("expired token admitted")
	}
	e.mu.Lock()
	e.tokenExpires = expiry
	e.mu.Unlock()
	ch, err := s.OpenDataChannel(r, response.SessionID, response.TokenValue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.OpenDataChannel(r, response.SessionID, response.TokenValue); err == nil {
		t.Fatal("token replay")
	}
	if err = ch.Input([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	out, err := ch.Next()
	if err != nil || string(out) != "hello" {
		t.Fatal(string(out), err)
	}
	if err = ch.Resize(25, 90); err != nil {
		t.Fatal(err)
	}
	out, err = ch.Next()
	if err != nil || string(out) != "resized" {
		t.Fatal(string(out), err)
	}
	if err = ch.Resize(0, 90); err == nil {
		t.Fatal("invalid size accepted")
	}
	if err = ch.Input(make([]byte, 8193)); err == nil {
		t.Fatal("oversized input accepted")
	}
	_ = ch.Close()
	if err = ch.Input([]byte("late")); err == nil {
		t.Fatal("closed consumer accepted input")
	}
	next, err := s.Resume(r, "us-east-1", response.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if b.opens != 1 || e.driver != driver || next.TokenValue == response.TokenValue {
		t.Fatal("resume replaced driver or reused token")
	}
	if _, err = s.OpenDataChannel(r, response.SessionID, response.TokenValue); err == nil {
		t.Fatal("previous generation token accepted")
	}
	resumed, err := s.OpenDataChannel(r, response.SessionID, next.TokenValue)
	if err != nil {
		t.Fatal(err)
	}
	_ = resumed.Close()
	if err = s.Terminate(r, "us-east-1", response.SessionID); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = s.db.QueryRow("SELECT state FROM aws_ssm_sessions WHERE id=?", response.SessionID).Scan(&state); err != nil || state != "terminated" || !driver.ExitAcknowledged() {
		t.Fatal("no exact exit and persisted termination", state, err)
	}
	if _, err = s.Resume(r, "us-east-1", response.SessionID); err == nil {
		t.Fatal("terminated resumed")
	}
	if err = s.Terminate(r, "us-east-1", response.SessionID); err == nil {
		t.Fatal("duplicate terminate admitted")
	}
	if _, err = s.OpenDataChannel(r, response.SessionID, next.TokenValue); err == nil {
		t.Fatal("terminated channel opened")
	}
}
func TestSessionMemoryAdmissionFailuresRetainNoFalseSuccess(t *testing.T) {
	for _, kind := range []string{"document", "resolve", "foreign-target", "closed", "busy", "quota", "nil-driver"} {
		t.Run(kind, func(t *testing.T) {
			s, r, b := memorySessionFixture(t)
			document := ""
			switch kind {
			case "document":
				document = "other"
			case "resolve":
				b.resolveErr = errors.New("unavailable")
			case "foreign-target":
				b.target.InstanceID = "i-peer"
			case "closed":
				s.closed = true
			case "busy":
				s.entries["old"] = &ssmLiveSession{target: b.target, state: "failed"}
			case "quota":
				for i := 0; i < 100; i++ {
					s.entries[fmt.Sprint(i)] = &ssmLiveSession{state: "terminated"}
				}
			case "nil-driver":
				b.nilDriver = true
			}
			if _, err := s.Start(r, "us-east-1", "i-own", document); err == nil {
				t.Fatal("failed boundary accepted")
			}
			if kind != "nil-driver" && b.opens != 0 {
				t.Fatal("admission failure opened backend")
			}
			if kind == "nil-driver" {
				var state string
				if err := s.db.QueryRow("SELECT state FROM aws_ssm_sessions").Scan(&state); err != nil || state != "failed" {
					t.Fatal("failed backend not retained", state, err)
				}
			}
		})
	}
}

func TestSessionMemoryConstructorAndOwnershipRefuseUntrustedInputs(t *testing.T) {
	s, r, b := memorySessionFixture(t)
	for _, u := range []string{"http://localhost", "ws://", "ws://user:pass@localhost", "ws://localhost/?token=x", "ws://localhost/#token", "%"} {
		if _, err := NewSSMSessionStore(s.db, b, u); err == nil {
			t.Fatalf("untrusted stream base accepted %q", u)
		}
	}
	if _, err := NewSSMSessionStore(nil, b, s.base); err == nil {
		t.Fatal("nil DB accepted")
	}
	if _, err := NewSSMSessionStore(s.db, nil, s.base); err == nil {
		t.Fatal("nil backend accepted")
	}
	response, err := s.Start(r, "us-east-1", "i-own", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, where := range []struct{ region, id string }{{"us-west-2", response.SessionID}, {"us-east-1", "missing"}} {
		if _, err = s.Resume(r, where.region, where.id); err == nil {
			t.Fatal("foreign session resumed")
		}
	}
	unsigned, _ := http.NewRequest("POST", "http://localhost/", nil)
	if _, err = s.OpenDataChannel(unsigned, response.SessionID, response.TokenValue); err == nil {
		t.Fatal("unsigned data channel")
	}
	if _, err = s.OpenDataChannel(r, "missing", response.TokenValue); err == nil {
		t.Fatal("missing session channel")
	}
	e := s.entries[response.SessionID]
	e.mu.Lock()
	originalDeadline := e.deadline
	e.deadline = time.Now().Add(10 * time.Second)
	receipt, err := s.rotateToken(e)
	expires := e.tokenExpires
	e.mu.Unlock()
	if err != nil || expires != e.deadline {
		t.Fatal("token outlived session", err)
	}
	if receipt.SessionID != response.SessionID {
		t.Fatal("token rotation changed session identity")
	}
	// The temporary unit fixture deadline no longer matches the persisted reservation.
	if err = s.Terminate(r, "us-east-1", response.SessionID); err == nil || err.Error() != "session transition not acknowledged" {
		t.Fatal("changed reservation deadline was not refused", err)
	}
	e.mu.Lock()
	e.deadline = originalDeadline
	e.mu.Unlock()
	if err = s.Terminate(r, "us-east-1", response.SessionID); err != nil {
		t.Fatal(err)
	}
}
