package catalog

import (
	"strings"
	"testing"
)

func businessHarness(t *testing.T, services ...*Service) *harness {
	t.Helper()
	return newHarness(t, services...)
}

func items(t *testing.T, r reply, member string) []any {
	t.Helper()
	list, _ := r.body[member].([]any)
	return list
}

func TestB2BILifecycleOfEveryResource(t *testing.T) {
	h := businessHarness(t, b2bi)
	call := func(op string, input map[string]any) reply { return h.json1("b2bi", op, input) }

	profile := call("CreateProfile", map[string]any{"name": "acme", "phone": "+1 555 0100", "businessName": "Acme", "logging": "ENABLED", "email": "ops@example.com", "tags": []any{map[string]any{"Key": "team", "Value": "a"}}})
	expect(t, profile, 200, "")
	profileID := str(profile.body["profileId"])
	if !strings.HasPrefix(profileID, "p-") || profile.body["profileArn"] != "arn:aws:b2bi:us-east-1:000000000000:profiles/"+profileID || profile.body["logGroupName"] != nil {
		t.Fatalf("profile %v", profile.body)
	}
	got := call("GetProfile", map[string]any{"profileId": profileID})
	if got.body["modifiedAt"] != nil || got.body["logging"] != "ENABLED" {
		t.Fatalf("get %v", got.body)
	}
	updated := call("UpdateProfile", map[string]any{"profileId": profileID, "name": "acme2", "email": "new@example.com"})
	if updated.body["name"] != "acme2" || updated.body["modifiedAt"] == nil || updated.body["businessName"] != "Acme" {
		t.Fatalf("update %v", updated.body)
	}
	expect(t, call("UpdateProfile", map[string]any{"profileId": profileID, "logging": "DISABLED"}), 400, "ValidationException")
	expect(t, call("CreateProfile", map[string]any{"name": "x", "phone": "+1 555 0100", "businessName": "X", "logging": "MAYBE"}), 400, "ValidationException")
	expect(t, call("CreateProfile", map[string]any{"name": "x", "phone": "abc", "businessName": "X", "logging": "ENABLED"}), 400, "ValidationException")
	if n := len(items(t, call("ListProfiles", map[string]any{}), "profiles")); n != 1 {
		t.Fatalf("profiles %d", n)
	}
	for i := 0; i < 4; i++ {
		expect(t, call("CreateProfile", map[string]any{"name": "p", "phone": "+1 555 0100", "businessName": "P", "logging": "DISABLED"}), 200, "")
	}
	expect(t, call("CreateProfile", map[string]any{"name": "sixth", "phone": "+1 555 0100", "businessName": "P", "logging": "DISABLED"}), 400, "ServiceQuotaExceededException")

	capability := call("CreateCapability", map[string]any{"name": "inbound", "type": "edi", "configuration": map[string]any{"edi": map[string]any{
		"capabilityDirection": "INBOUND", "type": map[string]any{"x12Details": map[string]any{"transactionSet": "X12_214", "version": "VERSION_4010"}},
		"inputLocation": map[string]any{"bucketName": "inbox", "key": "in/"}, "outputLocation": map[string]any{"bucketName": "outbox", "key": "out/"}, "transformerId": "tr-1"}},
		"instructionsDocuments": []any{map[string]any{"bucketName": "docs", "key": "sample.edi"}}})
	expect(t, capability, 200, "")
	capabilityID := str(capability.body["capabilityId"])
	updated = call("UpdateCapability", map[string]any{"capabilityId": capabilityID, "name": "inbound2", "instructionsDocuments": []any{}})
	if updated.body["name"] != "inbound2" || updated.body["modifiedAt"] == nil || len(updated.body["instructionsDocuments"].([]any)) != 0 || updated.body["type"] != "edi" {
		t.Fatalf("capability update %v", updated.body)
	}
	expect(t, call("UpdateCapability", map[string]any{"capabilityId": capabilityID, "type": "edi"}), 400, "ValidationException")
	expect(t, call("CreateCapability", map[string]any{"name": "bad", "type": "edifact", "configuration": map[string]any{}}), 400, "ValidationException")
	if n := len(items(t, call("ListCapabilities", map[string]any{}), "capabilities")); n != 1 {
		t.Fatalf("capabilities %d", n)
	}

	transformer := call("CreateTransformer", map[string]any{"name": "x12_to_json", "fileFormat": "JSON", "mapping": map[string]any{"templateLanguage": "JSONATA", "template": "{}"},
		"inputConversion": map[string]any{"fromFormat": "X12"}, "clientToken": "token-1"})
	expect(t, transformer, 200, "")
	transformerID := str(transformer.body["transformerId"])
	if transformer.body["status"] != "inactive" || transformer.body["transformerArn"] != "arn:aws:b2bi:us-east-1:000000000000:transformers/"+transformerID {
		t.Fatalf("transformer %v", transformer.body)
	}
	replay := call("CreateTransformer", map[string]any{"name": "x12_to_json", "fileFormat": "JSON", "mapping": map[string]any{"templateLanguage": "JSONATA", "template": "{}"},
		"inputConversion": map[string]any{"fromFormat": "X12"}, "clientToken": "token-1"})
	if replay.body["transformerId"] != transformerID {
		t.Fatalf("replay %v", replay.body)
	}
	expect(t, call("CreateTransformer", map[string]any{"name": "other", "clientToken": "token-1"}), 400, "")
	expect(t, call("CreateTransformer", map[string]any{"name": "has space"}), 400, "ValidationException")
	updated = call("UpdateTransformer", map[string]any{"transformerId": transformerID, "status": "active", "name": "renamed"})
	if updated.body["status"] != "active" || updated.body["modifiedAt"] == nil || updated.body["fileFormat"] != "JSON" {
		t.Fatalf("transformer update %v", updated.body)
	}
	expect(t, call("UpdateTransformer", map[string]any{"transformerId": transformerID, "status": "running"}), 400, "ValidationException")
	if n := len(items(t, call("ListTransformers", map[string]any{}), "transformers")); n != 1 {
		t.Fatalf("transformers %d", n)
	}

	partnership := call("CreatePartnership", map[string]any{"profileId": profileID, "name": "p1", "email": "partner@example.com", "capabilities": []any{capabilityID}})
	expect(t, partnership, 200, "")
	partnershipID := str(partnership.body["partnershipId"])
	other := call("CreatePartnership", map[string]any{"profileId": "p-other", "name": "p2", "email": "partner@example.com", "capabilities": []any{}})
	expect(t, other, 200, "")
	if n := len(items(t, call("ListPartnerships", map[string]any{"profileId": profileID}), "partnerships")); n != 1 {
		t.Fatalf("filtered partnerships %d", n)
	}
	if n := len(items(t, call("ListPartnerships", map[string]any{}), "partnerships")); n != 2 {
		t.Fatalf("partnerships %d", n)
	}
	updated = call("UpdatePartnership", map[string]any{"partnershipId": partnershipID, "name": "p1b", "capabilityOptions": map[string]any{"inboundEdi": map[string]any{}}})
	if updated.body["name"] != "p1b" || updated.body["modifiedAt"] == nil || updated.body["profileId"] != profileID {
		t.Fatalf("partnership update %v", updated.body)
	}
	expect(t, call("UpdatePartnership", map[string]any{"partnershipId": partnershipID, "profileId": "p-x"}), 400, "ValidationException")
	expect(t, call("GetPartnership", map[string]any{"partnershipId": "ps-missing"}), 400, "ResourceNotFoundException")

	arn := str(partnership.body["partnershipArn"])
	expect(t, call("TagResource", map[string]any{"ResourceARN": arn, "Tags": []any{map[string]any{"Key": "k", "Value": "v"}}}), 200, "")
	tags := items(t, call("ListTagsForResource", map[string]any{"ResourceARN": arn}), "Tags")
	if len(tags) != 1 {
		t.Fatalf("tags %v", tags)
	}
	expect(t, call("UntagResource", map[string]any{"ResourceARN": arn, "TagKeys": []any{"k"}}), 200, "")
	if tags := items(t, call("ListTagsForResource", map[string]any{"ResourceARN": str(profile.body["profileArn"])}), "Tags"); len(tags) != 1 {
		t.Fatalf("profile tags %v", tags)
	}
	expect(t, call("TagResource", map[string]any{"ResourceARN": "arn:aws:b2bi:us-east-1:000000000000:partnerships/ps-nope", "Tags": []any{}}), 400, "ResourceNotFoundException")

	for _, op := range []string{"StartTransformerJob", "GetTransformerJob", "TestMapping", "TestParsing", "TestConversion", "GenerateMapping", "CreateStarterMappingTemplate"} {
		expect(t, call(op, map[string]any{}), 501, "UnknownOperationException")
	}
	expect(t, call("DeletePartnership", map[string]any{"partnershipId": partnershipID}), 200, "")
	expect(t, call("DeletePartnership", map[string]any{"partnershipId": partnershipID}), 400, "ResourceNotFoundException")
	expect(t, call("DeleteTransformer", map[string]any{"transformerId": transformerID}), 200, "")
	expect(t, call("DeleteCapability", map[string]any{"capabilityId": capabilityID}), 200, "")
	expect(t, call("DeleteProfile", map[string]any{"profileId": profileID}), 200, "")
	expect(t, call("GetProfile", map[string]any{"profileId": profileID}), 400, "ResourceNotFoundException")
}

