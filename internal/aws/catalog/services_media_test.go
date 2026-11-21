package catalog

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestMediaCheckHooksApplyDocumentedRules(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	playlist := func(settings, member string, value int64) map[string]any {
		return map[string]any{"Outputs": []any{map[string]any{settings: map[string]any{member: value}}}}
	}
	secret := "arn:aws:secretsmanager:us-east-1:000000000000:secret:cdn-AbCdEf"
	role := "arn:aws:iam::000000000000:role/packager"
	cases := []struct {
		name  string
		hook  func(map[string]any) error
		input map[string]any
		code  string
	}{
		{"queue", mediaConvertQueue, map[string]any{"Name": "q"}, ""},
		{"reserved queue", mediaConvertQueue, map[string]any{"PricingPlan": "RESERVED"}, "BadRequestException"},
		{"reservation settings", mediaConvertQueue, map[string]any{"ReservationPlanSettings": map[string]any{}}, "BadRequestException"},
		{"queue tags", mediaConvertQueue, map[string]any{"Tags": map[string]any{"a": "b"}}, "BadRequestException"},
		{"empty tags", mediaConvertUntagged, map[string]any{"Tags": map[string]any{}}, ""},
		{"channel", mediaPackageV2Channel, map[string]any{}, ""},
		{"cmaf switching", mediaPackageV2Channel, map[string]any{"InputType": "CMAF", "InputSwitchConfiguration": map[string]any{"MQCSInputSwitching": true}}, ""},
		{"hls switching", mediaPackageV2Channel, map[string]any{"InputType": "HLS", "InputSwitchConfiguration": map[string]any{"MQCSInputSwitching": true}}, "ValidationException"},
		{"default input publishing", mediaPackageV2Channel, map[string]any{"OutputHeaderConfiguration": map[string]any{"PublishMQCS": true}}, "ValidationException"},
		{"switching off", mediaPackageV2Channel, map[string]any{"InputSwitchConfiguration": map[string]any{"MQCSInputSwitching": false}}, ""},
		{"no authorization", mediaPackageVODAuthorization, map[string]any{}, ""},
		{"authorization", mediaPackageVODAuthorization, map[string]any{"Authorization": map[string]any{"CdnIdentifierSecret": secret, "SecretsRoleArn": role}}, ""},
		{"not a secret", mediaPackageVODAuthorization, map[string]any{"Authorization": map[string]any{"CdnIdentifierSecret": "secret", "SecretsRoleArn": role}}, "ValidationException"},
		{"not a secret arn", mediaPackageVODAuthorization, map[string]any{"Authorization": map[string]any{"CdnIdentifierSecret": "arn:aws:secretsmanager:us-east-1:000000000000:other", "SecretsRoleArn": role}}, "ValidationException"},
		{"not a role", mediaPackageVODAuthorization, map[string]any{"Authorization": map[string]any{"CdnIdentifierSecret": secret, "SecretsRoleArn": "role"}}, "ValidationException"},
		{"playback", mediaTailorPlayback, map[string]any{"AdDecisionServerUrl": "https://ads.example.com/vast", "VideoContentSourceUrl": long(512)}, ""},
		{"long ads url", mediaTailorPlayback, map[string]any{"AdDecisionServerUrl": long(25001)}, "ValidationException"},
		{"long pre-roll url", mediaTailorPlayback, map[string]any{"LivePreRollConfiguration": map[string]any{"AdDecisionServerUrl": long(25001)}}, "ValidationException"},
		{"short pre-roll url", mediaTailorPlayback, map[string]any{"LivePreRollConfiguration": map[string]any{"AdDecisionServerUrl": "https://ads.example.com"}}, ""},
		{"long content url", mediaTailorPlayback, map[string]any{"VideoContentSourceUrl": long(513)}, "ValidationException"},
		{"single prefetch", mediaTailorPrefetch, map[string]any{}, ""},
		{"recurring prefetch", mediaTailorPrefetch, map[string]any{"ScheduleType": "RECURRING", "RecurringPrefetchConfiguration": map[string]any{}}, ""},
		{"recurring without settings", mediaTailorPrefetch, map[string]any{"ScheduleType": "RECURRING"}, "ValidationException"},
		{"source location", mediaTailorSourceLocation, map[string]any{"HttpConfiguration": map[string]any{"BaseUrl": "https://origin.example.com"},
			"DefaultSegmentDeliveryConfiguration": map[string]any{"BaseUrl": "https://cdn.example.com"},
			"SegmentDeliveryConfigurations":       []any{map[string]any{"Name": "a", "BaseUrl": "/relative"}, map[string]any{"Name": "b", "BaseUrl": "http://cdn.example.com"}, map[string]any{}}}, ""},
		{"base url without protocol", mediaTailorSourceLocation, map[string]any{"HttpConfiguration": map[string]any{"BaseUrl": "origin.example.com"}}, "ValidationException"},
		{"relative default", mediaTailorSourceLocation, map[string]any{"HttpConfiguration": map[string]any{"BaseUrl": "https://o"}, "DefaultSegmentDeliveryConfiguration": map[string]any{"BaseUrl": "/relative"}}, "ValidationException"},
		{"default without url", mediaTailorSourceLocation, map[string]any{"DefaultSegmentDeliveryConfiguration": map[string]any{}}, ""},
		{"bad delivery url", mediaTailorSourceLocation, map[string]any{"SegmentDeliveryConfigurations": []any{map[string]any{"BaseUrl": "cdn"}}}, "ValidationException"},
		{"duplicate delivery name", mediaTailorSourceLocation, map[string]any{"SegmentDeliveryConfigurations": []any{map[string]any{"Name": "a"}, map[string]any{"Name": "a"}}}, "ValidationException"},
		{"window", mediaTailorChannel, playlist("HlsPlaylistSettings", "ManifestWindowSeconds", 30), ""},
		{"short window", mediaTailorChannel, playlist("HlsPlaylistSettings", "ManifestWindowSeconds", 29), "ValidationException"},
		{"long window", mediaTailorChannel, playlist("DashPlaylistSettings", "ManifestWindowSeconds", 3601), "ValidationException"},
		{"buffer", mediaTailorChannel, playlist("DashPlaylistSettings", "MinBufferTimeSeconds", 1), "ValidationException"},
		{"update period", mediaTailorChannel, playlist("DashPlaylistSettings", "MinUpdatePeriodSeconds", 61), "ValidationException"},
		{"presentation delay", mediaTailorChannel, playlist("DashPlaylistSettings", "SuggestedPresentationDelaySeconds", 60), ""},
		{"time shift", mediaTailorChannel, map[string]any{"TimeShiftConfiguration": map[string]any{"MaxTimeDelaySeconds": int64(21600)}}, ""},
		{"long time shift", mediaTailorChannel, map[string]any{"TimeShiftConfiguration": map[string]any{"MaxTimeDelaySeconds": int64(21601)}}, "ValidationException"},
		{"update without mode", mediaTailorChannel, map[string]any{"FillerSlate": map[string]any{}}, ""},
		{"linear slate", mediaTailorChannel, map[string]any{"PlaybackMode": "LINEAR", "FillerSlate": map[string]any{}}, ""},
		{"linear without slate", mediaTailorChannel, map[string]any{"PlaybackMode": "LINEAR"}, "ValidationException"},
		{"loop slate", mediaTailorChannel, map[string]any{"PlaybackMode": "LOOP", "FillerSlate": map[string]any{}}, "ValidationException"},
		{"loop", mediaTailorSlate, map[string]any{"PlaybackMode": "LOOP"}, ""},
	}
	for _, c := range cases {
		err := c.hook(c.input)
		if c.code == "" {
			if err != nil {
				t.Errorf("%s: unexpected %v", c.name, err)
			}
			continue
		}
		f, ok := err.(*fault)
		if !ok || f.code != c.code {
			t.Errorf("%s: want %s, got %v", c.name, c.code, err)
		}
	}
}

