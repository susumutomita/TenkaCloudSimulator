package cognitoidp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserPoolWireAdmissionAndClosedState(t *testing.T) {
	s, c := poolFixture(t)
	scope := Scope{"012345678901", "us-east-1"}
	actor := Actor{"credential-a", "exercise-a"}
	authCalls := 0
	allow := func(context.Context, string, string) bool {
		authCalls++
		return true
	}
	serve := func(h http.Handler, r *http.Request, status int, code string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var errorBody struct {
			Type string `json:"__type"`
		}
		if w.Code != status || w.Header().Get("Content-Type") != JSONMediaType ||
			w.Header().Get("X-Amzn-Errortype") != code ||
			json.Unmarshal(w.Body.Bytes(), &errorBody) != nil || errorBody.Type != code {
			t.Fatalf("got %d/%s, want %d/%s", w.Code, w.Header().Get("X-Amzn-Errortype"), status, code)
		}
	}
	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", strings.NewReader(`{"PoolName":"valid pool"}`))
		r.Header.Set("Content-Type", JSONMediaType)
		r.Header.Set("X-Amz-Target", TargetPrefix+"CreateUserPool")
		return r
	}
	for _, tc := range []struct {
		name string
		h    http.Handler
	}{
		{"no store", NewHTTPHandler(nil, scope, actor, allow)},
		{"no owned sqlite", NewHTTPHandler(&Store{}, scope, actor, allow)},
		{"no trusted scope", NewHTTPHandler(s, Scope{}, actor, allow)},
		{"no trusted actor", NewHTTPHandler(s, scope, Actor{}, allow)},
		{"no authorization callback", NewHTTPHandler(s, scope, actor, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serve(tc.h, request(), http.StatusForbidden, "UnrecognizedClientException")
		})
	}
	h := NewHTTPHandler(s, scope, actor, allow)
	for _, tc := range []struct {
		name string
		edit func(*http.Request)
	}{
		{"wrong method", func(r *http.Request) { r.Method = http.MethodGet }},
		{"wrong path", func(r *http.Request) { r.URL.Path = "/other" }},
		{"query", func(r *http.Request) { r.URL.RawQuery = "fixture=1" }},
		{"empty query delimiter", func(r *http.Request) { r.URL.ForceQuery = true }},
		{"two media declarations", func(r *http.Request) { r.Header.Add("Content-Type", JSONMediaType) }},
		{"two targets", func(r *http.Request) { r.Header.Add("X-Amz-Target", TargetPrefix+"ListUserPools") }},
		{"unsupported media type", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-amz-json-1.0") }},
		{"missing body", func(r *http.Request) { r.Body = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request()
			tc.edit(r)
			serve(h, r, http.StatusBadRequest, "InvalidParameterException")
		})
	}
	for _, tc := range []struct{ name, body string }{
		{"invalid UTF-8", "{\"PoolName\":\"\xff\"}"},
		{"non-object body", `["valid pool"]`},
		{"third property", `{"PoolName":"valid pool","DeletionProtection":"INACTIVE","Tags":{}}`},
		{"invalid nested element", `{"PoolName": [null,]}`},
		{"unterminated object", `{"PoolName":"valid pool"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			poolRequest(t, h, "CreateUserPool", tc.body, http.StatusBadRequest, "InvalidParameterException")
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	serve(h, request().WithContext(ctx), http.StatusInternalServerError, "InternalErrorException")
	if authCalls != 0 {
		t.Fatal("unbound, malformed or canceled input reached authorization")
	}
	if w := poolRequest(t, h, "ListUserPools", `{"MaxResults":1}`, http.StatusOK, ""); !strings.Contains(w.Body.String(), `"UserPools":[]`) {
		t.Fatal("invalid or canceled request changed state")
	}
	if _, err := NewStore(nil); err == nil {
		t.Fatal("store was created without caller-owned SQLite")
	}
	// A failed/freed database is not an empty or successful listing; an
	// operator must not receive engine details or state from a failed operation.
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(s.db); err == nil {
		t.Fatal("store initialization did not surface closed SQLite")
	}
	for _, tc := range []struct{ operation, body string }{
		{"CreateUserPool", `{"PoolName":"valid pool"}`},
		{"DescribeUserPool", `{"UserPoolId":"us-east-1_fixture"}`},
		{"ListUserPools", `{"MaxResults":1}`},
		{"DeleteUserPool", `{"UserPoolId":"us-east-1_fixture"}`},
	} {
		w := poolRequest(t, h, tc.operation, tc.body, http.StatusInternalServerError, "InternalErrorException")
		if !strings.Contains(w.Body.String(), "Local user pool state operation failed.") ||
			strings.Contains(w.Body.String(), "database") || strings.Contains(w.Body.String(), "UserPools") {
			t.Fatal("failed state operation did not return its opaque error")
		}
	}
}

func TestUserPoolContinuationSurvivesDeletionButNotStoreRestart(t *testing.T) {
	s, _ := poolFixture(t)
	scope := Scope{"012345678901", "us-east-1"}
	actor := Actor{"credential-a", "exercise-a"}
	ctx := t.Context()
	for _, name := range []string{"first pool", "second pool"} {
		if _, err := s.create(ctx, scope, name); err != nil {
			t.Fatal(err)
		}
	}
	page, token, err := s.list(ctx, scope, actor, 1, "")
	if err != nil || len(page) != 1 || token == "" {
		t.Fatal("first page did not produce a continuation", err)
	}
	// A deleted marker cannot resurrect a row or erase the next existing row.
	if err := s.delete(ctx, scope, page[0].ID); err != nil {
		t.Fatal(err)
	}
	next, continuation, err := s.list(ctx, scope, actor, 1, token)
	if err != nil || len(next) != 1 || next[0].ID == page[0].ID || continuation != "" {
		t.Fatal("continuation after deletion did not return just the remaining row", err)
	}
	for _, tc := range []struct{ name, value string }{
		{"missing separator", "not-a-cursor"},
		{"wrapped cursor", token + "\n"},
		{"invalid base64", "!._"},
		{"oversized cursor", strings.Repeat("a", 1025)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rows, next, err := s.list(ctx, scope, actor, 1, tc.value); rows != nil || next != "" || !errors.Is(err, errInvalid) {
				t.Fatal("malformed continuation returned rows or a replacement cursor", err)
			}
		})
	}
	fresh, err := NewStore(s.db)
	if err != nil {
		t.Fatal(err)
	}
	if rows, next, err := fresh.list(ctx, scope, actor, 1, token); rows != nil || next != "" || !errors.Is(err, errInvalid) {
		t.Fatal("new process authority admitted its predecessor's continuation", err)
	}
	if rows, marker, err := fresh.list(ctx, scope, actor, 1, ""); len(rows) != 1 || rows[0] != next[0] || marker != "" || err != nil {
		t.Fatal("reinstantiating the provider changed durable pool metadata", err)
	}
}

func TestUserPoolQuotaIsWireVisibleAndDeletionReleasesCapacity(t *testing.T) {
	s, _ := poolFixture(t)
	scope := Scope{"012345678901", "us-east-1"}
	actor := Actor{"credential-a", "exercise-a"}
	ctx := t.Context()
	var first string
	for i := range PoolLimit {
		row, err := s.create(ctx, scope, "quota slot")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = row.ID
		}
	}
	h := NewHTTPHandler(s, scope, actor, func(context.Context, string, string) bool { return true })
	poolRequest(t, h, "CreateUserPool", `{"PoolName":"over limit"}`, http.StatusBadRequest, "LimitExceededException")
	body, err := json.Marshal(map[string]string{"UserPoolId": first})
	if err != nil {
		t.Fatal(err)
	}
	poolRequest(t, h, "DeleteUserPool", string(body), http.StatusOK, "")
	poolRequest(t, h, "DeleteUserPool", string(body), http.StatusBadRequest, "ResourceNotFoundException")
	poolRequest(t, h, "CreateUserPool", `{"PoolName":"now admitted"}`, http.StatusOK, "")
}

func TestUserPoolListNeverReturnsPartialResultsOnUnusableRows(t *testing.T) {
	s, _ := poolFixture(t)
	scope := Scope{"012345678901", "us-east-1"}
	ctx := t.Context()
	if _, err := s.create(ctx, scope, "healthy pool"); err != nil {
		t.Fatal(err)
	}
	// Model a damaged or unmigrated SQLite fixture; valid rows in the same
	// scope do not turn an undecodable page into a successful partial result.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO aws_cognito_user_pools(account,region,pool_id,name,created_ms)
		VALUES (?,?,'us-east-1_z','broken pool','not a timestamp')`, scope.Account, scope.Region); err != nil {
		t.Fatal(err)
	}
	h := NewHTTPHandler(s, scope, Actor{"credential-a", "exercise-a"}, func(context.Context, string, string) bool { return true })
	w := poolRequest(t, h, "ListUserPools", `{"MaxResults":10}`, http.StatusInternalServerError, "InternalErrorException")
	if strings.Contains(w.Body.String(), "healthy pool") || strings.Contains(w.Body.String(), "timestamp") ||
		strings.Contains(w.Body.String(), "NextToken") {
		t.Fatal("failed pool page leaked a partial result, cursor, or internal data")
	}
}
