package guestvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func preparation(t *testing.T) *Prepared {
	t.Helper()
	requireLinux(t)
	b, layout := testBundle(t, imageHeader())
	p, err := Prepare(context.Background(), shortDir(t), b, layout, acquire(), []byte("#!/bin/sh\necho unchanged\n"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func rewrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestInspectPreparedDoesNotRecreateOrRelaunch(t *testing.T) {
	p := preparation(t)
	record := p.Record()
	diskPath := filepath.Join(p.Directory(), "root.qcow2")
	disk, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	disk[len(disk)-1] ^= 1
	if err := os.WriteFile(diskPath, disk, 0600); err != nil {
		t.Fatal(err)
	} // Normal mutable guest disk must not be compared with base hash.
	before := map[string][]byte{}
	for _, name := range []string{"root.qcow2", "seed.iso", "prepared.json"} {
		before[name], err = os.ReadFile(filepath.Join(p.Directory(), name))
		if err != nil {
			t.Fatal(err)
		}
	}
	restored, err := InspectPrepared(context.Background(), p.Directory(), record)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Record() != record || restored.Directory() != p.Directory() || restored.QMPSocket() != p.QMPSocket() || restored.QGASocket() != p.QGASocket() {
		t.Fatal("recovery identity changed")
	}
	for _, name := range []string{"Launch", "Invocation"} {
		if _, ok := reflect.TypeOf(restored).MethodByName(name); ok {
			t.Fatal("recovery exposes replay method", name)
		}
	}
	for name, want := range before {
		got, err := os.ReadFile(filepath.Join(p.Directory(), name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("inspection wrote state", name, err)
		}
	}
	for _, path := range []string{restored.QMPSocket(), restored.QGASocket()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("inspection created a socket", err)
		}
	}
	copy := restored.Record()
	copy.UUID = "changed"
	if restored.Record() != record {
		t.Fatal("mutable recovered identity")
	}
}

func TestInspectPreparedRejectsWrongExpectedIdentity(t *testing.T) {
	p := preparation(t)
	for _, change := range []func(*Record){func(r *Record) { r.Version++ }, func(r *Record) { r.Intent.Fence = "bad" }, func(r *Record) { r.Intent.Operation++ }, func(r *Record) { r.Intent.Action = "release" }, func(r *Record) { r.Intent.Lease = "selected" }, func(r *Record) { r.Lease = "wrong" }, func(r *Record) { r.UUID = "invalid" }, func(r *Record) { r.BaseSHA256 = "invalid" }, func(r *Record) { r.SeedSHA256 = "invalid" }, func(r *Record) { r.UUID = "00000000-0000-4000-8000-000000000000" }, func(r *Record) { r.BaseSHA256 = strings.Repeat("b", 64) }} {
		expected := p.Record()
		change(&expected)
		if _, err := InspectPrepared(context.Background(), p.Directory(), expected); err == nil {
			t.Fatal("wrong authority accepted")
		}
	}
	if _, err := InspectPrepared(nil, p.Directory(), p.Record()); err == nil {
		t.Fatal("nil context")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InspectPrepared(ctx, p.Directory(), p.Record()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, path := range []string{"relative", "/tmp/x,y", "/tmp/x:y", "/tmp/" + strings.Repeat("x", 108), filepath.Join(shortDir(t), "missing")} {
		if _, err := InspectPrepared(context.Background(), path, p.Record()); err == nil {
			t.Fatal("invalid directory", path)
		}
	}
}

func TestInspectPreparedRejectsDamagedPrivateState(t *testing.T) {
	for _, kind := range []string{"receipt-truncated", "receipt-whitespace", "receipt-duplicate", "receipt-unknown", "receipt-missing", "receipt-mode", "receipt-symlink", "receipt-oversize", "seed-modified", "seed-mode", "seed-missing", "seed-oversize", "disk-missing", "disk-mode", "disk-truncated", "disk-symlink", "directory-mode", "directory-symlink"} {
		t.Run(kind, func(t *testing.T) {
			p := preparation(t)
			dir := p.Directory()
			expected := p.Record()
			receipt := filepath.Join(dir, "prepared.json")
			seed := filepath.Join(dir, "seed.iso")
			disk := filepath.Join(dir, "root.qcow2")
			raw, err := os.ReadFile(receipt)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "receipt-truncated":
				rewrite(t, receipt, []byte("{"), 0400)
			case "receipt-whitespace":
				rewrite(t, receipt, append(raw, '\n'), 0400)
			case "receipt-duplicate":
				rewrite(t, receipt, append([]byte(`{"version":1,`), raw[1:]...), 0400)
			case "receipt-unknown":
				rewrite(t, receipt, append([]byte(`{"unknown":true,`), raw[1:]...), 0400)
			case "receipt-missing":
				os.Remove(receipt)
			case "receipt-mode":
				os.Chmod(receipt, 0600)
			case "receipt-symlink":
				os.Remove(receipt)
				if err := os.Symlink(seed, receipt); err != nil {
					t.Fatal(err)
				}
			case "receipt-oversize":
				rewrite(t, receipt, bytes.Repeat([]byte(" "), 4097), 0400)
			case "seed-modified":
				b, err := os.ReadFile(seed)
				if err != nil {
					t.Fatal(err)
				}
				b[len(b)-1] ^= 1
				rewrite(t, seed, b, 0400)
			case "seed-mode":
				os.Chmod(seed, 0644)
			case "seed-missing":
				os.Remove(seed)
			case "seed-oversize":
				rewrite(t, seed, make([]byte, 64<<10+1), 0400)
			case "disk-missing":
				os.Remove(disk)
			case "disk-mode":
				os.Chmod(disk, 0644)
			case "disk-truncated":
				rewrite(t, disk, []byte("short"), 0600)
			case "disk-symlink":
				os.Remove(disk)
				if err := os.Symlink(seed, disk); err != nil {
					t.Fatal(err)
				}
			case "directory-mode":
				os.Chmod(dir, 0755)
			case "directory-symlink":
				link := filepath.Join(shortDir(t), "link")
				if err := os.Symlink(dir, link); err != nil {
					t.Fatal(err)
				}
				dir = link
			}
			if _, err := InspectPrepared(context.Background(), dir, expected); err == nil {
				t.Fatal("damaged state accepted")
			}
			if kind != "receipt-missing" && kind != "receipt-symlink" && kind != "receipt-truncated" && kind != "receipt-duplicate" && kind != "receipt-unknown" && kind != "receipt-oversize" && kind != "receipt-whitespace" {
				after, err := os.ReadFile(receipt)
				if err != nil || !bytes.Equal(after, raw) {
					t.Fatal("failed inspection repaired/replaced receipt", err)
				}
			}
		})
	}
}

func TestInspectPreparedRejectsCanonicalButDifferentReceipt(t *testing.T) {
	p := preparation(t)
	r := p.Record()
	r.SeedSHA256 = strings.Repeat("a", 64)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	rewrite(t, filepath.Join(p.Directory(), "prepared.json"), raw, 0400)
	if _, err := InspectPrepared(context.Background(), p.Directory(), p.Record()); err == nil {
		t.Fatal("different receipt accepted")
	}
}

func TestRecoveryAndPreparationRejectDirectoryAliases(t *testing.T) {
	p := preparation(t)
	linkRoot := shortDir(t)
	link := filepath.Join(linkRoot, "alias")
	if err := os.Symlink(filepath.Dir(p.Directory()), link); err != nil {
		t.Fatal(err)
	}
	ancestorAlias := filepath.Join(link, filepath.Base(p.Directory()))
	directLink := filepath.Join(linkRoot, "direct")
	if err := os.Symlink(p.Directory(), directLink); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{p.Directory() + "/", ancestorAlias, directLink + "/", p.Directory() + "/../" + filepath.Base(p.Directory())} {
		if _, err := InspectPrepared(context.Background(), dir, p.Record()); err == nil {
			t.Fatal("directory alias accepted", dir)
		}
	}
	b, layout := testBundle(t, imageHeader())
	if _, err := Prepare(context.Background(), link, b, layout, acquire(), []byte("x")); err == nil {
		t.Fatal("symlink parent accepted")
	}
	public := shortDir(t)
	if err := os.Chmod(public, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), public, b, layout, acquire(), []byte("x")); err == nil {
		t.Fatal("nonprivate parent accepted")
	}
}
