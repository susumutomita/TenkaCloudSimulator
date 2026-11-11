package aws

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestSecretMetadataUpdateLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.db")
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s, err := NewSecretStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	allow := func(string, string) error { return nil }
	ctx := context.Background()
	const token = "00000000-0000-0000-0000-000000000001"
	created, err := s.Create(ctx, "us-east-1", "metadata", "before", token, newSecretMaterial("DUMMY-private"), allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Put(ctx, "us-east-1", created.ARN, strings.Repeat("b", 32), newSecretMaterial("DUMMY-next"), allow); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(ctx, "us-west-2", "metadata", "peer", token, newSecretMaterial("DUMMY-peer"), allow); err != nil {
		t.Fatal(err)
	}
	before := secretStateSnapshot(t, db)
	// The expected whole state differs only in the two metadata columns.
	var oldChanged int64
	if err = db.Database().QueryRow("SELECT changed FROM aws_secrets WHERE arn=?", created.ARN).Scan(&oldChanged); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.UnixMilli(1900000000000) }
	for _, description := range []string{"replacement 秘密", "", strings.Repeat("界", 2048)} {
		calls := 0
		auth := func(action, arn string) error {
			calls++
			if action != "secretsmanager:UpdateSecret" || arn != created.ARN {
				t.Fatal("incorrect physical authority")
			}
			return nil
		}
		body := secretJSONFixture(t, map[string]any{"SecretId": "metadata", "Description": description, "ClientRequestToken": token})
		w := secretHTTPCall(s, "UpdateSecret", body, auth)
		var out map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out) != 2 || out["ARN"] != created.ARN || out["Name"] != "metadata" || calls != 1 {
			t.Fatal("metadata response/authority mismatch", w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Simulator-Encryption") != "none" {
			t.Fatal("missing confidentiality headers")
		}
		d, e := s.Describe(ctx, "us-east-1", created.ARN, allow)
		if e != nil || d.Description != description || d.LastChangedDate != 1900000000 {
			t.Fatal("description not persisted", e)
		}
	}
	// Restore only intended columns to compare every secret table, including key,
	// immutable values, stages, retired identities and other-region metadata.
	if _, err = db.Database().Exec("UPDATE aws_secrets SET description=?,changed=? WHERE arn=?", "before", oldChanged, created.ARN); err != nil {
		t.Fatal(err)
	}
	if before != secretStateSnapshot(t, db) {
		t.Fatal("metadata update changed unrelated state")
	}
	if _, err = s.UpdateDescription(ctx, "us-east-1", created.ARN, "after restart", allow); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err = NewSecretStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Describe(ctx, "us-east-1", created.ARN, allow)
	if err != nil || d.Description != "after restart" {
		t.Fatal("restart lost metadata", err)
	}
	if _, err = s.DeleteForce(ctx, "us-east-1", created.ARN, allow); err != nil {
		t.Fatal(err)
	}
	before = secretStateSnapshot(t, db)
	if _, err = s.UpdateDescription(ctx, "us-east-1", created.ARN, "gone", allow); secretTestCode(err) != "ResourceNotFoundException" {
		t.Fatal("deleted ARN adopted")
	}
	if before != secretStateSnapshot(t, db) {
		t.Fatal("deleted update changed state")
	}
}

