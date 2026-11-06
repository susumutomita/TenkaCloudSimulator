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

func physicalDeleteQuery(g *Gateway, action, name, body string) *httptest.ResponseRecorder {
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

// Hash every actual table row, including the owned and peer stack/resource graph.
// A refused deletion must retain the entire SQLite frame.
func physicalDeleteFrame(t *testing.T, db *sql.DB) string {
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

// Capture every column of the peer's physical resource, stack and ownership
// records, not just their existence or metadata projection.
func physicalDeletePeerFrame(t *testing.T, db *sql.DB, table, name, stackID string) string {
	t.Helper()
	frame := map[string][]string{}
	for label, q := range map[string]struct{ query, arg string }{
		"resource": {"SELECT * FROM " + table + " WHERE name=?", name},
		"stack":    {"SELECT * FROM aws_cfn_stacks WHERE id=?", stackID},
		"claims":   {"SELECT * FROM aws_cfn_ownership WHERE stack_id=?", stackID},
	} {
		rows, err := db.Query(q.query, q.arg)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(cols))
			dest := make([]any, len(cols))
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
			frame[label] = append(frame[label], string(b))
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err = rows.Close(); err != nil {
			t.Fatal(err)
		}
		sort.Strings(frame[label])
	}
	b, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// Test-only fault injection: not a real AWS comparison or an authorization bypass.
// The intended contract is refusal+whole-frame rollback when a physical DELETE
// reports no SQL error but leaves the selected native resource present.
func TestCFNPhysicalResourceDeleteMustBeAcknowledged(t *testing.T) {
	for _, kind := range []string{"SSM", "S3"} {
		for _, fault := range []string{"ignore", "reinsert", "abort", "late-claim-reinsert", "late-stack-reinsert"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				store, s3, stacks, parameters := s3StackState(t)
				g := &Gateway{Store: store, S3: s3, Stacks: stacks, Parameters: parameters, Identities: core.NewIdentities(store)}
				template := func(name string) string {
					if kind == "SSM" {
						return "Resources:\n  P:\n    Type: AWS::SSM::Parameter\n    Properties: {Name: /cfn/" + name + ", Type: String, Value: dummy}\n"
					}
					return "Resources:\n  B:\n    Type: AWS::S3::Bucket\n    Properties: {BucketName: cfn-" + name + "}\n"
				}
				for _, name := range []string{"physical-owned", "physical-peer"} {
					if w := physicalDeleteQuery(g, "CreateStack", name, template(name)); w.Code != 200 {
						t.Fatal("setup", kind, w.Code)
					}
				}
				owned, err := stacks.Records("us-east-1", "physical-owned")
				if err != nil || len(owned) != 1 {
					t.Fatal("owned setup", err)
				}
				peer, err := stacks.Records("us-east-1", "physical-peer")
				if err != nil || len(peer) != 1 {
					t.Fatal("peer setup", err)
				}
				peerBefore, _ := json.Marshal(peer)
				table, ownedName, peerName := "aws_ssm_parameters", "/cfn/physical-owned", "/cfn/physical-peer"
				if kind == "S3" {
					table, ownedName, peerName = "buckets", "cfn-physical-owned", "cfn-physical-peer"
				}
				peerPhysicalBefore := physicalDeletePeerFrame(t, stacks.db, table, peerName, peer[0].ID)
				trigger := "CREATE TRIGGER physical_ack BEFORE DELETE ON " + table + " WHEN OLD.name='" + ownedName + "' BEGIN SELECT RAISE(IGNORE); END"
				oldValues := "OLD.region,OLD.name,OLD.type,OLD.value,OLD.description,OLD.version,OLD.modified"
				literalQuery := "SELECT quote(region)||','||quote(name)||','||quote(type)||','||quote(value)||','||quote(description)||','||quote(version)||','||quote(modified) FROM aws_ssm_parameters WHERE region='us-east-1' AND name=?"
				if kind == "S3" {
					oldValues = "OLD.name,OLD.region,OLD.created"
					literalQuery = "SELECT quote(name)||','||quote(region)||','||quote(created) FROM buckets WHERE name=?"
				}
				var savedValues string
				if err = stacks.db.QueryRow(literalQuery, ownedName).Scan(&savedValues); err != nil {
					t.Fatal(err)
				}
				switch fault {
				case "reinsert":
					trigger = "CREATE TRIGGER physical_ack AFTER DELETE ON " + table + " WHEN OLD.name='" + ownedName + "' BEGIN INSERT INTO " + table + " VALUES(" + oldValues + "); END"
				case "abort":
					trigger = "CREATE TRIGGER physical_ack BEFORE DELETE ON " + table + " WHEN OLD.name='" + ownedName + "' BEGIN SELECT RAISE(ABORT,'local fault'); END"
				case "late-claim-reinsert":
					trigger = "CREATE TRIGGER physical_ack AFTER DELETE ON aws_cfn_ownership WHEN OLD.stack_id='" + strings.ReplaceAll(owned[0].ID, "'", "''") + "' BEGIN INSERT INTO " + table + " VALUES(" + savedValues + "); END"
				case "late-stack-reinsert":
					trigger = "CREATE TRIGGER physical_ack AFTER DELETE ON aws_cfn_stacks WHEN OLD.id='" + strings.ReplaceAll(owned[0].ID, "'", "''") + "' BEGIN INSERT INTO " + table + " VALUES(" + savedValues + "); END"
				}
				if _, err = stacks.db.Exec(trigger); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := stacks.db.Exec("DROP TRIGGER IF EXISTS physical_ack"); err != nil {
						t.Error("drop trigger", err)
					}
				})
				before := physicalDeleteFrame(t, stacks.db)
				w := physicalDeleteQuery(g, "DeleteStack", "physical-owned", "")
				after := physicalDeleteFrame(t, stacks.db)
				var remainingResource, remainingStack, remainingClaims, peerResource int
				for _, q := range []struct {
					sql string
					arg string
					dst *int
				}{
					{"SELECT count(*) FROM " + table + " WHERE name=?", ownedName, &remainingResource},
					{"SELECT count(*) FROM aws_cfn_stacks WHERE id=?", owned[0].ID, &remainingStack},
					{"SELECT count(*) FROM aws_cfn_ownership WHERE stack_id=?", owned[0].ID, &remainingClaims},
					{"SELECT count(*) FROM " + table + " WHERE name=?", peerName, &peerResource},
				} {
					if err = stacks.db.QueryRow(q.sql, q.arg).Scan(q.dst); err != nil {
						t.Fatal(err)
					}
				}
				peerAfter, err := stacks.Records("us-east-1", "physical-peer")
				if err != nil {
					t.Fatal(err)
				}
				peerJSON, _ := json.Marshal(peerAfter)
				if string(peerJSON) != string(peerBefore) || peerResource != 1 || physicalDeletePeerFrame(t, stacks.db, table, peerName, peer[0].ID) != peerPhysicalBefore {
					t.Error("peer graph changed")
				}
				if w.Code != 500 || before != after {
					// Only bounded status/count/boolean diagnostics; no row values, IDs or secrets.
					t.Errorf("physical delete false acknowledgement: status=%d resourceRows=%d stackRows=%d claimRows=%d peerResourceRows=%d wholeFrameSame=%t", w.Code, remainingResource, remainingStack, remainingClaims, peerResource, before == after)
					return
				}
				if remainingResource != 1 || remainingStack != 1 || remainingClaims != 1 {
					t.Fatal("refusal did not retain exact owned graph")
				}
				if _, err = stacks.db.Exec("DROP TRIGGER physical_ack"); err != nil {
					t.Fatal(err)
				}
				if w = physicalDeleteQuery(g, "DeleteStack", "physical-owned", ""); w.Code != 200 {
					t.Fatal("repaired cleanup", w.Code)
				}
				if err = stacks.db.QueryRow("SELECT count(*) FROM "+table+" WHERE name=?", ownedName).Scan(&remainingResource); err != nil || remainingResource != 0 {
					t.Fatal("repaired cleanup retained resource", err)
				}
			})
		}
	}
}

