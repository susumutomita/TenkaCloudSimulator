package appengine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const docsDirectory = "../../server/apidocs/gcp-appengine"

func TestEmbeddedModelBindsEveryReviewedMethodToItsOfficialRoute(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	if len(h.engine.Methods()) != len(handlers) || h.engine.OfficialMethods() != 59 {
		t.Fatalf("methods %d of %d", len(h.engine.Methods()), h.engine.OfficialMethods())
	}
	if h.engine.model.Source.SHA256 != "e35418b4cee467a81c938fe186d14647e3451084298bd4971ee4bb0fcfa33cbd" || h.engine.model.Source.Version != "2.199.0" {
		t.Fatalf("provenance: %+v", h.engine.model.Source)
	}
	reviewed := map[string]bool{}
	for id := range handlers {
		reviewed[id] = true
	}
	var m map[string]any
	_ = json.Unmarshal(modelJSON, &m)
	broken := func(mutate func(map[string]any)) []byte {
		var copy map[string]any
		_ = json.Unmarshal(modelJSON, &copy)
		mutate(copy)
		data, _ := json.Marshal(copy)
		return data
	}
	for name, data := range map[string][]byte{
		"malformed":  []byte("{"),
		"provenance": broken(func(c map[string]any) { c["source"].(map[string]any)["package"] = "other" }),
		"extra":      broken(func(c map[string]any) { c["methods"].(map[string]any)["apps.repair"] = map[string]any{} }),
		"handler": broken(func(c map[string]any) {
			delete(c["methods"].(map[string]any), "apps.get")
			c["methods"].(map[string]any)["apps.repair"] = map[string]any{}
		}),
		"schema": broken(func(c map[string]any) { delete(c["schemas"].(map[string]any), "Application") }),
	} {
		if _, _, err := loadModel(data, reviewed); err == nil {
			t.Errorf("%s model was accepted", name)
		}
	}
	db := h.db
	cfg := h.config()
	for name, mutate := range map[string]func(*Config){
		"project": func(c *Config) { c.Project = "Bad_Project" }, "owner": func(c *Config) { c.Owner = "" },
		"subject": func(c *Config) { c.Subject = nil }, "source": func(c *Config) { c.Source = nil },
		"timeout": func(c *Config) { c.RequestTimeout = time.Minute },
	} {
		c := cfg
		mutate(&c)
		if _, err := New(db, c); err == nil {
			t.Errorf("%s configuration was accepted", name)
		}
	}
	if _, err := New(nil, cfg); err == nil {
		t.Fatal("a missing database was accepted")
	}
}

// The served API reference must be regenerated from the embedded model.
func TestServedReferenceMatchesTheEmbeddedDiscoverySubset(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	document := h.engine.OpenAPI()
	data, err := json.MarshalIndent(document, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	index, _ := json.MarshalIndent([]map[string]any{{"model": "appengine.v1", "title": "App Engine Admin API", "methods": len(h.engine.Methods()), "official": h.engine.OfficialMethods()}}, "", " ")
	want := map[string][]byte{"appengine.v1.json": append(data, '\n'), "index.json": append(index, '\n')}
	if os.Getenv("APPENGINE_WRITE_DOCS") == "1" {
		_ = os.RemoveAll(docsDirectory)
		if err := os.MkdirAll(docsDirectory, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, data := range want {
			if err := os.WriteFile(filepath.Join(docsDirectory, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	entries, err := os.ReadDir(docsDirectory)
	if err != nil || len(entries) != len(want) {
		t.Fatalf("served reference is stale; run APPENGINE_WRITE_DOCS=1 go test ./internal/gcp/appengine -run TestServedReference (%v)", err)
	}
	for name, data := range want {
		got, err := os.ReadFile(filepath.Join(docsDirectory, name))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%s is stale; regenerate the App Engine reference", name)
		}
	}
	paths := document["paths"].(map[string]any)
	if len(paths) == 0 || paths["/v1/apps/{appsId}/services/{servicesId}/versions"].(map[string]any)["post"] == nil || !strings.Contains(document["info"].(map[string]any)["description"].(string), "UNIMPLEMENTED") {
		t.Fatal("reference incomplete")
	}
}

// The embedded discovery data ships with its original license.
func TestDiscoveryNoticesNameTheAppEngineSubset(t *testing.T) {
	original, err := os.ReadFile("../../../third_party/GOOGLE-DISCOVERY-NOTICES.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"Apache License", "google-api-python-client 2.199.0", "internal/gcp/appengine/model.json", "tools/gcpmodels/appengine.py"} {
		if !bytes.Contains(original, []byte(needle)) {
			t.Fatalf("notice lacks %q", needle)
		}
	}
}
