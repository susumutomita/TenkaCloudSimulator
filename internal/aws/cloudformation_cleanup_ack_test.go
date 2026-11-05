package aws

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func cleanupACKQuery(g *Gateway, action, name, body string) *httptest.ResponseRecorder {
	form := url.Values{"Action": {action}, "Version": {"2010-05-15"}, "StackName": {name}}
	if action == "CreateStack" {
		form.Set("TemplateBody", body)
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}

// Hash every actual table row, including peer stacks/resources. No Logs store or
// retention adapter is constructed: this regression applies to existing CFN.
func cleanupACKFrame(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	frame := map[string][]string{}
	for _, name := range names {
		frame[name] = nil
		rows, err = db.Query("SELECT * FROM \"" + strings.ReplaceAll(name, "\"", "\"\"") + "\"")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(values))
			for i := range values {
				dest[i] = &values[i]
			}
			if err = rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			frame[name] = append(frame[name], string(b))
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err = rows.Close(); err != nil {
			t.Fatal(err)
		}
		sort.Strings(frame[name])
	}
	b, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func TestCFNFinalCleanupACKExistingSSMAndS3(t *testing.T) {
	for _, kind := range []string{"SSM", "S3"} {
		for _, fault := range []string{"ignore-stack", "reinsert-stack", "ignore-claims"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				store, s3, stacks, parameters := s3StackState(t)
				g := &Gateway{Store: store, S3: s3, Stacks: stacks, Parameters: parameters, Identities: core.NewIdentities(store)}
				template := func(name string) string {
					if kind == "SSM" {
						return "Resources:\n  P:\n    Type: AWS::SSM::Parameter\n    Properties: {Name: /cfn/" + name + ", Type: String, Value: dummy}\n"
					}
					return "Resources:\n  B:\n    Type: AWS::S3::Bucket\n    Properties: {BucketName: cfn-" + name + "}\n"
				}
				for _, name := range []string{"ack-owned", "ack-peer"} {
					if w := cleanupACKQuery(g, "CreateStack", name, template(name)); w.Code != 200 {
						t.Fatal("setup", kind, w.Code)
					}
				}
				ownedBefore, err := stacks.Records("us-east-1", "ack-owned")
				if err != nil || len(ownedBefore) != 1 {
					t.Fatal("owned setup", err)
				}
				ownedID := ownedBefore[0].ID
				peerBefore, err := stacks.Records("us-east-1", "ack-peer")
				if err != nil {
					t.Fatal(err)
				}
				peerJSON, _ := json.Marshal(peerBefore)
				trigger := `CREATE TRIGGER cleanup_ack BEFORE DELETE ON aws_cfn_stacks WHEN OLD.name='ack-owned' BEGIN SELECT RAISE(IGNORE); END`
				if fault == "reinsert-stack" {
					trigger = `CREATE TRIGGER cleanup_ack AFTER DELETE ON aws_cfn_stacks WHEN OLD.name='ack-owned' BEGIN INSERT INTO aws_cfn_stacks VALUES(OLD.region,OLD.name,OLD.id,OLD.document); END`
				}
				if fault == "ignore-claims" {
					trigger = `CREATE TRIGGER cleanup_ack BEFORE DELETE ON aws_cfn_ownership WHEN OLD.stack_id=(SELECT id FROM aws_cfn_stacks WHERE name='ack-owned') BEGIN SELECT RAISE(IGNORE); END`
				}
				if _, err = stacks.db.Exec(trigger); err != nil {
					t.Fatal(err)
				}
				before := cleanupACKFrame(t, stacks.db)
				w := cleanupACKQuery(g, "DeleteStack", "ack-owned", "")
				if w.Code != 500 || !strings.Contains(w.Body.String(), "InternalFailure") || cleanupACKFrame(t, stacks.db) != before {
					t.Fatal("cleanup falsely acknowledged or changed frame", kind, fault, w.Code)
				}
				if _, err = stacks.db.Exec("DROP TRIGGER cleanup_ack"); err != nil {
					t.Fatal(err)
				}
				if w = cleanupACKQuery(g, "DeleteStack", "ack-owned", ""); w.Code != 200 {
					t.Fatal("retry", w.Code)
				}
				var ownedStacks int
				if err = stacks.db.QueryRow("SELECT count(*) FROM aws_cfn_stacks WHERE id=?", ownedID).Scan(&ownedStacks); err != nil || ownedStacks != 0 {
					t.Fatal("stack retained after successful retry", err)
				}
				var claims int
				if err = stacks.db.QueryRow("SELECT count(*) FROM aws_cfn_ownership WHERE stack_id=?", ownedID).Scan(&claims); err != nil || claims != 0 {
					t.Fatal("owned claims", err)
				}
				var resources int
				query := "SELECT count(*) FROM aws_ssm_parameters WHERE region='us-east-1' AND name='/cfn/ack-owned'"
				if kind == "S3" {
					query = "SELECT count(*) FROM buckets WHERE name='cfn-ack-owned'"
				}
				if err = stacks.db.QueryRow(query).Scan(&resources); err != nil || resources != 0 {
					t.Fatal("resource retained", err)
				}
				peerQuery := "SELECT count(*) FROM aws_ssm_parameters WHERE region='us-east-1' AND name='/cfn/ack-peer' AND value='dummy'"
				if kind == "S3" {
					peerQuery = "SELECT count(*) FROM buckets WHERE name='cfn-ack-peer'"
				}
				var peerResources int
				if err = stacks.db.QueryRow(peerQuery).Scan(&peerResources); err != nil || peerResources != 1 {
					t.Fatal("peer physical resource changed", err)
				}
				peerAfter, err := stacks.Records("us-east-1", "ack-peer")
				if err != nil {
					t.Fatal(err)
				}
				afterJSON, _ := json.Marshal(peerAfter)
				if string(peerJSON) != string(afterJSON) {
					t.Fatal("peer changed")
				}
				if w = cleanupACKQuery(g, "DeleteStack", "ack-peer", ""); w.Code != 200 {
					t.Fatal("peer cleanup", w.Code)
				}
			})
		}
	}
}
