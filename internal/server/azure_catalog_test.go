package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// The ARM catalog is its own provider behind the local access boundary; the
// capability document stays compact and full descriptors have their own route.
func TestAzureCatalogCompositionCapabilitiesAndAuthentication(t *testing.T) {
	cfg := DefaultCloudStorageConfig()
	cfg.AzureOwner = "learner"
	handler, closeStore, err := NewWithCloudStorage(filepath.Join(t.TempDir(), "state.db"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeStore() }()
	access, err := NewAccess(handler, strings.Repeat("o", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(access)
	defer server.Close()
	call := func(method, path, token, body string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("X-Simulator-Token", token)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, data
	}
	operator := strings.Repeat("o", 64)
	status, data := call("GET", "/_simulator/v1/capabilities", operator, "")
	if status != 200 || len(data) > 128<<10 {
		t.Fatalf("capabilities %d (%d bytes)", status, len(data))
	}
	var capabilities struct {
		Providers    []string       `json:"providers"`
		AzureCatalog map[string]any `json:"azureCatalog"`
	}
	if err := json.Unmarshal(data, &capabilities); err != nil {
		t.Fatal(err)
	}
	summary := capabilities.AzureCatalog
	if summary["descriptors"] != azureCatalogDescriptorsPath || summary["subscription"] != AzureSubscription || summary["endpoint"] != "/_simulator/providers/azure-arm" || len(summary["providers"].([]any)) != 11 {
		t.Fatalf("summary %v", summary)
	}
	found := false
	for _, name := range capabilities.Providers {
		found = found || name == AzureARMProvider
	}
	if !found {
		t.Fatalf("providers %v", capabilities.Providers)
	}
	status, data = call("GET", azureCatalogDescriptorsPath, operator, "")
	var descriptors struct {
		Providers []struct {
			Namespace  string   `json:"namespace"`
			Operations []string `json:"operations"`
		} `json:"providers"`
		Locations []string `json:"locations"`
	}
	if status != 200 || json.Unmarshal(data, &descriptors) != nil || len(descriptors.Providers) != 11 || len(descriptors.Locations) == 0 || len(descriptors.Providers[0].Operations) == 0 {
		t.Fatalf("descriptors %d %s", status, data)
	}
	if status, _ := call("POST", azureCatalogDescriptorsPath, operator, ""); status != 405 {
		t.Fatalf("descriptors method %d", status)
	}
	if status, _ := call("GET", "/_simulator/v1/catalog/other", operator, ""); status != 404 {
		t.Fatalf("other catalog paths stay closed: %d", status)
	}
	group := "/_simulator/providers/azure-arm/subscriptions/" + AzureSubscription + "/resourcegroups/rg1?api-version=2025-04-01"
	status, data = call("PUT", group, "", `{"location":"eastus"}`)
	if status != 401 || !strings.Contains(string(data), `"code":"AuthenticationFailed"`) {
		t.Fatalf("unauthenticated ARM envelope %d %s", status, data)
	}
	// The operator is not the configured Azure owner, and AWS policy grants nothing.
	status, data = call("PUT", group, operator, `{"location":"eastus"}`)
	if status != 403 || !strings.Contains(string(data), "AuthorizationFailed") {
		t.Fatalf("operator %d %s", status, data)
	}
	_, data = call("POST", "/_simulator/v1/access", operator, `{"Principal":"learner"}`)
	var issued struct{ AccessToken string }
	if err := json.Unmarshal(data, &issued); err != nil || issued.AccessToken == "" {
		t.Fatalf("participant token %s", data)
	}
	if status, data := call("PUT", group, issued.AccessToken, `{"location":"eastus"}`); status != 201 || !strings.Contains(string(data), `"provisioningState":"Succeeded"`) {
		t.Fatalf("owner create %d %s", status, data)
	}
}
