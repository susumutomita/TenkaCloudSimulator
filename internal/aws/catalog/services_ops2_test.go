package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeadlineCloudLifecycle(t *testing.T) {
	h := newHarness(t, deadlineCloud)
	const base = "/2023-10-12/farms"
	farm := h.rest("deadline", "POST", base, map[string]any{"displayName": "studio", "description": "d", "tags": map[string]any{"team": "a"}})
	expect(t, farm, 200, "")
	farmID := str(farm.body["farmId"])
	if !strings.HasPrefix(farmID, "farm-") || len(farmID) != 37 {
		t.Fatalf("farm id %v", farm.body)
	}
	got := h.rest("deadline", "GET", base+"/"+farmID, nil)
	if got.body["displayName"] != "studio" || got.body["createdBy"] != "000000000000" {
		t.Fatalf("farm %v", got.body)
	}
	expect(t, h.rest("deadline", "PATCH", base+"/"+farmID, map[string]any{"displayName": "studio2"}), 200, "")
	expect(t, h.rest("deadline", "GET", base+"?principalId=x", nil), 400, "ValidationException")
	if farms := h.rest("deadline", "GET", base, nil).body["farms"].([]any); len(farms) != 1 || farms[0].(map[string]any)["displayName"] != "studio2" {
		t.Fatalf("farms %v", farms)
	}

	queue := h.rest("deadline", "POST", base+"/"+farmID+"/queues", map[string]any{"displayName": "q", "tags": map[string]any{"k": "v"}})
	expect(t, queue, 200, "")
	queueID := str(queue.body["queueId"])
	qpath := base + "/" + farmID + "/queues/" + queueID
	q := h.rest("deadline", "GET", qpath, nil)
	if q.body["status"] != "IDLE" || q.body["defaultBudgetAction"] != "NONE" || q.body["farmId"] != farmID {
		t.Fatalf("queue %v", q.body)
	}
	expect(t, h.rest("deadline", "PATCH", qpath, map[string]any{"defaultBudgetAction": "STOP_SCHEDULING_AND_CANCEL_TASKS"}), 200, "")
	expect(t, h.rest("deadline", "PATCH", qpath, map[string]any{"allowedStorageProfileIdsToAdd": []any{"sp-" + strings.Repeat("a", 32)}}), 400, "ValidationException")
	if h.rest("deadline", "GET", qpath, nil).body["defaultBudgetAction"] != "STOP_SCHEDULING_AND_CANCEL_TASKS" {
		t.Fatal("queue update not stored")
	}
	if qs := h.rest("deadline", "GET", base+"/"+farmID+"/queues?status=IDLE", nil).body["queues"].([]any); len(qs) != 1 {
		t.Fatalf("queues %v", qs)
	}
	if qs := h.rest("deadline", "GET", base+"/"+farmID+"/queues?status=SCHEDULING", nil).body["queues"].([]any); len(qs) != 0 {
		t.Fatalf("queues %v", qs)
	}

	fleetBody := map[string]any{"displayName": "f", "roleArn": "arn:aws:iam::000000000000:role/Fleet", "maxWorkerCount": 5,
		"configuration": map[string]any{"customerManaged": map[string]any{"mode": "NO_SCALING", "workerCapabilities": map[string]any{
			"vCpuCount": map[string]any{"min": 1}, "memoryMiB": map[string]any{"min": 1024}, "osFamily": "LINUX", "cpuArchitectureType": "x86_64"}}}}
	fleet := h.rest("deadline", "POST", base+"/"+farmID+"/fleets", fleetBody)
	expect(t, fleet, 200, "")
	fpath := base + "/" + farmID + "/fleets/" + str(fleet.body["fleetId"])
	f := h.rest("deadline", "GET", fpath, nil)
	if f.body["status"] != "ACTIVE" || f.body["workerCount"] != json.Number("0") || f.body["minWorkerCount"] != json.Number("0") {
		t.Fatalf("fleet %v", f.body)
	}
	expect(t, h.rest("deadline", "PATCH", fpath, map[string]any{"minWorkerCount": 9}), 400, "ValidationException")
	expect(t, h.rest("deadline", "PATCH", fpath, map[string]any{"maxWorkerCount": 8, "minWorkerCount": 2}), 200, "")
	bad := map[string]any{"displayName": "f", "roleArn": "arn:aws:iam::000000000000:role/Fleet", "maxWorkerCount": 1, "minWorkerCount": 2, "configuration": fleetBody["configuration"]}
	expect(t, h.rest("deadline", "POST", base+"/"+farmID+"/fleets", bad), 400, "ValidationException")
	bad["minWorkerCount"], bad["configuration"] = 0, map[string]any{}
	expect(t, h.rest("deadline", "POST", base+"/"+farmID+"/fleets", bad), 400, "ValidationException")
	expect(t, h.rest("deadline", "PATCH", fpath, map[string]any{"configuration": map[string]any{}}), 400, "ValidationException")
	if fs := h.rest("deadline", "GET", base+"/"+farmID+"/fleets?status=ACTIVE&displayName=f", nil).body["fleets"].([]any); len(fs) != 1 {
		t.Fatalf("fleets %v", fs)
	}

	sp := h.rest("deadline", "POST", base+"/"+farmID+"/storage-profiles", map[string]any{"displayName": "sp", "osFamily": "LINUX",
		"fileSystemLocations": []any{map[string]any{"name": "assets", "path": "/mnt/assets", "type": "SHARED"}}})
	expect(t, sp, 200, "")
	spath := base + "/" + farmID + "/storage-profiles/" + str(sp.body["storageProfileId"])
	expect(t, h.rest("deadline", "PATCH", spath, map[string]any{"osFamily": "MACOS"}), 200, "")
	if h.rest("deadline", "GET", spath, nil).body["osFamily"] != "MACOS" {
		t.Fatal("storage profile update")
	}
	if l := h.rest("deadline", "GET", base+"/"+farmID+"/storage-profiles", nil).body["storageProfiles"].([]any); len(l) != 1 {
		t.Fatalf("storage profiles %v", l)
	}

	limit := h.rest("deadline", "POST", base+"/"+farmID+"/limits", map[string]any{"displayName": "l", "amountRequirementName": "amount.license", "maxCount": 4})
	expect(t, limit, 200, "")
	lpath := base + "/" + farmID + "/limits/" + str(limit.body["limitId"])
	expect(t, h.rest("deadline", "POST", base+"/"+farmID+"/limits", map[string]any{"displayName": "l", "amountRequirementName": "amount.x", "maxCount": 0}), 400, "ValidationException")
	expect(t, h.rest("deadline", "PATCH", lpath, map[string]any{"maxCount": 0}), 400, "ValidationException")
	expect(t, h.rest("deadline", "PATCH", lpath, map[string]any{"maxCount": -1}), 200, "")
	if g := h.rest("deadline", "GET", lpath, nil); g.body["maxCount"] != json.Number("-1") || g.body["currentCount"] != json.Number("0") {
		t.Fatalf("limit %v", g.body)
	}
	if l := h.rest("deadline", "GET", base+"/"+farmID+"/limits", nil).body["limits"].([]any); len(l) != 1 {
		t.Fatalf("limits %v", l)
	}

	budget := h.rest("deadline", "POST", base+"/"+farmID+"/budgets", map[string]any{"displayName": "b", "approximateDollarLimit": 100.5,
		"usageTrackingResource": map[string]any{"queueId": queueID},
		"actions":               []any{map[string]any{"type": "STOP_SCHEDULING_AND_COMPLETE_TASKS", "thresholdPercentage": 90}},
		"schedule":              map[string]any{"fixed": map[string]any{"startTime": 1700000000, "endTime": 1800000000}}})
	expect(t, budget, 200, "")
	bpath := base + "/" + farmID + "/budgets/" + str(budget.body["budgetId"])
	expect(t, h.rest("deadline", "PATCH", bpath, map[string]any{"status": "INACTIVE", "approximateDollarLimit": 50}), 200, "")
	if g := h.rest("deadline", "GET", bpath, nil); g.body["status"] != "INACTIVE" || g.body["usages"].(map[string]any)["approximateDollarUsage"] != json.Number("0") {
		t.Fatalf("budget %v", g.body)
	}
	if l := h.rest("deadline", "GET", base+"/"+farmID+"/budgets?status=ACTIVE", nil).body["budgets"].([]any); len(l) != 0 {
		t.Fatalf("budgets %v", l)
	}
	if l := h.rest("deadline", "GET", base+"/"+farmID+"/budgets?status=INACTIVE", nil).body["budgets"].([]any); len(l) != 1 {
		t.Fatalf("budgets %v", l)
	}

	// Tags.
	tagPath := func(arn string) string {
		return "/2023-10-12/tags/" + strings.NewReplacer(":", "%3A", "/", "%2F").Replace(arn)
	}
	arn := "arn:aws:deadline:us-east-1:000000000000:farm/" + farmID
	if tags := h.rest("deadline", "GET", tagPath(arn), nil).body["tags"].(map[string]any); tags["team"] != "a" {
		t.Fatalf("farm tags %v", tags)
	}
	expect(t, h.rest("deadline", "POST", tagPath(arn+"/queue/"+queueID), map[string]any{"tags": map[string]any{"x": "y"}}), 204, "")
	if tags := h.rest("deadline", "GET", tagPath(arn+"/queue/"+queueID), nil).body["tags"].(map[string]any); tags["x"] != "y" || tags["k"] != "v" {
		t.Fatalf("queue tags %v", tags)
	}
	expect(t, h.rest("deadline", "DELETE", tagPath(arn+"/queue/"+queueID)+"?tagKeys=x", nil), 204, "")
	expect(t, h.rest("deadline", "GET", tagPath(arn+"/budget/"+strings.TrimPrefix(bpath, base+"/"+farmID+"/budgets/")), nil), 400, "ValidationException")
	expect(t, h.rest("deadline", "GET", tagPath(arn+"/queue/fleet-"+strings.Repeat("a", 32)), nil), 400, "ValidationException")
	expect(t, h.rest("deadline", "GET", tagPath(arn+"/fleet/queue-"+strings.Repeat("a", 32)), nil), 400, "ValidationException")

	// A farm with children is kept; children go first.
	expect(t, h.rest("deadline", "DELETE", base+"/"+farmID, nil), 400, "ValidationException")
	for _, p := range []string{bpath, lpath, spath, fpath, qpath} {
		expect(t, h.rest("deadline", "DELETE", p, nil), 200, "")
	}
	expect(t, h.rest("deadline", "GET", qpath, nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("deadline", "DELETE", base+"/"+farmID, nil), 200, "")
	expect(t, h.rest("deadline", "GET", base+"/"+farmID, nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("deadline", "POST", "/2023-10-12/monitors", map[string]any{"displayName": "m"}), 501, "UnknownOperationException")
	expect(t, h.rest("deadline", "POST", base+"/"+farmID+"/queues", map[string]any{"displayName": "q"}), 404, "ResourceNotFoundException")
}

func TestDatabaseMigrationLifecycle(t *testing.T) {
	h := newHarness(t, databaseMigration)
	call := func(op string, input map[string]any) reply { return h.json1("dms", op, input) }
	list := func(op, key string) []any { return call(op, map[string]any{}).body[key].([]any) }

	// Replication subnet groups.
	expect(t, call("CreateReplicationSubnetGroup", map[string]any{"ReplicationSubnetGroupIdentifier": "default", "ReplicationSubnetGroupDescription": "d", "SubnetIds": []any{"subnet-a", "subnet-b"}}), 400, "ValidationException")
	expect(t, call("CreateReplicationSubnetGroup", map[string]any{"ReplicationSubnetGroupIdentifier": "bad name", "ReplicationSubnetGroupDescription": "d", "SubnetIds": []any{"subnet-a", "subnet-b"}}), 400, "")
	expect(t, call("CreateReplicationSubnetGroup", map[string]any{"ReplicationSubnetGroupIdentifier": "one", "ReplicationSubnetGroupDescription": "d", "SubnetIds": []any{"subnet-a"}}), 400, "")
	sg := call("CreateReplicationSubnetGroup", map[string]any{"ReplicationSubnetGroupIdentifier": "MyGroup", "ReplicationSubnetGroupDescription": "d",
		"SubnetIds": []any{"subnet-a", "subnet-b"}})
	expect(t, sg, 200, "")
	group := sg.body["ReplicationSubnetGroup"].(map[string]any)
	if group["ReplicationSubnetGroupIdentifier"] != "mygroup" || group["SubnetGroupStatus"] != "Complete" || len(group["Subnets"].([]any)) != 2 {
		t.Fatalf("subnet group %v", group)
	}
	expect(t, call("CreateReplicationSubnetGroup", map[string]any{"ReplicationSubnetGroupIdentifier": "mygroup", "ReplicationSubnetGroupDescription": "d", "SubnetIds": []any{"subnet-a", "subnet-b"}}), 400, "ResourceAlreadyExistsFault")
	expect(t, call("ModifyReplicationSubnetGroup", map[string]any{"ReplicationSubnetGroupIdentifier": "mygroup", "ReplicationSubnetGroupDescription": "e", "SubnetIds": []any{"subnet-c", "subnet-d", "subnet-e"}}), 200, "")
	if groups := list("DescribeReplicationSubnetGroups", "ReplicationSubnetGroups"); len(groups) != 1 || len(groups[0].(map[string]any)["Subnets"].([]any)) != 3 {
		t.Fatalf("groups %v", groups)
	}
	expect(t, call("DescribeReplicationSubnetGroups", map[string]any{"Filters": []any{map[string]any{"Name": "x", "Values": []any{"y"}}}}), 400, "")

	// Event subscriptions.
	topic := "arn:aws:sns:us-east-1:000000000000:events"
	expect(t, call("CreateEventSubscription", map[string]any{"SubscriptionName": "s", "SnsTopicArn": topic, "SourceType": "endpoint"}), 400, "")
	expect(t, call("CreateEventSubscription", map[string]any{"SubscriptionName": "s", "SnsTopicArn": topic, "SourceIds": []any{"task-1"}}), 400, "")
	expect(t, call("CreateEventSubscription", map[string]any{"SubscriptionName": strings.Repeat("a", 255), "SnsTopicArn": topic}), 400, "")
	sub := call("CreateEventSubscription", map[string]any{"SubscriptionName": "s", "SnsTopicArn": topic, "SourceType": "replication-task", "EventCategories": []any{"failure"}})
	expect(t, sub, 200, "")
	if s := sub.body["EventSubscription"].(map[string]any); s["Status"] != "active" || s["Enabled"] != true || s["CustomerAwsId"] != "000000000000" {
		t.Fatalf("subscription %v", s)
	}
	expect(t, call("ModifyEventSubscription", map[string]any{"SubscriptionName": "s", "Enabled": false, "SourceType": "replication-instance"}), 200, "")
	if subs := call("DescribeEventSubscriptions", map[string]any{"SubscriptionName": "s"}).body["EventSubscriptionsList"].([]any); len(subs) != 1 || subs[0].(map[string]any)["Enabled"] != false {
		t.Fatalf("subscriptions %v", subs)
	}
	if subs := call("DescribeEventSubscriptions", map[string]any{"SubscriptionName": "other"}).body["EventSubscriptionsList"].([]any); len(subs) != 0 {
		t.Fatalf("subscriptions %v", subs)
	}

	// Data providers.
	settings := map[string]any{"PostgreSqlSettings": map[string]any{"ServerName": "db.example.invalid", "Port": 5432, "DatabaseName": "app", "SslMode": "none"}}
	expect(t, call("CreateDataProvider", map[string]any{"Engine": "oracle2", "Settings": settings}), 400, "")
	expect(t, call("CreateDataProvider", map[string]any{"Engine": "postgres", "Settings": map[string]any{}}), 400, "")
	dp := call("CreateDataProvider", map[string]any{"Engine": "postgres", "DataProviderName": "source", "Settings": settings})
	expect(t, dp, 200, "")
	provider := dp.body["DataProvider"].(map[string]any)
	providerARN := str(provider["DataProviderArn"])
	if provider["DataProviderName"] != "source" || !strings.Contains(providerARN, ":dms:us-east-1:000000000000:data-provider:") {
		t.Fatalf("provider %v", provider)
	}
	unnamed := call("CreateDataProvider", map[string]any{"Engine": "mysql", "Settings": map[string]any{"MySqlSettings": map[string]any{"ServerName": "db2.example.invalid"}}})
	expect(t, unnamed, 200, "")
	if n := str(unnamed.body["DataProvider"].(map[string]any)["DataProviderName"]); len(n) != 26 {
		t.Fatalf("generated name %q", n)
	}
	expect(t, call("ModifyDataProvider", map[string]any{"DataProviderIdentifier": "source", "Settings": settings}), 400, "")
	expect(t, call("ModifyDataProvider", map[string]any{"DataProviderIdentifier": "source", "Settings": settings, "ExactSettings": true, "Description": "d"}), 200, "")
	expect(t, call("ModifyDataProvider", map[string]any{"DataProviderIdentifier": providerARN, "Engine": "bogus"}), 400, "")
	if ps := list("DescribeDataProviders", "DataProviders"); len(ps) != 2 {
		t.Fatalf("providers %v", ps)
	}

	// Instance profiles and migration projects.
	ip := call("CreateInstanceProfile", map[string]any{"InstanceProfileName": "profile", "SubnetGroupIdentifier": "mygroup", "VpcSecurityGroups": []any{"sg-a"}})
	expect(t, ip, 200, "")
	profile := ip.body["InstanceProfile"].(map[string]any)
	profileARN := str(profile["InstanceProfileArn"])
	if profile["PubliclyAccessible"] != true {
		t.Fatalf("profile %v", profile)
	}
	expect(t, call("ModifyInstanceProfile", map[string]any{"InstanceProfileIdentifier": "profile", "PubliclyAccessible": false, "Description": "x"}), 200, "")
	if ps := list("DescribeInstanceProfiles", "InstanceProfiles"); len(ps) != 1 || ps[0].(map[string]any)["PubliclyAccessible"] != false {
		t.Fatalf("profiles %v", ps)
	}
	descriptor := func(id string) []any {
		return []any{map[string]any{"DataProviderIdentifier": id, "SecretsManagerAccessRoleArn": "arn:aws:iam::000000000000:role/dms"}}
	}
	project := map[string]any{"MigrationProjectName": "proj", "SourceDataProviderDescriptors": descriptor(providerARN), "TargetDataProviderDescriptors": descriptor(providerARN), "InstanceProfileIdentifier": profileARN}
	expect(t, call("CreateMigrationProject", map[string]any{"SourceDataProviderDescriptors": descriptor("source"), "TargetDataProviderDescriptors": descriptor(providerARN), "InstanceProfileIdentifier": profileARN}), 400, "")
	expect(t, call("CreateMigrationProject", map[string]any{"SourceDataProviderDescriptors": []any{}, "TargetDataProviderDescriptors": descriptor(providerARN), "InstanceProfileIdentifier": profileARN}), 400, "")
	expect(t, call("CreateMigrationProject", map[string]any{"SourceDataProviderDescriptors": descriptor(providerARN), "TargetDataProviderDescriptors": descriptor(providerARN), "InstanceProfileIdentifier": "profile"}), 400, "")
	missing := strings.Replace(profileARN, "instance-profile:", "instance-profile:z", 1)
	missing = missing[:len(missing)-1]
	expect(t, call("CreateMigrationProject", map[string]any{"SourceDataProviderDescriptors": descriptor(providerARN), "TargetDataProviderDescriptors": descriptor(providerARN), "InstanceProfileIdentifier": missing}), 400, "ResourceNotFoundFault")
	created := call("CreateMigrationProject", project)
	expect(t, created, 200, "")
	mp := created.body["MigrationProject"].(map[string]any)
	if mp["InstanceProfileArn"] != profileARN || mp["SourceDataProviderDescriptors"].([]any)[0].(map[string]any)["DataProviderArn"] != providerARN {
		t.Fatalf("project %v", mp)
	}
	expect(t, call("ModifyMigrationProject", map[string]any{"MigrationProjectIdentifier": "proj", "Description": "d", "InstanceProfileIdentifier": profileARN}), 200, "")
	if ps := list("DescribeMigrationProjects", "MigrationProjects"); len(ps) != 1 || ps[0].(map[string]any)["Description"] != "d" {
		t.Fatalf("projects %v", ps)
	}

	// Tags cannot be represented and are refused.
	expect(t, call("CreateDataProvider", map[string]any{"Engine": "postgres", "Settings": settings, "Tags": []any{map[string]any{"Key": "k", "Value": "v"}}}), 400, "")
	expect(t, call("AddTagsToResource", map[string]any{"ResourceArn": providerARN, "Tags": []any{map[string]any{"Key": "k", "Value": "v"}}}), 501, "UnknownOperationException")

	// Deletion and unbound operations.
	for _, c := range []struct {
		op string
		in map[string]any
	}{
		{"DeleteMigrationProject", map[string]any{"MigrationProjectIdentifier": "proj"}},
		{"DeleteInstanceProfile", map[string]any{"InstanceProfileIdentifier": profileARN}},
		{"DeleteDataProvider", map[string]any{"DataProviderIdentifier": "source"}},
		{"DeleteEventSubscription", map[string]any{"SubscriptionName": "s"}},
		{"DeleteReplicationSubnetGroup", map[string]any{"ReplicationSubnetGroupIdentifier": "mygroup"}},
	} {
		expect(t, call(c.op, c.in), 200, "")
		expect(t, call(c.op, c.in), 400, "ResourceNotFoundFault")
	}
	expect(t, call("CreateEndpoint", map[string]any{"EndpointIdentifier": "e", "EndpointType": "source", "EngineName": "mysql"}), 501, "UnknownOperationException")
}

func TestCostExplorerLifecycle(t *testing.T) {
	h := newHarness(t, costExplorer)
	call := func(op string, input map[string]any) reply { return h.json1("ce", op, input) }
	rule := func(value string) map[string]any {
		return map[string]any{"Type": "REGULAR", "Value": value, "Rule": map[string]any{"Dimensions": map[string]any{"Key": "LINKED_ACCOUNT", "Values": []any{"000000000000"}}}}
	}

	// Cost categories.
	base := map[string]any{"Name": "teams", "RuleVersion": "CostCategoryExpression.v1", "Rules": []any{rule("a")}, "DefaultValue": "other",
		"ResourceTags": []any{map[string]any{"Key": "team", "Value": "a"}}}
	expect(t, call("CreateCostCategoryDefinition", map[string]any{"Name": "teams", "RuleVersion": "CostCategoryExpression.v1", "Rules": []any{}}), 400, "")
	expect(t, call("CreateCostCategoryDefinition", map[string]any{"Name": "teams", "RuleVersion": "CostCategoryExpression.v1", "Rules": []any{map[string]any{"Type": "INHERITED_VALUE", "Value": "x"}}}), 400, "")
	expect(t, call("CreateCostCategoryDefinition", map[string]any{"Name": "teams", "RuleVersion": "CostCategoryExpression.v1", "Rules": []any{map[string]any{"Type": "REGULAR", "InheritedValue": map[string]any{"DimensionName": "LINKED_ACCOUNT_NAME"}}}}), 400, "")
	expect(t, call("CreateCostCategoryDefinition", map[string]any{"Name": "teams", "RuleVersion": "CostCategoryExpression.v1", "Rules": []any{rule("a")}, "EffectiveStart": "2026-01-15T00:00:00Z"}), 400, "")
	expect(t, call("CreateCostCategoryDefinition", map[string]any{"Name": "", "RuleVersion": "CostCategoryExpression.v1", "Rules": []any{rule("a")}}), 400, "")
	inherited := map[string]any{"Name": "inherit", "RuleVersion": "CostCategoryExpression.v1", "EffectiveStart": "2026-01-01T00:00:00Z",
		"Rules": []any{map[string]any{"Type": "INHERITED_VALUE", "InheritedValue": map[string]any{"DimensionName": "LINKED_ACCOUNT_NAME"}}}}
	expect(t, call("CreateCostCategoryDefinition", inherited), 200, "")
	created := call("CreateCostCategoryDefinition", base)
	expect(t, created, 200, "")
	categoryARN := str(created.body["CostCategoryArn"])
	if !strings.HasPrefix(categoryARN, "arn:aws:ce::000000000000:costcategory/") || !strings.HasSuffix(str(created.body["EffectiveStart"]), "-01T00:00:00Z") {
		t.Fatalf("category %v", created.body)
	}
	expect(t, call("CreateCostCategoryDefinition", base), 400, "")
	got := call("DescribeCostCategoryDefinition", map[string]any{"CostCategoryArn": categoryARN, "EffectiveOn": "2026-01-01T00:00:00Z"})
	if c := got.body["CostCategory"].(map[string]any); c["Name"] != "teams" || c["DefaultValue"] != "other" {
		t.Fatalf("category %v", c)
	}
	expect(t, call("UpdateCostCategoryDefinition", map[string]any{"CostCategoryArn": categoryARN, "RuleVersion": "CostCategoryExpression.v1", "Rules": []any{rule("b")}}), 200, "")
	if c := call("DescribeCostCategoryDefinition", map[string]any{"CostCategoryArn": categoryARN}).body["CostCategory"].(map[string]any); c["DefaultValue"] != nil || c["Rules"].([]any)[0].(map[string]any)["Value"] != "b" {
		t.Fatalf("updated category %v", c)
	}
	if refs := call("ListCostCategoryDefinitions", map[string]any{"EffectiveOn": "2026-01-01T00:00:00Z"}).body["CostCategoryReferences"].([]any); len(refs) != 2 {
		t.Fatalf("references %v", refs)
	}
	expect(t, call("DescribeCostCategoryDefinition", map[string]any{"CostCategoryArn": "arn:aws:ce::000000000000:costcategory/00000000-0000-4000-8000-000000000000"}), 400, "ResourceNotFoundException")

	// Anomaly monitors.
	expect(t, call("CreateAnomalyMonitor", map[string]any{"AnomalyMonitor": map[string]any{"MonitorName": "m", "MonitorType": "DIMENSIONAL"}}), 400, "")
	expect(t, call("CreateAnomalyMonitor", map[string]any{"AnomalyMonitor": map[string]any{"MonitorName": "m", "MonitorType": "CUSTOM", "MonitorDimension": "SERVICE"}}), 400, "")
	monitor := call("CreateAnomalyMonitor", map[string]any{"AnomalyMonitor": map[string]any{"MonitorName": "services", "MonitorType": "DIMENSIONAL", "MonitorDimension": "SERVICE"},
		"ResourceTags": []any{map[string]any{"Key": "k", "Value": "v"}}})
	expect(t, monitor, 200, "")
	monitorARN := str(monitor.body["MonitorArn"])
	custom := call("CreateAnomalyMonitor", map[string]any{"AnomalyMonitor": map[string]any{"MonitorName": "custom", "MonitorType": "CUSTOM",
		"MonitorSpecification": map[string]any{"Dimensions": map[string]any{"Key": "SERVICE", "Values": []any{"Amazon S3"}}}}})
	expect(t, custom, 200, "")
	expect(t, call("UpdateAnomalyMonitor", map[string]any{"MonitorArn": monitorARN, "MonitorName": "renamed"}), 200, "")
	list := call("GetAnomalyMonitors", map[string]any{}).body["AnomalyMonitors"].([]any)
	if len(list) != 2 {
		t.Fatalf("monitors %v", list)
	}
	one := call("GetAnomalyMonitors", map[string]any{"MonitorArnList": []any{monitorARN}}).body["AnomalyMonitors"].([]any)
	if m := one[0].(map[string]any); len(one) != 1 || m["MonitorName"] != "renamed" || m["DimensionalValueCount"] != json.Number("0") || m["MonitorArn"] != monitorARN {
		t.Fatalf("monitor %v", one)
	}

	// Anomaly subscriptions.
	subscription := func(mutate func(map[string]any)) map[string]any {
		s := map[string]any{"SubscriptionName": "alerts", "Frequency": "DAILY", "MonitorArnList": []any{monitorARN},
			"Subscribers": []any{map[string]any{"Type": "EMAIL", "Address": "ops@example.invalid"}}, "ThresholdExpression": map[string]any{"Dimensions": map[string]any{"Key": "ANOMALY_TOTAL_IMPACT_ABSOLUTE", "Values": []any{"100"}}}}
		if mutate != nil {
			mutate(s)
		}
		return map[string]any{"AnomalySubscription": s}
	}
	for _, mutate := range []func(map[string]any){
		func(s map[string]any) { s["MonitorArnList"] = []any{} },
		func(s map[string]any) { s["MonitorArnList"] = []any{"monitor"} },
		func(s map[string]any) { s["Subscribers"] = []any{} },
		func(s map[string]any) { s["Subscribers"] = []any{map[string]any{"Type": "EMAIL", "Address": "nobody"}} },
		func(s map[string]any) { s["Subscribers"] = []any{map[string]any{"Type": "SNS", "Address": "topic"}} },
		func(s map[string]any) { s["Threshold"] = 10 },
	} {
		expect(t, call("CreateAnomalySubscription", subscription(mutate)), 400, "")
	}
	sub := call("CreateAnomalySubscription", subscription(func(s map[string]any) {
		s["Subscribers"] = []any{map[string]any{"Type": "SNS", "Address": "arn:aws:sns:us-east-1:000000000000:alerts"}}
	}))
	expect(t, sub, 200, "")
	subARN := str(sub.body["SubscriptionArn"])
	expect(t, call("UpdateAnomalySubscription", map[string]any{"SubscriptionArn": subARN, "Frequency": "IMMEDIATE"}), 200, "")
	expect(t, call("UpdateAnomalySubscription", map[string]any{"SubscriptionArn": subARN, "MonitorArnList": []any{}}), 400, "")
	expect(t, call("UpdateAnomalySubscription", map[string]any{"SubscriptionArn": subARN, "Threshold": 5, "ThresholdExpression": map[string]any{}}), 400, "")
	subs := call("GetAnomalySubscriptions", map[string]any{"SubscriptionArnList": []any{subARN}}).body["AnomalySubscriptions"].([]any)
	if s := subs[0].(map[string]any); len(subs) != 1 || s["Frequency"] != "IMMEDIATE" || s["AccountId"] != "000000000000" {
		t.Fatalf("subscriptions %v", subs)
	}

	// Tags.
	expect(t, call("TagResource", map[string]any{"ResourceArn": categoryARN, "ResourceTags": []any{map[string]any{"Key": "x", "Value": "y"}}}), 200, "")
	expect(t, call("UntagResource", map[string]any{"ResourceArn": categoryARN, "ResourceTagKeys": []any{"team"}}), 200, "")
	if tags := call("ListTagsForResource", map[string]any{"ResourceArn": categoryARN}).body["ResourceTags"].([]any); len(tags) != 1 || tags[0].(map[string]any)["Key"] != "x" {
		t.Fatalf("tags %v", tags)
	}
	if tags := call("ListTagsForResource", map[string]any{"ResourceArn": monitorARN}).body["ResourceTags"].([]any); len(tags) != 1 {
		t.Fatalf("tags %v", tags)
	}

	// Deletion and unbound operations.
	expect(t, call("DeleteAnomalySubscription", map[string]any{"SubscriptionArn": subARN}), 200, "")
	expect(t, call("DeleteAnomalySubscription", map[string]any{"SubscriptionArn": subARN}), 400, "ResourceNotFoundException")
	expect(t, call("DeleteAnomalyMonitor", map[string]any{"MonitorArn": monitorARN}), 200, "")
	expect(t, call("DeleteAnomalyMonitor", map[string]any{"MonitorArn": monitorARN}), 400, "ResourceNotFoundException")
	expect(t, call("DeleteCostCategoryDefinition", map[string]any{"CostCategoryArn": categoryARN}), 200, "")
	expect(t, call("DeleteCostCategoryDefinition", map[string]any{"CostCategoryArn": categoryARN}), 400, "ResourceNotFoundException")
	expect(t, call("GetCostAndUsage", map[string]any{}), 501, "UnknownOperationException")
}

func TestBillingDataExportsLifecycle(t *testing.T) {
	h := newHarness(t, bcmDataExports)
	call := func(op string, input map[string]any) reply { return h.json1("bcm-data-exports", op, input) }
	export := func(name, query string) map[string]any {
		return map[string]any{"Name": name, "Description": "d", "RefreshCadence": map[string]any{"Frequency": "SYNCHRONOUS"},
			"DataQuery": map[string]any{"QueryStatement": query},
			"DestinationConfigurations": map[string]any{"S3Destination": map[string]any{"S3Bucket": "bucket", "S3Prefix": "p", "S3Region": "us-east-1",
				"S3OutputConfigurations": map[string]any{"Compression": "GZIP", "Format": "TEXT_OR_CSV", "OutputType": "CUSTOM", "Overwrite": "OVERWRITE_REPORT"}}}}
	}
	expect(t, call("CreateExport", map[string]any{"Export": export("bad", "DROP TABLE x")}), 400, "")
	expect(t, call("CreateExport", map[string]any{"Export": export("bad name!", "SELECT 1")}), 400, "")
	created := call("CreateExport", map[string]any{"Export": export("costs", " select identity_line_item_id from COST_AND_USAGE_REPORT"),
		"ResourceTags": []any{map[string]any{"Key": "team", "Value": "a"}}})
	expect(t, created, 200, "")
	arn := str(created.body["ExportArn"])
	if !strings.HasPrefix(arn, "arn:aws:bcm-data-exports:us-east-1:000000000000:export/costs-") {
		t.Fatalf("arn %s", arn)
	}
	got := call("GetExport", map[string]any{"ExportArn": arn})
	if e := got.body["Export"].(map[string]any); e["Name"] != "costs" || e["ExportArn"] != arn || got.body["ExportStatus"] != nil {
		t.Fatalf("export %v", got.body)
	}
	replacement := export("costs2", "SELECT 2")
	delete(replacement, "Description")
	expect(t, call("UpdateExport", map[string]any{"ExportArn": arn, "Export": export("x", "UPDATE")}), 400, "")
	wrong := export("costs2", "SELECT 2")
	wrong["ExportArn"] = "arn:aws:bcm-data-exports:us-east-1:000000000000:export/other"
	expect(t, call("UpdateExport", map[string]any{"ExportArn": arn, "Export": wrong}), 400, "")
	same := export("costs2", "SELECT 2")
	same["ExportArn"] = arn
	expect(t, call("UpdateExport", map[string]any{"ExportArn": arn, "Export": same}), 200, "")
	expect(t, call("UpdateExport", map[string]any{"ExportArn": arn, "Export": replacement}), 200, "")
	if e := call("GetExport", map[string]any{"ExportArn": arn}).body["Export"].(map[string]any); e["Name"] != "costs2" || e["Description"] != nil {
		t.Fatalf("updated %v", e)
	}
	if list := call("ListExports", map[string]any{}).body["Exports"].([]any); len(list) != 1 || list[0].(map[string]any)["ExportName"] != "costs2" {
		t.Fatalf("exports %v", list)
	}
	expect(t, call("TagResource", map[string]any{"ResourceArn": arn, "ResourceTags": []any{map[string]any{"Key": "x", "Value": "y"}}}), 200, "")
	expect(t, call("UntagResource", map[string]any{"ResourceArn": arn, "ResourceTagKeys": []any{"team"}}), 200, "")
	if tags := call("ListTagsForResource", map[string]any{"ResourceArn": arn}).body["ResourceTags"].([]any); len(tags) != 1 || tags[0].(map[string]any)["Key"] != "x" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, call("DeleteExport", map[string]any{"ExportArn": arn}), 200, "")
	expect(t, call("GetExport", map[string]any{"ExportArn": arn}), 400, "ResourceNotFoundException")
	expect(t, call("ListTables", map[string]any{}), 501, "UnknownOperationException")
}

func escape(arn string) string { return strings.NewReplacer(":", "%3A", "/", "%2F").Replace(arn) }

func TestBraketSpendingLimits(t *testing.T) {
	h := newHarness(t, braket)
	device := "arn:aws:braket:us-east-1::device/qpu/ionq/Forte-1"
	body := func(mutate func(map[string]any)) map[string]any {
		b := map[string]any{"clientToken": "token-1", "deviceArn": device, "spendingLimit": "25.50",
			"timePeriod": map[string]any{"startAt": 1700000000, "endAt": 1800000000}, "tags": map[string]any{"team": "a"}}
		if mutate != nil {
			mutate(b)
		}
		return b
	}
	for i, mutate := range []func(map[string]any){
		func(b map[string]any) { b["deviceArn"] = "device" },
		func(b map[string]any) { b["spendingLimit"] = "-1" },
		func(b map[string]any) { b["spendingLimit"] = "lots" },
		func(b map[string]any) { b["timePeriod"] = map[string]any{"startAt": 5, "endAt": 4} },
	} {
		if r := h.rest("braket", "POST", "/spending-limit", body(mutate)); r.status != 400 {
			t.Fatalf("case %d: %d %v", i, r.status, r.body)
		}
	}
	created := h.rest("braket", "POST", "/spending-limit", body(nil))
	expect(t, created, 201, "")
	arn := str(created.body["spendingLimitArn"])
	if !strings.HasPrefix(arn, "arn:aws:braket:us-east-1:000000000000:spending-limit/") {
		t.Fatalf("arn %s", arn)
	}
	again := h.rest("braket", "POST", "/spending-limit", body(nil))
	if again.body["spendingLimitArn"] != arn {
		t.Fatalf("idempotent create %v", again.body)
	}
	update := func(b map[string]any) reply {
		b["clientToken"] = "token-2"
		return h.rest("braket", "PATCH", "/spending-limit/"+escape(arn)+"/update", b)
	}
	expect(t, update(map[string]any{"spendingLimit": "40"}), 200, "")
	expect(t, update(map[string]any{"timePeriod": map[string]any{"startAt": 1700000000, "endAt": 1600000000}}), 400, "ValidationException")
	limits := h.rest("braket", "POST", "/spending-limits", map[string]any{}).body["spendingLimits"].([]any)
	if l := limits[0].(map[string]any); len(limits) != 1 || l["spendingLimit"] != "40" || l["totalSpend"] != "0" || l["queuedSpend"] != "0" || l["deviceArn"] != device {
		t.Fatalf("limits %v", limits)
	}
	expect(t, h.rest("braket", "POST", "/spending-limits", map[string]any{"filters": []any{map[string]any{"name": "deviceArn", "values": []any{device}, "operator": "EQUAL"}}}), 400, "")
	esc := escape(arn)
	expect(t, h.rest("braket", "POST", "/tags/"+esc, map[string]any{"tags": map[string]any{"x": "y"}}), 200, "")
	expect(t, h.rest("braket", "DELETE", "/tags/"+esc+"?tagKeys=team", nil), 200, "")
	if tags := h.rest("braket", "GET", "/tags/"+esc, nil).body["tags"].(map[string]any); len(tags) != 1 || tags["x"] != "y" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("braket", "DELETE", "/spending-limit/"+escape(arn)+"/delete", nil), 200, "")
	expect(t, h.rest("braket", "DELETE", "/spending-limit/"+escape(arn)+"/delete", nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("braket", "POST", "/quantum-tasks", map[string]any{}), 501, "UnknownOperationException")
}

func TestArtifactAccountSettings(t *testing.T) {
	h := newHarness(t, artifact)
	got := h.rest("artifact", "GET", "/v1/account-settings/get", nil)
	expect(t, got, 200, "")
	if s := got.body["accountSettings"].(map[string]any); s["notificationSubscriptionStatus"] != "NOT_SUBSCRIBED" {
		t.Fatalf("default %v", got.body)
	}
	put := h.rest("artifact", "PUT", "/v1/account-settings/put", map[string]any{"notificationSubscriptionStatus": "SUBSCRIBED"})
	expect(t, put, 200, "")
	if h.rest("artifact", "GET", "/v1/account-settings/get", nil).body["accountSettings"].(map[string]any)["notificationSubscriptionStatus"] != "SUBSCRIBED" {
		t.Fatal("setting not stored")
	}
	expect(t, h.rest("artifact", "PUT", "/v1/account-settings/put", map[string]any{"notificationSubscriptionStatus": "MAYBE"}), 400, "ValidationException")
	expect(t, h.rest("artifact", "GET", "/v1/report/list", nil), 501, "UnknownOperationException")
}

func TestAIOpsInvestigationGroup(t *testing.T) {
	h := newHarness(t, aiopsGroup)
	role := "arn:aws:iam::000000000000:role/Investigations"
	create := func(b map[string]any) reply { return h.rest("aiops", "POST", "/investigationGroups", b) }
	expect(t, create(map[string]any{"name": "g", "roleArn": role, "chatbotNotificationChannel": map[string]any{"topic": []any{"arn:aws:chatbot::000000000000:chat-configuration/slack-channel/x"}}}), 400, "ValidationException")
	expect(t, create(map[string]any{"name": "g", "roleArn": role, "encryptionConfiguration": map[string]any{"type": "CUSTOMER_MANAGED_KMS_KEY"}}), 400, "ValidationException")
	expect(t, create(map[string]any{"name": "g", "roleArn": role, "retentionInDays": 3}), 400, "ValidationException")
	created := create(map[string]any{"name": "group", "roleArn": role, "tags": map[string]any{"team": "a"},
		"chatbotNotificationChannel": map[string]any{"arn:aws:sns:us-east-1:000000000000:alerts": []any{"arn:aws:chatbot::000000000000:chat-configuration/slack-channel/x"}}})
	expect(t, created, 201, "")
	arn := str(created.body["arn"])
	if !strings.Contains(arn, ":aiops:us-east-1:000000000000:investigation-group/") {
		t.Fatalf("arn %s", arn)
	}
	expect(t, create(map[string]any{"name": "other", "roleArn": role}), 402, "ServiceQuotaExceededException")
	got := h.rest("aiops", "GET", "/investigationGroups/"+str(created.body["arn"])[strings.LastIndex(arn, "/")+1:], nil)
	expect(t, got, 200, "")
	if got.body["retentionInDays"] != json.Number("90") || got.body["isCloudTrailEventHistoryEnabled"] != true || got.body["name"] != "group" || got.body["arn"] != arn {
		t.Fatalf("group %v", got.body)
	}
	id := arn[strings.LastIndex(arn, "/")+1:]
	expect(t, h.rest("aiops", "PATCH", "/investigationGroups/"+id, map[string]any{"isCloudTrailEventHistoryEnabled": false, "tagKeyBoundaries": []any{"app"}}), 200, "")
	expect(t, h.rest("aiops", "PATCH", "/investigationGroups/"+id, map[string]any{"retentionInDays": 10}), 400, "")
	if l := h.rest("aiops", "GET", "/investigationGroups", nil).body["investigationGroups"].([]any); len(l) != 1 || l[0].(map[string]any)["name"] != "group" {
		t.Fatalf("groups %v", l)
	}

	// Policy.
	policyPath := "/investigationGroups/" + id + "/policy"
	if p := h.rest("aiops", "GET", policyPath, nil); p.status != 200 || p.body["policy"] != nil || p.body["investigationGroupArn"] != arn {
		t.Fatalf("empty policy %d %v", p.status, p.body)
	}
	expect(t, h.rest("aiops", "POST", policyPath, map[string]any{"policy": "not json"}), 400, "ValidationException")
	expect(t, h.rest("aiops", "POST", policyPath, map[string]any{"policy": `{"Version":"2012-10-17","Statement":[]}`}), 200, "")
	if p := h.rest("aiops", "GET", policyPath, nil); p.body["policy"] != `{"Version":"2012-10-17","Statement":[]}` {
		t.Fatalf("policy %v", p.body)
	}
	expect(t, h.rest("aiops", "DELETE", policyPath, nil), 200, "")
	expect(t, h.rest("aiops", "POST", "/investigationGroups/missing/policy", map[string]any{"policy": "{}"}), 404, "ResourceNotFoundException")

	esc := strings.NewReplacer(":", "%3A", "/", "%2F").Replace(arn)
	expect(t, h.rest("aiops", "POST", "/tags/"+esc, map[string]any{"tags": map[string]any{"x": "y"}}), 200, "")
	expect(t, h.rest("aiops", "DELETE", "/tags/"+esc+"?tagKeys=team", nil), 200, "")
	if tags := h.rest("aiops", "GET", "/tags/"+esc, nil).body["tags"].(map[string]any); len(tags) != 1 {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("aiops", "POST", policyPath, map[string]any{"policy": "{}"}), 200, "")
	expect(t, h.rest("aiops", "DELETE", "/investigationGroups/"+id, nil), 200, "")
	expect(t, h.rest("aiops", "GET", "/investigationGroups/"+id, nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("aiops", "GET", policyPath, nil), 404, "ResourceNotFoundException")
}
