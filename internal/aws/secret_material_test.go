package aws

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretMaterialGenericFormattingAndLoggingCannotExposeValues(t *testing.T) {
	const sentinel = "DUMMY-secret-privacy-sentinel-秘密"
	material := secretMaterial{raw: sentinel}
	for _, value := range []any{material, &material, struct{ Value secretMaterial }{material}} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
			output := fmt.Sprintf(format, value)
			if strings.Contains(output, sentinel) || strings.Contains(output, "privacy-sentinel") {
				t.Fatal("generic formatting exposed secret material")
			}
		}
		encoded, err := json.Marshal(map[string]any{"value": value})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte(sentinel)) || !bytes.Contains(encoded, []byte("REDACTED")) {
			t.Fatal("generic JSON logging did not redact secret material")
		}
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logger.Info("dummy redaction test", "secret", material, "nested", map[string]any{"value": material})
	if strings.Contains(output.String(), sentinel) || !strings.Contains(output.String(), "REDACTED") {
		t.Fatal("structured logging exposed secret material")
	}
	if material.raw != sentinel {
		t.Fatal("redaction destroyed the stored value")
	}
}

func TestSecretMetadataUsesValueFreeNativeWireShapes(t *testing.T) {
	metadata := SecretMetadata{ARN: "arn:aws:secretsmanager:us-east-1:000000000000:secret:dummy-Ab1234", Name: "dummy", Description: "dummy metadata", CreatedDate: 1, LastChangedDate: 2}
	stages := map[string][]string{"00000000-0000-0000-0000-000000000001": {"AWSCURRENT"}}
	for _, fixture := range []struct {
		value any
		field string
	}{
		{SecretDescription{SecretMetadata: metadata, VersionIdsToStages: stages}, "VersionIdsToStages"},
		{SecretListEntry{SecretMetadata: metadata, SecretVersionsToStages: stages}, "SecretVersionsToStages"},
	} {
		encoded, err := json.Marshal(fixture.value)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		if wire[fixture.field] == nil || wire["SecretString"] != nil || wire["SecretBinary"] != nil || wire["KmsKeyId"] != nil {
			t.Fatal("metadata wire shape gained a value or a fake KMS field")
		}
	}
}
