package aws

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestsession"
)

type sessionCloseErrorConn struct{ net.Conn }

func (c sessionCloseErrorConn) Close() error {
	_ = c.Conn.Close()
	return errors.New("injected transport close debt")
}

// The only peer is net.Pipe: no shell, PTY, device, VM, or external socket.
func liveSessionShutdownFixture(t *testing.T, closeError bool) (*SSMSessionStore, *http.Request, *ssmLiveSession, <-chan error) {
	t.Helper()
	r, _, _, _ := sessionAuthFixture(t)
	owner, err := bindSSMSessionCaller(r)
	if err != nil {
		t.Fatal(err)
	}
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSSMSessionStore(db.Database(), noSessionBackend{}, "ws://localhost/_simulator/v1/terminal-channel")
	if err != nil {
		t.Fatal(err)
	}
	// SQLite/WASM initialization is not part of the guest lifecycle deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	r = r.WithContext(copySessionTestContext(r.Context(), ctx))
	host, guest := net.Pipe()
	t.Cleanup(func() { _ = host.Close(); _ = guest.Close() })
	var conn net.Conn = host
	if closeError {
		conn = sessionCloseErrorConn{host}
	}
	done := make(chan error, 1)
	const uuid = "11111111-1111-1111-1111-111111111111"
	go func() {
		codec := guestsession.NewCodec(guest)
		if err := codec.Write(guestsession.Frame{Kind: guestsession.Hello, Payload: []byte(uuid)}); err != nil {
			done <- err
			return
		}
		start, err := codec.Read()
		if err != nil {
			done <- err
			return
		}
		if start.Kind != guestsession.Start {
			done <- fmt.Errorf("missing actual Start")
			return
		}
		if err = codec.Write(guestsession.Frame{Kind: guestsession.Ready, Session: start.Session}); err != nil {
			done <- err
			return
		}
		closeFrame, err := codec.Read()
		if err != nil {
			done <- err
			return
		}
		if closeFrame.Kind != guestsession.Close || closeFrame.Session != start.Session {
			done <- fmt.Errorf("missing matching Close")
			return
		}
		done <- codec.Write(guestsession.Frame{Kind: guestsession.Exit, Session: start.Session, Payload: make([]byte, 4)})
	}()
	lifetime, cancelLifetime := context.WithCancel(ctx)
	t.Cleanup(cancelLifetime)
	driver, err := guestsession.New(lifetime, conn, uuid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	var id guestsession.ID
	id[0] = 42
	if err = driver.Open(ctx, id, 24, 80); err != nil {
		t.Fatal(err)
	}
	deadline, _ := ctx.Deadline()
	e := &ssmLiveSession{id: owner.identity.UserID + "-shutdown", region: "us-east-1", owner: owner, target: SSMSessionTarget{Region: "us-east-1", StackID: "stack-1", InstanceID: "i-own"}, driver: driver, cancelLifetime: cancelLifetime, deadline: deadline, state: "connected"}
	if err = store.reserve(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err = store.transition(ctx, e, "starting", "connected"); err != nil {
		t.Fatal(err)
	}
	store.entries[e.id] = e
	return store, r, e, done
}

// Keep the validated identity values while imposing a real test deadline.
func copySessionTestContext(values, lifetime context.Context) context.Context {
	ctx := context.WithValue(lifetime, stsAuthorityContextKey{}, values.Value(stsAuthorityContextKey{}))
	ctx = context.WithValue(ctx, sdkIdentityContextKey{}, values.Value(sdkIdentityContextKey{}))
	return context.WithValue(ctx, sdkSessionKeyContextKey{}, values.Value(sdkSessionKeyContextKey{}))
}
func TestSessionActiveTerminationAndShutdownReceipts(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		for _, closeError := range []bool{false, true} {
			t.Run(fmt.Sprintf("shutdown=%v/closeError=%v", shutdown, closeError), func(t *testing.T) {
				store, r, e, peer := liveSessionShutdownFixture(t, closeError)
				var err error
				if shutdown {
					err = store.Shutdown(r.Context())
				} else {
					err = store.Terminate(r, e.region, e.id)
				}
				if (err != nil) != closeError {
					t.Fatalf("cleanup result error=%v, expected debt=%v", err, closeError)
				}
				select {
				case peerErr := <-peer:
					if peerErr != nil {
						t.Fatal(peerErr)
					}
				case <-r.Context().Done():
					t.Fatal("peer did not confirm matching Exit write")
				}
				var state string
				if err = store.db.QueryRow(`SELECT state FROM aws_ssm_sessions WHERE id=?`, e.id).Scan(&state); err != nil {
					t.Fatal(err)
				}
				if !closeError {
					if state != "terminated" || e.state != "terminated" {
						t.Fatal("confirmed termination not durable")
					}
				} else if state == "terminated" {
					t.Fatal("close debt silently became participant termination")
				}
				if closeError && store.entries[e.id] != e {
					t.Fatal("uncertain ownership lost")
				}
				if shutdown && !store.closed {
					t.Fatal("shutdown left admission open")
				}
				if e.driver.Active() {
					t.Fatal("closed guest remains active")
				}
			})
		}
	}
}
