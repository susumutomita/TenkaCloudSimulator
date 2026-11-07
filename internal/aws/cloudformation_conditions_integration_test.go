package aws

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Exact unmodified test input from the user-owned Apache-2.0 Challenge catalog:
// susumutomita/TenkaCloudChallenge a749b142416770abee3f173673702c55df3856aa,
// battles/crypto-battle/template.yaml. License retained beside this fixture.
// No problem code executes, and this is not a coordination/scoring substitute.
// Go test runs this package from internal/aws. The fixture is test-only; it is
// not embedded into the regular simulator binary or executed as problem code.
func stackCryptoTemplate(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../test/fixtures/crypto-battle-a749/template.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func stackCryptoParameters(enabled bool) map[string]string {
	p := map[string]string{"NamePrefix": "tc-crypto-team", "TenkaCloudAccountId": "000000000000", "ExternalId": strings.Repeat("L", 16), "ScoreStealSeed": strings.Repeat("D", 32)}
	if enabled {
		p["ScoreStealEnabled"] = "true"
	}
	return p
}

func stackConditionalDBFrame(t *testing.T, db *sql.DB) string {
	return stackConditionalFrameExceptHistory(t, db, "")
}

// Ignore only an explicitly verified completed generation when comparing a
// successful lifecycle with its baseline. Failure/peer checks exclude nothing.
func stackConditionalFrameExceptHistory(t *testing.T, db *sql.DB, completedID string) string {
	t.Helper()
	names, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	tables := []string{}
	for names.Next() {
		var table string
		if err = names.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err = names.Err(); err != nil {
		t.Fatal(err)
	}
	names.Close()
	h := sha256.New()
	for _, table := range tables {
		query := `SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`
		var args []any
		if completedID != "" && (table == "aws_cfn_events" || table == "aws_cfn_event_history") {
			query += " WHERE stack_id<>?"
			args = append(args, completedID)
		}
		rows, err := db.Query(query, args...)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		encoded := []string{}
		for rows.Next() {
			values := make([]any, len(cols))
			pointers := make([]any, len(cols))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			for i, v := range values {
				if b, ok := v.([]byte); ok {
					values[i] = string(b)
				}
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			encoded = append(encoded, string(raw))
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		sort.Strings(encoded)
		fmt.Fprintln(h, table, cols, encoded)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestStackConditionalOriginalCryptoNativeLifecycle(t *testing.T) {
	stackCryptoOriginal := stackCryptoTemplate(t)
	digest := sha256.Sum256([]byte(stackCryptoOriginal))
	if hex.EncodeToString(digest[:]) != "fb2af9be3ba67619b80de0ad8f00dc102aaade740742aea5861c9039d8bd09e6" {
		t.Fatal("original catalog fixture changed")
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			db, s3, stacks, parameters := s3StackState(t)
			peer, _, _, _ := s3StackState(t)
			peerBefore := stackConditionalDBFrame(t, peer.Database())
			identities := core.NewIdentities(db)
			g := &Gateway{Store: db, S3: s3, Stacks: stacks, Parameters: parameters, Identities: identities}
			send := func(action, name, body string, params map[string]string, participant bool) *httptest.ResponseRecorder {
				form := url.Values{"Action": {action}, "Version": {"2010-05-15"}, "StackName": {name}}
				if body != "" {
					form.Set("TemplateBody", body)
					form.Set("Capabilities.member.1", "CAPABILITY_IAM")
				}
				keys := []string{}
				for k := range params {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for i, k := range keys {
					prefix := fmt.Sprintf("Parameters.member.%d.", i+1)
					form.Set(prefix+"ParameterKey", k)
					form.Set(prefix+"ParameterValue", params[k])
				}
				r := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("X-Simulator-Region", "ap-northeast-1")
				if participant {
					r.Header.Set("X-Simulator-Access-Role", "participant")
				}
				w := httptest.NewRecorder()
				g.serveCFN(w, r)
				return w
			}
			response := send("CreateStack", "crypto", stackCryptoOriginal, stackCryptoParameters(enabled), false)
			if response.Code != 200 {
				t.Fatal("original full-template CreateStack failed", response.Code)
			}
			records, err := stacks.Records("ap-northeast-1", "crypto")
			if err != nil || len(records) != 1 {
				t.Fatal("stack was not durably created")
			}
			record := records[0]
			expectedResources := 1
			if enabled {
				expectedResources = 2
			}
			if len(record.Resources) != expectedResources {
				t.Fatal("conditional resource set wrong")
			}
			var role string
			for _, r := range record.Resources {
				if r.LogicalID == "ParticipantViewerRole" {
					role = r.PhysicalID
				}
			}
			policy, found, err := identities.Policy(role)
			if err != nil || !found {
				t.Fatal("actual legacy role missing")
			}
			target := "arn:aws:ssm:ap-northeast-1:000000000000:parameter/tc-crypto-team/score-item"
			allowed := core.Evaluate(policy, "ssm:GetParameter", target) == "allowed"
			if allowed != enabled {
				t.Fatal("conditional inline policy grant wrong")
			}
			if core.Evaluate(policy, "ssm:GetParameter", target+"-peer") == "allowed" {
				t.Fatal("conditional policy crossed exact SSM scope")
			}
			item, err := parameters.Get("ap-northeast-1", "/tc-crypto-team/score-item")
			if enabled {
				if err != nil {
					t.Fatal("selected parameter not persisted")
				}
				var value struct {
					Key     int    `json:"key"`
					Receipt string `json:"receipt"`
				}
				if json.Unmarshal([]byte(item.Value), &value) != nil || value.Key != 3 || value.Receipt != strings.Repeat("D", 32) {
					t.Fatal("original parameter substitution changed")
				}
				if record.Outputs["CoordinationPrivateItem"] != item.Value {
					t.Fatal("host coordination output mismatch")
				}
			} else {
				if err == nil {
					t.Fatal("disabled parameter was created")
				}
				for _, key := range []string{"CoordinationPrivateItem", "CoordinationParameterName", "CoordinationParameterConsoleUrl"} {
					if _, exists := record.Outputs[key]; exists {
						t.Fatal("disabled output published")
					}
				}
			}
			hidden := send("DescribeStacks", "crypto", "", nil, true)
			if hidden.Code != 403 || strings.Contains(hidden.Body.String(), strings.Repeat("D", 32)) {
				t.Fatal("participant received host-private stack output")
			}
			if response = send("DeleteStack", record.ID, "", nil, false); response.Code != 200 {
				t.Fatal("native conditional stack cleanup failed")
			}
			if _, err = parameters.Get("ap-northeast-1", "/tc-crypto-team/score-item"); err == nil {
				t.Fatal("conditional cleanup retained SSM item")
			}
			if _, exists, err := identities.Policy(role); err != nil || exists {
				t.Fatal("conditional cleanup retained role")
			}
			if peerBefore != stackConditionalDBFrame(t, peer.Database()) {
				t.Fatal("native stack lifecycle changed peer DB")
			}
		})
	}
}

func TestStackConditionalInvalidDefinitionsPreserveNativeFrame(t *testing.T) {
	stackCryptoOriginal := stackCryptoTemplate(t)
	db, _, _, _ := s3StackState(t)
	before := stackConditionalDBFrame(t, db.Database())
	for _, change := range []func(map[string]string){func(p map[string]string) { p["ScoreStealKey"] = "10" }, func(p map[string]string) { p["ScoreStealEnabled"] = "TRUE" }, func(p map[string]string) { p["ScoreStealSeed"] = strings.Repeat("-", 32) }, func(p map[string]string) { p["Unknown"] = "dummy" }} {
		p := stackCryptoParameters(true)
		change(p)
		record, err := compileStack("ap-northeast-1", "invalid", stackCryptoOriginal, p, "CAPABILITY_IAM")
		if err == nil || len(record.Resources) != 0 || len(record.Outputs) != 0 {
			t.Fatal("invalid original input produced a partial compilation")
		}
		if before != stackConditionalDBFrame(t, db.Database()) {
			t.Fatal("invalid condition/parameter compilation changed native graph")
		}
	}
	// Explicit positive for the formerly unsupported AllowedValues feature.
	good := `{"Parameters":{"P":{"Type":"String","Default":"x","AllowedValues":["x"]}},"Resources":{"Value":{"Type":"AWS::SSM::Parameter","Properties":{"Name":"/safe/allowed","Type":"String","Value":{"Ref":"P"}}}}}`
	if _, err := compileStack("us-east-1", "allowed", good, nil, ""); err != nil {
		t.Fatal("valid String AllowedValues refused")
	}
	if record, err := compileStack("us-east-1", "allowed", good, map[string]string{"P": "y"}, ""); err == nil || len(record.Resources) != 0 {
		t.Fatal("nonmember AllowedValues admitted")
	}
	if before != stackConditionalDBFrame(t, db.Database()) {
		t.Fatal("AllowedValues preflight changed native graph")
	}
}

func TestStackConditionalInactiveDeclarationsAndLinksFailClosed(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"Conditions": map[string]any{"Off": map[string]any{"Fn::Equals": []any{"false", "true"}}}, "Resources": map[string]any{"Hidden": map[string]any{"Type": "AWS::SSM::Parameter", "Condition": "Off", "Properties": map[string]any{"Name": "/conditional/hidden", "Type": "String", "Value": "dummy"}}, "Live": map[string]any{"Type": "AWS::SSM::Parameter", "Properties": map[string]any{"Name": "/conditional/live", "Type": "String", "Value": "dummy"}}}}
	}
	changes := []func(map[string]any){
		func(m map[string]any) { object(object(m["Resources"])["Hidden"])["Type"] = "AWS::EC2::Instance" },
		func(m map[string]any) { object(object(m["Resources"])["Hidden"])["DeletionPolicy"] = "Retain" },
		func(m map[string]any) {
			object(object(object(m["Resources"])["Hidden"])["Properties"])["Unknown"] = "ignored"
		},
		func(m map[string]any) {
			object(object(object(m["Resources"])["Hidden"])["Properties"])["Type"] = "SecureString"
		},
		func(m map[string]any) {
			object(object(object(m["Resources"])["Live"])["Properties"])["Value"] = map[string]any{"Ref": "Hidden"}
		},
		func(m map[string]any) {
			m["Outputs"] = map[string]any{"Leaked": map[string]any{"Value": map[string]any{"Ref": "Hidden"}}}
		},
		func(m map[string]any) {
			m["Outputs"] = map[string]any{"HiddenOutput": map[string]any{"Condition": "Off", "Value": map[string]any{"Ref": "Unknown"}}}
		},
		func(m map[string]any) {
			object(object(object(m["Resources"])["Live"])["Properties"])["Value"] = map[string]any{"Fn::If": []any{"Off", map[string]any{"Ref": "Unknown"}, "safe"}}
		},
		func(m map[string]any) { object(m["Conditions"])["Off"] = map[string]any{"Condition": "Off"} },
		func(m map[string]any) { object(object(m["Resources"])["Live"])["Condition"] = "Unknown" },
	}
	for i, change := range changes {
		m := base()
		change(m)
		body, _ := json.Marshal(m)
		if record, err := compileStack("us-east-1", "condition", string(body), nil, ""); err == nil || len(record.Resources) != 0 || len(record.Outputs) != 0 {
			t.Fatalf("inactive/unknown declaration %d admitted", i)
		}
	}
	m := base()
	m["Outputs"] = map[string]any{"HiddenOutput": map[string]any{"Condition": "Off", "Value": map[string]any{"Fn::GetAtt": "Hidden.Value"}}}
	body, _ := json.Marshal(m)
	record, err := compileStack("us-east-1", "condition", string(body), nil, "")
	if err != nil || len(record.Resources) != 1 || record.Resources[0].LogicalID != "Live" || len(record.Outputs) != 0 {
		t.Fatal("known inactive reference was resolved or published")
	}
}

func TestStackConditionalSelectedNativeFailureRollsBack(t *testing.T) {
	stackCryptoOriginal := stackCryptoTemplate(t)
	db, _, stacks, parameters := s3StackState(t)
	name := "/tc-crypto-team/score-item"
	if _, err := parameters.Put("ap-northeast-1", name, "String", "existing-dummy", nil, false); err != nil {
		t.Fatal(err)
	}
	record, err := compileStack("ap-northeast-1", "conditional-rollback", stackCryptoOriginal, stackCryptoParameters(true), "CAPABILITY_IAM")
	if err != nil || len(record.Resources) != 2 || record.Resources[0].LogicalID != "ParticipantViewerRole" {
		t.Fatal("original dependency order changed")
	}
	before := stackConditionalDBFrame(t, db.Database())
	if err = stacks.Create(record); err == nil {
		t.Fatal("selected existing SSM conflict was acknowledged")
	}
	if before != stackConditionalDBFrame(t, db.Database()) {
		t.Fatal("later selected conflict committed role/claim/stack or changed existing data")
	}
	if _, exists, err := core.NewIdentities(db).Policy(record.Resources[0].PhysicalID); err != nil || exists {
		t.Fatal("failed conditional stack left a role")
	}
	existing, err := parameters.Get("ap-northeast-1", name)
	if err != nil || existing.Value != "existing-dummy" {
		t.Fatal("failed conditional stack replaced existing SSM data")
	}
	if err = parameters.Delete("ap-northeast-1", name); err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(record); err != nil {
		t.Fatal("exact conditional stack retry after repair failed")
	}
	if err = stacks.Delete(record.Region, record.ID); err != nil {
		t.Fatal("conditional retry cleanup failed")
	}
}
