package catalog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const docsDirectory = "../../server/apidocs/catalog"

// The served API reference must be regenerated from the embedded models.
func TestServedReferenceMatchesEmbeddedOfficialModels(t *testing.T) {
	h := newHarness(t)
	documents := h.engine.OpenAPI()
	index := []map[string]any{}
	for _, d := range h.engine.Describe() {
		index = append(index, map[string]any{"model": d.Model, "title": d.Title, "protocol": d.Protocol, "operations": len(d.Operations), "official": d.Official})
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
		t.Fatalf("served reference is stale; run CATALOG_WRITE_DOCS=1 go test ./internal/aws/catalog -run TestServedReference (%v)", err)
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

// The embedded official model data must ship with its original notices.
func TestBotocoreNoticesAreMirroredForRedistribution(t *testing.T) {
	original, err := os.ReadFile("../../../third_party/BOTOCORE-NOTICES.txt")
	if err != nil {
		t.Fatal(err)
	}
	served, err := os.ReadFile("../../legal/assets/BOTOCORE-NOTICES.txt")
	if err != nil || !bytes.Equal(original, served) {
		t.Fatal("the served botocore notices must equal third_party/BOTOCORE-NOTICES.txt")
	}
	for _, needle := range []string{"Apache License", "Botocore\nCopyright 2012-2022 Amazon.com", "botocore 1.41.6"} {
		if !bytes.Contains(original, []byte(needle)) {
			t.Fatalf("notice lacks %q", needle)
		}
	}
}
