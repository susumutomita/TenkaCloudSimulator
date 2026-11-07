package catalog

import (
	"regexp"
	"strings"
)

// Reviewed bounded specifications for security, identity and compliance
// services. Each binds only official operations whose effect is configuration
// metadata: nothing is issued, signed, scanned, detected, evaluated, shared
// with another account or sent to an external system.

var acm = &Service{
	Model: "acm", Title: "AWS Certificate Manager", IAMPrefix: "acm", Issues: []int{115},
	Resources: []*Resource{
		{Kind: "certificate", Shape: "CertificateDetail", Summary: "CertificateSummary", ID: "uuid",
			ARN:       "arn:{partition}:acm:{region}:{account}:certificate/{id}",
			ARNMember: "CertificateArn", NameMember: "DomainName", Limit: 100,
			Generate: map[string]any{"CertificateArn": "$arn", "CreatedAt": "$now", "Status": "PENDING_VALIDATION", "Type": "AMAZON_ISSUED",
				"KeyAlgorithm": "RSA_2048", "RenewalEligibility": "INELIGIBLE", "InUseBy": []any{},
				"Options": map[string]any{"CertificateTransparencyLoggingPreference": "ENABLED", "Export": "DISABLED"}}},
		{Kind: "accountConfiguration", Shape: "GetAccountConfigurationResponse", Singleton: true, ID: "name",
			ARN: "arn:{partition}:acm:{region}:{account}:account-configuration", NameMember: "ExpiryEvents",
			Generate: map[string]any{"ExpiryEvents": map[string]any{"DaysBeforeExpiry": 45}}},
	},
	Ops: []Op{
		{Name: "RequestCertificate", Kind: Create, Resource: "certificate", Check: certificateRequest},
		{Name: "DescribeCertificate", Kind: Read, Resource: "certificate", Lookup: "CertificateArn"},
		{Name: "ListCertificates", Kind: List, Resource: "certificate"},
		{Name: "UpdateCertificateOptions", Kind: Update, Resource: "certificate", Lookup: "CertificateArn", Fields: []string{"Options"}},
		{Name: "DeleteCertificate", Kind: Delete, Resource: "certificate", Lookup: "CertificateArn"},
		{Name: "GetAccountConfiguration", Kind: Read, Resource: "accountConfiguration"},
		{Name: "PutAccountConfiguration", Kind: Put, Resource: "accountConfiguration", Accept: []string{"IdempotencyToken"}},
		{Name: "AddTagsToCertificate", Kind: Tag, Lookup: "CertificateArn"},
		{Name: "ListTagsForCertificate", Kind: ListTags, Lookup: "CertificateArn"},
	},
	Bounds: []string{
		"Requested certificates stay PENDING_VALIDATION with RSA_2048 keys: no key pair, certificate body, domain validation record, validation email or renewal is produced, and no DNS, HTTP or email system is contacted.",
		"ValidationMethod, IdempotencyToken, CertificateAuthorityArn (private issuance), ManagedBy and other key algorithms are refused; ImportCertificate (private keys), ExportCertificate, GetCertificate, RenewCertificate, RevokeCertificate and ResendValidationEmail return UnknownOperationException with HTTP 501.",
		"ListCertificates returns every stored certificate in creation order; its Includes, CertificateStatuses, SortBy and SortOrder members are refused.",
		"RemoveTagsFromCertificate is not implemented because its key/value removal list has no catalog tag binding; AddTagsToCertificate and ListTagsForCertificate are.",
		"PutAccountConfiguration stores ExpiryEvents on every call; its idempotency token is accepted but no earlier result is replayed, and no expiry event is ever emitted.",
	},
}

// certificateRequest refuses request members whose official effect is
// issuance or key generation the simulator does not perform.
func certificateRequest(input map[string]any) error {
	if _, ok := input["CertificateAuthorityArn"]; ok {
		return Failure("InvalidParameterException", "Private certificate issuance through ACM Private CA is not performed by the simulator.")
	}
	if _, ok := input["ManagedBy"]; ok {
		return Failure("InvalidParameterException", "Certificates managed by another service are not supported by the simulator.")
	}
	if algorithm, ok := input["KeyAlgorithm"].(string); ok && algorithm != "RSA_2048" {
		return Failure("InvalidParameterException", "The simulator stores only RSA_2048 certificate requests.")
	}
	return nil
}

