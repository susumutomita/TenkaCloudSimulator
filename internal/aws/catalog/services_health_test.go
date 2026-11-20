package catalog

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func healthHarness(t *testing.T) *harness {
	return newHarness(t, healthImaging, healthLake)
}

func TestHealthImagingDatastoreLifecycle(t *testing.T) {
	h := healthHarness(t)
	key := "arn:aws:kms:us-east-1:000000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	created := h.rest("medical-imaging", "POST", "/datastore", map[string]any{"datastoreName": "scans", "clientToken": "token-1", "tags": map[string]any{"team": "a"}, "kmsKeyArn": key, "losslessStorageFormat": "HTJ2K"})
	expect(t, created, 200, "")
	id := str(created.body["datastoreId"])
	if len(id) != 32 || created.body["datastoreStatus"] != "CREATING" {
		t.Fatalf("created %v", created.body)
	}
	replay := h.rest("medical-imaging", "POST", "/datastore", map[string]any{"datastoreName": "scans", "clientToken": "token-1", "tags": map[string]any{"team": "a"}, "kmsKeyArn": key, "losslessStorageFormat": "HTJ2K"})
	if replay.body["datastoreId"] != id {
		t.Fatalf("replayed token created %v", replay.body)
	}
	expect(t, h.rest("medical-imaging", "POST", "/datastore", map[string]any{"datastoreName": "other", "clientToken": "token-1"}), 409, "ConflictException")

	got := h.rest("medical-imaging", "GET", "/datastore/"+id, nil)
	expect(t, got, 200, "")
	props := got.body["datastoreProperties"].(map[string]any)
	arn := "arn:aws:medical-imaging:us-east-1:000000000000:datastore/" + id
	if props["datastoreArn"] != arn || props["datastoreName"] != "scans" || props["kmsKeyArn"] != key || props["losslessStorageFormat"] != "HTJ2K" || props["createdAt"] == nil {
		t.Fatalf("properties %v", props)
	}
	for _, member := range []string{"datastoreEndpoint", "endpoint"} {
		if _, present := props[member]; present {
			t.Fatalf("%s must not be reported", member)
		}
	}

	unnamed := h.rest("medical-imaging", "POST", "/datastore", map[string]any{"clientToken": "token-2"})
	expect(t, unnamed, 200, "")
	unnamedID := str(unnamed.body["datastoreId"])
	if name := h.rest("medical-imaging", "GET", "/datastore/"+unnamedID, nil).body["datastoreProperties"].(map[string]any)["datastoreName"]; name != unnamedID {
		t.Fatalf("default name %v", name)
	}

	list := h.rest("medical-imaging", "GET", "/datastore", nil)
	expect(t, list, 200, "")
	if summaries := list.body["datastoreSummaries"].([]any); len(summaries) != 2 || summaries[0].(map[string]any)["datastoreId"] != id {
		t.Fatalf("list %v", list.body)
	}
	filtered := h.rest("medical-imaging", "GET", "/datastore?datastoreStatus=ACTIVE", nil)
	if len(filtered.body["datastoreSummaries"].([]any)) != 0 {
		t.Fatalf("filter %v", filtered.body)
	}
	paged := h.rest("medical-imaging", "GET", "/datastore?maxResults=1", nil)
	if len(paged.body["datastoreSummaries"].([]any)) != 1 || str(paged.body["nextToken"]) == "" {
		t.Fatalf("page %v", paged.body)
	}
	expect(t, h.rest("medical-imaging", "GET", "/datastore?nextToken=bogus", nil), 400, "ValidationException")

	path := "/tags/" + url.PathEscape(arn)
	expect(t, h.rest("medical-imaging", "POST", path, map[string]any{"tags": map[string]any{"stage": "dev"}}), 200, "")
	tags := h.rest("medical-imaging", "GET", path, nil)
	if tags.body["tags"].(map[string]any)["stage"] != "dev" || tags.body["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("tags %v", tags.body)
	}
	expect(t, h.rest("medical-imaging", "DELETE", path+"?tagKeys=stage", nil), 200, "")
	if _, present := h.rest("medical-imaging", "GET", path, nil).body["tags"].(map[string]any)["stage"]; present {
		t.Fatal("tag survived untag")
	}
	imageSet := "arn:aws:medical-imaging:us-east-1:000000000000:datastore/" + id + "/imageset/" + strings.Repeat("a", 32)
	expect(t, h.rest("medical-imaging", "GET", "/tags/"+url.PathEscape(imageSet), nil), 404, "ResourceNotFoundException")

	deleted := h.rest("medical-imaging", "DELETE", "/datastore/"+id, nil)
	expect(t, deleted, 200, "")
	if deleted.body["datastoreId"] != id || deleted.body["datastoreStatus"] != "DELETING" {
		t.Fatalf("deleted %v", deleted.body)
	}
	expect(t, h.rest("medical-imaging", "GET", "/datastore/"+id, nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("medical-imaging", "DELETE", "/datastore/"+id, nil), 404, "ResourceNotFoundException")
}

func TestHealthImagingFaultsAndUnboundOperations(t *testing.T) {
	h := healthHarness(t)
	expect(t, h.rest("medical-imaging", "POST", "/datastore", map[string]any{"datastoreName": "bad name!", "clientToken": "t"}), 400, "ValidationException")
	expect(t, h.rest("medical-imaging", "POST", "/datastore", map[string]any{"clientToken": "t", "kmsKeyArn": "alias/app"}), 400, "ValidationException")
	expect(t, h.rest("medical-imaging", "POST", "/datastore", map[string]any{"clientToken": "t", "losslessStorageFormat": "PNG"}), 400, "ValidationException")
	expect(t, h.rest("medical-imaging", "POST", "/datastore", map[string]any{}), 400, "ValidationException")
	expect(t, h.rest("medical-imaging", "GET", "/datastore/"+strings.Repeat("a", 32), nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("medical-imaging", "GET", "/datastore/short", nil), 400, "ValidationException")
	for i := 0; i < 10; i++ {
		expect(t, h.rest("medical-imaging", "POST", "/datastore", map[string]any{"clientToken": "limit-" + string(rune('a'+i))}), 200, "")
	}
	expect(t, h.rest("medical-imaging", "POST", "/datastore", map[string]any{"clientToken": "limit-z"}), 402, "ServiceQuotaExceededException")
	id := strings.Repeat("b", 32)
	expect(t, h.rest("medical-imaging", "POST", "/runtime/datastore/"+id+"/importJob", nil), 404, "UnknownOperationException")
	expect(t, h.rest("medical-imaging", "POST", "/startDICOMImportJob/datastore/"+id, map[string]any{"jobName": "j"}), 501, "UnknownOperationException")
	expect(t, h.rest("medical-imaging", "POST", "/datastore/"+id+"/searchImageSets", map[string]any{}), 501, "UnknownOperationException")
}

func TestHealthLakeCheckHooksApplyDocumentedRules(t *testing.T) {
	provider := func(members map[string]any) map[string]any {
		return map[string]any{"IdentityProviderConfiguration": members}
	}
	sse := func(kind string, key bool) map[string]any {
		config := map[string]any{"CmkType": kind}
		if key {
			config["KmsKeyId"] = "alias/app"
		}
		return map[string]any{"SseConfiguration": map[string]any{"KmsEncryptionConfig": config}}
	}
	smart := func(metadata string) map[string]any {
		return provider(map[string]any{"AuthorizationStrategy": healthLakeSMART, "Metadata": metadata})
	}
	cases := []struct {
		name  string
		input map[string]any
		code  string
	}{
		{"plain", map[string]any{}, ""},
		{"preload", map[string]any{"PreloadDataConfig": map[string]any{"PreloadDataType": "SYNTHEA"}}, "ValidationException"},
		{"owned key", sse("AWS_OWNED_KMS_KEY", false), ""},
		{"owned key with id", sse("AWS_OWNED_KMS_KEY", true), "ValidationException"},
		{"customer key", sse("CUSTOMER_MANAGED_KMS_KEY", true), ""},
		{"customer key without id", sse("CUSTOMER_MANAGED_KMS_KEY", false), "ValidationException"},
		{"aws auth", provider(map[string]any{"AuthorizationStrategy": "AWS_AUTH"}), ""},
		{"aws auth metadata", provider(map[string]any{"AuthorizationStrategy": "AWS_AUTH", "Metadata": "{}"}), "ValidationException"},
		{"aws auth lambda", provider(map[string]any{"AuthorizationStrategy": "AWS_AUTH", "IdpLambdaArn": "arn"}), "ValidationException"},
		{"aws auth fine grained", provider(map[string]any{"AuthorizationStrategy": "AWS_AUTH", "FineGrainedAuthorizationEnabled": true}), "ValidationException"},
		{"aws auth fine grained off", provider(map[string]any{"AuthorizationStrategy": "AWS_AUTH", "FineGrainedAuthorizationEnabled": false}), ""},
		{"smart without metadata", provider(map[string]any{"AuthorizationStrategy": "SMART_ON_FHIR"}), ""},
		{"smart metadata", smart(`{"grant_types_supported":["authorization_code"],"code_challenge_methods_supported":["S256"]}`), ""},
		{"smart metadata text", smart(`not json`), "ValidationException"},
		{"smart no grants", smart(`{"grant_types_supported":[]}`), "ValidationException"},
		{"smart unknown grant", smart(`{"grant_types_supported":["password"]}`), "ValidationException"},
		{"smart without S256", smart(`{"code_challenge_methods_supported":["plain"]}`), "ValidationException"},
		{"smart code challenge not a list", smart(`{"code_challenge_methods_supported":"S256"}`), "ValidationException"},
		{"smart other elements", smart(`{"token_endpoint":"https://idp.example.com/token"}`), ""},
	}
	for _, c := range cases {
		err := healthLakeCreate(c.input)
		var f *fault
		if c.code == "" && err != nil || c.code != "" && (!errors.As(err, &f) || f.code != c.code) {
			t.Fatalf("%s: want %q, got %v", c.name, c.code, err)
		}
	}
	filter := map[string]any{"Filter": map[string]any{"DatastoreName": "a", "DatastoreStatus": "ACTIVE"}}
	if err := healthLakeList(filter); err != nil || filter["DatastoreName"] != "a" || filter["DatastoreStatus"] != "ACTIVE" || filter["Filter"] != nil {
		t.Fatalf("flattened filter %v %v", filter, err)
	}
	if err := healthLakeList(map[string]any{}); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"CreatedBefore", "CreatedAfter"} {
		var f *fault
		if err := healthLakeList(map[string]any{"Filter": map[string]any{member: "t"}}); !errors.As(err, &f) || f.code != "ValidationException" {
			t.Fatalf("%s: %v", member, err)
		}
	}
}

func TestHealthLakeDatastoreLifecycle(t *testing.T) {
	h := healthHarness(t)
	create := map[string]any{"DatastoreName": "records", "DatastoreTypeVersion": "R4", "ClientToken": "token-1", "Tags": []any{map[string]any{"Key": "team", "Value": "a"}},
		"IdentityProviderConfiguration": map[string]any{"AuthorizationStrategy": "SMART_ON_FHIR", "FineGrainedAuthorizationEnabled": true,
			"Metadata": `{"grant_types_supported":["authorization_code"]}`, "IdpLambdaArn": "arn:aws:lambda:us-east-1:000000000000:function:decode"}}
	created := h.json1("healthlake", "CreateFHIRDatastore", create)
	expect(t, created, 200, "")
	id := str(created.body["DatastoreId"])
	arn := "arn:aws:healthlake:us-east-1:000000000000:datastore/fhir/" + id
	if len(id) != 32 || created.body["DatastoreArn"] != arn || created.body["DatastoreStatus"] != "CREATING" {
		t.Fatalf("created %v", created.body)
	}
	if _, present := created.body["DatastoreEndpoint"]; present {
		t.Fatal("the endpoint must not be reported")
	}
	if replay := h.json1("healthlake", "CreateFHIRDatastore", create); replay.body["DatastoreId"] != id {
		t.Fatalf("replay %v", replay.body)
	}
	expect(t, h.json1("healthlake", "CreateFHIRDatastore", map[string]any{"DatastoreTypeVersion": "R4", "ClientToken": "token-1"}), 400, "")

	described := h.json1("healthlake", "DescribeFHIRDatastore", map[string]any{"DatastoreId": id})
	expect(t, described, 200, "")
	props := described.body["DatastoreProperties"].(map[string]any)
	idp := props["IdentityProviderConfiguration"].(map[string]any)
	if props["DatastoreName"] != "records" || props["DatastoreTypeVersion"] != "R4" || idp["AuthorizationStrategy"] != "SMART_ON_FHIR" || props["CreatedAt"] == nil {
		t.Fatalf("properties %v", props)
	}
	if kms := props["SseConfiguration"].(map[string]any)["KmsEncryptionConfig"].(map[string]any); kms["CmkType"] != "AWS_OWNED_KMS_KEY" {
		t.Fatalf("default encryption %v", props["SseConfiguration"])
	}
	if _, present := props["DatastoreEndpoint"]; present {
		t.Fatal("the endpoint must not be reported")
	}
	keyed := h.json1("healthlake", "CreateFHIRDatastore", map[string]any{"DatastoreTypeVersion": "R4",
		"SseConfiguration": map[string]any{"KmsEncryptionConfig": map[string]any{"CmkType": "CUSTOMER_MANAGED_KMS_KEY", "KmsKeyId": "alias/records"}}})
	expect(t, keyed, 200, "")
	keyedID := str(keyed.body["DatastoreId"])
	if _, present := h.json1("healthlake", "DescribeFHIRDatastore", map[string]any{"DatastoreId": keyedID}).body["DatastoreProperties"].(map[string]any)["DatastoreName"]; present {
		t.Fatal("an omitted name is not invented")
	}

	list := h.json1("healthlake", "ListFHIRDatastores", map[string]any{})
	expect(t, list, 200, "")
	if items := list.body["DatastorePropertiesList"].([]any); len(items) != 2 || items[0].(map[string]any)["DatastoreId"] != id {
		t.Fatalf("list %v", list.body)
	}
	byName := h.json1("healthlake", "ListFHIRDatastores", map[string]any{"Filter": map[string]any{"DatastoreName": "records"}})
	if items := byName.body["DatastorePropertiesList"].([]any); len(items) != 1 || items[0].(map[string]any)["DatastoreId"] != id {
		t.Fatalf("name filter %v", byName.body)
	}
	if items := h.json1("healthlake", "ListFHIRDatastores", map[string]any{"Filter": map[string]any{"DatastoreStatus": "ACTIVE"}}).body["DatastorePropertiesList"].([]any); len(items) != 0 {
		t.Fatalf("status filter %v", items)
	}
	if items := h.json1("healthlake", "ListFHIRDatastores", map[string]any{"Filter": map[string]any{"DatastoreStatus": "CREATING"}}).body["DatastorePropertiesList"].([]any); len(items) != 2 {
		t.Fatalf("matching status filter %v", items)
	}
	paged := h.json1("healthlake", "ListFHIRDatastores", map[string]any{"MaxResults": 1})
	if len(paged.body["DatastorePropertiesList"].([]any)) != 1 || str(paged.body["NextToken"]) == "" {
		t.Fatalf("page %v", paged.body)
	}
	next := h.json1("healthlake", "ListFHIRDatastores", map[string]any{"MaxResults": 1, "NextToken": paged.body["NextToken"]})
	if items := next.body["DatastorePropertiesList"].([]any); len(items) != 1 || items[0].(map[string]any)["DatastoreId"] != keyedID {
		t.Fatalf("second page %v", next.body)
	}
	expect(t, h.json1("healthlake", "ListFHIRDatastores", map[string]any{"NextToken": "bogus"}), 400, "ValidationException")
	expect(t, h.json1("healthlake", "ListFHIRDatastores", map[string]any{"Filter": map[string]any{"CreatedBefore": 1}}), 400, "ValidationException")

	expect(t, h.json1("healthlake", "TagResource", map[string]any{"ResourceARN": arn, "Tags": []any{map[string]any{"Key": "stage", "Value": "dev"}}}), 200, "")
	tags := h.json1("healthlake", "ListTagsForResource", map[string]any{"ResourceARN": arn}).body["Tags"].([]any)
	if len(tags) != 2 || tags[0].(map[string]any)["Key"] != "stage" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.json1("healthlake", "UntagResource", map[string]any{"ResourceARN": arn, "TagKeys": []any{"stage"}}), 200, "")
	if tags := h.json1("healthlake", "ListTagsForResource", map[string]any{"ResourceARN": arn}).body["Tags"].([]any); len(tags) != 1 {
		t.Fatalf("tags after untag %v", tags)
	}

	deleted := h.json1("healthlake", "DeleteFHIRDatastore", map[string]any{"DatastoreId": id})
	expect(t, deleted, 200, "")
	if deleted.body["DatastoreArn"] != arn || deleted.body["DatastoreStatus"] != "DELETING" {
		t.Fatalf("deleted %v", deleted.body)
	}
	expect(t, h.json1("healthlake", "DescribeFHIRDatastore", map[string]any{"DatastoreId": id}), 400, "ResourceNotFoundException")
	expect(t, h.json1("healthlake", "DeleteFHIRDatastore", map[string]any{"DatastoreId": id}), 400, "ResourceNotFoundException")
}

