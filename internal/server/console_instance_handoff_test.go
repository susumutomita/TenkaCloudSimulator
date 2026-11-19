package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestsession"
)

const instanceID = "i-12345678"

// pendingBackend models a guest whose relay does not answer yet.
type pendingBackend struct{}

func (*pendingBackend) ResolveSessionTarget(context.Context, string, string) (aws.SSMSessionTarget, error) {
	return aws.SSMSessionTarget{}, aws.ErrEC2GuestAttachmentPending
}
func (*pendingBackend) OpenSessionChannel(context.Context, aws.SSMSessionTarget) (*guestsession.Driver, error) {
	return nil, aws.ErrEC2GuestAttachmentPending
}

type instanceConsole struct {
	t      *testing.T
	a      *Access
	creds  aws.STSCredentials
	valid  *bool
	policy *core.Policy
	server *httptest.Server
	cookie *http.Cookie
	flow   *flowBackend
}

func newInstanceConsole(t *testing.T, backend aws.SSMSessionBackend) *instanceConsole {
	t.Helper()
	h, closeState, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeState() })
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sessions, err := aws.NewSSMSessionStore(db.Database(), backend, "ws://127.0.0.1:4566/_simulator/v1/terminal-channel")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sessions.Shutdown(ctx)
	})
	terminal := terminalHandler{store: sessions}
	valid := true
	policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"ssm:*", "ssmmessages:*", "ec2:DescribeInstances"}, Resource: core.StringList{"*"}}}}
	a, err := NewAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_simulator/v1/terminal-") {
			terminal.ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(w, r)
	}), strings.Repeat("a", 32), false)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := a.sdk.Issue(aws.STSSessionRequest{Principal: "viewer", RoleARN: "arn:aws:iam::000000000000:role/viewer", RoleID: "original", SessionName: "console", Duration: 15 * time.Minute, Validate: func(context.Context) error {
		if !valid {
			return fmt.Errorf("revoked")
		}
		return nil
	}, Snapshot: func(context.Context) (core.Policy, error) { return policy, nil }})
	if err != nil {
		t.Fatal(err)
	}
	f := &instanceConsole{t: t, a: a, creds: creds, valid: &valid, policy: &policy}
	f.flow, _ = backend.(*flowBackend)
	f.server = httptest.NewServer(a)
	t.Cleanup(f.server.Close)
	return f
}

func (f *instanceConsole) issue(body string) *httptest.ResponseRecorder {
	return handoffRequest(f.a, signedHandoff(f.creds, body))
}

func (f *instanceConsole) login() {
	f.t.Helper()
	w := f.issue(`{"InstanceId":"` + instanceID + `","Region":"us-east-1"}`)
	var out struct{ Code, ExpiresAt string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Code) != 64 || out.ExpiresAt == "" {
		f.t.Fatalf("instance handoff %d %s", w.Code, w.Body.String())
	}
	login := handoffRedeem(f.a, out.Code)
	if login.Code != 303 || login.Header().Get("Location") != "/_simulator/console/#/home?region=us-east-1" || len(login.Result().Cookies()) != 1 {
		f.t.Fatal("instance handoff login", login.Code, login.Header())
	}
	f.cookie = login.Result().Cookies()[0]
}

func (f *instanceConsole) do(method, path string, headers map[string]string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:4566"+path, strings.NewReader(body))
	r.AddCookie(f.cookie)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return handoffRequest(f.a, r)
}

func (f *instanceConsole) describe(headers map[string]string, query string) *httptest.ResponseRecorder {
	h := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "X-Simulator-Service": "ec2", "X-Simulator-Region": "us-east-1"}
	for k, v := range headers {
		h[k] = v
	}
	return f.do("POST", "/", h, query)
}

func TestConsoleInstanceHandoffRequestShape(t *testing.T) {
	f := newInstanceConsole(t, &flowBackend{})
	for name, body := range map[string]string{
		"both targets":         `{"InstanceId":"` + instanceID + `","ParameterName":"/own/value","Region":"us-east-1"}`,
		"neither target":       `{"Region":"us-east-1"}`,
		"unknown field":        `{"InstanceId":"` + instanceID + `","Region":"us-east-1","Extra":"x"}`,
		"malformed instance":   `{"InstanceId":"vm-1","Region":"us-east-1"}`,
		"uppercase instance":   `{"InstanceId":"i-ABCDEF12","Region":"us-east-1"}`,
		"short instance":       `{"InstanceId":"i-1234567","Region":"us-east-1"}`,
		"unsupported region":   `{"InstanceId":"` + instanceID + `","Region":"mars-1"}`,
		"missing region":       `{"InstanceId":"` + instanceID + `"}`,
		"trailing document":    `{"InstanceId":"` + instanceID + `","Region":"us-east-1"}{}`,
		"empty parameter name": `{"ParameterName":"","Region":"us-east-1"}`,
	} {
		if w := f.issue(body); w.Code != 400 {
			t.Fatalf("%s: status %d", name, w.Code)
		}
	}
}

