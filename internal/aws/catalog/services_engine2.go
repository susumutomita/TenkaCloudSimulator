package catalog

import "strings"

// Services that use engine vocabulary added after the first catalog
// services: optimistic concurrency tokens, members stored beside the
// official resource shape, reference paths with in-use deletion checks, ARN
// member placeholders and matched lookup members.

var networkFirewall = &Service{
	Model: "network-firewall", Title: "AWS Network Firewall", IAMPrefix: "network-firewall", Issues: []int{263},
	Resources: []*Resource{
		{Kind: "firewallPolicy", Shape: "FirewallPolicyResponse", Summary: "FirewallPolicyMetadata", ID: "uuid",
			ARN:       "arn:{partition}:network-firewall:{region}:{account}:firewall-policy/{name}",
			ARNMember: "FirewallPolicyArn", NameMember: "FirewallPolicyName", IDMember: "FirewallPolicyId", Unique: true, Limit: 20,
			Token: "UpdateToken", Beside: []string{"FirewallPolicy"},
			Refs: map[string]string{"FirewallPolicy.StatelessRuleGroupReferences[].ResourceArn": "ruleGroup",
				"FirewallPolicy.StatefulRuleGroupReferences[].ResourceArn": "ruleGroup",
				"FirewallPolicy.TLSInspectionConfigurationArn":             "tlsInspectionConfiguration"},
			Generate: map[string]any{"FirewallPolicyStatus": "ACTIVE", "NumberOfAssociations": 0, "Name": "$name", "Arn": "$arn"}},
		{Kind: "ruleGroup", Shape: "RuleGroupResponse", Summary: "RuleGroupMetadata", ID: "uuid",
			ARN:       "arn:{partition}:network-firewall:{region}:{account}:{lower:Type}-rulegroup/{name}",
			ARNMember: "RuleGroupArn", NameMember: "RuleGroupName", IDMember: "RuleGroupId", Unique: true, Limit: 50,
			Token: "UpdateToken", Beside: []string{"RuleGroup"}, InUse: "InvalidOperationException",
			Generate: map[string]any{"RuleGroupStatus": "ACTIVE", "Name": "$name", "Arn": "$arn"}},
		{Kind: "tlsInspectionConfiguration", Shape: "TLSInspectionConfigurationResponse", Summary: "TLSInspectionConfigurationMetadata", ID: "uuid",
			ARN:       "arn:{partition}:network-firewall:{region}:{account}:tls-configuration/{name}",
			ARNMember: "TLSInspectionConfigurationArn", NameMember: "TLSInspectionConfigurationName", IDMember: "TLSInspectionConfigurationId", Unique: true, Limit: 20,
			Token: "UpdateToken", Beside: []string{"TLSInspectionConfiguration"}, InUse: "InvalidOperationException",
			Generate: map[string]any{"TLSInspectionConfigurationStatus": "ACTIVE", "NumberOfAssociations": 0, "Name": "$name", "Arn": "$arn"}},
	},
	Ops: []Op{
		{Name: "CreateFirewallPolicy", Kind: Create, Resource: "firewallPolicy", Accept: []string{"DryRun"}, Check: networkFirewallPolicy, Touch: []string{"LastModifiedTime"}},
		{Name: "DescribeFirewallPolicy", Kind: Read, Resource: "firewallPolicy", Lookup: "FirewallPolicyName", LookupAlt: []string{"FirewallPolicyArn"}},
		{Name: "ListFirewallPolicies", Kind: List, Resource: "firewallPolicy"},
		{Name: "UpdateFirewallPolicy", Kind: Update, Resource: "firewallPolicy", Lookup: "FirewallPolicyName", LookupAlt: []string{"FirewallPolicyArn"}, Token: "UpdateToken",
			Accept: []string{"DryRun"}, Fields: []string{"FirewallPolicy", "Description", "EncryptionConfiguration"}, Check: networkFirewallPolicy, CheckReplacement: networkFirewallPolicyReplacement, Touch: []string{"LastModifiedTime"}},
		{Name: "DeleteFirewallPolicy", Kind: Delete, Resource: "firewallPolicy", Lookup: "FirewallPolicyName", LookupAlt: []string{"FirewallPolicyArn"},
			Set: map[string]any{"FirewallPolicyStatus": "DELETING"}},
		{Name: "CreateRuleGroup", Kind: Create, Resource: "ruleGroup", Accept: []string{"DryRun", "AnalyzeRuleGroup"}, Check: networkFirewallRuleGroup, Touch: []string{"LastModifiedTime"}},
		{Name: "DescribeRuleGroup", Kind: Read, Resource: "ruleGroup", Lookup: "RuleGroupName", LookupAlt: []string{"RuleGroupArn"}, Match: []string{"Type"},
			Accept: []string{"AnalyzeRuleGroup"}, Check: networkFirewallRuleGroup},
		{Name: "ListRuleGroups", Kind: List, Resource: "ruleGroup", Filters: map[string]string{"Type": "Type"}, Accept: []string{"Scope"}, Check: networkFirewallScope},
		{Name: "UpdateRuleGroup", Kind: Update, Resource: "ruleGroup", Lookup: "RuleGroupName", LookupAlt: []string{"RuleGroupArn"}, Match: []string{"Type"}, Token: "UpdateToken",
			Accept: []string{"DryRun", "AnalyzeRuleGroup"}, Fields: []string{"RuleGroup", "Description", "EncryptionConfiguration", "SourceMetadata", "SummaryConfiguration"},
			Check: networkFirewallRuleGroup, Touch: []string{"LastModifiedTime"}},
		{Name: "DeleteRuleGroup", Kind: Delete, Resource: "ruleGroup", Lookup: "RuleGroupName", LookupAlt: []string{"RuleGroupArn"}, Match: []string{"Type"},
			Set: map[string]any{"RuleGroupStatus": "DELETING"}},
		{Name: "CreateTLSInspectionConfiguration", Kind: Create, Resource: "tlsInspectionConfiguration", Check: networkFirewallEncryption, Touch: []string{"LastModifiedTime"}},
		{Name: "DescribeTLSInspectionConfiguration", Kind: Read, Resource: "tlsInspectionConfiguration", Lookup: "TLSInspectionConfigurationName", LookupAlt: []string{"TLSInspectionConfigurationArn"}},
		{Name: "ListTLSInspectionConfigurations", Kind: List, Resource: "tlsInspectionConfiguration"},
		{Name: "UpdateTLSInspectionConfiguration", Kind: Update, Resource: "tlsInspectionConfiguration", Lookup: "TLSInspectionConfigurationName", LookupAlt: []string{"TLSInspectionConfigurationArn"},
			Token: "UpdateToken", Fields: []string{"TLSInspectionConfiguration", "Description", "EncryptionConfiguration"}, Check: networkFirewallEncryption, Touch: []string{"LastModifiedTime"}},
		{Name: "DeleteTLSInspectionConfiguration", Kind: Delete, Resource: "tlsInspectionConfiguration", Lookup: "TLSInspectionConfigurationName", LookupAlt: []string{"TLSInspectionConfigurationArn"},
			Set: map[string]any{"TLSInspectionConfigurationStatus": "DELETING"}},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Firewall policies, rule groups and TLS inspection configurations are stored configuration records: no firewall, firewall endpoint, VPC endpoint association, proxy or traffic inspection exists, so no packet is ever filtered, decrypted or logged and no rule is evaluated. Firewalls, VPC endpoint associations, proxies, resource policies, logging, analysis reports, flow operations, AWS managed rule groups and every other official operation return UnknownOperationException with HTTP 501, and a policy's NumberOfAssociations is always 0.",
		"Every create, describe and update returns an UpdateToken, and the update operations fail with InvalidTokenException and change nothing unless the UpdateToken they carry equals the current one; each successful update issues a new token. An update replaces the whole FirewallPolicy, RuleGroup or TLSInspectionConfiguration body it carries. A firewall policy update can replace an existing TLS inspection configuration, but cannot add one or remove it. Deleting a resource removes it at once and the response reports the status DELETING.",
		"A firewall policy may reference only rule groups and a TLS inspection configuration of this account that exist, stateless references must name STATELESS groups and stateful references STATEFUL groups, and deleting a rule group or TLS inspection configuration that a policy references fails with InvalidOperationException. Rule group capacity is stored as given and never consumed, so ConsumedCapacity, NumberOfAssociations of rule groups and the consumed capacities of policies are never reported. TLS inspection configurations keep their ACM certificate and certificate authority ARNs without resolving them, so no Certificates or CertificateAuthority status is reported.",
		"A rule group takes its rules in RuleGroup; the Rules string is refused, so Suricata rules are given as RuleGroup.RulesSource.RulesString and are stored without being parsed. The rule group ARN carries the lowercased Type (stateful-rulegroup or stateless-rulegroup), names are unique per Region across both types, and a Type sent beside a name or ARN must be the stored one or the group is not found. ListRuleGroups lists the account's own groups: Scope MANAGED and the managed type and subscription filters are refused.",
		"DryRun true and AnalyzeRuleGroup true are refused instead of being ignored; false is accepted. A CUSTOMER_KMS EncryptionConfiguration needs a KeyId, which is stored without resolving the key. At most 20 firewall policies, 50 rule groups and 20 TLS inspection configurations exist per Region as local limits, and ListTagsForResource returns every tag in one response and refuses MaxResults and NextToken.",
	},
}

