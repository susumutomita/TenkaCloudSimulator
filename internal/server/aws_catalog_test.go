package server

import (
	"encoding/json"
	"testing"
)

// The capability document stays bounded: catalog services are summarized
// and their full native descriptors are served by their own endpoint.
func TestCatalogDescriptorsAreServedApartFromCapabilities(t *testing.T) {
	s := instance(t)
	raw := status(t, s, "GET", "/_simulator/v1/capabilities", "", 200, nil)
	if len(raw) > 128<<10 {
		t.Fatalf("capabilities grew to %d bytes; keep descriptors on %s", len(raw), catalogDescriptorsPath)
	}
	var capabilities struct {
		Services struct {
			Catalog struct {
				Descriptors string           `json:"descriptors"`
				Services    []map[string]any `json:"services"`
			} `json:"catalog"`
		} `json:"services"`
	}
	if err := json.Unmarshal(raw, &capabilities); err != nil {
		t.Fatal(err)
	}
	summary := capabilities.Services.Catalog
	if summary.Descriptors != catalogDescriptorsPath || len(summary.Services) == 0 {
		t.Fatalf("summary %+v", summary)
	}
	var full struct {
		Services []struct {
			Model      string   `json:"model"`
			Operations []string `json:"operations"`
		} `json:"services"`
	}
	if err := json.Unmarshal(status(t, s, "GET", catalogDescriptorsPath, "", 200, nil), &full); err != nil {
		t.Fatal(err)
	}
	if len(full.Services) != len(summary.Services) {
		t.Fatalf("descriptors %d, summaries %d", len(full.Services), len(summary.Services))
	}
	for i, d := range full.Services {
		if d.Model != summary.Services[i]["model"] || len(d.Operations) == 0 || summary.Services[i]["operations"] != float64(len(d.Operations)) {
			t.Fatalf("descriptor %s does not match its summary %v", d.Model, summary.Services[i])
		}
	}
	status(t, s, "POST", catalogDescriptorsPath, "", 405, nil)
}
