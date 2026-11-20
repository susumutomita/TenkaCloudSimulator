package catalog

import (
	"encoding/base64"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Query-protocol compute services whose bound resources are stored
// configuration only: Amazon EC2 Auto Scaling launch configurations and AWS
// Elastic Beanstalk applications and application versions. Nothing that
// launches an instance, builds a bundle or provisions an environment is bound.

var (
	// autoScalingEphemeral is the documented "ephemeralX where X is a number
	// starting from zero" instance store name.
	autoScalingEphemeral = regexp.MustCompile(`^ephemeral[0-9]+$`)
	// beanstalkRole is the documented "ARN of an IAM service role".
	beanstalkRole = regexp.MustCompile(`^arn:aws:iam::[0-9]{12}:role/.+$`)

	autoScalingVolumeTypes = []string{"standard", "io1", "gp2", "st1", "sc1", "gp3"}
	// autoScalingVolumeSizes are the documented supported sizes in GiB.
	autoScalingVolumeSizes = map[string][2]int64{
		"gp2": {1, 16384}, "gp3": {1, 16384}, "io1": {4, 16384}, "st1": {125, 16384}, "sc1": {125, 16384}, "standard": {1, 1024},
	}
	// autoScalingIops are the documented supported IOPS per volume type.
	autoScalingIops = map[string][2]int64{"gp3": {3000, 16000}, "io1": {100, 64000}}
)

// autoScaling serves launch configurations over the Query protocol. No Auto
// Scaling group exists, so nothing is launched, scaled or replaced.
var autoScaling = &Service{
	Model: "autoscaling", Title: "Amazon EC2 Auto Scaling", IAMPrefix: "autoscaling", Issues: []int{190},
	Resources: []*Resource{
		{Kind: "launchConfiguration", Shape: "LaunchConfiguration", ID: "uuid",
			ARN:       "arn:{partition}:autoscaling:{region}:{account}:launchConfiguration:{id}:launchConfigurationName/{name}",
			ARNMember: "LaunchConfigurationARN", NameMember: "LaunchConfigurationName", Unique: true, Limit: 200,
			Generate: map[string]any{"CreatedTime": "$now", "InstanceMonitoring": map[string]any{"Enabled": true}, "EbsOptimized": false,
				"SecurityGroups": []any{}, "ClassicLinkVPCSecurityGroups": []any{}, "BlockDeviceMappings": []any{}}},
	},
	Ops: []Op{
		{Name: "CreateLaunchConfiguration", Kind: Create, Resource: "launchConfiguration", Check: autoScalingCreate},
		{Name: "DescribeLaunchConfigurations", Kind: List, Resource: "launchConfiguration", Names: "LaunchConfigurationNames", Check: autoScalingDescribe},
		{Name: "DeleteLaunchConfiguration", Kind: Delete, Resource: "launchConfiguration", Lookup: "LaunchConfigurationName"},
	},
	Bounds: []string{
		"Launch configurations are stored configuration served over the Query protocol: image IDs, instance types, key pairs, security groups, instance profiles, kernel and RAM disk IDs and block device mappings are stored as given and never resolved against EC2, EBS or IAM, and no instance is ever launched. No Auto Scaling group exists, so CreateAutoScalingGroup, scaling policies, scheduled actions, lifecycle hooks, notification configurations, warm pools, instance refreshes, traffic sources, tags (which apply to groups), metrics collection and every other official operation return UnknownOperationException with HTTP 501.",
		"A launch configuration must name ImageId and InstanceType: InstanceId, which derives them from a running instance, is refused. PlacementTenancy must be default or dedicated, SpotPrice at least 0.001, and UserData valid base64 of at most 16 KB, as documented. Block device mappings must set the device name and exactly one of Ebs, NoDevice or VirtualName (ephemeralN), an Ebs volume needs SnapshotId or VolumeSize, the volume type one of standard, io1, gp2, st1, sc1 or gp3 with the documented size and Iops ranges (Iops required for io1, accepted only for io1 and gp3), and Throughput only for gp3. Whether a snapshot, key pair, security group or instance profile exists is not checked, and an unset InstanceMonitoring reports detailed monitoring enabled as documented.",
		"Launch configuration names are 1 to 255 characters and unique per Region; at most 200 may exist, a local limit that fails with LimitExceeded. Describe returns 50 launch configurations per page by default and accepts MaxRecords from 1 to 100 and at most 50 names. The official operation silently skips a name that does not exist; the simulator instead fails the request, with InvalidParameterValue because the model has no not-found fault, and DeleteLaunchConfiguration of an unknown name fails the same way. ResourceInUse cannot occur because no group can use a launch configuration.",
		"Failures the official model does not define (invalid input, unknown name) use the common Query code InvalidParameterValue with HTTP 400; the real service reports ValidationError for these. The launch configuration ARN has the documented form arn:aws:autoscaling:region:account:launchConfiguration:uuid:launchConfigurationName/name, and ClassicLink members are stored without any EC2-Classic behavior.",
	},
}

func autoScalingCreate(input map[string]any) error {
	if _, ok := input["InstanceId"]; ok {
		return invalid("InstanceId is not supported: specify ImageId and InstanceType instead.")
	}
	for _, member := range []string{"ImageId", "InstanceType"} {
		if _, ok := input[member]; !ok {
			return invalid("%s is required when InstanceId is not specified.", member)
		}
	}
	if tenancy, ok := input["PlacementTenancy"].(string); ok && tenancy != "default" && tenancy != "dedicated" {
		return invalid("PlacementTenancy must be default or dedicated.")
	}
	if price, ok := input["SpotPrice"].(string); ok {
		if value, err := strconv.ParseFloat(price, 64); err != nil || value < 0.001 {
			return invalid("SpotPrice must be a number of at least 0.001.")
		}
	}
	if data, ok := input["UserData"].(string); ok {
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil || len(decoded) > 16*1024 {
			return invalid("UserData must be base64-encoded text of at most 16 KB.")
		}
	}
	mappings, _ := input["BlockDeviceMappings"].([]any)
	for _, value := range mappings {
		if err := autoScalingMapping(value.(map[string]any)); err != nil {
			return err
		}
	}
	return nil
}

// autoScalingMapping applies the documented block device mapping rules.
func autoScalingMapping(mapping map[string]any) error {
	set := 0
	for _, member := range []string{"Ebs", "NoDevice", "VirtualName"} {
		if _, ok := mapping[member]; ok {
			set++
		}
	}
	if set != 1 {
		return invalid("A block device mapping sets the device name and exactly one of Ebs, NoDevice or VirtualName.")
	}
	if name, ok := mapping["VirtualName"].(string); ok && !autoScalingEphemeral.MatchString(name) {
		return invalid("VirtualName must be ephemeralX, where X is a number starting from zero.")
	}
	ebs, ok := mapping["Ebs"].(map[string]any)
	if !ok {
		return nil
	}
	_, size := ebs["VolumeSize"]
	if _, snapshot := ebs["SnapshotId"]; !size && !snapshot {
		return invalid("Specify either a VolumeSize or a SnapshotId.")
	}
	kind, _ := ebs["VolumeType"].(string)
	if _, typed := ebs["VolumeType"]; typed && !slices.Contains(autoScalingVolumeTypes, kind) {
		return invalid("VolumeType must be one of %s.", strings.Join(autoScalingVolumeTypes, ", "))
	}
	if n, ok := ebs["VolumeSize"].(int64); ok && kind != "" {
		if bounds := autoScalingVolumeSizes[kind]; n < bounds[0] || n > bounds[1] {
			return invalid("A %s volume is %d to %d GiB.", kind, bounds[0], bounds[1])
		}
	}
	iops, hasIops := ebs["Iops"].(int64)
	if _, typed := ebs["VolumeType"]; typed && hasIops && kind != "io1" && kind != "gp3" {
		return invalid("Iops is supported only for io1 and gp3 volumes.")
	}
	if kind == "io1" && !hasIops {
		return invalid("Iops is required for io1 volumes.")
	}
	if bounds, ranged := autoScalingIops[kind]; ranged && hasIops && (iops < bounds[0] || iops > bounds[1]) {
		return invalid("A %s volume provisions %d to %d IOPS.", kind, bounds[0], bounds[1])
	}
	if _, throughput := ebs["Throughput"]; throughput && kind != "" && kind != "gp3" {
		return invalid("Throughput is supported only for gp3 volumes.")
	}
	return nil
}

func autoScalingDescribe(input map[string]any) error {
	if names, _ := input["LaunchConfigurationNames"].([]any); len(names) > 50 {
		return invalid("At most 50 launch configuration names may be specified.")
	}
	if n, ok := input["MaxRecords"].(int64); ok {
		if n < 1 || n > 100 {
			return invalid("MaxRecords must be from 1 to 100.")
		}
		return nil
	}
	input["MaxRecords"] = int64(50)
	return nil
}

// elasticBeanstalk serves applications and application versions over the
// Query protocol. No environment, configuration template or platform exists.
var elasticBeanstalk = &Service{
	Model: "elasticbeanstalk", Title: "AWS Elastic Beanstalk", IAMPrefix: "elasticbeanstalk", Issues: []int{107},
	Resources: []*Resource{
		{Kind: "application", Shape: "ApplicationDescription", ID: "name", ARN: "arn:{partition}:elasticbeanstalk:{region}:{account}:application/{name}",
			ARNMember: "ApplicationArn", NameMember: "ApplicationName", Unique: true, Limit: 75, Cascade: true,
			Generate: map[string]any{"DateCreated": "$now", "DateUpdated": "$now"}},
		{Kind: "applicationVersion", Shape: "ApplicationVersionDescription", Parent: "application", ParentMember: "ApplicationName", ID: "name",
			ARN:       "arn:{partition}:elasticbeanstalk:{region}:{account}:applicationversion/{parentName}/{name}",
			ARNMember: "ApplicationVersionArn", NameMember: "VersionLabel", Unique: true, Limit: 100,
			Generate: map[string]any{"ApplicationName": "$parentName", "DateCreated": "$now", "DateUpdated": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateApplication", Kind: Create, Resource: "application", Check: beanstalkCreateApplication},
		{Name: "DescribeApplications", Kind: List, Resource: "application", Names: "ApplicationNames"},
		{Name: "UpdateApplication", Kind: Update, Resource: "application", Lookup: "ApplicationName", Fields: []string{"Description"}, Touch: []string{"DateUpdated"}},
		{Name: "UpdateApplicationResourceLifecycle", Kind: Update, Resource: "application", Lookup: "ApplicationName",
			Fields: []string{"ResourceLifecycleConfig"}, Touch: []string{"DateUpdated"}, Check: beanstalkConfig},
		{Name: "DeleteApplication", Kind: Delete, Resource: "application", Lookup: "ApplicationName", Accept: []string{"TerminateEnvByForce"}},
		{Name: "CreateApplicationVersion", Kind: Create, Resource: "applicationVersion", Parent: "ApplicationName", Check: beanstalkCreateVersion},
		{Name: "DescribeApplicationVersions", Kind: List, Resource: "applicationVersion", Parent: "ApplicationName", Names: "VersionLabels", Check: beanstalkDescribeVersions},
		{Name: "UpdateApplicationVersion", Kind: Update, Resource: "applicationVersion", Parent: "ApplicationName", Lookup: "VersionLabel",
			Fields: []string{"Description"}, Touch: []string{"DateUpdated"}},
		{Name: "DeleteApplicationVersion", Kind: Delete, Resource: "applicationVersion", Parent: "ApplicationName", Lookup: "VersionLabel", Accept: []string{"DeleteSourceBundle"}},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Applications and application versions are stored configuration served over the Query protocol: no environment, configuration template, platform version, instance, load balancer, managed action, event or health record exists, and CreateEnvironment, CreateConfigurationTemplate, CreatePlatformVersion, DescribeEnvironments, DescribeEvents, ListAvailableSolutionStacks and every other official operation return UnknownOperationException with HTTP 501. Nothing is deployed.",
		"An application version is created without a source: SourceBundle, SourceBuildInformation, BuildConfiguration, Process=true and AutoCreateApplication=true are refused, because the simulator never reads, builds or validates an S3, CodeCommit or CodeBuild source, and the version is the sample application the official operation documents for a request without a source. Versions report no Status, SourceBundle or BuildArn. DeleteApplicationVersion accepts DeleteSourceBundle because no bundle exists to delete.",
		"DescribeApplications reports no Versions or ConfigurationTemplates; read versions with DescribeApplicationVersions, which requires ApplicationName (the official operation also lists every application's versions). An unknown name in ApplicationNames, VersionLabels or a lookup fails with InvalidParameterValue, as UpdateApplication documents, instead of being skipped. Application names and version labels are 1 to 100 characters and must not contain a forward slash, which the ARNs arn:aws:elasticbeanstalk:region:account:application/name and applicationversion/application/label would make ambiguous. DeleteApplication removes its versions, accepts TerminateEnvByForce because no environment exists, and does not delete anything from S3.",
		"ResourceLifecycleConfig is stored and returned but never applied: no version is deleted by a MaxCountRule or MaxAgeRule, the service role is checked only for the form arn:aws:iam::account:role/name and is never assumed, and rule members are validated only against the official shape. ServiceRole must accompany every VersionLifecycleConfig, in CreateApplication and in each UpdateApplicationResourceLifecycle call, where the official service persists it after the first one.",
		"Tags are accepted at CreateApplication and CreateApplicationVersion and read with ListTagsForResource by the ARN of an application or an application version; the response omits ResourceArn. UpdateTagsForResource is not implemented because one request both adds and removes tags, which the catalog's tag operations cannot express, so tags cannot change after creation. An unknown ARN fails with ResourceNotFoundException. Applications are limited to 75 and versions to 100 per application locally; exceeding them, like more than 50 tags in one request or a name that already exists, fails with InvalidParameterValue because TooManyApplicationsException, TooManyApplicationVersionsException and TooManyTagsException are not selectable for those faults.",
	},
}

// beanstalkName refuses a name or label the ARN form cannot carry.
func beanstalkName(member string, input map[string]any) error {
	if name, ok := input[member].(string); ok && strings.Contains(name, "/") {
		return invalid("%s must not contain a forward slash.", member)
	}
	return nil
}

// beanstalkConfig checks a resource lifecycle configuration: a version
// lifecycle needs the service role that would apply it.
func beanstalkConfig(input map[string]any) error {
	config, ok := input["ResourceLifecycleConfig"].(map[string]any)
	if !ok {
		return nil
	}
	role, hasRole := config["ServiceRole"].(string)
	if hasRole && !beanstalkRole.MatchString(role) {
		return invalid("ServiceRole must be the ARN of an IAM role.")
	}
	if _, versions := config["VersionLifecycleConfig"]; versions && !hasRole {
		return invalid("ServiceRole is required with VersionLifecycleConfig.")
	}
	return nil
}

func beanstalkCreateApplication(input map[string]any) error {
	if err := beanstalkName("ApplicationName", input); err != nil {
		return err
	}
	return beanstalkConfig(input)
}

func beanstalkCreateVersion(input map[string]any) error {
	if err := beanstalkName("VersionLabel", input); err != nil {
		return err
	}
	for _, member := range []string{"SourceBundle", "SourceBuildInformation", "BuildConfiguration"} {
		if _, ok := input[member]; ok {
			return invalid("%s is not supported: the simulator never reads or builds a source.", member)
		}
	}
	for _, member := range []string{"Process", "AutoCreateApplication"} {
		if value, ok := input[member].(bool); ok {
			if value {
				return invalid("%s=true is not supported by the simulator.", member)
			}
			delete(input, member)
		}
	}
	return nil
}

func beanstalkDescribeVersions(input map[string]any) error {
	if _, ok := input["ApplicationName"]; !ok {
		return invalid("ApplicationName is required by the simulator.")
	}
	return nil
}
