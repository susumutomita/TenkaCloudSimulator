package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestcommand"
)

const nativeHTTPRole = "AROA0000000000000000"

type nativeCommandHTTPFixture struct {
	api     *SSMCommandAPI
	store   *SSMCommandStore
	db      *faultDB
	ctx     context.Context
	policy  *core.Policy
	leases  []EC2GuestLease
	grant   SSMCommandAuthority
	sent    []SSMCommand
	allowed core.Policy
}

// All effects are memory doubles; requests carry an already verified native
// identity, exactly as the gateway hands them to ServeNative.
func newNativeCommandHTTPFixture(t *testing.T, commands int) *nativeCommandHTTPFixture {
	t.Helper()
	db := newFaultDB(t)
	ls := faultLeaseStore(t, db.DB)
	var leases []EC2GuestLease
	for n := 0; n < 3; n++ {
		o := leaseOwner()
		o.Instance = fmt.Sprintf("i-%017x", n+1)
		l, err := ls.Acquire(context.Background(), o)
		if err == nil {
			l, err = ls.Reconcile(context.Background(), l)
		}
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, l)
	}
	backend := &nativeCommandBackendDouble{ssmCommandDouble: &ssmCommandDouble{dispatch: func(guestcommand.DurableIntent) error { return nil }, observe: func(i guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
		return guestcommand.DurableObservation{Intent: i, State: "Rejected", FinishedAt: i.RequestedAt}, nil
	}}}
	backend.resolve = func(context.Context, string, string) (EC2GuestLease, error) { return leases[0], nil }
	store, err := NewSSMCommandStore(db.DB, backend)
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewSSMCommandAPI(store)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:*","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	f := &nativeCommandHTTPFixture{api: api, store: store, db: db, leases: leases, allowed: allowed, policy: &core.Policy{}}
	*f.policy = allowed
	_, authority, identity, key := sessionAuthFixture(t)
	identity.UserID = nativeHTTPRole + ":job"
	session := authority.sessions[key]
	session.identity = identity
	session.snapshot = func(context.Context) (core.Policy, error) { return *f.policy, nil }
	authority.sessions[key] = session
	ctx := context.WithValue(context.Background(), stsAuthorityContextKey{}, authority)
	ctx = context.WithValue(ctx, sdkIdentityContextKey{}, identity)
	f.ctx = context.WithValue(ctx, sdkSessionKeyContextKey{}, key)
	f.grant = SSMCommandAuthority{Account: "000000000000", Region: "us-east-1", Principal: nativeHTTPRole, Deployment: leases[0].Owner.Deployment}
	for n := 0; n < commands; n++ {
		r, err := store.Send(context.Background(), f.grant, ssmCommandRequestFixture(leases[n:n+1]), leases[n:n+1])
		if err != nil {
			t.Fatal(err)
		}
		f.sent = append(f.sent, r)
	}
	return f
}

func (f *nativeCommandHTTPFixture) request(operation, body string, edit func(*http.Request)) *http.Request {
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/", strings.NewReader(body)).WithContext(f.ctx)
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "AmazonSSM."+operation)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIMTEST/20261112/us-east-1/ssm/aws4_request, SignedHeaders=host, Signature="+strings.Repeat("ab", 32))
	if edit != nil {
		edit(r)
	}
	return r
}

func (f *nativeCommandHTTPFixture) call(t *testing.T, r *http.Request) (int, map[string]json.RawMessage) {
	t.Helper()
	w := httptest.NewRecorder()
	f.api.ServeNative(w, r)
	var out map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("non-JSON response %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sqlite") || strings.Contains(w.Body.String(), "printf hello") && w.Code != 200 {
		t.Fatalf("response leaked internals: %s", w.Body.String())
	}
	return w.Code, out
}

func wireType(out map[string]json.RawMessage) string {
	var s string
	_ = json.Unmarshal(out["__type"], &s)
	return s
}

func listBody(after string, max int, token string) string {
	b := map[string]any{"Filters": []map[string]string{{"key": "InvokedAfter", "value": after}}, "MaxResults": max}
	if token != "" {
		b["NextToken"] = token
	}
	return ssmEncode(b)
}

