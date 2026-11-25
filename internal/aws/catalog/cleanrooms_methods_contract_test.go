package catalog

import "testing"

func TestCleanRoomsSelectedAnalysisMethodsAreDistinct(t *testing.T) {
	for _, mode := range []string{"create", "update"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, cleanRooms)
			table := map[string]any{"name": "orders", "analysisMethod": "MULTIPLE",
				"tableReference": map[string]any{"glue": map[string]any{"databaseName": "sales", "tableName": "orders"}},
				"allowedColumns": []any{"order_id"}, "selectedAnalysisMethods": []any{"DIRECT_QUERY", "DIRECT_JOB"}}
			if mode == "create" {
				table["selectedAnalysisMethods"] = []any{"DIRECT_QUERY", "DIRECT_QUERY"}
				result := h.rest("cleanrooms", "POST", "/configuredTables", table)
				n := len(h.rest("cleanrooms", "GET", "/configuredTables", nil).body["configuredTableSummaries"].([]any))
				t.Logf("duplicate request status=%d, stored rows=%d", result.status, n)
				expect(t, result, 400, "ValidationException")
				if n != 0 {
					t.Fatalf("invalid create persisted %d rows", n)
				}
			} else {
				c := h.rest("cleanrooms", "POST", "/configuredTables", table)
				expect(t, c, 200, "")
				path := "/configuredTables/" + str(c.body["configuredTable"].(map[string]any)["id"])
				result := h.rest("cleanrooms", "PATCH", path, map[string]any{"selectedAnalysisMethods": []any{"DIRECT_QUERY", "DIRECT_QUERY"}, "description": "do not persist"})
				current := h.rest("cleanrooms", "GET", path, nil).body["configuredTable"].(map[string]any)
				t.Logf("duplicate request status=%d, stored values=%#v description=%#v", result.status, current["selectedAnalysisMethods"], current["description"])
				expect(t, result, 400, "ValidationException")
				if current["description"] != nil || current["selectedAnalysisMethods"].([]any)[1] != "DIRECT_JOB" {
					t.Fatal("invalid update mutated stored table")
				}
				unchanged := h.rest("cleanrooms", "PATCH", path, map[string]any{"description": "valid omission"})
				expect(t, unchanged, 200, "")
				got := unchanged.body["configuredTable"].(map[string]any)
				if got["description"] != "valid omission" || got["selectedAnalysisMethods"].([]any)[1] != "DIRECT_JOB" {
					t.Fatal("omitting selection did not preserve both methods")
				}
				swapped := h.rest("cleanrooms", "PATCH", path, map[string]any{"selectedAnalysisMethods": []any{"DIRECT_JOB", "DIRECT_QUERY"}})
				expect(t, swapped, 200, "")
				pair := swapped.body["configuredTable"].(map[string]any)["selectedAnalysisMethods"].([]any)
				if len(pair) != 2 || pair[0] != "DIRECT_JOB" || pair[1] != "DIRECT_QUERY" {
					t.Fatalf("distinct reversed pair changed: %v", pair)
				}
			}
		})
	}
}
