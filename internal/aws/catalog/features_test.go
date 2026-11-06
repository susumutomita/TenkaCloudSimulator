package catalog

import "testing"

// featureModel exercises Put (create-or-replace), account singletons and
// batch reads that report missing identifiers in an official failure list.
func featureModel() *Model {
	m := syntheticModel()
	m.Name, m.Metadata.TargetPrefix = "features", "Features_2026"
	m.Routes = map[string]Route{"PutRule": {}, "DescribeRules": {}, "GetSettings": {}, "UpdateSettings": {}, "PutSettings": {}}
	m.Operations = map[string]*Operation{
		"PutRule":        {Name: "PutRule", Input: "PutRuleInput", Output: "PutRuleOutput"},
		"DescribeRules":  {Name: "DescribeRules", Input: "DescribeRulesInput", Output: "DescribeRulesOutput"},
		"GetSettings":    {Name: "GetSettings", Output: "SettingsOutput"},
		"UpdateSettings": {Name: "UpdateSettings", Input: "Settings", Output: "SettingsOutput"},
		"PutSettings":    {Name: "PutSettings", Input: "Settings", Output: "SettingsOutput"},
	}
	m.Shapes["Rule"] = &Shape{Type: "structure", Members: map[string]Member{"Name": {Shape: "Name"}, "RuleArn": {Shape: "Text"}, "State": {Shape: "Text"}, "Size": {Shape: "Size"}}}
	m.Shapes["PutRuleInput"] = &Shape{Type: "structure", Required: []string{"Name"}, Members: map[string]Member{"Name": {Shape: "Name"}, "State": {Shape: "Text"}, "Size": {Shape: "Size"}, "Tags": {Shape: "Tags"}}}
	m.Shapes["PutRuleOutput"] = &Shape{Type: "structure", Members: map[string]Member{"RuleArn": {Shape: "Text"}}}
	m.Shapes["Failure"] = &Shape{Type: "structure", Members: map[string]Member{"Arn": {Shape: "Text"}, "Reason": {Shape: "Text"}}}
	m.Shapes["Failures"] = &Shape{Type: "list", Member: &Member{Shape: "Failure"}}
	m.Shapes["Rules"] = &Shape{Type: "list", Member: &Member{Shape: "Rule"}}
	m.Shapes["DescribeRulesInput"] = &Shape{Type: "structure", Required: []string{"Names"}, Members: map[string]Member{"Names": {Shape: "Names"}}}
	m.Shapes["DescribeRulesOutput"] = &Shape{Type: "structure", Members: map[string]Member{"Rules": {Shape: "Rules"}, "Failures": {Shape: "Failures"}}}
	m.Shapes["Settings"] = &Shape{Type: "structure", Members: map[string]Member{"Mode": {Shape: "Text"}, "Size": {Shape: "Size"}}}
	m.Shapes["SettingsOutput"] = &Shape{Type: "structure", Members: map[string]Member{"Settings": {Shape: "Settings"}}}
	return m
}

func featureSpec() *Service {
	return &Service{
		Model: "features", Title: "Features", IAMPrefix: "features", Issues: []int{1}, Bounds: []string{"test only"},
		Resources: []*Resource{
			{Kind: "rule", Shape: "Rule", ID: "name", ARN: "arn:{partition}:features:{region}:{account}:rule/{name}", ARNMember: "RuleArn", NameMember: "Name", Unique: true,
				Generate: map[string]any{"State": "ENABLED"}},
			{Kind: "settings", Shape: "Settings", Singleton: true, ID: "name", ARN: "arn:{partition}:features:{region}:{account}:settings", NameMember: "Mode",
				Generate: map[string]any{"Mode": "STANDARD", "Size": 1}},
		},
		Ops: []Op{
			{Name: "PutRule", Kind: Put, Resource: "rule", Lookup: "Name"},
			{Name: "DescribeRules", Kind: List, Resource: "rule", Names: "Names", Failures: "Failures", FailureItem: map[string]any{"Arn": "$name", "Reason": "MISSING"}},
			{Name: "GetSettings", Kind: Read, Resource: "settings"},
			{Name: "UpdateSettings", Kind: Update, Resource: "settings", Fields: []string{"Mode", "Size"}},
			{Name: "PutSettings", Kind: Put, Resource: "settings"},
		},
	}
}

