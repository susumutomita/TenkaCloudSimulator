package main

import (
	"errors"
	"net"

	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
)

func configureOwnedPostgres(options server.ProviderConfig, configPath, statePath string, ip net.IP, noAuth, remote bool) (server.ProviderConfig, error) {
	if configPath == "" {
		return options, nil
	}
	if ip == nil || !ip.IsLoopback() || noAuth || remote {
		return server.ProviderConfig{}, errors.New("--rds-owned-postgres-config requires authenticated numeric loopback")
	}
	if !options.RDSPrivatePostgresTopology {
		return server.ProviderConfig{}, errors.New("--rds-owned-postgres-config requires --rds-private-postgres-topology")
	}
	raw, err := boundedRegular(configPath, 4096, true)
	if err != nil {
		return server.ProviderConfig{}, errors.New("owned PostgreSQL config must be an absolute regular private file of at most 4096 bytes")
	}
	var selected server.OwnedPostgresOperatorConfig
	if err := strictRuntimeJSON(raw, &selected); err != nil {
		return server.ProviderConfig{}, errors.New("invalid owned PostgreSQL config")
	}
	return server.WithOwnedPostgresOperator(options, statePath, selected)
}
