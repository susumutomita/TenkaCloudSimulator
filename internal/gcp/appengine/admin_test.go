package appengine

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestApplicationIsCreatedOnceInASupportedLocationAndCannotBeDeleted(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	h.fails("NOT_FOUND", "GET", base, nil)
	h.fails("INVALID_ARGUMENT", "POST", "/appengine/v1/apps", map[string]any{"locationId": "us-central"})
	h.fails("INVALID_ARGUMENT", "POST", "/appengine/v1/apps", map[string]any{"id": project, "locationId": "mars-north1"})
	h.fails("INVALID_ARGUMENT", "POST", "/appengine/v1/apps", map[string]any{"id": project, "authDomain": "example.com"})
	h.fails("INVALID_ARGUMENT", "POST", "/appengine/v1/apps", map[string]any{"id": project, "colour": "blue"})
	h.fails("INVALID_ARGUMENT", "POST", "/appengine/v1/apps", map[string]any{"id": 7})
	h.fails("INVALID_ARGUMENT", "POST", "/appengine/v1/apps", `{"id":`)
	h.fails("INVALID_ARGUMENT", "POST", "/appengine/v1/apps", `{} {}`)
	if h.fails("PERMISSION_DENIED", "POST", "/appengine/v1/apps", map[string]any{"id": "other-project"}) == "" {
		t.Fatal("denial needs a message")
	}
	// The location defaults to us-central and output-only fields are ignored.
	op := h.ok("POST", "/appengine/v1/apps", map[string]any{"id": project, "name": "apps/ignored", "defaultHostname": "ignored"})
	response := op["response"].(map[string]any)
	if op["done"] != true || response["@type"] != "type.googleapis.com/google.appengine.v1.Application" || response["locationId"] != "us-central" ||
		response["defaultHostname"] != project+".uc.r.appspot.com" || response["servingStatus"] != "SERVING" {
		t.Fatalf("create operation: %v", op)
	}
	if !strings.Contains(h.fails("ALREADY_EXISTS", "POST", "/appengine/v1/apps", map[string]any{"id": project, "locationId": "europe-west"}), "cannot be created again") {
		t.Fatal("re-create must explain that the application is permanent")
	}
	got := h.ok("GET", base, nil)
	if got["name"] != "apps/"+project || got["locationId"] != "us-central" {
		t.Fatalf("get: %v", got)
	}
	h.ok("GET", base+"?includeExtraData=INCLUDE_EXTRA_DATA_NONE", nil)
	h.fails("INVALID_ARGUMENT", "GET", base+"?includeExtraData=INCLUDE_GOOGLE_GENERATED_METADATA", nil)
	// There is no official delete method.
	if r := h.call("owner", "DELETE", base, nil); r.status != 404 {
		t.Fatalf("delete application: %d %s", r.status, r.raw)
	}
}

func TestApplicationPatchChangesOnlyTheMaskedServingStatus(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	h.fails("NOT_FOUND", "PATCH", base+"?updateMask=servingStatus", map[string]any{"servingStatus": "USER_DISABLED"})
	h.createApp("asia-northeast1")
	h.fails("INVALID_ARGUMENT", "PATCH", base, map[string]any{"servingStatus": "USER_DISABLED"})
	h.fails("INVALID_ARGUMENT", "PATCH", base+"?updateMask=authDomain", map[string]any{"authDomain": "x"})
	h.fails("INVALID_ARGUMENT", "PATCH", base+"?updateMask=servingStatus", map[string]any{"servingStatus": "SYSTEM_DISABLED"})
	h.fails("INVALID_ARGUMENT", "PATCH", base+"?updateMask=servingStatus", map[string]any{"servingStatus": true})
	h.fails("INVALID_ARGUMENT", "PATCH", base+"?updateMask=servingStatus", map[string]any{"servingStatus": "SERVING", "unknown": 1})
	// Fields outside the mask are ignored, so a client may send the resource back.
	op := h.ok("PATCH", base+"?updateMask=servingStatus,servingStatus", map[string]any{"servingStatus": "USER_DISABLED", "locationId": "us-central"})
	if op["response"].(map[string]any)["servingStatus"] != "USER_DISABLED" || h.ok("GET", base, nil)["locationId"] != "asia-northeast1" {
		t.Fatalf("patch: %v", op)
	}
}

