package catalog

import (
	"strings"
)

// Reviewed bounded specifications for AWS Direct Connect, AWS Ground Station
// and Amazon Kinesis Video Streams. Each binds only official operations whose
// effect is configuration metadata: no physical connection, virtual interface,
// satellite contact, receiver endpoint, media fragment or signaling message
// exists, and nothing outside the simulator is contacted.

var directConnect = &Service{
	Model: "directconnect", Title: "AWS Direct Connect", IAMPrefix: "directconnect", Issues: []int{239}, Global: true,
	Resources: []*Resource{
		{Kind: "gateway", Shape: "DirectConnectGateway", ID: "uuid",
			ARN:        "arn:{partition}:directconnect::{account}:dx-gateway/{id}",
			NameMember: "directConnectGatewayName", IDMember: "directConnectGatewayId", Limit: 50,
			Aliases:  map[string]string{"newDirectConnectGatewayName": "directConnectGatewayName"},
			Generate: map[string]any{"amazonSideAsn": 64512, "ownerAccount": "$account", "directConnectGatewayState": "pending"}},
	},
	Ops: []Op{
		{Name: "CreateDirectConnectGateway", Kind: Create, Resource: "gateway", Check: directConnectASN},
		{Name: "DescribeDirectConnectGateways", Kind: List, Resource: "gateway", Filters: map[string]string{"directConnectGatewayId": "directConnectGatewayId"}},
		{Name: "UpdateDirectConnectGateway", Kind: Update, Resource: "gateway", Lookup: "directConnectGatewayId", Fields: []string{"newDirectConnectGatewayName"}},
		{Name: "DeleteDirectConnectGateway", Kind: Delete, Resource: "gateway", Lookup: "directConnectGatewayId", Set: map[string]any{"directConnectGatewayState": "deleted"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Direct Connect gateways are stored records that stay pending and never become available, because nothing is provisioned: no connection, interconnect, link aggregation group, virtual interface, BGP session or routing exists, and a gateway forwards no traffic. Gateways are visible from every Region, as AWS documents, and deleting one removes it at once (the delete response reports deleted). Every connection, LAG, virtual interface, hosted connection, location, letter of authorization, router configuration, test and dependent-resource API returns UnknownOperationException with HTTP 501.",
		"Gateway associations and association proposals are not bound: an association would claim that a virtual private gateway or transit gateway of another service is attached and ready to pass traffic, which this record store cannot check. DescribeTags is not bound because it addresses many ARNs in one request; tags are returned in the tags member of each gateway instead.",
		"amazonSideAsn defaults to 64512 and must be 64512 to 65534 or 4200000000 to 4294967294; it cannot be changed after creation. Gateway names are not required to be unique, as in AWS. DescribeDirectConnectGateways returns an empty list for an identifier that does not exist, and a missing gateway in other operations fails with DirectConnectClientException.",
	},
}

// directConnectASN applies the documented private ASN ranges.
func directConnectASN(input map[string]any) error {
	value, present := input["amazonSideAsn"]
	if !present {
		return nil
	}
	asn, ok := Number(value)
	if ok && (asn >= 64512 && asn <= 65534 || asn >= 4200000000 && asn <= 4294967294) {
		return nil
	}
	return Failure("DirectConnectClientException", "amazonSideAsn must be 64512 to 65534 or 4200000000 to 4294967294.")
}

var groundStation = &Service{
	Model: "groundstation", Title: "AWS Ground Station", IAMPrefix: "groundstation", Issues: []int{251},
	Resources: []*Resource{
		// The configuration type is stored as the row name so that the ARN
		// carries the documented config/{type}/{id} form.
		{Kind: "config", Shape: "GetConfigResponse", Summary: "ConfigListItem", ID: "uuid",
			ARN:       "arn:{partition}:groundstation:{region}:{account}:config/{name}/{id}",
			ARNMember: "configArn", NameMember: "configType", IDMember: "configId", Limit: 100, InUse: "InvalidParameterException",
			Refs: map[string]string{"configData.uplinkEchoConfig.antennaUplinkConfigArn": "config"}},
		{Kind: "missionProfile", Shape: "GetMissionProfileResponse", Summary: "MissionProfileListItem", ID: "uuid",
			ARN:       "arn:{partition}:groundstation:{region}:{account}:mission-profile/{id}",
			ARNMember: "missionProfileArn", IDMember: "missionProfileId", Limit: 50,
			Refs:     map[string]string{"trackingConfigArn": "config", "dataflowEdges[][]": "config"},
			Generate: map[string]any{"region": "$region"}},
	},
	Ops: []Op{
		{Name: "CreateConfig", Kind: Create, Resource: "config", Check: groundStationConfig},
		{Name: "GetConfig", Kind: Read, Resource: "config", Lookup: "configId", Match: []string{"configType"}},
		{Name: "ListConfigs", Kind: List, Resource: "config"},
		{Name: "UpdateConfig", Kind: Update, Resource: "config", Lookup: "configId", Accept: []string{"configType"}, Fields: []string{"name", "configData"},
			Check: groundStationConfig, Merged: groundStationConfigType},
		{Name: "DeleteConfig", Kind: Delete, Resource: "config", Lookup: "configId", Match: []string{"configType"}},
		{Name: "CreateMissionProfile", Kind: Create, Resource: "missionProfile", Check: groundStationMissionProfile},
		{Name: "GetMissionProfile", Kind: Read, Resource: "missionProfile", Lookup: "missionProfileId"},
		{Name: "ListMissionProfiles", Kind: List, Resource: "missionProfile"},
		{Name: "UpdateMissionProfile", Kind: Update, Resource: "missionProfile", Lookup: "missionProfileId", Check: groundStationMissionProfile,
			Fields: []string{"name", "contactPrePassDurationSeconds", "contactPostPassDurationSeconds", "minimumViableContactDurationSeconds",
				"dataflowEdges", "trackingConfigArn", "streamsKmsKey", "streamsKmsRole"}},
		{Name: "DeleteMissionProfile", Kind: Delete, Resource: "missionProfile", Lookup: "missionProfileId"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Configs and mission profiles are stored records: no antenna, contact, satellite pass, data delivery or receiver endpoint exists. ReserveContact, CancelContact, DescribeContact, ListContacts, satellites, ground stations, minute usage and the agent APIs return UnknownOperationException with HTTP 501, as do dataflow endpoint groups (their records carry receiver addresses and ports) and ephemerides (their validation and use depend on AWS-owned satellite data).",
		"A config holds exactly one configData member, which sets its type; the config ARN carries that type. A GetConfig or DeleteConfig whose path configType is not the stored type fails with ResourceNotFoundException, and UpdateConfig refuses a type change. The center frequency, bandwidth and EIRP ranges documented by the API are enforced; demodulation and decode settings, S3 bucket and role ARNs, and dataflow endpoint names are stored without being resolved.",
		"A mission profile must name an existing tracking config in trackingConfigArn, and every ARN in dataflowEdges must name an existing config; an uplink echo config must name an existing antenna-uplink config. Deleting a config that a mission profile or an uplink echo config still names fails with InvalidParameterException (the simulator's choice of the modeled exception; DependencyException is a server fault). The streamsKmsKey and streamsKmsRole are stored without resolving keys or roles.",
	},
}

// groundStationConfigTypes maps each configData member to its config type.
var groundStationConfigTypes = map[string]string{
	"antennaDownlinkConfig": "antenna-downlink", "antennaDownlinkDemodDecodeConfig": "antenna-downlink-demod-decode",
	"trackingConfig": "tracking", "dataflowEndpointConfig": "dataflow-endpoint", "antennaUplinkConfig": "antenna-uplink",
	"uplinkEchoConfig": "uplink-echo", "s3RecordingConfig": "s3-recording",
}

// groundStationConfig derives the config type from the single configData
// member and applies the documented frequency, bandwidth and EIRP ranges.
func groundStationConfig(input map[string]any) error {
	data, _ := input["configData"].(map[string]any)
	if len(data) != 1 {
		return Failure("InvalidParameterException", "Exactly one configData member must be set.")
	}
	var member string
	for key := range data {
		member = key
	}
	kind := groundStationConfigTypes[member]
	if given, ok := input["configType"].(string); ok && given != kind {
		return Failure("InvalidParameterException", "configType "+given+" does not match the "+kind+" configData.")
	}
	input["configType"] = kind
	section, _ := data[member].(map[string]any)
	switch member {
	case "antennaDownlinkConfig":
		return groundStationSpectrum(section, 10, 54000, false)
	case "antennaDownlinkDemodDecodeConfig":
		return groundStationSpectrum(section, 125, 650000, false)
	case "antennaUplinkConfig":
		if err := groundStationSpectrum(section, 0, 0, true); err != nil {
			return err
		}
		eirp, _ := section["targetEirp"].(map[string]any)
		if value, ok := Number(eirp["value"]); !ok || value < 20 || value > 50 {
			return Failure("InvalidParameterException", "targetEirp must be from 20.0 to 50.0 dBW.")
		}
	case "uplinkEchoConfig":
		if arn, _ := section["antennaUplinkConfigArn"].(string); groundStationARNType(arn) != "antenna-uplink" {
			return Failure("InvalidParameterException", "antennaUplinkConfigArn must name an antenna-uplink config.")
		}
	}
	return nil
}

// groundStationConfigType refuses an update that would change the config type.
func groundStationConfigType(document, supplied map[string]any) error {
	if supplied["configType"] != document["configType"] {
		return Failure("InvalidParameterException", "The type of a config cannot be changed.")
	}
	return nil
}

// groundStationSpectrum checks the center frequency (MHz) and, for downlink,
// the bandwidth (kHz) against the documented limits.
func groundStationSpectrum(section map[string]any, lowKHz, highKHz float64, uplink bool) error {
	spectrum, _ := section["spectrumConfig"].(map[string]any)
	center, _ := spectrum["centerFrequency"].(map[string]any)
	mhz, ok := groundStationScale(center, map[string]float64{"GHz": 1000, "MHz": 1, "kHz": 0.001})
	switch {
	case uplink && (!ok || mhz < 2025 || mhz > 2120):
		return Failure("InvalidParameterException", "The uplink center frequency must be from 2025 to 2120 MHz.")
	case !uplink && (!ok || mhz < 2200 || mhz > 2300 && mhz < 7750 || mhz > 8400):
		return Failure("InvalidParameterException", "The downlink center frequency must be from 2200 to 2300 MHz or 7750 to 8400 MHz.")
	}
	if uplink {
		return nil
	}
	width, _ := spectrum["bandwidth"].(map[string]any)
	if khz, ok := groundStationScale(width, map[string]float64{"GHz": 1e6, "MHz": 1000, "kHz": 1}); !ok || khz < lowKHz || khz > highKHz {
		return Failure("InvalidParameterException", "The bandwidth is outside the documented limits for this config type.")
	}
	return nil
}

// groundStationScale converts a value and units structure.
func groundStationScale(quantity map[string]any, factors map[string]float64) (float64, bool) {
	value, ok := Number(quantity["value"])
	units, _ := quantity["units"].(string)
	factor, known := factors[units]
	return value * factor, ok && known
}

// groundStationARNType returns the type segment of a config ARN.
func groundStationARNType(arn string) string {
	_, rest, found := strings.Cut(arn, ":config/")
	if !found {
		return ""
	}
	kind, _, _ := strings.Cut(rest, "/")
	return kind
}

// groundStationMissionProfile requires the tracking config ARN to name a
// tracking config and every dataflow edge to join two config ARNs.
func groundStationMissionProfile(input map[string]any) error {
	if arn, ok := input["trackingConfigArn"].(string); ok && groundStationARNType(arn) != "tracking" {
		return Failure("InvalidParameterException", "trackingConfigArn must name a tracking config.")
	}
	edges, _ := input["dataflowEdges"].([]any)
	for _, item := range edges {
		edge, _ := item.([]any)
		for _, end := range edge {
			if arn, _ := end.(string); groundStationARNType(arn) == "" {
				return Failure("InvalidParameterException", "Every dataflow edge must join two config ARNs.")
			}
		}
	}
	return nil
}

var kinesisVideo = &Service{
	Model: "kinesisvideo", Title: "Amazon Kinesis Video Streams", IAMPrefix: "kinesisvideo", Issues: []int{280},
	Resources: []*Resource{
		{Kind: "stream", Shape: "StreamInfo", ID: "name",
			ARN:       "arn:{partition}:kinesisvideo:{region}:{account}:stream/{name}/1",
			ARNMember: "StreamARN", NameMember: "StreamName", Unique: true, Limit: 50, Token: "Version", TokenID: "alnum:20",
			Generate: map[string]any{"Status": "CREATING", "CreationTime": "$now", "DataRetentionInHours": 0, "KmsKeyId": "alias/aws/kinesisvideo"}},
		{Kind: "channel", Shape: "ChannelInfo", ID: "name",
			ARN:       "arn:{partition}:kinesisvideo:{region}:{account}:channel/{name}/1",
			ARNMember: "ChannelARN", NameMember: "ChannelName", Unique: true, Limit: 50, Token: "Version", TokenID: "alnum:20",
			Generate: map[string]any{"ChannelStatus": "CREATING", "ChannelType": "SINGLE_MASTER", "CreationTime": "$now",
				"SingleMasterConfiguration": map[string]any{"MessageTtlSeconds": 60}}},
	},
	Ops: []Op{
		{Name: "CreateStream", Kind: Create, Resource: "stream", Check: kinesisVideoStream},
		{Name: "DescribeStream", Kind: Read, Resource: "stream", Lookup: "StreamName", LookupAlt: []string{"StreamARN"}, Check: kinesisVideoStreamSelector},
		{Name: "ListStreams", Kind: List, Resource: "stream"},
		{Name: "UpdateStream", Kind: Update, Resource: "stream", Lookup: "StreamName", LookupAlt: []string{"StreamARN"}, Check: kinesisVideoStreamSelector, Token: "CurrentVersion",
			Fields: []string{"DeviceName", "MediaType"}},
		{Name: "UpdateDataRetention", Kind: Update, Resource: "stream", Lookup: "StreamName", LookupAlt: []string{"StreamARN"}, Check: kinesisVideoStreamSelector, Token: "CurrentVersion",
			Accept: []string{"Operation", "DataRetentionChangeInHours"}, Merged: kinesisVideoRetention},
		{Name: "DeleteStream", Kind: Delete, Resource: "stream", Lookup: "StreamARN", Token: "CurrentVersion"},
		{Name: "CreateSignalingChannel", Kind: Create, Resource: "channel", Check: kinesisVideoChannel},
		{Name: "DescribeSignalingChannel", Kind: Read, Resource: "channel", Lookup: "ChannelName", LookupAlt: []string{"ChannelARN"}},
		{Name: "ListSignalingChannels", Kind: List, Resource: "channel"},
		{Name: "UpdateSignalingChannel", Kind: Update, Resource: "channel", Lookup: "ChannelARN", Token: "CurrentVersion",
			Fields: []string{"SingleMasterConfiguration"}},
		{Name: "DeleteSignalingChannel", Kind: Delete, Resource: "channel", Lookup: "ChannelARN", Token: "CurrentVersion"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Streams and signaling channels are stored records: no media is ingested, stored or played, and no WebRTC signaling, relay or ICE server exists. GetDataEndpoint, GetSignalingChannelEndpoint, the edge agent, media storage, image generation, notification, stream storage and mapped resource configuration APIs return UnknownOperationException with HTTP 501, so no endpoint is ever returned.",
		"Streams and signaling channels are stored records that stay CREATING and never become ACTIVE, because nothing is provisioned. The ARN ends in the fixed number 1 instead of the creation timestamp AWS uses, so a deleted and recreated name has the same ARN. A stream created without a KmsKeyId reports the alias alias/aws/kinesisvideo instead of the ARN of that managed key; the key is not resolved. StreamStorageConfiguration is refused.",
		"Updates require CurrentVersion, which must equal the stored Version or the request fails with VersionMismatchException; every successful update gives the resource a new Version. UpdateDataRetention adds or subtracts the change from the retention in hours, which must stay from 0 to 87600. DeleteStream and DeleteSignalingChannel delete the latest version, or refuse with VersionMismatchException a CurrentVersion that is not the stored Version; ListStreams and ListSignalingChannels refuse the name condition. Only SINGLE_MASTER channels can be created.",
		"Tags are read and changed through TagResource, UntagResource and ListTagsForResource by ARN; TagStream, UntagStream and ListTagsForStream are not bound. A tag count above 50 reached through TagResource fails with ClientLimitExceededException and a channel count above the simulator limit fails the same way, because the engine cannot name the more specific modeled exceptions.",
	},
}

// kinesisVideoStreamSelector rejects a request with no stream identifier.
func kinesisVideoStreamSelector(input map[string]any) error {
	name, _ := input["StreamName"].(string)
	arn, _ := input["StreamARN"].(string)
	if name == "" && arn == "" {
		return Failure("InvalidArgumentException", "StreamName or StreamARN is required.")
	}
	return nil
}

// kinesisVideoStream applies the documented retention range.
func kinesisVideoStream(input map[string]any) error {
	if hours, ok := Number(input["DataRetentionInHours"]); ok && hours > 87600 {
		return Failure("InvalidArgumentException", "DataRetentionInHours must be from 0 to 87600.")
	}
	if _, ok := input["StreamStorageConfiguration"]; ok {
		return Failure("InvalidArgumentException", "StreamStorageConfiguration is not supported by the simulator.")
	}
	return nil
}

// kinesisVideoChannel accepts SINGLE_MASTER, the only channel type the API
// documents as supported.
func kinesisVideoChannel(input map[string]any) error {
	if kind, ok := input["ChannelType"].(string); ok && kind != "SINGLE_MASTER" {
		return Failure("InvalidArgumentException", "SINGLE_MASTER is the only supported channel type.")
	}
	return nil
}

// kinesisVideoRetention applies an increase or decrease of the retention
// period; the engine has already compared CurrentVersion.
func kinesisVideoRetention(document, supplied map[string]any) error {
	current, _ := Number(document["DataRetentionInHours"])
	change, _ := Number(supplied["DataRetentionChangeInHours"])
	switch supplied["Operation"] {
	case "INCREASE_DATA_RETENTION":
		current += change
	case "DECREASE_DATA_RETENTION":
		current -= change
	}
	if current < 0 || current > 87600 {
		return Failure("InvalidArgumentException", "The data retention period must stay from 0 to 87600 hours.")
	}
	document["DataRetentionInHours"] = int64(current)
	return nil
}
