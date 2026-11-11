package server

import (
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/sakura"
)

// ProviderConfig preserves the existing cloud storage configuration and enables
// Sakura Disk metadata only with an explicit nonnil, fully specified scope.
type ProviderConfig struct {
	CloudStorage CloudStorageConfig
	SakuraDisks  *sakura.Config
	// BedrockRuntime is the trusted, operator-built local inference adapter.
	// nil keeps Bedrock Runtime inference disabled with explicit errors. The
	// caller owns it and closes it; closing the composition does not.
	BedrockRuntime *aws.BedrockRuntime
}

func DefaultProviderConfig() ProviderConfig {
	return ProviderConfig{CloudStorage: DefaultCloudStorageConfig()}
}
func validatedSakuraScope(scope *sakura.Config) (*sakura.Config, error) {
	if scope == nil {
		return nil, nil
	}
	copy := *scope
	if err := sakura.ValidateConfig(copy); err != nil {
		return nil, err
	}
	return &copy, nil
}
func addSakuraCapabilities(base map[string]any, scope *sakura.Config) map[string]any {
	if scope == nil {
		return base
	}
	base["sakura"] = map[string]any{
		"service": "IaaS Disk configuration", "apiVersion": "1.1",
		"endpoint":             "/_simulator/providers/sakura/cloud/zone/" + scope.Zone + "/api/cloud/1.1/disk",
		"operations":           []string{"GET /disk", "POST /disk", "GET /disk/{id}", "PUT /disk/{id}", "DELETE /disk/{id}"},
		"authorization":        "Explicit local configured owner through authenticated typed context; not Sakura IAM",
		"storage":              "SQLite configuration metadata only; no block device or VM",
		"availabilityReported": false, "console": true, "sdkVerified": false,
		"limits":      map[string]any{"activeDisks": 100, "sizeMB": 20480, "planID": 4, "connection": "virtio", "requestBytes": 65536, "queryBytes": 8192},
		"unsupported": []string{"VM/boot/block I/O", "copies/snapshots/archives", "encryption/KMS", "tags/icons/placement except neutral empty markers", "disk attachment/resize/config/monitor", "Sakura IAM/real credentials", "AppRun", "Object Storage control or S3 compatibility", "SDK verification"},
	}
	return base
}
