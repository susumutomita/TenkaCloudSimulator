package guestvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestseed"
)

func networkConfig() guestseed.IPv4Network {
	return guestseed.IPv4Network{MACAddress: "02:00:00:00:00:04", Address: "10.0.0.4/24", Gateway: "10.0.0.1"}
}

func networkPreparation(t *testing.T, userData []byte) *Prepared {
	t.Helper()
	requireLinux(t)
	bundle, layout := testBundle(t, imageHeader())
	p, err := PrepareWithNetwork(context.Background(), shortDir(t), bundle, layout, acquire(), userData, networkConfig())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPrepareWithNetworkIntegrityAndDisabledInvocation(t *testing.T) {
	for _, userData := range [][]byte{[]byte("#!/bin/sh\nprintf 'original bytes\\n'\n\x00\xff"), bytes.Repeat([]byte{0xff}, guestseed.MaxUserDataBytes)} {
		p := networkPreparation(t, userData)
		want, err := guestseed.BuildWithNetwork("vm-"+acquire().Fence, "guest-"+acquire().Fence[:12], userData, networkConfig())
		if err != nil {
			t.Fatal(err)
		}
		seedPath := filepath.Join(p.Directory(), "seed.iso")
		seed, err := os.ReadFile(seedPath)
		if err != nil || !bytes.Equal(seed, want) || len(seed) > 64<<10 {
			t.Fatal("prepared seed mismatch or exceeds recovery limit", err)
		}
		receipt := p.Record()
		if receipt.Version != 1 || receipt.SeedSHA256 != digest(seed) || receipt.Intent != acquire() {
			t.Fatal("seed not bound to unchanged preparation record")
		}
		// Existing coordinator mappings depend on Record being a comparable value.
		if !map[Record]bool{receipt: true}[p.Record()] {
			t.Fatal("receipt identity changed")
		}
		raw, err := os.ReadFile(filepath.Join(p.Directory(), "prepared.json"))
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := json.Marshal(receipt)
		if err != nil || !bytes.Equal(raw, canonical) || bytes.Contains(raw, []byte("network")) {
			t.Fatal("changed receipt format", err)
		}
		recovered, err := InspectPrepared(context.Background(), p.Directory(), receipt)
		if err != nil || recovered.Record() != receipt || recovered.Directory() != p.Directory() {
			t.Fatal("network seed receipt recovery failed", err)
		}
		for _, name := range []string{"Launch", "Invocation"} {
			if _, ok := reflect.TypeOf(recovered).MethodByName(name); ok {
				t.Fatal("recovery exposes replay", name)
			}
		}
		args := p.Invocation().Args
		joined := strings.Join(args, "\n")
		for _, required := range []string{"-S\n", "-nic\nnone", "-nodefaults", "-no-user-config"} {
			if !strings.Contains(joined, required) {
				t.Fatal("network seed changed the disabled launch plan", required)
			}
		}
		for _, flag := range []string{"-netdev", "-net"} {
			if slices.Contains(args, flag) {
				t.Fatal("network seed enabled an attachment", flag)
			}
		}
		for _, forbidden := range []string{networkConfig().MACAddress, networkConfig().Address, networkConfig().Gateway} {
			if strings.Contains(joined, forbidden) {
				t.Fatal("network seed enabled or parameterized an attachment", forbidden)
			}
		}
		for _, socket := range []string{p.QMPSocket(), p.QGASocket()} {
			if _, err := os.Lstat(socket); !os.IsNotExist(err) {
				t.Fatal("file preparation or inspection created a socket", err)
			}
		}
		st, err := os.Stat(seedPath)
		if err != nil || st.Mode().Perm() != 0400 {
			t.Fatal("seed not read-only", err)
		}
	}
}

func TestPrepareWithNetworkRejectsBeforeMaterialization(t *testing.T) {
	requireLinux(t)
	bundle, layout := testBundle(t, imageHeader())
	parent := shortDir(t)
	for _, config := range []guestseed.IPv4Network{{}, {MACAddress: "02:00:00:00:00:04", Address: "10.0.0.4/24", Gateway: "10.0.1.1"}} {
		if p, err := PrepareWithNetwork(context.Background(), parent, bundle, layout, acquire(), []byte("x"), config); p != nil || !errors.Is(err, guestseed.ErrNetwork) {
			t.Fatal("invalid opt-in silently accepted", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, input := range []struct {
		ctx    context.Context
		intent guestlease.Intent
		data   []byte
	}{{nil, acquire(), []byte("x")}, {ctx, acquire(), []byte("x")}, {context.Background(), guestlease.Intent{}, []byte("x")}, {context.Background(), acquire(), nil}} {
		if p, err := PrepareWithNetwork(input.ctx, parent, bundle, layout, input.intent, input.data, networkConfig()); p != nil || err == nil {
			t.Fatal("network mode bypassed preparation validation")
		}
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("invalid preparation left partial state", err)
	}
}

func TestInspectPreparedNetworkTamperAndLegacyMismatch(t *testing.T) {
	for _, kind := range []string{"network-byte", "legacy-seed", "different-gateway"} {
		t.Run(kind, func(t *testing.T) {
			userData := []byte("#!/bin/sh\necho unchanged\n")
			p := networkPreparation(t, userData)
			expected := p.Record()
			seedPath := filepath.Join(p.Directory(), "seed.iso")
			seed, err := os.ReadFile(seedPath)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "network-byte":
				index := bytes.Index(seed, []byte(networkConfig().Address))
				if index < 0 {
					t.Fatal("missing fixed address")
				}
				seed[index] ^= 1
			case "legacy-seed":
				seed, err = guestseed.Build("vm-"+acquire().Fence, "guest-"+acquire().Fence[:12], userData)
			case "different-gateway":
				config := networkConfig()
				config.Gateway = "10.0.0.2"
				seed, err = guestseed.BuildWithNetwork("vm-"+acquire().Fence, "guest-"+acquire().Fence[:12], userData, config)
			}
			if err != nil {
				t.Fatal(err)
			}
			rewrite(t, seedPath, seed, 0400)
			if r, err := InspectPrepared(context.Background(), p.Directory(), expected); r != nil || err == nil {
				t.Fatal("receipt accepted changed or removed network configuration")
			}
			// Even a canonical rewritten receipt must not replace the coordinator's
			// independently retained original seed commitment.
			changed := expected
			changed.SeedSHA256 = digest(seed)
			raw, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			rewrite(t, filepath.Join(p.Directory(), "prepared.json"), raw, 0400)
			if r, err := InspectPrepared(context.Background(), p.Directory(), expected); r != nil || err == nil {
				t.Fatal("changed receipt replaced trusted seed identity")
			}
		})
	}
}