func TestCFNPhysicalAbsencePreservesAlreadyMissingCleanup(t *testing.T) {
	for _, kind := range []string{"SSM", "S3"} {
		t.Run(kind, func(t *testing.T) {
			store, s3, stacks, parameters := s3StackState(t)
			g := &Gateway{Store: store, S3: s3, Stacks: stacks, Parameters: parameters, Identities: core.NewIdentities(store)}
			body := "Resources:\n  P:\n    Type: AWS::SSM::Parameter\n    Properties: {Name: /cfn/missing, Type: String, Value: dummy}\n"
			deletion := "DELETE FROM aws_ssm_parameters WHERE region='us-east-1' AND name='/cfn/missing'"
			if kind == "S3" {
				body = "Resources:\n  B:\n    Type: AWS::S3::Bucket\n    Properties: {BucketName: cfn-missing-physical}\n"
				deletion = "DELETE FROM buckets WHERE name='cfn-missing-physical'"
			}
			if w := physicalDeleteQuery(g, "CreateStack", "missing", body); w.Code != 200 {
				t.Fatal("create", w.Code)
			}
			if _, err := stacks.db.Exec(deletion); err != nil {
				t.Fatal(err)
			}
			if w := physicalDeleteQuery(g, "DeleteStack", "missing", ""); w.Code != 200 {
				t.Fatal("already-missing compatibility changed", w.Code)
			}
		})
	}
}

