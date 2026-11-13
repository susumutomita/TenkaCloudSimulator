//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestbundle"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestvm"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/probeclock"
)

func validateBattlePlatform() error {
	if runtime.GOARCH != "amd64" {
		return errors.New("Battle runtime requires Linux/amd64")
	}
	return nil
}

type battleServerConstructor func(string, server.ProviderConfig, string, guesttcp.HTTPProbeCommandClock, aws.EC2InstanceRuntimeConfig) (http.Handler, func() error, error)

func newBattleServer(ctx context.Context, path, origin, state string, options server.ProviderConfig, clocks ...*probeclock.Clock) (http.Handler, func() error, error) {
	if len(clocks) > 1 {
		return nil, nil, errors.New("invalid clock selection")
	}
	if len(clocks) == 1 && clocks[0] != nil {
		return loadBattleServer(ctx, path, origin, state, options, func(p string, o server.ProviderConfig, u string, c guesttcp.HTTPProbeCommandClock, a aws.EC2InstanceRuntimeConfig) (http.Handler, func() error, error) {
			return server.NewWithMonotonicBattleRuntime(p, o, u, c, a, clocks[0])
		})
	}
	return loadBattleServer(ctx, path, origin, state, options, server.NewWithBattleRuntime)
}
func loadBattleServer(ctx context.Context, path, origin, state string, options server.ProviderConfig, create battleServerConstructor) (http.Handler, func() error, error) {
	if runtime.GOARCH != "amd64" {
		return nil, nil, errors.New("Battle runtime requires Linux/amd64")
	}
	cfg, e := readBattleRuntimeFile(path, time.Now())
	if e != nil {
		return nil, nil, e
	}
	raw, e := boundedRegular(cfg.BundleManifest, guestbundle.MaxManifestBytes, false)
	if e != nil {
		return nil, nil, e
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != cfg.ManifestSHA256 {
		return nil, nil, errors.New("bundle manifest pin mismatch")
	}
	var manifest guestbundle.Manifest
	if e = strictRuntimeJSON(raw, &manifest); e != nil {
		return nil, nil, e
	}
	info, e := os.Lstat(cfg.BundleArchive)
	if e != nil || !info.Mode().IsRegular() || info.Size() > guestbundle.MaxArchiveBytes || !filepath.IsAbs(cfg.BundleArchive) || filepath.Clean(cfg.BundleArchive) != cfg.BundleArchive {
		return nil, nil, errors.New("invalid pinned archive")
	}
	archive, e := os.Open(cfg.BundleArchive)
	if e != nil {
		return nil, nil, e
	}
	actual, e := archive.Stat()
	if e != nil || !os.SameFile(info, actual) {
		_ = archive.Close()
		return nil, nil, errors.New("archive changed")
	}
	lifetime, cancel := context.WithDeadline(ctx, time.UnixMilli(cfg.LifetimeUntilMs))
	bundle, e := guestbundle.Extract(lifetime, cfg.RuntimeDirectory, archive, manifest)
	closeErr := archive.Close()
	if e != nil || closeErr != nil {
		cancel()
		if bundle != nil {
			_ = bundle.Close()
		}
		return nil, nil, errors.Join(e, closeErr)
	}
	for i, name := range []string{cfg.Layout.Emulator, cfg.Layout.Firmware, cfg.Layout.BaseDisk} {
		asset, err := bundle.Info(name)
		if err != nil || asset.Executable != (i == 0) {
			cancel()
			_ = bundle.Close()
			return nil, nil, errors.New("bundle role mismatch")
		}
	}
	if cfg.SessionImageSHA256 != "" {
		asset, err := bundle.Info(cfg.Layout.BaseDisk)
		if err != nil || asset.SHA256 != cfg.SessionImageSHA256 {
			cancel()
			_ = bundle.Close()
			return nil, nil, errors.New("session image pin does not match verified BaseDisk")
		}
	}
	handler, closeState, e := create(state, options, origin, guesttcp.HTTPProbeCommandClock{Domain: cfg.Clock.Domain, ValidFromMs: cfg.Clock.ValidFromMs, ValidUntilMs: cfg.Clock.ValidUntilMs}, aws.EC2InstanceRuntimeConfig{Lifetime: lifetime, Bundle: bundle, Layout: guestvm.Layout{Emulator: cfg.Layout.Emulator, Firmware: cfg.Layout.Firmware, BaseDisk: cfg.Layout.BaseDisk}, Directory: cfg.RuntimeDirectory, ImageIDs: cfg.ImageIDs, SessionImageSHA256: cfg.SessionImageSHA256})
	if e != nil {
		cancel()
		_ = bundle.Close()
		return nil, nil, e
	}
	return handler, func() error {
		cancel()
		if e := closeState(); e != nil {
			return e
		}
		return bundle.Close()
	}, nil
}
