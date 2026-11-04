package aws

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const secretContractA = "00000000-0000-0000-0000-000000000001"
const secretContractB = "00000000-0000-0000-0000-000000000002"
const secretContractC = "00000000-0000-0000-0000-000000000003"

var secretContractAllow SecretAuthorizer = func(string, string) error { return nil }

func secretContractFixture(t *testing.T) (*core.Store, *SecretStore, string) {
	t.Helper()
	db, s := secretTestStore(t)
	a, err := s.Create(context.Background(), "us-east-1", "training/storage-contract", "", secretContractA, newSecretMaterial("DUMMY-storage-A"), secretContractAllow)
	if err != nil {
		t.Fatal("cannot create storage fixture")
	}
	if _, err = s.Put(context.Background(), "us-east-1", a.ARN, secretContractB, newSecretMaterial("DUMMY-storage-B"), secretContractAllow); err != nil {
		t.Fatal("cannot seed version history")
	}
	s.now = func() time.Time { return time.UnixMilli(1800003600000) }
	return db, s, a.ARN
}

func secretContractMutation(s *SecretStore, arn, op string) error {
	ctx := context.Background()
	switch op {
	case "create":
		_, err := s.Create(ctx, "us-east-1", "training/fault-new", "", secretContractC, newSecretMaterial("DUMMY-storage-C"), secretContractAllow)
		return err
	case "put":
		_, err := s.Put(ctx, "us-east-1", arn, secretContractC, newSecretMaterial("DUMMY-storage-C"), secretContractAllow)
		return err
	case "promote":
		_, err := s.Promote(ctx, "us-east-1", arn, secretContractA, secretContractB, secretContractAllow)
		return err
	case "delete":
		_, err := s.DeleteForce(ctx, "us-east-1", arn, secretContractAllow)
		return err
	default:
		panic("unknown test operation")
	}
}

func TestSecretStorageWriteAndDeferredCommitFaultsRollbackAndRetry(t *testing.T) {
	cases := []struct {
		name, op, table, event string
		deferred               bool
	}{
		{name: "create ARN insert", op: "create", table: "aws_secret_arns", event: "INSERT"},
		{name: "create metadata insert", op: "create", table: "aws_secrets", event: "INSERT"},
		{name: "create version insert", op: "create", table: "aws_secret_versions", event: "INSERT"},
		{name: "put version insert", op: "put", table: "aws_secret_versions", event: "INSERT"},
		{name: "put remove old labels", op: "put", table: "aws_secret_stages", event: "DELETE"},
		{name: "promote remove old labels", op: "promote", table: "aws_secret_stages", event: "DELETE"},
		{name: "promote metadata write", op: "promote", table: "aws_secrets", event: "UPDATE"},
		{name: "delete parent row", op: "delete", table: "aws_secrets", event: "DELETE"},
		{name: "create commit constraint", op: "create", table: "aws_secrets", event: "INSERT", deferred: true},
		{name: "put commit constraint", op: "put", table: "aws_secrets", event: "UPDATE", deferred: true},
		{name: "promote commit constraint", op: "promote", table: "aws_secrets", event: "UPDATE", deferred: true},
		{name: "delete commit constraint", op: "delete", table: "aws_secrets", event: "DELETE", deferred: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, s, arn := secretContractFixture(t)
			before := secretStateSnapshot(t, db)
			action := "SELECT RAISE(ABORT,'storage contract fault');"
			if tc.deferred {
				if _, err := db.Database().Exec("CREATE TABLE secret_fault_parent(id INTEGER PRIMARY KEY); CREATE TABLE secret_fault_child(parent INTEGER REFERENCES secret_fault_parent(id) DEFERRABLE INITIALLY DEFERRED)"); err != nil {
					t.Fatal("cannot install deferred constraint")
				}
				action = "INSERT INTO secret_fault_child VALUES(1);"
			}
			trigger := "CREATE TRIGGER secret_contract_fault AFTER " + tc.event + " ON " + tc.table + " BEGIN " + action + " END"
			if _, err := db.Database().Exec(trigger); err != nil {
				t.Fatal("cannot install real SQLite fault")
			}
			err := secretContractMutation(s, arn, tc.op)
			if err == nil || secretTestCode(err) != "infrastructure" {
				t.Fatal("failed SQL mutation was acknowledged or mislabeled")
			}
			if before != secretStateSnapshot(t, db) {
				t.Fatal("failed SQL mutation changed private durable state")
			}
			if tc.deferred {
				var count int
				if err = db.Database().QueryRow("SELECT count(*) FROM secret_fault_child").Scan(&count); err != nil || count != 0 {
					t.Fatal("failed commit retained side effect")
				}
			}
			if _, err = db.Database().Exec("DROP TRIGGER secret_contract_fault"); err != nil {
				t.Fatal("cannot clear test fault")
			}
			if err = secretContractMutation(s, arn, tc.op); err != nil {
				t.Fatal("repaired mutation did not retry cleanly")
			}
			if tc.op == "delete" {
				if _, err = s.Get(context.Background(), "us-east-1", arn, "", "", secretContractAllow); secretTestCode(err) != "ResourceNotFoundException" {
					t.Fatal("successful delete retained value")
				}
				return
			}
			id, expected := arn, "DUMMY-storage-A"
			if tc.op == "create" {
				id = "training/fault-new"
				expected = "DUMMY-storage-C"
			} else if tc.op == "put" {
				expected = "DUMMY-storage-C"
			}
			got, err := s.Get(context.Background(), "us-east-1", id, "", "", secretContractAllow)
			if err != nil || got.material.plaintext() != expected {
				t.Fatal("retry did not produce requested value")
			}
		})
	}
}

