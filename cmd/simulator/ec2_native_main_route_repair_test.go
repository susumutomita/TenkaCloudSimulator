//go:build (linux || darwin) && !sqlite3_flock && !sqlite3_dotlk

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func repairCLIDB(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	store, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEC2NativeMainRepairVerifiesLiveSQLiteInodeAndMode(t *testing.T) {
	aPath, bPath := repairCLIDB(t, "original.sqlite"), repairCLIDB(t, "different.sqlite")
	a, err := openMainRepairFile(aPath)
	if err != nil {
		t.Fatal(err)
	}
	defer a.file.Close()
	b, err := openMainRepairFile(bPath)
	if err != nil {
		t.Fatal(err)
	}
	defer b.file.Close()
	conn, closeDB, err := b.database(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	if err := a.verifyVFS(conn, false); err == nil || !strings.Contains(err.Error(), "live SQLite file") {
		t.Fatal("allowed the wrong actual database handle", err)
	}
	if err := b.verifyVFS(conn, true); err == nil || !strings.Contains(err.Error(), "wrong repair access mode") {
		t.Fatal("accepted read-only connection for repair transaction", err)
	}
	if err := b.verifyVFS(conn, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(bPath, bPath+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("different file at old path"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.checkIdentity(false); err == nil {
		t.Fatal("accepted a canonical path after it was replaced")
	}
}

func TestEC2NativeMainRepairRejectsInvalidFileAuthorityWithoutBootstrap(t *testing.T) {
	path := repairCLIDB(t, "state.sqlite")
	for _, target := range []string{":memory:", "file:" + path, path + ".missing"} {
		var output bytes.Buffer
		err := run(t.Context(), []string{ec2NativeMainRepairCommand, "plan", "--state", target, "--region", "us-east-1",
			"--vpc-id", "vpc-00000000000000172"}, &output)
		if err == nil || output.Len() != 0 {
			t.Fatal("maintenance CLI initialized or emitted success for an invalid path", target, err, output.String())
		}
	}
	if _, err := os.Stat(path + ".missing"); !os.IsNotExist(err) {
		t.Fatal("plan created a state file", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if opened, err := openMainRepairFile(path); err == nil {
		opened.file.Close()
		t.Fatal("accepted state exposed to other users")
	}
}

func TestEC2NativeMainRepairCLICopyAndRepeat(t *testing.T) {
	const region, vpc, table = "us-east-1", "vpc-00000000000000172", "rtb-00000000000000173"
	ctx := t.Context()
	original := repairCLIDB(t, "legacy.sqlite")
	working := filepath.Join(filepath.Dir(original), "working.sqlite")
	store, err := core.New(original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aws.NewStackStore(store.Database()); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	for _, oldRow := range []aws.EC2NetworkResource{
		{ID: vpc, Kind: "AWS::EC2::VPC", Region: region, StackID: "ec2:native", CIDR: "10.190.0.0/16", DNSSupport: true, Revision: 1},
		{ID: table, Kind: "AWS::EC2::RouteTable", Region: region, StackID: "ec2:native", VPCID: vpc, Revision: 1},
	} {
		encoded, err := json.Marshal(oldRow)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Database().ExecContext(ctx, `INSERT INTO aws_ec2_network_resources VALUES(?,?,?,?,?,?)`,
			region, oldRow.ID, oldRow.StackID, oldRow.Kind, string(encoded), oldRow.Revision); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	read := func(path string) []byte {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	initial := read(original)
	if err := os.Chmod(original, 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(working, initial, 0600); err != nil {
		t.Fatal(err)
	}
	call := func(operation, state, refusal string, extra ...string) aws.EC2NativeMainRepairPlan {
		t.Helper()
		args := append([]string{ec2NativeMainRepairCommand, operation, "--state", state, "--region", region, "--vpc-id", vpc}, extra...)
		var output bytes.Buffer
		err := run(ctx, args, &output)
		if refusal != "" {
			if err == nil || !strings.Contains(err.Error(), refusal) || output.Len() != 0 {
				t.Fatal(refusal, err, output.String())
			}
			return aws.EC2NativeMainRepairPlan{}
		}
		var plan aws.EC2NativeMainRepairPlan
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(output.Bytes(), &plan); err != nil {
			t.Fatal(err)
		}
		return plan
	}
	initialPlan := call("plan", original, "")
	if initialPlan.Status != "ready" || initialPlan.ExtraPersistedRows != 1 || len(initialPlan.CustomRouteTableIDs) != 1 || initialPlan.CustomRouteTableIDs[0] != table {
		t.Fatal(initialPlan)
	}
	consent := []string{"--original", original, "--expect-plan", initialPlan.PlanDigest}
	call("apply", original, "distinct working copy", consent...)
	call("apply", working, "stale repair plan", "--original", original, "--expect-plan", strings.Repeat("e", 64))
	if !bytes.Equal(read(working), initial) || !bytes.Equal(read(original), initial) {
		t.Fatal("a plan or refusal changed saved state")
	}
	result := call("apply", working, "", consent...)
	if result.Status != "applied" || result.VPCRevision != 2 || result.PlanDigest != initialPlan.PlanDigest || result.MainRouteTableID != initialPlan.MainRouteTableID {
		t.Fatal(result)
	}
	saved := read(working)
	if bytes.Equal(saved, initial) || !bytes.Equal(read(original), initial) {
		t.Fatal("apply changed the original or did not change the working copy")
	}
	if repeated := call("apply", working, "", consent...); repeated.Status != "already-repaired" || repeated.ExtraPersistedRows != 0 || repeated.PlanDigest != initialPlan.PlanDigest {
		t.Fatal(repeated)
	}
	if !bytes.Equal(read(working), saved) || !bytes.Equal(read(original), initial) {
		t.Fatal("repeat changed a file")
	}
}
