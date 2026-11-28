package catalog

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestMSKConfigurationRevisionLifecycle(t *testing.T) {
	h := newHarness(t, msk)
	const collection = "/v1/configurations"
	props := base64.StdEncoding.EncodeToString([]byte("auto.create.topics.enable=false\n"))
	versions := []any{"3.6.0"}
	spec := map[string]any{"name": "practice", "serverProperties": props, "description": "first revision", "kafkaVersions": versions}
	created := h.rest("kafka", "POST", collection, spec)
	expect(t, created, 200, "")
	arn := str(created.body["arn"])
	if !strings.HasPrefix(arn, "arn:aws:kafka:us-east-1:000000000000:configuration/practice/") {
		t.Fatalf("unexpected configuration ARN %q", arn)
	}
	if !slices.Contains(h.policy.calls, "simulator-admin kafka:CreateConfiguration "+arn) {
		t.Fatalf("creation did not authorize the generated configuration ARN")
	}
	path := collection + "/" + url.PathEscape(arn)
	latest := map[string]any{"revision": json.Number("1"), "creationTime": created.body["creationTime"], "description": "first revision"}
	if created.body["state"] != "ACTIVE" || str(created.body["creationTime"]) == "" || !reflect.DeepEqual(created.body["latestRevision"], latest) || created.body["serverProperties"] != nil {
		t.Fatalf("incorrect creation metadata: %v", created.body)
	}
	get := func(path string) reply { return h.rest("kafka", "GET", path, nil) }
	meta := get(path)
	expect(t, meta, 200, "")
	if meta.body["name"] != "practice" || meta.body["description"] != "first revision" || !reflect.DeepEqual(meta.body["kafkaVersions"], versions) ||
		!reflect.DeepEqual(meta.body["latestRevision"], latest) || meta.body["serverProperties"] != nil {
		t.Fatalf("incorrect configuration metadata: %v", meta.body)
	}
	revision := get(path + "/revisions/1")
	expect(t, revision, 200, "")
	if revision.body["serverProperties"] != props || revision.body["revision"] != json.Number("1") || revision.body["arn"] != arn ||
		revision.body["creationTime"] != created.body["creationTime"] || revision.body["description"] != "first revision" {
		t.Fatalf("incorrect revision data: %v", revision.body)
	}
	expect(t, get(path+"/revisions/2"), 404, "NotFoundException")
	expect(t, get(collection+"/practice"), 400, "BadRequestException")
	for _, other := range []string{strings.Replace(arn, "us-east-1", "ap-northeast-1", 1), strings.Replace(arn, "000000000000", "111111111111", 1)} {
		expect(t, get(collection+"/"+url.PathEscape(other)), 404, "NotFoundException")
	}
	expect(t, get(collection+"/"+url.PathEscape(strings.Replace(arn, ":configuration/", ":cluster/", 1))), 400, "BadRequestException")
	expect(t, get(path+"/revisions"), 501, "UnknownOperationException")
	expect(t, h.rest("kafka", "PUT", path, map[string]any{"serverProperties": props}), 501, "UnknownOperationException")
	expect(t, get("/v1/tags/"+url.PathEscape(arn)), 400, "BadRequestException")
	expect(t, h.rest("kafka", "POST", "/v1/tags/"+url.PathEscape(arn), map[string]any{"tags": map[string]any{"label": "practice"}}), 400, "BadRequestException")
	expect(t, h.rest("kafka", "DELETE", "/v1/tags/"+url.PathEscape(arn)+"?tagKeys=label", nil), 400, "BadRequestException")

	h.policy.deny["kafka:DeleteConfiguration "+arn] = true
	expect(t, h.rest("kafka", "DELETE", path, nil), 403, "")
	delete(h.policy.deny, "kafka:DeleteConfiguration "+arn)
	inject(t, h.db, "CREATE TRIGGER msk_fail_delete BEFORE DELETE ON aws_catalog_resources BEGIN SELECT RAISE(ABORT,'test failure'); END")
	expect(t, h.rest("kafka", "DELETE", path, nil), 500, "")
	inject(t, h.db, "DROP TRIGGER msk_fail_delete")
	if after := get(path + "/revisions/1"); !reflect.DeepEqual(after.body, revision.body) {
		t.Fatalf("failed deletion changed the revision")
	}

	second := h.rest("kafka", "POST", collection, map[string]any{"name": "peer", "serverProperties": props})
	expect(t, second, 200, "")
	list := get(collection + "?maxResults=1")
	expect(t, list, 200, "")
	if !slices.Contains(h.policy.calls, "simulator-admin kafka:ListConfigurations *") {
		t.Fatal("list did not authorize the wildcard resource")
	}
	if items := list.body["configurations"].([]any); len(items) != 1 || !reflect.DeepEqual(items[0], meta.body) {
		t.Fatalf("incorrect first page: %v", list.body)
	}
	next := str(list.body["nextToken"])
	if next == "" {
		t.Fatal("missing second page token")
	}
	page := get(collection + "?maxResults=1&nextToken=" + url.QueryEscape(next))
	expect(t, page, 200, "")
	if items := page.body["configurations"].([]any); len(items) != 1 || items[0].(map[string]any)["arn"] != second.body["arn"] || page.body["nextToken"] != nil {
		t.Fatalf("incorrect second page: %v", page.body)
	}

	removed := h.rest("kafka", "DELETE", path, nil)
	expect(t, removed, 200, "")
	if removed.body["arn"] != arn || removed.body["state"] != "DELETING" {
		t.Fatalf("incorrect deletion acknowledgement: %v", removed.body)
	}
	expect(t, get(path), 404, "NotFoundException")
	expect(t, get(path+"/revisions/1"), 404, "NotFoundException")
	recreated := h.rest("kafka", "POST", collection, spec)
	expect(t, recreated, 200, "")
	if recreated.body["arn"] == arn {
		t.Fatal("recreated configuration reused deleted ARN")
	}
	expect(t, h.rest("kafka", "DELETE", path, nil), 404, "NotFoundException")
	for _, c := range []reply{second, recreated} {
		expect(t, h.rest("kafka", "DELETE", collection+"/"+url.PathEscape(str(c.body["arn"])), nil), 200, "")
	}
}

