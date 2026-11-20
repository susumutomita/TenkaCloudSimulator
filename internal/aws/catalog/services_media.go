package catalog

import (
	"strconv"
	"strings"
)

// Reviewed bounded specifications for AWS Elemental media services. Each binds
// only official operations whose effect is configuration metadata: no media is
// transcoded, ingested, packaged, stitched or delivered, and no ingest,
// playback or manifest URL, credential or key is generated.

var mediaConvert = &Service{
	Model: "mediaconvert", Title: "AWS Elemental MediaConvert", IAMPrefix: "mediaconvert", Issues: []int{221},
	Resources: []*Resource{
		{Kind: "queue", Shape: "Queue", ID: "name", ARN: "arn:{partition}:mediaconvert:{region}:{account}:queues/{name}",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 10,
			Generate: map[string]any{"CreatedAt": "$now", "LastUpdated": "$now", "PricingPlan": "ON_DEMAND", "Status": "ACTIVE",
				"Type": "CUSTOM", "ProgressingJobsCount": 0, "SubmittedJobsCount": 0}},
		{Kind: "jobTemplate", Shape: "JobTemplate", ID: "name", ARN: "arn:{partition}:mediaconvert:{region}:{account}:jobTemplates/{name}",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 100, Refs: map[string]string{"Queue": "queue"},
			Generate: map[string]any{"CreatedAt": "$now", "LastUpdated": "$now", "Type": "CUSTOM"}},
		{Kind: "preset", Shape: "Preset", ID: "name", ARN: "arn:{partition}:mediaconvert:{region}:{account}:presets/{name}",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"CreatedAt": "$now", "LastUpdated": "$now", "Type": "CUSTOM"}},
		{Kind: "policy", Shape: "Policy", Singleton: true, ID: "name",
			ARN: "arn:{partition}:mediaconvert:{region}:{account}:policy", NameMember: "HttpInputs"},
	},
	Ops: []Op{
		{Name: "CreateQueue", Kind: Create, Resource: "queue", Check: mediaConvertQueue},
		{Name: "GetQueue", Kind: Read, Resource: "queue", Lookup: "Name"},
		{Name: "ListQueues", Kind: List, Resource: "queue"},
		{Name: "UpdateQueue", Kind: Update, Resource: "queue", Lookup: "Name", Touch: []string{"LastUpdated"}, Check: mediaConvertQueue,
			Fields: []string{"ConcurrentJobs", "Description", "ReservationPlanSettings", "Status"}},
		{Name: "DeleteQueue", Kind: Delete, Resource: "queue", Lookup: "Name"},
		{Name: "CreateJobTemplate", Kind: Create, Resource: "jobTemplate", Check: mediaConvertUntagged},
		{Name: "GetJobTemplate", Kind: Read, Resource: "jobTemplate", Lookup: "Name"},
		{Name: "ListJobTemplates", Kind: List, Resource: "jobTemplate", Filters: map[string]string{"Category": "Category"}},
		{Name: "UpdateJobTemplate", Kind: Update, Resource: "jobTemplate", Lookup: "Name", Touch: []string{"LastUpdated"},
			Fields: []string{"AccelerationSettings", "Category", "Description", "HopDestinations", "Priority", "Queue", "Settings", "StatusUpdateInterval"}},
		{Name: "DeleteJobTemplate", Kind: Delete, Resource: "jobTemplate", Lookup: "Name"},
		{Name: "CreatePreset", Kind: Create, Resource: "preset", Check: mediaConvertUntagged},
		{Name: "GetPreset", Kind: Read, Resource: "preset", Lookup: "Name"},
		{Name: "ListPresets", Kind: List, Resource: "preset", Filters: map[string]string{"Category": "Category"}},
		{Name: "UpdatePreset", Kind: Update, Resource: "preset", Lookup: "Name", Touch: []string{"LastUpdated"}, Fields: []string{"Category", "Description", "Settings"}},
		{Name: "DeletePreset", Kind: Delete, Resource: "preset", Lookup: "Name"},
		{Name: "GetPolicy", Kind: Read, Resource: "policy"},
		{Name: "PutPolicy", Kind: Put, Resource: "policy", From: "Policy"},
		{Name: "DeletePolicy", Kind: Delete, Resource: "policy"},
	},
	Bounds: []string{
		"Queues, job templates, presets and the account policy are stored records: no transcoding job, endpoint, certificate or resource share exists, and no input is read from or output written to Amazon S3, HTTP or HTTPS. CreateJob, GetJob, ListJobs, SearchJobs, CancelJob, StartJobsQuery, GetJobsQueryResults, DescribeEndpoints, Probe, AssociateCertificate, DisassociateCertificate, CreateResourceShare and ListVersions return UnknownOperationException with HTTP 501.",
		"Queues are ON_DEMAND, CUSTOM and ACTIVE or PAUSED with zero submitted and progressing jobs; PricingPlan RESERVED and ReservationPlanSettings are refused because a reservation is a purchase. The account's Default queue and the system presets and job templates that AWS owns are not served, so a job template Queue must name a queue created here, and the queues named in HopDestinations are stored without being resolved. Every custom queue is limited to 10 per Region and every job template or preset to 100 by the simulator, which are not AWS quotas.",
		"The tag operations are not implemented because MediaConvert reads tags only through ListTagsForResource, whose nested ResourceTags output the catalog cannot render: TagResource, UntagResource and ListTagsForResource return HTTP 501, and Tags at creation are refused instead of being stored unreadable. The list members ListBy and Order are refused and listing returns creation order; Category filters job templates and presets.",
		"GetPolicy before PutPolicy returns an empty policy instead of the account default, and the stored policy limits no input. PutPolicy replaces only the members it supplies, and DeletePolicy fails with NotFoundException (HTTP 404) when no policy was stored.",
	},
}

