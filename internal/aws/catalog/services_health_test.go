package catalog

import (
	"net/url"
	"strings"
	"testing"
)

func healthHarness(t *testing.T) *harness {
	return newHarness(t, healthImaging)
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
