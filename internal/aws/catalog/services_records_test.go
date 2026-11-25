package catalog

import (
	"fmt"
	"strings"
	"testing"
)

// Tests of the configuration records for DB instances and clusters, cache
// clusters, replication groups, serverless caches and Redshift clusters:
// each record holds the settings it was given, shows no endpoint or
// address, refuses passwords and tokens instead of dropping them, and the
// operations that would act on a running thing stay unbound.

// connectionNodes are response elements that would carry an address, port,
// hostname or node list of a running database, cache or warehouse.
var connectionNodes = []string{"Endpoint", "ListenerEndpoint", "ReaderEndpoint", "ConfigurationEndpoint", "Address", "Port", "DbInstancePort",
	"HostedZoneId", "CacheNodes", "NodeGroups", "ClusterNodes", "ClusterPublicKey", "CustomEndpoints", "MasterUserSecret", "ElasticIpStatus", "ClientDownloadLandingPage"}

func hasNode(n *xnode, names []string) string {
	for _, name := range names {
		if n.name == name {
			return name
		}
	}
	for _, kid := range n.kids {
		if found := hasNode(kid, names); found != "" {
			return found
		}
	}
	return ""
}

func noConnection(t *testing.T, r queryReply) {
	t.Helper()
	if found := hasNode(r.root, connectionNodes); found != "" {
		t.Fatalf("response carries %s: %s", found, r.raw)
	}
}

// expectRefusal checks a fault whose message names the refused member.
func expectRefusal(t *testing.T, r queryReply, status int, code, mention string) {
	t.Helper()
	expectFault(t, r, status, code)
	if message := r.root.get("Error", "Message").text; !strings.Contains(message, mention) {
		t.Fatalf("message %q does not mention %q", message, mention)
	}
}

func nodeText(n *xnode, path ...string) string {
	if found := n.get(path...); found != nil {
		return found.text
	}
	return ""
}

func TestRDSInstanceRecordLifecycle(t *testing.T) {
	h := newHarness(t, rds)
	create := func(extra ...string) queryReply {
		return h.query("CreateDBInstance", append([]string{"DBInstanceIdentifier", "App-DB", "DBInstanceClass", "db.t3.micro", "Engine", "mysql",
			"MasterUsername", "dbadmin", "AllocatedStorage", "20", "DBName", "appdata", "BackupRetentionPeriod", "7", "EnableIAMDatabaseAuthentication", "true",
			"DBParameterGroupName", "Default.Mysql8.0", "DBSubnetGroupName", "App-Subnets", "OptionGroupName", "default:mysql-8-0",
			"VpcSecurityGroupIds.VpcSecurityGroupId.1", "sg-0a1b2c3d", "EnableCloudwatchLogsExports.member.1", "error",
			"Tags.Tag.1.Key", "team", "Tags.Tag.1.Value", "a"}, extra...)...)
	}
	created := create()
	noConnection(t, created)
	instance := created.result(t, "CreateDBInstance").get("DBInstance")
	for path, want := range map[string]string{
		"DBInstanceIdentifier": "app-db", "DBInstanceStatus": "creating", "Engine": "mysql", "MasterUsername": "dbadmin", "AllocatedStorage": "20", "DBName": "appdata",
		"BackupRetentionPeriod": "7", "IAMDatabaseAuthenticationEnabled": "true", "DBInstanceArn": "arn:aws:rds:us-east-1:000000000000:db:app-db",
	} {
		if got := nodeText(instance, path); got != want {
			t.Fatalf("%s = %q, want %q: %s", path, got, want, created.raw)
		}
	}
	if instance.get("InstanceCreateTime") == nil || nodeText(instance, "DBParameterGroups", "DBParameterGroup", "DBParameterGroupName") != "default.mysql8.0" ||
		nodeText(instance, "DBParameterGroups", "DBParameterGroup", "ParameterApplyStatus") != "in-sync" ||
		nodeText(instance, "DBSubnetGroup", "DBSubnetGroupName") != "app-subnets" || nodeText(instance, "OptionGroupMemberships", "OptionGroupMembership", "Status") != "in-sync" ||
		nodeText(instance, "VpcSecurityGroups", "VpcSecurityGroupMembership", "VpcSecurityGroupId") != "sg-0a1b2c3d" ||
		nodeText(instance, "EnabledCloudwatchLogsExports", "member") != "error" || nodeText(instance, "TagList", "Tag", "Key") != "team" {
		t.Fatalf("stored settings %s", created.raw)
	}
	arn := nodeText(instance, "DBInstanceArn")
	expectFault(t, create(), 400, "DBInstanceAlreadyExists")

	described := h.query("DescribeDBInstances", "DBInstanceIdentifier", "APP-DB")
	noConnection(t, described)
	if items := described.result(t, "DescribeDBInstances").get("DBInstances").all("DBInstance"); len(items) != 1 || nodeText(items[0], "DBInstanceStatus") != "creating" {
		t.Fatalf("describe %s", described.raw)
	}
	expectFault(t, h.query("DescribeDBInstances", "DBInstanceIdentifier", "missing"), 404, "DBInstanceNotFound")
	expectFault(t, h.query("DescribeDBInstances", "MaxRecords", "19"), 400, "InvalidParameterValue")
	expectFault(t, h.query("DescribeDBInstances", "Filters.Filter.1.Name", "engine", "Filters.Filter.1.Values.Value.1", "mysql"), 400, "InvalidParameterValue")

	modified := h.query("ModifyDBInstance", "DBInstanceIdentifier", "app-db", "ApplyImmediately", "true", "AllocatedStorage", "50", "MultiAZ", "true",
		"DBParameterGroupName", "other", "VpcSecurityGroupIds.VpcSecurityGroupId.1", "sg-0123456789abcdef0", "EnablePerformanceInsights", "true", "DeletionProtection", "false")
	noConnection(t, modified)
	changed := modified.result(t, "ModifyDBInstance").get("DBInstance")
	if nodeText(changed, "AllocatedStorage") != "50" || nodeText(changed, "MultiAZ") != "true" || nodeText(changed, "PerformanceInsightsEnabled") != "true" ||
		nodeText(changed, "DBParameterGroups", "DBParameterGroup", "DBParameterGroupName") != "other" ||
		nodeText(changed, "VpcSecurityGroups", "VpcSecurityGroupMembership", "VpcSecurityGroupId") != "sg-0123456789abcdef0" ||
		nodeText(changed, "DBInstanceStatus") != "creating" || nodeText(changed, "Engine") != "mysql" {
		t.Fatalf("modify %s", modified.raw)
	}
	expectFault(t, h.query("ModifyDBInstance", "DBInstanceIdentifier", "missing", "AllocatedStorage", "5"), 404, "DBInstanceNotFound")
	expectFault(t, h.query("ModifyDBInstance", "DBInstanceIdentifier", "app-db", "NewDBInstanceIdentifier", "renamed"), 400, "InvalidParameterValue")
	expectFault(t, h.query("ModifyDBInstance", "DBInstanceIdentifier", "app-db", "Engine", "postgres"), 400, "InvalidParameterValue")
	expectFault(t, h.query("ModifyDBInstance", "DBInstanceIdentifier", "app-db", "DeletionProtection", "true"), 400, "InvalidParameterValue")
	expectFault(t, h.query("ModifyDBInstance", "DBInstanceIdentifier", "app-db", "BackupRetentionPeriod", "36"), 400, "InvalidParameterValue")

	// Tags work by ARN on the record.
	h.query("AddTagsToResource", "ResourceName", arn, "Tags.Tag.1.Key", "stage", "Tags.Tag.1.Value", "dev").result(t, "AddTagsToResource")
	h.query("RemoveTagsFromResource", "ResourceName", arn, "TagKeys.member.1", "team").result(t, "RemoveTagsFromResource")
	tags := h.query("ListTagsForResource", "ResourceName", arn).result(t, "ListTagsForResource").get("TagList").all("Tag")
	if len(tags) != 1 || nodeText(tags[0], "Key") != "stage" {
		t.Fatalf("tags %v", tags)
	}

	// A delete cannot take the final snapshot that does not exist.
	expectFault(t, h.query("DeleteDBInstance", "DBInstanceIdentifier", "app-db", "SkipFinalSnapshot", "false"), 400, "InvalidParameterValue")
	expectRefusal(t, h.query("DeleteDBInstance", "DBInstanceIdentifier", "app-db", "FinalDBSnapshotIdentifier", "final"), 400, "InvalidParameterValue", "snapshot")
	deleted := h.query("DeleteDBInstance", "DBInstanceIdentifier", "app-db", "SkipFinalSnapshot", "true", "DeleteAutomatedBackups", "true")
	noConnection(t, deleted)
	if status := nodeText(deleted.result(t, "DeleteDBInstance").get("DBInstance"), "DBInstanceStatus"); status != "deleting" {
		t.Fatalf("delete %s", deleted.raw)
	}
	expectFault(t, h.query("DescribeDBInstances", "DBInstanceIdentifier", "app-db"), 404, "DBInstanceNotFound")
	expectFault(t, h.query("DeleteDBInstance", "DBInstanceIdentifier", "app-db", "SkipFinalSnapshot", "true"), 404, "DBInstanceNotFound")
}

