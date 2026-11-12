package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

type catalogStep struct{ method, path string }

// catalogFaultSweep runs steps against a catalog engine over a fault-injecting
// database. An injected storage fault must surface as an INTERNAL failure of
// the step it hit, never as a success or as a misleading client error.
func catalogFaultSweep(t *testing.T, steps []catalogStep, bodies []any) {
	t.Helper()
	sweepFaultsMode(t, faultReported, func(t *testing.T, db *sql.DB) func(context.Context) error {
		h := &harness{t: t, now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
		engine, err := New(db, Config{Project: "tenkacloud-local", ProjectNumber: "100000000001", Owner: "owner", Locations: []string{"us-central1", "asia-northeast1"},
			Subject: func(ctx context.Context) (string, bool) { s, ok := ctx.Value(subjectKey{}).(string); return s, ok }, Now: func() time.Time { return h.now }}, APIs())
		if err != nil {
			t.Fatal(err)
		}
		h.engine = engine
		return func(context.Context) error {
			for i, s := range steps {
				r := h.call("owner", s.method, s.path, bodies[i])
				switch {
				case r.status == 200:
				case r.status >= 500:
					return fmt.Errorf("%s %s: %d %s", s.method, s.path, r.status, r.raw)
				default:
					t.Fatalf("storage fault reported as client error by %s %s: %d %s", s.method, s.path, r.status, r.raw)
				}
			}
			return nil
		}
	})
}

func TestCatalogComputeLifecycleReportsStorageFaults(t *testing.T) {
	subnet := map[string]any{"name": "app", "network": "global/networks/vpc", "ipCidrRange": "10.0.0.0/24"}
	network := map[string]any{"name": "vpc", "autoCreateSubnetworks": false}
	catalogFaultSweep(t, []catalogStep{
		{"POST", ce + "/global/networks?requestId=6d5c84d6-7a8e-4d1d-9c33-2f0f6a5b9e01"},
		{"POST", ce + "/global/networks?requestId=6d5c84d6-7a8e-4d1d-9c33-2f0f6a5b9e01"},
		{"PATCH", ce + "/global/networks/vpc"},
		{"POST", ce + "/regions/us-central1/subnetworks"},
		{"GET", ce + "/regions/us-central1/subnetworks"},
		{"GET", ce + "/global/operations"},
		{"DELETE", ce + "/regions/us-central1/subnetworks/app"},
		{"DELETE", ce + "/global/networks/vpc"},
	}, []any{network, network, map[string]any{"name": "vpc", "routingConfig": map[string]any{"routingMode": "GLOBAL"}}, subnet, nil, nil, nil, nil})
}

func TestCatalogLongRunningLifecycleReportsStorageFaults(t *testing.T) {
	catalogFaultSweep(t, []catalogStep{
		{"POST", loc + "/clusters?clusterId=main&requestId=r1"},
		{"POST", loc + "/clusters/main/instances?instanceId=primary"},
		{"POST", loc + "/clusters/main/users?userId=app"},
		{"PATCH", loc + "/clusters/main?updateMask=displayName"},
		{"GET", loc + "/clusters/main/instances?pageSize=1"},
		{"DELETE", loc + "/clusters/main?force=true"},
	}, []any{
		map[string]any{"networkConfig": map[string]any{"network": "projects/tenkacloud-local/global/networks/default"}},
		map[string]any{"instanceType": "PRIMARY"},
		map[string]any{"databaseRoles": []any{"alloydbsuperuser"}},
		map[string]any{"displayName": "Main"},
		nil, nil,
	})
}