func TestMSKConfigurationOpaqueProperties(t *testing.T) {
	h := newHarness(t, msk)
	const collection = "/v1/configurations"
	properties := []byte{'a', 0, 0xff, '\n'}
	created := h.rest("kafka", "POST", collection, map[string]any{
		"name": "opaque", "serverProperties": base64.StdEncoding.EncodeToString(properties),
	})
	expect(t, created, 200, "")
	path := collection + "/" + url.PathEscape(str(created.body["arn"]))
	metadata := h.rest("kafka", "GET", path, nil)
	expect(t, metadata, 200, "")
	list := h.rest("kafka", "GET", collection, nil)
	expect(t, list, 200, "")
	items := list.body["configurations"].([]any)
	if len(items) != 1 || !reflect.DeepEqual(items[0], metadata.body) {
		t.Fatalf("unexpected configuration list %v", items)
	}
	for _, result := range []map[string]any{created.body, metadata.body} {
		for _, key := range []string{"description", "kafkaVersions", "serverProperties"} {
			if _, present := result[key]; present {
				t.Fatalf("unexpected %s in configuration metadata", key)
			}
		}
		latest := result["latestRevision"].(map[string]any)
		if _, present := latest["description"]; present {
			t.Fatal("unexpected description in latest revision")
		}
	}
	revision := h.rest("kafka", "GET", path+"/revisions/1", nil)
	expect(t, revision, 200, "")
	if _, present := revision.body["description"]; present {
		t.Fatal("unexpected description in revision")
	}
	decoded, err := base64.StdEncoding.DecodeString(str(revision.body["serverProperties"]))
	if err != nil || !bytes.Equal(decoded, properties) {
		t.Fatalf("revision properties %x, want %x (decode error %v)", decoded, properties, err)
	}
}