func TestSecretMetadataUpdateRefusals(t *testing.T) {
	db, s := secretTestStore(t)
	allow := func(string, string) error { return nil }
	created, err := s.Create(context.Background(), "us-east-1", "metadata", "original", strings.Repeat("a", 32), newSecretMaterial("DUMMY-private"), allow)
	if err != nil {
		t.Fatal(err)
	}
	before := secretStateSnapshot(t, db)
	bodies := []string{`{}`, `{"SecretId":"metadata"}`, `{"SecretId":"metadata","Description":null}`, `{"SecretId":"metadata","Description":1}`, `{"SecretId":"metadata","Description":"a","Description":"b"}`, `{"secretid":"metadata","Description":"a"}`, `{"SecretId":"metadata","Description":"\ud800"}`, `{"SecretId":"metadata","Description":"a"} {}`, `{"SecretId":"metadata","Description":"a","ClientRequestToken":""}`, `{"SecretId":"metadata","Description":"a","ClientRequestToken":null}`}
	for _, field := range []string{"SecretString", "SecretBinary", "KmsKeyId", "RotationEnabled", "RotationLambdaARN", "Type", "Tags", "VersionStages", "Name"} {
		bodies = append(bodies, secretJSONFixture(t, map[string]any{"SecretId": "metadata", "Description": "a", field: "DUMMY-unsupported"}))
	}
	bodies = append(bodies, secretJSONFixture(t, map[string]any{"SecretId": "metadata", "Description": strings.Repeat("界", 2049)}))
	for _, body := range bodies {
		w := secretHTTPCall(s, "UpdateSecret", body, allow)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "InvalidParameterException") || strings.Contains(w.Body.String(), "DUMMY-") {
			t.Fatal("malformed request not refused safely", w.Code)
		}
		if before != secretStateSnapshot(t, db) {
			t.Fatal("malformed request changed whole state")
		}
	}
	denied := func(string, string) error { return secretFailure{"AccessDeniedException", "denied"} }
	for _, tc := range []struct {
		auth   SecretAuthorizer
		status int
	}{{denied, 403}, {nil, 500}} {
		w := secretHTTPCall(s, "UpdateSecret", `{"SecretId":"metadata","Description":"denied"}`, tc.auth)
		if w.Code != tc.status {
			t.Fatal("authority refusal failed", w.Code)
		}
		if before != secretStateSnapshot(t, db) {
			t.Fatal("denied update changed state")
		}
	}
	for _, id := range []string{"missing", "", strings.Replace(created.ARN, "us-east-1", "us-west-2", 1), strings.Replace(created.ARN, "000000000000", "123456789012", 1)} {
		if _, err = s.UpdateDescription(context.Background(), "us-east-1", id, "bad", allow); err == nil {
			t.Fatal("invalid target accepted")
		}
		if before != secretStateSnapshot(t, db) {
			t.Fatal("bad target changed state")
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.UpdateDescription(canceled, "us-east-1", "metadata", "bad", allow); err == nil {
		t.Fatal("canceled update accepted")
	}
	if _, err = s.UpdateDescription(context.Background(), "invalid-region", "metadata", "bad", allow); err == nil {
		t.Fatal("invalid region accepted")
	}
	if _, err = s.UpdateDescription(context.Background(), "us-east-1", "metadata", string([]byte{0xff}), allow); err == nil {
		t.Fatal("invalid utf8 accepted")
	}
	if before != secretStateSnapshot(t, db) {
		t.Fatal("refusal changed state")
	}
}

func TestSecretMetadataUpdateWriteFaults(t *testing.T) {
	for _, fault := range []string{
		`BEFORE UPDATE ON aws_secrets BEGIN SELECT RAISE(ABORT,'fault'); END`,
		`BEFORE UPDATE ON aws_secrets BEGIN SELECT RAISE(IGNORE); END`,
		`AFTER UPDATE ON aws_secrets BEGIN UPDATE aws_secrets SET description='drift' WHERE arn=NEW.arn; END`,
		`AFTER UPDATE ON aws_secrets BEGIN UPDATE aws_secrets SET changed=1 WHERE arn=NEW.arn; END`,
		`AFTER UPDATE ON aws_secrets BEGIN UPDATE aws_secrets SET created=1 WHERE arn=NEW.arn; END`,
		`AFTER UPDATE ON aws_secrets BEGIN UPDATE aws_secrets SET name='drift' WHERE arn=NEW.arn; END`,
		`AFTER UPDATE ON aws_secrets BEGIN DELETE FROM aws_secrets WHERE arn=NEW.arn; END`,
	} {
		t.Run(fault, func(t *testing.T) {
			db, s := secretTestStore(t)
			allow := func(string, string) error { return nil }
			_, err := s.Create(context.Background(), "us-east-1", "metadata", "old", strings.Repeat("a", 32), newSecretMaterial("DUMMY-private"), allow)
			if err != nil {
				t.Fatal(err)
			}
			before := secretStateSnapshot(t, db)
			if _, err = db.Database().Exec("CREATE TRIGGER metadata_fault " + fault); err != nil {
				t.Fatal(err)
			}
			w := secretHTTPCall(s, "UpdateSecret", `{"SecretId":"metadata","Description":"new"}`, allow)
			if w.Code != 500 || strings.Contains(w.Body.String(), "fault") || strings.Contains(w.Body.String(), "DUMMY-") {
				t.Fatal("write failure not safely reported", w.Code)
			}
			if before != secretStateSnapshot(t, db) {
				t.Fatal("write failure did not roll back whole state")
			}
			if _, err = db.Database().Exec("DROP TRIGGER metadata_fault"); err != nil {
				t.Fatal(err)
			}
			if _, err = s.UpdateDescription(context.Background(), "us-east-1", "metadata", "recovered", allow); err != nil {
				t.Fatal("recovery failed", err)
			}
		})
	}
}

func TestSecretMetadataUpdateColdStoreAndNoValueReads(t *testing.T) {
	db, s := secretTestStore(t)
	allow := func(string, string) error { return nil }
	before := secretStateSnapshot(t, db)
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"SecretId":"absent","Description":"cold"}`))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "secretsmanager.UpdateSecret")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r, "us-west-2", allow)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "ResourceNotFoundException") || before != secretStateSnapshot(t, db) {
		t.Fatal("cold region update mutated state")
	}
	_, err := s.Create(context.Background(), "us-east-1", "metadata", "old", strings.Repeat("a", 32), newSecretMaterial("DUMMY-private"), allow)
	if err != nil {
		t.Fatal(err)
	}
	// Renaming value storage proves the metadata path never queries it. Existing
	// foreign-key references follow the rename, so the metadata update is valid.
	if _, err = db.Database().Exec("ALTER TABLE aws_secret_versions RENAME TO metadata_unread_values"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateDescription(context.Background(), "us-east-1", "metadata", "new", allow); err != nil {
		t.Fatal("metadata path accessed value table", err)
	}
	if _, err = db.Database().Exec("ALTER TABLE aws_secrets RENAME TO metadata_missing_schema"); err != nil {
		t.Fatal(err)
	}
	w = secretHTTPCall(s, "UpdateSecret", `{"SecretId":"metadata","Description":"bad"}`, allow)
	if w.Code != 500 {
		t.Fatal("missing schema silently succeeded", w.Code)
	}
}

func TestSecretMetadataUpdateGatewayIAMAndIndependentStores(t *testing.T) {
	db, s := secretTestStore(t)
	peerDB, peer := secretTestStore(t)
	allow := func(string, string) error { return nil }
	for _, store := range []*SecretStore{s, peer} {
		if _, err := store.Create(context.Background(), "us-east-1", "metadata", "old", strings.Repeat("a", 32), newSecretMaterial("DUMMY-private"), allow); err != nil {
			t.Fatal(err)
		}
	}
	peerBefore := secretStateSnapshot(t, peerDB)
	identities := core.NewIdentities(db)
	gateway := &Gateway{Store: db, Secrets: s, Identities: identities}
	for _, effect := range []string{"Allow", "Deny"} {
		policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"` + effect + `","Action":"secretsmanager:UpdateSecret","Resource":"arn:aws:secretsmanager:us-east-1:000000000000:secret:metadata-*"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err = identities.Set("metadata-author", policy); err != nil {
			t.Fatal(err)
		}
		before := secretStateSnapshot(t, db)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"SecretId":"metadata","Description":"new"}`)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "secretsmanager.UpdateSecret")
		r.Header.Set("X-Simulator-Principal", "metadata-author")
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, r)
		cancel()
		if effect == "Allow" {
			if w.Code != 200 || w.Header().Get("X-Simulator-IAM-Decision") != "allowed" {
				t.Fatal("metadata-only permission failed", w.Code)
			}
		} else if w.Code != 403 || before != secretStateSnapshot(t, db) {
			t.Fatal("explicit IAM denial mutated state", w.Code)
		}
		if peerBefore != secretStateSnapshot(t, peerDB) {
			t.Fatal("independent team store changed")
		}
	}
}

