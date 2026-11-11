package aws

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func sessionAuthFixture(t *testing.T) (*http.Request, *STSAuthority, SDKIdentity, string) {
	t.Helper()
	now := time.Unix(1800000000, 0)
	id := SDKIdentity{Principal: "viewer", ARN: "arn:aws:sts::000000000000:assumed-role/viewer/alice", Account: "000000000000", UserID: "ROLE:alice", Expires: now.Add(time.Hour)}
	key := "TCSIM-test-session"
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:StartSession","Resource":"arn:aws:ec2:us-east-1:000000000000:instance/i-own"},{"Effect":"Allow","Action":"ssm:TerminateSession","Resource":"arn:aws:ssm:us-east-1:000000000000:session/*","Condition":{"StringLike":{"ssm:resourceTag/aws:ssmmessages:session-id":"${aws:userid}*"}}},{"Effect":"Allow","Action":"ssmmessages:OpenDataChannel","Resource":"arn:aws:ssm:us-east-1:000000000000:session/${aws:userid}-*"},{"Effect":"Allow","Action":"ec2:DescribeInstances","Resource":"*","Condition":{"StringEquals":{"aws:ResourceTag/TenkaCloud:NamePrefix":"team"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	a := NewSTSAuthority("")
	a.now = func() time.Time { return now }
	a.sessions[key] = sdkSession{credentials: STSCredentials{AccessKeyID: key, Expiration: id.Expires}, identity: id, validate: func(context.Context) error { return nil }, snapshot: func(context.Context) (core.Policy, error) { return policy, nil }}
	ctx := context.WithValue(context.Background(), stsAuthorityContextKey{}, a)
	ctx = context.WithValue(ctx, sdkIdentityContextKey{}, id)
	ctx = context.WithValue(ctx, sdkSessionKeyContextKey{}, key)
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/", nil)
	return r, a, id, key
}

func TestSessionAuthVerifiedBindingNotHeaders(t *testing.T) {
	r, _, id, _ := sessionAuthFixture(t)
	r.Header.Set("X-Simulator-Principal", "simulator-admin")
	r.Header.Set("X-Aws-Userid", "ROLE:bob")
	c, err := bindSSMSessionCaller(r)
	if err != nil || c.identity != id {
		t.Fatalf("trusted identity changed: %v", err)
	}
	plain, _ := http.NewRequest(http.MethodPost, "http://localhost/", nil)
	plain.Header = r.Header.Clone()
	if _, err = bindSSMSessionCaller(plain); err == nil {
		t.Fatal("headers minted authority")
	}
	for _, ctx := range []context.Context{
		context.WithValue(r.Context(), consoleParameterBoundaryKey{}, consoleParameterBoundary{}),
		context.WithValue(r.Context(), sdkSessionKeyContextKey{}, "TCSIMOPERATOR"),
		context.WithValue(r.Context(), sdkIdentityContextKey{}, SDKIdentity{}),
		context.WithValue(r.Context(), sdkSessionKeyContextKey{}, "missing"),
	} {
		if _, err = bindSSMSessionCaller(r.WithContext(ctx)); err == nil {
			t.Fatal("invalid binding accepted")
		}
	}
}

func TestSessionAuthOwnershipRequiresExactCredential(t *testing.T) {
	r, a, id, key := sessionAuthFixture(t)
	c, err := bindSSMSessionCaller(r)
	if err != nil {
		t.Fatal(err)
	}
	if !c.owns(c) {
		t.Fatal("self ownership lost")
	}
	for _, peer := range []ssmSessionCaller{
		{authority: a, key: key + "-other", identity: id},
		{authority: NewSTSAuthority(""), key: key, identity: id},
		{authority: a, key: key, identity: SDKIdentity{UserID: "ROLE:alice2"}},
		{},
	} {
		if c.owns(peer) {
			t.Fatal("peer credential took ownership")
		}
	}
}

func TestSessionAuthPolicyContextIsBounded(t *testing.T) {
	r, _, _, _ := sessionAuthFixture(t)
	c, err := bindSSMSessionCaller(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		action, resource, tag string
		allow                 bool
	}{
		{"ssm:StartSession", "arn:aws:ec2:us-east-1:000000000000:instance/i-own", "", true},
		{"ssm:StartSession", "arn:aws:ec2:us-east-1:000000000000:instance/i-peer", "", false},
		{"ssm:TerminateSession", "arn:aws:ssm:us-east-1:000000000000:session/one", "ROLE:alice", true},
		{"ssm:TerminateSession", "arn:aws:ssm:us-east-1:000000000000:session/one", "ROLE:bob", false},
		{"ssm:TerminateSession", "arn:aws:ssm:us-east-1:000000000000:session/one", "", false},
		{"ssmmessages:OpenDataChannel", "arn:aws:ssm:us-east-1:000000000000:session/ROLE:alice-1", "", true},
		{"ssmmessages:OpenDataChannel", "arn:aws:ssm:us-east-1:000000000000:session/ROLE:alice2-1", "", false},
		{"ec2:DescribeInstances", "*", "team", false},
	} {
		if got := c.authorize(r.Context(), tc.action, tc.resource, tc.tag) == nil; got != tc.allow {
			t.Fatalf("%+v got %v", tc, got)
		}
	}
}

func TestSessionAuthRevalidatesExpiryAndRole(t *testing.T) {
	for _, kind := range []string{"expired", "deleted", "role-revoked", "policy-error", "identity-replaced"} {
		t.Run(kind, func(t *testing.T) {
			r, a, id, key := sessionAuthFixture(t)
			c, err := bindSSMSessionCaller(r)
			if err != nil {
				t.Fatal(err)
			}
			s := a.sessions[key]
			switch kind {
			case "expired":
				a.now = func() time.Time { return id.Expires }
			case "deleted":
				delete(a.sessions, key)
			case "role-revoked":
				s.validate = func(context.Context) error { return errors.New("generation revoked") }
				a.sessions[key] = s
			case "policy-error":
				s.snapshot = func(context.Context) (core.Policy, error) { return core.Policy{}, errors.New("policy unavailable") }
				a.sessions[key] = s
			case "identity-replaced":
				s.identity.UserID = "ROLE:bob"
				a.sessions[key] = s
			}
			if c.authorize(r.Context(), "ssm:StartSession", "arn:aws:ec2:us-east-1:000000000000:instance/i-own", "") == nil {
				t.Fatal("stale caller authorized")
			}
		})
	}
}

func TestSessionAuthRechecksAfterSnapshot(t *testing.T) {
	r, a, _, key := sessionAuthFixture(t)
	s := a.sessions[key]
	original := s.snapshot
	s.snapshot = func(ctx context.Context) (core.Policy, error) {
		p, err := original(ctx)
		a.mu.Lock()
		delete(a.sessions, key)
		a.mu.Unlock()
		return p, err
	}
	a.sessions[key] = s
	if _, err := bindSSMSessionCaller(r); err == nil {
		t.Fatal("removed credential survived snapshot")
	}
}

func TestSessionAuthDoesNotCachePolicyAllow(t *testing.T) {
	r, a, _, key := sessionAuthFixture(t)
	c, err := bindSSMSessionCaller(r)
	if err != nil {
		t.Fatal(err)
	}
	resource := "arn:aws:ec2:us-east-1:000000000000:instance/i-own"
	if err = c.authorize(r.Context(), "ssm:StartSession", resource, ""); err != nil {
		t.Fatal(err)
	}
	s := a.sessions[key]
	deny, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":{"Effect":"Deny","Action":"*","Resource":"*"}}`))
	if err != nil {
		t.Fatal(err)
	}
	s.snapshot = func(context.Context) (core.Policy, error) { return deny, nil }
	a.sessions[key] = s
	if c.authorize(r.Context(), "ssm:StartSession", resource, "") == nil {
		t.Fatal("old allow cached after policy revocation")
	}
}
