package dns

import (
	"path/filepath"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// This is the Cloud DNS Changes API, not a DNS resolver or SRV load balancer.
func TestCloudDNSSRVServiceEndpointLifecycle(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "srv.db")
	open := func() (*core.Store, factoryDNS) {
		t.Helper()
		store, err := core.New(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		return store, factoryEngine(t, store.Database(), "project-one")
	}
	store, f := open()
	t.Cleanup(func() { _ = store.Close() })
	f.create("services")
	base := f.root() + "/services"
	name := "_submission._tcp.example.test."
	path := base + "/rrsets/" + name + "/SRV"
	data := func(values ...string) map[string]any {
		return map[string]any{"kind": "dns#resourceRecordSet", "name": name, "type": "SRV", "ttl": 300, "rrdatas": values}
	}
	old := data("0 1 587 mail.example.test.", "5 2 587 backup.example.test.")
	add := f.call("POST", base+"/changes", factoryChange([]any{old}, nil), 200)
	factoryEqual(t, add["additions"], []any{old})
	factoryEqual(t, f.call("GET", path, nil, 200), old)
	factoryEqual(t, factoryItems(t, f.call("GET", base+"/rrsets?name="+name+"&type=SRV", nil, 200), "rrsets"), []any{old})

	// RFC 2782: a sole "." target advertises that the service is unavailable.
	unavailable := data("0 0 0 .")
	replace := f.call("POST", base+"/changes", factoryChange([]any{unavailable}, []any{old}), 200)
	factoryEqual(t, replace["deletions"], []any{old})
	factoryEqual(t, replace["additions"], []any{unavailable})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, f = open()
	factoryEqual(t, f.call("GET", path, nil, 200), unavailable)
	factoryEqual(t, factoryItems(t, f.call("GET", base+"/changes", nil, 200), "changes"), []any{add, replace})

	// A stale delete must leave the replacement and its change history intact.
	before := f.snapshot("services")
	f.call("POST", base+"/changes", factoryChange(nil, []any{old}), 412)
	if f.snapshot("services") != before {
		t.Fatal("stale SRV deletion changed record or history")
	}
	f.call("POST", base+"/changes", factoryChange(nil, []any{unavailable}), 200)
	f.call("GET", path, nil, 404)
	f.call("DELETE", base, nil, 200)
	f.call("GET", base, nil, 404)
}

func TestCloudDNSSRVBoundedRdata(t *testing.T) {
	tests := []struct {
		rdata string
		valid bool
	}{
		{"65535 65535 65535 host.example.", true},
		{"0 0 0 .", true},
		{"65536 0 80 host.example.", false},
		{"0 65536 80 host.example.", false},
		{"0 0 65536 host.example.", false},
		{"-1 0 80 host.example.", false},
		{"01 0 80 host.example.", false},
		{"0 0 80", false},
		{"0 0 80  host.example.", false},
		{"0 0 80 relative", false},
		{"0 0 80 invalid..example.", false},
		{"0 0 80 host.example. extra", false},
	}
	for _, tc := range tests {
		t.Run(tc.rdata, func(t *testing.T) {
			r := record{Name: "_web._tcp.example.", Type: "SRV", TTL: seconds(60), Data: []string{tc.rdata}}
			if err := r.validate(); (err == nil) != tc.valid {
				t.Errorf("valid = %t; err = %v", tc.valid, err)
			}
		})
	}
}
