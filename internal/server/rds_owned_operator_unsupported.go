//go:build !linux || !amd64

package server

import (
	"database/sql"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
)

// No production constructor can set this on an unsupported target.
type ownedPostgresOperator struct{}

func admitOwnedPostgresOperator(string, OwnedPostgresOperatorConfig) (*ownedPostgresOperator, error) {
	return nil, errOwnedPostgresPlatform
}

func (*ownedPostgresOperator) checkState(string) error { return errOwnedPostgresPlatform }

func (*ownedPostgresOperator) open(*sql.DB, catalog.Config) (*catalog.Engine, func() error, error) {
	return nil, nil, errOwnedPostgresPlatform
}
