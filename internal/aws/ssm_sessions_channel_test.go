package aws

import (
	"context"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestsession"
	"net"
	"testing"
	"time"
)

func TestSessionChannelRechecksRevocationAfterBlockedOutput(t *testing.T) {
	r, a, _, key := sessionAuthFixture(t)
	owner, err := bindSSMSessionCaller(r)
	if err != nil {
		t.Fatal(err)
	}
	host, guest := net.Pipe()
	defer guest.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release := make(chan struct{})
	peerDone := make(chan error, 1)
	const uuid = "11111111-1111-1111-1111-111111111111"
	go func() {
		codec := guestsession.NewCodec(guest)
		if err := codec.Write(guestsession.Frame{Kind: guestsession.Hello, Payload: []byte(uuid)}); err != nil {
			peerDone <- err
			return
		}
		start, err := codec.Read()
		if err != nil {
			peerDone <- err
			return
		}
		if err = codec.Write(guestsession.Frame{Kind: guestsession.Ready, Session: start.Session}); err != nil {
			peerDone <- err
			return
		}
		<-release
		peerDone <- codec.Write(guestsession.Frame{Kind: guestsession.Output, Session: start.Session, Payload: []byte("must not leak")})
	}()
	driver, err := guestsession.New(ctx, host, uuid)
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	var id guestsession.ID
	id[0] = 1
	if err = driver.Open(ctx, id, 24, 80); err != nil {
		t.Fatal(err)
	}
	entry := &ssmLiveSession{id: owner.identity.UserID + "-one", region: "us-east-1", owner: owner, driver: driver, state: "connected"}
	channel := &SSMSessionChannel{entry: entry, ctx: ctx}
	authorized := make(chan struct{}, 1)
	a.mu.Lock()
	session := a.sessions[key]
	snapshot := session.snapshot
	session.snapshot = func(ctx context.Context) (core.Policy, error) {
		p, e := snapshot(ctx)
		authorized <- struct{}{}
		return p, e
	}
	a.sessions[key] = session
	a.mu.Unlock()
	type outputResult struct {
		data []byte
		err  error
	}
	result := make(chan outputResult, 1)
	go func() { data, err := channel.Next(); result <- outputResult{data, err} }()
	select {
	case <-authorized:
	case <-ctx.Done():
		t.Fatal("authorization not reached")
	}
	a.mu.Lock()
	delete(a.sessions, key)
	a.mu.Unlock()
	close(release)
	select {
	case got := <-result:
		if got.err == nil || len(got.data) != 0 {
			t.Fatal("revoked output accepted")
		}
	case <-ctx.Done():
		t.Fatal("output did not resolve")
	}
	if err = <-peerDone; err != nil {
		t.Fatal(err)
	}
}
