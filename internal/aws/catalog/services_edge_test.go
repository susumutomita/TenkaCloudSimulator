package catalog

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestEdgeCheckHooksApplyDocumentedRules(t *testing.T) {
	rules := func(rules ...string) map[string]any {
		items := []any{}
		for _, rule := range rules {
			items = append(items, map[string]any{"ipRule": rule})
		}
		return map[string]any{"UserRules": items}
	}
	window := func(members ...string) map[string]any {
		w := map[string]any{"type": "CUSTOM"}
		for _, m := range members {
			w[m] = int64(1)
		}
		return map[string]any{"maintenanceWindow": w}
	}
	ruleSet := func(body string) map[string]any { return map[string]any{"RuleSetBody": body} }
	cases := []struct {
		name  string
		hook  func(map[string]any) error
		input map[string]any
		code  string
	}{
		{"ip tags", workSpacesIPGroup, map[string]any{"Tags": []any{map[string]any{"Key": "a"}}}, "InvalidParameterValuesException"},
		{"ip cidr and address", workSpacesIPGroup, rules("10.0.0.0/16", "192.0.2.1"), ""},
		{"ip text", workSpacesIPGroup, rules("office"), "InvalidParameterValuesException"},
		{"ipv6 cidr", workSpacesIPGroup, rules("2001:db8::/32"), "InvalidParameterValuesException"},
		{"ipv6 address", workSpacesIPGroup, rules("::1"), "InvalidParameterValuesException"},
		{"alias empty tags", workSpacesNoTags, map[string]any{"Tags": []any{}}, ""},
		{"custom app block", appStreamSetupScript, map[string]any{"PackagingType": "CUSTOM"}, "InvalidParameterCombinationException"},
		{"default app block", appStreamSetupScript, map[string]any{}, "InvalidParameterCombinationException"},
		{"appstream2 app block", appStreamSetupScript, map[string]any{"PackagingType": "APPSTREAM2"}, ""},
		{"scripted app block", appStreamSetupScript, map[string]any{"SetupScriptDetails": map[string]any{}}, ""},
		{"workspaces desktop", thinClientEnvironment, map[string]any{"desktopArn": "arn:aws:workspaces:us-east-1:000000000000:directory/d-1234567890"}, ""},
		{"appstream desktop", thinClientEnvironment, map[string]any{"desktopArn": "arn:aws:appstream:us-east-1:000000000000:stack/kiosk"}, ""},
		{"wrong desktop resource", thinClientEnvironment, map[string]any{"desktopArn": "arn:aws:workspaces:us-east-1:000000000000:workspace/ws-1"}, "ValidationException"},
		{"unknown desktop service", thinClientEnvironment, map[string]any{"desktopArn": "arn:aws:s3:::bucket/key"}, "ValidationException"},
		{"malformed desktop", thinClientEnvironment, map[string]any{"desktopArn": "desktop"}, "ValidationException"},
		{"desired software set", thinClientEnvironment, map[string]any{"desiredSoftwareSetId": "1"}, "ResourceNotFoundException"},
		{"use desired", thinClientEnvironment, map[string]any{"softwareSetUpdateMode": "USE_DESIRED"}, "ResourceNotFoundException"},
		{"partial window", thinClientEnvironment, window("startTimeHour", "startTimeMinute", "endTimeHour"), "ValidationException"},
		{"custom window", thinClientEnvironment, window("startTimeHour", "startTimeMinute", "endTimeHour", "endTimeMinute"), ""},
		{"system window", thinClientEnvironment, map[string]any{"maintenanceWindow": map[string]any{"type": "SYSTEM"}}, ""},
		{"whole portal", secureBrowserPortal, map[string]any{"portalArn": "arn:aws:workspaces-web:us-east-1:000000000000:portal/0f1e2d3c-aaaa-bbbb-cccc-111122223333"}, ""},
		{"portal suffix", secureBrowserPortal, map[string]any{"portalArn": "arn:aws:workspaces-web:us-east-1:000000000000:portal/0f1e/browserSettings"}, "ValidationException"},
		{"browser policy", secureBrowserPolicy, map[string]any{"browserPolicy": `{"chromePolicies":{}}`}, ""},
		{"browser policy text", secureBrowserPolicy, map[string]any{"browserPolicy": `{not json}`}, "ValidationException"},
		{"no browser policy", secureBrowserPolicy, map[string]any{}, ""},
		{"terminal alias", gameLiftRouting, map[string]any{"RoutingStrategy": map[string]any{"Type": "TERMINAL", "Message": "closed"}}, ""},
		{"simple alias", gameLiftRouting, map[string]any{"RoutingStrategy": map[string]any{"Type": "SIMPLE", "FleetId": "fleet-1"}}, "InvalidRequestException"},
		{"terminal fleet", gameLiftRouting, map[string]any{"RoutingStrategy": map[string]any{"Type": "TERMINAL", "FleetId": "fleet-1"}}, "InvalidRequestException"},
		{"alias rename", gameLiftRouting, map[string]any{"Name": "renamed"}, ""},
		{"queue destinations", gameLiftDestinations, map[string]any{"Destinations": []any{map[string]any{"DestinationArn": "arn:aws:gamelift:us-east-1::fleet/fleet-1"}}}, "InvalidRequestException"},
		{"queue without destinations", gameLiftDestinations, map[string]any{"Destinations": []any{}}, ""},
		{"rule set", gameLiftRuleSet, ruleSet(`{"ruleLanguageVersion":"1.0","teams":[{"name":"red","minPlayers":1,"maxPlayers":4}]}`), ""},
		{"rule set text", gameLiftRuleSet, ruleSet(`teams`), "InvalidRequestException"},
		{"rule set version", gameLiftRuleSet, ruleSet(`{"teams":[{}]}`), "InvalidRequestException"},
		{"rule set teams", gameLiftRuleSet, ruleSet(`{"ruleLanguageVersion":"1.0","teams":[]}`), "InvalidRequestException"},
		{"stream source", gameLiftStreamsSource, map[string]any{"ApplicationSourceUri": "s3://bucket/game"}, ""},
		{"stream web source", gameLiftStreamsSource, map[string]any{"ApplicationSourceUri": "https://example.com/game"}, "ValidationException"},
		{"expected version", iotExpectedVersion, map[string]any{"expectedVersion": int64(1)}, ""},
		{"stale version", iotExpectedVersion, map[string]any{"expectedVersion": int64(2)}, "VersionConflictException"},
		{"thing version", iotThing, map[string]any{"expectedVersion": int64(3)}, "VersionConflictException"},
		{"billing group", iotThing, map[string]any{"billingGroupName": "b"}, "InvalidRequestException"},
		{"remove type", iotThing, map[string]any{"removeThingType": true}, "InvalidRequestException"},
		{"merge thing", iotThing, map[string]any{"attributePayload": map[string]any{"merge": true}}, "InvalidRequestException"},
		{"group version", iotThingGroup, map[string]any{"expectedVersion": int64(2)}, "VersionConflictException"},
		{"group parent", iotThingGroup, map[string]any{"parentGroupName": "root"}, "InvalidRequestException"},
		{"merge group", iotThingGroup, map[string]any{"thingGroupProperties": map[string]any{"attributePayload": map[string]any{"merge": true}}}, "InvalidRequestException"},
		{"group without attributes", iotThingGroup, map[string]any{"thingGroupProperties": map[string]any{"thingGroupDescription": "d"}}, ""},
		{"policy", iotPolicyDocument, map[string]any{"policyDocument": `{"Version":"2012-10-17","Statement":[]}`}, ""},
		{"policy text", iotPolicyDocument, map[string]any{"policyDocument": `allow`}, "MalformedPolicyException"},
		{"policy size", iotPolicyDocument, map[string]any{"policyDocument": `{"Statement":"` + strings.Repeat("a", 2040) + `"}`}, "MalformedPolicyException"},
		{"policy statement", iotPolicyDocument, map[string]any{"policyDocument": `{"Version":"2012-10-17"}`}, "MalformedPolicyException"},
		{"profile version", iotSecurityProfile, map[string]any{"expectedVersion": int64(4)}, "VersionConflictException"},
		{"profile delete flag", iotSecurityProfile, map[string]any{"deleteAlertTargets": true}, "InvalidRequestException"},
		{"duplicate behaviors", iotSecurityProfile, map[string]any{"behaviors": []any{map[string]any{"name": "a"}, map[string]any{"name": "a"}}}, "InvalidRequestException"},
		{"unique behaviors", iotSecurityProfile, map[string]any{"deleteBehaviors": false, "behaviors": []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}}}, ""},
		{"tag thing", iotTaggable, map[string]any{"resourceArn": "arn:aws:iot:us-east-1:000000000000:thing/sensor"}, "InvalidRequestException"},
		{"tag group", iotTaggable, map[string]any{"resourceArn": "arn:aws:iot:us-east-1:000000000000:thinggroup/line"}, ""},
		{"service role", greengrassRole, map[string]any{"roleArn": "arn:aws:iam::000000000000:role/Greengrass_ServiceRole"}, ""},
		{"service role user", greengrassRole, map[string]any{"roleArn": "arn:aws:iam::000000000000:user/admin"}, "ValidationException"},
		{"one platform", siteWiseGatewayPlatform, map[string]any{"gatewayPlatform": map[string]any{"greengrassV2": map[string]any{"coreDeviceThingName": "core"}}}, ""},
		{"no platform", siteWiseGatewayPlatform, map[string]any{"gatewayPlatform": map[string]any{}}, "InvalidRequestException"},
		{"two platforms", siteWiseGatewayPlatform, map[string]any{"gatewayPlatform": map[string]any{"greengrassV2": map[string]any{}, "siemensIE": map[string]any{}}}, "InvalidRequestException"},
		{"sidewalk", wirelessNoSidewalk, map[string]any{"Sidewalk": map[string]any{}}, "ValidationException"},
		{"lorawan", wirelessNoSidewalk, map[string]any{"LoRaWAN": map[string]any{"MacVersion": "1.0.3"}}, ""},
	}
	for _, c := range cases {
		err := c.hook(c.input)
		var f *fault
		if c.code == "" && err != nil || c.code != "" && (!errors.As(err, &f) || f.code != c.code) {
			t.Fatalf("%s: want %q, got %v", c.name, c.code, err)
		}
	}
	desktop := map[string]any{"desktopArn": "arn:aws:workspaces-web:us-east-1:000000000000:portal/p"}
	if err := thinClientEnvironment(desktop); err != nil || desktop["desktopType"] != "workspaces-web" {
		t.Fatalf("desktop type %v %v", desktop, err)
	}
	thing := map[string]any{"removeThingType": false, "attributePayload": map[string]any{"attributes": map[string]any{"room": "a", "old": ""}, "merge": false}}
	if err := iotThing(thing); err != nil || len(thing) != 1 || thing["attributes"].(map[string]any)["room"] != "a" || len(thing["attributes"].(map[string]any)) != 1 {
		t.Fatalf("thing attributes %v %v", thing, err)
	}
	group := map[string]any{"thingGroupProperties": map[string]any{"attributePayload": map[string]any{"attributes": map[string]any{"line": "1"}, "merge": false}}}
	if err := iotThingGroup(group); err != nil {
		t.Fatal(err)
	}
	if payload := group["thingGroupProperties"].(map[string]any)["attributePayload"].(map[string]any); len(payload) != 1 || payload["attributes"].(map[string]any)["line"] != "1" {
		t.Fatalf("group attributes %v", payload)
	}
	profile := map[string]any{"deleteBehaviors": false, "deleteMetricsExportConfig": false}
	tagged := map[string]any{"resourceArn": "arn", "clientToken": "t"}
	if iotSecurityProfile(profile) != nil || len(profile) != 0 || dropClientToken(tagged) != nil || len(tagged) != 1 {
		t.Fatalf("false flags and tokens are removed: %v %v", profile, tagged)
	}
}

