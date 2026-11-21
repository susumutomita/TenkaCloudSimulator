package catalog

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestNetwork2CheckHooksApplyDocumentedRules(t *testing.T) {
	number := func(v float64) any { return v }
	quantity := func(value float64, units string) map[string]any {
		return map[string]any{"value": number(value), "units": units}
	}
	downlink := func(freq, width map[string]any) map[string]any {
		return map[string]any{"configData": map[string]any{"antennaDownlinkConfig": map[string]any{
			"spectrumConfig": map[string]any{"centerFrequency": freq, "bandwidth": width}}}}
	}
	demod := func(freq, width map[string]any) map[string]any {
		return map[string]any{"configData": map[string]any{"antennaDownlinkDemodDecodeConfig": map[string]any{
			"spectrumConfig": map[string]any{"centerFrequency": freq, "bandwidth": width}}}}
	}
	uplink := func(freq, eirp map[string]any) map[string]any {
		return map[string]any{"configData": map[string]any{"antennaUplinkConfig": map[string]any{
			"spectrumConfig": map[string]any{"centerFrequency": freq}, "targetEirp": eirp}}}
	}
	echo := func(arn string) map[string]any {
		return map[string]any{"configData": map[string]any{"uplinkEchoConfig": map[string]any{"enabled": true, "antennaUplinkConfigArn": arn}}}
	}
	arn := func(kind string) string {
		return "arn:aws:groundstation:us-east-1:000000000000:config/" + kind + "/0f1e2d3c-aaaa-4bbb-8ccc-111122223333"
	}
	cases := []struct {
		name  string
		hook  func(map[string]any) error
		input map[string]any
		code  string
	}{
		{"asn default", directConnectASN, map[string]any{}, ""},
		{"asn low private", directConnectASN, map[string]any{"amazonSideAsn": int64(64512)}, ""},
		{"asn high private", directConnectASN, map[string]any{"amazonSideAsn": int64(4294967294)}, ""},
		{"asn public", directConnectASN, map[string]any{"amazonSideAsn": int64(7224)}, "DirectConnectClientException"},
		{"asn gap", directConnectASN, map[string]any{"amazonSideAsn": int64(65535)}, "DirectConnectClientException"},
		{"asn text", directConnectASN, map[string]any{"amazonSideAsn": "64512"}, "DirectConnectClientException"},
		{"no config data", groundStationConfig, map[string]any{}, "InvalidParameterException"},
		{"two members", groundStationConfig, map[string]any{"configData": map[string]any{"trackingConfig": map[string]any{}, "dataflowEndpointConfig": map[string]any{}}}, "InvalidParameterException"},
		{"tracking", groundStationConfig, map[string]any{"configData": map[string]any{"trackingConfig": map[string]any{"autotrack": "REQUIRED"}}}, ""},
		{"path type matches", groundStationConfig, map[string]any{"configType": "tracking", "configData": map[string]any{"trackingConfig": map[string]any{}}}, ""},
		{"path type differs", groundStationConfig, map[string]any{"configType": "s3-recording", "configData": map[string]any{"trackingConfig": map[string]any{}}}, "InvalidParameterException"},
		{"x band downlink", groundStationConfig, downlink(quantity(8000, "MHz"), quantity(40, "MHz")), ""},
		{"s band downlink in GHz", groundStationConfig, downlink(quantity(2.25, "GHz"), quantity(500, "kHz")), ""},
		{"downlink gap", groundStationConfig, downlink(quantity(5000, "MHz"), quantity(40, "MHz")), "InvalidParameterException"},
		{"downlink between bands", groundStationConfig, downlink(quantity(2400, "MHz"), quantity(40, "MHz")), "InvalidParameterException"},
		{"downlink above bands", groundStationConfig, downlink(quantity(8500, "MHz"), quantity(40, "MHz")), "InvalidParameterException"},
		{"downlink units", groundStationConfig, downlink(quantity(8000, "THz"), quantity(40, "MHz")), "InvalidParameterException"},
		{"downlink narrow", groundStationConfig, downlink(quantity(8000, "MHz"), quantity(5, "kHz")), "InvalidParameterException"},
		{"downlink wide", groundStationConfig, downlink(quantity(8000, "MHz"), quantity(60, "MHz")), "InvalidParameterException"},
		{"demod wide", groundStationConfig, demod(quantity(8000, "MHz"), quantity(600, "MHz")), ""},
		{"demod too wide", groundStationConfig, demod(quantity(8000, "MHz"), quantity(0.7, "GHz")), "InvalidParameterException"},
		{"demod narrow", groundStationConfig, demod(quantity(8000, "MHz"), quantity(100, "kHz")), "InvalidParameterException"},
		{"uplink", groundStationConfig, uplink(quantity(2100, "MHz"), quantity(30, "dBW")), ""},
		{"uplink frequency", groundStationConfig, uplink(quantity(2200, "MHz"), quantity(30, "dBW")), "InvalidParameterException"},
		{"uplink low eirp", groundStationConfig, uplink(quantity(2100, "MHz"), quantity(10, "dBW")), "InvalidParameterException"},
		{"uplink high eirp", groundStationConfig, uplink(quantity(2100, "MHz"), quantity(60, "dBW")), "InvalidParameterException"},
		{"uplink no eirp", groundStationConfig, map[string]any{"configData": map[string]any{"antennaUplinkConfig": map[string]any{"spectrumConfig": map[string]any{"centerFrequency": quantity(2100, "MHz")}}}}, "InvalidParameterException"},
		{"echo of uplink", groundStationConfig, echo(arn("antenna-uplink")), ""},
		{"echo of tracking", groundStationConfig, echo(arn("tracking")), "InvalidParameterException"},
		{"echo of text", groundStationConfig, echo("uplink"), "InvalidParameterException"},
		{"profile", groundStationMissionProfile, map[string]any{"trackingConfigArn": arn("tracking"), "dataflowEdges": []any{[]any{arn("antenna-downlink"), arn("dataflow-endpoint")}}}, ""},
		{"profile without edges", groundStationMissionProfile, map[string]any{}, ""},
		{"profile tracking type", groundStationMissionProfile, map[string]any{"trackingConfigArn": arn("antenna-downlink")}, "InvalidParameterException"},
		{"profile edge text", groundStationMissionProfile, map[string]any{"dataflowEdges": []any{[]any{arn("antenna-downlink"), "endpoint"}}}, "InvalidParameterException"},
		{"stream", kinesisVideoStream, map[string]any{"DataRetentionInHours": int64(24)}, ""},
		{"stream retention", kinesisVideoStream, map[string]any{"DataRetentionInHours": int64(87601)}, "InvalidArgumentException"},
		{"stream storage", kinesisVideoStream, map[string]any{"StreamStorageConfiguration": map[string]any{}}, "InvalidArgumentException"},
		{"channel", kinesisVideoChannel, map[string]any{"ChannelType": "SINGLE_MASTER"}, ""},
		{"default channel", kinesisVideoChannel, map[string]any{}, ""},
		{"full mesh", kinesisVideoChannel, map[string]any{"ChannelType": "FULL_MESH"}, "InvalidArgumentException"},
	}
	for _, c := range cases {
		err := c.hook(c.input)
		var f *fault
		if c.code == "" && err != nil || c.code != "" && (!errors.As(err, &f) || f.code != c.code) {
			t.Fatalf("%s: want %q, got %v", c.name, c.code, err)
		}
	}
}

