package catalog

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func checkRDSInstanceGroupConfiguration(t *testing.T, instance *xnode, parameter, option, status string) {
	t.Helper()
	if instance == nil || nodeText(instance, "DBInstanceStatus") != status {
		t.Fatalf("want DB instance status %q in %+v", status, instance)
	}
	pg := instance.get("DBParameterGroups", "DBParameterGroup")
	og := instance.get("OptionGroupMemberships", "OptionGroupMembership")
	if pg == nil || nodeText(pg, "DBParameterGroupName") != parameter || pg.get("ParameterApplyStatus") != nil {
		t.Fatalf("want configured parameter group %q without application status, got %+v", parameter, pg)
	}
	if og == nil || nodeText(og, "OptionGroupName") != option || og.get("Status") != nil {
		t.Fatalf("want configured option group %q without synchronization status, got %+v", option, og)
	}
	if instance.get("Endpoint") != nil {
		t.Fatal("configuration record reported an endpoint")
	}
}

func TestRDSInstanceGroupConfigurationAcrossSQLiteRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rds.db")
	p := &policy{deny: map[string]bool{}}
	open := func() (*core.Store, *harness) {
		store, err := core.New(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		cfg := Config{
			Regions: []string{"us-east-1"}, Account: "000000000000",
			DefaultPrincipal: "simulator-admin", Authorize: p.authorize,
		}
		engine, err := New(store.Database(), cfg, []*Service{rds})
		if err != nil {
			t.Fatal(err)
		}
		return store, &harness{t: t, db: store.Database(), engine: engine, policy: p}
	}
	store, h := open()
	const liveARN = "arn:aws:rds:us-east-1:000000000000:db:current"
	const oldARN = "arn:aws:rds:us-east-1:000000000000:db:legacy"
	// This is the persisted output-shaped document from the old metadata
	// behavior: group names and optimistic Status, with no live endpoint.
	tx, err := h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	legacy := row{Service: "rds", Region: "us-east-1", Kind: "dbInstance", ID: "legacy", Name: "legacy", ARN: oldARN,
		Document: map[string]any{
			"DBInstanceArn": oldARN, "DBInstanceIdentifier": "legacy", "DBInstanceStatus": "creating", "Engine": "postgres",
			"DBInstanceClass": "db.t3.micro", "DBName": "retained", "BackupRetentionPeriod": 7,
			"DBParameterGroups":      []any{map[string]any{"DBParameterGroupName": "legacy-params", "ParameterApplyStatus": "in-sync"}},
			"OptionGroupMemberships": []any{map[string]any{"OptionGroupName": "legacy-options", "Status": "in-sync"}},
		}, Tags: map[string]string{"fixture": "retained"}}
	if err := insertNewResource(tx, rds.resource("dbInstance"), &legacy, nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	raw := func(arn string) string {
		t.Helper()
		var doc string
		if err := h.db.QueryRow("SELECT document FROM aws_catalog_resources WHERE arn=?", arn).Scan(&doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	legacyDoc := raw(oldARN)

	create := h.query("CreateDBInstance", "DBInstanceIdentifier", "current", "Engine", "postgres",
		"DBInstanceClass", "db.t3.micro", "DBParameterGroupName", "current-params", "OptionGroupName", "current-options",
		"VpcSecurityGroupIds.VpcSecurityGroupId.1", "sg-0123456789abcdef0")
	created := create.result(t, "CreateDBInstance").get("DBInstance")
	checkRDSInstanceGroupConfiguration(t, created, "current-params", "current-options", "creating")
	if doc := raw(liveARN); strings.Contains(doc, "in-sync") || strings.Contains(doc, "ParameterApplyStatus") {
		t.Fatalf("create stored synthetic runtime status: %s", doc)
	}
	if got := nodeText(created, "VpcSecurityGroups", "VpcSecurityGroupMembership", "Status"); got != "active" {
		t.Fatalf("security group membership changed: %q", got)
	}
	describe := func(name, parameter, option string) {
		t.Helper()
		res := h.query("DescribeDBInstances", "DBInstanceIdentifier", name)
		items := res.result(t, "DescribeDBInstances").get("DBInstances").all("DBInstance")
		if len(items) != 1 || nodeText(items[0], "DBInstanceIdentifier") != name {
			t.Fatalf("wrong instance: %s", res.raw)
		}
		checkRDSInstanceGroupConfiguration(t, items[0], parameter, option, "creating")
	}
	describe("current", "current-params", "current-options")
	describe("legacy", "legacy-params", "legacy-options")
	listed := h.query("DescribeDBInstances").result(t, "DescribeDBInstances").get("DBInstances").all("DBInstance")
	if len(listed) != 2 || nodeText(listed[0], "DBInstanceIdentifier") != "legacy" || nodeText(listed[1], "DBInstanceIdentifier") != "current" {
		t.Fatalf("wrong list: %+v", listed)
	}
	checkRDSInstanceGroupConfiguration(t, listed[0], "legacy-params", "legacy-options", "creating")
	checkRDSInstanceGroupConfiguration(t, listed[1], "current-params", "current-options", "creating")

	p.deny["rds:DescribeDBInstances"] = true
	expectFault(t, h.query("DescribeDBInstances", "DBInstanceIdentifier", "legacy"), 403, "AccessDenied")
	delete(p.deny, "rds:DescribeDBInstances")
	p.deny["rds:ModifyDBInstance "+oldARN] = true
	expectFault(t, h.query("ModifyDBInstance", "DBInstanceIdentifier", "legacy", "OptionGroupName", "never-saved"), 403, "AccessDenied")
	delete(p.deny, "rds:ModifyDBInstance "+oldARN)
	if got := raw(oldARN); got != legacyDoc {
		t.Fatalf("readback or denied modify changed legacy storage: %s", got)
	}
	modify := h.query("ModifyDBInstance", "DBInstanceIdentifier", "current",
		"DBParameterGroupName", "updated-params", "OptionGroupName", "updated-options")
	checkRDSInstanceGroupConfiguration(t, modify.result(t, "ModifyDBInstance").get("DBInstance"),
		"updated-params", "updated-options", "creating")
	if doc := raw(liveARN); strings.Contains(doc, "in-sync") || strings.Contains(doc, "ParameterApplyStatus") {
		t.Fatalf("rewrote synthetic runtime status: %s", doc)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, h = open()

	describe("current", "updated-params", "updated-options")
	describe("legacy", "legacy-params", "legacy-options")
	if got := raw(oldARN); got != legacyDoc {
		t.Fatalf("restart/readback changed legacy storage: %s", got)
	}
	expectFault(t, h.query("DeleteDBInstance", "DBInstanceIdentifier", "legacy", "SkipFinalSnapshot", "false"), 400, "InvalidParameterValue")
	p.deny["rds:DeleteDBInstance "+oldARN] = true
	expectFault(t, h.query("DeleteDBInstance", "DBInstanceIdentifier", "legacy", "SkipFinalSnapshot", "true"), 403, "AccessDenied")
	delete(p.deny, "rds:DeleteDBInstance "+oldARN)
	describe("legacy", "legacy-params", "legacy-options")
	if got := raw(oldARN); got != legacyDoc {
		t.Fatalf("refused deletes or readback changed legacy storage: %s", got)
	}

	storedIdentity := func() (sequence, generation int64, tags string) {
		t.Helper()
		if err := h.db.QueryRow("SELECT seq,generation,tags FROM aws_catalog_resources WHERE arn=?", oldARN).
			Scan(&sequence, &generation, &tags); err != nil {
			t.Fatal(err)
		}
		return
	}
	sequence, generation, tags := storedIdentity()
	// A successful, unrelated metadata edit is the migration opportunity.
	// It must keep both configured group names plus every other saved field.
	changeLegacy := h.query("ModifyDBInstance", "DBInstanceIdentifier", "legacy", "BackupRetentionPeriod", "8")
	legacyModified := changeLegacy.result(t, "ModifyDBInstance").get("DBInstance")
	checkRDSInstanceGroupConfiguration(t, legacyModified, "legacy-params", "legacy-options", "creating")
	if nodeText(legacyModified, "BackupRetentionPeriod") != "8" || nodeText(legacyModified, "DBName") != "retained" {
		t.Fatalf("authorized modify did not retain unrelated settings: %s", changeLegacy.raw)
	}
	describe("legacy", "legacy-params", "legacy-options")

	var expectedDocument map[string]any
	if err := json.Unmarshal([]byte(legacyDoc), &expectedDocument); err != nil {
		t.Fatal(err)
	}
	expectedDocument["BackupRetentionPeriod"] = 8
	expectedDocument["DBParameterGroups"] = []any{map[string]any{"DBParameterGroupName": "legacy-params"}}
	expectedDocument["OptionGroupMemberships"] = []any{map[string]any{"OptionGroupName": "legacy-options"}}
	expectedJSON, err := json.Marshal(expectedDocument)
	if err != nil {
		t.Fatal(err)
	}
	if got := raw(oldARN); got != string(expectedJSON) {
		t.Fatalf("modify changed more than the requested setting and stale statuses: %s", got)
	}
	if seq, gen, keptTags := storedIdentity(); seq != sequence || gen != generation+1 || keptTags != tags {
		t.Fatalf("modify changed resource identity or tags: seq=%d gen=%d tags=%s", seq, gen, keptTags)
	}

	for _, current := range []struct{ name, parameter, option string }{
		{"current", "updated-params", "updated-options"},
		{"legacy", "legacy-params", "legacy-options"},
	} {
		deleted := h.query("DeleteDBInstance", "DBInstanceIdentifier", current.name, "SkipFinalSnapshot", "true")
		checkRDSInstanceGroupConfiguration(t, deleted.result(t, "DeleteDBInstance").get("DBInstance"),
			current.parameter, current.option, "deleting")
		expectFault(t, h.query("DescribeDBInstances", "DBInstanceIdentifier", current.name), 404, "DBInstanceNotFound")
	}
}

func TestRDSInstanceGroupConfigurationLeavesSharedEnginesUnchanged(t *testing.T) {
	for _, engine := range []string{"aurora-postgresql", "aurora-mysql", "docdb", "neptune"} {
		t.Run(engine, func(t *testing.T) {
			h := newHarness(t, rds)
			created := h.query("CreateDBInstance", "DBInstanceIdentifier", engine, "Engine", engine, "DBInstanceClass", "db.t3.micro",
				"DBParameterGroupName", "compatible-params", "OptionGroupName", "compatible-options")
			instance := created.result(t, "CreateDBInstance").get("DBInstance")
			if nodeText(instance, "DBParameterGroups", "DBParameterGroup", "ParameterApplyStatus") != "in-sync" ||
				nodeText(instance, "OptionGroupMemberships", "OptionGroupMembership", "Status") != "in-sync" {
				t.Fatalf("shared engine response changed: %s", created.raw)
			}
		})
	}
}

func TestRDSInstanceGroupConfigurationDoesNotInventDefaults(t *testing.T) {
	h := newHarness(t, rds)
	create := h.query("CreateDBInstance", "DBInstanceIdentifier", "no-groups", "Engine", "postgres", "DBInstanceClass", "db.t3.micro")
	list := h.query("DescribeDBInstances", "DBInstanceIdentifier", "no-groups")
	for _, instance := range []*xnode{
		create.result(t, "CreateDBInstance").get("DBInstance"),
		list.result(t, "DescribeDBInstances").get("DBInstances", "DBInstance"),
	} {
		if instance == nil || instance.get("DBParameterGroups") != nil || instance.get("OptionGroupMemberships") != nil {
			t.Fatalf("omitted settings acquired synthetic default groups: %+v", instance)
		}
	}
	modify := h.query("ModifyDBInstance", "DBInstanceIdentifier", "no-groups",
		"DBParameterGroupName", "default.postgres15", "OptionGroupName", "default:postgres-15")
	checkRDSInstanceGroupConfiguration(t, modify.result(t, "ModifyDBInstance").get("DBInstance"),
		"default.postgres15", "default:postgres-15", "creating")
	list = h.query("DescribeDBInstances", "DBInstanceIdentifier", "no-groups")
	checkRDSInstanceGroupConfiguration(t, list.result(t, "DescribeDBInstances").get("DBInstances", "DBInstance"),
		"default.postgres15", "default:postgres-15", "creating")
}