// IoT registry writes store attributes, keep one version and refuse tagging
// things; summaries use the official list element shapes.
func TestIoTRegistryKeepsOfficialShapes(t *testing.T) {
	h := newHarness(t, iotCore)
	thing := h.rest("iot", "POST", "/things/sensor-1", map[string]any{"thingTypeName": "sensor", "attributePayload": map[string]any{"attributes": map[string]any{"room": "a"}}})
	expect(t, thing, 200, "")
	if thing.body["thingArn"] != "arn:aws:iot:us-east-1:000000000000:thing/sensor-1" || len(str(thing.body["thingId"])) != 36 {
		t.Fatalf("thing identity %v", thing.body)
	}
	expect(t, h.rest("iot", "PATCH", "/things/sensor-1", map[string]any{"attributePayload": map[string]any{"attributes": map[string]any{"room": "b", "floor": "2"}}, "expectedVersion": 1}), 200, "")
	described := h.rest("iot", "GET", "/things/sensor-1", nil)
	if attributes := described.body["attributes"].(map[string]any); attributes["room"] != "b" || described.body["defaultClientId"] != "sensor-1" || described.body["version"] != json.Number("1") {
		t.Fatalf("thing description %v", described.body)
	}
	expect(t, h.rest("iot", "PATCH", "/things/sensor-1", map[string]any{"expectedVersion": 2}), 409, "VersionConflictException")
	expect(t, h.rest("iot", "POST", "/tags", map[string]any{"resourceArn": thing.body["thingArn"], "tags": []any{map[string]any{"Key": "a", "Value": "b"}}}), 400, "InvalidRequestException")
	if listed := h.rest("iot", "GET", "/things?thingTypeName=sensor", nil).body["things"].([]any); len(listed) != 1 || listed[0].(map[string]any)["attributes"].(map[string]any)["floor"] != "2" {
		t.Fatalf("thing list %v", listed)
	}
	group := h.rest("iot", "POST", "/thing-groups/line-1", map[string]any{"thingGroupProperties": map[string]any{"thingGroupDescription": "line"}})
	expect(t, group, 200, "")
	groups := h.rest("iot", "GET", "/thing-groups", nil).body["thingGroups"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["groupName"] != "line-1" || groups[0].(map[string]any)["groupArn"] != group.body["thingGroupArn"] {
		t.Fatalf("group summaries %v", groups)
	}
	expect(t, h.rest("iot", "DELETE", "/thing-groups/line-1", nil), 200, "")
	expect(t, h.rest("iot", "DELETE", "/thing-groups/line-1", nil), 200, "")
	expect(t, h.rest("iot", "DELETE", "/things/sensor-1?expectedVersion=1", nil), 200, "")
	expect(t, h.rest("iot", "DELETE", "/things/sensor-1", nil), 200, "")
	expect(t, h.rest("iot", "GET", "/things/sensor-1", nil), 404, "ResourceNotFoundException")
}

// Routes that extend a greedy portal ARN are refused, never treated as a
// successful portal update or idempotent portal deletion.
func TestSecureBrowserRefusesRoutesBelowPortalARNs(t *testing.T) {
	h := newHarness(t, secureBrowser)
	created := h.rest("workspaces-web", "POST", "/portals", map[string]any{"displayName": "Kiosk"})
	expect(t, created, 200, "")
	arn := str(created.body["portalArn"])
	if !strings.HasPrefix(arn, "arn:aws:workspaces-web:us-east-1:000000000000:portal/") || created.body["portalEndpoint"] != strings.TrimPrefix(arn, "arn:aws:workspaces-web:us-east-1:000000000000:portal/")+".workspaces-web.com" {
		t.Fatalf("portal %v", created.body)
	}
	settings := h.rest("workspaces-web", "POST", "/browserSettings", map[string]any{"browserPolicy": `{"chromePolicies":{}}`})
	expect(t, settings, 200, "")
	query := "?browserSettingsArn=" + url.QueryEscape(str(settings.body["browserSettingsArn"]))
	expect(t, h.rest("workspaces-web", "PUT", "/portals/"+arn+"/browserSettings"+query, nil), 501, "UnknownOperationException")
	expect(t, h.rest("workspaces-web", "DELETE", "/portals/"+arn+"/browserSettings", nil), 501, "UnknownOperationException")
	expect(t, h.rest("workspaces-web", "GET", "/portals/"+arn+"/identityProviders", nil), 501, "UnknownOperationException")
	if got := h.rest("workspaces-web", "GET", "/portals/"+arn, nil); got.body["portal"].(map[string]any)["portalStatus"] != "Incomplete" {
		t.Fatalf("portal remains %v", got.body)
	}
	expect(t, h.rest("workspaces-web", "DELETE", "/portals/"+arn, nil), 200, "")
	expect(t, h.rest("workspaces-web", "DELETE", "/portals/"+arn, nil), 200, "")
	expect(t, h.rest("workspaces-web", "GET", "/portals/"+arn, nil), 404, "ResourceNotFoundException")
}

// The Greengrass service role is a Regional singleton association.
func TestGreengrassServiceRoleAssociation(t *testing.T) {
	h := newHarness(t, greengrassV2)
	if got := h.rest("greengrassv2", "GET", "/greengrass/servicerole", nil); got.status != 200 || len(got.body) != 0 {
		t.Fatalf("no association %d %v", got.status, got.body)
	}
	role := "arn:aws:iam::000000000000:role/Greengrass_ServiceRole"
	associated := h.rest("greengrassv2", "PUT", "/greengrass/servicerole", map[string]any{"RoleArn": role})
	expect(t, associated, 200, "")
	if associated.body["AssociatedAt"] == nil {
		t.Fatalf("association time %v", associated.body)
	}
	expect(t, h.rest("greengrassv2", "PUT", "/greengrass/servicerole", map[string]any{"RoleArn": "arn:aws:iam::000000000000:user/u"}), 400, "ValidationException")
	if got := h.rest("greengrassv2", "GET", "/greengrass/servicerole", nil); got.body["RoleArn"] != role {
		t.Fatalf("stored role %v", got.body)
	}
	disassociated := h.rest("greengrassv2", "DELETE", "/greengrass/servicerole", nil)
	if disassociated.status != 200 || disassociated.body["DisassociatedAt"] == nil {
		t.Fatalf("disassociation %d %v", disassociated.status, disassociated.body)
	}
	expect(t, h.rest("greengrassv2", "DELETE", "/greengrass/servicerole", nil), 404, "ResourceNotFoundException")
}

// WorkSpaces IP groups map the official create members onto the lowercase
// members of the described group.
func TestWorkSpacesIPGroupsUseOfficialMemberNames(t *testing.T) {
	h := newHarness(t, workSpaces)
	created := h.json1("workspaces", "CreateIpGroup", map[string]any{"GroupName": "office", "GroupDesc": "HQ", "UserRules": []any{map[string]any{"ipRule": "198.51.100.0/24"}}})
	expect(t, created, 200, "")
	id := str(created.body["GroupId"])
	if !strings.HasPrefix(id, "wsipg-") {
		t.Fatalf("group id %v", created.body)
	}
	expect(t, h.json1("workspaces", "UpdateRulesOfIpGroup", map[string]any{"GroupId": id, "UserRules": []any{}}), 200, "")
	groups := h.json1("workspaces", "DescribeIpGroups", map[string]any{"GroupIds": []any{id}}).body["Result"].([]any)
	if group := groups[0].(map[string]any); group["groupName"] != "office" || group["groupDesc"] != "HQ" || len(group["userRules"].([]any)) != 0 {
		t.Fatalf("group %v", group)
	}
	expect(t, h.json1("workspaces", "DescribeIpGroups", map[string]any{"GroupIds": []any{"wsipg-missing00"}}), 400, "InvalidParameterValuesException")
	expect(t, h.json1("workspaces", "CreateIpGroup", map[string]any{"GroupName": "office"}), 400, "ResourceAlreadyExistsException")
}
