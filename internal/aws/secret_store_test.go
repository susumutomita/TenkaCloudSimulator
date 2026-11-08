package aws

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func secretTestStore(t *testing.T) (*core.Store, *SecretStore) {
	t.Helper()
	db, err := core.New(filepath.Join(t.TempDir(), "secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewSecretStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.UnixMilli(1800000000000) }
	return db, s
}

func secretTestCode(err error) string {
	var e secretFailure
	if errors.As(err, &e) {
		return e.code
	}
	return "infrastructure"
}

func secretAssertEmptyResources(t *testing.T, db *core.Store) {
	t.Helper()
	for _, table := range []string{"aws_secret_arns", "aws_secrets", "aws_secret_versions", "aws_secret_stages"} {
		var count int
		if err := db.Database().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("unexpected partial secret state", table, count, err)
		}
	}
}

func TestSecretCreateTokenRetryAndDatabaseImmutability(t *testing.T) {
	ctx := context.Background()
	db, s := secretTestStore(t)
	const token = "00000000-0000-0000-0000-000000000001"
	const value = "DUMMY-only-creation-value-秘密"
	var authorized []string
	authorize := func(action, arn string) error { authorized = append(authorized, action+" "+arn); return nil }
	created, err := s.Create(ctx, "us-east-1", "training/example", "original metadata", token, newSecretMaterial(value), authorize)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.ARN, "arn:aws:secretsmanager:us-east-1:000000000000:secret:training/example-") || created.VersionId != token {
		t.Fatal("creation did not retain native identifiers")
	}
	s.now = func() time.Time { return time.UnixMilli(1800003600000) }
	retried, err := s.Create(ctx, "us-east-1", "training/example", "must not replace metadata", token, newSecretMaterial(value), authorize)
	if err != nil || retried.ARN != created.ARN || retried.VersionId != created.VersionId {
		t.Fatal("retry changed the physical identity", err)
	}
	if _, err = s.Create(ctx, "us-east-1", "training/example", "", token, newSecretMaterial("DUMMY-different-value"), authorize); secretTestCode(err) != "ResourceExistsException" {
		t.Fatal("retry overwrote an immutable value")
	}
	if len(authorized) != 3 {
		t.Fatal("creation/retries skipped authorization")
	}
	for _, entry := range authorized {
		if entry != "secretsmanager:CreateSecret "+created.ARN {
			t.Fatal("retry authority did not use the saved ARN")
		}
	}
	var description, stored string
	var changed int64
	if err = db.Database().QueryRow("SELECT description,changed FROM aws_secrets WHERE arn=?", created.ARN).Scan(&description, &changed); err != nil || description != "original metadata" || changed != 1800000000000 {
		t.Fatal("retry changed metadata or its timestamp", err)
	}
	if _, err = db.Database().Exec("UPDATE aws_secret_versions SET value=? WHERE arn=?", []byte("DUMMY-overwrite-attempt"), created.ARN); err == nil {
		t.Fatal("database accepted a version overwrite")
	}
	if err = db.Database().QueryRow("SELECT value FROM aws_secret_versions WHERE arn=? AND version_id=?", created.ARN, token).Scan(&stored); err != nil || stored != value {
		t.Fatal("saved plaintext practice value did not survive unchanged", err)
	}
	encoded, err := json.Marshal(created)
	if err != nil || strings.Contains(string(encoded), value) || strings.Contains(string(encoded), "SecretString") || strings.Contains(string(encoded), "KmsKeyId") {
		t.Fatal("creation response exposed a value or fake encryption metadata")
	}
	reopened, err := NewSecretStore(db.Database())
	if err != nil || string(reopened.cursorKey) != string(s.cursorKey) {
		t.Fatal("reopening rotated cursor authority", err)
	}
}

func TestSecretCreationDenialCancellationAndLateFailureLeaveNoState(t *testing.T) {
	ctx := context.Background()
	db, s := secretTestStore(t)
	const token = "00000000-0000-0000-0000-000000000001"
	material := newSecretMaterial("DUMMY-practice-value")
	denied := func(string, string) error {
		return secretFailure{"AccessDeniedException", "IAM evaluation: explicitDeny"}
	}
	if _, err := s.Create(ctx, "us-east-1", "training/denied", "", token, material, denied); secretTestCode(err) != "AccessDeniedException" {
		t.Fatal("denied creation did not fail closed")
	}
	secretAssertEmptyResources(t, db)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	allow := func(string, string) error { return nil }
	if _, err := s.Create(canceled, "us-east-1", "training/canceled", "", token, material, allow); err == nil {
		t.Fatal("canceled creation succeeded")
	}
	secretAssertEmptyResources(t, db)
	if _, err := db.Database().Exec(`CREATE TRIGGER reject_initial_secret_label BEFORE INSERT ON aws_secret_stages BEGIN SELECT RAISE(ABORT,'test late label failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "us-east-1", "training/rollback", "", token, material, allow); err == nil {
		t.Fatal("partial creation reported success")
	}
	secretAssertEmptyResources(t, db)
	if _, err := db.Database().Exec("DROP TRIGGER reject_initial_secret_label"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "us-east-1", "training/rollback", "", token, material, allow); err != nil {
		t.Fatal("failed transaction prevented a corrected retry", err)
	}
}
