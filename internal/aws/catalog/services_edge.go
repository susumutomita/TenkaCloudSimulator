package catalog

import (
	"encoding/json"
	"net"
	"regexp"
	"strings"
)

// Reviewed bounded specifications for end-user computing, game technology and
// IoT services. Each binds only official operations whose effect is
// configuration metadata: no desktop, application stream, game server, game
// session, device connection, MQTT message, telemetry or deployment is
// provisioned, and no external system is contacted.

var workSpaces = &Service{
	Model: "workspaces", Title: "Amazon WorkSpaces", IAMPrefix: "workspaces", Issues: []int{142, 144},
	Resources: []*Resource{
		{Kind: "ipGroup", Shape: "WorkspacesIpGroup", ID: "alnum:9:wsipg-",
			ARN:        "arn:{partition}:workspaces:{region}:{account}:workspaceipgroup/{id}",
			NameMember: "groupName", IDMember: "groupId", Unique: true, Limit: 100,
			Aliases:  map[string]string{"GroupName": "groupName", "GroupDesc": "groupDesc", "UserRules": "userRules"},
			Generate: map[string]any{"GroupId": "$id", "userRules": []any{}}},
		{Kind: "connectionAlias", Shape: "ConnectionAlias", ID: "alnum:9:wsca-",
			ARN:        "arn:{partition}:workspaces:{region}:{account}:connectionalias/{id}",
			NameMember: "ConnectionString", IDMember: "AliasId", Unique: true, Limit: 20,
			Generate: map[string]any{"State": "CREATED", "OwnerAccountId": "$account", "Associations": []any{}}},
	},
	Ops: []Op{
		{Name: "CreateIpGroup", Kind: Create, Resource: "ipGroup", Check: workSpacesIPGroup},
		{Name: "DescribeIpGroups", Kind: List, Resource: "ipGroup", Names: "GroupIds"},
		{Name: "UpdateRulesOfIpGroup", Kind: Update, Resource: "ipGroup", Lookup: "GroupId", Fields: []string{"UserRules"}, Check: workSpacesIPGroup},
		{Name: "DeleteIpGroup", Kind: Delete, Resource: "ipGroup", Lookup: "GroupId"},
		{Name: "CreateConnectionAlias", Kind: Create, Resource: "connectionAlias", Check: workSpacesNoTags},
		{Name: "DescribeConnectionAliases", Kind: List, Resource: "connectionAlias", Names: "AliasIds"},
		{Name: "DeleteConnectionAlias", Kind: Delete, Resource: "connectionAlias", Lookup: "AliasId"},
	},
	Bounds: []string{
		"IP access control groups and connection aliases are configuration metadata: no WorkSpace, WorkSpaces Core instance, directory, bundle, image or pool is provisioned, no client traffic is filtered and no connection string is resolved. CreateWorkspaces, RegisterWorkspaceDirectory, CreateWorkspacesPool, ImportWorkspaceImage and the other provisioning and streaming APIs return UnknownOperationException with HTTP 501.",
		"Connection aliases are CREATED at once and cannot be associated, shared or redirected; DescribeConnectionAliases ResourceId is refused. UpdateRulesOfIpGroup replaces the whole rule list, every rule must be an IPv4 address or CIDR range, and AuthorizeIpRules, RevokeIpRules and AssociateIpGroups return HTTP 501.",
		"CreateTags, DeleteTags and DescribeTags address resources by identifier rather than ARN and are not implemented, so Tags at creation are refused instead of being stored unreadable. DescribeIpGroups and DescribeConnectionAliases fail with ResourceNotFoundException (HTTP 400), which they do not model, when a requested identifier does not exist; generic input validation failures use ValidationException because InvalidParameterValuesException is not a catalog validation name.",
	},
}

// workSpacesIPGroup requires IPv4 addresses or CIDR ranges in IP access
// control rules, as the official rule format documents.
func workSpacesIPGroup(input map[string]any) error {
	if err := workSpacesNoTags(input); err != nil {
		return err
	}
	rules, _ := input["UserRules"].([]any)
	for _, item := range rules {
		rule, _ := item.(map[string]any)
		text, _ := rule["ipRule"].(string)
		if ip := net.ParseIP(text); ip != nil && ip.To4() != nil {
			continue
		}
		if ip, _, err := net.ParseCIDR(text); err == nil && ip.To4() != nil {
			continue
		}
		return Failure("InvalidParameterValuesException", "Each IP rule must be an IPv4 address or CIDR range.")
	}
	return nil
}

// workSpacesNoTags refuses tags that no implemented operation could return.
func workSpacesNoTags(input map[string]any) error {
	if tags, _ := input["Tags"].([]any); len(tags) > 0 {
		return Failure("InvalidParameterValuesException", "The simulator does not implement the WorkSpaces tag APIs, so tags cannot be stored.")
	}
	delete(input, "Tags")
	return nil
}

var appStream = &Service{
	Model: "appstream", Title: "Amazon WorkSpaces Applications", IAMPrefix: "appstream", Issues: []int{143},
	Resources: []*Resource{
		{Kind: "stack", Shape: "Stack", ID: "name", ARN: "arn:{partition}:appstream:{region}:{account}:stack/{name}",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"CreatedTime": "$now", "StackErrors": []any{}}},
		{Kind: "appBlock", Shape: "AppBlock", ID: "name", ARN: "arn:{partition}:appstream:{region}:{account}:app-block/{name}",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"CreatedTime": "$now", "State": "INACTIVE", "PackagingType": "CUSTOM", "AppBlockErrors": []any{}}},
		{Kind: "application", Shape: "Application", ID: "name", ARN: "arn:{partition}:appstream:{region}:{account}:application/{name}",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 100, Refs: map[string]string{"AppBlockArn": "appBlock"},
			Generate: map[string]any{"CreatedTime": "$now", "Enabled": true, "Metadata": map[string]any{}}},
	},
	Ops: []Op{
		{Name: "CreateStack", Kind: Create, Resource: "stack"},
		{Name: "DescribeStacks", Kind: List, Resource: "stack", Names: "Names"},
		{Name: "UpdateStack", Kind: Update, Resource: "stack", Lookup: "Name",
			Fields: []string{"DisplayName", "Description", "StorageConnectors", "RedirectURL", "FeedbackURL", "UserSettings", "ApplicationSettings",
				"AccessEndpoints", "EmbedHostDomains", "StreamingExperienceSettings"}},
		{Name: "DeleteStack", Kind: Delete, Resource: "stack", Lookup: "Name"},
		{Name: "CreateAppBlock", Kind: Create, Resource: "appBlock", Check: appStreamSetupScript},
		{Name: "DescribeAppBlocks", Kind: List, Resource: "appBlock", Names: "Arns"},
		{Name: "DeleteAppBlock", Kind: Delete, Resource: "appBlock", Lookup: "Name"},
		{Name: "CreateApplication", Kind: Create, Resource: "application"},
		{Name: "DescribeApplications", Kind: List, Resource: "application", Names: "Arns"},
		{Name: "UpdateApplication", Kind: Update, Resource: "application", Lookup: "Name",
			Fields: []string{"DisplayName", "Description", "IconS3Location", "LaunchPath", "WorkingDirectory", "LaunchParameters", "AppBlockArn"}},
		{Name: "DeleteApplication", Kind: Delete, Resource: "application", Lookup: "Name"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Stacks, app blocks and applications are configuration metadata: no fleet, image, image builder, app block builder, streaming instance, streaming URL, user or session exists. CreateFleet, CreateImageBuilder, CreateStreamingURL, AssociateFleet and the other provisioning and streaming APIs return UnknownOperationException with HTTP 501.",
		"App blocks stay INACTIVE and no virtual hard disk, setup script or icon is read from Amazon S3; CUSTOM app blocks require SetupScriptDetails and applications report no IconURL. Storage connectors, access endpoints, embed host domains and application settings are stored without creating buckets, endpoints or identity provider links.",
		"UpdateStack DeleteStorageConnectors and AttributesToDelete, and UpdateApplication AttributesToDelete, are refused. Applications must reference a live app block, but deleting an app block does not check the applications that reference it. Entitlements, directory configurations (which hold service account passwords), themes and usage report subscriptions return HTTP 501.",
	},
}

