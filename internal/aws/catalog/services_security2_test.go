package catalog

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// restPartition sends a Cloud Directory request whose schema ARN travels in
// the official x-amz-data-partition header.
func restPartition(h *harness, method, path string, input map[string]any, schemaArn string) reply {
	h.t.Helper()
	var body []byte
	if input != nil {
		body, _ = json.Marshal(input)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("x-amz-data-partition", schemaArn)
	sign(req, "clouddirectory", "us-east-1")
	return h.do(req)
}

func security2Harness(t *testing.T) *harness {
	return newHarness(t, auditManager, cloudDirectory, firewallManager, guardDuty, identityCenter, inspector, macie, paymentCryptography, securityIncidentResponse, securityLake)
}

func TestSecurity2CheckHooksApplyDocumentedRules(t *testing.T) {
	words := func(n int, word string) []any {
		out := []any{}
		for i := 0; i < n; i++ {
			out = append(out, word)
		}
		return out
	}
	appsList := newFirewallResource("AppsList", "ListId", "ListUpdateToken")
	cases := []struct {
		name  string
		check func(map[string]any) error
		input map[string]any
		code  string
	}{
		{"enabled detector", enabledDetector, map[string]any{"Enable": true, "FindingPublishingFrequency": "ONE_HOUR"}, ""},
		{"disabled detector", enabledDetector, map[string]any{"Enable": false}, "BadRequestException"},
		{"detector data sources", enabledDetector, map[string]any{"Enable": true, "DataSources": map[string]any{}}, "BadRequestException"},
		{"detector features", enabledDetector, map[string]any{"Features": []any{}}, "BadRequestException"},
		{"all settings", allSettings, map[string]any{"attribute": "ALL"}, ""},
		{"one setting", allSettings, map[string]any{"attribute": "SNS_TOPIC"}, "ValidationException"},
		{"regex allow list", regexAllowList, map[string]any{"criteria": map[string]any{"regex": "a+"}}, ""},
		{"s3 allow list", regexAllowList, map[string]any{"criteria": map[string]any{"s3WordsList": map[string]any{"bucketName": "b", "objectKey": "k"}}}, "ValidationException"},
		{"empty allow list", regexAllowList, map[string]any{"criteria": map[string]any{}}, "ValidationException"},
		{"identifier", customDataIdentifier, map[string]any{"name": "emp", "regex": "E[0-9]+", "keywords": []any{"emp"}, "ignoreWords": []any{"EMP-0"}, "maximumMatchDistance": int64(300)}, ""},
		{"long regex", customDataIdentifier, map[string]any{"name": "emp", "regex": strings.Repeat("a", 513)}, "ValidationException"},
		{"long name", customDataIdentifier, map[string]any{"name": strings.Repeat("n", 129), "regex": "a"}, "ValidationException"},
		{"long description", customDataIdentifier, map[string]any{"name": "n", "regex": "a", "description": strings.Repeat("d", 513)}, "ValidationException"},
		{"many keywords", customDataIdentifier, map[string]any{"name": "n", "regex": "a", "keywords": words(51, "word")}, "ValidationException"},
		{"short keyword", customDataIdentifier, map[string]any{"name": "n", "regex": "a", "keywords": []any{"ab"}}, "ValidationException"},
		{"long keyword", customDataIdentifier, map[string]any{"name": "n", "regex": "a", "keywords": []any{strings.Repeat("k", 91)}}, "ValidationException"},
		{"many ignore words", customDataIdentifier, map[string]any{"name": "n", "regex": "a", "ignoreWords": words(11, "word")}, "ValidationException"},
		{"short ignore word", customDataIdentifier, map[string]any{"name": "n", "regex": "a", "ignoreWords": []any{"abc"}}, "ValidationException"},
		{"zero distance", customDataIdentifier, map[string]any{"name": "n", "regex": "a", "maximumMatchDistance": int64(0)}, "ValidationException"},
		{"far distance", customDataIdentifier, map[string]any{"name": "n", "regex": "a", "maximumMatchDistance": int64(301)}, "ValidationException"},
		{"findings filter", macieFindingsFilter, map[string]any{"name": "archive", "description": "d"}, ""},
		{"filter update without name", macieFindingsFilter, map[string]any{"action": "NOOP"}, ""},
		{"short filter name", macieFindingsFilter, map[string]any{"name": "ab"}, "ValidationException"},
		{"long filter name", macieFindingsFilter, map[string]any{"name": strings.Repeat("f", 65)}, "ValidationException"},
		{"long filter description", macieFindingsFilter, map[string]any{"name": "abc", "description": strings.Repeat("d", 513)}, "ValidationException"},
		{"keyless alias", noPaymentKey, map[string]any{"AliasName": "alias/a"}, ""},
		{"alias key", noPaymentKey, map[string]any{"KeyArn": "arn:aws:payment-cryptography:us-east-1:000000000000:key/abcdefghijklmnop"}, "ResourceNotFoundException"},
		{"standalone membership", standaloneMembership, map[string]any{"coverEntireOrganization": false, "optInFeatures": []any{map[string]any{"featureName": "Triage", "isEnabled": false}}}, ""},
		{"organization membership", standaloneMembership, map[string]any{"coverEntireOrganization": true}, "ValidationException"},
		{"triage enabled", standaloneMembership, map[string]any{"optInFeatures": []any{map[string]any{"featureName": "Triage", "isEnabled": true}}}, "ValidationException"},
		{"new apps list", appsList, map[string]any{"AppsList": map[string]any{"ListName": "l"}}, ""},
		{"apps list update", appsList, map[string]any{"AppsList": map[string]any{"ListName": "l", "ListUpdateToken": "t"}}, "InvalidInputException"},
		{"own list", noDefaultList, map[string]any{"ListId": "x", "DefaultList": false}, ""},
		{"default list", noDefaultList, map[string]any{"ListId": "x", "DefaultList": true}, "ResourceNotFoundException"},
		{"default session", sessionDuration, map[string]any{"Name": "ps"}, ""},
		{"hours", sessionDuration, map[string]any{"SessionDuration": "PT12H"}, ""},
		{"hours and minutes", sessionDuration, map[string]any{"SessionDuration": "PT1H30M"}, ""},
		{"seconds", sessionDuration, map[string]any{"SessionDuration": "PT3600S"}, ""},
		{"too long", sessionDuration, map[string]any{"SessionDuration": "PT12H1S"}, "ValidationException"},
		{"too short", sessionDuration, map[string]any{"SessionDuration": "PT59M"}, "ValidationException"},
		{"days", sessionDuration, map[string]any{"SessionDuration": "P1D"}, "ValidationException"},
		{"empty", sessionDuration, map[string]any{"SessionDuration": "PT"}, "ValidationException"},
	}
	for _, c := range cases {
		if got := faultCode(t, c.check(c.input)); got != c.code {
			t.Errorf("%s: want %q, got %q", c.name, c.code, got)
		}
	}
}

// Detection, ingestion and key-bearing requests are refused before any
// state is stored, with each operation's modeled exception.
func TestSecurity2RefusalsStoreNothing(t *testing.T) {
	h := security2Harness(t)
	team := []any{map[string]any{"name": "Alice", "jobTitle": "Lead", "email": "alice@example.com"}, map[string]any{"name": "Bobby", "jobTitle": "Engineer", "email": "bob@example.com"}}
	expect(t, h.rest("guardduty", "POST", "/detector", map[string]any{"enable": false}), 400, "BadRequestException")
	expect(t, h.rest("guardduty", "POST", "/detector", map[string]any{"enable": true, "dataSources": map[string]any{"s3Logs": map[string]any{"enable": true}}}), 400, "BadRequestException")
	expect(t, h.rest("macie2", "POST", "/allow-lists", map[string]any{"name": "s3", "clientToken": "a", "criteria": map[string]any{"s3WordsList": map[string]any{"bucketName": "bucket", "objectKey": "k"}}}), 400, "ValidationException")
	expect(t, h.rest("macie2", "POST", "/custom-data-identifiers", map[string]any{"name": "id", "regex": "a", "maximumMatchDistance": 0}), 400, "ValidationException")
	expect(t, h.rest("security-ir", "POST", "/v1/membership", map[string]any{"membershipName": "org", "incidentResponseTeam": team, "coverEntireOrganization": true}), 400, "ValidationException")
	expect(t, h.json1("payment-cryptography", "CreateAlias", map[string]any{"AliasName": "alias/pin", "KeyArn": "arn:aws:payment-cryptography:us-east-1:000000000000:key/abcdefghijklmnop"}), 400, "ResourceNotFoundException")
	expect(t, h.json1("fms", "PutAppsList", map[string]any{"AppsList": map[string]any{"ListId": "00000000-0000-4000-8000-000000000000", "ListName": "l", "AppsList": []any{}}}), 400, "InvalidInputException")
	expect(t, h.json1("fms", "PutResourceSet", map[string]any{"ResourceSet": map[string]any{"Name": "s", "ResourceTypeList": []any{}, "ResourceSetStatus": "OUT_OF_ADMIN_SCOPE"}}), 400, "InvalidInputException")
	expect(t, h.rest("clouddirectory", "PUT", "/amazonclouddirectory/2017-01-11/schema/create", map[string]any{"Name": "org"}), 200, "")
	var rows int
	_ = h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources").Scan(&rows)
	if rows != 1 {
		t.Fatalf("refused requests stored resources: %d rows", rows)
	}
	expect(t, h.rest("auditmanager", "GET", "/settings/SNS_TOPIC", nil), 400, "ValidationException")
	expect(t, h.rest("auditmanager", "PUT", "/settings", map[string]any{"evidenceFinderEnabled": true}), 400, "ValidationException")
}

func TestGuardDutyDetectorsFiltersAndErrors(t *testing.T) {
	h := security2Harness(t)
	created := h.rest("guardduty", "POST", "/detector", map[string]any{"enable": true, "tags": map[string]any{"team": "a"}})
	expect(t, created, 200, "")
	id := str(created.body["detectorId"])
	if len(id) != 32 {
		t.Fatalf("detector id %q", id)
	}
	expect(t, h.rest("guardduty", "POST", "/detector", map[string]any{"enable": true}), 400, "LimitExceededException")
	expect(t, h.rest("guardduty", "GET", "/detector/"+strings.Repeat("0", 32), nil), 400, "BadRequestException")
	filter := map[string]any{"name": "archive-low", "findingCriteria": map[string]any{"criterion": map[string]any{"severity": map[string]any{"lt": 4}}}}
	expect(t, h.rest("guardduty", "POST", "/detector/"+id+"/filter", filter), 200, "")
	expect(t, h.rest("guardduty", "POST", "/detector/"+id+"/filter", filter), 400, "BadRequestException")
	got := h.rest("guardduty", "GET", "/detector/"+id+"/filter/archive-low", nil)
	if got.body["action"] != "NOOP" || got.body["rank"] != nil {
		t.Fatalf("filter defaults %v", got.body)
	}
	expect(t, h.rest("guardduty", "POST", "/tags/"+url.PathEscape("arn:aws:guardduty:us-east-1:000000000000:detector/"+id+"/filter/archive-low"), map[string]any{"tags": map[string]any{"k": "v"}}), 204, "")
	expect(t, h.rest("guardduty", "DELETE", "/detector/"+id, nil), 200, "")
	var rows int
	_ = h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources WHERE service='guardduty'").Scan(&rows)
	if rows != 0 {
		t.Fatalf("deleting a detector must delete its filters: %d rows", rows)
	}
}

func TestSecurity2ParentsSingletonsAndStates(t *testing.T) {
	h := security2Harness(t)
	const cd = "/amazonclouddirectory/2017-01-11"
	schema := h.rest("clouddirectory", "PUT", cd+"/schema/create", map[string]any{"Name": "org"})
	expect(t, schema, 200, "")
	arn := str(schema.body["SchemaArn"])
	facet := func(method, path string, body map[string]any) reply {
		return restPartition(h, method, cd+path, body, arn)
	}
	expect(t, facet("PUT", "/facet/create", map[string]any{"Name": "person", "ObjectType": "NODE"}), 200, "")
	expect(t, facet("PUT", "/facet", map[string]any{"Name": "person", "AttributeUpdates": []any{}}), 400, "ValidationException")
	expect(t, facet("POST", "/facet", map[string]any{"Name": "nobody"}), 404, "ResourceNotFoundException")
	expect(t, restPartition(h, "PUT", cd+"/schema", nil, arn), 200, "")
	var rows int
	_ = h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources WHERE service='clouddirectory'").Scan(&rows)
	if rows != 0 {
		t.Fatalf("deleting a development schema must delete its facets: %d rows", rows)
	}

	expect(t, h.rest("securitylake", "GET", "/v1/datalake/exceptions/subscription", nil), 200, "")
	subscription := map[string]any{"notificationEndpoint": "arn:aws:sqs:us-east-1:000000000000:q", "subscriptionProtocol": "sqs"}
	expect(t, h.rest("securitylake", "POST", "/v1/datalake/exceptions/subscription", subscription), 200, "")
	expect(t, h.rest("securitylake", "POST", "/v1/datalake/exceptions/subscription", subscription), 409, "ConflictException")
	expect(t, h.rest("securitylake", "DELETE", "/v1/datalake/exceptions/subscription", nil), 200, "")
	expect(t, h.rest("securitylake", "DELETE", "/v1/datalake/exceptions/subscription", nil), 404, "ResourceNotFoundException")

	team := []any{map[string]any{"name": "Alice", "jobTitle": "Lead", "email": "alice@example.com"}, map[string]any{"name": "Bobby", "jobTitle": "Engineer", "email": "bob@example.com"}}
	membership := h.rest("security-ir", "POST", "/v1/membership", map[string]any{"membershipName": "team-ir", "incidentResponseTeam": team, "coverEntireOrganization": false})
	expect(t, membership, 201, "")
	id := str(membership.body["membershipId"])
	expect(t, h.rest("security-ir", "PUT", "/v1/membership/"+id, nil), 200, "")
	if got := h.rest("security-ir", "GET", "/v1/membership/"+id, nil); got.body["membershipStatus"] != "Cancelled" || got.body["membershipDeactivationTimestamp"] == nil {
		t.Fatalf("cancelled membership %v", got.body)
	}
	expect(t, h.rest("security-ir", "POST", "/v1/membership", map[string]any{"membershipName": "again", "incidentResponseTeam": team}), 402, "ServiceQuotaExceededException")

	apps := h.json1("fms", "PutAppsList", map[string]any{"AppsList": map[string]any{"ListName": "web", "AppsList": []any{map[string]any{"AppName": "https", "Protocol": "tcp", "Port": 443}}}})
	expect(t, apps, 200, "")
	for flag, want := range map[bool]int{false: 1, true: 0} {
		if got := h.json1("fms", "ListAppsLists", map[string]any{"MaxResults": 10, "DefaultLists": flag}).body["AppsLists"].([]any); len(got) != want {
			t.Fatalf("DefaultLists %v listed %v", flag, got)
		}
	}

	instance := h.json1("sso-admin", "CreateInstance", map[string]any{"Name": "center"})
	expect(t, instance, 200, "")
	instanceArn := str(instance.body["InstanceArn"])
	if !strings.HasPrefix(instanceArn, "arn:aws:sso:::instance/ssoins-") {
		t.Fatalf("instance ARN %s", instanceArn)
	}
	expect(t, h.json1("sso-admin", "CreatePermissionSet", map[string]any{"InstanceArn": instanceArn, "Name": "Admin", "SessionDuration": "PT13H"}), 400, "ValidationException")
	expect(t, h.json1("sso-admin", "CreatePermissionSet", map[string]any{"InstanceArn": instanceArn, "Name": "Admin"}), 200, "")
	expect(t, h.json1("sso-admin", "TagResource", map[string]any{"InstanceArn": instanceArn, "ResourceArn": instanceArn, "Tags": []any{}}), 400, "ValidationException")
	expect(t, h.json1("sso-admin", "DeleteInstance", map[string]any{"InstanceArn": instanceArn}), 200, "")
	_ = h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources WHERE service='sso-admin'").Scan(&rows)
	if rows != 0 {
		t.Fatalf("deleting an instance must delete its permission sets: %d rows", rows)
	}
}
