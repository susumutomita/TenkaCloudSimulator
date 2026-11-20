package catalog

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func (h *harness) scaling(action string, pairs ...string) queryReply {
	h.t.Helper()
	return h.call("autoscaling", "2011-01-01", action, pairs...)
}

func (h *harness) beanstalk(action string, pairs ...string) queryReply {
	h.t.Helper()
	return h.call("elasticbeanstalk", "2010-12-01", action, pairs...)
}

func launchPairs(name string, extra ...string) []string {
	return append([]string{"LaunchConfigurationName", name, "ImageId", "ami-0123456789abcdef0", "InstanceType", "t3.micro"}, extra...)
}

func TestAutoScalingLaunchConfigurationLifecycleMatchesOfficialWire(t *testing.T) {
	h := newHarness(t, autoScaling)
	userData := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\necho hello\n"))
	created := h.scaling("CreateLaunchConfiguration", launchPairs("web",
		"SecurityGroups.member.1", "sg-0a1b2c3d", "SecurityGroups.member.2", "sg-4e5f6a7b", "KeyName", "ops", "UserData", userData,
		"IamInstanceProfile", "web-profile", "InstanceMonitoring.Enabled", "false", "EbsOptimized", "true", "AssociatePublicIpAddress", "true",
		"PlacementTenancy", "dedicated", "SpotPrice", "0.05", "MetadataOptions.HttpTokens", "required", "MetadataOptions.HttpPutResponseHopLimit", "2",
		"BlockDeviceMappings.member.1.DeviceName", "/dev/sda1", "BlockDeviceMappings.member.1.Ebs.VolumeSize", "30",
		"BlockDeviceMappings.member.1.Ebs.VolumeType", "gp3", "BlockDeviceMappings.member.1.Ebs.Iops", "3000", "BlockDeviceMappings.member.1.Ebs.Throughput", "125",
		"BlockDeviceMappings.member.2.DeviceName", "/dev/sdb", "BlockDeviceMappings.member.2.VirtualName", "ephemeral0",
		"BlockDeviceMappings.member.3.DeviceName", "/dev/sdc", "BlockDeviceMappings.member.3.NoDevice", "true",
		"BlockDeviceMappings.member.4.DeviceName", "/dev/sdd", "BlockDeviceMappings.member.4.Ebs.SnapshotId", "snap-0123456789abcdef0")...)
	if created.root.attrs["xmlns"] != "http://autoscaling.amazonaws.com/doc/2011-01-01/" || created.status != 200 || created.root.name != "CreateLaunchConfigurationResponse" {
		t.Fatalf("create %d %s", created.status, created.raw)
	}
	listed := h.scaling("DescribeLaunchConfigurations", "LaunchConfigurationNames.member.1", "web").result(t, "DescribeLaunchConfigurations")
	items := listed.get("LaunchConfigurations").all("member")
	if len(items) != 1 || listed.get("NextToken") != nil {
		t.Fatalf("describe %v", listed)
	}
	lc := items[0]
	arn := lc.get("LaunchConfigurationARN").text
	if !strings.HasPrefix(arn, "arn:aws:autoscaling:us-east-1:000000000000:launchConfiguration:") || !strings.HasSuffix(arn, ":launchConfigurationName/web") ||
		lc.get("LaunchConfigurationName").text != "web" || lc.get("ImageId").text != "ami-0123456789abcdef0" || lc.get("InstanceType").text != "t3.micro" ||
		lc.get("CreatedTime") == nil || lc.get("InstanceMonitoring", "Enabled").text != "false" || lc.get("EbsOptimized").text != "true" ||
		len(lc.get("SecurityGroups").all("member")) != 2 || len(lc.get("BlockDeviceMappings").all("member")) != 4 || lc.get("UserData").text != userData ||
		lc.get("PlacementTenancy").text != "dedicated" || lc.get("MetadataOptions", "HttpTokens").text != "required" {
		t.Fatalf("launch configuration %s", h.scaling("DescribeLaunchConfigurations").raw)
	}

	// Defaults are the documented ones.
	h.scaling("CreateLaunchConfiguration", launchPairs("plain")...).result(t, "CreateLaunchConfiguration")
	plain := h.scaling("DescribeLaunchConfigurations", "LaunchConfigurationNames.member.1", "plain").result(t, "DescribeLaunchConfigurations").get("LaunchConfigurations").all("member")[0]
	if plain.get("InstanceMonitoring", "Enabled").text != "true" || plain.get("EbsOptimized").text != "false" || len(plain.get("SecurityGroups").kids) != 0 {
		t.Fatalf("defaults %s", h.scaling("DescribeLaunchConfigurations").raw)
	}
	all := h.scaling("DescribeLaunchConfigurations").result(t, "DescribeLaunchConfigurations").get("LaunchConfigurations").all("member")
	if len(all) != 2 {
		t.Fatalf("describe all %d", len(all))
	}

	expectFault(t, h.scaling("CreateLaunchConfiguration", launchPairs("web")...), 400, "AlreadyExists")
	expectFault(t, h.scaling("DescribeLaunchConfigurations", "LaunchConfigurationNames.member.1", "missing"), 400, "InvalidParameterValue")
	expectFault(t, h.scaling("DeleteLaunchConfiguration", "LaunchConfigurationName", "missing"), 400, "InvalidParameterValue")
	h.scaling("DeleteLaunchConfiguration", "LaunchConfigurationName", "web").result(t, "DeleteLaunchConfiguration")
	expectFault(t, h.scaling("DeleteLaunchConfiguration", "LaunchConfigurationName", "web"), 400, "InvalidParameterValue")
	if left := h.scaling("DescribeLaunchConfigurations").result(t, "DescribeLaunchConfigurations").get("LaunchConfigurations").all("member"); len(left) != 1 {
		t.Fatalf("after delete %d", len(left))
	}
}

