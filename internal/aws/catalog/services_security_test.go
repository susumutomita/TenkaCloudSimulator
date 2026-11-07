package catalog

import "testing"

func caConfig() map[string]any {
	return map[string]any{"KeyAlgorithm": "RSA_2048", "SigningAlgorithm": "SHA256WITHRSA", "Subject": map[string]any{"CommonName": "ca.example"}}
}

// Issuance, signing, deletion protection and unsupported targets are refused
// with each operation's modeled exception before any state is stored.
func TestSecurityServicesRefuseUnsupportedEffectsBeforeStoring(t *testing.T) {
	h := newHarness(t)
	refusals := []struct {
		model, op, code string
		input           map[string]any
	}{
		{"acm", "RequestCertificate", "InvalidParameterException", map[string]any{"DomainName": "a.example.com", "CertificateAuthorityArn": "arn:aws:acm-pca:us-east-1:000000000000:certificate-authority/x"}},
		{"acm", "RequestCertificate", "InvalidParameterException", map[string]any{"DomainName": "a.example.com", "ManagedBy": "CLOUDFRONT"}},
		{"acm", "RequestCertificate", "InvalidParameterException", map[string]any{"DomainName": "a.example.com", "KeyAlgorithm": "EC_prime256v1"}},
		{"acm", "RequestCertificate", "InvalidParameterException", map[string]any{"DomainName": "a.example.com", "ValidationMethod": "DNS"}},
		{"acm-pca", "CreateCertificateAuthority", "InvalidArgsException", map[string]any{"CertificateAuthorityConfiguration": caConfig(), "CertificateAuthorityType": "ROOT", "IdempotencyToken": "t"}},
		{"shield", "CreateProtection", "InvalidResourceException", map[string]any{"Name": "bucket", "ResourceArn": "arn:aws:s3:::bucket"}},
		{"shield", "CreateProtectionGroup", "InvalidParameterException", map[string]any{"ProtectionGroupId": "g", "Aggregation": "SUM", "Pattern": "ARBITRARY"}},
		{"shield", "CreateProtectionGroup", "InvalidParameterException", map[string]any{"ProtectionGroupId": "g", "Aggregation": "SUM", "Pattern": "ALL", "ResourceType": "GLOBAL_ACCELERATOR"}},
		{"verifiedpermissions", "CreatePolicyStore", "ValidationException", map[string]any{"validationSettings": map[string]any{"mode": "OFF"}, "deletionProtection": "ENABLED"}},
	}
	for _, c := range refusals {
		expect(t, h.json1(c.model, c.op, c.input), 400, c.code)
	}
	expect(t, h.rest("signer", "PUT", "/signing-profiles/device_profile", map[string]any{"platformId": "AWSIoTDeviceManagement-SHA256-ECDSA"}), 400, "ValidationException")
	expect(t, h.rest("signer", "PUT", "/signing-profiles/lambda_profile", map[string]any{"platformId": "AWSLambda-SHA384-ECDSA", "signingMaterial": map[string]any{"certificateArn": "arn:aws:acm:us-east-1:000000000000:certificate/x"}}), 400, "ValidationException")
	var rows int
	_ = h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources").Scan(&rows)
	if rows != 0 {
		t.Fatalf("refused requests stored %d resources", rows)
	}
	expect(t, h.json1("shield", "CreateProtectionGroup", map[string]any{"ProtectionGroupId": "all", "Aggregation": "MAX", "Pattern": "ALL"}), 200, "")
	expect(t, h.json1("shield", "CreateProtectionGroup", map[string]any{"ProtectionGroupId": "albs", "Aggregation": "SUM", "Pattern": "BY_RESOURCE_TYPE", "ResourceType": "APPLICATION_LOAD_BALANCER"}), 200, "")
	expect(t, h.json1("shield", "CreateProtection", map[string]any{"Name": "alb", "ResourceArn": "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/50dc6c495c0c9188"}), 200, "")
	if group := h.json1("shield", "DescribeProtectionGroup", map[string]any{"ProtectionGroupId": "all"}).body["ProtectionGroup"].(map[string]any); group["ProtectionGroupArn"] != "arn:aws:shield::000000000000:protection-group/all" || len(group["Members"].([]any)) != 0 {
		t.Fatalf("global protection group %v", group)
	}
}

