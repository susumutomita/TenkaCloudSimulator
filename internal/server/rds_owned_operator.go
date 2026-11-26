package server

import "errors"

var (
	errOwnedPostgresTopology        = errors.New("owned PostgreSQL requires explicit private RDS topology validation")
	errOwnedPostgresAlreadySelected = errors.New("owned PostgreSQL is already selected")
	errOwnedPostgresBattleConflict  = errors.New("owned PostgreSQL and Battle runtime cannot be enabled together")
	errOwnedPostgresConfig          = errors.New("owned PostgreSQL requires one explicit bundle, independently approved pin, work root, region and database identifier")
	errOwnedPostgresPlatform        = errors.New("owned PostgreSQL requires Linux/amd64 and a non-root operator")
)

// OwnedPostgresOperatorConfig is a trusted local startup configuration, not an
// AWS request type. BundleSHA256 is supplied independently of BundleDirectory.
type OwnedPostgresOperatorConfig struct {
	BundleDirectory string `json:"bundleDirectory"`
	BundleSHA256    string `json:"bundleSha256"`
	WorkRoot        string `json:"workRoot"`
	Region          string `json:"region"`
	Identifier      string `json:"dbInstanceIdentifier"`
}

// WithOwnedPostgresOperator opts into the existing provider-owned runtime for
// one nominated instance. The caller must serve only authenticated numeric
// loopback. It does not create a handler, workload, credential, or endpoint.
func WithOwnedPostgresOperator(options ProviderConfig, statePath string, settings OwnedPostgresOperatorConfig) (ProviderConfig, error) {
	if options.ownedPostgres != nil {
		return ProviderConfig{}, errOwnedPostgresAlreadySelected
	}
	if !options.RDSPrivatePostgresTopology {
		return ProviderConfig{}, errOwnedPostgresTopology
	}
	if settings.BundleDirectory == "" || settings.BundleSHA256 == "" || settings.WorkRoot == "" || settings.Region == "" || settings.Identifier == "" {
		return ProviderConfig{}, errOwnedPostgresConfig
	}
	owned, err := admitOwnedPostgresOperator(statePath, settings)
	if err != nil {
		return ProviderConfig{}, err
	}
	options.ownedPostgres = owned
	return options, nil
}