func TestAutoScalingDescribePagesFiftyByDefault(t *testing.T) {
	h := newHarness(t, autoScaling)
	for i := 0; i < 60; i++ {
		h.scaling("CreateLaunchConfiguration", launchPairs(fmt.Sprintf("lc-%02d", i))...).result(t, "CreateLaunchConfiguration")
	}
	first := h.scaling("DescribeLaunchConfigurations").result(t, "DescribeLaunchConfigurations")
	token := first.get("NextToken")
	if len(first.get("LaunchConfigurations").all("member")) != 50 || token == nil {
		t.Fatalf("first page %d", len(first.get("LaunchConfigurations").all("member")))
	}
	second := h.scaling("DescribeLaunchConfigurations", "NextToken", token.text).result(t, "DescribeLaunchConfigurations")
	if len(second.get("LaunchConfigurations").all("member")) != 10 || second.get("NextToken") != nil {
		t.Fatalf("second page %v", second)
	}
	small := h.scaling("DescribeLaunchConfigurations", "MaxRecords", "5").result(t, "DescribeLaunchConfigurations")
	if len(small.get("LaunchConfigurations").all("member")) != 5 || small.get("NextToken") == nil {
		t.Fatalf("small page %v", small)
	}
	expectFault(t, h.scaling("DescribeLaunchConfigurations", "NextToken", "nonsense"), 400, "InvalidNextToken")
	expectFault(t, h.scaling("DescribeLaunchConfigurations", "MaxRecords", "0"), 400, "InvalidParameterValue")
	expectFault(t, h.scaling("DescribeLaunchConfigurations", "MaxRecords", "101"), 400, "InvalidParameterValue")
	var names []string
	for i := 1; i <= 51; i++ {
		names = append(names, fmt.Sprintf("LaunchConfigurationNames.member.%d", i), "lc-00")
	}
	expectFault(t, h.scaling("DescribeLaunchConfigurations", names...), 400, "InvalidParameterValue")
}