func escapeARN(arn string) string { return strings.NewReplacer(":", "%3A", "/", "%2F").Replace(arn) }

func TestQBusinessCheckHooksApplyDocumentedRules(t *testing.T) {
	cases := []struct {
		name  string
		hook  func(map[string]any) error
		input map[string]any
		ok    bool
	}{
		{"anonymous", qBusinessApplication, map[string]any{"identityType": "ANONYMOUS"}, true},
		{"default identity", qBusinessApplication, map[string]any{}, false},
		{"identity center", qBusinessApplication, map[string]any{"identityType": "AWS_IAM_IDC"}, false},
		{"identity center instance", qBusinessApplication, map[string]any{"identityType": "ANONYMOUS", "identityCenterInstanceArn": "arn"}, false},
		{"provider", qBusinessApplication, map[string]any{"identityType": "ANONYMOUS", "iamIdentityProviderArn": "arn"}, false},
		{"oidc clients", qBusinessApplication, map[string]any{"identityType": "ANONYMOUS", "clientIdsForOIDC": []any{"c"}}, false},
		{"quicksight", qBusinessApplication, map[string]any{"identityType": "ANONYMOUS", "quickSightConfiguration": map[string]any{}}, false},
		{"native", qBusinessRetriever, map[string]any{"type": "NATIVE_INDEX", "configuration": map[string]any{"nativeIndexConfiguration": map[string]any{}}}, true},
		{"kendra", qBusinessRetriever, map[string]any{"type": "KENDRA_INDEX", "configuration": map[string]any{"kendraIndexConfiguration": map[string]any{}}}, true},
		{"mismatch", qBusinessRetriever, map[string]any{"type": "KENDRA_INDEX", "configuration": map[string]any{"nativeIndexConfiguration": map[string]any{}}}, false},
		{"unknown type", qBusinessRetriever, map[string]any{"type": "OTHER", "configuration": map[string]any{}}, false},
	}
	for _, c := range cases {
		if err := c.hook(c.input); (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if bizText(1) != "" || bizText("a") != "a" {
		t.Fatal("bizText")
	}
}

func TestQBusinessLifecycleOfEveryResource(t *testing.T) {
	h := businessHarness(t, qBusiness)
	call := func(method, path string, input any) reply { return h.rest("qbusiness", method, path, input) }

	expect(t, call("POST", "/applications", map[string]any{"displayName": "app", "identityType": "AWS_IAM_IDC"}), 400, "ValidationException")
	expect(t, call("POST", "/applications", map[string]any{"displayName": "app", "identityType": "ANONYMOUS", "identityCenterInstanceArn": "arn:aws:sso:::instance/ssoins-0123456789abcdef"}), 400, "ValidationException")
	created := call("POST", "/applications", map[string]any{"displayName": "app", "identityType": "ANONYMOUS", "description": "d", "clientToken": "tok-1", "tags": []any{map[string]any{"key": "team", "value": "a"}}})
	expect(t, created, 200, "")
	appID := str(created.body["applicationId"])
	appARN := "arn:aws:qbusiness:us-east-1:000000000000:application/" + appID
	if created.body["applicationArn"] != appARN || len(created.body) != 2 {
		t.Fatalf("create %v", created.body)
	}
	if replay := call("POST", "/applications", map[string]any{"displayName": "app", "identityType": "ANONYMOUS", "description": "d", "clientToken": "tok-1", "tags": []any{map[string]any{"key": "team", "value": "a"}}}); replay.body["applicationId"] != appID {
		t.Fatalf("replay %v", replay.body)
	}
	got := call("GET", "/applications/"+appID, nil)
	if got.body["status"] != "CREATING" || got.body["identityType"] != "ANONYMOUS" || got.body["description"] != "d" {
		t.Fatalf("get %v", got.body)
	}
	expect(t, call("PUT", "/applications/"+appID, map[string]any{"displayName": "app2", "autoSubscriptionConfiguration": map[string]any{"autoSubscribe": "DISABLED"}}), 200, "")
	expect(t, call("PUT", "/applications/"+appID, map[string]any{"identityCenterInstanceArn": "arn:aws:sso:::instance/ssoins-0123456789abcdef"}), 400, "ValidationException")
	if apps := items(t, call("GET", "/applications", nil), "applications"); len(apps) != 1 || apps[0].(map[string]any)["displayName"] != "app2" {
		t.Fatalf("applications %v", apps)
	}
	expect(t, call("GET", "/applications/"+strings.Repeat("a", 36), nil), 404, "ResourceNotFoundException")

	index := call("POST", "/applications/"+appID+"/indices", map[string]any{"displayName": "idx", "capacityConfiguration": map[string]any{"units": 1}})
	expect(t, index, 200, "")
	indexID := str(index.body["indexId"])
	gotIndex := call("GET", "/applications/"+appID+"/indices/"+indexID, nil)
	if gotIndex.body["type"] != "ENTERPRISE" || gotIndex.body["status"] != "CREATING" || gotIndex.body["indexArn"] != appARN+"/index/"+indexID || gotIndex.body["indexStatistics"] != nil {
		t.Fatalf("index %v", gotIndex.body)
	}
	expect(t, call("PUT", "/applications/"+appID+"/indices/"+indexID, map[string]any{"displayName": "idx2", "description": "x"}), 200, "")
	expect(t, call("PUT", "/applications/"+appID+"/indices/"+indexID, map[string]any{"type": "STARTER"}), 400, "ValidationException")
	if list := items(t, call("GET", "/applications/"+appID+"/indices", nil), "indices"); len(list) != 1 {
		t.Fatalf("indices %v", list)
	}
	expect(t, call("POST", "/applications/"+strings.Repeat("b", 36)+"/indices", map[string]any{"displayName": "idx"}), 404, "ResourceNotFoundException")

	expect(t, call("POST", "/applications/"+appID+"/retrievers", map[string]any{"type": "KENDRA_INDEX", "displayName": "r", "configuration": map[string]any{"nativeIndexConfiguration": map[string]any{"indexId": indexID}}}), 400, "ValidationException")
	retriever := call("POST", "/applications/"+appID+"/retrievers", map[string]any{"type": "NATIVE_INDEX", "displayName": "r", "configuration": map[string]any{"nativeIndexConfiguration": map[string]any{"indexId": indexID}}})
	expect(t, retriever, 200, "")
	retrieverID := str(retriever.body["retrieverId"])
	if got := call("GET", "/applications/"+appID+"/retrievers/"+retrieverID, nil); got.body["status"] != "ACTIVE" || got.body["retrieverArn"] != appARN+"/retriever/"+retrieverID {
		t.Fatalf("retriever %v", got.body)
	}
	expect(t, call("PUT", "/applications/"+appID+"/retrievers/"+retrieverID, map[string]any{"displayName": "r2"}), 200, "")
	if list := items(t, call("GET", "/applications/"+appID+"/retrievers", nil), "retrievers"); len(list) != 1 {
		t.Fatalf("retrievers %v", list)
	}

	expect(t, call("POST", "/v1/tags/"+escapeARN(appARN), map[string]any{"tags": []any{map[string]any{"key": "k", "value": "v"}}}), 200, "")
	if tags := items(t, call("GET", "/v1/tags/"+escapeARN(appARN), nil), "tags"); len(tags) != 2 {
		t.Fatalf("tags %v", tags)
	}
	expect(t, call("DELETE", "/v1/tags/"+escapeARN(appARN)+"?tagKeys=k", nil), 200, "")
	expect(t, call("GET", "/v1/tags/"+escapeARN(appARN+"x"), nil), 404, "ResourceNotFoundException")

	for _, path := range []string{"/applications/" + appID + "/chatresponseconfigurations", "/applications/" + appID + "/chatcontrols", "/applications/" + appID + "/dataaccessors", "/applications/" + appID + "/plugins", "/applications/" + appID + "/indices/" + indexID + "/datasources"} {
		expect(t, call("GET", path, nil), 501, "UnknownOperationException")
	}
	expect(t, call("DELETE", "/applications/"+appID+"/retrievers/"+retrieverID, nil), 200, "")
	expect(t, call("DELETE", "/applications/"+appID+"/indices/"+indexID, nil), 200, "")
	retriever = call("POST", "/applications/"+appID+"/retrievers", map[string]any{"type": "KENDRA_INDEX", "displayName": "r3", "configuration": map[string]any{"kendraIndexConfiguration": map[string]any{"indexId": strings.Repeat("k", 36)}}})
	expect(t, retriever, 200, "")
	expect(t, call("DELETE", "/applications/"+appID, nil), 200, "")
	expect(t, call("GET", "/applications/"+appID+"/retrievers/"+str(retriever.body["retrieverId"]), nil), 404, "ResourceNotFoundException")
	expect(t, call("DELETE", "/applications/"+appID, nil), 404, "ResourceNotFoundException")
}