func TestKinesisVideoVersionAndRetentionRules(t *testing.T) {
	var f *fault
	for _, c := range []struct {
		operation string
		change    int64
		want      int64
		code      string
	}{
		{"INCREASE_DATA_RETENTION", 5, 15, ""},
		{"DECREASE_DATA_RETENTION", 10, 0, ""},
		{"DECREASE_DATA_RETENTION", 11, 0, "InvalidArgumentException"},
		{"INCREASE_DATA_RETENTION", 87600, 0, "InvalidArgumentException"},
	} {
		doc := map[string]any{"Version": "v1", "DataRetentionInHours": json.Number("10")}
		err := kinesisVideoRetention(doc, map[string]any{"CurrentVersion": "v1", "Operation": c.operation, "DataRetentionChangeInHours": c.change})
		if c.code == "" && (err != nil || doc["DataRetentionInHours"] != c.want) || c.code != "" && (!errors.As(err, &f) || f.code != c.code) {
			t.Fatalf("%s %d: %v %v", c.operation, c.change, err, doc)
		}
	}
}

func TestDirectConnectGatewayLifecycle(t *testing.T) {
	h := newHarness(t, directConnect)
	created := h.json1("directconnect", "CreateDirectConnectGateway", map[string]any{"directConnectGatewayName": "core", "amazonSideAsn": 64600, "tags": []any{map[string]any{"key": "team", "value": "net"}}})
	expect(t, created, 200, "")
	gateway := created.body["directConnectGateway"].(map[string]any)
	id := str(gateway["directConnectGatewayId"])
	if len(id) != 36 || gateway["directConnectGatewayState"] != "pending" || gateway["ownerAccount"] != "000000000000" || gateway["amazonSideAsn"] != json.Number("64600") {
		t.Fatalf("gateway %v", gateway)
	}
	defaults := h.json1("directconnect", "CreateDirectConnectGateway", map[string]any{"directConnectGatewayName": "core"})
	expect(t, defaults, 200, "")
	if defaults.body["directConnectGateway"].(map[string]any)["amazonSideAsn"] != json.Number("64512") {
		t.Fatalf("default asn %v", defaults.body)
	}
	all := h.json1("directconnect", "DescribeDirectConnectGateways", map[string]any{})
	if got := all.body["directConnectGateways"].([]any); len(got) != 2 {
		t.Fatalf("both gateways listed: %v", all.body)
	}
	one := h.json1("directconnect", "DescribeDirectConnectGateways", map[string]any{"directConnectGatewayId": id})
	if got := one.body["directConnectGateways"].([]any); len(got) != 1 || got[0].(map[string]any)["tags"].([]any)[0].(map[string]any)["key"] != "team" {
		t.Fatalf("filtered gateway %v", one.body)
	}
	none := h.json1("directconnect", "DescribeDirectConnectGateways", map[string]any{"directConnectGatewayId": "missing"})
	if got := none.body["directConnectGateways"].([]any); len(got) != 0 {
		t.Fatalf("missing gateway is an empty list: %v", none.body)
	}
	renamed := h.json1("directconnect", "UpdateDirectConnectGateway", map[string]any{"directConnectGatewayId": id, "newDirectConnectGatewayName": "backbone"})
	expect(t, renamed, 200, "")
	if renamed.body["directConnectGateway"].(map[string]any)["directConnectGatewayName"] != "backbone" {
		t.Fatalf("rename %v", renamed.body)
	}
	arn := "arn:aws:directconnect::000000000000:dx-gateway/" + id
	expect(t, h.json1("directconnect", "TagResource", map[string]any{"resourceArn": arn, "tags": []any{map[string]any{"key": "env", "value": "lab"}}}), 200, "")
	expect(t, h.json1("directconnect", "UntagResource", map[string]any{"resourceArn": arn, "tagKeys": []any{"team"}}), 200, "")
	tags := h.json1("directconnect", "DescribeDirectConnectGateways", map[string]any{"directConnectGatewayId": id}).body["directConnectGateways"].([]any)[0].(map[string]any)["tags"].([]any)
	if len(tags) != 1 || tags[0].(map[string]any)["key"] != "env" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.json1("directconnect", "TagResource", map[string]any{"resourceArn": arn + "x", "tags": []any{map[string]any{"key": "a", "value": "b"}}}), 400, "DirectConnectClientException")
	deleted := h.json1("directconnect", "DeleteDirectConnectGateway", map[string]any{"directConnectGatewayId": id})
	expect(t, deleted, 200, "")
	if deleted.body["directConnectGateway"].(map[string]any)["directConnectGatewayState"] != "deleted" {
		t.Fatalf("delete %v", deleted.body)
	}
	expect(t, h.json1("directconnect", "DeleteDirectConnectGateway", map[string]any{"directConnectGatewayId": id}), 400, "DirectConnectClientException")
	expect(t, h.json1("directconnect", "UpdateDirectConnectGateway", map[string]any{"directConnectGatewayId": id, "newDirectConnectGatewayName": "x"}), 400, "DirectConnectClientException")
}

