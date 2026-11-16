//go:build !linux && !darwin

package main

import (
	"context"
	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/probeclock"
	"net/http"
)

func newBattleServer(context.Context, string, string, string, server.ProviderConfig, ...*probeclock.Clock) (http.Handler, func() error, error) {
	return nil, nil, errBattlePlatform
}

func validateBattlePlatform() error { return errBattlePlatform }
