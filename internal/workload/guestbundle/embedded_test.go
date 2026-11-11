package guestbundle

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func embeddedFixture(t *testing.T) fstest.MapFS {
	t.Helper()
	data, m := good(t)
	manifest, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{"manifest.json": &fstest.MapFile{Data: manifest}, "assets.tar.gz": &fstest.MapFile{Data: data}}
}

func TestExtractFS(t *testing.T) {
	assets := embeddedFixture(t)
	bundle, err := ExtractFS(context.Background(), t.TempDir(), assets)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	name, err := bundle.Path("bin/emulator")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(name)
	if err != nil || string(data) != "runtime" {
		t.Fatalf("asset: %q, %v", data, err)
	}
}

func TestExtractFSRejectsInvalidAssets(t *testing.T) {
	cases := map[string]func(fstest.MapFS){
		"missing manifest":   func(f fstest.MapFS) { delete(f, "manifest.json") },
		"missing archive":    func(f fstest.MapFS) { delete(f, "assets.tar.gz") },
		"manifest directory": func(f fstest.MapFS) { f["manifest.json"].Mode = fs.ModeDir },
		"archive symlink":    func(f fstest.MapFS) { f["assets.tar.gz"].Mode = fs.ModeSymlink },
		"oversize manifest":  func(f fstest.MapFS) { f["manifest.json"].Data = []byte(strings.Repeat(" ", MaxManifestBytes+1)) },
		"unknown field":      func(f fstest.MapFS) { f["manifest.json"].Data = []byte(`{"future":true}`) },
		"malformed":          func(f fstest.MapFS) { f["manifest.json"].Data = []byte(`{`) },
		"null manifest":      func(f fstest.MapFS) { f["manifest.json"].Data = []byte(`null`) },
		"extra value":        func(f fstest.MapFS) { f["manifest.json"].Data = append(f["manifest.json"].Data, []byte(` {}`)...) },
		"trailing junk":      func(f fstest.MapFS) { f["manifest.json"].Data = append(f["manifest.json"].Data, '!') },
		"corrupt archive":    func(f fstest.MapFS) { f["assets.tar.gz"].Data[0] ^= 255 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			assets := embeddedFixture(t)
			change(assets)
			parent := t.TempDir()
			if bundle, err := ExtractFS(context.Background(), parent, assets); err == nil || bundle != nil {
				t.Fatalf("accepted invalid assets: %v", err)
			}
			files, err := os.ReadDir(parent)
			if err != nil || len(files) != 0 {
				t.Fatalf("partial files: %v %v", files, err)
			}
		})
	}
}

func TestExtractFSRequiresContextAndFilesystem(t *testing.T) {
	assets := embeddedFixture(t)
	if _, err := ExtractFS(nil, t.TempDir(), assets); err == nil {
		t.Fatal("nil context")
	}
	if _, err := ExtractFS(context.Background(), t.TempDir(), nil); err == nil {
		t.Fatal("nil filesystem")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ExtractFS(ctx, t.TempDir(), assets); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type brokenAssetFS struct {
	fs.FS
	target                     string
	statErr, readErr, closeErr error
}
type brokenAsset struct {
	fs.File
	statErr, readErr, closeErr error
}

func (f brokenAssetFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil || name != f.target {
		return file, err
	}
	return brokenAsset{file, f.statErr, f.readErr, f.closeErr}, nil
}
func (f brokenAsset) Stat() (fs.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.File.Stat()
}
func (f brokenAsset) Read(b []byte) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	return f.File.Read(b)
}
func (f brokenAsset) Close() error { return errors.Join(f.File.Close(), f.closeErr) }

func TestExtractFSIOFailuresCleanUp(t *testing.T) {
	failure := errors.New("asset failure")
	for _, target := range []string{"manifest.json", "assets.tar.gz"} {
		for _, operation := range []string{"stat", "read", "close"} {
			t.Run(target+"/"+operation, func(t *testing.T) {
				assets := brokenAssetFS{FS: embeddedFixture(t), target: target}
				switch operation {
				case "stat":
					assets.statErr = failure
				case "read":
					assets.readErr = failure
				case "close":
					assets.closeErr = failure
				}
				parent := t.TempDir()
				bundle, err := ExtractFS(context.Background(), parent, assets)
				if bundle != nil || !errors.Is(err, failure) {
					t.Fatalf("failure lost: %v", err)
				}
				files, err := os.ReadDir(parent)
				if err != nil || len(files) != 0 {
					t.Fatalf("partial files: %v %v", files, err)
				}
			})
		}
	}
}
