package aws

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestSecretForceDeletionPreservesRetiredARNAndDoesNotAdoptRecreatedName(t *testing.T) {
	ctx := context.Background()
	db, s := secretTestStore(t)
	const id = "00000000-0000-0000-0000-000000000001"
	allow := func(string, string) error { return nil }
	created, err := s.Create(ctx, "us-east-1", "training/recreated", "", id, newSecretMaterial("DUMMY-old"), allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteForce(ctx, "us-east-1", "training/recreated", allow); secretTestCode(err) != "InvalidParameterException" {
		t.Fatal("name-only deletion escaped bounded ARN contract")
	}
	if _, err = s.DeleteForce(ctx, "us-east-1", created.ARN, allow); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"aws_secrets", "aws_secret_versions", "aws_secret_stages"} {
		var count int
		if err = db.Database().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("force deletion orphaned active secret data", table, err)
		}
	}
	var reserved int
	if err = db.Database().QueryRow("SELECT count(*) FROM aws_secret_arns WHERE arn=?", created.ARN).Scan(&reserved); err != nil || reserved != 1 {
		t.Fatal("force deletion released the retired ARN", err)
	}
	// Force the first generated candidate to collide with the old physical ARN.
	// The allocator must retain its reservation and choose another identifier.
	oldSuffix := created.ARN[len(created.ARN)-6:]
	attempt := 0
	s.suffix = func() string {
		attempt++
		if attempt == 1 {
			return oldSuffix
		}
		return "NEW123"
	}
	recreated, err := s.Create(ctx, "us-east-1", "training/recreated", "", id, newSecretMaterial("DUMMY-new"), allow)
	if err != nil || recreated.ARN == created.ARN {
		t.Fatal("recreated secret reused an old ARN", err)
	}
	if _, err = s.DeleteForce(ctx, "us-east-1", created.ARN, allow); err != nil {
		t.Fatal("absent old ARN was not idempotent", err)
	}
	value, err := s.Get(ctx, "us-east-1", recreated.ARN, "", "", allow)
	if err != nil || value.material.plaintext() != "DUMMY-new" {
		t.Fatal("old-ARN cleanup deleted the new secret", err)
	}
	if _, err = s.Get(ctx, "us-east-1", created.ARN, "", "", allow); secretTestCode(err) != "ResourceNotFoundException" {
		t.Fatal("old ARN resolved through its former name")
	}
	oldPermission := func(action, arn string) error {
		if arn == created.ARN {
			return nil
		}
		return secretFailure{"AccessDeniedException", "IAM evaluation: implicitDeny"}
	}
	if _, err = s.Get(ctx, "us-east-1", "training/recreated", "", "", oldPermission); secretTestCode(err) != "AccessDeniedException" {
		t.Fatal("old ARN permission authorized new identity")
	}
}

func TestSecretMetadataPagingIsValueFreeScopedAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	db, s := secretTestStore(t)
	_, other := secretTestStore(t)
	const id = "00000000-0000-0000-0000-000000000001"
	allow := func(string, string) error { return nil }
	for _, name := range []string{"training/a", "training/b", "training/c"} {
		if _, err := s.Create(ctx, "us-east-1", name, "", id, newSecretMaterial("DUMMY-never-list-this"), allow); err != nil {
			t.Fatal(err)
		}
	}
	q := SecretListQuery{MaxResults: 1, SortBy: "name", SortOrder: "asc"}
	first, next, err := s.List(ctx, "us-east-1", q, allow)
	if err != nil || len(first) != 1 || first[0].Name != "training/a" || next == "" {
		t.Fatal("first native metadata page failed", err)
	}
	encoded, err := json.Marshal(first)
	if err != nil || strings.Contains(string(encoded), "DUMMY-") || strings.Contains(string(encoded), "SecretString") || strings.Contains(string(encoded), "KmsKeyId") {
		t.Fatal("metadata listing exposed a value or fake KMS metadata")
	}
	q.NextToken = next
	if _, _, err = other.List(ctx, "us-east-1", q, allow); secretTestCode(err) != "InvalidParameterException" {
		t.Fatal("cursor crossed database authorities")
	}
	if _, _, err = s.List(ctx, "us-west-2", q, allow); secretTestCode(err) != "InvalidParameterException" {
		t.Fatal("cursor crossed region scope")
	}
	changed := q
	changed.SortOrder = "desc"
	if _, _, err = s.List(ctx, "us-east-1", changed, allow); secretTestCode(err) != "InvalidParameterException" {
		t.Fatal("cursor changed its query")
	}
	var path string
	if err = db.Database().QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s, err = NewSecretStore(reopened.Database())
	if err != nil {
		t.Fatal(err)
	}
	second, next, err := s.List(ctx, "us-east-1", q, allow)
	if err != nil || len(second) != 1 || second[0].Name != "training/b" || next == "" {
		t.Fatal("reopen changed metadata paging authority", err)
	}
	q.NextToken = next
	third, next, err := s.List(ctx, "us-east-1", q, allow)
	if err != nil || len(third) != 1 || third[0].Name != "training/c" || next != "" {
		t.Fatal("pagination duplicated or skipped metadata", err)
	}
}

func TestSecretCascadeFailureLeavesValuesLabelsAndIdentityIntact(t *testing.T) {
	ctx := context.Background()
	db, s := secretTestStore(t)
	const id = "00000000-0000-0000-0000-000000000001"
	allow := func(string, string) error { return nil }
	created, err := s.Create(ctx, "us-east-1", "training/delete-rollback", "", id, newSecretMaterial("DUMMY-retained"), allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec(`CREATE TRIGGER reject_secret_value_cleanup BEFORE DELETE ON aws_secret_versions BEGIN SELECT RAISE(ABORT,'test version cleanup failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteForce(ctx, "us-east-1", created.ARN, allow); err == nil {
		t.Fatal("failed cascade returned success")
	}
	value, err := s.Get(ctx, "us-east-1", created.ARN, "", "", allow)
	if err != nil || value.material.plaintext() != "DUMMY-retained" || len(value.VersionStages) != 1 || value.VersionStages[0] != "AWSCURRENT" {
		t.Fatal("failed cascade damaged secret identity, label or value", err)
	}
}
