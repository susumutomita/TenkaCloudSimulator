package arm

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

type armStep struct {
	method, path string
	body         any
}

// armFaultSweep runs steps against an ARM engine over a fault-injecting
// database. An injected storage fault must surface as a server failure of the
// step it hit, never as success or as a misleading client error.
func armFaultSweep(t *testing.T, steps []armStep) {
	t.Helper()
	sweepFaultsMode(t, faultReported, func(t *testing.T, db *sql.DB) func(context.Context) error {
		h := &harness{t: t, now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
		engine, err := New(db, testConfig(h), Providers())
		if err != nil {
			t.Fatal(err)
		}
		h.engine = engine
		return func(context.Context) error {
			for _, s := range steps {
				r := h.do(s.method, s.path, s.body)
				switch {
				case r.status == 200 || r.status == 201 || r.status == 204:
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

func TestARMLifecycleReportsStorageFaults(t *testing.T) {
	groups := "/subscriptions/" + sub + "/resourcegroups"
	vault := rg + "/providers/Microsoft.KeyVault/vaults/vault-a"
	armFaultSweep(t, []armStep{
		{"PUT", groups + "/rg1" + v("2025-04-01"), map[string]any{"location": "eastus"}},
		{"PUT", groups + "/rg2" + v("2025-04-01"), map[string]any{"location": "eastus"}},
		{"PATCH", groups + "/rg1" + v("2025-04-01"), map[string]any{"tags": map[string]any{"a": "b"}}},
		{"PUT", vnetPath + netAPI, map[string]any{"location": "eastus", "properties": map[string]any{"addressSpace": map[string]any{"addressPrefixes": []any{"10.0.0.0/16"}}}}},
		{"PUT", vnetPath + "/subnets/kv" + netAPI, map[string]any{"properties": map[string]any{"addressPrefix": "10.0.1.0/24"}}},
		{"PUT", vault + kvAPI, map[string]any{"location": "eastus", "properties": map[string]any{"tenantId": tenant, "sku": map[string]any{"family": "A", "name": "standard"},
			"networkAcls": map[string]any{"virtualNetworkRules": []any{map[string]any{"id": vnetPath + "/subnets/kv"}}}}}},
		{"PATCH", vault + kvAPI, map[string]any{"tags": map[string]any{"k": "v"}}},
		{"GET", rg + "/providers/Microsoft.KeyVault/vaults" + kvAPI, nil},
		{"GET", "/subscriptions/" + sub + "/providers/Microsoft.KeyVault/vaults" + kvAPI, nil},
		{"GET", groups + v("2025-04-01") + "&$top=1", nil},
		{"DELETE", vnetPath + "/subnets/kv" + netAPI, nil},
		{"DELETE", vault + kvAPI, nil},
		{"DELETE", groups + "/rg1" + v("2025-04-01"), nil},
	})
}