func TestConsoleInstanceHandoffRefusesOperatorAndExpiredCredentials(t *testing.T) {
	f := newInstanceConsole(t, &flowBackend{})
	body := `{"InstanceId":"` + instanceID + `","Region":"us-east-1"}`
	for _, token := range []string{strings.Repeat("a", 32), ""} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:4566/_simulator/v1/console-handoffs", strings.NewReader(body))
		if token != "" {
			r.Header.Set("X-Simulator-Token", token)
		}
		if w := handoffRequest(f.a, r); w.Code == 200 {
			t.Fatal("operator or anonymous caller obtained an instance handoff")
		}
	}
	// A participant without ssm:StartSession on that instance cannot issue.
	f.policy.Statement = core.Statements{{Effect: "Allow", Action: core.StringList{"ssm:GetParameter"}, Resource: core.StringList{"*"}}}
	if w := f.issue(body); w.Code != 403 {
		t.Fatal("handoff issued without ssm:StartSession", w.Code)
	}
	f.policy.Statement = core.Statements{{Effect: "Allow", Action: core.StringList{"ssm:StartSession"}, Resource: core.StringList{"arn:aws:ec2:us-east-1:000000000000:instance/i-87654321"}}}
	if w := f.issue(body); w.Code != 403 {
		t.Fatal("handoff issued for another instance's grant", w.Code)
	}
	f.policy.Statement = core.Statements{{Effect: "Allow", Action: core.StringList{"ssm:StartSession"}, Resource: core.StringList{"*"}}}
	*f.valid = false
	if w := f.issue(body); w.Code == 200 {
		t.Fatal("handoff issued for a replaced role session")
	}
	*f.valid = true
	expired := *f
	expired.creds.SessionToken = "tampered"
	if w := expired.issue(body); w.Code == 200 {
		t.Fatal("handoff issued for an unknown session")
	}
	if w := f.issue(body); w.Code != 200 {
		t.Fatal("valid participant refused", w.Code)
	}
}

func TestConsoleInstanceHandoffRevokedWhenRoleReplaced(t *testing.T) {
	f := newInstanceConsole(t, &flowBackend{})
	f.login()
	if w := f.describe(nil, "Action=DescribeInstances&Version=2016-11-15"); w.Code != 200 {
		t.Fatal("scoped describe", w.Code, w.Body.String())
	}
	*f.valid = false
	if w := f.describe(nil, "Action=DescribeInstances&Version=2016-11-15"); w.Code == 200 {
		t.Fatal("console session survived role replacement")
	}
}

func TestConsoleInstanceHandoffCapabilities(t *testing.T) {
	f := newInstanceConsole(t, &flowBackend{})
	f.login()
	w := f.do("GET", "/_simulator/v1/capabilities", nil, "")
	var caps struct {
		ConsoleHandoff struct {
			Version  int
			Services []string
		}
		ConsoleScope map[string]string
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &caps) != nil {
		t.Fatal("capabilities", w.Code)
	}
	if caps.ConsoleHandoff.Version != 1 || strings.Join(caps.ConsoleHandoff.Services, ",") != "ssm-parameter,ec2-instance" {
		t.Fatal("handoff capability", caps.ConsoleHandoff)
	}
	if caps.ConsoleScope["instanceId"] != instanceID || caps.ConsoleScope["region"] != "us-east-1" || caps.ConsoleScope["parameterName"] != "" {
		t.Fatal("scope", caps.ConsoleScope)
	}
}

