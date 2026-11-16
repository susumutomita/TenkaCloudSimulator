//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestbundle"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestBattleLoaderPinnedInertAssetsAndCleanupDebt(t *testing.T) {
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	manifest := guestbundle.Manifest{OS: runtime.GOOS, Arch: runtime.GOARCH}
	for index, name := range []string{"qemu", "firmware", "disk"} {
		data := []byte("inert test bytes, never executed: " + name)
		digest := sha256.Sum256(data)
		manifest.Files = append(manifest.Files, guestbundle.File{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Executable: index == 0})
		if e := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); e != nil {
			t.Fatal(e)
		}
		if _, e := tarWriter.Write(data); e != nil {
			t.Fatal(e)
		}
	}
	if e := tarWriter.Close(); e != nil {
		t.Fatal(e)
	}
	if e := gzipWriter.Close(); e != nil {
		t.Fatal(e)
	}
	archiveHash := sha256.Sum256(archive.Bytes())
	manifest.ArchiveSHA256 = hex.EncodeToString(archiveHash[:])
	manifestBytes, _ := json.Marshal(manifest)
	manifestHash := sha256.Sum256(manifestBytes)
	manifestPath := filepath.Join(dir, "manifest.json")
	archivePath := filepath.Join(dir, "assets.tar.gz")
	if e := os.WriteFile(manifestPath, manifestBytes, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(archivePath, archive.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	cfg := battleRuntimeFile{BundleManifest: manifestPath, BundleArchive: archivePath, ManifestSHA256: hex.EncodeToString(manifestHash[:]), RuntimeDirectory: dir, ImageIDs: []string{"ami-12345678"}, LifetimeUntilMs: now.Add(time.Hour).UnixMilli()}
	cfg.Layout.Emulator = "qemu"
	cfg.Layout.Firmware = "firmware"
	cfg.Layout.BaseDisk = "disk"
	cfg.Clock.Domain = "test-domain"
	cfg.Clock.ValidFromMs = now.Add(-time.Minute).UnixMilli()
	cfg.Clock.ValidUntilMs = now.Add(2 * time.Hour).UnixMilli()
	configPath := filepath.Join(dir, "runtime.json")
	raw, _ := json.Marshal(cfg)
	if e := os.WriteFile(configPath, raw, 0600); e != nil {
		t.Fatal(e)
	}
	calls := 0
	blocked := true
	wanted := errors.New("retained cleanup debt")
	assetPath := ""
	create := func(_ string, _ server.ProviderConfig, _ string, _ guesttcp.HTTPProbeCommandClock, config aws.EC2InstanceRuntimeConfig) (http.Handler, func() error, error) {
		calls++
		var e error
		assetPath, e = config.Bundle.Path("qemu")
		if e != nil {
			t.Fatal(e)
		}
		return http.NotFoundHandler(), func() error {
			if blocked {
				return wanted
			}
			return nil
		}, nil
	}
	_, closeState, e := loadBattleServer(context.Background(), configPath, "http://127.0.0.1:4566", ":memory:", server.DefaultProviderConfig(), create)
	if e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("factory not reached once")
	}
	if !errors.Is(closeState(), wanted) {
		t.Fatal("debt lost")
	}
	if _, e = os.Stat(assetPath); e != nil {
		t.Fatal("bundle removed with unresolved cleanup")
	}
	blocked = false
	if e = closeState(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(assetPath); !os.IsNotExist(e) {
		t.Fatal("positive cleanup did not retire bundle")
	}
	// A modified manifest never reaches the injected inert constructor.
	if e = os.WriteFile(manifestPath, append(manifestBytes, ' '), 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = loadBattleServer(context.Background(), configPath, "http://127.0.0.1:4566", ":memory:", server.DefaultProviderConfig(), create); e == nil {
		t.Fatal("manifest pin bypass")
	}
	if calls != 1 {
		t.Fatal("invalid config reached factory")
	}
}