func TestOnlyTheConfiguredOwnerReachesTheApplicationAndDenialsChangeNothing(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	h.createApp("us-central")
	h.deploy("default", "v1", map[string]string{"main.py": app})
	before := h.ok("GET", base+"/services/default", nil)
	for _, c := range []struct{ method, path string }{
		{"GET", base}, {"POST", "/appengine/v1/apps"}, {"PATCH", base + "?updateMask=servingStatus"},
		{"GET", base + "/services"}, {"DELETE", base + "/services/default/versions/v1"},
		{"POST", base + "/firewall/ingressRules"}, {"GET", base + "/operations"}, {"GET", base + "/services/missing"},
	} {
		r := h.call("intruder", c.method, c.path, map[string]any{"servingStatus": "USER_DISABLED"})
		if r.status != 403 || r.body["error"].(map[string]any)["status"] != "PERMISSION_DENIED" {
			t.Fatalf("%s %s by another subject: %d %s", c.method, c.path, r.status, r.raw)
		}
	}
	// Another project is refused before its existence is checked.
	if r := h.call("owner", "GET", "/appengine/v1/apps/other-project/services/missing", nil); r.status != 403 {
		t.Fatalf("other project: %d %s", r.status, r.raw)
	}
	if r := h.call("", "GET", base, nil); r.status != 401 || !strings.Contains(r.raw, "UNAUTHENTICATED") {
		t.Fatalf("no subject: %d %s", r.status, r.raw)
	}
	after := h.ok("GET", base+"/services/default", nil)
	if fmt.Sprint(before) != fmt.Sprint(after) || h.ok("GET", base, nil)["servingStatus"] != "SERVING" {
		t.Fatal("a refused request changed state")
	}
}

func TestOfficialMethodsOutsideTheSubsetAreUnimplementedAndPathsAreStrict(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	h.createApp("us-central")
	for _, c := range []struct{ method, path string }{
		{"POST", base + ":repair"}, {"GET", base + ":listRuntimes"}, {"GET", base + "/domainMappings"},
		{"GET", base + "/authorizedDomains"}, {"GET", base + "/services/default/versions/v1/instances"},
		{"POST", base + "/services/default/versions/v1:exportAppImage"},
		{"PATCH", "/appengine/v1/projects/" + project + "/locations/us-central/applications/" + project},
	} {
		h.fails("UNIMPLEMENTED", c.method, c.path, map[string]any{})
	}
	for _, path := range []string{"/appengine", "/appengine/v2/apps", base + "/services/a%2Fb/versions", base + "/nothing"} {
		h.fails("NOT_FOUND", "GET", path, nil)
	}
	h.fails("NOT_FOUND", "GET", "/elsewhere", nil)
	h.fails("INVALID_ARGUMENT", "GET", base+"?unknown=1", nil)
	h.fails("INVALID_ARGUMENT", "GET", base+"/services?pageSize=1&pageSize=2", nil)
	h.fails("INVALID_ARGUMENT", "GET", base+"/services?pageSize=-1", nil)
	h.fails("INVALID_ARGUMENT", "GET", base+"/services?pageToken=***", nil)
	h.fails("INVALID_ARGUMENT", "DELETE", base+"/services/default", "{}")
	h.fails("INVALID_ARGUMENT", "POST", base+"/firewall/ingressRules", strings.Repeat(" ", maxRequestBytes+1))
	h.ok("GET", base+"?alt=json&prettyPrint=false", nil)
}

