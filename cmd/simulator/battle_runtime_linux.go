//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestbundle"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestvm"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/probeclock"
)

type battleRuntimeFile struct {
	BundleManifest     string `json:"bundleManifest"`
	BundleArchive      string `json:"bundleArchive"`
	ManifestSHA256     string `json:"manifestSha256"`
	SessionImageSHA256 string `json:"sessionImageSha256,omitempty"`
	RuntimeDirectory   string `json:"runtimeDirectory"`
	Layout             struct {
		Emulator string `json:"emulator"`
		Firmware string `json:"firmware"`
		BaseDisk string `json:"baseDisk"`
	} `json:"layout"`
	ImageIDs        []string `json:"imageIds"`
	LifetimeUntilMs int64    `json:"lifetimeUntilMs"`
	Clock           struct {
		Domain       string `json:"domain"`
		ValidFromMs  int64  `json:"validFromMs"`
		ValidUntilMs int64  `json:"validUntilMs"`
	} `json:"clock"`
}

func strictRuntimeJSON(raw []byte, target any) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid runtime UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 8 {
			return errors.New("runtime JSON depth")
		}
		v, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := v.(json.Delim)
		if !ok {
			return nil
		}
		if delim == '{' {
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				name, ok := k.(string)
				if !ok || seen[strings.ToLower(name)] {
					return errors.New("duplicate runtime key")
				}
				seen[strings.ToLower(name)] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		}
		if delim == '[' {
			for d.More() {
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		}
		return errors.New("invalid delimiter")
	}
	if e := walk(0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing runtime JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(target)
}
func boundedRegular(path string, limit int64, private bool) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("absolute canonical runtime path required")
	}
	info, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Size() > limit || info.Size() < 0 || (private && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("invalid runtime file")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	actual, e := f.Stat()
	if e != nil || !os.SameFile(info, actual) {
		return nil, errors.New("runtime file changed")
	}
	raw, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(raw)) > limit {
		return nil, errors.New("runtime file too large")
	}
	return raw, nil
}
func readBattleRuntimeFile(path string, now time.Time) (battleRuntimeFile, error) {
	var cfg battleRuntimeFile
	raw, e := boundedRegular(path, 65536, true)
	if e != nil {
		return cfg, e
	}
	if e = strictRuntimeJSON(raw, &cfg); e != nil {
		return cfg, e
	}
	hash, e := hex.DecodeString(cfg.ManifestSHA256)
	if e != nil || len(hash) != 32 || strings.ToLower(cfg.ManifestSHA256) != cfg.ManifestSHA256 {
		return cfg, errors.New("invalid manifest pin")
	}
	if cfg.SessionImageSHA256 != "" {
		pin, err := hex.DecodeString(cfg.SessionImageSHA256)
		if err != nil || len(pin) != 32 || strings.ToLower(cfg.SessionImageSHA256) != cfg.SessionImageSHA256 {
			return cfg, errors.New("invalid session image pin")
		}
	}
	if !filepath.IsAbs(cfg.RuntimeDirectory) || filepath.Clean(cfg.RuntimeDirectory) != cfg.RuntimeDirectory {
		return cfg, errors.New("absolute private runtime directory required")
	}
	st, e := os.Lstat(cfg.RuntimeDirectory)
	if e != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return cfg, errors.New("private nonsymlink runtime directory required")
	}
	if cfg.LifetimeUntilMs <= now.UnixMilli() || cfg.LifetimeUntilMs > now.Add(24*time.Hour).UnixMilli() || cfg.Clock.ValidFromMs < 1 || cfg.Clock.ValidFromMs > now.UnixMilli() || cfg.Clock.ValidUntilMs < cfg.LifetimeUntilMs || cfg.Clock.ValidUntilMs > 1<<53-1 || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(cfg.Clock.Domain) {
		return cfg, errors.New("invalid bounded runtime lifetime or clock domain")
	}
	if len(cfg.ImageIDs) == 0 || len(cfg.ImageIDs) > 16 {
		return cfg, errors.New("explicit image aliases required")
	}
	seen := map[string]bool{}
	for _, id := range cfg.ImageIDs {
		if !regexp.MustCompile(`^ami-[a-f0-9]{8,32}$`).MatchString(id) || seen[id] {
			return cfg, errors.New("invalid image alias")
		}
		seen[id] = true
	}
	layout := []string{cfg.Layout.Emulator, cfg.Layout.Firmware, cfg.Layout.BaseDisk}
	for _, name := range layout {
		if !fs.ValidPath(name) || name == "." {
			return cfg, errors.New("invalid bundle layout")
		}
	}
	if layout[0] == layout[1] || layout[0] == layout[2] || layout[1] == layout[2] {
		return cfg, errors.New("overlapping bundle roles")
	}
	return cfg, nil
}
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