// networkFirewallPolicyReplacement preserves whether TLS inspection was
// configured at creation. A valid replacement ARN is validated as a reference
// by the ordinary policy update path.
func networkFirewallPolicyReplacement(previous, next map[string]any) error {
	hasTLSInspection := func(document map[string]any) bool {
		policy, _ := document["FirewallPolicy"].(map[string]any)
		_, present := policy["TLSInspectionConfigurationArn"]
		return present
	}
	if hasTLSInspection(previous) != hasTLSInspection(next) {
		return Failure("InvalidRequestException", "TLS inspection configuration cannot be added to or removed from an existing firewall policy.")
	}
	return nil
}

// networkFirewallPolicy refuses a dry run and references whose ARN names
// the other rule group type.
func networkFirewallPolicy(input map[string]any) error {
	if input["DryRun"] == true {
		return Failure("InvalidRequestException", "DryRun is not supported by the simulator.")
	}
	if policy, ok := input["FirewallPolicy"].(map[string]any); ok {
		for member, form := range map[string]string{"StatelessRuleGroupReferences": ":stateless-rulegroup/", "StatefulRuleGroupReferences": ":stateful-rulegroup/"} {
			for _, arn := range refValues(policy, member+"[].ResourceArn") {
				if !strings.Contains(arn, form) {
					return Failure("InvalidRequestException", member+" must reference rule groups of the matching type.")
				}
			}
		}
	}
	return networkFirewallEncryption(input)
}

