package catalog

// Services that use engine vocabulary added after the first catalog
// services: optimistic concurrency tokens and members stored beside the
// official resource shape.

var networkFirewall = &Service{
	Model: "network-firewall", Title: "AWS Network Firewall", IAMPrefix: "network-firewall", Issues: []int{263},
	Resources: []*Resource{
		{Kind: "firewallPolicy", Shape: "FirewallPolicyResponse", Summary: "FirewallPolicyMetadata", ID: "uuid",
			ARN:       "arn:{partition}:network-firewall:{region}:{account}:firewall-policy/{name}",
			ARNMember: "FirewallPolicyArn", NameMember: "FirewallPolicyName", IDMember: "FirewallPolicyId", Unique: true, Limit: 20,
			Token: "UpdateToken", Beside: []string{"FirewallPolicy"},
			Generate: map[string]any{"FirewallPolicyStatus": "ACTIVE", "NumberOfAssociations": 0, "Name": "$name", "Arn": "$arn"}},
		{Kind: "tlsInspectionConfiguration", Shape: "TLSInspectionConfigurationResponse", Summary: "TLSInspectionConfigurationMetadata", ID: "uuid",
			ARN:       "arn:{partition}:network-firewall:{region}:{account}:tls-configuration/{name}",
			ARNMember: "TLSInspectionConfigurationArn", NameMember: "TLSInspectionConfigurationName", IDMember: "TLSInspectionConfigurationId", Unique: true, Limit: 20,
			Token: "UpdateToken", Beside: []string{"TLSInspectionConfiguration"},
			Generate: map[string]any{"TLSInspectionConfigurationStatus": "ACTIVE", "NumberOfAssociations": 0, "Name": "$name", "Arn": "$arn"}},
	},
	Ops: []Op{
		{Name: "CreateFirewallPolicy", Kind: Create, Resource: "firewallPolicy", Accept: []string{"DryRun"}, Check: networkFirewallPolicy, Touch: []string{"LastModifiedTime"}},
		{Name: "DescribeFirewallPolicy", Kind: Read, Resource: "firewallPolicy", Lookup: "FirewallPolicyName", LookupAlt: []string{"FirewallPolicyArn"}},
		{Name: "ListFirewallPolicies", Kind: List, Resource: "firewallPolicy"},
		{Name: "UpdateFirewallPolicy", Kind: Update, Resource: "firewallPolicy", Lookup: "FirewallPolicyName", LookupAlt: []string{"FirewallPolicyArn"}, Token: "UpdateToken",
			Accept: []string{"DryRun"}, Fields: []string{"FirewallPolicy", "Description", "EncryptionConfiguration"}, Check: networkFirewallPolicy, Touch: []string{"LastModifiedTime"}},
		{Name: "DeleteFirewallPolicy", Kind: Delete, Resource: "firewallPolicy", Lookup: "FirewallPolicyName", LookupAlt: []string{"FirewallPolicyArn"},
			Set: map[string]any{"FirewallPolicyStatus": "DELETING"}},
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
		"Firewall policies and TLS inspection configurations are stored configuration records: no firewall, firewall endpoint, VPC endpoint association, proxy or traffic inspection exists, so no packet is ever filtered, decrypted or logged. Firewalls, VPC endpoint associations, proxies, rule groups, resource policies, logging, analysis reports, flow operations and every other official operation return UnknownOperationException with HTTP 501, and NumberOfAssociations is always 0.",
		"Every create, describe and update returns an UpdateToken, and UpdateFirewallPolicy and UpdateTLSInspectionConfiguration fail with InvalidTokenException and change nothing unless the UpdateToken they carry equals the current one; each successful update issues a new token. An update replaces the whole FirewallPolicy or TLSInspectionConfiguration body it carries. Deleting a resource removes it at once and the response reports the status DELETING.",
		"A firewall policy holds no rule group references and no TLSInspectionConfigurationArn, which are refused because rule groups are not implemented and a reference is not resolved yet, so ConsumedStatelessRuleCapacity and ConsumedStatefulRuleCapacity are never reported. TLS inspection configurations keep their ACM certificate and certificate authority ARNs without resolving them, so no Certificates or CertificateAuthority status is reported.",
		"DryRun true is refused instead of being ignored; DryRun false is accepted. A CUSTOMER_KMS EncryptionConfiguration needs a KeyId, which is stored without resolving the key. Names are unique per Region and kind, at most 20 firewall policies and 20 TLS inspection configurations exist per Region as local limits, and ListTagsForResource returns every tag in one response and refuses MaxResults and NextToken.",
	},
}

// networkFirewallPolicy refuses a dry run, rule group references and a TLS
// inspection reference, which this bounded model cannot resolve.
func networkFirewallPolicy(input map[string]any) error {
	if input["DryRun"] == true {
		return Failure("InvalidRequestException", "DryRun is not supported by the simulator.")
	}
	if policy, ok := input["FirewallPolicy"].(map[string]any); ok {
		for _, member := range []string{"StatelessRuleGroupReferences", "StatefulRuleGroupReferences", "TLSInspectionConfigurationArn"} {
			if _, present := policy[member]; present {
				return Failure("InvalidRequestException", member+" is not supported by the simulator.")
			}
		}
	}
	return networkFirewallEncryption(input)
}

// networkFirewallEncryption requires the key of a customer managed
// encryption configuration.
func networkFirewallEncryption(input map[string]any) error {
	if encryption, ok := input["EncryptionConfiguration"].(map[string]any); ok && encryption["Type"] == "CUSTOMER_KMS" && encryption["KeyId"] == nil {
		return Failure("InvalidRequestException", "A CUSTOMER_KMS encryption configuration needs a KeyId.")
	}
	return nil
}