var acmPCA = &Service{
	Model: "acm-pca", Title: "AWS Certificate Manager Private Certificate Authority", IAMPrefix: "acm-pca", Issues: []int{118},
	Resources: []*Resource{
		{Kind: "certificateAuthority", Shape: "CertificateAuthority", ID: "uuid",
			ARN:       "arn:{partition}:acm-pca:{region}:{account}:certificate-authority/{id}",
			ARNMember: "Arn", Limit: 50, Aliases: map[string]string{"CertificateAuthorityType": "Type"},
			Generate: map[string]any{"Arn": "$arn", "CertificateAuthorityArn": "$arn", "OwnerAccount": "$account", "ResourceOwner": "SELF",
				"CreatedAt": "$now", "LastStateChangeAt": "$now", "Status": "PENDING_CERTIFICATE",
				"KeyStorageSecurityStandard": "FIPS_140_2_LEVEL_3_OR_HIGHER", "UsageMode": "GENERAL_PURPOSE"}},
	},
	Ops: []Op{
		{Name: "CreateCertificateAuthority", Kind: Create, Resource: "certificateAuthority", Check: noAuthorityToken},
		{Name: "DescribeCertificateAuthority", Kind: Read, Resource: "certificateAuthority", Lookup: "CertificateAuthorityArn"},
		{Name: "ListCertificateAuthorities", Kind: List, Resource: "certificateAuthority", Filters: map[string]string{"ResourceOwner": "ResourceOwner"}},
		{Name: "DeleteCertificateAuthority", Kind: Delete, Resource: "certificateAuthority", Lookup: "CertificateAuthorityArn"},
		{Name: "TagCertificateAuthority", Kind: Tag, Lookup: "CertificateAuthorityArn"},
		{Name: "ListTags", Kind: ListTags, Lookup: "CertificateAuthorityArn"},
	},
	Bounds: []string{
		"Certificate authorities are configuration metadata that stay PENDING_CERTIFICATE: no key pair, CSR, CA certificate, CRL, OCSP response or audit report is generated, and nothing is written to S3.",
		"IssueCertificate, GetCertificate, GetCertificateAuthorityCsr, ImportCertificateAuthorityCertificate, RevokeCertificate, UpdateCertificateAuthority, RestoreCertificateAuthority, permissions and policies return UnknownOperationException with HTTP 501.",
		"DeleteCertificateAuthority removes the authority immediately instead of keeping a restorable DELETED state; PermanentDeletionTimeInDays and IdempotencyToken are refused.",
		"ListCertificateAuthorities returns no authorities for ResourceOwner OTHER_ACCOUNTS. UntagCertificateAuthority is not implemented because its key/value removal list has no catalog tag binding.",
	},
}

// noAuthorityToken refuses the idempotency token, which the official model
// does not mark for replay and the bounded create therefore cannot honor.
func noAuthorityToken(input map[string]any) error {
	if _, ok := input["IdempotencyToken"]; ok {
		return Failure("InvalidArgsException", "IdempotencyToken is not supported by the simulator.")
	}
	return nil
}

