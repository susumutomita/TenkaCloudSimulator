package catalog

import "testing"

func TestIVSRecordingCustomRenditionsRequireSelection(t *testing.T) {
	h := newHarness(t, ivs)
	create := func(config map[string]any) reply {
		return h.rest("ivs", "POST", "/CreateRecordingConfiguration", map[string]any{
			"destinationConfiguration": map[string]any{"s3": map[string]any{"bucketName": "archive-bucket"}},
			"renditionConfiguration":   config,
		})
	}
	for _, config := range []map[string]any{
		{"renditionSelection": "CUSTOM"},
		{"renditionSelection": "CUSTOM", "renditions": []any{}},
	} {
		expect(t, create(config), 400, "ValidationException")
	}
	list := h.rest("ivs", "POST", "/ListRecordingConfigurations", map[string]any{})
	expect(t, list, 200, "")
	if len(list.body["recordingConfigurations"].([]any)) != 0 {
		t.Fatalf("invalid recording configuration was persisted: %v", list.body)
	}
	for _, config := range []map[string]any{
		{"renditionSelection": "ALL"},
		{"renditionSelection": "NONE"},
		{"renditionSelection": "CUSTOM", "renditions": []any{"SD"}},
	} {
		expect(t, create(config), 200, "")
	}
}
