package guestvm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCanonicalSessionImageHookMemoryOnly(t *testing.T) {
	requireLinux(t)
	bundle, layout := testBundle(t, imageHeader())
	info, err := bundle.Info(layout.BaseDisk)
	if err != nil {
		t.Fatal(err)
	}
	for _, pin := range []string{"", strings.Repeat("0", 64), "BAD"} {
		dir := shortDir(t)
		if _, err := PrepareWithNetworkAndSession(context.Background(), dir, bundle, layout, acquire(), []byte("unchanged"), networkConfig(), pin); err == nil {
			t.Fatal("invalid pin accepted")
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatal("invalid pin wrote deployment", err)
		}
	}
	p, err := PrepareWithNetworkAndSession(context.Background(), shortDir(t), bundle, layout, acquire(), []byte("unchanged"), networkConfig(), info.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(p.Invocation().Args, " ")
	if strings.Count(args, "name=org.tenkacloud.session.0") != 1 || !strings.Contains(args, "path="+p.SessionSocket()+",server=on,wait=off") || !strings.Contains(args, "name=org.qemu.guest_agent.0") || !strings.Contains(args, p.QGASocket()) {
		t.Fatal("distinct fixed channels missing", args)
	}
	if p.SessionSocket() == p.QGASocket() || p.SessionSocket() == p.QMPSocket() {
		t.Fatal("control channel reused")
	}
	data, err := os.ReadFile(filepath.Join(p.Directory(), "prepared.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record Record
	if json.Unmarshal(data, &record) != nil || record.SessionImageSHA256 != record.BaseSHA256 || record != p.Record() {
		t.Fatal("pin not persisted")
	}
	if _, err := InspectPrepared(context.Background(), p.Directory(), record); err != nil {
		t.Fatal(err)
	}
	plain := networkPreparation(t, []byte("unchanged"))
	if plain.SessionSocket() != "" || strings.Contains(strings.Join(plain.Invocation().Args, " "), "org.tenkacloud.session.0") {
		t.Fatal("default unexpectedly enabled")
	}
	if plain.Record().SeedSHA256 != p.Record().SeedSHA256 {
		t.Fatal("original UserData/seed changed")
	}
}

func TestCanonicalSessionEndpointBoundsMemoryOnly(t *testing.T) {
	for _, n := range []int{94, 95, 96, 98, 99} {
		directory := "/" + strings.Repeat("x", n-1)
		if got := validPrivateEndpoints(directory, true); got != (n+len("/session.sock") <= 107) {
			t.Fatalf("session boundary %d: %v", n, got)
		}
		if got := validPrivateEndpoints(directory, false); got != (n+len("/qmp.sock") <= 107) {
			t.Fatalf("control boundary %d: %v", n, got)
		}
	}
}

func TestCanonicalSessionEndpointInspectionRejectsOverlongMemoryOnly(t *testing.T) {
	requireLinux(t)
	// Real inspection path in the four-byte gap: controls fit, session does not.
	bundle, layout := testBundle(t, imageHeader())
	info, _ := bundle.Info(layout.BaseDisk)
	p, err := PrepareWithNetworkAndSession(context.Background(), shortDir(t), bundle, layout, acquire(), []byte("unchanged"), networkConfig(), info.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	parent := shortDir(t)
	target := filepath.Join(parent, strings.Repeat("x", 96-len(parent)-1))
	if err := os.Rename(p.Directory(), target); err != nil {
		t.Fatal(err)
	}
	if !validPrivateEndpoints(target, false) || validPrivateEndpoints(target, true) {
		t.Fatal("fixture not in endpoint gap")
	}
	if _, err := InspectPrepared(context.Background(), target, p.Record()); err == nil {
		t.Fatal("overlong session inspection accepted")
	}
}

// Unsupported hosts must reject preparation rather than silently constructing
// a deployment that their process backend cannot safely own.
func TestCanonicalSessionPreparationRejectsUnsupportedPlatform(t *testing.T) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		t.Skip("unsupported-platform rejection is checked on other hosts")
	}
	bundle, layout := testBundle(t, imageHeader())
	info, err := bundle.Info(layout.BaseDisk)
	if err != nil {
		t.Fatal(err)
	}
	parent := shortDir(t)
	p, err := PrepareWithNetworkAndSession(context.Background(), parent, bundle, layout, acquire(), []byte("unchanged"), networkConfig(), info.SHA256)
	if p != nil || err == nil || err.Error() != "guest launch plan supports Linux/amd64 only" {
		t.Fatalf("unsupported platform preparation=%v err=%v", p, err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unsupported platform wrote deployment: entries=%v err=%v", entries, err)
	}
}
