package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/codestarnotifications"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

type ruleWire struct {
	f       *branchNotifyFixture
	gateway *Gateway
}

func (w ruleWire) post(actor, method, path string, body []byte, change func(*http.Request), status int, code string) map[string]any {
	t := w.f.t
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Simulator-Service", notificationService)
	if actor != "" {
		role := provider.CredentialParticipant
		if actor == core.Administrator {
			role = provider.CredentialOperator
		}
		request = request.WithContext(provider.WithActor(request.Context(), provider.Actor{
			CredentialRole: role, ExerciseIdentity: actor, CredentialIdentity: actor}))
	}
	if change != nil {
		change(request)
	}
	res := httptest.NewRecorder()
	w.gateway.ServeHTTP(res, request)
	var result map[string]any
	if res.Code != status || json.Unmarshal(res.Body.Bytes(), &result) != nil || code != "" && (result["__type"] != code || res.Header().Get("X-Amzn-Errortype") != code) {
		t.Fatalf("%s %s received %d %q, want %d/%s", method, path, res.Code, res.Body.String(), status, code)
	}
	return result
}

func (w ruleWire) op(op string, in map[string]any, status int, code string) map[string]any {
	w.f.t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		w.f.t.Fatal(err)
	}
	return w.post(core.Administrator, "POST", "/"+strings.ToLower(op[:1])+op[1:], b, nil, status, code)
}

func TestNotificationRESTInputsAndAuthority(t *testing.T) {
	f := branchFixture(t, filepath.Join(t.TempDir(), "rules.db"))
	f.createResources()
	w := ruleWire{f, &Gateway{Notifications: f.rules, Identities: core.NewIdentities(f.team)}}
	path := "/listNotificationRules"
	empty := []byte("{}")
	before := f.counts()
	const bad = "ValidationException"
	w.post("", "POST", path, empty, nil, 403, "AccessDeniedException")
	w.post("reader", "POST", path, empty, nil, 403, "AccessDeniedException")
	w.post(core.Administrator, "GET", path, empty, nil, 400, bad)
	w.post(core.Administrator, "POST", path+"?count=2", empty, nil, 400, bad)
	w.post(core.Administrator, "POST", "/listEventTypes", empty, nil, 501, "UnknownOperationException")
	w.post(core.Administrator, "POST", path, []byte(`{"Filters":[1]}`), nil, 400, bad)
	w.post(core.Administrator, "POST", path, bytes.Repeat([]byte(" "), 65537), nil, 400, bad)
	w.post(core.Administrator, "POST", path, empty, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 400, bad)
	w.post(core.Administrator, "POST", path, empty, func(r *http.Request) { r.Header.Add("X-Simulator-Service", notificationService) }, 400, bad)
	w.post(core.Administrator, "POST", path, empty, func(r *http.Request) { r.Header.Set("X-Amz-Target", "CodeCommit_20150413.ListBranches") }, 400, bad)
	w.post(core.Administrator, "POST", path, empty, func(r *http.Request) { r.Header.Set("X-Amz-Security-Token", "temporary-local-session") }, 403, "AccessDeniedException")
	w.post(core.Administrator, "POST", path, empty, func(r *http.Request) { r.Header.Set("X-Simulator-Region", "invalid-region") }, 400, bad)
	w.post(core.Administrator, "POST", path, empty, func(r *http.Request) { r.Header.Set("Authorization", "unsupported") }, 403, "AccessDeniedException")
	signed := func(r *http.Request) {
		r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261128/us-east-1/codestar-notifications/aws4_request, SignedHeaders=host, Signature=local")
	}
	w.post(core.Administrator, "POST", path, empty, func(r *http.Request) { signed(r); r.Header.Set("X-Simulator-Region", "us-west-2") }, 403, "AccessDeniedException")
	result := w.post(core.Administrator, "POST", path, empty, func(r *http.Request) { signed(r); r.Header.Del("X-Simulator-Service") }, 200, "")
	if len(result["NotificationRules"].([]any)) != 1 {
		t.Fatal(result)
	}
	noStore := ruleWire{f, &Gateway{Identities: w.gateway.Identities}}
	noStore.post(core.Administrator, "POST", path, empty, nil, 500, "InternalServerException")
	if !slices.Equal(before, f.counts()) {
		t.Fatal("invalid requests changed Git or notification data", f.counts())
	}
}