func TestDirectConnectFaultsAndUnboundOperations(t *testing.T) {
	h := newHarness(t, directConnect)
	expect(t, h.json1("directconnect", "CreateDirectConnectGateway", map[string]any{"directConnectGatewayName": "x", "amazonSideAsn": 100}), 400, "DirectConnectClientException")
	expect(t, h.json1("directconnect", "CreateDirectConnectGateway", map[string]any{}), 400, "")
	for i := 0; i < 50; i++ {
		expect(t, h.json1("directconnect", "CreateDirectConnectGateway", map[string]any{"directConnectGatewayName": "g"}), 200, "")
	}
	expect(t, h.json1("directconnect", "CreateDirectConnectGateway", map[string]any{"directConnectGatewayName": "g"}), 400, "DirectConnectClientException")
	for _, op := range []string{"DescribeTags", "CreateDirectConnectGatewayAssociation", "DescribeConnections", "CreateConnection", "CreatePublicVirtualInterface"} {
		expect(t, h.json1("directconnect", op, map[string]any{}), 501, "UnknownOperationException")
	}
}

func TestGroundStationConfigAndMissionProfileLifecycle(t *testing.T) {
	h := newHarness(t, groundStation)
	downlink := map[string]any{"antennaDownlinkConfig": map[string]any{"spectrumConfig": map[string]any{
		"centerFrequency": map[string]any{"value": 8000, "units": "MHz"}, "bandwidth": map[string]any{"value": 40, "units": "MHz"}, "polarization": "RIGHT_HAND"}}}
	created := h.rest("groundstation", "POST", "/config", map[string]any{"name": "x-band", "configData": downlink, "tags": map[string]any{"team": "space"}})
	expect(t, created, 200, "")
	id, configArn := str(created.body["configId"]), str(created.body["configArn"])
	if created.body["configType"] != "antenna-downlink" || len(id) != 36 || configArn != "arn:aws:groundstation:us-east-1:000000000000:config/antenna-downlink/"+id {
		t.Fatalf("created config %v", created.body)
	}
	path := "/config/antenna-downlink/" + id
	got := h.rest("groundstation", "GET", path, nil)
	expect(t, got, 200, "")
	if got.body["name"] != "x-band" || got.body["configArn"] != configArn || got.body["tags"].(map[string]any)["team"] != "space" ||
		got.body["configData"].(map[string]any)["antennaDownlinkConfig"] == nil {
		t.Fatalf("config %v", got.body)
	}
	tracking := h.rest("groundstation", "POST", "/config", map[string]any{"name": "autotrack", "configData": map[string]any{"trackingConfig": map[string]any{"autotrack": "PREFERRED"}}})
	expect(t, tracking, 200, "")
	trackingArn := str(tracking.body["configArn"])
	listed := h.rest("groundstation", "GET", "/config", nil)
	items := listed.body["configList"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["name"] != "x-band" || items[1].(map[string]any)["configType"] != "tracking" {
		t.Fatalf("config list %v", listed.body)
	}
	updated := downlink["antennaDownlinkConfig"].(map[string]any)
	updated["spectrumConfig"].(map[string]any)["bandwidth"] = map[string]any{"value": 20, "units": "MHz"}
	expect(t, h.rest("groundstation", "PUT", path, map[string]any{"name": "x-band-narrow", "configData": downlink}), 200, "")
	got = h.rest("groundstation", "GET", path, nil)
	if got.body["name"] != "x-band-narrow" || got.body["configData"].(map[string]any)["antennaDownlinkConfig"].(map[string]any)["spectrumConfig"].(map[string]any)["bandwidth"].(map[string]any)["value"] != json.Number("20") {
		t.Fatalf("updated config %v", got.body)
	}
	expect(t, h.rest("groundstation", "PUT", "/config/tracking/"+id, map[string]any{"name": "x", "configData": map[string]any{"trackingConfig": map[string]any{"autotrack": "REQUIRED"}}}), 431, "InvalidParameterException")
	expect(t, h.rest("groundstation", "PUT", "/config/tracking/"+id, map[string]any{"name": "x", "configData": downlink}), 431, "InvalidParameterException")

	profile := h.rest("groundstation", "POST", "/missionprofile", map[string]any{"name": "pass", "minimumViableContactDurationSeconds": 180, "trackingConfigArn": trackingArn,
		"dataflowEdges": []any{[]any{configArn, trackingArn}}, "contactPrePassDurationSeconds": 120, "tags": map[string]any{"env": "lab"}})
	expect(t, profile, 200, "")
	profileID := str(profile.body["missionProfileId"])
	profileArn := "arn:aws:groundstation:us-east-1:000000000000:mission-profile/" + profileID
	shown := h.rest("groundstation", "GET", "/missionprofile/"+profileID, nil)
	if shown.body["missionProfileArn"] != profileArn || shown.body["region"] != "us-east-1" || shown.body["minimumViableContactDurationSeconds"] != json.Number("180") ||
		shown.body["trackingConfigArn"] != trackingArn || len(shown.body["dataflowEdges"].([]any)) != 1 {
		t.Fatalf("mission profile %v", shown.body)
	}
	expect(t, h.rest("groundstation", "PUT", "/missionprofile/"+profileID, map[string]any{"name": "pass-long", "minimumViableContactDurationSeconds": 240}), 200, "")
	listedProfiles := h.rest("groundstation", "GET", "/missionprofile", nil).body["missionProfileList"].([]any)
	if len(listedProfiles) != 1 || listedProfiles[0].(map[string]any)["name"] != "pass-long" || listedProfiles[0].(map[string]any)["region"] != "us-east-1" {
		t.Fatalf("profile list %v", listedProfiles)
	}

	tagPath := "/tags/" + url.PathEscape(profileArn)
	expect(t, h.rest("groundstation", "POST", tagPath, map[string]any{"tags": map[string]any{"owner": "ops"}}), 200, "")
	expect(t, h.rest("groundstation", "DELETE", tagPath+"?tagKeys=env", nil), 200, "")
	tags := h.rest("groundstation", "GET", tagPath, nil)
	if got := tags.body["tags"].(map[string]any); len(got) != 1 || got["owner"] != "ops" {
		t.Fatalf("tags %v", tags.body)
	}
	expect(t, h.rest("groundstation", "GET", "/tags/"+url.PathEscape(profileArn+"x"), nil), 434, "ResourceNotFoundException")

	// The path type must be the stored one, and configs in use cannot be deleted.
	expect(t, h.rest("groundstation", "GET", "/config/tracking/"+id, nil), 434, "ResourceNotFoundException")
	expect(t, h.rest("groundstation", "DELETE", "/config/tracking/"+id, nil), 434, "ResourceNotFoundException")
	expect(t, h.rest("groundstation", "DELETE", path, nil), 431, "InvalidParameterException")
	expect(t, h.rest("groundstation", "GET", path, nil), 200, "")
	gone := strings.Replace(configArn, id, "0f1e2d3c-aaaa-4bbb-8ccc-111122223333", 1)
	expect(t, h.rest("groundstation", "POST", "/missionprofile", map[string]any{"name": "dangling", "minimumViableContactDurationSeconds": 180, "trackingConfigArn": trackingArn,
		"dataflowEdges": []any{[]any{gone, trackingArn}}}), 434, "ResourceNotFoundException")
	expect(t, h.rest("groundstation", "DELETE", "/missionprofile/"+profileID, nil), 200, "")
	expect(t, h.rest("groundstation", "GET", "/missionprofile/"+profileID, nil), 434, "ResourceNotFoundException")
	expect(t, h.rest("groundstation", "DELETE", path, nil), 200, "")
	expect(t, h.rest("groundstation", "GET", path, nil), 434, "ResourceNotFoundException")
}

func TestGroundStationFaultsAndUnboundOperations(t *testing.T) {
	h := newHarness(t, groundStation)
	tracking := map[string]any{"trackingConfig": map[string]any{"autotrack": "REQUIRED"}}
	expect(t, h.rest("groundstation", "POST", "/config", map[string]any{"name": "t", "configData": map[string]any{}}), 431, "InvalidParameterException")
	expect(t, h.rest("groundstation", "POST", "/config", map[string]any{"name": "bad name!", "configData": tracking}), 431, "InvalidParameterException")
	created := h.rest("groundstation", "POST", "/config", map[string]any{"name": "t", "configData": tracking})
	expect(t, created, 200, "")
	trackingArn := str(created.body["configArn"])
	base := map[string]any{"name": "p", "minimumViableContactDurationSeconds": 60, "dataflowEdges": []any{}}
	profile := func(arn string) map[string]any {
		out := map[string]any{"trackingConfigArn": arn}
		for k, v := range base {
			out[k] = v
		}
		return out
	}
	expect(t, h.rest("groundstation", "POST", "/missionprofile", profile("arn:aws:groundstation:us-east-1:000000000000:config/antenna-downlink/0f1e2d3c-aaaa-4bbb-8ccc-111122223333")), 431, "InvalidParameterException")
	expect(t, h.rest("groundstation", "POST", "/missionprofile", profile("arn:aws:groundstation:us-east-1:000000000000:config/tracking/0f1e2d3c-aaaa-4bbb-8ccc-111122223333")), 434, "ResourceNotFoundException")
	tooLong := profile(trackingArn)
	tooLong["minimumViableContactDurationSeconds"] = 30000
	expect(t, h.rest("groundstation", "POST", "/missionprofile", tooLong), 431, "InvalidParameterException")
	expect(t, h.rest("groundstation", "GET", "/missionprofile/0f1e2d3c-aaaa-4bbb-8ccc-111122223333", nil), 434, "ResourceNotFoundException")
	for i := 0; i < 99; i++ {
		expect(t, h.rest("groundstation", "POST", "/config", map[string]any{"name": "t", "configData": tracking}), 200, "")
	}
	expect(t, h.rest("groundstation", "POST", "/config", map[string]any{"name": "t", "configData": tracking}), 429, "ResourceLimitExceededException")
	expect(t, h.rest("groundstation", "POST", "/contact", map[string]any{}), 501, "UnknownOperationException")
	expect(t, h.rest("groundstation", "POST", "/dataflowEndpointGroup", map[string]any{}), 501, "UnknownOperationException")
	expect(t, h.rest("groundstation", "POST", "/ephemeris", map[string]any{}), 501, "UnknownOperationException")
}

func TestKinesisVideoStreamLifecycle(t *testing.T) {
	h := newHarness(t, kinesisVideo)
	created := h.rest("kinesisvideo", "POST", "/createStream", map[string]any{"StreamName": "front-door", "DeviceName": "cam-1", "MediaType": "video/h264", "DataRetentionInHours": 24, "Tags": map[string]any{"site": "hq"}})
	expect(t, created, 200, "")
	arn := str(created.body["StreamARN"])
	if arn != "arn:aws:kinesisvideo:us-east-1:000000000000:stream/front-door/1" {
		t.Fatalf("stream ARN %q", arn)
	}
	for _, request := range []map[string]any{{"StreamName": "front-door"}, {"StreamARN": arn}} {
		info := h.rest("kinesisvideo", "POST", "/describeStream", request).body["StreamInfo"].(map[string]any)
		if info["Status"] != "CREATING" || info["DataRetentionInHours"] != json.Number("24") || info["DeviceName"] != "cam-1" || info["MediaType"] != "video/h264" ||
			info["KmsKeyId"] != "alias/aws/kinesisvideo" || len(str(info["Version"])) != 20 || info["StreamARN"] != arn || info["CreationTime"] == nil {
			t.Fatalf("stream info %v", info)
		}
	}
	plain := h.rest("kinesisvideo", "POST", "/createStream", map[string]any{"StreamName": "lobby", "KmsKeyId": "alias/mine"})
	expect(t, plain, 200, "")
	lobby := h.rest("kinesisvideo", "POST", "/describeStream", map[string]any{"StreamName": "lobby"}).body["StreamInfo"].(map[string]any)
	if lobby["DataRetentionInHours"] != json.Number("0") || lobby["KmsKeyId"] != "alias/mine" {
		t.Fatalf("defaults %v", lobby)
	}
	listed := h.rest("kinesisvideo", "POST", "/listStreams", map[string]any{})
	if got := listed.body["StreamInfoList"].([]any); len(got) != 2 {
		t.Fatalf("streams %v", listed.body)
	}
	version := str(h.rest("kinesisvideo", "POST", "/describeStream", map[string]any{"StreamName": "front-door"}).body["StreamInfo"].(map[string]any)["Version"])

	expect(t, h.rest("kinesisvideo", "POST", "/updateStream", map[string]any{"StreamName": "front-door", "CurrentVersion": "stale", "MediaType": "video/h265"}), 400, "VersionMismatchException")
	expect(t, h.rest("kinesisvideo", "POST", "/updateStream", map[string]any{"StreamARN": arn, "CurrentVersion": version, "MediaType": "video/h265", "DeviceName": "cam-2"}), 200, "")
	info := h.rest("kinesisvideo", "POST", "/describeStream", map[string]any{"StreamName": "front-door"}).body["StreamInfo"].(map[string]any)
	next := str(info["Version"])
	if info["MediaType"] != "video/h265" || info["DeviceName"] != "cam-2" || next == version {
		t.Fatalf("updated stream %v", info)
	}
	expect(t, h.rest("kinesisvideo", "POST", "/updateStream", map[string]any{"StreamName": "front-door", "CurrentVersion": version}), 400, "VersionMismatchException")

	expect(t, h.rest("kinesisvideo", "POST", "/updateDataRetention", map[string]any{"StreamName": "front-door", "CurrentVersion": next, "Operation": "DECREASE_DATA_RETENTION", "DataRetentionChangeInHours": 30}), 400, "InvalidArgumentException")
	expect(t, h.rest("kinesisvideo", "POST", "/updateDataRetention", map[string]any{"StreamName": "front-door", "CurrentVersion": next, "Operation": "DECREASE_DATA_RETENTION", "DataRetentionChangeInHours": 4}), 200, "")
	info = h.rest("kinesisvideo", "POST", "/describeStream", map[string]any{"StreamName": "front-door"}).body["StreamInfo"].(map[string]any)
	if info["DataRetentionInHours"] != json.Number("20") || info["Version"] == next {
		t.Fatalf("retention %v", info)
	}

	expect(t, h.rest("kinesisvideo", "POST", "/TagResource", map[string]any{"ResourceARN": arn, "Tags": []any{map[string]any{"Key": "env", "Value": "lab"}}}), 200, "")
	expect(t, h.rest("kinesisvideo", "POST", "/UntagResource", map[string]any{"ResourceARN": arn, "TagKeyList": []any{"site"}}), 200, "")
	tags := h.rest("kinesisvideo", "POST", "/ListTagsForResource", map[string]any{"ResourceARN": arn}).body["Tags"].(map[string]any)
	if len(tags) != 1 || tags["env"] != "lab" {
		t.Fatalf("tags %v", tags)
	}

	expect(t, h.rest("kinesisvideo", "POST", "/createStream", map[string]any{"StreamName": "front-door"}), 400, "ResourceInUseException")
	expect(t, h.rest("kinesisvideo", "POST", "/listStreams", map[string]any{"StreamNameCondition": map[string]any{"ComparisonOperator": "BEGINS_WITH", "ComparisonValue": "front"}}), 400, "InvalidArgumentException")
	expect(t, h.rest("kinesisvideo", "POST", "/deleteStream", map[string]any{"StreamARN": arn, "CurrentVersion": "x"}), 400, "VersionMismatchException")
	if h.rest("kinesisvideo", "POST", "/describeStream", map[string]any{"StreamARN": arn}).status != 200 {
		t.Fatal("a stale delete removed the stream")
	}
	expect(t, h.rest("kinesisvideo", "POST", "/deleteStream", map[string]any{"StreamARN": arn}), 200, "")
	expect(t, h.rest("kinesisvideo", "POST", "/describeStream", map[string]any{"StreamName": "front-door"}), 404, "ResourceNotFoundException")
	expect(t, h.rest("kinesisvideo", "POST", "/deleteStream", map[string]any{"StreamARN": arn}), 404, "ResourceNotFoundException")
	expect(t, h.rest("kinesisvideo", "POST", "/ListTagsForResource", map[string]any{"ResourceARN": arn}), 404, "ResourceNotFoundException")
	for i := 0; i < 49; i++ {
		expect(t, h.rest("kinesisvideo", "POST", "/createStream", map[string]any{"StreamName": "s" + strings.Repeat("a", i+1)}), 200, "")
	}
	expect(t, h.rest("kinesisvideo", "POST", "/createStream", map[string]any{"StreamName": "overflow"}), 400, "AccountStreamLimitExceededException")
}

func TestKinesisVideoSignalingChannelLifecycle(t *testing.T) {
	h := newHarness(t, kinesisVideo)
	created := h.rest("kinesisvideo", "POST", "/createSignalingChannel", map[string]any{"ChannelName": "doorbell", "Tags": []any{map[string]any{"Key": "site", "Value": "hq"}}})
	expect(t, created, 200, "")
	arn := str(created.body["ChannelARN"])
	if arn != "arn:aws:kinesisvideo:us-east-1:000000000000:channel/doorbell/1" {
		t.Fatalf("channel ARN %q", arn)
	}
	info := h.rest("kinesisvideo", "POST", "/describeSignalingChannel", map[string]any{"ChannelName": "doorbell"}).body["ChannelInfo"].(map[string]any)
	if info["ChannelType"] != "SINGLE_MASTER" || info["ChannelStatus"] != "CREATING" || info["SingleMasterConfiguration"].(map[string]any)["MessageTtlSeconds"] != json.Number("60") || len(str(info["Version"])) != 20 {
		t.Fatalf("channel info %v", info)
	}
	version := str(info["Version"])
	expect(t, h.rest("kinesisvideo", "POST", "/updateSignalingChannel", map[string]any{"ChannelARN": arn, "CurrentVersion": "stale", "SingleMasterConfiguration": map[string]any{"MessageTtlSeconds": 90}}), 400, "VersionMismatchException")
	expect(t, h.rest("kinesisvideo", "POST", "/updateSignalingChannel", map[string]any{"ChannelARN": arn, "CurrentVersion": version, "SingleMasterConfiguration": map[string]any{"MessageTtlSeconds": 90}}), 200, "")
	info = h.rest("kinesisvideo", "POST", "/describeSignalingChannel", map[string]any{"ChannelARN": arn}).body["ChannelInfo"].(map[string]any)
	if info["SingleMasterConfiguration"].(map[string]any)["MessageTtlSeconds"] != json.Number("90") || info["Version"] == version {
		t.Fatalf("updated channel %v", info)
	}
	listed := h.rest("kinesisvideo", "POST", "/listSignalingChannels", map[string]any{}).body["ChannelInfoList"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["ChannelName"] != "doorbell" {
		t.Fatalf("channels %v", listed)
	}
	expect(t, h.rest("kinesisvideo", "POST", "/TagResource", map[string]any{"ResourceARN": arn, "Tags": []any{map[string]any{"Key": "env", "Value": "lab"}}}), 200, "")
	tags := h.rest("kinesisvideo", "POST", "/ListTagsForResource", map[string]any{"ResourceARN": arn}).body["Tags"].(map[string]any)
	if len(tags) != 2 || tags["site"] != "hq" {
		t.Fatalf("channel tags %v", tags)
	}
	expect(t, h.rest("kinesisvideo", "POST", "/createSignalingChannel", map[string]any{"ChannelName": "doorbell"}), 400, "ResourceInUseException")
	expect(t, h.rest("kinesisvideo", "POST", "/createSignalingChannel", map[string]any{"ChannelName": "mesh", "ChannelType": "FULL_MESH"}), 400, "InvalidArgumentException")
	expect(t, h.rest("kinesisvideo", "POST", "/createSignalingChannel", map[string]any{"ChannelName": "bad name"}), 400, "InvalidArgumentException")
	expect(t, h.rest("kinesisvideo", "POST", "/deleteSignalingChannel", map[string]any{"ChannelARN": arn, "CurrentVersion": version}), 400, "VersionMismatchException")
	current := str(h.rest("kinesisvideo", "POST", "/describeSignalingChannel", map[string]any{"ChannelARN": arn}).body["ChannelInfo"].(map[string]any)["Version"])
	expect(t, h.rest("kinesisvideo", "POST", "/describeSignalingChannel", map[string]any{"ChannelARN": arn}), 200, "")
	expect(t, h.rest("kinesisvideo", "POST", "/deleteSignalingChannel", map[string]any{"ChannelARN": arn, "CurrentVersion": current}), 200, "")
	expect(t, h.rest("kinesisvideo", "POST", "/createSignalingChannel", map[string]any{"ChannelName": "doorbell"}), 200, "")
	arn = str(h.rest("kinesisvideo", "POST", "/describeSignalingChannel", map[string]any{"ChannelName": "doorbell"}).body["ChannelInfo"].(map[string]any)["ChannelARN"])
	expect(t, h.rest("kinesisvideo", "POST", "/deleteSignalingChannel", map[string]any{"ChannelARN": arn}), 200, "")
	expect(t, h.rest("kinesisvideo", "POST", "/describeSignalingChannel", map[string]any{"ChannelName": "doorbell"}), 404, "ResourceNotFoundException")
	for i := 0; i < 50; i++ {
		expect(t, h.rest("kinesisvideo", "POST", "/createSignalingChannel", map[string]any{"ChannelName": "c" + strings.Repeat("a", i+1)}), 200, "")
	}
	expect(t, h.rest("kinesisvideo", "POST", "/createSignalingChannel", map[string]any{"ChannelName": "overflow"}), 400, "ClientLimitExceededException")
}

func TestKinesisVideoUnboundOperationsReturn501(t *testing.T) {
	h := newHarness(t, kinesisVideo)
	for _, path := range []string{"/getDataEndpoint", "/getSignalingChannelEndpoint", "/tagStream", "/untagStream", "/listTagsForStream",
		"/updateStreamStorageConfiguration", "/describeNotificationConfiguration", "/updateImageGenerationConfiguration"} {
		expect(t, h.rest("kinesisvideo", "POST", path, map[string]any{}), 501, "UnknownOperationException")
	}
}
