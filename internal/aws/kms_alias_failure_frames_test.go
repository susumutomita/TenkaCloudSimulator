package aws

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Include schema and every persisted row, including cursor authority and key
// material, but expose only a digest to assertions/diagnostics.
func kmsAliasDatabaseFrame(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query("SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name")
	if err != nil {
		t.Fatal(err)
	}
	schema := [][]any{}
	tables := []string{}
	for rows.Next() {
		var kind, name, table string
		var definition any
		if err := rows.Scan(&kind, &name, &table, &definition); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		schema = append(schema, []any{kind, name, table, definition})
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	frame := map[string]any{"schema": schema}
	for _, name := range tables {
		quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		rows, err := db.Query("SELECT * FROM " + quoted)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		values := [][]any{}
		for rows.Next() {
			row := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for i := range row {
				destinations[i] = &row[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for i, v := range row {
				if b, ok := v.([]byte); ok {
					row[i] = bytes.Clone(b)
				}
			}
			values = append(values, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		frame[name] = values
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func kmsAliasRegionHTTP(g *Gateway, region, principal, operation, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", "/", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-amz-json-1.1")
	request.Header.Set("X-Amz-Target", "TrentService."+operation)
	request.Header.Set("X-Simulator-Region", region)
	request.Header.Set("X-Simulator-Principal", principal)
	response := httptest.NewRecorder()
	g.ServeHTTP(response, request)
	return response
}

func TestKMSAliasesFirstKnownPolicyDenialPreservesSchemaAndAuthority(t *testing.T) {
	db, keys := kmsTestStore(t, "")
	identities := core.NewIdentities(db)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":{"Effect":"Deny","Action":"kms:ListAliases","Resource":"*"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := identities.Set("known-denied", policy); err != nil {
		t.Fatal(err)
	}
	if _, found, err := identities.Policy("known-denied"); err != nil || !found {
		t.Fatal("known caller projection missing")
	}
	gateway := &Gateway{KMS: keys, Identities: identities}
	for _, region := range []string{"us-west-2", "ap-northeast-1"} {
		t.Run(region, func(t *testing.T) {
			before := kmsAliasDatabaseFrame(t, keys.db)
			response := kmsAliasRegionHTTP(gateway, region, "known-denied", "ListAliases", `{}`)
			if response.Code != 403 || response.Header().Get("X-Amzn-Errortype") != "AccessDeniedException" || response.Header().Get("X-Simulator-IAM-Decision") != "explicitDeny" {
				t.Fatal("expected actual known-policy denial", response.Code, response.Header().Get("X-Simulator-IAM-Decision"))
			}
			if kmsAliasDatabaseFrame(t, keys.db) != before {
				t.Fatal("denied first-region request changed schema or cursor authority")
			}
		})
	}
}

func TestKMSAliasesColdFailuresRollbackTentativeBootstrap(t *testing.T) {
	for _, operation := range []string{"CreateAlias", "UpdateAlias", "DeleteAlias", "ListAliases", "Encrypt", "Decrypt"} {
		t.Run(operation, func(t *testing.T) {
			_, keys := kmsTestStore(t, "")
			key, err := keys.CreateKey(context.Background(), "us-east-1", "bootstrap fixture")
			if err != nil {
				t.Fatal(err)
			}
			blob, _, err := keys.Encrypt(context.Background(), "us-east-1", key.KeyID, nil, []byte("dummy"))
			if err != nil {
				t.Fatal(err)
			}
			body := map[string]any{"AliasName": "alias/missing"}
			switch operation {
			case "CreateAlias", "UpdateAlias":
				body["TargetKeyId"] = "11111111-1111-4111-8111-111111111111"
			case "ListAliases":
				body = map[string]any{"Limit": 1, "Marker": "invalid-local-marker"}
			case "Encrypt":
				body = map[string]any{"KeyId": "alias/missing", "Plaintext": []byte("dummy")}
			case "Decrypt":
				body = map[string]any{"KeyId": "alias/missing", "CiphertextBlob": blob}
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			before := kmsAliasDatabaseFrame(t, keys.db)
			response := kmsAliasRegionHTTP(&Gateway{KMS: keys}, "us-east-1", "", operation, string(encoded))
			wanted := "NotFoundException"
			if operation == "ListAliases" {
				wanted = "InvalidMarkerException"
			}
			if response.Code != 400 || response.Header().Get("X-Amzn-Errortype") != wanted {
				t.Fatal("wrong cold refusal", operation, response.Code, response.Header().Get("X-Amzn-Errortype"))
			}
			if kmsAliasDatabaseFrame(t, keys.db) != before {
				t.Fatal("failed cold operation committed bootstrap", operation)
			}
		})
	}
}

func TestKMSAliasesBootstrapStorageRefusalAndRepair(t *testing.T) {
	_, keys := kmsTestStore(t, "")
	// A real ignored initialization cannot silently replace cursor authority or
	// leave the other newly-created table committed.
	_, err := keys.db.Exec(`CREATE TABLE aws_kms_alias_state(region TEXT PRIMARY KEY,revision INTEGER,secret BLOB);
 CREATE TRIGGER ignore_alias_bootstrap BEFORE INSERT ON aws_kms_alias_state BEGIN SELECT RAISE(IGNORE); END;`)
	if err != nil {
		t.Fatal(err)
	}
	before := kmsAliasDatabaseFrame(t, keys.db)
	response := kmsAliasRegionHTTP(&Gateway{KMS: keys}, "us-east-1", "", "ListAliases", `{}`)
	if response.Code != 500 || response.Header().Get("X-Amzn-Errortype") != "KMSInternalException" {
		t.Fatal("ignored initialization acknowledged", response.Code)
	}
	if kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("failed initialization changed schema/rows")
	}
	if _, err = keys.db.Exec(`DROP TRIGGER ignore_alias_bootstrap`); err != nil {
		t.Fatal(err)
	}
	response = kmsAliasRegionHTTP(&Gateway{KMS: keys}, "us-east-1", "", "ListAliases", `{}`)
	if response.Code != 200 {
		t.Fatal("bootstrap repair did not recover", response.Code)
	}
	var result KMSAliasList
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Aliases) != 0 || result.Truncated || result.NextMarker != "" {
		t.Fatal("invalid repaired empty page")
	}
	var states int
	if err = keys.db.QueryRow(`SELECT count(*) FROM aws_kms_alias_state WHERE region='us-east-1' AND length(secret)=32 AND revision=0`).Scan(&states); err != nil || states != 1 {
		t.Fatal("bootstrap state not persisted exactly once")
	}
}

func TestKMSAliasesCursorAuthorityCorruptionRefusesAndRecovers(t *testing.T) {
	keys, aliases, key, _ := kmsAliasFixture(t)
	if err := aliases.CreateAlias(context.Background(), "alias/kept", key.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"exhausted", "missing-column"} {
		t.Run(fault, func(t *testing.T) {
			var revision int64
			if err := keys.db.QueryRow(`SELECT revision FROM aws_kms_alias_state WHERE region='us-east-1'`).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			var err error
			if fault == "exhausted" {
				_, err = keys.db.Exec(`UPDATE aws_kms_alias_state SET revision=? WHERE region='us-east-1'`, int64(math.MaxInt64))
			} else {
				_, err = keys.db.Exec(`ALTER TABLE aws_kms_alias_state RENAME COLUMN revision TO unavailable_revision`)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := kmsAliasDatabaseFrame(t, keys.db)
			if _, err = NewKMSAliasStore(keys, "us-east-1"); err == nil {
				t.Fatal("corrupt authority reopened")
			}
			response := kmsAliasRegionHTTP(&Gateway{KMS: keys}, "us-east-1", "", "ListAliases", `{}`)
			if response.Code != 500 || strings.Contains(response.Body.String(), "alias/kept") || strings.Contains(response.Body.String(), "unavailable_revision") {
				t.Fatal("corrupt authority leaked/acknowledged")
			}
			if kmsAliasDatabaseFrame(t, keys.db) != before {
				t.Fatal("corrupt authority changed persisted frame")
			}
			if fault == "exhausted" {
				_, err = keys.db.Exec(`UPDATE aws_kms_alias_state SET revision=? WHERE region='us-east-1'`, revision)
			} else {
				_, err = keys.db.Exec(`ALTER TABLE aws_kms_alias_state RENAME COLUMN unavailable_revision TO revision`)
			}
			if err != nil {
				t.Fatal(err)
			}
			page, err := aliases.ListAliases(context.Background(), "", 10, "", kmsAliasAllow)
			if err != nil || len(page.Aliases) != 1 || page.Aliases[0].AliasName != "alias/kept" {
				t.Fatal("authority repair failed")
			}
		})
	}
}

func TestKMSAliasesLateProjectionFailureNeverPublishesPartialPage(t *testing.T) {
	keys, aliases, key, _ := kmsAliasFixture(t)
	for _, name := range []string{"alias/a-good", "alias/z-damaged"} {
		if err := aliases.CreateAlias(context.Background(), name, key.KeyID, kmsAliasAllow); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := keys.db.Exec(`UPDATE aws_kms_aliases SET created=0 WHERE name='alias/z-damaged'`); err != nil {
		t.Fatal(err)
	}
	before := kmsAliasDatabaseFrame(t, keys.db)
	page, err := aliases.ListAliases(context.Background(), "", 1, "", kmsAliasAllow)
	if err != nil || len(page.Aliases) != 1 || !page.Truncated {
		t.Fatal("good first page unavailable")
	}
	page, err = aliases.ListAliases(context.Background(), "", 10, "", kmsAliasAllow)
	if err == nil || len(page.Aliases) != 0 || page.NextMarker != "" || page.Truncated {
		t.Fatal("partial successful page escaped")
	}
	response := kmsAliasRegionHTTP(&Gateway{KMS: keys}, "us-east-1", "", "ListAliases", `{"Limit":10}`)
	if response.Code != 500 || strings.Contains(response.Body.String(), "a-good") || strings.Contains(response.Body.String(), "z-damaged") {
		t.Fatal("partial native page escaped")
	}
	if kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("failed read changed frame")
	}
	if _, err = keys.db.Exec(`UPDATE aws_kms_aliases SET created=updated WHERE name='alias/z-damaged'`); err != nil {
		t.Fatal(err)
	}
	page, err = aliases.ListAliases(context.Background(), "", 10, "", kmsAliasAllow)
	if err != nil || len(page.Aliases) != 2 {
		t.Fatal("projection repair did not recover")
	}
}

func TestKMSAliasesAuthorizationInfrastructureErrorsPreserveCompleteFrame(t *testing.T) {
	keys, aliases, key, _ := kmsAliasFixture(t)
	ctx := context.Background()
	if err := aliases.CreateAlias(ctx, "alias/existing", key.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	blob, _, err := keys.Encrypt(ctx, "us-east-1", key.KeyID, nil, []byte("dummy"))
	if err != nil {
		t.Fatal(err)
	}
	unavailable := errors.New("fixture authority unavailable")
	auth := func(_ context.Context, tx *sql.Tx, action, resource string) (bool, error) {
		if tx == nil || !strings.HasPrefix(action, "kms:") || resource == "" {
			t.Fatal("invalid trusted callback seam")
		}
		return false, unavailable
	}
	operations := map[string]func() error{
		"create": func() error { return aliases.CreateAlias(ctx, "alias/new", key.KeyID, auth) },
		"update": func() error { return aliases.UpdateAlias(ctx, "alias/existing", key.KeyID, auth) },
		"delete": func() error { return aliases.DeleteAlias(ctx, "alias/existing", auth) },
		"list": func() error {
			page, e := aliases.ListAliases(ctx, "", 10, "", auth)
			if len(page.Aliases) != 0 || page.Truncated || page.NextMarker != "" {
				t.Fatal("failed auth exposed page")
			}
			return e
		},
		"encrypt": func() error {
			data, arn, e := aliases.Encrypt(ctx, "alias/existing", nil, []byte("dummy"), auth)
			if len(data) != 0 || arn != "" {
				t.Fatal("failed auth exposed ciphertext")
			}
			return e
		},
		"decrypt": func() error {
			data, arn, e := aliases.Decrypt(ctx, "alias/existing", nil, blob, auth)
			if len(data) != 0 || arn != "" {
				t.Fatal("failed auth exposed plaintext")
			}
			return e
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			before := kmsAliasDatabaseFrame(t, keys.db)
			if !errors.Is(operation(), unavailable) {
				t.Fatal("authority infrastructure error masked")
			}
			if kmsAliasDatabaseFrame(t, keys.db) != before {
				t.Fatal("failed authority changed frame")
			}
		})
	}
}

func TestKMSAliasesSchemaOutagePreservesNativeOperationsAndRepair(t *testing.T) {
	keys, aliases, key, next := kmsAliasFixture(t)
	ctx := context.Background()
	if err := aliases.CreateAlias(ctx, "alias/existing", key.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	blob, _, err := keys.Encrypt(ctx, "us-east-1", key.KeyID, nil, []byte("dummy"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = keys.db.Exec(`ALTER TABLE aws_kms_aliases RENAME COLUMN key_id TO unavailable_key`); err != nil {
		t.Fatal(err)
	}
	before := kmsAliasDatabaseFrame(t, keys.db)
	cases := map[string]map[string]any{
		"CreateAlias": {"AliasName": "alias/new", "TargetKeyId": next.KeyID},
		"UpdateAlias": {"AliasName": "alias/existing", "TargetKeyId": next.KeyID},
		"DeleteAlias": {"AliasName": "alias/existing"},
		"ListAliases": {"Limit": 10},
		"Encrypt":     {"KeyId": "alias/existing", "Plaintext": []byte("dummy")},
		"Decrypt":     {"KeyId": "alias/existing", "CiphertextBlob": blob},
	}
	for operation, body := range cases {
		t.Run(operation, func(t *testing.T) {
			encoded, e := json.Marshal(body)
			if e != nil {
				t.Fatal(e)
			}
			response := kmsAliasRegionHTTP(&Gateway{KMS: keys}, "us-east-1", "", operation, string(encoded))
			if response.Code != 500 || strings.Contains(response.Body.String(), "unavailable_key") || strings.Contains(response.Body.String(), "alias/existing") {
				t.Fatal("schema outage acknowledged/leaked", operation, response.Code)
			}
			if kmsAliasDatabaseFrame(t, keys.db) != before {
				t.Fatal("schema outage changed state", operation)
			}
		})
	}
	if _, err = keys.db.Exec(`ALTER TABLE aws_kms_aliases RENAME COLUMN unavailable_key TO key_id`); err != nil {
		t.Fatal(err)
	}
	if err = aliases.UpdateAlias(ctx, "alias/existing", next.KeyID, kmsAliasAllow); err != nil {
		t.Fatal("repair/update failed", err)
	}
	page, err := aliases.ListAliases(ctx, "", 10, "", kmsAliasAllow)
	if err != nil || len(page.Aliases) != 1 || page.Aliases[0].TargetKeyId != next.KeyID {
		t.Fatal("repair did not restore actual persisted target")
	}
}

func TestKMSAliasesBorrowedTransactionRetainsCallerOwnership(t *testing.T) {
	_, keys := kmsTestStore(t, "")
	ctx := context.Background()
	first, err := keys.CreateKey(ctx, "us-east-1", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := keys.CreateKey(ctx, "us-east-1", "second")
	if err != nil {
		t.Fatal(err)
	}
	before := kmsAliasDatabaseFrame(t, keys.db)
	tx, err := keys.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	aliases, err := newKMSAliasStoreTx(ctx, tx, keys, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	if !aliases.UsesDatabase(keys.db) {
		t.Fatal("borrowed database identity lost")
	}
	if err = aliases.createAliasTx(ctx, tx, "alias/borrowed", first.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	if err = aliases.updateAliasTx(ctx, tx, "alias/borrowed", second.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	page, err := aliases.listAliasesTx(ctx, tx, "", 1, "", kmsAliasAllow)
	if err != nil || len(page.Aliases) != 1 || page.Aliases[0].TargetKeyId != second.KeyID {
		t.Fatal("same-transaction saved target missing")
	}
	encrypted, arn, err := aliases.encryptAliasTx(ctx, tx, "alias/borrowed", nil, []byte("dummy"), kmsAliasAllow)
	if err != nil || arn != second.Arn {
		t.Fatal("borrowed encryption failed")
	}
	plaintext, arn, err := aliases.decryptAliasTx(ctx, tx, "alias/borrowed", nil, encrypted, kmsAliasAllow)
	if err != nil || arn != second.Arn || string(plaintext) != "dummy" {
		t.Fatal("borrowed decryption failed")
	}
	if err = aliases.deleteAliasTx(ctx, tx, "alias/borrowed", kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	var alive int
	if err = tx.QueryRow(`SELECT 1`).Scan(&alive); err != nil || alive != 1 {
		t.Fatal("leaf closed caller transaction")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("caller rollback failed to restore schema/key/cursor frame")
	}
}

func TestKMSAliasesBootstrapSchemaConflictsAndCommitFailureRollback(t *testing.T) {
	for _, name := range []string{"aws_kms_aliases", "aws_kms_alias_state"} {
		t.Run(name, func(t *testing.T) {
			_, keys := kmsTestStore(t, "")
			if _, err := keys.db.Exec(`CREATE INDEX ` + name + ` ON aws_kms_keys(key_id)`); err != nil {
				t.Fatal(err)
			}
			before := kmsAliasDatabaseFrame(t, keys.db)
			if _, err := NewKMSAliasStore(keys, "us-east-1"); err == nil {
				t.Fatal("incompatible alias schema accepted")
			}
			if kmsAliasDatabaseFrame(t, keys.db) != before {
				t.Fatal("failed bootstrap left partial schema")
			}
			if _, err := keys.db.Exec(`DROP INDEX ` + name); err != nil {
				t.Fatal(err)
			}
			if _, err := NewKMSAliasStore(keys, "us-east-1"); err != nil {
				t.Fatal("schema repair did not recover", err)
			}
		})
	}
	keys, _, _, _ := kmsAliasFixture(t)
	// Real deferred SQLite failure happens at commit, after the read succeeded.
	_, err := keys.db.Exec(`CREATE TABLE alias_fault_parent(id INTEGER PRIMARY KEY);
 CREATE TABLE alias_fault_child(id INTEGER REFERENCES alias_fault_parent(id) DEFERRABLE INITIALLY DEFERRED);
 CREATE TRIGGER alias_commit_fault AFTER INSERT ON aws_kms_alias_state BEGIN INSERT INTO alias_fault_child VALUES(1); END;`)
	if err != nil {
		t.Fatal(err)
	}
	before := kmsAliasDatabaseFrame(t, keys.db)
	if _, err = NewKMSAliasStore(keys, "us-west-2"); err == nil {
		t.Fatal("initializing constructor acknowledged failed commit")
	}
	if kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("failed initializing commit changed frame")
	}
	response := kmsAliasRegionHTTP(&Gateway{KMS: keys}, "us-west-2", "", "ListAliases", `{}`)
	if response.Code != 500 || strings.Contains(response.Body.String(), "Aliases") {
		t.Fatal("HTTP published before bootstrap commit")
	}
	if kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("failed HTTP commit changed frame")
	}
	if _, err = keys.db.Exec(`DROP TRIGGER alias_commit_fault`); err != nil {
		t.Fatal(err)
	}
	response = kmsAliasRegionHTTP(&Gateway{KMS: keys}, "us-west-2", "", "ListAliases", `{}`)
	if response.Code != 200 {
		t.Fatal("commit repair did not recover")
	}
}

func TestKMSAliasesBorrowedBootstrapValidatesHostInputsAndCancellation(t *testing.T) {
	db, keys := kmsTestStore(t, "")
	before := kmsAliasDatabaseFrame(t, keys.db)
	for _, input := range []struct {
		keys   *KMSStore
		region string
	}{{nil, "us-east-1"}, {&KMSStore{}, "us-east-1"}, {keys, "unsupported"}} {
		if _, err := NewKMSAliasStore(input.keys, input.region); err == nil {
			t.Fatal("invalid host configuration accepted")
		}
		if kmsAliasDatabaseFrame(t, keys.db) != before {
			t.Fatal("invalid configuration created authority")
		}
	}
	tx, err := keys.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, input := range []struct {
		keys   *KMSStore
		region string
	}{{nil, "us-east-1"}, {keys, "unsupported"}} {
		if _, err = newKMSAliasStoreTx(context.Background(), tx, input.keys, input.region); err == nil {
			t.Fatal("invalid borrowed initialization accepted")
		}
	}
	if _, err = newKMSAliasStoreTx(context.Background(), nil, keys, "us-east-1"); err == nil {
		t.Fatal("missing caller transaction accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = newKMSAliasStoreTx(canceled, tx, keys, "us-east-1"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled initialization not propagated")
	}
	var alive int
	if err = tx.QueryRow(`SELECT 1`).Scan(&alive); err != nil {
		t.Fatal("failed helper rolled caller back")
	}
	tx.Rollback()
	if kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("failed initialization changed frame")
	}
	raw, err := core.New("")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	missing := &KMSStore{db: raw.Database()}
	if _, err = NewKMSAliasStore(missing, "us-east-1"); err == nil {
		t.Fatal("missing key ledger accepted")
	}
	if (&KMSAliasStore{db: keys.db}).UsesDatabase(raw.Database()) || (*KMSAliasStore)(nil).UsesDatabase(db.Database()) {
		t.Fatal("foreign/missing database identity accepted")
	}
}

func TestKMSAliasesUnavailableAuthorityRollsBackNativeMutation(t *testing.T) {
	keys, aliases, key, _ := kmsAliasFixture(t)
	if _, err := keys.db.Exec(`UPDATE aws_kms_alias_state SET revision=? WHERE region='us-east-1'`, int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	before := kmsAliasDatabaseFrame(t, keys.db)
	if err := aliases.CreateAlias(context.Background(), "alias/must-rollback", key.KeyID, kmsAliasAllow); err == nil {
		t.Fatal("mutation bypassed exhausted authority")
	}
	if kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("authority failure retained tentative alias")
	}
}

func TestKMSAliasesDirectHTTPRequiresNativeStateAndAuthorization(t *testing.T) {
	_, keys := kmsTestStore(t, "")
	for _, store := range []*KMSStore{nil, {}, keys} {
		before := kmsAliasDatabaseFrame(t, keys.db)
		request := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
		request.Header.Set("X-Amz-Target", "TrentService.ListAliases")
		request.Header.Set("Content-Type", "application/x-amz-json-1.1")
		response := httptest.NewRecorder()
		store.ServeHTTP(response, request, "us-east-1", nil)
		wanted := 500
		if store == keys {
			wanted = 403
		}
		if response.Code != wanted {
			t.Fatal("unconfigured/unauthorized direct module accepted", response.Code)
		}
		if kmsAliasDatabaseFrame(t, keys.db) != before {
			t.Fatal("direct module refusal initialized state")
		}
	}
}

func TestKMSAliasesConcreteFiltersAndCryptoRefusalsPreserveFrame(t *testing.T) {
	keys, aliases, key, _ := kmsAliasFixture(t)
	ctx := context.Background()
	if err := aliases.CreateAlias(ctx, "alias/valid", key.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	before := kmsAliasDatabaseFrame(t, keys.db)
	for _, filter := range []string{"alias/valid", "not-a-key", "11111111-1111-4111-8111-111111111111"} {
		page, err := aliases.ListAliases(ctx, filter, 1, "", kmsAliasAllow)
		if err == nil || len(page.Aliases) != 0 {
			t.Fatal("invalid or absent concrete filter accepted")
		}
	}
	for _, input := range []struct {
		plain   []byte
		context map[string]string
	}{{nil, nil}, {make([]byte, 4097), nil}, {[]byte("dummy"), map[string]string{"": "invalid"}}} {
		data, arn, err := aliases.Encrypt(ctx, "alias/valid", input.context, input.plain, kmsAliasAllow)
		if err == nil || len(data) != 0 || arn != "" {
			t.Fatal("invalid crypto input acknowledged")
		}
	}
	if kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("refused filters or encryption inputs changed persisted state")
	}
	blob, _, err := keys.Encrypt(ctx, "us-east-1", key.KeyID, nil, []byte("dummy"))
	if err != nil {
		t.Fatal(err)
	}
	before = kmsAliasDatabaseFrame(t, keys.db)
	for _, input := range []struct {
		blob    []byte
		context map[string]string
	}{{[]byte("invalid"), nil}, {blob, map[string]string{"": "invalid"}}} {
		data, arn, err := aliases.Decrypt(ctx, "alias/valid", input.context, input.blob, kmsAliasAllow)
		if err == nil || len(data) != 0 || arn != "" {
			t.Fatal("invalid decrypt input exposed data")
		}
	}
	west, err := keys.CreateKey(ctx, "us-west-2", "west")
	if err != nil {
		t.Fatal(err)
	}
	otherBlob, _, err := keys.Encrypt(ctx, "us-west-2", west.KeyID, nil, []byte("dummy"))
	if err != nil {
		t.Fatal(err)
	}
	before = kmsAliasDatabaseFrame(t, keys.db)
	data, arn, err := aliases.Decrypt(ctx, "alias/valid", nil, otherBlob, kmsAliasAllow)
	if err == nil || len(data) != 0 || arn != "" {
		t.Fatal("foreign-region ciphertext accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, operation := range []func() error{
		func() error { return aliases.CreateAlias(canceled, "alias/new", key.KeyID, kmsAliasAllow) },
		func() error { return aliases.UpdateAlias(canceled, "alias/valid", key.KeyID, kmsAliasAllow) },
		func() error { return aliases.DeleteAlias(canceled, "alias/valid", kmsAliasAllow) },
		func() error {
			_, _, e := aliases.Encrypt(canceled, "alias/valid", nil, []byte("dummy"), kmsAliasAllow)
			return e
		},
		func() error { _, _, e := aliases.Decrypt(canceled, "alias/valid", nil, blob, kmsAliasAllow); return e },
	} {
		if !errors.Is(operation(), context.Canceled) {
			t.Fatal("canceled operation not propagated")
		}
	}
	if kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("refused input changed key/cursor/alias frame")
	}
}

func TestKMSAliasesBorrowedPortRefusalsDoNotCloseOrChangeCallerState(t *testing.T) {
	keys, aliases, key, _ := kmsAliasFixture(t)
	ctx := context.Background()
	if err := aliases.CreateAlias(ctx, "alias/existing", key.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	blob, _, err := keys.Encrypt(ctx, "us-east-1", key.KeyID, nil, []byte("dummy"))
	if err != nil {
		t.Fatal(err)
	}
	west, err := keys.CreateKey(ctx, "us-west-2", "west")
	if err != nil {
		t.Fatal(err)
	}
	westBlob, _, err := keys.Encrypt(ctx, "us-west-2", west.KeyID, nil, []byte("dummy"))
	if err != nil {
		t.Fatal(err)
	}
	before := kmsAliasDatabaseFrame(t, keys.db)
	tx, err := keys.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	checks := []struct {
		name, code string
		call       func() error
	}{
		{"missing-create-name", "ValidationException", func() error { return aliases.createAliasTx(ctx, tx, "", key.KeyID, kmsAliasAllow) }},
		{"malformed-create-target", "ValidationException", func() error { return aliases.createAliasTx(ctx, tx, "alias/new", "alias/existing", kmsAliasAllow) }},
		{"malformed-update-target", "ValidationException", func() error { return aliases.updateAliasTx(ctx, tx, "alias/existing", "not-a-key", kmsAliasAllow) }},
		{"zero-page-size", "ValidationException", func() error { _, e := aliases.listAliasesTx(ctx, tx, "", 0, "", kmsAliasAllow); return e }},
		{"empty-encryption", "ValidationException", func() error {
			data, arn, e := aliases.encryptAliasTx(ctx, tx, "alias/existing", nil, nil, kmsAliasAllow)
			if len(data) > 0 || arn != "" {
				t.Fatal("invalid encryption emitted data")
			}
			return e
		}},
		{"invalid-encryption-context", "ValidationException", func() error {
			_, _, e := aliases.encryptAliasTx(ctx, tx, "alias/existing", map[string]string{"": "bad"}, []byte("dummy"), kmsAliasAllow)
			return e
		}},
		{"invalid-ciphertext", "InvalidCiphertextException", func() error {
			data, arn, e := aliases.decryptAliasTx(ctx, tx, "alias/existing", nil, []byte("invalid"), kmsAliasAllow)
			if len(data) > 0 || arn != "" {
				t.Fatal("invalid decryption emitted data")
			}
			return e
		}},
		{"foreign-ciphertext", "InvalidCiphertextException", func() error {
			_, _, e := aliases.decryptAliasTx(ctx, tx, "alias/existing", nil, westBlob, kmsAliasAllow)
			return e
		}},
		{"invalid-decryption-context", "ValidationException", func() error {
			_, _, e := aliases.decryptAliasTx(ctx, tx, "alias/existing", map[string]string{"": "bad"}, blob, kmsAliasAllow)
			return e
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			kmsWantCode(t, check.call(), check.code)
			var count int
			if e := tx.QueryRow(`SELECT count(*) FROM aws_kms_aliases`).Scan(&count); e != nil || count != 1 {
				t.Fatal("refused leaf changed/closed caller transaction")
			}
		})
	}
	// Concrete IDs/ARNs and omitted decrypt select the same actual key without
	// converting identifier selection into an authorization grant.
	data, arn, err := aliases.encryptAliasTx(ctx, tx, key.Arn, nil, []byte("concrete"), kmsAliasAllow)
	if err != nil || arn != key.Arn {
		t.Fatal("concrete trusted selection failed")
	}
	plain, arn, err := aliases.decryptAliasTx(ctx, tx, "", nil, data, kmsAliasAllow)
	if err != nil || arn != key.Arn || string(plain) != "concrete" {
		t.Fatal("embedded key selection failed")
	}
	if _, err = aliases.ResolveAliasTx(ctx, nil, "alias/existing"); err == nil {
		t.Fatal("missing caller transaction accepted")
	}
	if _, err = aliases.ResolveAliasTx(ctx, tx, "arn:aws:kms:us-west-2:000000000000:alias/existing"); err == nil {
		t.Fatal("foreign alias ARN adopted")
	}
	if _, err = aliases.ResolveAliasTx(ctx, tx, "not-an-alias"); err == nil {
		t.Fatal("invalid alias identifier adopted")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("borrowed operations escaped caller rollback")
	}
}

func TestKMSAliasesMissingTargetsClockRegressionAndDisabledDecrypt(t *testing.T) {
	keys, aliases, key, next := kmsAliasFixture(t)
	ctx := context.Background()
	if err := aliases.CreateAlias(ctx, "alias/existing", key.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	before := kmsAliasDatabaseFrame(t, keys.db)
	kmsWantCode(t, aliases.CreateAlias(ctx, "alias/new", "alias/existing", kmsAliasAllow), "ValidationException")
	kmsWantCode(t, aliases.UpdateAlias(ctx, "alias/existing", "11111111-1111-4111-8111-111111111111", kmsAliasAllow), "NotFoundException")
	kmsWantCode(t, aliases.UpdateAlias(ctx, "alias/existing", "invalid", kmsAliasAllow), "ValidationException")
	kmsWantCode(t, aliases.DeleteAlias(ctx, "alias/aws/reserved", kmsAliasAllow), "ValidationException")
	if kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("missing/invalid target changed frame")
	}
	// A persisted timestamp may legitimately be ahead after a host clock rollback.
	page, err := aliases.ListAliases(ctx, "", 10, "", kmsAliasAllow)
	if err != nil {
		t.Fatal(err)
	}
	creation := page.Aliases[0].CreationDate
	future := page.Aliases[0].LastUpdatedDate + 86400
	if _, err = keys.db.Exec(`UPDATE aws_kms_aliases SET updated=? WHERE name='alias/existing'`, future); err != nil {
		t.Fatal(err)
	}
	if err = aliases.UpdateAlias(ctx, "alias/existing", next.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	page, err = aliases.ListAliases(ctx, "", 10, "", kmsAliasAllow)
	if err != nil || page.Aliases[0].CreationDate != creation || page.Aliases[0].LastUpdatedDate != future || page.Aliases[0].TargetKeyId != next.KeyID {
		t.Fatal("retarget regressed saved time/creation identity")
	}
	blob, _, err := keys.Encrypt(ctx, "us-east-1", next.KeyID, nil, []byte("dummy"))
	if err != nil {
		t.Fatal(err)
	}
	if err = keys.SetEnabled(ctx, "us-east-1", next.KeyID, false); err != nil {
		t.Fatal(err)
	}
	before = kmsAliasDatabaseFrame(t, keys.db)
	plain, arn, err := aliases.Decrypt(ctx, "alias/existing", nil, blob, kmsAliasAllow)
	kmsWantCode(t, err, "DisabledException")
	if len(plain) != 0 || arn != "" || kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("disabled key disclosed/changed state")
	}
	if err = keys.SetEnabled(ctx, "us-east-1", next.KeyID, true); err != nil {
		t.Fatal(err)
	}
	plain, _, err = aliases.Decrypt(ctx, "alias/existing", nil, blob, kmsAliasAllow)
	if err != nil || string(plain) != "dummy" {
		t.Fatal("enabled repair did not recover ciphertext")
	}
}

func TestKMSAliasesBootstrapInsertFailureAndKeyLedgerOutage(t *testing.T) {
	_, keys := kmsTestStore(t, "")
	_, err := keys.db.Exec(`CREATE TABLE aws_kms_alias_state(region TEXT PRIMARY KEY,revision INTEGER,secret BLOB);
 CREATE TRIGGER fail_alias_bootstrap BEFORE INSERT ON aws_kms_alias_state BEGIN SELECT RAISE(ABORT,'fixture bootstrap failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	before := kmsAliasDatabaseFrame(t, keys.db)
	if _, err = NewKMSAliasStore(keys, "us-east-1"); err == nil {
		t.Fatal("failed state insertion acknowledged")
	}
	if kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("failed state insertion retained schema")
	}
	if _, err = keys.db.Exec(`DROP TRIGGER fail_alias_bootstrap`); err != nil {
		t.Fatal(err)
	}
	aliases, err := NewKMSAliasStore(keys, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	key, err := keys.CreateKey(context.Background(), "us-east-1", "dummy")
	if err != nil {
		t.Fatal(err)
	}
	if err = aliases.CreateAlias(context.Background(), "alias/existing", key.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	if _, err = keys.db.Exec(`ALTER TABLE aws_kms_keys RENAME COLUMN description TO unavailable_description`); err != nil {
		t.Fatal(err)
	}
	before = kmsAliasDatabaseFrame(t, keys.db)
	response := kmsAliasRegionHTTP(&Gateway{KMS: keys}, "us-east-1", "", "ListAliases", `{}`)
	if response.Code != 500 || strings.Contains(response.Body.String(), "unavailable_description") || strings.Contains(response.Body.String(), "alias/existing") {
		t.Fatal("key ledger outage disclosed/acknowledged page")
	}
	if kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("key ledger outage changed frame")
	}
	if _, err = keys.db.Exec(`ALTER TABLE aws_kms_keys RENAME COLUMN unavailable_description TO description`); err != nil {
		t.Fatal(err)
	}
	page, err := aliases.ListAliases(context.Background(), "", 10, "", kmsAliasAllow)
	if err != nil || len(page.Aliases) != 1 {
		t.Fatal("key ledger repair did not recover")
	}
}

func TestKMSAliasesCanceledHTTPDoesNotBootstrapOrAuthorize(t *testing.T) {
	_, keys := kmsTestStore(t, "")
	before := kmsAliasDatabaseFrame(t, keys.db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest("POST", "/", strings.NewReader(`{}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/x-amz-json-1.1")
	request.Header.Set("X-Amz-Target", "TrentService.ListAliases")
	response := httptest.NewRecorder()
	calls := 0
	keys.ServeHTTP(response, request, "us-east-1", func(string, string) bool { calls++; return true })
	if response.Code != 500 || calls != 0 || kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("canceled HTTP acquired authority or saved state")
	}
}

func TestKMSAliasesStoredTypeFaultHasNoPartialOutput(t *testing.T) {
	keys, aliases, key, _ := kmsAliasFixture(t)
	for _, name := range []string{"alias/a-good", "alias/z-type-fault"} {
		if err := aliases.CreateAlias(context.Background(), name, key.KeyID, kmsAliasAllow); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := keys.db.Exec(`UPDATE aws_kms_aliases SET target_created='invalid-saved-number' WHERE name='alias/z-type-fault'`); err != nil {
		t.Fatal(err)
	}
	before := kmsAliasDatabaseFrame(t, keys.db)
	page, err := aliases.ListAliases(context.Background(), "", 10, "", kmsAliasAllow)
	if err == nil || len(page.Aliases) != 0 || page.Truncated || page.NextMarker != "" {
		t.Fatal("stored type fault published a partial page")
	}
	response := kmsAliasRegionHTTP(&Gateway{KMS: keys}, "us-east-1", "", "ListAliases", `{}`)
	if response.Code != 500 || strings.Contains(response.Body.String(), "invalid-saved-number") || strings.Contains(response.Body.String(), "a-good") {
		t.Fatal("stored type failure leaked data")
	}
	if kmsAliasDatabaseFrame(t, keys.db) != before {
		t.Fatal("stored type failure changed state")
	}
	if _, err = keys.db.Exec(`UPDATE aws_kms_aliases SET target_created=? WHERE name='alias/z-type-fault'`, key.CreationDate); err != nil {
		t.Fatal(err)
	}
	page, err = aliases.ListAliases(context.Background(), "", 10, "", kmsAliasAllow)
	if err != nil || len(page.Aliases) != 2 {
		t.Fatal("stored type repair failed")
	}
}

func TestKMSAliasesClosedBorrowedDatabaseDoesNotReinitialize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "borrowed.db")
	db, keys := kmsTestStore(t, path)
	key, err := keys.CreateKey(context.Background(), "us-east-1", "preserved")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if store, e := NewKMSAliasStore(keys, "us-east-1"); e == nil || store != nil {
		t.Fatal("closed borrowed database was replaced")
	}
	reopened, actual := kmsTestStore(t, path)
	metadata, err := actual.DescribeKey(context.Background(), "us-east-1", key.KeyID)
	if err != nil || metadata.KeyID != key.KeyID {
		t.Fatal("borrowed key ledger lost")
	}
	var schema int
	if err = reopened.Database().QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('aws_kms_aliases','aws_kms_alias_state')`).Scan(&schema); err != nil || schema != 0 {
		t.Fatal("failed closed-db initialization created alias schema")
	}
}
