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