func TestRDSInstanceRefusesWhatItCannotStoreHonestly(t *testing.T) {
	h := newHarness(t, rds)
	create := func(extra ...string) queryReply {
		return h.query("CreateDBInstance", append([]string{"DBInstanceIdentifier", "db1", "DBInstanceClass", "db.t3.micro", "Engine", "mysql"}, extra...)...)
	}
	for member, value := range map[string]string{"MasterUserPassword": "x", "TdeCredentialPassword": "x", "TdeCredentialArn": "x", "ManageMasterUserPassword": "true",
		"MasterUserSecretKmsKeyId": "x", "DomainAuthSecretArn": "x"} {
		expectRefusal(t, create(member, value), 400, "InvalidParameterValue", "never stores")
	}
	expectRefusal(t, h.query("ModifyDBInstance", "DBInstanceIdentifier", "db1", "RotateMasterUserPassword", "true"), 400, "InvalidParameterValue", "never stores")
	expectRefusal(t, h.query("ModifyDBInstance", "DBInstanceIdentifier", "db1", "DBPortNumber", "3306"), 400, "InvalidParameterValue", "listener")
	expectRefusal(t, create("Port", "3306"), 400, "InvalidParameterValue", "listener")
	expectFault(t, create("Domain", "d-1"), 400, "InvalidParameterValue")
	expectFault(t, create("DBSecurityGroups.DBSecurityGroupName.1", "classic"), 400, "InvalidParameterValue")
	expectFault(t, create("DeletionProtection", "true"), 400, "InvalidParameterValue")
	expectFault(t, create("Endpoint.Address", "db.example"), 400, "InvalidParameterValue")
	expectFault(t, create("MonitoringInterval", "7"), 400, "InvalidParameterValue")
	expectFault(t, create("PromotionTier", "16"), 400, "InvalidParameterValue")
	expectFault(t, create("VpcSecurityGroupIds.VpcSecurityGroupId.1", "sg-1"), 400, "InvalidParameterValue")
	expectFault(t, create("DBParameterGroupName", "1bad"), 400, "InvalidParameterValue")
	expectFault(t, create("Tags.Tag.1.Key", "aws:x", "Tags.Tag.1.Value", "v"), 400, "InvalidParameterValue")
	expectFault(t, h.query("CreateDBInstance", "DBInstanceIdentifier", "db1", "DBInstanceClass", "db.t3.micro", "Engine", "mongo"), 400, "InvalidParameterValue")
	for _, name := range []string{"1db", "db-", "d--b", strings.Repeat("a", 64), "db_1"} {
		expectFault(t, h.query("CreateDBInstance", "DBInstanceIdentifier", name, "DBInstanceClass", "db.t3.micro", "Engine", "mysql"), 400, "InvalidParameterValue")
	}
	expectRefusal(t, h.query("ModifyDBInstance", "DBInstanceIdentifier", "db1", "MasterUserPassword", "value-1"), 400, "InvalidParameterValue", "never stores")
	create().result(t, "CreateDBInstance")
	noPassword := h.query("DescribeDBInstances", "DBInstanceIdentifier", "db1")
	if strings.Contains(noPassword.raw, "assword") {
		t.Fatalf("a password member is reported: %s", noPassword.raw)
	}
	expectRefusal(t, h.query("ModifyDBInstance", "DBInstanceIdentifier", "db1", "MasterUserPassword", "value-1"), 400, "InvalidParameterValue", "never stores")
	// The record limit is local.
	for i := 0; i < 39; i++ {
		h.query("CreateDBInstance", "DBInstanceIdentifier", fmt.Sprintf("many-%d", i), "DBInstanceClass", "db.t3.micro", "Engine", "mysql").result(t, "CreateDBInstance")
	}
	expectFault(t, h.query("CreateDBInstance", "DBInstanceIdentifier", "over", "DBInstanceClass", "db.t3.micro", "Engine", "mysql"), 400, "InstanceQuotaExceeded")
	h.policy.deny["rds:CreateDBInstance"] = true
	expectFault(t, create(), 403, "AccessDenied")
}