func TestMediaConvertQueuesTemplatesPresetsAndPolicy(t *testing.T) {
	h := newHarness(t, mediaConvert)
	const base = "/2017-08-29"
	queue := h.rest("mediaconvert", "POST", base+"/queues", map[string]any{"name": "batch", "description": "nightly", "concurrentJobs": 2})
	expect(t, queue, 201, "")
	q := queue.body["queue"].(map[string]any)
	arn := str(q["arn"])
	if arn != "arn:aws:mediaconvert:us-east-1:000000000000:queues/batch" || q["status"] != "ACTIVE" || q["pricingPlan"] != "ON_DEMAND" || q["type"] != "CUSTOM" ||
		q["submittedJobsCount"] != json.Number("0") || q["description"] != "nightly" || q["createdAt"] == nil {
		t.Fatalf("queue %v", q)
	}
	expect(t, h.rest("mediaconvert", "POST", base+"/queues", map[string]any{"name": "batch"}), 409, "ConflictException")
	expect(t, h.rest("mediaconvert", "POST", base+"/queues", map[string]any{"name": "reserved", "pricingPlan": "RESERVED"}), 400, "BadRequestException")
	expect(t, h.rest("mediaconvert", "POST", base+"/queues", map[string]any{"name": "tagged", "tags": map[string]any{"a": "b"}}), 400, "BadRequestException")
	expect(t, h.rest("mediaconvert", "GET", base+"/queues/missing", nil), 404, "NotFoundException")
	expect(t, h.rest("mediaconvert", "GET", base+"/queues?order=ASCENDING", nil), 400, "BadRequestException")
	if got := h.rest("mediaconvert", "GET", base+"/queues/batch", nil).body["queue"].(map[string]any); got["name"] != "batch" {
		t.Fatalf("get %v", got)
	}
	if got := h.rest("mediaconvert", "GET", base+"/queues", nil).body["queues"].([]any); len(got) != 1 {
		t.Fatalf("list %v", got)
	}
	updated := h.rest("mediaconvert", "PUT", base+"/queues/batch", map[string]any{"status": "PAUSED", "concurrentJobs": 4})
	expect(t, updated, 200, "")
	if u := updated.body["queue"].(map[string]any); u["status"] != "PAUSED" || u["concurrentJobs"] != json.Number("4") || u["description"] != "nightly" {
		t.Fatalf("update %v", u)
	}
	expect(t, h.rest("mediaconvert", "PUT", base+"/queues/batch", map[string]any{"reservationPlanSettings": map[string]any{"commitment": "ONE_YEAR", "renewalType": "EXPIRE", "reservedSlots": 1}}), 400, "BadRequestException")

	// Job templates must name a live queue.
	settings := map[string]any{"outputGroups": []any{}}
	expect(t, h.rest("mediaconvert", "POST", base+"/jobTemplates", map[string]any{"name": "hd", "settings": settings, "queue": "arn:aws:mediaconvert:us-east-1:000000000000:queues/missing"}), 404, "NotFoundException")
	expect(t, h.rest("mediaconvert", "POST", base+"/jobTemplates", map[string]any{"name": "hd", "settings": settings, "tags": map[string]any{"a": "b"}}), 400, "BadRequestException")
	template := h.rest("mediaconvert", "POST", base+"/jobTemplates", map[string]any{"name": "hd", "settings": settings, "queue": arn, "category": "video", "priority": 5})
	expect(t, template, 201, "")
	if tpl := template.body["jobTemplate"].(map[string]any); tpl["type"] != "CUSTOM" || tpl["queue"] != arn || tpl["arn"] != "arn:aws:mediaconvert:us-east-1:000000000000:jobTemplates/hd" {
		t.Fatalf("template %v", tpl)
	}
	expect(t, h.rest("mediaconvert", "POST", base+"/jobTemplates", map[string]any{"name": "sd", "settings": settings, "category": "other"}), 201, "")
	if got := h.rest("mediaconvert", "GET", base+"/jobTemplates?category=video", nil).body["jobTemplates"].([]any); len(got) != 1 {
		t.Fatalf("category filter %v", got)
	}
	if got := h.rest("mediaconvert", "GET", base+"/jobTemplates", nil).body["jobTemplates"].([]any); len(got) != 2 {
		t.Fatalf("template list %v", got)
	}
	if got := h.rest("mediaconvert", "GET", base+"/jobTemplates/hd", nil).body["jobTemplate"].(map[string]any); got["category"] != "video" {
		t.Fatalf("template get %v", got)
	}
	expect(t, h.rest("mediaconvert", "PUT", base+"/jobTemplates/hd", map[string]any{"description": "changed", "priority": 7}), 200, "")
	if got := h.rest("mediaconvert", "GET", base+"/jobTemplates/hd", nil).body["jobTemplate"].(map[string]any); got["description"] != "changed" || got["priority"] != json.Number("7") {
		t.Fatalf("template update %v", got)
	}
	expect(t, h.rest("mediaconvert", "DELETE", base+"/jobTemplates/sd", nil), 202, "")

	expect(t, h.rest("mediaconvert", "POST", base+"/presets", map[string]any{"name": "web", "settings": map[string]any{}, "tags": map[string]any{"a": "b"}}), 400, "BadRequestException")
	expect(t, h.rest("mediaconvert", "POST", base+"/presets", map[string]any{"name": "web", "settings": map[string]any{}, "category": "video"}), 201, "")
	if got := h.rest("mediaconvert", "GET", base+"/presets?category=audio", nil).body["presets"].([]any); len(got) != 0 {
		t.Fatalf("preset filter %v", got)
	}
	if got := h.rest("mediaconvert", "GET", base+"/presets", nil).body["presets"].([]any); len(got) != 1 {
		t.Fatalf("preset list %v", got)
	}
	expect(t, h.rest("mediaconvert", "GET", base+"/presets/web", nil), 200, "")
	expect(t, h.rest("mediaconvert", "PUT", base+"/presets/web", map[string]any{"description": "web delivery"}), 200, "")
	expect(t, h.rest("mediaconvert", "DELETE", base+"/presets/web", nil), 202, "")
	expect(t, h.rest("mediaconvert", "GET", base+"/presets/web", nil), 404, "NotFoundException")

	// The account policy is a singleton.
	if got := h.rest("mediaconvert", "GET", base+"/policy", nil); got.status != 200 || len(got.body["policy"].(map[string]any)) != 0 {
		t.Fatalf("empty policy %s", got.raw)
	}
	expect(t, h.rest("mediaconvert", "DELETE", base+"/policy", nil), 404, "NotFoundException")
	put := h.rest("mediaconvert", "PUT", base+"/policy", map[string]any{"policy": map[string]any{"httpInputs": "DISALLOWED", "s3Inputs": "ALLOWED"}})
	expect(t, put, 200, "")
	if p := put.body["policy"].(map[string]any); p["httpInputs"] != "DISALLOWED" || p["s3Inputs"] != "ALLOWED" {
		t.Fatalf("policy %v", p)
	}
	expect(t, h.rest("mediaconvert", "PUT", base+"/policy", map[string]any{"policy": map[string]any{"httpsInputs": "ALLOWED"}}), 200, "")
	if p := h.rest("mediaconvert", "GET", base+"/policy", nil).body["policy"].(map[string]any); p["httpInputs"] != "DISALLOWED" || p["httpsInputs"] != "ALLOWED" {
		t.Fatalf("policy merge %v", p)
	}
	expect(t, h.rest("mediaconvert", "PUT", base+"/policy", map[string]any{"policy": map[string]any{"httpInputs": "MAYBE"}}), 400, "BadRequestException")
	expect(t, h.rest("mediaconvert", "DELETE", base+"/policy", nil), 200, "")

	expect(t, h.rest("mediaconvert", "DELETE", base+"/queues/batch", nil), 202, "")
	expect(t, h.rest("mediaconvert", "DELETE", base+"/queues/batch", nil), 404, "NotFoundException")

	// Transcoding, endpoints and tags stay unbound.
	for _, call := range [][3]string{{"POST", "/jobs", ""}, {"GET", "/jobs/1", ""}, {"POST", "/endpoints", ""}, {"POST", "/tags", ""},
		{"GET", "/tags/" + url.PathEscape(arn), ""}, {"GET", "/versions", ""}} {
		expect(t, h.rest("mediaconvert", call[0], base+call[1], nil), 501, "UnknownOperationException")
	}
}

