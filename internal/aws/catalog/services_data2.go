package catalog

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Query-protocol database services whose bound resources are stored
// configuration only: Amazon ElastiCache and Amazon Redshift. Nothing that
// provisions or runs a cache or a warehouse is bound.

var (
	// querySubnetGroupPattern is the documented "no more than 255
	// alphanumeric characters or hyphens" of cache and cluster subnet groups.
	querySubnetGroupPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,255}$`)
	// queryGroupPattern is the documented "1 to 255 alphanumeric characters or
	// hyphens, first character a letter" of Redshift parameter group names.
	queryGroupPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,254}$`)
	elastiCacheARN    = regexp.MustCompile(`^arn:[a-z-]+:elasticache:[a-z0-9-]*:[0-9]{12}:[a-z]+:[^:]+$`)

	// elastiCacheFamilies are the "valid values" the official documentation of
	// CreateCacheParameterGroup and DescribeCacheEngineVersions lists.
	elastiCacheFamilies = []string{"memcached1.4", "memcached1.5", "memcached1.6", "redis2.6", "redis2.8", "redis3.2", "redis4.0", "redis5.0",
		"redis6.x", "redis6.2", "redis7", "valkey7", "valkey8"}
	elastiCacheEngines = map[string]string{"redis": "6.0", "valkey": "7.2"}
)

// queryMaxRecords applies a documented MaxRecords range.
func queryMaxRecords(input map[string]any, low, high int64) error {
	if n, ok := input["MaxRecords"].(int64); ok && (n < low || n > high) {
		return invalid("MaxRecords must be from %d to %d.", low, high)
	}
	return nil
}

// queryTags refuses tags without a key and the reserved aws: prefix, and
// more tags than one resource may hold in a single request.
func queryTags(input map[string]any, invalidCode, quotaCode string) error {
	tags, _ := input["Tags"].([]any)
	if len(tags) > maxTagsPerResource {
		return Failure(quotaCode, "A resource holds at most 50 tags.")
	}
	for _, tag := range tags {
		key, ok := tag.(map[string]any)["Key"].(string)
		if !ok || key == "" || strings.HasPrefix(strings.ToLower(key), "aws:") {
			return Failure(invalidCode, "Tag keys are required and must not start with aws:.")
		}
	}
	return nil
}

// querySubnets stores the requested subnet IDs as the official Subnets
// list. Subnet IDs are not resolved, so their zones and VPC are unknown.
func querySubnets(input map[string]any, quotaCode string) error {
	ids, _ := input["SubnetIds"].([]any)
	seen := map[string]bool{}
	subnets := make([]any, 0, len(ids))
	for _, value := range ids {
		id, _ := value.(string)
		if !rdsSubnetPattern.MatchString(id) {
			return Failure("InvalidSubnet", "Subnet IDs look like subnet-0123456789abcdef0.")
		}
		if seen[id] {
			return invalid("Subnet %s is listed twice.", id)
		}
		seen[id] = true
		subnets = append(subnets, map[string]any{"SubnetIdentifier": id})
	}
	if len(subnets) == 0 {
		return invalid("Specify at least one subnet ID.")
	}
	if len(subnets) > 20 {
		return Failure(quotaCode, "A subnet group holds at most 20 subnets.")
	}
	delete(input, "SubnetIds")
	input["Subnets"] = subnets
	return nil
}

// queryHyphenName applies the shared rule that a name neither ends with a
// hyphen nor contains two consecutive hyphens.
func queryHyphenName(name string) error {
	if !queryGroupPattern.MatchString(name) || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return invalid("Names are 1 to 255 letters, digits or hyphens, start with a letter and contain no trailing or consecutive hyphens.")
	}
	return nil
}

// queryDescribe lowercases a stored-lowercase name filter and applies the
// documented MaxRecords range of the Describe operation.
func queryDescribe(member string, lower bool) func(map[string]any) error {
	return func(input map[string]any) error {
		if lower {
			lowerMember(input, member)
		}
		return queryMaxRecords(input, 20, 100)
	}
}

