package cognitoidp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func federationTestPool(t *testing.T, path string) (*Store, *core.Store) {
	t.Helper()
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	return s, db
}

func optIn(t *testing.T, s *Store) *federationStorage {
	t.Helper()
	f, err := s.openFederationStorage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// Use a canonical #559 row (including its original five-column insert) with
// an explicit suffix in storage-only fixtures to exercise exact ID reuse.
func seedSameKey(t *testing.T, s *Store, scope Scope, id string) {
	t.Helper()
	_, err := s.db.Exec(`INSERT INTO aws_cognito_user_pools(account, region, pool_id, name, created_ms) VALUES (?,?,?,'fixture',1)`, scope.Account, scope.Region, id)
	if err != nil {
		t.Fatal(err)
	}
}

func TestFederationMetadataCanonicalLifetimeAndWireCompatibility(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "canonical.sqlite")
	s, db := federationTestPool(t, path)
	scope := Scope{"012345678901", "us-east-1"}
	allow := true
	h := NewHTTPHandler(s, scope, Actor{"fixture", "reader"}, func(context.Context, string, string) bool { return allow })
	b := poolRequest(t, h, "CreateUserPool", `{"PoolName":"native"}`, 200, "").Body.Bytes()
	var response struct {
		UserPool struct {
			ID string `json:"Id"`
		}
	}
	if err := json.Unmarshal(b, &response); err != nil || !scope.validPoolID(response.UserPool.ID) {
		t.Fatal("invalid native fixture", err)
	}
	id := response.UserPool.ID
	body := `{"UserPoolId":"` + id + `"}`
	before := poolRequest(t, h, "DescribeUserPool", body, 200, "").Body.String()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopen a pre-migration file through the default #559 entry point.
	s, db = federationTestPool(t, path)
	h = NewHTTPHandler(s, scope, Actor{"fixture", "reader"}, func(context.Context, string, string) bool { return allow })
	if after := poolRequest(t, h, "DescribeUserPool", body, 200, "").Body.String(); after != before {
		t.Fatal("original SDK pool did not survive an unmodified default reopen")
	}
	var exists bool
	if err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM pragma_table_info('aws_cognito_user_pools') WHERE name='federation_state')").Scan(&exists); err != nil || exists {
		t.Fatal("default create/reopen installed federation storage", err)
	}

	f := optIn(t, s)
	initial, err := f.snapshot(ctx, scope, id)
	if err != nil || len(initial.metadata) != 0 {
		t.Fatal("existing #559 pool was not admitted", err)
	}
	committed, err := f.replace(ctx, scope, id, initial.revision, []byte("fixture metadata"))
	if err != nil || committed.revision == initial.revision {
		t.Fatal("missing new revision", err)
	}
	if after := poolRequest(t, h, "DescribeUserPool", body, 200, "").Body.String(); after != before {
		t.Fatal("SDK projection changed after optional storage opened")
	}
	if b := poolRequest(t, h, "ListUserPools", `{"MaxResults":1}`, 200, "").Body.String(); !strings.Contains(b, id) || strings.Contains(b, "fixture metadata") {
		t.Fatal("list projection changed or exposed internal metadata")
	}
	poolRequest(t, h, "CreateIdentityProvider", `{}`, 400, "UnknownOperationException")
	allow = false
	poolRequest(t, h, "DeleteUserPool", body, 403, "AccessDeniedException")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Fresh SQLite handle and store after closing the original process handle.
	s, _ = federationTestPool(t, path)
	f = optIn(t, s)
	restored, err := f.snapshot(ctx, scope, id)
	if err != nil || restored.revision != committed.revision || !bytes.Equal(restored.metadata, committed.metadata) {
		t.Fatal("committed revision did not survive restart or denied SDK delete", err)
	}
	// Retain the Store but retire the physical SQLite connection. No TEMP
	// trigger, per-connection FK setting, or in-memory generation may own the slot.
	if _, err := s.db.Exec("CREATE TEMP TABLE connection_marker (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	s.db.SetMaxIdleConns(0)
	s.db.SetMaxIdleConns(1)
	if err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_temp_master WHERE name='connection_marker')").Scan(&exists); err != nil || exists {
		t.Fatal("did not replace the physical connection", err)
	}
	committed, err = f.replace(ctx, scope, id, restored.revision, []byte("after reconnect"))
	if err != nil {
		t.Fatal("existing storage handle could not replace after reconnect", err)
	}
	h = NewHTTPHandler(s, scope, Actor{"fixture", "reader"}, func(context.Context, string, string) bool { return true })
	poolRequest(t, h, "DeleteUserPool", body, 200, "")
	if _, err := f.snapshot(ctx, scope, id); !errors.Is(err, errMissing) {
		t.Fatal("snapshot fabricated a deleted parent", err)
	}
	if _, err := f.replace(ctx, scope, id, committed.revision, []byte("late")); !errors.Is(err, errMissing) {
		t.Fatal("write recreated deleted parent", err)
	}
	poolRequest(t, h, "DescribeUserPool", body, 400, "ResourceNotFoundException")
	seedSameKey(t, s, scope, id)
	if _, err := f.replace(ctx, scope, id, committed.revision, []byte("late")); !errors.Is(err, errFederationStale) {
		t.Fatal("old revision attached to never-opened replacement", err)
	}
	fresh, err := f.snapshot(ctx, scope, id)
	if err != nil || fresh.revision == committed.revision || len(fresh.metadata) != 0 {
		t.Fatal("recreated ID inherited prior federation generation", err)
	}
	if _, err := f.replace(ctx, scope, id, committed.revision, []byte("late")); !errors.Is(err, errFederationStale) {
		t.Fatal("old revision attached to initialized replacement", err)
	}
}