func TestMediaPackageChannelsOmitIngestAndRefuseEndpoints(t *testing.T) {
	h := newHarness(t, mediaPackage)
	channel := h.rest("mediapackage", "POST", "/channels", map[string]any{"id": "news", "description": "evening", "tags": map[string]any{"team": "a"}})
	expect(t, channel, 200, "")
	arn := str(channel.body["arn"])
	if arn != "arn:aws:mediapackage:us-east-1:000000000000:channels/news" || channel.body["id"] != "news" || channel.body["hlsIngest"] != nil ||
		channel.body["createdAt"] == nil || channel.body["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("channel %v", channel.body)
	}
	expect(t, h.rest("mediapackage", "POST", "/channels", map[string]any{"id": "news"}), 409, "")
	expect(t, h.rest("mediapackage", "GET", "/channels/missing", nil), 404, "NotFoundException")
	expect(t, h.rest("mediapackage", "GET", "/channels/news", nil), 200, "")
	if got := h.rest("mediapackage", "GET", "/channels", nil).body["channels"].([]any); len(got) != 1 {
		t.Fatalf("list %v", got)
	}
	updated := h.rest("mediapackage", "PUT", "/channels/news", map[string]any{"description": "night"})
	expect(t, updated, 200, "")
	if updated.body["description"] != "night" || updated.body["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("update %v", updated.body)
	}
	path := "/tags/" + url.PathEscape(arn)
	expect(t, h.rest("mediapackage", "POST", path, map[string]any{"tags": map[string]any{"k": "v"}}), 204, "")
	if tags := h.rest("mediapackage", "GET", path, nil).body["tags"].(map[string]any); len(tags) != 2 {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("mediapackage", "DELETE", path+"?tagKeys=k", nil), 204, "")
	if tags := h.rest("mediapackage", "GET", path, nil).body["tags"].(map[string]any); len(tags) != 1 {
		t.Fatalf("untag %v", tags)
	}
	for _, call := range [][2]string{{"POST", "/origin_endpoints"}, {"GET", "/origin_endpoints/x"}, {"GET", "/origin_endpoints"}, {"PUT", "/origin_endpoints/x"},
		{"DELETE", "/origin_endpoints/x"}, {"PUT", "/channels/news/credentials"}, {"PUT", "/channels/news/configure_logs"}, {"POST", "/harvest_jobs"}} {
		expect(t, h.rest("mediapackage", call[0], call[1], nil), 501, "UnknownOperationException")
	}
	expect(t, h.rest("mediapackage", "DELETE", "/channels/news", nil), 202, "")
	expect(t, h.rest("mediapackage", "DELETE", "/channels/news", nil), 404, "NotFoundException")
}

