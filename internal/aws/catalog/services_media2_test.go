package catalog

import (
	"errors"
	"strings"
	"testing"
)

func media2Harness(t *testing.T) *harness {
	t.Helper()
	return newHarness(t, mediaConnect, mediaLive, ivs, ivsChat, ivsRealtime)
}

func member(t *testing.T, body map[string]any, path ...string) map[string]any {
	t.Helper()
	current := body
	for _, key := range path {
		next, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("missing %s in %v", key, body)
		}
		current = next
	}
	return current
}

func TestMedia2CheckHooksApplyDocumentedRules(t *testing.T) {
	sdi := map[string]any{"Username": "u"}
	nested := map[string]any{"EncoderSettings": map[string]any{"OutputGroups": []any{map[string]any{"Outputs": []any{map[string]any{"PasswordParam": "p"}}}}}}
	cases := []struct {
		name  string
		hook  func(map[string]any) error
		input map[string]any
		code  string
	}{
		{"gateway", mediaConnectGateway, map[string]any{"EgressCidrBlocks": []any{"10.0.0.0/24"}, "Networks": []any{map[string]any{"CidrBlock": "10.1.0.0/24"}}}, ""},
		{"gateway egress", mediaConnectGateway, map[string]any{"EgressCidrBlocks": []any{"office"}}, "BadRequestException"},
		{"gateway ipv6", mediaConnectGateway, map[string]any{"EgressCidrBlocks": []any{"2001:db8::/32"}}, "BadRequestException"},
		{"gateway network", mediaConnectGateway, map[string]any{"Networks": []any{map[string]any{"CidrBlock": "x"}}}, "BadRequestException"},
		{"interface region", mediaConnectNetworkInterface, map[string]any{"RegionName": "eu-west-1"}, "BadRequestException"},
		{"interface served region", mediaConnectNetworkInterface, map[string]any{"RegionName": "us-east-1"}, ""},
		{"interface both", mediaConnectNetworkInterface, map[string]any{"Configuration": map[string]any{"Public": map[string]any{}, "Vpc": map[string]any{}}}, "BadRequestException"},
		{"interface neither", mediaConnectNetworkInterface, map[string]any{"Configuration": map[string]any{}}, "BadRequestException"},
		{"interface vpc", mediaConnectNetworkInterface, map[string]any{"Configuration": map[string]any{"Vpc": map[string]any{}}}, ""},
		{"interface rule", mediaConnectNetworkInterface, map[string]any{"Configuration": map[string]any{"Public": map[string]any{"AllowRules": []any{map[string]any{"Cidr": "x"}}}}}, "BadRequestException"},
		{"interface no config", mediaConnectNetworkInterface, map[string]any{}, ""},
		{"whitelist", mediaLiveWhitelist, map[string]any{"WhitelistRules": []any{map[string]any{"Cidr": "10.0.0.0/8"}}}, ""},
		{"whitelist text", mediaLiveWhitelist, map[string]any{"WhitelistRules": []any{map[string]any{"Cidr": "any"}}}, "BadRequestException"},
		{"network", mediaLiveNetwork, map[string]any{"IpPools": []any{map[string]any{"Cidr": "10.0.0.0/24"}}, "Routes": []any{map[string]any{"Cidr": "10.1.0.0/24"}}}, ""},
		{"network pool", mediaLiveNetwork, map[string]any{"IpPools": []any{map[string]any{"Cidr": "x"}}}, "BadRequestException"},
		{"network route", mediaLiveNetwork, map[string]any{"Routes": []any{map[string]any{"Cidr": "x"}}}, "BadRequestException"},
		{"channel inputs", mediaLiveChannel, map[string]any{"InputAttachments": []any{}}, "BadRequestException"},
		{"channel vpc", mediaLiveChannel, map[string]any{"Vpc": map[string]any{}}, "BadRequestException"},
		{"channel cluster", mediaLiveChannel, map[string]any{"AnywhereSettings": map[string]any{}}, "BadRequestException"},
		{"channel reserved", mediaLiveChannel, map[string]any{"Reserved": "x"}, "BadRequestException"},
		{"channel dry run", mediaLiveChannel, map[string]any{"DryRun": true}, "BadRequestException"},
		{"channel no dry run", mediaLiveChannel, map[string]any{"DryRun": false}, ""},
		{"channel user", mediaLiveChannel, map[string]any{"Destinations": []any{map[string]any{"Settings": []any{sdi}}}}, "BadRequestException"},
		{"channel nested password", mediaLiveChannel, nested, "BadRequestException"},
		{"channel stream name", mediaLiveChannel, map[string]any{"Destinations": []any{map[string]any{"Settings": []any{map[string]any{"StreamName": "k"}}}}}, "BadRequestException"},
		{"channel url", mediaLiveChannel, map[string]any{"Destinations": []any{map[string]any{"Settings": []any{map[string]any{"Url": "s3://bucket/key"}}}}}, ""},
		{"recording", ivsRecording, map[string]any{"destinationConfiguration": map[string]any{"s3": map[string]any{}}}, ""},
		{"recording bucket", ivsRecording, map[string]any{"destinationConfiguration": map[string]any{}}, "ValidationException"},
		{"chat destination", ivsChatDestination, map[string]any{"destinationConfiguration": map[string]any{"s3": map[string]any{}}}, ""},
		{"chat two destinations", ivsChatDestination, map[string]any{"destinationConfiguration": map[string]any{"s3": map[string]any{}, "firehose": map[string]any{}}}, "ValidationException"},
		{"chat no destination", ivsChatDestination, map[string]any{"name": "x"}, ""},
		{"stage tokens", ivsRealtimeStage, map[string]any{"participantTokenConfigurations": []any{}}, "ValidationException"},
		{"stage", ivsRealtimeStage, map[string]any{"name": "x"}, ""},
	}
	for _, c := range cases {
		err := c.hook(c.input)
		var f *fault
		if c.code == "" && err != nil || c.code != "" && (!errors.As(err, &f) || f.code != c.code) {
			t.Fatalf("%s: want %q, got %v", c.name, c.code, err)
		}
	}
	input := map[string]any{"Configuration": map[string]any{"Public": map[string]any{}}, "RegionName": "us-east-1"}
	if err := mediaConnectNetworkInterface(input); err != nil || input["NetworkInterfaceType"] != "PUBLIC" || input["RegionName"] != nil {
		t.Fatalf("interface type %v %v", input, err)
	}
}

