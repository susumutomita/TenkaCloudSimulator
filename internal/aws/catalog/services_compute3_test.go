package catalog

import (
	"fmt"
	"strings"
	"testing"
)

func escapeARN(arn string) string {
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
	expect(t, h.rest("batch", "POST", "/v1/tags/"+escapeARN(arn), map[string]any{"tags": map[string]any{"k": "v"}}), 200, "")
	expect(t, h.rest("batch", "DELETE", "/v1/tags/"+escapeARN(arn)+"?tagKeys=team", nil), 200, "")
	if tags := h.rest("batch", "GET", "/v1/tags/"+escapeARN(arn), nil).body["tags"].(map[string]any); len(tags) != 1 || tags["k"] != "v" {
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
	expect(t, h.rest("batch", "POST", "/v1/registerjobdefinition", map[string]any{"jobDefinitionName": "d", "type": "container"}), 501, "UnknownOperationException")
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
	eks := h.rest("batch", "POST", "/v1/createcomputeenvironment", map[string]any{"computeEnvironmentName": "eks", "type": "UNMANAGED", "unmanagedvCpus": 4, "eksConfiguration": map[string]any{"eksClusterArn": "arn:aws:eks:us-east-1:000000000000:cluster/c", "kubernetesNamespace": "batch"}})
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
	expect(t, h.rest("batch", "POST", "/v1/tags/"+escapeARN(ceARN), map[string]any{"tags": map[string]any{"k": "v"}}), 200, "")
	if tags := h.rest("batch", "GET", "/v1/tags/"+escapeARN(ceARN), nil).body["tags"].(map[string]any); tags["k"] != "v" {
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
	expect(t, h.rest("outposts", "POST", "/tags/"+escapeARN(arn), map[string]any{"Tags": map[string]any{"c": "d"}}), 200, "")
	expect(t, h.rest("outposts", "DELETE", "/tags/"+escapeARN(arn)+"?tagKeys=a", nil), 200, "")
	if tags := h.rest("outposts", "GET", "/tags/"+escapeARN(arn), nil).body["Tags"].(map[string]any); len(tags) != 1 || tags["c"] != "d" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("outposts", "DELETE", "/outposts/"+opID, nil), 200, "")
	expect(t, h.rest("outposts", "DELETE", "/outposts/"+opID, nil), 404, "NotFoundException")
	expect(t, h.rest("outposts", "DELETE", "/sites/"+siteID, nil), 200, "")
	expect(t, h.rest("outposts", "DELETE", "/sites/"+siteID, nil), 404, "NotFoundException")

	expect(t, h.rest("outposts", "GET", "/sites/"+siteID+"/address?AddressType=SHIPPING_ADDRESS", nil), 501, "UnknownOperationException")
	expect(t, h.rest("outposts", "POST", "/orders", map[string]any{"OutpostIdentifier": opID, "LineItems": []any{}}), 501, "UnknownOperationException")
}
