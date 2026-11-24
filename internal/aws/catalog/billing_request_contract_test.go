package catalog

import "testing"

func TestBillingCostCategoryRejectImpossibleMonth(t *testing.T) {
	h := newHarness(t, costExplorer)
	create := func(name, start string) map[string]any {
		return map[string]any{"Name": name, "RuleVersion": "CostCategoryExpression.v1", "EffectiveStart": start,
			"Rules": []any{map[string]any{"Type": "REGULAR", "Value": "a", "Rule": map[string]any{"Dimensions": map[string]any{"Key": "LINKED_ACCOUNT", "Values": []any{"000000000000"}}}}}}
	}
	const valid = "2026-01-01T00:00:00Z"
	const bad = "2026-13-01T00:00:00Z"
	good := h.json1("ce", "CreateCostCategoryDefinition", create("teams", valid))
	expect(t, good, 200, "")
	arn := str(good.body["CostCategoryArn"])
	rejected := h.json1("ce", "CreateCostCategoryDefinition", create("other", bad))
	if rejected.status != 400 {
		t.Errorf("impossible month create: got HTTP %d", rejected.status)
	}
	change := create("", bad)
	delete(change, "Name")
	change["CostCategoryArn"] = arn
	rejected = h.json1("ce", "UpdateCostCategoryDefinition", change)
	if rejected.status != 400 {
		t.Errorf("impossible month update: got HTTP %d", rejected.status)
	}
	listed := h.json1("ce", "ListCostCategoryDefinitions", map[string]any{})
	rows, ok := listed.body["CostCategoryReferences"].([]any)
	if !ok || len(rows) != 1 {
		t.Errorf("invalid create changed stored list: %v", listed.body)
	}
	after := h.json1("ce", "DescribeCostCategoryDefinition", map[string]any{"CostCategoryArn": arn})
	stored, ok := after.body["CostCategory"].(map[string]any)
	if !ok || stored["EffectiveStart"] != valid {
		t.Errorf("invalid update changed stored category: %v", after.body)
	}
}

func TestBillingDataExportRequiresSelectKeyword(t *testing.T) {
	h := newHarness(t, bcmDataExports)
	export := func(name, query string) map[string]any {
		return map[string]any{"Name": name, "RefreshCadence": map[string]any{"Frequency": "SYNCHRONOUS"},
			"DataQuery": map[string]any{"QueryStatement": query}, "DestinationConfigurations": map[string]any{"S3Destination": map[string]any{
				"S3Bucket": "bucket", "S3Prefix": "p", "S3Region": "us-east-1", "S3OutputConfigurations": map[string]any{"Compression": "GZIP", "Format": "TEXT_OR_CSV", "OutputType": "CUSTOM", "Overwrite": "OVERWRITE_REPORT"}}}}
	}
	const goodQuery = " \tSeLeCt\tfoo FROM COST_AND_USAGE_REPORT"
	good := h.json1("bcm-data-exports", "CreateExport", map[string]any{"Export": export("original", goodQuery)})
	expect(t, good, 200, "")
	arn := str(good.body["ExportArn"])
	for _, badQuery := range []string{"SELECTED foo FROM COST_AND_USAGE_REPORT", "SELECT"} {
		rejectCreate := h.json1("bcm-data-exports", "CreateExport", map[string]any{"Export": export("unwanted", badQuery)})
		if rejectCreate.status != 400 {
			t.Errorf("invalid create %q: got HTTP %d", badQuery, rejectCreate.status)
		}
		rejectUpdate := h.json1("bcm-data-exports", "UpdateExport", map[string]any{"ExportArn": arn, "Export": export("updated", badQuery)})
		if rejectUpdate.status != 400 {
			t.Errorf("invalid update %q: got HTTP %d", badQuery, rejectUpdate.status)
		}
	}
	listed := h.json1("bcm-data-exports", "ListExports", map[string]any{})
	rows, ok := listed.body["Exports"].([]any)
	if !ok || len(rows) != 1 {
		t.Errorf("invalid create changed stored list: %v", listed.body)
	}
	after := h.json1("bcm-data-exports", "GetExport", map[string]any{"ExportArn": arn})
	stored, ok := after.body["Export"].(map[string]any)
	query, _ := stored["DataQuery"].(map[string]any)
	if !ok || stored["Name"] != "original" || query["QueryStatement"] != goodQuery {
		t.Errorf("invalid update changed stored export: %v", after.body)
	}
}