// elastiCache serves cache parameter groups, cache subnet groups,
// IAM-authenticated users and, as configuration records, cache clusters,
// replication groups and serverless caches over the Query protocol. Nothing
// that acts on a running cache is bound.
var elastiCache = &Service{
	Model: "elasticache", Title: "Amazon ElastiCache", IAMPrefix: "elasticache", Issues: []int{125},
	Resources: []*Resource{
		{Kind: "cacheParameterGroup", Shape: "CacheParameterGroup", ID: "name", ARN: "arn:{partition}:elasticache:{region}:{account}:parametergroup:{name}",
			ARNMember: "ARN", NameMember: "CacheParameterGroupName", Unique: true, Limit: 50, Generate: map[string]any{"IsGlobal": false}},
		{Kind: "cacheSubnetGroup", Shape: "CacheSubnetGroup", ID: "name", ARN: "arn:{partition}:elasticache:{region}:{account}:subnetgroup:{name}",
			ARNMember: "ARN", NameMember: "CacheSubnetGroupName", Unique: true, Limit: 50},
		{Kind: "user", Shape: "User", ID: "name", ARN: "arn:{partition}:elasticache:{region}:{account}:user:{name}",
			ARNMember: "ARN", NameMember: "UserId", Unique: true, Limit: 100,
			Generate: map[string]any{"Status": "active", "UserGroupIds": []any{}}},
		{Kind: "cacheCluster", Shape: "CacheCluster", ID: "name", ARN: "arn:{partition}:elasticache:{region}:{account}:cluster:{name}",
			ARNMember: "ARN", NameMember: "CacheClusterId", Unique: true, Limit: 50,
			Generate: map[string]any{"CacheClusterStatus": "creating", "CacheClusterCreateTime": "$now"}},
		{Kind: "replicationGroup", Shape: "ReplicationGroup", ID: "name", ARN: "arn:{partition}:elasticache:{region}:{account}:replicationgroup:{name}",
			ARNMember: "ARN", NameMember: "ReplicationGroupId", Unique: true, Limit: 50, Aliases: map[string]string{"ReplicationGroupDescription": "Description"},
			Generate: map[string]any{"Status": "creating", "ReplicationGroupCreateTime": "$now"}},
		{Kind: "serverlessCache", Shape: "ServerlessCache", ID: "name", ARN: "arn:{partition}:elasticache:{region}:{account}:serverlesscache:{name}",
			ARNMember: "ARN", NameMember: "ServerlessCacheName", Unique: true, Limit: 40,
			Generate: map[string]any{"Status": "creating", "CreateTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateCacheParameterGroup", Kind: Create, Resource: "cacheParameterGroup", Check: elastiCacheCreateParameterGroup},
		{Name: "DescribeCacheParameterGroups", Kind: List, Resource: "cacheParameterGroup", Names: "CacheParameterGroupName", Check: queryDescribe("", false)},
		{Name: "DeleteCacheParameterGroup", Kind: Delete, Resource: "cacheParameterGroup", Lookup: "CacheParameterGroupName"},
		{Name: "CreateCacheSubnetGroup", Kind: Create, Resource: "cacheSubnetGroup", Check: elastiCacheCreateSubnetGroup},
		{Name: "DescribeCacheSubnetGroups", Kind: List, Resource: "cacheSubnetGroup", Names: "CacheSubnetGroupName", Check: queryDescribe("CacheSubnetGroupName", true)},
		{Name: "ModifyCacheSubnetGroup", Kind: Update, Resource: "cacheSubnetGroup", Lookup: "CacheSubnetGroupName",
			Fields: []string{"CacheSubnetGroupDescription", "Subnets"}, Check: elastiCacheModifySubnetGroup},
		{Name: "DeleteCacheSubnetGroup", Kind: Delete, Resource: "cacheSubnetGroup", Lookup: "CacheSubnetGroupName", Check: func(input map[string]any) error {
			lowerMember(input, "CacheSubnetGroupName")
			return nil
		}},
		{Name: "CreateUser", Kind: Create, Resource: "user", Check: elastiCacheCreateUser},
		{Name: "DescribeUsers", Kind: List, Resource: "user", Names: "UserId", Filters: map[string]string{"Engine": "Engine"}},
		{Name: "ModifyUser", Kind: Update, Resource: "user", Lookup: "UserId", Fields: []string{"AccessString", "Engine"},
			Check: elastiCacheModifyUser, Merged: elastiCacheUserVersion},
		{Name: "DeleteUser", Kind: Delete, Resource: "user", Lookup: "UserId", Set: map[string]any{"Status": "deleting"}},
		{Name: "CreateCacheCluster", Kind: Create, Resource: "cacheCluster", Check: elastiCacheCreateCluster},
		{Name: "DescribeCacheClusters", Kind: List, Resource: "cacheCluster", Names: "CacheClusterId", Check: elastiCacheDescribeClusters},
		{Name: "ModifyCacheCluster", Kind: Update, Resource: "cacheCluster", Lookup: "CacheClusterId", Accept: []string{"ApplyImmediately"},
			Fields: elastiCacheClusterFields, Check: elastiCacheModifyCluster, Merged: elastiCacheClusterNodes},
		{Name: "DeleteCacheCluster", Kind: Delete, Resource: "cacheCluster", Lookup: "CacheClusterId",
			Check: elastiCacheDeleteRecord("CacheClusterId", "FinalSnapshotIdentifier"), Set: map[string]any{"CacheClusterStatus": "deleting"}},
		{Name: "CreateReplicationGroup", Kind: Create, Resource: "replicationGroup", Check: elastiCacheCreateGroup},
		{Name: "DescribeReplicationGroups", Kind: List, Resource: "replicationGroup", Names: "ReplicationGroupId", Check: queryDescribe("ReplicationGroupId", true)},
		{Name: "ModifyReplicationGroup", Kind: Update, Resource: "replicationGroup", Lookup: "ReplicationGroupId", Accept: []string{"ApplyImmediately"},
			Fields: elastiCacheGroupFields, Check: elastiCacheModifyGroup},
		{Name: "DeleteReplicationGroup", Kind: Delete, Resource: "replicationGroup", Lookup: "ReplicationGroupId", Accept: []string{"RetainPrimaryCluster"},
			Check: elastiCacheDeleteRecord("ReplicationGroupId", "FinalSnapshotIdentifier"), Set: map[string]any{"Status": "deleting"}},
		{Name: "CreateServerlessCache", Kind: Create, Resource: "serverlessCache", Check: elastiCacheCreateServerless},
		{Name: "DescribeServerlessCaches", Kind: List, Resource: "serverlessCache", Names: "ServerlessCacheName", Check: func(input map[string]any) error {
			lowerMember(input, "ServerlessCacheName")
			return nil
		}},
		{Name: "ModifyServerlessCache", Kind: Update, Resource: "serverlessCache", Lookup: "ServerlessCacheName",
			Fields: elastiCacheServerlessFields, Check: elastiCacheModifyServerless},
		{Name: "DeleteServerlessCache", Kind: Delete, Resource: "serverlessCache", Lookup: "ServerlessCacheName",
			Check: elastiCacheDeleteRecord("ServerlessCacheName", "FinalSnapshotName"), Set: map[string]any{"Status": "deleting"}},
		{Name: "AddTagsToResource", Kind: Tag, Lookup: "ResourceName", Check: elastiCacheTagResource},
		{Name: "RemoveTagsFromResource", Kind: Untag, Lookup: "ResourceName", Check: elastiCacheResourceName},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceName", Check: elastiCacheResourceName},
	},
	Bounds: []string{
		"Cache clusters, replication groups and serverless caches are stored configuration records that stay in the creating state: no cache engine, node, shard, replica, endpoint, address or port exists, no endpoint, address, port, node list or node group is ever returned, and no key is stored or read. Cache parameter groups, cache subnet groups and IAM-authenticated users are stored configuration too. Operations that act on a running cache (RebootCacheCluster, IncreaseReplicaCount, DecreaseReplicaCount, TestFailover, TestMigration, StartMigration, CompleteMigration, snapshots, copies and restores, global datastores, security groups, reserved nodes, service updates) and every other official operation return UnknownOperationException with HTTP 501.",
		"The simulator stores no credentials: AuthToken and AuthTokenUpdateStrategy are refused with InvalidParameterValue instead of being accepted and discarded, so a record is created without an auth token (and, like a user, without a password); Port is refused because no listener exists. SnapshotArns, SnapshotName and ServerlessCacheSnapshotName (restores), CacheSecurityGroupNames (EC2-Classic), UserGroupId and UserGroupIds, LogDeliveryConfigurations (the request and description shapes differ), outposts, global replication groups, a cache cluster's ReplicationGroupId and every member the official description does not hold are refused as well. A replication group description holds no engine version, maintenance window, notification topic, parameter group, subnet group, security group, node count or node group, so EngineVersion, CacheParameterGroupName, CacheSubnetGroupName, SecurityGroupIds, PreferredMaintenanceWindow, NotificationTopicArn, NumCacheClusters, NumNodeGroups, ReplicasPerNodeGroup, PreferredCacheClusterAZs and NodeGroupConfiguration are refused for replication groups, which therefore have no MemberClusters.",
		"User groups are not implemented: a group references users by ID (a member the engine cannot check against existing users), requires the account's default user, which is not modeled, and must drop a deleted user and keep ReplicationGroups and ServerlessCaches in step, none of which the stored model expresses, so CreateUserGroup, ModifyUserGroup, DescribeUserGroups and DeleteUserGroup return UnknownOperationException with HTTP 501, users always report no UserGroupIds, and records cannot name user groups.",
		"A record keeps exactly the settings it was given: engine versions, node types, maintenance and snapshot windows and similar settings are stored without checking them against ElastiCache offerings and no default is filled in. CacheClusterStatus and Status are creating from creation until deletion; Modify operations are accepted in any state and apply immediately (ApplyImmediately is accepted, no pending values exist), and Delete returns the record with status deleting and refuses FinalSnapshotIdentifier and FinalSnapshotName because no snapshot can be taken (RetainPrimaryCluster is accepted and has no effect because a replication group has no member clusters). Identifiers are letters, digits and hyphens that start with a letter and neither end with a hyphen nor contain two consecutive hyphens (1 to 50 characters for cache clusters, 40 for replication groups and serverless caches) and are stored in lowercase; Engine is memcached, redis or valkey (redis or valkey for replication groups); NumCacheNodes is 1 for redis and valkey and 1 to 40 for memcached; AutomaticFailoverEnabled, MultiAZEnabled and DataTieringEnabled are reported as enabled or disabled. At most 50 cache clusters, 50 replication groups and 40 serverless caches exist per Region (local limits); exceeding them answers InvalidParameterValue for cache clusters and serverless caches and NodeGroupsPerReplicationGroupQuotaExceeded, the modeled quota fault the engine selects, for replication groups.",
		"A cache cluster reports its parameter group (status in-sync), notification topic (not checked, status active) and security groups (status active) by ID or name only, and names its cache subnet group, which, like the parameter group, need not exist and can be deleted while in use. DescribeCacheClusters refuses ShowCacheNodeInfo true because no node exists, and treats ShowCacheClustersNotInReplicationGroups as having no effect because no cluster belongs to a replication group.",
		"Users must use iam authentication and the engine valkey or redis: passwords and NoPasswordRequired are refused so no password is stored, the user name must equal the user ID (the ElastiCache rule for IAM-enabled users), and the default user is refused and not pre-created. Access strings are stored without parsing, and AppendAccessString is not implemented. MinimumEngineVersion is 6.0 for redis and 7.2 for valkey as documented; DeleteUser returns the user with status deleting.",
		"Parameter values are not modeled: ModifyCacheParameterGroup, ResetCacheParameterGroup, DescribeCacheParameters, DescribeEngineDefaultParameters and DescribeCacheEngineVersions are not implemented, and default parameter groups and the default cache subnet group are not pre-created. A parameter group family must be one of the documented values and is not checked against engine versions; names are stored as given and IsGlobal is always false.",
		"A cache subnet group needs 1 to 20 distinct subnet-xxxxxxxx or subnet-xxxxxxxxxxxxxxxxx IDs that are not resolved, so it reports no VPC, Availability Zone, Outpost or supported network type, and the CacheSubnetQuotaExceededFault limit of 20 is a local limit. Cache subnet group names are 1 to 255 letters, digits or hyphens and are stored in lowercase.",
		"Describe operations refuse Marker values that were not issued for the request, accept MaxRecords from 20 to 100 for parameter groups, subnet groups, cache clusters and replication groups (DescribeServerlessCaches takes the official MaxResults) and fail with the operation's not-found fault for an unknown name; DescribeUsers refuses Filters and a MaxRecords below 1. Resource counts per Region (50 parameter groups, 50 subnet groups, 100 users) are local limits.",
		"Tags apply to the six resource kinds by ARN (arn:aws:elasticache:region:account:parametergroup:name, subnetgroup:name, user:id, cluster:id, replicationgroup:id and serverlesscache:name). Tag keys are required and must not start with aws:. AddTagsToResource and RemoveTagsFromResource return an empty TagList, removing a missing key succeeds instead of failing with TagNotFound, and a well-formed ARN of an unknown resource fails with CacheClusterNotFound, the first not-found fault the official tag operations model; a malformed ARN fails with InvalidARN. More than 50 tags in one request fail with TagQuotaPerResourceExceeded, but adding tags that bring the total beyond 50 fails with InvalidParameterValue.",
	},
}

func elastiCacheCreateParameterGroup(input map[string]any) error {
	if family, _ := input["CacheParameterGroupFamily"].(string); !slices.Contains(elastiCacheFamilies, family) {
		return Failure("InvalidParameterValueException", "CacheParameterGroupFamily must be one of "+strings.Join(elastiCacheFamilies, ", ")+".")
	}
	return queryTags(input, "InvalidParameterValueException", "TagQuotaPerResourceExceeded")
}

func elastiCacheCreateSubnetGroup(input map[string]any) error {
	name, _ := input["CacheSubnetGroupName"].(string)
	if !querySubnetGroupPattern.MatchString(name) {
		return Failure("InvalidParameterValueException", "Cache subnet group names are 1 to 255 letters, digits or hyphens.")
	}
	lowerMember(input, "CacheSubnetGroupName")
	if err := querySubnets(input, "CacheSubnetQuotaExceededFault"); err != nil {
		return err
	}
	return queryTags(input, "InvalidParameterValueException", "TagQuotaPerResourceExceeded")
}

func elastiCacheModifySubnetGroup(input map[string]any) error {
	lowerMember(input, "CacheSubnetGroupName")
	if _, ok := input["SubnetIds"]; !ok {
		return nil
	}
	return querySubnets(input, "CacheSubnetQuotaExceededFault")
}

// elastiCacheNoPasswords refuses every way of storing or waiving a password.
func elastiCacheNoPasswords(input map[string]any) error {
	for _, member := range []string{"Passwords", "NoPasswordRequired"} {
		if _, ok := input[member]; ok {
			return Failure("InvalidParameterValueException", "Only iam authentication is supported: the simulator never stores passwords.")
		}
	}
	if mode, ok := input["AuthenticationMode"].(map[string]any); ok {
		if mode["Type"] != "iam" || mode["Passwords"] != nil {
			return Failure("InvalidParameterValueException", "Only iam authentication is supported: the simulator never stores passwords.")
		}
	}
	return nil
}

func elastiCacheEngine(input map[string]any) error {
	if engine, ok := input["Engine"].(string); ok {
		if _, known := elastiCacheEngines[engine]; !known {
			return Failure("InvalidParameterValueException", "Engine must be valkey or redis.")
		}
	}
	return nil
}

func elastiCacheCreateUser(input map[string]any) error {
	if err := elastiCacheNoPasswords(input); err != nil {
		return err
	}
	if _, ok := input["AuthenticationMode"]; !ok {
		return Failure("InvalidParameterCombinationException", "Specify AuthenticationMode with Type iam.")
	}
	id, _ := input["UserId"].(string)
	if name, _ := input["UserName"].(string); name != id {
		return Failure("InvalidParameterValueException", "The user name of an IAM-authenticated user must equal its user ID.")
	}
	if strings.EqualFold(id, "default") {
		return Failure("InvalidParameterValueException", "The default user is reserved and not modeled by the simulator.")
	}
	if err := elastiCacheEngine(input); err != nil {
		return err
	}
	input["MinimumEngineVersion"] = elastiCacheEngines[input["Engine"].(string)]
	delete(input, "AuthenticationMode")
	input["Authentication"] = map[string]any{"Type": "iam"}
	return queryTags(input, "InvalidParameterValueException", "TagQuotaPerResourceExceeded")
}

func elastiCacheModifyUser(input map[string]any) error {
	if err := elastiCacheNoPasswords(input); err != nil {
		return err
	}
	delete(input, "AuthenticationMode")
	return elastiCacheEngine(input)
}

// elastiCacheUserVersion keeps MinimumEngineVersion in step with a changed
// engine.
func elastiCacheUserVersion(document, supplied map[string]any) error {
	if engine, ok := supplied["Engine"].(string); ok {
		document["MinimumEngineVersion"] = elastiCacheEngines[engine]
	}
	return nil
}

func elastiCacheResourceName(input map[string]any) error {
	if name, _ := input["ResourceName"].(string); !elastiCacheARN.MatchString(name) {
		return Failure("InvalidARNFault", "ResourceName must be the ARN of an ElastiCache resource.")
	}
	return nil
}

func elastiCacheTagResource(input map[string]any) error {
	if err := elastiCacheResourceName(input); err != nil {
		return err
	}
	return queryTags(input, "InvalidParameterValueException", "TagQuotaPerResourceExceeded")
}

// redshift serves cluster parameter groups and cluster subnet groups and, as
// configuration records, clusters over the Query protocol. Nothing that acts
// on a running warehouse is bound.
var redshift = &Service{
	Model: "redshift", Title: "Amazon Redshift", IAMPrefix: "redshift", Issues: []int{77},
	Resources: []*Resource{
		{Kind: "clusterParameterGroup", Shape: "ClusterParameterGroup", ID: "name", ARN: "arn:{partition}:redshift:{region}:{account}:parametergroup:{name}",
			NameMember: "ParameterGroupName", Unique: true, Limit: 50},
		{Kind: "clusterSubnetGroup", Shape: "ClusterSubnetGroup", ID: "name", ARN: "arn:{partition}:redshift:{region}:{account}:subnetgroup:{name}",
			NameMember: "ClusterSubnetGroupName", Unique: true, Limit: 50},
		{Kind: "cluster", Shape: "Cluster", ID: "name", ARN: "arn:{partition}:redshift:{region}:{account}:cluster:{name}",
			NameMember: "ClusterIdentifier", Unique: true, Limit: 20,
			Generate: map[string]any{"ClusterStatus": "creating", "ClusterCreateTime": "$now", "DBName": "dev"}},
	},
	Ops: []Op{
		{Name: "CreateClusterParameterGroup", Kind: Create, Resource: "clusterParameterGroup", Check: redshiftCreateParameterGroup},
		{Name: "DescribeClusterParameterGroups", Kind: List, Resource: "clusterParameterGroup", Names: "ParameterGroupName", Check: queryDescribe("ParameterGroupName", true)},
		{Name: "DeleteClusterParameterGroup", Kind: Delete, Resource: "clusterParameterGroup", Lookup: "ParameterGroupName", Check: func(input map[string]any) error {
			lowerMember(input, "ParameterGroupName")
			return nil
		}},
		{Name: "CreateClusterSubnetGroup", Kind: Create, Resource: "clusterSubnetGroup", Check: redshiftCreateSubnetGroup},
		{Name: "DescribeClusterSubnetGroups", Kind: List, Resource: "clusterSubnetGroup", Names: "ClusterSubnetGroupName", Check: queryDescribe("ClusterSubnetGroupName", true)},
		{Name: "ModifyClusterSubnetGroup", Kind: Update, Resource: "clusterSubnetGroup", Lookup: "ClusterSubnetGroupName",
			Fields: []string{"Description", "Subnets"}, Check: redshiftModifySubnetGroup},
		{Name: "DeleteClusterSubnetGroup", Kind: Delete, Resource: "clusterSubnetGroup", Lookup: "ClusterSubnetGroupName", Check: func(input map[string]any) error {
			lowerMember(input, "ClusterSubnetGroupName")
			return nil
		}},
		{Name: "CreateCluster", Kind: Create, Resource: "cluster", Check: redshiftCreateCluster},
		{Name: "DescribeClusters", Kind: List, Resource: "cluster", Names: "ClusterIdentifier", Check: queryDescribe("ClusterIdentifier", true)},
		{Name: "ModifyCluster", Kind: Update, Resource: "cluster", Lookup: "ClusterIdentifier", Fields: redshiftClusterFields, Check: redshiftModifyCluster},
		{Name: "DeleteCluster", Kind: Delete, Resource: "cluster", Lookup: "ClusterIdentifier", Accept: []string{"SkipFinalClusterSnapshot"},
			Check: redshiftDeleteCluster, Set: map[string]any{"ClusterStatus": "deleting"}},
		{Name: "CreateTags", Kind: Tag, Lookup: "ResourceName", Check: redshiftTags},
		{Name: "DeleteTags", Kind: Untag, Lookup: "ResourceName"},
	},
	Bounds: []string{
		"Clusters are stored configuration records that stay in the creating state: no node, database, endpoint, address, port, listener, public key or network interface exists, no endpoint, address, port or node list is ever returned, and no SQL, data share, integration or warehouse exists. Cluster parameter groups and cluster subnet groups are stored configuration too. Operations that act on a running warehouse (RebootCluster, PauseCluster, ResumeCluster, ResizeCluster, FailoverPrimaryCompute, snapshots, copies and restores, data sharing, integrations, queries) and every other official operation return UnknownOperationException with HTTP 501.",
		"The simulator stores no credentials: MasterUserPassword, ManageMasterPassword and MasterPasswordSecretKmsKeyId are refused with InvalidParameterValue instead of being accepted and discarded, so a cluster is created with MasterUsername and no password (AWS would require a password or a managed secret). Port and ElasticIp are refused because no address or listener exists; cluster security groups, HSM certificates and configurations, snapshot schedules, AdditionalInfo, LoadSampleData, IAM Identity Center applications, catalogs, Availability Zone relocation, Aqua, renaming (NewClusterIdentifier) and every member the official cluster description does not hold are refused as well.",
		"A cluster keeps exactly the settings it was given: node types, versions, maintenance windows, encryption keys and similar settings are stored without checking them against Redshift offerings, and the only defaults are DBName dev and one node. ClusterStatus is creating from creation until deletion; ModifyCluster is accepted in any state and applies immediately (no pending values exist), and DeleteCluster returns the cluster with status deleting. DeleteCluster needs SkipFinalClusterSnapshot true because no snapshot can be taken, so FinalClusterSnapshotIdentifier and FinalClusterSnapshotRetentionPeriod are refused. ClusterIdentifier is 1 to 63 lowercase letters, digits or hyphens that start with a letter and neither end with a hyphen nor contain two consecutive hyphens (uppercase letters are refused, as documented), MasterUsername is required and 1 to 128 lowercase letters, digits or _+.@- characters that start with a letter and are not public, DBName is 1 to 64 lowercase letters or digits, ClusterType is stored as its node count (a single-node cluster has one node, a multi-node cluster states NumberOfNodes from 1 to 100, and ModifyCluster needs NodeType with either), AutomatedSnapshotRetentionPeriod is 0 to 35, ManualSnapshotRetentionPeriod is -1 or 1 to 3653, IpAddressType is ipv4 or dualstack, and MultiAZ is reported as Enabled or Disabled. At most 20 clusters exist per Region (a local limit that answers ClusterQuotaExceeded).",
		"A cluster reports its parameter group (status in-sync), VPC security groups (status active) and IAM roles (status in-sync) by name or ID only and names its cluster subnet group, none of which needs to exist or is protected from deletion while in use; it reports no VPC, subnets or Availability Zones of its own, and DescribeClusters refuses TagKeys and TagValues.",
		"Snapshot schedules, scheduled actions, usage limits, event subscriptions, HSM certificates and configurations, snapshot copy grants, authentication profiles and cluster security groups are not implemented: they need a cluster, a topic, a key, an IAM role or a schedule grammar the stored model cannot evaluate, so their APIs return UnknownOperationException with HTTP 501.",
		"Parameter values are not modeled: ModifyClusterParameterGroup, ResetClusterParameterGroup, DescribeClusterParameters and DescribeDefaultClusterParameters are not implemented, and default parameter groups and the default subnet group are not pre-created. A parameter group family is stored without checking that it exists. Parameter group names are 1 to 255 letters, digits or hyphens, start with a letter, contain no trailing or consecutive hyphens and are stored in lowercase.",
		"A subnet group needs 1 to 20 distinct subnet-xxxxxxxx or subnet-xxxxxxxxxxxxxxxxx IDs that are not resolved, so it reports no VPC, Availability Zone, subnet status, group status or supported IP address type; the 20-subnet limit is returned as ClusterSubnetQuotaExceededFault. Subnet group names are 1 to 255 letters, digits or hyphens, are stored in lowercase and must not be Default.",
		"Describe operations refuse TagKeys, TagValues and Marker values that were not issued for the request, accept MaxRecords from 20 to 100 and fail with the operation's not-found fault for an unknown name. Resource counts per Region (50 parameter groups, 50 subnet groups and 20 clusters) are local limits.",
		"CreateTags and DeleteTags apply to the three resource kinds by ARN (arn:aws:redshift:region:account:parametergroup:name, subnetgroup:name and cluster:id) and fail with ResourceNotFoundFault for any other ARN; tags appear on the group and cluster descriptions. Tag keys are required and must not start with aws:. Removing a missing key succeeds, and DescribeTags is not implemented.",
	},
}

func redshiftCreateParameterGroup(input map[string]any) error {
	name, _ := input["ParameterGroupName"].(string)
	if err := queryHyphenName(name); err != nil {
		return err
	}
	lowerMember(input, "ParameterGroupName")
	return queryTags(input, "InvalidTagFault", "TagLimitExceededFault")
}

func redshiftCreateSubnetGroup(input map[string]any) error {
	name, _ := input["ClusterSubnetGroupName"].(string)
	if !querySubnetGroupPattern.MatchString(name) {
		return invalid("Subnet group names are 1 to 255 letters, digits or hyphens.")
	}
	if strings.EqualFold(name, "default") {
		return invalid("The subnet group name must not be Default.")
	}
	lowerMember(input, "ClusterSubnetGroupName")
	if err := querySubnets(input, "ClusterSubnetQuotaExceededFault"); err != nil {
		return err
	}
	return queryTags(input, "InvalidTagFault", "TagLimitExceededFault")
}

func redshiftModifySubnetGroup(input map[string]any) error {
	lowerMember(input, "ClusterSubnetGroupName")
	return querySubnets(input, "ClusterSubnetQuotaExceededFault")
}

func redshiftTags(input map[string]any) error {
	return queryTags(input, "InvalidTagFault", "TagLimitExceededFault")
}

// Cache cluster, replication group and serverless cache records. They hold
// the settings a request names and nothing a running cache would report.

var (
	elastiCacheNamePattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)
	elastiCacheSnapshotTime   = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)
	elastiCacheTopicPattern   = regexp.MustCompile(`^arn:aws:sns:[a-z0-9-]+:[0-9]{12}:[A-Za-z0-9_-]{1,256}$`)
	elastiCacheClusterEngines = []string{"memcached", "redis", "valkey"}
	elastiCacheGroupEngines   = []string{"redis", "valkey"}

	elastiCacheClusterFields = []string{"NumCacheNodes", "SecurityGroups", "PreferredMaintenanceWindow", "CacheParameterGroup", "NotificationConfiguration", "Engine",
		"EngineVersion", "AutoMinorVersionUpgrade", "SnapshotRetentionLimit", "SnapshotWindow", "CacheNodeType", "IpDiscovery"}
	elastiCacheGroupFields = []string{"ReplicationGroupDescription", "AutomaticFailover", "MultiAZ", "AutoMinorVersionUpgrade", "SnapshotRetentionLimit",
		"SnapshotWindow", "CacheNodeType", "Engine", "TransitEncryptionEnabled", "TransitEncryptionMode", "ClusterMode", "IpDiscovery"}
	elastiCacheServerlessFields = []string{"Description", "CacheUsageLimits", "SecurityGroupIds", "SnapshotRetentionLimit", "DailySnapshotTime", "Engine", "MajorEngineVersion"}
)

