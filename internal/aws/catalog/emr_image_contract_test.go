package catalog

import "testing"

func TestEMRServerlessImageScopesAreExclusive(t *testing.T) {
	h := newHarness(t, emrServerless)
	image := map[string]any{"imageUri": "123456789012.dkr.ecr.us-east-1.amazonaws.com/demo:stable"}
	specs := map[string]any{"Driver": map[string]any{"imageConfiguration": image}, "Executor": map[string]any{"imageConfiguration": image}}
	conflict := h.rest("emr-serverless", "POST", "/applications", map[string]any{"name": "bad", "releaseLabel": "emr-7.5.0", "type": "SPARK", "clientToken": "bad", "imageConfiguration": image, "workerTypeSpecifications": specs})
	rows := h.rest("emr-serverless", "GET", "/applications", nil).body["applications"].([]any)
	t.Run("create", func(t *testing.T) {
		t.Logf("conflict status=%d rows=%d", conflict.status, len(rows))
		expect(t, conflict, 400, "ValidationException")
		if len(rows) != 0 {
			t.Fatal("rejected conflict persisted")
		}
	})
	valid := h.rest("emr-serverless", "POST", "/applications", map[string]any{"name": "valid", "releaseLabel": "emr-7.5.0", "type": "SPARK", "clientToken": "valid"})
	expect(t, valid, 200, "")
	path := "/applications/" + str(valid.body["applicationId"])
	update := h.rest("emr-serverless", "PATCH", path, map[string]any{"clientToken": "update", "architecture": "ARM64", "imageConfiguration": image, "workerTypeSpecifications": specs})
	current := h.rest("emr-serverless", "GET", path, nil).body["application"].(map[string]any)
	t.Run("update", func(t *testing.T) {
		t.Logf("conflict status=%d stored architecture=%v global image present=%t per-worker image present=%t", update.status, current["architecture"], current["imageConfiguration"] != nil, current["workerTypeSpecifications"] != nil)
		expect(t, update, 400, "ValidationException")
		if current["imageConfiguration"] != nil || current["workerTypeSpecifications"] != nil || current["architecture"] == "ARM64" {
			t.Fatal("rejected conflict persisted")
		}
	})
}

func TestEMRServerlessImageScopeAllowsEachOption(t *testing.T) {
	h := newHarness(t, emrServerless)
	image := map[string]any{"imageUri": "123456789012.dkr.ecr.us-east-1.amazonaws.com/demo:stable"}
	metadataOnly := map[string]any{"Driver": map[string]any{}, "Executor": map[string]any{}}
	perWorker := map[string]any{"Driver": map[string]any{"imageConfiguration": image}, "Executor": map[string]any{"imageConfiguration": image}}
	for _, tc := range []struct {
		name                        string
		fields                      map[string]any
		wantGlobal, wantWorkerImage bool
	}{
		{"global-with-worker-metadata", map[string]any{"imageConfiguration": image, "workerTypeSpecifications": metadataOnly}, true, false},
		{"per-worker", map[string]any{"workerTypeSpecifications": perWorker}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := map[string]any{"name": tc.name, "type": "SPARK", "releaseLabel": "emr-7.5.0", "clientToken": tc.name}
			for name, value := range tc.fields {
				input[name] = value
			}
			created := h.rest("emr-serverless", "POST", "/applications", input)
			expect(t, created, 200, "")
			path := "/applications/" + str(created.body["applicationId"])
			updated := h.rest("emr-serverless", "PATCH", path, map[string]any{"clientToken": "update-" + tc.name, "architecture": "ARM64"})
			expect(t, updated, 200, "")
			record := h.rest("emr-serverless", "GET", path, nil).body["application"].(map[string]any)
			workers, _ := record["workerTypeSpecifications"].(map[string]any)
			driver, _ := workers["Driver"].(map[string]any)
			if len(workers) != 2 || (record["imageConfiguration"] != nil) != tc.wantGlobal || (driver["imageConfiguration"] != nil) != tc.wantWorkerImage || record["architecture"] != "ARM64" {
				t.Fatalf("image scope or worker metadata was not preserved: %#v", record)
			}
		})
	}
}