func TestRDSClusterRecordLifecycle(t *testing.T) {
	h := newHarness(t, rds)
	create := func(extra ...string) queryReply {
		return h.query("CreateDBCluster", append([]string{"DBClusterIdentifier", "Aurora-One", "Engine", "aurora-mysql", "MasterUsername", "dbadmin", "DatabaseName", "appdata",
			"DBClusterParameterGroupName", "App-Cluster-Params", "DBSubnetGroupName", "App-Subnets", "EnableIAMDatabaseAuthentication", "true", "EnableHttpEndpoint", "true",
			"VpcSecurityGroupIds.VpcSecurityGroupId.1", "sg-0a1b2c3d", "ServerlessV2ScalingConfiguration.MinCapacity", "0.5", "ServerlessV2ScalingConfiguration.MaxCapacity", "4",
			"AvailabilityZones.AvailabilityZone.1", "us-east-1a", "EnableCloudwatchLogsExports.member.1", "audit", "Tags.Tag.1.Key", "team", "Tags.Tag.1.Value", "a"}, extra...)...)
	}
	created := create()
	noConnection(t, created)
	cluster := created.result(t, "CreateDBCluster").get("DBCluster")
	for path, want := range map[string]string{
		"DBClusterIdentifier": "aurora-one", "Status": "creating", "Engine": "aurora-mysql", "MasterUsername": "dbadmin", "DatabaseName": "appdata",
		"DBClusterParameterGroup": "app-cluster-params", "DBSubnetGroup": "app-subnets", "IAMDatabaseAuthenticationEnabled": "true", "HttpEndpointEnabled": "true",
		"DBClusterArn": "arn:aws:rds:us-east-1:000000000000:cluster:aurora-one",
	} {
		if got := nodeText(cluster, path); got != want {
			t.Fatalf("%s = %q, want %q: %s", path, got, want, created.raw)
		}
	}
	if cluster.get("ClusterCreateTime") == nil || nodeText(cluster, "VpcSecurityGroups", "VpcSecurityGroupMembership", "Status") != "active" ||
		nodeText(cluster, "ServerlessV2ScalingConfiguration", "MaxCapacity") != "4" || nodeText(cluster, "EnabledCloudwatchLogsExports", "member") != "audit" ||
		nodeText(cluster, "AvailabilityZones", "AvailabilityZone") != "us-east-1a" || nodeText(cluster, "TagList", "Tag", "Value") != "a" {
		t.Fatalf("stored settings %s", created.raw)
	}
	arn := nodeText(cluster, "DBClusterArn")
	expectFault(t, create(), 400, "DBClusterAlreadyExistsFault")

	described := h.query("DescribeDBClusters", "DBClusterIdentifier", "AURORA-ONE")
	noConnection(t, described)
	if items := described.result(t, "DescribeDBClusters").get("DBClusters").all("DBCluster"); len(items) != 1 {
		t.Fatalf("describe %s", described.raw)
	}
	expectFault(t, h.query("DescribeDBClusters", "DBClusterIdentifier", "missing"), 404, "DBClusterNotFoundFault")

	modified := h.query("ModifyDBCluster", "DBClusterIdentifier", "aurora-one", "ApplyImmediately", "true", "BackupRetentionPeriod", "5", "DeletionProtection", "false",
		"OptionGroupName", "default:aurora", "EngineVersion", "8.0.mysql_aurora.3.05.2", "AllowMajorVersionUpgrade", "true")
	noConnection(t, modified)
	changed := modified.result(t, "ModifyDBCluster").get("DBCluster")
	if nodeText(changed, "BackupRetentionPeriod") != "5" || nodeText(changed, "EngineVersion") != "8.0.mysql_aurora.3.05.2" ||
		nodeText(changed, "DBClusterOptionGroupMemberships", "DBClusterOptionGroup", "DBClusterOptionGroupName") != "default:aurora" || nodeText(changed, "Status") != "creating" {
		t.Fatalf("modify %s", modified.raw)
	}
	expectFault(t, h.query("ModifyDBCluster", "DBClusterIdentifier", "missing", "BackupRetentionPeriod", "5"), 404, "DBClusterNotFoundFault")
	expectFault(t, h.query("ModifyDBCluster", "DBClusterIdentifier", "aurora-one", "NewDBClusterIdentifier", "renamed"), 400, "InvalidParameterValue")
	expectFault(t, h.query("ModifyDBCluster", "DBClusterIdentifier", "aurora-one", "EngineMode", "serverless"), 400, "InvalidParameterValue")
	expectRefusal(t, h.query("ModifyDBCluster", "DBClusterIdentifier", "aurora-one", "MasterUserPassword", "value-1"), 400, "InvalidParameterValue", "never stores")

	h.query("AddTagsToResource", "ResourceName", arn, "Tags.Tag.1.Key", "stage", "Tags.Tag.1.Value", "dev").result(t, "AddTagsToResource")
	if tags := h.query("ListTagsForResource", "ResourceName", arn).result(t, "ListTagsForResource").get("TagList").all("Tag"); len(tags) != 2 {
		t.Fatalf("tags %v", tags)
	}

	expectFault(t, h.query("DeleteDBCluster", "DBClusterIdentifier", "aurora-one"), 400, "InvalidParameterValue")
	expectRefusal(t, h.query("DeleteDBCluster", "DBClusterIdentifier", "aurora-one", "FinalDBSnapshotIdentifier", "final"), 400, "InvalidParameterValue", "snapshot")
	deleted := h.query("DeleteDBCluster", "DBClusterIdentifier", "aurora-one", "SkipFinalSnapshot", "true")
	if status := nodeText(deleted.result(t, "DeleteDBCluster").get("DBCluster"), "Status"); status != "deleting" {
		t.Fatalf("delete %s", deleted.raw)
	}
	expectFault(t, h.query("DescribeDBClusters", "DBClusterIdentifier", "aurora-one"), 404, "DBClusterNotFoundFault")
}

func TestRDSClusterRefusesWhatItCannotStoreHonestly(t *testing.T) {
	h := newHarness(t, rds)
	create := func(extra ...string) queryReply {
		return h.query("CreateDBCluster", append([]string{"DBClusterIdentifier", "c1", "Engine", "neptune"}, extra...)...)
	}
	for member, value := range map[string]string{"MasterUserPassword": "x", "ManageMasterUserPassword": "true", "MasterUserSecretKmsKeyId": "x", "PreSignedUrl": "x"} {
		expectRefusal(t, create(member, value), 400, "InvalidParameterValue", "never stores")
	}
	expectRefusal(t, create("Port", "3306"), 400, "InvalidParameterValue", "listener")
	for _, member := range []string{"ReplicationSourceIdentifier", "GlobalClusterIdentifier", "EnableGlobalWriteForwarding", "Domain"} {
		expectFault(t, create(member, "x"), 400, "InvalidParameterValue")
	}
	expectFault(t, create("EngineMode", "bogus"), 400, "InvalidParameterValue")
	expectFault(t, create("DeletionProtection", "true"), 400, "InvalidParameterValue")
	expectFault(t, create("OptionGroupName", "9"), 400, "InvalidParameterValue")
	expectFault(t, create("DBClusterParameterGroupName", "9"), 400, "InvalidParameterValue")
	expectFault(t, h.query("CreateDBCluster", "DBClusterIdentifier", "c1", "Engine", "mariadb"), 400, "InvalidParameterValue")
	expectFault(t, h.query("CreateDBCluster", "DBClusterIdentifier", "-c1", "Engine", "neptune"), 400, "InvalidParameterValue")
	create().result(t, "CreateDBCluster")
	for i := 0; i < 39; i++ {
		h.query("CreateDBCluster", "DBClusterIdentifier", fmt.Sprintf("many-%d", i), "Engine", "docdb").result(t, "CreateDBCluster")
	}
	expectFault(t, h.query("CreateDBCluster", "DBClusterIdentifier", "over", "Engine", "docdb"), 403, "DBClusterQuotaExceededFault")
	h.policy.deny["rds:DeleteDBCluster"] = true
	expectFault(t, h.query("DeleteDBCluster", "DBClusterIdentifier", "c1", "SkipFinalSnapshot", "true"), 403, "AccessDenied")
}