func TestMediaConnectRecordsKeepOfficialShapes(t *testing.T) {
	h := media2Harness(t)
	created := h.rest("mediaconnect", "POST", "/v1/gateways", map[string]any{"name": "ground", "egressCidrBlocks": []any{"10.0.0.0/24"},
		"networks": []any{map[string]any{"cidrBlock": "10.1.0.0/24", "name": "lan"}}})
	expect(t, created, 201, "")
	gateway := member(t, created.body, "gateway")
	arn := str(gateway["gatewayArn"])
	if !strings.HasPrefix(arn, "arn:aws:mediaconnect:us-east-1:000000000000:gateway:") || !strings.HasSuffix(arn, ":ground") || gateway["gatewayState"] != "ACTIVE" {
		t.Fatalf("gateway %v", gateway)
	}
	for key := range gateway {
		if strings.Contains(strings.ToLower(key), "endpoint") || strings.Contains(strings.ToLower(key), "ip") && key != "egressCidrBlocks" {
			t.Fatalf("gateway reports an address %s", key)
		}
	}
	described := h.rest("mediaconnect", "GET", "/v1/gateways/"+escapeARN(arn), nil)
	expect(t, described, 200, "")
	if member(t, described.body, "gateway")["name"] != "ground" {
		t.Fatalf("described %v", described.body)
	}
	listed := h.rest("mediaconnect", "GET", "/v1/gateways", nil)
	if items := listed.body["gateways"].([]any); len(items) != 1 || items[0].(map[string]any)["gatewayState"] != "ACTIVE" {
		t.Fatalf("listed %v", listed.body)
	}
	expect(t, h.rest("mediaconnect", "POST", "/v1/gateways", map[string]any{"name": "bad", "egressCidrBlocks": []any{"x"}, "networks": []any{}}), 400, "BadRequestException")
	expect(t, h.rest("mediaconnect", "GET", "/v1/gateways/"+escapeARN("arn:aws:mediaconnect:us-east-1:000000000000:gateway:none:none"), nil), 404, "NotFoundException")
	deleted := h.rest("mediaconnect", "DELETE", "/v1/gateways/"+escapeARN(arn), nil)
	expect(t, deleted, 200, "")
	if deleted.body["gatewayArn"] != arn {
		t.Fatalf("deleted %v", deleted.body)
	}
	expect(t, h.rest("mediaconnect", "GET", "/v1/gateways/"+escapeARN(arn), nil), 404, "NotFoundException")

	nic := h.rest("mediaconnect", "POST", "/v1/routerNetworkInterface", map[string]any{"name": "edge",
		"configuration": map[string]any{"public": map[string]any{"allowRules": []any{map[string]any{"cidr": "10.0.0.0/8"}}}}, "tags": map[string]any{"team": "a"}})
	expect(t, nic, 201, "")
	face := member(t, nic.body, "routerNetworkInterface")
	faceARN := str(face["arn"])
	if face["networkInterfaceType"] != "PUBLIC" || face["state"] != "ACTIVE" || face["regionName"] != "us-east-1" || face["associatedInputCount"] != float64(0) && str(face["id"]) == "" {
		t.Fatalf("interface %v", face)
	}
	vpc := h.rest("mediaconnect", "POST", "/v1/routerNetworkInterface", map[string]any{"name": "private", "regionName": "us-east-1",
		"configuration": map[string]any{"vpc": map[string]any{"securityGroupIds": []any{"sg-1"}, "subnetId": "subnet-1"}}})
	expect(t, vpc, 201, "")
	if member(t, vpc.body, "routerNetworkInterface")["networkInterfaceType"] != "VPC" {
		t.Fatalf("vpc %v", vpc.body)
	}
	updated := h.rest("mediaconnect", "PUT", "/v1/routerNetworkInterface/"+escapeARN(faceARN), map[string]any{"name": "edge-2"})
	expect(t, updated, 202, "")
	if member(t, updated.body, "routerNetworkInterface")["name"] != "edge-2" {
		t.Fatalf("updated %v", updated.body)
	}
	expect(t, h.rest("mediaconnect", "PUT", "/v1/routerNetworkInterface/"+escapeARN(faceARN), map[string]any{"configuration": map[string]any{}}), 400, "BadRequestException")
	if items := h.rest("mediaconnect", "POST", "/v1/routerNetworkInterfaces", map[string]any{}).body["routerNetworkInterfaces"].([]any); len(items) != 2 {
		t.Fatalf("interfaces %v", items)
	}
	expect(t, h.rest("mediaconnect", "GET", "/v1/routerNetworkInterface/"+escapeARN(faceARN), nil), 200, "")

	// Both tag route families address the same stored tags.
	expect(t, h.rest("mediaconnect", "POST", "/tags/global/"+escapeARN(faceARN), map[string]any{"tags": map[string]any{"env": "dev"}}), 204, "")
	expect(t, h.rest("mediaconnect", "POST", "/tags/"+escapeARN(faceARN), map[string]any{"tags": map[string]any{"owner": "ops"}}), 204, "")
	tags := h.rest("mediaconnect", "GET", "/tags/global/"+escapeARN(faceARN), nil)
	if got := member(t, tags.body, "tags"); len(got) != 3 || got["env"] != "dev" || got["team"] != "a" {
		t.Fatalf("tags %v", tags.body)
	}
	expect(t, h.rest("mediaconnect", "DELETE", "/tags/"+escapeARN(faceARN)+"?tagKeys=env", nil), 204, "")
	expect(t, h.rest("mediaconnect", "DELETE", "/tags/global/"+escapeARN(faceARN)+"?tagKeys=owner", nil), 204, "")
	if got := member(t, h.rest("mediaconnect", "GET", "/tags/"+escapeARN(faceARN), nil).body, "tags"); len(got) != 1 {
		t.Fatalf("tags after untag %v", got)
	}
	expect(t, h.rest("mediaconnect", "GET", "/tags/"+escapeARN("arn:aws:mediaconnect:us-east-1:000000000000:routerNetworkInterface:none"), nil), 404, "NotFoundException")
	expect(t, h.rest("mediaconnect", "DELETE", "/v1/routerNetworkInterface/"+escapeARN(faceARN), nil), 200, "")

	// Flows, bridges and router endpoints are not served.
	expect(t, h.rest("mediaconnect", "POST", "/v1/flows", map[string]any{"name": "f"}), 501, "UnknownOperationException")
	expect(t, h.rest("mediaconnect", "POST", "/v1/bridges", map[string]any{"name": "b"}), 501, "UnknownOperationException")
	expect(t, h.rest("mediaconnect", "POST", "/v1/routerInput", map[string]any{"name": "i"}), 501, "UnknownOperationException")
}