var signer = &Service{
	Model: "signer", Title: "AWS Signer", IAMPrefix: "signer", Issues: []int{119},
	AccountMembers: []string{"profileOwner"},
	Resources: []*Resource{
		{Kind: "signingProfile", Shape: "GetSigningProfileResponse", Summary: "SigningProfile", ID: "alnum:10",
			ARN:       "arn:{partition}:signer:{region}:{account}:/signing-profiles/{name}",
			ARNMember: "arn", NameMember: "profileName", Unique: true, Limit: 100,
			Generate: map[string]any{"arn": "$arn", "profileVersion": "$id", "status": "Active",
				"profileVersionArn":       "$fmt:arn:{partition}:signer:{region}:{account}:/signing-profiles/{name}/{id}",
				"signatureValidityPeriod": map[string]any{"value": 135, "type": "MONTHS"}}},
	},
	Ops: []Op{
		{Name: "PutSigningProfile", Kind: Create, Resource: "signingProfile", Check: signingProfile},
		{Name: "GetSigningProfile", Kind: Read, Resource: "signingProfile", Lookup: "profileName"},
		{Name: "ListSigningProfiles", Kind: List, Resource: "signingProfile", Filters: map[string]string{"platformId": "platformId"}},
		{Name: "CancelSigningProfile", Kind: Update, Resource: "signingProfile", Lookup: "profileName", Set: map[string]any{"status": "Canceled"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Signing profiles are configuration metadata for the AWS-managed AWSLambda-SHA384-ECDSA and Notation-OCI-SHA384-ECDSA platforms: no signing key or certificate is used and no code, image or payload is ever signed or verified.",
		"StartSigningJob, SignPayload, signing jobs, revocations, platform listings and profile permissions return UnknownOperationException with HTTP 501; signingMaterial and other platforms are refused.",
		"PutSigningProfile for an existing profile name, including a canceled one, is refused; profile versions are never replaced.",
		"CancelSigningProfile keeps the profile with status Canceled, and ListSigningProfiles lists canceled profiles as well; its includeCanceled and statuses members are refused.",
	},
}

// signerPlatforms are the AWS-managed signing platforms that need no
// customer signing certificate.
var signerPlatforms = map[string]bool{"AWSLambda-SHA384-ECDSA": true, "Notation-OCI-SHA384-ECDSA": true}

// signingProfile keeps profiles on platforms whose signing material AWS
// manages, so no certificate or key reference is stored.
func signingProfile(input map[string]any) error {
	if platform, _ := input["platformId"].(string); !signerPlatforms[platform] {
		return Failure("ValidationException", "The simulator supports only the AWSLambda-SHA384-ECDSA and Notation-OCI-SHA384-ECDSA platforms.")
	}
	if _, ok := input["signingMaterial"]; ok {
		return Failure("ValidationException", "AWS-managed signing platforms do not take signing material.")
	}
	return nil
}

var detective = &Service{
	Model: "detective", Title: "Amazon Detective", IAMPrefix: "detective", Issues: []int{258},
	Resources: []*Resource{
		{Kind: "graph", Shape: "Graph", ID: "hex:32", ARN: "arn:{partition}:detective:{region}:{account}:graph:{id}",
			ARNMember: "Arn", Limit: 1,
			Generate: map[string]any{"GraphArn": "$arn", "CreatedTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateGraph", Kind: Create, Resource: "graph"},
		{Name: "ListGraphs", Kind: List, Resource: "graph"},
		{Name: "DeleteGraph", Kind: Delete, Resource: "graph", Lookup: "GraphArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"A behavior graph is configuration metadata: no data source is ingested and no investigation, indicator or finding is produced. A second graph in the same Region is refused.",
		"Members, invitations, datasource packages, investigations and organization APIs return UnknownOperationException with HTTP 501.",
	},
}

var ram = &Service{
	Model: "ram", Title: "AWS Resource Access Manager", IAMPrefix: "ram", Issues: []int{265},
	Resources: []*Resource{
		{Kind: "resourceShare", Shape: "ResourceShare", ID: "uuid", ARN: "arn:{partition}:ram:{region}:{account}:resource-share/{id}",
			ARNMember: "resourceShareArn", NameMember: "name", Limit: 100,
			Generate: map[string]any{"owningAccountId": "$account", "resourceOwner": "SELF", "status": "ACTIVE", "featureSet": "STANDARD",
				"allowExternalPrincipals": true, "creationTime": "$now", "lastUpdatedTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateResourceShare", Kind: Create, Resource: "resourceShare"},
		{Name: "GetResourceShares", Kind: List, Resource: "resourceShare", Names: "resourceShareArns",
			Filters: map[string]string{"resourceOwner": "resourceOwner", "name": "name", "resourceShareStatus": "status"}},
		{Name: "UpdateResourceShare", Kind: Update, Resource: "resourceShare", Lookup: "resourceShareArn",
			Fields: []string{"name", "allowExternalPrincipals"}, Touch: []string{"lastUpdatedTime"}},
		{Name: "DeleteResourceShare", Kind: Delete, Resource: "resourceShare", Lookup: "resourceShareArn", Set: map[string]any{"returnValue": true}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceShareArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceShareArn"},
	},
	Bounds: []string{
		"Resource shares are configuration metadata owned by the simulator account: resourceArns, principals, sources and permissionArns are refused, so nothing is shared, no invitation is sent and no other account gains access.",
		"Associations, invitations, managed permissions, resource policies and organization sharing return UnknownOperationException with HTTP 501. GetResourceShares returns no shares for resourceOwner OTHER-ACCOUNTS.",
		"DeleteResourceShare removes the share immediately instead of keeping it in the DELETED state; clientToken members are refused.",
	},
}

var securityHub = &Service{
	Model: "securityhub", Title: "AWS Security Hub", IAMPrefix: "securityhub", Issues: []int{268},
	Resources: []*Resource{
		// The create request is the only official shape that carries the
		// caller-chosen Id that forms the action target ARN.
		{Kind: "actionTarget", Shape: "CreateActionTargetRequest", Summary: "ActionTarget", ID: "name",
			ARN:       "arn:{partition}:securityhub:{region}:{account}:action/custom/{name}",
			ARNMember: "ActionTargetArn", NameMember: "Id", Unique: true, Limit: 50},
		{Kind: "insight", Shape: "Insight", ID: "uuid", ARN: "arn:{partition}:securityhub:{region}:{account}:insight/{account}/custom/{id}",
			ARNMember: "InsightArn", NameMember: "Name", Limit: 100},
	},
	Ops: []Op{
		{Name: "CreateActionTarget", Kind: Create, Resource: "actionTarget"},
		{Name: "DescribeActionTargets", Kind: List, Resource: "actionTarget", Names: "ActionTargetArns"},
		{Name: "UpdateActionTarget", Kind: Update, Resource: "actionTarget", Lookup: "ActionTargetArn", Fields: []string{"Name", "Description"}},
		{Name: "DeleteActionTarget", Kind: Delete, Resource: "actionTarget", Lookup: "ActionTargetArn"},
		{Name: "CreateInsight", Kind: Create, Resource: "insight"},
		{Name: "GetInsights", Kind: List, Resource: "insight", Names: "InsightArns"},
		{Name: "UpdateInsight", Kind: Update, Resource: "insight", Lookup: "InsightArn", Fields: []string{"Name", "Filters", "GroupByAttribute"}},
		{Name: "DeleteInsight", Kind: Delete, Resource: "insight", Lookup: "InsightArn"},
	},
	Bounds: []string{
		"Custom action targets and custom insights are configuration metadata that need no prior EnableSecurityHub call: no finding is ingested or sent to EventBridge, and insight results are never computed. Managed insights are not listed.",
		"Hub enablement, findings, standards, controls, automation rules, configuration policies, aggregators, members and tagging return UnknownOperationException with HTTP 501.",
	},
}

var shield = &Service{
	Model: "shield", Title: "AWS Shield", IAMPrefix: "shield", Issues: []int{271}, Global: true,
	Resources: []*Resource{
		// One protection per protected resource: the resource ARN is the
		// unique name, the protection ID the identifier.
		{Kind: "protection", Shape: "Protection", ID: "uuid", ARN: "arn:{partition}:shield::{account}:protection/{id}",
			ARNMember: "ProtectionArn", IDMember: "Id", NameMember: "ResourceArn", Unique: true, Limit: 100,
			Generate: map[string]any{"ProtectionId": "$id"}},
		{Kind: "protectionGroup", Shape: "ProtectionGroup", ID: "name", ARN: "arn:{partition}:shield::{account}:protection-group/{name}",
			ARNMember: "ProtectionGroupArn", NameMember: "ProtectionGroupId", Unique: true, Limit: 100,
			Generate: map[string]any{"Members": []any{}}},
	},
	Ops: []Op{
		{Name: "CreateProtection", Kind: Create, Resource: "protection", Check: protectedResource},
		{Name: "DescribeProtection", Kind: Read, Resource: "protection", Lookup: "ProtectionId"},
		{Name: "ListProtections", Kind: List, Resource: "protection"},
		{Name: "DeleteProtection", Kind: Delete, Resource: "protection", Lookup: "ProtectionId"},
		{Name: "CreateProtectionGroup", Kind: Create, Resource: "protectionGroup", Check: protectionGroupPattern},
		{Name: "DescribeProtectionGroup", Kind: Read, Resource: "protectionGroup", Lookup: "ProtectionGroupId"},
		{Name: "ListProtectionGroups", Kind: List, Resource: "protectionGroup"},
		{Name: "DeleteProtectionGroup", Kind: Delete, Resource: "protectionGroup", Lookup: "ProtectionGroupId"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Protections and protection groups are configuration metadata that behave as if a Shield Advanced subscription exists: no traffic is inspected or mitigated, no attack is detected and no subscription or billing commitment is made.",
		"Protected resource ARNs must name a supported resource type but are not checked against other services; DescribeProtection looks up by ProtectionId only, and list InclusionFilters are refused.",
		"Subscriptions, attacks, DRT access, health checks, emergency contacts, proactive engagement, application-layer automatic response and UpdateProtectionGroup return UnknownOperationException with HTTP 501.",
	},
}

// shieldResource matches the resource types Shield Advanced can protect.
var shieldResource = regexp.MustCompile(`^arn:aws:(cloudfront::\d{12}:distribution/|route53:::hostedzone/|globalaccelerator::\d{12}:accelerator/|ec2:[a-z0-9-]+:\d{12}:eip-allocation/eipalloc-|elasticloadbalancing:[a-z0-9-]+:\d{12}:loadbalancer/)[A-Za-z0-9/_.-]+$`)

// protectedResource refuses ARNs of resource types Shield cannot protect.
func protectedResource(input map[string]any) error {
	if arn, _ := input["ResourceArn"].(string); !shieldResource.MatchString(arn) {
		return Failure("InvalidResourceException", "ResourceArn must name a CloudFront distribution, Route 53 hosted zone, Global Accelerator accelerator, Elastic IP allocation or load balancer.")
	}
	return nil
}

// protectionGroupPattern applies the documented rule that Members belong to
// ARBITRARY groups and ResourceType to BY_RESOURCE_TYPE groups only.
func protectionGroupPattern(input map[string]any) error {
	pattern, _ := input["Pattern"].(string)
	_, members := input["Members"]
	_, resourceType := input["ResourceType"]
	if members != (pattern == "ARBITRARY") || resourceType != (pattern == "BY_RESOURCE_TYPE") {
		return Failure("InvalidParameterException", "Set Members only for the ARBITRARY pattern and ResourceType only for the BY_RESOURCE_TYPE pattern.")
	}
	return nil
}

var verifiedPermissions = &Service{
	Model: "verifiedpermissions", Title: "Amazon Verified Permissions", IAMPrefix: "verifiedpermissions", Issues: []int{272},
	Resources: []*Resource{
		{Kind: "policyStore", Shape: "GetPolicyStoreOutput", Summary: "PolicyStoreItem", ID: "alnum:22",
			ARN:       "arn:{partition}:verifiedpermissions::{account}:policy-store/{id}",
			ARNMember: "arn", IDMember: "policyStoreId", Limit: 50,
			Generate: map[string]any{"createdDate": "$now", "lastUpdatedDate": "$now", "deletionProtection": "DISABLED", "cedarVersion": "CEDAR_4"}},
	},
	Ops: []Op{
		{Name: "CreatePolicyStore", Kind: Create, Resource: "policyStore", Check: noDeletionProtection},
		{Name: "GetPolicyStore", Kind: Read, Resource: "policyStore", Lookup: "policyStoreId", Accept: []string{"tags"}},
		{Name: "ListPolicyStores", Kind: List, Resource: "policyStore"},
		{Name: "UpdatePolicyStore", Kind: Update, Resource: "policyStore", Lookup: "policyStoreId", Check: noDeletionProtection,
			Fields: []string{"validationSettings", "deletionProtection", "description"}, Touch: []string{"lastUpdatedDate"}},
		{Name: "DeletePolicyStore", Kind: Delete, Resource: "policyStore", Lookup: "policyStoreId"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Policy stores are configuration metadata: no Cedar policy, template, schema or identity source is stored and no authorization request is evaluated.",
		"IsAuthorized, IsAuthorizedWithToken, their batch forms, policies, policy templates, schemas and identity sources return UnknownOperationException with HTTP 501.",
		"deletionProtection ENABLED is refused; GetPolicyStore always includes tags, and deleting a missing policy store returns ResourceNotFoundException instead of succeeding.",
	},
}

// noDeletionProtection refuses deletion protection, which would require the
// bounded delete to consult stored state it does not inspect.
func noDeletionProtection(input map[string]any) error {
	if protection, _ := input["deletionProtection"].(string); strings.EqualFold(protection, "ENABLED") {
		return Failure("ValidationException", "Deletion protection is not supported by the simulator.")
	}
	return nil
}