func TestAutoScalingRefusesWhatItCannotStoreHonestly(t *testing.T) {
	h := newHarness(t, autoScaling)
	create := func(extra ...string) queryReply {
		return h.scaling("CreateLaunchConfiguration", launchPairs("probe", extra...)...)
	}
	mapping := func(pairs ...string) queryReply {
		var extra []string
		for i := 0; i+1 < len(pairs); i += 2 {
			extra = append(extra, "BlockDeviceMappings.member.1."+pairs[i], pairs[i+1])
		}
		return create(extra...)
	}
	expectFault(t, h.scaling("CreateLaunchConfiguration", "LaunchConfigurationName", "from-instance", "InstanceId", "i-0123456789abcdef0"), 400, "InvalidParameterValue")
	expectFault(t, h.scaling("CreateLaunchConfiguration", "LaunchConfigurationName", "no-image", "InstanceType", "t3.micro"), 400, "InvalidParameterValue")
	expectFault(t, h.scaling("CreateLaunchConfiguration", "LaunchConfigurationName", "no-type", "ImageId", "ami-1"), 400, "InvalidParameterValue")
	expectFault(t, create("PlacementTenancy", "host"), 400, "InvalidParameterValue")
	expectFault(t, create("SpotPrice", "free"), 400, "InvalidParameterValue")
	expectFault(t, create("SpotPrice", "0.0001"), 400, "InvalidParameterValue")
	expectFault(t, create("UserData", "not base64!"), 400, "InvalidParameterValue")
	expectFault(t, create("UserData", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 16*1024+1)))), 400, "InvalidParameterValue")
	expectFault(t, create("Unknown", "x"), 400, "InvalidParameterValue")
	expectFault(t, mapping("DeviceName", "/dev/sdb"), 400, "InvalidParameterValue")
	expectFault(t, mapping("DeviceName", "/dev/sdb", "VirtualName", "ephemeral0", "NoDevice", "true"), 400, "InvalidParameterValue")
	expectFault(t, mapping("DeviceName", "/dev/sdb", "VirtualName", "swap"), 400, "InvalidParameterValue")
	expectFault(t, mapping("DeviceName", "/dev/sdb", "Ebs.VolumeType", "gp3"), 400, "InvalidParameterValue")
	expectFault(t, mapping("DeviceName", "/dev/sdb", "Ebs.VolumeSize", "8", "Ebs.VolumeType", "tape"), 400, "InvalidParameterValue")
	expectFault(t, mapping("DeviceName", "/dev/sdb", "Ebs.VolumeSize", "1025", "Ebs.VolumeType", "standard"), 400, "InvalidParameterValue")
	expectFault(t, mapping("DeviceName", "/dev/sdb", "Ebs.VolumeSize", "3", "Ebs.VolumeType", "io1", "Ebs.Iops", "100"), 400, "InvalidParameterValue")
	expectFault(t, mapping("DeviceName", "/dev/sdb", "Ebs.VolumeSize", "8", "Ebs.VolumeType", "io1"), 400, "InvalidParameterValue")
	expectFault(t, mapping("DeviceName", "/dev/sdb", "Ebs.VolumeSize", "8", "Ebs.VolumeType", "gp2", "Ebs.Iops", "300"), 400, "InvalidParameterValue")
	expectFault(t, mapping("DeviceName", "/dev/sdb", "Ebs.VolumeSize", "8", "Ebs.VolumeType", "gp3", "Ebs.Iops", "2999"), 400, "InvalidParameterValue")
	expectFault(t, mapping("DeviceName", "/dev/sdb", "Ebs.VolumeSize", "8", "Ebs.VolumeType", "io1", "Ebs.Iops", "64001"), 400, "InvalidParameterValue")
	expectFault(t, mapping("DeviceName", "/dev/sdb", "Ebs.VolumeSize", "8", "Ebs.VolumeType", "gp2", "Ebs.Throughput", "125"), 400, "InvalidParameterValue")
	// Typed volumes inside the documented ranges are stored.
	mapping("DeviceName", "/dev/sdb", "Ebs.VolumeSize", "125", "Ebs.VolumeType", "st1").result(t, "CreateLaunchConfiguration")
	h.scaling("DeleteLaunchConfiguration", "LaunchConfigurationName", "probe").result(t, "DeleteLaunchConfiguration")
	mapping("DeviceName", "/dev/sdb", "Ebs.VolumeSize", "8", "Ebs.Iops", "100").result(t, "CreateLaunchConfiguration")
	h.scaling("DeleteLaunchConfiguration", "LaunchConfigurationName", "probe").result(t, "DeleteLaunchConfiguration")
	mapping("DeviceName", "/dev/sdb", "Ebs.VolumeSize", "8", "Ebs.VolumeType", "io1", "Ebs.Iops", "1000", "Ebs.Encrypted", "true", "Ebs.DeleteOnTermination", "false").result(t, "CreateLaunchConfiguration")

	// Operations that launch or scale instances stay unbound.
	for _, action := range []string{"CreateAutoScalingGroup", "PutScalingPolicy", "PutScheduledUpdateGroupAction", "PutLifecycleHook", "CreateOrUpdateTags",
		"DescribeAutoScalingGroups", "DescribeAccountLimits", "SetDesiredCapacity", "StartInstanceRefresh", "PutWarmPool"} {
		expectFault(t, h.scaling(action), 501, "UnknownOperationException")
	}
	// The resource limit is local and per Region.
	h2 := newHarness(t, autoScaling)
	for i := 0; i < 200; i++ {
		h2.scaling("CreateLaunchConfiguration", launchPairs(fmt.Sprintf("lc-%03d", i))...).result(t, "CreateLaunchConfiguration")
	}
	expectFault(t, h2.scaling("CreateLaunchConfiguration", launchPairs("over")...), 400, "LimitExceeded")
	h2.policy.deny["autoscaling:CreateLaunchConfiguration"] = true
	expectFault(t, h2.scaling("CreateLaunchConfiguration", launchPairs("denied")...), 403, "AccessDenied")
	h2.policy.deny["autoscaling:DescribeLaunchConfigurations"] = true
	expectFault(t, h2.scaling("DescribeLaunchConfigurations"), 403, "AccessDenied")
}

