package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func ssmBatchFixture(t *testing.T) *Gateway {
	t.Helper()
	db, err := core.New(filepath.Join(t.TempDir(), "parameters.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	parameters, err := NewParameterStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	return &Gateway{Store: db, Parameters: parameters, Identities: core.NewIdentities(db)}
}

func ssmBatchSeed(t *testing.T, g *Gateway, region string, names ...string) {
	t.Helper()
	for _, name := range names {
		description := "preserved metadata"
		if _, err := g.Parameters.Put(region, name, "String", "dummy-value", &description, false); err != nil {
			t.Fatal(err)
		}
	}
}

func ssmBatchHTTP(g *Gateway, ctx context.Context, region, principal, operation, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/", strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "AmazonSSM."+operation)
	r.Header.Set("X-Simulator-Region", region)
	r.Header.Set("X-Simulator-Principal", principal)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}

func ssmBatchResult(t *testing.T, w *httptest.ResponseRecorder, deleted, missing []string) {
	t.Helper()
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/x-amz-json-1.1" {
		t.Fatal(w.Code, w.Body.String())
	}
	var result map[string][]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || !reflect.DeepEqual(result["DeletedParameters"], deleted) || !reflect.DeepEqual(result["InvalidParameters"], missing) {
		t.Fatal("unexpected batch result", result)
	}
}

func ssmBatchFrame(t *testing.T, g *Gateway) string {
	t.Helper()
	// Include metadata and versions, not only parameter presence.
	rows, err := g.Parameters.db.Query("SELECT region,name,type,value,description,version,modified FROM aws_ssm_parameters ORDER BY region,name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	frame := [][]any{}
	for rows.Next() {
		var region, name, kind, value, description string
		var version int64
		var modified float64
		if err := rows.Scan(&region, &name, &kind, &value, &description, &version, &modified); err != nil {
			t.Fatal(err)
		}
		frame = append(frame, []any{region, name, kind, value, description, version, modified})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestSSMDeleteParametersLifecycleIsolationAndRestart(t *testing.T) {
	ctx := context.Background()
	g, peer := ssmBatchFixture(t), ssmBatchFixture(t)
	ssmBatchSeed(t, g, "us-east-1", "/batch/z", "/batch/a", "/batch/Case", "/batch/keep", "batch/a")
	ssmBatchSeed(t, g, "eu-west-1", "/batch/z", "/batch/a")
	ssmBatchSeed(t, peer, "us-east-1", "/batch/z", "/batch/a")
	peerBefore := ssmBatchFrame(t, peer)
	keep, err := g.Parameters.Get("us-east-1", "/batch/keep")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"Names":["/batch/z","/batch/missing","/batch/a","/batch/z","/batch/case","/batch/missing"]}`
	ssmBatchResult(t, ssmBatchHTTP(g, ctx, "us-east-1", "", "DeleteParameters", body), []string{"/batch/z", "/batch/a"}, []string{"/batch/missing", "/batch/case"})
	ssmBatchResult(t, ssmBatchHTTP(g, ctx, "us-east-1", "", "DeleteParameters", body), []string{}, []string{"/batch/z", "/batch/missing", "/batch/a", "/batch/case"})
	if ssmBatchFrame(t, peer) != peerBefore {
		t.Fatal("separate tenant database changed")
	}
	var path string
	if err := g.Parameters.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err := g.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	g.Parameters, err = NewParameterStore(reopened.Database())
	if err != nil {
		t.Fatal(err)
	}
	g.Store, g.Identities = reopened, core.NewIdentities(reopened)
	ssmBatchResult(t, ssmBatchHTTP(g, ctx, "us-east-1", "", "DeleteParameters", `{"Names":["/batch/z","/batch/a"]}`), []string{}, []string{"/batch/z", "/batch/a"})
	for _, target := range []struct{ region, name string }{{"us-east-1", "/batch/Case"}, {"us-east-1", "batch/a"}, {"eu-west-1", "/batch/z"}, {"eu-west-1", "/batch/a"}} {
		if _, err := g.Parameters.Get(target.region, target.name); err != nil {
			t.Fatal("peer parameter lost", target, err)
		}
	}
	if got, err := g.Parameters.Get("us-east-1", "/batch/keep"); err != nil || !reflect.DeepEqual(got, keep) {
		t.Fatal("untargeted parameter changed", got, err)
	}
	// The bounded simulator permits immediate recreation, without AWS's delay.
	ssmBatchSeed(t, g, "us-east-1", "/batch/a")
	got, err := g.Parameters.Get("us-east-1", "/batch/a")
	if err != nil || got.Version != 1 {
		t.Fatal("recreation inherited deleted state", got, err)
	}
}

func TestSSMDeleteParametersValidatesWholeInput(t *testing.T) {
	g := ssmBatchFixture(t)
	ssmBatchSeed(t, g, "us-east-1", "/batch/a", "/batch/b")
	before := ssmBatchFrame(t, g)
	bodies := []string{
		`{}`, `null`, `[]`, `{"Names":null}`, `{"Names":[]}`, `{"Names":"/batch/a"}`,
		`{"Names":["/batch/a",null]}`, `{"Names":["/batch/a",1]}`, `{"Names":["/batch/a",{}]}`,
		`{"Names":["/batch/a",""]}`, `{"Names":["/batch/a","bad name"]}`,
		`{"Names":["/batch/a"," /batch/b "]}`, `{"Names":["/batch/a","/batch/a:1"]}`,
		`{"Names":["/batch/a","arn:aws:ssm:us-east-1:000000000000:parameter/batch/b"]}`,
		`{"Names":["/batch/a","/aws/reserved"]}`, `{"Names":["/batch/a","/SSM/reserved"]}`,
		`{"Names":["/batch/a","/batch/雪"]}`, `{"Names":["/batch/a","\u0000"]}`,
		`{"names":["/batch/a"]}`, `{"NAMES":["/batch/a"]}`, `{"Name":"/batch/a"}`,
		`{"Names":["/batch/a"],"Names":["/batch/b"]}`,
		`{"Names":["/batch/a"],"\u004eames":["/batch/b"]}`,
		`{"Names":["/batch/a"],"names":["/batch/b"]}`,
		`{"Names":["/batch/a"],"WithDecryption":false}`,
		`{"Names":["/batch/a"]} {}`, `{"Names":["/batch/a"],}`, `{"Names":`,
		`{"Names":["/batch/a","` + strings.Repeat("x", 1012) + `"]}`,
		`{"Names":["/batch/a","` + strings.Repeat("/a", 16) + `"]}`,
		`{"Names":[` + strings.Repeat(`"/batch/a",`, 10) + `"/batch/a"]}`,
		strings.Repeat("x", (48<<10)+1),
	}
	for index, body := range bodies {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			// An unknown principal would be denied if admission reached IAM.
			w := ssmBatchHTTP(g, context.Background(), "us-east-1", "unknown", "DeleteParameters", body)
			if w.Code != 400 || !strings.Contains(w.Body.String(), "ValidationException") || w.Header().Get("X-Simulator-IAM-Decision") != "" || ssmBatchFrame(t, g) != before {
				t.Fatal("invalid batch authorized or mutated", w.Code, w.Body.String())
			}
		})
	}
	tooMany := make([]string, 11)
	for index := range tooMany {
		tooMany[index] = "/batch/a"
	}
	for _, names := range [][]string{nil, {}, {"/batch/a", "bad name"}, {"/batch/a", ""}, tooMany} {
		deleted, missing, err := g.Parameters.DeleteMany(context.Background(), "us-east-1", names)
		if err == nil || deleted != nil || missing != nil || ssmBatchFrame(t, g) != before {
			t.Fatal("store accepted invalid batch", err)
		}
	}
	if _, _, err := g.Parameters.DeleteMany(context.Background(), "moon-1", []string{"/batch/a"}); err == nil || ssmBatchFrame(t, g) != before {
		t.Fatal("store accepted invalid region", err)
	}
}

func TestSSMDeleteParametersTenNamesAndNameBoundaries(t *testing.T) {
	g := ssmBatchFixture(t)
	names := []string{strings.Repeat("x", 1011), strings.Repeat("/a", 15)}
	for index := 2; index < 10; index++ {
		names = append(names, fmt.Sprintf("/batch/%d", index))
	}
	ssmBatchSeed(t, g, "us-east-1", names...)
	body, err := json.Marshal(map[string]any{"Names": names})
	if err != nil {
		t.Fatal(err)
	}
	ssmBatchResult(t, ssmBatchHTTP(g, context.Background(), "us-east-1", "", "DeleteParameters", string(body)), names, []string{})
}

func TestSSMDeleteParametersPreflightsEveryIAMTarget(t *testing.T) {
	g := ssmBatchFixture(t)
	ssmBatchSeed(t, g, "us-east-1", "/batch/a", "/batch/b")
	before := ssmBatchFrame(t, g)
	const arnPrefix = "arn:aws:ssm:us-east-1:000000000000:parameter/"
	for _, denied := range []string{"batch/b", "batch/missing"} {
		policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{
			{Effect: "Allow", Action: core.StringList{"ssm:DeleteParameters"}, Resource: core.StringList{arnPrefix + "batch/*"}},
			{Effect: "Deny", Action: core.StringList{"ssm:DeleteParameters"}, Resource: core.StringList{arnPrefix + denied}},
		}}
		if err := g.Identities.Set("deleter", policy); err != nil {
			t.Fatal(err)
		}
		body := `{"Names":["/batch/a","/` + denied + `"]}`
		w := ssmBatchHTTP(g, context.Background(), "us-east-1", "deleter", "DeleteParameters", body)
		if w.Code != 403 || w.Header().Get("X-Simulator-IAM-Decision") != "explicitDeny" || ssmBatchFrame(t, g) != before {
			t.Fatal("explicit deny did not protect entire batch", w.Code, w.Body.String())
		}
	}
	for _, statement := range []core.Statement{
		{Effect: "Allow", Action: core.StringList{"ssm:DeleteParameter"}, Resource: core.StringList{"*"}},
		{Effect: "Allow", Action: core.StringList{"ssm:DeleteParameters"}, Resource: core.StringList{arnPrefix + "batch/a"}},
		{Effect: "Allow", Action: core.StringList{"ssm:DeleteParameters"}, Resource: core.StringList{"arn:aws:ssm:eu-west-1:000000000000:parameter/batch/*"}},
	} {
		if err := g.Identities.Set("deleter", core.Policy{Version: "2012-10-17", Statement: core.Statements{statement}}); err != nil {
			t.Fatal(err)
		}
		w := ssmBatchHTTP(g, context.Background(), "us-east-1", "deleter", "DeleteParameters", `{"Names":["/batch/a","/batch/b"]}`)
		if w.Code != 403 || ssmBatchFrame(t, g) != before {
			t.Fatal("batch borrowed single-delete or other resource permission", w.Code)
		}
	}
	// SDK credentials carry an immutable policy snapshot. The plural IAM action
	// is separate from DeleteParameter, including when the latter is denied.
	policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{
		{Effect: "Allow", Action: core.StringList{"ssm:DeleteParameters"}, Resource: core.StringList{arnPrefix + "batch/*"}},
		{Effect: "Deny", Action: core.StringList{"ssm:DeleteParameter"}, Resource: core.StringList{"*"}},
	}}
	ctx := context.WithValue(context.Background(), sdkPolicyContextKey{}, policy)
	ssmBatchResult(t, ssmBatchHTTP(g, ctx, "us-east-1", "deleter", "DeleteParameters", `{"Names":["/batch/a","/batch/b"]}`), []string{"/batch/a", "/batch/b"}, []string{})
}

func TestSSMDeleteParametersCannotEscapeConsoleBoundary(t *testing.T) {
	g := ssmBatchFixture(t)
	ssmBatchSeed(t, g, "us-east-1", "/batch/a", "batch/a")
	before := ssmBatchFrame(t, g)
	ctx := context.WithValue(context.Background(), consoleParameterBoundaryKey{}, consoleParameterBoundary{name: "/batch/a", region: "us-east-1", arn: "arn:aws:ssm:us-east-1:000000000000:parameter/batch/a"})
	for _, body := range []string{`{"Names":["/batch/a"]}`, `{"Names":["batch/a"]}`, `{"Names":["/batch/a","batch/a"]}`} {
		w := ssmBatchHTTP(g, ctx, "us-east-1", "", "DeleteParameters", body)
		if w.Code != 403 || ssmBatchFrame(t, g) != before {
			t.Fatal("console session gained batch deletion", w.Code)
		}
	}
}

func ssmBatchAuthorizer(t *testing.T, db *sql.DB, fn func(sqlite3.AuthorizerActionCode, string, string, string, string) sqlite3.AuthorizerReturnCode) {
	t.Helper()
	connection, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = connection.Raw(func(raw any) error { return raw.(driver.Conn).Raw().SetAuthorizer(fn) })
	if closeErr := connection.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
}

func TestSSMDeleteParametersUsesOneLocalPolicySnapshot(t *testing.T) {
	g := ssmBatchFixture(t)
	ssmBatchSeed(t, g, "us-east-1", "/batch/a", "/batch/b")
	policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"ssm:DeleteParameters"}, Resource: core.StringList{"arn:aws:ssm:us-east-1:000000000000:parameter/batch/*"}}}}
	if err := g.Identities.Set("deleter", policy); err != nil {
		t.Fatal(err)
	}
	reads := 0
	ssmBatchAuthorizer(t, g.Parameters.db, func(action sqlite3.AuthorizerActionCode, table, column, _, _ string) sqlite3.AuthorizerReturnCode {
		if action == sqlite3.AUTH_READ && table == "identity_policies" && column == "document" {
			reads++
		}
		return sqlite3.AUTH_OK
	})
	ssmBatchResult(t, ssmBatchHTTP(g, context.Background(), "us-east-1", "deleter", "DeleteParameters", `{"Names":["/batch/a","/batch/b","/batch/missing"]}`), []string{"/batch/a", "/batch/b"}, []string{"/batch/missing"})
	if reads != 1 {
		t.Fatal("batch did not use one local policy snapshot", reads)
	}
	w := ssmBatchHTTP(g, context.Background(), "us-east-1", "unknown", "DeleteParameters", `{"Names":["/batch/missing"]}`)
	if w.Code != 403 || w.Header().Get("X-Simulator-IAM-Decision") != "implicitDeny" {
		t.Fatal("missing identity disclosed parameter existence", w.Code)
	}
	ssmBatchSQL(t, g.Parameters.db, "ALTER TABLE identity_policies RENAME TO private_identity_policies")
	w = ssmBatchHTTP(g, context.Background(), "us-east-1", "deleter", "DeleteParameters", `{"Names":["/batch/missing"]}`)
	if w.Code != 500 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "InvalidParameters") {
		t.Fatal("policy read failure disclosed batch result", w.Code)
	}
}

func ssmBatchSQL(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func TestSSMDeleteParametersFailuresRollBackWithoutSuccess(t *testing.T) {
	for _, fault := range []struct{ name, setup, repair string }{
		{"abort-second", `CREATE TRIGGER batch_fault BEFORE DELETE ON aws_ssm_parameters WHEN OLD.name='/batch/b' BEGIN SELECT RAISE(ABORT,'private-storage-fault'); END`, `DROP TRIGGER batch_fault`},
		{"fail-second", `CREATE TRIGGER batch_fault BEFORE DELETE ON aws_ssm_parameters WHEN OLD.name='/batch/b' BEGIN SELECT RAISE(FAIL,'private-storage-fault'); END`, `DROP TRIGGER batch_fault`},
		{"rollback-second", `CREATE TRIGGER batch_fault BEFORE DELETE ON aws_ssm_parameters WHEN OLD.name='/batch/b' BEGIN SELECT RAISE(ROLLBACK,'private-storage-fault'); END`, `DROP TRIGGER batch_fault`},
		{"ignore-second", `CREATE TRIGGER batch_fault BEFORE DELETE ON aws_ssm_parameters WHEN OLD.name='/batch/b' BEGIN SELECT RAISE(IGNORE); END`, `DROP TRIGGER batch_fault`},
		{"recreate-second", `CREATE TRIGGER batch_fault AFTER DELETE ON aws_ssm_parameters WHEN OLD.name='/batch/b' BEGIN INSERT INTO aws_ssm_parameters VALUES(OLD.region,OLD.name,OLD.type,OLD.value,OLD.description,OLD.version,OLD.modified); END`, `DROP TRIGGER batch_fault`},
		{"recreate-earlier", `CREATE TABLE batch_saved AS SELECT * FROM aws_ssm_parameters WHERE name='/batch/a'; CREATE TRIGGER batch_fault AFTER DELETE ON aws_ssm_parameters WHEN OLD.name='/batch/b' BEGIN INSERT INTO aws_ssm_parameters SELECT * FROM batch_saved; END`, `DROP TRIGGER batch_fault`},
		{"delete-later", `CREATE TRIGGER batch_fault AFTER DELETE ON aws_ssm_parameters WHEN OLD.name='/batch/a' BEGIN DELETE FROM aws_ssm_parameters WHERE region=OLD.region AND name='/batch/b'; END`, `DROP TRIGGER batch_fault`},
		{"commit-failure", `CREATE TABLE batch_parent(id INTEGER PRIMARY KEY); CREATE TABLE batch_child(id INTEGER REFERENCES batch_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER batch_fault AFTER DELETE ON aws_ssm_parameters WHEN OLD.name='/batch/b' BEGIN INSERT INTO batch_child VALUES(1); END`, `DROP TRIGGER batch_fault`},
		{"read-failure", `ALTER TABLE aws_ssm_parameters RENAME TO private_parameter_table`, `ALTER TABLE private_parameter_table RENAME TO aws_ssm_parameters`},
	} {
		t.Run(fault.name, func(t *testing.T) {
			g := ssmBatchFixture(t)
			ssmBatchSeed(t, g, "us-east-1", "/batch/a", "/batch/b", "/batch/peer")
			ssmBatchSeed(t, g, "eu-west-1", "/batch/a")
			before := ssmBatchFrame(t, g)
			ssmBatchSQL(t, g.Parameters.db, fault.setup)
			deleted, missing, err := g.Parameters.DeleteMany(context.Background(), "us-east-1", []string{"/batch/a", "/batch/missing", "/batch/b"})
			if err == nil || deleted != nil || missing != nil {
				t.Fatal("storage fault returned successful names", err)
			}
			w := ssmBatchHTTP(g, context.Background(), "us-east-1", "", "DeleteParameters", `{"Names":["/batch/a","/batch/missing","/batch/b"]}`)
			if w.Code != 500 || !strings.Contains(w.Body.String(), "InternalServerError") || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "DeletedParameters") || strings.Contains(w.Body.String(), "/batch/") {
				t.Fatal("storage failure acknowledged or leaked", w.Code, w.Body.String())
			}
			ssmBatchSQL(t, g.Parameters.db, fault.repair)
			if ssmBatchFrame(t, g) != before {
				t.Fatal("failed batch changed parameter state")
			}
			if fault.name == "commit-failure" {
				var count int
				if err := g.Parameters.db.QueryRow("SELECT count(*) FROM batch_child").Scan(&count); err != nil || count != 0 {
					t.Fatal("commit failure retained side effects", count, err)
				}
			}
			ssmBatchResult(t, ssmBatchHTTP(g, context.Background(), "us-east-1", "", "DeleteParameters", `{"Names":["/batch/a","/batch/b"]}`), []string{"/batch/a", "/batch/b"}, []string{})
		})
	}
}

func TestSSMDeleteParametersCanceledBatchHasNoPartialDeletion(t *testing.T) {
	for _, when := range []string{"before-transaction", "after-first-delete", "after-all-deletes"} {
		t.Run(when, func(t *testing.T) {
			g := ssmBatchFixture(t)
			ssmBatchSeed(t, g, "us-east-1", "/batch/a", "/batch/b")
			before := ssmBatchFrame(t, g)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deletes := 0
			if when == "before-transaction" {
				cancel()
			} else {
				ssmBatchAuthorizer(t, g.Parameters.db, func(action sqlite3.AuthorizerActionCode, _, _, _, _ string) sqlite3.AuthorizerReturnCode {
					if action == sqlite3.AUTH_DELETE {
						deletes++
						if deletes == 2 && when == "after-first-delete" {
							cancel()
						}
					}
					if action == sqlite3.AUTH_SELECT && deletes == 2 && when == "after-all-deletes" {
						cancel()
					}
					return sqlite3.AUTH_OK
				})
			}
			deleted, missing, err := g.Parameters.DeleteMany(ctx, "us-east-1", []string{"/batch/a", "/batch/b"})
			if err == nil || ctx.Err() != context.Canceled || deleted != nil || missing != nil {
				t.Fatal("canceled batch returned success", err)
			}
			if when != "before-transaction" && deletes != 2 {
				t.Fatal("cancellation did not exercise partial mutation", deletes)
			}
			if ssmBatchFrame(t, g) != before {
				t.Fatal("cancellation retained a partial deletion")
			}
			w := ssmBatchHTTP(g, ctx, "us-east-1", "", "DeleteParameters", `{"Names":["/batch/a","/batch/b"]}`)
			if w.Code != 500 || strings.Contains(w.Body.String(), "DeletedParameters") || ssmBatchFrame(t, g) != before {
				t.Fatal("canceled HTTP batch returned success", w.Code)
			}
		})
	}
}

func TestSSMDeleteParametersVerificationReadFailureRollsBack(t *testing.T) {
	g := ssmBatchFixture(t)
	ssmBatchSeed(t, g, "us-east-1", "/batch/a", "/batch/b")
	before := ssmBatchFrame(t, g)
	deletes, failed := 0, false
	ssmBatchAuthorizer(t, g.Parameters.db, func(action sqlite3.AuthorizerActionCode, _, _, _, _ string) sqlite3.AuthorizerReturnCode {
		if action == sqlite3.AUTH_DELETE {
			deletes++
		}
		if action == sqlite3.AUTH_SELECT && deletes == 2 {
			failed = true
			return sqlite3.AUTH_DENY
		}
		return sqlite3.AUTH_OK
	})
	deleted, missing, err := g.Parameters.DeleteMany(context.Background(), "us-east-1", []string{"/batch/a", "/batch/b"})
	if err == nil || !failed || deleted != nil || missing != nil {
		t.Fatal("final verification failure returned success", err)
	}
	ssmBatchAuthorizer(t, g.Parameters.db, nil)
	if ssmBatchFrame(t, g) != before {
		t.Fatal("final verification failure retained deletion")
	}
}

func TestSSMDeleteParametersClosedStoreAndSingleDeleteRegression(t *testing.T) {
	g := ssmBatchFixture(t)
	ssmBatchSeed(t, g, "us-east-1", "/batch/a", "/batch/b")
	ctx := context.Background()
	w := ssmBatchHTTP(g, ctx, "us-east-1", "", "DeleteParameter", `{"Name":"/batch/a"}`)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatal("single delete changed", w.Code, w.Body.String())
	}
	w = ssmBatchHTTP(g, ctx, "us-east-1", "", "DeleteParameter", `{"Name":"/batch/a"}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "ParameterNotFound") {
		t.Fatal("missing single delete changed", w.Code, w.Body.String())
	}
	ssmBatchResult(t, ssmBatchHTTP(g, ctx, "us-east-1", "", "DeleteParameters", `{"Names":["/batch/a"]}`), []string{}, []string{"/batch/a"})
	if err := g.Store.Close(); err != nil {
		t.Fatal(err)
	}
	deleted, missing, err := g.Parameters.DeleteMany(ctx, "us-east-1", []string{"/batch/b"})
	if err == nil || errors.Is(err, context.Canceled) || deleted != nil || missing != nil {
		t.Fatal("closed store returned successful names", err)
	}
	w = ssmBatchHTTP(g, ctx, "us-east-1", "", "DeleteParameters", `{"Names":["/batch/b"]}`)
	if w.Code != 500 || strings.Contains(w.Body.String(), "DeletedParameters") {
		t.Fatal("closed store returned HTTP success", w.Code)
	}
}
