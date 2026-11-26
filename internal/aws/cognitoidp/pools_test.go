package cognitoidp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func poolFixture(t *testing.T) (*Store, *core.Store) {
	t.Helper()
	c, err := core.New("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	s, err := NewStore(c.Database())
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func TestUserPoolsScopedStoragePageReauthorizationAndIsolation(t *testing.T) {
	s, _ := poolFixture(t)
	ctx := t.Context()
	sc := Scope{"012345678901", "us-east-1"}
	a := Actor{"credential-a", "exercise-a"}
	p1, err := s.create(ctx, sc, "first pool")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.create(ctx, sc, "second pool")
	if err != nil || p1.ID == p2.ID {
		t.Fatal("distinct creation failed", err)
	}
	first, token, err := s.list(ctx, sc, a, 1, "")
	if len(first) != 1 || token == "" || err != nil {
		t.Fatal("bounded first page failed", err)
	}
	second, end, err := s.list(ctx, sc, a, 1, token)
	if len(second) != 1 || second[0].ID == first[0].ID || end != "" || err != nil {
		t.Fatal("lost or repeated pool on continuation", err)
	}
	for _, query := range []struct {
		scope Scope
		actor Actor
		limit int
		token string
	}{
		{Scope{"012345678902", sc.Region}, a, 1, token},
		{Scope{sc.Account, "us-west-2"}, a, 1, token},
		{sc, Actor{"credential-b", a.Exercise}, 1, token},
		{sc, Actor{a.Credential, "exercise-b"}, 1, token},
		{sc, a, 2, token},
		{sc, a, 1, token + "A"},
	} {
		if pools, next, err := s.list(ctx, query.scope, query.actor, query.limit, query.token); !errors.Is(err, errInvalid) || pools != nil || next != "" {
			t.Fatal("foreign or altered continuation was admitted", err)
		}
	}
	foreign := Scope{"012345678902", sc.Region}
	if _, err := s.describe(ctx, foreign, p1.ID); !errors.Is(err, errMissing) {
		t.Fatal("described foreign account pool", err)
	}
	if err := s.delete(ctx, foreign, p1.ID); !errors.Is(err, errMissing) {
		t.Fatal("deleted foreign account pool", err)
	}
	if row, err := s.describe(ctx, sc, p1.ID); err != nil || row != p1 {
		t.Fatal("cross-scope query changed the owner row", err)
	}

	allow := false
	authCalls := 0
	h := NewHTTPHandler(s, sc, a, func(_ context.Context, action, arn string) bool {
		authCalls++
		if action != "cognito-idp:ListUserPools" || arn != "*" {
			t.Error("list required unrelated policy")
		}
		return allow
	})
	body, _ := json.Marshal(map[string]any{"MaxResults": 1, "NextToken": token})
	poolRequest(t, h, "ListUserPools", string(body), 403, "AccessDeniedException")
	allow = true
	poolRequest(t, h, "ListUserPools", string(body), 200, "")
	if authCalls != 2 {
		t.Fatal("each continuation must call current authorization")
	}
}

func TestUserPoolsWireRejectsUnsupportedAndAmbiguousInputsBeforeAuthority(t *testing.T) {
	s, _ := poolFixture(t)
	scope := Scope{"012345678901", "us-east-1"}
	calls := 0
	h := NewHTTPHandler(s, scope, Actor{"test-credential", "fixture"}, func(context.Context, string, string) bool {
		calls++
		return true
	})
	for _, input := range []struct{ op, body, code string }{
		{"CreateUserPool", `{}`, "InvalidParameterException"},
		{"CreateUserPool", `{"PoolName":"   "}`, "InvalidParameterException"},
		{"CreateUserPool", `{"PoolName":"a","PoolName":"b"}`, "InvalidParameterException"},
		{"CreateUserPool", `{"poolName":"case-mismatch"}`, "InvalidParameterException"},
		{"CreateUserPool", `{"PoolName":"test","DeletionProtection":"ACTIVE"}`, "InvalidParameterException"},
		{"CreateUserPool", `{"PoolName":"test","LambdaConfig":{}}`, "InvalidParameterException"},
		{"CreateUserPool", `{"PoolName":"test"}{}`, "InvalidParameterException"},
		{"CreateUserPool", `{"PoolName":"` + strings.Repeat("a", RequestLimit) + `"}`, "InvalidParameterException"},
		{"ListUserPools", `{"MaxResults":0}`, "InvalidParameterException"},
		{"ListUserPools", `{"MaxResults":61}`, "InvalidParameterException"},
		{"ListUserPools", `{"MaxResults":1.0}`, "InvalidParameterException"},
		{"ListUserPools", `{"MaxResults":1,"NextToken":null}`, "InvalidParameterException"},
		{"DescribeUserPool", `{"UserPoolId":"us-west-2_foreign"}`, "InvalidParameterException"},
		{"DeleteUserPool", `{"UserPoolId":""}`, "InvalidParameterException"},
		{"InitiateAuth", `{}`, "UnknownOperationException"},
		{"CreateIdentityProvider", `{}`, "UnknownOperationException"},
	} {
		poolRequest(t, h, input.op, input.body, 400, input.code)
	}
	if calls != 0 {
		t.Fatal("unsupported inputs reached IAM")
	}
	resp := poolRequest(t, h, "ListUserPools", `{"MaxResults":60}`, 200, "")
	if !strings.Contains(resp.Body.String(), `"UserPools":[]`) {
		t.Fatal("unsupported create or sign-in changed pool state")
	}
}

func poolRequest(t *testing.T, h http.Handler, operation, body string, status int, code string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", strings.NewReader(body))
	r.Header.Set("Content-Type", JSONMediaType)
	r.Header.Set("X-Amz-Target", TargetPrefix+operation)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status || w.Header().Get("Content-Type") != JSONMediaType || w.Header().Get("X-Amzn-Errortype") != code {
		t.Fatalf("%s: status=%d type=%s err=%s", operation, w.Code, w.Header().Get("Content-Type"), w.Header().Get("X-Amzn-Errortype"))
	}
	return w
}

func TestUserPoolQuotaSerializesIndependentDatabaseHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pools.sqlite")
	setup := func() *Store {
		c, err := core.New(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		s, err := NewStore(c.Database())
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s1, s2 := setup(), setup()
	scope := Scope{"012345678901", "us-east-1"}
	ctx := t.Context()
	for range PoolLimit - 1 {
		if _, err := s1.create(ctx, scope, "same friendly name"); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	out := make(chan error, 2)
	for _, s := range []*Store{s1, s2} {
		go func() {
			<-start
			_, err := s.create(ctx, scope, "quota contender")
			out <- err
		}()
	}
	close(start)
	one, two := <-out, <-out
	if !(one == nil && errors.Is(two, errQuota) || two == nil && errors.Is(one, errQuota)) {
		t.Fatal("two connections did not share the same atomic quota", one, two)
	}
	var count int
	if err := s1.db.QueryRowContext(ctx, "SELECT count(*) FROM aws_cognito_user_pools").Scan(&count); err != nil || count != PoolLimit {
		t.Fatal("incorrect persisted pool count", count, err)
	}
	if _, err := s2.create(ctx, Scope{scope.Account, "us-west-2"}, "different region"); err != nil {
		t.Fatal("regional quota leaked across boundaries", err)
	}
}
