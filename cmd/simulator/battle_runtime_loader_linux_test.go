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
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestbundle"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/probeclock"
)

// inertBattleBundle writes a pinned manifest and archive of inert bytes (never
// executed) and a private runtime configuration that references them.
type inertBattleBundle struct {
	dir, configPath, manifestPath, archivePath string
	manifestBytes, archiveBytes                []byte
	diskSHA                                    string
	cfg                                        battleRuntimeFile
}

func newInertBattleBundle(t *testing.T) *inertBattleBundle {
	t.Helper()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	manifest := guestbundle.Manifest{OS: runtime.GOOS, Arch: runtime.GOARCH}
	b := &inertBattleBundle{dir: dir}
	for index, name := range []string{"qemu", "firmware", "disk"} {
		data := []byte("inert loader test bytes, never executed: " + name)
		digest := sha256.Sum256(data)
		if name == "disk" {
			b.diskSHA = hex.EncodeToString(digest[:])
		}
		manifest.Files = append(manifest.Files, guestbundle.File{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Executable: index == 0})
		if e := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); e != nil {
			t.Fatal(e)
		}
		if _, e := tw.Write(data); e != nil {
			t.Fatal(e)
		}
	}
	if e := tw.Close(); e != nil {
		t.Fatal(e)
	}
	if e := gz.Close(); e != nil {
		t.Fatal(e)
	}
	b.archiveBytes = archive.Bytes()
	archiveHash := sha256.Sum256(b.archiveBytes)
	manifest.ArchiveSHA256 = hex.EncodeToString(archiveHash[:])
	b.manifestBytes, _ = json.Marshal(manifest)
	b.manifestPath = filepath.Join(dir, "manifest.json")
	b.archivePath = filepath.Join(dir, "assets.tar.gz")
	b.configPath = filepath.Join(dir, "runtime.json")
	b.writeManifest(t, b.manifestBytes)
	if e := os.WriteFile(b.archivePath, b.archiveBytes, 0600); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	b.cfg.BundleManifest = b.manifestPath
	b.cfg.BundleArchive = b.archivePath
	b.cfg.RuntimeDirectory = dir
	b.cfg.ImageIDs = []string{"ami-12345678"}
	b.cfg.LifetimeUntilMs = now.Add(time.Hour).UnixMilli()
	b.cfg.Layout.Emulator, b.cfg.Layout.Firmware, b.cfg.Layout.BaseDisk = "qemu", "firmware", "disk"
	b.cfg.Clock.Domain = "loader-domain"
	b.cfg.Clock.ValidFromMs = now.Add(-time.Minute).UnixMilli()
	b.cfg.Clock.ValidUntilMs = now.Add(2 * time.Hour).UnixMilli()
	b.writeConfig(t, b.cfg)
	return b
}

// writeManifest stores raw manifest bytes and pins the configuration to them.
func (b *inertBattleBundle) writeManifest(t *testing.T, raw []byte) {
	t.Helper()
	if e := os.WriteFile(b.manifestPath, raw, 0600); e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(raw)
	b.cfg.ManifestSHA256 = hex.EncodeToString(digest[:])
}

func (b *inertBattleBundle) writeConfig(t *testing.T, cfg battleRuntimeFile) {
	t.Helper()
	raw, e := json.Marshal(cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(b.configPath, raw, 0600); e != nil {
		t.Fatal(e)
	}
}

// extractedEntries lists what the loader left in the private runtime directory
// beyond the operator's own inputs.
func (b *inertBattleBundle) extractedEntries(t *testing.T) []string {
	t.Helper()
	entries, e := os.ReadDir(b.dir)
	if e != nil {
		t.Fatal(e)
	}
	var left []string
	for _, entry := range entries {
		switch entry.Name() {
		case "manifest.json", "assets.tar.gz", "runtime.json", "state.db":
		default:
			left = append(left, entry.Name())
		}
	}
	return left
}

func TestNewBattleServerComposesRealRuntimeFromPinnedBundle(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "configured-clock", true: "owned-monotonic-clock"}[owned], func(t *testing.T) {
			b := newInertBattleBundle(t)
			b.cfg.SessionImageSHA256 = b.diskSHA
			b.writeConfig(t, b.cfg)
			var clocks []*probeclock.Clock
			if owned {
				var clock *probeclock.Clock
				for i := 0; i < 50 && clock == nil; i++ {
					clock, _ = probeclock.New()
				}
				if clock == nil {
					t.Fatal("owned monotonic clock unavailable")
				}
				clocks = append(clocks, clock)
			}
			h, closeState, e := newBattleServer(context.Background(), b.configPath, "http://127.0.0.1:4566", filepath.Join(b.dir, "state.db"), server.DefaultProviderConfig(), clocks...)
			if e != nil {
				t.Fatal(e)
			}
			if h == nil {
				t.Fatal("composed Battle server has no handler")
			}
			if len(b.extractedEntries(t)) == 0 {
				t.Fatal("verified bundle was not extracted into the private runtime directory")
			}
			if e = closeState(); e != nil {
				t.Fatal("composed Battle server did not shut down cleanly:", e)
			}
			if left := b.extractedEntries(t); len(left) != 0 {
				t.Fatalf("bundle not retired after clean shutdown: %v", left)
			}
		})
	}
	b := newInertBattleBundle(t)
	if _, _, e := newBattleServer(context.Background(), b.configPath, "http://127.0.0.1:4566", ":memory:", server.DefaultProviderConfig(), nil, nil); e == nil || !strings.Contains(e.Error(), "clock selection") {
		t.Fatalf("ambiguous clock selection accepted: %v", e)
	}
	if left := b.extractedEntries(t); len(left) != 0 {
		t.Fatalf("refused clock selection still extracted the bundle: %v", left)
	}
}

