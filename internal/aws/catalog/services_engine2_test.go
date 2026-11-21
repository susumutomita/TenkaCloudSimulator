package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func nfPolicyBody(extra map[string]any) map[string]any {
	body := map[string]any{"StatelessDefaultActions": []any{"aws:forward_to_sfe"}, "StatelessFragmentDefaultActions": []any{"aws:drop"}}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func TestNetworkFirewallPolicyTokenLifecycle(t *testing.T) {
	h := newHarness(t, networkFirewall)
	nf := func(op string, input map[string]any) reply { return h.json1("network-firewall", op, input) }
	created := nf("CreateFirewallPolicy", map[string]any{"FirewallPolicyName": "edge", "FirewallPolicy": nfPolicyBody(nil), "Description": "first",
		"Tags": []any{map[string]any{"Key": "team", "Value": "a"}}, "DryRun": false})
	expect(t, created, 200, "")
	token := str(created.body["UpdateToken"])
	response := created.body["FirewallPolicyResponse"].(map[string]any)
	arn := str(response["FirewallPolicyArn"])
	if len(token) != 36 || arn != "arn:aws:network-firewall:us-east-1:000000000000:firewall-policy/edge" || response["FirewallPolicyStatus"] != "ACTIVE" ||
		len(str(response["FirewallPolicyId"])) != 36 || response["NumberOfAssociations"] != json.Number("0") || response["LastModifiedTime"] == nil {
		t.Fatalf("created %v", created.body)
	}
	if _, leaked := response["FirewallPolicy"]; leaked {
		t.Fatalf("the body is kept beside the metadata: %v", response)
	}
	if _, present := created.body["FirewallPolicy"]; present {
		t.Fatal("CreateFirewallPolicy has no FirewallPolicy output member")
	}
	described := nf("DescribeFirewallPolicy", map[string]any{"FirewallPolicyArn": arn})
	expect(t, described, 200, "")
	body := described.body["FirewallPolicy"].(map[string]any)
	if described.body["UpdateToken"] != token || body["StatelessFragmentDefaultActions"].([]any)[0] != "aws:drop" ||
		described.body["FirewallPolicyResponse"].(map[string]any)["Tags"].([]any)[0].(map[string]any)["Value"] != "a" {
		t.Fatalf("described %v", described.body)
	}
	listed := nf("ListFirewallPolicies", map[string]any{}).body["FirewallPolicies"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["Name"] != "edge" || listed[0].(map[string]any)["Arn"] != arn {
		t.Fatalf("listed %v", listed)
	}

	// A stale token changes nothing; the current one rotates it.
	stale := "00000000-0000-4000-8000-000000000000"
	expect(t, nf("UpdateFirewallPolicy", map[string]any{"FirewallPolicyName": "edge", "UpdateToken": stale, "FirewallPolicy": nfPolicyBody(nil), "Description": "lost"}), 400, "InvalidTokenException")
	if nf("DescribeFirewallPolicy", map[string]any{"FirewallPolicyName": "edge"}).body["FirewallPolicyResponse"].(map[string]any)["Description"] != "first" {
		t.Fatal("a stale update changed the policy")
	}
	h.policy.deny["network-firewall:UpdateFirewallPolicy"] = true
	expect(t, nf("UpdateFirewallPolicy", map[string]any{"FirewallPolicyName": "edge", "UpdateToken": stale, "FirewallPolicy": nfPolicyBody(nil)}), 403, "")
	delete(h.policy.deny, "network-firewall:UpdateFirewallPolicy")
	updated := nf("UpdateFirewallPolicy", map[string]any{"FirewallPolicyName": "edge", "UpdateToken": token,
		"FirewallPolicy": nfPolicyBody(map[string]any{"StatefulDefaultActions": []any{"aws:drop_strict"}, "StatefulEngineOptions": map[string]any{"RuleOrder": "STRICT_ORDER"}}), "Description": "second"})
	expect(t, updated, 200, "")
	next := str(updated.body["UpdateToken"])
	if next == token || len(next) != 36 || updated.body["FirewallPolicyResponse"].(map[string]any)["Description"] != "second" {
		t.Fatalf("updated %v", updated.body)
	}
	described = nf("DescribeFirewallPolicy", map[string]any{"FirewallPolicyName": "edge"})
	if described.body["UpdateToken"] != next || described.body["FirewallPolicy"].(map[string]any)["StatefulDefaultActions"].([]any)[0] != "aws:drop_strict" {
		t.Fatalf("after update %v", described.body)
	}
	expect(t, nf("UpdateFirewallPolicy", map[string]any{"FirewallPolicyName": "edge", "UpdateToken": token, "FirewallPolicy": nfPolicyBody(nil)}), 400, "InvalidTokenException")
	expect(t, nf("UpdateFirewallPolicy", map[string]any{"FirewallPolicyName": "edge", "FirewallPolicy": nfPolicyBody(nil)}), 400, "InvalidRequestException")
	expect(t, nf("UpdateFirewallPolicy", map[string]any{"FirewallPolicyName": "missing", "UpdateToken": next, "FirewallPolicy": nfPolicyBody(nil)}), 400, "ResourceNotFoundException")

	// Refusals instead of silently ignored members.
	expect(t, nf("CreateFirewallPolicy", map[string]any{"FirewallPolicyName": "dry", "FirewallPolicy": nfPolicyBody(nil), "DryRun": true}), 400, "InvalidRequestException")
	expect(t, nf("CreateFirewallPolicy", map[string]any{"FirewallPolicyName": "refs", "FirewallPolicy": nfPolicyBody(map[string]any{
		"StatefulRuleGroupReferences": []any{map[string]any{"ResourceArn": "arn:aws:network-firewall:us-east-1:000000000000:stateful-rulegroup/x"}}})}), 400, "InvalidRequestException")
	expect(t, nf("CreateFirewallPolicy", map[string]any{"FirewallPolicyName": "kms", "FirewallPolicy": nfPolicyBody(nil), "EncryptionConfiguration": map[string]any{"Type": "CUSTOMER_KMS"}}), 400, "InvalidRequestException")
	expect(t, nf("CreateFirewallPolicy", map[string]any{"FirewallPolicyName": "edge", "FirewallPolicy": nfPolicyBody(nil)}), 400, "InvalidRequestException")
	if n := len(nf("ListFirewallPolicies", map[string]any{}).body["FirewallPolicies"].([]any)); n != 1 {
		t.Fatalf("refused creates stored %d policies", n)
	}

	expect(t, nf("TagResource", map[string]any{"ResourceArn": arn, "Tags": []any{map[string]any{"Key": "env", "Value": "lab"}}}), 200, "")
	tags := nf("ListTagsForResource", map[string]any{"ResourceArn": arn}).body["Tags"].([]any)
	if len(tags) != 2 {
		t.Fatalf("tags %v", tags)
	}
	if nf("DescribeFirewallPolicy", map[string]any{"FirewallPolicyName": "edge"}).body["UpdateToken"] != next {
		t.Fatal("tagging rotated the update token")
	}
	expect(t, nf("UntagResource", map[string]any{"ResourceArn": arn, "TagKeys": []any{"env"}}), 200, "")
	deleted := nf("DeleteFirewallPolicy", map[string]any{"FirewallPolicyArn": arn})
	expect(t, deleted, 200, "")
	if deleted.body["FirewallPolicyResponse"].(map[string]any)["FirewallPolicyStatus"] != "DELETING" {
		t.Fatalf("deleted %v", deleted.body)
	}
	expect(t, nf("DescribeFirewallPolicy", map[string]any{"FirewallPolicyName": "edge"}), 400, "ResourceNotFoundException")
}

func TestNetworkFirewallTLSInspectionConfiguration(t *testing.T) {
	h := newHarness(t, networkFirewall)
	nf := func(op string, input map[string]any) reply { return h.json1("network-firewall", op, input) }
	config := map[string]any{"ServerCertificateConfigurations": []any{map[string]any{
		"ServerCertificates": []any{map[string]any{"ResourceArn": "arn:aws:acm:us-east-1:000000000000:certificate/0f1e2d3c-aaaa-4bbb-8ccc-111122223333"}},
		"Scopes":             []any{map[string]any{"Protocols": []any{6}, "DestinationPorts": []any{map[string]any{"FromPort": 443, "ToPort": 443}}}}}}}
	created := nf("CreateTLSInspectionConfiguration", map[string]any{"TLSInspectionConfigurationName": "inbound", "TLSInspectionConfiguration": config})
	expect(t, created, 200, "")
	token := str(created.body["UpdateToken"])
	arn := str(created.body["TLSInspectionConfigurationResponse"].(map[string]any)["TLSInspectionConfigurationArn"])
	if !strings.HasSuffix(arn, ":tls-configuration/inbound") {
		t.Fatalf("arn %s", arn)
	}
	described := nf("DescribeTLSInspectionConfiguration", map[string]any{"TLSInspectionConfigurationName": "inbound"})
	if described.body["UpdateToken"] != token || described.body["TLSInspectionConfiguration"] == nil {
		t.Fatalf("described %v", described.body)
	}
	expect(t, nf("UpdateTLSInspectionConfiguration", map[string]any{"TLSInspectionConfigurationArn": arn, "UpdateToken": "00000000-0000-4000-8000-000000000000", "TLSInspectionConfiguration": map[string]any{}}), 400, "InvalidTokenException")
	updated := nf("UpdateTLSInspectionConfiguration", map[string]any{"TLSInspectionConfigurationArn": arn, "UpdateToken": token, "TLSInspectionConfiguration": map[string]any{}, "Description": "emptied"})
	expect(t, updated, 200, "")
	if updated.body["UpdateToken"] == token {
		t.Fatal("token not rotated")
	}
	if got := nf("DescribeTLSInspectionConfiguration", map[string]any{"TLSInspectionConfigurationArn": arn}).body["TLSInspectionConfiguration"].(map[string]any); len(got) != 0 {
		t.Fatalf("an update replaces the body: %v", got)
	}
	if n := len(nf("ListTLSInspectionConfigurations", map[string]any{}).body["TLSInspectionConfigurations"].([]any)); n != 1 {
		t.Fatalf("listed %d", n)
	}
	expect(t, nf("DeleteTLSInspectionConfiguration", map[string]any{"TLSInspectionConfigurationName": "inbound"}), 200, "")
	expect(t, nf("CreateFirewall", map[string]any{"FirewallName": "f", "FirewallPolicyArn": arn, "VpcId": "vpc-1", "SubnetMappings": []any{}}), 501, "UnknownOperationException")
}

func TestConcurrencyTokenVocabularyIsChecked(t *testing.T) {
	model, err := loadModel("network-firewall")
	if err != nil {
		t.Fatal(err)
	}
	base := func() *Service {
		spec := *networkFirewall
		spec.Resources = nil
		for _, res := range networkFirewall.Resources {
			copied := *res
			spec.Resources = append(spec.Resources, &copied)
		}
		spec.Ops = append([]Op(nil), networkFirewall.Ops...)
		return &spec
	}
	for name, mutate := range map[string]func(*Service){
		"generator without token": func(s *Service) { s.Resources[0].Token, s.Resources[0].TokenID = "", "uuid" },
		"name generator":          func(s *Service) { s.Resources[0].TokenID = "name" },
		"unknown generator":       func(s *Service) { s.Resources[0].TokenID = "words" },
		"beside in shape":         func(s *Service) { s.Resources[0].Beside = []string{"Description"} },
		"token on read":           func(s *Service) { s.Ops[1].Token = "UpdateToken" },
		"token missing":           func(s *Service) { s.Ops[3].Token = "Version" },
		"resource without token":  func(s *Service) { s.Resources[0].Token = "" },
	} {
		spec := base()
		mutate(spec)
		if _, err := bindModel(spec, model); err == nil {
			t.Fatalf("%s: composition accepted an inconsistent token", name)
		}
	}
	counter := base()
	counter.Resources[0].TokenID = "counter"
	if _, err := bindModel(counter, model); err != nil {
		t.Fatal(err)
	}
}

func TestCounterTokensCompareNumbers(t *testing.T) {
	res := &Resource{Token: "Version", TokenID: "counter"}
	q := &request{op: &Op{Token: "CurrentVersion"}}
	document := map[string]any{}
	if err := q.rotateToken(res, document); err != nil || document["Version"] != int64(1) {
		t.Fatalf("first %v", document)
	}
	document["Version"] = json.Number("1")
	q.input = map[string]any{"CurrentVersion": int64(1)}
	if err := q.checkToken(res, row{Document: document}); err != nil {
		t.Fatal(err)
	}
	q.input = map[string]any{"CurrentVersion": int64(2)}
	if err := q.checkToken(res, row{Document: document}); err == nil {
		t.Fatal("a stale counter was accepted")
	}
	q.input = map[string]any{"CurrentVersion": "1"}
	if err := q.checkToken(res, row{Document: document}); err == nil {
		t.Fatal("a text token matched a number")
	}
	if err := q.rotateToken(res, document); err != nil || document["Version"] != int64(2) {
		t.Fatalf("next %v", document)
	}
	if err := q.rotateToken(&Resource{Token: "T", TokenID: "words"}, document); err == nil {
		t.Fatal("unknown generator")
	}
}

func TestNetworkFirewallRuleGroupReferencesAndInUse(t *testing.T) {
	h := newHarness(t, networkFirewall)
	nf := func(op string, input map[string]any) reply { return h.json1("network-firewall", op, input) }
	stateful := nf("CreateRuleGroup", map[string]any{"RuleGroupName": "block", "Type": "STATEFUL", "Capacity": 100, "RuleGroup": map[string]any{
		"RulesSource": map[string]any{"RulesString": `drop tls $HOME_NET any -> $EXTERNAL_NET any (msg:"x"; sid:1;)`}}})
	expect(t, stateful, 200, "")
	statefulArn := str(stateful.body["RuleGroupResponse"].(map[string]any)["RuleGroupArn"])
	if statefulArn != "arn:aws:network-firewall:us-east-1:000000000000:stateful-rulegroup/block" || stateful.body["RuleGroupResponse"].(map[string]any)["Type"] != "STATEFUL" {
		t.Fatalf("stateful %v", stateful.body)
	}
	stateless := nf("CreateRuleGroup", map[string]any{"RuleGroupName": "pass", "Type": "STATELESS", "Capacity": 10, "RuleGroup": map[string]any{
		"RulesSource": map[string]any{"StatelessRulesAndCustomActions": map[string]any{"StatelessRules": []any{map[string]any{"Priority": 1,
			"RuleDefinition": map[string]any{"Actions": []any{"aws:pass"}, "MatchAttributes": map[string]any{}}}}}}}})
	expect(t, stateless, 200, "")
	statelessArn := str(stateless.body["RuleGroupResponse"].(map[string]any)["RuleGroupArn"])
	if !strings.HasSuffix(statelessArn, ":stateless-rulegroup/pass") {
		t.Fatalf("stateless arn %s", statelessArn)
	}
	// A Type beside the name must be the stored one; filters select by type.
	expect(t, nf("DescribeRuleGroup", map[string]any{"RuleGroupName": "block", "Type": "STATELESS"}), 400, "ResourceNotFoundException")
	described := nf("DescribeRuleGroup", map[string]any{"RuleGroupName": "block", "Type": "STATEFUL", "AnalyzeRuleGroup": false})
	expect(t, described, 200, "")
	if described.body["RuleGroup"].(map[string]any)["RulesSource"] == nil || described.body["UpdateToken"] == nil {
		t.Fatalf("described %v", described.body)
	}
	if items := nf("ListRuleGroups", map[string]any{"Type": "STATELESS", "Scope": "ACCOUNT"}).body["RuleGroups"].([]any); len(items) != 1 || items[0].(map[string]any)["Arn"] != statelessArn {
		t.Fatalf("stateless list %v", items)
	}
	expect(t, nf("ListRuleGroups", map[string]any{"Scope": "MANAGED"}), 400, "InvalidRequestException")
	expect(t, nf("ListRuleGroups", map[string]any{"ManagedType": "AWS_MANAGED_DOMAIN_LISTS"}), 400, "InvalidRequestException")
	expect(t, nf("CreateRuleGroup", map[string]any{"RuleGroupName": "flat", "Type": "STATEFUL", "Capacity": 1, "Rules": "pass ip any any -> any any (sid:2;)"}), 400, "InvalidRequestException")
	expect(t, nf("CreateRuleGroup", map[string]any{"RuleGroupName": "empty", "Type": "STATEFUL", "Capacity": 1}), 400, "InvalidRequestException")
	expect(t, nf("CreateRuleGroup", map[string]any{"RuleGroupName": "dry", "Type": "STATEFUL", "Capacity": 1, "AnalyzeRuleGroup": true, "RuleGroup": map[string]any{"RulesSource": map[string]any{}}}), 400, "InvalidRequestException")
	expect(t, nf("DeleteRuleGroup", map[string]any{"RuleGroupArn": statelessArn, "Type": "STATEFUL"}), 400, "ResourceNotFoundException")

	tls := nf("CreateTLSInspectionConfiguration", map[string]any{"TLSInspectionConfigurationName": "inbound", "TLSInspectionConfiguration": map[string]any{}})
	tlsArn := str(tls.body["TLSInspectionConfigurationResponse"].(map[string]any)["TLSInspectionConfigurationArn"])
	refs := map[string]any{
		"StatelessRuleGroupReferences":  []any{map[string]any{"ResourceArn": statelessArn, "Priority": 1}},
		"StatefulRuleGroupReferences":   []any{map[string]any{"ResourceArn": statefulArn}},
		"TLSInspectionConfigurationArn": tlsArn,
	}
	// Unknown, mistyped and wrong-kind references are refused before storing.
	missing := strings.Replace(statefulArn, "/block", "/gone", 1)
	expect(t, nf("CreateFirewallPolicy", map[string]any{"FirewallPolicyName": "edge", "FirewallPolicy": nfPolicyBody(map[string]any{
		"StatefulRuleGroupReferences": []any{map[string]any{"ResourceArn": missing}}})}), 400, "InvalidRequestException")
	expect(t, nf("CreateFirewallPolicy", map[string]any{"FirewallPolicyName": "edge", "FirewallPolicy": nfPolicyBody(map[string]any{
		"StatefulRuleGroupReferences": []any{map[string]any{"ResourceArn": statelessArn}}})}), 400, "InvalidRequestException")
	expect(t, nf("CreateFirewallPolicy", map[string]any{"FirewallPolicyName": "edge", "FirewallPolicy": nfPolicyBody(map[string]any{"TLSInspectionConfigurationArn": statefulArn})}), 400, "InvalidRequestException")
	if n := len(nf("ListFirewallPolicies", map[string]any{}).body["FirewallPolicies"].([]any)); n != 0 {
		t.Fatalf("refused references stored %d policies", n)
	}
	created := nf("CreateFirewallPolicy", map[string]any{"FirewallPolicyName": "edge", "FirewallPolicy": nfPolicyBody(refs)})
	expect(t, created, 200, "")
	policyArn := str(created.body["FirewallPolicyResponse"].(map[string]any)["FirewallPolicyArn"])
	for _, c := range []struct{ op, member, arn string }{
		{"DeleteRuleGroup", "RuleGroupArn", statelessArn}, {"DeleteRuleGroup", "RuleGroupArn", statefulArn},
		{"DeleteTLSInspectionConfiguration", "TLSInspectionConfigurationArn", tlsArn},
	} {
		expect(t, nf(c.op, map[string]any{c.member: c.arn}), 400, "InvalidOperationException")
	}
	if nf("DescribeRuleGroup", map[string]any{"RuleGroupArn": statefulArn}).status != 200 {
		t.Fatal("a refused delete removed the rule group")
	}
	// Dropping a reference through an update releases that group only.
	token := str(nf("DescribeFirewallPolicy", map[string]any{"FirewallPolicyArn": policyArn}).body["UpdateToken"])
	expect(t, nf("UpdateFirewallPolicy", map[string]any{"FirewallPolicyArn": policyArn, "UpdateToken": token, "FirewallPolicy": nfPolicyBody(map[string]any{
		"StatelessRuleGroupReferences": []any{map[string]any{"ResourceArn": missing, "Priority": 1}}})}), 400, "InvalidRequestException")
	expect(t, nf("UpdateFirewallPolicy", map[string]any{"FirewallPolicyArn": policyArn, "UpdateToken": token, "FirewallPolicy": nfPolicyBody(map[string]any{
		"StatefulRuleGroupReferences": []any{map[string]any{"ResourceArn": statefulArn}}})}), 200, "")
	expect(t, nf("DeleteRuleGroup", map[string]any{"RuleGroupName": "pass", "Type": "STATELESS"}), 200, "")
	expect(t, nf("DeleteTLSInspectionConfiguration", map[string]any{"TLSInspectionConfigurationArn": tlsArn}), 200, "")
	expect(t, nf("DeleteRuleGroup", map[string]any{"RuleGroupArn": statefulArn}), 400, "InvalidOperationException")
	ruleToken := str(nf("DescribeRuleGroup", map[string]any{"RuleGroupArn": statefulArn}).body["UpdateToken"])
	expect(t, nf("UpdateRuleGroup", map[string]any{"RuleGroupArn": statefulArn, "Type": "STATELESS", "UpdateToken": ruleToken, "Description": "x"}), 400, "ResourceNotFoundException")
	expect(t, nf("UpdateRuleGroup", map[string]any{"RuleGroupArn": statefulArn, "UpdateToken": ruleToken, "Description": "kept"}), 200, "")
	expect(t, nf("DeleteFirewallPolicy", map[string]any{"FirewallPolicyName": "edge"}), 200, "")
	expect(t, nf("DeleteRuleGroup", map[string]any{"RuleGroupArn": statefulArn}), 200, "")
}

func TestReferencePathsAndMatchVocabulary(t *testing.T) {
	document := map[string]any{"a": map[string]any{"refs": []any{map[string]any{"arn": "x"}, map[string]any{"arn": "y"}, map[string]any{"other": "z"}, "bad"}},
		"edges": []any{[]any{"p", "q"}, []any{"r"}, "s"}, "plain": "t", "number": json.Number("1")}
	for path, want := range map[string]string{"a.refs[].arn": "x,y", "edges[][]": "p,q,r", "plain": "t", "number": "", "missing.deeper": "", "a.refs": ""} {
		if got := strings.Join(refValues(document, path), ","); got != want {
			t.Fatalf("%s: %q, want %q", path, got, want)
		}
	}
	model, err := loadModel("network-firewall")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Service){
		"unknown kind": func(s *Service) {
			s.Resources[0].Refs = map[string]string{"FirewallPolicy.TLSInspectionConfigurationArn": "nothing"}
		},
		"unstored member":  func(s *Service) { s.Resources[0].Refs = map[string]string{"Missing[].Arn": "ruleGroup"} },
		"match on list":    func(s *Service) { s.Ops[2].Match = []string{"MaxResults"} },
		"match not member": func(s *Service) { s.Ops[1].Match = []string{"Type"} },
	} {
		spec := *networkFirewall
		spec.Resources = nil
		for _, res := range networkFirewall.Resources {
			copied := *res
			spec.Resources = append(spec.Resources, &copied)
		}
		spec.Ops = append([]Op(nil), networkFirewall.Ops...)
		mutate(&spec)
		if _, err := bindModel(&spec, model); err == nil {
			t.Fatalf("%s: composition accepted it", name)
		}
	}
}