func TestNativeCommandHTTPRefusesMalformedTransport(t *testing.T) {
	f := newNativeCommandHTTPFixture(t, 0)
	get := ssmEncode(map[string]string{"CommandId": "00000000-0000-4000-8000-000000000000", "InstanceId": "i-0000000000000000a"})
	cases := map[string]struct {
		operation, body string
		edit            func(*http.Request)
		status          int
		code            string
	}{
		"GET":             {"ListCommands", "{}", func(r *http.Request) { r.Method = "GET" }, 400, "ValidationException"},
		"path":            {"ListCommands", "{}", func(r *http.Request) { r.URL.Path = "/x" }, 400, "ValidationException"},
		"query":           {"ListCommands", "{}", func(r *http.Request) { r.URL.RawQuery = "a=b" }, 400, "ValidationException"},
		"content type":    {"ListCommands", "{}", func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }, 400, "ValidationException"},
		"two types":       {"ListCommands", "{}", func(r *http.Request) { r.Header.Add("Content-Type", "application/x-amz-json-1.1") }, 400, "ValidationException"},
		"two targets":     {"ListCommands", "{}", func(r *http.Request) { r.Header.Add("X-Amz-Target", "AmazonSSM.ListCommands") }, 400, "ValidationException"},
		"other operation": {"DescribeInstanceInformation", "{}", nil, 400, "ValidationException"},
		"unsigned":        {"ListCommands", "{}", func(r *http.Request) { r.Header.Del("Authorization") }, 403, "AccessDeniedException"},
		"other service": {"ListCommands", "{}", func(r *http.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "/ssm/", "/sts/", 1))
		}, 403, "AccessDeniedException"},
		"region header": {"ListCommands", "{}", func(r *http.Request) { r.Header.Set("X-Simulator-Region", "eu-west-1") }, 400, "ValidationException"},
		"oversized":     {"ListCommands", `{"NextToken":"` + strings.Repeat("a", 49<<10) + `"}`, nil, 400, "ValidationException"},
		"terminal handoff": {"ListCommands", "{}", func(r *http.Request) {
			*r = *r.WithContext(context.WithValue(r.Context(), terminalBoundaryKey{}, terminalBoundary{}))
		}, 403, "AccessDeniedException"},
		"unverified": {"ListCommands", "{}", func(r *http.Request) { *r = *r.WithContext(context.Background()) }, 403, "AccessDeniedException"},
		"session name role": {"ListCommands", "{}", func(r *http.Request) {
			id, _ := SDKIdentityFromContext(r.Context())
			id.UserID = "viewer:job"
			*r = *r.WithContext(context.WithValue(r.Context(), sdkIdentityContextKey{}, id))
		}, 403, "AccessDeniedException"},
		"not JSON":          {"ListCommands", "{", nil, 400, "ValidationException"},
		"unknown field":     {"GetCommandInvocation", `{"CommandId":"x","InstanceId":"i-00","PluginName":"p"}`, nil, 400, "ValidationException"},
		"bad command ID":    {"GetCommandInvocation", `{"CommandId":"x","InstanceId":"i-00"}`, nil, 400, "ValidationException"},
		"send wrong type":   {"SendCommand", `{"DocumentName":1}`, nil, 400, "ValidationException"},
		"send bad instance": {"SendCommand", ssmEncode(SSMCommandRequest{DocumentName: "AWS-RunShellScript", InstanceIds: []string{"vm-01"}, Parameters: map[string][]string{"commands": {"true"}}, TimeoutSeconds: 30}), nil, 400, "ValidationException"},
		"max 0":             {"ListCommands", listBody("2000-01-01T00:00:00Z", 0, ""), nil, 400, "ValidationException"},
		"long token":        {"ListCommands", listBody("2000-01-01T00:00:00Z", 1, strings.Repeat("a", 513)), nil, 400, "ValidationException"},
		"no filter":         {"ListCommands", `{"MaxResults":1}`, nil, 400, "ValidationException"},
		"two filters":       {"ListCommands", `{"Filters":[{"key":"InvokedAfter","value":"2000-01-01T00:00:00Z"},{"key":"InvokedAfter","value":"2000-01-01T00:00:00Z"}]}`, nil, 400, "ValidationException"},
		"filter key":        {"ListCommands", `{"Filters":[{"key":"Status","value":"Success"}]}`, nil, 400, "ValidationException"},
		"filter time":       {"ListCommands", `{"Filters":[{"key":"InvokedAfter","value":"yesterday"}]}`, nil, 400, "ValidationException"},
		"pre-epoch":         {"ListCommands", `{"Filters":[{"key":"InvokedAfter","value":"1960-01-01T00:00:00Z"}]}`, nil, 400, "ValidationException"},
		"unknown command":   {"GetCommandInvocation", get, nil, 400, "InvalidCommandId"},
	}
	for name, c := range cases {
		status, out := f.call(t, f.request(c.operation, c.body, c.edit))
		if status != c.status || wireType(out) != c.code {
			t.Errorf("%s: %d %s", name, status, wireType(out))
		}
	}
	unconfigured := (*SSMCommandAPI)(nil)
	w := httptest.NewRecorder()
	unconfigured.ServeNative(w, f.request("ListCommands", "{}", nil))
	if w.Code != 501 {
		t.Fatal("unconfigured backend", w.Code)
	}
	var n int
	if err := f.db.QueryRow(`SELECT count(*) FROM aws_ssm_commands`).Scan(&n); err != nil || n != 0 {
		t.Fatal("refused request retained a command", n, err)
	}
}

