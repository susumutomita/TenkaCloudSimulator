package guesttcp

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func scopeBinding() HTTPProbeScopeBinding {
	return HTTPProbeScopeBinding{Owner: [32]byte{1}, Deployment: [32]byte{2}, Lease: [32]byte{3}, BackendIncarnation: [32]byte{4}}
}
func bindTestScope(t *testing.T, f *probeHTTPFixture) *HTTPProbeOperationScope {
	t.Helper()
	s, err := f.transport.BindHTTPProbeOperations(scopeBinding(), []HTTPProbeService{{Name: "web", Port: 80, Path: "/"}, {Name: "health", Port: 8080, Path: "/healthz"}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestHTTPProbeScopeImmutableConcurrentBinding(t *testing.T) {
	f := newProbeHTTPFixture(t, new(probeHTTPMemoryOwner), context.Background(), persistentConfig(), probeHTTPReply)
	s := bindTestScope(t, f)
	services := s.Services()
	services[0].Name = "changed"
	b, _ := s.Binding()
	b.Owner[0] = 9
	if got, _ := s.Binding(); got != scopeBinding() || s.Services()[0].Name != "health" {
		t.Fatal("mutable binding")
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			alias := &PersistentTransport{nic: f.transport.nic}
			got, err := alias.BindHTTPProbeOperations(scopeBinding(), []HTTPProbeService{{"web", 80, "/"}, {"health", 8080, "/healthz"}})
			if err != nil || got != s {
				t.Error("rewrap lost scope", err)
			}
		}()
	}
	wg.Wait()
	if got, err := f.transport.BindHTTPProbeOperations(b, s.Services()); got != nil || !errors.Is(err, ErrHTTPProbeOperationBinding) {
		t.Fatal("rebound owner", err)
	}
	changed := s.Services()
	changed[0].Path = "/"
	if got, err := f.transport.BindHTTPProbeOperations(scopeBinding(), changed); got != nil || !errors.Is(err, ErrHTTPProbeOperationBinding) {
		t.Fatal("rebound table", err)
	}
}
func TestHTTPProbeScopeStartSnapshotAndOwnership(t *testing.T) {
	f := newProbeHTTPFixture(t, new(probeHTTPMemoryOwner), context.Background(), persistentConfig(), probeHTTPReply)
	s := bindTestScope(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	h, err := s.Start(ctx, operationKey(1), "web")
	if err != nil {
		t.Fatal(err)
	}
	final := operationSettled(t, h)
	snapshot, found, err := s.Snapshot(operationKey(1))
	if err != nil || !found || snapshot.Binding != scopeBinding() || snapshot.Service.Name != "web" || !snapshot.Operation.RouteReady || snapshot.Operation.Route != final.Observation.Route || snapshot.Operation.ResultError != nil {
		t.Fatal("lost scoped snapshot", snapshot, err)
	}
	if got, err := s.Start(nil, operationKey(1), "web"); err != nil || got != h {
		t.Fatal("duplicate renewed", err)
	}
	if _, found := f.transport.LookupHTTPObservation(operationKey(1)); found {
		t.Fatal("raw lookup adopted scoped operation")
	}
	if _, err := f.transport.StartHTTPObservation(ctx, operationKey(1), 80, "/"); !errors.Is(err, ErrHTTPProbeOperationBinding) {
		t.Fatal("raw start adopted scoped operation", err)
	}
	if _, err := s.Start(ctx, operationKey(1), "health"); !errors.Is(err, ErrHTTPProbeOperationBinding) {
		t.Fatal("service rebound", err)
	}
	raw := startOperation(t, f, ctx, operationKey(2), 80, "/")
	operationSettled(t, raw)
	if _, found, err := s.Snapshot(operationKey(2)); err != nil || found {
		t.Fatal("scope adopted raw", err)
	}
	if _, err := s.Start(ctx, operationKey(2), "web"); !errors.Is(err, ErrHTTPProbeOperationBinding) {
		t.Fatal("scope start adopted raw", err)
	}
	if _, found, err := s.Snapshot(operationKey(99)); err != nil || found {
		t.Fatal("unknown treated settled", err)
	}
}
func TestHTTPProbeScopeValidationBeforeRetention(t *testing.T) {
	f := newProbeHTTPFixture(t, new(probeHTTPMemoryOwner), context.Background(), persistentConfig(), probeHTTPReply)
	for _, services := range [][]HTTPProbeService{nil, {{"", 80, "/"}}, {{"web", 81, "/"}}, {{"web", 80, "/bad"}}, {{"web", 80, "/"}, {"web", 8080, "/"}}, {{"bad/name", 80, "/"}}} {
		if s, err := f.transport.BindHTTPProbeOperations(scopeBinding(), services); s != nil || !errors.Is(err, ErrConfig) {
			t.Fatal("invalid table", services, err)
		}
	}
	if s, err := f.transport.BindHTTPProbeOperations(HTTPProbeScopeBinding{}, []HTTPProbeService{{"web", 80, "/"}}); s != nil || !errors.Is(err, ErrConfig) {
		t.Fatal("empty binding", err)
	}
	s := bindTestScope(t, f)
	for _, ctx := range []context.Context{nil, context.Background(), canceledTransportContext(), passiveDeadlineParent{Context: context.Background(), deadline: time.Now().Add(-time.Second)}} {
		if h, err := s.Start(ctx, operationKey(1), "web"); h != nil || err == nil {
			t.Fatal("invalid context retained", err)
		}
	}
	if _, found, _ := s.Snapshot(operationKey(1)); found {
		t.Fatal("invalid call retained")
	}
}
func TestHTTPProbeScopeFailedResultRetainsTuple(t *testing.T) {
	f := newProbeHTTPFixture(t, new(probeHTTPMemoryOwner), context.Background(), persistentConfig(), func(c net.Conn, _ probeHTTPRequest) { _, _ = c.Write([]byte("invalid HTTP\r\n\r\n")) })
	s := bindTestScope(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	h, err := s.Start(ctx, operationKey(1), "web")
	if err != nil {
		t.Fatal(err)
	}
	got := operationSettled(t, h)
	req := <-f.requests
	if got.ResultError == nil || !got.RouteReady || got.Route != req.route || got.Observation.StatusCode != 0 {
		t.Fatal("failed request lost route or fabricated success", got)
	}
}
func TestHTTPProbeScopeSnapshotAfterRevocation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newProbeHTTPFixture(t, new(probeHTTPMemoryOwner), ctx, persistentConfig(), probeHTTPReply)
	s := bindTestScope(t, f)
	call, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	h, err := s.Start(call, operationKey(1), "web")
	if err != nil {
		t.Fatal(err)
	}
	operationSettled(t, h)
	cancel()
	if _, found, err := s.Snapshot(operationKey(1)); err != nil || !found {
		t.Fatal("revocation hid retained state", err)
	}
	if got, err := f.transport.BindHTTPProbeOperations(scopeBinding(), s.Services()); err != nil || got != s {
		t.Fatal("revocation reset binding", err)
	}
}

func TestHTTPProbeScopeTuplePrecedesFirstFrame(t *testing.T) {
	var scope *HTTPProbeOperationScope
	owner := new(probeHTTPMemoryOwner)
	var observed sync.Once
	seen := make(chan struct{})
	owner.frame = func(_ context.Context, d FrameDirection, _ []byte, commit func() error) error {
		if d == ToGuest {
			observed.Do(func() {
				snapshot, found, err := scope.Snapshot(operationKey(1))
				if err != nil || !found || !snapshot.Operation.RouteReady || snapshot.Operation.Route.PeerPort != 80 {
					t.Error("first frame preceded retained tuple", snapshot, err)
				}
				close(seen)
			})
		}
		return commit()
	}
	f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), probeHTTPReply)
	scope = bindTestScope(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	h, err := scope.Start(ctx, operationKey(1), "web")
	if err != nil {
		t.Fatal(err)
	}
	operationSettled(t, h)
	select {
	case <-seen:
	default:
		t.Fatal("no actual frame checked")
	}
}