// elastiCacheRefuse rejects members the bounded records cannot honour. An
// auth token is refused rather than accepted and dropped.
func elastiCacheRefuse(input map[string]any, unsimulated ...string) error {
	for _, member := range []string{"AuthToken", "AuthTokenUpdateStrategy"} {
		if _, ok := input[member]; ok {
			return Failure("InvalidParameterValueException", "The simulator never stores passwords, tokens or secrets: omit "+member+" and create the record without one.")
		}
	}
	if _, ok := input["Port"]; ok {
		return Failure("InvalidParameterValueException", "Port configures a network listener. No listener exists in the simulator, so no port is stored.")
	}
	for _, member := range unsimulated {
		if _, ok := input[member]; ok {
			return Failure("InvalidParameterValueException", member+" is not supported by the simulator: it needs a resource or behavior the stored record does not provide.")
		}
	}
	return nil
}

// elastiCacheIdentifier applies the documented identifier rule with the
// longest length each record kind allows and stores the name in lowercase.
func elastiCacheIdentifier(input map[string]any, member string, most int) error {
	name, _ := input[member].(string)
	if len(name) > most || !elastiCacheNamePattern.MatchString(name) || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return Failure("InvalidParameterValueException", fmt.Sprintf("%s must be 1 to %d letters, digits or hyphens, start with a letter and neither end with a hyphen nor contain two consecutive hyphens.", member, most))
	}
	lowerMember(input, member)
	return nil
}

