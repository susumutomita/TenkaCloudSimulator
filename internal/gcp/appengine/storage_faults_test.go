package appengine

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Construction, deployment, traffic, firewall and deletion report every
// injected storage fault as an opaque INTERNAL failure.
func TestAppEngineAdminReportsEveryStorageFault(t *testing.T) {
	sweepFaultsMode(t, faultReported, func(t *testing.T, db *sql.DB) func(context.Context) error {
		h := &harness{t: t, db: db, objects: map[string][]byte{}, now: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), runner: &fakeRunner{}}
		h.put("src/default-v1.zip", zipOf(t, map[string]string{"main.py": app}))
		h.put("src/default-v2.zip", zipOf(t, map[string]string{"main.py": app}))
		split := base + "/services/default?updateMask=split"
		rules := base + "/firewall/ingressRules"
		zip := func(id string) map[string]any {
			return map[string]any{"id": id, "runtime": "python312", "deployment": map[string]any{"zip": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/default-" + id + ".zip"}}}
		}
		return func(context.Context) error {
			engine, err := New(db, h.config())
			if err != nil {
				return err
			}
			t.Cleanup(func() { _ = engine.Close() })
			h.engine = engine
			for _, step := range []struct {
				method, path string
				body         any
			}{
				{"POST", "/appengine/v1/apps", map[string]any{"id": project, "locationId": "us-central"}},
				{"PATCH", base + "?updateMask=servingStatus", map[string]any{"servingStatus": "SERVING"}},
				{"POST", base + "/services/default/versions", zip("v1")},
				{"POST", base + "/services/default/versions", zip("v2")},
				{"GET", base + "/services/default/versions?pageSize=1", nil},
				{"PATCH", split, map[string]any{"split": map[string]any{"shardBy": "RANDOM", "allocations": map[string]any{"v1": 0.25, "v2": 0.75}}}},
				{"PATCH", split, map[string]any{"split": map[string]any{"allocations": map[string]any{"v1": 1}}}},
				{"DELETE", base + "/services/default/versions/v2", nil},
				{"POST", rules, map[string]any{"priority": 100, "action": "DENY", "sourceRange": "192.0.2.0/24"}},
				{"GET", rules + "?matchingAddress=192.0.2.7", nil},
				{"DELETE", rules + "/100", nil},
				{"GET", base + "/operations?pageSize=2", nil},
			} {
				r := h.call("owner", step.method, step.path, step.body)
				switch {
				case r.status == 200:
				case r.status == 500:
					if strings.Contains(r.raw, errInjectedStorage.Error()) {
						t.Fatalf("driver detail leaked: %s", r.raw)
					}
					return fmt.Errorf("%s %s: %s", step.method, step.path, r.raw)
				default:
					t.Fatalf("storage fault misreported by %s %s: %d %s", step.method, step.path, r.status, r.raw)
				}
			}
			return nil
		}
	})
}