func TestSecretStorageUnavailableTablesAndCorruptMetadataFailClosed(t *testing.T) {
	for _, fault := range []string{"versions unavailable", "stages unavailable", "identity mismatch", "created not integer", "changed not integer"} {
		t.Run(fault, func(t *testing.T) {
			db, s, arn := secretContractFixture(t)
			before := secretStateSnapshot(t, db)
			install, repair := "", ""
			switch fault {
			case "versions unavailable":
				install = "ALTER TABLE aws_secret_versions RENAME TO secret_unavailable_versions"
				repair = "ALTER TABLE secret_unavailable_versions RENAME TO aws_secret_versions"
			case "stages unavailable":
				install = "ALTER TABLE aws_secret_stages RENAME TO secret_unavailable_stages"
				repair = "ALTER TABLE secret_unavailable_stages RENAME TO aws_secret_stages"
			case "identity mismatch":
				install = "UPDATE aws_secrets SET name='training/wrong-identity'"
				repair = "UPDATE aws_secrets SET name='training/storage-contract'"
			case "created not integer":
				install = "UPDATE aws_secrets SET created='invalid persisted timestamp'"
				repair = "UPDATE aws_secrets SET created=1800000000000"
			case "changed not integer":
				install = "UPDATE aws_secrets SET changed='invalid persisted timestamp'"
				repair = "UPDATE aws_secrets SET changed=1800000000000"
			}
			if _, err := db.Database().Exec(install); err != nil {
				t.Fatal("cannot install persisted failure")
			}
			// Only metadata queries may remain available when value storage is absent.
			// This also verifies that they do not secretly query a raw value table.
			ctx := context.Background()
			if fault == "versions unavailable" {
				if _, err := s.Describe(ctx, "us-east-1", arn, secretContractAllow); err != nil {
					t.Fatal("metadata unexpectedly requires value table")
				}
				if _, _, err := s.List(ctx, "us-east-1", SecretListQuery{MaxResults: 10}, secretContractAllow); err != nil {
					t.Fatal("listing unexpectedly requires value table")
				}
			}
			if fault == "stages unavailable" {
				// Explicit immutable-version reads load bytes before obtaining
				// stage metadata. A late failure must not return partial data.
				value, err := s.Get(ctx, "us-east-1", arn, secretContractB, "", secretContractAllow)
				if err == nil || secretTestCode(err) != "infrastructure" || value.material.plaintext() != "" || value.ARN != "" {
					t.Fatal("late stage lookup returned partial secret value")
				}
				response := secretHTTPCall(s, "GetSecretValue", secretJSONFixture(t, map[string]any{"SecretId": arn, "VersionId": secretContractB}), secretContractAllow)
				if response.Code != 500 || strings.Contains(response.Body.String(), "DUMMY-") || strings.Contains(response.Body.String(), "SecretString") || strings.Contains(response.Body.String(), "secret_unavailable_stages") {
					t.Fatal("late stage lookup disclosed value or storage details on native HTTP")
				}
				replay, err := s.Put(ctx, "us-east-1", arn, secretContractB, newSecretMaterial("DUMMY-storage-B"), secretContractAllow)
				if err == nil || secretTestCode(err) != "infrastructure" || replay.VersionId != "" {
					t.Fatal("same-token replay falsely acknowledged unavailable stage metadata")
				}
			}
			operations := []string{"get", "put", "promote"}
			if fault != "versions unavailable" {
				operations = append(operations, "describe", "list")
			}
			if fault == "identity mismatch" || strings.Contains(fault, "not integer") {
				operations = append(operations, "delete")
			}
			for _, op := range operations {
				var err error
				switch op {
				case "get":
					_, err = s.Get(ctx, "us-east-1", arn, "", "", secretContractAllow)
				case "describe":
					_, err = s.Describe(ctx, "us-east-1", arn, secretContractAllow)
				case "list":
					_, _, err = s.List(ctx, "us-east-1", SecretListQuery{MaxResults: 10}, secretContractAllow)
				default:
					err = secretContractMutation(s, arn, op)
				}
				if err == nil || secretTestCode(err) != "infrastructure" {
					t.Fatal("persisted fault did not fail closed", op)
				}
			}
			if fault == "versions unavailable" {
				if _, err := s.Create(ctx, "us-east-1", "training/storage-contract", "", secretContractA, newSecretMaterial("DUMMY-storage-A"), secretContractAllow); err == nil {
					t.Fatal("creation retry ignored unavailable immutable history")
				}
				if err := secretContractMutation(s, arn, "create"); err == nil {
					t.Fatal("creation ignored missing version storage")
				}
			}
			if _, err := db.Database().Exec(repair); err != nil {
				t.Fatal("cannot restore persisted fixture")
			}
			if before != secretStateSnapshot(t, db) {
				t.Fatal("failed query or mutation changed durable private state")
			}
			if fault == "stages unavailable" {
				value, err := s.Get(ctx, "us-east-1", arn, secretContractB, "", secretContractAllow)
				if err != nil || value.material.plaintext() != "DUMMY-storage-B" || len(value.VersionStages) != 1 || value.VersionStages[0] != "AWSCURRENT" {
					t.Fatal("repaired explicit-version read did not return actual value/stage")
				}
				replay, err := s.Put(ctx, "us-east-1", arn, secretContractB, newSecretMaterial("DUMMY-storage-B"), secretContractAllow)
				if err != nil || replay.VersionId != secretContractB || replay.ARN != arn {
					t.Fatal("repaired immutable replay did not succeed")
				}
				if before != secretStateSnapshot(t, db) {
					t.Fatal("repaired same-token replay changed private state")
				}
			}
			if _, err := s.Put(ctx, "us-east-1", arn, secretContractC, newSecretMaterial("DUMMY-storage-C"), secretContractAllow); err != nil {
				t.Fatal("repaired state could not accept clean retry")
			}
		})
	}
}

