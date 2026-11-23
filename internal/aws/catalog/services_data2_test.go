package catalog

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// call sends one Query Action signed for the given credential-scope service.
func (h *harness) call(service, version, action string, pairs ...string) queryReply {
	h.t.Helper()
	form := url.Values{"Action": {action}, "Version": {version}}
	for i := 0; i+1 < len(pairs); i += 2 {
		form.Add(pairs[i], pairs[i+1])
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	sign(req, service, "us-east-1")
	if !h.engine.Owns(req) {
		h.t.Fatalf("the catalog does not own a %s-signed Query request", service)
	}
	return h.send(req)
}

func (h *harness) cache(action string, pairs ...string) queryReply {
	h.t.Helper()
	return h.call("elasticache", "2015-02-02", action, pairs...)
}

func (h *harness) warehouse(action string, pairs ...string) queryReply {
	h.t.Helper()
	return h.call("redshift", "2012-12-01", action, pairs...)
}

func expectFault(t *testing.T, r queryReply, status int, code string) {
	t.Helper()
	if r.status != status || r.code() != code || r.root.get("Error", "Message") == nil {
		t.Fatalf("want %d %s, got %d %s: %s", status, code, r.status, r.code(), r.raw)
	}
}

func subnetPairs(prefix string, ids ...string) []string {
	var pairs []string
	for i, id := range ids {
		pairs = append(pairs, fmt.Sprintf("%s.SubnetIdentifier.%d", prefix, i+1), id)
	}
	return pairs
}

func TestElastiCacheLifecycleMatchesOfficialWire(t *testing.T) {
	h := newHarness(t, elastiCache)

	created := h.cache("CreateCacheParameterGroup", "CacheParameterGroupName", "App-Params", "CacheParameterGroupFamily", "valkey8", "Description", "app",
		"Tags.Tag.1.Key", "team", "Tags.Tag.1.Value", "a")
	if created.root.attrs["xmlns"] != "http://elasticache.amazonaws.com/doc/2015-02-02/" {
		t.Fatalf("namespace %s", created.raw)
	}
	pg := created.result(t, "CreateCacheParameterGroup").get("CacheParameterGroup")
	if pg.get("CacheParameterGroupName").text != "App-Params" || pg.get("IsGlobal").text != "false" ||
		pg.get("ARN").text != "arn:aws:elasticache:us-east-1:000000000000:parametergroup:App-Params" {
		t.Fatalf("parameter group %s", created.raw)
	}
	expectFault(t, h.cache("CreateCacheParameterGroup", "CacheParameterGroupName", "App-Params", "CacheParameterGroupFamily", "valkey8", "Description", "x"), 400, "CacheParameterGroupAlreadyExists")
	expectFault(t, h.cache("CreateCacheParameterGroup", "CacheParameterGroupName", "bad", "CacheParameterGroupFamily", "mysql8.0", "Description", "x"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("DescribeCacheParameterGroups", "CacheParameterGroupName", "missing"), 404, "CacheParameterGroupNotFound")
	expectFault(t, h.cache("DescribeCacheParameterGroups", "MaxRecords", "19"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("DescribeCacheParameterGroups", "MaxRecords", "101"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("DeleteCacheParameterGroup", "CacheParameterGroupName", "missing"), 404, "CacheParameterGroupNotFound")
	if items := h.cache("DescribeCacheParameterGroups", "CacheParameterGroupName", "App-Params").result(t, "DescribeCacheParameterGroups").get("CacheParameterGroups").all("CacheParameterGroup"); len(items) != 1 {
		t.Fatalf("describe %d", len(items))
	}

	subnet := append([]string{"CacheSubnetGroupName", "App-Subnets", "CacheSubnetGroupDescription", "app"}, subnetPairs("SubnetIds", "subnet-0a1b2c3d", "subnet-4e5f6a7b")...)
	group := h.cache("CreateCacheSubnetGroup", subnet...).result(t, "CreateCacheSubnetGroup").get("CacheSubnetGroup")
	if group.get("CacheSubnetGroupName").text != "app-subnets" || group.get("ARN").text != "arn:aws:elasticache:us-east-1:000000000000:subnetgroup:app-subnets" ||
		len(group.get("Subnets").all("Subnet")) != 2 || group.get("VpcId") != nil {
		t.Fatalf("subnet group %v", group)
	}
	expectFault(t, h.cache("CreateCacheSubnetGroup", subnet...), 400, "CacheSubnetGroupAlreadyExists")
	expectFault(t, h.cache("DescribeCacheSubnetGroups", "CacheSubnetGroupName", "missing"), 400, "CacheSubnetGroupNotFoundFault")
	if items := h.cache("DescribeCacheSubnetGroups", "CacheSubnetGroupName", "APP-SUBNETS").result(t, "DescribeCacheSubnetGroups").get("CacheSubnetGroups").all("CacheSubnetGroup"); len(items) != 1 {
		t.Fatalf("describe subnet groups %d", len(items))
	}
	expectFault(t, h.cache("DescribeCacheSubnetGroups", "MaxRecords", "5"), 400, "InvalidParameterValue")
	// Only the description changes when no subnets are supplied.
	described := h.cache("ModifyCacheSubnetGroup", "CacheSubnetGroupName", "APP-subnets", "CacheSubnetGroupDescription", "renamed").
		result(t, "ModifyCacheSubnetGroup").get("CacheSubnetGroup")
	if described.get("CacheSubnetGroupDescription").text != "renamed" || len(described.get("Subnets").all("Subnet")) != 2 {
		t.Fatalf("modify description %v", described)
	}
	three := append([]string{"CacheSubnetGroupName", "app-subnets"}, subnetPairs("SubnetIds", "subnet-0a1b2c3d", "subnet-4e5f6a7b", "subnet-0123456789abcdef0")...)
	if g := h.cache("ModifyCacheSubnetGroup", three...).result(t, "ModifyCacheSubnetGroup").get("CacheSubnetGroup"); len(g.get("Subnets").all("Subnet")) != 3 {
		t.Fatalf("modify subnets %v", g)
	}
	expectFault(t, h.cache("ModifyCacheSubnetGroup", "CacheSubnetGroupName", "missing", "CacheSubnetGroupDescription", "x"), 400, "CacheSubnetGroupNotFoundFault")
	expectFault(t, h.cache("ModifyCacheSubnetGroup", "CacheSubnetGroupName", "app-subnets", "CacheSubnetGroupDescription", "x", "VpcId", "vpc-1"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("DeleteCacheSubnetGroup", "CacheSubnetGroupName", "missing"), 400, "CacheSubnetGroupNotFoundFault")

	user := h.cache("CreateUser", "UserId", "alice", "UserName", "alice", "Engine", "redis", "AccessString", "on ~app:* +get",
		"AuthenticationMode.Type", "iam", "Tags.Tag.1.Key", "team", "Tags.Tag.1.Value", "a").result(t, "CreateUser")
	if user.get("UserId").text != "alice" || user.get("Status").text != "active" || user.get("MinimumEngineVersion").text != "6.0" ||
		user.get("Authentication", "Type").text != "iam" || user.get("Authentication", "PasswordCount") != nil ||
		user.get("ARN").text != "arn:aws:elasticache:us-east-1:000000000000:user:alice" || len(user.get("UserGroupIds").kids) != 0 {
		t.Fatalf("user %v", user)
	}
	expectFault(t, h.cache("CreateUser", "UserId", "alice", "UserName", "alice", "Engine", "valkey", "AccessString", "on", "AuthenticationMode.Type", "iam"), 400, "UserAlreadyExists")
	if items := h.cache("DescribeUsers", "Engine", "valkey").result(t, "DescribeUsers").get("Users").all("member"); len(items) != 0 {
		t.Fatalf("engine filter %d", len(items))
	}
	if items := h.cache("DescribeUsers", "UserId", "alice").result(t, "DescribeUsers").get("Users").all("member"); len(items) != 1 {
		t.Fatalf("describe user %d", len(items))
	}
	expectFault(t, h.cache("DescribeUsers", "UserId", "missing"), 404, "UserNotFound")
	expectFault(t, h.cache("DescribeUsers", "Filters.member.1.Name", "UserId", "Filters.member.1.Values.member.1", "alice"), 400, "InvalidParameterValue")
	modified := h.cache("ModifyUser", "UserId", "alice", "AccessString", "on ~* +@all", "Engine", "valkey", "AuthenticationMode.Type", "iam").result(t, "ModifyUser")
	if modified.get("AccessString").text != "on ~* +@all" || modified.get("Engine").text != "valkey" || modified.get("MinimumEngineVersion").text != "7.2" || modified.get("Authentication", "Type").text != "iam" {
		t.Fatalf("modify user %v", modified)
	}
	h.cache("ModifyUser", "UserId", "alice", "AccessString", "on ~app:* +get").result(t, "ModifyUser")
	expectFault(t, h.cache("ModifyUser", "UserId", "missing", "AccessString", "on"), 404, "UserNotFound")
	expectFault(t, h.cache("ModifyUser", "UserId", "alice", "AppendAccessString", "+set"), 400, "InvalidParameterValue")

	arn := "arn:aws:elasticache:us-east-1:000000000000:user:alice"
	h.cache("AddTagsToResource", "ResourceName", arn, "Tags.Tag.1.Key", "stage", "Tags.Tag.1.Value", "dev").result(t, "AddTagsToResource")
	h.cache("RemoveTagsFromResource", "ResourceName", arn, "TagKeys.member.1", "team", "TagKeys.member.2", "absent").result(t, "RemoveTagsFromResource")
	tags := h.cache("ListTagsForResource", "ResourceName", arn).result(t, "ListTagsForResource").get("TagList").all("Tag")
	if len(tags) != 1 || tags[0].get("Key").text != "stage" || tags[0].get("Value").text != "dev" {
		t.Fatalf("tags %v", tags)
	}
	expectFault(t, h.cache("ListTagsForResource", "ResourceName", "arn:aws:elasticache:us-east-1:000000000000:parametergroup:missing"), 404, "CacheClusterNotFound")
	expectFault(t, h.cache("ListTagsForResource", "ResourceName", "not-an-arn"), 400, "InvalidARN")
	expectFault(t, h.cache("RemoveTagsFromResource", "ResourceName", "not-an-arn", "TagKeys.member.1", "x"), 400, "InvalidARN")
	expectFault(t, h.cache("AddTagsToResource", "ResourceName", "not-an-arn", "Tags.Tag.1.Key", "a", "Tags.Tag.1.Value", "b"), 400, "InvalidARN")
	expectFault(t, h.cache("AddTagsToResource", "ResourceName", arn, "Tags.Tag.1.Key", "aws:x", "Tags.Tag.1.Value", "y"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("AddTagsToResource", "ResourceName", arn, "Tags.Tag.1.Value", "y"), 400, "InvalidParameterValue")
	var many []string
	for i := 1; i <= 51; i++ {
		many = append(many, fmt.Sprintf("Tags.Tag.%d.Key", i), fmt.Sprintf("k%d", i), fmt.Sprintf("Tags.Tag.%d.Value", i), "v")
	}
	expectFault(t, h.cache("AddTagsToResource", append([]string{"ResourceName", arn}, many...)...), 400, "TagQuotaPerResourceExceeded")
	expectFault(t, h.cache("CreateCacheParameterGroup", append([]string{"CacheParameterGroupName", "many", "CacheParameterGroupFamily", "redis7", "Description", "d"}, many...)...), 400, "TagQuotaPerResourceExceeded")

	deleted := h.cache("DeleteUser", "UserId", "alice").result(t, "DeleteUser")
	if deleted.get("Status").text != "deleting" {
		t.Fatalf("delete user %v", deleted)
	}
	expectFault(t, h.cache("DeleteUser", "UserId", "alice"), 404, "UserNotFound")
	h.cache("DeleteCacheSubnetGroup", "CacheSubnetGroupName", "App-Subnets").result(t, "DeleteCacheSubnetGroup")
	h.cache("DeleteCacheParameterGroup", "CacheParameterGroupName", "App-Params").result(t, "DeleteCacheParameterGroup")
	expectFault(t, h.cache("DeleteCacheParameterGroup", "CacheParameterGroupName", "App-Params"), 404, "CacheParameterGroupNotFound")
}

func TestElastiCacheRefusesWhatItCannotStoreHonestly(t *testing.T) {
	h := newHarness(t, elastiCache)
	user := func(extra ...string) queryReply {
		return h.cache("CreateUser", append([]string{"UserId", "bob", "UserName", "bob", "Engine", "redis", "AccessString", "on"}, extra...)...)
	}
	expectFault(t, user(), 400, "InvalidParameterCombination")
	expectFault(t, user("AuthenticationMode.Type", "password", "AuthenticationMode.Passwords.member.1", "secretsecretsecret"), 400, "InvalidParameterValue")
	expectFault(t, user("AuthenticationMode.Type", "no-password-required"), 400, "InvalidParameterValue")
	expectFault(t, user("AuthenticationMode.Type", "iam", "Passwords.member.1", "secretsecretsecret"), 400, "InvalidParameterValue")
	expectFault(t, user("AuthenticationMode.Type", "iam", "NoPasswordRequired", "true"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateUser", "UserId", "bob", "UserName", "robert", "Engine", "redis", "AccessString", "on", "AuthenticationMode.Type", "iam"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateUser", "UserId", "default", "UserName", "default", "Engine", "redis", "AccessString", "on", "AuthenticationMode.Type", "iam"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateUser", "UserId", "bob", "UserName", "bob", "Engine", "memcached", "AccessString", "on", "AuthenticationMode.Type", "iam"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateUser", "UserId", "bob", "UserName", "bob", "Engine", "redis", "AccessString", "on", "AuthenticationMode.Type", "iam", "Tags.Tag.1.Value", "x"), 400, "InvalidParameterValue")
	user("AuthenticationMode.Type", "iam").result(t, "CreateUser")
	expectFault(t, h.cache("ModifyUser", "UserId", "bob", "Passwords.member.1", "secretsecretsecret"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("ModifyUser", "UserId", "bob", "AuthenticationMode.Type", "password", "AuthenticationMode.Passwords.member.1", "secretsecretsecret"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("ModifyUser", "UserId", "bob", "Engine", "memcached"), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateCacheSubnetGroup", "CacheSubnetGroupName", "under_score", "CacheSubnetGroupDescription", "d", "SubnetIds", ""), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateCacheSubnetGroup", "CacheSubnetGroupName", "empty", "CacheSubnetGroupDescription", "d", "SubnetIds", ""), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateCacheSubnetGroup", append([]string{"CacheSubnetGroupName", "bad", "CacheSubnetGroupDescription", "d"}, subnetPairs("SubnetIds", "subnet-1")...)...), 400, "InvalidSubnet")
	expectFault(t, h.cache("CreateCacheSubnetGroup", append([]string{"CacheSubnetGroupName", "twice", "CacheSubnetGroupDescription", "d"}, subnetPairs("SubnetIds", "subnet-0a1b2c3d", "subnet-0a1b2c3d")...)...), 400, "InvalidParameterValue")
	expectFault(t, h.cache("CreateCacheSubnetGroup", append([]string{"CacheSubnetGroupName", "tagless", "CacheSubnetGroupDescription", "d", "Tags.Tag.1.Value", "x"}, subnetPairs("SubnetIds", "subnet-0a1b2c3d")...)...), 400, "InvalidParameterValue")
	var ids []string
	for i := 0; i < 21; i++ {
		ids = append(ids, fmt.Sprintf("subnet-%08x", i))
	}
	expectFault(t, h.cache("CreateCacheSubnetGroup", append([]string{"CacheSubnetGroupName", "wide", "CacheSubnetGroupDescription", "d"}, subnetPairs("SubnetIds", ids...)...)...), 400, "CacheSubnetQuotaExceededFault")
	one := h.cache("CreateCacheSubnetGroup", append([]string{"CacheSubnetGroupName", "one", "CacheSubnetGroupDescription", "d"}, subnetPairs("SubnetIds", "subnet-0a1b2c3d")...)...)
	one.result(t, "CreateCacheSubnetGroup")
	expectFault(t, h.cache("ModifyCacheSubnetGroup", append([]string{"CacheSubnetGroupName", "one"}, subnetPairs("SubnetIds", ids...)...)...), 400, "CacheSubnetQuotaExceededFault")
	// Operations that provision or run a cache stay unbound.
	expectFault(t, h.cache("CreateCacheCluster", "CacheClusterId", "c"), 501, "UnknownOperationException")
	expectFault(t, h.cache("CreateUserGroup", "UserGroupId", "g", "Engine", "redis"), 501, "UnknownOperationException")
	expectFault(t, h.cache("ModifyCacheParameterGroup", "CacheParameterGroupName", "g"), 501, "UnknownOperationException")
	// Resource limits are local and per Region.
	for i := 0; i < 50; i++ {
		h.cache("CreateCacheParameterGroup", "CacheParameterGroupName", fmt.Sprintf("p%d", i), "CacheParameterGroupFamily", "redis7", "Description", "d").result(t, "CreateCacheParameterGroup")
	}
	expectFault(t, h.cache("CreateCacheParameterGroup", "CacheParameterGroupName", "over", "CacheParameterGroupFamily", "redis7", "Description", "d"), 400, "CacheParameterGroupQuotaExceeded")
	h.policy.deny["elasticache:CreateUser"] = true
	expectFault(t, user("AuthenticationMode.Type", "iam"), 403, "AccessDenied")
}

func TestRedshiftLifecycleMatchesOfficialWire(t *testing.T) {
	h := newHarness(t, redshift)

	created := h.warehouse("CreateClusterParameterGroup", "ParameterGroupName", "App-Params", "ParameterGroupFamily", "redshift-1.0", "Description", "app",
		"Tags.Tag.1.Key", "team", "Tags.Tag.1.Value", "a")
	if created.root.attrs["xmlns"] != "http://redshift.amazonaws.com/doc/2012-12-01/" {
		t.Fatalf("namespace %s", created.raw)
	}
	pg := created.result(t, "CreateClusterParameterGroup").get("ClusterParameterGroup")
	if pg.get("ParameterGroupName").text != "app-params" || pg.get("ParameterGroupFamily").text != "redshift-1.0" ||
		pg.get("Tags").all("Tag")[0].get("Key").text != "team" {
		t.Fatalf("parameter group %s", created.raw)
	}
	expectFault(t, h.warehouse("CreateClusterParameterGroup", "ParameterGroupName", "app-params", "ParameterGroupFamily", "redshift-1.0", "Description", "x"), 400, "ClusterParameterGroupAlreadyExists")
	for _, name := range []string{"1abc", "bad_name", "trail-", "dou--ble"} {
		expectFault(t, h.warehouse("CreateClusterParameterGroup", "ParameterGroupName", name, "ParameterGroupFamily", "redshift-1.0", "Description", "x"), 400, "InvalidParameterValue")
	}
	expectFault(t, h.warehouse("CreateClusterParameterGroup", "ParameterGroupName", "tagless", "ParameterGroupFamily", "redshift-1.0", "Description", "x", "Tags.Tag.1.Key", "aws:x", "Tags.Tag.1.Value", "y"), 400, "InvalidTagFault")
	expectFault(t, h.warehouse("DescribeClusterParameterGroups", "ParameterGroupName", "missing"), 404, "ClusterParameterGroupNotFound")
	expectFault(t, h.warehouse("DescribeClusterParameterGroups", "MaxRecords", "19"), 400, "InvalidParameterValue")
	expectFault(t, h.warehouse("DescribeClusterParameterGroups", "TagKeys.TagKey.1", "team"), 400, "InvalidParameterValue")
	expectFault(t, h.warehouse("DeleteClusterParameterGroup", "ParameterGroupName", "missing"), 404, "ClusterParameterGroupNotFound")
	if items := h.warehouse("DescribeClusterParameterGroups", "ParameterGroupName", "APP-Params").result(t, "DescribeClusterParameterGroups").get("ParameterGroups").all("ClusterParameterGroup"); len(items) != 1 {
		t.Fatalf("describe %d", len(items))
	}

	subnet := append([]string{"ClusterSubnetGroupName", "App-Subnets", "Description", "app"}, subnetPairs("SubnetIds", "subnet-0a1b2c3d", "subnet-4e5f6a7b")...)
	group := h.warehouse("CreateClusterSubnetGroup", subnet...).result(t, "CreateClusterSubnetGroup").get("ClusterSubnetGroup")
	if group.get("ClusterSubnetGroupName").text != "app-subnets" || len(group.get("Subnets").all("Subnet")) != 2 || group.get("VpcId") != nil || group.get("SubnetGroupStatus") != nil {
		t.Fatalf("subnet group %v", group)
	}
	expectFault(t, h.warehouse("CreateClusterSubnetGroup", subnet...), 400, "ClusterSubnetGroupAlreadyExists")
	expectFault(t, h.warehouse("DescribeClusterSubnetGroups", "ClusterSubnetGroupName", "missing"), 400, "ClusterSubnetGroupNotFoundFault")
	if items := h.warehouse("DescribeClusterSubnetGroups", "ClusterSubnetGroupName", "APP-SUBNETS").result(t, "DescribeClusterSubnetGroups").get("ClusterSubnetGroups").all("ClusterSubnetGroup"); len(items) != 1 {
		t.Fatalf("describe subnet groups %d", len(items))
	}
	three := append([]string{"ClusterSubnetGroupName", "APP-subnets", "Description", "three"}, subnetPairs("SubnetIds", "subnet-0a1b2c3d", "subnet-4e5f6a7b", "subnet-0123456789abcdef0")...)
	if g := h.warehouse("ModifyClusterSubnetGroup", three...).result(t, "ModifyClusterSubnetGroup").get("ClusterSubnetGroup"); len(g.get("Subnets").all("Subnet")) != 3 || g.get("Description").text != "three" {
		t.Fatalf("modify %v", g)
	}
	expectFault(t, h.warehouse("ModifyClusterSubnetGroup", append([]string{"ClusterSubnetGroupName", "missing"}, subnetPairs("SubnetIds", "subnet-0a1b2c3d")...)...), 400, "ClusterSubnetGroupNotFoundFault")
	expectFault(t, h.warehouse("DeleteClusterSubnetGroup", "ClusterSubnetGroupName", "missing"), 400, "ClusterSubnetGroupNotFoundFault")

	// Tags apply by ARN and show on the group description.
	arn := "arn:aws:redshift:us-east-1:000000000000:subnetgroup:app-subnets"
	h.warehouse("CreateTags", "ResourceName", arn, "Tags.Tag.1.Key", "stage", "Tags.Tag.1.Value", "dev").result(t, "CreateTags")
	h.warehouse("CreateTags", "ResourceName", "arn:aws:redshift:us-east-1:000000000000:parametergroup:app-params", "Tags.Tag.1.Key", "stage", "Tags.Tag.1.Value", "dev").result(t, "CreateTags")
	h.warehouse("DeleteTags", "ResourceName", arn, "TagKeys.TagKey.1", "absent").result(t, "DeleteTags")
	tagged := h.warehouse("DescribeClusterSubnetGroups", "ClusterSubnetGroupName", "app-subnets").result(t, "DescribeClusterSubnetGroups").get("ClusterSubnetGroups").all("ClusterSubnetGroup")[0].get("Tags").all("Tag")
	if len(tagged) != 1 || tagged[0].get("Key").text != "stage" {
		t.Fatalf("tags %v", tagged)
	}
	h.warehouse("DeleteTags", "ResourceName", arn, "TagKeys.TagKey.1", "stage").result(t, "DeleteTags")
	expectFault(t, h.warehouse("CreateTags", "ResourceName", "arn:aws:redshift:us-east-1:000000000000:cluster:missing", "Tags.Tag.1.Key", "a", "Tags.Tag.1.Value", "b"), 404, "ResourceNotFoundFault")
	expectFault(t, h.warehouse("DeleteTags", "ResourceName", "arn:aws:redshift:us-east-1:000000000000:cluster:missing", "TagKeys.TagKey.1", "a"), 404, "ResourceNotFoundFault")
	expectFault(t, h.warehouse("CreateTags", "ResourceName", arn, "Tags.Tag.1.Key", "AWS:x", "Tags.Tag.1.Value", "y"), 400, "InvalidTagFault")
	var many []string
	for i := 1; i <= 51; i++ {
		many = append(many, fmt.Sprintf("Tags.Tag.%d.Key", i), fmt.Sprintf("k%d", i), fmt.Sprintf("Tags.Tag.%d.Value", i), "v")
	}
	expectFault(t, h.warehouse("CreateTags", append([]string{"ResourceName", arn}, many...)...), 400, "TagLimitExceededFault")

	h.warehouse("DeleteClusterSubnetGroup", "ClusterSubnetGroupName", "App-Subnets").result(t, "DeleteClusterSubnetGroup")
	h.warehouse("DeleteClusterParameterGroup", "ParameterGroupName", "App-Params").result(t, "DeleteClusterParameterGroup")
	expectFault(t, h.warehouse("DeleteClusterParameterGroup", "ParameterGroupName", "app-params"), 404, "ClusterParameterGroupNotFound")
}

func TestRedshiftRefusesWhatItCannotStoreHonestly(t *testing.T) {
	h := newHarness(t, redshift)
	create := func(name string, extra ...string) queryReply {
		return h.warehouse("CreateClusterSubnetGroup", append([]string{"ClusterSubnetGroupName", name, "Description", "d"}, extra...)...)
	}
	one := subnetPairs("SubnetIds", "subnet-0a1b2c3d")
	expectFault(t, create("Default", one...), 400, "InvalidParameterValue")
	expectFault(t, create("under_score", one...), 400, "InvalidParameterValue")
	expectFault(t, create("empty", "SubnetIds", ""), 400, "InvalidParameterValue")
	expectFault(t, create("bad", subnetPairs("SubnetIds", "subnet-1")...), 400, "InvalidSubnet")
	expectFault(t, create("twice", subnetPairs("SubnetIds", "subnet-0a1b2c3d", "subnet-0a1b2c3d")...), 400, "InvalidParameterValue")
	expectFault(t, create("tagless", append(one, "Tags.Tag.1.Value", "x")...), 400, "InvalidTagFault")
	var ids []string
	for i := 0; i < 21; i++ {
		ids = append(ids, fmt.Sprintf("subnet-%08x", i))
	}
	expectFault(t, create("wide", subnetPairs("SubnetIds", ids...)...), 400, "ClusterSubnetQuotaExceededFault")
	create("one", one...).result(t, "CreateClusterSubnetGroup")
	expectFault(t, h.warehouse("ModifyClusterSubnetGroup", append([]string{"ClusterSubnetGroupName", "one"}, subnetPairs("SubnetIds", ids...)...)...), 400, "ClusterSubnetQuotaExceededFault")
	for _, action := range []string{"CreateCluster", "CreateClusterSnapshot", "CreateSnapshotSchedule", "CreateEventSubscription", "DescribeTags", "ModifyClusterParameterGroup", "CreateUsageLimit"} {
		expectFault(t, h.warehouse(action), 501, "UnknownOperationException")
	}
	for i := 0; i < 50; i++ {
		h.warehouse("CreateClusterParameterGroup", "ParameterGroupName", fmt.Sprintf("p%d", i), "ParameterGroupFamily", "redshift-1.0", "Description", "d").result(t, "CreateClusterParameterGroup")
	}
	expectFault(t, h.warehouse("CreateClusterParameterGroup", "ParameterGroupName", "over", "ParameterGroupFamily", "redshift-1.0", "Description", "d"), 400, "ClusterParameterGroupQuotaExceeded")
	h.policy.deny["redshift:CreateClusterParameterGroup"] = true
	expectFault(t, h.warehouse("CreateClusterParameterGroup", "ParameterGroupName", "denied", "ParameterGroupFamily", "redshift-1.0", "Description", "d"), 403, "AccessDenied")
}

// Each service is routed by its own credential-scope name, so signing as
// one never reaches the other and neither claims the RDS requests.
func TestQueryServicesRouteByCredentialScope(t *testing.T) {
	h := newHarness(t, rds, elastiCache, redshift)
	h.cache("CreateCacheParameterGroup", "CacheParameterGroupName", "shared", "CacheParameterGroupFamily", "redis7", "Description", "d").result(t, "CreateCacheParameterGroup")
	h.warehouse("CreateClusterParameterGroup", "ParameterGroupName", "shared", "ParameterGroupFamily", "redshift-1.0", "Description", "d").result(t, "CreateClusterParameterGroup")
	h.query("CreateDBParameterGroup", "DBParameterGroupName", "shared", "DBParameterGroupFamily", "mysql8.0", "Description", "d").result(t, "CreateDBParameterGroup")
	expectFault(t, h.call("elasticache", "2015-02-02", "CreateClusterParameterGroup", "ParameterGroupName", "x"), 400, "InvalidAction")
	expectFault(t, h.call("redshift", "2012-12-01", "CreateCacheParameterGroup"), 400, "InvalidAction")
}
