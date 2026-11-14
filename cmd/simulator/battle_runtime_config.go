package main

// Configuration admission is shared; successful validation does not qualify a
// platform, start a guest, or bypass the Linux/amd64 runtime gate.
import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
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
	if cfg.LifetimeUntilMs == 0 {
		cfg.LifetimeUntilMs = now.Add(guesttcp.DefaultBattleLifetime).UnixMilli()
	}
	if cfg.LifetimeUntilMs <= now.UnixMilli() || cfg.LifetimeUntilMs > now.Add(guesttcp.MaxPassiveLifetime).UnixMilli() || cfg.Clock.ValidFromMs < 1 || cfg.Clock.ValidFromMs > now.UnixMilli() || cfg.Clock.ValidUntilMs < cfg.LifetimeUntilMs || cfg.Clock.ValidUntilMs > 1<<53-1 || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(cfg.Clock.Domain) {
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
