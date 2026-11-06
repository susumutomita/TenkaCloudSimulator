package catalog

import (
	"fmt"
	"strings"
	"testing"
)

const (
	netopsProject = "projects/tenkacloud-local"
	netopsNetwork = "projects/tenkacloud-local/global/networks/default"
	netopsGlobal  = netopsProject + "/locations/global"
	netopsRegion  = netopsProject + "/locations/us-central1"
	netopsZone    = netopsProject + "/locations/us-central1-a"
)

// netopsCase is one resource created, read, listed, patched and deleted
// through its official paths.
type netopsCase struct {
	api, parent, collection, query, id string
	body                               map[string]any
	patch                              string
	value                              any
}

func netopsCases() []netopsCase {
	sd, ncc, nm, ids, ns, nsv, lg, mon := "/servicedirectory/v1/", "/networkconnectivity/v1/", "/networkmanagement/v1/", "/ids/v1/", "/networksecurity/v1/", "/networkservices/v1/", "/logging/v2/", "/monitoring/v3/"
	m := func(kv ...any) map[string]any {
		out := map[string]any{}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i].(string)] = kv[i+1]
		}
		return out
	}
	return []netopsCase{
		{sd, netopsRegion, "namespaces", "namespaceId=ns", "ns", m("labels", m("team", "a")), "labels", m("team", "b")},
		{sd, netopsRegion + "/namespaces/ns", "services", "serviceId=svc", "svc", m("annotations", m("k", "v")), "annotations", m("k", "w")},
		{sd, netopsRegion + "/namespaces/ns/services/svc", "endpoints", "endpointId=ep", "ep", m("address", "10.0.0.1", "port", 443, "network", "projects/100000000001/locations/global/networks/default"), "port", 8443},
		{ncc, netopsGlobal, "hubs", "hubId=hub", "hub", m("description", "Hub"), "description", "Main hub"},
		{ncc, netopsRegion, "spokes", "spokeId=gw", "gw", m("hub", netopsGlobal+"/hubs/hub", "gateway", m("capacity", "CAPACITY_1_GBPS", "ipRangeReservations", []any{m("ipRange", "10.1.0.0/23")})), "description", "Gateway spoke"},
		{ncc, netopsRegion + "/spokes/gw", "gatewayAdvertisedRoutes", "gatewayAdvertisedRouteId=route", "route", m("ipRange", "10.2.0.0/24", "recipient", "ADVERTISE_TO_HUB", "priority", 100), "priority", 200},
		{nm, netopsGlobal, "connectivityTests", "testId=web-1", "web-1", m("source", m("ipAddress", "10.0.0.1"), "destination", m("ipAddress", "10.0.0.2", "port", 443), "protocol", "TCP"), "description", "Web"},
		{ids, netopsZone, "endpoints", "endpointId=ids", "ids", m("network", netopsNetwork, "severity", "HIGH"), "trafficLogs", true},
		{ns, netopsGlobal, "securityProfiles", "securityProfileId=sp", "sp", m("type", "THREAT_PREVENTION", "threatPreventionProfile", m("severityOverrides", []any{m("severity", "HIGH", "action", "DENY")})), "description", "Strict"},
		{ns, netopsGlobal, "securityProfileGroups", "securityProfileGroupId=spg", "spg", m("threatPreventionProfile", netopsGlobal+"/securityProfiles/sp"), "description", "Group"},
		{ns, netopsZone, "firewallEndpoints", "firewallEndpointId=fe", "fe", m("billingProjectId", "tenkacloud-local"), "description", "Endpoint"},
		{ns, netopsZone, "firewallEndpointAssociations", "firewallEndpointAssociationId=fea", "fea", m("firewallEndpoint", netopsZone+"/firewallEndpoints/fe", "network", netopsNetwork), "disabled", true},
		{ns, netopsGlobal, "mirroringDeploymentGroups", "mirroringDeploymentGroupId=mdg", "mdg", m("network", netopsNetwork), "description", "Collectors"},
		{ns, netopsZone, "mirroringDeployments", "mirroringDeploymentId=md", "md", m("forwardingRule", netopsProject+"/regions/us-central1/forwardingRules/fr", "mirroringDeploymentGroup", netopsGlobal+"/mirroringDeploymentGroups/mdg"), "description", "Zone A"},
		{ns, netopsGlobal, "mirroringEndpointGroups", "mirroringEndpointGroupId=meg", "meg", m("mirroringDeploymentGroup", netopsGlobal+"/mirroringDeploymentGroups/mdg"), "description", "Consumers"},
		{ns, netopsGlobal, "mirroringEndpointGroupAssociations", "mirroringEndpointGroupAssociationId=mega", "mega", m("mirroringEndpointGroup", netopsGlobal+"/mirroringEndpointGroups/meg", "network", netopsNetwork), "labels", m("a", "b")},
		{ns, netopsGlobal, "interceptDeploymentGroups", "interceptDeploymentGroupId=idg", "idg", m("network", netopsNetwork), "description", "Appliances"},
		{ns, netopsZone, "interceptDeployments", "interceptDeploymentId=idp", "idp", m("forwardingRule", netopsProject+"/regions/us-central1/forwardingRules/fr", "interceptDeploymentGroup", netopsGlobal+"/interceptDeploymentGroups/idg"), "description", "Zone A"},
		{ns, netopsGlobal, "interceptEndpointGroups", "interceptEndpointGroupId=ieg", "ieg", m("interceptDeploymentGroup", netopsGlobal+"/interceptDeploymentGroups/idg"), "description", "Consumers"},
		{ns, netopsGlobal, "interceptEndpointGroupAssociations", "interceptEndpointGroupAssociationId=iega", "iega", m("interceptEndpointGroup", netopsGlobal+"/interceptEndpointGroups/ieg", "network", netopsNetwork), "labels", m("a", "b")},
		{ns, netopsRegion, "gatewaySecurityPolicies", "gatewaySecurityPolicyId=gsp", "gsp", m("description", "Proxy policy"), "description", "Policy"},
		{ns, netopsRegion + "/gatewaySecurityPolicies/gsp", "rules", "gatewaySecurityPolicyRuleId=allow-docs", "allow-docs", m("basicProfile", "ALLOW", "enabled", true, "priority", 10, "sessionMatcher", "host() == 'docs.example.com'"), "priority", 20},
		{ns, netopsRegion, "urlLists", "urlListId=allowed", "allowed", m("values", []any{"example.com"}), "values", []any{"example.org"}},
		{ns, netopsRegion, "tlsInspectionPolicies", "tlsInspectionPolicyId=tls", "tls", m("caPool", netopsRegion+"/caPools/pool"), "description", "Inspect"},
		{ns, netopsRegion, "addressGroups", "addressGroupId=ag", "ag", m("type", "IPV4", "capacity", 10, "items", []any{"10.0.0.0/24"}), "items", []any{"10.0.1.0/24"}},
		{nsv, netopsRegion, "gateways", "gatewayId=swp", "swp", m("type", "SECURE_WEB_GATEWAY", "ports", []any{443}, "gatewaySecurityPolicy", netopsRegion+"/gatewaySecurityPolicies/gsp", "network", netopsNetwork), "description", "Proxy"},
		{lg, netopsProject, "sinks", "", "errors", m("name", "errors", "destination", "logging.googleapis.com/projects/tenkacloud-local/locations/global/buckets/app", "filter", "severity>=ERROR"), "filter", "severity>=WARNING"},
		{lg, netopsProject, "exclusions", "", "no-debug", m("name", "no-debug", "filter", "severity<INFO"), "disabled", true},
		{lg, netopsProject, "metrics", "", "error_count", m("name", "error_count", "filter", "severity>=ERROR"), "", nil},
		{lg, netopsGlobal, "buckets", "bucketId=app", "app", m("retentionDays", 30), "retentionDays", 60},
		{lg, netopsGlobal + "/buckets/app", "views", "viewId=errors", "errors", m("filter", `SOURCE("projects/tenkacloud-local")`), "description", "Errors"},
		{mon, netopsProject, "notificationChannels", "", "", m("type", "email", "displayName", "Ops", "labels", m("email_address", "ops@example.com")), "displayName", "On-call"},
		{mon, netopsProject, "alertPolicies", "", "", m("displayName", "High errors", "combiner", "OR", "conditions", []any{m("displayName", "errors", "conditionThreshold", m("filter", `metric.type="logging.googleapis.com/user/error_count"`, "comparison", "COMPARISON_GT", "thresholdValue", 1, "duration", "60s"))}), "displayName", "Errors"},
		{mon, netopsProject, "uptimeCheckConfigs", "", "", m("displayName", "Site", "monitoredResource", m("type", "uptime_url", "labels", m("host", "example.com")), "httpCheck", m("path", "/", "port", 443, "useSsl", true), "timeout", "10s", "period", "60s"), "displayName", "Website"},
	}
}

