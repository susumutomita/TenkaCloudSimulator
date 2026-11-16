package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBattleRuntimeConfigurationValidationOnly(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	now := time.UnixMilli(1700000000000)
	cfg := battleRuntimeFile{BundleManifest: filepath.Join(dir, "manifest.json"), BundleArchive: filepath.Join(dir, "assets.tar.gz"), ManifestSHA256: strings.Repeat("a", 64), RuntimeDirectory: dir, ImageIDs: []string{"ami-12345678"}, LifetimeUntilMs: now.Add(time.Hour).UnixMilli()}
	cfg.Layout.Emulator = "qemu"
	cfg.Layout.Firmware = "firmware"
	cfg.Layout.BaseDisk = "disk"
	cfg.Clock.Domain = "operator-qualified"
	cfg.Clock.ValidFromMs = now.UnixMilli()
	cfg.Clock.ValidUntilMs = now.Add(2 * time.Hour).UnixMilli()
	file := filepath.Join(dir, "config.json")
	write := func(c battleRuntimeFile) {
		raw, e := json.Marshal(c)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(file, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write(cfg)
	if _, e := readBattleRuntimeFile(file, now); e != nil {
		t.Fatal(e)
	}
	long := cfg
	long.LifetimeUntilMs = 0
	long.Clock.ValidUntilMs = now.Add(9 * time.Hour).UnixMilli()
	write(long)
	if got, e := readBattleRuntimeFile(file, now); e != nil || got.LifetimeUntilMs != now.Add(8*time.Hour).UnixMilli() {
		t.Fatalf("default Battle lifetime not applied: %v %v", got.LifetimeUntilMs, e)
	}
	long.LifetimeUntilMs = now.Add(25 * time.Hour).UnixMilli()
	long.Clock.ValidUntilMs = now.Add(26 * time.Hour).UnixMilli()
	write(long)
	if _, e := readBattleRuntimeFile(file, now); e == nil {
		t.Fatal("lifetime beyond the bounded maximum accepted")
	}
	// No archive, execution, listener, or bundle extraction is used by this test.
	for _, change := range []func(*battleRuntimeFile){func(c *battleRuntimeFile) { c.LifetimeUntilMs = now.UnixMilli() }, func(c *battleRuntimeFile) { c.ManifestSHA256 = "bad" }, func(c *battleRuntimeFile) { c.Layout.Emulator = c.Layout.BaseDisk }, func(c *battleRuntimeFile) { c.ImageIDs = []string{"arbitrary-url"} }, func(c *battleRuntimeFile) { c.Clock.Domain = "bad domain" }} {
		changed := cfg
		change(&changed)
		write(changed)
		if _, e := readBattleRuntimeFile(file, now); e == nil {
			t.Fatal("invalid operator config accepted")
		}
	}
	write(cfg)
	if e := os.Chmod(file, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := readBattleRuntimeFile(file, now); e == nil {
		t.Fatal("public config accepted")
	}
	link := filepath.Join(dir, "link")
	if e := os.Symlink(dir, link); e != nil {
		t.Fatal(e)
	}
	cfg.RuntimeDirectory = link
	if e := os.Chmod(file, 0600); e != nil {
		t.Fatal(e)
	}
	write(cfg)
	if _, e := readBattleRuntimeFile(file, now); e == nil {
		t.Fatal("symlink runtime accepted")
	}
}
func TestBattleRuntimeStrictJSONAndUnsafeFlags(t *testing.T) {
	for _, raw := range []string{`{"clock":{"domain":"a","domain":"b"}}`, `{"imageIds":[],"ImageIDs":[]}`, `{"unknown":true}`, `{} {}`, "{\"x\":\"\xff\"}"} {
		var cfg battleRuntimeFile
		if strictRuntimeJSON([]byte(raw), &cfg) == nil {
			t.Fatalf("ambiguous configuration accepted %q", raw)
		}
	}
	if e := run(context.Background(), []string{"--battle-runtime-config", "/never-opened", "--unsafe-no-auth"}, io.Discard); e == nil || !strings.Contains(e.Error(), "authenticated numeric loopback") {
		t.Fatal("unsafe Battle selection not rejected before listener")
	}
}

func TestSharedBattleAdmissionDoesNotEnableUnsupportedRuntime(t *testing.T) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		t.Skip("unsupported-platform assertion")
	}
	if validateBattlePlatform() == nil {
		t.Fatal("configuration portability enabled unsupported runtime")
	}
}
