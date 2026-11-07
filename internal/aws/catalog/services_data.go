package catalog

import (
	"net"
	"regexp"
	"strings"
)

// Reviewed bounded specifications for database, file storage, backup,
// archive, streaming and messaging services. Each binds only official
// operations whose effect is configuration metadata: no database, cache,
// file system, vault, gateway, broker, stream or cluster runs, no data is
// stored, moved, backed up, restored or delivered, and no external system is
// contacted. Passwords and other secrets are refused, never stored.

var auroraDSQL = &Service{
	Model: "dsql", Title: "Amazon Aurora DSQL", IAMPrefix: "dsql", Issues: []int{123},
	Resources: []*Resource{
		{Kind: "cluster", Shape: "GetClusterOutput", Summary: "ClusterSummary", ID: "alnum:26",
			ARN:       "arn:{partition}:dsql:{region}:{account}:cluster/{id}",
			ARNMember: "arn", IDMember: "identifier", Limit: 20,
			Generate: map[string]any{"status": "CREATING", "creationTime": "$now",
				"encryptionDetails": map[string]any{"encryptionType": "AWS_OWNED_KMS_KEY", "encryptionStatus": "ENABLED"}}},
	},
	Ops: []Op{
		{Name: "CreateCluster", Kind: Create, Resource: "cluster", Check: dsqlCreateCluster},
		{Name: "GetCluster", Kind: Read, Resource: "cluster", Lookup: "identifier"},
		{Name: "ListClusters", Kind: List, Resource: "cluster"},
		{Name: "UpdateCluster", Kind: Update, Resource: "cluster", Lookup: "identifier", Check: dsqlClusterOptions,
			Fields: []string{"deletionProtectionEnabled", "encryptionDetails"}, Accept: []string{"clientToken"}},
		{Name: "DeleteCluster", Kind: Delete, Resource: "cluster", Lookup: "identifier", Accept: []string{"clientToken"}, Set: map[string]any{"status": "DELETING"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Clusters are configuration records that stay CREATING: no PostgreSQL-compatible database, endpoint, connection, authentication token or VPC endpoint exists, endpoint is never returned, and GetVpcEndpointServiceName returns UnknownOperationException with HTTP 501.",
		"Deletion protection is not enforced, so CreateCluster requires deletionProtectionEnabled false and enabling it is refused. Multi-Region peering (multiRegionProperties) and resource-based policies (policy, PutClusterPolicy, GetClusterPolicy and DeleteClusterPolicy) are not implemented.",
		"kmsEncryptionKey accepts AWS_OWNED_KMS_KEY or a KMS key ARN, stored in encryptionDetails without contacting KMS; key IDs and aliases are refused. Client tokens on UpdateCluster and DeleteCluster are accepted without replaying earlier responses.",
	},
}

func dsqlCreateCluster(input map[string]any) error {
	if input["deletionProtectionEnabled"] != false {
		return Failure("ValidationException", "Set deletionProtectionEnabled to false: the simulator does not enforce deletion protection.")
	}
	return dsqlClusterOptions(input)
}

// dsqlClusterOptions refuses options the simulator cannot honor and stores
// the requested key as the official encryptionDetails member.
func dsqlClusterOptions(input map[string]any) error {
	if input["deletionProtectionEnabled"] == true {
		return Failure("ValidationException", "Deletion protection is not enforced by the simulator.")
	}
	if _, ok := input["multiRegionProperties"]; ok {
		return Failure("ValidationException", "Multi-Region clusters are not supported by the simulator.")
	}
	key, ok := input["kmsEncryptionKey"].(string)
	if !ok {
		return nil
	}
	delete(input, "kmsEncryptionKey")
	details := map[string]any{"encryptionType": "AWS_OWNED_KMS_KEY", "encryptionStatus": "ENABLED"}
	if key != "AWS_OWNED_KMS_KEY" {
		if !strings.HasPrefix(key, "arn:") || !strings.Contains(key, ":kms:") {
			return Failure("ValidationException", "Specify kmsEncryptionKey as AWS_OWNED_KMS_KEY or a KMS key ARN; the simulator does not resolve key IDs or aliases.")
		}
		details = map[string]any{"encryptionType": "CUSTOMER_MANAGED_KMS_KEY", "kmsKeyArn": key, "encryptionStatus": "ENABLED"}
	}
	input["encryptionDetails"] = details
	return nil
}

var timestreamWrite = &Service{
	Model: "timestream-write", Title: "Amazon Timestream for LiveAnalytics", IAMPrefix: "timestream", Issues: []int{131},
	Resources: []*Resource{
		{Kind: "database", Shape: "Database", ID: "name", ARN: "arn:{partition}:timestream:{region}:{account}:database/{name}",
			ARNMember: "Arn", NameMember: "DatabaseName", Unique: true, Limit: 50,
			Generate: map[string]any{"CreationTime": "$now", "LastUpdatedTime": "$now"}},
		{Kind: "table", Shape: "Table", Parent: "database", ParentMember: "DatabaseName", ID: "name",
			ARN:       "arn:{partition}:timestream:{region}:{account}:database/{parentName}/table/{name}",
			ARNMember: "Arn", NameMember: "TableName", Unique: true, Limit: 100,
			Generate: map[string]any{"DatabaseName": "$parentName", "TableStatus": "ACTIVE", "CreationTime": "$now", "LastUpdatedTime": "$now",
				"RetentionProperties": map[string]any{"MemoryStoreRetentionPeriodInHours": 6, "MagneticStoreRetentionPeriodInDays": 73000}}},
	},
	Ops: []Op{
		{Name: "CreateDatabase", Kind: Create, Resource: "database"},
		{Name: "DescribeDatabase", Kind: Read, Resource: "database", Lookup: "DatabaseName"},
		{Name: "ListDatabases", Kind: List, Resource: "database"},
		{Name: "UpdateDatabase", Kind: Update, Resource: "database", Lookup: "DatabaseName", Fields: []string{"KmsKeyId"}, Touch: []string{"LastUpdatedTime"}},
		{Name: "DeleteDatabase", Kind: Delete, Resource: "database", Lookup: "DatabaseName"},
		{Name: "CreateTable", Kind: Create, Resource: "table", Parent: "DatabaseName"},
		{Name: "DescribeTable", Kind: Read, Resource: "table", Parent: "DatabaseName", Lookup: "TableName"},
		{Name: "ListTables", Kind: List, Resource: "table", Parent: "DatabaseName", Check: timestreamListTables},
		{Name: "UpdateTable", Kind: Update, Resource: "table", Parent: "DatabaseName", Lookup: "TableName",
			Fields: []string{"RetentionProperties", "MagneticStoreWriteProperties", "Schema"}, Touch: []string{"LastUpdatedTime"}},
		{Name: "DeleteTable", Kind: Delete, Resource: "table", Parent: "DatabaseName", Lookup: "TableName"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Databases and tables are stored configuration: no record is written, stored, tiered or queried, and WriteRecords, DescribeEndpoints and the batch load APIs return UnknownOperationException with HTTP 501. SDK clients reach the simulator through an explicit endpoint URL, which disables endpoint discovery.",
		"Tables keep the documented default retention of 6 hours in the memory store and 73000 days in the magnetic store unless RetentionProperties is given; magnetic store rejected-data locations in S3 are stored but never written. KMS keys are stored without contacting KMS, and the Timestream managed key is not reported when KmsKeyId is omitted.",
		"Database TableCount is not reported. A database with tables cannot be deleted, and ListTables requires DatabaseName instead of listing every table of the Region.",
	},
}

func timestreamListTables(input map[string]any) error {
	if _, ok := input["DatabaseName"]; !ok {
		return Failure("ValidationException", "Specify DatabaseName: the simulator lists tables of one database at a time.")
	}
	return nil
}

var memoryDB = &Service{
	Model: "memorydb", Title: "Amazon MemoryDB", IAMPrefix: "memorydb", Issues: []int{127},
	Resources: []*Resource{
		{Kind: "parameterGroup", Shape: "ParameterGroup", ID: "name", ARN: "arn:{partition}:memorydb:{region}:{account}:parametergroup/{name}",
			ARNMember: "ARN", NameMember: "Name", Unique: true, Limit: 50, Aliases: map[string]string{"ParameterGroupName": "Name"}},
		{Kind: "subnetGroup", Shape: "SubnetGroup", ID: "name", ARN: "arn:{partition}:memorydb:{region}:{account}:subnetgroup/{name}",
			ARNMember: "ARN", NameMember: "Name", Unique: true, Limit: 50, Aliases: map[string]string{"SubnetGroupName": "Name"}},
		{Kind: "user", Shape: "User", ID: "name", ARN: "arn:{partition}:memorydb:{region}:{account}:user/{name}",
			ARNMember: "ARN", NameMember: "Name", Unique: true, Limit: 100, Aliases: map[string]string{"UserName": "Name"},
			Generate: map[string]any{"Status": "active", "ACLNames": []any{}}},
	},
	Ops: []Op{
		{Name: "CreateParameterGroup", Kind: Create, Resource: "parameterGroup"},
		{Name: "DescribeParameterGroups", Kind: List, Resource: "parameterGroup", Filters: map[string]string{"ParameterGroupName": "Name"}},
		{Name: "DeleteParameterGroup", Kind: Delete, Resource: "parameterGroup", Lookup: "ParameterGroupName"},
		{Name: "CreateSubnetGroup", Kind: Create, Resource: "subnetGroup", Check: memoryDBSubnets},
		{Name: "DescribeSubnetGroups", Kind: List, Resource: "subnetGroup", Filters: map[string]string{"SubnetGroupName": "Name"}},
		{Name: "UpdateSubnetGroup", Kind: Update, Resource: "subnetGroup", Lookup: "SubnetGroupName", Fields: []string{"Description", "Subnets"}, Check: memoryDBSubnets},
		{Name: "DeleteSubnetGroup", Kind: Delete, Resource: "subnetGroup", Lookup: "SubnetGroupName"},
		{Name: "CreateUser", Kind: Create, Resource: "user", Check: memoryDBCreateUser},
		{Name: "DescribeUsers", Kind: List, Resource: "user", Filters: map[string]string{"UserName": "Name"}},
		{Name: "UpdateUser", Kind: Update, Resource: "user", Lookup: "UserName", Fields: []string{"AccessString", "Authentication"}, Check: memoryDBAuthentication},
		{Name: "DeleteUser", Kind: Delete, Resource: "user", Lookup: "UserName"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTags", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Parameter groups, subnet groups and IAM-authenticated users are stored configuration: no cluster, shard, node, snapshot, ACL or multi-Region cluster exists, and CreateCluster, CreateACL, CreateSnapshot and the other data-plane APIs return UnknownOperationException with HTTP 501.",
		"Users must use iam authentication; password authentication is refused so no password is stored. The default user, default parameter groups and the open-access ACL are not pre-created, and parameter values (DescribeParameters, UpdateParameterGroup, ResetParameterGroup) are not implemented.",
		"Parameter group families, subnet IDs and access strings are stored without validation; subnet groups report no VPC or Availability Zone. Describe filters by name return an empty list for an unknown name instead of a not-found fault, and the Filters member is refused. TagResource and UntagResource return no TagList, and removing a missing tag key succeeds.",
	},
}

// memoryDBSubnets stores the requested subnet IDs as the official Subnets list.
func memoryDBSubnets(input map[string]any) error {
	ids, ok := input["SubnetIds"].([]any)
	if !ok {
		return nil
	}
	subnets := make([]any, 0, len(ids))
	for _, id := range ids {
		subnets = append(subnets, map[string]any{"Identifier": id})
	}
	delete(input, "SubnetIds")
	input["Subnets"] = subnets
	return nil
}

func memoryDBCreateUser(input map[string]any) error {
	if input["UserName"] == "default" {
		return Failure("InvalidParameterValueException", "The default user is reserved and not modeled by the simulator.")
	}
	return memoryDBAuthentication(input)
}

// memoryDBAuthentication accepts only IAM authentication, so that no
// password is ever stored, and records it as the official Authentication.
func memoryDBAuthentication(input map[string]any) error {
	mode, ok := input["AuthenticationMode"].(map[string]any)
	if !ok {
		return nil
	}
	if mode["Type"] != "iam" || mode["Passwords"] != nil {
		return Failure("InvalidParameterValueException", "Only iam authentication is supported: the simulator never stores passwords.")
	}
	delete(input, "AuthenticationMode")
	input["Authentication"] = map[string]any{"Type": "iam"}
	return nil
}

var fsx = &Service{
	Model: "fsx", Title: "Amazon FSx", IAMPrefix: "fsx", Issues: []int{278, 293},
	Resources: []*Resource{
		{Kind: "fileSystem", Shape: "FileSystem", ID: "hex:17:fs-", ARN: "arn:{partition}:fsx:{region}:{account}:file-system/{id}",
			ARNMember: "ResourceARN", IDMember: "FileSystemId", Limit: 20,
			Generate: map[string]any{"OwnerId": "$account", "CreationTime": "$now", "Lifecycle": "CREATING", "StorageType": "SSD"}},
		{Kind: "fileCache", Shape: "FileCache", Summary: "FileCacheCreating", ID: "hex:17:fc-", ARN: "arn:{partition}:fsx:{region}:{account}:file-cache/{id}",
			ARNMember: "ResourceARN", IDMember: "FileCacheId", Limit: 20,
			Generate: map[string]any{"OwnerId": "$account", "CreationTime": "$now", "Lifecycle": "CREATING"}},
	},
	Ops: []Op{
		{Name: "CreateFileSystem", Kind: Create, Resource: "fileSystem", Check: fsxFileSystem},
		{Name: "DescribeFileSystems", Kind: List, Resource: "fileSystem", Names: "FileSystemIds"},
		{Name: "DeleteFileSystem", Kind: Delete, Resource: "fileSystem", Lookup: "FileSystemId", Accept: []string{"ClientRequestToken", "LustreConfiguration"},
			Check: fsxDeleteFileSystem, Set: map[string]any{"Lifecycle": "DELETING"}},
		{Name: "CreateFileCache", Kind: Create, Resource: "fileCache", Check: fsxFileCache},
		{Name: "DescribeFileCaches", Kind: List, Resource: "fileCache", Names: "FileCacheIds"},
		{Name: "DeleteFileCache", Kind: Delete, Resource: "fileCache", Lookup: "FileCacheId", Accept: []string{"ClientRequestToken"}, Set: map[string]any{"Lifecycle": "DELETING"}},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"FSx for Lustre file systems and Amazon File Cache resources are configuration records that stay CREATING: no file server, network interface, DNS name, mount name or storage exists and no data is read or written. Windows File Server, ONTAP and OpenZFS file systems are refused because they need Active Directory credentials, administrator passwords or volumes the simulator does not provide.",
		"Data repository links (ImportPath, ExportPath, AutoImportPolicy, data repository associations and tasks), backups, final backups on deletion, snapshots, storage virtual machines, volumes and aliases are not implemented; their APIs return UnknownOperationException with HTTP 501. SecurityGroupIds are refused because no network interface is created.",
		"Exactly one subnet ID is accepted, StorageCapacity is required for file systems and Lustre deployments default to SCRATCH_1. UpdateFileSystem and UpdateFileCache are not implemented because they merge nested configuration. Client tokens on deletions are accepted without replaying earlier responses.",
	},
}

// fsxNetwork accepts the single subnet a Lustre file system or cache uses
// and refuses security groups, which would apply to network interfaces the
// simulator does not create.
func fsxNetwork(input map[string]any) error {
	if subnets, _ := input["SubnetIds"].([]any); len(subnets) != 1 {
		return Failure("BadRequest", "Specify exactly one subnet ID.")
	}
	if _, ok := input["SecurityGroupIds"]; ok {
		return Failure("BadRequest", "SecurityGroupIds are not supported: the simulator creates no network interface.")
	}
	return nil
}

// fsxFileSystem accepts only Lustre file systems without S3 data repository
// links, the only type whose creation needs no credentials or volumes.
func fsxFileSystem(input map[string]any) error {
	if input["FileSystemType"] != "LUSTRE" {
		return Failure("BadRequest", "Only LUSTRE file systems are supported: the other types need credentials or volumes the simulator does not provide.")
	}
	for _, member := range []string{"WindowsConfiguration", "OntapConfiguration", "OpenZFSConfiguration"} {
		if _, ok := input[member]; ok {
			return Failure("IncompatibleParameterError", member+" does not apply to a LUSTRE file system.")
		}
	}
	if _, ok := input["StorageCapacity"]; !ok {
		return Failure("BadRequest", "StorageCapacity is required for a Lustre file system.")
	}
	lustre, ok := input["LustreConfiguration"].(map[string]any)
	if !ok {
		lustre = map[string]any{}
		input["LustreConfiguration"] = lustre
	}
	for _, member := range []string{"ImportPath", "ExportPath", "ImportedFileChunkSize", "AutoImportPolicy"} {
		if _, ok := lustre[member]; ok {
			return Failure("BadRequest", "LustreConfiguration."+member+" links an S3 data repository, which the simulator does not implement.")
		}
	}
	if _, ok := lustre["DeploymentType"]; !ok {
		lustre["DeploymentType"] = "SCRATCH_1"
	}
	return fsxNetwork(input)
}

// fsxDeleteFileSystem refuses a final backup, which the simulator cannot take.
func fsxDeleteFileSystem(input map[string]any) error {
	lustre, _ := input["LustreConfiguration"].(map[string]any)
	if lustre["SkipFinalBackup"] == false || lustre["FinalBackupTags"] != nil {
		return Failure("BadRequest", "Final backups are not supported by the simulator; omit SkipFinalBackup false and FinalBackupTags.")
	}
	return nil
}

func fsxFileCache(input map[string]any) error {
	if _, ok := input["LustreConfiguration"]; !ok {
		return Failure("MissingFileCacheConfiguration", "LustreConfiguration is required for a LUSTRE cache.")
	}
	return fsxNetwork(input)
}

var efs = &Service{
	Model: "efs", Title: "Amazon Elastic File System", IAMPrefix: "elasticfilesystem", Issues: []int{277},
	Resources: []*Resource{
		{Kind: "fileSystem", Shape: "FileSystemDescription", ID: "hex:17:fs-", ARN: "arn:{partition}:elasticfilesystem:{region}:{account}:file-system/{id}",
			ARNMember: "FileSystemArn", IDMember: "FileSystemId", Limit: 20,
			Generate: map[string]any{"OwnerId": "$account", "CreationTime": "$now", "LifeCycleState": "available", "NumberOfMountTargets": 0,
				"SizeInBytes":     map[string]any{"Value": 0, "ValueInStandard": 0, "ValueInIA": 0, "ValueInArchive": 0},
				"PerformanceMode": "generalPurpose", "ThroughputMode": "bursting", "Encrypted": false,
				"FileSystemProtection": map[string]any{"ReplicationOverwriteProtection": "ENABLED"}}},
		{Kind: "accountPreferences", Shape: "ResourceIdPreference", Singleton: true, ID: "name",
			ARN: "arn:{partition}:elasticfilesystem:{region}:{account}:account-preferences", NameMember: "ResourceIdType",
			Generate: map[string]any{"ResourceIdType": "LONG_ID", "Resources": []any{"FILE_SYSTEM", "MOUNT_TARGET"}}},
	},
	Ops: []Op{
		{Name: "CreateFileSystem", Kind: Create, Resource: "fileSystem", Check: efsFileSystem},
		{Name: "DescribeFileSystems", Kind: List, Resource: "fileSystem", Filters: map[string]string{"FileSystemId": "FileSystemId"}, Check: efsFileSystemFilter},
		{Name: "UpdateFileSystem", Kind: Update, Resource: "fileSystem", Lookup: "FileSystemId", Fields: []string{"ThroughputMode", "ProvisionedThroughputInMibps"}, Check: efsThroughput},
		{Name: "DeleteFileSystem", Kind: Delete, Resource: "fileSystem", Lookup: "FileSystemId"},
		{Name: "PutAccountPreferences", Kind: Put, Resource: "accountPreferences", Check: efsPreferences},
		{Name: "DescribeAccountPreferences", Kind: Read, Resource: "accountPreferences"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceId", Check: efsResourceARN},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceId", Check: efsResourceARN},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceId", Check: efsResourceARN},
	},
	Bounds: []string{
		"File systems are configuration records that are available immediately with no mount targets: no NFS endpoint, storage or data exists, SizeInBytes is always zero, and mount target, access point, replication, file system policy, lifecycle and backup policy APIs return UnknownOperationException with HTTP 501. The Backup member is refused.",
		"CreationToken is used as the client token: repeating it with identical input returns the original file system instead of FileSystemAlreadyExists, and it is not echoed in descriptions. The Name member, which AWS derives from the Name tag, is not reported, and KMS keys are stored without contacting KMS.",
		"DescribeFileSystems filters by FileSystemId and returns an empty list for an unknown ID; the CreationToken filter is refused. Tagging APIs require the resource ARN, and the legacy CreateTags, DeleteTags and DescribeTags APIs are not implemented. PutAccountPreferences accepts only LONG_ID because identifiers are always long.",
	},
}

func efsThroughput(input map[string]any) error {
	_, provisioned := input["ProvisionedThroughputInMibps"]
	if mode, ok := input["ThroughputMode"]; ok && (mode == "provisioned") != provisioned {
		return Failure("BadRequest", "ProvisionedThroughputInMibps is required with, and only with, the provisioned throughput mode.")
	}
	return nil
}

func efsFileSystem(input map[string]any) error {
	if _, ok := input["KmsKeyId"]; ok && input["Encrypted"] != true {
		return Failure("BadRequest", "Encrypted must be true when KmsKeyId is specified.")
	}
	if _, ok := input["ProvisionedThroughputInMibps"]; ok && input["ThroughputMode"] != "provisioned" {
		return Failure("BadRequest", "ProvisionedThroughputInMibps requires the provisioned throughput mode.")
	}
	return efsThroughput(input)
}

// efsFileSystemFilter compares a FileSystemId given as an ARN by its ID.
func efsFileSystemFilter(input map[string]any) error {
	if id, ok := input["FileSystemId"].(string); ok {
		input["FileSystemId"] = id[strings.LastIndex(id, "/")+1:]
	}
	return nil
}

func efsPreferences(input map[string]any) error {
	if input["ResourceIdType"] != "LONG_ID" {
		return Failure("BadRequest", "Only LONG_ID is supported: the simulator always issues long identifiers.")
	}
	return nil
}

func efsResourceARN(input map[string]any) error {
	if id, _ := input["ResourceId"].(string); !strings.HasPrefix(id, "arn:") {
		return Failure("BadRequest", "Specify the resource ARN: the simulator resolves tagged resources by ARN.")
	}
	return nil
}

var awsBackup = &Service{
	Model: "backup", Title: "AWS Backup", IAMPrefix: "backup", Issues: []int{274},
	AccountMembers: []string{"BackupVaultAccountId"},
	Resources: []*Resource{
		{Kind: "backupVault", Shape: "DescribeBackupVaultOutput", Summary: "BackupVaultListMember", ID: "name",
			ARN:       "arn:{partition}:backup:{region}:{account}:backup-vault:{name}",
			ARNMember: "BackupVaultArn", NameMember: "BackupVaultName", Unique: true, Limit: 100,
			Generate: map[string]any{"CreationDate": "$now", "VaultType": "BACKUP_VAULT", "VaultState": "AVAILABLE", "NumberOfRecoveryPoints": 0, "Locked": false}},
		{Kind: "framework", Shape: "DescribeFrameworkOutput", Summary: "Framework", ID: "uuid",
			ARN:       "arn:{partition}:backup:{region}:{account}:framework:{name}-{id}",
			ARNMember: "FrameworkArn", NameMember: "FrameworkName", Unique: true, Limit: 50,
			Generate: map[string]any{"CreationTime": "$now", "FrameworkStatus": "INACTIVE"}},
		{Kind: "reportPlan", Shape: "ReportPlan", ID: "uuid",
			ARN:       "arn:{partition}:backup:{region}:{account}:report-plan:{name}-{id}",
			ARNMember: "ReportPlanArn", NameMember: "ReportPlanName", Unique: true, Limit: 50,
			Generate: map[string]any{"CreationTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateBackupVault", Kind: Create, Resource: "backupVault"},
		{Name: "DescribeBackupVault", Kind: Read, Resource: "backupVault", Lookup: "BackupVaultName"},
		{Name: "ListBackupVaults", Kind: List, Resource: "backupVault", Filters: map[string]string{"ByVaultType": "VaultType"}},
		{Name: "DeleteBackupVault", Kind: Delete, Resource: "backupVault", Lookup: "BackupVaultName"},
		{Name: "CreateFramework", Kind: Create, Resource: "framework"},
		{Name: "DescribeFramework", Kind: Read, Resource: "framework", Lookup: "FrameworkName"},
		{Name: "ListFrameworks", Kind: List, Resource: "framework"},
		{Name: "UpdateFramework", Kind: Update, Resource: "framework", Lookup: "FrameworkName", Fields: []string{"FrameworkDescription", "FrameworkControls"}, Accept: []string{"IdempotencyToken"}},
		{Name: "DeleteFramework", Kind: Delete, Resource: "framework", Lookup: "FrameworkName"},
		{Name: "CreateReportPlan", Kind: Create, Resource: "reportPlan", Check: backupReportPlan},
		{Name: "DescribeReportPlan", Kind: Read, Resource: "reportPlan", Lookup: "ReportPlanName"},
		{Name: "ListReportPlans", Kind: List, Resource: "reportPlan"},
		{Name: "UpdateReportPlan", Kind: Update, Resource: "reportPlan", Lookup: "ReportPlanName", Fields: []string{"ReportPlanDescription", "ReportDeliveryChannel", "ReportSetting"},
			Accept: []string{"IdempotencyToken"}, Check: backupReportPlan},
		{Name: "DeleteReportPlan", Kind: Delete, Resource: "reportPlan", Lookup: "ReportPlanName"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTags", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Backup vaults, audit frameworks and report plans are stored configuration: no backup, copy, restore, scan or report job runs, vaults never hold recovery points, and StartBackupJob, StartRestoreJob, StartReportJob and the other job APIs return UnknownOperationException with HTTP 501.",
		"Backup plans and selections, legal holds, restore testing, logically air-gapped and restore access vaults, vault access policies, notifications and Vault Lock, and global and Region settings are not implemented. Encryption keys are stored without contacting KMS, and the AWS managed backup key is not reported when EncryptionKeyArn is omitted.",
		"Frameworks report FrameworkStatus INACTIVE and no DeploymentStatus or NumberOfControls because no AWS Config rule is deployed or evaluated. Report plans store their S3 delivery channel without writing to it, ReportDeliveryChannel.Formats defaults to CSV, and framework ARNs in report settings are not checked. CreatorRequestId and IdempotencyToken are client tokens and are not echoed.",
	},
}

var backupReportTemplates = map[string]bool{"RESOURCE_COMPLIANCE_REPORT": true, "CONTROL_COMPLIANCE_REPORT": true, "BACKUP_JOB_REPORT": true,
	"COPY_JOB_REPORT": true, "RESTORE_JOB_REPORT": true, "SCAN_JOB_REPORT": true}

// backupReportPlan applies the documented report template and format values.
func backupReportPlan(input map[string]any) error {
	if setting, ok := input["ReportSetting"].(map[string]any); ok && !backupReportTemplates[dataText(setting["ReportTemplate"])] {
		return Failure("InvalidParameterValueException", "ReportSetting.ReportTemplate is not a documented report template.")
	}
	channel, ok := input["ReportDeliveryChannel"].(map[string]any)
	if !ok {
		return nil
	}
	formats, ok := channel["Formats"].([]any)
	if !ok {
		channel["Formats"] = []any{"CSV"}
		return nil
	}
	for _, format := range formats {
		if format != "CSV" && format != "JSON" {
			return Failure("InvalidParameterValueException", "ReportDeliveryChannel.Formats accepts CSV and JSON.")
		}
	}
	return nil
}

var storageGateway = &Service{
	Model: "storagegateway", Title: "AWS Storage Gateway", IAMPrefix: "storagegateway", Issues: []int{279},
	Resources: []*Resource{
		{Kind: "tapePool", Shape: "PoolInfo", ID: "hex:8:pool-", ARN: "arn:{partition}:storagegateway:{region}:{account}:tapepool/{id}",
			ARNMember: "PoolARN", NameMember: "PoolName", Limit: 50,
			Generate: map[string]any{"PoolStatus": "ACTIVE", "RetentionLockType": "NONE", "RetentionLockTimeInDays": 0}},
	},
	Ops: []Op{
		{Name: "CreateTapePool", Kind: Create, Resource: "tapePool", Check: storageGatewayRetention},
		{Name: "ListTapePools", Kind: List, Resource: "tapePool", Names: "PoolARNs"},
		{Name: "DeleteTapePool", Kind: Delete, Resource: "tapePool", Lookup: "PoolARN"},
		{Name: "AddTagsToResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "RemoveTagsFromResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Only custom tape pools are stored: no gateway is activated or contacted, no tape, volume, file share, cache or upload buffer exists, and ActivateGateway and every gateway, tape, volume and file share API return UnknownOperationException with HTTP 501.",
		"Tape pool IDs use lowercase hexadecimal, pool names are not required to be unique and ListTapePools fails when any requested pool ARN is missing. Unknown members, missing pools and simulator limits are reported as the modeled InvalidGatewayRequestException, and the tagging APIs do not echo ResourceARN.",
	},
}

// storageGatewayRetention requires a lock period exactly when a lock is set.
func storageGatewayRetention(input map[string]any) error {
	days, _ := input["RetentionLockTimeInDays"].(int64)
	locked := input["RetentionLockType"] == "COMPLIANCE" || input["RetentionLockType"] == "GOVERNANCE"
	if locked != (days > 0) {
		return Failure("InvalidGatewayRequestException", "RetentionLockTimeInDays must be positive with, and only with, a COMPLIANCE or GOVERNANCE retention lock.")
	}
	return nil
}

var glacier = &Service{
	Model: "glacier", Title: "Amazon S3 Glacier", IAMPrefix: "glacier", Issues: []int{302},
	Resources: []*Resource{
		{Kind: "vault", Shape: "DescribeVaultOutput", ID: "name", ARN: "arn:{partition}:glacier:{region}:{account}:vaults/{name}",
			ARNMember: "VaultARN", NameMember: "VaultName", Unique: true, Limit: 100, Aliases: map[string]string{"vaultName": "VaultName"},
			Generate: map[string]any{"CreationDate": "$now", "NumberOfArchives": 0, "SizeInBytes": 0, "location": "$fmt:/{account}/vaults/{name}"}},
		{Kind: "dataRetrievalPolicy", Shape: "DataRetrievalPolicy", Singleton: true, ID: "name",
			ARN: "arn:{partition}:glacier:{region}:{account}:policies/data-retrieval", NameMember: "Rules"},
	},
	Ops: []Op{
		{Name: "CreateVault", Kind: Put, Resource: "vault", Lookup: "vaultName", Accept: []string{"accountId"}, Check: glacierVaultName},
		{Name: "DescribeVault", Kind: Read, Resource: "vault", Lookup: "vaultName", Accept: []string{"accountId"}, Check: glacierAccount},
		{Name: "ListVaults", Kind: List, Resource: "vault", Accept: []string{"accountId"}, Check: glacierListVaults},
		{Name: "DeleteVault", Kind: Delete, Resource: "vault", Lookup: "vaultName", Accept: []string{"accountId"}, Check: glacierAccount},
		{Name: "GetDataRetrievalPolicy", Kind: Read, Resource: "dataRetrievalPolicy", Accept: []string{"accountId"}, Check: glacierAccount},
		{Name: "SetDataRetrievalPolicy", Kind: Put, Resource: "dataRetrievalPolicy", From: "Policy", Accept: []string{"accountId"}, Check: glacierRetrievalPolicy},
	},
	Bounds: []string{
		"Vaults are empty configuration records: no archive is uploaded, stored, inventoried or retrieved, NumberOfArchives and SizeInBytes stay zero, and UploadArchive, InitiateJob, multipart upload and provisioned capacity APIs return UnknownOperationException with HTTP 501.",
		"accountId must be - or the simulator account. CreateVault is idempotent for an existing vault. ListVaults returns vaults in creation order rather than ASCII order, up to 100 per page, and its limit member is refused.",
		"Vault tags, access policies, notifications and Vault Lock are not implemented. Before SetDataRetrievalPolicy, GetDataRetrievalPolicy returns a policy without rules instead of the account default, and the stored policy limits no retrieval.",
	},
}

var glacierVaultPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,255}$`)

// glacierAccount accepts the account the request names: "-" for the
// caller's own account, or the simulator account ID.
func glacierAccount(input map[string]any) error {
	if account := input["accountId"]; account != "-" && account != "000000000000" {
		return Failure("InvalidParameterValueException", "accountId must be - or the simulator account 000000000000.")
	}
	return nil
}

func glacierVaultName(input map[string]any) error {
	if !glacierVaultPattern.MatchString(dataText(input["vaultName"])) {
		return Failure("InvalidParameterValueException", "Vault names use 1 to 255 letters, digits, underscores, hyphens and periods.")
	}
	return glacierAccount(input)
}

func glacierListVaults(input map[string]any) error {
	if _, ok := input["limit"]; ok {
		return Failure("InvalidParameterValueException", "The limit member is not supported by the simulator.")
	}
	return glacierAccount(input)
}

// glacierRetrievalPolicy applies the documented single-rule policy shapes.
func glacierRetrievalPolicy(input map[string]any) error {
	policy, ok := input["Policy"].(map[string]any)
	if !ok {
		return Failure("MissingParameterValueException", "Policy is required.")
	}
	rules, _ := policy["Rules"].([]any)
	if len(rules) != 1 {
		return Failure("InvalidParameterValueException", "A data retrieval policy has exactly one rule.")
	}
	rule, _ := rules[0].(map[string]any)
	_, bytes := rule["BytesPerHour"]
	switch rule["Strategy"] {
	case "BytesPerHour":
		if !bytes {
			return Failure("InvalidParameterValueException", "The BytesPerHour strategy requires BytesPerHour.")
		}
	case "FreeTier", "None":
		if bytes {
			return Failure("InvalidParameterValueException", "BytesPerHour applies only to the BytesPerHour strategy.")
		}
	default:
		return Failure("InvalidParameterValueException", "Strategy must be BytesPerHour, FreeTier or None.")
	}
	return glacierAccount(input)
}

var amazonMQ = &Service{
	Model: "mq", Title: "Amazon MQ", IAMPrefix: "mq", Issues: []int{80},
	Resources: []*Resource{
		{Kind: "configuration", Shape: "DescribeConfigurationResponse", Summary: "Configuration", ID: "hex:32:c-",
			ARN:       "arn:{partition}:mq:{region}:{account}:configuration:{id}",
			ARNMember: "Arn", NameMember: "Name", IDMember: "Id", Limit: 100,
			Generate: map[string]any{"Created": "$now", "AuthenticationStrategy": "SIMPLE", "ConfigurationId": "$id",
				"LatestRevision": map[string]any{"Created": "$now", "Revision": 1}}},
	},
	Ops: []Op{
		{Name: "CreateConfiguration", Kind: Create, Resource: "configuration"},
		{Name: "DescribeConfiguration", Kind: Read, Resource: "configuration", Lookup: "ConfigurationId"},
		{Name: "ListConfigurations", Kind: List, Resource: "configuration"},
		{Name: "DeleteConfiguration", Kind: Delete, Resource: "configuration", Lookup: "ConfigurationId"},
		{Name: "CreateTags", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "DeleteTags", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTags", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Only broker configurations are stored: no ActiveMQ or RabbitMQ broker runs and no message is sent or consumed. Brokers and broker users are not implemented because they require user passwords or LDAP service account passwords, which the simulator never stores; their APIs return UnknownOperationException with HTTP 501.",
		"Each configuration keeps revision 1 with no stored XML or Cuttlefish data and no generated description: UpdateConfiguration, DescribeConfigurationRevision and ListConfigurationRevisions are not implemented. Configuration IDs use c- followed by 32 hexadecimal digits.",
	},
}

var firehose = &Service{
	Model: "firehose", Title: "Amazon Data Firehose", IAMPrefix: "firehose", Issues: []int{65},
	Resources: []*Resource{
		{Kind: "deliveryStream", Shape: "DeliveryStreamDescription", ID: "name",
			ARN:       "arn:{partition}:firehose:{region}:{account}:deliverystream/{name}",
			ARNMember: "DeliveryStreamARN", NameMember: "DeliveryStreamName", Unique: true, Limit: 50,
			Generate: map[string]any{"DeliveryStreamStatus": "CREATING", "DeliveryStreamType": "DirectPut", "VersionId": "1", "CreateTimestamp": "$now",
				"HasMoreDestinations": false, "DeliveryStreamEncryptionConfiguration": map[string]any{"Status": "DISABLED"}}},
	},
	Ops: []Op{
		{Name: "CreateDeliveryStream", Kind: Create, Resource: "deliveryStream", Check: firehoseCreate},
		{Name: "DescribeDeliveryStream", Kind: Read, Resource: "deliveryStream", Lookup: "DeliveryStreamName"},
		{Name: "ListDeliveryStreams", Kind: List, Resource: "deliveryStream", ListValue: "DeliveryStreamName", HasMore: "HasMoreDeliveryStreams",
			Filters: map[string]string{"DeliveryStreamType": "DeliveryStreamType"}, Check: firehoseList},
		{Name: "DeleteDeliveryStream", Kind: Delete, Resource: "deliveryStream", Lookup: "DeliveryStreamName", Accept: []string{"AllowForceDelete"}},
		{Name: "StartDeliveryStreamEncryption", Kind: Update, Resource: "deliveryStream", Lookup: "DeliveryStreamName",
			Fields: []string{"DeliveryStreamEncryptionConfiguration"}, Check: firehoseEncryption},
		{Name: "StopDeliveryStreamEncryption", Kind: Update, Resource: "deliveryStream", Lookup: "DeliveryStreamName",
			Set: map[string]any{"DeliveryStreamEncryptionConfiguration": map[string]any{"Status": "DISABLED"}}},
	},
	Bounds: []string{
		"Firehose streams are configuration records that stay CREATING: no record is accepted, buffered, transformed or delivered, and PutRecord, PutRecordBatch and UpdateDestination return UnknownOperationException with HTTP 501.",
		"Only DirectPut streams with one Amazon S3 destination (ExtendedS3DestinationConfiguration) are supported; buffering defaults to 5 MiB and 300 seconds, compression to UNCOMPRESSED and encryption to NoEncryption. Lambda processing, S3 backup, format conversion, dynamic partitioning, Kinesis, MSK and database sources and every other destination are refused, so no Redshift password, Splunk token, HTTP endpoint key or Snowflake private key is stored. The bucket and role are not checked or written.",
		"Server-side encryption becomes ENABLED or DISABLED immediately and keys are not checked with KMS. Tags are refused because TagDeliveryStream, UntagDeliveryStream and ListTagsForDeliveryStream address streams by name, which the catalog tagging model does not support. ListDeliveryStreams returns every stream in one page and refuses Limit.",
	},
}

// firehoseCreate accepts a DirectPut stream with one S3 destination and
// stores it as the official destination description with documented defaults.
func firehoseCreate(input map[string]any) error {
	if kind, ok := input["DeliveryStreamType"]; ok && kind != "DirectPut" {
		return Failure("InvalidArgumentException", "Only DirectPut streams are supported: the simulator reads no Kinesis, MSK or database source.")
	}
	if _, ok := input["Tags"]; ok {
		return Failure("InvalidArgumentException", "Stream tags are not supported because the name-based tagging APIs are not implemented.")
	}
	destination, ok := input["ExtendedS3DestinationConfiguration"].(map[string]any)
	if !ok {
		return Failure("InvalidArgumentException", "Specify ExtendedS3DestinationConfiguration: the simulator supports only Amazon S3 destinations.")
	}
	for _, member := range []string{"ProcessingConfiguration", "S3BackupConfiguration", "DataFormatConversionConfiguration", "DynamicPartitioningConfiguration"} {
		if _, ok := destination[member]; ok {
			return Failure("InvalidArgumentException", "ExtendedS3DestinationConfiguration."+member+" is not supported by the simulator.")
		}
	}
	if destination["S3BackupMode"] == "Enabled" {
		return Failure("InvalidArgumentException", "S3 backup is not supported by the simulator.")
	}
	hints, ok := destination["BufferingHints"].(map[string]any)
	if !ok {
		hints = map[string]any{"SizeInMBs": int64(5), "IntervalInSeconds": int64(300)}
		destination["BufferingHints"] = hints
	}
	if _, size := hints["SizeInMBs"]; size != (hints["IntervalInSeconds"] != nil) {
		return Failure("InvalidArgumentException", "BufferingHints needs both SizeInMBs and IntervalInSeconds.")
	}
	if _, ok := destination["CompressionFormat"]; !ok {
		destination["CompressionFormat"] = "UNCOMPRESSED"
	}
	if _, ok := destination["EncryptionConfiguration"]; !ok {
		destination["EncryptionConfiguration"] = map[string]any{"NoEncryptionConfig": "NoEncryption"}
	}
	delete(input, "ExtendedS3DestinationConfiguration")
	input["Destinations"] = []any{map[string]any{"DestinationId": "destinationId-000000000001", "ExtendedS3DestinationDescription": destination}}
	if _, ok := input["DeliveryStreamEncryptionConfigurationInput"]; ok {
		return firehoseEncryption(input)
	}
	return nil
}

// firehoseEncryption stores the requested server-side encryption as enabled.
func firehoseEncryption(input map[string]any) error {
	requested, ok := input["DeliveryStreamEncryptionConfigurationInput"].(map[string]any)
	if !ok {
		requested = map[string]any{"KeyType": "AWS_OWNED_CMK"}
	}
	_, keyed := requested["KeyARN"]
	if (requested["KeyType"] == "CUSTOMER_MANAGED_CMK") != keyed {
		return Failure("InvalidArgumentException", "KeyARN is required with, and only with, CUSTOMER_MANAGED_CMK.")
	}
	delete(input, "DeliveryStreamEncryptionConfigurationInput")
	configuration := map[string]any{"KeyType": requested["KeyType"], "Status": "ENABLED"}
	if keyed {
		configuration["KeyARN"] = requested["KeyARN"]
	}
	input["DeliveryStreamEncryptionConfiguration"] = configuration
	return nil
}

func firehoseList(input map[string]any) error {
	if _, ok := input["Limit"]; ok {
		return Failure("ValidationException", "Limit is not supported: the simulator returns every stream in one page.")
	}
	return nil
}

// mskClusterVersion is the constant CurrentVersion of every cluster: no
// update operation is implemented, so a cluster's version never changes.
const mskClusterVersion = "K1"

var msk = &Service{
	Model: "kafka", Title: "Amazon Managed Streaming for Apache Kafka", IAMPrefix: "kafka", Issues: []int{74},
	Resources: []*Resource{
		{Kind: "cluster", Shape: "ClusterInfo", ID: "uuid", ARN: "arn:{partition}:kafka:{region}:{account}:cluster/{name}/{id}",
			ARNMember: "ClusterArn", NameMember: "ClusterName", Unique: true, Limit: 20,
			Generate: map[string]any{"State": "CREATING", "CreationTime": "$now", "CurrentVersion": mskClusterVersion, "EnhancedMonitoring": "DEFAULT", "StorageMode": "LOCAL",
				"EncryptionInfo": map[string]any{"EncryptionInTransit": map[string]any{"ClientBroker": "TLS_PLAINTEXT", "InCluster": true}}}},
	},
	Ops: []Op{
		{Name: "CreateCluster", Kind: Create, Resource: "cluster", Check: mskCreateCluster},
		{Name: "DescribeCluster", Kind: Read, Resource: "cluster", Lookup: "ClusterArn"},
		{Name: "ListClusters", Kind: List, Resource: "cluster"},
		{Name: "DeleteCluster", Kind: Delete, Resource: "cluster", Lookup: "ClusterArn", Accept: []string{"CurrentVersion"}, Check: mskCurrentVersion,
			Set: map[string]any{"State": "DELETING"}},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Provisioned clusters are configuration records that stay CREATING: no broker, ZooKeeper or KRaft controller, topic, bootstrap endpoint or network interface exists, no record is produced or consumed, and GetBootstrapBrokers, topic, replicator, VPC connection and SCRAM secret APIs return UnknownOperationException with HTTP 501.",
		"The V2 and serverless APIs, configurations (whose server properties need revision storage), ConfigurationInfo, cluster policies and every cluster update operation are not implemented. CurrentVersion is the constant K1 because no update is possible, and DeleteCluster refuses any other version.",
		"NumberOfBrokerNodes must be a multiple of the number of client subnets; subnets, security groups, certificate authorities, KMS keys and log destinations are stored without being checked. ListClusters refuses ClusterNameFilter.",
	},
}

// mskCreateCluster checks the broker layout and stores the requested Kafka
// version as the official current broker software.
func mskCreateCluster(input map[string]any) error {
	if _, ok := input["ConfigurationInfo"]; ok {
		return Failure("BadRequestException", "MSK configurations are not supported by the simulator; omit ConfigurationInfo.")
	}
	group, _ := input["BrokerNodeGroupInfo"].(map[string]any)
	subnets, _ := group["ClientSubnets"].([]any)
	brokers, _ := input["NumberOfBrokerNodes"].(int64)
	if len(subnets) == 0 || brokers%int64(len(subnets)) != 0 {
		return Failure("BadRequestException", "NumberOfBrokerNodes must be a multiple of the number of client subnets.")
	}
	input["CurrentBrokerSoftwareInfo"] = map[string]any{"KafkaVersion": input["KafkaVersion"]}
	delete(input, "KafkaVersion")
	return nil
}

func mskCurrentVersion(input map[string]any) error {
	if version, ok := input["CurrentVersion"]; ok && version != mskClusterVersion {
		return Failure("BadRequestException", "CurrentVersion does not match the cluster's current version.")
	}
	return nil
}

var oracleDatabaseAtAWS = &Service{
	Model: "odb", Title: "Oracle Database@AWS", IAMPrefix: "odb", Issues: []int{129},
	Resources: []*Resource{
		{Kind: "odbNetwork", Shape: "OdbNetwork", Summary: "OdbNetworkSummary", ID: "alnum:10:odbnet_",
			ARN:       "arn:{partition}:odb:{region}:{account}:odb-network/{id}",
			ARNMember: "odbNetworkArn", NameMember: "displayName", IDMember: "odbNetworkId", Limit: 10,
			Generate: map[string]any{"status": "PROVISIONING", "createdAt": "$now"}},
		{Kind: "cloudExadataInfrastructure", Shape: "CloudExadataInfrastructure", Summary: "CloudExadataInfrastructureSummary", ID: "alnum:10:exa_",
			ARN:       "arn:{partition}:odb:{region}:{account}:cloud-exadata-infrastructure/{id}",
			ARNMember: "cloudExadataInfrastructureArn", NameMember: "displayName", IDMember: "cloudExadataInfrastructureId", Limit: 10,
			Generate: map[string]any{"status": "PROVISIONING", "createdAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateOdbNetwork", Kind: Create, Resource: "odbNetwork", Check: odbNetwork},
		{Name: "GetOdbNetwork", Kind: Read, Resource: "odbNetwork", Lookup: "odbNetworkId"},
		{Name: "ListOdbNetworks", Kind: List, Resource: "odbNetwork"},
		{Name: "UpdateOdbNetwork", Kind: Update, Resource: "odbNetwork", Lookup: "odbNetworkId", Fields: []string{"displayName"}},
		{Name: "DeleteOdbNetwork", Kind: Delete, Resource: "odbNetwork", Lookup: "odbNetworkId", Accept: []string{"deleteAssociatedResources"}},
		{Name: "CreateCloudExadataInfrastructure", Kind: Create, Resource: "cloudExadataInfrastructure", Check: odbZone},
		{Name: "GetCloudExadataInfrastructure", Kind: Read, Resource: "cloudExadataInfrastructure", Lookup: "cloudExadataInfrastructureId"},
		{Name: "ListCloudExadataInfrastructures", Kind: List, Resource: "cloudExadataInfrastructure"},
		{Name: "UpdateCloudExadataInfrastructure", Kind: Update, Resource: "cloudExadataInfrastructure", Lookup: "cloudExadataInfrastructureId", Fields: []string{"maintenanceWindow"}},
		{Name: "DeleteCloudExadataInfrastructure", Kind: Delete, Resource: "cloudExadataInfrastructure", Lookup: "cloudExadataInfrastructureId"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"ODB networks and Exadata infrastructures are configuration records that stay PROVISIONING: nothing is provisioned in Oracle Cloud Infrastructure, no OCI identifier, URL, capacity or maintenance run is reported, and customer contacts are stored without being sent to OCI.",
		"Marketplace registration and OCI onboarding are not modeled and not required. VM clusters, Autonomous VM clusters, database servers and nodes, peering connections, IAM role association and the shape, version and onboarding status APIs (ListDbSystemShapes, ListGiVersions, ListSystemVersions, GetOciOnboardingStatus) return UnknownOperationException with HTTP 501.",
		"Managed service access (S3, Zero-ETL, STS, KMS and their policy documents), cross-Region restore sources and peered CIDR changes are refused. Client subnet CIDRs must parse, avoid the OCI reserved ranges and not overlap the backup subnet. ODB network deletion accepts deleteAssociatedResources because no associated resource exists.",
	},
}

func odbZone(input map[string]any) error {
	_, zone := input["availabilityZone"]
	_, zoneID := input["availabilityZoneId"]
	if !zone && !zoneID {
		return Failure("ValidationException", "Specify availabilityZone or availabilityZoneId.")
	}
	return nil
}

// odbReservedRanges are the CIDR ranges reserved by OCI for ODB networks.
var odbReservedRanges = []string{"100.106.0.0/16", "100.107.0.0/16", "169.254.0.0/16", "224.0.0.0/4", "240.0.0.0/4"}

// dataText returns a string input value, or "" when it is absent.
func dataText(value any) string {
	text, _ := value.(string)
	return text
}

func cidrsOverlap(a, b *net.IPNet) bool { return a.Contains(b.IP) || b.Contains(a.IP) }

// odbNetwork applies the documented subnet constraints of an ODB network.
func odbNetwork(input map[string]any) error {
	if err := odbZone(input); err != nil {
		return err
	}
	_, client, err := net.ParseCIDR(dataText(input["clientSubnetCidr"]))
	if err != nil {
		return Failure("ValidationException", "clientSubnetCidr must be a CIDR block.")
	}
	blocks := []*net.IPNet{client}
	if text, ok := input["backupSubnetCidr"].(string); ok {
		_, backup, err := net.ParseCIDR(text)
		if err != nil {
			return Failure("ValidationException", "backupSubnetCidr must be a CIDR block.")
		}
		if cidrsOverlap(client, backup) {
			return Failure("ValidationException", "The client and backup subnet CIDR ranges must not overlap.")
		}
		blocks = append(blocks, backup)
	}
	for _, reserved := range odbReservedRanges {
		_, block, _ := net.ParseCIDR(reserved)
		for _, subnet := range blocks {
			if cidrsOverlap(block, subnet) {
				return Failure("ValidationException", "Subnet CIDR ranges must not use the ranges reserved by OCI.")
			}
		}
	}
	return nil
}