func netopsHarness(t *testing.T) *harness {
	return newHarnessAt(t, []string{"global", "us-central1", "asia-northeast1"}, APIs()...)
}

func resourceOf(r reply) map[string]any {
	if response, ok := r.body["response"].(map[string]any); ok && r.body["done"] == true {
		return response
	}
	return r.body
}

func TestNetopsLifecyclesCoverEveryBoundCollection(t *testing.T) {
	h := netopsHarness(t)
	names := make([]string, 0)
	operations := map[string]string{}
	for _, c := range netopsCases() {
		url := c.api + c.parent + "/" + c.collection
		if c.query != "" {
			url += "?" + c.query
		}
		created := h.call("owner", "POST", url, c.body)
		expect(t, created, 200, "")
		name := c.parent + "/" + c.collection + "/" + c.id
		resource := resourceOf(created)
		if c.id == "" {
			name = resource["name"].(string)
		}
		if created.body["done"] == true {
			operations[c.api+strings.SplitN(created.body["name"].(string), "/operations/", 2)[0]] = c.api + created.body["name"].(string)
			if !strings.HasSuffix(resource["@type"].(string), "."+h.engine.byName[strings.Split(c.api, "/")[1]].api.collection(c.collection).Schema) {
				t.Fatalf("%s response type %v", c.collection, resource["@type"])
			}
		}
		names = append(names, name)
		expect(t, h.call("owner", "GET", c.api+name, nil), 200, "")
		listed := h.call("owner", "GET", c.api+c.parent+"/"+c.collection+"?pageSize=10", nil)
		expect(t, listed, 200, "")
		count := 0
		for _, v := range listed.body {
			if items, ok := v.([]any); ok {
				count = len(items)
			}
		}
		if count != 1 {
			t.Fatalf("%s list %v", c.collection, listed.body)
		}
		if c.patch == "" {
			continue
		}
		expect(t, h.call("owner", "PATCH", c.api+name+"?updateMask="+c.patch, map[string]any{c.patch: c.value}), 200, "")
		got := h.call("owner", "GET", c.api+name, nil)
		if fmt.Sprint(got.body[c.patch]) != fmt.Sprint(c.value) {
			t.Fatalf("%s patch %s: %v", c.collection, c.patch, got.body)
		}
	}
	if len(operations) != 8 {
		t.Fatalf("operation scopes %v", operations)
	}
	for scope, op := range operations {
		expect(t, h.call("owner", "GET", op, nil), 200, "")
		listed := h.call("owner", "GET", scope+"/operations", nil)
		expect(t, listed, 200, "")
		if len(listed.body["operations"].([]any)) == 0 {
			t.Fatalf("operations in %s", scope)
		}
	}
	cases := netopsCases()
	for i := len(cases) - 1; i >= 0; i-- {
		expect(t, h.call("owner", "DELETE", cases[i].api+names[i], nil), 200, "")
		expect(t, h.call("owner", "GET", cases[i].api+names[i], nil), 404, "NOT_FOUND")
	}
}