func TestConsoleInstanceSessionIsLimitedToConsoleEC2AndSessionManager(t *testing.T) {
	f := newInstanceConsole(t, &flowBackend{})
	f.login()
	if w := f.describe(nil, "Action=DescribeInstances&Version=2016-11-15"); w.Code != 200 {
		t.Fatal("DescribeInstances", w.Code, w.Body.String())
	}
	for name, w := range map[string]*httptest.ResponseRecorder{
		"other region":  f.describe(map[string]string{"X-Simulator-Region": "us-west-2"}, "Action=DescribeInstances&Version=2016-11-15"),
		"VPC discovery": f.describe(nil, "Action=DescribeVpcs&Version=2016-11-15"),

		"parameter read":    f.do("POST", "/", map[string]string{"Content-Type": "application/x-amz-json-1.1", "X-Amz-Target": "AmazonSSM.GetParameter", "X-Simulator-Region": "us-east-1"}, `{"Name":"/own/value"}`),
		"S3 listing":        f.do("GET", "/", nil, ""),
		"IAM control":       f.do("GET", "/_simulator/v1/iam/roles", nil, ""),
		"more handoffs":     f.do("POST", "/_simulator/v1/console-handoffs", map[string]string{"Content-Type": "application/json"}, `{"InstanceId":"`+instanceID+`","Region":"us-east-1"}`),
		"terminal handoff":  f.do("POST", "/_simulator/v1/terminal-handoffs", map[string]string{"Content-Type": "application/json"}, `{"Target":"`+instanceID+`","Region":"us-east-1"}`),
		"query on terminal": f.do("GET", "/_simulator/v1/terminal-readiness?x=1", nil, ""),
		"readiness method":  f.do("POST", "/_simulator/v1/terminal-readiness", nil, ""),
		"SigV4 mixed":       f.do("POST", "/_simulator/v1/terminal-control", map[string]string{"Authorization": "AWS4-HMAC-SHA256 x", "Content-Type": "application/x-amz-json-1.1"}, `{}`),
	} {
		if w.Code != 403 {
			t.Fatalf("%s: status %d %s", name, w.Code, w.Body.String())
		}
	}
	// A participant whose role cannot describe instances gets no EC2 view.
	f.policy.Statement = core.Statements{{Effect: "Allow", Action: core.StringList{"ssm:StartSession"}, Resource: core.StringList{"*"}}}
	if w := f.describe(nil, "Action=DescribeInstances&Version=2016-11-15"); w.Code != 403 {
		t.Fatal("EC2 view ignored the role policy", w.Code)
	}
}

func TestConsoleInstanceReadinessReflectsGuestRelay(t *testing.T) {
	ready := newInstanceConsole(t, &flowBackend{})
	ready.login()
	w := ready.do("GET", "/_simulator/v1/terminal-readiness", nil, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"Connectable":true`) || !strings.Contains(w.Body.String(), instanceID) {
		t.Fatal("ready relay not reported", w.Code, w.Body.String())
	}
	pending := newInstanceConsole(t, &pendingBackend{})
	pending.login()
	w = pending.do("GET", "/_simulator/v1/terminal-readiness", nil, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"Connectable":false`) {
		t.Fatal("preparing relay reported connectable", w.Code, w.Body.String())
	}
	// The server refuses the start too; the UI state is not the only guard.
	r, _ := http.NewRequest("POST", pending.server.URL+"/_simulator/v1/terminal-control", strings.NewReader(`{"Target":"`+instanceID+`"}`))
	r.Host = "127.0.0.1:4566"
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "AmazonSSM.StartSession")
	r.Header.Set("Origin", "http://127.0.0.1:4566")
	r.AddCookie(pending.cookie)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatal("session started on a preparing guest", resp.StatusCode)
	}
}

func TestConsoleInstanceSessionStartsTerminalWithoutPastedCode(t *testing.T) {
	f := newInstanceConsole(t, &flowBackend{closed: make(chan struct{}, 1)})
	f.login()
	flow := &terminalFlow{t: t, server: f.server, cookie: f.cookie, backend: f.backend()}
	if code, _ := flow.control("StartSession", `{"Target":"i-87654321"}`); code != 403 {
		t.Fatal("console session started another instance", code)
	}
	if code, _ := flow.control("StartSession", `{"Target":"`+instanceID+`","SessionId":"x"}`); code == 200 {
		t.Fatal("start accepted a session id")
	}
	started := startFlowSession(t, flow)
	ws, status := flow.dial(started.SessionID, nil)
	if !strings.Contains(status, "101") {
		t.Fatal("upgrade refused", status)
	}
	if err := ws.sendJSON(map[string]string{"type": "open", "token": started.TokenValue}); err != nil {
		t.Fatal(err)
	}
	if m, err := ws.next(); err != nil || m["type"] != "ready" {
		t.Fatalf("ready %v %v", m, err)
	}
	if err := ws.sendJSON(map[string]string{"type": "input", "data": base64.StdEncoding.EncodeToString([]byte("id\n"))}); err != nil {
		t.Fatal(err)
	}
	ws.expectOutput(t, "echo:id\n")
	if code, _ := flow.control("TerminateSession", `{"SessionId":"`+started.SessionID+`"}`); code != 200 {
		t.Fatal("terminate", code)
	}
}

func (f *instanceConsole) backend() *flowBackend {
	// The fixture's backend is the flowBackend handed to newInstanceConsole.
	return f.flow
}

func TestConsoleInstanceHandoffLeavesParameterHandoffUnchanged(t *testing.T) {
	f := newInstanceConsole(t, &flowBackend{})
	body, _ := json.Marshal(map[string]string{"ParameterName": "/own/value", "Region": "us-east-1"})
	// The legacy shape still needs ssm:GetParameter to be useful, but issuance is unchanged.
	if w := f.issue(string(body)); w.Code != 200 {
		t.Fatal("parameter handoff changed", w.Code, w.Body.String())
	}
}