func TestBattleLoaderRefusesUnverifiedBundlesBeforeConstruction(t *testing.T) {
	never := func(string, server.ProviderConfig, string, guesttcp.HTTPProbeCommandClock, aws.EC2InstanceRuntimeConfig) (http.Handler, func() error, error) {
		t.Fatal("unverified bundle reached the server constructor")
		return nil, nil, nil
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, b *inertBattleBundle)
		want  string
	}{
		{"session image pin differs from verified BaseDisk", func(t *testing.T, b *inertBattleBundle) {
			b.cfg.SessionImageSHA256 = strings.Repeat("0", 64)
		}, "session image pin"},
		{"emulator role is not the executable asset", func(t *testing.T, b *inertBattleBundle) {
			b.cfg.Layout.Emulator, b.cfg.Layout.Firmware = "firmware", "qemu"
		}, "bundle role mismatch"},
		{"layout names an asset outside the manifest", func(t *testing.T, b *inertBattleBundle) {
			b.cfg.Layout.BaseDisk = "missing-disk"
		}, "bundle role mismatch"},
		{"archive bytes differ from the manifest pin", func(t *testing.T, b *inertBattleBundle) {
			if e := os.WriteFile(b.archivePath, append(append([]byte{}, b.archiveBytes...), 0), 0600); e != nil {
				t.Fatal(e)
			}
		}, ""},
		{"archive is a symlink", func(t *testing.T, b *inertBattleBundle) {
			real := filepath.Join(b.dir, "real.tar.gz")
			if e := os.Rename(b.archivePath, real); e != nil {
				t.Fatal(e)
			}
			if e := os.Symlink(real, b.archivePath); e != nil {
				t.Fatal(e)
			}
		}, "invalid pinned archive"},
		{"archive is missing", func(t *testing.T, b *inertBattleBundle) {
			if e := os.Remove(b.archivePath); e != nil {
				t.Fatal(e)
			}
		}, "invalid pinned archive"},
		{"archive path is not canonical", func(t *testing.T, b *inertBattleBundle) {
			b.cfg.BundleArchive = b.dir + "/./assets.tar.gz"
		}, "invalid pinned archive"},
		{"pinned manifest has duplicate keys", func(t *testing.T, b *inertBattleBundle) {
			raw := append([]byte(`{"os":"linux",`), b.manifestBytes[1:]...)
			b.writeManifest(t, raw)
		}, "duplicate runtime key"},
		{"manifest is missing", func(t *testing.T, b *inertBattleBundle) {
			if e := os.Remove(b.manifestPath); e != nil {
				t.Fatal(e)
			}
		}, "no such file"},
		{"runtime config is missing", func(t *testing.T, b *inertBattleBundle) {
			if e := os.Remove(b.configPath); e != nil {
				t.Fatal(e)
			}
		}, "no such file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newInertBattleBundle(t)
			tc.setup(t, b)
			if tc.name != "runtime config is missing" {
				b.writeConfig(t, b.cfg)
			}
			_, _, e := loadBattleServer(context.Background(), b.configPath, "http://127.0.0.1:4566", ":memory:", server.DefaultProviderConfig(), never)
			if e == nil || (tc.want != "" && !strings.Contains(e.Error(), tc.want)) {
				t.Fatalf("got %v, want refusal containing %q", e, tc.want)
			}
			if left := b.extractedEntries(t); len(left) != 0 && tc.name != "archive is a symlink" {
				t.Fatalf("refused bundle left extracted assets behind: %v", left)
			}
		})
	}
}

