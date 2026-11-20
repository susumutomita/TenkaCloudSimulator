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
