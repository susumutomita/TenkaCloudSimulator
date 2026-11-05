package aws

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func conditionBoundaryTemplate() map[string]any {
	return map[string]any{
		"Parameters": map[string]any{"Input": map[string]any{"Type": "String", "Default": "dummy"}},
		"Conditions": map[string]any{"Off": map[string]any{"Fn::Equals": []any{"off", "on"}}},
		"Resources": map[string]any{
			"Hidden": map[string]any{"Type": "AWS::SSM::Parameter", "Condition": "Off", "Properties": map[string]any{"Name": "/boundary/hidden", "Type": "String", "Value": "hidden"}},
			"Live":   map[string]any{"Type": "AWS::SSM::Parameter", "Properties": map[string]any{"Name": "/boundary/live", "Type": "String", "Value": map[string]any{"Ref": "Input"}}},
		},
	}
}

func conditionBoundaryBody(t *testing.T, template map[string]any) string {
	t.Helper()
	b, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func conditionBoundarySend(g *Gateway, action, body, principal string, supplied url.Values) *httptest.ResponseRecorder {
	form := url.Values{"Action": {action}, "Version": {"2010-05-15"}, "StackName": {"boundary"}}
	if body != "" {
		form.Set("TemplateBody", body)
	}
	for key, values := range supplied {
		form[key] = append([]string(nil), values...)
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Simulator-Region", "ap-northeast-1")
	if principal != "" {
		r.Header.Set("X-Simulator-Principal", principal)
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}

func conditionBoundaryError(t *testing.T, w *httptest.ResponseRecorder, status int, code, message string) {
	t.Helper()
	var parsed struct {
		XMLName xml.Name
		Error   struct{ Code, Message string }
	}
	if w.Code != status || xml.Unmarshal(w.Body.Bytes(), &parsed) != nil || parsed.XMLName.Local != "ErrorResponse" || parsed.XMLName.Space != "http://cloudformation.amazonaws.com/doc/2010-05-15/" || parsed.Error.Code != code || !strings.Contains(parsed.Error.Message, message) {
		t.Fatalf("native error contract status=%d code=%q message=%q", w.Code, parsed.Error.Code, parsed.Error.Message)
	}
}

// Every malformed declaration reaches the real Query handler and compiler. A
// corrected request then creates/deletes actual SSM state; validation must not
// reserve a stack, claim or physical resource, even for disabled declarations.
func TestStackConditionBoundaryAdmissionRefusesWithoutGraphAndRecovers(t *testing.T) {
	parameter := func(m map[string]any) map[string]any { return object(object(m["Parameters"])["Input"]) }
	live := func(m map[string]any) map[string]any {
		return object(object(object(m["Resources"])["Live"])["Properties"])
	}
	cases := []struct {
		name, message string
		change        func(map[string]any)
	}{
		{"logical_name", "invalid logical name", func(m map[string]any) {
			m["Parameters"] = map[string]any{"bad-name": map[string]any{"Type": "String", "Default": "dummy"}}
			live(m)["Value"] = "dummy"
		}},
		{"non_object_definition", "definition must be an object", func(m map[string]any) { object(m["Parameters"])["Input"] = "not-an-object" }},
		{"description_type", "invalid description", func(m map[string]any) { parameter(m)["Description"] = true }},
		{"description_bound", "invalid description", func(m map[string]any) { parameter(m)["ConstraintDescription"] = strings.Repeat("D", 4001) }},
		{"pattern_bound", "invalid AllowedPattern", func(m map[string]any) { parameter(m)["AllowedPattern"] = strings.Repeat("x", 1025) }},
		{"pattern_type", "invalid AllowedPattern", func(m map[string]any) { parameter(m)["AllowedPattern"] = true }},
		{"number_with_length", "length requires String", func(m map[string]any) {
			parameter(m)["Type"] = "Number"
			parameter(m)["Default"] = "3"
			parameter(m)["MinLength"] = "1"
		}},
		{"numeric_constraint", "invalid numeric constraint", func(m map[string]any) {
			parameter(m)["Type"] = "Number"
			parameter(m)["Default"] = "3"
			parameter(m)["MinValue"] = "NaN"
		}},
		{"numeric_allowed_member", "invalid numeric AllowedValues", func(m map[string]any) {
			parameter(m)["Type"] = "Number"
			parameter(m)["Default"] = "3"
			parameter(m)["AllowedValues"] = []any{"NaN"}
		}},
		{"length_null", "invalid length constraint", func(m map[string]any) { parameter(m)["MinLength"] = nil }},
		{"length_text_bound", "invalid length constraint", func(m map[string]any) { parameter(m)["MaxLength"] = "1234567" }},
		{"unused_condition_name", "invalid condition name", func(m map[string]any) {
			object(m["Conditions"])["1Bad"] = map[string]any{"Fn::Equals": []any{"a", "a"}}
		}},
		{"unused_alias_type", "invalid condition alias", func(m map[string]any) { object(m["Conditions"])["Alias"] = map[string]any{"Condition": false} }},
		{"unused_right_operand", "condition operand must be scalar", func(m map[string]any) {
			object(m["Conditions"])["Extra"] = map[string]any{"Fn::Equals": []any{"valid-left", true}}
		}},
		{"unused_alias_depth", "condition depth exceeds 32", func(m map[string]any) {
			d := object(m["Conditions"])
			for i := 0; i < 33; i++ {
				d[fmt.Sprintf("C%02d", i)] = map[string]any{"Condition": fmt.Sprintf("C%02d", i+1)}
			}
			d["C33"] = map[string]any{"Fn::Equals": []any{"a", "a"}}
		}},
		{"condition_count", "condition count exceeds 100", func(m map[string]any) {
			d := object(m["Conditions"])
			for i := 0; i < 100; i++ {
				d[fmt.Sprintf("C%03d", i)] = map[string]any{"Fn::Equals": []any{"a", "a"}}
			}
		}},
		{"if_name_type", "Invalid conditional resource properties", func(m map[string]any) { live(m)["Value"] = map[string]any{"Fn::If": []any{true, "good", "also-good"}} }},
		{"ref_attribute_carrier", "Ref requires a logical ID", func(m map[string]any) { live(m)["Value"] = map[string]any{"Ref": "Hidden.Value"} }},
		{"inactive_bad_attribute", "unsupported resource attribute reference", func(m map[string]any) {
			live(m)["Value"] = map[string]any{"Fn::If": []any{"Off", map[string]any{"Fn::GetAtt": "Hidden.Arn"}, "safe"}}
		}},
		{"unterminated_sub", "unterminated substitution", func(m map[string]any) { live(m)["Value"] = map[string]any{"Fn::Sub": "${Input"} }},
		{"dependency_type", "invalid DependsOn", func(m map[string]any) { object(object(m["Resources"])["Live"])["DependsOn"] = true }},
		{"dependency_empty", "empty DependsOn", func(m map[string]any) { object(object(m["Resources"])["Live"])["DependsOn"] = []any{} }},
		{"dependency_member", "invalid DependsOn logical ID", func(m map[string]any) { object(object(m["Resources"])["Live"])["DependsOn"] = []any{true} }},
		{"active_dependency_on_disabled", "depends on a disabled resource", func(m map[string]any) { object(object(m["Resources"])["Live"])["DependsOn"] = "Hidden" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, s3, stacks, parameters := s3StackState(t)
			peer, _, _, _ := s3StackState(t)
			if _, err := parameters.Put("ap-northeast-1", "/boundary/neighbor", "String", "neighbor", nil, false); err != nil {
				t.Fatal(err)
			}
			g := &Gateway{Store: db, S3: s3, Stacks: stacks, Parameters: parameters, Identities: core.NewIdentities(db)}
			before, peerBefore := stackConditionalDBFrame(t, db.Database()), stackConditionalDBFrame(t, peer.Database())
			m := conditionBoundaryTemplate()
			tc.change(m)
			body := conditionBoundaryBody(t, m)
			plan, err := compileStack("ap-northeast-1", "boundary", body, nil, "")
			if err == nil || !reflect.DeepEqual(plan, StackRecord{}) {
				t.Fatal("invalid declaration returned a partial plan")
			}
			conditionBoundaryError(t, conditionBoundarySend(g, "CreateStack", body, "", nil), 400, "ValidationError", tc.message)
			if stackConditionalDBFrame(t, db.Database()) != before || stackConditionalDBFrame(t, peer.Database()) != peerBefore {
				t.Fatal("refusal changed native graph/neighbor/peer")
			}
			repaired := conditionBoundaryBody(t, conditionBoundaryTemplate())
			if w := conditionBoundarySend(g, "CreateStack", repaired, "", nil); w.Code != 200 {
				t.Fatalf("repaired request status %d", w.Code)
			}
			item, err := parameters.Get("ap-northeast-1", "/boundary/live")
			if err != nil || item.Value != "dummy" {
				t.Fatal("corrected create did not reach native SSM")
			}
			if _, err = parameters.Get("ap-northeast-1", "/boundary/hidden"); err == nil {
				t.Fatal("disabled declaration was provisioned")
			}
			if w := conditionBoundarySend(g, "DeleteStack", "", "", nil); w.Code != 200 {
				t.Fatalf("cleanup status %d", w.Code)
			}
			if stackConditionalDBFrame(t, db.Database()) != before || stackConditionalDBFrame(t, peer.Database()) != peerBefore {
				t.Fatal("repair/cleanup changed preexisting state")
			}
		})
	}
}

func TestStackConditionBoundarySuppliedBytesAndDuplicateNames(t *testing.T) {
	db, s3, stacks, parameters := s3StackState(t)
	g := &Gateway{Store: db, S3: s3, Stacks: stacks, Parameters: parameters, Identities: core.NewIdentities(db)}
	body := conditionBoundaryBody(t, conditionBoundaryTemplate())
	before := stackConditionalDBFrame(t, db.Database())
	large := strings.Repeat("V", 51201)
	supplied := url.Values{"Parameters.member.1.ParameterKey": {"Input"}, "Parameters.member.1.ParameterValue": {large}}
	rejected := conditionBoundarySend(g, "CreateStack", body, "", supplied)
	conditionBoundaryError(t, rejected, 400, "ValidationError", "value exceeds local byte limit")
	if strings.Contains(rejected.Body.String(), large) || stackConditionalDBFrame(t, db.Database()) != before {
		t.Fatal("oversized supplied value reflected or persisted")
	}
	supplied.Set("Parameters.member.1.ParameterValue", "first")
	supplied.Set("Parameters.member.2.ParameterKey", "Input")
	supplied.Set("Parameters.member.2.ParameterValue", "second")
	conditionBoundaryError(t, conditionBoundarySend(g, "CreateStack", body, "", supplied), 400, "ValidationError", "Duplicate parameter name")
	if stackConditionalDBFrame(t, db.Database()) != before {
		t.Fatal("duplicate parameter name changed state")
	}
	supplied.Del("Parameters.member.2.ParameterKey")
	supplied.Del("Parameters.member.2.ParameterValue")
	if w := conditionBoundarySend(g, "CreateStack", body, "", supplied); w.Code != 200 {
		t.Fatal("fixed member list rejected")
	}
	item, err := parameters.Get("ap-northeast-1", "/boundary/live")
	if err != nil || item.Value != "first" {
		t.Fatal("repaired supplied value not used")
	}
	if w := conditionBoundarySend(g, "DeleteStack", "", "", nil); w.Code != 200 {
		t.Fatal("cleanup failed")
	}
	if stackConditionalDBFrame(t, db.Database()) != before {
		t.Fatal("cleanup did not restore baseline")
	}
}

// These are pure-port bounds: a >51,200-byte literal cannot fit inside the
// already-bounded native TemplateBody. Do not mislabel this as HTTP parity.
func TestStackConditionBoundaryPureOperandAndInactiveLiteralBudget(t *testing.T) {
	huge := strings.Repeat("Z", 51201)
	definitions := map[string]any{"Unused": map[string]any{"Fn::Equals": []any{"left", huge}}}
	before := stackConditionTestCopy(t, definitions)
	if values, err := stackConditionValues(definitions, nil); err == nil || values != nil || !reflect.DeepEqual(definitions, before) {
		t.Fatal("oversized unused condition operand returned result or changed input")
	}
	value := map[string]any{"Fn::If": []any{"Off", huge, "selected-safe"}}
	saved := stackConditionTestCopy(t, value)
	if selected, omit, err := stackSelectConditional(value, map[string]bool{"Off": false}); err == nil || selected != nil || omit || !reflect.DeepEqual(value, saved) {
		t.Fatal("invalid unchosen literal bypassed validation or changed caller")
	}
	object(definitions["Unused"])["Fn::Equals"] = []any{"equal", "equal"}
	if values, err := stackConditionValues(definitions, nil); err != nil || !values["Unused"] {
		t.Fatal("corrected condition port did not recover")
	}
	value["Fn::If"] = []any{"Off", "valid-unselected", "selected-safe"}
	if selected, omit, err := stackSelectConditional(value, map[string]bool{"Off": false}); err != nil || omit || selected != "selected-safe" {
		t.Fatal("corrected selection port did not recover")
	}
}

func TestStackConditionBoundaryPolicyOutageIsNotDeniedAndRecovers(t *testing.T) {
	db, s3, stacks, parameters := s3StackState(t)
	identities := core.NewIdentities(db)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["cloudformation:*","ssm:*"],"Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = identities.Set("boundary-caller", policy); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Store: db, S3: s3, Stacks: stacks, Parameters: parameters, Identities: identities}
	body := conditionBoundaryBody(t, conditionBoundaryTemplate())
	before := stackConditionalDBFrame(t, db.Database())
	if _, err = db.Database().Exec("ALTER TABLE identity_policies RENAME TO boundary_policy_outage"); err != nil {
		t.Fatal(err)
	}
	broken := stackConditionalDBFrame(t, db.Database())
	response := conditionBoundarySend(g, "CreateStack", body, "boundary-caller", nil)
	conditionBoundaryError(t, response, 500, "InternalFailure", "Simulator stack operation failed")
	if strings.Contains(response.Body.String(), "boundary_policy_outage") || strings.Contains(response.Body.String(), "no such table") || stackConditionalDBFrame(t, db.Database()) != broken {
		t.Fatal("policy outage leaked storage details or changed state")
	}
	if _, err = db.Database().Exec("ALTER TABLE boundary_policy_outage RENAME TO identity_policies"); err != nil {
		t.Fatal(err)
	}
	if stackConditionalDBFrame(t, db.Database()) != before {
		t.Fatal("schema repair changed identity data")
	}
	if response = conditionBoundarySend(g, "CreateStack", body, "boundary-caller", nil); response.Code != 200 {
		t.Fatal("repaired caller could not create")
	}
	if response = conditionBoundarySend(g, "DeleteStack", "", "boundary-caller", nil); response.Code != 200 {
		t.Fatal("repaired caller could not delete")
	}
	if stackConditionalDBFrame(t, db.Database()) != before {
		t.Fatal("post-repair lifecycle changed preexisting identity")
	}
}
