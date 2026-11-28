package aws

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestSTSAssumeRoleCancellationBeforePublication(t *testing.T) {
	db, _ := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	role, err := roles.Create(t.Context(), "context-role", stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	if err := roles.PutPolicy(t.Context(), role.RoleName, "read", nativeReadPolicy); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Roles: roles, Identities: core.NewIdentities(db)}
	authority := NewSTSAuthority("local-test-only")
	caller := SDKIdentity{Principal: core.Administrator, Account: "000000000000", ARN: "arn:aws:iam::000000000000:root"}
	call := func(ctx context.Context, name string) *httptest.ResponseRecorder {
		ctx = context.WithValue(ctx, stsAuthorityContextKey{}, authority)
		ctx = context.WithValue(ctx, sdkIdentityContextKey{}, caller)
		form := url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"},
			"RoleArn": {role.Arn}, "RoleSessionName": {name}, "ExternalId": {"team-external"}}
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(form.Encode()))
		w := httptest.NewRecorder()
		gateway.serveSTS(w, r)
		return w
	}
	if w := call(t.Context(), "peer"); w.Code != http.StatusOK || len(authority.sessions) != 1 {
		t.Fatal("healthy peer issuance failed", w.Code)
	}
	var peer STSCredentials
	for _, session := range authority.sessions {
		peer = session.credentials
	}
	denied := func(ctx context.Context) {
		t.Helper()
		w := call(ctx, "interrupted")
		if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "<Credentials>") ||
			len(authority.sessions) != 1 || authority.sessions[peer.AccessKeyID].credentials != peer {
			t.Fatal("interrupted issuance changed session state or response", w.Code)
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	denied(canceled)
	expired, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stop()
	denied(expired)

	// The authority samples its clock under the lock, after the final role check.
	atPublication, interrupt := context.WithCancel(t.Context())
	defer interrupt()
	authority.now = func() time.Time {
		interrupt()
		return time.Now()
	}
	denied(atPublication)
	if !errors.Is(atPublication.Err(), context.Canceled) {
		t.Fatal("request did not reach the publication decision")
	}
	authority.now = time.Now
	if w := call(t.Context(), "subsequent"); w.Code != http.StatusOK || len(authority.sessions) != 2 {
		t.Fatal("subsequent issuance failed", w.Code)
	}
	current, err := roles.Get(t.Context(), role.RoleName)
	if err != nil || current.RoleID != role.RoleID || current.AssumeRolePolicyDocument != role.AssumeRolePolicyDocument {
		t.Fatal("committed role or trust changed", err)
	}
	policy, err := roles.Policy(t.Context(), role.RoleName, "read")
	if err != nil || policy != nativeReadPolicy {
		t.Fatal("committed role policy changed", err)
	}
}

func TestSTSIssuanceCancelsQueuedRoleLookup(t *testing.T) {
	store, _ := s3TestState(t)
	db := store.Database()
	roles, err := NewIAMRoleStore(db)
	if err != nil {
		t.Fatal(err)
	}
	role, err := roles.Create(t.Context(), "queued-role", stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Roles: roles}
	authority := NewSTSAuthority("local-test-only")
	request := STSSessionRequest{Principal: role.RoleName, RoleARN: role.Arn, RoleID: role.RoleID,
		SessionName: "queued", Duration: 15 * time.Minute,
		Validate: func(ctx context.Context) error { _, err := gateway.stsRole(ctx, role.Arn); return err },
		Snapshot: func(ctx context.Context) (core.Policy, error) { return gateway.stsSessionPolicy(ctx, role) },
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			held, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var ctx context.Context
			var cancel context.CancelFunc
			if cause == context.DeadlineExceeded {
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			} else {
				ctx, cancel = context.WithCancel(t.Context())
			}
			before := db.Stats().WaitCount
			type outcome struct {
				credentials STSCredentials
				err         error
			}
			done := make(chan outcome, 1)
			go func() {
				defer close(done)
				c, err := authority.IssueContext(ctx, request)
				done <- outcome{c, err}
			}()
			defer func() {
				cancel()
				held.Close()
				<-done
			}()
			until := time.Now().Add(3 * time.Second)
			for db.Stats().WaitCount == before {
				if time.Now().After(until) {
					t.Fatal("role validation did not queue for the held connection")
				}
				runtime.Gosched()
			}
			if cause == context.Canceled {
				cancel()
			}
			select {
			case result := <-done:
				if !errors.Is(result.err, cause) || result.credentials != (STSCredentials{}) || len(authority.sessions) != 0 {
					t.Fatal("interrupted role lookup issued a session or lost its context error", result.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("role lookup waited for connection release after cancellation")
			}
		})
	}
	if _, err := authority.IssueContext(t.Context(), request); err != nil || len(authority.sessions) != 1 {
		t.Fatal("subsequent role validation failed", err)
	}
}
