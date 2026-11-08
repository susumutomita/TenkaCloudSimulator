package aws

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSecretVersionsHaveOneCurrentPreviousAndImmutableRetries(t *testing.T) {
	ctx := context.Background()
	_, s := secretTestStore(t)
	const a = "00000000-0000-0000-0000-000000000001"
	const b = "00000000-0000-0000-0000-000000000002"
	const c = "00000000-0000-0000-0000-000000000003"
	allow := func(string, string) error { return nil }
	created, err := s.Create(ctx, "us-east-1", "training/versions", "", a, newSecretMaterial("DUMMY-A"), allow)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct{ id, value string }{{b, "DUMMY-B"}, {c, "DUMMY-C"}} {
		if _, err = s.Put(ctx, "us-east-1", created.ARN, v.id, newSecretMaterial(v.value), allow); err != nil {
			t.Fatal(err)
		}
	}
	meta, err := s.Describe(ctx, "us-east-1", created.ARN, allow)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string][]string{b: {"AWSPREVIOUS"}, c: {"AWSCURRENT"}}
	if !reflect.DeepEqual(meta.VersionIdsToStages, expected) {
		t.Fatal("three puts duplicated a staging-label owner")
	}
	for _, query := range []struct{ id, stage, value string }{{"", "", "DUMMY-C"}, {"", "AWSPREVIOUS", "DUMMY-B"}, {a, "", "DUMMY-A"}} {
		value, err := s.Get(ctx, "us-east-1", "training/versions", query.id, query.stage, allow)
		if err != nil || value.material.plaintext() != query.value {
			t.Fatal("version selection returned the wrong immutable value", err)
		}
		encoded, err := json.Marshal(value)
		if err != nil || strings.Contains(string(encoded), "DUMMY-") {
			t.Fatal("default store-result serialization exposed a secret")
		}
	}
	if value, err := s.Get(ctx, "us-east-1", created.ARN, a, "AWSCURRENT", allow); secretTestCode(err) != "InvalidRequestException" || value.material.plaintext() != "" {
		t.Fatal("mismatched selectors exposed a value")
	}
	s.now = func() time.Time { return time.UnixMilli(1800003600000) }
	if _, err = s.Put(ctx, "us-east-1", created.ARN, a, newSecretMaterial("DUMMY-A"), allow); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(ctx, "us-east-1", "training/versions", "must-not-change", a, newSecretMaterial("DUMMY-A"), allow); err != nil {
		t.Fatal(err)
	}
	after, err := s.Describe(ctx, "us-east-1", created.ARN, allow)
	if err != nil || !reflect.DeepEqual(meta, after) {
		t.Fatal("retry of an old version changed labels or metadata", err)
	}
	if _, err = s.Put(ctx, "us-east-1", created.ARN, a, newSecretMaterial("DUMMY-wrong"), allow); secretTestCode(err) != "ResourceExistsException" {
		t.Fatal("conflicting token was accepted")
	}
	if _, err = s.Promote(ctx, "us-east-1", created.ARN, a, c, allow); err != nil {
		t.Fatal(err)
	}
	meta, err = s.Describe(ctx, "us-east-1", created.ARN, allow)
	if err != nil || !reflect.DeepEqual(meta.VersionIdsToStages, map[string][]string{a: {"AWSCURRENT"}, c: {"AWSPREVIOUS"}}) {
		t.Fatal("rollback did not move labels atomically", err)
	}
	if _, err = s.Promote(ctx, "us-east-1", created.ARN, b, c, allow); secretTestCode(err) != "InvalidRequestException" {
		t.Fatal("stale label owner was accepted")
	}
	if _, err = s.Promote(ctx, "us-east-1", created.ARN, a, "", allow); err != nil {
		t.Fatal("same-owner promotion failed", err)
	}
}

func TestSecretLateLabelFailureRollsBackVersionAndMetadata(t *testing.T) {
	ctx := context.Background()
	db, s := secretTestStore(t)
	const a = "00000000-0000-0000-0000-000000000001"
	const b = "00000000-0000-0000-0000-000000000002"
	const c = "00000000-0000-0000-0000-000000000003"
	allow := func(string, string) error { return nil }
	created, err := s.Create(ctx, "us-east-1", "training/atomic", "", a, newSecretMaterial("DUMMY-A"), allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Put(ctx, "us-east-1", created.ARN, b, newSecretMaterial("DUMMY-B"), allow); err != nil {
		t.Fatal(err)
	}
	before, err := s.Describe(ctx, "us-east-1", created.ARN, allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec(`CREATE TRIGGER reject_new_secret_current BEFORE INSERT ON aws_secret_stages WHEN NEW.stage='AWSCURRENT' BEGIN SELECT RAISE(ABORT,'test current-stage failure'); END`); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.UnixMilli(1800003600000) }
	if _, err = s.Put(ctx, "us-east-1", created.ARN, c, newSecretMaterial("DUMMY-C"), allow); err == nil {
		t.Fatal("failed stage insert reported success")
	}
	after, err := s.Describe(ctx, "us-east-1", created.ARN, allow)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed put changed metadata or label ownership", err)
	}
	if _, err = s.Get(ctx, "us-east-1", created.ARN, c, "", allow); secretTestCode(err) != "ResourceNotFoundException" {
		t.Fatal("failed put retained a new version")
	}
	if _, err = db.Database().Exec("DROP TRIGGER reject_new_secret_current"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Put(ctx, "us-east-1", created.ARN, c, newSecretMaterial("DUMMY-C"), allow); err != nil {
		t.Fatal("corrected retry failed", err)
	}
}

func TestSecretGetAuthorizesSavedARNBeforeReadingAnyValue(t *testing.T) {
	ctx := context.Background()
	db, s := secretTestStore(t)
	const id = "00000000-0000-0000-0000-000000000001"
	allow := func(string, string) error { return nil }
	created, err := s.Create(ctx, "us-east-1", "training/read-boundary", "", id, newSecretMaterial("DUMMY-private"), allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec("ALTER TABLE aws_secret_versions RENAME TO unavailable_secret_versions"); err != nil {
		t.Fatal(err)
	}
	denied := func(action, arn string) error {
		if action != "secretsmanager:GetSecretValue" || arn != created.ARN {
			t.Fatal("Get did not resolve and authorize the saved ARN")
		}
		return secretFailure{"AccessDeniedException", "IAM evaluation: explicitDeny"}
	}
	result, err := s.Get(ctx, "us-east-1", "training/read-boundary", "", "", denied)
	if secretTestCode(err) != "AccessDeniedException" || result.material.plaintext() != "" {
		t.Fatal("denied Get attempted a value read")
	}
	if _, err = s.Get(ctx, "us-east-1", created.ARN, "", "", allow); err == nil || secretTestCode(err) != "infrastructure" {
		t.Fatal("unavailable value storage returned success or an IAM denial")
	}
}
