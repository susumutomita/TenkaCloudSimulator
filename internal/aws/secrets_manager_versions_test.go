package aws

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestSecretVersionListMetadataFilteringAndAuthorityFrames(t *testing.T) {
	db, s, arn := secretContractFixture(t)
	ctx := context.Background()
	if _, err := s.Put(ctx, "us-east-1", arn, secretContractC, newSecretMaterial("DUMMY-never-list-me"), secretContractAllow); err != nil {
		t.Fatal("seed", err)
	}
	before := secretStateSnapshot(t, db)
	calls := 0
	auth := func(action, resource string) error {
		calls++
		if action != "secretsmanager:ListSecretVersionIds" || resource != arn {
			t.Fatal("wrong authority target")
		}
		return nil
	}
	out, err := s.ListSecretVersionIds(ctx, "us-east-1", arn, SecretVersionListQuery{MaxResults: 100}, auth)
	if err != nil || calls != 1 || out.ARN != arn || len(out.Versions) != 2 || out.Versions[0].VersionId != secretContractB || out.Versions[1].VersionId != secretContractC {
		t.Fatal("labeled history contract", err)
	}
	if !reflect.DeepEqual(out.Versions[0].VersionStages, []string{"AWSPREVIOUS"}) || !reflect.DeepEqual(out.Versions[1].VersionStages, []string{"AWSCURRENT"}) {
		t.Fatal("labels not saved state")
	}
	full, err := s.ListSecretVersionIds(ctx, "us-east-1", out.Name, SecretVersionListQuery{MaxResults: 100, IncludeDeprecated: true}, auth)
	if err != nil || len(full.Versions) != 3 || full.Versions[0].VersionId != secretContractA {
		t.Fatal("deprecated history", err)
	}
	data, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SecretString", "SecretBinary", "DUMMY-", "LastAccessedDate", "KmsKeyIds"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatal("metadata exposed unsupported/value fields")
		}
	}
	var shape struct{ Versions []map[string]json.RawMessage }
	if json.Unmarshal(data, &shape) != nil || len(shape.Versions[0]) != 2 || shape.Versions[0]["VersionStages"] != nil {
		t.Fatal("deprecated omission contract")
	}
	if full.Versions[0].CreatedDate != 1800000000 {
		t.Fatal("created timestamp not stored seconds")
	}
	denied := func(string, string) error { return secretFailure{"AccessDeniedException", "Denied."} }
	out, err = s.ListSecretVersionIds(ctx, "us-east-1", arn, SecretVersionListQuery{MaxResults: 1, NextToken: "malformed"}, denied)
	if secretTestCode(err) != "AccessDeniedException" || out.ARN != "" {
		t.Fatal("cursor examined before permission or partial result")
	}
	if before != secretStateSnapshot(t, db) {
		t.Fatal("metadata/denied read changed persistent state")
	}
}

