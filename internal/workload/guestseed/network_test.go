package guestseed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func testNetwork() IPv4Network {
	return IPv4Network{MACAddress: "02:00:00:00:00:04", Address: "10.0.0.4/24", Gateway: "10.0.0.1"}
}

func TestBuildWithNetworkSeparateCanonicalFile(t *testing.T) {
	config := testNetwork()
	// This represents opaque original workload bytes, not a script to execute.
	userData := []byte("#!/bin/sh\nprintf 'こんにちは\\n'\n\x00\xff\r\n")
	before := bytes.Clone(userData)
	image, err := BuildWithNetwork("lease_012345", "guest-1", userData, config)
	if err != nil {
		t.Fatal(err)
	}
	again, err := BuildWithNetwork("lease_012345", "guest-1", userData, config)
	if err != nil || !bytes.Equal(image, again) || !bytes.Equal(userData, before) || config != testNetwork() {
		t.Fatal("non-deterministic output or mutated caller values", err)
	}
	files := parseSeedDirectory(t, image)
	if len(files) != 3 || !bytes.Equal(files["user-data"], userData) || string(files["meta-data"]) != "instance-id: \"lease_012345\"\nlocal-hostname: \"guest-1\"\n" {
		t.Fatal("unexpected seed contents")
	}
	var identifiers []string
	dir := image[20*sector : 21*sector]
	for offset := 0; offset < len(dir) && dir[offset] != 0; {
		record := dir[offset : offset+int(dir[offset])]
		offset += len(record)
		if record[25]&2 == 0 {
			identifiers = append(identifiers, string(record[33:33+int(record[32])]))
		}
	}
	if !slices.Equal(identifiers, []string{"META_DAT.;1", "NETWORK.;1", "USER_DAT.;1"}) {
		t.Fatal("ISO9660 directory identifiers are not ordered", identifiers)
	}
	want := "version: 1\nconfig:\n  - type: physical\n    name: eth0\n    mac_address: \"02:00:00:00:00:04\"\n    mtu: 1500\n    subnets:\n      - type: static\n        control: auto\n        address: \"10.0.0.4/24\"\n        gateway: \"10.0.0.1\"\n"
	if string(files["network-config"]) != want {
		t.Fatalf("unexpected network-config: %q", files["network-config"])
	}
	// Independently decode the bounded schema. This is YAML/shape verification,
	// not execution of the cloud-init renderer or evidence of Linux behavior.
	var decoded struct {
		Version int `yaml:"version"`
		Config  []struct {
			Type       string `yaml:"type"`
			Name       string `yaml:"name"`
			MACAddress string `yaml:"mac_address"`
			MTU        int    `yaml:"mtu"`
			Subnets    []struct {
				Type    string `yaml:"type"`
				Control string `yaml:"control"`
				Address string `yaml:"address"`
				Gateway string `yaml:"gateway"`
			} `yaml:"subnets"`
		} `yaml:"config"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(files["network-config"]))
	decoder.KnownFields(true)
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Version != 1 || len(decoded.Config) != 1 {
		t.Fatal("unexpected network schema")
	}
	device := decoded.Config[0]
	if device.Type != "physical" || device.Name != "eth0" || device.MACAddress != config.MACAddress || device.MTU != 1500 || len(device.Subnets) != 1 {
		t.Fatal("unexpected device schema")
	}
	subnet := device.Subnets[0]
	if subnet.Type != "static" || subnet.Control != "auto" || subnet.Address != config.Address || subnet.Gateway != config.Gateway {
		t.Fatal("unexpected subnet schema")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatal("extra YAML document", err)
	}
	userData[0] ^= 1
	config.Address = "10.0.0.5/24"
	if !bytes.Equal(files["user-data"], before) || string(files["network-config"]) != want {
		t.Fatal("seed aliases caller memory")
	}
}

func TestBuildWithNetworkRejectsUnsupportedValues(t *testing.T) {
	for _, field := range []struct {
		name   string
		set    func(*IPv4Network, string)
		values []string
	}{
		{"mac", func(c *IPv4Network, v string) { c.MACAddress = v }, []string{"", "00:00:00:00:00:00", "00:11:22:33:44:55", "03:00:00:00:00:04", "ff:ff:ff:ff:ff:ff", "02:AA:00:00:00:04", "02-00-00-00-00-04", "0200.0000.0004", "02:00:00:00:00:00:00:04", "02:00:00:00:00:04\n", "../../eth0", "\"\nconfig: []", strings.Repeat("0", 1<<16)}},
		{"address", func(c *IPv4Network, v string) { c.Address = v }, []string{"", "10.0.0.4", "10.0.0.4/15", "10.0.0.4/29", "10.0.0.4/32", "10.0.0.4/024", "010.0.0.4/24", "10.0.0.0/24", "10.0.0.255/24", "10.0.0.1/24", "127.0.0.4/24", "169.254.1.4/24", "224.0.0.4/24", "0.0.0.4/24", "192.0.2.4/24", "172.15.0.4/16", "172.32.0.4/16", "192.169.0.4/16", "fc00::4/24", "::ffff:10.0.0.4/120", "10.0.0.4/24\n", "10.0.0.4/24\nfoo:", strings.Repeat("a", 1<<16)}},
		{"gateway", func(c *IPv4Network, v string) { c.Gateway = v }, []string{"", "10.0.0.4", "10.0.0.0", "10.0.0.255", "10.0.1.1", "010.0.0.1", "10.0.0.1/24", "10.0.0.1%eth0", "::ffff:10.0.0.1", "fc00::1", "0.0.0.0", "127.0.0.1", "169.254.1.1", "224.0.0.1", "255.255.255.255", "10.0.0.1\n", "\"\nfoo: yes", strings.Repeat("a", 1<<16)}},
	} {
		for _, value := range field.values {
			config := testNetwork()
			field.set(&config, value)
			if image, err := BuildWithNetwork("id", "guest", []byte("x"), config); image != nil || !errors.Is(err, ErrNetwork) {
				t.Fatalf("accepted unsupported %s value %q: %v", field.name, value, err)
			}
		}
	}
	if image, err := BuildWithNetwork("id", "guest", []byte("x"), IPv4Network{}); image != nil || !errors.Is(err, ErrNetwork) {
		t.Fatal("zero network silently became legacy mode", err)
	}
}

func TestBuildWithNetworkBoundsAndSeedIdentity(t *testing.T) {
	for _, config := range []IPv4Network{
		testNetwork(),
		{"02:00:00:00:00:00", "10.255.255.254/16", "10.255.0.1"},
		{"fe:ff:ff:ff:ff:ff", "172.16.0.1/28", "172.16.0.14"},
		{"0a:11:22:33:44:55", "172.31.255.254/28", "172.31.255.241"},
		{"06:11:22:33:44:55", "192.168.255.254/16", "192.168.0.1"},
	} {
		for _, size := range []int{1, 2047, 2048, 2049, MaxUserDataBytes} {
			data := bytes.Repeat([]byte{0xff}, size)
			image, err := BuildWithNetwork(strings.Repeat("a", 128), strings.Repeat("h", 63), data, config)
			if err != nil {
				t.Fatal(config, err)
			}
			if len(image) > 64<<10 || len(image)%sector != 0 || !bytes.Equal(parseSeedDirectory(t, image)["user-data"], data) {
				t.Fatal("image exceeds recovery bounds or changed UserData", size)
			}
		}
	}
	for _, input := range []struct {
		id, host string
		data     []byte
	}{{"../bad", "guest", []byte("x")}, {"id", "bad\nhost", []byte("x")}, {"id", "guest", nil}, {"id", "guest", make([]byte, MaxUserDataBytes+1)}} {
		if image, err := BuildWithNetwork(input.id, input.host, input.data, testNetwork()); image != nil || err == nil {
			t.Fatal("network mode bypassed seed validation")
		}
	}
	legacy, err := Build("id", "guest", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	hashes := map[[32]byte]bool{sha256.Sum256(legacy): true}
	for _, change := range []func(*IPv4Network){
		func(c *IPv4Network) {},
		func(c *IPv4Network) { c.MACAddress = "02:00:00:00:00:05" },
		func(c *IPv4Network) { c.Address = "10.0.0.5/24" },
		func(c *IPv4Network) { c.Address = "10.0.0.4/25" },
		func(c *IPv4Network) { c.Gateway = "10.0.0.2" },
	} {
		config := testNetwork()
		change(&config)
		image, err := BuildWithNetwork("id", "guest", []byte("x"), config)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(image)
		if hashes[hash] {
			t.Fatal("different network configurations share a seed hash")
		}
		hashes[hash] = true
	}
}

func TestBuildWithNetworkOriginalBattleUserData(t *testing.T) {
	// Test-only original fixture. Preserve its UserData scalar exactly, including
	// the unresolved template parameter. Intrinsic resolution has separate AWS
	// tests; this test neither resolves nor executes any original workload.
	source, err := os.ReadFile("../../../test/fixtures/hello-world-battle-c1aaaa9/template.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(source)
	if hex.EncodeToString(sum[:]) != "4de530625bd2a3c0b5a5881f511061709c94c9eb6884d8d6fd1037f3d9bc6bc5" {
		t.Fatal("original fixture changed")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Content) != 1 {
		t.Fatal("invalid original template document")
	}
	node := document.Content[0]
	for _, key := range []string{"Resources", "Ec2", "Properties", "UserData", "Fn::Base64"} {
		if node.Kind != yaml.MappingNode {
			t.Fatal("missing original UserData mapping", key)
		}
		var child *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				child = node.Content[i+1]
				break
			}
		}
		if child == nil {
			t.Fatal("missing original UserData key", key)
		}
		node = child
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!Sub" || len(node.Value) == 0 {
		t.Fatal("unexpected original UserData scalar")
	}
	userData := []byte(node.Value)
	legacy, err := Build("original-1", "guest", userData)
	if err != nil {
		t.Fatal(err)
	}
	image, err := BuildWithNetwork("original-1", "guest", userData, testNetwork())
	if err != nil {
		t.Fatal(err)
	}
	before, after := parseSeedDirectory(t, legacy), parseSeedDirectory(t, image)
	if !bytes.Equal(after["user-data"], userData) || !bytes.Equal(after["user-data"], before["user-data"]) || !bytes.Equal(after["meta-data"], before["meta-data"]) || len(after) != 3 {
		t.Fatal("network seed altered original UserData or metadata")
	}
}
