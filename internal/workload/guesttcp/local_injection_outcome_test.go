package guesttcp

import (
	"context"
	"errors"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"testing"
	"time"
)

// Real Engine injection completes before the controlled child-close race.
func TestOwnedSuccessfulInboundChildCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	local, remote := memoryStreams()
	r := &issuedPersistentMemory{&issuedMemoryResource{stream: local}, persistentOwnedDescriptor()}
	owner, err := NewOwnedPersistentProbeSession(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	service := persistentFixtureService(t)
	h, err := owner.AttachPersistent(ctx, service, time.Now().Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = remote.Close()
		wait, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		if e := h.CloseContext(wait); e != nil {
			t.Error(e)
		}
	}()
	p, err := h.transport.OpenProbe(ctx, 80)
	if err != nil {
		t.Fatal(err)
	}
	dialDone := make(chan struct{})
	go func() {
		defer close(dialDone)
		conn, _ := p.Dial(ctx)
		if conn != nil {
			_ = conn.Close()
		}
	}()
	decoder := guestnet.NewDecoder(remote)
	frame, err := decoder.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = guestnet.ParseTCPFrame(frame); err != nil {
		t.Fatal(err)
	}
	// Reading the real SYN establishes Engine.Dial admission without mutating internals.
	if owner.ctx.Err() != nil || p.ctx.Err() != nil || owner.terminal.Load() {
		t.Fatal("preexisting cancellation")
	}
	rawSucceeded := false
	err = h.transport.nic.base.commitLocalInjection(p.ctx, reverseTCPFrame(config()), func() error {
		raw := p.engine.Inject(reverseTCPFrame(config()))
		if raw != nil {
			return raw
		}
		rawSucceeded = true

		p.beginClose()
		return nil
	})
	if !rawSucceeded {
		t.Fatalf("no successful injection witness: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("missing canceled packet outcome: %v", err)
	}
	if owner.terminal.Load() {
		t.Fatal("local child completion revoked shared owner")
	}
	owner.Revoke()
	<-dialDone
}

func TestLocalInjectionProviderAndOwnerFailuresStayTerminal(t *testing.T) {
	for _, mode := range []string{"propagate", "wrap", "swallow", "independent", "twice", "owner revoke", "apply error", "generic", "prior callback"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			local, remote := memoryStreams()
			defer remote.Close()
			var old error
			resource := &issuedPersistentMemory{&issuedMemoryResource{stream: local}, persistentOwnedDescriptor()}
			resource.transaction = func(_ context.Context, _ OwnedProbeAcceptance, f func() error) error {
				e := f()
				switch mode {
				case "wrap":
					return errors.Join(e, errors.New("provider error"))
				case "swallow":
					return nil
				case "independent":
					return errors.New("provider failure")
				case "twice":
					_ = f()
				case "prior callback":
					if old != nil {
						return old
					}
					old = e
				}
				return e
			}
			owner, e := NewOwnedPersistentProbeSession(ctx, resource)
			if e != nil {
				t.Fatal(e)
			}
			h, e := owner.AttachPersistent(ctx, persistentFixtureService(t), time.Now().Add(time.Second))
			if e != nil {
				t.Fatal(e)
			}
			defer func() {
				wait, done := context.WithTimeout(context.Background(), time.Second)
				defer done()
				if e := h.CloseContext(wait); e != nil {
					t.Error(e)
				}
			}()
			call := func() error {
				child, stop := context.WithCancel(ctx)
				defer stop()
				apply := func() error {
					stop()
					if mode == "owner revoke" {
						owner.Revoke()
					}
					if mode == "apply error" {
						return errors.New("injection failed")
					}
					return nil
				}
				if mode == "generic" {
					return h.transport.nic.base.commitContext(child, FromGuest, reverseTCPFrame(config()), apply)
				}
				return h.transport.nic.base.commitLocalInjection(child, reverseTCPFrame(config()), apply)
			}
			if e = call(); e == nil {
				t.Fatal("canceled callback succeeded")
			}
			if mode == "prior callback" {
				if owner.terminal.Load() {
					t.Fatal("first exact callback poisoned")
				}
				if e = call(); e == nil {
					t.Fatal("reused prior callback succeeded")
				}
			}
			wantTerminal := mode != "propagate"
			if owner.terminal.Load() != wantTerminal {
				t.Fatalf("terminal=%v want %v", owner.terminal.Load(), wantTerminal)
			}
			evidence, e := owner.Evidence(context.Background())
			if e != nil || len(evidence) != 0 {
				t.Fatal("callback fabricated observation", evidence, e)
			}
		})
	}
}

