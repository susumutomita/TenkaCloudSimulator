//go:build linux

package guestprocess

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryNICEndpoint struct {
	once   sync.Once
	done   chan struct{}
	closes atomic.Int32
	err    error
}

func (p *memoryNICEndpoint) Read([]byte) (int, error)    { <-p.done; return 0, io.EOF }
func (p *memoryNICEndpoint) Write(b []byte) (int, error) { return len(b), nil }
func (p *memoryNICEndpoint) Close() error {
	p.once.Do(func() { p.closes.Add(1); close(p.done) })
	return p.err
}
func TestCanonicalNICAcquisitionMemoryOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	parent := &memoryNICEndpoint{done: make(chan struct{})}
	child := new(os.File) // Identity-only fake; no method or syscall touches this file.
	var owned *LaunchAttempt
	launch := func(ctx context.Context, s Spec, f *os.File) (*LaunchAttempt, error) {
		if f != child || s.Executable != "/inert/qemu" {
			t.Fatal("wrong inheritance")
		}
		lifetime, stop := context.WithCancelCause(ctx)
		owned = &LaunchAttempt{ctx: lifetime, cancel: stop, ready: make(chan struct{}), done: make(chan struct{})}
		close(owned.ready)
		return owned, nil
	}
	n, err := beginNICLaunch(ctx, Spec{Executable: "/inert/qemu", Env: []string{}}, func() (nicPair, error) { return nicPair{parent: parent, child: child}, nil }, launch)
	if err != nil {
		t.Fatal(err)
	}
	if err = n.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-owned.ctx.Done():
	default:
		t.Fatal("close did not cancel actualattempt")
	}
	// Cleanup must not become complete just because stream Close returned.
	short, stop := context.WithCancel(context.Background())
	stop()
	if err = n.WaitCleanup(short); err == nil {
		t.Fatal("cancelled observer claimed cleanup")
	}
	owned.exit = Exit{Reaped: true}
	close(owned.done)
	if err = n.WaitCleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if parent.closes.Load() != 1 {
		t.Fatal("double close")
	}
}
func TestCanonicalNICPartialAcquisitionRetainsMemoryDebt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	parent := &memoryNICEndpoint{done: make(chan struct{}), err: errors.New("unknown close")}
	called := false
	n, err := beginNICLaunch(ctx, Spec{Executable: "/inert/qemu", Env: []string{}}, func() (nicPair, error) { return nicPair{parent: parent}, nil }, func(context.Context, Spec, *os.File) (*LaunchAttempt, error) { called = true; return nil, nil })
	if n == nil || err == nil || called {
		t.Fatal("partial ownership lost", n, err, called)
	}
	nicOwners.Lock()
	_, retained := nicOwners.retained[n]
	nicOwners.Unlock()
	if !retained || n.stream != parent {
		t.Fatal("no reachable endpoint debt")
	}
	if err = n.WaitCleanup(ctx); err == nil {
		t.Fatal("unknown close settled")
	}
	// Conservative retained slot intentionally remains until this test process exits.
}