func TestRDSRecordsLeaveRunningDatabaseOperationsUnbound(t *testing.T) {
	h := newHarness(t, rds)
	for _, action := range []string{"RebootDBInstance", "StartDBInstance", "StopDBInstance", "FailoverDBCluster", "RebootDBCluster", "StartDBCluster", "StopDBCluster",
		"CreateDBSnapshot", "CreateDBClusterSnapshot", "RestoreDBInstanceFromDBSnapshot", "RestoreDBClusterFromSnapshot", "RestoreDBInstanceToPointInTime",
		"CreateDBInstanceReadReplica", "PromoteReadReplica", "BacktrackDBCluster", "SwitchoverReadReplica", "SwitchoverGlobalCluster", "CreateGlobalCluster",
		"CreateDBProxy", "ModifyDBParameterGroup", "DescribeDBEngineVersions", "DescribeOrderableDBInstanceOptions", "DescribeDBLogFiles"} {
		expectFault(t, h.query(action, "DBInstanceIdentifier", "db1"), 501, "UnknownOperationException")
	}
}

func TestElastiCacheClusterRecordLifecycle(t *testing.T) {
	h := newHarness(t, elastiCache)
	create := func(extra ...string) queryReply {
		return h.cache("CreateCacheCluster", append([]string{"CacheClusterId", "App-Cache", "Engine", "redis", "EngineVersion", "7.1", "CacheNodeType", "cache.t3.micro",
			"NumCacheNodes", "1", "CacheParameterGroupName", "App-Params", "CacheSubnetGroupName", "App-Subnets", "SecurityGroupIds.SecurityGroupId.1", "sg-0a1b2c3d",
			"NotificationTopicArn", "arn:aws:sns:us-east-1:000000000000:alerts", "SnapshotRetentionLimit", "3", "TransitEncryptionEnabled", "true",
			"Tags.Tag.1.Key", "team", "Tags.Tag.1.Value", "a"}, extra...)...)
	}
	created := create()
	noConnection(t, created)
	cluster := created.result(t, "CreateCacheCluster").get("CacheCluster")
	for path, want := range map[string]string{
		"CacheClusterId": "app-cache", "CacheClusterStatus": "creating", "Engine": "redis", "NumCacheNodes": "1", "CacheSubnetGroupName": "app-subnets",
		"ARN": "arn:aws:elasticache:us-east-1:000000000000:cluster:app-cache", "TransitEncryptionEnabled": "true",
	} {
		if got := nodeText(cluster, path); got != want {
			t.Fatalf("%s = %q, want %q: %s", path, got, want, created.raw)
		}
	}
	if cluster.get("CacheClusterCreateTime") == nil || nodeText(cluster, "CacheParameterGroup", "CacheParameterGroupName") != "app-params" ||
		nodeText(cluster, "CacheParameterGroup", "ParameterApplyStatus") != "in-sync" ||
		nodeText(cluster, "SecurityGroups", "member", "SecurityGroupId") != "sg-0a1b2c3d" ||
		nodeText(cluster, "NotificationConfiguration", "TopicStatus") != "active" {
		t.Fatalf("stored settings %s", created.raw)
	}
	arn := nodeText(cluster, "ARN")
	expectFault(t, create(), 400, "CacheClusterAlreadyExists")

	described := h.cache("DescribeCacheClusters", "CacheClusterId", "APP-CACHE", "ShowCacheNodeInfo", "false", "ShowCacheClustersNotInReplicationGroups", "true")
	noConnection(t, described)
	if items := described.result(t, "DescribeCacheClusters").get("CacheClusters").all("CacheCluster"); len(items) != 1 {
		t.Fatalf("describe %s", described.raw)
	}
	expectFault(t, h.cache("DescribeCacheClusters", "CacheClusterId", "missing"), 404, "CacheClusterNotFound")
	expectFault(t, h.cache("DescribeCacheClusters", "ShowCacheNodeInfo", "true"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("DescribeCacheClusters", "MaxRecords", "5"), 400, "InvalidParameterValue")

	modified := h.cache("ModifyCacheCluster", "CacheClusterId", "app-cache", "ApplyImmediately", "true", "CacheNodeType", "cache.t3.small", "SnapshotRetentionLimit", "5",
		"CacheParameterGroupName", "Other", "SecurityGroupIds.SecurityGroupId.1", "sg-0123456789abcdef0", "Engine", "valkey")
	noConnection(t, modified)
	changed := modified.result(t, "ModifyCacheCluster").get("CacheCluster")
	if nodeText(changed, "CacheNodeType") != "cache.t3.small" || nodeText(changed, "SnapshotRetentionLimit") != "5" || nodeText(changed, "Engine") != "valkey" ||
		nodeText(changed, "CacheParameterGroup", "CacheParameterGroupName") != "other" || nodeText(changed, "CacheClusterStatus") != "creating" {
		t.Fatalf("modify %s", modified.raw)
	}
	expectFault(t, h.cache("ModifyCacheCluster", "CacheClusterId", "missing", "CacheNodeType", "x"), 404, "CacheClusterNotFound")
	expectFault(t, h.cache("ModifyCacheCluster", "CacheClusterId", "app-cache", "NumCacheNodes", "2"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("ModifyCacheCluster", "CacheClusterId", "app-cache", "Engine", "mongo"), 400, "InvalidParameterValue")
	expectRefusal(t, h.cache("ModifyCacheCluster", "CacheClusterId", "app-cache", "AuthToken", "x"), 400, "InvalidParameterValue", "never stores")
	expectFault(t, h.cache("ModifyCacheCluster", "CacheClusterId", "app-cache", "CacheNodeIdsToRemove.CacheNodeId.1", "0001"), 400, "InvalidParameterValue")

	h.cache("AddTagsToResource", "ResourceName", arn, "Tags.Tag.1.Key", "stage", "Tags.Tag.1.Value", "dev").result(t, "AddTagsToResource")
	if tags := h.cache("ListTagsForResource", "ResourceName", arn).result(t, "ListTagsForResource").get("TagList").all("Tag"); len(tags) != 2 {
		t.Fatalf("tags %v", tags)
	}

	expectRefusal(t, h.cache("DeleteCacheCluster", "CacheClusterId", "app-cache", "FinalSnapshotIdentifier", "final"), 400, "InvalidParameterValue", "snapshot")
	deleted := h.cache("DeleteCacheCluster", "CacheClusterId", "app-cache")
	noConnection(t, deleted)
	if status := nodeText(deleted.result(t, "DeleteCacheCluster").get("CacheCluster"), "CacheClusterStatus"); status != "deleting" {
		t.Fatalf("delete %s", deleted.raw)
	}
	expectFault(t, h.cache("DescribeCacheClusters", "CacheClusterId", "app-cache"), 404, "CacheClusterNotFound")
}

func TestElastiCacheClusterRefusesWhatItCannotStoreHonestly(t *testing.T) {
	h := newHarness(t, elastiCache)
	create := func(extra ...string) queryReply {
		return h.cache("CreateCacheCluster", append([]string{"CacheClusterId", "c1", "Engine", "memcached", "NumCacheNodes", "3"}, extra...)...)
	}
	expectRefusal(t, create("AuthToken", "x"), 400, "InvalidParameterValue", "never stores")
	expectRefusal(t, h.cache("ModifyCacheCluster", "CacheClusterId", "c1", "AuthTokenUpdateStrategy", "SET"), 400, "InvalidParameterValue", "never stores")
	expectRefusal(t, create("Port", "11211"), 400, "InvalidParameterValue", "listener")
	for _, member := range []string{"CacheSecurityGroupNames.CacheSecurityGroupName.1", "ReplicationGroupId", "PreferredOutpostArn", "SnapshotName", "AZMode"} {
		expectFault(t, create(member, "x"), 400, "InvalidParameterValue")
	}
	expectFault(t, create("LogDeliveryConfigurations.LogDeliveryConfigurationRequest.1.LogType", "slow-log"), 400, "InvalidParameterValue")
	expectFault(t, create("NotificationTopicArn", "topic"), 400, "InvalidParameterValue")
	expectFault(t, create("SecurityGroupIds.SecurityGroupId.1", "sg-1"), 400, "InvalidParameterValue")
	expectFault(t, create("CacheParameterGroupName", "1bad"), 400, "InvalidParameterValue")
	expectFault(t, create("Tags.Tag.1.Value", "x"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateCacheCluster", "CacheClusterId", "c1", "Engine", "redis", "NumCacheNodes", "2"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateCacheCluster", "CacheClusterId", "c1", "Engine", "memcached", "NumCacheNodes", "41"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateCacheCluster", "CacheClusterId", "c1", "Engine", "mongo"), 400, "InvalidParameterValue")
	for _, name := range []string{"1c", "c-", "c--c", strings.Repeat("c", 51), "c_1"} {
		expectFault(t, h.cache("CreateCacheCluster", "CacheClusterId", name, "Engine", "redis"), 400, "InvalidParameterValue")
	}
	create().result(t, "CreateCacheCluster")
	for i := 0; i < 49; i++ {
		h.cache("CreateCacheCluster", "CacheClusterId", fmt.Sprintf("many-%d", i), "Engine", "redis").result(t, "CreateCacheCluster")
	}
	expectFault(t, h.cache("CreateCacheCluster", "CacheClusterId", "over", "Engine", "redis"), 400, "InvalidParameterValue")
	h.policy.deny["elasticache:DeleteCacheCluster"] = true
	expectFault(t, h.cache("DeleteCacheCluster", "CacheClusterId", "c1"), 403, "AccessDenied")
}

func TestElastiCacheReplicationGroupRecordLifecycle(t *testing.T) {
	h := newHarness(t, elastiCache)
	create := func(extra ...string) queryReply {
		return h.cache("CreateReplicationGroup", append([]string{"ReplicationGroupId", "App-Group", "ReplicationGroupDescription", "app", "Engine", "valkey",
			"CacheNodeType", "cache.t3.micro", "AutomaticFailoverEnabled", "true", "MultiAZEnabled", "false", "AtRestEncryptionEnabled", "true", "TransitEncryptionEnabled", "true",
			"SnapshotRetentionLimit", "2", "Tags.Tag.1.Key", "team", "Tags.Tag.1.Value", "a"}, extra...)...)
	}
	created := create()
	noConnection(t, created)
	group := created.result(t, "CreateReplicationGroup").get("ReplicationGroup")
	for path, want := range map[string]string{
		"ReplicationGroupId": "app-group", "Status": "creating", "Description": "app", "Engine": "valkey", "AutomaticFailover": "enabled", "MultiAZ": "disabled",
		"AtRestEncryptionEnabled": "true", "ARN": "arn:aws:elasticache:us-east-1:000000000000:replicationgroup:app-group",
	} {
		if got := nodeText(group, path); got != want {
			t.Fatalf("%s = %q, want %q: %s", path, got, want, created.raw)
		}
	}
	if group.get("ReplicationGroupCreateTime") == nil || group.get("MemberClusters") != nil && len(group.get("MemberClusters").kids) != 0 {
		t.Fatalf("stored settings %s", created.raw)
	}
	arn := nodeText(group, "ARN")
	expectFault(t, create(), 400, "ReplicationGroupAlreadyExists")

	described := h.cache("DescribeReplicationGroups", "ReplicationGroupId", "APP-GROUP")
	noConnection(t, described)
	if items := described.result(t, "DescribeReplicationGroups").get("ReplicationGroups").all("ReplicationGroup"); len(items) != 1 {
		t.Fatalf("describe %s", described.raw)
	}
	expectFault(t, h.cache("DescribeReplicationGroups", "ReplicationGroupId", "missing"), 404, "ReplicationGroupNotFoundFault")

	modified := h.cache("ModifyReplicationGroup", "ReplicationGroupId", "app-group", "ApplyImmediately", "true", "ReplicationGroupDescription", "changed",
		"MultiAZEnabled", "true", "CacheNodeType", "cache.t3.small")
	noConnection(t, modified)
	changed := modified.result(t, "ModifyReplicationGroup").get("ReplicationGroup")
	if nodeText(changed, "Description") != "changed" || nodeText(changed, "MultiAZ") != "enabled" || nodeText(changed, "CacheNodeType") != "cache.t3.small" ||
		nodeText(changed, "Status") != "creating" {
		t.Fatalf("modify %s", modified.raw)
	}
	expectFault(t, h.cache("ModifyReplicationGroup", "ReplicationGroupId", "missing", "CacheNodeType", "x"), 404, "ReplicationGroupNotFoundFault")
	expectFault(t, h.cache("ModifyReplicationGroup", "ReplicationGroupId", "app-group", "SnapshottingClusterId", "x"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("ModifyReplicationGroup", "ReplicationGroupId", "app-group", "UserGroupIdsToAdd.member.1", "g"), 400, "InvalidParameterValue")
	expectRefusal(t, h.cache("ModifyReplicationGroup", "ReplicationGroupId", "app-group", "AuthToken", "x"), 400, "InvalidParameterValue", "never stores")

	h.cache("AddTagsToResource", "ResourceName", arn, "Tags.Tag.1.Key", "stage", "Tags.Tag.1.Value", "dev").result(t, "AddTagsToResource")
	if tags := h.cache("ListTagsForResource", "ResourceName", arn).result(t, "ListTagsForResource").get("TagList").all("Tag"); len(tags) != 2 {
		t.Fatalf("tags %v", tags)
	}
	expectRefusal(t, h.cache("DeleteReplicationGroup", "ReplicationGroupId", "app-group", "FinalSnapshotIdentifier", "final"), 400, "InvalidParameterValue", "snapshot")
	deleted := h.cache("DeleteReplicationGroup", "ReplicationGroupId", "app-group", "RetainPrimaryCluster", "true")
	if status := nodeText(deleted.result(t, "DeleteReplicationGroup").get("ReplicationGroup"), "Status"); status != "deleting" {
		t.Fatalf("delete %s", deleted.raw)
	}
	expectFault(t, h.cache("DescribeReplicationGroups", "ReplicationGroupId", "app-group"), 404, "ReplicationGroupNotFoundFault")
}

func TestElastiCacheReplicationGroupRefusesWhatItCannotStoreHonestly(t *testing.T) {
	h := newHarness(t, elastiCache)
	create := func(extra ...string) queryReply {
		return h.cache("CreateReplicationGroup", append([]string{"ReplicationGroupId", "g1", "ReplicationGroupDescription", "d", "Engine", "redis"}, extra...)...)
	}
	expectRefusal(t, create("AuthToken", "x"), 400, "InvalidParameterValue", "never stores")
	expectRefusal(t, create("Port", "6379"), 400, "InvalidParameterValue", "listener")
	for _, member := range []string{"UserGroupIds.member.1", "GlobalReplicationGroupId", "PrimaryClusterId", "CacheSecurityGroupNames.CacheSecurityGroupName.1",
		"NumCacheClusters", "NumNodeGroups", "ReplicasPerNodeGroup", "EngineVersion", "PreferredMaintenanceWindow", "CacheParameterGroupName", "CacheSubnetGroupName", "SecurityGroupIds.SecurityGroupId.1"} {
		expectFault(t, create(member, "2"), 400, "InvalidParameterValue")
	}
	expectFault(t, h.cache("CreateReplicationGroup", "ReplicationGroupId", "g1", "ReplicationGroupDescription", "d", "Engine", "memcached"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateReplicationGroup", "ReplicationGroupId", strings.Repeat("g", 41), "ReplicationGroupDescription", "d"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateReplicationGroup", "ReplicationGroupId", "g1", "ReplicationGroupDescription", "d", "Tags.Tag.1.Value", "x"), 400, "InvalidParameterValue")
	create("DataTieringEnabled", "false").result(t, "CreateReplicationGroup")
	for i := 0; i < 49; i++ {
		h.cache("CreateReplicationGroup", "ReplicationGroupId", fmt.Sprintf("many-%d", i), "ReplicationGroupDescription", "d").result(t, "CreateReplicationGroup")
	}
	expectFault(t, h.cache("CreateReplicationGroup", "ReplicationGroupId", "over", "ReplicationGroupDescription", "d"), 400, "NodeGroupsPerReplicationGroupQuotaExceeded")
	expectFault(t, create("EngineVersion", "7.1"), 400, "InvalidParameterValue")
}

func TestElastiCacheServerlessCacheRecordLifecycle(t *testing.T) {
	h := newHarness(t, elastiCache)
	create := func(extra ...string) queryReply {
		return h.cache("CreateServerlessCache", append([]string{"ServerlessCacheName", "App-Serverless", "Engine", "valkey", "Description", "app", "MajorEngineVersion", "8",
			"SubnetIds.SubnetId.1", "subnet-0a1b2c3d", "SecurityGroupIds.SecurityGroupId.1", "sg-0a1b2c3d", "SnapshotRetentionLimit", "2", "DailySnapshotTime", "05:00",
			"CacheUsageLimits.DataStorage.Maximum", "10", "CacheUsageLimits.DataStorage.Unit", "GB", "CacheUsageLimits.ECPUPerSecond.Maximum", "5000",
			"Tags.Tag.1.Key", "team", "Tags.Tag.1.Value", "a"}, extra...)...)
	}
	created := create()
	noConnection(t, created)
	cache := created.result(t, "CreateServerlessCache").get("ServerlessCache")
	for path, want := range map[string]string{
		"ServerlessCacheName": "app-serverless", "Status": "creating", "Engine": "valkey", "Description": "app", "MajorEngineVersion": "8", "DailySnapshotTime": "05:00",
		"ARN": "arn:aws:elasticache:us-east-1:000000000000:serverlesscache:app-serverless",
	} {
		if got := nodeText(cache, path); got != want {
			t.Fatalf("%s = %q, want %q: %s", path, got, want, created.raw)
		}
	}
	if cache.get("CreateTime") == nil || nodeText(cache, "CacheUsageLimits", "DataStorage", "Unit") != "GB" || nodeText(cache, "SubnetIds", "SubnetId") != "subnet-0a1b2c3d" {
		t.Fatalf("stored settings %s", created.raw)
	}
	arn := nodeText(cache, "ARN")
	expectFault(t, create(), 400, "ServerlessCacheAlreadyExistsFault")

	described := h.cache("DescribeServerlessCaches", "ServerlessCacheName", "APP-SERVERLESS")
	noConnection(t, described)
	if items := described.result(t, "DescribeServerlessCaches").get("ServerlessCaches").all("member"); len(items) != 1 {
		t.Fatalf("describe %s", described.raw)
	}
	expectFault(t, h.cache("DescribeServerlessCaches", "ServerlessCacheName", "missing"), 404, "ServerlessCacheNotFoundFault")

	modified := h.cache("ModifyServerlessCache", "ServerlessCacheName", "app-serverless", "Description", "changed", "SnapshotRetentionLimit", "4", "DailySnapshotTime", "06:30")
	noConnection(t, modified)
	changed := modified.result(t, "ModifyServerlessCache").get("ServerlessCache")
	if nodeText(changed, "Description") != "changed" || nodeText(changed, "SnapshotRetentionLimit") != "4" || nodeText(changed, "Status") != "creating" {
		t.Fatalf("modify %s", modified.raw)
	}
	expectFault(t, h.cache("ModifyServerlessCache", "ServerlessCacheName", "missing", "Description", "x"), 404, "ServerlessCacheNotFoundFault")
	expectFault(t, h.cache("ModifyServerlessCache", "ServerlessCacheName", "app-serverless", "UserGroupId", "g"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("ModifyServerlessCache", "ServerlessCacheName", "app-serverless", "DailySnapshotTime", "25:00"), 400, "InvalidParameterValue")
	h.cache("AddTagsToResource", "ResourceName", arn, "Tags.Tag.1.Key", "stage", "Tags.Tag.1.Value", "dev").result(t, "AddTagsToResource")
	if tags := h.cache("ListTagsForResource", "ResourceName", arn).result(t, "ListTagsForResource").get("TagList").all("Tag"); len(tags) != 2 {
		t.Fatalf("tags %v", tags)
	}
	expectRefusal(t, h.cache("DeleteServerlessCache", "ServerlessCacheName", "app-serverless", "FinalSnapshotName", "final"), 400, "InvalidParameterValue", "snapshot")
	deleted := h.cache("DeleteServerlessCache", "ServerlessCacheName", "app-serverless")
	if status := nodeText(deleted.result(t, "DeleteServerlessCache").get("ServerlessCache"), "Status"); status != "deleting" {
		t.Fatalf("delete %s", deleted.raw)
	}
	expectFault(t, h.cache("DescribeServerlessCaches", "ServerlessCacheName", "app-serverless"), 404, "ServerlessCacheNotFoundFault")
}

func TestElastiCacheServerlessCacheRefusesWhatItCannotStoreHonestly(t *testing.T) {
	h := newHarness(t, elastiCache)
	create := func(extra ...string) queryReply {
		return h.cache("CreateServerlessCache", append([]string{"ServerlessCacheName", "s1", "Engine", "redis"}, extra...)...)
	}
	expectFault(t, create("UserGroupId", "g"), 400, "InvalidParameterValue")
	expectFault(t, create("SnapshotArnsToRestore.member.1", "arn:aws:s3:::bucket/key"), 400, "InvalidParameterValue")
	expectFault(t, create("SubnetIds.SubnetId.1", "subnet-1"), 400, "InvalidParameterValue")
	expectFault(t, create("SecurityGroupIds.SecurityGroupId.1", "sg-1"), 400, "InvalidParameterValue")
	expectFault(t, create("DailySnapshotTime", "5pm"), 400, "InvalidParameterValue")
	expectFault(t, create("Tags.Tag.1.Value", "x"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateServerlessCache", "ServerlessCacheName", "s1", "Engine", "mongo"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateServerlessCache", "ServerlessCacheName", "-s1", "Engine", "redis"), 400, "InvalidParameterValue")
	create().result(t, "CreateServerlessCache")
	for i := 0; i < 39; i++ {
		h.cache("CreateServerlessCache", "ServerlessCacheName", fmt.Sprintf("many-%d", i), "Engine", "redis").result(t, "CreateServerlessCache")
	}
	expectFault(t, h.cache("CreateServerlessCache", "ServerlessCacheName", "over", "Engine", "redis"), 400, "InvalidParameterValue")
}

func TestElastiCacheRecordsLeaveRunningCacheOperationsUnbound(t *testing.T) {
	h := newHarness(t, elastiCache)
	for _, action := range []string{"RebootCacheCluster", "TestFailover", "IncreaseReplicaCount", "DecreaseReplicaCount", "CreateSnapshot", "CopySnapshot", "TestMigration",
		"StartMigration", "CompleteMigration", "CreateGlobalReplicationGroup", "CreateServerlessCacheSnapshot", "CreateCacheSecurityGroup", "ModifyReplicationGroupShardConfiguration",
		"DescribeCacheEngineVersions", "DescribeReservedCacheNodes", "CreateUserGroup", "DescribeServiceUpdates", "DescribeEvents"} {
		expectFault(t, h.cache(action, "CacheClusterId", "c1"), 501, "UnknownOperationException")
	}
}

func TestRedshiftClusterRecordLifecycle(t *testing.T) {
	h := newHarness(t, redshift)
	create := func(extra ...string) queryReply {
		return h.warehouse("CreateCluster", append([]string{"ClusterIdentifier", "app-wh", "NodeType", "ra3.xlplus", "MasterUsername", "dbadmin", "ClusterType", "multi-node", "NumberOfNodes", "2",
			"ClusterParameterGroupName", "App-Params", "ClusterSubnetGroupName", "App-Subnets", "VpcSecurityGroupIds.VpcSecurityGroupId.1", "sg-0a1b2c3d",
			"IamRoles.IamRoleArn.1", "arn:aws:iam::000000000000:role/loader", "Encrypted", "true", "MultiAZ", "false", "PubliclyAccessible", "false",
			"AutomatedSnapshotRetentionPeriod", "7", "Tags.Tag.1.Key", "team", "Tags.Tag.1.Value", "a"}, extra...)...)
	}
	created := create()
	noConnection(t, created)
	cluster := created.result(t, "CreateCluster").get("Cluster")
	for path, want := range map[string]string{
		"ClusterIdentifier": "app-wh", "ClusterStatus": "creating", "NodeType": "ra3.xlplus", "MasterUsername": "dbadmin", "DBName": "dev", "NumberOfNodes": "2",
		"ClusterSubnetGroupName": "app-subnets", "Encrypted": "true", "MultiAZ": "Disabled", "AutomatedSnapshotRetentionPeriod": "7",
	} {
		if got := nodeText(cluster, path); got != want {
			t.Fatalf("%s = %q, want %q: %s", path, got, want, created.raw)
		}
	}
	if cluster.get("ClusterCreateTime") == nil || nodeText(cluster, "ClusterParameterGroups", "ClusterParameterGroup", "ParameterGroupName") != "app-params" ||
		nodeText(cluster, "ClusterParameterGroups", "ClusterParameterGroup", "ParameterApplyStatus") != "in-sync" ||
		nodeText(cluster, "VpcSecurityGroups", "VpcSecurityGroup", "Status") != "active" || nodeText(cluster, "IamRoles", "ClusterIamRole", "ApplyStatus") != "in-sync" ||
		nodeText(cluster, "Tags", "Tag", "Key") != "team" {
		t.Fatalf("stored settings %s", created.raw)
	}
	expectFault(t, create(), 400, "ClusterAlreadyExists")

	described := h.warehouse("DescribeClusters", "ClusterIdentifier", "APP-WH")
	noConnection(t, described)
	if items := described.result(t, "DescribeClusters").get("Clusters").all("Cluster"); len(items) != 1 {
		t.Fatalf("describe %s", described.raw)
	}
	expectFault(t, h.warehouse("DescribeClusters", "ClusterIdentifier", "missing"), 404, "ClusterNotFound")
	expectFault(t, h.warehouse("DescribeClusters", "MaxRecords", "5"), 400, "InvalidParameterValue")

	modified := h.warehouse("ModifyCluster", "ClusterIdentifier", "app-wh", "NodeType", "ra3.4xlarge", "ClusterType", "single-node", "MultiAZ", "true",
		"ClusterParameterGroupName", "Other", "AutomatedSnapshotRetentionPeriod", "3", "ManualSnapshotRetentionPeriod", "-1")
	noConnection(t, modified)
	changed := modified.result(t, "ModifyCluster").get("Cluster")
	if nodeText(changed, "NodeType") != "ra3.4xlarge" || nodeText(changed, "NumberOfNodes") != "1" || nodeText(changed, "MultiAZ") != "Enabled" ||
		nodeText(changed, "ClusterParameterGroups", "ClusterParameterGroup", "ParameterGroupName") != "other" || nodeText(changed, "ClusterStatus") != "creating" {
		t.Fatalf("modify %s", modified.raw)
	}
	expectFault(t, h.warehouse("ModifyCluster", "ClusterIdentifier", "missing", "NodeType", "x"), 404, "ClusterNotFound")
	expectFault(t, h.warehouse("ModifyCluster", "ClusterIdentifier", "app-wh", "NumberOfNodes", "3"), 400, "InvalidParameterValue")
	expectFault(t, h.warehouse("ModifyCluster", "ClusterIdentifier", "app-wh", "NodeType", "x", "ClusterType", "multi-node"), 400, "InvalidParameterValue")
	expectFault(t, h.warehouse("ModifyCluster", "ClusterIdentifier", "app-wh", "NodeType", "x", "ClusterType", "single-node", "NumberOfNodes", "2"), 400, "InvalidParameterValue")
	expectFault(t, h.warehouse("ModifyCluster", "ClusterIdentifier", "app-wh", "NodeType", "x", "ClusterType", "grid"), 400, "InvalidParameterValue")
	expectFault(t, h.warehouse("ModifyCluster", "ClusterIdentifier", "app-wh", "NodeType", "x", "NumberOfNodes", "101"), 400, "InvalidParameterValue")
	expectFault(t, h.warehouse("ModifyCluster", "ClusterIdentifier", "app-wh", "NewClusterIdentifier", "renamed"), 400, "InvalidParameterValue")
	expectRefusal(t, h.warehouse("ModifyCluster", "ClusterIdentifier", "app-wh", "MasterUserPassword", "x"), 400, "InvalidParameterValue", "never stores")

	arn := "arn:aws:redshift:us-east-1:000000000000:cluster:app-wh"
	h.warehouse("CreateTags", "ResourceName", arn, "Tags.Tag.1.Key", "stage", "Tags.Tag.1.Value", "dev").result(t, "CreateTags")
	if tags := h.warehouse("DescribeClusters", "ClusterIdentifier", "app-wh").result(t, "DescribeClusters").get("Clusters").get("Cluster").get("Tags").all("Tag"); len(tags) != 2 {
		t.Fatalf("tags %v", tags)
	}
	h.warehouse("DeleteTags", "ResourceName", arn, "TagKeys.TagKey.1", "team").result(t, "DeleteTags")

	expectFault(t, h.warehouse("DeleteCluster", "ClusterIdentifier", "app-wh"), 400, "InvalidParameterValue")
	expectFault(t, h.warehouse("DeleteCluster", "ClusterIdentifier", "app-wh", "SkipFinalClusterSnapshot", "false", "FinalClusterSnapshotIdentifier", "final"), 400, "InvalidParameterValue")
	expectRefusal(t, h.warehouse("DeleteCluster", "ClusterIdentifier", "app-wh", "SkipFinalClusterSnapshot", "true", "FinalClusterSnapshotRetentionPeriod", "3"), 400, "InvalidParameterValue", "snapshot")
	deleted := h.warehouse("DeleteCluster", "ClusterIdentifier", "app-wh", "SkipFinalClusterSnapshot", "true")
	noConnection(t, deleted)
	if status := nodeText(deleted.result(t, "DeleteCluster").get("Cluster"), "ClusterStatus"); status != "deleting" {
		t.Fatalf("delete %s", deleted.raw)
	}
	expectFault(t, h.warehouse("DescribeClusters", "ClusterIdentifier", "app-wh"), 404, "ClusterNotFound")
}

func TestRedshiftClusterRefusesWhatItCannotStoreHonestly(t *testing.T) {
	h := newHarness(t, redshift)
	create := func(extra ...string) queryReply {
		return h.warehouse("CreateCluster", append([]string{"ClusterIdentifier", "wh1", "NodeType", "ra3.xlplus", "MasterUsername", "dbadmin"}, extra...)...)
	}
	for member, value := range map[string]string{"MasterUserPassword": "x", "ManageMasterPassword": "true", "MasterPasswordSecretKmsKeyId": "x"} {
		expectRefusal(t, create(member, value), 400, "InvalidParameterValue", "never stores")
	}
	expectRefusal(t, create("Port", "5439"), 400, "InvalidParameterValue", "listener")
	expectRefusal(t, create("ElasticIp", "x"), 400, "InvalidParameterValue", "address")
	for member, value := range map[string]string{"ClusterSecurityGroups.ClusterSecurityGroupName.1": "x", "HsmClientCertificateIdentifier": "x", "SnapshotScheduleIdentifier": "x",
		"AdditionalInfo": "x", "LoadSampleData": "x", "AvailabilityZoneRelocation": "true", "AquaConfigurationStatus": "enabled", "CatalogName": "x"} {
		expectFault(t, create(member, value), 400, "InvalidParameterValue")
	}
	expectFault(t, create("ClusterType", "multi-node"), 400, "InvalidParameterValue")
	expectFault(t, create("ClusterType", "single-node", "NumberOfNodes", "2"), 400, "InvalidParameterValue")
	expectFault(t, create("ClusterType", "grid"), 400, "InvalidParameterValue")
	expectFault(t, create("NumberOfNodes", "0"), 400, "InvalidParameterValue")
	expectFault(t, create("DBName", "Mixed"), 400, "InvalidParameterValue")
	expectFault(t, create("AutomatedSnapshotRetentionPeriod", "36"), 400, "InvalidParameterValue")
	expectFault(t, create("ManualSnapshotRetentionPeriod", "0"), 400, "InvalidParameterValue")
	expectFault(t, create("IpAddressType", "ipv6"), 400, "InvalidParameterValue")
	expectFault(t, create("VpcSecurityGroupIds.VpcSecurityGroupId.1", "sg-1"), 400, "InvalidParameterValue")
	expectFault(t, create("ClusterParameterGroupName", "1bad"), 400, "InvalidParameterValue")
	expectFault(t, create("Tags.Tag.1.Value", "x"), 400, "InvalidTagFault")
	expectFault(t, create("IamRoles.IamRoleArn.1", "role"), 400, "InvalidParameterValue")
	var roles []string
	for i := 0; i < 11; i++ {
		roles = append(roles, fmt.Sprintf("IamRoles.IamRoleArn.%d", i+1), fmt.Sprintf("arn:aws:iam::000000000000:role/r%d", i))
	}
	expectFault(t, create(roles...), 400, "InvalidParameterValue")
	for _, name := range []string{"Upper", "1wh", "wh-", "w--h", strings.Repeat("w", 64), "w_h"} {
		expectFault(t, h.warehouse("CreateCluster", "ClusterIdentifier", name, "NodeType", "ra3.xlplus", "MasterUsername", "dbadmin"), 400, "InvalidParameterValue")
	}
	for _, user := range []string{"PUBLIC", "public", "1admin", "Admin", "ad:min"} {
		expectFault(t, h.warehouse("CreateCluster", "ClusterIdentifier", "wh1", "NodeType", "ra3.xlplus", "MasterUsername", user), 400, "InvalidParameterValue")
	}
	create().result(t, "CreateCluster")
	for i := 0; i < 19; i++ {
		h.warehouse("CreateCluster", "ClusterIdentifier", fmt.Sprintf("many-%d", i), "NodeType", "ra3.xlplus", "MasterUsername", "dbadmin").result(t, "CreateCluster")
	}
	expectFault(t, h.warehouse("CreateCluster", "ClusterIdentifier", "over", "NodeType", "ra3.xlplus", "MasterUsername", "dbadmin"), 400, "ClusterQuotaExceeded")
	h.policy.deny["redshift:DeleteCluster"] = true
	expectFault(t, h.warehouse("DeleteCluster", "ClusterIdentifier", "wh1", "SkipFinalClusterSnapshot", "true"), 403, "AccessDenied")
}

func TestRedshiftRecordsLeaveRunningWarehouseOperationsUnbound(t *testing.T) {
	h := newHarness(t, redshift)
	for _, action := range []string{"RebootCluster", "PauseCluster", "ResumeCluster", "ResizeCluster", "FailoverPrimaryCompute", "CreateClusterSnapshot", "RestoreFromClusterSnapshot",
		"RestoreTableFromClusterSnapshot", "CopyClusterSnapshot", "CreateAuthenticationProfile", "AuthorizeDataShare", "DescribeClusterVersions",
		"DescribeOrderableClusterOptions", "GetClusterCredentials", "EnableSnapshotCopy", "ModifyClusterIamRoles", "DescribeTags"} {
		expectFault(t, h.warehouse(action, "ClusterIdentifier", "wh1"), 501, "UnknownOperationException")
	}
}

func TestElastiCacheEngineLessNodeCount(t *testing.T) {
	h := newHarness(t, elastiCache)
	for _, n := range []string{"1000", "-3", "0"} {
		expectFault(t, h.cache("CreateCacheCluster", "CacheClusterId", "n1", "NumCacheNodes", n), 400, "InvalidParameterValue")
	}
	if r := h.cache("CreateCacheCluster", "CacheClusterId", "n2", "NumCacheNodes", "5"); r.status != 200 {
		t.Fatalf("engine-less cluster with 5 nodes: %d %s", r.status, r.raw)
	}
	expectFault(t, h.cache("ModifyCacheCluster", "CacheClusterId", "n2", "NumCacheNodes", "9000"), 400, "InvalidParameterValue")
}
