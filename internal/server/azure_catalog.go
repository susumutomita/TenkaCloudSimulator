package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/susumutomita/TenkaCloudSimulator/internal/azure/arm"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

// AzureARMProvider is the provider route of the Azure Resource Manager
// catalog. SDK clients use <endpoint>/_simulator/providers/azure-arm as their
// base URL and send official /subscriptions/... paths.
const AzureARMProvider = "azure-arm"

// The synthetic tenant and subscription of every environment. They are fixed
// local identifiers, never real Azure tenants or subscriptions.
const (
	AzureSubscription = "00000000-0000-4000-8000-000000000001"
	AzureTenant       = "00000000-0000-4000-8000-000000000000"
)

// azureLocations are the Azure regions the catalog accepts.
var azureLocations = []string{"eastus", "eastus2", "westus2", "westeurope", "northeurope", "japaneast", "japanwest", "southeastasia"}

// registerAzureARM composes the ARM catalog as its own provider. It shares
// the configured Azure owner and environment with Blob Storage; that owner
// is local authority, not Azure RBAC, and AWS identity policies grant nothing.
func registerAzureARM(db *sql.DB, registry *provider.Registry, cfg CloudStorageConfig) (*arm.Engine, error) {
	engine, err := arm.New(db, arm.Config{Environment: cfg.Environment, Subscription: AzureSubscription, Tenant: AzureTenant, Owner: cfg.AzureOwner,
		Locations: azureLocations, BasePath: provider.Prefix + AzureARMProvider,
		Subject: func(ctx context.Context) (string, bool) {
			actor, ok := provider.ActorFromContext(ctx)
			return actor.ExerciseIdentity, ok
		}}, arm.Providers())
	if err != nil {
		return nil, err
	}
	return engine, registry.Register(AzureARMProvider, engine)
}

// azureCatalogDescriptorsPath serves the full descriptors of every reviewed
// resource provider.
const azureCatalogDescriptorsPath = "/_simulator/v1/catalog/azure"

// azureCatalogDescriptors serves the full descriptors that first-party
// clients use to build native ARM requests.
func azureCatalogDescriptors(engine *arm.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"providers": engine.Describe(), "subscription": AzureSubscription, "tenant": AzureTenant,
			"locations": azureLocations, "endpoint": provider.Prefix + AzureARMProvider})
	}
}

// azureCatalogCapabilities advertises the reviewed resource providers. Every
// other official operation of their api-versions returns HTTP 501. The
// capability document carries compact summaries; full descriptors are served
// by azureCatalogDescriptorsPath so the capability document stays bounded.
func azureCatalogCapabilities(engine *arm.Engine) map[string]any {
	summaries := []map[string]any{}
	for _, d := range engine.Describe() {
		types := []map[string]any{}
		for _, t := range d.Types {
			types = append(types, map[string]any{"type": t.Type, "title": t.Title})
		}
		summaries = append(summaries, map[string]any{"namespace": d.Namespace, "apiVersion": d.APIVersion, "title": d.Title, "issues": d.Issues,
			"operations": len(d.Operations), "officialOperations": d.Official, "types": types})
	}
	return map[string]any{"providers": summaries, "descriptors": azureCatalogDescriptorsPath, "subscription": AzureSubscription, "tenant": AzureTenant, "locations": azureLocations,
		"endpoint": provider.Prefix + AzureARMProvider, "reference": "/_simulator/docs/azure-catalog.html",
		"authorization": "Local configured Azure owner only (--cloud-storage-owner); not Azure RBAC or Entra ID",
		"operations":    "long-running operations complete synchronously with provisioningState Succeeded; lists page with absolute nextLink URLs",
		"state":         "configuration metadata in provider-owned SQLite; no workload, data plane, network, secret or real Azure call",
		"requestBytes":  256 << 10, "documentBytes": 64 << 10, "storedBytes": 32 << 20, "resources": 10000,
		"unsupported": "official operations of each reviewed api-version not listed in the descriptors return HTTP 501 NotImplemented; unsupported members are refused with InvalidRequestContent",
		"model":       "documentation-free subsets of github.com/Azure/azure-rest-api-specs (MIT) at the pinned commit in each provider source"}
}
