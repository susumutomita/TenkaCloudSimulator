package catalog

import (
	"bytes"
	"encoding/json"
	"slices"
)

// Reviewed bounded specifications for migration and transfer services. Each
// binds only official operations whose effect is stored configuration: no
// mainframe workload runs, no migration tool reports, no file moves, no
// source server replicates and no recovery instance launches.

// mainframeModernization serves AWS Mainframe Modernization applications as
// stored records. No runtime environment, deployment or batch job exists.
var mainframeModernization = &Service{
	Model: "m2", Title: "AWS Mainframe Modernization", IAMPrefix: "m2", Issues: []int{231},
	Resources: []*Resource{
		{Kind: "application", Shape: "GetApplicationResponse", Summary: "ApplicationSummary", ID: "alnum:10",
			ARN:       "arn:{partition}:m2:{region}:{account}:app/{id}",
			ARNMember: "applicationArn", NameMember: "name", IDMember: "applicationId", Unique: true, Limit: 50,
			Generate: map[string]any{"creationTime": "$now", "status": "Created", "applicationVersion": 1, "versionStatus": "Available",
				"latestVersion": map[string]any{"applicationVersion": 1, "creationTime": "$now", "status": "Available"}}},
	},
	Ops: []Op{
		{Name: "CreateApplication", Kind: Create, Resource: "application", Accept: []string{"definition"}, Check: m2Definition},
		{Name: "GetApplication", Kind: Read, Resource: "application", Lookup: "applicationId"},
		{Name: "ListApplications", Kind: List, Resource: "application", Filters: map[string]string{"environmentId": "environmentId"}},
		{Name: "UpdateApplication", Kind: Update, Resource: "application", Lookup: "applicationId", Fields: []string{"description"},
			Accept: []string{"currentApplicationVersion", "definition"}, Touch: []string{"updateTime"}, Check: m2Definition, Merged: m2NewVersion},
		{Name: "DeleteApplication", Kind: Delete, Resource: "application", Lookup: "applicationId"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Applications are stored records: no runtime environment, deployment, listener, load balancer, batch job, data set, log group or BluInsights session exists, so every application stays Created with no environment, no deployed version and no endpoint. Environments, deployments, StartApplication, StopApplication, StartBatchJob, data set import and export, application versions, GetSignedBluinsightsUrl and every other official operation return UnknownOperationException with HTTP 501.",
		"The application definition is validated and then discarded because GetApplicationVersion is not implemented: exactly one of content (a JSON object) or s3Location must be given, the S3 object is never read, and definitionContent is never returned. UpdateApplication creates the next version number and reports it as latestVersion, but only that number, its creation time and the status Available are stored.",
		"UpdateApplication refuses a currentApplicationVersion other than the latest version with ConflictException (the simulator's reading of the documented current version of the application to update). ListApplications supports the environmentId filter, which never matches because no application has an environment; the names filter is refused instead of being ignored. The applicationId is a 10 character simulator identifier. Names must be unique within the Region as a local limit, and at most 50 applications exist per Region. DeleteApplication does not model ResourceNotFoundException, so deleting a missing application fails with ValidationException.",
	},
}

// m2Definition applies the documented application definition union: inline
// JSON content or an S3 location, never both, and content must be a JSON
// object.
func m2Definition(input map[string]any) error {
	definition, ok := input["definition"].(map[string]any)
	if !ok {
		return nil
	}
	content, hasContent := definition["content"].(string)
	location, hasLocation := definition["s3Location"].(string)
	if hasContent == hasLocation || hasLocation && location == "" {
		return invalid("A definition holds exactly one of content or s3Location.")
	}
	if hasContent {
		var object map[string]any
		if err := json.NewDecoder(bytes.NewReader([]byte(content))).Decode(&object); err != nil || object == nil {
			return invalid("The definition content must be a JSON object.")
		}
	}
	return nil
}

// m2NewVersion rotates the application version on every update and refuses a
// stale currentApplicationVersion.
func m2NewVersion(document, supplied map[string]any) error {
	current, _ := Number(supplied["currentApplicationVersion"])
	latest, _ := Number(document["applicationVersion"])
	if current != latest {
		return Failure("ConflictException", "The current application version is not the latest version of the application.")
	}
	next := int64(latest) + 1
	document["applicationVersion"] = next
	document["latestVersion"] = map[string]any{"applicationVersion": next, "creationTime": document["updateTime"], "status": "Available"}
	delete(document, "updateTime")
	return nil
}

// migrationHub serves progress update streams and the migration tasks
// imported into them as stored tracking records.
var migrationHub = &Service{
	Model: "mgh", Title: "AWS Migration Hub", IAMPrefix: "mgh", Issues: []int{232},
	Resources: []*Resource{
		{Kind: "progressUpdateStream", Shape: "ProgressUpdateStreamSummary", ID: "name",
			ARN:        "arn:{partition}:mgh:{region}:{account}:progressUpdateStream/{name}",
			NameMember: "ProgressUpdateStreamName", Unique: true, Limit: 100, Cascade: true},
		{Kind: "migrationTask", Shape: "MigrationTask", ID: "name", Parent: "progressUpdateStream",
			ARN:        "arn:{partition}:mgh:{region}:{account}:progressUpdateStream/{parentName}/migrationTask/{name}",
			NameMember: "MigrationTaskName", Unique: true, Limit: 200,
			Generate: map[string]any{"ProgressUpdateStream": "$parentName", "UpdateDateTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateProgressUpdateStream", Kind: Create, Resource: "progressUpdateStream"},
		{Name: "ListProgressUpdateStreams", Kind: List, Resource: "progressUpdateStream"},
		{Name: "DeleteProgressUpdateStream", Kind: Delete, Resource: "progressUpdateStream", Lookup: "ProgressUpdateStreamName"},
		{Name: "ImportMigrationTask", Kind: Create, Resource: "migrationTask", Parent: "ProgressUpdateStream"},
		{Name: "DescribeMigrationTask", Kind: Read, Resource: "migrationTask", Parent: "ProgressUpdateStream", Lookup: "MigrationTaskName"},
		{Name: "NotifyMigrationTaskState", Kind: Update, Resource: "migrationTask", Parent: "ProgressUpdateStream", Lookup: "MigrationTaskName",
			Fields: []string{"Task", "UpdateDateTime"}, Accept: []string{"NextUpdateSeconds"}},
	},
	Bounds: []string{
		"Progress update streams and migration tasks are stored tracking records: no migration tool, discovery connector, server or application is contacted, no task changes state on its own, and the only state is what NotifyMigrationTaskState stores (NextUpdateSeconds is accepted and not acted on). Created artifacts, discovered resources, source resources, resource attributes, application states and the other official operations return UnknownOperationException with HTTP 501.",
		"ListMigrationTasks is not implemented because its request carries no stream name, so a task cannot be addressed without its stream; tasks are read with DescribeMigrationTask. DeleteProgressUpdateStream also deletes the stream's tasks, as the official operation documents. DryRun is refused instead of being ignored, and the home Region is not checked, so HomeRegionNotSetException is never returned.",
		"No official output carries an ARN, so the IAM resource names progressUpdateStream/{name} and progressUpdateStream/{stream}/migrationTask/{task} are the simulator's reading of the authorization reference, not a documented value. Streams are limited to 100 and tasks to 200 per Region as local limits.",
	},
}

// transferFamily serves AWS Transfer Family workflows, AS2 profiles,
// connectors and servers as stored configuration. A server is a stored
// record: no endpoint exists, nothing listens, and no file moves.
var transferFamily = &Service{
	Model: "transfer", Title: "AWS Transfer Family", IAMPrefix: "transfer", Issues: []int{233},
	Resources: []*Resource{
		{Kind: "workflow", Shape: "DescribedWorkflow", Summary: "ListedWorkflow", ID: "alnum:17:w-",
			ARN:       "arn:{partition}:transfer:{region}:{account}:workflow/{id}",
			ARNMember: "Arn", IDMember: "WorkflowId", Limit: 50},
		{Kind: "profile", Shape: "DescribedProfile", Summary: "ListedProfile", ID: "hex:17:p-",
			ARN:       "arn:{partition}:transfer:{region}:{account}:profile/{id}",
			ARNMember: "Arn", IDMember: "ProfileId", Limit: 100},
		{Kind: "connector", Shape: "DescribedConnector", Summary: "ListedConnector", ID: "hex:17:c-",
			ARN:       "arn:{partition}:transfer:{region}:{account}:connector/{id}",
			ARNMember: "Arn", IDMember: "ConnectorId", Limit: 100,
			Generate: map[string]any{"Status": "ACTIVE", "EgressType": "SERVICE_MANAGED"}},
		{Kind: "server", Shape: "DescribedServer", Summary: "ListedServer", ID: "hex:17:s-",
			ARN:       "arn:{partition}:transfer:{region}:{account}:server/{id}",
			ARNMember: "Arn", IDMember: "ServerId", Limit: 20,
			Generate: map[string]any{"State": "OFFLINE", "UserCount": 0, "Domain": "S3", "EndpointType": "PUBLIC",
				"IdentityProviderType": "SERVICE_MANAGED", "Protocols": []any{"SFTP"}}},
	},
	Ops: []Op{
		{Name: "CreateWorkflow", Kind: Create, Resource: "workflow", Check: transferWorkflow},
		{Name: "DescribeWorkflow", Kind: Read, Resource: "workflow", Lookup: "WorkflowId"},
		{Name: "ListWorkflows", Kind: List, Resource: "workflow"},
		{Name: "DeleteWorkflow", Kind: Delete, Resource: "workflow", Lookup: "WorkflowId"},
		{Name: "CreateProfile", Kind: Create, Resource: "profile", Check: transferProfile},
		{Name: "DescribeProfile", Kind: Read, Resource: "profile", Lookup: "ProfileId"},
		{Name: "ListProfiles", Kind: List, Resource: "profile", Filters: map[string]string{"ProfileType": "ProfileType"}},
		{Name: "UpdateProfile", Kind: Update, Resource: "profile", Lookup: "ProfileId", Fields: []string{"CertificateIds"}},
		{Name: "DeleteProfile", Kind: Delete, Resource: "profile", Lookup: "ProfileId"},
		{Name: "CreateConnector", Kind: Create, Resource: "connector", Check: transferConnector},
		{Name: "DescribeConnector", Kind: Read, Resource: "connector", Lookup: "ConnectorId"},
		{Name: "ListConnectors", Kind: List, Resource: "connector"},
		{Name: "UpdateConnector", Kind: Update, Resource: "connector", Lookup: "ConnectorId",
			Fields: []string{"Url", "As2Config", "AccessRole", "LoggingRole", "SftpConfig", "SecurityPolicyName"}, Check: transferConnector},
		{Name: "DeleteConnector", Kind: Delete, Resource: "connector", Lookup: "ConnectorId"},
		{Name: "CreateServer", Kind: Create, Resource: "server", Check: transferServerCreate},
		{Name: "DescribeServer", Kind: Read, Resource: "server", Lookup: "ServerId"},
		{Name: "ListServers", Kind: List, Resource: "server"},
		{Name: "UpdateServer", Kind: Update, Resource: "server", Lookup: "ServerId",
			Fields: []string{"Certificate", "ProtocolDetails", "EndpointDetails", "EndpointType", "IdentityProviderDetails", "IdentityProviderType",
				"LoggingRole", "PostAuthenticationLoginBanner", "PreAuthenticationLoginBanner", "Protocols", "SecurityPolicyName", "WorkflowDetails",
				"StructuredLogDestinations", "S3StorageOptions", "IpAddressType"},
			Merged: func(document, _ map[string]any) error { return transferServerRules(document) }},
		{Name: "DeleteServer", Kind: Delete, Resource: "server", Lookup: "ServerId"},
		{Name: "TagResource", Kind: Tag, Lookup: "Arn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "Arn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "Arn"},
	},
	Bounds: []string{
		"Workflows, AS2 profiles, connectors and servers are stored configuration. A server is a stored record that stays OFFLINE: no endpoint, address, hostname, host key, listener or file store exists, nothing can connect, and StartServer, StopServer, StartFileTransfer, StartDirectoryListing, StartRemoteDelete, StartRemoteMove, TestConnection, TestIdentityProvider, workflow executions and every other official operation return UnknownOperationException with HTTP 501. No workflow step runs.",
		"Key material is never accepted: ImportCertificate, ImportHostKey, ImportSshPublicKey and the user, host key, access, agreement and web app operations are not implemented, and the HostKey member and SshPublicKeyBody are refused, so certificates, host keys, SSH public keys and private keys are neither stored nor returned. ListUsers, ListAccesses, ListAgreements and ListHostKeys also need their ServerId echoed in the output, which the catalog list projection cannot express. Profile CertificateIds and server Certificate are stored as given and not resolved, as are connector profile IDs, secret IDs, IAM roles, workflow IDs, VPC settings and Lambda functions.",
		"Connectors are ACTIVE service-managed connectors with the URL as given: EgressConfig (VPC Lattice) is refused and no egress IP address is reported. Servers default to the documented S3 domain, PUBLIC endpoint type, SERVICE_MANAGED identity provider and SFTP protocol, and report UserCount 0 and no HostKeyFingerprint or security policy default. FTP and FTPS need a VPC endpoint type and a directory, Lambda or API Gateway identity provider, FTPS needs a certificate, and each identity provider type needs its documented details; VPC_ENDPOINT servers cannot be created. At most 20 servers, 50 workflows, 100 profiles and 100 connectors exist per Region as local limits.",
	},
}

// transferStepDetails maps a workflow step type to its details member.
var transferStepDetails = map[string]string{"COPY": "CopyStepDetails", "CUSTOM": "CustomStepDetails", "TAG": "TagStepDetails",
	"DELETE": "DeleteStepDetails", "DECRYPT": "DecryptStepDetails"}

// transferWorkflow requires each step to carry exactly the details member of
// its type.
func transferWorkflow(input map[string]any) error {
	for _, member := range []string{"Steps", "OnExceptionSteps"} {
		steps, _ := input[member].([]any)
		for _, item := range steps {
			step, _ := item.(map[string]any)
			count, matches := 0, true
			stepType, _ := step["Type"].(string)
			for kind, details := range transferStepDetails {
				if _, ok := step[details]; ok {
					count++
					matches = matches && kind == stepType
				}
			}
			if count != 1 || !matches {
				return invalid("Each workflow step needs a Type and exactly the matching details member, such as COPY with CopyStepDetails.")
			}
		}
	}
	return nil
}

// transferProfile applies the documented rule that an AS2 ID has no spaces.
func transferProfile(input map[string]any) error {
	if id, _ := input["As2Id"].(string); bytes.ContainsAny([]byte(id), " \t\n\r") {
		return invalid("The As2Id cannot include spaces.")
	}
	return nil
}

// transferConnector requires the partner URL that service-managed connectors
// document and refuses VPC Lattice egress, which the record cannot express.
func transferConnector(input map[string]any) error {
	if _, ok := input["EgressConfig"]; ok {
		return invalid("EgressConfig is not supported by the simulator; connectors use service-managed egress.")
	}
	if url, ok := input["Url"].(string); ok && url == "" {
		return invalid("A service-managed connector needs the URL of the partner endpoint.")
	}
	return nil
}

// transferServerCreate applies the server rules to the request with the
// documented defaults filled in, and refuses the deprecated endpoint type.
func transferServerCreate(input map[string]any) error {
	if input["EndpointType"] == "VPC_ENDPOINT" {
		return invalid("Servers can no longer be created with EndpointType VPC_ENDPOINT; use VPC.")
	}
	return transferServerRules(input)
}

// transferServerRules applies the documented relations between a server's
// protocols, endpoint type, identity provider and certificate.
func transferServerRules(server map[string]any) error {
	protocols := []string{"SFTP"}
	if list, ok := server["Protocols"].([]any); ok {
		protocols = protocols[:0]
		for _, item := range list {
			text, _ := item.(string)
			protocols = append(protocols, text)
		}
	}
	endpoint, _ := server["EndpointType"].(string)
	if endpoint == "" {
		endpoint = "PUBLIC"
	}
	provider, _ := server["IdentityProviderType"].(string)
	if provider == "" {
		provider = "SERVICE_MANAGED"
	}
	if slices.Contains(protocols, "FTP") || slices.Contains(protocols, "FTPS") {
		if endpoint != "VPC" {
			return invalid("FTP and FTPS need EndpointType VPC.")
		}
		if provider == "SERVICE_MANAGED" {
			return invalid("FTP and FTPS need IdentityProviderType AWS_DIRECTORY_SERVICE, AWS_LAMBDA or API_GATEWAY.")
		}
	}
	if certificate, _ := server["Certificate"].(string); slices.Contains(protocols, "FTPS") && certificate == "" {
		return invalid("FTPS needs a Certificate.")
	}
	details, _ := server["IdentityProviderDetails"].(map[string]any)
	required := map[string][]string{"AWS_DIRECTORY_SERVICE": {"DirectoryId"}, "API_GATEWAY": {"Url", "InvocationRole"}, "AWS_LAMBDA": {"Function"}}[provider]
	for _, member := range required {
		if text, _ := details[member].(string); text == "" {
			return invalid("IdentityProviderType %s needs IdentityProviderDetails.%s.", provider, member)
		}
	}
	return nil
}

// applicationMigration serves Application Migration Service applications,
// waves, connectors and the launch and replication templates as stored
// configuration. No source server exists and nothing replicates.
var applicationMigration = &Service{
	Model: "mgn", Title: "Application Migration Service", IAMPrefix: "mgn", Issues: []int{235},
	AccountMembers: []string{"accountID"},
	Resources: []*Resource{
		{Kind: "application", Shape: "Application", ID: "alnum:17:app-", ARN: "arn:{partition}:mgn:{region}:{account}:application/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "applicationID", Limit: 50,
			Generate: map[string]any{"creationDateTime": "$now", "lastModifiedDateTime": "$now", "isArchived": false}},
		{Kind: "wave", Shape: "Wave", ID: "alnum:17:wave-", ARN: "arn:{partition}:mgn:{region}:{account}:wave/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "waveID", Limit: 50,
			Generate: map[string]any{"creationDateTime": "$now", "lastModifiedDateTime": "$now", "isArchived": false}},
		{Kind: "connector", Shape: "Connector", ID: "alnum:17:connector-", ARN: "arn:{partition}:mgn:{region}:{account}:connector/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "connectorID", Limit: 20},
		{Kind: "launchConfigurationTemplate", Shape: "LaunchConfigurationTemplate", ID: "alnum:17:lct-",
			ARN:       "arn:{partition}:mgn:{region}:{account}:launch-configuration-template/{id}",
			ARNMember: "arn", IDMember: "launchConfigurationTemplateID", Limit: 20},
		{Kind: "replicationConfigurationTemplate", Shape: "ReplicationConfigurationTemplate", ID: "alnum:17:rct-",
			ARN:       "arn:{partition}:mgn:{region}:{account}:replication-configuration-template/{id}",
			ARNMember: "arn", IDMember: "replicationConfigurationTemplateID", Limit: 20},
	},
	Ops: []Op{
		{Name: "CreateApplication", Kind: Create, Resource: "application"},
		{Name: "ListApplications", Kind: List, Resource: "application"},
		{Name: "UpdateApplication", Kind: Update, Resource: "application", Lookup: "applicationID", Fields: []string{"name", "description"}, Touch: []string{"lastModifiedDateTime"}},
		{Name: "ArchiveApplication", Kind: Update, Resource: "application", Lookup: "applicationID", Touch: []string{"lastModifiedDateTime"}, Set: map[string]any{"isArchived": true}},
		{Name: "UnarchiveApplication", Kind: Update, Resource: "application", Lookup: "applicationID", Touch: []string{"lastModifiedDateTime"}, Set: map[string]any{"isArchived": false}},
		{Name: "DeleteApplication", Kind: Delete, Resource: "application", Lookup: "applicationID"},
		{Name: "CreateWave", Kind: Create, Resource: "wave"},
		{Name: "ListWaves", Kind: List, Resource: "wave"},
		{Name: "UpdateWave", Kind: Update, Resource: "wave", Lookup: "waveID", Fields: []string{"name", "description"}, Touch: []string{"lastModifiedDateTime"}},
		{Name: "ArchiveWave", Kind: Update, Resource: "wave", Lookup: "waveID", Touch: []string{"lastModifiedDateTime"}, Set: map[string]any{"isArchived": true}},
		{Name: "UnarchiveWave", Kind: Update, Resource: "wave", Lookup: "waveID", Touch: []string{"lastModifiedDateTime"}, Set: map[string]any{"isArchived": false}},
		{Name: "DeleteWave", Kind: Delete, Resource: "wave", Lookup: "waveID"},
		{Name: "CreateConnector", Kind: Create, Resource: "connector"},
		{Name: "ListConnectors", Kind: List, Resource: "connector"},
		{Name: "UpdateConnector", Kind: Update, Resource: "connector", Lookup: "connectorID", Fields: []string{"name", "ssmCommandConfig"}},
		{Name: "DeleteConnector", Kind: Delete, Resource: "connector", Lookup: "connectorID"},
		{Name: "CreateLaunchConfigurationTemplate", Kind: Create, Resource: "launchConfigurationTemplate"},
		{Name: "DescribeLaunchConfigurationTemplates", Kind: List, Resource: "launchConfigurationTemplate", Names: "launchConfigurationTemplateIDs"},
		{Name: "UpdateLaunchConfigurationTemplate", Kind: Update, Resource: "launchConfigurationTemplate", Lookup: "launchConfigurationTemplateID",
			Fields: []string{"associatePublicIpAddress", "bootMode", "copyPrivateIp", "copyTags", "enableMapAutoTagging", "largeVolumeConf", "launchDisposition",
				"licensing", "mapAutoTaggingMpeID", "postLaunchActions", "smallVolumeConf", "smallVolumeMaxSize", "targetInstanceTypeRightSizingMethod"}},
		{Name: "DeleteLaunchConfigurationTemplate", Kind: Delete, Resource: "launchConfigurationTemplate", Lookup: "launchConfigurationTemplateID"},
		{Name: "CreateReplicationConfigurationTemplate", Kind: Create, Resource: "replicationConfigurationTemplate"},
		{Name: "DescribeReplicationConfigurationTemplates", Kind: List, Resource: "replicationConfigurationTemplate", Names: "replicationConfigurationTemplateIDs"},
		{Name: "UpdateReplicationConfigurationTemplate", Kind: Update, Resource: "replicationConfigurationTemplate", Lookup: "replicationConfigurationTemplateID",
			Fields: []string{"associateDefaultSecurityGroup", "bandwidthThrottling", "createPublicIP", "dataPlaneRouting", "defaultLargeStagingDiskType", "ebsEncryption",
				"ebsEncryptionKeyArn", "replicationServerInstanceType", "replicationServersSecurityGroupsIDs", "stagingAreaSubnetId", "stagingAreaTags",
				"useDedicatedReplicationServer", "useFipsEndpoint"}},
		{Name: "DeleteReplicationConfigurationTemplate", Kind: Delete, Resource: "replicationConfigurationTemplate", Lookup: "replicationConfigurationTemplateID"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Applications, waves, connectors and the launch and replication configuration templates are stored configuration: no source server, replication agent, staging area, replication server, job, test or cutover instance exists, nothing replicates, and StartReplication, StartTest, StartCutover, FinalizeCutover, TerminateTargetInstances, imports, exports, every source server, job, vCenter, action and post-launch operation, InitializeService and the other official operations return UnknownOperationException with HTTP 501. UninitializedAccountException is never returned because no service-linked role is needed.",
		"Applications, waves and connectors have no read operation, so they are read through their List operations. The filters member of the List operations is refused instead of being ignored, AssociateApplications and DisassociateApplications are not implemented so waveID and the aggregated status members are never reported, and names are not required to be unique. Archive and Unarchive only set isArchived. Template members that are not supplied are not defaulted, and no EC2 launch template exists, so ec2LaunchTemplateID is never reported.",
		"Subnet, security group, KMS key, S3 bucket, IAM and SSM references in templates and connectors are stored as given and never resolved. The ARN forms application/{id}, wave/{id}, connector/{id}, launch-configuration-template/{id} and replication-configuration-template/{id} are the simulator's reading of the authorization reference. At most 50 applications, 50 waves, 20 connectors and 20 templates of each kind exist per Region as local limits.",
	},
}

// elasticDisasterRecovery serves Elastic Disaster Recovery launch and
// replication configuration templates and their launch actions as stored
// configuration. No source server replicates and no recovery runs.
var elasticDisasterRecovery = &Service{
	Model: "drs", Title: "AWS Elastic Disaster Recovery", IAMPrefix: "drs", Issues: []int{276},
	Resources: []*Resource{
		{Kind: "launchConfigurationTemplate", Shape: "LaunchConfigurationTemplate", ID: "alnum:17:lct-",
			ARN:       "arn:{partition}:drs:{region}:{account}:launch-configuration-template/{id}",
			ARNMember: "arn", IDMember: "launchConfigurationTemplateID", Limit: 20, Cascade: true},
		{Kind: "replicationConfigurationTemplate", Shape: "ReplicationConfigurationTemplate", ID: "alnum:17:rct-",
			ARN:       "arn:{partition}:drs:{region}:{account}:replication-configuration-template/{id}",
			ARNMember: "arn", IDMember: "replicationConfigurationTemplateID", Limit: 20},
		{Kind: "launchAction", Shape: "LaunchAction", ID: "name", Parent: "launchConfigurationTemplate",
			ARN:        "arn:{partition}:drs:{region}:{account}:launch-configuration-template/{parent}/launch-action/{name}",
			NameMember: "actionId", Unique: true, Limit: 50, Generate: map[string]any{"resourceId": "$parentId"}},
	},
	Ops: []Op{
		{Name: "CreateLaunchConfigurationTemplate", Kind: Create, Resource: "launchConfigurationTemplate"},
		{Name: "DescribeLaunchConfigurationTemplates", Kind: List, Resource: "launchConfigurationTemplate", Names: "launchConfigurationTemplateIDs"},
		{Name: "UpdateLaunchConfigurationTemplate", Kind: Update, Resource: "launchConfigurationTemplate", Lookup: "launchConfigurationTemplateID",
			Fields: []string{"copyPrivateIp", "copyTags", "exportBucketArn", "launchDisposition", "launchIntoSourceInstance", "licensing", "postLaunchEnabled",
				"targetInstanceTypeRightSizingMethod"}},
		{Name: "DeleteLaunchConfigurationTemplate", Kind: Delete, Resource: "launchConfigurationTemplate", Lookup: "launchConfigurationTemplateID"},
		{Name: "CreateReplicationConfigurationTemplate", Kind: Create, Resource: "replicationConfigurationTemplate"},
		{Name: "DescribeReplicationConfigurationTemplates", Kind: List, Resource: "replicationConfigurationTemplate", Names: "replicationConfigurationTemplateIDs"},
		{Name: "UpdateReplicationConfigurationTemplate", Kind: Update, Resource: "replicationConfigurationTemplate", Lookup: "replicationConfigurationTemplateID",
			Fields: []string{"associateDefaultSecurityGroup", "autoReplicateNewDisks", "bandwidthThrottling", "createPublicIP", "dataPlaneRouting",
				"defaultLargeStagingDiskType", "ebsEncryption", "ebsEncryptionKeyArn", "pitPolicy", "replicationServerInstanceType",
				"replicationServersSecurityGroupsIDs", "stagingAreaSubnetId", "stagingAreaTags", "useDedicatedReplicationServer"}},
		{Name: "DeleteReplicationConfigurationTemplate", Kind: Delete, Resource: "replicationConfigurationTemplate", Lookup: "replicationConfigurationTemplateID"},
		{Name: "PutLaunchAction", Kind: Put, Resource: "launchAction", Parent: "resourceId", Lookup: "actionId"},
		{Name: "ListLaunchActions", Kind: List, Resource: "launchAction", Parent: "resourceId"},
		{Name: "DeleteLaunchAction", Kind: Delete, Resource: "launchAction", Parent: "resourceId", Lookup: "actionId"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Launch and replication configuration templates and the launch actions of launch templates are stored configuration: no source server, replication agent, staging area, replication server, recovery instance, drill, recovery or failback job exists, nothing replicates, and every source server, recovery instance, job, source network and extensible source server operation, StartRecovery, StartReplication, ReverseReplication, InitializeService and the other official operations return UnknownOperationException with HTTP 501. UninitializedAccountException is never returned because no service-linked role is needed.",
		"PutLaunchAction accepts only a launch configuration template ID as resourceId; a source server ID does not exist and fails with ResourceNotFoundException. The action type member is never reported because the service derives it from the SSM document, which is not resolved, and the filters member of ListLaunchActions is refused instead of being ignored. Deleting a launch configuration template deletes its launch actions.",
		"Template members that are not supplied are not defaulted, and subnet, security group, KMS key, S3 bucket and SSM document references are stored as given and never resolved. The ARN forms launch-configuration-template/{id}, replication-configuration-template/{id} and launch-configuration-template/{id}/launch-action/{actionId} are the simulator's reading of the authorization reference. At most 20 templates of each kind and 50 launch actions per template exist per Region as local limits.",
	},
}