func TestNativeCommandHTTPReadsRequireCurrentPolicy(t *testing.T) {
	f := newNativeCommandHTTPFixture(t, 1)
	get := ssmEncode(map[string]string{"CommandId": f.sent[0].CommandId, "InstanceId": f.leases[0].Owner.Instance})
	status, out := f.call(t, f.request("GetCommandInvocation", get, nil))
	var state string
	_ = json.Unmarshal(out["Status"], &state)
	if status != 200 || state != "Failed" {
		t.Fatalf("rejected delivery not reported as failed: %d %s", status, out)
	}
	status, out = f.call(t, f.request("GetCommandInvocation", ssmEncode(map[string]string{"CommandId": f.sent[0].CommandId, "InstanceId": "i-00000000000000099"}), nil))
	if status != 400 || wireType(out) != "InvalidCommandId" {
		t.Fatal("foreign target read", status, out)
	}
	*f.policy = core.Policy{}
	for _, c := range []struct{ op, body string }{{"GetCommandInvocation", get}, {"ListCommands", listBody("2000-01-01T00:00:00Z", 5, "")}, {"SendCommand", ssmEncode(ssmCommandRequestFixture(f.leases[1:2]))}} {
		if status, out = f.call(t, f.request(c.op, c.body, nil)); status != 403 || wireType(out) != "AccessDeniedException" {
			t.Errorf("%s allowed without policy: %d %s", c.op, status, out)
		}
	}
	*f.policy = f.allowed
	// A command retained for another role incarnation is invisible.
	if _, err := f.db.Exec(`UPDATE aws_ssm_commands SET record=replace(record,?,?)`, nativeHTTPRole, "AROA1111111111111111"); err != nil {
		t.Fatal(err)
	}
	if status, out = f.call(t, f.request("GetCommandInvocation", get, nil)); status != 400 || wireType(out) != "InvalidCommandId" {
		t.Fatal("other role's command read", status, out)
	}
}

func TestNativeCommandHTTPListCursorsAreRequestBoundSnapshots(t *testing.T) {
	f := newNativeCommandHTTPFixture(t, 3)
	after := "2000-01-01T00:00:00Z"
	status, out := f.call(t, f.request("ListCommands", listBody(after, 2, ""), nil))
	var commands []nativeCommandSummary
	var next string
	_ = json.Unmarshal(out["Commands"], &commands)
	_ = json.Unmarshal(out["NextToken"], &next)
	if status != 200 || len(commands) != 2 || next == "" {
		t.Fatalf("first page: %d %s", status, out)
	}
	status, out = f.call(t, f.request("ListCommands", listBody(after, 2, next), nil))
	var rest []nativeCommandSummary
	_ = json.Unmarshal(out["Commands"], &rest)
	if status != 200 || len(rest) != 1 || rest[0].CommandID == commands[0].CommandID || rest[0].CommandID == commands[1].CommandID {
		t.Fatalf("second page: %d %s", status, out)
	}
	token, _, _ := strings.Cut(next, ".")
	for name, c := range map[string]struct {
		body string
	}{
		"no offset":     {listBody(after, 2, token)},
		"bad token":     {listBody(after, 2, "nope.2")},
		"zero offset":   {listBody(after, 2, token+".0")},
		"padded offset": {listBody(after, 2, token+".02")},
		"misaligned":    {listBody(after, 2, token+".1")},
		"past end":      {listBody(after, 2, token+".4")},
		"other size":    {listBody(after, 1, next)},
		"other filter":  {listBody("2001-01-01T00:00:00Z", 2, next)},
		"unknown token": {listBody(after, 2, strings.Repeat("c", 64)+".2")},
	} {
		if status, out = f.call(t, f.request("ListCommands", c.body, nil)); status == 200 {
			t.Errorf("%s cursor accepted: %s", name, out)
		}
	}
	// A cursor is not trusted opaque data: a corrupted stored snapshot fails closed.
	var raw string
	if err := f.db.QueryRow(`SELECT record FROM aws_ssm_native_command_pages WHERE token=?`, token).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var page nativeCommandPage
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*nativeCommandPage){
		"duplicate": func(p *nativeCommandPage) { p.Commands[1] = p.Commands[0] },
		"unordered": func(p *nativeCommandPage) { p.Commands[0], p.Commands[1] = p.Commands[1], p.Commands[0] },
		"early":     func(p *nativeCommandPage) { p.Commands[0].RequestedAt = 1 },
		"bad ID":    func(p *nativeCommandPage) { p.Commands[2].CommandId = "x" },
		"request":   func(p *nativeCommandPage) { p.Commands[2].Request.Comment = "rewritten" },
	} {
		var forged nativeCommandPage
		_ = json.Unmarshal([]byte(raw), &forged)
		edit(&forged)
		if _, err := f.db.Exec(`UPDATE aws_ssm_native_command_pages SET record=? WHERE token=?`, ssmEncode(forged), token); err != nil {
			t.Fatal(err)
		}
		if status, out = f.call(t, f.request("ListCommands", listBody(after, 2, next), nil)); status == 200 {
			t.Errorf("%s snapshot accepted: %s", name, out)
		}
	}
	if _, err := f.db.Exec(`UPDATE aws_ssm_native_command_pages SET record=? WHERE token=?`, raw, token); err != nil {
		t.Fatal(err)
	}
	if status, _ = f.call(t, f.request("ListCommands", listBody(after, 2, next), nil)); status != 200 {
		t.Fatal("restored snapshot refused")
	}
	if len(page.Commands) != 3 {
		t.Fatal("snapshot size", len(page.Commands))
	}
}