// Bounded state transitions keep the documented official semantics.
func TestSecurityServicesKeepBoundedStateSemantics(t *testing.T) {
	h := newHarness(t)
	created := h.rest("signer", "PUT", "/signing-profiles/app_profile", map[string]any{"platformId": "AWSLambda-SHA384-ECDSA"})
	expect(t, created, 200, "")
	if created.body["arn"] != "arn:aws:signer:us-east-1:000000000000:/signing-profiles/app_profile" || len(str(created.body["profileVersion"])) != 10 {
		t.Fatalf("signing profile identity %v", created.body)
	}
	expect(t, h.rest("signer", "DELETE", "/signing-profiles/app_profile", nil), 200, "")
	if got := h.rest("signer", "GET", "/signing-profiles/app_profile", nil); got.body["status"] != "Canceled" {
		t.Fatalf("canceled profile must stay readable: %v", got.body)
	}
	expect(t, h.rest("signer", "PUT", "/signing-profiles/app_profile", map[string]any{"platformId": "AWSLambda-SHA384-ECDSA"}), 400, "ValidationException")

	if got := h.json1("acm", "GetAccountConfiguration", map[string]any{}); asText(got.body["ExpiryEvents"].(map[string]any)["DaysBeforeExpiry"]) != "45" {
		t.Fatalf("default expiry events %v", got.body)
	}
	expect(t, h.json1("acm", "PutAccountConfiguration", map[string]any{"IdempotencyToken": "a", "ExpiryEvents": map[string]any{"DaysBeforeExpiry": 10}}), 200, "")
	if got := h.json1("acm", "GetAccountConfiguration", map[string]any{}); asText(got.body["ExpiryEvents"].(map[string]any)["DaysBeforeExpiry"]) != "10" {
		t.Fatalf("stored expiry events %v", got.body)
	}

	ca := h.json1("acm-pca", "CreateCertificateAuthority", map[string]any{"CertificateAuthorityConfiguration": caConfig(), "CertificateAuthorityType": "SUBORDINATE"})
	expect(t, ca, 200, "")
	authority := h.json1("acm-pca", "DescribeCertificateAuthority", map[string]any{"CertificateAuthorityArn": ca.body["CertificateAuthorityArn"]}).body["CertificateAuthority"].(map[string]any)
	if authority["Type"] != "SUBORDINATE" || authority["Status"] != "PENDING_CERTIFICATE" || authority["OwnerAccount"] != "000000000000" {
		t.Fatalf("authority projection %v", authority)
	}
	if _, leaked := authority["ResourceOwner"]; leaked {
		t.Fatal("the stored owner filter is not an official CertificateAuthority member")
	}
	for owner, want := range map[string]int{"SELF": 1, "OTHER_ACCOUNTS": 0} {
		if got := h.json1("acm-pca", "ListCertificateAuthorities", map[string]any{"ResourceOwner": owner}).body["CertificateAuthorities"].([]any); len(got) != want {
			t.Fatalf("%s authorities %v", owner, got)
		}
	}

	expect(t, h.rest("detective", "POST", "/graph", map[string]any{}), 200, "")
	expect(t, h.rest("detective", "POST", "/graph", map[string]any{}), 402, "ServiceQuotaExceededException")

	store := h.json1("verifiedpermissions", "CreatePolicyStore", map[string]any{"validationSettings": map[string]any{"mode": "OFF"}, "deletionProtection": "DISABLED"})
	expect(t, store, 200, "")
	expect(t, h.json1("verifiedpermissions", "UpdatePolicyStore", map[string]any{"policyStoreId": store.body["policyStoreId"], "validationSettings": map[string]any{"mode": "STRICT"}, "deletionProtection": "ENABLED"}), 400, "ValidationException")
	if got := h.json1("verifiedpermissions", "GetPolicyStore", map[string]any{"policyStoreId": store.body["policyStoreId"], "tags": true}); got.body["arn"] != "arn:aws:verifiedpermissions::000000000000:policy-store/"+str(store.body["policyStoreId"]) || got.body["validationSettings"].(map[string]any)["mode"] != "OFF" {
		t.Fatalf("policy store %v", got.body)
	}

	target := h.rest("securityhub", "POST", "/actionTargets", map[string]any{"Name": "Ticket", "Description": "Open a ticket", "Id": "ticket"})
	expect(t, target, 200, "")
	if target.body["ActionTargetArn"] != "arn:aws:securityhub:us-east-1:000000000000:action/custom/ticket" {
		t.Fatalf("action target ARN %v", target.body)
	}
}
