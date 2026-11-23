package catalog

import (
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

// elastiCache serves cache parameter groups, cache subnet groups and
// IAM-authenticated users over the Query protocol. No cache is bound.
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
		{Name: "AddTagsToResource", Kind: Tag, Lookup: "ResourceName", Check: elastiCacheTagResource},
		{Name: "RemoveTagsFromResource", Kind: Untag, Lookup: "ResourceName", Check: elastiCacheResourceName},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceName", Check: elastiCacheResourceName},
	},
	Bounds: []string{
		"Cache parameter groups, cache subnet groups and IAM-authenticated users are stored configuration served over the Query protocol: no cache cluster, replication group, serverless cache, global datastore, snapshot, security group or reserved node exists, and CreateCacheCluster, CreateReplicationGroup, CreateServerlessCache, CreateSnapshot and every other official operation return UnknownOperationException with HTTP 501.",
		"User groups are not implemented: a group references users, requires the account's default user and must drop a deleted user, none of which the stored model expresses, so CreateUserGroup, ModifyUserGroup, DescribeUserGroups and DeleteUserGroup return UnknownOperationException with HTTP 501 and users always report no UserGroupIds.",
		"Users must use iam authentication and the engine valkey or redis: passwords and NoPasswordRequired are refused so no password is stored, the user name must equal the user ID (the ElastiCache rule for IAM-enabled users), and the default user is refused and not pre-created. Access strings are stored without parsing, and AppendAccessString is not implemented. MinimumEngineVersion is 6.0 for redis and 7.2 for valkey as documented; DeleteUser returns the user with status deleting.",
		"Parameter values are not modeled: ModifyCacheParameterGroup, ResetCacheParameterGroup, DescribeCacheParameters, DescribeEngineDefaultParameters and DescribeCacheEngineVersions are not implemented, and default parameter groups and the default cache subnet group are not pre-created. A parameter group family must be one of the documented values and is not checked against engine versions; names are stored as given and IsGlobal is always false.",
		"A cache subnet group needs 1 to 20 distinct subnet-xxxxxxxx or subnet-xxxxxxxxxxxxxxxxx IDs that are not resolved, so it reports no VPC, Availability Zone, Outpost or supported network type, and the CacheSubnetQuotaExceededFault limit of 20 is a local limit. Cache subnet group names are 1 to 255 letters, digits or hyphens and are stored in lowercase.",
		"Describe operations refuse Marker values that were not issued for the request, accept MaxRecords from 20 to 100 for parameter and subnet groups and fail with the operation's not-found fault for an unknown name; DescribeUsers refuses Filters and a MaxRecords below 1. Resource counts per Region (50 parameter groups, 50 subnet groups, 100 users) are local limits.",
		"Tags apply to the three resource kinds by ARN (arn:aws:elasticache:region:account:parametergroup:name, subnetgroup:name and user:id). Tag keys are required and must not start with aws:. AddTagsToResource and RemoveTagsFromResource return an empty TagList, removing a missing key succeeds instead of failing with TagNotFound, and a well-formed ARN of an unknown resource fails with CacheClusterNotFound, the first not-found fault the official tag operations model; a malformed ARN fails with InvalidARN. More than 50 tags in one request fail with TagQuotaPerResourceExceeded, but adding tags that bring the total beyond 50 fails with InvalidParameterValue.",
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

// redshift serves cluster parameter groups and cluster subnet groups over
// the Query protocol. No cluster is bound.
var redshift = &Service{
	Model: "redshift", Title: "Amazon Redshift", IAMPrefix: "redshift", Issues: []int{77},
	Resources: []*Resource{
		{Kind: "clusterParameterGroup", Shape: "ClusterParameterGroup", ID: "name", ARN: "arn:{partition}:redshift:{region}:{account}:parametergroup:{name}",
			NameMember: "ParameterGroupName", Unique: true, Limit: 50},
		{Kind: "clusterSubnetGroup", Shape: "ClusterSubnetGroup", ID: "name", ARN: "arn:{partition}:redshift:{region}:{account}:subnetgroup:{name}",
			NameMember: "ClusterSubnetGroupName", Unique: true, Limit: 50},
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
		{Name: "CreateTags", Kind: Tag, Lookup: "ResourceName", Check: redshiftTags},
		{Name: "DeleteTags", Kind: Untag, Lookup: "ResourceName"},
	},
	Bounds: []string{
		"Cluster parameter groups and cluster subnet groups are stored configuration served over the Query protocol: no cluster, snapshot, node, endpoint, data share, integration or warehouse exists, and CreateCluster, CreateClusterSnapshot, RestoreFromClusterSnapshot and every other official operation return UnknownOperationException with HTTP 501.",
		"Snapshot schedules, scheduled actions, usage limits, event subscriptions, HSM certificates and configurations, snapshot copy grants, authentication profiles and cluster security groups are not implemented: they need a cluster, a topic, a key, an IAM role or a schedule grammar the stored model cannot evaluate, so their APIs return UnknownOperationException with HTTP 501.",
		"Parameter values are not modeled: ModifyClusterParameterGroup, ResetClusterParameterGroup, DescribeClusterParameters and DescribeDefaultClusterParameters are not implemented, and default parameter groups and the default subnet group are not pre-created. A parameter group family is stored without checking that it exists. Parameter group names are 1 to 255 letters, digits or hyphens, start with a letter, contain no trailing or consecutive hyphens and are stored in lowercase.",
		"A subnet group needs 1 to 20 distinct subnet-xxxxxxxx or subnet-xxxxxxxxxxxxxxxxx IDs that are not resolved, so it reports no VPC, Availability Zone, subnet status, group status or supported IP address type; the 20-subnet limit is returned as ClusterSubnetQuotaExceededFault. Subnet group names are 1 to 255 letters, digits or hyphens, are stored in lowercase and must not be Default.",
		"Describe operations refuse TagKeys, TagValues and Marker values that were not issued for the request, accept MaxRecords from 20 to 100 and fail with the operation's not-found fault for an unknown name. Resource counts per Region (50 parameter groups and 50 subnet groups) are local limits.",
		"CreateTags and DeleteTags apply to the two resource kinds by ARN (arn:aws:redshift:region:account:parametergroup:name and subnetgroup:name) and fail with ResourceNotFoundFault for any other ARN; tags appear on the group descriptions. Tag keys are required and must not start with aws:. Removing a missing key succeeds, and DescribeTags is not implemented.",
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
