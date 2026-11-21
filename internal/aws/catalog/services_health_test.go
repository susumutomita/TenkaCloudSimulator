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
	return newHarness(t, healthImaging, healthLake, omics, keyspaces, grafana)
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

func TestKeyspacesCQLTypes(t *testing.T) {
	cases := map[string]bool{
		"text": true, "INT": true, " uuid ": true, "list<text>": true, "set<int>": true, "map<text, int>": true, "tuple<int, text, boolean>": true,
		"frozen<address>": true, "list<frozen<address>>": true, "map<text, frozen<list<int>>>": true, "frozen<map<text,int>>": true,
		"address": false, "list<address>": false, "string": false, "": false, "list<>": false, "list<text": false, "list<text>>": false,
		"map<text>": false, "list<text,int>": false, "frozen<int,int>": false, "vector<float>": false, "list<unknown>": false,
	}
	for text, want := range cases {
		if got := keyspacesCQLType(text, false); got != want {
			t.Fatalf("%q: want %v", text, want)
		}
	}
}

func TestKeyspacesCheckHooksApplyDocumentedRules(t *testing.T) {
	column := func(name, kind string) any { return map[string]any{"name": name, "type": kind} }
	named := func(names ...string) []any {
		out := []any{}
		for _, name := range names {
			out = append(out, map[string]any{"name": name})
		}
		return out
	}
	schema := func(mutate func(map[string]any)) map[string]any {
		s := map[string]any{"allColumns": []any{column("id", "uuid"), column("Name", "text"), column("tags", "set<text>"), column("note", "text")},
			"partitionKeys": named("id"), "clusteringKeys": named("name"), "staticColumns": named("note")}
		if mutate != nil {
			mutate(s)
		}
		return s
	}
	cases := []struct {
		name   string
		schema map[string]any
		ok     bool
	}{
		{"valid", schema(nil), true},
		{"no partition key", schema(func(s map[string]any) { s["partitionKeys"] = []any{} }), false},
		{"duplicate column", schema(func(s map[string]any) { s["allColumns"] = append(s["allColumns"].([]any), column("ID", "int")) }), false},
		{"bad type", schema(func(s map[string]any) { s["allColumns"] = append(s["allColumns"].([]any), column("x", "string")) }), false},
		{"unknown partition key", schema(func(s map[string]any) { s["partitionKeys"] = named("missing") }), false},
		{"unknown clustering key", schema(func(s map[string]any) { s["clusteringKeys"] = named("missing") }), false},
		{"unknown static column", schema(func(s map[string]any) { s["staticColumns"] = named("missing") }), false},
		{"key twice", schema(func(s map[string]any) { s["clusteringKeys"] = named("id") }), false},
		{"static key", schema(func(s map[string]any) { s["staticColumns"] = named("id") }), false},
		{"no clustering or static", schema(func(s map[string]any) { delete(s, "clusteringKeys"); delete(s, "staticColumns") }), true},
	}
	for _, c := range cases {
		err := keyspacesCreateTable(map[string]any{"schemaDefinition": c.schema})
		var f *fault
		if c.ok && err != nil || !c.ok && (!errors.As(err, &f) || f.code != "ValidationException") {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	options := []struct {
		name  string
		input map[string]any
		ok    bool
	}{
		{"on demand", map[string]any{"capacitySpecification": map[string]any{"throughputMode": "PAY_PER_REQUEST"}}, true},
		{"on demand with units", map[string]any{"capacitySpecification": map[string]any{"throughputMode": "PAY_PER_REQUEST", "readCapacityUnits": int64(1)}}, false},
		{"on demand with write units", map[string]any{"capacitySpecification": map[string]any{"throughputMode": "PAY_PER_REQUEST", "writeCapacityUnits": int64(1)}}, false},
		{"provisioned", map[string]any{"capacitySpecification": map[string]any{"throughputMode": "PROVISIONED", "readCapacityUnits": int64(1), "writeCapacityUnits": int64(1)}}, true},
		{"provisioned without write", map[string]any{"capacitySpecification": map[string]any{"throughputMode": "PROVISIONED", "readCapacityUnits": int64(1)}}, false},
		{"provisioned without units", map[string]any{"capacitySpecification": map[string]any{"throughputMode": "PROVISIONED"}}, false},
		{"owned key", map[string]any{"encryptionSpecification": map[string]any{"type": "AWS_OWNED_KMS_KEY"}}, true},
		{"owned key with identifier", map[string]any{"encryptionSpecification": map[string]any{"type": "AWS_OWNED_KMS_KEY", "kmsKeyIdentifier": "arn"}}, false},
		{"customer key", map[string]any{"encryptionSpecification": map[string]any{"type": "CUSTOMER_MANAGED_KMS_KEY", "kmsKeyIdentifier": "arn"}}, true},
		{"customer key without identifier", map[string]any{"encryptionSpecification": map[string]any{"type": "CUSTOMER_MANAGED_KMS_KEY"}}, false},
		{"replicas", map[string]any{"replicaSpecifications": []any{}}, false},
		{"change stream", map[string]any{"cdcSpecification": map[string]any{"status": "ENABLED"}}, false},
		{"nothing", map[string]any{}, true},
	}
	for _, c := range options {
		err := keyspacesTableOptions(c.input)
		var f *fault
		if c.ok && err != nil || !c.ok && (!errors.As(err, &f) || f.code != "ValidationException") {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	if err := keyspacesCreateTable(map[string]any{"schemaDefinition": schema(nil), "cdcSpecification": map[string]any{}}); err == nil {
		t.Fatal("create refuses change streams")
	}
	if err := keyspacesUpdateTable(map[string]any{"addColumns": []any{}}); err == nil {
		t.Fatal("update refuses added columns")
	}
	if err := keyspacesUpdateTable(map[string]any{"ttl": map[string]any{"status": "ENABLED"}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"system", "System_Schema", "system_schema_mcs", "system_multiregion_info"} {
		var f *fault
		if err := keyspacesCreateKeyspace(map[string]any{"keyspaceName": name}); !errors.As(err, &f) || f.code != "ConflictException" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, spec := range []map[string]any{{"replicationStrategy": "MULTI_REGION", "regionList": []any{"us-east-1", "us-west-2"}}, {"replicationStrategy": "SINGLE_REGION", "regionList": []any{"us-east-1"}}} {
		var f *fault
		if err := keyspacesCreateKeyspace(map[string]any{"keyspaceName": "ks", "replicationSpecification": spec}); !errors.As(err, &f) || f.code != "ValidationException" {
			t.Fatalf("%v: %v", spec, err)
		}
	}
	if err := keyspacesCreateKeyspace(map[string]any{"keyspaceName": "ks", "replicationSpecification": map[string]any{"replicationStrategy": "SINGLE_REGION"}}); err != nil {
		t.Fatal(err)
	}
	typeCases := []struct {
		name  string
		input map[string]any
		ok    bool
	}{
		{"valid", map[string]any{"typeName": "Address", "fieldDefinitions": []any{column("Street", "text"), column("zip", "int")}}, true},
		{"quoted", map[string]any{"typeName": `"Address"`, "fieldDefinitions": []any{column("street", "text")}}, false},
		{"digit first", map[string]any{"typeName": "1address", "fieldDefinitions": []any{column("street", "text")}}, false},
		{"no fields", map[string]any{"typeName": "address", "fieldDefinitions": []any{}}, false},
		{"duplicate fields", map[string]any{"typeName": "address", "fieldDefinitions": []any{column("a", "text"), column("A", "int")}}, false},
		{"bad field type", map[string]any{"typeName": "address", "fieldDefinitions": []any{column("a", "string")}}, false},
	}
	for _, c := range typeCases {
		err := keyspacesCreateType(c.input)
		var f *fault
		if c.ok && err != nil || !c.ok && (!errors.As(err, &f) || f.code != "ValidationException") {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	named2 := map[string]any{"typeName": "Mixed"}
	if err := keyspacesTypeName(named2); err != nil || named2["typeName"] != "mixed" {
		t.Fatalf("lowercased %v %v", named2, err)
	}
}

func TestKeyspacesLifecycle(t *testing.T) {
	h := healthHarness(t)
	call := func(op string, input map[string]any) reply { return h.json1("keyspaces", op, input) }
	ksARN := "arn:aws:cassandra:us-east-1:000000000000:/keyspace/Shop/"
	created := call("CreateKeyspace", map[string]any{"keyspaceName": "Shop", "tags": []any{map[string]any{"key": "team", "value": "a"}}})
	expect(t, created, 200, "")
	if created.body["resourceArn"] != ksARN {
		t.Fatalf("created %v", created.body)
	}
	expect(t, call("CreateKeyspace", map[string]any{"keyspaceName": "Shop"}), 400, "ConflictException")
	expect(t, call("CreateKeyspace", map[string]any{"keyspaceName": "system"}), 400, "ConflictException")
	expect(t, call("CreateKeyspace", map[string]any{"keyspaceName": "_bad"}), 400, "ValidationException")
	expect(t, call("CreateKeyspace", map[string]any{"keyspaceName": "multi", "replicationSpecification": map[string]any{"replicationStrategy": "MULTI_REGION", "regionList": []any{"us-east-1", "eu-west-1"}}}), 400, "ValidationException")
	expect(t, call("CreateKeyspace", map[string]any{"keyspaceName": "single", "replicationSpecification": map[string]any{"replicationStrategy": "SINGLE_REGION"}}), 200, "")

	got := call("GetKeyspace", map[string]any{"keyspaceName": "Shop"})
	expect(t, got, 200, "")
	if got.body["keyspaceName"] != "Shop" || got.body["resourceArn"] != ksARN || got.body["replicationStrategy"] != "SINGLE_REGION" {
		t.Fatalf("keyspace %v", got.body)
	}
	if _, present := got.body["replicationGroupStatuses"]; present {
		t.Fatal("no replication status is reported")
	}
	list := call("ListKeyspaces", map[string]any{})
	if items := list.body["keyspaces"].([]any); len(items) != 2 || items[0].(map[string]any)["keyspaceName"] != "Shop" || items[0].(map[string]any)["replicationStrategy"] != "SINGLE_REGION" {
		t.Fatalf("keyspaces %v", list.body)
	}
	paged := call("ListKeyspaces", map[string]any{"maxResults": 1})
	if len(paged.body["keyspaces"].([]any)) != 1 || str(paged.body["nextToken"]) == "" {
		t.Fatalf("page %v", paged.body)
	}
	expect(t, call("GetKeyspace", map[string]any{"keyspaceName": "nothing"}), 400, "ResourceNotFoundException")

	schema := map[string]any{"allColumns": []any{map[string]any{"name": "id", "type": "uuid"}, map[string]any{"name": "title", "type": "text"}},
		"partitionKeys": []any{map[string]any{"name": "id"}}, "clusteringKeys": []any{map[string]any{"name": "title", "orderBy": "DESC"}}}
	tableARN := "arn:aws:cassandra:us-east-1:000000000000:/keyspace/Shop/table/orders"
	table := call("CreateTable", map[string]any{"keyspaceName": "Shop", "tableName": "orders", "schemaDefinition": schema, "comment": map[string]any{"message": "orders"},
		"tags": []any{map[string]any{"key": "stage", "value": "dev"}}, "ttl": map[string]any{"status": "ENABLED"}, "defaultTimeToLive": 3600})
	expect(t, table, 200, "")
	if table.body["resourceArn"] != tableARN {
		t.Fatalf("table %v", table.body)
	}
	expect(t, call("CreateTable", map[string]any{"keyspaceName": "Shop", "tableName": "orders", "schemaDefinition": schema}), 400, "ConflictException")
	expect(t, call("CreateTable", map[string]any{"keyspaceName": "missing", "tableName": "orders", "schemaDefinition": schema}), 400, "ResourceNotFoundException")
	expect(t, call("CreateTable", map[string]any{"keyspaceName": "Shop", "tableName": "other", "schemaDefinition": map[string]any{"allColumns": []any{}, "partitionKeys": []any{}}}), 400, "ValidationException")
	expect(t, call("CreateTable", map[string]any{"keyspaceName": "Shop", "tableName": "auto", "schemaDefinition": schema, "autoScalingSpecification": map[string]any{}}), 400, "ValidationException")

	info := call("GetTable", map[string]any{"keyspaceName": "Shop", "tableName": "orders"})
	expect(t, info, 200, "")
	if info.body["status"] != "ACTIVE" || info.body["keyspaceName"] != "Shop" || info.body["resourceArn"] != tableARN || info.body["creationTimestamp"] == nil ||
		info.body["capacitySpecification"].(map[string]any)["throughputMode"] != "PAY_PER_REQUEST" ||
		info.body["encryptionSpecification"].(map[string]any)["type"] != "AWS_OWNED_KMS_KEY" ||
		info.body["pointInTimeRecovery"].(map[string]any)["status"] != "DISABLED" || info.body["defaultTimeToLive"] != json.Number("3600") ||
		info.body["schemaDefinition"].(map[string]any)["clusteringKeys"].([]any)[0].(map[string]any)["orderBy"] != "DESC" {
		t.Fatalf("table %v", info.body)
	}
	tables := call("ListTables", map[string]any{"keyspaceName": "Shop"})
	if items := tables.body["tables"].([]any); len(items) != 1 || items[0].(map[string]any)["tableName"] != "orders" || items[0].(map[string]any)["resourceArn"] != tableARN {
		t.Fatalf("tables %v", tables.body)
	}
	expect(t, call("ListTables", map[string]any{"keyspaceName": "single"}), 200, "")
	expect(t, call("ListTables", map[string]any{"keyspaceName": "missing"}), 400, "ResourceNotFoundException")

	updated := call("UpdateTable", map[string]any{"keyspaceName": "Shop", "tableName": "orders", "capacitySpecification": map[string]any{"throughputMode": "PROVISIONED", "readCapacityUnits": 5, "writeCapacityUnits": 5},
		"pointInTimeRecovery": map[string]any{"status": "ENABLED"}, "clientSideTimestamps": map[string]any{"status": "ENABLED"}, "defaultTimeToLive": 60})
	expect(t, updated, 200, "")
	if updated.body["resourceArn"] != tableARN {
		t.Fatalf("updated %v", updated.body)
	}
	after := call("GetTable", map[string]any{"keyspaceName": "Shop", "tableName": "orders"}).body
	if after["capacitySpecification"].(map[string]any)["readCapacityUnits"] != json.Number("5") || after["pointInTimeRecovery"].(map[string]any)["status"] != "ENABLED" ||
		after["defaultTimeToLive"] != json.Number("60") || after["clientSideTimestamps"].(map[string]any)["status"] != "ENABLED" {
		t.Fatalf("updated table %v", after)
	}
	expect(t, call("UpdateTable", map[string]any{"keyspaceName": "Shop", "tableName": "orders", "addColumns": []any{map[string]any{"name": "x", "type": "int"}}}), 400, "ValidationException")
	expect(t, call("UpdateTable", map[string]any{"keyspaceName": "Shop", "tableName": "orders", "capacitySpecification": map[string]any{"throughputMode": "PROVISIONED"}}), 400, "ValidationException")
	expect(t, call("UpdateTable", map[string]any{"keyspaceName": "Shop", "tableName": "orders", "cdcSpecification": map[string]any{"status": "ENABLED"}}), 400, "ValidationException")
	expect(t, call("UpdateTable", map[string]any{"keyspaceName": "Shop", "tableName": "orders", "comment": map[string]any{"message": "x"}}), 400, "ValidationException")
	expect(t, call("UpdateTable", map[string]any{"keyspaceName": "Shop", "tableName": "none", "ttl": map[string]any{"status": "ENABLED"}}), 400, "ResourceNotFoundException")

	typeARN := "arn:aws:cassandra:us-east-1:000000000000:/keyspace/Shop/type/address"
	made := call("CreateType", map[string]any{"keyspaceName": "Shop", "typeName": "Address", "fieldDefinitions": []any{map[string]any{"name": "street", "type": "text"}, map[string]any{"name": "zip", "type": "int"}}})
	expect(t, made, 200, "")
	if made.body["typeName"] != "address" || made.body["keyspaceArn"] != ksARN {
		t.Fatalf("type %v", made.body)
	}
	expect(t, call("CreateType", map[string]any{"keyspaceName": "Shop", "typeName": "address", "fieldDefinitions": []any{map[string]any{"name": "a", "type": "text"}}}), 400, "ConflictException")
	described := call("GetType", map[string]any{"keyspaceName": "Shop", "typeName": "ADDRESS"})
	expect(t, described, 200, "")
	if described.body["status"] != "ACTIVE" || described.body["keyspaceArn"] != ksARN || len(described.body["fieldDefinitions"].([]any)) != 2 || len(described.body["directReferringTables"].([]any)) != 0 {
		t.Fatalf("type %v", described.body)
	}
	if types := call("ListTypes", map[string]any{"keyspaceName": "Shop"}).body["types"].([]any); len(types) != 1 || types[0] != "address" {
		t.Fatalf("types %v", types)
	}

	expect(t, call("TagResource", map[string]any{"resourceArn": typeARN, "tags": []any{map[string]any{"key": "owner", "value": "me"}}}), 200, "")
	tags := call("ListTagsForResource", map[string]any{"resourceArn": typeARN}).body["tags"].([]any)
	if len(tags) != 1 || tags[0].(map[string]any)["key"] != "owner" {
		t.Fatalf("type tags %v", tags)
	}
	if tags := call("ListTagsForResource", map[string]any{"resourceArn": tableARN}).body["tags"].([]any); len(tags) != 1 || tags[0].(map[string]any)["value"] != "dev" {
		t.Fatalf("table tags %v", tags)
	}
	expect(t, call("ListTagsForResource", map[string]any{"resourceArn": ksARN, "maxResults": 1}), 400, "ValidationException")
	expect(t, call("ListTagsForResource", map[string]any{"resourceArn": "arn:aws:cassandra:us-east-1:000000000000:/keyspace/Shop/table/none"}), 400, "ResourceNotFoundException")

	expect(t, call("DeleteKeyspace", map[string]any{"keyspaceName": "Shop"}), 200, "")
	expect(t, call("GetTable", map[string]any{"keyspaceName": "Shop", "tableName": "orders"}), 400, "ResourceNotFoundException")
	expect(t, call("GetType", map[string]any{"keyspaceName": "Shop", "typeName": "address"}), 400, "ResourceNotFoundException")
	expect(t, call("DeleteKeyspace", map[string]any{"keyspaceName": "Shop"}), 400, "ResourceNotFoundException")
	expect(t, call("CreateKeyspace", map[string]any{"keyspaceName": "Shop"}), 200, "")
	expect(t, call("CreateTable", map[string]any{"keyspaceName": "Shop", "tableName": "orders", "schemaDefinition": schema}), 200, "")
	expect(t, call("CreateType", map[string]any{"keyspaceName": "Shop", "typeName": "Pair", "fieldDefinitions": []any{map[string]any{"name": "a", "type": "frozen<address>"}}}), 200, "")
	expect(t, call("DeleteType", map[string]any{"keyspaceName": "Shop", "typeName": "PAIR"}), 200, "")
	expect(t, call("DeleteType", map[string]any{"keyspaceName": "Shop", "typeName": "pair"}), 400, "ResourceNotFoundException")
	deleted := call("DeleteTable", map[string]any{"keyspaceName": "Shop", "tableName": "orders"})
	expect(t, deleted, 200, "")
	expect(t, call("DeleteTable", map[string]any{"keyspaceName": "Shop", "tableName": "orders"}), 400, "ResourceNotFoundException")
}

func TestKeyspacesLimitsAndUnboundOperations(t *testing.T) {
	h := healthHarness(t)
	call := func(op string, input map[string]any) reply { return h.json1("keyspaces", op, input) }
	for i := 0; i < 50; i++ {
		expect(t, call("CreateKeyspace", map[string]any{"keyspaceName": "ks" + strings.Repeat("a", i/26) + string(rune('a'+i%26))}), 200, "")
	}
	expect(t, call("CreateKeyspace", map[string]any{"keyspaceName": "over"}), 400, "ServiceQuotaExceededException")
	for _, op := range []string{"UpdateKeyspace", "UntagResource", "RestoreTable", "GetTableAutoScalingSettings"} {
		expect(t, call(op, map[string]any{"keyspaceName": "ksa"}), 501, "UnknownOperationException")
	}
}

func TestGrafanaCheckHooksApplyDocumentedRules(t *testing.T) {
	role := "arn:aws:iam::000000000000:role/grafana"
	create := func(mutate func(map[string]any)) map[string]any {
		input := map[string]any{"accountAccessType": "CURRENT_ACCOUNT", "authenticationProviders": []any{"AWS_SSO"}, "permissionType": "CUSTOMER_MANAGED", "workspaceRoleArn": role}
		if mutate != nil {
			mutate(input)
		}
		return input
	}
	cases := []struct {
		name  string
		input map[string]any
		ok    bool
	}{
		{"valid", create(nil), true},
		{"version", create(func(m map[string]any) { m["grafanaVersion"] = "10.4" }), true},
		{"patch version", create(func(m map[string]any) { m["grafanaVersion"] = "9.4.7" }), true},
		{"bad version", create(func(m map[string]any) { m["grafanaVersion"] = "latest" }), false},
		{"service managed", create(func(m map[string]any) { m["permissionType"] = "SERVICE_MANAGED" }), false},
		{"no role", create(func(m map[string]any) { delete(m, "workspaceRoleArn") }), false},
		{"user as role", create(func(m map[string]any) { m["workspaceRoleArn"] = "arn:aws:iam::000000000000:user/u" }), false},
		{"organization without units", create(func(m map[string]any) { m["accountAccessType"] = "ORGANIZATION" }), false},
		{"organization", create(func(m map[string]any) {
			m["accountAccessType"] = "ORGANIZATION"
			m["workspaceOrganizationalUnits"] = []any{"ou-1234-abcdefgh"}
		}), true},
		{"prefix list", create(func(m map[string]any) {
			m["networkAccessControl"] = map[string]any{"prefixListIds": []any{"pl-1a2b3c4d", "pl-0123456789abcdef0"}, "vpceIds": []any{}}
		}), true},
		{"bad prefix list", create(func(m map[string]any) { m["networkAccessControl"] = map[string]any{"prefixListIds": []any{"cidr"}} }), false},
		{"vpc", create(func(m map[string]any) {
			m["vpcConfiguration"] = map[string]any{"subnetIds": []any{"subnet-1", "subnet-2"}, "securityGroupIds": []any{"sg-1"}}
		}), true},
		{"duplicate subnets", create(func(m map[string]any) {
			m["vpcConfiguration"] = map[string]any{"subnetIds": []any{"subnet-1", "subnet-1"}, "securityGroupIds": []any{"sg-1"}}
		}), false},
		{"duplicate security groups", create(func(m map[string]any) {
			m["vpcConfiguration"] = map[string]any{"subnetIds": []any{"subnet-1", "subnet-2"}, "securityGroupIds": []any{"sg-1", "sg-1"}}
		}), false},
	}
	for _, c := range cases {
		err := grafanaCreate(c.input)
		var f *fault
		if c.ok && err != nil || !c.ok && (!errors.As(err, &f) || f.code != "ValidationException") {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	both := create(func(m map[string]any) { m["authenticationProviders"] = []any{"AWS_SSO", "SAML"} })
	if err := grafanaCreate(both); err != nil || both["authenticationProviders"] != nil {
		t.Fatalf("%v %v", both, err)
	} else if auth := both["authentication"].(map[string]any); auth["samlConfigurationStatus"] != "NOT_CONFIGURED" || len(auth["providers"].([]any)) != 2 {
		t.Fatalf("authentication %v", auth)
	}
	single := create(nil)
	if err := grafanaCreate(single); err != nil || single["authentication"].(map[string]any)["samlConfigurationStatus"] != nil {
		t.Fatalf("%v %v", single, err)
	}
	flags := map[string]any{"removeVpcConfiguration": false, "removeNetworkAccessConfiguration": true}
	if err := grafanaUpdate(flags); err != nil || flags["removeVpcConfiguration"] != nil || flags["removeNetworkAccessConfiguration"] != true {
		t.Fatalf("flags %v %v", flags, err)
	}
	var f *fault
	if err := grafanaUpdate(map[string]any{"removeVpcConfiguration": true, "vpcConfiguration": map[string]any{}}); !errors.As(err, &f) || f.code != "ValidationException" {
		t.Fatalf("combined flag: %v", err)
	}
	if err := grafanaUpdate(map[string]any{"permissionType": "SERVICE_MANAGED"}); !errors.As(err, &f) {
		t.Fatalf("service managed update: %v", err)
	}
	document := map[string]any{"vpcConfiguration": true, "networkAccessControl": map[string]any{}, "accountAccessType": "CURRENT_ACCOUNT"}
	if err := grafanaMerged(document, map[string]any{"removeVpcConfiguration": true, "removeNetworkAccessConfiguration": false}); err != nil || document["vpcConfiguration"] != nil || document["networkAccessControl"] == nil {
		t.Fatalf("merged %v %v", document, err)
	}
	if err := grafanaMerged(map[string]any{"accountAccessType": "ORGANIZATION"}, map[string]any{}); !errors.As(err, &f) {
		t.Fatalf("organization without units: %v", err)
	}
	if err := grafanaMerged(map[string]any{"accountAccessType": "ORGANIZATION", "organizationalUnits": []any{"ou"}}, map[string]any{}); err != nil {
		t.Fatal(err)
	}
}

func TestGrafanaWorkspaceLifecycle(t *testing.T) {
	h := healthHarness(t)
	role := "arn:aws:iam::000000000000:role/grafana"
	body := map[string]any{"accountAccessType": "CURRENT_ACCOUNT", "authenticationProviders": []any{"AWS_SSO", "SAML"}, "permissionType": "CUSTOMER_MANAGED", "workspaceRoleArn": role,
		"workspaceName": "ops", "workspaceDescription": "dashboards", "clientToken": "token-1", "tags": map[string]any{"team": "a"},
		"workspaceDataSources": []any{"CLOUDWATCH"}, "workspaceNotificationDestinations": []any{"SNS"},
		"vpcConfiguration":     map[string]any{"subnetIds": []any{"subnet-1", "subnet-2"}, "securityGroupIds": []any{"sg-1"}},
		"networkAccessControl": map[string]any{"prefixListIds": []any{"pl-1a2b3c4d"}, "vpceIds": []any{"vpce-1"}}}
	created := h.rest("grafana", "POST", "/workspaces", body)
	expect(t, created, 202, "")
	ws := created.body["workspace"].(map[string]any)
	id := str(ws["id"])
	if len(id) != 12 || !strings.HasPrefix(id, "g-") || ws["status"] != "ACTIVE" || ws["grafanaVersion"] != "10.4" || ws["name"] != "ops" || ws["description"] != "dashboards" ||
		ws["created"] == nil || ws["tags"].(map[string]any)["team"] != "a" || ws["workspaceRoleArn"] != role {
		t.Fatalf("created %v", ws)
	}
	for _, member := range []string{"endpoint", "grafanaToken", "licenseType", "freeTrialExpiration"} {
		if _, present := ws[member]; present {
			t.Fatalf("%s must not be reported", member)
		}
	}
	auth := ws["authentication"].(map[string]any)
	if auth["samlConfigurationStatus"] != "NOT_CONFIGURED" || len(auth["providers"].([]any)) != 2 || ws["dataSources"].([]any)[0] != "CLOUDWATCH" || ws["notificationDestinations"].([]any)[0] != "SNS" {
		t.Fatalf("created members %v", ws)
	}
	if replay := h.rest("grafana", "POST", "/workspaces", body); replay.body["workspace"].(map[string]any)["id"] != id {
		t.Fatalf("replay %v", replay.body)
	}
	body["workspaceName"] = "changed"
	expect(t, h.rest("grafana", "POST", "/workspaces", body), 409, "ConflictException")
	expect(t, h.rest("grafana", "POST", "/workspaces", map[string]any{"accountAccessType": "CURRENT_ACCOUNT", "authenticationProviders": []any{"AWS_SSO"}, "permissionType": "CUSTOMER_MANAGED", "workspaceRoleArn": role, "configuration": "{}"}), 400, "ValidationException")
	expect(t, h.rest("grafana", "POST", "/workspaces", map[string]any{"accountAccessType": "CURRENT_ACCOUNT", "authenticationProviders": []any{"AWS_SSO"}, "permissionType": "SERVICE_MANAGED"}), 400, "ValidationException")
	expect(t, h.rest("grafana", "POST", "/workspaces", map[string]any{"accountAccessType": "CURRENT_ACCOUNT", "authenticationProviders": []any{"AWS_SSO"}, "permissionType": "CUSTOMER_MANAGED", "workspaceRoleArn": role, "workspaceName": "bad name"}), 400, "ValidationException")
	expect(t, h.rest("grafana", "POST", "/workspaces", map[string]any{"accountAccessType": "CURRENT_ACCOUNT", "permissionType": "CUSTOMER_MANAGED", "workspaceRoleArn": role}), 400, "ValidationException")
	plain := h.rest("grafana", "POST", "/workspaces", map[string]any{"accountAccessType": "CURRENT_ACCOUNT", "authenticationProviders": []any{"AWS_SSO"}, "permissionType": "CUSTOMER_MANAGED", "workspaceRoleArn": role, "grafanaVersion": "9.4"})
	expect(t, plain, 202, "")
	plainWS := plain.body["workspace"].(map[string]any)
	if plainWS["grafanaVersion"] != "9.4" || plainWS["dataSources"] == nil || plainWS["authentication"].(map[string]any)["samlConfigurationStatus"] != nil {
		t.Fatalf("plain %v", plainWS)
	}

	got := h.rest("grafana", "GET", "/workspaces/"+id, nil)
	expect(t, got, 200, "")
	if got.body["workspace"].(map[string]any)["id"] != id || got.body["workspace"].(map[string]any)["vpcConfiguration"] == nil {
		t.Fatalf("described %v", got.body)
	}
	list := h.rest("grafana", "GET", "/workspaces", nil)
	if items := list.body["workspaces"].([]any); len(items) != 2 || items[0].(map[string]any)["id"] != id || items[0].(map[string]any)["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("list %v", list.body)
	}
	paged := h.rest("grafana", "GET", "/workspaces?maxResults=1", nil)
	if len(paged.body["workspaces"].([]any)) != 1 || str(paged.body["nextToken"]) == "" {
		t.Fatalf("page %v", paged.body)
	}

	h.now = h.now.Add(time.Hour)
	updated := h.rest("grafana", "PUT", "/workspaces/"+id, map[string]any{"workspaceName": "renamed", "workspaceDescription": "new", "removeVpcConfiguration": true, "removeNetworkAccessConfiguration": false,
		"workspaceNotificationDestinations": []any{"SNS"}})
	expect(t, updated, 202, "")
	after := updated.body["workspace"].(map[string]any)
	if after["name"] != "renamed" || after["description"] != "new" || after["vpcConfiguration"] != nil || after["networkAccessControl"] == nil || after["modified"] == after["created"] {
		t.Fatalf("updated %v", after)
	}
	expect(t, h.rest("grafana", "PUT", "/workspaces/"+id, map[string]any{"vpcConfiguration": map[string]any{"subnetIds": []any{"subnet-1", "subnet-2"}, "securityGroupIds": []any{"sg-1"}}, "removeVpcConfiguration": true}), 400, "ValidationException")
	expect(t, h.rest("grafana", "PUT", "/workspaces/"+id, map[string]any{"accountAccessType": "ORGANIZATION"}), 400, "ValidationException")
	expect(t, h.rest("grafana", "PUT", "/workspaces/"+id, map[string]any{"permissionType": "SERVICE_MANAGED"}), 400, "ValidationException")
	expect(t, h.rest("grafana", "PUT", "/workspaces/"+id, map[string]any{"grafanaVersion": "10.4"}), 400, "ValidationException")
	expect(t, h.rest("grafana", "PUT", "/workspaces/g-0123456789", map[string]any{"workspaceName": "x"}), 404, "ResourceNotFoundException")
	organization := h.rest("grafana", "PUT", "/workspaces/"+id, map[string]any{"accountAccessType": "ORGANIZATION", "workspaceOrganizationalUnits": []any{"ou-1234-abcdefgh"}})
	expect(t, organization, 202, "")
	if units := organization.body["workspace"].(map[string]any)["organizationalUnits"].([]any); len(units) != 1 {
		t.Fatalf("organization %v", organization.body)
	}

	arn := "arn:aws:grafana:us-east-1:000000000000:/workspaces/" + id
	tagPath := "/tags/" + url.PathEscape(arn)
	expect(t, h.rest("grafana", "POST", tagPath, map[string]any{"tags": map[string]any{"stage": "dev"}}), 200, "")
	if tags := h.rest("grafana", "GET", tagPath, nil).body["tags"].(map[string]any); len(tags) != 2 || tags["stage"] != "dev" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("grafana", "DELETE", tagPath+"?tagKeys=stage", nil), 200, "")
	if tags := h.rest("grafana", "GET", tagPath, nil).body["tags"].(map[string]any); len(tags) != 1 {
		t.Fatalf("tags after untag %v", tags)
	}

	deleted := h.rest("grafana", "DELETE", "/workspaces/"+id, nil)
	expect(t, deleted, 202, "")
	if deleted.body["workspace"].(map[string]any)["status"] != "DELETING" {
		t.Fatalf("deleted %v", deleted.body)
	}
	expect(t, h.rest("grafana", "GET", "/workspaces/"+id, nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("grafana", "DELETE", "/workspaces/"+id, nil), 404, "ResourceNotFoundException")
}

func TestGrafanaLimitsAndUnboundOperations(t *testing.T) {
	h := healthHarness(t)
	role := "arn:aws:iam::000000000000:role/grafana"
	for i := 0; i < 10; i++ {
		expect(t, h.rest("grafana", "POST", "/workspaces", map[string]any{"accountAccessType": "CURRENT_ACCOUNT", "authenticationProviders": []any{"AWS_SSO"}, "permissionType": "CUSTOMER_MANAGED", "workspaceRoleArn": role}), 202, "")
	}
	expect(t, h.rest("grafana", "POST", "/workspaces", map[string]any{"accountAccessType": "CURRENT_ACCOUNT", "authenticationProviders": []any{"AWS_SSO"}, "permissionType": "CUSTOMER_MANAGED", "workspaceRoleArn": role}), 402, "ServiceQuotaExceededException")
	id := "g-0123456789"
	for _, call := range []struct{ method, path string }{
		{"POST", "/workspaces/" + id + "/apikeys"}, {"POST", "/workspaces/" + id + "/serviceaccounts"}, {"POST", "/workspaces/" + id + "/serviceaccounts/1/tokens"},
		{"GET", "/workspaces/" + id + "/authentication"}, {"GET", "/workspaces/" + id + "/configuration"}, {"PATCH", "/workspaces/" + id + "/permissions"},
		{"GET", "/versions"}, {"POST", "/workspaces/" + id + "/licenses/ENTERPRISE"},
	} {
		if r := h.rest("grafana", call.method, call.path, map[string]any{}); r.status != 501 || r.code() != "UnknownOperationException" {
			t.Fatalf("%s %s: %d %s", call.method, call.path, r.status, r.raw)
		}
	}
}

// healthLakeSMART is the official SMART on FHIR authorization strategy value.
const healthLakeSMART = "SMART_ON_FHIR_V1"