func elastiCacheEngineIn(input map[string]any, allowed []string) error {
	if engine, ok := input["Engine"].(string); ok && !slices.Contains(allowed, engine) {
		return Failure("InvalidParameterValueException", "Engine must be one of "+strings.Join(allowed, ", ")+".")
	}
	return nil
}

// elastiCacheNodeCount applies the documented node counts: one for Redis OSS
// and Valkey clusters, 1 to 40 for Memcached.
func elastiCacheNodeCount(n float64, engine string) error {
	if engine == "memcached" && (n < 1 || n > 40) || engine != "memcached" && engine != "" && n != 1 {
		return Failure("InvalidParameterValueException", "NumCacheNodes must be 1 for redis and valkey and from 1 to 40 for memcached.")
	}
	return nil
}

func elastiCacheNodes(input map[string]any, engine string) error {
	if n, ok := Number(input["NumCacheNodes"]); ok {
		return elastiCacheNodeCount(n, engine)
	}
	return nil
}

// elastiCacheClusterNodes rechecks the node count against the stored engine
// when either changes.
func elastiCacheClusterNodes(document, supplied map[string]any) error {
	_, nodes := supplied["NumCacheNodes"]
	_, engine := supplied["Engine"]
	if n, ok := Number(document["NumCacheNodes"]); ok && (nodes || engine) {
		name, _ := document["Engine"].(string)
		return elastiCacheNodeCount(n, name)
	}
	return nil
}

