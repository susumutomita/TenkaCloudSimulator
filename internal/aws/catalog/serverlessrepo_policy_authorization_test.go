package catalog

import (
	"context"
	"reflect"
	"testing"
)

func TestServerlessRepositoryPolicyUsesApplicationAuthorization(t *testing.T) {
	h := newHarness(t, serverlessRepo)
	created := h.rest("serverlessrepo", "POST", "/applications", map[string]any{
		"name": "own-config", "author": "local-owner", "description": "saved local fixture",
	})
	expect(t, created, 201, "")
	const application = "arn:aws:serverlessrepo:us-east-1:000000000000:applications/own-config"
	if got := str(created.body["applicationId"]); got != application {
		t.Fatalf("fixture application ID: got %q", got)
	}
	target := "/applications/" + compute3EscapeARN(application) + "/policy"
	const get = "serverlessrepo:GetApplicationPolicy"
	const put = "serverlessrepo:PutApplicationPolicy"
	type evaluation struct{ action, resource string }
	var seen []evaluation
	grants := map[string]bool{get: true, put: true}
	h.engine.cfg.Authorize = func(_ context.Context, _, action, resource string) (string, error) {
		seen = append(seen, evaluation{action, resource})
		if resource == application && grants[action] {
			return "allowed", nil
		}
		return "denied", nil
	}
	call := func(method, action string, body any, status int, code string) reply {
		t.Helper()
		seen = nil
		r := h.rest("serverlessrepo", method, target, body)
		expect(t, r, status, code)
		if want := []evaluation{{action, application}}; !reflect.DeepEqual(seen, want) {
			t.Fatalf("IAM evaluations: got %v, want %v", seen, want)
		}
		return r
	}
	statements := func(label string) map[string]any {
		return map[string]any{"statements": []any{map[string]any{
			"actions": []any{"Deploy"}, "principals": []any{"111111111111"}, "statementId": label,
		}}}
	}
	call("GET", get, nil, 404, "NotFoundException")
	call("PUT", put, statements("initial"), 200, "")
	replaced := call("PUT", put, statements("replacement"), 200, "").body
	if !reflect.DeepEqual(replaced, statements("replacement")) {
		t.Fatalf("policy representation changed: %v", replaced)
	}
	grants[put] = false
	call("PUT", put, statements("must-not-persist"), 403, "AccessDeniedException")
	grants[get] = false
	call("GET", get, nil, 403, "AccessDeniedException")
	grants[get] = true
	if got := call("GET", get, nil, 200, "").body; !reflect.DeepEqual(got, replaced) {
		t.Fatalf("refused write changed stored policy: got %v, want %v", got, replaced)
	}
}
