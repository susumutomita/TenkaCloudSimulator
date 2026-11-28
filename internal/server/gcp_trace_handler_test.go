package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

func TestGCPTraceUsesEachConfiguredProjectAndTrustedOwner(t *testing.T) {
	const root = "/_simulator/providers/gcp/cloudtrace/v1/projects/"
	const id = "1234567890abcdef1234567890abcdef"
	first := dnsHandlerStart(t, "project-one", "a")
	second := dnsHandlerStart(t, "project-two", "b")
	oneOwner, twoOwner := first.mint(t, "learner"), second.mint(t, "learner")
	outsider := first.mint(t, "other")
	one, two := root+first.project+"/traces", root+second.project+"/traces"
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
	row := func(project string) map[string]any {
		return map[string]any{"projectId": project, "traceId": id, "spans": []any{
			map[string]any{"spanId": "9007199254740993", "name": project, "startTime": now, "endTime": now},
		}}
	}
	body := func(project string) any { return map[string]any{"traces": []any{row(project)}} }

	first.call(t, http.MethodPatch, one, oneOwner, "", body(first.project), 200)
	second.call(t, http.MethodGet, two+"/"+id, twoOwner, "", nil, 404)
	second.call(t, http.MethodPatch, two, twoOwner, "", body(second.project), 200)
	first.call(t, http.MethodGet, two+"/"+id, oneOwner, "", nil, 403)
	second.call(t, http.MethodPatch, one, twoOwner, "", body(first.project), 403)
	first.call(t, http.MethodPatch, one, twoOwner, "", body(first.project), 401)
	first.call(t, http.MethodPatch, one, outsider, "learner", body(first.project), 403)
	first.call(t, http.MethodPatch, one, oneOwner, "", body(second.project), 400)

	for _, check := range []struct {
		host            dnsHandlerHost
		endpoint, token string
	}{{first, one, oneOwner}, {second, two, twoOwner}} {
		got := check.host.call(t, http.MethodGet, check.endpoint+"/"+id, check.token, "", nil, 200)
		want := row(check.host.project)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("wrong project or span: %v", got)
		}
	}
	const caps = "/_simulator/v1/capabilities"
	for _, check := range []struct {
		token, selected string
		visible         bool
	}{
		{oneOwner, "", true}, {first.token, "learner", true}, {first.token, "", false}, {outsider, "", false}, {outsider, "learner", false},
	} {
		got := first.call(t, http.MethodGet, caps, check.token, check.selected, nil, 200)
		value, visible := got["gcpTrace"]
		if visible != check.visible {
			t.Fatalf("wrong Trace capability visibility: %v", visible)
		}
		if visible && value.(map[string]any)["project"] != first.project {
			t.Fatal(value)
		}
	}
	req := httptest.NewRequest(http.MethodGet, caps, nil)
	req.Header.Set("X-Simulator-Principal", "learner")
	cfg := CloudStorageConfig{GCPProject: first.project, GCPOwner: "learner"}
	trusted := provider.WithActor(req.Context(), provider.Actor{CredentialIdentity: "learner", ExerciseIdentity: "learner", CredentialRole: provider.CredentialParticipant})
	for _, ctx := range []context.Context{req.Context(), context.WithValue(trusted, consoleScopeKey{}, consoleScope{ParameterName: "/example"})} {
		values := map[string]any{}
		addGCPTraceCapability(req.WithContext(ctx), values, cfg)
		if _, ok := values["gcpTrace"]; ok {
			t.Fatal("ineligible credential received a project capability")
		}
	}
}