func TestFederationMetadataIsolationAndFailedWrites(t *testing.T) {
	s, _ := poolFixture(t)
	f := optIn(t, s)
	ctx := t.Context()
	owner := Scope{"012345678901", "us-east-1"}
	peerAccount := Scope{"012345678902", owner.Region}
	peerRegion := Scope{owner.Account, "us-west-2"}
	id := owner.Region + "_fixed"
	scopes := []Scope{owner, peerAccount, peerRegion}
	saved := make([]federationSnapshot, len(scopes))
	for i, sc := range scopes {
		key := sc.Region + "_fixed"
		seedSameKey(t, s, sc, key)
		var err error
		saved[i], err = f.snapshot(ctx, sc, key)
		if err != nil {
			t.Fatal(err)
		}
		saved[i], err = f.replace(ctx, sc, key, saved[i].revision, []byte{byte(i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i, sc := range scopes[1:] {
		if _, err := f.replace(ctx, sc, sc.Region+"_fixed", saved[0].revision, nil); !errors.Is(err, errFederationStale) {
			t.Fatal("foreign revision accepted", i, err)
		}
	}
	siblingID := owner.Region + "_sibling"
	seedSameKey(t, s, owner, siblingID)
	sibling, err := f.snapshot(ctx, owner, siblingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.replace(ctx, owner, siblingID, saved[0].revision, nil); !errors.Is(err, errFederationStale) {
		t.Fatal("revision crossed pool ID", err)
	}
	if _, err := f.snapshot(ctx, peerRegion, id); !errors.Is(err, errInvalid) {
		t.Fatal("mis-scoped region ID accepted", err)
	}
	if _, err := f.snapshot(ctx, Scope{"bad", owner.Region}, id); !errors.Is(err, errInvalid) {
		t.Fatal("invalid account admitted", err)
	}
	if _, err := f.replace(ctx, owner, id, saved[0].revision, make([]byte, maxFederationMetadata+1)); !errors.Is(err, errInvalid) {
		t.Fatal("overlarge metadata admitted", err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER distort_federation_write AFTER UPDATE OF federation_state ON aws_cognito_user_pools
		BEGIN UPDATE aws_cognito_user_pools SET federation_state=OLD.federation_state WHERE account=NEW.account AND region=NEW.region AND pool_id=NEW.pool_id; END`); err != nil {
		t.Fatal(err)
	}
	if got, err := f.replace(ctx, owner, id, saved[0].revision, []byte("different")); !errors.Is(err, errFederationState) || len(got.metadata) != 0 {
		t.Fatal("acknowledged an altered write", err)
	}
	for i, sc := range scopes {
		snap, err := f.snapshot(ctx, sc, sc.Region+"_fixed")
		if err != nil || snap.revision != saved[i].revision || !bytes.Equal(snap.metadata, saved[i].metadata) {
			t.Fatal("failed write altered canonical or peer row", i, err)
		}
	}
	if _, err := s.db.Exec("DROP TRIGGER distort_federation_write"); err != nil {
		t.Fatal(err)
	}
	max := bytes.Repeat([]byte{1}, maxFederationMetadata)
	full, err := f.replace(ctx, owner, id, saved[0].revision, max)
	if err != nil || !bytes.Equal(full.metadata, max) {
		t.Fatal("maximum accepted size was not durable", err)
	}
	if err := s.delete(ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	if got, err := f.snapshot(ctx, owner, siblingID); err != nil || got.revision != sibling.revision {
		t.Fatal("delete crossed pool ID", err)
	}
	for i, sc := range scopes[1:] {
		snap, err := f.snapshot(ctx, sc, sc.Region+"_fixed")
		if err != nil || snap.revision != saved[i+1].revision {
			t.Fatal("delete crossed pool scope", i, err)
		}
	}
}

func TestFederationMetadataConcurrentOpenReplaceAndDelete(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "shared.sqlite")
	a, _ := federationTestPool(t, path)
	b, _ := federationTestPool(t, path)
	scope := Scope{"012345678901", "us-east-1"}
	p, err := a.create(ctx, scope, "shared")
	if err != nil {
		t.Fatal(err)
	}
	type opened struct {
		s    *federationStorage
		snap federationSnapshot
		err  error
	}
	ready := make(chan struct{})
	readers := make(chan opened, 2)
	for _, store := range []*Store{a, b} {
		go func() {
			<-ready
			f, err := store.openFederationStorage(ctx)
			var snap federationSnapshot
			if err == nil {
				snap, err = f.snapshot(ctx, scope, p.ID)
			}
			readers <- opened{f, snap, err}
		}()
	}
	close(ready)
	left, right := <-readers, <-readers
	if left.err != nil || right.err != nil || left.snap.revision != right.snap.revision {
		t.Fatal("two database handles did not see the same initial state", left.err, right.err)
	}
	type outcome struct {
		snap federationSnapshot
		err  error
	}
	writers := make(chan outcome, 2)
	goNow := make(chan struct{})
	for i, handle := range []opened{left, right} {
		go func() {
			<-goNow
			snap, err := handle.s.replace(ctx, scope, p.ID, handle.snap.revision, []byte{byte(i)})
			writers <- outcome{snap, err}
		}()
	}
	close(goNow)
	one, two := <-writers, <-writers
	if one.err != nil {
		one, two = two, one
	}
	if one.err != nil || !errors.Is(two.err, errFederationStale) {
		t.Fatal("concurrent replacements did not have exactly one winner", one.err, two.err)
	}
	current, err := left.s.snapshot(ctx, scope, p.ID)
	if err != nil || current.revision != one.snap.revision || !bytes.Equal(one.snap.metadata, current.metadata) {
		t.Fatal("winning revision not observed", err)
	}

	write := make(chan error, 1)
	del := make(chan error, 1)
	race := make(chan struct{})
	go func() {
		<-race
		_, err := left.s.replace(ctx, scope, p.ID, current.revision, []byte("racing"))
		write <- err
	}()
	// Delete with the independent canonical #559 handle.
	go func() { <-race; del <- right.s.pools.delete(ctx, scope, p.ID) }()
	close(race)
	writeErr, deleteErr := <-write, <-del
	if deleteErr != nil || (writeErr != nil && !errors.Is(writeErr, errMissing)) {
		t.Fatal("non-serializable delete/write", writeErr, deleteErr)
	}
	if _, err := right.s.snapshot(ctx, scope, p.ID); !errors.Is(err, errMissing) {
		t.Fatal("concurrent write resurrected deleted pool", err)
	}
}

func TestFederationMetadataFailureReturnsNoHandle(t *testing.T) {
	s, _ := poolFixture(t)
	ctx := t.Context()
	scope := Scope{"012345678901", "us-east-1"}
	p, err := s.create(ctx, scope, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an incompatible old local extension, not an operational database.
	if _, err := s.db.Exec("ALTER TABLE aws_cognito_user_pools ADD COLUMN federation_state"); err != nil {
		t.Fatal(err)
	}
	f := optIn(t, s)
	for _, value := range []any{[]byte{}, []byte{1}, strings.Repeat("x", federationRevisionBytes), make([]byte, maxFederationMetadata+federationRevisionBytes+1)} {
		if _, err := s.db.Exec("UPDATE aws_cognito_user_pools SET federation_state=?", value); err != nil {
			t.Fatal(err)
		}
		got, err := f.snapshot(ctx, scope, p.ID)
		if !errors.Is(err, errFederationState) || got.revision != (federationRevision{}) || got.metadata != nil {
			t.Fatal("malformed slot acknowledged", err)
		}
	}
	if _, err := s.db.Exec("UPDATE aws_cognito_user_pools SET federation_state=NULL"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER ignore_federation_write BEFORE UPDATE OF federation_state ON aws_cognito_user_pools BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if got, err := f.snapshot(ctx, scope, p.ID); !errors.Is(err, errFederationState) || got.revision != (federationRevision{}) {
		t.Fatal("ignored initialization returned a generation", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.openFederationStorage(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled migration admitted", err)
	}
	if _, err := f.snapshot(canceled, scope, p.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled snapshot admitted", err)
	}
}