func elastiCacheSecurityGroups(input map[string]any, source, target, idMember string) error {
	ids, ok := input[source].([]any)
	if !ok {
		return nil
	}
	items := make([]any, 0, len(ids))
	for _, value := range ids {
		id, _ := value.(string)
		if !rdsSecurityPattern.MatchString(id) {
			return Failure("InvalidParameterValueException", "Security group IDs look like sg-0123456789abcdef0.")
		}
		items = append(items, map[string]any{idMember: id, "Status": "active"})
	}
	delete(input, source)
	input[target] = items
	return nil
}

// elastiCacheClusterReferences stores group names as the official status
// members of a cache cluster description.
func elastiCacheClusterReferences(input map[string]any) error {
	if name, ok := input["CacheParameterGroupName"].(string); ok {
		if !rdsReferencePattern.MatchString(name) {
			return Failure("InvalidParameterValueException", "CacheParameterGroupName is not a valid group name.")
		}
		delete(input, "CacheParameterGroupName")
		input["CacheParameterGroup"] = map[string]any{"CacheParameterGroupName": strings.ToLower(name), "ParameterApplyStatus": "in-sync", "CacheNodeIdsToReboot": []any{}}
	}
	lowerMember(input, "CacheSubnetGroupName")
	if topic, ok := input["NotificationTopicArn"].(string); ok {
		if !elastiCacheTopicPattern.MatchString(topic) {
			return Failure("InvalidParameterValueException", "NotificationTopicArn must be the ARN of an SNS topic.")
		}
		delete(input, "NotificationTopicArn")
		input["NotificationConfiguration"] = map[string]any{"TopicArn": topic, "TopicStatus": "active"}
	}
	return elastiCacheSecurityGroups(input, "SecurityGroupIds", "SecurityGroups", "SecurityGroupId")
}