func TestMediaPackageV2ChannelGroupsHoldChannels(t *testing.T) {
	h := newHarness(t, mediaPackageV2)
	group := h.rest("mediapackagev2", "POST", "/channelGroup", map[string]any{"ChannelGroupName": "live", "Description": "all", "tags": map[string]any{"team": "a"}})
	expect(t, group, 200, "")
	groupARN := str(group.body["Arn"])
	if groupARN != "arn:aws:mediapackagev2:us-east-1:000000000000:channelGroup/live" || group.body["EgressDomain"] != nil || group.body["ETag"] != nil || group.body["CreatedAt"] == nil {
		t.Fatalf("group %v", group.body)
	}
	expect(t, h.rest("mediapackagev2", "POST", "/channelGroup", map[string]any{"ChannelGroupName": "live"}), 409, "ConflictException")
	expect(t, h.rest("mediapackagev2", "POST", "/channelGroup", map[string]any{"ChannelGroupName": "bad name"}), 400, "ValidationException")
	expect(t, h.rest("mediapackagev2", "GET", "/channelGroup/missing", nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("mediapackagev2", "GET", "/channelGroup/live", nil), 200, "")
	if got := h.rest("mediapackagev2", "GET", "/channelGroup", nil).body["Items"].([]any); len(got) != 1 {
		t.Fatalf("groups %v", got)
	}
	expect(t, h.rest("mediapackagev2", "PUT", "/channelGroup/live", map[string]any{"Description": "renamed"}), 200, "")
	if got := h.rest("mediapackagev2", "GET", "/channelGroup/live", nil).body; got["Description"] != "renamed" || got["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("group update %v", got)
	}
	expect(t, h.rest("mediapackagev2", "PUT", "/channelGroup/live", map[string]any{"Description": "x", "ETag": "tag"}), 400, "")

	expect(t, h.rest("mediapackagev2", "POST", "/channelGroup/missing/channel", map[string]any{"ChannelName": "main"}), 404, "ResourceNotFoundException")
	channel := h.rest("mediapackagev2", "POST", "/channelGroup/live/channel", map[string]any{"ChannelName": "main", "Description": "main feed"})
	expect(t, channel, 200, "")
	if channel.body["Arn"] != "arn:aws:mediapackagev2:us-east-1:000000000000:channelGroup/live/channel/main" || channel.body["ChannelGroupName"] != "live" ||
		channel.body["InputType"] != "HLS" || channel.body["IngestEndpoints"] != nil {
		t.Fatalf("channel %v", channel.body)
	}
	expect(t, h.rest("mediapackagev2", "POST", "/channelGroup/live/channel", map[string]any{"ChannelName": "main"}), 409, "ConflictException")
	expect(t, h.rest("mediapackagev2", "POST", "/channelGroup/live/channel", map[string]any{"ChannelName": "switch", "InputType": "HLS",
		"InputSwitchConfiguration": map[string]any{"MQCSInputSwitching": true}}), 400, "ValidationException")
	expect(t, h.rest("mediapackagev2", "POST", "/channelGroup/live/channel", map[string]any{"ChannelName": "cmaf", "InputType": "CMAF",
		"InputSwitchConfiguration": map[string]any{"MQCSInputSwitching": true, "PreferredInput": 2}}), 200, "")
	expect(t, h.rest("mediapackagev2", "DELETE", "/channelGroup/live", nil), 409, "ConflictException")
	expect(t, h.rest("mediapackagev2", "GET", "/channelGroup/live/channel/main/", nil), 200, "")
	expect(t, h.rest("mediapackagev2", "GET", "/channelGroup/live/channel/missing/", nil), 404, "ResourceNotFoundException")
	if got := h.rest("mediapackagev2", "GET", "/channelGroup/live/channel", nil).body["Items"].([]any); len(got) != 2 {
		t.Fatalf("channels %v", got)
	}
	expect(t, h.rest("mediapackagev2", "PUT", "/channelGroup/live/channel/main/", map[string]any{"OutputHeaderConfiguration": map[string]any{"PublishMQCS": true}}), 400, "ValidationException")
	expect(t, h.rest("mediapackagev2", "PUT", "/channelGroup/live/channel/cmaf/", map[string]any{"OutputHeaderConfiguration": map[string]any{"PublishMQCS": true}}), 200, "")
	expect(t, h.rest("mediapackagev2", "PUT", "/channelGroup/live/channel/main/", map[string]any{"Description": "updated"}), 200, "")
	if got := h.rest("mediapackagev2", "GET", "/channelGroup/live/channel/main/", nil).body; got["Description"] != "updated" {
		t.Fatalf("channel update %v", got)
	}

	channelARN := url.PathEscape(str(channel.body["Arn"]))
	expect(t, h.rest("mediapackagev2", "POST", "/tags/"+channelARN, map[string]any{"tags": map[string]any{"k": "v"}}), 204, "")
	if tags := h.rest("mediapackagev2", "GET", "/tags/"+channelARN, nil).body["tags"].(map[string]any); tags["k"] != "v" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("mediapackagev2", "DELETE", "/tags/"+channelARN+"?tagKeys=k", nil), 204, "")

	base := "/channelGroup/live/channel/main"
	for _, call := range [][2]string{{"POST", base + "/originEndpoint"}, {"GET", base + "/originEndpoint/e"}, {"GET", base + "/originEndpoint"}, {"PUT", base + "/originEndpoint/e"},
		{"DELETE", base + "/originEndpoint/e"}, {"PUT", base + "/policy"}, {"GET", base + "/policy"}, {"DELETE", base + "/policy"}, {"POST", base + "/reset"},
		{"POST", base + "/originEndpoint/e/policy"}, {"POST", base + "/originEndpoint/e/reset"}, {"GET", "/channelGroup/live/harvestJob"}} {
		expect(t, h.rest("mediapackagev2", call[0], call[1], nil), 501, "UnknownOperationException")
	}
	expect(t, h.rest("mediapackagev2", "DELETE", "/channelGroup/live/channel/main/", nil), 200, "")
	expect(t, h.rest("mediapackagev2", "DELETE", "/channelGroup/live/channel/cmaf/", nil), 200, "")
	expect(t, h.rest("mediapackagev2", "DELETE", "/channelGroup/live", nil), 200, "")
	expect(t, h.rest("mediapackagev2", "GET", "/channelGroup/live", nil), 404, "ResourceNotFoundException")
}

