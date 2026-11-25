package catalog

import "testing"

func TestQuickSightRootDefaultsAndExplicitMetadata(t *testing.T) {
	h := newHarness(t, quickSight)
	base := "/accounts/000000000000/folders"
	for _, item := range []struct {
		name                  string
		body                  map[string]any
		wantType, wantSharing string
	}{
		{"omitted", map[string]any{"Name": "Omitted"}, "SHARED", "ACCOUNT"},
		{"empty", map[string]any{"Name": "Empty", "ParentFolderArn": ""}, "SHARED", "ACCOUNT"},
		{"explicit", map[string]any{"Name": "Explicit", "FolderType": "RESTRICTED", "SharingModel": "NAMESPACE"}, "RESTRICTED", "NAMESPACE"},
	} {
		t.Run(item.name, func(t *testing.T) {
			res := h.rest("quicksight", "POST", base+"/"+item.name, item.body)
			expect(t, res, 200, "")
			got := h.rest("quicksight", "GET", base+"/"+item.name, nil)
			expect(t, got, 200, "")
			f := got.body["Folder"].(map[string]any)
			if f["FolderType"] != item.wantType || f["SharingModel"] != item.wantSharing || f["FolderPath"] != nil {
				t.Fatalf("unexpected root folder metadata: %v", f)
			}
		})
	}
	for _, item := range []struct {
		name string
		body map[string]any
	}{
		{"nested", map[string]any{"Name": "Nested", "ParentFolderArn": "arn:aws:quicksight:us-east-1:000000000000:folder/omitted"}},
		{"permissions", map[string]any{"Name": "Permissions", "Permissions": []any{map[string]any{"Principal": "p", "Actions": []any{"quicksight:DescribeFolder"}}}}},
	} {
		t.Run(item.name, func(t *testing.T) {
			expect(t, h.rest("quicksight", "POST", base+"/"+item.name, item.body), 400, "InvalidParameterValueException")
			expect(t, h.rest("quicksight", "GET", base+"/"+item.name, nil), 404, "ResourceNotFoundException")
		})
	}
}
