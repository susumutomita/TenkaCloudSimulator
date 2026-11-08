package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"

	azureblob "github.com/susumutomita/TenkaCloudSimulator/internal/azure/blob"
	gcpcatalog "github.com/susumutomita/TenkaCloudSimulator/internal/gcp/catalog"
	gcpstorage "github.com/susumutomita/TenkaCloudSimulator/internal/gcp/storage"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

// CloudStorageConfig binds local provider scopes and their exercise owners.
// These are host settings, never caller-selected request headers or cloud IAM.
// Changing a persisted owner or scope is not a supported ownership migration.
type CloudStorageConfig struct {
	Environment      string
	GCPProject       string
	GCPProjectNumber string
	GCPOwner         string
	AzureAccount     string
	AzureOwner       string
}

func DefaultCloudStorageConfig() CloudStorageConfig {
	return CloudStorageConfig{Environment: "local", GCPProject: "tenkacloud-local", GCPProjectNumber: "100000000001", GCPOwner: "simulator-admin", AzureAccount: "tenkacloudlocal", AzureOwner: "simulator-admin"}
}

// gcpLocations are the Google Cloud locations the catalog accepts.
var gcpLocations = []string{"global", "us-central1", "us-east1", "us-west1", "europe-west1", "asia-northeast1"}

func registerCloudStorage(db *sql.DB, registry *provider.Registry, cfg CloudStorageConfig) (*gcpcatalog.Engine, error) {
	gcp, err := gcpstorage.NewProjectHandler(db, gcpstorage.FactoryConfig{
		Environment: cfg.Environment,
		Projects:    []gcpstorage.ProjectBinding{{Project: gcpstorage.Project{ID: cfg.GCPProject, Number: cfg.GCPProjectNumber}, OwnerSubject: cfg.GCPOwner}},
		ExtractSubject: func(ctx context.Context) (string, bool) {
			actor, ok := provider.ActorFromContext(ctx)
			return actor.ExerciseIdentity, ok
		},
	})
	if err != nil {
		return nil, err
	}
	// The GCP catalog shares the provider route and the configured project
	// owner; it claims only its registered API names.
	catalog, err := gcpcatalog.New(db, gcpcatalog.Config{Project: cfg.GCPProject, ProjectNumber: cfg.GCPProjectNumber, Owner: cfg.GCPOwner, Locations: gcpLocations,
		Subject: func(ctx context.Context) (string, bool) {
			actor, ok := provider.ActorFromContext(ctx)
			return actor.ExerciseIdentity, ok
		}}, gcpcatalog.APIs())
	if err != nil {
		return nil, err
	}
	storage := gcp
	gcp = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if catalog.Owns(r) {
			catalog.ServeHTTP(w, r)
			return
		}
		storage.ServeHTTP(w, r)
	})
	azure, err := azureblob.NewOwnedHandler(db, azureblob.Config{Account: cfg.AzureAccount, Owner: cfg.AzureOwner, Environment: cfg.Environment}, func(ctx context.Context) (azureblob.AuthenticatedActor, bool) {
		actor, ok := provider.ActorFromContext(ctx)
		return azureblob.AuthenticatedActor{ExerciseIdentity: actor.ExerciseIdentity}, ok
	})
	if err != nil {
		return nil, err
	}
	for _, entry := range []struct {
		name    string
		handler http.Handler
	}{{"gcp", gcp}, {"azure", azure}} {
		if err = registry.Register(entry.name, entry.handler); err != nil {
			return nil, err
		}
	}
	return catalog, nil
}

// gcpCatalogDescriptorsPath serves the full descriptors of every reviewed
// Google API, apart from the bounded capability document.
const gcpCatalogDescriptorsPath = "/_simulator/v1/catalog/gcp"

// gcpCatalogDescriptors serves the full Google API catalog descriptors that
// first-party clients use to build native requests.
func gcpCatalogDescriptors(engine *gcpcatalog.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"apis": engine.Describe()})
	}
}

// gcpCatalogCapabilities advertises the reviewed Google APIs. Every other
// official method of these APIs returns UNIMPLEMENTED. The capability
// document carries compact summaries; full descriptors (methods,
// collections, fields) are served by gcpCatalogDescriptorsPath so the
// capability document stays bounded as APIs are added.
func gcpCatalogCapabilities(engine *gcpcatalog.Engine, cfg CloudStorageConfig) map[string]any {
	summaries := []map[string]any{}
	for _, d := range engine.Describe() {
		summaries = append(summaries, map[string]any{"name": d.Name, "version": d.Version, "title": d.Title, "issues": d.Issues,
			"methods": len(d.Methods), "officialMethods": d.Official, "endpoint": d.Endpoint, "reference": "/_simulator/docs/gcp-catalog/" + d.Name + "." + d.Version + ".json"})
	}
	return map[string]any{"apis": summaries, "descriptors": gcpCatalogDescriptorsPath, "project": cfg.GCPProject, "projectNumber": cfg.GCPProjectNumber,
		"locations": gcpLocations, "zones": gcpcatalog.Zones(gcpLocations),
		"endpoint": "/_simulator/providers/gcp/{api}/", "reference": "/_simulator/docs/gcp-catalog.html",
		"authorization": "Local configured project owner only; not Google IAM", "operations": "long-running operations complete synchronously and stay readable through operations.get/list",
		"state": "configuration metadata in provider-owned SQLite; no workload, network, data plane or real Google Cloud call"}
}

// Keep provider scopes separate from AWS service/action metadata.
func cloudStorageCapabilities(cfg CloudStorageConfig) map[string]any {
	return map[string]any{
		"gcp":   map[string]any{"service": "Cloud Storage", "project": cfg.GCPProject, "projectNumber": cfg.GCPProjectNumber, "endpoint": "/_simulator/providers/gcp", "operations": []string{"buckets.insert", "buckets.list", "buckets.get", "buckets.delete", "objects.insert", "objects.list", "objects.get", "objects.delete"}, "authorization": "Local configured project owner only; not Google IAM", "unsupported": []string{"Google credentials/OAuth", "ACLs", "resumable uploads", "cloud project provisioning"}},
		"azure": map[string]any{"service": "Blob Storage", "account": cfg.AzureAccount, "endpoint": "/_simulator/providers/azure/" + cfg.AzureAccount, "apiVersion": azureblob.APIVersion, "operations": []string{"CreateContainer", "ListContainers", "GetContainerProperties", "DeleteContainer", "PutBlob", "GetBlob", "GetBlobProperties", "ListBlobs", "DeleteBlob"}, "authorization": "Local configured account owner only; not Azure RBAC", "unsupported": []string{"Azure credentials/Entra/SharedKey/SAS", "ARM storage account provisioning", "block staging/commit", "asynchronous container deletion"}},
	}
}
