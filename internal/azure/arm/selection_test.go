package arm

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

type selectionEntry struct {
	APIVersion string   `json:"apiVersion"`
	Files      []string `json:"files"`
	Operations []string `json:"operations"`
}

type selectionFile struct {
	Commit    string                     `json:"commit"`
	Providers map[string]*selectionEntry `json:"providers"`
}

// The extractor input must list exactly the reviewed operations and
// api-versions; the source files and commit are maintained by hand.
func TestSelectionMatchesReviewedSpecifications(t *testing.T) {
	data, err := os.ReadFile("selection.json")
	if err != nil {
		t.Fatal(err)
	}
	var got selectionFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{}
	versions := map[string]string{}
	for _, p := range Providers() {
		for _, b := range p.Bindings {
			want[p.Namespace] = append(want[p.Namespace], b.Operation)
		}
		sort.Strings(want[p.Namespace])
		versions[p.Namespace] = p.APIVersion
	}
	if os.Getenv("CATALOG_WRITE_SELECTION") == "1" {
		for namespace, operations := range want {
			entry := got.Providers[namespace]
			if entry == nil {
				entry = &selectionEntry{}
				got.Providers[namespace] = entry
			}
			entry.APIVersion, entry.Operations = versions[namespace], operations
		}
		for namespace := range got.Providers {
			if want[namespace] == nil {
				delete(got.Providers, namespace)
			}
		}
		out, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile("selection.json", append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(got.Providers) != len(want) {
		t.Fatal("selection.json is stale; run CATALOG_WRITE_SELECTION=1 go test ./internal/azure/arm -run TestSelection and tools/azuremodels/extract.py")
	}
	for namespace, operations := range want {
		entry := got.Providers[namespace]
		if entry == nil || entry.APIVersion != versions[namespace] || !reflect.DeepEqual(entry.Operations, operations) || len(entry.Files) == 0 {
			t.Fatalf("selection.json is stale for %s; run CATALOG_WRITE_SELECTION=1 go test ./internal/azure/arm -run TestSelection and tools/azuremodels/extract.py", namespace)
		}
		model, err := loadModel(namespace)
		if err != nil {
			t.Fatal(err)
		}
		if model.Source.Commit != got.Commit {
			t.Fatalf("%s was extracted from another commit; rerun tools/azuremodels/extract.py", namespace)
		}
	}
}
