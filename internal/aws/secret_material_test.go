package aws

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretMaterialGenericFormattingAndLoggingCannotExposeValues(t *testing.T) {
	const sentinel = "DUMMY-secret-privacy-sentinel-秘密"
	material := newSecretMaterial(sentinel)
	for _, value := range []any{material, &material, struct{ Value secretMaterial }{material}, struct{ private secretMaterial }{material}} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
			output := fmt.Sprintf(format, value)
			if strings.Contains(output, sentinel) || strings.Contains(output, "privacy-sentinel") || strings.Contains(output, hex.EncodeToString([]byte(sentinel))) {
				t.Fatal("generic formatting exposed secret material")
			}
		}
		encoded, err := json.Marshal(map[string]any{"value": value})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte(sentinel)) {
			t.Fatal("generic JSON logging did not redact secret material")
		}
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logger.Info("dummy redaction test", "secret", material, "nested", map[string]any{"value": material})
	if strings.Contains(output.String(), sentinel) || !strings.Contains(output.String(), "REDACTED") {
		t.Fatal("structured logging exposed secret material")
	}
	if material.plaintext() != sentinel {
		t.Fatal("redaction destroyed the stored value")
	}
	// Negative control: a nested unexported plain-string field really bypasses
	// Stringer dispatch. The protected representation above must withstand the
	// same reflective traversal without emitting those bytes.
	unsafeFixture := struct{ private struct{ raw string } }{private: struct{ raw string }{raw: sentinel}}
	if !strings.Contains(fmt.Sprintf("%+v", unsafeFixture), sentinel) {
		t.Fatal("formatting fixture no longer exercises private-field traversal")
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

func TestSecretMaterialUnicodeEscapesAreNotSilentlyReplaced(t *testing.T) {
	fixtures := []struct {
		name  string
		wire  string
		valid bool
		value string
	}{
		{"literal UTF8", `{"SecretString":"DUMMY-秘密"}`, true, "DUMMY-秘密"},
		{"paired surrogate", `{"SecretString":"DUMMY-\ud83d\ude80"}`, true, "DUMMY-🚀"},
		{"escaped literal", `{"SecretString":"DUMMY-\\ud800"}`, true, `DUMMY-\ud800`},
		{"escaped quote", `{"SecretString":"DUMMY-\"quoted\""}`, true, `DUMMY-"quoted"`},
		{"unpaired high", `{"SecretString":"DUMMY-\ud800"}`, false, ""},
		{"unpaired low", `{"SecretString":"DUMMY-\udc00"}`, false, ""},
		{"high then ordinary", `{"SecretString":"DUMMY-\ud800\u0041"}`, false, ""},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			if secretUnicodeScalars([]byte(fixture.wire)) != fixture.valid {
				t.Fatal("Unicode scalar validation disagreed with the request contract")
			}
			if fixture.valid {
				var input struct{ SecretString string }
				if err := json.Unmarshal([]byte(fixture.wire), &input); err != nil || input.SecretString != fixture.value {
					t.Fatal("valid decoded secret value bytes changed")
				}
			}
		})
	}
}