// networkFirewallRuleGroup refuses a dry run, rule analysis and the Rules
// string, and requires the RuleGroup body on create.
func networkFirewallRuleGroup(input map[string]any) error {
	if input["DryRun"] == true || input["AnalyzeRuleGroup"] == true {
		return Failure("InvalidRequestException", "DryRun and AnalyzeRuleGroup are not supported by the simulator.")
	}
	if _, ok := input["Rules"]; ok {
		return Failure("InvalidRequestException", "Rules is not supported by the simulator; give the rules in RuleGroup.RulesSource.RulesString.")
	}
	if _, typed := input["Capacity"]; typed && input["RuleGroup"] == nil {
		return Failure("InvalidRequestException", "A rule group needs a RuleGroup.")
	}
	return networkFirewallEncryption(input)
}

// networkFirewallScope lists only the account's own rule groups.
func networkFirewallScope(input map[string]any) error {
	if input["Scope"] == "MANAGED" {
		return Failure("InvalidRequestException", "AWS managed rule groups are not supported by the simulator.")
	}
	return nil
}

// networkFirewallEncryption requires the key of a customer managed
// encryption configuration.
func networkFirewallEncryption(input map[string]any) error {
	if encryption, ok := input["EncryptionConfiguration"].(map[string]any); ok && encryption["Type"] == "CUSTOMER_KMS" && encryption["KeyId"] == nil {
		return Failure("InvalidRequestException", "A CUSTOMER_KMS encryption configuration needs a KeyId.")
	}
	return nil
}