// mediaConvertUntagged refuses tags that no implemented operation could return.
func mediaConvertUntagged(input map[string]any) error {
	if tags, _ := input["Tags"].(map[string]any); len(tags) > 0 {
		return Failure("BadRequestException", "The simulator does not implement the MediaConvert tag APIs, so tags cannot be stored.")
	}
	delete(input, "Tags")
	return nil
}

// mediaConvertQueue refuses reserved queues, which are bought with a
// twelve-month commitment, and queue tags.
func mediaConvertQueue(input map[string]any) error {
	if input["PricingPlan"] == "RESERVED" || input["ReservationPlanSettings"] != nil {
		return Failure("BadRequestException", "Reserved queues are a purchase and are not available in the simulator.")
	}
	return mediaConvertUntagged(input)
}

var mediaPackage = &Service{
	Model: "mediapackage", Title: "AWS Elemental MediaPackage", IAMPrefix: "mediapackage", Issues: []int{223},
	Resources: []*Resource{
		{Kind: "channel", Shape: "Channel", ID: "name", ARN: "arn:{partition}:mediapackage:{region}:{account}:channels/{id}",
			ARNMember: "Arn", NameMember: "Id", Unique: true, Limit: 100,
			Generate: map[string]any{"CreatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateChannel", Kind: Create, Resource: "channel"},
		{Name: "DescribeChannel", Kind: Read, Resource: "channel", Lookup: "Id"},
		{Name: "ListChannels", Kind: List, Resource: "channel"},
		{Name: "UpdateChannel", Kind: Update, Resource: "channel", Lookup: "Id", Fields: []string{"Description"}},
		{Name: "DeleteChannel", Kind: Delete, Resource: "channel", Lookup: "Id"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Channels are stored records: nothing is ingested, packaged or delivered, no HLS ingest endpoint, ingest URL, username or password is issued, so HlsIngest is never returned, and no access log is written. Origin endpoints are not served because DescribeOriginEndpoint, UpdateOriginEndpoint and DeleteOriginEndpoint identify an endpoint without naming its channel, which the catalog cannot check against a live channel; CreateOriginEndpoint, DescribeOriginEndpoint, ListOriginEndpoints, UpdateOriginEndpoint and DeleteOriginEndpoint return UnknownOperationException with HTTP 501, as do ConfigureLogs, RotateChannelCredentials, RotateIngestEndpointCredentials and the harvest job operations (which would copy content to Amazon S3).",
		"Channel IDs are unique per Region and cannot change; Description is the only editable member. The official model gives no ID pattern, so none is enforced. A duplicate ID answers ConflictException (HTTP 409) and invalid input answers ValidationException (HTTP 400), neither of which the official operations model; AWS answers UnprocessableEntityException (HTTP 422). CreatedAt is an RFC 3339 time. The simulator allows 100 channels per Region, which is not an AWS quota.",
	},
}

var mediaPackageV2 = &Service{
	Model: "mediapackagev2", Title: "AWS Elemental MediaPackage v2", IAMPrefix: "mediapackagev2", Issues: []int{223},
	Resources: []*Resource{
		{Kind: "channelGroup", Shape: "GetChannelGroupResponse", Summary: "ChannelGroupListConfiguration", ID: "name",
			ARN:       "arn:{partition}:mediapackagev2:{region}:{account}:channelGroup/{name}",
			ARNMember: "Arn", NameMember: "ChannelGroupName", Unique: true, Limit: 100,
			Generate: map[string]any{"CreatedAt": "$now", "ModifiedAt": "$now"}},
		{Kind: "channel", Shape: "GetChannelResponse", Summary: "ChannelListConfiguration", ID: "name", Parent: "channelGroup",
			ARN:       "arn:{partition}:mediapackagev2:{region}:{account}:channelGroup/{parentName}/channel/{name}",
			ARNMember: "Arn", NameMember: "ChannelName", Unique: true, Limit: 100,
			Generate: map[string]any{"ChannelGroupName": "$parentName", "CreatedAt": "$now", "ModifiedAt": "$now", "InputType": "HLS"}},
	},
	Ops: []Op{
		{Name: "CreateChannelGroup", Kind: Create, Resource: "channelGroup"},
		{Name: "GetChannelGroup", Kind: Read, Resource: "channelGroup", Lookup: "ChannelGroupName"},
		{Name: "ListChannelGroups", Kind: List, Resource: "channelGroup"},
		{Name: "UpdateChannelGroup", Kind: Update, Resource: "channelGroup", Lookup: "ChannelGroupName", Touch: []string{"ModifiedAt"}, Fields: []string{"Description"}},
		{Name: "DeleteChannelGroup", Kind: Delete, Resource: "channelGroup", Lookup: "ChannelGroupName"},
		{Name: "CreateChannel", Kind: Create, Resource: "channel", Parent: "ChannelGroupName", Check: mediaPackageV2Channel},
		{Name: "GetChannel", Kind: Read, Resource: "channel", Parent: "ChannelGroupName", Lookup: "ChannelName"},
		{Name: "ListChannels", Kind: List, Resource: "channel", Parent: "ChannelGroupName"},
		{Name: "UpdateChannel", Kind: Update, Resource: "channel", Parent: "ChannelGroupName", Lookup: "ChannelName", Touch: []string{"ModifiedAt"},
			Fields: []string{"Description", "InputSwitchConfiguration", "OutputHeaderConfiguration"},
			Merged: func(document, _ map[string]any) error { return mediaPackageV2Channel(document) }},
		{Name: "DeleteChannel", Kind: Delete, Resource: "channel", Parent: "ChannelGroupName", Lookup: "ChannelName"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Channel groups and channels are stored records: nothing is ingested, packaged or delivered, and no egress domain, ingest endpoint or URL is issued, so EgressDomain and IngestEndpoints are never returned. Origin endpoints are not served because they sit a third level below the channel group, which the catalog cannot address; CreateOriginEndpoint, GetOriginEndpoint, ListOriginEndpoints, UpdateOriginEndpoint, DeleteOriginEndpoint, ResetOriginEndpointState and the origin endpoint policy operations return UnknownOperationException with HTTP 501. So do the channel policy operations (the policy would be a third-level child of a channel), ResetChannelState and the harvest job operations (which copy content to Amazon S3).",
		"Channel groups and channels are identified by name. A channel group with channels cannot be deleted (ConflictException, HTTP 409). Channels are created and edited within a group; InputType defaults to HLS and cannot change, and MQCSInputSwitching and PublishMQCS require the CMAF input type. No ETag is generated, so ETag on UpdateChannelGroup or UpdateChannel is refused. The simulator allows 100 channel groups per Region and 100 channels per group, which are not AWS quotas.",
	},
}

// mediaPackageV2Channel applies the documented rule that media quality
// confidence score (MQCS) settings are valid only for the CMAF input type.
func mediaPackageV2Channel(input map[string]any) error {
	if input["InputType"] == "CMAF" {
		return nil
	}
	switching, _ := input["InputSwitchConfiguration"].(map[string]any)
	header, _ := input["OutputHeaderConfiguration"].(map[string]any)
	if switching["MQCSInputSwitching"] == true || header["PublishMQCS"] == true {
		return Failure("ValidationException", "MQCSInputSwitching and PublishMQCS are valid only when InputType is CMAF.")
	}
	return nil
}

var mediaPackageVOD = &Service{
	Model: "mediapackage-vod", Title: "AWS Elemental MediaPackage VOD", IAMPrefix: "mediapackage-vod", Issues: []int{223},
	Resources: []*Resource{
		{Kind: "packagingGroup", Shape: "PackagingGroup", ID: "name", ARN: "arn:{partition}:mediapackage-vod:{region}:{account}:packaging-groups/{id}",
			ARNMember: "Arn", NameMember: "Id", Unique: true, Limit: 100,
			Generate: map[string]any{"CreatedAt": "$now", "ApproximateAssetCount": 0}},
	},
	Ops: []Op{
		{Name: "CreatePackagingGroup", Kind: Create, Resource: "packagingGroup", Check: mediaPackageVODAuthorization},
		{Name: "DescribePackagingGroup", Kind: Read, Resource: "packagingGroup", Lookup: "Id"},
		{Name: "ListPackagingGroups", Kind: List, Resource: "packagingGroup"},
		{Name: "UpdatePackagingGroup", Kind: Update, Resource: "packagingGroup", Lookup: "Id", Fields: []string{"Authorization"}, Check: mediaPackageVODAuthorization},
		{Name: "DeletePackagingGroup", Kind: Delete, Resource: "packagingGroup", Lookup: "Id"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Packaging groups are stored records: no asset is ingested or packaged and no domain name is issued, so DomainName is never returned and ApproximateAssetCount stays 0. Packaging configurations are not served because DescribePackagingConfiguration and DeletePackagingConfiguration identify one without naming its group, which the catalog cannot check against a live group; CreatePackagingConfiguration, DescribePackagingConfiguration, ListPackagingConfigurations and DeletePackagingConfiguration return UnknownOperationException with HTTP 501, as do the asset operations (CreateAsset ingests content from Amazon S3, and an asset needs a packaging group and configuration) and ConfigureLogs.",
		"Authorization is stored without reading the secret or assuming the role: CdnIdentifierSecret must be a Secrets Manager secret ARN and SecretsRoleArn an IAM role ARN. Only Authorization can change after creation. Group IDs are unique per Region; a duplicate ID answers ConflictException (HTTP 409) and invalid input ValidationException (HTTP 400), neither of which the official operations model. EgressAccessLogs names a log group that is never written. The simulator allows 100 packaging groups per Region, which is not an AWS quota.",
	},
}

// mediaPackageVODAuthorization requires the documented ARN kinds for CDN
// authorization: a Secrets Manager secret and an IAM role.
func mediaPackageVODAuthorization(input map[string]any) error {
	authorization, ok := input["Authorization"].(map[string]any)
	if !ok {
		return nil
	}
	secret, _ := authorization["CdnIdentifierSecret"].(string)
	role, _ := authorization["SecretsRoleArn"].(string)
	if !strings.HasPrefix(secret, "arn:aws:secretsmanager:") || !strings.Contains(secret, ":secret:") {
		return Failure("ValidationException", "CdnIdentifierSecret must be the ARN of a Secrets Manager secret.")
	}
	if !iamRoleARN.MatchString(role) {
		return Failure("ValidationException", "SecretsRoleArn must be an IAM role ARN.")
	}
	return nil
}

var mediaTailor = &Service{
	Model: "mediatailor", Title: "AWS Elemental MediaTailor", IAMPrefix: "mediatailor", Issues: []int{224},
	Resources: []*Resource{
		{Kind: "playbackConfiguration", Shape: "PlaybackConfiguration", ID: "name",
			ARN:       "arn:{partition}:mediatailor:{region}:{account}:playbackConfiguration/{name}",
			ARNMember: "PlaybackConfigurationArn", NameMember: "Name", Unique: true, Limit: 100},
		{Kind: "prefetchSchedule", Shape: "PrefetchSchedule", ID: "name", Parent: "playbackConfiguration",
			ARN:       "arn:{partition}:mediatailor:{region}:{account}:prefetchSchedule/{parentName}/{name}",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"PlaybackConfigurationName": "$parentName"}},
		{Kind: "sourceLocation", Shape: "SourceLocation", ID: "name", ARN: "arn:{partition}:mediatailor:{region}:{account}:sourceLocation/{name}",
			ARNMember: "Arn", NameMember: "SourceLocationName", Unique: true, Limit: 100,
			Generate: map[string]any{"CreationTime": "$now", "LastModifiedTime": "$now"}},
		{Kind: "vodSource", Shape: "VodSource", ID: "name", Parent: "sourceLocation",
			ARN:       "arn:{partition}:mediatailor:{region}:{account}:vodSource/{parentName}/{name}",
			ARNMember: "Arn", NameMember: "VodSourceName", Unique: true, Limit: 100,
			Generate: map[string]any{"SourceLocationName": "$parentName", "CreationTime": "$now", "LastModifiedTime": "$now"}},
		{Kind: "liveSource", Shape: "LiveSource", ID: "name", Parent: "sourceLocation",
			ARN:       "arn:{partition}:mediatailor:{region}:{account}:liveSource/{parentName}/{name}",
			ARNMember: "Arn", NameMember: "LiveSourceName", Unique: true, Limit: 100,
			Generate: map[string]any{"SourceLocationName": "$parentName", "CreationTime": "$now", "LastModifiedTime": "$now"}},
		{Kind: "channel", Shape: "DescribeChannelResponse", Summary: "Channel", ID: "name", ARN: "arn:{partition}:mediatailor:{region}:{account}:channel/{name}",
			ARNMember: "Arn", NameMember: "ChannelName", Unique: true, Limit: 100,
			Generate: map[string]any{"ChannelState": "STOPPED", "CreationTime": "$now", "LastModifiedTime": "$now",
				"LogConfiguration": map[string]any{"LogTypes": []any{}}}},
	},
	Ops: []Op{
		{Name: "PutPlaybackConfiguration", Kind: Put, Resource: "playbackConfiguration", Lookup: "Name", Check: mediaTailorPlayback},
		{Name: "GetPlaybackConfiguration", Kind: Read, Resource: "playbackConfiguration", Lookup: "Name"},
		{Name: "ListPlaybackConfigurations", Kind: List, Resource: "playbackConfiguration"},
		{Name: "DeletePlaybackConfiguration", Kind: Delete, Resource: "playbackConfiguration", Lookup: "Name"},
		{Name: "CreatePrefetchSchedule", Kind: Create, Resource: "prefetchSchedule", Parent: "PlaybackConfigurationName", Check: mediaTailorPrefetch},
		{Name: "GetPrefetchSchedule", Kind: Read, Resource: "prefetchSchedule", Parent: "PlaybackConfigurationName", Lookup: "Name"},
		{Name: "ListPrefetchSchedules", Kind: List, Resource: "prefetchSchedule", Parent: "PlaybackConfigurationName",
			Filters: map[string]string{"ScheduleType": "ScheduleType", "StreamId": "StreamId"}},
		{Name: "DeletePrefetchSchedule", Kind: Delete, Resource: "prefetchSchedule", Parent: "PlaybackConfigurationName", Lookup: "Name"},
		{Name: "CreateSourceLocation", Kind: Create, Resource: "sourceLocation", Check: mediaTailorSourceLocation},
		{Name: "DescribeSourceLocation", Kind: Read, Resource: "sourceLocation", Lookup: "SourceLocationName"},
		{Name: "ListSourceLocations", Kind: List, Resource: "sourceLocation"},
		{Name: "UpdateSourceLocation", Kind: Update, Resource: "sourceLocation", Lookup: "SourceLocationName", Touch: []string{"LastModifiedTime"}, Check: mediaTailorSourceLocation,
			Fields: []string{"AccessConfiguration", "DefaultSegmentDeliveryConfiguration", "HttpConfiguration", "SegmentDeliveryConfigurations"}},
		{Name: "DeleteSourceLocation", Kind: Delete, Resource: "sourceLocation", Lookup: "SourceLocationName"},
		{Name: "CreateVodSource", Kind: Create, Resource: "vodSource", Parent: "SourceLocationName"},
		{Name: "DescribeVodSource", Kind: Read, Resource: "vodSource", Parent: "SourceLocationName", Lookup: "VodSourceName"},
		{Name: "ListVodSources", Kind: List, Resource: "vodSource", Parent: "SourceLocationName"},
		{Name: "UpdateVodSource", Kind: Update, Resource: "vodSource", Parent: "SourceLocationName", Lookup: "VodSourceName", Touch: []string{"LastModifiedTime"},
			Fields: []string{"HttpPackageConfigurations"}},
		{Name: "DeleteVodSource", Kind: Delete, Resource: "vodSource", Parent: "SourceLocationName", Lookup: "VodSourceName"},
		{Name: "CreateLiveSource", Kind: Create, Resource: "liveSource", Parent: "SourceLocationName"},
		{Name: "DescribeLiveSource", Kind: Read, Resource: "liveSource", Parent: "SourceLocationName", Lookup: "LiveSourceName"},
		{Name: "ListLiveSources", Kind: List, Resource: "liveSource", Parent: "SourceLocationName"},
		{Name: "UpdateLiveSource", Kind: Update, Resource: "liveSource", Parent: "SourceLocationName", Lookup: "LiveSourceName", Touch: []string{"LastModifiedTime"},
			Fields: []string{"HttpPackageConfigurations"}},
		{Name: "DeleteLiveSource", Kind: Delete, Resource: "liveSource", Parent: "SourceLocationName", Lookup: "LiveSourceName"},
		{Name: "CreateChannel", Kind: Create, Resource: "channel", Check: mediaTailorChannel},
		{Name: "DescribeChannel", Kind: Read, Resource: "channel", Lookup: "ChannelName"},
		{Name: "ListChannels", Kind: List, Resource: "channel"},
		{Name: "UpdateChannel", Kind: Update, Resource: "channel", Lookup: "ChannelName", Touch: []string{"LastModifiedTime"}, Check: mediaTailorChannel,
			Fields: []string{"Audiences", "FillerSlate", "Outputs", "TimeShiftConfiguration"},
			Merged: func(document, _ map[string]any) error { return mediaTailorSlate(document) }},
		{Name: "DeleteChannel", Kind: Delete, Resource: "channel", Lookup: "ChannelName"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Playback configurations, prefetch schedules, source locations, VOD and live sources and channels are stored records: no manifest is stitched, no ad decision server, origin or Secrets Manager secret is contacted, no ad is prefetched and no playback or session endpoint is issued, so HlsConfiguration, PlaybackEndpointPrefix, SessionInitializationEndpointPrefix and DashConfiguration ManifestEndpointPrefix are never returned, and output PlaybackUrl is omitted. Channels stay STOPPED and StartChannel and StopChannel return UnknownOperationException with HTTP 501, as do programs (which refer to source locations and sources by name, which the catalog cannot check), the channel policy operations, ConfigureLogsForChannel, ConfigureLogsForPlaybackConfiguration, GetChannelSchedule and ListAlerts.",
		"PutPlaybackConfiguration replaces only the members it supplies instead of the whole configuration. A playback configuration with prefetch schedules, and a source location with VOD or live sources, cannot be deleted. Slate and FillerSlate references, TranscodeProfileName, secret ARNs and the VOD source group names are stored without being resolved. LINEAR channels require FillerSlate and LOOP channels refuse it; Tier is returned only when supplied; PlaybackMode and Tier cannot change; Outputs are stored without a PlaybackUrl, and LogConfiguration reports no log types.",
		"The official operations model no exceptions, so failures answer with the generic names ValidationException (HTTP 400), ResourceNotFoundException (HTTP 404) and ConflictException (HTTP 409), where AWS answers BadRequestException. The simulator allows 100 of each resource kind per scope, which is not an AWS quota.",
	},
}

// mediaTailorURLLimit is the documented maximum length of ad decision server
// URLs.
const mediaTailorURLLimit = 25000

// mediaTailorPlayback applies the documented URL length limits.
func mediaTailorPlayback(input map[string]any) error {
	if url, _ := input["AdDecisionServerUrl"].(string); len(url) > mediaTailorURLLimit {
		return Failure("ValidationException", "AdDecisionServerUrl is at most 25,000 characters.")
	}
	if pre, _ := input["LivePreRollConfiguration"].(map[string]any); pre != nil {
		if url, _ := pre["AdDecisionServerUrl"].(string); len(url) > mediaTailorURLLimit {
			return Failure("ValidationException", "The pre-roll AdDecisionServerUrl is at most 25,000 characters.")
		}
	}
	if url, _ := input["VideoContentSourceUrl"].(string); len(url) > 512 {
		return Failure("ValidationException", "VideoContentSourceUrl is at most 512 characters.")
	}
	return nil
}

// mediaTailorPrefetch requires the recurring settings of recurring schedules.
func mediaTailorPrefetch(input map[string]any) error {
	if input["ScheduleType"] == "RECURRING" && input["RecurringPrefetchConfiguration"] == nil {
		return Failure("ValidationException", "RECURRING prefetch schedules require RecurringPrefetchConfiguration.")
	}
	return nil
}

// mediaTailorSourceLocation requires the protocol in base URLs, as the
// official members document, and unique segment delivery configuration names.
func mediaTailorSourceLocation(input map[string]any) error {
	absolute := func(url string) bool { return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") }
	if http, _ := input["HttpConfiguration"].(map[string]any); http != nil {
		if url, _ := http["BaseUrl"].(string); !absolute(url) {
			return Failure("ValidationException", "HttpConfiguration BaseUrl must include the protocol, such as https://.")
		}
	}
	if def, _ := input["DefaultSegmentDeliveryConfiguration"].(map[string]any); def != nil {
		if url, _ := def["BaseUrl"].(string); url != "" && !absolute(url) {
			return Failure("ValidationException", "DefaultSegmentDeliveryConfiguration BaseUrl must be an absolute URL.")
		}
	}
	names := map[string]bool{}
	configs, _ := input["SegmentDeliveryConfigurations"].([]any)
	for _, item := range configs {
		config, _ := item.(map[string]any)
		if url, _ := config["BaseUrl"].(string); url != "" && !absolute(url) && !strings.HasPrefix(url, "/") {
			return Failure("ValidationException", "A segment delivery BaseUrl must be an absolute URL or a relative path starting with /.")
		}
		if name, _ := config["Name"].(string); name != "" {
			if names[name] {
				return Failure("ValidationException", "Segment delivery configuration names must be unique within a source location.")
			}
			names[name] = true
		}
	}
	return nil
}

// mediaTailorChannel applies the documented playlist and time shift ranges
// and the filler slate rule of the playback modes.
func mediaTailorChannel(input map[string]any) error {
	ranges := map[string][2]int64{"ManifestWindowSeconds": {30, 3600}, "MinBufferTimeSeconds": {2, 60}, "MinUpdatePeriodSeconds": {2, 60},
		"SuggestedPresentationDelaySeconds": {2, 60}}
	outputs, _ := input["Outputs"].([]any)
	for _, item := range outputs {
		output, _ := item.(map[string]any)
		for _, settings := range []string{"DashPlaylistSettings", "HlsPlaylistSettings"} {
			playlist, _ := output[settings].(map[string]any)
			for member, bounds := range ranges {
				if value, ok := Number(playlist[member]); ok && (value < float64(bounds[0]) || value > float64(bounds[1])) {
					return Failure("ValidationException", member+" must be between "+strconv.FormatInt(bounds[0], 10)+" and "+strconv.FormatInt(bounds[1], 10)+" seconds.")
				}
			}
		}
	}
	if shift, _ := input["TimeShiftConfiguration"].(map[string]any); shift != nil {
		if value, ok := Number(shift["MaxTimeDelaySeconds"]); ok && (value < 0 || value > 21600) {
			return Failure("ValidationException", "MaxTimeDelaySeconds must be between 0 and 21600 seconds.")
		}
	}
	if _, creating := input["PlaybackMode"]; creating {
		return mediaTailorSlate(input)
	}
	return nil
}

// mediaTailorSlate requires a filler slate for LINEAR channels and refuses
// one for LOOP channels.
func mediaTailorSlate(document map[string]any) error {
	slate := document["FillerSlate"] != nil
	switch document["PlaybackMode"] {
	case "LINEAR":
		if !slate {
			return Failure("ValidationException", "A LINEAR channel requires FillerSlate.")
		}
	case "LOOP":
		if slate {
			return Failure("ValidationException", "A LOOP channel does not support FillerSlate.")
		}
	}
	return nil
}
