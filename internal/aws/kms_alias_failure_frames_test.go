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