func featureHarness(t *testing.T) *harness {
	h := newHarness(t, ecr)
	b, err := bindModel(featureSpec(), featureModel())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPutCreatesThenReplacesSuppliedMembers(t *testing.T) {
	h := featureHarness(t)
	first := h.json1("features", "PutRule", map[string]any{"Name": "nightly", "Size": 2, "Tags": []any{map[string]any{"Key": "a", "Value": "1"}}})
	expect(t, first, 200, "")
	arn := str(first.body["RuleArn"])
	second := h.json1("features", "PutRule", map[string]any{"Name": "nightly", "State": "DISABLED", "Tags": []any{map[string]any{"Key": "b", "Value": "2"}}})
	expect(t, second, 200, "")
	if second.body["RuleArn"] != arn {
		t.Fatalf("put changed identity: %v", second.body)
	}
	described := h.json1("features", "DescribeRules", map[string]any{"Names": []any{"nightly", "ghost"}})
	expect(t, described, 200, "")
	rules := described.body["Rules"].([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["State"] != "DISABLED" || asText(rules[0].(map[string]any)["Size"]) != "2" {
		t.Fatalf("put did not replace only supplied members: %v", rules)
	}
	failures := described.body["Failures"].([]any)
	if len(failures) != 1 || failures[0].(map[string]any)["Arn"] != "ghost" || failures[0].(map[string]any)["Reason"] != "MISSING" {
		t.Fatalf("failures %v", failures)
	}
	if got := h.json1("features", "DescribeRules", map[string]any{"Names": []any{"nightly"}}); len(got.body["Failures"].([]any)) != 0 {
		t.Fatalf("empty failures must be an empty list: %v", got.body)
	}
	h.policy.deny["features:PutRule "+arn] = true
	expect(t, h.json1("features", "PutRule", map[string]any{"Name": "nightly", "State": "ENABLED"}), 403, "AccessDeniedException")
	delete(h.policy.deny, "features:PutRule "+arn)
	expect(t, h.json1("features", "PutRule", map[string]any{"Name": "nightly", "Mystery": "x"}), 400, "ValidationException")
	many := []any{}
	for i := 0; i < maxTagsPerResource; i++ {
		many = append(many, map[string]any{"Key": string(rune('A' + i%26)) + string(rune('a'+i/26)), "Value": "v"})
	}
	expect(t, h.json1("features", "PutRule", map[string]any{"Name": "nightly", "Tags": many}), 400, "LimitExceededException")
}

func TestSingletonDefaultsAreReadableAndStoredOnWrite(t *testing.T) {
	h := featureHarness(t)
	got := h.json1("features", "GetSettings", map[string]any{})
	expect(t, got, 200, "")
	if got.body["Settings"].(map[string]any)["Mode"] != "STANDARD" {
		t.Fatalf("defaults %v", got.body)
	}
	var rows int
	_ = h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources").Scan(&rows)
	if rows != 0 {
		t.Fatal("reading a singleton must not store it")
	}
	expect(t, h.json1("features", "UpdateSettings", map[string]any{"Mode": "FAST"}), 200, "")
	expect(t, h.json1("features", "UpdateSettings", map[string]any{"Size": 4}), 200, "")
	settings := h.json1("features", "GetSettings", map[string]any{}).body["Settings"].(map[string]any)
	if settings["Mode"] != "FAST" || asText(settings["Size"]) != "4" {
		t.Fatalf("singleton update %v", settings)
	}
	expect(t, h.json1("features", "PutSettings", map[string]any{"Mode": "SLOW"}), 200, "")
	if h.json1("features", "GetSettings", map[string]any{}).body["Settings"].(map[string]any)["Mode"] != "SLOW" {
		t.Fatal("put on stored singleton")
	}
	_ = h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources").Scan(&rows)
	if rows != 1 {
		t.Fatalf("one singleton row expected, got %d", rows)
	}
	h.policy.deny["features:GetSettings"] = true
	expect(t, h.json1("features", "GetSettings", map[string]any{}), 403, "AccessDeniedException")
	fresh := featureHarness(t)
	expect(t, fresh.json1("features", "PutSettings", map[string]any{"Mode": "NEW"}), 200, "")
	bad := featureSpec()
	bad.Ops[1].Failures = "Nope"
	if _, err := bindModel(bad, featureModel()); err == nil {
		t.Fatal("failures member must exist in the official output")
	}
}