func TestSecretMetadataUpdateTokenBoundsAndRepeatedTimestamp(t *testing.T) {
	db, s := secretTestStore(t)
	allow := func(string, string) error { return nil }
	if _, err := s.Create(context.Background(), "us-east-1", "metadata", "same", strings.Repeat("a", 32), newSecretMaterial("DUMMY-private"), allow); err != nil {
		t.Fatal(err)
	}
	var clock int64 = 1800000000000
	s.now = func() time.Time { return time.UnixMilli(clock) }
	for _, character := range []string{"a", "界"} {
		for _, length := range []int{31, 32, 64, 65} {
			before := secretStateSnapshot(t, db)
			clock += 1000
			w := secretHTTPCall(s, "UpdateSecret", secretJSONFixture(t, map[string]any{"SecretId": "metadata", "Description": "same", "ClientRequestToken": strings.Repeat(character, length)}), allow)
			if length == 31 || length == 65 {
				if w.Code != 400 || !strings.Contains(w.Body.String(), "InvalidParameterException") || before != secretStateSnapshot(t, db) {
					t.Fatal("token length refusal failed", length, w.Code)
				}
			} else {
				if w.Code != 200 {
					t.Fatal("valid token length refused", length, w.Code)
				}
				d, err := s.Describe(context.Background(), "us-east-1", "metadata", allow)
				if err != nil || d.Description != "same" || d.LastChangedDate != float64(clock)/1000 {
					t.Fatal("identical description failed to refresh timestamp", err)
				}
			}
		}
	}
}