func TestSecretStorageStageSelectorsRefuseAndPreserveCurrent(t *testing.T) {
	db, s, arn := secretContractFixture(t)
	ctx := context.Background()
	before := secretStateSnapshot(t, db)
	cases := []struct{ target, remove, code string }{
		{secretContractA, "", "InvalidRequestException"},
		{secretContractA, secretContractA, "InvalidRequestException"},
		{secretContractC, secretContractB, "ResourceNotFoundException"},
		{"short", secretContractB, "InvalidParameterException"},
		{secretContractA, "short", "InvalidParameterException"},
	}
	for _, tc := range cases {
		if _, err := s.Promote(ctx, "us-east-1", arn, tc.target, tc.remove, secretContractAllow); secretTestCode(err) != tc.code {
			t.Fatal("promotion accepted invalid ownership/target")
		}
		if before != secretStateSnapshot(t, db) {
			t.Fatal("refused promotion changed state")
		}
	}
	if _, err := s.Promote(ctx, "us-east-1", arn, secretContractB, "", secretContractAllow); err != nil {
		t.Fatal("idempotent promotion failed")
	}
	if before != secretStateSnapshot(t, db) {
		t.Fatal("idempotent promotion changed metadata")
	}
	if _, err := db.Database().Exec("DELETE FROM aws_secret_stages WHERE stage='AWSCURRENT'"); err != nil {
		t.Fatal("cannot install missing-current fault")
	}
	faulted := secretStateSnapshot(t, db)
	if _, err := s.Put(ctx, "us-east-1", arn, secretContractC, newSecretMaterial("DUMMY-storage-C"), secretContractAllow); err == nil {
		t.Fatal("Put guessed missing current version")
	}
	if _, err := s.Promote(ctx, "us-east-1", arn, secretContractA, secretContractB, secretContractAllow); err == nil {
		t.Fatal("promotion guessed missing current version")
	}
	if _, err := s.Get(ctx, "us-east-1", arn, "", "AWSCURRENT", secretContractAllow); secretTestCode(err) != "ResourceNotFoundException" {
		t.Fatal("missing current selector disclosed fallback version")
	}
	if faulted != secretStateSnapshot(t, db) {
		t.Fatal("refused repair guessed new label ownership")
	}
	if _, err := db.Database().Exec("INSERT INTO aws_secret_stages VALUES('us-east-1',?,'AWSCURRENT',?)", arn, secretContractB); err != nil {
		t.Fatal("cannot restore known current label")
	}
	if _, err := s.Put(ctx, "us-east-1", arn, secretContractC, newSecretMaterial("DUMMY-storage-C"), secretContractAllow); err != nil {
		t.Fatal("repaired current label did not permit retry")
	}
}