func TestNetopsServerFieldsAndUnboundMethods(t *testing.T) {
	h := netopsHarness(t)
	spoke := resourceOf(h.call("owner", "POST", "/networkconnectivity/v1/"+netopsRegion+"/spokes?spokeId=vpn", map[string]any{"hub": netopsGlobal + "/hubs/h", "linkedVpnTunnels": map[string]any{"uris": []any{"t"}}}))
	if spoke["spokeType"] != "VPN_TUNNEL" || spoke["state"] != "ACTIVE" || spoke["etag"] == nil {
		t.Fatalf("spoke %v", spoke)
	}
	test := resourceOf(h.call("owner", "POST", "/networkmanagement/v1/"+netopsGlobal+"/connectivityTests?testId=t", map[string]any{"source": map[string]any{"ipAddress": "10.0.0.1"}, "destination": map[string]any{"ipAddress": "10.0.0.2"}}))
	if test["reachabilityDetails"] != nil || test["createTime"] == nil {
		t.Fatalf("connectivity tests never claim a result: %v", test)
	}
	expect(t, h.call("owner", "POST", "/networkmanagement/v1/"+netopsGlobal+"/connectivityTests/t:rerun", map[string]any{}), 501, "UNIMPLEMENTED")
	ids := resourceOf(h.call("owner", "POST", "/ids/v1/"+netopsZone+"/endpoints?endpointId=e", map[string]any{"network": netopsNetwork, "severity": "LOW"}))
	if ids["state"] != "READY" || ids["endpointIp"] != nil {
		t.Fatalf("ids endpoint %v", ids)
	}
	expect(t, h.call("owner", "POST", "/ids/v1/"+netopsRegion+"/endpoints?endpointId=r", map[string]any{"network": netopsNetwork, "severity": "LOW"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", "/networkconnectivity/v1/"+netopsRegion+"/hubs?hubId=h", map[string]any{}), 404, "NOT_FOUND")
	expect(t, h.call("owner", "GET", "/networkconnectivity/v1/"+netopsGlobal+"/hubs/h:listSpokes", nil), 501, "UNIMPLEMENTED")
	// Deleting a namespace removes its services and endpoints.
	sd := "/servicedirectory/v1/" + netopsRegion + "/namespaces"
	expect(t, h.call("owner", "POST", sd+"?namespaceId=n", map[string]any{}), 200, "")
	expect(t, h.call("owner", "POST", sd+"/n/services?serviceId=s", map[string]any{}), 200, "")
	expect(t, h.call("owner", "POST", sd+"/n/services/s/endpoints?endpointId=e", map[string]any{"address": "2001:db8::1"}), 200, "")
	expect(t, h.call("owner", "POST", sd+"/n/services/s:resolve", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "DELETE", sd+"/n", nil), 200, "")
	expect(t, h.call("owner", "GET", sd+"/n/services/s/endpoints/e", nil), 404, "NOT_FOUND")
	// Cloud Logging serves project paths, short names and refuses log data.
	sink := h.call("owner", "POST", "/logging/v2/"+netopsProject+"/sinks", map[string]any{"name": "All.Logs_1", "destination": "storage.googleapis.com/bucket"})
	expect(t, sink, 200, "")
	if sink.body["name"] != "All.Logs_1" || sink.body["writerIdentity"] != nil {
		t.Fatalf("sink %v", sink.body)
	}
	expect(t, h.call("owner", "GET", "/logging/v2/folders/f/sinks", nil), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "POST", "/logging/v2/entries:write", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "PUT", "/logging/v2/"+netopsProject+"/metrics/m", map[string]any{"name": "m", "filter": "x"}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "POST", "/logging/v2/"+netopsGlobal+"/buckets?bucketId=b", map[string]any{}), 200, "")
	expect(t, h.call("owner", "POST", "/logging/v2/"+netopsGlobal+"/buckets/b/views?viewId=v", map[string]any{}), 200, "")
	expect(t, h.call("owner", "DELETE", "/logging/v2/"+netopsGlobal+"/buckets/b", nil), 400, "FAILED_PRECONDITION")
	// Monitoring assigns names and drops records the official API ignores.
	policy := h.call("owner", "POST", "/monitoring/v3/"+netopsProject+"/alertPolicies", map[string]any{"displayName": "p", "creationRecord": map[string]any{"mutatedBy": "me"}, "validity": map[string]any{"code": 3}})
	expect(t, policy, 200, "")
	if !strings.HasPrefix(policy.body["name"].(string), netopsProject+"/alertPolicies/") || policy.body["creationRecord"] != nil || policy.body["validity"] != nil || policy.body["enabled"] != true {
		t.Fatalf("policy %v", policy.body)
	}
	channel := h.call("owner", "POST", "/monitoring/v3/"+netopsProject+"/notificationChannels", map[string]any{"type": "email", "verificationStatus": "VERIFIED"})
	if channel.body["verificationStatus"] != nil {
		t.Fatalf("channel %v", channel.body)
	}
	expect(t, h.call("owner", "POST", "/monitoring/v3/"+channel.body["name"].(string)+":sendVerificationCode", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "GET", "/monitoring/v3/"+netopsProject+"/snoozes", nil), 501, "UNIMPLEMENTED")
	expect(t, h.call("intruder", "GET", "/monitoring/v3/"+netopsProject+"/alertPolicies", nil), 403, "PERMISSION_DENIED")
}

func TestNetopsChecksRefuseInvalidAndSecretConfiguration(t *testing.T) {
	h := netopsHarness(t)
	seed := []struct {
		path string
		body map[string]any
	}{
		{"/servicedirectory/v1/" + netopsRegion + "/namespaces?namespaceId=n", map[string]any{}},
		{"/servicedirectory/v1/" + netopsRegion + "/namespaces/n/services?serviceId=s", map[string]any{}},
		{"/servicedirectory/v1/" + netopsRegion + "/namespaces/n/services/s/endpoints?endpointId=e", map[string]any{}},
		{"/networkconnectivity/v1/" + netopsRegion + "/spokes?spokeId=s", map[string]any{"hub": "h", "gateway": map[string]any{}}},
		{"/networkconnectivity/v1/" + netopsRegion + "/spokes/s/gatewayAdvertisedRoutes?gatewayAdvertisedRouteId=r", map[string]any{"ipRange": "10.0.0.1"}},
		{"/networksecurity/v1/" + netopsGlobal + "/securityProfiles?securityProfileId=p", map[string]any{"type": "URL_FILTERING", "urlFilteringProfile": map[string]any{}}},
		{"/networksecurity/v1/" + netopsRegion + "/addressGroups?addressGroupId=a", map[string]any{"type": "IPV6", "capacity": 1, "items": []any{"2001:db8::/64"}}},
		{"/networkservices/v1/" + netopsRegion + "/gateways?gatewayId=g", map[string]any{"type": "OPEN_MESH", "ports": []any{80}}},
		{"/logging/v2/" + netopsGlobal + "/buckets?bucketId=b", map[string]any{}},
	}
	for _, s := range seed {
		expect(t, h.call("owner", "POST", s.path, s.body), 200, "")
	}
	channel := h.call("owner", "POST", "/monitoring/v3/"+netopsProject+"/notificationChannels", map[string]any{"type": "slack"}).body["name"].(string)
	uptime := h.call("owner", "POST", "/monitoring/v3/"+netopsProject+"/uptimeCheckConfigs", map[string]any{"monitoredResource": map[string]any{"type": "uptime_url", "labels": map[string]any{"host": "h"}}}).body["name"].(string)
	long := strings.Repeat("x", 2001)
	refused := []struct {
		method, path string
		body         map[string]any
	}{
		{"POST", "/servicedirectory/v1/" + netopsRegion + "/namespaces/n/services?serviceId=big", map[string]any{"annotations": map[string]any{"k": long}}},
		{"PATCH", "/servicedirectory/v1/" + netopsRegion + "/namespaces/n/services/s", map[string]any{"annotations": map[string]any{"k": long}}},
		{"POST", "/servicedirectory/v1/" + netopsRegion + "/namespaces/n/services/s/endpoints?endpointId=x", map[string]any{"address": "8.8.8.8:53"}},
		{"POST", "/servicedirectory/v1/" + netopsRegion + "/namespaces/n/services/s/endpoints?endpointId=x", map[string]any{"port": 70000}},
		{"POST", "/servicedirectory/v1/" + netopsRegion + "/namespaces/n/services/s/endpoints?endpointId=x", map[string]any{"annotations": map[string]any{"k": strings.Repeat("y", 600)}}},
		{"PATCH", "/servicedirectory/v1/" + netopsRegion + "/namespaces/n/services/s/endpoints/e", map[string]any{"address": "[::1]"}},
		{"POST", "/networkconnectivity/v1/" + netopsRegion + "/spokes?spokeId=x", map[string]any{"gateway": map[string]any{}}},
		{"POST", "/networkconnectivity/v1/" + netopsRegion + "/spokes?spokeId=x", map[string]any{"hub": "h"}},
		{"POST", "/networkconnectivity/v1/" + netopsRegion + "/spokes?spokeId=x", map[string]any{"hub": "h", "gateway": map[string]any{}, "linkedVpcNetwork": map[string]any{"uri": "n"}}},
		{"POST", "/networkconnectivity/v1/" + netopsRegion + "/spokes/s/gatewayAdvertisedRoutes?gatewayAdvertisedRouteId=x", map[string]any{}},
		{"POST", "/networkconnectivity/v1/" + netopsRegion + "/spokes/s/gatewayAdvertisedRoutes?gatewayAdvertisedRouteId=x", map[string]any{"ipRange": "10.0.0.0/33"}},
		{"POST", "/networkconnectivity/v1/" + netopsRegion + "/spokes/s/gatewayAdvertisedRoutes?gatewayAdvertisedRouteId=x", map[string]any{"ipRange": "10.0.0.0/24", "priority": 70000}},
		{"PATCH", "/networkconnectivity/v1/" + netopsRegion + "/spokes/s/gatewayAdvertisedRoutes/r", map[string]any{"priority": -1}},
		{"POST", "/ids/v1/" + netopsZone + "/endpoints?endpointId=x", map[string]any{"network": netopsNetwork}},
		{"POST", "/ids/v1/" + netopsZone + "/endpoints?endpointId=x", map[string]any{"network": netopsNetwork, "severity": "SEVERITY_UNSPECIFIED"}},
		{"POST", "/networksecurity/v1/" + netopsGlobal + "/securityProfiles?securityProfileId=x", map[string]any{}},
		{"POST", "/networksecurity/v1/" + netopsGlobal + "/securityProfiles?securityProfileId=x", map[string]any{"type": "URL_FILTERING", "threatPreventionProfile": map[string]any{}}},
		{"POST", "/networksecurity/v1/" + netopsGlobal + "/securityProfiles?securityProfileId=1x", map[string]any{"type": "URL_FILTERING"}},
		{"PATCH", "/networksecurity/v1/" + netopsGlobal + "/securityProfiles/p", map[string]any{"urlFilteringProfile": map[string]any{}, "threatPreventionProfile": map[string]any{}}},
		{"POST", "/networksecurity/v1/" + netopsRegion + "/addressGroups?addressGroupId=x", map[string]any{"capacity": 1}},
		{"POST", "/networksecurity/v1/" + netopsRegion + "/addressGroups?addressGroupId=x", map[string]any{"type": "IPV4", "capacity": 1, "items": []any{"10.0.0.1", "10.0.0.2"}}},
		{"POST", "/networksecurity/v1/" + netopsRegion + "/addressGroups?addressGroupId=x", map[string]any{"type": "IPV4", "capacity": 2, "items": []any{"example.com"}}},
		{"PATCH", "/networksecurity/v1/" + netopsRegion + "/addressGroups/a", map[string]any{"items": []any{"nope"}}},
		{"POST", "/networksecurity/v1/" + netopsRegion + "/gatewaySecurityPolicies?gatewaySecurityPolicyId=gp", map[string]any{"bogus": true}},
		{"POST", "/networksecurity/v1/" + netopsZone + "/securityProfiles?securityProfileId=z", map[string]any{"type": "URL_FILTERING"}},
		{"POST", "/networkservices/v1/" + netopsRegion + "/gateways?gatewayId=x", map[string]any{"ports": []any{443}}},
		{"POST", "/networkservices/v1/" + netopsRegion + "/gateways?gatewayId=x", map[string]any{"type": "OPEN_MESH", "ports": []any{0}}},
		{"POST", "/networkservices/v1/" + netopsRegion + "/gateways?gatewayId=x", map[string]any{"type": "OPEN_MESH", "ports": []any{80}, "allPorts": true}},
		{"PATCH", "/networkservices/v1/" + netopsRegion + "/gateways/g", map[string]any{"ports": []any{70000}}},
		{"POST", "/logging/v2/" + netopsGlobal + "/buckets?bucketId=locked", map[string]any{"locked": true}},
		{"PATCH", "/logging/v2/" + netopsGlobal + "/buckets/b", map[string]any{"locked": true}},
		{"POST", "/logging/v2/" + netopsProject + "/metrics", map[string]any{"name": "nginx/requests", "filter": "x"}},
		{"POST", "/logging/v2/" + netopsProject + "/sinks", map[string]any{"name": "-bad", "destination": "d"}},
		{"POST", "/logging/v2/" + netopsProject + "/sinks", map[string]any{"name": "ok", "destination": "d", "writerIdentity": 7}},
		{"POST", "/monitoring/v3/" + netopsProject + "/notificationChannels", map[string]any{"labels": map[string]any{"email_address": "a@example.com"}}},
		{"POST", "/monitoring/v3/" + netopsProject + "/notificationChannels", map[string]any{"type": "slack", "labels": map[string]any{"auth_token": "xoxb"}}},
		{"PATCH", "/monitoring/v3/" + channel, map[string]any{"labels": map[string]any{"service_key": "k"}}},
		{"POST", "/monitoring/v3/" + netopsProject + "/uptimeCheckConfigs", map[string]any{"httpCheck": map[string]any{}}},
		{"POST", "/monitoring/v3/" + netopsProject + "/uptimeCheckConfigs", map[string]any{"monitoredResource": map[string]any{"type": "t", "labels": map[string]any{}}, "resourceGroup": map[string]any{}}},
		{"POST", "/monitoring/v3/" + netopsProject + "/uptimeCheckConfigs", map[string]any{"syntheticMonitor": map[string]any{}, "httpCheck": map[string]any{}, "tcpCheck": map[string]any{}}},
		{"POST", "/monitoring/v3/" + netopsProject + "/uptimeCheckConfigs", map[string]any{"syntheticMonitor": map[string]any{}, "httpCheck": map[string]any{"authInfo": map[string]any{"username": "u", "password": "p"}}}},
		{"POST", "/monitoring/v3/" + netopsProject + "/uptimeCheckConfigs", map[string]any{"syntheticMonitor": map[string]any{}, "httpCheck": map[string]any{"maskHeaders": true}}},
		{"POST", "/monitoring/v3/" + netopsProject + "/uptimeCheckConfigs", map[string]any{"syntheticMonitor": map[string]any{}, "httpCheck": map[string]any{"headers": map[string]any{"Authorization": "Bearer x"}}}},
		{"PATCH", "/monitoring/v3/" + uptime, map[string]any{"resourceGroup": map[string]any{}, "syntheticMonitor": map[string]any{}}},
	}
	for i, r := range refused {
		got := h.call("owner", r.method, r.path, r.body)
		if got.status != 400 || got.code() != "INVALID_ARGUMENT" {
			t.Fatalf("case %d %s %s: %d %s", i, r.method, r.path, got.status, got.raw)
		}
	}
	// A valid patch passes the same checks.
	expect(t, h.call("owner", "PATCH", "/servicedirectory/v1/"+netopsRegion+"/namespaces/n/services/s/endpoints/e?updateMask=address", map[string]any{"address": "192.0.2.1"}), 200, "")
	expect(t, h.call("owner", "PATCH", "/networksecurity/v1/"+netopsGlobal+"/securityProfiles/p?updateMask=urlFilteringProfile", map[string]any{"urlFilteringProfile": map[string]any{}}), 200, "")
	expect(t, h.call("owner", "PATCH", "/monitoring/v3/"+channel+"?updateMask=labels", map[string]any{"labels": map[string]any{"channel_name": "#ops"}}), 200, "")
}