func elastiCacheCreateCluster(input map[string]any) error {
	if err := elastiCacheRefuse(input, "CacheSecurityGroupNames", "ReplicationGroupId", "LogDeliveryConfigurations", "PreferredOutpostArn", "NotificationTopicStatus"); err != nil {
		return err
	}
	if err := elastiCacheIdentifier(input, "CacheClusterId", 50); err != nil {
		return err
	}
	if err := elastiCacheEngineIn(input, elastiCacheClusterEngines); err != nil {
		return err
	}
	engine, _ := input["Engine"].(string)
	if err := elastiCacheNodes(input, engine); err != nil {
		return err
	}
	if err := elastiCacheClusterReferences(input); err != nil {
		return err
	}
	return queryTags(input, "InvalidParameterValueException", "TagQuotaPerResourceExceeded")
}

func elastiCacheModifyCluster(input map[string]any) error {
	if err := elastiCacheRefuse(input, "CacheSecurityGroupNames", "LogDeliveryConfigurations", "NotificationTopicStatus"); err != nil {
		return err
	}
	lowerMember(input, "CacheClusterId")
	if err := elastiCacheEngineIn(input, elastiCacheClusterEngines); err != nil {
		return err
	}
	engine, _ := input["Engine"].(string)
	if err := elastiCacheNodes(input, engine); err != nil {
		return err
	}
	return elastiCacheClusterReferences(input)
}