func TestSecretStorageCursorOrderingTamperingExpiryAndQueryFrame(t *testing.T) {
	db, s := secretTestStore(t)
	ctx := context.Background()
	arns := []string{}
	for _, name := range []string{"training/b", "training/a", "training/c"} {
		m, err := s.Create(ctx, "us-east-1", name, "", secretContractA, newSecretMaterial("DUMMY-cursor"), secretContractAllow)
		if err != nil {
			t.Fatal("cannot create cursor fixture")
		}
		arns = append(arns, m.ARN)
	}
	if _, err := s.Create(ctx, "us-west-2", "training/peer", "", secretContractA, newSecretMaterial("DUMMY-cursor-peer"), secretContractAllow); err != nil {
		t.Fatal("cannot create region peer")
	}
	before := secretStateSnapshot(t, db)
	for _, by := range []string{"name", "created-date"} {
		for _, direction := range []string{"asc", "desc"} {
			got := []string{}
			q := SecretListQuery{MaxResults: 1, SortBy: by, SortOrder: direction}
			seen := map[string]bool{}
			for {
				items, next, err := s.List(ctx, "us-east-1", q, secretContractAllow)
				if err != nil || len(items) != 1 {
					t.Fatal("bounded page did not contain exact local row")
				}
				if by == "name" {
					got = append(got, items[0].Name)
				} else {
					got = append(got, items[0].ARN)
				}
				if next == "" {
					break
				}
				if seen[next] {
					t.Fatal("cursor cycled")
				}
				seen[next] = true
				q.NextToken = next
			}
			expected := []string{"training/a", "training/b", "training/c"}
			if by == "created-date" {
				expected = append([]string(nil), arns...)
				sort.Strings(expected)
			}
			if direction == "desc" {
				for a, b := 0, len(expected)-1; a < b; a, b = a+1, b-1 {
					expected[a], expected[b] = expected[b], expected[a]
				}
			}
			if strings.Join(got, "\n") != strings.Join(expected, "\n") {
				t.Fatal("pagination aliased region, timestamp tie or descending position")
			}
		}
	}
	_, token, err := s.List(ctx, "us-east-1", SecretListQuery{MaxResults: 1}, secretContractAllow)
	if err != nil || token == "" {
		t.Fatal("cannot obtain opaque cursor")
	}
	for _, q := range []SecretListQuery{{MaxResults: 0}, {MaxResults: 101}, {MaxResults: 1, SortBy: "last-accessed-date"}, {MaxResults: 1, SortOrder: "sideways"}, {MaxResults: 1, NextToken: "missing-signature"}, {MaxResults: 1, NextToken: "!.!"}, {MaxResults: 1, NextToken: token + "extra"}, {MaxResults: 1, NextToken: strings.Repeat("x", 4097)}} {
		if _, _, err = s.List(ctx, "us-east-1", q, secretContractAllow); secretTestCode(err) != "InvalidParameterException" {
			t.Fatal("invalid query/cursor was accepted")
		}
		if before != secretStateSnapshot(t, db) {
			t.Fatal("invalid query mutated private state")
		}
	}
	originalNow := s.now
	s.now = func() time.Time { return originalNow().Add(24*time.Hour + time.Millisecond) }
	if _, _, err = s.List(ctx, "us-east-1", SecretListQuery{MaxResults: 1, NextToken: token}, secretContractAllow); secretTestCode(err) != "InvalidParameterException" {
		t.Fatal("expired cursor was accepted")
	}
	s.now = originalNow
	if before != secretStateSnapshot(t, db) {
		t.Fatal("cursor expiry mutated persistent state")
	}
	if _, _, err = s.List(ctx, "us-east-1", SecretListQuery{MaxResults: 1, NextToken: token}, secretContractAllow); err != nil {
		t.Fatal("restored valid cursor could not continue")
	}
}

