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
