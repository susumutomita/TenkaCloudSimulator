package catalog

import "net"

// Reviewed bounded specifications for AWS Elemental MediaConnect, AWS Elemental
// MediaLive and Amazon Interactive Video Service. Each binds only official
// operations whose effect is a stored configuration record: no flow, channel,
// input, stage or room carries media, no ingest or playback endpoint is
// created, and no stream key, token or other secret is generated or returned.

var mediaConnect = &Service{
	Model: "mediaconnect", Title: "AWS Elemental MediaConnect", IAMPrefix: "mediaconnect", Issues: []int{220},
	Resources: []*Resource{
		{Kind: "gateway", Shape: "Gateway", Summary: "ListedGateway", ID: "alnum:12",
			ARN:       "arn:{partition}:mediaconnect:{region}:{account}:gateway:{id}:{name}",
			ARNMember: "GatewayArn", NameMember: "Name", Limit: 20,
			Generate: map[string]any{"GatewayState": "ACTIVE", "GatewayMessages": []any{}}},
		{Kind: "routerNetworkInterface", Shape: "RouterNetworkInterface", Summary: "ListedRouterNetworkInterface", ID: "alnum:12",
			ARN:       "arn:{partition}:mediaconnect:{region}:{account}:routerNetworkInterface:{id}",
			ARNMember: "Arn", NameMember: "Name", IDMember: "Id", Limit: 20,
			Generate: map[string]any{"State": "ACTIVE", "AssociatedOutputCount": 0, "AssociatedInputCount": 0, "RegionName": "$region",
				"CreatedAt": "$now", "UpdatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateGateway", Kind: Create, Resource: "gateway", Check: mediaConnectGateway},
		{Name: "DescribeGateway", Kind: Read, Resource: "gateway", Lookup: "GatewayArn"},
		{Name: "ListGateways", Kind: List, Resource: "gateway"},
		{Name: "DeleteGateway", Kind: Delete, Resource: "gateway", Lookup: "GatewayArn"},
		{Name: "CreateRouterNetworkInterface", Kind: Create, Resource: "routerNetworkInterface", Check: mediaConnectNetworkInterface},
		{Name: "GetRouterNetworkInterface", Kind: Read, Resource: "routerNetworkInterface", Lookup: "Arn"},
		{Name: "ListRouterNetworkInterfaces", Kind: List, Resource: "routerNetworkInterface"},
		{Name: "UpdateRouterNetworkInterface", Kind: Update, Resource: "routerNetworkInterface", Lookup: "Arn", Touch: []string{"UpdatedAt"},
			Fields: []string{"Name", "Configuration"}, Check: mediaConnectNetworkInterface},
		{Name: "DeleteRouterNetworkInterface", Kind: Delete, Resource: "routerNetworkInterface", Lookup: "Arn"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
		{Name: "TagGlobalResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagGlobalResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForGlobalResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Gateways and router network interfaces are stored records: no flow, bridge, gateway instance, router input or router output exists, nothing is ingested or delivered, and no endpoint, IP address or thumbnail is generated. Gateways are ACTIVE and router network interfaces ACTIVE at once; nothing registers an on-premises instance with a gateway. Flows, bridges, router inputs and router outputs, the Add*, Remove*, Grant*, Revoke* and Update*Entitlement/MediaStream/Source/Output operations, Start*, Stop*, Restart*, Take*, source metadata and thumbnail reads, gateway instances, offerings and reservations (purchases) all return UnknownOperationException with HTTP 501.",
		"A flow cannot be stored honestly because its required source carries an AWS-generated source ARN and ingest address, a bridge list entry needs a BridgeType value the model does not enumerate, and router inputs and outputs are themselves ingest and delivery endpoints, so none of these kinds is bound. Gateway networks and egress CIDR blocks must be IPv4 CIDR ranges and are not contacted; gateways have no update operation and are not checked against bridges because bridges are not stored.",
		"RouterNetworkInterface NetworkInterfaceType is derived from the configuration (public or vpc); VPC subnets and security groups are stored without being resolved, and RegionName must be the request Region. TagResource and TagGlobalResource, and their Untag and ListTags counterparts, address the same stored tags. Limits per Region are simulator limits, not AWS quotas.",
	},
}

// mediaConnectGateway requires IPv4 CIDR ranges for the gateway egress and
// its networks.
func mediaConnectGateway(input map[string]any) error {
	blocks, _ := input["EgressCidrBlocks"].([]any)
	for _, item := range blocks {
		if !ipv4CIDR(item) {
			return Failure("BadRequestException", "EgressCidrBlocks must be IPv4 CIDR ranges.")
		}
	}
	networks, _ := input["Networks"].([]any)
	for _, item := range networks {
		network, _ := item.(map[string]any)
		if !ipv4CIDR(network["CidrBlock"]) {
			return Failure("BadRequestException", "Network CidrBlock values must be IPv4 CIDR ranges.")
		}
	}
	return nil
}

// ipv4CIDR reports whether a value is an IPv4 CIDR range.
func ipv4CIDR(value any) bool {
	text, _ := value.(string)
	ip, network, err := net.ParseCIDR(text)
	if err != nil || ip.To4() == nil {
		return false
	}
	// To4 accepts IPv4-mapped IPv6 addresses, but their CIDR mask is 128-bit.
	_, bits := network.Mask.Size()
	return bits == net.IPv4len*8
}

// mediaConnectNetworkInterface derives the interface type from its configuration
// and requires the request Region.
func mediaConnectNetworkInterface(input map[string]any) error {
	if name, ok := input["RegionName"].(string); ok {
		if name != "us-east-1" && name != "ap-northeast-1" {
			return Failure("BadRequestException", "RegionName must be a Region the simulator serves.")
		}
		delete(input, "RegionName")
	}
	if configuration, ok := input["Configuration"].(map[string]any); ok {
		_, public := configuration["Public"]
		_, vpc := configuration["Vpc"]
		switch {
		case public == vpc:
			return Failure("BadRequestException", "Configuration must hold exactly one of Public or Vpc.")
		case public:
			input["NetworkInterfaceType"] = "PUBLIC"
		default:
			input["NetworkInterfaceType"] = "VPC"
		}
		if public, _ := configuration["Public"].(map[string]any); public != nil {
			return cidrRules(public["AllowRules"], "Cidr", "Allow rules")
		}
	}
	return nil
}

var mediaLive = &Service{
	Model: "medialive", Title: "AWS Elemental MediaLive", IAMPrefix: "medialive", Issues: []int{222},
	Resources: []*Resource{
		{Kind: "inputSecurityGroup", Shape: "InputSecurityGroup", ID: "alnum:7",
			ARN:       "arn:{partition}:medialive:{region}:{account}:inputSecurityGroup:{id}",
			ARNMember: "Arn", IDMember: "Id", Limit: 5,
			Generate: map[string]any{"State": "IDLE", "Inputs": []any{}}},
		{Kind: "network", Shape: "DescribeNetworkResponse", Summary: "DescribeNetworkSummary", ID: "alnum:7",
			ARN:       "arn:{partition}:medialive:{region}:{account}:network:{id}",
			ARNMember: "Arn", NameMember: "Name", IDMember: "Id", Limit: 20,
			Generate: map[string]any{"State": "IDLE", "AssociatedClusterIds": []any{}}},
		{Kind: "sdiSource", Shape: "SdiSource", Summary: "SdiSourceSummary", ID: "alnum:7",
			ARN:       "arn:{partition}:medialive:{region}:{account}:sdiSource:{id}",
			ARNMember: "Arn", NameMember: "Name", IDMember: "Id", Limit: 20,
			Generate: map[string]any{"State": "IDLE", "Inputs": []any{}}},
		{Kind: "channel", Shape: "Channel", Summary: "ChannelSummary", ID: "alnum:7",
			ARN:       "arn:{partition}:medialive:{region}:{account}:channel:{id}",
			ARNMember: "Arn", NameMember: "Name", IDMember: "Id", Limit: 20,
			Generate: map[string]any{"State": "IDLE", "PipelinesRunningCount": 0, "ChannelClass": "STANDARD", "EgressEndpoints": []any{}}},
	},
	Ops: []Op{
		{Name: "CreateInputSecurityGroup", Kind: Create, Resource: "inputSecurityGroup", Check: mediaLiveWhitelist},
		{Name: "DescribeInputSecurityGroup", Kind: Read, Resource: "inputSecurityGroup", Lookup: "InputSecurityGroupId"},
		{Name: "ListInputSecurityGroups", Kind: List, Resource: "inputSecurityGroup"},
		{Name: "UpdateInputSecurityGroup", Kind: Update, Resource: "inputSecurityGroup", Lookup: "InputSecurityGroupId",
			Fields: []string{"WhitelistRules"}, Check: mediaLiveWhitelist},
		{Name: "DeleteInputSecurityGroup", Kind: Delete, Resource: "inputSecurityGroup", Lookup: "InputSecurityGroupId"},
		{Name: "CreateNetwork", Kind: Create, Resource: "network", Check: mediaLiveNetwork},
		{Name: "DescribeNetwork", Kind: Read, Resource: "network", Lookup: "NetworkId"},
		{Name: "ListNetworks", Kind: List, Resource: "network"},
		{Name: "UpdateNetwork", Kind: Update, Resource: "network", Lookup: "NetworkId", Fields: []string{"Name", "IpPools", "Routes"}, Check: mediaLiveNetwork},
		{Name: "DeleteNetwork", Kind: Delete, Resource: "network", Lookup: "NetworkId"},
		{Name: "CreateSdiSource", Kind: Create, Resource: "sdiSource"},
		{Name: "DescribeSdiSource", Kind: Read, Resource: "sdiSource", Lookup: "SdiSourceId"},
		{Name: "ListSdiSources", Kind: List, Resource: "sdiSource"},
		{Name: "UpdateSdiSource", Kind: Update, Resource: "sdiSource", Lookup: "SdiSourceId", Fields: []string{"Name", "Mode", "Type"}},
		{Name: "DeleteSdiSource", Kind: Delete, Resource: "sdiSource", Lookup: "SdiSourceId"},
		{Name: "CreateChannel", Kind: Create, Resource: "channel", Check: mediaLiveChannel},
		{Name: "DescribeChannel", Kind: Read, Resource: "channel", Lookup: "ChannelId"},
		{Name: "ListChannels", Kind: List, Resource: "channel"},
		{Name: "UpdateChannel", Kind: Update, Resource: "channel", Lookup: "ChannelId", Check: mediaLiveChannel,
			Fields: []string{"CdiInputSpecification", "Destinations", "EncoderSettings", "InputSpecification", "LogLevel", "Maintenance", "Name", "RoleArn", "ChannelEngineVersion"}},
		{Name: "DeleteChannel", Kind: Delete, Resource: "channel", Lookup: "ChannelId"},
		{Name: "CreateTags", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "DeleteTags", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Input security groups, networks, SDI sources and channels are stored records: a channel stays IDLE with no pipeline, encoder, egress endpoint or output, and no input, device, cluster or multiplex exists. StartChannel, StopChannel, RestartChannelPipelines, UpdateChannelClass, schedule actions, inputs and partner inputs (their push destinations and pull credentials), input devices, clusters, nodes, channel placement groups, multiplexes, signal maps, the CloudWatch alarm and EventBridge rule templates and groups (whose group template counts the catalog cannot maintain), offerings and reservations (purchases), thumbnails, alerts, account configuration and ListVersions return UnknownOperationException with HTTP 501.",
		"Channels cannot attach inputs, so InputAttachments, Vpc, AnywhereSettings, Reserved and DryRun true are refused. Any stream name, user name or password parameter anywhere in Destinations or EncoderSettings is refused because the catalog stores no stream key or credential reference; the official model requires none of the channel members, so a channel with no encoder settings is accepted where AWS demands them. Role ARNs and S3 or RTMP destination URLs are stored without being resolved or contacted.",
		"Input security groups are IDLE with no inputs and accept IPv4 CIDR whitelist rules; UpdateInputSecurityGroup changes the rules and refuses Tags (use CreateTags and DeleteTags). Networks are IDLE with no associated clusters, and their CIDRs must be IPv4 ranges. Update operations replace the supplied members whole. Limits per Region are simulator limits, not AWS quotas.",
	},
}

// cidrRules requires an IPv4 CIDR range in the named member of each rule.
func cidrRules(items any, member, label string) error {
	list, _ := items.([]any)
	for _, item := range list {
		rule, _ := item.(map[string]any)
		if !ipv4CIDR(rule[member]) {
			return Failure("BadRequestException", label+" must be IPv4 CIDR ranges.")
		}
	}
	return nil
}

func mediaLiveWhitelist(input map[string]any) error {
	return cidrRules(input["WhitelistRules"], "Cidr", "Whitelist rules")
}

func mediaLiveNetwork(input map[string]any) error {
	if err := cidrRules(input["IpPools"], "Cidr", "IP pools"); err != nil {
		return err
	}
	return cidrRules(input["Routes"], "Cidr", "Routes")
}

// mediaLiveChannelRefused lists channel input members whose targets (inputs,
// VPC network interfaces, clusters) the catalog does not store.
var mediaLiveChannelRefused = []string{"InputAttachments", "Vpc", "AnywhereSettings", "Reserved"}

// mediaLiveSecrets are members that name a stream key or a credential.
var mediaLiveSecrets = map[string]bool{"StreamName": true, "PasswordParam": true, "Username": true}

func mediaLiveChannel(input map[string]any) error {
	for _, member := range mediaLiveChannelRefused {
		if _, ok := input[member]; ok {
			return Failure("BadRequestException", member+" is not supported: the simulator stores no input, VPC or cluster for a channel to use.")
		}
	}
	if input["DryRun"] == true {
		return Failure("BadRequestException", "DryRun is not supported.")
	}
	delete(input, "DryRun")
	if hasSecretMember(input) {
		return Failure("BadRequestException", "Stream names, user names and password parameters are not stored by the simulator.")
	}
	return nil
}

// hasSecretMember reports a stream key or credential member at any depth.
func hasSecretMember(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if mediaLiveSecrets[key] || hasSecretMember(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if hasSecretMember(item) {
				return true
			}
		}
	}
	return false
}

// Amazon Interactive Video Service is three official models. ivs and
// ivs-realtime share the signing name "ivs"; their routes differ except for the
// tag routes, which no model binds. The official resourceArn pattern of all
// three models, [a-z-]/[a-zA-Z0-9-]+, admits only a one-character resource
// type, so no real ARN passes input validation and the tag operations stay
// unbound; tags are accepted and returned at creation only.

var ivs = &Service{
	Model: "ivs", Title: "Amazon Interactive Video Service", IAMPrefix: "ivs", Issues: []int{225},
	Resources: []*Resource{
		{Kind: "playbackRestrictionPolicy", Shape: "PlaybackRestrictionPolicy", Summary: "PlaybackRestrictionPolicySummary", ID: "alnum:12",
			ARN:       "arn:{partition}:ivs:{region}:{account}:playback-restriction-policy/{id}",
			ARNMember: "arn", NameMember: "name", Limit: 100,
			Generate: map[string]any{"enableStrictOriginEnforcement": false}},
		{Kind: "recordingConfiguration", Shape: "RecordingConfiguration", Summary: "RecordingConfigurationSummary", ID: "alnum:12",
			ARN:       "arn:{partition}:ivs:{region}:{account}:recording-configuration/{id}",
			ARNMember: "arn", NameMember: "name", Limit: 20,
			Generate: map[string]any{"state": "ACTIVE", "recordingReconnectWindowSeconds": 0}},
	},
	Ops: []Op{
		{Name: "CreatePlaybackRestrictionPolicy", Kind: Create, Resource: "playbackRestrictionPolicy"},
		{Name: "GetPlaybackRestrictionPolicy", Kind: Read, Resource: "playbackRestrictionPolicy", Lookup: "arn"},
		{Name: "ListPlaybackRestrictionPolicies", Kind: List, Resource: "playbackRestrictionPolicy"},
		{Name: "UpdatePlaybackRestrictionPolicy", Kind: Update, Resource: "playbackRestrictionPolicy", Lookup: "arn",
			Fields: []string{"allowedCountries", "allowedOrigins", "enableStrictOriginEnforcement", "name"}},
		{Name: "DeletePlaybackRestrictionPolicy", Kind: Delete, Resource: "playbackRestrictionPolicy", Lookup: "arn"},
		{Name: "CreateRecordingConfiguration", Kind: Create, Resource: "recordingConfiguration", Check: ivsRecording},
		{Name: "GetRecordingConfiguration", Kind: Read, Resource: "recordingConfiguration", Lookup: "arn"},
		{Name: "ListRecordingConfigurations", Kind: List, Resource: "recordingConfiguration"},
		{Name: "DeleteRecordingConfiguration", Kind: Delete, Resource: "recordingConfiguration", Lookup: "arn"},
	},
	Bounds: []string{
		"Playback restriction policies and recording configurations are stored records: no channel, stream key, stream, stream session or recording exists, no ingest or playback endpoint is created, nothing is written to Amazon S3, and a recording configuration is ACTIVE at once. CreateChannel and the other channel and stream key operations are not bound because the official CreateChannel output carries a stream key, a secret the catalog never generates; ImportPlaybackKeyPair and the playback key pair operations are not bound because they store key material. GetStream, GetStreamSession, ListStreams, ListStreamSessions, StopStream, PutMetadata and the viewer session revocation operations return UnknownOperationException with HTTP 501.",
		"A recording configuration needs an S3 destination; the bucket is stored by name and is not looked up or written to, and an in-use configuration is never refused deletion because no channel can reference it. Rendition and thumbnail settings are stored without producing renditions or thumbnails. Update operations replace the supplied members whole; limits per Region are simulator limits, not AWS quotas.",
		"TagResource, UntagResource and ListTagsForResource are not bound: the official resourceArn pattern admits only a one-character resource type, so no real IVS ARN passes the catalog's input validation. Tags can be set at creation and read from the resource.",
	},
}

// ivsRecording requires the S3 destination the official model offers.
func ivsRecording(input map[string]any) error {
	destination, _ := input["destinationConfiguration"].(map[string]any)
	if _, ok := destination["s3"]; !ok {
		return Failure("ValidationException", "destinationConfiguration must name an S3 bucket.")
	}
	return nil
}

var ivsChat = &Service{
	Model: "ivschat", Title: "Amazon Interactive Video Service Chat", IAMPrefix: "ivschat", Issues: []int{225},
	Resources: []*Resource{
		{Kind: "room", Shape: "GetRoomResponse", Summary: "RoomSummary", ID: "alnum:12",
			ARN:       "arn:{partition}:ivschat:{region}:{account}:room/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "id", Limit: 100,
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "maximumMessageRatePerSecond": 10, "maximumMessageLength": 500,
				"loggingConfigurationIdentifiers": []any{}}},
		{Kind: "loggingConfiguration", Shape: "GetLoggingConfigurationResponse", Summary: "LoggingConfigurationSummary", ID: "alnum:12",
			ARN:       "arn:{partition}:ivschat:{region}:{account}:logging-configuration/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "id", Limit: 10,
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "state": "ACTIVE"}},
	},
	Ops: []Op{
		{Name: "CreateRoom", Kind: Create, Resource: "room"},
		{Name: "GetRoom", Kind: Read, Resource: "room", Lookup: "identifier"},
		{Name: "ListRooms", Kind: List, Resource: "room", Filters: map[string]string{"name": "name"}},
		{Name: "UpdateRoom", Kind: Update, Resource: "room", Lookup: "identifier", Touch: []string{"updateTime"},
			Fields: []string{"name", "maximumMessageRatePerSecond", "maximumMessageLength", "messageReviewHandler", "loggingConfigurationIdentifiers"}},
		{Name: "DeleteRoom", Kind: Delete, Resource: "room", Lookup: "identifier"},
		{Name: "CreateLoggingConfiguration", Kind: Create, Resource: "loggingConfiguration", Check: ivsChatDestination},
		{Name: "GetLoggingConfiguration", Kind: Read, Resource: "loggingConfiguration", Lookup: "identifier"},
		{Name: "ListLoggingConfigurations", Kind: List, Resource: "loggingConfiguration"},
		{Name: "UpdateLoggingConfiguration", Kind: Update, Resource: "loggingConfiguration", Lookup: "identifier", Touch: []string{"updateTime"},
			Fields: []string{"name", "destinationConfiguration"}, Check: ivsChatDestination},
		{Name: "DeleteLoggingConfiguration", Kind: Delete, Resource: "loggingConfiguration", Lookup: "identifier"},
	},
	Bounds: []string{
		"Chat rooms and logging configurations are stored records: no chat connection, message or user exists and no message is logged to the destination. CreateChatToken (which mints a credential), SendEvent, DeleteMessage and DisconnectUser return UnknownOperationException with HTTP 501.",
		"A logging configuration is ACTIVE at once and its CloudWatch Logs, Firehose or S3 destination is stored by name without being looked up. loggingConfigurationIdentifiers of a room are stored without checking that the configurations exist, and deleting a logging configuration does not check the rooms that name it. The message review handler Lambda function is stored without being invoked. TagResource, UntagResource and ListTagsForResource are not bound because the official resourceArn pattern admits no real ARN; tags are set at creation only. ListRooms filters only by name; messageReviewHandlerUri and loggingConfigurationIdentifier filters are refused. Limits per Region are simulator limits, not AWS quotas.",
	},
}

// ivsChatDestination requires exactly one destination, as the official
// documentation states.
func ivsChatDestination(input map[string]any) error {
	destination, ok := input["destinationConfiguration"].(map[string]any)
	if ok && len(destination) != 1 {
		return Failure("ValidationException", "destinationConfiguration must name exactly one of s3, cloudWatchLogs or firehose.")
	}
	return nil
}

var ivsRealtime = &Service{
	Model: "ivs-realtime", Title: "Amazon Interactive Video Service RealTime", IAMPrefix: "ivs", Issues: []int{225},
	Resources: []*Resource{
		{Kind: "encoderConfiguration", Shape: "EncoderConfiguration", Summary: "EncoderConfigurationSummary", ID: "alnum:12",
			ARN:       "arn:{partition}:ivs:{region}:{account}:encoder-configuration/{id}",
			ARNMember: "arn", NameMember: "name", Limit: 20},
		{Kind: "storageConfiguration", Shape: "StorageConfiguration", Summary: "StorageConfigurationSummary", ID: "alnum:12",
			ARN:       "arn:{partition}:ivs:{region}:{account}:storage-configuration/{id}",
			ARNMember: "arn", NameMember: "name", Limit: 20},
		{Kind: "stage", Shape: "Stage", Summary: "StageSummary", ID: "alnum:12",
			ARN:       "arn:{partition}:ivs:{region}:{account}:stage/{id}",
			ARNMember: "arn", NameMember: "name", Limit: 20},
	},
	Ops: []Op{
		{Name: "CreateEncoderConfiguration", Kind: Create, Resource: "encoderConfiguration"},
		{Name: "GetEncoderConfiguration", Kind: Read, Resource: "encoderConfiguration", Lookup: "arn"},
		{Name: "ListEncoderConfigurations", Kind: List, Resource: "encoderConfiguration"},
		{Name: "DeleteEncoderConfiguration", Kind: Delete, Resource: "encoderConfiguration", Lookup: "arn"},
		{Name: "CreateStorageConfiguration", Kind: Create, Resource: "storageConfiguration"},
		{Name: "GetStorageConfiguration", Kind: Read, Resource: "storageConfiguration", Lookup: "arn"},
		{Name: "ListStorageConfigurations", Kind: List, Resource: "storageConfiguration"},
		{Name: "DeleteStorageConfiguration", Kind: Delete, Resource: "storageConfiguration", Lookup: "arn"},
		{Name: "CreateStage", Kind: Create, Resource: "stage", Check: ivsRealtimeStage},
		{Name: "GetStage", Kind: Read, Resource: "stage", Lookup: "arn"},
		{Name: "ListStages", Kind: List, Resource: "stage"},
		{Name: "UpdateStage", Kind: Update, Resource: "stage", Lookup: "arn", Fields: []string{"name", "autoParticipantRecordingConfiguration"}},
		{Name: "DeleteStage", Kind: Delete, Resource: "stage", Lookup: "arn"},
	},
	Bounds: []string{
		"Encoder configurations, storage configurations and stages are stored records: no participant, session, composition or recording exists, a stage reports no activeSessionId and none of its WHIP, RTMP or events endpoints, and no S3 bucket is looked up or written to. CreateStage refuses participantTokenConfigurations because the catalog mints no participant token, so it never returns participantTokens; CreateParticipantToken and DisconnectParticipant, participants, stage sessions, compositions and participant replication return UnknownOperationException with HTTP 501.",
		"Ingest configurations (their create output carries a stream key) and public keys (they store key material) are not bound. The tag operations are not bound for the reason given for ivs (the official resourceArn pattern admits no real ARN), so tags can be set at creation but not changed afterwards. The storage configuration named by a stage's autoParticipantRecordingConfiguration is stored without being checked, and a storage configuration in use by a stage can be deleted. Limits per Region are simulator limits, not AWS quotas.",
	},
}

// ivsRealtimeStage refuses participant tokens, which the catalog never mints.
func ivsRealtimeStage(input map[string]any) error {
	if _, ok := input["participantTokenConfigurations"]; ok {
		return Failure("ValidationException", "participantTokenConfigurations is not supported: the simulator mints no participant token.")
	}
	return nil
}