func TestSSMCommandStoreListRefusesForgedCursors(t *testing.T) {
	f := newNativeCommandHTTPFixture(t, 3)
	ctx := context.Background()
	after := "2000-01-01T00:00:00Z"
	for name, c := range map[string]struct {
		after string
		a     SSMCommandAuthority
		max   int
	}{
		"bad time":  {"yesterday", f.grant, 1},
		"pre-epoch": {"1960-01-01T00:00:00Z", f.grant, 1},
		"authority": {after, SSMCommandAuthority{}, 1},
		"max 0":     {after, f.grant, 0},
		"max 51":    {after, f.grant, 51},
	} {
		if _, _, err := f.store.List(ctx, c.a, c.after, c.max, ""); !errors.Is(err, ErrSSMCommandInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	other := f.grant
	other.Principal = "other-principal"
	if got, next, err := f.store.List(ctx, other, after, 5, ""); err != nil || len(got) != 0 || next != "" {
		t.Fatal("foreign authority listed commands", got, err)
	}
	first, next, err := f.store.List(ctx, f.grant, after, 1, "")
	if err != nil || len(first) != 1 || next == "" {
		t.Fatal(first, next, err)
	}
	token, _, _ := strings.Cut(next, ".")
	for name, cursor := range map[string]string{"no offset": token, "bad token": "x.1", "zero": token + ".0", "padded": token + ".01", "huge": token + ".99999999"} {
		if _, _, err = f.store.List(ctx, f.grant, after, 1, cursor); !errors.Is(err, ErrSSMCommandInvalid) {
			t.Errorf("%s cursor accepted: %v", name, err)
		}
	}
	var raw string
	if err = f.db.QueryRow(`SELECT record FROM aws_ssm_command_pages WHERE token=?`, token).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*ssmCommandPage){
		"early":     func(p *ssmCommandPage) { p.Commands[0].RequestedAt = 1 },
		"unordered": func(p *ssmCommandPage) { p.Commands[0], p.Commands[1] = p.Commands[1], p.Commands[0] },
		"bad ID":    func(p *ssmCommandPage) { p.Commands[2].CommandId = "x" },
		"request":   func(p *ssmCommandPage) { p.Commands[2].Request.TimeoutSeconds = 1 },
	} {
		var forged ssmCommandPage
		_ = json.Unmarshal([]byte(raw), &forged)
		edit(&forged)
		if _, err = f.db.Exec(`UPDATE aws_ssm_command_pages SET record=? WHERE token=?`, ssmEncode(forged), token); err != nil {
			t.Fatal(err)
		}
		if _, _, err = f.store.List(ctx, f.grant, after, 1, next); !errors.Is(err, ErrSSMCommandInvalid) {
			t.Errorf("%s snapshot accepted: %v", name, err)
		}
	}
	if _, err = f.db.Exec(`UPDATE aws_ssm_command_pages SET record=? WHERE token=?`, raw, token); err != nil {
		t.Fatal(err)
	}
	if got, _, err := f.store.List(ctx, f.grant, after, 1, next); err != nil || len(got) != 1 || got[0].CommandId == first[0].CommandId {
		t.Fatal("restored snapshot", got, err)
	}
}