// appStreamSetupScript applies the documented rule that CUSTOM app blocks,
// the default packaging type, need setup script details.
func appStreamSetupScript(input map[string]any) error {
	if _, ok := input["SetupScriptDetails"]; !ok && input["PackagingType"] != "APPSTREAM2" {
		return Failure("InvalidParameterCombinationException", "SetupScriptDetails must be provided for the CUSTOM packaging type.")
	}
	return nil
}

var thinClient = &Service{
	Model: "workspaces-thin-client", Title: "Amazon WorkSpaces Thin Client", IAMPrefix: "thinclient", Issues: []int{145},
	Resources: []*Resource{
		{Kind: "environment", Shape: "Environment", Summary: "EnvironmentSummary", ID: "alnum:9",
			ARN:       "arn:{partition}:thinclient:{region}:{account}:environment/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "id", Limit: 50,
			Generate: map[string]any{"createdAt": "$now", "updatedAt": "$now", "registeredDevicesCount": 0, "softwareSetComplianceStatus": "NO_REGISTERED_DEVICES"}},
	},
	Ops: []Op{
		{Name: "CreateEnvironment", Kind: Create, Resource: "environment", Check: thinClientEnvironment},
		{Name: "GetEnvironment", Kind: Read, Resource: "environment", Lookup: "id"},
		{Name: "ListEnvironments", Kind: List, Resource: "environment"},
		{Name: "UpdateEnvironment", Kind: Update, Resource: "environment", Lookup: "id", Touch: []string{"updatedAt"}, Check: thinClientEnvironment,
			Fields: []string{"name", "desktopArn", "desktopType", "desktopEndpoint", "softwareSetUpdateSchedule", "maintenanceWindow",
				"softwareSetUpdateMode", "desiredSoftwareSetId", "deviceCreationTags"}},
		{Name: "DeleteEnvironment", Kind: Delete, Resource: "environment", Lookup: "id", Accept: []string{"clientToken"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Environments are configuration metadata: no device is activated, registered or updated and no activation code is issued, so registeredDevicesCount stays 0. Devices and software sets are not implemented: GetDevice, ListDevices, DeregisterDevice, ListSoftwareSets and UpdateSoftwareSet return UnknownOperationException with HTTP 501.",
		"desktopArn must be a WorkSpaces directory, WorkSpaces Applications stack or WorkSpaces Secure Browser portal ARN; desktopType is derived from it, but the desktop and its endpoint are neither looked up nor contacted. desiredSoftwareSetId and the USE_DESIRED update mode are refused because no software set exists; KMS keys are stored without resolving them.",
		"CUSTOM maintenance windows require start and end hours and minutes. Delete client tokens are accepted but not recorded. SDK clients must disable host-prefix injection (botocore inject_host_prefix=False) because the official api. host prefix cannot address the loopback endpoint.",
	},
}

// thinClientDesktops maps the service of a desktop ARN to its desktop type
// and required resource type.
var thinClientDesktops = map[string][2]string{
	"workspaces":     {"workspaces", "directory/"},
	"appstream":      {"appstream", "stack/"},
	"workspaces-web": {"workspaces-web", "portal/"},
}

// thinClientEnvironment derives desktopType from the desktop ARN and refuses
// software sets, which the simulator does not publish.
func thinClientEnvironment(input map[string]any) error {
	if arn, ok := input["desktopArn"].(string); ok {
		parts := strings.SplitN(arn, ":", 6)
		var desktop [2]string
		if len(parts) == 6 {
			desktop = thinClientDesktops[parts[2]]
		}
		if desktop[0] == "" || !strings.HasPrefix(parts[5], desktop[1]) {
			return Failure("ValidationException", "desktopArn must be a WorkSpaces directory, WorkSpaces Applications stack or WorkSpaces Secure Browser portal ARN.")
		}
		input["desktopType"] = desktop[0]
	}
	if _, ok := input["desiredSoftwareSetId"]; ok || input["softwareSetUpdateMode"] == "USE_DESIRED" {
		return Failure("ResourceNotFoundException", "No software set exists in the simulator.")
	}
	if window, ok := input["maintenanceWindow"].(map[string]any); ok && window["type"] == "CUSTOM" {
		for _, member := range []string{"startTimeHour", "startTimeMinute", "endTimeHour", "endTimeMinute"} {
			if _, set := window[member]; !set {
				return Failure("ValidationException", "CUSTOM maintenance windows require start and end hours and minutes.")
			}
		}
	}
	return nil
}

var secureBrowser = &Service{
	Model: "workspaces-web", Title: "Amazon WorkSpaces Secure Browser", IAMPrefix: "workspaces-web", Issues: []int{146},
	Resources: []*Resource{
		{Kind: "portal", Shape: "Portal", Summary: "PortalSummary", ID: "uuid",
			ARN: "arn:{partition}:workspaces-web:{region}:{account}:portal/{id}", ARNMember: "portalArn", Limit: 50,
			Generate: map[string]any{"portalEndpoint": "$fmt:{id}.workspaces-web.com", "portalStatus": "Incomplete", "rendererType": "AppStream",
				"browserType": "Chrome", "creationDate": "$now", "authenticationType": "Standard"}},
		{Kind: "browserSettings", Shape: "BrowserSettings", Summary: "BrowserSettingsSummary", ID: "uuid",
			ARN: "arn:{partition}:workspaces-web:{region}:{account}:browserSettings/{id}", ARNMember: "browserSettingsArn", Limit: 50,
			Generate: map[string]any{"associatedPortalArns": []any{}}},
		{Kind: "networkSettings", Shape: "NetworkSettings", Summary: "NetworkSettingsSummary", ID: "uuid",
			ARN: "arn:{partition}:workspaces-web:{region}:{account}:networkSettings/{id}", ARNMember: "networkSettingsArn", Limit: 50,
			Generate: map[string]any{"associatedPortalArns": []any{}}},
		{Kind: "userSettings", Shape: "UserSettings", Summary: "UserSettingsSummary", ID: "uuid",
			ARN: "arn:{partition}:workspaces-web:{region}:{account}:userSettings/{id}", ARNMember: "userSettingsArn", Limit: 50,
			Generate: map[string]any{"associatedPortalArns": []any{}}},
		{Kind: "ipAccessSettings", Shape: "IpAccessSettings", Summary: "IpAccessSettingsSummary", ID: "uuid",
			ARN: "arn:{partition}:workspaces-web:{region}:{account}:ipAccessSettings/{id}", ARNMember: "ipAccessSettingsArn", Limit: 50,
			Generate: map[string]any{"associatedPortalArns": []any{}, "creationDate": "$now"}},
	},
	Ops: []Op{
		{Name: "CreatePortal", Kind: Create, Resource: "portal"},
		{Name: "GetPortal", Kind: Read, Resource: "portal", Lookup: "portalArn", Check: secureBrowserPortal},
		{Name: "ListPortals", Kind: List, Resource: "portal"},
		{Name: "UpdatePortal", Kind: Update, Resource: "portal", Lookup: "portalArn", Check: secureBrowserPortal,
			Fields: []string{"displayName", "authenticationType", "instanceType", "maxConcurrentSessions"}},
		{Name: "DeletePortal", Kind: Delete, Resource: "portal", Lookup: "portalArn", IdempotentDelete: true, Check: secureBrowserPortal},
		{Name: "CreateBrowserSettings", Kind: Create, Resource: "browserSettings", Check: secureBrowserPolicy},
		{Name: "GetBrowserSettings", Kind: Read, Resource: "browserSettings", Lookup: "browserSettingsArn"},
		{Name: "ListBrowserSettings", Kind: List, Resource: "browserSettings"},
		{Name: "UpdateBrowserSettings", Kind: Update, Resource: "browserSettings", Lookup: "browserSettingsArn", Accept: []string{"clientToken"},
			Fields: []string{"browserPolicy", "webContentFilteringPolicy"}, Check: secureBrowserPolicy},
		{Name: "DeleteBrowserSettings", Kind: Delete, Resource: "browserSettings", Lookup: "browserSettingsArn", IdempotentDelete: true},
		{Name: "CreateNetworkSettings", Kind: Create, Resource: "networkSettings"},
		{Name: "GetNetworkSettings", Kind: Read, Resource: "networkSettings", Lookup: "networkSettingsArn"},
		{Name: "ListNetworkSettings", Kind: List, Resource: "networkSettings"},
		{Name: "UpdateNetworkSettings", Kind: Update, Resource: "networkSettings", Lookup: "networkSettingsArn", Accept: []string{"clientToken"},
			Fields: []string{"vpcId", "subnetIds", "securityGroupIds"}},
		{Name: "DeleteNetworkSettings", Kind: Delete, Resource: "networkSettings", Lookup: "networkSettingsArn", IdempotentDelete: true},
		{Name: "CreateUserSettings", Kind: Create, Resource: "userSettings"},
		{Name: "GetUserSettings", Kind: Read, Resource: "userSettings", Lookup: "userSettingsArn"},
		{Name: "ListUserSettings", Kind: List, Resource: "userSettings"},
		{Name: "UpdateUserSettings", Kind: Update, Resource: "userSettings", Lookup: "userSettingsArn", Accept: []string{"clientToken"},
			Fields: []string{"copyAllowed", "pasteAllowed", "downloadAllowed", "uploadAllowed", "printAllowed", "disconnectTimeoutInMinutes",
				"idleDisconnectTimeoutInMinutes", "cookieSynchronizationConfiguration", "deepLinkAllowed", "toolbarConfiguration"}},
		{Name: "DeleteUserSettings", Kind: Delete, Resource: "userSettings", Lookup: "userSettingsArn", IdempotentDelete: true},
		{Name: "CreateIpAccessSettings", Kind: Create, Resource: "ipAccessSettings"},
		{Name: "GetIpAccessSettings", Kind: Read, Resource: "ipAccessSettings", Lookup: "ipAccessSettingsArn"},
		{Name: "ListIpAccessSettings", Kind: List, Resource: "ipAccessSettings"},
		{Name: "UpdateIpAccessSettings", Kind: Update, Resource: "ipAccessSettings", Lookup: "ipAccessSettingsArn", Accept: []string{"clientToken"},
			Fields: []string{"displayName", "description", "ipRules"}},
		{Name: "DeleteIpAccessSettings", Kind: Delete, Resource: "ipAccessSettings", Lookup: "ipAccessSettingsArn", IdempotentDelete: true},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn", Check: dropClientToken},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Portals and browser, network, user and IP access settings are configuration metadata: portals stay Incomplete, the portal endpoint is not served, and no streaming instance, session, network interface or IAM Identity Center application is created. Sessions, identity providers, trust stores, data protection settings, session loggers and user access logging settings return UnknownOperationException with HTTP 501.",
		"Settings cannot be associated with portals, so associatedPortalArns stays empty and portals name no settings. The official Associate*, Disassociate* and ListIdentityProviders operations below a portal ARN return HTTP 501.",
		"VPCs, subnets, security groups and KMS keys are stored without checking them, and browser policies must be JSON objects. Deleting a missing resource succeeds, as the official delete operations model no not-found error. Update and tagging client tokens are accepted but not recorded.",
	},
}

var portalARN = regexp.MustCompile(`^arn:aws:workspaces-web:[a-z0-9-]+:[0-9]{12}:portal/[0-9a-f-]+$`)

// secureBrowserPortal refuses anything but a whole portal ARN.
func secureBrowserPortal(input map[string]any) error {
	if arn, _ := input["portalArn"].(string); !portalARN.MatchString(arn) {
		return Failure("ValidationException", "The simulator serves only whole portal ARNs; association and identity provider operations are not implemented.")
	}
	return nil
}

// secureBrowserPolicy requires a browser policy to be a JSON object.
func secureBrowserPolicy(input map[string]any) error {
	if text, ok := input["browserPolicy"].(string); ok {
		var policy map[string]any
		if json.Unmarshal([]byte(text), &policy) != nil {
			return Failure("ValidationException", "browserPolicy must be a JSON object.")
		}
	}
	return nil
}

// dropClientToken accepts an idempotency token on an operation whose effect
// is already idempotent; the token is not recorded.
func dropClientToken(input map[string]any) error {
	delete(input, "clientToken")
	return nil
}

var gameLift = &Service{
	Model: "gamelift", Title: "Amazon GameLift Servers", IAMPrefix: "gamelift", Issues: []int{151},
	Resources: []*Resource{
		{Kind: "alias", Shape: "Alias", ID: "hex:32:alias-", ARN: "arn:{partition}:gamelift:{region}:{account}:alias/{id}",
			ARNMember: "AliasArn", NameMember: "Name", IDMember: "AliasId", Limit: 100,
			Generate: map[string]any{"CreationTime": "$now", "LastUpdatedTime": "$now"}},
		{Kind: "location", Shape: "LocationModel", ID: "name", ARN: "arn:{partition}:gamelift:{region}:{account}:location/{name}",
			ARNMember: "LocationArn", NameMember: "LocationName", Unique: true, Limit: 100},
		{Kind: "gameSessionQueue", Shape: "GameSessionQueue", ID: "name", ARN: "arn:{partition}:gamelift:{region}:{account}:gamesessionqueue/{name}",
			ARNMember: "GameSessionQueueArn", NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"TimeoutInSeconds": 600, "Destinations": []any{}, "PlayerLatencyPolicies": []any{}}},
		{Kind: "matchmakingRuleSet", Shape: "MatchmakingRuleSet", ID: "name", ARN: "arn:{partition}:gamelift:{region}:{account}:matchmakingruleset/{name}",
			ARNMember: "RuleSetArn", NameMember: "RuleSetName", Unique: true, Limit: 100,
			Aliases: map[string]string{"Name": "RuleSetName"}, Generate: map[string]any{"CreationTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateAlias", Kind: Create, Resource: "alias", Check: gameLiftRouting},
		{Name: "DescribeAlias", Kind: Read, Resource: "alias", Lookup: "AliasId"},
		{Name: "ListAliases", Kind: List, Resource: "alias", Filters: map[string]string{"Name": "Name"}},
		{Name: "UpdateAlias", Kind: Update, Resource: "alias", Lookup: "AliasId", Fields: []string{"Name", "Description", "RoutingStrategy"},
			Touch: []string{"LastUpdatedTime"}, Check: gameLiftRouting},
		{Name: "DeleteAlias", Kind: Delete, Resource: "alias", Lookup: "AliasId"},
		{Name: "CreateLocation", Kind: Create, Resource: "location"},
		{Name: "ListLocations", Kind: List, Resource: "location"},
		{Name: "DeleteLocation", Kind: Delete, Resource: "location", Lookup: "LocationName"},
		{Name: "CreateGameSessionQueue", Kind: Create, Resource: "gameSessionQueue", Check: gameLiftDestinations},
		{Name: "DescribeGameSessionQueues", Kind: List, Resource: "gameSessionQueue", Names: "Names"},
		{Name: "UpdateGameSessionQueue", Kind: Update, Resource: "gameSessionQueue", Lookup: "Name", Check: gameLiftDestinations,
			Fields: []string{"TimeoutInSeconds", "PlayerLatencyPolicies", "Destinations", "FilterConfiguration", "PriorityConfiguration", "CustomEventData", "NotificationTarget"}},
		{Name: "DeleteGameSessionQueue", Kind: Delete, Resource: "gameSessionQueue", Lookup: "Name"},
		{Name: "CreateMatchmakingRuleSet", Kind: Create, Resource: "matchmakingRuleSet", Check: gameLiftRuleSet},
		{Name: "DescribeMatchmakingRuleSets", Kind: List, Resource: "matchmakingRuleSet", Names: "Names"},
		{Name: "DeleteMatchmakingRuleSet", Kind: Delete, Resource: "matchmakingRuleSet", Lookup: "Name"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Aliases, custom locations, game session queues and FlexMatch rule sets are configuration metadata: no build, script, fleet, container group, compute, game server, game session, player session, placement or matchmaking ticket exists. CreateFleet, CreateGameSession, StartGameSessionPlacement, StartMatchmaking and the other hosting and session APIs return UnknownOperationException with HTTP 501.",
		"Aliases support only TERMINAL routing because no fleet exists; alias identifiers are alias- followed by 32 hexadecimal digits. Game session queues refuse destinations, and notification targets and allowed locations are stored without contacting Amazon SNS or checking locations. ListAliases RoutingStrategyType and ListLocations Filters are refused, and ListLocations lists only custom locations, not AWS Regions or Local Zones.",
		"Rule set bodies must be JSON objects with ruleLanguageVersion and teams; no further FlexMatch validation is performed and rule sets cannot be updated, as in the official API. Matchmaking configurations and ValidateMatchmakingRuleSet return HTTP 501.",
	},
}

// gameLiftRouting allows only terminal aliases, which need no fleet.
func gameLiftRouting(input map[string]any) error {
	strategy, ok := input["RoutingStrategy"].(map[string]any)
	if ok && (strategy["Type"] != "TERMINAL" || strategy["FleetId"] != nil) {
		return Failure("InvalidRequestException", "The simulator has no GameLift fleets, so aliases support only the TERMINAL routing strategy.")
	}
	return nil
}

// gameLiftDestinations refuses queue destinations, which name fleets or
// fleet aliases the simulator cannot place game sessions on.
func gameLiftDestinations(input map[string]any) error {
	if destinations, _ := input["Destinations"].([]any); len(destinations) > 0 {
		return Failure("InvalidRequestException", "Game session queue destinations are not resolved by the simulator.")
	}
	return nil
}

// gameLiftRuleSet requires the documented top-level rule set members.
func gameLiftRuleSet(input map[string]any) error {
	var body map[string]any
	text, _ := input["RuleSetBody"].(string)
	if json.Unmarshal([]byte(text), &body) != nil || body["ruleLanguageVersion"] == nil {
		return Failure("InvalidRequestException", "RuleSetBody must be a JSON object with ruleLanguageVersion and teams.")
	}
	if teams, _ := body["teams"].([]any); len(teams) == 0 {
		return Failure("InvalidRequestException", "RuleSetBody must be a JSON object with ruleLanguageVersion and teams.")
	}
	return nil
}

var gameLiftStreams = &Service{
	Model: "gameliftstreams", Title: "Amazon GameLift Streams", IAMPrefix: "gameliftstreams", Issues: []int{152},
	Resources: []*Resource{
		{Kind: "application", Shape: "GetApplicationOutput", Summary: "ApplicationSummary", ID: "alnum:9:a-",
			ARN:       "arn:{partition}:gameliftstreams:{region}:{account}:application/{id}",
			ARNMember: "Arn", IDMember: "Id", Limit: 50,
			Generate: map[string]any{"Status": "INITIALIZED", "CreatedAt": "$now", "LastUpdatedAt": "$now", "AssociatedStreamGroups": []any{}, "ReplicationStatuses": []any{}}},
	},
	Ops: []Op{
		{Name: "CreateApplication", Kind: Create, Resource: "application", Check: gameLiftStreamsSource},
		{Name: "GetApplication", Kind: Read, Resource: "application", Lookup: "Identifier"},
		{Name: "ListApplications", Kind: List, Resource: "application"},
		{Name: "UpdateApplication", Kind: Update, Resource: "application", Lookup: "Identifier", Touch: []string{"LastUpdatedAt"},
			Fields: []string{"Description", "ApplicationLogPaths", "ApplicationLogOutputUri"}},
		{Name: "DeleteApplication", Kind: Delete, Resource: "application", Lookup: "Identifier"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Applications are configuration metadata that stay INITIALIZED: no content is copied from Amazon S3, replicated, processed or streamed, and nothing is written to ApplicationLogOutputUri. ApplicationSourceUri must be an Amazon S3 URI; runtime environments are stored without checking the supported versions.",
		"Stream groups, stream sessions, their connections, locations and file exports return UnknownOperationException with HTTP 501, so applications are never associated with a stream group.",
	},
}

// gameLiftStreamsSource requires the documented Amazon S3 content location.
func gameLiftStreamsSource(input map[string]any) error {
	if uri, _ := input["ApplicationSourceUri"].(string); !strings.HasPrefix(uri, "s3://") {
		return Failure("ValidationException", "ApplicationSourceUri must be an Amazon S3 URI.")
	}
	return nil
}

var iotCore = &Service{
	Model: "iot", Title: "AWS IoT", IAMPrefix: "iot", Issues: []int{153, 154, 155},
	Resources: []*Resource{
		{Kind: "thing", Shape: "DescribeThingResponse", Summary: "ThingAttribute", ID: "uuid",
			ARN:       "arn:{partition}:iot:{region}:{account}:thing/{name}",
			ARNMember: "thingArn", NameMember: "thingName", IDMember: "thingId", Unique: true, Limit: 500,
			Generate: map[string]any{"defaultClientId": "$name", "version": 1, "attributes": map[string]any{}}},
		{Kind: "thingType", Shape: "DescribeThingTypeResponse", Summary: "ThingTypeDefinition", ID: "uuid",
			ARN:       "arn:{partition}:iot:{region}:{account}:thingtype/{name}",
			ARNMember: "thingTypeArn", NameMember: "thingTypeName", IDMember: "thingTypeId", Unique: true, Limit: 100,
			Generate: map[string]any{"thingTypeMetadata": map[string]any{"deprecated": false, "creationDate": "$now"}}},
		{Kind: "policy", Shape: "GetPolicyResponse", Summary: "Policy", ID: "name",
			ARN:       "arn:{partition}:iot:{region}:{account}:policy/{name}",
			ARNMember: "policyArn", NameMember: "policyName", Unique: true, Limit: 100,
			Generate: map[string]any{"defaultVersionId": "1", "policyVersionId": "1", "generationId": "$uuid", "creationDate": "$now", "lastModifiedDate": "$now"}},
		{Kind: "thingGroup", Shape: "DescribeThingGroupResponse", Summary: "GroupNameAndArn", ID: "uuid",
			ARN:       "arn:{partition}:iot:{region}:{account}:thinggroup/{name}",
			ARNMember: "thingGroupArn", NameMember: "thingGroupName", IDMember: "thingGroupId", Unique: true, Limit: 100,
			Generate: map[string]any{"groupName": "$name", "groupArn": "$arn", "version": 1,
				"thingGroupMetadata": map[string]any{"rootToParentThingGroups": []any{}, "creationDate": "$now"}}},
		{Kind: "securityProfile", Shape: "DescribeSecurityProfileResponse", Summary: "SecurityProfileIdentifier", ID: "name",
			ARN:       "arn:{partition}:iot:{region}:{account}:securityprofile/{name}",
			ARNMember: "securityProfileArn", NameMember: "securityProfileName", Unique: true, Limit: 100,
			Generate: map[string]any{"name": "$name", "arn": "$arn", "version": 1, "creationDate": "$now", "lastModifiedDate": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateThing", Kind: Create, Resource: "thing", Check: iotThing},
		{Name: "DescribeThing", Kind: Read, Resource: "thing", Lookup: "thingName"},
		{Name: "ListThings", Kind: List, Resource: "thing", Filters: map[string]string{"thingTypeName": "thingTypeName"}},
		{Name: "UpdateThing", Kind: Update, Resource: "thing", Lookup: "thingName", Fields: []string{"thingTypeName", "attributes"},
			Accept: []string{"expectedVersion"}, Check: iotThing},
		{Name: "DeleteThing", Kind: Delete, Resource: "thing", Lookup: "thingName", Accept: []string{"expectedVersion"}, IdempotentDelete: true, Check: iotExpectedVersion},
		{Name: "CreateThingType", Kind: Create, Resource: "thingType"},
		{Name: "DescribeThingType", Kind: Read, Resource: "thingType", Lookup: "thingTypeName"},
		{Name: "ListThingTypes", Kind: List, Resource: "thingType", Filters: map[string]string{"thingTypeName": "thingTypeName"}},
		{Name: "UpdateThingType", Kind: Update, Resource: "thingType", Lookup: "thingTypeName", Fields: []string{"thingTypeProperties"}},
		{Name: "DeleteThingType", Kind: Delete, Resource: "thingType", Lookup: "thingTypeName"},
		{Name: "CreatePolicy", Kind: Create, Resource: "policy", Check: iotPolicyDocument},
		{Name: "GetPolicy", Kind: Read, Resource: "policy", Lookup: "policyName"},
		{Name: "ListPolicies", Kind: List, Resource: "policy"},
		{Name: "DeletePolicy", Kind: Delete, Resource: "policy", Lookup: "policyName"},
		{Name: "CreateThingGroup", Kind: Create, Resource: "thingGroup", Check: iotThingGroup},
		{Name: "DescribeThingGroup", Kind: Read, Resource: "thingGroup", Lookup: "thingGroupName"},
		{Name: "ListThingGroups", Kind: List, Resource: "thingGroup"},
		{Name: "UpdateThingGroup", Kind: Update, Resource: "thingGroup", Lookup: "thingGroupName", Fields: []string{"thingGroupProperties"},
			Accept: []string{"expectedVersion"}, Check: iotThingGroup},
		{Name: "DeleteThingGroup", Kind: Delete, Resource: "thingGroup", Lookup: "thingGroupName", Accept: []string{"expectedVersion"},
			IdempotentDelete: true, Check: iotExpectedVersion},
		{Name: "CreateSecurityProfile", Kind: Create, Resource: "securityProfile", Check: iotSecurityProfile},
		{Name: "DescribeSecurityProfile", Kind: Read, Resource: "securityProfile", Lookup: "securityProfileName"},
		{Name: "ListSecurityProfiles", Kind: List, Resource: "securityProfile"},
		{Name: "UpdateSecurityProfile", Kind: Update, Resource: "securityProfile", Lookup: "securityProfileName", Accept: []string{"expectedVersion"},
			Touch: []string{"lastModifiedDate"}, Check: iotSecurityProfile,
			Fields: []string{"securityProfileDescription", "behaviors", "alertTargets", "additionalMetricsToRetain", "additionalMetricsToRetainV2", "metricsExportConfig"}},
		{Name: "DeleteSecurityProfile", Kind: Delete, Resource: "securityProfile", Lookup: "securityProfileName", Accept: []string{"expectedVersion"},
			IdempotentDelete: true, Check: iotExpectedVersion},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn", Check: iotTaggable},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn", Check: iotTaggable},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn", Check: iotTaggable},
	},
	Bounds: []string{
		"Things, thing types, policies, static thing groups and Device Defender security profiles are registry metadata: no certificate, principal, MQTT connection, device shadow, message, rule, job, audit, detection or alert exists, thing groups hold no things and security profiles have no targets. CreateKeysAndCertificate, AttachPolicy, AddThingToThingGroup, AttachSecurityProfile, CreateTopicRule and the other device, data and job APIs return UnknownOperationException with HTTP 501.",
		"Versions of things, thing groups and security profiles stay 1, so expectedVersion must be 1. Creating an existing name fails with ResourceAlreadyExistsException even when the configuration is identical. Things store thingTypeName without checking that the type exists; billing groups, thing group parents, attribute merging and removeThingType are refused, and empty attribute values remove the attribute.",
		"Thing types cannot be deprecated (DeprecateThingType returns HTTP 501) and are deleted at once without checking things that use them. Policies hold only version 1, policy documents must be JSON objects with a Statement and at most 2048 characters excluding whitespace, and policies are never evaluated or attached. UpdateThingGroup replaces thingGroupProperties as a whole, and UpdateSecurityProfile delete flags are refused.",
		"Things cannot be tagged, as in the official API. ListThings attribute filters, ListThingGroups parent, prefix and recursive filters, ListSecurityProfiles dimension and metric filters, ListPolicies ascendingOrder and ListTagsForResource nextToken are refused. Deleting a missing thing succeeds, as documented, and so does deleting a missing thing group or security profile, whose official operations model no not-found error.",
	},
}

// iotExpectedVersion compares optimistic-locking versions with the single
// version the simulator stores.
func iotExpectedVersion(input map[string]any) error {
	if version, ok := input["expectedVersion"].(int64); ok && version != 1 {
		return Failure("VersionConflictException", "The simulator stores version 1 of every IoT registry resource.")
	}
	return nil
}

// iotAttributes flattens an attribute payload into the stored attribute map,
// dropping empty values (which remove an attribute) and refusing merges.
func iotAttributes(payload map[string]any) (map[string]any, error) {
	if merge, _ := payload["merge"].(bool); merge {
		return nil, Failure("InvalidRequestException", "Attribute merging is not supported by the simulator; send the complete attribute map.")
	}
	attributes := map[string]any{}
	values, _ := payload["attributes"].(map[string]any)
	for key, value := range values {
		if value != "" {
			attributes[key] = value
		}
	}
	return attributes, nil
}

// iotThing converts the attribute payload of thing writes into the stored
// attributes member and refuses references the simulator does not model.
func iotThing(input map[string]any) error {
	if err := iotExpectedVersion(input); err != nil {
		return err
	}
	if _, ok := input["billingGroupName"]; ok {
		return Failure("InvalidRequestException", "Billing groups are not implemented by the simulator.")
	}
	if remove, _ := input["removeThingType"].(bool); remove {
		return Failure("InvalidRequestException", "removeThingType is not supported by the simulator.")
	}
	delete(input, "removeThingType")
	if payload, ok := input["attributePayload"].(map[string]any); ok {
		attributes, err := iotAttributes(payload)
		if err != nil {
			return err
		}
		delete(input, "attributePayload")
		input["attributes"] = attributes
	}
	return nil
}

// iotThingGroup refuses thing group hierarchies and attribute merging.
func iotThingGroup(input map[string]any) error {
	if err := iotExpectedVersion(input); err != nil {
		return err
	}
	if _, ok := input["parentGroupName"]; ok {
		return Failure("InvalidRequestException", "Thing group hierarchies are not modeled by the simulator.")
	}
	properties, _ := input["thingGroupProperties"].(map[string]any)
	if payload, ok := properties["attributePayload"].(map[string]any); ok {
		attributes, err := iotAttributes(payload)
		if err != nil {
			return err
		}
		properties["attributePayload"] = map[string]any{"attributes": attributes}
	}
	return nil
}

// iotPolicyDocument requires a JSON policy document with statements of at
// most 2048 characters excluding whitespace, as documented.
func iotPolicyDocument(input map[string]any) error {
	var document map[string]any
	text, _ := input["policyDocument"].(string)
	if json.Unmarshal([]byte(text), &document) != nil || document["Statement"] == nil || len(strings.Join(strings.Fields(text), "")) > 2048 {
		return Failure("MalformedPolicyException", "The policy document must be a JSON object with a Statement and at most 2048 characters excluding whitespace.")
	}
	return nil
}

// iotSecurityProfile requires unique behavior names and refuses the delete
// flags of updates, whose removal semantics the simulator does not apply.
func iotSecurityProfile(input map[string]any) error {
	if err := iotExpectedVersion(input); err != nil {
		return err
	}
	for _, flag := range []string{"deleteBehaviors", "deleteAlertTargets", "deleteAdditionalMetricsToRetain", "deleteMetricsExportConfig"} {
		if set, _ := input[flag].(bool); set {
			return Failure("InvalidRequestException", "Security profile delete flags are not supported by the simulator; send the replacement members instead.")
		}
		delete(input, flag)
	}
	names := map[string]bool{}
	behaviors, _ := input["behaviors"].([]any)
	for _, item := range behaviors {
		behavior, _ := item.(map[string]any)
		name, _ := behavior["name"].(string)
		if names[name] {
			return Failure("InvalidRequestException", "Behavior names must be unique within a security profile.")
		}
		names[name] = true
	}
	return nil
}

// iotTaggable refuses thing ARNs: things carry attributes, not tags.
func iotTaggable(input map[string]any) error {
	if arn, _ := input["resourceArn"].(string); strings.Contains(arn, ":thing/") {
		return Failure("InvalidRequestException", "Things cannot be tagged; use thing attributes.")
	}
	return nil
}

var iotFleetWise = &Service{
	Model: "iotfleetwise", Title: "AWS IoT FleetWise", IAMPrefix: "iotfleetwise", Issues: []int{156},
	Resources: []*Resource{
		{Kind: "signalCatalog", Shape: "GetSignalCatalogResponse", Summary: "SignalCatalogSummary", ID: "name",
			ARN:       "arn:{partition}:iotfleetwise:{region}:{account}:signal-catalog/{name}",
			ARNMember: "arn", NameMember: "name", Unique: true, Limit: 1,
			Generate: map[string]any{"creationTime": "$now", "lastModificationTime": "$now",
				"nodeCounts": map[string]any{"totalNodes": 0, "totalBranches": 0, "totalSensors": 0, "totalAttributes": 0, "totalActuators": 0, "totalStructs": 0, "totalProperties": 0}}},
		{Kind: "fleet", Shape: "GetFleetResponse", Summary: "FleetSummary", ID: "name",
			ARN:       "arn:{partition}:iotfleetwise:{region}:{account}:fleet/{name}",
			ARNMember: "arn", NameMember: "id", Unique: true, Limit: 100,
			Aliases: map[string]string{"fleetId": "id"}, Refs: map[string]string{"signalCatalogArn": "signalCatalog"},
			Generate: map[string]any{"creationTime": "$now", "lastModificationTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateSignalCatalog", Kind: Create, Resource: "signalCatalog"},
		{Name: "GetSignalCatalog", Kind: Read, Resource: "signalCatalog", Lookup: "name"},
		{Name: "ListSignalCatalogs", Kind: List, Resource: "signalCatalog"},
		{Name: "UpdateSignalCatalog", Kind: Update, Resource: "signalCatalog", Lookup: "name", Fields: []string{"description"}, Touch: []string{"lastModificationTime"}},
		{Name: "DeleteSignalCatalog", Kind: Delete, Resource: "signalCatalog", Lookup: "name", IdempotentDelete: true},
		{Name: "CreateFleet", Kind: Create, Resource: "fleet"},
		{Name: "GetFleet", Kind: Read, Resource: "fleet", Lookup: "fleetId"},
		{Name: "ListFleets", Kind: List, Resource: "fleet"},
		{Name: "UpdateFleet", Kind: Update, Resource: "fleet", Lookup: "fleetId", Fields: []string{"description"}, Touch: []string{"lastModificationTime"}},
		{Name: "DeleteFleet", Kind: Delete, Resource: "fleet", Lookup: "fleetId", IdempotentDelete: true},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Signal catalogs and fleets are configuration metadata: no vehicle, decoder manifest, model manifest, state template or campaign exists, so fleets hold no vehicles and no vehicle data is collected. CreateVehicle, CreateCampaign, RegisterAccount and the other vehicle and data-collection APIs return UnknownOperationException with HTTP 501.",
		"Signal catalogs hold no nodes: nodes, nodesToAdd, nodesToUpdate and nodesToRemove are refused, node counts stay 0, and ImportSignalCatalog and ListSignalCatalogNodes return HTTP 501. One signal catalog exists per Region.",
		"Fleets must reference a live signal catalog, but deleting a signal catalog does not check the fleets that reference it. Deleting a missing signal catalog or fleet succeeds, as the official operations model no not-found error; ListFleets listResponseScope is refused.",
	},
}

var greengrassV2 = &Service{
	Model: "greengrassv2", Title: "AWS IoT Greengrass", IAMPrefix: "greengrass", Issues: []int{157},
	Resources: []*Resource{
		{Kind: "serviceRole", Shape: "GetServiceRoleForAccountResponse", Singleton: true, ID: "name",
			ARN: "arn:{partition}:greengrass:{region}:{account}:servicerole", NameMember: "roleArn"},
	},
	Ops: []Op{
		{Name: "AssociateServiceRoleToAccount", Kind: Put, Resource: "serviceRole", Touch: []string{"associatedAt"}, Check: greengrassRole},
		{Name: "GetServiceRoleForAccount", Kind: Read, Resource: "serviceRole"},
		{Name: "DisassociateServiceRoleFromAccount", Kind: Delete, Resource: "serviceRole", Set: map[string]any{"disassociatedAt": "$now"}},
	},
	Bounds: []string{
		"Only the Regional Greengrass service role association is implemented. The role is stored without checking that it exists or carries AWSGreengrassResourceAccessRolePolicy, and nothing assumes it.",
		"Components, deployments, core devices, client devices and connectivity information return UnknownOperationException with HTTP 501: deployments and core devices would reach devices, and component version ARNs combine the recipe's name and version, which the simulator does not derive. Disassociating when no role is associated fails with ResourceNotFoundException (HTTP 404) instead of succeeding.",
	},
}

var iamRoleARN = regexp.MustCompile(`^arn:aws:iam::[0-9]{12}:role/.+$`)

// greengrassRole requires an IAM role ARN for the service role.
func greengrassRole(input map[string]any) error {
	if arn, _ := input["roleArn"].(string); !iamRoleARN.MatchString(arn) {
		return Failure("ValidationException", "roleArn must be an IAM role ARN.")
	}
	return nil
}

var iotSiteWise = &Service{
	Model: "iotsitewise", Title: "AWS IoT SiteWise", IAMPrefix: "iotsitewise", Issues: []int{158},
	Resources: []*Resource{
		{Kind: "gateway", Shape: "DescribeGatewayResponse", Summary: "GatewaySummary", ID: "uuid",
			ARN:       "arn:{partition}:iotsitewise:{region}:{account}:gateway/{id}",
			ARNMember: "gatewayArn", NameMember: "gatewayName", IDMember: "gatewayId", Limit: 100,
			Generate: map[string]any{"gatewayCapabilitySummaries": []any{}, "creationDate": "$now", "lastUpdateDate": "$now"}},
		{Kind: "loggingOptions", Shape: "DescribeLoggingOptionsResponse", Singleton: true, ID: "name",
			ARN: "arn:{partition}:iotsitewise:{region}:{account}:logging-options", NameMember: "loggingOptions",
			Generate: map[string]any{"loggingOptions": map[string]any{"level": "OFF"}}},
	},
	Ops: []Op{
		{Name: "CreateGateway", Kind: Create, Resource: "gateway", Check: siteWiseGatewayPlatform},
		{Name: "DescribeGateway", Kind: Read, Resource: "gateway", Lookup: "gatewayId"},
		{Name: "ListGateways", Kind: List, Resource: "gateway"},
		{Name: "UpdateGateway", Kind: Update, Resource: "gateway", Lookup: "gatewayId", Fields: []string{"gatewayName"}, Touch: []string{"lastUpdateDate"}},
		{Name: "DeleteGateway", Kind: Delete, Resource: "gateway", Lookup: "gatewayId"},
		{Name: "DescribeLoggingOptions", Kind: Read, Resource: "loggingOptions"},
		{Name: "PutLoggingOptions", Kind: Put, Resource: "loggingOptions"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Gateways are configuration metadata: no SiteWise Edge software, Greengrass core device or Siemens Industrial Edge device is contacted, no capability configuration is synchronized and no data is ingested. Asset models, assets, portals, projects, dashboards, access policies, datasets, computation models, time series and property values return UnknownOperationException with HTTP 501.",
		"Gateway platforms must name exactly one of greengrass, greengrassV2 or siemensIE; the referenced group, core device or thing is not checked. Logging options default to OFF and no log is written. SDK clients must disable host-prefix injection (botocore inject_host_prefix=False) because the official api. host prefix cannot address the loopback endpoint.",
	},
}

// siteWiseGatewayPlatform requires exactly one gateway platform, as the
// official union-like structure documents.
func siteWiseGatewayPlatform(input map[string]any) error {
	if platform, _ := input["gatewayPlatform"].(map[string]any); len(platform) != 1 {
		return Failure("InvalidRequestException", "gatewayPlatform must name exactly one of greengrass, greengrassV2 or siemensIE.")
	}
	return nil
}

var iotTwinMaker = &Service{
	Model: "iottwinmaker", Title: "AWS IoT TwinMaker", IAMPrefix: "iottwinmaker", Issues: []int{159},
	Resources: []*Resource{
		{Kind: "workspace", Shape: "GetWorkspaceResponse", Summary: "WorkspaceSummary", ID: "name",
			ARN:       "arn:{partition}:iottwinmaker:{region}:{account}:workspace/{name}",
			ARNMember: "arn", NameMember: "workspaceId", Unique: true, Limit: 50,
			Generate: map[string]any{"creationDateTime": "$now", "updateDateTime": "$now", "linkedServices": []any{}}},
		{Kind: "scene", Shape: "GetSceneResponse", Summary: "SceneSummary", ID: "name", Parent: "workspace", ParentMember: "workspaceId",
			ARN:       "arn:{partition}:iottwinmaker:{region}:{account}:workspace/{parent}/scene/{name}",
			ARNMember: "arn", NameMember: "sceneId", Unique: true, Limit: 100,
			Generate: map[string]any{"workspaceId": "$parentId", "creationDateTime": "$now", "updateDateTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateWorkspace", Kind: Create, Resource: "workspace"},
		{Name: "GetWorkspace", Kind: Read, Resource: "workspace", Lookup: "workspaceId"},
		{Name: "ListWorkspaces", Kind: List, Resource: "workspace"},
		{Name: "UpdateWorkspace", Kind: Update, Resource: "workspace", Lookup: "workspaceId", Fields: []string{"description", "role", "s3Location"}, Touch: []string{"updateDateTime"}},
		{Name: "DeleteWorkspace", Kind: Delete, Resource: "workspace", Lookup: "workspaceId"},
		{Name: "CreateScene", Kind: Create, Resource: "scene", Parent: "workspaceId"},
		{Name: "GetScene", Kind: Read, Resource: "scene", Parent: "workspaceId", Lookup: "sceneId"},
		{Name: "ListScenes", Kind: List, Resource: "scene", Parent: "workspaceId"},
		{Name: "UpdateScene", Kind: Update, Resource: "scene", Parent: "workspaceId", Lookup: "sceneId",
			Fields: []string{"contentLocation", "description", "capabilities", "sceneMetadata"}, Touch: []string{"updateDateTime"}},
		{Name: "DeleteScene", Kind: Delete, Resource: "scene", Parent: "workspaceId", Lookup: "sceneId"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceARN"},
	},
	Bounds: []string{
		"Workspaces and scenes are configuration metadata: no entity, component type, property value, query, sync job or metadata transfer job exists, and workspace buckets, IAM roles and scene files in Amazon S3 are neither read nor checked. CreateEntity, CreateComponentType, ExecuteQuery and the other data APIs return UnknownOperationException with HTTP 501.",
		"Workspaces with scenes cannot be deleted; delete the scenes first. Scenes report no generated scene metadata, the pricing plan is not modeled and ListTagsForResource paging members are refused. SDK clients must disable host-prefix injection (botocore inject_host_prefix=False) because the official api. host prefix cannot address the loopback endpoint.",
	},
}

var iotWireless = &Service{
	Model: "iotwireless", Title: "AWS IoT Wireless", IAMPrefix: "iotwireless", Issues: []int{160},
	Resources: []*Resource{
		{Kind: "destination", Shape: "GetDestinationResponse", Summary: "Destinations", ID: "name",
			ARN:       "arn:{partition}:iotwireless:{region}:{account}:Destination/{name}",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 100},
		{Kind: "deviceProfile", Shape: "GetDeviceProfileResponse", Summary: "DeviceProfile", ID: "uuid",
			ARN:       "arn:{partition}:iotwireless:{region}:{account}:DeviceProfile/{id}",
			ARNMember: "Arn", NameMember: "Name", IDMember: "Id", Limit: 100},
		{Kind: "serviceProfile", Shape: "GetServiceProfileResponse", Summary: "ServiceProfile", ID: "uuid",
			ARN:       "arn:{partition}:iotwireless:{region}:{account}:ServiceProfile/{id}",
			ARNMember: "Arn", NameMember: "Name", IDMember: "Id", Limit: 100},
	},
	Ops: []Op{
		{Name: "CreateDestination", Kind: Create, Resource: "destination"},
		{Name: "GetDestination", Kind: Read, Resource: "destination", Lookup: "Name"},
		{Name: "ListDestinations", Kind: List, Resource: "destination"},
		{Name: "UpdateDestination", Kind: Update, Resource: "destination", Lookup: "Name", Fields: []string{"ExpressionType", "Expression", "Description", "RoleArn"}},
		{Name: "DeleteDestination", Kind: Delete, Resource: "destination", Lookup: "Name"},
		{Name: "CreateDeviceProfile", Kind: Create, Resource: "deviceProfile", Check: wirelessNoSidewalk},
		{Name: "GetDeviceProfile", Kind: Read, Resource: "deviceProfile", Lookup: "Id"},
		{Name: "ListDeviceProfiles", Kind: List, Resource: "deviceProfile"},
		{Name: "DeleteDeviceProfile", Kind: Delete, Resource: "deviceProfile", Lookup: "Id"},
		{Name: "CreateServiceProfile", Kind: Create, Resource: "serviceProfile"},
		{Name: "GetServiceProfile", Kind: Read, Resource: "serviceProfile", Lookup: "Id"},
		{Name: "ListServiceProfiles", Kind: List, Resource: "serviceProfile"},
		{Name: "DeleteServiceProfile", Kind: Delete, Resource: "serviceProfile", Lookup: "Id"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Destinations and LoRaWAN device and service profiles are configuration metadata: no wireless device, gateway, FUOTA task, multicast group, network analyzer, partner account or position resource exists, and no uplink is routed to a destination's AWS IoT rule or MQTT topic. CreateWirelessDevice, CreateWirelessGateway, SendDataToWirelessDevice and the other device and data APIs return UnknownOperationException with HTTP 501.",
		"Amazon Sidewalk device profiles are refused because their official description carries an application server public key and device attestation certificates. Service profiles report only the supplied LoRaWAN members, not the network server's derived values, and roles are stored without checking them. ListDeviceProfiles DeviceProfileType is refused.",
	},
}

// wirelessNoSidewalk refuses Sidewalk profiles, whose official description
// holds keys and certificates the simulator does not generate.
func wirelessNoSidewalk(input map[string]any) error {
	if _, ok := input["Sidewalk"]; ok {
		return Failure("ValidationException", "Amazon Sidewalk device profiles are not supported by the simulator.")
	}
	return nil
}
