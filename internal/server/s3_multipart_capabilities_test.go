package server

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestMultipartCapabilitiesAreConsistent(t *testing.T) {
	s := instance(t)
	var manifest struct {
		Operations  []string `json:"operations"`
		Unsupported []string `json:"unsupported"`
		Services    map[string]struct {
			Operations []string `json:"operations"`
		} `json:"services"`
	}
	if err := json.Unmarshal(status(t, s, "GET", "/_simulator/v1/capabilities", "", 200, nil), &manifest); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"CreateMultipartUpload", "UploadPart", "ListParts", "ListMultipartUploads", "CompleteMultipartUpload", "AbortMultipartUpload"} {
		if !slices.Contains(manifest.Operations, operation) || !slices.Contains(manifest.Services["s3"].Operations, operation) {
			t.Errorf("implemented multipart operation %s missing from capabilities", operation)
		}
	}
	for _, unsupported := range manifest.Unsupported {
		if strings.EqualFold(strings.TrimSpace(unsupported), "multipart uploads") {
			t.Error("implemented multipart uploads are advertised as unsupported")
		}
	}
}