func TestMediaPackageVODPackagingGroupsOmitDomains(t *testing.T) {
	h := newHarness(t, mediaPackageVOD)
	secret := "arn:aws:secretsmanager:us-east-1:000000000000:secret:cdn-AbCdEf"
	role := "arn:aws:iam::000000000000:role/packager"
	group := h.rest("mediapackage-vod", "POST", "/packaging_groups", map[string]any{"id": "vod", "tags": map[string]any{"team": "a"},
		"authorization": map[string]any{"cdnIdentifierSecret": secret, "secretsRoleArn": role}, "egressAccessLogs": map[string]any{"logGroupName": "/aws/vod"}})
	expect(t, group, 200, "")
	arn := str(group.body["arn"])
	if arn != "arn:aws:mediapackage-vod:us-east-1:000000000000:packaging-groups/vod" || group.body["domainName"] != nil || group.body["createdAt"] == nil {
		t.Fatalf("group %v", group.body)
	}
	expect(t, h.rest("mediapackage-vod", "POST", "/packaging_groups", map[string]any{"id": "vod"}), 409, "")
	expect(t, h.rest("mediapackage-vod", "POST", "/packaging_groups", map[string]any{"id": "bad", "authorization": map[string]any{"cdnIdentifierSecret": "x", "secretsRoleArn": role}}), 400, "ValidationException")
	described := h.rest("mediapackage-vod", "GET", "/packaging_groups/vod", nil)
	expect(t, described, 200, "")
	if described.body["approximateAssetCount"] != json.Number("0") || described.body["authorization"].(map[string]any)["secretsRoleArn"] != role {
		t.Fatalf("describe %v", described.body)
	}
	expect(t, h.rest("mediapackage-vod", "GET", "/packaging_groups/missing", nil), 404, "NotFoundException")
	if got := h.rest("mediapackage-vod", "GET", "/packaging_groups", nil).body["packagingGroups"].([]any); len(got) != 1 {
		t.Fatalf("list %v", got)
	}
	other := "arn:aws:iam::000000000000:role/other"
	updated := h.rest("mediapackage-vod", "PUT", "/packaging_groups/vod", map[string]any{"authorization": map[string]any{"cdnIdentifierSecret": secret, "secretsRoleArn": other}})
	expect(t, updated, 200, "")
	if updated.body["authorization"].(map[string]any)["secretsRoleArn"] != other {
		t.Fatalf("update %v", updated.body)
	}
	path := "/tags/" + url.PathEscape(arn)
	expect(t, h.rest("mediapackage-vod", "POST", path, map[string]any{"tags": map[string]any{"k": "v"}}), 204, "")
	if tags := h.rest("mediapackage-vod", "GET", path, nil).body["tags"].(map[string]any); len(tags) != 2 {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("mediapackage-vod", "DELETE", path+"?tagKeys=k", nil), 204, "")
	for _, call := range [][2]string{{"POST", "/packaging_configurations"}, {"GET", "/packaging_configurations/x"}, {"GET", "/packaging_configurations"},
		{"DELETE", "/packaging_configurations/x"}, {"POST", "/assets"}, {"GET", "/assets/x"}, {"GET", "/assets"}, {"DELETE", "/assets/x"}, {"PUT", "/packaging_groups/vod/configure_logs"}} {
		expect(t, h.rest("mediapackage-vod", call[0], call[1], nil), 501, "UnknownOperationException")
	}
	expect(t, h.rest("mediapackage-vod", "DELETE", "/packaging_groups/vod", nil), 202, "")
	expect(t, h.rest("mediapackage-vod", "DELETE", "/packaging_groups/vod", nil), 404, "NotFoundException")
}

