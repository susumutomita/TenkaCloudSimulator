package guestseed

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBuildNoCloudImage(t *testing.T) {
	data := []byte("#!/bin/sh\nprintf 'こんにちは\\n'\n")
	a, err := Build("lease_012345", "guest-1", data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build("lease_012345", "guest-1", data)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("not deterministic", err)
	}
	// Captured from the unmodified builder at 1a73221. Adding opt-in network
	// configuration must not change existing receipt-bound legacy seed bytes.
	hash := sha256.Sum256(a)
	if hex.EncodeToString(hash[:]) != "ec4a922d52b4820bd5927db2e0b73371d9f7dbb1ce1ef480a88af1dd47f03344" {
		t.Fatal("legacy seed changed")
	}
	if !bytes.Equal(data, []byte("#!/bin/sh\nprintf 'こんにちは\\n'\n")) {
		t.Fatal("caller bytes mutated")
	}
	if len(a)%2048 != 0 || !bytes.Equal(a[:16*2048], make([]byte, 16*2048)) {
		t.Fatal("invalid system area")
	}
	pvd := a[16*2048 : 17*2048]
	if string(pvd[1:6]) != "CD001" || pvd[0] != 1 || pvd[6] != 1 || strings.TrimSpace(string(pvd[40:72])) != "cidata" {
		t.Fatal("invalid primary descriptor")
	}
	if int(binary.LittleEndian.Uint32(pvd[80:84])) != len(a)/2048 || binary.LittleEndian.Uint32(pvd[80:84]) != binary.BigEndian.Uint32(pvd[84:88]) {
		t.Fatal("volume size")
	}
	if a[17*2048] != 255 || string(a[17*2048+1:17*2048+6]) != "CD001" {
		t.Fatal("missing terminator")
	}
	if binary.LittleEndian.Uint32(a[18*2048+2:18*2048+6]) != 20 || binary.BigEndian.Uint32(a[19*2048+2:19*2048+6]) != 20 {
		t.Fatal("path tables")
	}
	entries := parseSeedDirectory(t, a)
	if len(entries) != 2 || !bytes.Equal(entries["user-data"], data) || string(entries["meta-data"]) != "instance-id: \"lease_012345\"\nlocal-hostname: \"guest-1\"\n" {
		t.Fatalf("unexpected contents: %v", entries)
	}
	// Each call owns its bytes; modifying the caller input after Build cannot
	// rewrite a seed image already bound to a guest incarnation.
	data[0] = '!'
	if bytes.Equal(entries["user-data"], data) {
		t.Fatal("aliased user data")
	}
}

func parseSeedDirectory(t *testing.T, image []byte) map[string][]byte {
	t.Helper()
	dir := image[20*2048 : 21*2048]
	files := map[string][]byte{}
	for offset := 0; offset < len(dir) && dir[offset] != 0; {
		n := int(dir[offset])
		r := dir[offset : offset+n]
		offset += n
		extent := binary.LittleEndian.Uint32(r[2:6])
		size := binary.LittleEndian.Uint32(r[10:14])
		if extent != binary.BigEndian.Uint32(r[6:10]) || size != binary.BigEndian.Uint32(r[14:18]) {
			t.Fatal("both-endian mismatch")
		}
		if r[25]&2 != 0 {
			continue
		}
		start := 33 + int(r[32])
		if start%2 != 0 {
			start++
		}
		name := ""
		for start+4 <= len(r) {
			length := int(r[start+2])
			if length < 4 || start+length > len(r) {
				t.Fatal("invalid system entry")
			}
			if string(r[start:start+2]) == "NM" {
				name = string(r[start+5 : start+length])
			}
			start += length
		}
		if name == "" {
			t.Fatal("missing Rock Ridge name")
		}
		end := int(extent)*2048 + int(size)
		if end > len(image) {
			t.Fatal("extent outside image")
		}
		files[name] = image[int(extent)*2048 : end]
	}
	return files
}