func TestNotificationRegistrationBudgetsAndFilters(t *testing.T) {
	f := branchFixture(t, filepath.Join(t.TempDir(), "registration.db"))
	f.createResources()
	w := ruleWire{f, &Gateway{Notifications: f.rules, Identities: core.NewIdentities(f.team)}}
	const create = "CreateNotificationRule"
	const list = "ListNotificationRules"
	const validation = "ValidationException"
	input := func() map[string]any {
		return map[string]any{"Name": "Additional", "DetailType": "BASIC", "Resource": f.source, "Targets": []any{}, "EventTypeIds": []string{codestarnotifications.BranchCreated}}
	}
	for _, tc := range []struct {
		field string
		value any
		code  string
	}{
		{"Name", "", validation}, {"Name", strings.Repeat("A", 65), validation}, {"Name", "Branches", "ResourceAlreadyExistsException"},
		{"Status", "ACTIVE", validation}, {"DetailType", "FULL", validation}, {"EventTypeIds", []string{}, validation},
		{"EventTypeIds", []string{"other"}, validation}, {"EventTypeIds", []string{codestarnotifications.BranchCreated, codestarnotifications.BranchCreated}, validation},
		{"ClientRequestToken", strings.Repeat("t", 257), validation}, {"Resource", "arn:aws:codecommit:us-west-2:000000000000:exercises", validation},
		{"Resource", "arn:aws:codecommit:us-east-1:000000000000:missing", "ConfigurationException"},
		{"Targets", []any{map[string]string{"TargetType": "OTHER", "TargetAddress": f.topic.ARN}}, validation},
		{"Targets", []any{map[string]string{"TargetType": "SNS", "TargetAddress": "arn:aws:sns:us-west-2:000000000000:other"}}, validation},
		{"Targets", []any{map[string]string{"TargetType": "SNS", "TargetAddress": "arn:aws:sns:us-east-1:000000000000:absent"}}, "ConfigurationException"},
	} {
		in := input()
		in[tc.field] = tc.value
		status := 400
		if tc.code == "ResourceAlreadyExistsException" {
			status = 409
		}
		w.op(create, in, status, tc.code)
	}
	for _, key := range []string{"Name", "Resource", "EventTypeIds", "Targets", "DetailType"} {
		in := input()
		delete(in, key)
		w.op(create, in, 400, validation)
	}
	target := map[string]string{"TargetType": "SNS", "TargetAddress": f.topic.ARN}
	in := input()
	in["Targets"] = []any{target, target}
	w.op(create, in, 400, validation)
	many := make([]any, 11)
	for i := range many {
		many[i] = target
	}
	in["Targets"] = many
	w.op(create, in, 400, validation)
	f.policy("Deny")
	in["Targets"] = []any{target}
	w.op(create, in, 400, "ConfigurationException")
	f.policy("Allow")
	if f.count("aws_notification_rules") != 1 || f.count("aws_notification_targets") != 1 {
		t.Fatal("partial rejected registration")
	}
	first := w.op(create, input(), 200, "")["Arn"].(string)
	w.op("UpdateNotificationRule", map[string]any{"Arn": first, "Name": "Branches"}, 400, validation)
	w.op("UpdateNotificationRule", map[string]any{"Arn": first, "Status": "invalid"}, 400, validation)
	w.op("DescribeNotificationRule", map[string]any{"Arn": "unscoped"}, 400, validation)
	w.op("DeleteNotificationRule", map[string]any{"Arn": strings.TrimSuffix(first, first[len(first)-40:]) + strings.Repeat("0", 40)}, 400, validation)
	for i := 2; i < 10; i++ {
		in := input()
		in["Name"] = fmt.Sprintf("Rule%d", i)
		w.op(create, in, 200, "")
	}
	overflow := input()
	overflow["Name"] = "Overflow"
	w.op(create, overflow, 400, "LimitExceededException")
	if f.count("aws_notification_rules") != 10 {
		t.Fatal("wrong repository quota")
	}
	w.op(list, map[string]any{"MaxResults": 0}, 400, validation)
	w.op(list, map[string]any{"MaxResults": 101}, 400, validation)
	for _, filter := range []map[string]string{{"Name": "OTHER", "Value": "sample"}, {"Name": "RESOURCE", "Value": ""}} {
		w.op(list, map[string]any{"Filters": []any{filter}}, 400, validation)
	}
	filters := make([]map[string]string, 11)
	for i := range filters {
		filters[i] = map[string]string{"Name": "RESOURCE", "Value": f.source}
	}
	w.op(list, map[string]any{"Filters": filters}, 400, validation)
	if rows := w.op(list, map[string]any{"Filters": []any{map[string]string{"Name": "EVENT_TYPE_ID", "Value": "unknown"}}}, 200, "")["NotificationRules"].([]any); len(rows) != 0 {
		t.Fatal(rows)
	}
	page := w.op(list, map[string]any{"MaxResults": 1}, 200, "")
	token := page["NextToken"].(string)
	w.op(list, map[string]any{"NextToken": "unusable cursor"}, 400, "InvalidNextTokenException")
	w.op(list, map[string]any{"NextToken": token, "Filters": []any{map[string]string{"Name": "RESOURCE", "Value": f.source}}}, 400, "InvalidNextTokenException")
	reopen, err := NewNotificationRules(f.team.Database())
	if err != nil {
		t.Fatal(err)
	}
	_, err = reopen.Run(t.Context(), list, f.region, core.Administrator, codestarnotifications.Input{NextToken: token}, func(string, string) string { return "allowed" })
	var fault codestarnotifications.Fault
	if !errors.As(err, &fault) || fault.Code != "InvalidNextTokenException" {
		t.Fatal(err)
	}
	w.op("DeleteNotificationRule", map[string]any{"Arn": first}, 200, "")
	w.op(create, overflow, 200, "")
}

func TestNotificationWorkerResumesActualDeniedDelivery(t *testing.T) {
	f := branchFixture(t, filepath.Join(t.TempDir(), "worker.db"))
	f.createResources()
	f.policy("Deny")
	f.put("queued", "", 200)
	g := &Gateway{Notifications: f.rules}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); g.DeliverNotifications(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(6 * time.Second)
	var retry int64
	for retry == 0 && time.Now().Before(deadline) {
		if err := f.team.Database().QueryRow("SELECT retry_ms FROM aws_notification_outbox").Scan(&retry); err != nil {
			t.Fatal(err)
		}
		if retry == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if retry == 0 || f.count("aws_sns_messages") != 0 {
		t.Fatal("no durable retry", retry, f.counts())
	}
	if status := f.ruleCall("DescribeNotificationRule", codestarnotifications.Input{ARN: f.rule})["Targets"].([]map[string]string)[0]["TargetStatus"]; status != "UNREACHABLE" {
		t.Fatal(status)
	}
	f.policy("Allow")
	for f.count("aws_notification_outbox") != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if f.count("aws_notification_deliveries") != 1 || f.count("aws_sqs_messages") != 2 || f.count("aws_sns_messages") != 1 {
		t.Fatal("worker did not finish once", f.counts())
	}
}