func TestLocalInjectionCancellationWindows(t *testing.T) {
	for _, window := range []string{"inside apply", "after transport postcheck", "after owner callback"} {
		t.Run(window, func(t *testing.T) {
			child, cancel := context.WithCancel(context.Background())
			defer cancel()
			resource := &issuedMemoryResource{stream: &ownedMemoryStream{closed: make(chan struct{})}}
			resource.transaction = func(_ context.Context, _ OwnedProbeAcceptance, f func() error) error {
				e := f()
				if window == "after owner callback" {
					if e != nil {
						t.Error("callback must have returned nil before cancellation")
					}
					cancel()
				}
				return e
			}
			owner := ownedFixture(t, resource)
			committer := commitFunc(func(c context.Context, d FrameDirection, b []byte, apply func() error) error {
				return owner.CommitFrame(c, d, b, func() error {
					e := apply()
					if window == "after transport postcheck" {
						if e != nil {
							t.Error("transport must have returned nil before cancellation")
						}
						cancel()
					}
					return e
				})
			})
			transport := &Transport{ctx: owner.ctx, owner: committer}
			called := false
			e := transport.commitLocalInjection(child, reverseTCPFrame(config()), func() error {
				called = true
				if window == "inside apply" {
					cancel()
				}
				return nil
			})
			if !called || !errors.Is(e, context.Canceled) || owner.terminal.Load() {
				t.Fatalf("called=%v result=%v terminal=%v", called, e, owner.terminal.Load())
			}
		})
	}
}

func TestLocalInjectionSuccessfulContextCannotBeReplayed(t *testing.T) {
	var captured context.Context
	resource := &issuedMemoryResource{stream: &ownedMemoryStream{closed: make(chan struct{})}}
	resource.transaction = func(c context.Context, _ OwnedProbeAcceptance, f func() error) error { captured = c; return f() }
	owner := ownedFixture(t, resource)
	transport := &Transport{ctx: owner.ctx, owner: owner}
	if e := transport.commitLocalInjection(context.Background(), reverseTCPFrame(config()), func() error { return nil }); e != nil {
		t.Fatal(e)
	}
	called := false
	if e := owner.CommitFrame(captured, FromGuest, reverseTCPFrame(config()), func() error { called = true; return nil }); e == nil || called || !owner.terminal.Load() {
		t.Fatalf("replayed proof: %v called=%v", e, called)
	}
}

func TestLocalInjectionSharedTransportFailureStillSeals(t *testing.T) {
	owner := ownedFixture(t, &issuedMemoryResource{stream: &ownedMemoryStream{closed: make(chan struct{})}})
	shared, sealTransport := context.WithCancel(owner.ctx)
	defer sealTransport()
	child, cancel := context.WithCancel(owner.ctx)
	defer cancel()
	transport := &Transport{ctx: shared, owner: owner}
	e := transport.commitLocalInjection(child, reverseTCPFrame(config()), func() error { sealTransport(); cancel(); return nil })
	if e == nil || !owner.terminal.Load() {
		t.Fatalf("shared transport failure spared owner: %v", e)
	}
}

func TestLocalInjectionWrongKindAndLateOwnerRevokeSeal(t *testing.T) {
	for _, mode := range []string{"outbound mode", "observation kind", "owner revoked after callback"} {
		t.Run(mode, func(t *testing.T) {
			child, cancel := context.WithCancel(context.Background())
			defer cancel()
			resource := &issuedMemoryResource{stream: &ownedMemoryStream{closed: make(chan struct{})}}
			owner := ownedFixture(t, resource)
			resource.transaction = func(_ context.Context, _ OwnedProbeAcceptance, f func() error) error {
				e := f()
				if mode == "owner revoked after callback" {
					owner.Revoke()
					cancel()
				}
				return e
			}
			var e error
			if mode == "observation kind" {
				token := &localInjectionToken{}
				c := context.WithValue(child, localInjectionKey{}, token)
				e = owner.transaction(c, OwnedProbeAcceptance{Direction: FromGuest, Observation: &ProbeHTTPObservation{}}, func() error { token.completed.Store(true); token.eligible.Store(true); cancel(); return nil })
			} else {
				var committer FrameCommitter = owner
				if mode == "outbound mode" {
					// Direct transaction bypasses frame-shape rejection to exercise kind guard.
					committer = commitFunc(func(c context.Context, d FrameDirection, _ []byte, f func() error) error {
						return owner.transaction(c, OwnedProbeAcceptance{Direction: d}, f)
					})
				}
				transport := &Transport{ctx: owner.ctx, owner: committer}
				direction := FromGuest
				if mode == "outbound mode" {
					direction = ToGuest
				}
				e = transport.commitContextMode(child, direction, reverseTCPFrame(config()), func() error {
					if mode == "outbound mode" {
						cancel()
					}
					return nil
				}, true)
			}
			if e == nil || !owner.terminal.Load() {
				t.Fatalf("invalid exemption: %v terminal=%v", e, owner.terminal.Load())
			}
		})
	}
}