// Stream every private column into a length-framed digest; do not retain or log
// the large quota fixture's actual plaintext or cursor key in an assertion.
func secretContractFingerprint(t *testing.T, db *core.Store) string {
	t.Helper()
	h := sha256.New()
	for _, q := range []string{"SELECT * FROM aws_secret_settings ORDER BY id", "SELECT * FROM aws_secret_arns ORDER BY arn", "SELECT * FROM aws_secrets ORDER BY region,arn", "SELECT * FROM aws_secret_versions ORDER BY region,arn,version_id", "SELECT * FROM aws_secret_stages ORDER BY region,arn,stage"} {
		h.Write([]byte("table:" + q + "\x00"))
		rows, err := db.Database().Query(q)
		if err != nil {
			t.Fatal("cannot read fingerprint")
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal("cannot read fingerprint columns")
		}
		for rows.Next() {
			h.Write([]byte("row\x00"))
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				rows.Close()
				t.Fatal("cannot read fingerprint row")
			}
			for _, v := range vals {
				var b []byte
				var tag byte
				switch x := v.(type) {
				case []byte:
					tag = 1
					b = x
				case string:
					tag = 2
					b = []byte(x)
				case int64:
					tag = 3
					b = []byte(fmt.Sprintf("integer:%d", x))
				default:
					rows.Close()
					t.Fatal("unexpected private cell type")
				}
				var n [8]byte
				binary.BigEndian.PutUint64(n[:], uint64(len(b)))
				h.Write([]byte{tag})
				h.Write(n[:])
				h.Write(b)
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal("cannot finish fingerprint")
		}
		rows.Close()
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestSecretStorageLocalCapsRetainFramesAndPermitSupportedRelief(t *testing.T) {
	t.Run("100 live secrets", func(t *testing.T) {
		db, s := secretTestStore(t)
		ctx := context.Background()
		first := ""
		for i := 0; i < 100; i++ {
			m, err := s.Create(ctx, "us-east-1", fmt.Sprintf("training/cap-%03d", i), "", secretContractA, newSecretMaterial("DUMMY-cap"), secretContractAllow)
			if err != nil {
				t.Fatal("documented secret capacity rejected early")
			}
			if i == 0 {
				first = m.ARN
			}
		}
		before := secretStateSnapshot(t, db)
		if _, err := s.Create(ctx, "us-east-1", "training/cap-new", "", secretContractA, newSecretMaterial("DUMMY-cap"), secretContractAllow); secretTestCode(err) != "LimitExceededException" {
			t.Fatal("live secret cap not enforced")
		}
		if before != secretStateSnapshot(t, db) {
			t.Fatal("secret cap left a partial reservation")
		}
		if _, err := s.DeleteForce(ctx, "us-east-1", first, secretContractAllow); err != nil {
			t.Fatal("cannot release live slot")
		}
		if _, err := s.Create(ctx, "us-east-1", "training/cap-new", "", secretContractA, newSecretMaterial("DUMMY-cap"), secretContractAllow); err != nil {
			t.Fatal("deleted live slot did not permit retry")
		}
	})
	t.Run("100 immutable versions", func(t *testing.T) {
		db, s := secretTestStore(t)
		ctx := context.Background()
		m, err := s.Create(ctx, "us-east-1", "training/version-cap", "", secretContractA, newSecretMaterial("DUMMY-cap"), secretContractAllow)
		if err != nil {
			t.Fatal("cannot create version fixture")
		}
		for i := 2; i <= 100; i++ {
			if _, err = s.Put(ctx, "us-east-1", m.ARN, fmt.Sprintf("%036d", i), newSecretMaterial("DUMMY-cap"), secretContractAllow); err != nil {
				t.Fatal("version capacity rejected early")
			}
		}
		before := secretStateSnapshot(t, db)
		if _, err = s.Put(ctx, "us-east-1", m.ARN, fmt.Sprintf("%036d", 101), newSecretMaterial("DUMMY-cap"), secretContractAllow); secretTestCode(err) != "LimitExceededException" {
			t.Fatal("per-secret version cap not enforced")
		}
		if before != secretStateSnapshot(t, db) {
			t.Fatal("version cap changed labels or values")
		}
		if _, err = s.Put(ctx, "us-east-1", m.ARN, secretContractA, newSecretMaterial("DUMMY-cap"), secretContractAllow); err != nil {
			t.Fatal("capacity blocked immutable same-token retry")
		}
		if before != secretStateSnapshot(t, db) {
			t.Fatal("same-token retry at capacity changed state")
		}
	})
	t.Run("10000 retired physical identities", func(t *testing.T) {
		db, s := secretTestStore(t)
		ctx := context.Background()
		if _, err := db.Database().Exec(`WITH RECURSIVE ids(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM ids WHERE n<10000) INSERT INTO aws_secret_arns SELECT 'arn:aws:secretsmanager:us-east-1:000000000000:secret:training/retired-'||printf('%06d',n),'us-east-1','training/retired',0 FROM ids`); err != nil {
			t.Fatal("cannot seed real retired rows")
		}
		before := secretContractFingerprint(t, db)
		if _, err := s.Create(ctx, "us-east-1", "training/lifetime-cap", "", secretContractA, newSecretMaterial("DUMMY-cap"), secretContractAllow); secretTestCode(err) != "LimitExceededException" {
			t.Fatal("lifetime physical identity cap not enforced")
		}
		if before != secretContractFingerprint(t, db) {
			t.Fatal("lifetime cap changed physical reservations")
		}
		reopened, err := NewSecretStore(db.Database())
		if err != nil {
			t.Fatal("cannot reopen capacity state")
		}
		if _, err = reopened.Create(ctx, "us-east-1", "training/lifetime-cap", "", secretContractA, newSecretMaterial("DUMMY-cap"), secretContractAllow); secretTestCode(err) != "LimitExceededException" {
			t.Fatal("reopen released retired identities")
		}
	})
	t.Run("64MiB actual retained bytes", func(t *testing.T) {
		db, s := secretTestStore(t)
		ctx := context.Background()
		arns := []string{}
		for i := 0; i < 11; i++ {
			m, err := s.Create(ctx, "us-east-1", fmt.Sprintf("training/bytes-%02d", i), "", secretContractA, newSecretMaterial(strings.Repeat("x", 65536)), secretContractAllow)
			if err != nil {
				t.Fatal("cannot create physical-byte fixture")
			}
			arns = append(arns, m.ARN)
			last := 100
			if i == 10 {
				last = 24
			}
			if _, err = db.Database().Exec(`WITH RECURSIVE ids(n) AS (VALUES(2) UNION ALL SELECT n+1 FROM ids WHERE n<?) INSERT INTO aws_secret_versions SELECT 'us-east-1',?,printf('%036d',n),value,created FROM ids,aws_secret_versions WHERE region='us-east-1' AND arn=? AND version_id=?`, last, m.ARN, m.ARN, secretContractA); err != nil {
				t.Fatal("cannot materialize actual retained bytes")
			}
			// This is the reachable state after inserting each immutable
			// version and promoting the original version back to current.
			if _, err := db.Database().Exec("INSERT INTO aws_secret_stages VALUES('us-east-1',?,'AWSPREVIOUS',?)", m.ARN, fmt.Sprintf("%036d", last)); err != nil {
				t.Fatal("cannot materialize historical label")
			}
		}
		var rows, bytes int64
		if err := db.Database().QueryRow("SELECT count(*),sum(length(value)) FROM aws_secret_versions").Scan(&rows, &bytes); err != nil || rows != 1024 || bytes != 64<<20 {
			t.Fatal("byte fixture did not reach exact physical boundary")
		}
		before := secretContractFingerprint(t, db)
		target := arns[10]
		if _, err := s.Put(ctx, "us-east-1", target, fmt.Sprintf("%036d", 25), newSecretMaterial("DUMMY-byte-retry"), secretContractAllow); secretTestCode(err) != "LimitExceededException" {
			t.Fatal("Put exceeded retained byte limit")
		}
		if _, err := s.Create(ctx, "us-east-1", "training/bytes-new", "", secretContractA, newSecretMaterial("DUMMY-byte-retry"), secretContractAllow); secretTestCode(err) != "LimitExceededException" {
			t.Fatal("Create exceeded retained byte limit")
		}
		if before != secretContractFingerprint(t, db) {
			t.Fatal("byte quota changed existing identity/value/labels")
		}
		if _, err := s.DeleteForce(ctx, "us-east-1", arns[0], secretContractAllow); err != nil {
			t.Fatal("native delete did not release stored bytes")
		}
		if _, err := s.Put(ctx, "us-east-1", target, fmt.Sprintf("%036d", 25), newSecretMaterial("DUMMY-byte-retry"), secretContractAllow); err != nil {
			t.Fatal("released byte capacity did not permit retry")
		}
		v, err := s.Get(ctx, "us-east-1", target, "", "", secretContractAllow)
		if err != nil || v.material.plaintext() != "DUMMY-byte-retry" {
			t.Fatal("byte relief retry did not persist requested value")
		}
	})
}

func TestSecretStorageClosedAuthorityReopenAndConstructorDrift(t *testing.T) {
	t.Run("closed database preserves all operations and restarts", func(t *testing.T) {
		db, s, arn := secretContractFixture(t)
		before := secretStateSnapshot(t, db)
		var path string
		if err := db.Database().QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
			t.Fatal("cannot locate fixture")
		}
		if err := db.Close(); err != nil {
			t.Fatal("cannot close fixture")
		}
		ctx := context.Background()
		checks := []func() error{func() error { return secretContractMutation(s, arn, "create") }, func() error { _, e := s.Get(ctx, "us-east-1", arn, "", "", secretContractAllow); return e }, func() error { _, e := s.Describe(ctx, "us-east-1", arn, secretContractAllow); return e }, func() error {
			_, _, e := s.List(ctx, "us-east-1", SecretListQuery{MaxResults: 1}, secretContractAllow)
			return e
		}, func() error { return secretContractMutation(s, arn, "put") }, func() error { return secretContractMutation(s, arn, "promote") }, func() error { return secretContractMutation(s, arn, "delete") }}
		for _, op := range checks {
			if err := op(); err == nil || secretTestCode(err) != "infrastructure" {
				t.Fatal("closed database was acknowledged as success or denial")
			}
		}
		if _, err := NewSecretStore(db.Database()); err == nil {
			t.Fatal("constructor accepted closed database")
		}
		recovered, err := core.New(path)
		if err != nil {
			t.Fatal("cannot reopen actual state file")
		}
		defer recovered.Close()
		restored, err := NewSecretStore(recovered.Database())
		if err != nil {
			t.Fatal("cannot restore storage")
		}
		if before != secretStateSnapshot(t, recovered) {
			t.Fatal("outage/reopen changed private state")
		}
		if err = secretContractMutation(restored, arn, "put"); err != nil {
			t.Fatal("reopened storage did not permit retry")
		}
	})
	for _, fault := range []string{"wrong key length", "missing key after ignored insert", "key insert abort"} {
		t.Run(fault, func(t *testing.T) {
			db, err := core.New(filepath.Join(t.TempDir(), "cursor.db"))
			if err != nil {
				t.Fatal("cannot create constructor fixture")
			}
			defer db.Close()
			var setup string
			switch fault {
			case "wrong key length":
				setup = "CREATE TABLE aws_secret_settings(id INTEGER PRIMARY KEY,cursor_key BLOB); INSERT INTO aws_secret_settings VALUES(1,x'01')"
			case "missing key after ignored insert":
				setup = "CREATE TABLE aws_secret_settings(id INTEGER PRIMARY KEY,cursor_key BLOB); CREATE TRIGGER ignore_cursor BEFORE INSERT ON aws_secret_settings BEGIN SELECT RAISE(IGNORE); END"
			case "key insert abort":
				setup = "CREATE TABLE aws_secret_settings(id INTEGER PRIMARY KEY,cursor_key BLOB); CREATE TRIGGER abort_cursor BEFORE INSERT ON aws_secret_settings BEGIN SELECT RAISE(ABORT,'cursor fault'); END"
			}
			if _, err = db.Database().Exec(setup); err != nil {
				t.Fatal("cannot install persisted constructor fault")
			}
			if _, err = NewSecretStore(db.Database()); err == nil {
				t.Fatal("invalid persisted cursor authority accepted")
			}
		})
	}
}
