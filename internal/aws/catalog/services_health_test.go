package catalog

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func healthHarness(t *testing.T) *harness {
	return newHarness(t, healthImaging, healthLake, omics)
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

func TestOmicsNumericIdentifiers(t *testing.T) {
	digits := func(id string) bool {
		if len(id) != 10 || id[0] == '0' {
			return false
		}
		for _, c := range id {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	if a, b := omicsNumericID("Op", "token"), omicsNumericID("Op", "token"); a != b || !digits(a) {
		t.Fatalf("token identifiers %s %s", a, b)
	}
	if omicsNumericID("Op", "token") == omicsNumericID("Other", "token") || omicsNumericID("Op", "a") == omicsNumericID("Op", "b") {
		t.Fatal("identifiers must differ by operation and token")
	}
	if a, b := omicsNumericID("Op", ""), omicsNumericID("Op", ""); !digits(a) || !digits(b) || a == b {
		t.Fatalf("random identifiers %s %s", a, b)
	}
	input := map[string]any{"requestId": "r"}
	if err := omicsIdentity("Op", "requestId")(input); err != nil || input["id"] != omicsNumericID("Op", "r") {
		t.Fatalf("hook %v %v", input, err)
	}
}

func TestOmicsS3Locations(t *testing.T) {
	cases := []struct {
		value string
		ok    bool
	}{{"s3://bucket", true}, {"s3://bucket/prefix/", true}, {"s3://my.bucket-1/a/b", true}, {"", true}, {"bucket", false}, {"https://bucket", false}, {"s3://B", false}, {"s3://bucket/x", true}}
	for _, c := range cases {
		input := map[string]any{"cacheS3Location": c.value}
		err := omicsLocation("cacheS3Location")(input)
		if (err == nil) != c.ok {
			t.Fatalf("%q: %v", c.value, err)
		}
	}
	if err := omicsLocation("cacheS3Location")(map[string]any{}); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"clientToken": "t", "fallbackLocation": "elsewhere"}
	if err := omicsCreate("CreateSequenceStore", "clientToken", "fallbackLocation")(input); err == nil || input["id"] != nil {
		t.Fatalf("a bad location must stop creation: %v %v", err, input)
	}
	input = map[string]any{"clientToken": "t", "fallbackLocation": "s3://bucket"}
	if err := omicsCreate("CreateSequenceStore", "clientToken", "fallbackLocation")(input); err != nil || input["id"] == nil {
		t.Fatalf("a good location creates: %v %v", err, input)
	}
}

func TestOmicsStoreFilters(t *testing.T) {
	input := map[string]any{"filter": map[string]any{"name": "a", "status": "CREATING"}}
	if err := omicsStoreFilter(true)(input); err != nil || input["name"] != "a" || input["status"] != "CREATING" || input["filter"] != nil {
		t.Fatalf("flattened %v %v", input, err)
	}
	if err := omicsStoreFilter(true)(map[string]any{}); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"createdAfter", "createdBefore", "updatedAfter", "updatedBefore"} {
		var f *fault
		if err := omicsStoreFilter(true)(map[string]any{"filter": map[string]any{member: "t"}}); !errors.As(err, &f) || f.code != "ValidationException" {
			t.Fatalf("%s: %v", member, err)
		}
	}
	var f *fault
	if err := omicsStoreFilter(false)(map[string]any{"filter": map[string]any{"status": "ACTIVE"}}); !errors.As(err, &f) {
		t.Fatalf("reference stores filter by name only: %v", err)
	}
}

func TestOmicsRunGroupAndRunCacheLifecycle(t *testing.T) {
	h := healthHarness(t)
	group := map[string]any{"requestId": "rg-1", "name": "batch", "maxCpus": 8, "maxRuns": 2, "maxDuration": 60, "maxGpus": 1, "tags": map[string]any{"team": "a"}}
	created := h.rest("omics", "POST", "/runGroup", group)
	expect(t, created, 201, "")
	id := str(created.body["id"])
	arn := "arn:aws:omics:us-east-1:000000000000:runGroup/" + id
	if len(id) != 10 || created.body["arn"] != arn || created.body["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("created %v", created.body)
	}
	if replay := h.rest("omics", "POST", "/runGroup", group); replay.body["id"] != id {
		t.Fatalf("replay %v", replay.body)
	}
	group["maxRuns"] = 3
	expect(t, h.rest("omics", "POST", "/runGroup", group), 409, "ConflictException")
	expect(t, h.rest("omics", "POST", "/runGroup", map[string]any{"requestId": "rg-2", "name": "other"}), 201, "")

	got := h.rest("omics", "GET", "/runGroup/"+id, nil)
	expect(t, got, 200, "")
	if got.body["name"] != "batch" || got.body["maxCpus"] != json.Number("8") || got.body["creationTime"] == nil || got.body["id"] != id {
		t.Fatalf("run group %v", got.body)
	}
	byName := h.rest("omics", "GET", "/runGroup?name=batch", nil)
	if items := byName.body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["arn"] != arn {
		t.Fatalf("by name %v", byName.body)
	}
	if items := h.rest("omics", "GET", "/runGroup", nil).body["items"].([]any); len(items) != 2 {
		t.Fatalf("list %v", items)
	}
	paged := h.rest("omics", "GET", "/runGroup?maxResults=1", nil)
	if len(paged.body["items"].([]any)) != 1 || str(paged.body["nextToken"]) == "" {
		t.Fatalf("page %v", paged.body)
	}
	expect(t, h.rest("omics", "POST", "/runGroup/"+id, map[string]any{"name": "renamed", "maxRuns": 5}), 202, "")
	if got := h.rest("omics", "GET", "/runGroup/"+id, nil).body; got["name"] != "renamed" || got["maxRuns"] != json.Number("5") {
		t.Fatalf("updated %v", got)
	}
	expect(t, h.rest("omics", "POST", "/runGroup/"+id, map[string]any{"maxCpus": 0}), 400, "ValidationException")

	tagPath := "/tags/" + url.PathEscape(arn)
	expect(t, h.rest("omics", "POST", tagPath, map[string]any{"tags": map[string]any{"stage": "dev"}}), 204, "")
	if tags := h.rest("omics", "GET", tagPath, nil).body["tags"].(map[string]any); len(tags) != 2 {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("omics", "DELETE", tagPath+"?tagKeys=stage", nil), 204, "")
	if tags := h.rest("omics", "GET", tagPath, nil).body["tags"].(map[string]any); len(tags) != 1 {
		t.Fatalf("tags after untag %v", tags)
	}
	expect(t, h.rest("omics", "DELETE", "/runGroup/"+id, nil), 202, "")
	expect(t, h.rest("omics", "GET", "/runGroup/"+id, nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("omics", "DELETE", "/runGroup/"+id, nil), 404, "ResourceNotFoundException")

	cache := h.rest("omics", "POST", "/runCache", map[string]any{"requestId": "rc-1", "cacheS3Location": "s3://bucket/cache", "name": "cache", "description": "d", "cacheBucketOwnerId": "000000000000"})
	expect(t, cache, 201, "")
	cacheID := str(cache.body["id"])
	if cache.body["status"] != "ACTIVE" || cache.body["arn"] != "arn:aws:omics:us-east-1:000000000000:runCache/"+cacheID {
		t.Fatalf("created cache %v", cache.body)
	}
	described := h.rest("omics", "GET", "/runCache/"+cacheID, nil)
	if described.body["cacheS3Uri"] != "s3://bucket/cache" || described.body["cacheBehavior"] != "CACHE_ON_FAILURE" || described.body["name"] != "cache" {
		t.Fatalf("cache %v", described.body)
	}
	if items := h.rest("omics", "GET", "/runCache", nil).body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["cacheS3Uri"] != "s3://bucket/cache" {
		t.Fatalf("cache list %v", items)
	}
	expect(t, h.rest("omics", "POST", "/runCache/"+cacheID, map[string]any{"cacheBehavior": "CACHE_ALWAYS", "description": "changed"}), 202, "")
	if got := h.rest("omics", "GET", "/runCache/"+cacheID, nil).body; got["cacheBehavior"] != "CACHE_ALWAYS" || got["description"] != "changed" {
		t.Fatalf("updated cache %v", got)
	}
	expect(t, h.rest("omics", "POST", "/runCache/"+cacheID, map[string]any{"cacheBehavior": "NEVER"}), 400, "ValidationException")
	expect(t, h.rest("omics", "POST", "/runCache", map[string]any{"requestId": "rc-2"}), 400, "ValidationException")
	expect(t, h.rest("omics", "POST", "/runCache", map[string]any{"requestId": "rc-3", "cacheS3Location": "not-an-s3-uri"}), 400, "ValidationException")
	expect(t, h.rest("omics", "DELETE", "/runCache/"+cacheID, nil), 202, "")
	expect(t, h.rest("omics", "GET", "/runCache/"+cacheID, nil), 404, "ResourceNotFoundException")
}

func TestOmicsStoresLifecycle(t *testing.T) {
	h := healthHarness(t)
	create := map[string]any{"name": "reads", "description": "fixture", "clientToken": "ss-1", "sseConfig": map[string]any{"type": "KMS", "keyArn": "arn:aws:kms:us-east-1:000000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab"},
		"fallbackLocation": "s3://bucket/fallback", "propagatedSetLevelTags": []any{"team"}, "tags": map[string]any{"team": "a"}}
	created := h.rest("omics", "POST", "/sequencestore", create)
	expect(t, created, 200, "")
	id := str(created.body["id"])
	arn := "arn:aws:omics:us-east-1:000000000000:sequenceStore/" + id
	if len(id) != 10 || created.body["arn"] != arn || created.body["status"] != "CREATING" || created.body["eTagAlgorithmFamily"] != "MD5up" {
		t.Fatalf("created %v", created.body)
	}
	if _, present := created.body["s3Access"]; present {
		t.Fatal("no S3 access point exists")
	}
	if replay := h.rest("omics", "POST", "/sequencestore", create); replay.body["id"] != id {
		t.Fatalf("replay %v", replay.body)
	}
	expect(t, h.rest("omics", "POST", "/sequencestore", map[string]any{"name": "access", "s3AccessConfig": map[string]any{"accessLogLocation": "s3://bucket/logs"}}), 400, "ValidationException")
	expect(t, h.rest("omics", "POST", "/sequencestore", map[string]any{"name": "other"}), 200, "")
	expect(t, h.rest("omics", "POST", "/sequencestore", map[string]any{}), 400, "ValidationException")

	got := h.rest("omics", "GET", "/sequencestore/"+id, nil)
	expect(t, got, 200, "")
	if got.body["name"] != "reads" || got.body["fallbackLocation"] != "s3://bucket/fallback" || got.body["propagatedSetLevelTags"].([]any)[0] != "team" {
		t.Fatalf("store %v", got.body)
	}
	h.now = h.now.Add(time.Hour)
	updated := h.rest("omics", "PATCH", "/sequencestore/"+id, map[string]any{"description": "changed", "clientToken": "ss-2"})
	expect(t, updated, 200, "")
	if updated.body["description"] != "changed" || updated.body["updateTime"] == got.body["updateTime"] {
		t.Fatalf("updated %v vs %v", updated.body, got.body)
	}
	expect(t, h.rest("omics", "PATCH", "/sequencestore/"+id, map[string]any{"sseConfig": map[string]any{"type": "KMS"}}), 400, "ValidationException")
	expect(t, h.rest("omics", "PATCH", "/sequencestore/"+id, map[string]any{"fallbackLocation": "elsewhere"}), 400, "ValidationException")

	list := h.rest("omics", "POST", "/sequencestores", map[string]any{})
	expect(t, list, 200, "")
	if items := list.body["sequenceStores"].([]any); len(items) != 2 || items[0].(map[string]any)["id"] != id {
		t.Fatalf("list %v", list.body)
	}
	byName := h.rest("omics", "POST", "/sequencestores", map[string]any{"filter": map[string]any{"name": "reads", "status": "CREATING"}})
	if items := byName.body["sequenceStores"].([]any); len(items) != 1 {
		t.Fatalf("filter %v", byName.body)
	}
	if items := h.rest("omics", "POST", "/sequencestores", map[string]any{"filter": map[string]any{"status": "ACTIVE"}}).body["sequenceStores"].([]any); len(items) != 0 {
		t.Fatalf("status filter %v", items)
	}
	expect(t, h.rest("omics", "POST", "/sequencestores", map[string]any{"filter": map[string]any{"createdAfter": "2026-01-01T00:00:00Z"}}), 400, "ValidationException")
	paged := h.rest("omics", "POST", "/sequencestores?maxResults=1", map[string]any{})
	if len(paged.body["sequenceStores"].([]any)) != 1 || str(paged.body["nextToken"]) == "" {
		t.Fatalf("page %v", paged.body)
	}
	tagPath := "/tags/" + url.PathEscape(arn)
	if tags := h.rest("omics", "GET", tagPath, nil).body["tags"].(map[string]any); tags["team"] != "a" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("omics", "DELETE", "/sequencestore/"+id, nil), 200, "")
	expect(t, h.rest("omics", "GET", "/sequencestore/"+id, nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("omics", "GET", tagPath, nil), 404, "ResourceNotFoundException")

	reference := h.rest("omics", "POST", "/referencestore", map[string]any{"name": "genomes", "description": "d", "clientToken": "rs-1", "sseConfig": map[string]any{"type": "KMS"}, "tags": map[string]any{"team": "a"}})
	expect(t, reference, 200, "")
	referenceID := str(reference.body["id"])
	if reference.body["arn"] != "arn:aws:omics:us-east-1:000000000000:referenceStore/"+referenceID || reference.body["creationTime"] == nil {
		t.Fatalf("reference store %v", reference.body)
	}
	if got := h.rest("omics", "GET", "/referencestore/"+referenceID, nil).body; got["name"] != "genomes" || got["description"] != "d" {
		t.Fatalf("reference store %v", got)
	}
	listed := h.rest("omics", "POST", "/referencestores", map[string]any{"filter": map[string]any{"name": "genomes"}})
	if items := listed.body["referenceStores"].([]any); len(items) != 1 {
		t.Fatalf("reference list %v", listed.body)
	}
	if items := h.rest("omics", "POST", "/referencestores", map[string]any{"filter": map[string]any{"name": "none"}}).body["referenceStores"].([]any); len(items) != 0 {
		t.Fatalf("reference name filter %v", items)
	}
	expect(t, h.rest("omics", "POST", "/referencestores", map[string]any{"filter": map[string]any{"createdBefore": "2026-01-01T00:00:00Z"}}), 400, "ValidationException")
	expect(t, h.rest("omics", "DELETE", "/referencestore/"+referenceID, nil), 200, "")
	expect(t, h.rest("omics", "GET", "/referencestore/"+referenceID, nil), 404, "ResourceNotFoundException")
}

func TestOmicsLimitsAndUnboundOperations(t *testing.T) {
	h := healthHarness(t)
	for i := 0; i < 5; i++ {
		expect(t, h.rest("omics", "POST", "/referencestore", map[string]any{"name": "r"}), 200, "")
	}
	expect(t, h.rest("omics", "POST", "/referencestore", map[string]any{"name": "r"}), 402, "ServiceQuotaExceededException")
	expect(t, h.rest("omics", "GET", "/runGroup/123", nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("omics", "GET", "/runGroup/abc", nil), 400, "ValidationException")
	for _, call := range []struct{ method, path string }{{"POST", "/workflow"}, {"POST", "/run"}, {"POST", "/annotationStore"}, {"POST", "/variantStore"}, {"POST", "/sequencestore/1234567890/importjob"}, {"POST", "/sequencestore/1234567890/readset/batch/delete"}} {
		expect(t, h.rest("omics", call.method, call.path, map[string]any{}), 501, "UnknownOperationException")
	}
}

// healthLakeSMART is the official SMART on FHIR authorization strategy value.
const healthLakeSMART = "SMART_ON_FHIR_V1"