// elastiCacheDescribeClusters answers DescribeCacheClusters: no node exists
// to show, and a cluster is never a member of a replication group.
func elastiCacheDescribeClusters(input map[string]any) error {
	if show, ok := input["ShowCacheNodeInfo"].(bool); ok {
		if show {
			return Failure("InvalidParameterValueException", "ShowCacheNodeInfo is not supported: no cache node exists in the simulator.")
		}
		delete(input, "ShowCacheNodeInfo")
	}
	delete(input, "ShowCacheClustersNotInReplicationGroups")
	return queryDescribe("CacheClusterId", true)(input)
}

// elastiCacheEnabled stores a boolean request member as the "enabled" or
// "disabled" text of the replication group description.
func elastiCacheEnabled(input map[string]any, request, stored string) {
	if value, ok := input[request].(bool); ok {
		delete(input, request)
		text := "disabled"
		if value {
			text = "enabled"
		}
		input[stored] = text
	}
}

func elastiCacheGroupMembers(input map[string]any) {
	elastiCacheEnabled(input, "AutomaticFailoverEnabled", "AutomaticFailover")
	elastiCacheEnabled(input, "MultiAZEnabled", "MultiAZ")
	elastiCacheEnabled(input, "DataTieringEnabled", "DataTiering")
}

func elastiCacheCreateGroup(input map[string]any) error {
	if err := elastiCacheRefuse(input, "UserGroupIds", "LogDeliveryConfigurations", "GlobalReplicationGroupId", "PrimaryClusterId", "CacheSecurityGroupNames"); err != nil {
		return err
	}
	if err := elastiCacheIdentifier(input, "ReplicationGroupId", 40); err != nil {
		return err
	}
	if err := elastiCacheEngineIn(input, elastiCacheGroupEngines); err != nil {
		return err
	}
	elastiCacheGroupMembers(input)
	return queryTags(input, "InvalidParameterValueException", "TagQuotaPerResourceExceeded")
}

func elastiCacheModifyGroup(input map[string]any) error {
	if err := elastiCacheRefuse(input, "UserGroupIdsToAdd", "UserGroupIdsToRemove", "RemoveUserGroups", "LogDeliveryConfigurations", "CacheSecurityGroupNames", "NotificationTopicStatus"); err != nil {
		return err
	}
	lowerMember(input, "ReplicationGroupId")
	if err := elastiCacheEngineIn(input, elastiCacheGroupEngines); err != nil {
		return err
	}
	elastiCacheGroupMembers(input)
	return nil
}

func elastiCacheServerless(input map[string]any) error {
	for _, member := range []string{"SubnetIds", "SecurityGroupIds"} {
		ids, _ := input[member].([]any)
		for _, value := range ids {
			id, _ := value.(string)
			if member == "SubnetIds" && !rdsSubnetPattern.MatchString(id) {
				return Failure("InvalidParameterValueException", "Subnet IDs look like subnet-0123456789abcdef0.")
			}
			if member == "SecurityGroupIds" && !rdsSecurityPattern.MatchString(id) {
				return Failure("InvalidParameterValueException", "Security group IDs look like sg-0123456789abcdef0.")
			}
		}
	}
	if at, ok := input["DailySnapshotTime"].(string); ok && !elastiCacheSnapshotTime.MatchString(at) {
		return Failure("InvalidParameterValueException", "DailySnapshotTime must be a time of day such as 05:00.")
	}
	return elastiCacheEngineIn(input, elastiCacheClusterEngines)
}

func elastiCacheCreateServerless(input map[string]any) error {
	if err := elastiCacheRefuse(input, "UserGroupId", "SnapshotArnsToRestore"); err != nil {
		return err
	}
	if err := elastiCacheIdentifier(input, "ServerlessCacheName", 40); err != nil {
		return err
	}
	if err := elastiCacheServerless(input); err != nil {
		return err
	}
	return queryTags(input, "InvalidParameterValueException", "TagQuotaPerResourceExceeded")
}

func elastiCacheModifyServerless(input map[string]any) error {
	if err := elastiCacheRefuse(input, "UserGroupId", "RemoveUserGroup"); err != nil {
		return err
	}
	lowerMember(input, "ServerlessCacheName")
	return elastiCacheServerless(input)
}

// elastiCacheDeleteRecord refuses the final snapshot a delete would take: no
// snapshot can exist.
func elastiCacheDeleteRecord(member, snapshot string) func(map[string]any) error {
	return func(input map[string]any) error {
		lowerMember(input, member)
		if _, ok := input[snapshot]; ok {
			return Failure("InvalidParameterValueException", "No snapshot can be taken because the simulator has no snapshots: omit "+snapshot+".")
		}
		return nil
	}
}

// Cluster records. They hold the settings a request names and nothing a
// running warehouse would report.

var (
	redshiftClusterPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	redshiftUserPattern     = regexp.MustCompile(`^[a-z][a-z0-9_+.@-]{0,127}$`)
	redshiftDatabasePattern = regexp.MustCompile(`^[a-z0-9]{1,64}$`)
	redshiftRolePattern     = regexp.MustCompile(`^arn:aws:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]{1,512}$`)

	redshiftClusterFields = []string{"NodeType", "NumberOfNodes", "VpcSecurityGroups", "ClusterParameterGroups", "AutomatedSnapshotRetentionPeriod",
		"ManualSnapshotRetentionPeriod", "PreferredMaintenanceWindow", "ClusterVersion", "AllowVersionUpgrade", "PubliclyAccessible", "EnhancedVpcRouting",
		"MaintenanceTrackName", "Encrypted", "KmsKeyId", "AvailabilityZone", "IpAddressType", "MultiAZ"}
)

// redshiftRefuse rejects members the bounded cluster record cannot honour. A
// password or secret is refused rather than accepted and dropped.
func redshiftRefuse(input map[string]any) error {
	for _, member := range []string{"MasterUserPassword", "ManageMasterPassword", "MasterPasswordSecretKmsKeyId"} {
		if _, ok := input[member]; ok {
			return invalid("The simulator never stores passwords, tokens or secrets: omit %s and create the cluster without one.", member)
		}
	}
	for _, member := range []string{"Port", "ElasticIp"} {
		if _, ok := input[member]; ok {
			return invalid("%s configures a network address or listener. None exists in the simulator, so it is not stored.", member)
		}
	}
	for _, member := range []string{"ClusterSecurityGroups", "HsmClientCertificateIdentifier", "HsmConfigurationIdentifier", "SnapshotScheduleIdentifier", "AdditionalInfo",
		"LoadSampleData", "RedshiftIdcApplicationArn", "CatalogName", "AvailabilityZoneRelocation", "AquaConfigurationStatus", "NewClusterIdentifier"} {
		if _, ok := input[member]; ok {
			return invalid("%s is not supported by the simulator: it needs a resource or behavior the stored cluster does not provide.", member)
		}
	}
	return nil
}