func TestLocationsAndOperationsPageWithOpaqueTokens(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	first := h.ok("GET", base+"/locations?pageSize=3", nil)
	if len(first["locations"].([]any)) != 3 || first["nextPageToken"] == nil {
		t.Fatalf("first page: %v", first)
	}
	second := h.ok("GET", base+"/locations?pageSize=3&pageToken="+first["nextPageToken"].(string), nil)
	if len(second["locations"].([]any)) != 1 || second["nextPageToken"] != nil {
		t.Fatalf("second page: %v", second)
	}
	tokyo := h.ok("GET", base+"/locations/asia-northeast1", nil)
	if tokyo["labels"].(map[string]any)["cloud.googleapis.com/region"] != "asia-northeast1" || tokyo["metadata"].(map[string]any)["standardEnvironmentAvailable"] != true {
		t.Fatalf("location: %v", tokyo)
	}
	h.fails("NOT_FOUND", "GET", base+"/locations/us-west9", nil)
	h.fails("INVALID_ARGUMENT", "GET", base+"/locations?filter=x", nil)
	h.createApp("asia-northeast1")
	for _, status := range []string{"USER_DISABLED", "SERVING", "USER_DISABLED"} {
		h.ok("PATCH", base+"?updateMask=servingStatus", map[string]any{"servingStatus": status})
	}
	page := h.ok("GET", base+"/operations?pageSize=2", nil)
	ops := page["operations"].([]any)
	if len(ops) != 2 || ops[0].(map[string]any)["metadata"].(map[string]any)["method"] != "google.appengine.v1.Applications.CreateApplication" {
		t.Fatalf("operations: %v", page)
	}
	rest := h.ok("GET", base+"/operations?pageToken="+page["nextPageToken"].(string), nil)
	if len(rest["operations"].([]any)) != 2 || rest["nextPageToken"] != nil {
		t.Fatalf("rest: %v", rest)
	}
	name := ops[1].(map[string]any)["name"].(string)
	if got := h.ok("GET", "/appengine/v1/"+name, nil); got["name"] != name || got["done"] != true {
		t.Fatalf("operation get: %v", got)
	}
	h.fails("NOT_FOUND", "GET", base+"/operations/missing", nil)
	h.fails("INVALID_ARGUMENT", "GET", base+"/operations?filter=done", nil)
	h.fails("INVALID_ARGUMENT", "GET", base+"/operations?returnPartialSuccess=true", nil)
	h.fails("INVALID_ARGUMENT", "GET", base+"/operations?pageToken="+pageToken("x"), nil)
}

func TestOperationsKeepOnlyTheNewestThousand(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	h.createApp("us-central")
	first := h.ok("GET", base+"/operations", nil)["operations"].([]any)[0].(map[string]any)["name"].(string)
	for i := 0; i < maxOperations; i++ {
		h.ok("PATCH", base+"?updateMask=servingStatus", map[string]any{"servingStatus": "SERVING"})
	}
	var n int
	if err := h.db.QueryRow("SELECT count(*) FROM gcp_appengine_operations").Scan(&n); err != nil || n != maxOperations {
		t.Fatalf("retained operations: %d %v", n, err)
	}
	h.fails("NOT_FOUND", "GET", "/appengine/v1/"+first, nil)
}

