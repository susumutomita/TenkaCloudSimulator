package catalog

import (
	"strings"
	"testing"
)

// AWS CreateAppInstance limits Metadata to 1 KiB of UTF-8, not 1,024 runes.
func TestChimeIdentityCreateAppInstanceMetadataByteLimit(t *testing.T) {
	h := businessHarness(t, chimeIdentity)
	call := func(method, path string, input any) reply { return h.rest("chime-sdk-identity", method, path, input) }
	sample := strings.Repeat("あ", 341) + "a" // 1024 UTF-8 bytes.
	valid := call("POST", "/app-instances", map[string]any{"Name": "valid", "Metadata": sample, "ClientRequestToken": "token-boundary"})
	expect(t, valid, 201, "")
	arn := str(valid.body["AppInstanceArn"])
	get := call("GET", "/app-instances/"+bizEscapeARN(arn), nil)
	expect(t, get, 200, "")
	if app, ok := get.body["AppInstance"].(map[string]any); !ok || app["Metadata"] != sample {
		t.Fatal("1024-byte Metadata did not round-trip")
	}
	oversize := call("POST", "/app-instances", map[string]any{"Name": "extra", "Metadata": sample + "a", "ClientRequestToken": "token-oversize"})
	if oversize.status != 400 {
		t.Errorf("CreateAppInstance accepted 1,025 UTF-8 bytes: HTTP %d", oversize.status)
	} else {
		expect(t, oversize, 400, "BadRequestException")
	}
	if listed := items(t, call("GET", "/app-instances", nil), "AppInstances"); len(listed) != 1 {
		t.Errorf("oversized create left an extra persisted AppInstance, count %d", len(listed))
	}
}