func TestBeanstalkApplicationLifecycleMatchesOfficialWire(t *testing.T) {
	h := newHarness(t, elasticBeanstalk)
	role := "arn:aws:iam::000000000000:role/beanstalk-service"
	created := h.beanstalk("CreateApplication", "ApplicationName", "shop", "Description", "storefront",
		"ResourceLifecycleConfig.ServiceRole", role, "ResourceLifecycleConfig.VersionLifecycleConfig.MaxCountRule.Enabled", "true",
		"ResourceLifecycleConfig.VersionLifecycleConfig.MaxCountRule.MaxCount", "5", "Tags.member.1.Key", "team", "Tags.member.1.Value", "a")
	if created.root.attrs["xmlns"] != "http://elasticbeanstalk.amazonaws.com/docs/2010-12-01/" {
		t.Fatalf("namespace %s", created.raw)
	}
	app := created.result(t, "CreateApplication").get("Application")
	appARN := "arn:aws:elasticbeanstalk:us-east-1:000000000000:application/shop"
	if app.get("ApplicationName").text != "shop" || app.get("ApplicationArn").text != appARN || app.get("Description").text != "storefront" ||
		app.get("DateCreated") == nil || app.get("DateUpdated") == nil || app.get("ResourceLifecycleConfig", "ServiceRole").text != role ||
		app.get("ResourceLifecycleConfig", "VersionLifecycleConfig", "MaxCountRule", "MaxCount").text != "5" || app.get("Versions") != nil {
		t.Fatalf("application %s", created.raw)
	}
	expectFault(t, h.beanstalk("CreateApplication", "ApplicationName", "shop"), 400, "InvalidParameterValue")
	expectFault(t, h.beanstalk("CreateApplication", "ApplicationName", "a/b"), 400, "InvalidParameterValue")
	expectFault(t, h.beanstalk("CreateApplication", "ApplicationName", "norole", "ResourceLifecycleConfig.VersionLifecycleConfig.MaxAgeRule.Enabled", "true"), 400, "InvalidParameterValue")
	expectFault(t, h.beanstalk("CreateApplication", "ApplicationName", "badrole", "ResourceLifecycleConfig.ServiceRole", "role"), 400, "InvalidParameterValue")
	h.beanstalk("CreateApplication", "ApplicationName", "blog").result(t, "CreateApplication")

	listed := h.beanstalk("DescribeApplications").result(t, "DescribeApplications").get("Applications").all("member")
	if len(listed) != 2 {
		t.Fatalf("describe %d", len(listed))
	}
	one := h.beanstalk("DescribeApplications", "ApplicationNames.member.1", "blog").result(t, "DescribeApplications").get("Applications").all("member")
	if len(one) != 1 || one[0].get("ApplicationName").text != "blog" {
		t.Fatalf("describe by name %v", one)
	}
	expectFault(t, h.beanstalk("DescribeApplications", "ApplicationNames.member.1", "missing"), 400, "InvalidParameterValue")

	updated := h.beanstalk("UpdateApplication", "ApplicationName", "blog", "Description", "writing").result(t, "UpdateApplication").get("Application")
	if updated.get("Description").text != "writing" {
		t.Fatalf("update %v", updated)
	}
	expectFault(t, h.beanstalk("UpdateApplication", "ApplicationName", "missing", "Description", "x"), 400, "InvalidParameterValue")
	h.now = h.now.Add(1)
	lifecycle := h.beanstalk("UpdateApplicationResourceLifecycle", "ApplicationName", "blog", "ResourceLifecycleConfig.ServiceRole", role,
		"ResourceLifecycleConfig.VersionLifecycleConfig.MaxAgeRule.Enabled", "true", "ResourceLifecycleConfig.VersionLifecycleConfig.MaxAgeRule.MaxAgeInDays", "30").
		result(t, "UpdateApplicationResourceLifecycle")
	if lifecycle.get("ApplicationName").text != "blog" || lifecycle.get("ResourceLifecycleConfig", "VersionLifecycleConfig", "MaxAgeRule", "MaxAgeInDays").text != "30" {
		t.Fatalf("lifecycle %v", lifecycle)
	}
	expectFault(t, h.beanstalk("UpdateApplicationResourceLifecycle", "ApplicationName", "blog", "ResourceLifecycleConfig.VersionLifecycleConfig.MaxAgeRule.Enabled", "false"), 400, "InvalidParameterValue")
	expectFault(t, h.beanstalk("UpdateApplicationResourceLifecycle", "ApplicationName", "missing", "ResourceLifecycleConfig.ServiceRole", role), 400, "InvalidParameterValue")

	// Versions belong to an application and carry no source.
	version := h.beanstalk("CreateApplicationVersion", "ApplicationName", "shop", "VersionLabel", "v1", "Description", "first",
		"Tags.member.1.Key", "stage", "Tags.member.1.Value", "dev").result(t, "CreateApplicationVersion").get("ApplicationVersion")
	versionARN := "arn:aws:elasticbeanstalk:us-east-1:000000000000:applicationversion/shop/v1"
	if version.get("ApplicationName").text != "shop" || version.get("VersionLabel").text != "v1" || version.get("ApplicationVersionArn").text != versionARN ||
		version.get("Description").text != "first" || version.get("DateCreated") == nil || version.get("SourceBundle") != nil || version.get("Status") != nil {
		t.Fatalf("version %v", version)
	}
	h.beanstalk("CreateApplicationVersion", "ApplicationName", "shop", "VersionLabel", "v2", "Process", "false", "AutoCreateApplication", "false").result(t, "CreateApplicationVersion")
	h.beanstalk("CreateApplicationVersion", "ApplicationName", "blog", "VersionLabel", "v1").result(t, "CreateApplicationVersion")
	expectFault(t, h.beanstalk("CreateApplicationVersion", "ApplicationName", "shop", "VersionLabel", "v1"), 400, "InvalidParameterValue")
	expectFault(t, h.beanstalk("CreateApplicationVersion", "ApplicationName", "missing", "VersionLabel", "v1"), 400, "InvalidParameterValue")
	expectFault(t, h.beanstalk("CreateApplicationVersion", "ApplicationName", "shop", "VersionLabel", "a/b"), 400, "InvalidParameterValue")
	for _, extra := range [][]string{{"SourceBundle.S3Bucket", "b", "SourceBundle.S3Key", "k"},
		{"SourceBuildInformation.SourceType", "Git", "SourceBuildInformation.SourceRepository", "CodeCommit", "SourceBuildInformation.SourceLocation", "repo/abc"},
		{"BuildConfiguration.CodeBuildServiceRole", role, "BuildConfiguration.Image", "i", "BuildConfiguration.ArtifactName", "a"}, {"Process", "true"}, {"AutoCreateApplication", "true"}} {
		expectFault(t, h.beanstalk("CreateApplicationVersion", append([]string{"ApplicationName", "shop", "VersionLabel", "v3"}, extra...)...), 400, "InvalidParameterValue")
	}

	versions := h.beanstalk("DescribeApplicationVersions", "ApplicationName", "shop").result(t, "DescribeApplicationVersions")
	if len(versions.get("ApplicationVersions").all("member")) != 2 || versions.get("NextToken") != nil {
		t.Fatalf("versions %v", versions)
	}
	page := h.beanstalk("DescribeApplicationVersions", "ApplicationName", "shop", "MaxRecords", "1").result(t, "DescribeApplicationVersions")
	if len(page.get("ApplicationVersions").all("member")) != 1 || page.get("NextToken") == nil {
		t.Fatalf("page %v", page)
	}
	byLabel := h.beanstalk("DescribeApplicationVersions", "ApplicationName", "shop", "VersionLabels.member.1", "v2").result(t, "DescribeApplicationVersions").get("ApplicationVersions").all("member")
	if len(byLabel) != 1 || byLabel[0].get("VersionLabel").text != "v2" {
		t.Fatalf("by label %v", byLabel)
	}
	expectFault(t, h.beanstalk("DescribeApplicationVersions"), 400, "InvalidParameterValue")
	expectFault(t, h.beanstalk("DescribeApplicationVersions", "ApplicationName", "missing"), 400, "InvalidParameterValue")
	expectFault(t, h.beanstalk("DescribeApplicationVersions", "ApplicationName", "shop", "VersionLabels.member.1", "missing"), 400, "InvalidParameterValue")
	renamed := h.beanstalk("UpdateApplicationVersion", "ApplicationName", "shop", "VersionLabel", "v2", "Description", "second").result(t, "UpdateApplicationVersion").get("ApplicationVersion")
	if renamed.get("Description").text != "second" {
		t.Fatalf("update version %v", renamed)
	}
	expectFault(t, h.beanstalk("UpdateApplicationVersion", "ApplicationName", "shop", "VersionLabel", "missing", "Description", "x"), 400, "InvalidParameterValue")

	// Tags are read by ARN.
	listing := h.beanstalk("ListTagsForResource", "ResourceArn", appARN).result(t, "ListTagsForResource")
	if listing.get("ResourceArn") != nil {
		t.Fatalf("the bounded response reports ResourceArn, so the Bounds text is stale: %v", listing)
	}
	tags := listing.get("ResourceTags").all("member")
	if len(tags) != 1 || tags[0].get("Key").text != "team" || tags[0].get("Value").text != "a" {
		t.Fatalf("application tags %v", tags)
	}
	tags = h.beanstalk("ListTagsForResource", "ResourceArn", versionARN).result(t, "ListTagsForResource").get("ResourceTags").all("member")
	if len(tags) != 1 || tags[0].get("Key").text != "stage" {
		t.Fatalf("version tags %v", tags)
	}
	expectFault(t, h.beanstalk("ListTagsForResource", "ResourceArn", "arn:aws:elasticbeanstalk:us-east-1:000000000000:application/missing"), 400, "ResourceNotFoundException")
	expectFault(t, h.beanstalk("ListTagsForResource", "ResourceArn", "not-an-arn"), 400, "ResourceNotFoundException")

	// Deleting a version leaves the application, and deleting the application removes its versions.
	h.beanstalk("DeleteApplicationVersion", "ApplicationName", "shop", "VersionLabel", "v2", "DeleteSourceBundle", "true").result(t, "DeleteApplicationVersion")
	expectFault(t, h.beanstalk("DeleteApplicationVersion", "ApplicationName", "shop", "VersionLabel", "v2"), 400, "InvalidParameterValue")
	expectFault(t, h.beanstalk("DeleteApplicationVersion", "ApplicationName", "missing", "VersionLabel", "v1"), 400, "InvalidParameterValue")
	h.beanstalk("DeleteApplication", "ApplicationName", "shop", "TerminateEnvByForce", "true").result(t, "DeleteApplication")
	expectFault(t, h.beanstalk("DeleteApplication", "ApplicationName", "shop"), 400, "InvalidParameterValue")
	expectFault(t, h.beanstalk("ListTagsForResource", "ResourceArn", versionARN), 400, "ResourceNotFoundException")
	h.beanstalk("CreateApplication", "ApplicationName", "shop").result(t, "CreateApplication")
	if again := h.beanstalk("DescribeApplicationVersions", "ApplicationName", "shop").result(t, "DescribeApplicationVersions").get("ApplicationVersions"); len(again.kids) != 0 {
		t.Fatalf("a recreated application inherited versions: %v", again)
	}
	if kept := h.beanstalk("DescribeApplicationVersions", "ApplicationName", "blog").result(t, "DescribeApplicationVersions").get("ApplicationVersions").all("member"); len(kept) != 1 {
		t.Fatalf("another application lost its version: %d", len(kept))
	}
}

