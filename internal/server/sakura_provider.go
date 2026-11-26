package server

import (
	"database/sql"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
	"github.com/susumutomita/TenkaCloudSimulator/internal/sakura"
	"net"
	"net/http"
)

// ProviderConfig preserves the existing cloud storage configuration and enables
// Sakura IaaS metadata only with an explicit nonnil, fully specified scope.
type ProviderConfig struct {
	configureInstances func(*sql.DB, *aws.StackStore) error
	closeInstances     func() error
	battleCapabilities http.Handler
	terminalSessions   http.Handler
	commandAPI         func() *aws.SSMCommandAPI
	CloudStorage       CloudStorageConfig
	SakuraDisks        *sakura.Config
	// SakuraServerConsole is an opt-in read-only presentation switch. Only bind authenticated numeric loopback when enabled.
	SakuraServerConsole bool
	// CognitoLocalFederationConfig admits only direct, signed loopback
	// configuration. It does not enable a sign-in surface.
	CognitoLocalFederationConfig bool
	// RDSPrivatePostgresTopology enables native private-network validation of
	// qualifying RDS metadata writes, not database execution or a listener.
	RDSPrivatePostgresTopology bool
	ownedPostgres              *ownedPostgresOperator
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
func addSakuraCapabilities(base map[string]any, scope *sakura.Config, serverConsole bool) map[string]any {
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
	// A separate read surface lets a Server view discover the native endpoint
	// without treating the legacy Sakura Disk capability as a Server contract.
	base["sakuraServer"] = map[string]any{
		"service":              "IaaS Server configuration",
		"apiVersion":           "1.1",
		"zone":                 scope.Zone,
		"endpoint":             "/_simulator/providers/sakura/cloud/zone/" + scope.Zone + "/api/cloud/1.1/server",
		"operations":           []string{"GET /server", "GET /server/{id}"},
		"authorization":        "Explicit local configured owner through authenticated typed context; fixed local Basic marker is not authentication",
		"storage":              "SQLite configuration metadata only; no VM",
		"availabilityReported": false,
		"console":              serverConsole,
		"sdkVerified":          false,
	}
	return base
}

// sakuraServerConsoleVisible never authenticates, routes or authorizes data.
// Require the ordinary authenticated typed Actor; HTTP headers are not proof.
func sakuraServerConsoleVisible(r *http.Request, scope *sakura.Config, enabled bool) bool {
	if !enabled || scope == nil || consoleScopeFromRequest(r) != nil {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return false
	}
	actor, ok := provider.ActorFromContext(r.Context())
	return ok && actor.ExerciseIdentity == scope.Owner
}
