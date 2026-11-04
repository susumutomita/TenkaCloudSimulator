package server

import (
	"context"
	"database/sql"
	"net/http"

	azureblob "github.com/susumutomita/TenkaCloudSimulator/internal/azure/blob"
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

func registerCloudStorage(db *sql.DB, registry *provider.Registry, cfg CloudStorageConfig) error {
	gcp, err := gcpstorage.NewProjectHandler(db, gcpstorage.FactoryConfig{
		Environment: cfg.Environment,
		Projects:    []gcpstorage.ProjectBinding{{Project: gcpstorage.Project{ID: cfg.GCPProject, Number: cfg.GCPProjectNumber}, OwnerSubject: cfg.GCPOwner}},
		ExtractSubject: func(ctx context.Context) (string, bool) {
			actor, ok := provider.ActorFromContext(ctx)
			return actor.ExerciseIdentity, ok
		},
	})
	if err != nil {
		return err
	}
	azure, err := azureblob.NewOwnedHandler(db, azureblob.Config{Account: cfg.AzureAccount, Owner: cfg.AzureOwner, Environment: cfg.Environment}, func(ctx context.Context) (azureblob.AuthenticatedActor, bool) {
		actor, ok := provider.ActorFromContext(ctx)
		return azureblob.AuthenticatedActor{ExerciseIdentity: actor.ExerciseIdentity}, ok
	})
	if err != nil {
		return err
	}
	for _, entry := range []struct {
		name    string
		handler http.Handler
	}{{"gcp", gcp}, {"azure", azure}} {
		if err = registry.Register(entry.name, entry.handler); err != nil {
			return err
		}
	}
	return nil
}

// Keep provider scopes separate from AWS service/action metadata.
func cloudStorageCapabilities(cfg CloudStorageConfig) map[string]any {
	return map[string]any{
		"gcp":   map[string]any{"service": "Cloud Storage", "project": cfg.GCPProject, "projectNumber": cfg.GCPProjectNumber, "endpoint": "/_simulator/providers/gcp", "operations": []string{"buckets.insert", "buckets.list", "buckets.get", "buckets.delete", "objects.insert", "objects.list", "objects.get", "objects.delete"}, "authorization": "Local configured project owner only; not Google IAM", "unsupported": []string{"Google credentials/OAuth", "ACLs", "resumable uploads", "cloud project provisioning"}},
		"azure": map[string]any{"service": "Blob Storage", "account": cfg.AzureAccount, "endpoint": "/_simulator/providers/azure/" + cfg.AzureAccount, "apiVersion": azureblob.APIVersion, "operations": []string{"CreateContainer", "ListContainers", "GetContainerProperties", "DeleteContainer", "PutBlob", "GetBlob", "GetBlobProperties", "ListBlobs", "DeleteBlob"}, "authorization": "Local configured account owner only; not Azure RBAC", "unsupported": []string{"Azure credentials/Entra/SharedKey/SAS", "ARM storage account provisioning", "block staging/commit", "asynchronous container deletion"}},
	}
}