func TestSecretVersionListCursorScopeReopenAndRecreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "versions.db")
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s, err := NewSecretStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1800000000000)
	s.now = func() time.Time { return now }
	ctx := context.Background()
	a, err := s.Create(ctx, "us-east-1", "training/list", "", secretContractA, newSecretMaterial("DUMMY-A"), secretContractAllow)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{secretContractB, secretContractC} {
		if _, err = s.Put(ctx, "us-east-1", a.ARN, id, newSecretMaterial("DUMMY-"+id), secretContractAllow); err != nil {
			t.Fatal(err)
		}
	}
	q := SecretVersionListQuery{MaxResults: 1, IncludeDeprecated: true}
	first, err := s.ListSecretVersionIds(ctx, "us-east-1", a.ARN, q, secretContractAllow)
	if err != nil || first.NextToken == "" || first.Versions[0].VersionId != secretContractA {
		t.Fatal("first page", err)
	}
	if _, err = s.Create(ctx, "us-east-1", "training/peer", "", secretContractA, newSecretMaterial("DUMMY-other"), secretContractAllow); err != nil {
		t.Fatal(err)
	}
	_, oldToken, err := s.List(ctx, "us-east-1", SecretListQuery{MaxResults: 1}, secretContractAllow)
	if err != nil || oldToken == "" {
		t.Fatal("old cursor fixture", err)
	}
	if _, err = s.ListSecretVersionIds(ctx, "us-east-1", a.ARN, SecretVersionListQuery{MaxResults: 1, NextToken: oldToken}, secretContractAllow); secretTestCode(err) != "InvalidNextTokenException" {
		t.Fatal("old list cursor crossed operation")
	}
	if _, _, err = s.List(ctx, "us-east-1", SecretListQuery{MaxResults: 1, NextToken: first.NextToken}, secretContractAllow); err == nil {
		t.Fatal("version cursor crossed old operation")
	}
	before := secretStateSnapshot(t, db)
	q.NextToken = first.NextToken
	_, foreign := secretTestStore(t)
	if _, err = foreign.Create(ctx, "us-east-1", "training/list", "", secretContractA, newSecretMaterial("DUMMY-peer"), secretContractAllow); err != nil {
		t.Fatal(err)
	}
	if _, err = foreign.ListSecretVersionIds(ctx, "us-east-1", "training/list", q, secretContractAllow); secretTestCode(err) != "InvalidNextTokenException" {
		t.Fatal("foreign token accepted")
	}
	for _, test := range []SecretVersionListQuery{{MaxResults: 1, NextToken: first.NextToken}, {MaxResults: 1, IncludeDeprecated: true, NextToken: first.NextToken + "x"}} {
		if _, err = s.ListSecretVersionIds(ctx, "us-east-1", a.ARN, test, secretContractAllow); secretTestCode(err) != "InvalidNextTokenException" {
			t.Fatal("query/tamper cursor accepted")
		}
	}
	if _, err = s.ListSecretVersionIds(ctx, "us-west-2", a.ARN, q, secretContractAllow); secretTestCode(err) != "InvalidParameterException" {
		t.Fatal("cross-region ARN accepted")
	}
	now = now.Add(25 * time.Hour)
	if _, err = s.ListSecretVersionIds(ctx, "us-east-1", a.ARN, q, secretContractAllow); secretTestCode(err) != "InvalidNextTokenException" {
		t.Fatal("expired cursor accepted")
	}
	now = now.Add(-25 * time.Hour)
	if before != secretStateSnapshot(t, db) {
		t.Fatal("cursor errors changed graph")
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
	s.now = func() time.Time { return now }
	second, err := s.ListSecretVersionIds(ctx, "us-east-1", a.ARN, q, secretContractAllow)
	if err != nil || len(second.Versions) != 1 || second.Versions[0].VersionId != secretContractB {
		t.Fatal("restarted cursor lost position", err)
	}
	q.NextToken = second.NextToken
	last, err := s.ListSecretVersionIds(ctx, "us-east-1", a.ARN, q, secretContractAllow)
	if err != nil || len(last.Versions) != 1 || last.Versions[0].VersionId != secretContractC || last.NextToken != "" {
		t.Fatal("cursor did not converge", err)
	}
	q.NextToken = first.NextToken
	if _, err = s.DeleteForce(ctx, "us-east-1", a.ARN, secretContractAllow); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListSecretVersionIds(ctx, "us-east-1", a.ARN, q, secretContractAllow); secretTestCode(err) != "ResourceNotFoundException" {
		t.Fatal("deleted identity listed")
	}
	b, err := s.Create(ctx, "us-east-1", "training/list", "", secretContractA, newSecretMaterial("DUMMY-recreated"), secretContractAllow)
	if err != nil || a.ARN == b.ARN {
		t.Fatal("fresh identity", err)
	}
	if _, err = s.ListSecretVersionIds(ctx, "us-east-1", b.Name, q, secretContractAllow); secretTestCode(err) != "InvalidNextTokenException" {
		t.Fatal("cursor adopted new incarnation")
	}
	// The list is a live keyset, not a snapshot or authority token: deleting the
	// old parent cannot be bypassed by addressing its former physical ARN.
	if _, err = s.ListSecretVersionIds(ctx, "us-east-1", a.ARN, SecretVersionListQuery{MaxResults: 100}, secretContractAllow); secretTestCode(err) != "ResourceNotFoundException" {
		t.Fatal("old ARN adopted replacement")
	}
}

