package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/sakura"
)

func parsedSakuraFlags(t *testing.T, args ...string) sakuraCLIFlags {
	t.Helper()
	set := flag.NewFlagSet("sakura-test", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	values, e := registerSakuraFlags(set)
	if e != nil {
		t.Fatal(e)
	}
	if e = set.Parse(args); e != nil {
		t.Fatal("flag parse failed")
	}
	if set.NArg() != 0 {
		t.Fatal("unexpected test positional argument")
	}
	return *values
}
func enabledSakuraArgs() []string {
	return []string{"--enable-sakura-disks", "--sakura-environment=team-a", "--sakura-project=practice", "--sakura-zone=is1a", "--sakura-owner=learner"}
}
func TestSakuraFlagsDefaultDisabledAndExplicitScopeCopy(t *testing.T) {
	for _, args := range [][]string{nil, {"--enable-sakura-disks=false"}} {
		values := parsedSakuraFlags(t, args...)
		for _, unsafe := range []bool{false, true} {
			scope, e := validateSakuraFlags(values, unsafe)
			if e != nil || scope != nil {
				t.Fatal("default provider became enabled", e)
			}
		}
	}
	values := parsedSakuraFlags(t, enabledSakuraArgs()...)
	scope, e := validateSakuraFlags(values, false)
	if e != nil {
		t.Fatal(e)
	}
	want := sakura.Config{Environment: "team-a", Project: "practice", Zone: "is1a", Owner: "learner"}
	if scope == nil || *scope != want {
		t.Fatal("parsed scope mismatch")
	}
	scope.Owner = "changed"
	values.scope.Environment = "changed"
	if values.scope.Owner != want.Owner || scope.Environment != want.Environment {
		t.Fatal("validated scope aliases mutable flag values")
	}
	// Enabling after the four string flags must behave identically.
	args := enabledSakuraArgs()
	args = append(args[1:], args[0])
	scope, e = validateSakuraFlags(parsedSakuraFlags(t, args...), false)
	if e != nil || *scope != want {
		t.Fatal("flag order changed configuration", e)
	}
}
func TestSakuraFlagsRequireAllScopeAndRejectOrphans(t *testing.T) {
	complete := enabledSakuraArgs()
	for remove := 1; remove < len(complete); remove++ {
		args := append([]string{}, complete[:remove]...)
		args = append(args, complete[remove+1:]...)
		if scope, e := validateSakuraFlags(parsedSakuraFlags(t, args...), false); scope != nil || e == nil {
			t.Fatal("missing scope flag was defaulted")
		}
	}
	for _, name := range []string{"sakura-environment", "sakura-project", "sakura-zone", "sakura-owner"} {
		for _, value := range []string{"", "dummy"} {
			args := []string{"--" + name + "=" + value}
			if scope, e := validateSakuraFlags(parsedSakuraFlags(t, args...), false); scope != nil || e == nil {
				t.Fatal("orphan explicit scope was ignored")
			}
		}
	}
	for _, args := range [][]string{{"--enable-sakura-disks"}, enabledSakuraArgs()} {
		if scope, e := validateSakuraFlags(parsedSakuraFlags(t, args...), true); scope != nil || e == nil {
			t.Fatal("unsafe enabled provider accepted")
		}
	}
	// A manually supplied nonzero value is not silently ignored either.
	if scope, e := validateSakuraFlags(sakuraCLIFlags{scope: sakura.Config{Owner: "learner"}}, false); scope != nil || e == nil {
		t.Fatal("nonzero disabled scope ignored")
	}
}
func TestSakuraFlagsUseFactoryBoundsAndLocalIdentitySyntax(t *testing.T) {
	cases := []struct {
		name, value string
		valid       bool
	}{
		{"sakura-environment", strings.Repeat("界", 128), true}, {"sakura-environment", strings.Repeat("界", 129), false},
		{"sakura-project", strings.Repeat("p", 128), true}, {"sakura-project", strings.Repeat("p", 129), false},
		{"sakura-environment", " leading", false}, {"sakura-project", "bad\x00label", false}, {"sakura-project", string([]byte{255}), false},
		{"sakura-zone", strings.Repeat("a", 32), true}, {"sakura-zone", strings.Repeat("a", 33), false}, {"sakura-zone", "Is1a", false}, {"sakura-zone", "-zone", false}, {"sakura-zone", "is1a/path", false},
		{"sakura-owner", strings.Repeat("a", 64), true}, {"sakura-owner", strings.Repeat("a", 65), false}, {"sakura-owner", "with space", false}, {"sakura-owner", "日本語", false}, {"sakura-owner", "simulator-admin", true},
	}
	for _, tc := range cases {
		t.Run(tc.name+"-bound", func(t *testing.T) {
			args := enabledSakuraArgs()
			for i, arg := range args {
				if strings.HasPrefix(arg, "--"+tc.name+"=") {
					args[i] = "--" + tc.name + "=" + tc.value
				}
			}
			scope, e := validateSakuraFlags(parsedSakuraFlags(t, args...), false)
			if tc.valid {
				if scope == nil || e != nil {
					t.Fatal("supported local bound rejected", e)
				}
			} else if scope != nil || e == nil {
				t.Fatal("invalid local bound accepted")
			}
		})
	}
}
func TestSakuraFlagsValidationDoesNotTouchStateOrTokenFiles(t *testing.T) {
	// These are only helper/parse tests. They do not invoke run and cannot prove
	// real CLI ordering, process isolation, or listener startup/shutdown behavior.
	dir := t.TempDir()
	state := filepath.Join(dir, "state.db")
	token := filepath.Join(dir, "token")
	if e := os.WriteFile(state, []byte("retained-test-state"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(token, []byte("retained-test-token-placeholder"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{nil, enabledSakuraArgs(), {"--sakura-owner="}, {"--enable-sakura-disks"}} {
		values := parsedSakuraFlags(t, args...)
		_, _ = validateSakuraFlags(values, false)
		_, _ = validateSakuraFlags(values, true)
	}
	got, e := os.ReadFile(state)
	if e != nil || string(got) != "retained-test-state" {
		t.Fatal("state file changed")
	}
	got, e = os.ReadFile(token)
	if e != nil || string(got) != "retained-test-token-placeholder" {
		t.Fatal("token placeholder changed")
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 2 {
		t.Fatal("validation created extra files")
	}
	if values, e := registerSakuraFlags(nil); values != nil || e == nil {
		t.Fatal("nil flag set accepted")
	}
}
