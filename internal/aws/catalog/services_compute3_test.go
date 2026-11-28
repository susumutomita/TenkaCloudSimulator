package catalog

import (
	"fmt"
	"strings"
	"testing"
)

func compute3EscapeARN(arn string) string {
	return strings.NewReplacer(":", "%3A", "/", "%2F").Replace(arn)
}

func TestBatchSchedulingPoliciesAndConsumableResources(t *testing.T) {
	h := newHarness(t, batch)
	created := h.rest("batch", "POST", "/v1/createschedulingpolicy", map[string]any{"name": "fair", "fairsharePolicy": map[string]any{"shareDecaySeconds": 3600, "computeReservation": 10}, "tags": map[string]any{"team": "a"}})
	expect(t, created, 200, "")
	arn := str(created.body["arn"])
	if arn != "arn:aws:batch:us-east-1:000000000000:scheduling-policy/fair" || str(created.body["name"]) != "fair" {
		t.Fatalf("created %v", created.body)
	}
	expect(t, h.rest("batch", "POST", "/v1/createschedulingpolicy", map[string]any{"name": "fair"}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createschedulingpolicy", map[string]any{"name": "bad name"}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createschedulingpolicy", map[string]any{"name": "x", "fairsharePolicy": map[string]any{"computeReservation": 100}}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createschedulingpolicy", map[string]any{"name": "x", "fairsharePolicy": map[string]any{"shareDecaySeconds": 604801}}), 400, "ClientException")
	described := h.rest("batch", "POST", "/v1/describeschedulingpolicies", map[string]any{"arns": []any{arn}})
	expect(t, described, 200, "")
	items := described.body["schedulingPolicies"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("described %v", described.body)
	}
	expect(t, h.rest("batch", "POST", "/v1/describeschedulingpolicies", map[string]any{"arns": []any{arn + "x"}}), 400, "ClientException")
	listed := h.rest("batch", "POST", "/v1/listschedulingpolicies", map[string]any{})
	if got := listed.body["schedulingPolicies"].([]any); len(got) != 1 || got[0].(map[string]any)["arn"] != arn {
		t.Fatalf("listed %v", listed.body)
	}
	expect(t, h.rest("batch", "POST", "/v1/updateschedulingpolicy", map[string]any{"arn": arn, "fairsharePolicy": map[string]any{"computeReservation": 20}}), 200, "")
	expect(t, h.rest("batch", "POST", "/v1/updateschedulingpolicy", map[string]any{"arn": arn, "fairsharePolicy": map[string]any{"computeReservation": 120}}), 400, "ClientException")
	if again := h.rest("batch", "POST", "/v1/describeschedulingpolicies", map[string]any{"arns": []any{arn}}); fmt.Sprint(again.body["schedulingPolicies"].([]any)[0].(map[string]any)["fairsharePolicy"].(map[string]any)["computeReservation"]) != "20" {
		t.Fatalf("updated %v", again.body)
	}

	// Tags apply by ARN.
	expect(t, h.rest("batch", "POST", "/v1/tags/"+compute3EscapeARN(arn), map[string]any{"tags": map[string]any{"k": "v"}}), 200, "")
	expect(t, h.rest("batch", "DELETE", "/v1/tags/"+compute3EscapeARN(arn)+"?tagKeys=team", nil), 200, "")
	if tags := h.rest("batch", "GET", "/v1/tags/"+compute3EscapeARN(arn), nil).body["tags"].(map[string]any); len(tags) != 1 || tags["k"] != "v" {
		t.Fatalf("tags %v", tags)
	}

	consumable := h.rest("batch", "POST", "/v1/createconsumableresource", map[string]any{"consumableResourceName": "licenses", "totalQuantity": 8, "tags": map[string]any{"a": "b"}})
	expect(t, consumable, 200, "")
	if consumable.body["consumableResourceArn"] != "arn:aws:batch:us-east-1:000000000000:consumable-resource/licenses" {
		t.Fatalf("consumable %v", consumable.body)
	}
	read := h.rest("batch", "POST", "/v1/describeconsumableresource", map[string]any{"consumableResource": "licenses"})
	expect(t, read, 200, "")
	if read.body["resourceType"] != "REPLENISHABLE" || fmt.Sprint(read.body["availableQuantity"]) != "8" || fmt.Sprint(read.body["inUseQuantity"]) != "0" || read.body["createdAt"] != nil {
		t.Fatalf("read %v", read.body)
	}
	expect(t, h.rest("batch", "POST", "/v1/createconsumableresource", map[string]any{"consumableResourceName": "licenses"}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createconsumableresource", map[string]any{"consumableResourceName": "n", "totalQuantity": -1}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createconsumableresource", map[string]any{"consumableResourceName": "n", "resourceType": "OTHER"}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createconsumableresource", map[string]any{"consumableResourceName": "bad name"}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/describeconsumableresource", map[string]any{"consumableResource": "missing"}), 400, "ClientException")
	if got := h.rest("batch", "POST", "/v1/listconsumableresources", map[string]any{}).body["consumableResources"].([]any); len(got) != 1 || got[0].(map[string]any)["consumableResourceName"] != "licenses" {
		t.Fatalf("list %v", got)
	}
	expect(t, h.rest("batch", "POST", "/v1/listconsumableresources", map[string]any{"filters": []any{map[string]any{"name": "x", "values": []any{"y"}}}}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/deleteconsumableresource", map[string]any{"consumableResource": "licenses"}), 200, "")
	expect(t, h.rest("batch", "POST", "/v1/deleteconsumableresource", map[string]any{"consumableResource": "licenses"}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/deleteschedulingpolicy", map[string]any{"arn": arn}), 200, "")

	// Unbound operations answer 501.
	expect(t, h.rest("batch", "POST", "/v1/updateconsumableresource", map[string]any{"consumableResource": "x", "operation": "ADD", "quantity": 1}), 501, "UnknownOperationException")
	expect(t, h.rest("batch", "POST", "/v1/submitjob", map[string]any{"jobName": "j", "jobQueue": "q", "jobDefinition": "d"}), 501, "UnknownOperationException")
	// Registration is now bound, but its inert container configuration is required.
	expect(t, h.rest("batch", "POST", "/v1/registerjobdefinition", map[string]any{"jobDefinitionName": "d", "type": "container"}), 400, "ClientException")
}

func TestBatchComputeEnvironmentsQueuesAndServiceEnvironments(t *testing.T) {
	h := newHarness(t, batch)
	managed := map[string]any{"computeEnvironmentName": "ce", "type": "MANAGED", "computeResources": map[string]any{"type": "EC2", "maxvCpus": 16, "subnets": []any{"subnet-0a1b2c3d"}, "instanceTypes": []any{"optimal"}}}
	created := h.rest("batch", "POST", "/v1/createcomputeenvironment", managed)
	expect(t, created, 200, "")
	ceARN := str(created.body["computeEnvironmentArn"])
	if ceARN != "arn:aws:batch:us-east-1:000000000000:compute-environment/ce" {
		t.Fatalf("created %v", created.body)
	}
	ce := h.rest("batch", "POST", "/v1/describecomputeenvironments", map[string]any{"computeEnvironments": []any{"ce"}}).body["computeEnvironments"].([]any)[0].(map[string]any)
	if ce["status"] != "CREATING" || ce["state"] != "ENABLED" || ce["containerOrchestrationType"] != "ECS" || ce["ecsClusterArn"] != nil || str(ce["uuid"]) == "" {
		t.Fatalf("compute environment %v", ce)
	}
	expect(t, h.rest("batch", "POST", "/v1/createcomputeenvironment", managed), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createcomputeenvironment", map[string]any{"computeEnvironmentName": "m2", "type": "MANAGED"}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createcomputeenvironment", map[string]any{"computeEnvironmentName": "m3", "type": "MANAGED", "unmanagedvCpus": 4, "computeResources": managed["computeResources"]}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createcomputeenvironment", map[string]any{"computeEnvironmentName": "u2", "type": "UNMANAGED", "computeResources": managed["computeResources"]}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createcomputeenvironment", map[string]any{"computeEnvironmentName": "bad name", "type": "UNMANAGED"}), 400, "ClientException")
	fargate := func(extra string, count int) map[string]any {
		resources := map[string]any{"type": "FARGATE", "maxvCpus": 4, "subnets": []any{"subnet-0a1b2c3d"}}
		if extra != "" {
			resources[extra] = "x"
		}
		if count > 0 {
			subnets := make([]any, count)
			for i := range subnets {
				subnets[i] = "subnet-0a1b2c3d"
			}
			resources["subnets"] = subnets
		}
		return map[string]any{"computeEnvironmentName": "fg", "type": "MANAGED", "computeResources": resources}
	}
	expect(t, h.rest("batch", "POST", "/v1/createcomputeenvironment", fargate("ec2KeyPair", 0)), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createcomputeenvironment", fargate("instanceRole", 0)), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createcomputeenvironment", fargate("", 17)), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createcomputeenvironment", fargate("", 16)), 200, "")
	eks := h.rest("batch", "POST", "/v1/createcomputeenvironment", map[string]any{"computeEnvironmentName": "eks", "type": "UNMANAGED", "serviceRole": "arn:aws:iam::000000000000:role/batch", "unmanagedvCpus": 4, "eksConfiguration": map[string]any{"eksClusterArn": "arn:aws:eks:us-east-1:000000000000:cluster/c", "kubernetesNamespace": "batch"}})
	expect(t, eks, 200, "")
	all := h.rest("batch", "POST", "/v1/describecomputeenvironments", map[string]any{}).body["computeEnvironments"].([]any)
	if len(all) != 3 || all[2].(map[string]any)["containerOrchestrationType"] != "EKS" {
		t.Fatalf("all %v", all)
	}
	expect(t, h.rest("batch", "POST", "/v1/describecomputeenvironments", map[string]any{"computeEnvironments": []any{"missing"}}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/updatecomputeenvironment", map[string]any{"computeEnvironment": "ce", "state": "DISABLED", "serviceRole": "arn:aws:iam::000000000000:role/batch", "context": "c", "updatePolicy": map[string]any{"terminateJobsOnUpdate": true}}), 200, "")
	expect(t, h.rest("batch", "POST", "/v1/updatecomputeenvironment", map[string]any{"computeEnvironment": "ce", "unmanagedvCpus": 2}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/updatecomputeenvironment", map[string]any{"computeEnvironment": "eks", "unmanagedvCpus": 2}), 200, "")
	expect(t, h.rest("batch", "POST", "/v1/updatecomputeenvironment", map[string]any{"computeEnvironment": "ce", "computeResources": map[string]any{"maxvCpus": 32}}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/updatecomputeenvironment", map[string]any{"computeEnvironment": "missing", "state": "DISABLED"}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/tags/"+compute3EscapeARN(ceARN), map[string]any{"tags": map[string]any{"k": "v"}}), 200, "")
	if tags := h.rest("batch", "GET", "/v1/tags/"+compute3EscapeARN(ceARN), nil).body["tags"].(map[string]any); tags["k"] != "v" {
		t.Fatalf("tags %v", tags)
	}

	policy := h.rest("batch", "POST", "/v1/createschedulingpolicy", map[string]any{"name": "fair"})
	policyARN := str(policy.body["arn"])
	order := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = map[string]any{"order": i, "computeEnvironment": ceARN}
		}
		return out
	}
	queue := h.rest("batch", "POST", "/v1/createjobqueue", map[string]any{"jobQueueName": "q", "priority": 5, "schedulingPolicyArn": policyARN, "computeEnvironmentOrder": order(1)})
	expect(t, queue, 200, "")
	if queue.body["jobQueueArn"] != "arn:aws:batch:us-east-1:000000000000:job-queue/q" {
		t.Fatalf("queue %v", queue.body)
	}
	expect(t, h.rest("batch", "POST", "/v1/createjobqueue", map[string]any{"jobQueueName": "q", "priority": 5}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createjobqueue", map[string]any{"jobQueueName": "bad name", "priority": 5}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createjobqueue", map[string]any{"jobQueueName": "q4", "priority": 5, "computeEnvironmentOrder": order(4)}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createjobqueue", map[string]any{"jobQueueName": "qn", "priority": 5, "computeEnvironmentOrder": []any{map[string]any{"order": -1, "computeEnvironment": ceARN}}}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createjobqueue", map[string]any{"jobQueueName": "qs", "priority": 5, "schedulingPolicyArn": policyARN + "x"}), 400, "ClientException")
	bare := h.rest("batch", "POST", "/v1/createjobqueue", map[string]any{"jobQueueName": "bare", "priority": 1})
	expect(t, bare, 200, "")
	described := h.rest("batch", "POST", "/v1/describejobqueues", map[string]any{"jobQueues": []any{"q"}}).body["jobQueues"].([]any)[0].(map[string]any)
	if described["status"] != "VALID" || described["state"] != "ENABLED" || len(described["computeEnvironmentOrder"].([]any)) != 1 {
		t.Fatalf("described %v", described)
	}
	if bareQueue := h.rest("batch", "POST", "/v1/describejobqueues", map[string]any{"jobQueues": []any{"bare"}}).body["jobQueues"].([]any)[0].(map[string]any); len(bareQueue["computeEnvironmentOrder"].([]any)) != 0 {
		t.Fatalf("bare %v", bareQueue)
	}
	expect(t, h.rest("batch", "POST", "/v1/describejobqueues", map[string]any{"jobQueues": []any{"missing"}}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/updatejobqueue", map[string]any{"jobQueue": "q", "state": "DISABLED", "priority": 9}), 200, "")
	expect(t, h.rest("batch", "POST", "/v1/updatejobqueue", map[string]any{"jobQueue": "q", "computeEnvironmentOrder": order(5)}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/updatejobqueue", map[string]any{"jobQueue": "q", "schedulingPolicyArn": policyARN + "x"}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/updatejobqueue", map[string]any{"jobQueue": "missing", "state": "DISABLED"}), 400, "ClientException")
	if got := h.rest("batch", "POST", "/v1/describejobqueues", map[string]any{"maxResults": 1}); len(got.body["jobQueues"].([]any)) != 1 || got.body["nextToken"] == nil {
		t.Fatalf("page %v", got.body)
	}

	service := map[string]any{"serviceEnvironmentName": "svc", "serviceEnvironmentType": "SAGEMAKER_TRAINING", "capacityLimits": []any{map[string]any{"maxCapacity": 50, "capacityUnit": "NUM_INSTANCES"}}}
	svc := h.rest("batch", "POST", "/v1/createserviceenvironment", service)
	expect(t, svc, 200, "")
	if svc.body["serviceEnvironmentArn"] != "arn:aws:batch:us-east-1:000000000000:service-environment/svc" {
		t.Fatalf("service %v", svc.body)
	}
	expect(t, h.rest("batch", "POST", "/v1/createserviceenvironment", service), 400, "ClientException")
	service["serviceEnvironmentName"] = "bad name"
	expect(t, h.rest("batch", "POST", "/v1/createserviceenvironment", service), 400, "ClientException")
	if got := h.rest("batch", "POST", "/v1/describeserviceenvironments", map[string]any{"serviceEnvironments": []any{"svc"}}).body["serviceEnvironments"].([]any)[0].(map[string]any); got["status"] != "VALID" || got["state"] != "ENABLED" {
		t.Fatalf("service %v", got)
	}
	expect(t, h.rest("batch", "POST", "/v1/describeserviceenvironments", map[string]any{"serviceEnvironments": []any{"missing"}}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/updateserviceenvironment", map[string]any{"serviceEnvironment": "svc", "state": "DISABLED", "capacityLimits": []any{map[string]any{"maxCapacity": 10, "capacityUnit": "NUM_INSTANCES"}}}), 200, "")
	expect(t, h.rest("batch", "POST", "/v1/updateserviceenvironment", map[string]any{"serviceEnvironment": "missing", "state": "DISABLED"}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/deleteserviceenvironment", map[string]any{"serviceEnvironment": "svc"}), 200, "")
	expect(t, h.rest("batch", "POST", "/v1/deleteserviceenvironment", map[string]any{"serviceEnvironment": "svc"}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/deletejobqueue", map[string]any{"jobQueue": "q"}), 200, "")
	expect(t, h.rest("batch", "POST", "/v1/deletejobqueue", map[string]any{"jobQueue": "q"}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/deletecomputeenvironment", map[string]any{"computeEnvironment": "ce"}), 200, "")
	expect(t, h.rest("batch", "POST", "/v1/deletecomputeenvironment", map[string]any{"computeEnvironment": "ce"}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/describejobs", map[string]any{"jobs": []any{"x"}}), 501, "UnknownOperationException")
}

func TestBatchUpdateHookRefusesUnmanagedCapacityOnManagedEnvironments(t *testing.T) {
	if err := batchUpdateComputeEnvironment(map[string]any{"type": "MANAGED"}, map[string]any{"state": "ENABLED"}); err != nil {
		t.Fatal(err)
	}
	if err := batchUpdateComputeEnvironment(map[string]any{"type": "MANAGED"}, map[string]any{"unmanagedvCpus": int64(1)}); err == nil {
		t.Fatal("managed environment accepted unmanagedvCpus")
	}
}

func TestOutpostsSitesAndOutpostsAreStoredRecords(t *testing.T) {
	h := newHarness(t, outposts)
	created := h.rest("outposts", "POST", "/sites", map[string]any{"Name": "tokyo lab", "Description": "lab", "Notes": "n", "Tags": map[string]any{"team": "a"},
		"RackPhysicalProperties": map[string]any{"PowerDrawKva": "POWER_10_KVA", "UplinkCount": "UPLINK_COUNT_2"}})
	expect(t, created, 200, "")
	site := created.body["Site"].(map[string]any)
	siteID := str(site["SiteId"])
	if !strings.HasPrefix(siteID, "os-") || len(siteID) != 20 || site["SiteArn"] != "arn:aws:outposts:us-east-1:000000000000:site/"+siteID ||
		site["AccountId"] != "000000000000" || site["Name"] != "tokyo lab" || site["Tags"].(map[string]any)["team"] != "a" ||
		site["RackPhysicalProperties"].(map[string]any)["PowerDrawKva"] != "POWER_10_KVA" || site["OperatingAddressCity"] != nil {
		t.Fatalf("site %v", site)
	}
	address := map[string]any{"ContactName": "Ops", "ContactPhoneNumber": "0", "AddressLine1": "1", "City": "Tokyo", "StateOrRegion": "Tokyo", "PostalCode": "1", "CountryCode": "JP"}
	for _, member := range []string{"OperatingAddress", "ShippingAddress"} {
		refused := h.rest("outposts", "POST", "/sites", map[string]any{"Name": "x", member: address})
		expect(t, refused, 400, "ValidationException")
		if !strings.Contains(refused.raw, member+" is not stored") {
			t.Fatalf("refusal %s", refused.raw)
		}
	}
	got := h.rest("outposts", "GET", "/sites/"+siteID, nil)
	expect(t, got, 200, "")
	if got.body["Site"].(map[string]any)["SiteId"] != siteID {
		t.Fatalf("get %v", got.body)
	}
	expect(t, h.rest("outposts", "GET", "/sites/os-00000000000000000", nil), 404, "NotFoundException")
	expect(t, h.rest("outposts", "GET", "/sites", nil), 501, "UnknownOperationException")
	updated := h.rest("outposts", "PATCH", "/sites/"+siteID, map[string]any{"Name": "renamed", "Notes": "later"})
	expect(t, updated, 200, "")
	if updated.body["Site"].(map[string]any)["Name"] != "renamed" || updated.body["Site"].(map[string]any)["Description"] != "lab" {
		t.Fatalf("update %v", updated.body)
	}
	expect(t, h.rest("outposts", "PATCH", "/sites/os-00000000000000000", map[string]any{"Name": "x"}), 404, "NotFoundException")

	outpost := h.rest("outposts", "POST", "/outposts", map[string]any{"Name": "op one", "SiteId": site["SiteArn"], "AvailabilityZoneId": "use1-az1", "SupportedHardwareType": "RACK", "Tags": map[string]any{"a": "b"}})
	expect(t, outpost, 200, "")
	op := outpost.body["Outpost"].(map[string]any)
	opID := str(op["OutpostId"])
	if !strings.HasPrefix(opID, "op-") || op["OutpostArn"] != "arn:aws:outposts:us-east-1:000000000000:outpost/"+opID || op["SiteId"] != siteID ||
		op["LifeCycleStatus"] != "REQUESTED" || op["OwnerId"] != "000000000000" || op["SiteArn"] != nil {
		t.Fatalf("outpost %v", op)
	}
	plain := h.rest("outposts", "POST", "/outposts", map[string]any{"Name": "op two", "SiteId": siteID, "Description": "d"})
	expect(t, plain, 200, "")
	if plain.body["Outpost"].(map[string]any)["SiteId"] != siteID {
		t.Fatalf("plain %v", plain.body)
	}
	expect(t, h.rest("outposts", "POST", "/outposts", map[string]any{"Name": "op", "SiteId": "bad"}), 400, "ValidationException")
	expect(t, h.rest("outposts", "GET", "/outposts/"+opID, nil), 200, "")
	expect(t, h.rest("outposts", "GET", "/outposts/op-00000000000000000", nil), 404, "NotFoundException")
	expect(t, h.rest("outposts", "GET", "/outposts", nil), 501, "UnknownOperationException")
	expect(t, h.rest("outposts", "PATCH", "/outposts/"+opID, map[string]any{"Name": "op uno", "Description": "x", "SupportedHardwareType": "SERVER"}), 200, "")
	expect(t, h.rest("outposts", "PATCH", "/outposts/"+opID, map[string]any{"SiteId": siteID}), 400, "ValidationException")

	arn := str(op["OutpostArn"])
	expect(t, h.rest("outposts", "POST", "/tags/"+compute3EscapeARN(arn), map[string]any{"Tags": map[string]any{"c": "d"}}), 200, "")
	expect(t, h.rest("outposts", "DELETE", "/tags/"+compute3EscapeARN(arn)+"?tagKeys=a", nil), 200, "")
	if tags := h.rest("outposts", "GET", "/tags/"+compute3EscapeARN(arn), nil).body["Tags"].(map[string]any); len(tags) != 1 || tags["c"] != "d" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("outposts", "DELETE", "/outposts/"+opID, nil), 200, "")
	expect(t, h.rest("outposts", "DELETE", "/outposts/"+opID, nil), 404, "NotFoundException")
	expect(t, h.rest("outposts", "DELETE", "/sites/"+siteID, nil), 200, "")
	expect(t, h.rest("outposts", "DELETE", "/sites/"+siteID, nil), 404, "NotFoundException")

	expect(t, h.rest("outposts", "GET", "/sites/"+siteID+"/address?AddressType=SHIPPING_ADDRESS", nil), 501, "UnknownOperationException")
	expect(t, h.rest("outposts", "POST", "/orders", map[string]any{"OutpostIdentifier": opID, "LineItems": []any{}}), 501, "UnknownOperationException")
}

func TestPCSClustersNodeGroupsAndQueues(t *testing.T) {
	h := newPCSHarness(t)
	cluster := map[string]any{"clusterName": "hpc", "scheduler": map[string]any{"type": "SLURM", "version": "24.11"}, "size": "SMALL",
		"networking": map[string]any{"subnetIds": []any{"subnet-0a1b2c3d"}}, "tags": map[string]any{"team": "a"}}
	created := h.json1("pcs", "CreateCluster", cluster)
	expect(t, created, 200, "")
	c := created.body["cluster"].(map[string]any)
	id := str(c["id"])
	if !strings.HasPrefix(id, "pcs_") || c["name"] != "hpc" || c["status"] != "CREATING" || c["arn"] != "arn:aws:pcs:us-east-1:000000000000:cluster/"+id ||
		c["endpoints"] != nil || c["createdAt"] == nil {
		t.Fatalf("cluster %v", c)
	}
	expect(t, h.json1("pcs", "CreateCluster", cluster), 400, "ConflictException")
	for _, name := range []string{"ab", "9hpc", "pcs_hpc", "has space", strings.Repeat("a", 41)} {
		bad := map[string]any{"clusterName": name, "scheduler": cluster["scheduler"], "size": "SMALL", "networking": cluster["networking"]}
		expect(t, h.json1("pcs", "CreateCluster", bad), 400, "ValidationException")
	}
	for _, key := range []string{"hpc", id} {
		got := h.json1("pcs", "GetCluster", map[string]any{"clusterIdentifier": key})
		expect(t, got, 200, "")
		if got.body["cluster"].(map[string]any)["id"] != id {
			t.Fatalf("get %v", got.body)
		}
	}
	expect(t, h.json1("pcs", "GetCluster", map[string]any{"clusterIdentifier": "missing"}), 400, "ResourceNotFoundException")
	listed := h.json1("pcs", "ListClusters", map[string]any{})
	if list, _ := listed.body["clusters"].([]any); len(list) != 1 {
		t.Fatalf("clusters %v", list)
	}

	group := map[string]any{"clusterIdentifier": "hpc", "computeNodeGroupName": "compute", "subnetIds": []any{"subnet-0a1b2c3d"},
		"customLaunchTemplate": map[string]any{"id": "lt-0123456789abcdef0", "version": "1"}, "iamInstanceProfileArn": "arn:aws:iam::000000000000:instance-profile/pcs",
		"scalingConfiguration": map[string]any{"minInstanceCount": 0, "maxInstanceCount": 4}, "instanceConfigs": []any{map[string]any{"instanceType": "c6i.large"}}, "clientToken": "token-1234"}
	made := h.json1("pcs", "CreateComputeNodeGroup", group)
	expect(t, made, 200, "")
	g := made.body["computeNodeGroup"].(map[string]any)
	gid := str(g["id"])
	if g["clusterId"] != id || g["status"] != "CREATING" || g["purchaseOption"] != "ONDEMAND" || g["arn"] != "arn:aws:pcs:us-east-1:000000000000:cluster/"+id+"/computenodegroup/"+gid {
		t.Fatalf("group %v", g)
	}
	expect(t, h.json1("pcs", "CreateComputeNodeGroup", group), 200, "")
	group["clientToken"] = "token-5678"
	expect(t, h.json1("pcs", "CreateComputeNodeGroup", group), 400, "ConflictException")
	for name, mutate := range map[string]func(map[string]any){
		"short name":  func(m map[string]any) { m["computeNodeGroupName"] = "ab" },
		"digit start": func(m map[string]any) { m["computeNodeGroupName"] = "9nodes" },
		"scaling order": func(m map[string]any) {
			m["scalingConfiguration"] = map[string]any{"minInstanceCount": 5, "maxInstanceCount": 4}
		},
		"spot options on demand": func(m map[string]any) {
			m["spotOptions"] = map[string]any{"allocationStrategy": "lowest-price"}
		},
	} {
		bad := map[string]any{}
		for k, v := range group {
			bad[k] = v
		}
		bad["computeNodeGroupName"] = "other"
		bad["clientToken"] = "token-" + name
		mutate(bad)
		if r := h.json1("pcs", "CreateComputeNodeGroup", bad); r.status != 400 || r.code() != "ValidationException" {
			t.Fatalf("%s: %d %s", name, r.status, r.raw)
		}
	}
	expect(t, h.json1("pcs", "CreateComputeNodeGroup", map[string]any{"clusterIdentifier": "missing", "computeNodeGroupName": "compute", "subnetIds": []any{"s"}, "customLaunchTemplate": group["customLaunchTemplate"],
		"iamInstanceProfileArn": group["iamInstanceProfileArn"], "scalingConfiguration": group["scalingConfiguration"], "instanceConfigs": group["instanceConfigs"]}), 400, "ResourceNotFoundException")
	for _, key := range []string{"compute", gid} {
		expect(t, h.json1("pcs", "GetComputeNodeGroup", map[string]any{"clusterIdentifier": id, "computeNodeGroupIdentifier": key}), 200, "")
	}
	expect(t, h.json1("pcs", "GetComputeNodeGroup", map[string]any{"clusterIdentifier": "hpc", "computeNodeGroupIdentifier": "absent"}), 400, "ResourceNotFoundException")
	if list := h.json1("pcs", "ListComputeNodeGroups", map[string]any{"clusterIdentifier": "hpc"}).body["computeNodeGroups"].([]any); len(list) != 1 {
		t.Fatalf("groups %v", list)
	}
	expect(t, h.json1("pcs", "UpdateComputeNodeGroup", map[string]any{"clusterIdentifier": "hpc", "computeNodeGroupIdentifier": "compute", "purchaseOption": "SPOT",
		"spotOptions": map[string]any{"allocationStrategy": "capacity-optimized"}, "scalingConfiguration": map[string]any{"minInstanceCount": 1, "maxInstanceCount": 8}, "clientToken": "token-update"}), 200, "")
	expect(t, h.json1("pcs", "UpdateComputeNodeGroup", map[string]any{"clusterIdentifier": "hpc", "computeNodeGroupIdentifier": "compute", "scalingConfiguration": map[string]any{"minInstanceCount": 9, "maxInstanceCount": 8}}), 400, "ValidationException")
	expect(t, h.json1("pcs", "UpdateComputeNodeGroup", map[string]any{"clusterIdentifier": "hpc", "computeNodeGroupIdentifier": "compute", "purchaseOption": "ONDEMAND"}), 400, "ValidationException")
	expect(t, h.json1("pcs", "UpdateComputeNodeGroup", map[string]any{"clusterIdentifier": "hpc", "computeNodeGroupIdentifier": "missing", "amiId": "ami-0123"}), 400, "ResourceNotFoundException")

	queue := h.json1("pcs", "CreateQueue", map[string]any{"clusterIdentifier": "hpc", "queueName": "main", "computeNodeGroupConfigurations": []any{map[string]any{"computeNodeGroupId": gid}}, "tags": map[string]any{"q": "1"}})
	expect(t, queue, 200, "")
	q := queue.body["queue"].(map[string]any)
	qid := str(q["id"])
	if q["clusterId"] != id || q["status"] != "CREATING" || q["arn"] != "arn:aws:pcs:us-east-1:000000000000:cluster/"+id+"/queue/"+qid {
		t.Fatalf("queue %v", q)
	}
	expect(t, h.json1("pcs", "CreateQueue", map[string]any{"clusterIdentifier": "hpc", "queueName": "main",
		"computeNodeGroupConfigurations": []any{map[string]any{"computeNodeGroupId": gid}}}), 400, "ConflictException")
	expect(t, h.json1("pcs", "CreateQueue", map[string]any{"clusterIdentifier": "hpc", "queueName": "1"}), 400, "ValidationException")
	expect(t, h.json1("pcs", "CreateQueue", map[string]any{"clusterIdentifier": "hpc", "queueName": "9queue"}), 400, "ValidationException")
	expect(t, h.json1("pcs", "CreateQueue", map[string]any{"clusterIdentifier": "hpc", "queueName": "9queue"}), 400, "ValidationException")
	expect(t, h.json1("pcs", "GetQueue", map[string]any{"clusterIdentifier": "hpc", "queueIdentifier": qid}), 200, "")
	expect(t, h.json1("pcs", "GetQueue", map[string]any{"clusterIdentifier": "hpc", "queueIdentifier": "absent"}), 400, "ResourceNotFoundException")
	if list := h.json1("pcs", "ListQueues", map[string]any{"clusterIdentifier": "hpc"}).body["queues"].([]any); len(list) != 1 {
		t.Fatalf("queues %v", list)
	}
	expect(t, h.json1("pcs", "UpdateQueue", map[string]any{"clusterIdentifier": "hpc", "queueIdentifier": "main", "computeNodeGroupConfigurations": []any{}}), 200, "")
	expect(t, h.json1("pcs", "UpdateQueue", map[string]any{"clusterIdentifier": "hpc", "queueIdentifier": "missing"}), 400, "ResourceNotFoundException")

	qARN := str(q["arn"])
	expect(t, h.json1("pcs", "TagResource", map[string]any{"resourceArn": qARN, "tags": map[string]any{"k": "v"}}), 200, "")
	expect(t, h.json1("pcs", "UntagResource", map[string]any{"resourceArn": qARN, "tagKeys": []any{"q"}}), 200, "")
	if tags := h.json1("pcs", "ListTagsForResource", map[string]any{"resourceArn": qARN}).body["tags"].(map[string]any); len(tags) != 1 || tags["k"] != "v" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.json1("pcs", "ListTagsForResource", map[string]any{"resourceArn": "arn:aws:pcs:us-east-1:000000000000:queue/pcs_missing"}), 400, "ResourceNotFoundException")

	// A cluster with children cannot be deleted.
	expect(t, h.json1("pcs", "DeleteCluster", map[string]any{"clusterIdentifier": "hpc"}), 400, "ConflictException")
	expect(t, h.json1("pcs", "DeleteQueue", map[string]any{"clusterIdentifier": "hpc", "queueIdentifier": "main", "clientToken": "token-del"}), 200, "")
	expect(t, h.json1("pcs", "DeleteQueue", map[string]any{"clusterIdentifier": "hpc", "queueIdentifier": "main"}), 400, "ResourceNotFoundException")
	expect(t, h.json1("pcs", "DeleteComputeNodeGroup", map[string]any{"clusterIdentifier": "hpc", "computeNodeGroupIdentifier": "compute"}), 200, "")
	expect(t, h.json1("pcs", "DeleteComputeNodeGroup", map[string]any{"clusterIdentifier": "hpc", "computeNodeGroupIdentifier": "compute"}), 400, "ResourceNotFoundException")
	expect(t, h.json1("pcs", "DeleteCluster", map[string]any{"clusterIdentifier": "hpc", "clientToken": "token-del2"}), 200, "")
	expect(t, h.json1("pcs", "DeleteCluster", map[string]any{"clusterIdentifier": "hpc"}), 400, "ResourceNotFoundException")

	// Unbound operations answer 501.
	expect(t, h.json1("pcs", "UpdateCluster", map[string]any{"clusterIdentifier": "hpc", "slurmConfiguration": map[string]any{"scaleDownIdleTimeInSeconds": 60}}), 501, "UnknownOperationException")
	expect(t, h.json1("pcs", "RegisterComputeNodeGroupInstance", map[string]any{"clusterIdentifier": "hpc", "bootstrapId": "x"}), 501, "UnknownOperationException")
}

func TestServerlessRepositoryApplicationsAndPolicies(t *testing.T) {
	h := newHarness(t, serverlessRepo)
	app := map[string]any{"name": "hello-app", "author": "alice-dev", "description": "greets", "labels": []any{"demo", "team:a"}, "homePageUrl": "https://example.invalid/app", "spdxLicenseId": "MIT"}
	created := h.rest("serverlessrepo", "POST", "/applications", app)
	expect(t, created, 201, "")
	id := str(created.body["applicationId"])
	if id != "arn:aws:serverlessrepo:us-east-1:000000000000:applications/hello-app" || created.body["isVerifiedAuthor"] != false || created.body["version"] != nil ||
		created.body["creationTime"] == nil || created.body["name"] != "hello-app" {
		t.Fatalf("created %v", created.body)
	}
	expect(t, h.rest("serverlessrepo", "POST", "/applications", app), 409, "ConflictException")
	for member, value := range map[string]any{"semanticVersion": "1.0.0", "templateBody": "{}", "templateUrl": "https://example.invalid/t", "sourceCodeUrl": "https://example.invalid/s",
		"licenseBody": "text", "licenseUrl": "https://example.invalid/l", "readmeBody": "text", "readmeUrl": "https://example.invalid/r"} {
		other := map[string]any{"name": "other", "author": "alice-dev", "description": "d", member: value}
		expect(t, h.rest("serverlessrepo", "POST", "/applications", other), 400, "BadRequestException")
	}
	for name, mutate := range map[string]func(map[string]any){
		"name":        func(m map[string]any) { m["name"] = "bad name" },
		"author":      func(m map[string]any) { m["author"] = "Alice" },
		"author dash": func(m map[string]any) { m["author"] = "a--b" },
		"author edge": func(m map[string]any) { m["author"] = "-ab" },
		"author long": func(m map[string]any) { m["author"] = strings.Repeat("a", 128) },
		"description": func(m map[string]any) { m["description"] = "" },
		"long text":   func(m map[string]any) { m["description"] = strings.Repeat("d", 257) },
		"labels":      func(m map[string]any) { m["labels"] = []any{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} },
		"label rule":  func(m map[string]any) { m["labels"] = []any{"bad label"} },
	} {
		bad := map[string]any{"name": "other", "author": "alice-dev", "description": "d"}
		mutate(bad)
		if r := h.rest("serverlessrepo", "POST", "/applications", bad); r.status != 400 || r.code() != "BadRequestException" {
			t.Fatalf("%s: %d %s", name, r.status, r.raw)
		}
	}
	path := "/applications/" + compute3EscapeARN(id)
	got := h.rest("serverlessrepo", "GET", path, nil)
	expect(t, got, 200, "")
	if got.body["applicationId"] != id || got.body["description"] != "greets" {
		t.Fatalf("get %v", got.body)
	}
	expect(t, h.rest("serverlessrepo", "GET", path+"?semanticVersion=1.0.0", nil), 400, "BadRequestException")
	expect(t, h.rest("serverlessrepo", "GET", "/applications/"+compute3EscapeARN(id+"x"), nil), 404, "NotFoundException")
	list := h.rest("serverlessrepo", "GET", "/applications", nil)
	expect(t, list, 200, "")
	if items := list.body["applications"].([]any); len(items) != 1 || items[0].(map[string]any)["name"] != "hello-app" {
		t.Fatalf("list %v", list.body)
	}
	updated := h.rest("serverlessrepo", "PATCH", path, map[string]any{"description": "greets everyone", "labels": []any{"demo"}})
	expect(t, updated, 200, "")
	if updated.body["description"] != "greets everyone" || updated.body["author"] != "alice-dev" {
		t.Fatalf("update %v", updated.body)
	}
	expect(t, h.rest("serverlessrepo", "PATCH", path, map[string]any{"author": "Bob"}), 400, "BadRequestException")
	expect(t, h.rest("serverlessrepo", "PATCH", path, map[string]any{"readmeBody": "text"}), 400, "BadRequestException")
	expect(t, h.rest("serverlessrepo", "PATCH", "/applications/"+compute3EscapeARN(id+"x"), map[string]any{"description": "d"}), 404, "NotFoundException")

	policy := map[string]any{"statements": []any{map[string]any{"actions": []any{"Deploy"}, "principals": []any{"123456789012", "*"}, "statementId": "share"}}}
	policyPath := path + "/policy"
	expect(t, h.rest("serverlessrepo", "GET", policyPath, nil), 404, "NotFoundException")
	put := h.rest("serverlessrepo", "PUT", policyPath, policy)
	expect(t, put, 200, "")
	if statements := put.body["statements"].([]any); len(statements) != 1 || statements[0].(map[string]any)["statementId"] != "share" {
		t.Fatalf("put %v", put.body)
	}
	policy["statements"] = []any{map[string]any{"actions": []any{"Deploy", "UnshareApplication"}, "principals": []any{"210987654321"}}}
	expect(t, h.rest("serverlessrepo", "PUT", policyPath, policy), 200, "")
	read := h.rest("serverlessrepo", "GET", policyPath, nil)
	expect(t, read, 200, "")
	if statements := read.body["statements"].([]any); len(statements) != 1 || len(statements[0].(map[string]any)["actions"].([]any)) != 2 {
		t.Fatalf("policy %v", read.body)
	}
	expect(t, h.rest("serverlessrepo", "PUT", policyPath, map[string]any{"statements": []any{map[string]any{"actions": []any{"Deploy"}, "principals": []any{"not-an-account"}}}}), 400, "BadRequestException")
	expect(t, h.rest("serverlessrepo", "PUT", "/applications/"+compute3EscapeARN("hello-app")+"/policy", policy), 400, "BadRequestException")
	expect(t, h.rest("serverlessrepo", "PUT", "/applications/"+compute3EscapeARN(id+"x")+"/policy", policy), 404, "NotFoundException")
	expect(t, h.rest("serverlessrepo", "GET", "/applications/"+compute3EscapeARN(id+"x")+"/policy", nil), 404, "NotFoundException")

	// Deleting the application deletes its policy.
	expect(t, h.rest("serverlessrepo", "DELETE", path, nil), 204, "")
	expect(t, h.rest("serverlessrepo", "DELETE", path, nil), 404, "NotFoundException")
	expect(t, h.rest("serverlessrepo", "GET", policyPath, nil), 404, "NotFoundException")

	// Unbound operations answer 501.
	expect(t, h.rest("serverlessrepo", "PUT", path+"/versions/1.0.0", map[string]any{"templateBody": "{}"}), 501, "UnknownOperationException")
	expect(t, h.rest("serverlessrepo", "GET", path+"/versions", nil), 501, "UnknownOperationException")
	expect(t, h.rest("serverlessrepo", "POST", path+"/unshare", map[string]any{"organizationId": "o-1"}), 501, "UnknownOperationException")
}

func TestBatchDocumentedCreateConfigurationConstraints(t *testing.T) {
	h := newHarness(t, batch)
	for _, role := range []map[string]any{
		{"computeEnvironmentName": "unmanaged-without-role", "type": "UNMANAGED"},
		{"computeEnvironmentName": "unmanaged-blank-role", "type": "UNMANAGED", "serviceRole": "  "},
	} {
		expect(t, h.rest("batch", "POST", "/v1/createcomputeenvironment", role), 400, "ClientException")
	}
	expect(t, h.rest("batch", "POST", "/v1/createcomputeenvironment", map[string]any{"computeEnvironmentName": "unmanaged-with-role", "type": "UNMANAGED", "serviceRole": "arn:aws:iam::000000000000:role/batch"}), 200, "")

	ce := []any{map[string]any{"order": 1, "computeEnvironment": "ce"}}
	se := []any{map[string]any{"order": 1, "serviceEnvironment": "se"}}
	expect(t, h.rest("batch", "POST", "/v1/createjobqueue", map[string]any{"jobQueueName": "mixed", "priority": 1, "computeEnvironmentOrder": ce, "serviceEnvironmentOrder": se}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createjobqueue", map[string]any{"jobQueueName": "mixed-empty", "priority": 1, "computeEnvironmentOrder": []any{}, "serviceEnvironmentOrder": se}), 400, "ClientException")
	expect(t, h.rest("batch", "POST", "/v1/createjobqueue", map[string]any{"jobQueueName": "q", "priority": 1, "computeEnvironmentOrder": ce}), 200, "")
	expect(t, h.rest("batch", "POST", "/v1/updatejobqueue", map[string]any{"jobQueue": "q", "serviceEnvironmentOrder": se, "priority": 2}), 400, "ClientException")
	get := func() map[string]any {
		r := h.rest("batch", "POST", "/v1/describejobqueues", map[string]any{"jobQueues": []any{"q"}})
		expect(t, r, 200, "")
		return r.body["jobQueues"].([]any)[0].(map[string]any)
	}
	if q := get(); len(q["computeEnvironmentOrder"].([]any)) != 1 || fmt.Sprint(q["priority"]) != "1" || q["serviceEnvironmentOrder"] != nil {
		t.Fatalf("rejection changed persisted queue: %v", q)
	}
	expect(t, h.rest("batch", "POST", "/v1/updatejobqueue", map[string]any{"jobQueue": "q", "computeEnvironmentOrder": []any{}, "serviceEnvironmentOrder": se}), 200, "")
	if q := get(); len(q["computeEnvironmentOrder"].([]any)) != 0 || len(q["serviceEnvironmentOrder"].([]any)) != 1 {
		t.Fatalf("clear+replace did not switch queue family: %v", q)
	}
	expect(t, h.rest("batch", "POST", "/v1/updatejobqueue", map[string]any{"jobQueue": "q", "priority": 3}), 200, "")
	expect(t, h.rest("batch", "POST", "/v1/updatejobqueue", map[string]any{"jobQueue": "q", "serviceEnvironmentOrder": []any{}, "computeEnvironmentOrder": ce}), 200, "")
}
