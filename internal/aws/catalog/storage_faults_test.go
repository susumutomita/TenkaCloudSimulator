package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

type catalogStep struct {
	service, method, path string // path empty: JSON operation named by method
	body                  map[string]any
}

// catalogFaultSweep runs steps against the AWS catalog engine over a
// fault-injecting database. An injected storage fault must surface as
// InternalFailure from the step it hit, never as success or a client error.
// Steps may reference "$ARN" and "$APP", filled from earlier responses.
func catalogFaultSweep(t *testing.T, steps []catalogStep) {
	t.Helper()
	sweepFaultsMode(t, faultReported, func(t *testing.T, db *sql.DB) func(context.Context) error {
		h := &harness{t: t, db: db, policy: &policy{deny: map[string]bool{}}, now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
		engine, err := New(db, Config{Regions: []string{"us-east-1", "ap-northeast-1"}, Account: "000000000000", DefaultPrincipal: "simulator-admin", Authorize: h.policy.authorize, Now: func() time.Time { return h.now }}, Services())
		if err != nil {
			t.Fatal(err)
		}
		h.engine = engine
		return func(context.Context) error {
			vars := map[string]string{}
			fill := func(s string) string {
				for k, v := range vars {
					s = strings.ReplaceAll(s, k, v)
				}
				return s
			}
			for _, s := range steps {
				body := map[string]any{}
				for k, v := range s.body {
					if text, ok := v.(string); ok {
						v = fill(text)
					}
					body[k] = v
				}
				var r reply
				if s.path == "" {
					r = h.json1(s.service, s.method, body)
				} else {
					r = h.rest(s.service, s.method, fill(s.path), body)
				}
				switch {
				case r.status == 200:
				case r.status == 500 && strings.HasPrefix(r.code(), "InternalFailure"):
					return fmt.Errorf("%s %s: %d %s", s.method, s.path, r.status, r.raw)
				default:
					t.Fatalf("storage fault misreported by %s %s: %d %q %s", s.method, s.path, r.status, r.code(), r.raw)
				}
				if arn, ok := r.body["ConnectionArn"].(string); ok {
					vars["$ARN"] = arn
				}
				if app, ok := r.body["app"].(map[string]any); ok {
					if id, ok := app["appId"].(string); ok {
						vars["$APP"] = id
					}
				}
			}
			return nil
		}
	})
}

func TestCatalogJSONLifecycleReportsStorageFaults(t *testing.T) {
	catalogFaultSweep(t, []catalogStep{
		{"codeconnections", "CreateConnection", "", map[string]any{"ConnectionName": "demo", "ProviderType": "GitHub", "Tags": []any{map[string]any{"Key": "team", "Value": "a"}}}},
		{"codeconnections", "GetConnection", "", map[string]any{"ConnectionArn": "$ARN"}},
		{"codeconnections", "ListConnections", "", map[string]any{}},
		{"codeconnections", "TagResource", "", map[string]any{"ResourceArn": "$ARN", "Tags": []any{map[string]any{"Key": "x", "Value": "y"}}}},
		{"codeconnections", "UntagResource", "", map[string]any{"ResourceArn": "$ARN", "TagKeys": []any{"team"}}},
		{"codeconnections", "ListTagsForResource", "", map[string]any{"ResourceArn": "$ARN"}},
		{"codeconnections", "DeleteConnection", "", map[string]any{"ConnectionArn": "$ARN"}},
	})
}

func TestCatalogRESTLifecycleReportsStorageFaults(t *testing.T) {
	catalogFaultSweep(t, []catalogStep{
		{"amplify", "POST", "/apps", map[string]any{"name": "site", "tags": map[string]any{"a": "b"}}},
		{"amplify", "POST", "/apps/$APP/branches", map[string]any{"branchName": "main", "stage": "PRODUCTION"}},
		{"amplify", "GET", "/apps/$APP/branches?maxResults=10", nil},
		{"amplify", "POST", "/apps/$APP", map[string]any{"description": "updated"}},
		{"amplify", "DELETE", "/apps/$APP/branches/main", nil},
		{"amplify", "DELETE", "/apps/$APP", nil},
	})
}
