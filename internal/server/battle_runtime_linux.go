//go:build linux

package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/probeclock"
)

// NewWithBattleRuntime installs a concrete provider runtime on the same database
// as CloudFormation. This explicit operator constructor does not run a VM;
// supported configuration is distinct from each deployment's physical readiness.
// Qualified Host clock bounds remain a separately supplied Host prerequisite.
func NewWithBattleRuntime(path string, options ProviderConfig, origin string, clock guesttcp.HTTPProbeCommandClock, config aws.EC2InstanceRuntimeConfig) (http.Handler, func() error, error) {
	return newWithBattleRuntime(path, options, origin, clock, config, nil)
}
func NewWithMonotonicBattleRuntime(path string, options ProviderConfig, origin string, clock guesttcp.HTTPProbeCommandClock, config aws.EC2InstanceRuntimeConfig, monotonic *probeclock.Clock) (http.Handler, func() error, error) {
	if monotonic == nil {
		return nil, nil, errors.New("owned monotonic clock required")
	}
	return newWithBattleRuntime(path, options, origin, clock, config, monotonic)
}
func newWithBattleRuntime(path string, options ProviderConfig, origin string, clock guesttcp.HTTPProbeCommandClock, config aws.EC2InstanceRuntimeConfig, monotonic *probeclock.Clock) (http.Handler, func() error, error) {
	if config.OnReady != nil || options.configureInstances != nil || options.battleCapabilities != nil || options.commandAPI != nil {
		return nil, nil, errors.New("Battle runtime already configured")
	}
	registry, err := NewBattleCapabilityRegistry(origin, clock)
	if err != nil {
		return nil, nil, err
	}
	registry.monotonic = monotonic
	if monotonic != nil {
		registry.clock.Domain = monotonic.Domain()
	}
	var runtime *aws.EC2InstanceRuntime
	var commands *aws.SSMCommandAPI
	options.commandAPI = func() *aws.SSMCommandAPI { return commands }
	terminal := &terminalHandler{}
	if config.SessionImageSHA256 != "" {
		options.terminalSessions = terminal
	}
	options.configureInstances = func(db *sql.DB, stacks *aws.StackStore) (setupErr error) {
		defer func() {
			if setupErr != nil && runtime != nil {
				commands = nil
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				setupErr = errors.Join(setupErr, runtime.Shutdown(ctx))
			}
		}()
		network, e := aws.NewEC2NetworkStore(db)
		if e != nil {
			return e
		}
		config.OnReady = registry.RegisterProviderReceipt
		runtime, e = aws.NewEC2InstanceRuntime(db, network, config)
		if e != nil {
			return e
		}
		commandStore, _, commandErr := aws.NewCanonicalSSMCommandStore(runtime)
		if commandErr != nil {
			return commandErr
		}
		commands, commandErr = aws.NewSSMCommandAPI(commandStore)
		if commandErr != nil {
			return commandErr
		}
		if e = stacks.ConfigureInstanceRuntime(runtime); e != nil {
			return e
		}
		if config.SessionImageSHA256 != "" {
			backend, err := aws.NewCanonicalSSMSessionBackend(runtime)
			if err != nil {
				return err
			}
			base, err := url.Parse(origin)
			if err != nil {
				return err
			}
			if base.Scheme == "https" {
				base.Scheme = "wss"
			} else {
				base.Scheme = "ws"
			}
			base.Path = "/_simulator/v1/terminal-channel"
			terminal.store, err = aws.NewSSMSessionStore(db, backend, base.String())
			if err != nil {
				return err
			}
		}
		registry.mu.Lock()
		registry.driver = runtime
		registry.mu.Unlock()
		return nil
	}
	options.closeInstances = func() error {
		if runtime == nil {
			return nil
		}
		var sessionErr error
		if terminal.store != nil {
			sessionCtx, sessionCancel := context.WithTimeout(context.Background(), 8*time.Second)
			sessionErr = terminal.store.Shutdown(sessionCtx)
			sessionCancel()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		return errors.Join(sessionErr, runtime.Shutdown(ctx))
	}
	options.battleCapabilities = registry
	return NewWithProviders(path, options)
}