func TestCFNPhysicalFinalCheckCatchesCrossResourceReinsertion(t *testing.T) {
	for _, fault := range []string{"reinsert", "abort"} {
		t.Run(fault, func(t *testing.T) {
			store, s3, stacks, parameters := s3StackState(t)
			g := &Gateway{Store: store, S3: s3, Stacks: stacks, Parameters: parameters, Identities: core.NewIdentities(store)}
			body := "Resources:\n  P:\n    Type: AWS::SSM::Parameter\n    Properties: {Name: /cfn/cross, Type: String, Value: dummy}\n  B:\n    Type: AWS::S3::Bucket\n    Properties: {BucketName: cfn-cross-physical}\n"
			if w := physicalDeleteQuery(g, "CreateStack", "cross", body); w.Code != 200 {
				t.Fatal("create", w.Code)
			}
			records, err := stacks.Records("us-east-1", "cross")
			if err != nil || len(records) != 1 || len(records[0].Resources) != 2 {
				t.Fatal("setup", err)
			}
			// CFN deletes resources in reverse recorded order. A deletion of the second
			// resource resurrects the first, after an immediate per-resource check would
			// already have passed. The final validation must detect the reappearance.
			first, last := records[0].Resources[1], records[0].Resources[0]
			table := func(kind string) string {
				if kind == "AWS::SSM::Parameter" {
					return "aws_ssm_parameters"
				}
				if kind == "AWS::S3::Bucket" {
					return "buckets"
				}
				t.Fatal("unexpected kind")
				return ""
			}
			literalQuery := "SELECT quote(region)||','||quote(name)||','||quote(type)||','||quote(value)||','||quote(description)||','||quote(version)||','||quote(modified) FROM aws_ssm_parameters WHERE region='us-east-1' AND name=?"
			if first.Type == "AWS::S3::Bucket" {
				literalQuery = "SELECT quote(name)||','||quote(region)||','||quote(created) FROM buckets WHERE name=?"
			}
			var saved string
			if err = stacks.db.QueryRow(literalQuery, first.PhysicalID).Scan(&saved); err != nil {
				t.Fatal(err)
			}
			trigger := "CREATE TRIGGER cross_ack AFTER DELETE ON " + table(last.Type) + " WHEN OLD.name='" + strings.ReplaceAll(last.PhysicalID, "'", "''") + "' BEGIN INSERT INTO " + table(first.Type) + " VALUES(" + saved + "); END"
			if fault == "abort" {
				trigger = "CREATE TRIGGER cross_ack BEFORE DELETE ON " + table(last.Type) + " WHEN OLD.name='" + strings.ReplaceAll(last.PhysicalID, "'", "''") + "' BEGIN SELECT RAISE(ABORT,'local late fault'); END"
			}
			if _, err = stacks.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := stacks.db.Exec("DROP TRIGGER IF EXISTS cross_ack"); err != nil {
					t.Error(err)
				}
			})
			before := physicalDeleteFrame(t, stacks.db)
			if w := physicalDeleteQuery(g, "DeleteStack", "cross", ""); w.Code != 500 || physicalDeleteFrame(t, stacks.db) != before {
				t.Fatal("late cross-resource reinsertion was acknowledged or partially committed", w.Code)
			}
			if _, err = stacks.db.Exec("DROP TRIGGER cross_ack"); err != nil {
				t.Fatal(err)
			}
			if w := physicalDeleteQuery(g, "DeleteStack", "cross", ""); w.Code != 200 {
				t.Fatal("repair", w.Code)
			}
		})
	}
}