func TestFirstVersionCreatesItsServiceWithAllTrafficAndLaterVersionsWaitForTheSplit(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	h.fails("NOT_FOUND", "GET", base+"/services", nil)
	h.put("src/a.zip", zipOf(t, map[string]string{"main.py": app}))
	h.fails("NOT_FOUND", "POST", base+"/services/default/versions", map[string]any{"id": "v1", "runtime": "python312", "deployment": map[string]any{"zip": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/a.zip"}}})
	h.createApp("us-central")
	h.put("src/api.zip", zipOf(t, map[string]string{"main.py": app}))
	if !strings.Contains(h.fails("FAILED_PRECONDITION", "POST", base+"/services/api/versions", map[string]any{"id": "v1", "runtime": "python312",
		"deployment": map[string]any{"zip": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/api.zip"}}}), "default service") {
		t.Fatal("the first service must be default")
	}
	op := h.deploy("default", "v1", map[string]string{"main.py": app, "lib/util.py": "X = 1\n"})
	v := op["response"].(map[string]any)
	if v["versionUrl"] != "https://v1-dot-default-dot-"+project+".uc.r.appspot.com" || v["createdBy"] != "owner" || v["deployment"] == nil {
		t.Fatalf("version: %v", v)
	}
	sum := sha1.Sum([]byte(app))
	h.put("src/files/main.py", []byte(app))
	h.ok("POST", base+"/services/default/versions", map[string]any{"id": "v2", "runtime": "python312", "env": "standard", "servingStatus": "STOPPED",
		"envVariables": map[string]any{"GREETING": "hi"}, "createTime": "ignored",
		"deployment": map[string]any{"files": map[string]any{"main.py": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/files/main.py", "sha1Sum": hex.EncodeToString(sum[:]), "mimeType": "text/x-python"}}}})
	h.fails("ALREADY_EXISTS", "POST", base+"/services/default/versions", map[string]any{"id": "v2", "runtime": "python312",
		"deployment": map[string]any{"files": map[string]any{"main.py": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/files/main.py"}}}})
	service := h.ok("GET", base+"/services/default", nil)
	if fmt.Sprint(service["split"]) != "map[allocations:map[v1:1]]" {
		t.Fatalf("split: %v", service)
	}
	basic := h.ok("GET", base+"/services/default/versions/v2", nil)
	full := h.ok("GET", base+"/services/default/versions/v2?view=FULL", nil)
	if basic["deployment"] != nil || basic["envVariables"] != nil || full["envVariables"].(map[string]any)["GREETING"] != "hi" || full["servingStatus"] != "STOPPED" {
		t.Fatalf("views: %v %v", basic, full)
	}
	h.fails("INVALID_ARGUMENT", "GET", base+"/services/default/versions/v2?view=ALL", nil)
	h.fails("INVALID_ARGUMENT", "GET", base+"/services/default/versions?view=ALL", nil)
	list := h.ok("GET", base+"/services/default/versions?pageSize=1&view=FULL", nil)
	if len(list["versions"].([]any)) != 1 || list["nextPageToken"] == nil {
		t.Fatalf("version page: %v", list)
	}
	rest := h.ok("GET", base+"/services/default/versions?pageToken="+list["nextPageToken"].(string), nil)
	if len(rest["versions"].([]any)) != 1 || rest["versions"].([]any)[0].(map[string]any)["id"] != "v2" {
		t.Fatalf("version rest: %v", rest)
	}
	h.fails("NOT_FOUND", "GET", base+"/services/default/versions/v9", nil)
	h.fails("NOT_FOUND", "GET", base+"/services/api/versions", nil)
	h.fails("NOT_FOUND", "GET", base+"/services/api/versions/v1", nil)
	h.fails("NOT_FOUND", "GET", base+"/services/api", nil)
	h.deploy("api", "v1", map[string]string{"main.py": app})
	services := h.ok("GET", base+"/services?pageSize=1", nil)
	if services["services"].([]any)[0].(map[string]any)["id"] != "api" || services["nextPageToken"] == nil {
		t.Fatalf("services: %v", services)
	}
}

func TestTrafficSplitsNameExistingVersionsAndAddUpToOne(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	h.createApp("us-central")
	h.deploy("default", "v1", map[string]string{"main.py": app})
	h.deploy("default", "v2", map[string]string{"main.py": app})
	path := base + "/services/default?updateMask=split"
	for _, split := range []any{
		map[string]any{"allocations": map[string]any{"v1": 0.5}},
		map[string]any{"allocations": map[string]any{"v1": 0.555, "v2": 0.445}},
		map[string]any{"shardBy": "COOKIE", "allocations": map[string]any{"v1": 0.5555, "v2": 0.4445}},
		map[string]any{"allocations": map[string]any{"v1": 1, "v3": 0}},
		map[string]any{"allocations": map[string]any{}},
		map[string]any{"shardBy": "HASH", "allocations": map[string]any{"v1": 1}},
		map[string]any{"shardBy": 1, "allocations": map[string]any{"v1": 1}},
		map[string]any{"allocations": map[string]any{"v1": "1"}},
		map[string]any{"allocations": map[string]any{"v1": 1}, "weights": 1},
		"not an object",
	} {
		h.fails("INVALID_ARGUMENT", "PATCH", path, map[string]any{"split": split})
	}
	h.fails("INVALID_ARGUMENT", "PATCH", path, map[string]any{"split": map[string]any{"allocations": map[string]any{"v1": 0.5, "v9": 0.5}}})
	h.fails("INVALID_ARGUMENT", "PATCH", path, map[string]any{})
	h.fails("INVALID_ARGUMENT", "PATCH", base+"/services/default", map[string]any{"split": map[string]any{"allocations": map[string]any{"v1": 1}}})
	h.fails("INVALID_ARGUMENT", "PATCH", base+"/services/default?updateMask=labels", map[string]any{"labels": map[string]any{}})
	h.fails("INVALID_ARGUMENT", "PATCH", path+"&migrateTraffic=true", map[string]any{"split": map[string]any{"shardBy": "IP", "allocations": map[string]any{"v2": 1}}})
	h.fails("NOT_FOUND", "PATCH", base+"/services/api?updateMask=split", map[string]any{"split": map[string]any{"allocations": map[string]any{"v1": 1}}})
	op := h.ok("PATCH", path, map[string]any{"id": "ignored", "split": map[string]any{"shardBy": "RANDOM", "allocations": map[string]any{"v1": 0.25, "v2": 0.75}}})
	if fmt.Sprint(op["response"].(map[string]any)["split"]) != "map[allocations:map[v1:0.25 v2:0.75] shardBy:RANDOM]" {
		t.Fatalf("split response: %v", op)
	}
	h.ok("PATCH", path, map[string]any{"split": map[string]any{"shardBy": "UNSPECIFIED", "allocations": map[string]any{"v1": 0.2, "v2": 0.8}}})
	if got := h.ok("GET", base+"/services/default", nil)["split"].(map[string]any); got["shardBy"] != nil {
		t.Fatalf("UNSPECIFIED is stored as no shard: %v", got)
	}
}

func TestVersionsWithTrafficAndTheDefaultServiceCannotBeDeleted(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	h.createApp("us-central")
	h.deploy("default", "v1", map[string]string{"main.py": app})
	h.deploy("default", "v2", map[string]string{"main.py": app})
	h.deploy("api", "v1", map[string]string{"main.py": app})
	h.deploy("api", "v2", map[string]string{"main.py": app})
	if !strings.Contains(h.fails("FAILED_PRECONDITION", "DELETE", base+"/services/default/versions/v1", nil), "receives traffic") {
		t.Fatal("a version with traffic must not be deleted")
	}
	h.fails("FAILED_PRECONDITION", "DELETE", base+"/services/default", nil)
	h.fails("NOT_FOUND", "DELETE", base+"/services/default/versions/v9", nil)
	h.fails("NOT_FOUND", "DELETE", base+"/services/web", nil)
	h.fails("NOT_FOUND", "DELETE", base+"/services/web/versions/v1", nil)
	op := h.ok("DELETE", base+"/services/default/versions/v2", nil)
	if op["response"].(map[string]any)["@type"] != "type.googleapis.com/google.protobuf.Empty" {
		t.Fatalf("delete version: %v", op)
	}
	h.ok("PATCH", base+"/services/default/versions/v1?updateMask=servingStatus", map[string]any{"servingStatus": "STOPPED", "runtime": "ignored"})
	if h.ok("GET", base+"/services/default/versions/v1", nil)["servingStatus"] != "STOPPED" {
		t.Fatal("stop did not persist")
	}
	h.fails("INVALID_ARGUMENT", "PATCH", base+"/services/default/versions/v1?updateMask=servingStatus", map[string]any{"servingStatus": "PAUSED"})
	h.fails("INVALID_ARGUMENT", "PATCH", base+"/services/default/versions/v1?updateMask=servingStatus", map[string]any{"servingStatus": 1})
	h.fails("INVALID_ARGUMENT", "PATCH", base+"/services/default/versions/v1?updateMask=instanceClass", map[string]any{"instanceClass": "F2"})
	h.fails("INVALID_ARGUMENT", "PATCH", base+"/services/default/versions/v1?updateMask=servingStatus", map[string]any{"bogus": 1})
	h.fails("NOT_FOUND", "PATCH", base+"/services/default/versions/v9?updateMask=servingStatus", map[string]any{"servingStatus": "SERVING"})
	h.fails("NOT_FOUND", "PATCH", base+"/services/web/versions/v1?updateMask=servingStatus", map[string]any{"servingStatus": "SERVING"})
	// Deleting a service removes its versions and split.
	h.ok("DELETE", base+"/services/api", nil)
	h.fails("NOT_FOUND", "GET", base+"/services/api", nil)
	for _, table := range []string{"gcp_appengine_versions", "gcp_appengine_allocations"} {
		var n int
		if err := h.db.QueryRow("SELECT count(*) FROM " + table + " WHERE service = 'api'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s kept %d rows (%v)", table, n, err)
		}
	}
	// The database refuses an out-of-order delete even without the handler.
	if _, err := h.db.Exec("DELETE FROM gcp_appengine_versions WHERE service = 'default' AND id = 'v1'"); err == nil {
		t.Fatal("a version with an allocation was deleted underneath its split")
	}
}

func TestDeploymentsCopyOnlyBoundedPythonSourcesFromCloudStorage(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	h.createApp("us-central")
	h.put("src/ok.zip", zipOf(t, map[string]string{"main.py": app}))
	h.put("src/static.zip", zipOf(t, map[string]string{"main.py": app, "index.html": "<p>"}))
	h.put("src/nomain.zip", zipOf(t, map[string]string{"app.py": app}))
	h.put("src/reserved.zip", zipOf(t, map[string]string{"main.py": app, adapterName: "x"}))
	h.put("src/escape.zip", zipOf(t, map[string]string{"main.py": app, "../up.py": "x"}))
	h.put("src/garbage.zip", []byte("not a zip"))
	h.put("src/huge.zip", make([]byte, 300<<10))
	many := map[string]string{"main.py": app}
	for i := 0; i < maxFiles; i++ {
		many[fmt.Sprintf("m%02d.py", i)] = ""
	}
	h.put("src/many.zip", zipOf(t, many))
	h.put("src/expanded.zip", zipOf(t, map[string]string{"main.py": app, "big.py": "#" + strings.Repeat("x", maxExpandedBytes)}))
	h.put("src/main.py", []byte(app))
	zip := func(url string, extra ...any) map[string]any {
		info := map[string]any{"sourceUrl": url}
		for i := 0; i+1 < len(extra); i += 2 {
			info[extra[i].(string)] = extra[i+1]
		}
		return map[string]any{"zip": info}
	}
	version := func(id string, deployment any, extra ...any) map[string]any {
		body := map[string]any{"id": id, "runtime": "python312", "deployment": deployment}
		for i := 0; i+1 < len(extra); i += 2 {
			body[extra[i].(string)] = extra[i+1]
		}
		return body
	}
	path := base + "/services/default/versions"
	good := zip("https://storage.googleapis.com/src/ok.zip")
	for name, body := range map[string]map[string]any{
		"runtime":        version("v1", good, "runtime", "python311"),
		"flex":           version("v1", good, "env", "flex"),
		"status":         version("v1", good, "servingStatus", "PAUSED"),
		"handlers":       version("v1", good, "handlers", []any{}),
		"unknown":        version("v1", good, "bogus", true),
		"default id":     version("default", good),
		"latest id":      version("latest", good),
		"ah id":          version("ah-builtin", good),
		"dot id":         version("a-dot-b", good),
		"upper id":       version("V1", good),
		"no deployment":  {"id": "v1", "runtime": "python312"},
		"bad deployment": version("v1", "zip"),
		"container":      version("v1", map[string]any{"container": map[string]any{"image": "x"}}),
		"build options":  version("v1", map[string]any{"cloudBuildOptions": map[string]any{}}),
		"deployment key": version("v1", map[string]any{"tarball": map[string]any{}}),
		"both":           version("v1", map[string]any{"zip": good["zip"], "files": map[string]any{}}),
		"neither":        version("v1", map[string]any{}),
		"zip info":       version("v1", map[string]any{"zip": "x"}),
		"zip field":      version("v1", zip("https://storage.googleapis.com/src/ok.zip", "checksum", "x")),
		"zip url type":   version("v1", map[string]any{"zip": map[string]any{"sourceUrl": 1}}),
		"files count":    version("v1", zip("https://storage.googleapis.com/src/ok.zip", "filesCount", -1)),
		"too few files":  version("v1", zip("https://storage.googleapis.com/src/many.zip", "filesCount", 3)),
		"gs url":         version("v1", zip("gs://src/ok.zip")),
		"other host":     version("v1", zip("https://example.com/src/ok.zip")),
		"no object":      version("v1", zip("https://storage.googleapis.com/src")),
		"query":          version("v1", zip("https://storage.googleapis.com/src/ok.zip?x=1")),
		"bad escape":     version("v1", zip("https://storage.googleapis.com/src/o%zz")),
		"missing":        version("v1", zip("https://storage.googleapis.com/src/missing.zip")),
		"too large":      version("v1", zip("https://storage.googleapis.com/src/huge.zip")),
		"garbage":        version("v1", zip("https://storage.googleapis.com/src/garbage.zip")),
		"static file":    version("v1", zip("https://storage.googleapis.com/src/static.zip")),
		"no main":        version("v1", zip("https://storage.googleapis.com/src/nomain.zip")),
		"reserved file":  version("v1", zip("https://storage.googleapis.com/src/reserved.zip")),
		"escape":         version("v1", zip("https://storage.googleapis.com/src/escape.zip")),
		"many files":     version("v1", zip("https://storage.googleapis.com/src/many.zip")),
		"expanded":       version("v1", zip("https://storage.googleapis.com/src/expanded.zip")),
		"files empty":    version("v1", map[string]any{"files": map[string]any{}}),
		"files field":    version("v1", map[string]any{"files": map[string]any{"main.py": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/main.py", "size": "1"}}}),
		"files path":     version("v1", map[string]any{"files": map[string]any{"/etc/main.py": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/main.py"}}}),
		"files sha1":     version("v1", map[string]any{"files": map[string]any{"main.py": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/main.py", "sha1Sum": strings.Repeat("0", 40)}}}),
		"files missing":  version("v1", map[string]any{"files": map[string]any{"main.py": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/missing.py"}}}),
		"env reserved":   version("v1", good, "envVariables", map[string]any{"GAE_VERSION": "x"}),
		"env name":       version("v1", good, "envVariables", map[string]any{"1BAD": "x"}),
		"env type":       version("v1", good, "envVariables", map[string]any{"A": 1}),
		"env size":       version("v1", good, "envVariables", map[string]any{"A": strings.Repeat("x", 5000)}),
	} {
		r := h.call("owner", "POST", path, body)
		if r.status != 400 || !strings.Contains(r.raw, "INVALID_ARGUMENT") {
			t.Errorf("%s: %d %s", name, r.status, r.raw)
		}
	}
	files := map[string]any{}
	for i := 0; i <= maxFiles; i++ {
		files[fmt.Sprintf("f%02d.py", i)] = map[string]any{"sourceUrl": "https://storage.googleapis.com/src/main.py"}
	}
	h.fails("INVALID_ARGUMENT", "POST", path, version("v1", map[string]any{"files": files}))
	h.fails("INVALID_ARGUMENT", "POST", base+"/services/Bad_Name/versions", version("v1", good))
	h.fails("INVALID_ARGUMENT", "POST", base+"/services/a-dot-b/versions", version("v1", good))
	var n int
	if err := h.db.QueryRow("SELECT count(*) FROM gcp_appengine_versions").Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused deployments stored %d versions (%v)", n, err)
	}
	h.ok("POST", path, version("v1", zip("https://storage.googleapis.com/src/ok.zip", "filesCount", 5)))
	// The version keeps its copy: a later change to the object does not change it.
	h.put("src/ok.zip", zipOf(t, map[string]string{"main.py": "broken"}))
	var code []byte
	if err := h.db.QueryRow("SELECT code FROM gcp_appengine_versions WHERE id = 'v1'").Scan(&code); err != nil {
		t.Fatal(err)
	}
	if files, err := unpack(code); err != nil || string(files["main.py"]) != app {
		t.Fatalf("stored code: %v %v", files, err)
	}
}

func TestServiceAndVersionBudgetsAreLocalLimits(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	h.createApp("us-central")
	h.put("src/ok.zip", zipOf(t, map[string]string{"main.py": app}))
	create := func(service, id string) reply {
		return h.call("owner", "POST", base+"/services/"+service+"/versions", map[string]any{"id": id, "runtime": "python312",
			"deployment": map[string]any{"zip": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/ok.zip"}}})
	}
	for i := 0; i < maxVersions; i++ {
		if r := create("default", fmt.Sprintf("v%d", i)); r.status != 200 {
			t.Fatal(r.raw)
		}
	}
	if r := create("default", "extra"); r.status != 429 {
		t.Fatalf("versions per service: %d %s", r.status, r.raw)
	}
	for i := 1; i < maxServices; i++ {
		if r := create(fmt.Sprintf("s%d", i), "v1"); r.status != 200 {
			t.Fatal(r.raw)
		}
	}
	if r := create("s99", "v1"); r.status != 429 {
		t.Fatalf("services: %d %s", r.status, r.raw)
	}
	// Fill other services up to the application-wide budget.
	for total, i := maxVersions+maxServices-1, 0; total < maxAllVersions; total, i = total+1, i+1 {
		if r := create(fmt.Sprintf("s%d", 1+i/(maxVersions-1)), fmt.Sprintf("w%d", i)); r.status != 200 {
			t.Fatal(r.raw)
		}
	}
	if r := create("s2", "extra"); r.status != 429 {
		t.Fatalf("versions per application: %d %s", r.status, r.raw)
	}
}

func TestConcurrentDeploymentsOfOneVersionStoreItOnce(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	h.createApp("us-central")
	h.put("src/ok.zip", zipOf(t, map[string]string{"main.py": app}))
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- h.call("owner", "POST", base+"/services/default/versions", map[string]any{"id": "v1", "runtime": "python312",
				"deployment": map[string]any{"zip": map[string]any{"sourceUrl": "https://storage.googleapis.com/src/ok.zip"}}}).status
		}()
	}
	wg.Wait()
	close(codes)
	created := 0
	for code := range codes {
		if code == 200 {
			created++
		} else if code != 409 && code != 500 {
			t.Fatalf("unexpected concurrent status %d", code)
		}
	}
	var n, shares int
	if err := h.db.QueryRow("SELECT count(*), (SELECT sum(share) FROM gcp_appengine_allocations) FROM gcp_appengine_versions").Scan(&n, &shares); err != nil || n != 1 || shares != allocationUnits || created < 1 {
		t.Fatalf("versions %d shares %d created %d (%v)", n, shares, created, err)
	}
}

func TestStateSurvivesARestartOfTheEngine(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	h.createApp("europe-west")
	h.deploy("default", "v1", map[string]string{"main.py": app})
	h.ok("POST", base+"/firewall/ingressRules", map[string]any{"priority": 10, "action": "DENY", "sourceRange": "10.0.0.0/8"})
	if err := h.engine.Close(); err != nil {
		t.Fatal(err)
	}
	engine, err := New(h.db, h.config())
	if err != nil {
		t.Fatal(err)
	}
	h.engine = engine
	defer engine.Close()
	if h.ok("GET", base, nil)["locationId"] != "europe-west" || h.ok("GET", base+"/firewall/ingressRules/10", nil)["action"] != "DENY" ||
		h.ok("GET", base+"/services/default/versions/v1", nil)["id"] != "v1" {
		t.Fatal("state did not survive the restart")
	}
}
