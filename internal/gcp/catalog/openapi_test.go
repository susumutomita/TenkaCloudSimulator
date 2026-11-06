package catalog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const docsDirectory = "../../server/apidocs/gcp-catalog"

// The served API reference must be regenerated from the embedded discovery subsets.
func TestServedReferenceMatchesEmbeddedDiscoveryDocuments(t *testing.T) {
	h := newHarness(t)
	documents := h.engine.OpenAPI()
	index := []map[string]any{}
	for _, d := range h.engine.Describe() {
		index = append(index, map[string]any{"model": d.Name + "." + d.Version, "title": d.Title, "methods": len(d.Methods), "official": d.Official})
	}
	want := map[string][]byte{}
	for model, document := range documents {
		data, err := json.MarshalIndent(document, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		want[model+".json"] = append(data, '\n')
	}
	data, _ := json.MarshalIndent(index, "", " ")
	want["index.json"] = append(data, '\n')
	if os.Getenv("CATALOG_WRITE_DOCS") == "1" {
		_ = os.RemoveAll(docsDirectory)
		if err := os.MkdirAll(docsDirectory, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, data := range want {
			if err := os.WriteFile(filepath.Join(docsDirectory, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	entries, err := os.ReadDir(docsDirectory)
	if err != nil || len(entries) != len(want) {
		t.Fatalf("served reference is stale; run CATALOG_WRITE_DOCS=1 go test ./internal/gcp/catalog -run TestServedReference (%v)", err)
	}
	for name, data := range want {
		got, err := os.ReadFile(filepath.Join(docsDirectory, name))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%s is stale; regenerate the catalog reference", name)
		}
	}
	for model, document := range documents {
		if document["openapi"] != "3.1.0" || len(document["paths"].(map[string]any)) == 0 {
			t.Fatalf("%s reference incomplete", model)
		}
	}
}

// The embedded discovery data must ship with its original license.
func TestDiscoveryNoticesAreMirroredForRedistribution(t *testing.T) {
	original, err := os.ReadFile("../../../third_party/GOOGLE-DISCOVERY-NOTICES.txt")
	if err != nil {
		t.Fatal(err)
	}
	served, err := os.ReadFile("../../legal/assets/GOOGLE-DISCOVERY-NOTICES.txt")
	if err != nil || !bytes.Equal(original, served) {
		t.Fatal("the served discovery notices must equal third_party/GOOGLE-DISCOVERY-NOTICES.txt")
	}
	for _, needle := range []string{"Apache License", "google-api-python-client 2.199.0", "internal/gcp/catalog/models"} {
		if !bytes.Contains(original, []byte(needle)) {
			t.Fatalf("notice lacks %q", needle)
		}
	}
}