func TestSecretVersionListStrictRequestAndSanitizedReadFailures(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `[]`, `{"SecretId":null}`, `{"SecretId":"x","IncludeDeprecated":null}`, `{"SecretId":"x","MaxResults":null}`, `{"SecretId":"x","NextToken":""}`, `{"SecretId":"x","MaxResults":0}`, `{"SecretId":"x","MaxResults":101}`, `{"SecretId":"x","MaxResults":1.0}`, `{"SecretId":"x","IncludeDeprecated":"true"}`, `{"SecretId":"x","SecretId":"x"}`, `{"secretId":"x"}`, `{"SecretId":"x","SortBy":"name"}`, `{"SecretId":"\ud800"}`, `{"SecretId":"x"} {}`} {
		if _, _, err := decodeSecretVersionListRequest([]byte(body)); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	id, q, err := decodeSecretVersionListRequest([]byte(`{"SecretId":"training/a+%","IncludeDeprecated":true,"MaxResults":1}`))
	if err != nil || id != "training/a+%" || !q.IncludeDeprecated || q.MaxResults != 1 {
		t.Fatal("valid body modified", err)
	}
	for _, fault := range []string{"version-time", "version-id", "stage", "read-table", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			db, s, arn := secretContractFixture(t)
			ctx := context.Background()
			repair := ""
			restoreTrigger := ""
			switch fault {
			case "version-time":
				if err = db.Database().QueryRow("SELECT sql FROM sqlite_master WHERE type='trigger' AND name='aws_secret_versions_immutable'").Scan(&restoreTrigger); err != nil {
					t.Fatal("trigger fixture")
				}
				if _, err = db.Database().Exec("DROP TRIGGER aws_secret_versions_immutable"); err != nil {
					t.Fatal("corruption fixture")
				}
				_, err = db.Database().Exec("UPDATE aws_secret_versions SET created=-1 WHERE version_id=?", secretContractA)
				repair = "UPDATE aws_secret_versions SET created=1800000000000 WHERE created=-1"
			case "version-id":
				_, err = db.Database().Exec("INSERT INTO aws_secret_versions VALUES(?,?,?,?,?)", "us-east-1", arn, "bad", []byte("DUMMY"), 0)
				repair = "DELETE FROM aws_secret_versions WHERE version_id='bad'"
			case "stage":
				_, err = db.Database().Exec("DELETE FROM aws_secret_stages WHERE stage='AWSCURRENT'")
				repair = "INSERT INTO aws_secret_stages VALUES('us-east-1','" + arn + "','AWSCURRENT','" + secretContractB + "')"
			case "read-table":
				_, err = db.Database().Exec("ALTER TABLE aws_secret_versions RENAME TO version_fault_private")
				repair = "ALTER TABLE version_fault_private RENAME TO aws_secret_versions"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err != nil {
				t.Fatal("fault fixture", err)
			}
			// Snapshot when schema is normal; renamed-table case checks repaired full frame.
			before := ""
			if fault != "read-table" {
				before = secretStateSnapshot(t, db)
			}
			out, e := s.ListSecretVersionIds(ctx, "us-east-1", arn, SecretVersionListQuery{MaxResults: 100}, secretContractAllow)
			if e == nil || out.ARN != "" || len(out.Versions) != 0 {
				t.Fatal("infrastructure failure emitted metadata")
			}
			w := httptest.NewRecorder()
			secretHTTPError(w, e)
			if w.Code != 500 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "DUMMY") || strings.Contains(w.Body.String(), "SELECT") {
				t.Fatal("read failure not sanitized")
			}
			if before != "" && before != secretStateSnapshot(t, db) {
				t.Fatal("failed read changed state")
			}
			if repair != "" {
				if _, err = db.Database().Exec(repair); err != nil {
					t.Fatal("repair fixture", err)
				}
			}
			if restoreTrigger != "" {
				if _, err = db.Database().Exec(restoreTrigger); err != nil {
					t.Fatal("restore immutable trigger")
				}
			}
			if _, err = s.ListSecretVersionIds(context.Background(), "us-east-1", arn, SecretVersionListQuery{MaxResults: 100}, secretContractAllow); err != nil {
				t.Fatal("repaired read did not recover", err)
			}
		})
	}
	var nilStore *SecretStore
	if _, err = nilStore.ListSecretVersionIds(context.Background(), "us-east-1", "x", SecretVersionListQuery{MaxResults: 1}, secretContractAllow); err == nil {
		t.Fatal("nil composition accepted")
	}
}

func TestSecretVersionListLiveStageChangesAndEmptyContinuation(t *testing.T) {
	db, s, arn := secretContractFixture(t)
	ctx := context.Background()
	q := SecretVersionListQuery{MaxResults: 1}
	first, err := s.ListSecretVersionIds(ctx, "us-east-1", arn, q, secretContractAllow)
	if err != nil || first.NextToken == "" {
		t.Fatal("first labeled page", err)
	}
	if _, err = s.Put(ctx, "us-east-1", arn, secretContractC, newSecretMaterial("DUMMY-C"), secretContractAllow); err != nil {
		t.Fatal(err)
	}
	q.NextToken = first.NextToken
	page, err := s.ListSecretVersionIds(ctx, "us-east-1", arn, q, secretContractAllow)
	if err != nil || len(page.Versions) != 1 || page.Versions[0].VersionId != secretContractB {
		t.Fatal("live membership position", err)
	}
	before := secretStateSnapshot(t, db)
	// Current max-results can change without changing selection authority.
	q.MaxResults = 100
	q.NextToken = page.NextToken
	last, err := s.ListSecretVersionIds(ctx, "us-east-1", arn, q, secretContractAllow)
	if err != nil || len(last.Versions) != 1 || last.Versions[0].VersionId != secretContractC {
		t.Fatal("live last page", err)
	}
	if before != secretStateSnapshot(t, db) {
		t.Fatal("continuation mutated history")
	}
	// Between pages, both labels can move before the saved position. A valid
	// continuation then returns an actual empty array and no further token.
	for _, move := range [][2]string{{secretContractA, secretContractC}, {secretContractB, secretContractA}} {
		if _, err = s.Promote(ctx, "us-east-1", arn, move[0], move[1], secretContractAllow); err != nil {
			t.Fatal(err)
		}
	}
	before = secretStateSnapshot(t, db)
	empty, err := s.ListSecretVersionIds(ctx, "us-east-1", arn, q, secretContractAllow)
	if err != nil || empty.Versions == nil || len(empty.Versions) != 0 || empty.NextToken != "" {
		t.Fatal("live empty continuation did not converge", err)
	}
	encoded, err := json.Marshal(empty)
	if err != nil || !strings.Contains(string(encoded), `"Versions":[]`) {
		t.Fatal("empty versions not an array")
	}
	if before != secretStateSnapshot(t, db) {
		t.Fatal("empty continuation mutated state")
	}
}