func TestMediaTailorConfigurationsSourcesAndChannels(t *testing.T) {
	h := newHarness(t, mediaTailor)

	put := h.rest("mediatailor", "PUT", "/playbackConfiguration", map[string]any{"Name": "ads", "AdDecisionServerUrl": "https://ads.example.com/vast",
		"VideoContentSourceUrl": "https://origin.example.com/", "DashConfiguration": map[string]any{"MpdLocation": "DISABLED"}, "tags": map[string]any{"team": "a"}})
	expect(t, put, 200, "")
	arn := str(put.body["PlaybackConfigurationArn"])
	if arn != "arn:aws:mediatailor:us-east-1:000000000000:playbackConfiguration/ads" || put.body["PlaybackEndpointPrefix"] != nil || put.body["HlsConfiguration"] != nil ||
		put.body["SessionInitializationEndpointPrefix"] != nil || put.body["DashConfiguration"].(map[string]any)["MpdLocation"] != "DISABLED" ||
		put.body["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("playback configuration %v", put.body)
	}
	expect(t, h.rest("mediatailor", "PUT", "/playbackConfiguration", map[string]any{"Name": "ads", "SlateAdUrl": "https://origin.example.com/slate.mp4"}), 200, "")
	got := h.rest("mediatailor", "GET", "/playbackConfiguration/ads", nil)
	expect(t, got, 200, "")
	if got.body["SlateAdUrl"] != "https://origin.example.com/slate.mp4" || got.body["AdDecisionServerUrl"] != "https://ads.example.com/vast" {
		t.Fatalf("put merges %v", got.body)
	}
	expect(t, h.rest("mediatailor", "PUT", "/playbackConfiguration", map[string]any{"Name": "long", "VideoContentSourceUrl": strings.Repeat("a", 513)}), 400, "ValidationException")
	expect(t, h.rest("mediatailor", "GET", "/playbackConfiguration/missing", nil), 404, "ResourceNotFoundException")
	if items := h.rest("mediatailor", "GET", "/playbackConfigurations", nil).body["Items"].([]any); len(items) != 1 {
		t.Fatalf("playback list %v", items)
	}

	prefetch := h.rest("mediatailor", "POST", "/prefetchSchedule/ads/morning", map[string]any{"StreamId": "s1", "Consumption": map[string]any{"EndTime": "2026-12-01T00:00:00Z"},
		"Retrieval": map[string]any{"EndTime": "2026-11-30T00:00:00Z"}})
	expect(t, prefetch, 200, "")
	if prefetch.body["Arn"] != "arn:aws:mediatailor:us-east-1:000000000000:prefetchSchedule/ads/morning" || prefetch.body["PlaybackConfigurationName"] != "ads" {
		t.Fatalf("prefetch %v", prefetch.body)
	}
	expect(t, h.rest("mediatailor", "POST", "/prefetchSchedule/ads/morning", map[string]any{}), 409, "ConflictException")
	expect(t, h.rest("mediatailor", "POST", "/prefetchSchedule/ads/weekly", map[string]any{"ScheduleType": "RECURRING"}), 400, "ValidationException")
	expect(t, h.rest("mediatailor", "POST", "/prefetchSchedule/missing/morning", map[string]any{}), 404, "ResourceNotFoundException")
	expect(t, h.rest("mediatailor", "GET", "/prefetchSchedule/ads/morning", nil), 200, "")
	expect(t, h.rest("mediatailor", "GET", "/prefetchSchedule/ads/missing", nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("mediatailor", "POST", "/prefetchSchedule/ads/recurring", map[string]any{"ScheduleType": "RECURRING", "RecurringPrefetchConfiguration": map[string]any{"EndTime": "2026-12-01T00:00:00Z", "RecurringConsumption": map[string]any{}, "RecurringRetrieval": map[string]any{}}}), 200, "")
	if items := h.rest("mediatailor", "POST", "/prefetchSchedule/ads", map[string]any{"ScheduleType": "RECURRING"}).body["Items"].([]any); len(items) != 1 {
		t.Fatalf("recurring filter %v", items)
	}
	// ALL lists every schedule; schedules created without a type are SINGLE.
	for filter, want := range map[string]int{"ALL": 2, "SINGLE": 1, "RECURRING": 1} {
		if items := h.rest("mediatailor", "POST", "/prefetchSchedule/ads", map[string]any{"ScheduleType": filter}).body["Items"].([]any); len(items) != want {
			t.Fatalf("%s filter %d items, want %d: %v", filter, len(items), want, items)
		}
	}
	if got := h.rest("mediatailor", "GET", "/prefetchSchedule/ads/morning", nil).body["ScheduleType"]; got != "SINGLE" {
		t.Fatalf("default schedule type %v", got)
	}
	if items := h.rest("mediatailor", "POST", "/prefetchSchedule/ads", map[string]any{"StreamId": "s1"}).body["Items"].([]any); len(items) != 1 {
		t.Fatalf("stream filter %v", items)
	}
	if items := h.rest("mediatailor", "POST", "/prefetchSchedule/ads", map[string]any{}).body["Items"].([]any); len(items) != 2 {
		t.Fatalf("prefetch list %v", items)
	}
	expect(t, h.rest("mediatailor", "DELETE", "/playbackConfiguration/ads", nil), 409, "ConflictException")
	expect(t, h.rest("mediatailor", "DELETE", "/prefetchSchedule/ads/morning", nil), 204, "")
	expect(t, h.rest("mediatailor", "DELETE", "/prefetchSchedule/ads/recurring", nil), 204, "")

	// Source locations hold VOD and live sources.
	location := h.rest("mediatailor", "POST", "/sourceLocation/origin", map[string]any{"HttpConfiguration": map[string]any{"BaseUrl": "https://origin.example.com"},
		"AccessConfiguration": map[string]any{"AccessType": "SECRETS_MANAGER_ACCESS_TOKEN"}, "tags": map[string]any{"team": "a"}})
	expect(t, location, 200, "")
	if location.body["Arn"] != "arn:aws:mediatailor:us-east-1:000000000000:sourceLocation/origin" || location.body["SourceLocationName"] != "origin" || location.body["CreationTime"] == nil {
		t.Fatalf("source location %v", location.body)
	}
	expect(t, h.rest("mediatailor", "POST", "/sourceLocation/origin", map[string]any{"HttpConfiguration": map[string]any{"BaseUrl": "https://origin.example.com"}}), 409, "ConflictException")
	expect(t, h.rest("mediatailor", "POST", "/sourceLocation/bad", map[string]any{"HttpConfiguration": map[string]any{"BaseUrl": "origin.example.com"}}), 400, "ValidationException")
	expect(t, h.rest("mediatailor", "GET", "/sourceLocation/origin", nil), 200, "")
	expect(t, h.rest("mediatailor", "GET", "/sourceLocation/missing", nil), 404, "ResourceNotFoundException")
	if items := h.rest("mediatailor", "GET", "/sourceLocations", nil).body["Items"].([]any); len(items) != 1 {
		t.Fatalf("locations %v", items)
	}
	updated := h.rest("mediatailor", "PUT", "/sourceLocation/origin", map[string]any{"HttpConfiguration": map[string]any{"BaseUrl": "https://origin2.example.com"}})
	expect(t, updated, 200, "")
	if updated.body["HttpConfiguration"].(map[string]any)["BaseUrl"] != "https://origin2.example.com" || updated.body["AccessConfiguration"] == nil {
		t.Fatalf("location update %v", updated.body)
	}
	packages := []any{map[string]any{"Path": "/main.m3u8", "SourceGroup": "hls", "Type": "HLS"}}
	vod := h.rest("mediatailor", "POST", "/sourceLocation/origin/vodSource/movie", map[string]any{"HttpPackageConfigurations": packages})
	expect(t, vod, 200, "")
	if vod.body["Arn"] != "arn:aws:mediatailor:us-east-1:000000000000:vodSource/origin/movie" || vod.body["SourceLocationName"] != "origin" || vod.body["VodSourceName"] != "movie" {
		t.Fatalf("vod %v", vod.body)
	}
	expect(t, h.rest("mediatailor", "POST", "/sourceLocation/origin/vodSource/movie", map[string]any{"HttpPackageConfigurations": packages}), 409, "ConflictException")
	expect(t, h.rest("mediatailor", "POST", "/sourceLocation/missing/vodSource/movie", map[string]any{"HttpPackageConfigurations": packages}), 404, "ResourceNotFoundException")
	expect(t, h.rest("mediatailor", "GET", "/sourceLocation/origin/vodSource/movie", nil), 200, "")
	expect(t, h.rest("mediatailor", "GET", "/sourceLocation/origin/vodSource/missing", nil), 404, "ResourceNotFoundException")
	if items := h.rest("mediatailor", "GET", "/sourceLocation/origin/vodSources", nil).body["Items"].([]any); len(items) != 1 {
		t.Fatalf("vod list %v", items)
	}
	expect(t, h.rest("mediatailor", "PUT", "/sourceLocation/origin/vodSource/movie", map[string]any{"HttpPackageConfigurations": []any{map[string]any{"Path": "/b.mpd", "SourceGroup": "dash", "Type": "DASH"}}}), 200, "")
	live := h.rest("mediatailor", "POST", "/sourceLocation/origin/liveSource/match", map[string]any{"HttpPackageConfigurations": packages})
	expect(t, live, 200, "")
	if live.body["Arn"] != "arn:aws:mediatailor:us-east-1:000000000000:liveSource/origin/match" || live.body["LiveSourceName"] != "match" {
		t.Fatalf("live %v", live.body)
	}
	expect(t, h.rest("mediatailor", "GET", "/sourceLocation/origin/liveSource/match", nil), 200, "")
	if items := h.rest("mediatailor", "GET", "/sourceLocation/origin/liveSources", nil).body["Items"].([]any); len(items) != 1 {
		t.Fatalf("live list %v", items)
	}
	expect(t, h.rest("mediatailor", "PUT", "/sourceLocation/origin/liveSource/match", map[string]any{"HttpPackageConfigurations": packages}), 200, "")
	expect(t, h.rest("mediatailor", "DELETE", "/sourceLocation/origin", nil), 409, "ConflictException")

	// Channels stay stopped.
	outputs := []any{map[string]any{"ManifestName": "main", "SourceGroup": "hls", "HlsPlaylistSettings": map[string]any{"ManifestWindowSeconds": 60}}}
	expect(t, h.rest("mediatailor", "POST", "/channel/live", map[string]any{"Outputs": outputs, "PlaybackMode": "LINEAR"}), 400, "ValidationException")
	expect(t, h.rest("mediatailor", "POST", "/channel/live", map[string]any{"Outputs": []any{map[string]any{"ManifestName": "main", "SourceGroup": "hls",
		"HlsPlaylistSettings": map[string]any{"ManifestWindowSeconds": 5}}}, "PlaybackMode": "LOOP"}), 400, "ValidationException")
	channel := h.rest("mediatailor", "POST", "/channel/live", map[string]any{"Outputs": outputs, "PlaybackMode": "LINEAR", "Tier": "STANDARD",
		"FillerSlate": map[string]any{"SourceLocationName": "origin", "VodSourceName": "movie"}, "tags": map[string]any{"team": "a"}})
	expect(t, channel, 200, "")
	channelARN := str(channel.body["Arn"])
	if channelARN != "arn:aws:mediatailor:us-east-1:000000000000:channel/live" || channel.body["ChannelState"] != "STOPPED" || channel.body["Tier"] != "STANDARD" ||
		channel.body["Outputs"].([]any)[0].(map[string]any)["PlaybackUrl"] != nil {
		t.Fatalf("channel %v", channel.body)
	}
	expect(t, h.rest("mediatailor", "POST", "/channel/live", map[string]any{"Outputs": outputs, "PlaybackMode": "LOOP"}), 409, "ConflictException")
	described := h.rest("mediatailor", "GET", "/channel/live", nil)
	expect(t, described, 200, "")
	if described.body["LogConfiguration"] == nil || described.body["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("describe %v", described.body)
	}
	expect(t, h.rest("mediatailor", "GET", "/channel/missing", nil), 404, "ResourceNotFoundException")
	if items := h.rest("mediatailor", "GET", "/channels", nil).body["Items"].([]any); len(items) != 1 {
		t.Fatalf("channels %v", items)
	}
	expect(t, h.rest("mediatailor", "PUT", "/channel/live", map[string]any{"Outputs": outputs, "TimeShiftConfiguration": map[string]any{"MaxTimeDelaySeconds": 30000}}), 400, "ValidationException")
	expect(t, h.rest("mediatailor", "PUT", "/channel/live", map[string]any{"Outputs": outputs, "TimeShiftConfiguration": map[string]any{"MaxTimeDelaySeconds": 60}}), 200, "")
	expect(t, h.rest("mediatailor", "POST", "/channel/loop", map[string]any{"Outputs": outputs, "PlaybackMode": "LOOP"}), 200, "")
	expect(t, h.rest("mediatailor", "PUT", "/channel/loop", map[string]any{"Outputs": outputs, "FillerSlate": map[string]any{"SourceLocationName": "origin", "VodSourceName": "movie"}}), 400, "ValidationException")
	expect(t, h.rest("mediatailor", "PUT", "/channel/live", map[string]any{"Outputs": outputs, "PlaybackMode": "LOOP"}), 400, "")

	path := "/tags/" + url.PathEscape(channelARN)
	expect(t, h.rest("mediatailor", "POST", path, map[string]any{"tags": map[string]any{"k": "v"}}), 204, "")
	if tags := h.rest("mediatailor", "GET", path, nil).body["tags"].(map[string]any); len(tags) != 2 {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("mediatailor", "DELETE", path+"?tagKeys=k", nil), 204, "")

	for _, call := range [][2]string{{"PUT", "/channel/live/start"}, {"PUT", "/channel/live/stop"}, {"POST", "/channel/live/program/p"}, {"GET", "/channel/live/program/p"},
		{"PUT", "/channel/live/policy"}, {"GET", "/channel/live/policy"}, {"DELETE", "/channel/live/policy"}, {"GET", "/channel/live/schedule"}, {"GET", "/alerts"},
		{"PUT", "/configureLogs/channel"}, {"PUT", "/configureLogs/playbackConfiguration"}} {
		expect(t, h.rest("mediatailor", call[0], call[1], nil), 501, "UnknownOperationException")
	}
	expect(t, h.rest("mediatailor", "DELETE", "/channel/live", nil), 200, "")
	expect(t, h.rest("mediatailor", "DELETE", "/channel/loop", nil), 200, "")
	expect(t, h.rest("mediatailor", "DELETE", "/sourceLocation/origin/vodSource/movie", nil), 200, "")
	expect(t, h.rest("mediatailor", "DELETE", "/sourceLocation/origin/liveSource/match", nil), 200, "")
	expect(t, h.rest("mediatailor", "DELETE", "/sourceLocation/origin", nil), 200, "")
	expect(t, h.rest("mediatailor", "DELETE", "/playbackConfiguration/ads", nil), 204, "")
	expect(t, h.rest("mediatailor", "DELETE", "/playbackConfiguration/ads", nil), 404, "ResourceNotFoundException")
}