func TestBeanstalkRefusesWhatItCannotStoreHonestly(t *testing.T) {
	h := newHarness(t, elasticBeanstalk)
	for _, action := range []string{"CreateEnvironment", "CreateConfigurationTemplate", "CreatePlatformVersion", "DescribeEnvironments", "DescribeEvents",
		"ListAvailableSolutionStacks", "UpdateTagsForResource", "CheckDNSAvailability", "DescribeAccountAttributes", "TerminateEnvironment"} {
		expectFault(t, h.beanstalk(action), 501, "UnknownOperationException")
	}
	var many []string
	for i := 1; i <= 51; i++ {
		many = append(many, fmt.Sprintf("Tags.member.%d.Key", i), fmt.Sprintf("k%d", i), fmt.Sprintf("Tags.member.%d.Value", i), "v")
	}
	expectFault(t, h.beanstalk("CreateApplication", append([]string{"ApplicationName", "many"}, many...)...), 400, "InvalidParameterValue")
	for i := 0; i < 75; i++ {
		h.beanstalk("CreateApplication", "ApplicationName", fmt.Sprintf("app-%02d", i)).result(t, "CreateApplication")
	}
	expectFault(t, h.beanstalk("CreateApplication", "ApplicationName", "over"), 400, "InvalidParameterValue")
	for i := 0; i < 100; i++ {
		h.beanstalk("CreateApplicationVersion", "ApplicationName", "app-00", "VersionLabel", fmt.Sprintf("v%03d", i)).result(t, "CreateApplicationVersion")
	}
	expectFault(t, h.beanstalk("CreateApplicationVersion", "ApplicationName", "app-00", "VersionLabel", "over"), 400, "InvalidParameterValue")
	if all := h.beanstalk("DescribeApplicationVersions", "ApplicationName", "app-00").result(t, "DescribeApplicationVersions").get("ApplicationVersions").all("member"); len(all) != 100 {
		t.Fatalf("a single page holds %d versions", len(all))
	}
	h.policy.deny["elasticbeanstalk:CreateApplication"] = true
	expectFault(t, h.beanstalk("CreateApplication", "ApplicationName", "denied"), 403, "AccessDenied")
	h.policy.deny["elasticbeanstalk:DescribeApplicationVersions"] = true
	expectFault(t, h.beanstalk("DescribeApplicationVersions", "ApplicationName", "app-00"), 403, "AccessDenied")
}

// Each service is routed by its own credential-scope name.
func TestComputeQueryServicesRouteByCredentialScope(t *testing.T) {
	h := newHarness(t, autoScaling, elasticBeanstalk, rds)
	h.scaling("CreateLaunchConfiguration", launchPairs("shared")...).result(t, "CreateLaunchConfiguration")
	h.beanstalk("CreateApplication", "ApplicationName", "shared").result(t, "CreateApplication")
	expectFault(t, h.call("autoscaling", "2011-01-01", "CreateApplication", "ApplicationName", "x"), 400, "InvalidAction")
	expectFault(t, h.call("elasticbeanstalk", "2010-12-01", "CreateLaunchConfiguration"), 400, "InvalidAction")
}