func TestMediaLiveRecordsKeepOfficialShapes(t *testing.T) {
	h := media2Harness(t)
	group := h.rest("medialive", "POST", "/prod/inputSecurityGroups", map[string]any{"whitelistRules": []any{map[string]any{"cidr": "10.0.0.0/8"}}, "tags": map[string]any{"team": "a"}})
	expect(t, group, 200, "")
	sg := member(t, group.body, "securityGroup")
	sgARN, sgID := str(sg["arn"]), str(sg["id"])
	if sg["state"] != "IDLE" || len(sg["inputs"].([]any)) != 0 || !strings.HasPrefix(sgARN, "arn:aws:medialive:us-east-1:000000000000:inputSecurityGroup:") {
		t.Fatalf("group %v", sg)
	}
	updated := h.rest("medialive", "PUT", "/prod/inputSecurityGroups/"+sgID, map[string]any{"whitelistRules": []any{map[string]any{"cidr": "192.0.2.0/24"}}})
	expect(t, updated, 200, "")
	if rules := member(t, updated.body, "securityGroup")["whitelistRules"].([]any); len(rules) != 1 || rules[0].(map[string]any)["cidr"] != "192.0.2.0/24" {
		t.Fatalf("rules %v", rules)
	}
	expect(t, h.rest("medialive", "PUT", "/prod/inputSecurityGroups/"+sgID, map[string]any{"whitelistRules": []any{map[string]any{"cidr": "x"}}}), 400, "BadRequestException")
	expect(t, h.rest("medialive", "PUT", "/prod/inputSecurityGroups/"+sgID, map[string]any{"tags": map[string]any{"a": "b"}}), 400, "")
	expect(t, h.rest("medialive", "GET", "/prod/inputSecurityGroups/"+sgID, nil), 200, "")
	if items := h.rest("medialive", "GET", "/prod/inputSecurityGroups", nil).body["inputSecurityGroups"].([]any); len(items) != 1 {
		t.Fatalf("groups %v", items)
	}
	expect(t, h.rest("medialive", "GET", "/prod/inputSecurityGroups/missing", nil), 404, "NotFoundException")

	network := h.rest("medialive", "POST", "/prod/networks", map[string]any{"name": "site", "ipPools": []any{map[string]any{"cidr": "10.0.0.0/24"}},
		"routes": []any{map[string]any{"cidr": "10.9.0.0/24", "gateway": "10.0.0.1"}}})
	expect(t, network, 201, "")
	networkID := str(network.body["id"])
	if network.body["state"] != "IDLE" || len(network.body["associatedClusterIds"].([]any)) != 0 {
		t.Fatalf("network %v", network.body)
	}
	expect(t, h.rest("medialive", "POST", "/prod/networks", map[string]any{"name": "bad", "ipPools": []any{map[string]any{"cidr": "x"}}}), 400, "BadRequestException")
	renamed := h.rest("medialive", "PUT", "/prod/networks/"+networkID, map[string]any{"name": "site-2"})
	expect(t, renamed, 200, "")
	if renamed.body["name"] != "site-2" || len(renamed.body["routes"].([]any)) != 1 {
		t.Fatalf("renamed %v", renamed.body)
	}
	expect(t, h.rest("medialive", "GET", "/prod/networks/"+networkID, nil), 200, "")
	if items := h.rest("medialive", "GET", "/prod/networks", nil).body["networks"].([]any); len(items) != 1 {
		t.Fatalf("networks %v", items)
	}

	sdi := h.rest("medialive", "POST", "/prod/sdiSources", map[string]any{"name": "card", "mode": "QUADRANT", "type": "QUAD"})
	expect(t, sdi, 200, "")
	sdiID := str(member(t, sdi.body, "sdiSource")["id"])
	expect(t, h.rest("medialive", "PUT", "/prod/sdiSources/"+sdiID, map[string]any{"mode": "INTERLEAVE"}), 200, "")
	if got := member(t, h.rest("medialive", "GET", "/prod/sdiSources/"+sdiID, nil).body, "sdiSource"); got["mode"] != "INTERLEAVE" || got["state"] != "IDLE" {
		t.Fatalf("sdi %v", got)
	}
	if items := h.rest("medialive", "GET", "/prod/sdiSources", nil).body["sdiSources"].([]any); len(items) != 1 {
		t.Fatalf("sdi sources %v", items)
	}

	channel := h.rest("medialive", "POST", "/prod/channels", map[string]any{"name": "main", "roleArn": "arn:aws:iam::000000000000:role/MediaLiveAccessRole",
		"encoderSettings": map[string]any{"audioDescriptions": []any{}, "outputGroups": []any{}, "timecodeConfig": map[string]any{"source": "EMBEDDED"}, "videoDescriptions": []any{}},
		"tags":            map[string]any{"team": "a"}})
	expect(t, channel, 201, "")
	ch := member(t, channel.body, "channel")
	channelID, channelARN := str(ch["id"]), str(ch["arn"])
	if ch["state"] != "IDLE" || ch["pipelinesRunningCount"] != float64(0) && str(channelID) == "" || ch["channelClass"] != "STANDARD" {
		t.Fatalf("channel %v", ch)
	}
	if _, ok := ch["egressEndpoints"].([]any); !ok || len(ch["egressEndpoints"].([]any)) != 0 {
		t.Fatalf("channel reports endpoints %v", ch)
	}
	changed := h.rest("medialive", "PUT", "/prod/channels/"+channelID, map[string]any{"logLevel": "ERROR"})
	expect(t, changed, 200, "")
	if member(t, changed.body, "channel")["logLevel"] != "ERROR" {
		t.Fatalf("changed %v", changed.body)
	}
	expect(t, h.rest("medialive", "PUT", "/prod/channels/"+channelID, map[string]any{"inputAttachments": []any{}}), 400, "BadRequestException")
	expect(t, h.rest("medialive", "POST", "/prod/channels", map[string]any{"name": "keyed", "destinations": []any{map[string]any{"id": "d", "settings": []any{map[string]any{"streamName": "k"}}}}}), 400, "BadRequestException")
	expect(t, h.rest("medialive", "GET", "/prod/channels/"+channelID, nil), 200, "")
	if items := h.rest("medialive", "GET", "/prod/channels", nil).body["channels"].([]any); len(items) != 1 {
		t.Fatalf("channels %v", items)
	}

	escaped := escapeARN(channelARN)
	expect(t, h.rest("medialive", "POST", "/prod/tags/"+escaped, map[string]any{"tags": map[string]any{"env": "dev"}}), 204, "")
	if got := member(t, h.rest("medialive", "GET", "/prod/tags/"+escaped, nil).body, "tags"); len(got) != 2 || got["env"] != "dev" {
		t.Fatalf("tags %v", got)
	}
	expect(t, h.rest("medialive", "DELETE", "/prod/tags/"+escaped+"?tagKeys=env", nil), 204, "")
	expect(t, h.rest("medialive", "GET", "/prod/tags/"+escapeARN("arn:aws:medialive:us-east-1:000000000000:channel:none"), nil), 404, "NotFoundException")

	for _, path := range []string{"/prod/channels/" + channelID, "/prod/sdiSources/" + sdiID, "/prod/networks/" + networkID, "/prod/inputSecurityGroups/" + sgID} {
		status := 200
		if strings.Contains(path, "sdiSources") || strings.Contains(path, "networks") {
			status = 202
		}
		expect(t, h.rest("medialive", "DELETE", path, nil), status, "")
		expect(t, h.rest("medialive", "GET", path, nil), 404, "NotFoundException")
	}
	expect(t, h.rest("medialive", "POST", "/prod/channels/"+channelID+"/start", nil), 501, "UnknownOperationException")
	expect(t, h.rest("medialive", "POST", "/prod/inputs", map[string]any{"name": "i"}), 501, "UnknownOperationException")
}

