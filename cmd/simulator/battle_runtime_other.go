//go:build !linux

package main

import (
	"context"
	"errors"
	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/probeclock"
	"net/http"
)

func newBattleServer(context.Context, string, string, string, server.ProviderConfig, ...*probeclock.Clock) (http.Handler, func() error, error) {
	return nil, nil, errors.New("Battle runtime requires Linux/amd64")
}

func validateBattlePlatform() error { return errors.New("Battle runtime requires Linux/amd64") }