func TestBuildBoundsAndMetadataSafety(t *testing.T) {
	for _, id := range []string{"", "../escape", "bad\nkey: value", "x\"", strings.Repeat("a", 129)} {
		if _, err := Build(id, "guest", []byte("x")); err == nil {
			t.Fatalf("accepted id %q", id)
		}
	}
	for _, host := range []string{"", "-guest", "guest-", "A", "a.b", "g\n", strings.Repeat("a", 64)} {
		if _, err := Build("id", host, []byte("x")); err == nil {
			t.Fatalf("accepted host %q", host)
		}
	}
	for _, data := range [][]byte{nil, {}, make([]byte, MaxUserDataBytes+1)} {
		if _, err := Build("id", "guest", data); err == nil {
			t.Fatal("accepted data length", len(data))
		}
	}
	for _, size := range []int{1, 2047, 2048, 2049, MaxUserDataBytes} {
		data := bytes.Repeat([]byte{0xff}, size)
		image, err := Build(strings.Repeat("a", 128), strings.Repeat("h", 63), data)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(parseSeedDirectory(t, image)["user-data"], data) {
			t.Fatal("changed bytes at boundary", size)
		}
	}
	image, err := Build("true", "null", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if string(parseSeedDirectory(t, image)["meta-data"]) != "instance-id: \"true\"\nlocal-hostname: \"null\"\n" {
		t.Fatal("implicit YAML scalar type")
	}
}

// Optional independent on-disk parser check. The ISO utility is a test-time
// tool only; production seed generation never shells out or mounts the image.
func TestIndependentISOReader(t *testing.T) {
	tool := os.Getenv("TENKA_TEST_ISOINFO")
	if tool == "" {
		t.Skip("set TENKA_TEST_ISOINFO to a trusted isoinfo executable")
	}
	data := []byte("#!/bin/sh\necho original-bytes\n")
	if input := os.Getenv("TENKA_TEST_USER_DATA"); input != "" {
		var err error
		data, err = os.ReadFile(input)
		if err != nil {
			t.Fatal(err)
		}
	}

	t.Run("provided-user-data", func(t *testing.T) { verifyIndependentISO(t, tool, data) })
	for _, size := range []int{1, 2047, 2048, 2049, MaxUserDataBytes} {
		t.Run(strconv.Itoa(size), func(t *testing.T) { verifyIndependentISO(t, tool, bytes.Repeat([]byte{0xff}, size)) })
	}
}

func verifyIndependentISO(t *testing.T, tool string, data []byte) {
	t.Helper()
	image, err := Build("fresh-lease-1", "guest-1", data)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "seed.iso")
	if err = os.WriteFile(file, image, 0600); err != nil {
		t.Fatal(err)
	}
	if verifier := os.Getenv("TENKA_TEST_ISOVFY"); verifier != "" {
		report, verifyErr := exec.Command(verifier, "-i", file).CombinedOutput()
		if verifyErr != nil || !bytes.Contains(report, []byte("No errors found")) {
			t.Fatalf("ISO structure verification: %s %v", report, verifyErr)
		}
	}
	out, err := exec.Command(tool, "-R", "-i", file, "-f").CombinedOutput()
	if err != nil {
		t.Fatalf("isoinfo: %v: %s", err, out)
	}
	if string(out) != "/meta-data\n/user-data\n" {
		t.Fatalf("names: %q", out)
	}
	for name, want := range map[string][]byte{"user-data": data, "meta-data": []byte("instance-id: \"fresh-lease-1\"\nlocal-hostname: \"guest-1\"\n")} {
		out, err = exec.Command(tool, "-R", "-i", file, "-x", "/"+name).CombinedOutput()
		if err != nil || !bytes.Equal(out, want) {
			t.Fatalf("%s extraction mismatch (got %d bytes, want %d): %v", name, len(out), len(want), err)
		}
	}
	out, err = exec.Command(tool, "-d", "-i", file).CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("Volume id: cidata")) || !bytes.Contains(out, []byte("Rock Ridge signatures version 1 found")) {
		t.Fatalf("descriptor: %s %v", out, err)
	}
}
