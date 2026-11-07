package server

import (
	"encoding/json"
	"testing"

	gcpcatalog "github.com/susumutomita/TenkaCloudSimulator/internal/gcp/catalog"
)

// The capability document stays bounded with every reviewed Google API
// registered: APIs are summarized there and their full descriptors are
// served by their own endpoint.
func TestGCPCatalogDescriptorsAreServedApartFromCapabilities(t *testing.T) {
	s := instance(t)
	raw := status(t, s, "GET", "/_simulator/v1/capabilities", "", 200, nil)
	if len(raw) > 128<<10 {
		t.Fatalf("capabilities grew to %d bytes; keep descriptors on %s", len(raw), gcpCatalogDescriptorsPath)
	}
	var capabilities struct {
		GCPCatalog struct {
			Descriptors string           `json:"descriptors"`
			Zones       []string         `json:"zones"`
			APIs        []map[string]any `json:"apis"`
		} `json:"gcpCatalog"`
	}
	if err := json.Unmarshal(raw, &capabilities); err != nil {
		t.Fatal(err)
	}
	summary := capabilities.GCPCatalog
	if summary.Descriptors != gcpCatalogDescriptorsPath || len(summary.APIs) != len(gcpcatalog.APIs()) || len(summary.Zones) == 0 {
		t.Fatalf("summary %+v", summary)
	}
	var full struct {
		APIs []gcpcatalog.Descriptor `json:"apis"`
	}
	if err := json.Unmarshal(status(t, s, "GET", gcpCatalogDescriptorsPath, "", 200, nil), &full); err != nil {
		t.Fatal(err)
	}
	if len(full.APIs) != len(summary.APIs) {
		t.Fatalf("descriptors %d, summaries %d", len(full.APIs), len(summary.APIs))
	}
	for i, d := range full.APIs {
		s := summary.APIs[i]
		if d.Name != s["name"] || d.Version != s["version"] || d.Endpoint != s["endpoint"] || len(d.Collections) == 0 || s["methods"] != float64(len(d.Methods)) {
			t.Fatalf("descriptor %s does not match its summary %v", d.Name, s)
		}
		if _, ok := s["collections"]; ok {
			t.Fatalf("summary %s carries full descriptors", d.Name)
		}
	}
	status(t, s, "POST", gcpCatalogDescriptorsPath, "", 405, nil)
}