func TestBattleLoaderRetiresBundleWhenConstructionFails(t *testing.T) {
	b := newInertBattleBundle(t)
	wanted := errors.New("constructor refused")
	assetPath := ""
	create := func(_ string, _ server.ProviderConfig, _ string, clock guesttcp.HTTPProbeCommandClock, config aws.EC2InstanceRuntimeConfig) (http.Handler, func() error, error) {
		if clock.Domain != "loader-domain" || config.Directory != b.dir || len(config.ImageIDs) != 1 || config.Layout.BaseDisk != "disk" {
			t.Fatalf("constructor saw an unexpected composition: %+v %+v", clock, config.Layout)
		}
		var e error
		if assetPath, e = config.Bundle.Path("disk"); e != nil {
			t.Fatal(e)
		}
		if config.Lifetime.Err() != nil {
			t.Fatal("bundle lifetime already ended during construction")
		}
		return nil, nil, wanted
	}
	if _, _, e := loadBattleServer(context.Background(), b.configPath, "http://127.0.0.1:4566", ":memory:", server.DefaultProviderConfig(), create); !errors.Is(e, wanted) {
		t.Fatalf("constructor failure not surfaced: %v", e)
	}
	if assetPath == "" {
		t.Fatal("constructor not reached")
	}
	if _, e := os.Stat(assetPath); !os.IsNotExist(e) {
		t.Fatal("failed construction left the extracted bundle behind")
	}
}

func TestBattleRuntimeFileRefusesAmbiguousOperatorInput(t *testing.T) {
	b := newInertBattleBundle(t)
	now := time.Now()
	if _, e := readBattleRuntimeFile(b.configPath, now); e != nil {
		t.Fatal(e)
	}
	for name, change := range map[string]func(*battleRuntimeFile){
		"uppercase session pin":     func(c *battleRuntimeFile) { c.SessionImageSHA256 = strings.Repeat("A", 64) },
		"short session pin":         func(c *battleRuntimeFile) { c.SessionImageSHA256 = "abcd" },
		"relative runtime dir":      func(c *battleRuntimeFile) { c.RuntimeDirectory = "runtime" },
		"non-canonical runtime dir": func(c *battleRuntimeFile) { c.RuntimeDirectory = c.RuntimeDirectory + "/" },
		"no image aliases":          func(c *battleRuntimeFile) { c.ImageIDs = nil },
		"too many image aliases": func(c *battleRuntimeFile) {
			c.ImageIDs = nil
			for i := 0; i < 17; i++ {
				c.ImageIDs = append(c.ImageIDs, "ami-"+strings.Repeat("a", 7)+string(rune('a'+i%6))+hex.EncodeToString([]byte{byte(i)}))
			}
		},
		"duplicate image alias": func(c *battleRuntimeFile) { c.ImageIDs = []string{"ami-12345678", "ami-12345678"} },
		"escaping layout":       func(c *battleRuntimeFile) { c.Layout.Firmware = "../firmware" },
		"dot layout":            func(c *battleRuntimeFile) { c.Layout.BaseDisk = "." },
	} {
		changed := b.cfg
		changed.ImageIDs = append([]string(nil), b.cfg.ImageIDs...)
		change(&changed)
		b.writeConfig(t, changed)
		if _, e := readBattleRuntimeFile(b.configPath, now); e == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	b.writeConfig(t, b.cfg)
	if e := os.WriteFile(b.configPath, []byte(`{"bundleManifest":`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := readBattleRuntimeFile(b.configPath, now); e == nil {
		t.Fatal("truncated runtime JSON accepted")
	}
	for _, path := range []string{"relative.json", b.dir + "/./runtime.json", filepath.Join(b.dir, "absent.json")} {
		if _, e := boundedRegular(path, 1024, true); e == nil {
			t.Fatalf("unsafe runtime path %q accepted", path)
		}
	}
	if _, e := boundedRegular(b.configPath, 4, true); e == nil {
		t.Fatal("oversized runtime file accepted")
	}
}

func TestStrictRuntimeJSONBoundsNestingAndArrays(t *testing.T) {
	type nested struct {
		Values [][]int `json:"values"`
	}
	var ok nested
	if e := strictRuntimeJSON([]byte(`{"values":[[1,2],[3]]}`), &ok); e != nil || len(ok.Values) != 2 || ok.Values[1][0] != 3 {
		t.Fatalf("nested arrays not decoded: %v %+v", e, ok)
	}
	deep := strings.Repeat("[", 10) + strings.Repeat("]", 10)
	var anyValue any
	if e := strictRuntimeJSON([]byte(deep), &anyValue); e == nil || !strings.Contains(e.Error(), "depth") {
		t.Fatalf("over-deep runtime JSON accepted: %v", e)
	}
	for _, raw := range []string{`{"values":[[1,}`, `{"values":[[1]`, `{"a":{"B":1,"b":2}}`, `{"a":[{"x":1,"X":2}]}`, `{1:2}`, `]`} {
		if e := strictRuntimeJSON([]byte(raw), &anyValue); e == nil {
			t.Fatalf("malformed runtime JSON %q accepted", raw)
		}
	}
}