func TestMSKConfigurationRejectedWrites(t *testing.T) {
	h := newHarness(t, msk)
	const path = "/v1/configurations"
	create := func(spec map[string]any) reply { return h.rest("kafka", "POST", path, spec) }
	for _, spec := range []map[string]any{
		{"serverProperties": "YQ=="}, {"name": "missing"}, {"name": "bad name", "serverProperties": "YQ=="},
		{"name": "invalid", "serverProperties": "%%%"}, {"name": "unknown", "serverProperties": "YQ==", "revision": 2},
		{"name": "oversized", "serverProperties": base64.StdEncoding.EncodeToString(make([]byte, maxDocumentBytes))},
	} {
		rejected := create(spec)
		if rejected.status < 400 || rejected.status >= 500 {
			t.Fatalf("invalid configuration returned %d", rejected.status)
		}
	}
	valid := map[string]any{"name": "one", "serverProperties": "YQ=="}
	h.policy.deny["kafka:CreateConfiguration"] = true
	expect(t, create(valid), 403, "")
	delete(h.policy.deny, "kafka:CreateConfiguration")
	inject(t, h.db, "CREATE TRIGGER msk_fail_insert BEFORE INSERT ON aws_catalog_resources BEGIN SELECT RAISE(ABORT,'test failure'); END")
	expect(t, create(valid), 500, "")
	inject(t, h.db, "DROP TRIGGER msk_fail_insert")
	list := h.rest("kafka", "GET", path, nil)
	expect(t, list, 200, "")
	if len(list.body["configurations"].([]any)) != 0 {
		t.Fatal("rejected create left a configuration")
	}
	var wg sync.WaitGroup
	results := make(chan reply, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- create(valid) }()
	}
	wg.Wait()
	close(results)
	var created, conflicts int
	for r := range results {
		switch {
		case r.status == 200:
			created++
		case r.status == 409 && r.code() == "ConflictException":
			conflicts++
		default:
			t.Fatalf("unexpected concurrent-create result %d %s", r.status, r.code())
		}
	}
	if created != 1 || conflicts != 1 {
		t.Fatal("expected exactly one successful create and one name conflict")
	}
}

func TestMSKConfigurationQuotaAndRegionalScope(t *testing.T) {
	h := newHarness(t, msk)
	const path = "/v1/configurations"
	for i := 0; i < 100; i++ {
		created := h.rest("kafka", "POST", path, map[string]any{"name": fmt.Sprintf("config-%d", i), "serverProperties": "YQ=="})
		expect(t, created, 200, "")
	}
	over := h.rest("kafka", "POST", path, map[string]any{"name": "extra", "serverProperties": "YQ=="})
	if over.status < 400 || over.status >= 500 {
		t.Fatalf("scope limit returned %d", over.status)
	}
	request := httptest.NewRequest("POST", path, strings.NewReader(`{"name":"config-0","serverProperties":"Yg=="}`))
	request.Header.Set("Content-Type", "application/json")
	sign(request, "kafka", "ap-northeast-1")
	peer := h.do(request)
	expect(t, peer, 200, "")
	peerPath := path + "/" + url.PathEscape(str(peer.body["arn"])) + "/revisions/1"
	expect(t, h.rest("kafka", "GET", peerPath, nil), 404, "NotFoundException")
	request = httptest.NewRequest("GET", peerPath, nil)
	sign(request, "kafka", "ap-northeast-1")
	revision := h.do(request)
	expect(t, revision, 200, "")
	if revision.body["serverProperties"] != "Yg==" {
		t.Fatal("wrong Region's server properties")
	}
}