func TestIVSRecordsKeepOfficialShapes(t *testing.T) {
	h := media2Harness(t)
	policy := h.rest("ivs", "POST", "/CreatePlaybackRestrictionPolicy", map[string]any{"name": "jp", "allowedCountries": []any{"JP"},
		"allowedOrigins": []any{"https://example.com"}, "tags": map[string]any{"team": "a"}})
	expect(t, policy, 200, "")
	p := member(t, policy.body, "playbackRestrictionPolicy")
	arn := str(p["arn"])
	if !strings.HasPrefix(arn, "arn:aws:ivs:us-east-1:000000000000:playback-restriction-policy/") || p["enableStrictOriginEnforcement"] != false {
		t.Fatalf("policy %v", p)
	}
	updated := h.rest("ivs", "POST", "/UpdatePlaybackRestrictionPolicy", map[string]any{"arn": arn, "allowedCountries": []any{"JP", "US"}, "enableStrictOriginEnforcement": true})
	expect(t, updated, 200, "")
	if got := member(t, updated.body, "playbackRestrictionPolicy"); len(got["allowedCountries"].([]any)) != 2 || got["enableStrictOriginEnforcement"] != true || got["name"] != "jp" {
		t.Fatalf("updated %v", got)
	}
	expect(t, h.rest("ivs", "POST", "/GetPlaybackRestrictionPolicy", map[string]any{"arn": arn}), 200, "")
	if items := h.rest("ivs", "POST", "/ListPlaybackRestrictionPolicies", map[string]any{}).body["playbackRestrictionPolicies"].([]any); len(items) != 1 {
		t.Fatalf("policies %v", items)
	}
	expect(t, h.rest("ivs", "POST", "/GetPlaybackRestrictionPolicy", map[string]any{"arn": "arn:aws:ivs:us-east-1:000000000000:playback-restriction-policy/missing"}), 404, "ResourceNotFoundException")

	recording := h.rest("ivs", "POST", "/CreateRecordingConfiguration", map[string]any{"name": "archive", "destinationConfiguration": map[string]any{"s3": map[string]any{"bucketName": "archive-bucket"}},
		"thumbnailConfiguration": map[string]any{"recordingMode": "INTERVAL", "targetIntervalSeconds": 30}})
	expect(t, recording, 200, "")
	r := member(t, recording.body, "recordingConfiguration")
	recordingARN := str(r["arn"])
	if r["state"] != "ACTIVE" || !strings.Contains(recordingARN, ":recording-configuration/") {
		t.Fatalf("recording %v", r)
	}
	expect(t, h.rest("ivs", "POST", "/CreateRecordingConfiguration", map[string]any{"destinationConfiguration": map[string]any{}}), 400, "ValidationException")
	expect(t, h.rest("ivs", "POST", "/GetRecordingConfiguration", map[string]any{"arn": recordingARN}), 200, "")
	if items := h.rest("ivs", "POST", "/ListRecordingConfigurations", map[string]any{}).body["recordingConfigurations"].([]any); len(items) != 1 {
		t.Fatalf("recordings %v", items)
	}
	expect(t, h.rest("ivs", "POST", "/DeleteRecordingConfiguration", map[string]any{"arn": recordingARN}), 204, "")
	expect(t, h.rest("ivs", "POST", "/DeletePlaybackRestrictionPolicy", map[string]any{"arn": arn}), 204, "")
	expect(t, h.rest("ivs", "POST", "/GetRecordingConfiguration", map[string]any{"arn": recordingARN}), 404, "ResourceNotFoundException")

	// No channel, stream key, key pair or tag operation is served.
	for _, path := range []string{"/CreateChannel", "/CreateStreamKey", "/ImportPlaybackKeyPair", "/ListStreams", "/PutMetadata", "/StopStream"} {
		expect(t, h.rest("ivs", "POST", path, map[string]any{}), 501, "UnknownOperationException")
	}
	expect(t, h.rest("ivs", "POST", "/tags/"+escapeARN(arn), map[string]any{"tags": map[string]any{"a": "b"}}), 501, "UnknownOperationException")
}

