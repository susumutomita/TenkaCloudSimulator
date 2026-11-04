package aws

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Kept only in test memory; never print the cursor key or dummy value rows.
func secretStateSnapshot(t *testing.T, db *core.Store) string {
	t.Helper()
	var state []any
	for _, query := range []string{"SELECT * FROM aws_secret_settings ORDER BY id", "SELECT * FROM aws_secret_arns ORDER BY arn", "SELECT * FROM aws_secrets ORDER BY region,arn", "SELECT * FROM aws_secret_versions ORDER BY region,arn,version_id", "SELECT * FROM aws_secret_stages ORDER BY region,arn,stage"} {
		rows, err := db.Database().Query(query)
		if err != nil {
			t.Fatal("cannot capture private state")
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal("cannot capture private columns")
		}
		var values [][]any
		for rows.Next() {
			row := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range row {
				targets[i] = &row[i]
			}
			if err = rows.Scan(targets...); err != nil {
				rows.Close()
				t.Fatal("cannot capture private row")
			}
			values = append(values, row)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal("cannot capture private state rows")
		}
		rows.Close()
		state = append(state, values)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal("cannot compare private state")
	}
	return string(data)
}

func TestSecretIgnoredWritesFailClosedAndRollback(t *testing.T) {
	const first = "00000000-0000-0000-0000-000000000001"
	const second = "00000000-0000-0000-0000-000000000002"
	for _, scenario := range []string{"initial-current", "previous-label", "put-current", "metadata-update", "delete-parent"} {
		t.Run(scenario, func(t *testing.T) {
			db, s := secretTestStore(t)
			allow := func(string, string) error { return nil }
			ctx := context.Background()
			arn := ""
			if scenario != "initial-current" {
				created, err := s.Create(ctx, "us-east-1", "training/ignored", "", first, newSecretMaterial("DUMMY-original"), allow)
				if err != nil {
					t.Fatal("fixture create failed")
				}
				arn = created.ARN
			}
			before := secretStateSnapshot(t, db)
			trigger := ""
			switch scenario {
			case "initial-current":
				trigger = `CREATE TRIGGER ignore_secret_write BEFORE INSERT ON aws_secret_stages WHEN NEW.stage='AWSCURRENT' BEGIN SELECT RAISE(IGNORE); END`
			case "previous-label":
				trigger = `CREATE TRIGGER ignore_secret_write BEFORE INSERT ON aws_secret_stages WHEN NEW.stage='AWSPREVIOUS' BEGIN SELECT RAISE(IGNORE); END`
			case "put-current":
				trigger = `CREATE TRIGGER ignore_secret_write BEFORE INSERT ON aws_secret_stages WHEN NEW.stage='AWSCURRENT' BEGIN SELECT RAISE(IGNORE); END`
			case "metadata-update":
				trigger = `CREATE TRIGGER ignore_secret_write BEFORE UPDATE ON aws_secrets BEGIN SELECT RAISE(IGNORE); END`
			case "delete-parent":
				trigger = `CREATE TRIGGER ignore_secret_write BEFORE DELETE ON aws_secrets BEGIN SELECT RAISE(IGNORE); END`
			}
			if _, err := db.Database().Exec(trigger); err != nil {
				t.Fatal("cannot install rollback fixture")
			}
			var err error
			switch scenario {
			case "initial-current":
				_, err = s.Create(ctx, "us-east-1", "training/ignored", "", first, newSecretMaterial("DUMMY-new"), allow)
			case "previous-label", "put-current", "metadata-update":
				_, err = s.Put(ctx, "us-east-1", arn, second, newSecretMaterial("DUMMY-replacement"), allow)
			case "delete-parent":
				_, err = s.DeleteForce(ctx, "us-east-1", arn, allow)
			}
			if err == nil {
				t.Fatal("ignored durable write reported success", scenario)
			}
			if _, err = db.Database().Exec("DROP TRIGGER ignore_secret_write"); err != nil {
				t.Fatal("cannot remove rollback fixture")
			}
			if after := secretStateSnapshot(t, db); after != before {
				t.Fatal("ignored durable write left partial state", scenario)
			}
		})
	}
}

func TestSecretDeniedMutationAndSelectorFramesNeverExposeValues(t *testing.T) {
	db, s := secretTestStore(t)
	const first = "00000000-0000-0000-0000-000000000001"
	const second = "00000000-0000-0000-0000-000000000002"
	allow := func(string, string) error { return nil }
	created, err := s.Create(context.Background(), "us-east-1", "training/denial-frame", "", first, newSecretMaterial("DUMMY-private-frame"), allow)
	if err != nil {
		t.Fatal("fixture create failed")
	}
	deny := func(string, string) error {
		return secretFailure{"AccessDeniedException", "IAM evaluation: explicitDeny"}
	}
	cases := []struct {
		operation string
		input     map[string]any
	}{
		{"CreateSecret", map[string]any{"Name": "training/denial-frame", "ClientRequestToken": first, "SecretString": "DUMMY-private-frame"}},
		{"DescribeSecret", map[string]any{"SecretId": created.ARN}},
		{"ListSecrets", map[string]any{}},
		{"PutSecretValue", map[string]any{"SecretId": created.ARN, "ClientRequestToken": second, "SecretString": "DUMMY-new-private"}},
		{"GetSecretValue", map[string]any{"SecretId": created.ARN}},
		{"UpdateSecretVersionStage", map[string]any{"SecretId": created.ARN, "VersionStage": "AWSCURRENT", "MoveToVersionId": first}},
		{"DeleteSecret", map[string]any{"SecretId": created.ARN, "ForceDeleteWithoutRecovery": true}},
	}
	for _, tc := range cases {
		before := secretStateSnapshot(t, db)
		w := secretHTTPCall(s, tc.operation, secretJSONFixture(t, tc.input), deny)
		if w.Code != 403 || strings.Contains(w.Body.String(), "DUMMY-") || strings.Contains(w.Body.String(), "SecretString") {
			t.Fatal("denied operation leaked or succeeded", tc.operation, w.Code)
		}
		if after := secretStateSnapshot(t, db); after != before {
			t.Fatal("denied operation changed complete state", tc.operation)
		}
	}
	before := secretStateSnapshot(t, db)
	w := secretHTTPCall(s, "GetSecretValue", secretJSONFixture(t, map[string]any{"SecretId": created.ARN, "VersionId": second, "VersionStage": "AWSCURRENT"}), allow)
	if w.Code != 400 || strings.Contains(w.Body.String(), "DUMMY-") {
		t.Fatal("mismatched selectors disclosed a value")
	}
	if after := secretStateSnapshot(t, db); after != before {
		t.Fatal("selector refusal changed persisted state")
	}
}
