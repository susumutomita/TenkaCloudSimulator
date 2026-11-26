package server

import (
	"encoding/json"
	"net/http"

	azurequeue "github.com/susumutomita/TenkaCloudSimulator/internal/azure/queue"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

const azureQueueReferencePath = "/_simulator/docs/azure-queue/openapi.json"

func registerAzureQueueReference(mux *http.ServeMux, cfg CloudStorageConfig) {
	mux.HandleFunc(azureQueueReferencePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !azureQueueReferenceVisible(r, cfg) {
			http.Error(w, "Local Azure storage account owner required", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(azurequeue.OpenAPI(cfg.AzureAccount))
	})
}

func azureQueueReferenceVisible(r *http.Request, cfg CloudStorageConfig) bool {
	actor, ok := provider.ActorFromContext(r.Context())
	return ok && cfg.AzureAccount != "" && actor.ExerciseIdentity == cfg.AzureOwner && consoleScopeFromRequest(r) == nil
}

func addAzureQueueConsoleCapability(r *http.Request, capabilities map[string]any, cfg CloudStorageConfig) {
	if !azureQueueReferenceVisible(r, cfg) {
		return
	}
	capabilities["azureQueue"] = map[string]any{
		"console": true, "account": cfg.AzureAccount, "apiVersion": "2023-11-03",
		"endpoint":   provider.Prefix + "azure-queue/" + cfg.AzureAccount,
		"reference":  "/_simulator/docs/azure-queue.html",
		"operations": []string{"CreateQueue", "DeleteQueue", "PutMessage", "GetMessages", "PeekMessages", "UpdateMessage", "DeleteMessage", "ClearMessages"},
	}
}
