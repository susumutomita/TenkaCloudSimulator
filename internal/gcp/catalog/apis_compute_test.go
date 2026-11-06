package catalog

import (
	"strings"
	"testing"
)

const ce = "/compute/compute/v1/projects/tenkacloud-local"

func computeLink(path string) string {
	return "https://www.googleapis.com/compute/v1/projects/tenkacloud-local/" + path
}

func (h *harness) ok(method, path string, body any) map[string]any {
	h.t.Helper()
	r := h.call("owner", method, path, body)
	expect(h.t, r, 200, "")
	return r.body
}

func TestComputeVPCLifecycle(t *testing.T) {
	h := newHarness(t)
	expect(t, h.call("owner", "POST", ce+"/global/networks", map[string]any{"name": "auto"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", ce+"/global/networks", map[string]any{"name": "auto", "autoCreateSubnetworks": true}), 400, "INVALID_ARGUMENT")
	for name, body := range map[string]map[string]any{
		"legacy range": {"IPv4Range": "10.0.0.0/8"}, "ula": {"enableUlaInternalIpv6": true}, "profile": {"networkProfile": "x"},
		"mtu": {"mtu": 9000}, "params": {"params": map[string]any{}}, "csek": {"description": "x", "routingConfig": map[string]any{"rawKey": "k"}},
	} {
		body["name"], body["autoCreateSubnetworks"] = "bad", false
		if r := h.call("owner", "POST", ce+"/global/networks", body); r.status != 400 {
			t.Fatalf("network %s accepted: %s", name, r.raw)
		}
	}
	op := h.ok("POST", ce+"/global/networks", map[string]any{"name": "vpc", "autoCreateSubnetworks": false, "description": "Main"})
	if op["kind"] != "compute#operation" || op["operationType"] != "insert" || op["status"] != "DONE" || op["targetLink"] != computeLink("global/networks/vpc") {
		t.Fatalf("network operation %v", op)
	}
	h.ok("GET", ce+"/global/operations/"+op["name"].(string), nil)
	h.ok("POST", ce+"/global/operations/"+op["name"].(string)+"/wait", nil)
	if ops := h.ok("GET", ce+"/global/operations", nil); ops["kind"] != "compute#operationList" || len(ops["items"].([]any)) != 1 {
		t.Fatalf("global operations %v", ops)
	}
	network := h.ok("GET", ce+"/global/networks/vpc", nil)
	if network["kind"] != "compute#network" || network["mtu"] != float64(1460) || network["routingConfig"].(map[string]any)["routingMode"] != "REGIONAL" || network["selfLink"] != computeLink("global/networks/vpc") {
		t.Fatalf("network %v", network)
	}
	h.ok("PATCH", ce+"/global/networks/vpc", map[string]any{"name": "vpc", "routingConfig": map[string]any{"routingMode": "GLOBAL"}})
	expect(t, h.call("owner", "PATCH", ce+"/global/networks/vpc", map[string]any{"mtu": 1500}), 400, "INVALID_ARGUMENT")
	if list := h.ok("GET", ce+"/global/networks", nil); list["kind"] != "compute#networkList" || len(list["items"].([]any)) != 1 {
		t.Fatalf("networks %v", list)
	}

	// Subnetworks reference the network, refuse IPv6 and overlapping ranges.
	subnets := ce + "/regions/us-central1/subnetworks"
	subnet := map[string]any{"name": "app", "network": "global/networks/vpc", "ipCidrRange": "10.0.0.0/24",
		"secondaryIpRanges": []any{map[string]any{"rangeName": "pods", "ipCidrRange": "10.4.0.0/16"}}}
	for name, change := range map[string]map[string]any{
		"no network": {"network": nil}, "no range": {"ipCidrRange": nil}, "missing network": {"network": "global/networks/none"},
		"host bits": {"ipCidrRange": "10.0.0.1/24"}, "too small": {"ipCidrRange": "10.0.0.0/30"}, "ipv6": {"ipCidrRange": "fd00::/64"},
		"stack": {"stackType": "IPV4_IPV6"}, "byoip": {"ipCollection": "x"}, "self overlap": {"secondaryIpRanges": []any{map[string]any{"rangeName": "a", "ipCidrRange": "10.0.0.0/16"}}},
		"no range name": {"secondaryIpRanges": []any{map[string]any{"ipCidrRange": "10.9.0.0/16"}}},
		"reserved":      {"secondaryIpRanges": []any{map[string]any{"rangeName": "a", "ipCidrRange": "10.9.0.0/16", "reservedInternalRange": "x"}}},
		"bad secondary": {"secondaryIpRanges": []any{map[string]any{"rangeName": "a", "ipCidrRange": "nope"}}},
	} {
		body := map[string]any{}
		for k, v := range subnet {
			body[k] = v
		}
		for k, v := range change {
			if v == nil {
				delete(body, k)
			} else {
				body[k] = v
			}
		}
		if r := h.call("owner", "POST", subnets, body); r.status < 400 {
			t.Fatalf("subnetwork %s accepted: %s", name, r.raw)
		}
	}
	created := h.ok("POST", subnets+"?requestId=6d5c84d6-7a8e-4d1d-9c33-2f0f6a5b9e01", subnet)
	if created["region"] != computeLink("regions/us-central1") || created["clientOperationId"] != "6d5c84d6-7a8e-4d1d-9c33-2f0f6a5b9e01" {
		t.Fatalf("subnetwork operation %v", created)
	}
	h.ok("GET", ce+"/regions/us-central1/operations/"+created["name"].(string), nil)
	h.ok("POST", ce+"/regions/us-central1/operations/"+created["name"].(string)+"/wait", nil)
	h.ok("GET", ce+"/regions/us-central1/operations", nil)
	got := h.ok("GET", subnets+"/app", nil)
	if got["network"] != computeLink("global/networks/vpc") || got["state"] != "READY" || got["purpose"] != "PRIVATE" || got["fingerprint"] == nil {
		t.Fatalf("subnetwork %v", got)
	}
	expect(t, h.call("owner", "GET", subnets+"/app?views=WITH_UTILIZATION", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", subnets+"?views=WITH_UTILIZATION", nil), 400, "INVALID_ARGUMENT")
	overlap := map[string]any{"name": "web", "network": "global/networks/vpc", "ipCidrRange": "10.4.8.0/24"}
	expect(t, h.call("owner", "POST", ce+"/regions/asia-northeast1/subnetworks", overlap), 400, "INVALID_ARGUMENT")
	overlap["ipCidrRange"] = "10.1.0.0/24"
	h.ok("POST", ce+"/regions/asia-northeast1/subnetworks", overlap)
	if items := h.ok("GET", subnets, nil)["items"].([]any); len(items) != 1 {
		t.Fatalf("regional list %v", items)
	}

	// Patch needs the current fingerprint; expand only widens; Private Google Access toggles.
	expect(t, h.call("owner", "PATCH", subnets+"/app", map[string]any{"privateIpGoogleAccess": true}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PATCH", subnets+"/app", map[string]any{"fingerprint": "AAAAAAAAAAA=", "privateIpGoogleAccess": true}), 409, "ABORTED")
	expect(t, h.call("owner", "PATCH", subnets+"/app?drainTimeoutSeconds=10", map[string]any{"fingerprint": got["fingerprint"]}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PATCH", subnets+"/app", map[string]any{"fingerprint": got["fingerprint"], "secondaryIpRanges": []any{map[string]any{"rangeName": "x", "ipCidrRange": "10.0.0.0/28"}}}), 400, "INVALID_ARGUMENT")
	h.ok("PATCH", subnets+"/app", map[string]any{"fingerprint": got["fingerprint"], "privateIpGoogleAccess": true, "ipCidrRange": "10.0.0.0/24"})
	expect(t, h.call("owner", "POST", subnets+"/app/expandIpCidrRange", map[string]any{"ipCidrRange": "10.0.0.0/25"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", subnets+"/app/expandIpCidrRange", map[string]any{"ipCidrRange": "10.0.0.0/15"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", subnets+"/app/expandIpCidrRange", map[string]any{"ipCidrRange": "10.0.0.0/22"}), 200, "")
	h.ok("POST", subnets+"/app/setPrivateIpGoogleAccess", map[string]any{"privateIpGoogleAccess": false})
	got = h.ok("GET", subnets+"/app", nil)
	if got["ipCidrRange"] != "10.0.0.0/22" || got["privateIpGoogleAccess"] != false {
		t.Fatalf("custom methods %v", got)
	}

	// Firewall rules validate rule shape and keep the network in use.
	firewalls := ce + "/global/firewalls"
	rule := map[string]any{"name": "allow-ssh", "network": "global/networks/vpc", "allowed": []any{map[string]any{"IPProtocol": "tcp", "ports": []any{"22"}}}, "sourceRanges": []any{"35.235.240.0/20"}, "targetTags": []any{"ssh"}}
	for name, change := range map[string]map[string]any{
		"no network": {"network": nil}, "both": {"denied": []any{map[string]any{"IPProtocol": "all"}}}, "neither": {"allowed": nil},
		"protocol": {"allowed": []any{map[string]any{"IPProtocol": "bogus"}}}, "icmp ports": {"allowed": []any{map[string]any{"IPProtocol": "icmp", "ports": []any{"1"}}}},
		"priority": {"priority": 70000}, "range": {"sourceRanges": []any{"300.0.0.0/8"}}, "no source": {"sourceRanges": nil},
		"mixed": {"targetServiceAccounts": []any{"a@b"}}, "egress tags": {"direction": "EGRESS", "sourceTags": []any{"a"}},
	} {
		body := map[string]any{}
		for k, v := range rule {
			body[k] = v
		}
		for k, v := range change {
			if v == nil {
				delete(body, k)
			} else {
				body[k] = v
			}
		}
		if r := h.call("owner", "POST", firewalls, body); r.status != 400 {
			t.Fatalf("firewall %s accepted: %s", name, r.raw)
		}
	}
	h.ok("POST", firewalls, rule)
	h.ok("POST", firewalls, map[string]any{"name": "deny-egress", "network": computeLink("global/networks/vpc"), "direction": "EGRESS", "denied": []any{map[string]any{"IPProtocol": "47"}}, "destinationRanges": []any{"0.0.0.0/0"}, "priority": 65000})
	fw := h.ok("GET", firewalls+"/allow-ssh", nil)
	if fw["direction"] != "INGRESS" || fw["priority"] != float64(1000) || fw["disabled"] != false || fw["network"] != computeLink("global/networks/vpc") {
		t.Fatalf("firewall %v", fw)
	}
	h.ok("PATCH", firewalls+"/allow-ssh", map[string]any{"sourceRanges": []any{"10.0.0.0/8"}, "disabled": true})
	expect(t, h.call("owner", "PATCH", firewalls+"/allow-ssh", map[string]any{"denied": []any{map[string]any{"IPProtocol": "all"}}}), 400, "INVALID_ARGUMENT")
	h.ok("PATCH", firewalls+"/allow-ssh", map[string]any{"allowed": []any{}, "denied": []any{map[string]any{"IPProtocol": "all"}}})
	if items := h.ok("GET", firewalls, nil)["items"].([]any); len(items) != 2 {
		t.Fatalf("firewalls %v", items)
	}

	// Static routes need one supported next hop.
	routes := ce + "/global/routes"
	for name, body := range map[string]map[string]any{
		"no hop":   {"destRange": "0.0.0.0/0", "priority": 1000, "network": "global/networks/vpc"},
		"two hops": {"destRange": "0.0.0.0/0", "priority": 1000, "network": "global/networks/vpc", "nextHopIp": "10.0.0.2", "nextHopGateway": "global/gateways/default-internet-gateway"},
		"gateway":  {"destRange": "0.0.0.0/0", "priority": 1000, "network": "global/networks/vpc", "nextHopGateway": "global/gateways/other"},
		"ip":       {"destRange": "0.0.0.0/0", "priority": 1000, "network": "global/networks/vpc", "nextHopIp": "ten"},
		"instance": {"destRange": "0.0.0.0/0", "priority": 1000, "network": "global/networks/vpc", "nextHopInstance": "zones/us-central1-a/instances/x"},
		"dest":     {"destRange": "nope", "priority": 1000, "network": "global/networks/vpc", "nextHopIp": "10.0.0.2"},
		"priority": {"destRange": "0.0.0.0/0", "priority": -1, "network": "global/networks/vpc", "nextHopIp": "10.0.0.2"},
		"network":  {"destRange": "0.0.0.0/0", "priority": 1000, "nextHopIp": "10.0.0.2"},
	} {
		body["name"] = "bad"
		if r := h.call("owner", "POST", routes, body); r.status != 400 {
			t.Fatalf("route %s accepted: %s", name, r.raw)
		}
	}
	h.ok("POST", routes, map[string]any{"name": "egress", "destRange": "0.0.0.0/0", "priority": 900, "network": "global/networks/vpc", "nextHopGateway": "projects/tenkacloud-local/global/gateways/default-internet-gateway", "tags": []any{"nat"}})
	if r := h.ok("GET", routes+"/egress", nil); r["routeType"] != "STATIC" || r["kind"] != "compute#route" {
		t.Fatalf("route %v", r)
	}
	h.ok("GET", routes, nil)

	// The network stays in use until every dependent is gone.
	expect(t, h.call("owner", "DELETE", ce+"/global/networks/vpc", nil), 400, "FAILED_PRECONDITION")
	for _, path := range []string{routes + "/egress", firewalls + "/allow-ssh", firewalls + "/deny-egress", subnets + "/app", ce + "/regions/asia-northeast1/subnetworks/web"} {
		if op := h.ok("DELETE", path, nil); op["operationType"] != "delete" {
			t.Fatalf("delete %v", op)
		}
	}
	h.ok("DELETE", ce+"/global/networks/vpc", nil)
	expect(t, h.call("owner", "GET", ce+"/global/networks/vpc", nil), 404, "NOT_FOUND")
	expect(t, h.call("owner", "POST", ce+"/global/networks/vpc/switchToCustomMode", nil), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "GET", ce+"/global/networks/vpc/getEffectiveFirewalls", nil), 501, "UNIMPLEMENTED")
}

func TestComputeDisksSnapshotsAndTemplates(t *testing.T) {
	h := newHarness(t)
	disks := ce + "/zones/us-central1-a/disks"
	for name, body := range map[string]map[string]any{
		"csek":     {"diskEncryptionKey": map[string]any{"rawKey": "a2V5"}},
		"rsa":      {"sourceImageEncryptionKey": map[string]any{"rsaEncryptedKey": "a2V5"}},
		"import":   {"sourceStorageObject": "gs://b/o"},
		"pool":     {"storagePool": "x"},
		"type":     {"type": "zones/us-central1-a/diskTypes/floppy"},
		"type url": {"type": "pd-ssd"},
		"sources":  {"sourceImage": "projects/debian-cloud/global/images/family/debian-12", "sourceDisk": "zones/us-central1-a/disks/x", "sizeGb": "10"},
		"unsized":  {"sourceImage": "projects/debian-cloud/global/images/family/debian-12"},
		"zero":     {"sizeGb": "0"},
		"snapshot": {"sourceSnapshot": "global/snapshots/none", "sizeGb": "10"},
	} {
		body["name"] = "bad"
		if r := h.call("owner", "POST", disks, body); r.status < 400 {
			t.Fatalf("disk %s accepted: %s", name, r.raw)
		}
	}
	expect(t, h.call("owner", "POST", disks+"?sourceImage=debian", map[string]any{"name": "img"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", ce+"/zones/us-central1-d/disks", map[string]any{"name": "d"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", ce+"/zones/us-east1-b/disks", map[string]any{"name": "d"}), 400, "INVALID_ARGUMENT")
	op := h.ok("POST", disks, map[string]any{"name": "blank", "labels": map[string]any{"team": "a"}})
	if op["zone"] != computeLink("zones/us-central1-a") || op["targetLink"] != computeLink("zones/us-central1-a/disks/blank") {
		t.Fatalf("disk operation %v", op)
	}
	h.ok("GET", ce+"/zones/us-central1-a/operations/"+op["name"].(string), nil)
	h.ok("POST", ce+"/zones/us-central1-a/operations/"+op["name"].(string)+"/wait", nil)
	blank := h.ok("GET", disks+"/blank", nil)
	if blank["sizeGb"] != "500" || blank["status"] != "READY" || blank["zone"] != computeLink("zones/us-central1-a") || blank["kind"] != "compute#disk" || blank["physicalBlockSizeBytes"] != "4096" {
		t.Fatalf("disk %v", blank)
	}
	h.ok("POST", disks, map[string]any{"name": "boot", "sizeGb": "20", "type": "zones/us-central1-a/diskTypes/pd-balanced", "sourceImage": "projects/debian-cloud/global/images/family/debian-12",
		"diskEncryptionKey": map[string]any{"kmsKeyName": "projects/tenkacloud-local/locations/us-central1/keyRings/r/cryptoKeys/k"}})

	// setLabels needs the current fingerprint; resize only grows.
	expect(t, h.call("owner", "POST", disks+"/blank/setLabels", map[string]any{"labels": map[string]any{"a": "b"}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", disks+"/blank/setLabels", map[string]any{"labels": map[string]any{"a": "b"}, "labelFingerprint": "AAAAAAAAAAA="}), 409, "ABORTED")
	labeled := h.ok("POST", disks+"/blank/setLabels", map[string]any{"labels": map[string]any{"team": "b"}, "labelFingerprint": blank["labelFingerprint"]})
	if labeled["operationType"] != "setLabels" {
		t.Fatalf("setLabels operation %v", labeled)
	}
	expect(t, h.call("owner", "POST", disks+"/blank/resize", map[string]any{}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", disks+"/blank/resize", map[string]any{"sizeGb": "100"}), 400, "INVALID_ARGUMENT")
	h.ok("POST", disks+"/blank/resize", map[string]any{"sizeGb": "1000"})
	if d := h.ok("GET", disks+"/blank", nil); d["sizeGb"] != "1000" || d["labels"].(map[string]any)["team"] != "b" || d["labelFingerprint"] == blank["labelFingerprint"] {
		t.Fatalf("disk after custom methods %v", d)
	}
	if items := h.ok("GET", disks, nil)["items"].([]any); len(items) != 2 {
		t.Fatalf("disks %v", items)
	}
	if ops := h.ok("GET", ce+"/zones/us-central1-a/operations?maxResults=1", nil); ops["nextPageToken"] == nil {
		t.Fatalf("zone operations page %v", ops)
	}

	// Snapshots outlive their source disk.
	snapshots := ce + "/global/snapshots"
	for name, body := range map[string]map[string]any{
		"no source": {}, "instant": {"sourceDisk": "zones/us-central1-a/disks/blank", "sourceInstantSnapshot": "x"},
		"flush": {"sourceDisk": "zones/us-central1-a/disks/blank", "guestFlush": true}, "missing": {"sourceDisk": "zones/us-central1-a/disks/none"},
		"csek": {"sourceDisk": "zones/us-central1-a/disks/blank", "snapshotEncryptionKey": map[string]any{"rawKey": "a2V5"}},
	} {
		body["name"] = "bad"
		if r := h.call("owner", "POST", snapshots, body); r.status < 400 {
			t.Fatalf("snapshot %s accepted: %s", name, r.raw)
		}
	}
	h.ok("POST", snapshots, map[string]any{"name": "nightly", "sourceDisk": "projects/tenkacloud-local/zones/us-central1-a/disks/blank", "storageLocations": []any{"us"}})
	snap := h.ok("GET", snapshots+"/nightly", nil)
	if snap["status"] != "READY" || snap["storageBytes"] != "0" || snap["sourceDisk"] != computeLink("zones/us-central1-a/disks/blank") {
		t.Fatalf("snapshot %v", snap)
	}
	h.ok("POST", snapshots+"/nightly/setLabels", map[string]any{"labels": map[string]any{"k": "v"}, "labelFingerprint": snap["labelFingerprint"]})
	expect(t, h.call("owner", "POST", snapshots+"/nightly/setLabels", map[string]any{}), 400, "INVALID_ARGUMENT")
	h.ok("GET", snapshots, nil)
	h.ok("POST", disks, map[string]any{"name": "restored", "sourceSnapshot": "global/snapshots/nightly", "sizeGb": "1000"})
	h.ok("DELETE", disks+"/blank", nil)
	h.ok("DELETE", snapshots+"/nightly", nil)
	if d := h.ok("GET", disks+"/restored", nil); d["sourceSnapshot"] != computeLink("global/snapshots/nightly") {
		t.Fatalf("restored disk %v", d)
	}

	// Instance templates store configuration and keep networks in use.
	h.ok("POST", ce+"/global/networks", map[string]any{"name": "vpc", "autoCreateSubnetworks": false})
	h.ok("POST", ce+"/regions/us-central1/subnetworks", map[string]any{"name": "app", "network": "global/networks/vpc", "ipCidrRange": "10.0.0.0/24"})
	templates := ce + "/global/instanceTemplates"
	nic := map[string]any{"subnetwork": "regions/us-central1/subnetworks/app"}
	for name, body := range map[string]map[string]any{
		"no properties": {}, "no machine": {"properties": map[string]any{}},
		"source":     {"sourceInstance": "zones/us-central1-a/instances/x", "properties": map[string]any{"machineType": "e2-small"}},
		"tags":       {"properties": map[string]any{"machineType": "e2-small", "resourceManagerTags": map[string]any{"a": "b"}}},
		"no network": {"properties": map[string]any{"machineType": "e2-small", "networkInterfaces": []any{map[string]any{}}}},
		"attachment": {"properties": map[string]any{"machineType": "e2-small", "networkInterfaces": []any{map[string]any{"network": "global/networks/vpc", "networkAttachment": "x"}}}},
		"csek":       {"properties": map[string]any{"machineType": "e2-small", "disks": []any{map[string]any{"diskEncryptionKey": map[string]any{"rawKey": "a2V5"}}}}},
		"dangling":   {"properties": map[string]any{"machineType": "e2-small", "networkInterfaces": []any{map[string]any{"network": "global/networks/none"}}}},
	} {
		body["name"] = "bad"
		if r := h.call("owner", "POST", templates, body); r.status < 400 {
			t.Fatalf("template %s accepted: %s", name, r.raw)
		}
	}
	h.ok("POST", templates, map[string]any{"name": "web", "properties": map[string]any{"machineType": "e2-small", "networkInterfaces": []any{nic},
		"disks":    []any{map[string]any{"boot": true, "autoDelete": true, "initializeParams": map[string]any{"sourceImage": "projects/debian-cloud/global/images/family/debian-12", "diskSizeGb": "10"}}},
		"metadata": map[string]any{"items": []any{map[string]any{"key": "startup-script", "value": "echo hi"}}}, "tags": map[string]any{"items": []any{"web"}}}})
	template := h.ok("GET", templates+"/web", nil)
	if template["properties"].(map[string]any)["networkInterfaces"].([]any)[0].(map[string]any)["subnetwork"] != computeLink("regions/us-central1/subnetworks/app") {
		t.Fatalf("template %v", template)
	}
	h.ok("GET", templates, nil)
	expect(t, h.call("owner", "DELETE", ce+"/regions/us-central1/subnetworks/app", nil), 400, "FAILED_PRECONDITION")
	h.ok("DELETE", templates+"/web", nil)
	h.ok("DELETE", ce+"/regions/us-central1/subnetworks/app", nil)
	h.ok("DELETE", ce+"/global/networks/vpc", nil)
	expect(t, h.call("owner", "POST", ce+"/zones/us-central1-a/instances", map[string]any{"name": "vm"}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "POST", disks+"/restored/createSnapshot", map[string]any{"name": "s"}), 501, "UNIMPLEMENTED")
}

func TestComputeAuthorityAndPermissions(t *testing.T) {
	h := newHarness(t)
	expect(t, h.call("intruder", "GET", ce+"/global/networks", nil), 403, "PERMISSION_DENIED")
	expect(t, h.call("owner", "GET", "/compute/compute/v1/projects/other/global/networks", nil), 403, "PERMISSION_DENIED")
	for path, want := range map[string]string{
		ce + "/global/networks":                                     "compute.networks.list",
		ce + "/zones/us-central1-a/operations/operation-x":          "compute.zoneOperations.get",
		ce + "/regions/us-central1/operations":                      "compute.regionOperations.list",
		ce + "/regions/us-central1/subnetworks/x":                   "compute.subnetworks.get",
		ce + "/zones/us-central1-a/disks/x/setLabels":               "compute.disks.setLabels",
		ce + "/regions/us-central1/subnetworks/x/expandIpCidrRange": "compute.subnetworks.expandIpCidrRange",
	} {
		method := "GET"
		if strings.HasSuffix(path, "Labels") || strings.HasSuffix(path, "Range") {
			method = "POST"
		}
		if got := h.callHeader(method, path); got != want {
			t.Fatalf("%s permission %q, want %q", path, got, want)
		}
	}
	h.ok("GET", "/compute/compute/v1/projects/100000000001/global/networks", nil)
	expect(t, h.call("owner", "GET", ce+"/regions/europe-west9/subnetworks", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", ce+"/aggregated/disks", nil), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "GET", ce+"/global/networks?filter=name%3Dx", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", ce+"/global/operations/operation-missing", nil), 404, "NOT_FOUND")
}