// redshiftNodes stores ClusterType as the node count it implies: a
// single-node cluster has one node, a multi-node cluster states its count.
func redshiftNodes(input map[string]any, create bool) error {
	kind, hasKind := input["ClusterType"].(string)
	nodes, hasNodes := input["NumberOfNodes"].(int64)
	delete(input, "ClusterType")
	if hasNodes && (nodes < 1 || nodes > 100) {
		return invalid("NumberOfNodes must be from 1 to 100.")
	}
	switch {
	case hasKind && kind == "single-node":
		if hasNodes && nodes != 1 {
			return invalid("A single-node cluster has one node.")
		}
		input["NumberOfNodes"] = int64(1)
	case hasKind && kind == "multi-node":
		if !hasNodes {
			return invalid("NumberOfNodes is required for a multi-node cluster.")
		}
	case hasKind:
		return invalid("ClusterType must be multi-node or single-node.")
	case create && !hasNodes:
		input["NumberOfNodes"] = int64(1)
	}
	return nil
}

// redshiftReferences stores group, role and security group names as the
// official membership members of the cluster description.
func redshiftReferences(input map[string]any) error {
	lowerMember(input, "ClusterSubnetGroupName")
	if name, ok := input["ClusterParameterGroupName"].(string); ok {
		if !rdsReferencePattern.MatchString(name) {
			return invalid("ClusterParameterGroupName is not a valid group name.")
		}
		delete(input, "ClusterParameterGroupName")
		input["ClusterParameterGroups"] = []any{map[string]any{"ParameterGroupName": strings.ToLower(name), "ParameterApplyStatus": "in-sync"}}
	}
	if ids, ok := input["VpcSecurityGroupIds"].([]any); ok {
		memberships := make([]any, 0, len(ids))
		for _, value := range ids {
			id, _ := value.(string)
			if !rdsSecurityPattern.MatchString(id) {
				return invalid("Security group IDs look like sg-0123456789abcdef0.")
			}
			memberships = append(memberships, map[string]any{"VpcSecurityGroupId": id, "Status": "active"})
		}
		delete(input, "VpcSecurityGroupIds")
		input["VpcSecurityGroups"] = memberships
	}
	if roles, ok := input["IamRoles"].([]any); ok {
		if len(roles) > 10 {
			return invalid("A cluster has at most 10 IAM roles.")
		}
		items := make([]any, 0, len(roles))
		for _, value := range roles {
			arn, _ := value.(string)
			if !redshiftRolePattern.MatchString(arn) {
				return invalid("IamRoles must be IAM role ARNs.")
			}
			items = append(items, map[string]any{"IamRoleArn": arn, "ApplyStatus": "in-sync"})
		}
		input["IamRoles"] = items
	}
	if multi, ok := input["MultiAZ"].(bool); ok {
		input["MultiAZ"] = "Disabled"
		if multi {
			input["MultiAZ"] = "Enabled"
		}
	}
	return nil
}

// redshiftSettings applies the documented ranges of settings a cluster holds.
func redshiftSettings(input map[string]any) error {
	if n, ok := input["AutomatedSnapshotRetentionPeriod"].(int64); ok && (n < 0 || n > 35) {
		return invalid("AutomatedSnapshotRetentionPeriod must be from 0 to 35.")
	}
	if n, ok := input["ManualSnapshotRetentionPeriod"].(int64); ok && n != -1 && (n < 1 || n > 3653) {
		return invalid("ManualSnapshotRetentionPeriod must be -1 or from 1 to 3653.")
	}
	if kind, ok := input["IpAddressType"].(string); ok && kind != "ipv4" && kind != "dualstack" {
		return invalid("IpAddressType must be ipv4 or dualstack.")
	}
	return nil
}

func redshiftCreateCluster(input map[string]any) error {
	if err := redshiftRefuse(input); err != nil {
		return err
	}
	name, _ := input["ClusterIdentifier"].(string)
	if !redshiftClusterPattern.MatchString(name) || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return invalid("ClusterIdentifier must be 1 to 63 lowercase letters, digits or hyphens, start with a letter and neither end with a hyphen nor contain two consecutive hyphens.")
	}
	user, _ := input["MasterUsername"].(string)
	if !redshiftUserPattern.MatchString(user) || user == "public" {
		return invalid("MasterUsername must be 1 to 128 lowercase letters, digits or _+.@- characters, start with a letter and not be public.")
	}
	if database, ok := input["DBName"].(string); ok && !redshiftDatabasePattern.MatchString(database) {
		return invalid("DBName must be 1 to 64 lowercase letters or digits.")
	}
	if err := redshiftNodes(input, true); err != nil {
		return err
	}
	if err := redshiftSettings(input); err != nil {
		return err
	}
	if err := redshiftReferences(input); err != nil {
		return err
	}
	return queryTags(input, "InvalidTagFault", "TagLimitExceededFault")
}

func redshiftModifyCluster(input map[string]any) error {
	if err := redshiftRefuse(input); err != nil {
		return err
	}
	lowerMember(input, "ClusterIdentifier")
	_, kind := input["ClusterType"]
	_, nodes := input["NumberOfNodes"]
	if _, node := input["NodeType"]; (kind || nodes) && !node {
		return invalid("NodeType is required together with a new ClusterType or NumberOfNodes.")
	}
	if err := redshiftNodes(input, false); err != nil {
		return err
	}
	if err := redshiftSettings(input); err != nil {
		return err
	}
	return redshiftReferences(input)
}

// redshiftDeleteCluster refuses the final snapshot a delete would otherwise
// take: no snapshot can exist, so the caller must skip it explicitly.
func redshiftDeleteCluster(input map[string]any) error {
	lowerMember(input, "ClusterIdentifier")
	for _, member := range []string{"FinalClusterSnapshotIdentifier", "FinalClusterSnapshotRetentionPeriod"} {
		if _, ok := input[member]; ok {
			return invalid("No snapshot can be taken because the simulator has no snapshots: omit %s and set SkipFinalClusterSnapshot to true.", member)
		}
	}
	if skip, _ := input["SkipFinalClusterSnapshot"].(bool); !skip {
		return invalid("SkipFinalClusterSnapshot must be true: the simulator cannot take a final snapshot.")
	}
	return nil
}