func TestHealthLakeFaultsAndUnboundOperations(t *testing.T) {
	h := healthHarness(t)
	expect(t, h.json1("healthlake", "CreateFHIRDatastore", map[string]any{}), 400, "ValidationException")
	expect(t, h.json1("healthlake", "CreateFHIRDatastore", map[string]any{"DatastoreTypeVersion": "R5"}), 400, "ValidationException")
	expect(t, h.json1("healthlake", "CreateFHIRDatastore", map[string]any{"DatastoreTypeVersion": "R4", "PreloadDataConfig": map[string]any{"PreloadDataType": "SYNTHEA"}}), 400, "ValidationException")
	expect(t, h.json1("healthlake", "CreateFHIRDatastore", map[string]any{"DatastoreTypeVersion": "R4", "DatastoreName": strings.Repeat("a", 257)}), 400, "ValidationException")
	expect(t, h.json1("healthlake", "DescribeFHIRDatastore", map[string]any{"DatastoreId": strings.Repeat("a", 32)}), 400, "ResourceNotFoundException")
	expect(t, h.json1("healthlake", "TagResource", map[string]any{"ResourceARN": "arn:aws:healthlake:us-east-1:000000000000:datastore/fhir/" + strings.Repeat("a", 32), "Tags": []any{}}), 400, "ResourceNotFoundException")
	for i := 0; i < 10; i++ {
		expect(t, h.json1("healthlake", "CreateFHIRDatastore", map[string]any{"DatastoreTypeVersion": "R4"}), 200, "")
	}
	expect(t, h.json1("healthlake", "CreateFHIRDatastore", map[string]any{"DatastoreTypeVersion": "R4"}), 400, "ValidationException")
	for _, op := range []string{"StartFHIRImportJob", "DescribeFHIRImportJob", "ListFHIRImportJobs", "StartFHIRExportJob", "DescribeFHIRExportJob", "ListFHIRExportJobs"} {
		expect(t, h.json1("healthlake", op, map[string]any{"DatastoreId": strings.Repeat("a", 32)}), 501, "UnknownOperationException")
	}
}

// healthLakeSMART is the official SMART on FHIR authorization strategy value.
const healthLakeSMART = "SMART_ON_FHIR_V1"
