package oracle_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSimpleDBFirstOracle(t *testing.T) {
	pinBytes, err := os.ReadFile("../files.sha256.json")
	if err != nil {
		t.Fatalf("HARNESS_ERROR hash manifest: %v", err)
	}
	var pins struct {
		Files map[string]string `json:"files"`
	}
	if err = json.Unmarshal(pinBytes, &pins); err != nil || len(pins.Files) != 8 {
		t.Fatalf("HARNESS_ERROR hash manifest shape: %v, files=%d", err, len(pins.Files))
	}
	for name, want := range pins.Files {
		if filepath.IsAbs(name) || filepath.Clean(name) != name || strings.HasPrefix(name, "..") {
			t.Fatalf("HARNESS_ERROR unsafe manifest path %q", name)
		}
		raw, e := os.ReadFile(filepath.Join("..", name))
		if e != nil {
			t.Fatalf("HARNESS_ERROR pinned input %s: %v", name, e)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != want {
			t.Fatalf("HARNESS_ERROR altered oracle/helper %s", name)
		}
	}
	data, err := os.ReadFile("fixtures.json")
	if err != nil {
		t.Fatalf("HARNESS_ERROR fixture: %v", err)
	}
	var f struct {
		APIVersion string   `json:"api_version"`
		Domains    []string `json:"domains"`
		Negative   []struct {
			ID, Action, Code string
			Status           int
		} `json:"negative_cases"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("HARNESS_ERROR JSON: %v", err)
	}
	if f.APIVersion != "2009-04-15" || len(f.Domains) != 3 || len(f.Negative) != 12 {
		t.Fatal("HARNESS_ERROR required fixture coverage missing")
	}
	seen := map[string]bool{}
	for _, c := range f.Negative {
		if c.ID == "" || seen[c.ID] || c.Action == "" || c.Status != 400 || c.Code == "" {
			t.Fatalf("HARNESS_ERROR malformed case: %+v", c)
		}
		seen[c.ID] = true
	}
}
