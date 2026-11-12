//go:build linux

package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestbundle"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestvm"
)

// inertBattleConfig is a pinned but inert runtime: extraction and validation
// run, no emulator is ever executed by these tests.
func inertBattleConfig(t *testing.T) (aws.EC2InstanceRuntimeConfig, string) {
	t.Helper()
	dir := t.TempDir()
	disk := make([]byte, 512)
	copy(disk, "QFI\xfb")
	binary.BigEndian.PutUint32(disk[4:8], 3)
	binary.BigEndian.PutUint32(disk[20:24], 9)
	binary.BigEndian.PutUint64(disk[24:32], 1<<20)
	binary.BigEndian.PutUint32(disk[100:104], 104)
	layout := guestvm.Layout{Emulator: "bin/qemu", Firmware: "firmware/bios", BaseDisk: "images/base.qcow2"}
	manifest := guestbundle.Manifest{OS: runtime.GOOS, Arch: runtime.GOARCH}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	digest := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	for i, x := range []struct {
		name string
		data []byte
	}{{layout.Emulator, []byte("inert-not-a-program")}, {layout.Firmware, []byte("inert")}, {layout.BaseDisk, disk}} {
		manifest.Files = append(manifest.Files, guestbundle.File{Path: x.name, Size: int64(len(x.data)), SHA256: digest(x.data), Executable: i == 0})
		if err := tw.WriteHeader(&tar.Header{Name: x.name, Size: int64(len(x.data)), Mode: 0o600}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(x.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	manifest.ArchiveSHA256 = digest(archive.Bytes())
	bundle, err := guestbundle.Extract(context.Background(), dir, bytes.NewReader(archive.Bytes()), manifest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bundle.Close() })
	lifetime, cancel := context.WithTimeout(context.Background(), time.Hour)
	t.Cleanup(cancel)
	return aws.EC2InstanceRuntimeConfig{Lifetime: lifetime, Bundle: bundle, Layout: layout, Directory: dir, ImageIDs: []string{"ami-0a1b2c3d4e5f60718"}}, digest(disk)
}

func battleClock() guesttcp.HTTPProbeCommandClock {
	return guesttcp.HTTPProbeCommandClock{Domain: "qualified", ValidFromMs: 1, ValidUntilMs: time.Now().Add(time.Hour).UnixMilli()}
}

func TestBattleRuntimeComposesConfiguredProviderWithoutVM(t *testing.T) {
	for _, session := range []bool{false, true} {
		t.Run(map[bool]string{false: "commands", true: "commands-and-terminal"}[session], func(t *testing.T) {
			config, diskSHA := inertBattleConfig(t)
			if session {
				config.SessionImageSHA256 = diskSHA
			}
			h, closeAll, err := NewWithBattleRuntime(filepath.Join(t.TempDir(), "state.db"), DefaultProviderConfig(), "https://127.0.0.1:4566", battleClock(), config)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", battleCapabilitiesPath+"/availability", strings.NewReader(`{"version":1}`))
			req = req.WithContext(provider.WithActor(req.Context(), provider.Actor{CredentialIdentity: "simulator-admin", ExerciseIdentity: "exercise", CredentialRole: provider.CredentialOperator}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"configured":true`) || !strings.Contains(w.Body.String(), `"clockDomain":"qualified"`) {
				t.Fatalf("availability %d %s", w.Code, w.Body.String())
			}
			if err = closeAll(); err != nil {
				t.Fatal("composed runtime did not shut down cleanly:", err)
			}
		})
	}
}

func TestBattleRuntimeRefusesInvalidComposition(t *testing.T) {
	config, _ := inertBattleConfig(t)
	path := func() string { return filepath.Join(t.TempDir(), "state.db") }
	if _, _, err := NewWithMonotonicBattleRuntime(path(), DefaultProviderConfig(), "http://127.0.0.1:4566", battleClock(), config, nil); err == nil {
		t.Fatal("missing owned monotonic clock accepted")
	}
	preset := config
	preset.OnReady = func(*aws.EC2VMProbeLaunch) error { return nil }
	if _, _, err := NewWithBattleRuntime(path(), DefaultProviderConfig(), "http://127.0.0.1:4566", battleClock(), preset); err == nil || !strings.Contains(err.Error(), "already configured") {
		t.Fatal("caller OnReady replaced registry receipt path", err)
	}
	if _, _, err := NewWithBattleRuntime(path(), DefaultProviderConfig(), "ftp://127.0.0.1:4566/x", battleClock(), config); err == nil {
		t.Fatal("invalid provider origin accepted")
	}
	pinned := config
	pinned.SessionImageSHA256 = strings.Repeat("0", 64)
	if _, _, err := NewWithBattleRuntime(path(), DefaultProviderConfig(), "http://127.0.0.1:4566", battleClock(), pinned); err == nil {
		t.Fatal("mismatched session image pin accepted")
	}
	expired := config
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	expired.Lifetime = ended
	if _, _, err := NewWithBattleRuntime(path(), DefaultProviderConfig(), "http://127.0.0.1:4566", battleClock(), expired); err == nil {
		t.Fatal("unbounded/ended runtime lifetime accepted")
	}
	registry, _ := NewBattleCapabilityRegistry("http://127.0.0.1:4566", battleClock())
	if _, _, err := NewWithBattleCapabilities(path(), DefaultProviderConfig(), nil); err == nil {
		t.Fatal("nil registry composed")
	}
	h, closeAll, err := NewWithBattleCapabilities(path(), DefaultProviderConfig(), registry)
	if err != nil || h == nil {
		t.Fatal(err)
	}
	_ = closeAll()
}

func TestBattleStrictJSONAndOwnerValidation(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"a":[1,{"b":[true,null]}],"c":"x"}`: true,
		`[]`:                                  true,
		`{"a":[1,2}`:                          false,
		`{"a":1`:                              false,
		`[[[[[[[[[[1]]]]]]]]]]`:               false, // nesting beyond the bound
		`{"a":{"a":1},"a":2}`:                 false,
		`{"a":1} 2`:                           false,
		``:                                    false,
		`]`:                                   false,
	} {
		if got := uniqueBattleJSON([]byte(raw)); got != want {
			t.Fatalf("uniqueBattleJSON(%q) = %v", raw, got)
		}
	}
	valid := BattleDeployment{EventID: "event", TeamID: "team", JobID: "job", UnitHash: strings.Repeat("a", 64), DeployedAt: 1, StackID: "stack", InstanceID: "i-1"}
	if !validBattleOwner(valid) {
		t.Fatal("valid owner rejected")
	}
	for name, mutate := range map[string]func(*BattleDeployment){
		"empty":      func(o *BattleDeployment) { o.TeamID = "" },
		"padded":     func(o *BattleDeployment) { o.EventID = " event" },
		"control":    func(o *BattleDeployment) { o.JobID = "job\x01" },
		"long":       func(o *BattleDeployment) { o.StackID = strings.Repeat("s", 1025) },
		"upper hash": func(o *BattleDeployment) { o.UnitHash = strings.Repeat("A", 64) },
		"short hash": func(o *BattleDeployment) { o.UnitHash = "ab" },
		"time zero":  func(o *BattleDeployment) { o.DeployedAt = 0 },
		"time huge":  func(o *BattleDeployment) { o.DeployedAt = 1 << 53 },
	} {
		o := valid
		mutate(&o)
		if validBattleOwner(o) {
			t.Fatalf("%s owner accepted", name)
		}
	}
	for _, origin := range []string{"http://user@127.0.0.1:4566", "http://127.0.0.1:4566/path", "http://127.0.0.1:4566?q=1", "http://127.0.0.1:4566#f", "ws://127.0.0.1:4566", "http://", "%zz"} {
		if _, err := NewBattleCapabilityRegistry(origin, battleClock()); err == nil {
			t.Fatalf("origin %q accepted", origin)
		}
	}
}
