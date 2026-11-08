package catalog

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

// The extractor input must list exactly the reviewed methods.
func TestSelectionMatchesReviewedSpecifications(t *testing.T) {
	want := map[string][]string{}
	for _, api := range APIs() {
		for _, b := range api.Bindings {
			want[api.Key] = append(want[api.Key], b.Method)
		}
		sort.Strings(want[api.Key])
	}
	if os.Getenv("CATALOG_WRITE_SELECTION") == "1" {
		data, _ := json.MarshalIndent(want, "", "  ")
		if err := os.WriteFile("selection.json", append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile("selection.json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string][]string
	if err := json.Unmarshal(data, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("selection.json is stale; run CATALOG_WRITE_SELECTION=1 go test ./internal/gcp/catalog -run TestSelection and tools/gcpmodels/extract.py")
	}
}
