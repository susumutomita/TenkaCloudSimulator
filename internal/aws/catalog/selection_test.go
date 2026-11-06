package catalog

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

// The extractor input must list exactly the reviewed operations so the
// embedded official subset can never contain an unbound operation.
func TestSelectionMatchesReviewedSpecifications(t *testing.T) {
	want := map[string][]string{}
	for _, service := range Services() {
		for _, op := range service.Ops {
			want[service.Model] = append(want[service.Model], op.Name)
		}
		sort.Strings(want[service.Model])
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
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selection.json is stale; run CATALOG_WRITE_SELECTION=1 go test ./internal/aws/catalog -run TestSelection and tools/awsmodels/extract.py")
	}
}