func TestIVSChatRecordsKeepOfficialShapes(t *testing.T) {
	h := media2Harness(t)
	logging := h.rest("ivschat", "POST", "/CreateLoggingConfiguration", map[string]any{"name": "audit", "destinationConfiguration": map[string]any{"cloudWatchLogs": map[string]any{"logGroupName": "/chat/audit"}}})
	expect(t, logging, 200, "")
	loggingARN := str(logging.body["arn"])
	if logging.body["state"] != "ACTIVE" || !strings.Contains(loggingARN, ":logging-configuration/") {
		t.Fatalf("logging %v", logging.body)
	}
	expect(t, h.rest("ivschat", "POST", "/CreateLoggingConfiguration", map[string]any{"destinationConfiguration": map[string]any{
		"s3": map[string]any{"bucketName": "chat-logs"}, "firehose": map[string]any{"deliveryStreamName": "chat"}}}), 400, "ValidationException")
	renamed := h.rest("ivschat", "POST", "/UpdateLoggingConfiguration", map[string]any{"identifier": loggingARN, "name": "audit-2", "destinationConfiguration": map[string]any{"s3": map[string]any{"bucketName": "chat-logs"}}})
	expect(t, renamed, 200, "")
	if renamed.body["name"] != "audit-2" || renamed.body["destinationConfiguration"].(map[string]any)["s3"] == nil {
		t.Fatalf("renamed %v", renamed.body)
	}
	expect(t, h.rest("ivschat", "POST", "/GetLoggingConfiguration", map[string]any{"identifier": loggingARN}), 200, "")
	if items := h.rest("ivschat", "POST", "/ListLoggingConfigurations", map[string]any{}).body["loggingConfigurations"].([]any); len(items) != 1 {
		t.Fatalf("logging configurations %v", items)
	}

	room := h.rest("ivschat", "POST", "/CreateRoom", map[string]any{"name": "lobby", "maximumMessageLength": 200, "loggingConfigurationIdentifiers": []any{loggingARN}, "tags": map[string]any{"team": "a"}})
	expect(t, room, 200, "")
	roomID := str(room.body["arn"])
	if room.body["maximumMessageRatePerSecond"] != float64(10) && room.body["maximumMessageRatePerSecond"] == nil || !strings.Contains(str(room.body["arn"]), ":room/") {
		t.Fatalf("room %v", room.body)
	}
	for key := range room.body {
		if strings.Contains(strings.ToLower(key), "token") {
			t.Fatalf("room carries a credential %s", key)
		}
	}
	updated := h.rest("ivschat", "POST", "/UpdateRoom", map[string]any{"identifier": roomID, "maximumMessageLength": 300, "messageReviewHandler": map[string]any{"fallbackResult": "ALLOW"}})
	expect(t, updated, 200, "")
	if updated.body["maximumMessageLength"] != float64(300) && updated.body["maximumMessageLength"] == nil || updated.body["name"] != "lobby" {
		t.Fatalf("updated %v", updated.body)
	}
	expect(t, h.rest("ivschat", "POST", "/GetRoom", map[string]any{"identifier": roomID}), 200, "")
	if items := h.rest("ivschat", "POST", "/ListRooms", map[string]any{"name": "lobby"}).body["rooms"].([]any); len(items) != 1 {
		t.Fatalf("rooms %v", items)
	}
	if items := h.rest("ivschat", "POST", "/ListRooms", map[string]any{"name": "other"}).body["rooms"].([]any); len(items) != 0 {
		t.Fatalf("filtered rooms %v", items)
	}
	expect(t, h.rest("ivschat", "POST", "/ListRooms", map[string]any{"loggingConfigurationIdentifier": loggingARN}), 400, "ValidationException")
	expect(t, h.rest("ivschat", "POST", "/GetRoom", map[string]any{"identifier": "arn:aws:ivschat:us-east-1:000000000000:room/missing"}), 404, "ResourceNotFoundException")
	expect(t, h.rest("ivschat", "POST", "/DeleteRoom", map[string]any{"identifier": roomID}), 204, "")
	expect(t, h.rest("ivschat", "POST", "/DeleteLoggingConfiguration", map[string]any{"identifier": loggingARN}), 204, "")
	for _, path := range []string{"/CreateChatToken", "/SendEvent", "/DeleteMessage", "/DisconnectUser"} {
		expect(t, h.rest("ivschat", "POST", path, map[string]any{}), 501, "UnknownOperationException")
	}
}

