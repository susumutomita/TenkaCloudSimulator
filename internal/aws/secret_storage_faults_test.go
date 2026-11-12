package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The Secrets Manager lifecycle reports every storage fault as an opaque
// InternalServiceError and never echoes secret material in that failure.
func TestSecretLifecycleReportsEveryStorageFault(t *testing.T) {
	const a = "00000000-0000-0000-0000-00000000000a"
	const b = "00000000-0000-0000-0000-00000000000b"
	steps := []struct {
		op    string
		input map[string]any
	}{
		{"CreateSecret", map[string]any{"Name": "training/sweep", "ClientRequestToken": a, "SecretString": "DUMMY-secret-sweep-A"}},
		{"DescribeSecret", map[string]any{"SecretId": "training/sweep"}},
		{"UpdateSecret", map[string]any{"SecretId": "training/sweep", "Description": "rotated by hand"}},
		{"PutSecretValue", map[string]any{"SecretId": "training/sweep", "ClientRequestToken": b, "SecretString": "DUMMY-secret-sweep-B", "VersionStages": []string{"AWSCURRENT"}}},
		{"GetSecretValue", map[string]any{"SecretId": "training/sweep", "VersionStage": "AWSPREVIOUS"}},
		{"ListSecretVersionIds", map[string]any{"SecretId": "training/sweep", "MaxResults": 1, "IncludeDeprecated": true}},
		{"ListSecrets", map[string]any{"MaxResults": 1, "SortBy": "name", "SortOrder": "asc"}},
		{"UpdateSecretVersionStage", map[string]any{"SecretId": "training/sweep", "VersionStage": "AWSCURRENT", "MoveToVersionId": a, "RemoveFromVersionId": b}},
		{"DeleteSecret", map[string]any{"SecretId": "$ARN", "ForceDeleteWithoutRecovery": true}},
	}
	allow := func(string, string) error { return nil }
	sweepStorageFaults(t, false, func(t *testing.T, db *sql.DB) func(context.Context) error {
		s, err := NewSecretStore(db)
		if err != nil {
			t.Fatal(err)
		}
		return func(context.Context) error {
			arn := ""
			for _, step := range steps {
				input := map[string]any{}
				for k, v := range step.input {
					if v == "$ARN" {
						v = arn
					}
					input[k] = v
				}
				w := secretHTTPCall(s, step.op, secretJSONFixture(t, input), allow)
				switch {
				case w.Code == 200:
				case w.Code == 500:
					if strings.Contains(w.Body.String(), "DUMMY-secret") || strings.Contains(w.Body.String(), errInjectedStorage.Error()) {
						t.Fatalf("failure leaked detail: %s", w.Body.String())
					}
					return fmt.Errorf("%s: %s", step.op, w.Body.String())
				default:
					t.Fatalf("storage fault misreported by %s: %d %s", step.op, w.Code, w.Body.String())
				}
				if step.op == "CreateSecret" {
					var created SecretMutationResult
					if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ARN == "" {
						t.Fatalf("create receipt %s %v", w.Body.String(), err)
					}
					arn = created.ARN
				}
			}
			return nil
		}
	})
}