func TestIVSRealtimeRecordsKeepOfficialShapes(t *testing.T) {
	h := media2Harness(t)
	encoder := h.rest("ivs-realtime", "POST", "/CreateEncoderConfiguration", map[string]any{"name": "hd", "video": map[string]any{"bitrate": 2500000, "framerate": 30, "height": 720, "width": 1280}, "tags": map[string]any{"team": "a"}})
	expect(t, encoder, 200, "")
	encoderARN := str(member(t, encoder.body, "encoderConfiguration")["arn"])
	if got := member(t, h.rest("ivs-realtime", "POST", "/GetEncoderConfiguration", map[string]any{"arn": encoderARN}).body, "encoderConfiguration"); member(t, got, "video")["height"] != float64(720) && member(t, got, "video")["height"] == nil {
		t.Fatalf("encoder %v", got)
	}
	if items := h.rest("ivs-realtime", "POST", "/ListEncoderConfigurations", map[string]any{}).body["encoderConfigurations"].([]any); len(items) != 1 {
		t.Fatalf("encoders %v", items)
	}

	storage := h.rest("ivs-realtime", "POST", "/CreateStorageConfiguration", map[string]any{"name": "rec", "s3": map[string]any{"bucketName": "recordings"}})
	expect(t, storage, 200, "")
	storageARN := str(member(t, storage.body, "storageConfiguration")["arn"])
	expect(t, h.rest("ivs-realtime", "POST", "/GetStorageConfiguration", map[string]any{"arn": storageARN}), 200, "")
	if items := h.rest("ivs-realtime", "POST", "/ListStorageConfigurations", map[string]any{}).body["storageConfigurations"].([]any); len(items) != 1 {
		t.Fatalf("storage %v", items)
	}

	stage := h.rest("ivs-realtime", "POST", "/CreateStage", map[string]any{"name": "room", "tags": map[string]any{"team": "a"}})
	expect(t, stage, 200, "")
	s := member(t, stage.body, "stage")
	stageARN := str(s["arn"])
	if _, ok := stage.body["participantTokens"]; ok || s["endpoints"] != nil || s["activeSessionId"] != nil || !strings.Contains(stageARN, ":stage/") {
		t.Fatalf("stage %v", stage.body)
	}
	expect(t, h.rest("ivs-realtime", "POST", "/CreateStage", map[string]any{"participantTokenConfigurations": []any{map[string]any{"userId": "u"}}}), 400, "ValidationException")
	updated := h.rest("ivs-realtime", "POST", "/UpdateStage", map[string]any{"arn": stageARN, "name": "room-2", "autoParticipantRecordingConfiguration": map[string]any{"storageConfigurationArn": storageARN}})
	expect(t, updated, 200, "")
	if got := member(t, updated.body, "stage"); got["name"] != "room-2" || member(t, got, "autoParticipantRecordingConfiguration")["storageConfigurationArn"] != storageARN {
		t.Fatalf("updated %v", got)
	}
	expect(t, h.rest("ivs-realtime", "POST", "/GetStage", map[string]any{"arn": stageARN}), 200, "")
	if items := h.rest("ivs-realtime", "POST", "/ListStages", map[string]any{}).body["stages"].([]any); len(items) != 1 {
		t.Fatalf("stages %v", items)
	}
	expect(t, h.rest("ivs-realtime", "POST", "/GetStage", map[string]any{"arn": "arn:aws:ivs:us-east-1:000000000000:stage/missing"}), 404, "ResourceNotFoundException")
	for _, call := range [][2]string{{"/DeleteStage", stageARN}, {"/DeleteStorageConfiguration", storageARN}, {"/DeleteEncoderConfiguration", encoderARN}} {
		expect(t, h.rest("ivs-realtime", "POST", call[0], map[string]any{"arn": call[1]}), 200, "")
	}
	for _, path := range []string{"/CreateParticipantToken", "/CreateIngestConfiguration", "/ImportPublicKey", "/StartComposition", "/ListParticipants"} {
		expect(t, h.rest("ivs-realtime", "POST", path, map[string]any{}), 501, "UnknownOperationException")
	}
	// The ivs and ivs-realtime models share one signing name; requests route by path.
	expect(t, h.rest("ivs", "POST", "/ListStages", map[string]any{}), 200, "")
}

func escapeARN(arn string) string {
	return strings.NewReplacer(":", "%3A", "/", "%2F").Replace(arn)
}
