package catalog

import (
	"net/url"
	"testing"
)

func dataHarness(t *testing.T) *harness {
	return newHarness(t, auroraDSQL, timestreamWrite, memoryDB, fsx, efs, awsBackup, storageGateway, glacier, amazonMQ, firehose, msk, oracleDatabaseAtAWS)
}

func TestDataCheckHooksApplyDocumentedRules(t *testing.T) {
	keyARN := "arn:aws:kms:us-east-1:000000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	s3 := func(extra map[string]any) map[string]any {
		destination := map[string]any{"RoleARN": "arn:aws:iam::000000000000:role/r", "BucketARN": "arn:aws:s3:::b"}
		for k, v := range extra {
			destination[k] = v
		}
		return map[string]any{"ExtendedS3DestinationConfiguration": destination}
	}
	lustre := func(extra map[string]any) map[string]any {
		input := map[string]any{"FileSystemType": "LUSTRE", "StorageCapacity": int64(1200), "SubnetIds": []any{"subnet-0a1b2c3d"}}
		for k, v := range extra {
			input[k] = v
		}
		return input
	}
	retrieval := func(rule map[string]any) map[string]any {
		return map[string]any{"accountId": "-", "Policy": map[string]any{"Rules": []any{rule}}}
	}
	msk := func(brokers int64, subnets ...any) map[string]any {
		return map[string]any{"KafkaVersion": "3.6.0", "NumberOfBrokerNodes": brokers, "BrokerNodeGroupInfo": map[string]any{"ClientSubnets": subnets}}
	}
	cases := []struct {
		name  string
		check func(map[string]any) error
		input map[string]any
		code  string
	}{
		{"dsql unprotected", dsqlCreateCluster, map[string]any{"deletionProtectionEnabled": false}, ""},
		{"dsql default protection", dsqlCreateCluster, map[string]any{}, "ValidationException"},
		{"dsql enable protection", dsqlClusterOptions, map[string]any{"deletionProtectionEnabled": true}, "ValidationException"},
		{"dsql multi-Region", dsqlClusterOptions, map[string]any{"multiRegionProperties": map[string]any{}}, "ValidationException"},
		{"dsql owned key", dsqlClusterOptions, map[string]any{"kmsEncryptionKey": "AWS_OWNED_KMS_KEY"}, ""},
		{"dsql key ARN", dsqlClusterOptions, map[string]any{"kmsEncryptionKey": keyARN}, ""},
		{"dsql key alias", dsqlClusterOptions, map[string]any{"kmsEncryptionKey": "alias/app"}, "ValidationException"},
		{"timestream tables of a database", timestreamListTables, map[string]any{"DatabaseName": "db"}, ""},
		{"timestream tables of the Region", timestreamListTables, map[string]any{}, "ValidationException"},
		{"memorydb description only", memoryDBSubnets, map[string]any{"Description": "d"}, ""},
		{"memorydb default user", memoryDBCreateUser, map[string]any{"UserName": "default"}, "InvalidParameterValueException"},
		{"memorydb iam user", memoryDBCreateUser, map[string]any{"UserName": "app", "AuthenticationMode": map[string]any{"Type": "iam"}}, ""},
		{"memorydb password user", memoryDBAuthentication, map[string]any{"AuthenticationMode": map[string]any{"Type": "password", "Passwords": []any{"p"}}}, "InvalidParameterValueException"},
		{"memorydb iam with passwords", memoryDBAuthentication, map[string]any{"AuthenticationMode": map[string]any{"Type": "iam", "Passwords": []any{"p"}}}, "InvalidParameterValueException"},
		{"memorydb access string only", memoryDBAuthentication, map[string]any{"AccessString": "on ~* +@all"}, ""},
		{"fsx lustre", fsxFileSystem, lustre(nil), ""},
		{"fsx windows", fsxFileSystem, map[string]any{"FileSystemType": "WINDOWS"}, "BadRequest"},
		{"fsx mixed configuration", fsxFileSystem, lustre(map[string]any{"OntapConfiguration": map[string]any{}}), "IncompatibleParameterError"},
		{"fsx no capacity", fsxFileSystem, map[string]any{"FileSystemType": "LUSTRE", "SubnetIds": []any{"subnet-0a1b2c3d"}}, "BadRequest"},
		{"fsx data repository", fsxFileSystem, lustre(map[string]any{"LustreConfiguration": map[string]any{"ImportPath": "s3://b"}}), "BadRequest"},
		{"fsx two subnets", fsxFileSystem, lustre(map[string]any{"SubnetIds": []any{"subnet-0a1b2c3d", "subnet-4e5f6a7b"}}), "BadRequest"},
		{"fsx skip final backup", fsxDeleteFileSystem, map[string]any{"LustreConfiguration": map[string]any{"SkipFinalBackup": true}}, ""},
		{"fsx final backup", fsxDeleteFileSystem, map[string]any{"LustreConfiguration": map[string]any{"SkipFinalBackup": false}}, "BadRequest"},
		{"fsx final backup tags", fsxDeleteFileSystem, map[string]any{"LustreConfiguration": map[string]any{"FinalBackupTags": []any{}}}, "BadRequest"},
		{"fsx cache without configuration", fsxFileCache, map[string]any{"SubnetIds": []any{"subnet-0a1b2c3d"}}, "MissingFileCacheConfiguration"},
		{"fsx security groups", fsxFileCache, map[string]any{"SubnetIds": []any{"subnet-0a1b2c3d"}, "SecurityGroupIds": []any{"sg-0a1b2c3d"}, "LustreConfiguration": map[string]any{}}, "BadRequest"},
		{"fsx cache", fsxFileCache, map[string]any{"SubnetIds": []any{"subnet-0a1b2c3d"}, "LustreConfiguration": map[string]any{}}, ""},
		{"efs encrypted key", efsFileSystem, map[string]any{"KmsKeyId": keyARN, "Encrypted": true}, ""},
		{"efs key without encryption", efsFileSystem, map[string]any{"KmsKeyId": keyARN}, "BadRequest"},
		{"efs throughput without mode", efsFileSystem, map[string]any{"ProvisionedThroughputInMibps": 10.0}, "BadRequest"},
		{"efs provisioned without throughput", efsThroughput, map[string]any{"ThroughputMode": "provisioned"}, "BadRequest"},
		{"efs elastic with throughput", efsThroughput, map[string]any{"ThroughputMode": "elastic", "ProvisionedThroughputInMibps": 10.0}, "BadRequest"},
		{"efs provisioned", efsThroughput, map[string]any{"ThroughputMode": "provisioned", "ProvisionedThroughputInMibps": 10.0}, ""},
		{"efs unfiltered", efsFileSystemFilter, map[string]any{}, ""},
		{"efs long IDs", efsPreferences, map[string]any{"ResourceIdType": "LONG_ID"}, ""},
		{"efs short IDs", efsPreferences, map[string]any{"ResourceIdType": "SHORT_ID"}, "BadRequest"},
		{"efs tag by ARN", efsResourceARN, map[string]any{"ResourceId": "arn:aws:elasticfilesystem:us-east-1:000000000000:file-system/fs-1"}, ""},
		{"efs tag by ID", efsResourceARN, map[string]any{"ResourceId": "fs-1"}, "BadRequest"},
		{"backup template", backupReportPlan, map[string]any{"ReportSetting": map[string]any{"ReportTemplate": "COPY_JOB_REPORT"}}, ""},
		{"backup unknown template", backupReportPlan, map[string]any{"ReportSetting": map[string]any{"ReportTemplate": "X"}}, "InvalidParameterValueException"},
		{"backup formats", backupReportPlan, map[string]any{"ReportDeliveryChannel": map[string]any{"S3BucketName": "b", "Formats": []any{"CSV", "JSON"}}}, ""},
		{"backup unknown format", backupReportPlan, map[string]any{"ReportDeliveryChannel": map[string]any{"S3BucketName": "b", "Formats": []any{"XML"}}}, "InvalidParameterValueException"},
		{"tape pool unlocked", storageGatewayRetention, map[string]any{"RetentionLockType": "NONE"}, ""},
		{"tape pool lock without days", storageGatewayRetention, map[string]any{"RetentionLockType": "COMPLIANCE"}, "InvalidGatewayRequestException"},
		{"tape pool days without lock", storageGatewayRetention, map[string]any{"RetentionLockTimeInDays": int64(5)}, "InvalidGatewayRequestException"},
		{"tape pool governance", storageGatewayRetention, map[string]any{"RetentionLockType": "GOVERNANCE", "RetentionLockTimeInDays": int64(5)}, ""},
		{"glacier own account", glacierAccount, map[string]any{"accountId": "000000000000"}, ""},
		{"glacier other account", glacierAccount, map[string]any{"accountId": "111111111111"}, "InvalidParameterValueException"},
		{"glacier vault name", glacierVaultName, map[string]any{"accountId": "-", "vaultName": "a.b_c-1"}, ""},
		{"glacier bad vault name", glacierVaultName, map[string]any{"accountId": "-", "vaultName": "a b"}, "InvalidParameterValueException"},
		{"glacier list", glacierListVaults, map[string]any{"accountId": "-"}, ""},
		{"glacier list limit", glacierListVaults, map[string]any{"accountId": "-", "limit": "5"}, "InvalidParameterValueException"},
		{"glacier rate policy", glacierRetrievalPolicy, retrieval(map[string]any{"Strategy": "BytesPerHour", "BytesPerHour": int64(1)}), ""},
		{"glacier free tier", glacierRetrievalPolicy, retrieval(map[string]any{"Strategy": "None"}), ""},
		{"glacier no policy", glacierRetrievalPolicy, map[string]any{"accountId": "-"}, "MissingParameterValueException"},
		{"glacier two rules", glacierRetrievalPolicy, map[string]any{"Policy": map[string]any{"Rules": []any{}}}, "InvalidParameterValueException"},
		{"glacier rate without bytes", glacierRetrievalPolicy, retrieval(map[string]any{"Strategy": "BytesPerHour"}), "InvalidParameterValueException"},
		{"glacier free tier bytes", glacierRetrievalPolicy, retrieval(map[string]any{"Strategy": "FreeTier", "BytesPerHour": int64(1)}), "InvalidParameterValueException"},
		{"glacier unknown strategy", glacierRetrievalPolicy, retrieval(map[string]any{"Strategy": "Unlimited"}), "InvalidParameterValueException"},
		{"firehose s3", firehoseCreate, s3(nil), ""},
		{"firehose kinesis source", firehoseCreate, map[string]any{"DeliveryStreamType": "KinesisStreamAsSource"}, "InvalidArgumentException"},
		{"firehose tags", firehoseCreate, map[string]any{"Tags": []any{}}, "InvalidArgumentException"},
		{"firehose no destination", firehoseCreate, map[string]any{}, "InvalidArgumentException"},
		{"firehose lambda processing", firehoseCreate, s3(map[string]any{"ProcessingConfiguration": map[string]any{}}), "InvalidArgumentException"},
		{"firehose s3 backup", firehoseCreate, s3(map[string]any{"S3BackupMode": "Enabled"}), "InvalidArgumentException"},
		{"firehose partial hints", firehoseCreate, s3(map[string]any{"BufferingHints": map[string]any{"SizeInMBs": int64(1)}}), "InvalidArgumentException"},
		{"firehose explicit options", firehoseCreate, s3(map[string]any{"BufferingHints": map[string]any{"SizeInMBs": int64(1), "IntervalInSeconds": int64(60)},
			"CompressionFormat": "GZIP", "EncryptionConfiguration": map[string]any{"KMSEncryptionConfig": map[string]any{"AWSKMSKeyARN": keyARN}}}), ""},
		{"firehose customer key without ARN", firehoseEncryption, map[string]any{"DeliveryStreamEncryptionConfigurationInput": map[string]any{"KeyType": "CUSTOMER_MANAGED_CMK"}}, "InvalidArgumentException"},
		{"firehose owned key with ARN", firehoseEncryption, map[string]any{"DeliveryStreamEncryptionConfigurationInput": map[string]any{"KeyType": "AWS_OWNED_CMK", "KeyARN": keyARN}}, "InvalidArgumentException"},
		{"firehose default key", firehoseEncryption, map[string]any{}, ""},
		{"firehose list", firehoseList, map[string]any{}, ""},
		{"firehose list limit", firehoseList, map[string]any{"Limit": int64(1)}, "ValidationException"},
		{"msk balanced", mskCreateCluster, msk(4, "subnet-a", "subnet-b"), ""},
		{"msk unbalanced", mskCreateCluster, msk(3, "subnet-a", "subnet-b"), "BadRequestException"},
		{"msk no subnets", mskCreateCluster, msk(3), "BadRequestException"},
		{"msk configuration", mskCreateCluster, map[string]any{"ConfigurationInfo": map[string]any{}}, "BadRequestException"},
		{"msk current version", mskCurrentVersion, map[string]any{"CurrentVersion": mskClusterVersion}, ""},
		{"msk stale version", mskCurrentVersion, map[string]any{"CurrentVersion": "K2"}, "BadRequestException"},
		{"msk unversioned delete", mskCurrentVersion, map[string]any{}, ""},
		{"odb zone ID", odbZone, map[string]any{"availabilityZoneId": "use1-az4"}, ""},
		{"odb no zone", odbZone, map[string]any{}, "ValidationException"},
		{"odb network", odbNetwork, map[string]any{"availabilityZone": "us-east-1a", "clientSubnetCidr": "10.0.0.0/24", "backupSubnetCidr": "10.0.1.0/24"}, ""},
		{"odb network without backup", odbNetwork, map[string]any{"availabilityZone": "us-east-1a", "clientSubnetCidr": "10.0.0.0/24"}, ""},
		{"odb network without zone", odbNetwork, map[string]any{"clientSubnetCidr": "10.0.0.0/24"}, "ValidationException"},
		{"odb bad client CIDR", odbNetwork, map[string]any{"availabilityZone": "a", "clientSubnetCidr": "10.0.0.0"}, "ValidationException"},
		{"odb bad backup CIDR", odbNetwork, map[string]any{"availabilityZone": "a", "clientSubnetCidr": "10.0.0.0/24", "backupSubnetCidr": "x"}, "ValidationException"},
		{"odb overlapping subnets", odbNetwork, map[string]any{"availabilityZone": "a", "clientSubnetCidr": "10.0.0.0/16", "backupSubnetCidr": "10.0.1.0/24"}, "ValidationException"},
		{"odb reserved client", odbNetwork, map[string]any{"availabilityZone": "a", "clientSubnetCidr": "100.106.4.0/24"}, "ValidationException"},
		{"odb reserved backup", odbNetwork, map[string]any{"availabilityZone": "a", "clientSubnetCidr": "10.0.0.0/24", "backupSubnetCidr": "224.0.0.0/24"}, "ValidationException"},
	}
	for _, c := range cases {
		if got := faultCode(t, c.check(c.input)); got != c.code {
			t.Fatalf("%s: got %q, want %q", c.name, got, c.code)
		}
	}
}

func TestDataCheckHooksStoreOfficialMembers(t *testing.T) {
	keyARN := "arn:aws:kms:us-east-1:000000000000:key/k"
	input := map[string]any{"kmsEncryptionKey": keyARN}
	_ = dsqlClusterOptions(input)
	if details := input["encryptionDetails"].(map[string]any); details["kmsKeyArn"] != keyARN || details["encryptionType"] != "CUSTOMER_MANAGED_KMS_KEY" || input["kmsEncryptionKey"] != nil {
		t.Fatalf("dsql key %v", input)
	}
	subnets := map[string]any{"SubnetIds": []any{"subnet-a"}}
	_ = memoryDBSubnets(subnets)
	if list := subnets["Subnets"].([]any); len(list) != 1 || list[0].(map[string]any)["Identifier"] != "subnet-a" || subnets["SubnetIds"] != nil {
		t.Fatalf("memorydb subnets %v", subnets)
	}
	filesystem := map[string]any{"FileSystemType": "LUSTRE", "StorageCapacity": int64(1200), "SubnetIds": []any{"subnet-a"}}
	_ = fsxFileSystem(filesystem)
	if filesystem["LustreConfiguration"].(map[string]any)["DeploymentType"] != "SCRATCH_1" {
		t.Fatalf("fsx default deployment %v", filesystem)
	}
	persistent := map[string]any{"FileSystemType": "LUSTRE", "StorageCapacity": int64(1200), "SubnetIds": []any{"subnet-a"}, "LustreConfiguration": map[string]any{"DeploymentType": "PERSISTENT_2"}}
	_ = fsxFileSystem(persistent)
	if persistent["LustreConfiguration"].(map[string]any)["DeploymentType"] != "PERSISTENT_2" {
		t.Fatalf("fsx explicit deployment %v", persistent)
	}
	filter := map[string]any{"FileSystemId": "arn:aws:elasticfilesystem:us-east-1:000000000000:file-system/fs-0123"}
	_ = efsFileSystemFilter(filter)
	if filter["FileSystemId"] != "fs-0123" {
		t.Fatalf("efs filter %v", filter)
	}
	plan := map[string]any{"ReportDeliveryChannel": map[string]any{"S3BucketName": "b"}}
	_ = backupReportPlan(plan)
	if formats := plan["ReportDeliveryChannel"].(map[string]any)["Formats"].([]any); len(formats) != 1 || formats[0] != "CSV" {
		t.Fatalf("backup default formats %v", plan)
	}
	stream := map[string]any{"ExtendedS3DestinationConfiguration": map[string]any{"RoleARN": "r", "BucketARN": "b"},
		"DeliveryStreamEncryptionConfigurationInput": map[string]any{"KeyType": "CUSTOMER_MANAGED_CMK", "KeyARN": keyARN}}
	if err := firehoseCreate(stream); err != nil {
		t.Fatal(err)
	}
	destination := stream["Destinations"].([]any)[0].(map[string]any)["ExtendedS3DestinationDescription"].(map[string]any)
	if destination["CompressionFormat"] != "UNCOMPRESSED" || destination["BufferingHints"].(map[string]any)["SizeInMBs"] != int64(5) ||
		destination["EncryptionConfiguration"].(map[string]any)["NoEncryptionConfig"] != "NoEncryption" {
		t.Fatalf("firehose destination defaults %v", destination)
	}
	if encryption := stream["DeliveryStreamEncryptionConfiguration"].(map[string]any); encryption["KeyARN"] != keyARN || encryption["Status"] != "ENABLED" {
		t.Fatalf("firehose encryption %v", stream)
	}
	cluster := map[string]any{"KafkaVersion": "3.6.0", "NumberOfBrokerNodes": int64(2), "BrokerNodeGroupInfo": map[string]any{"ClientSubnets": []any{"a", "b"}}}
	_ = mskCreateCluster(cluster)
	if cluster["CurrentBrokerSoftwareInfo"].(map[string]any)["KafkaVersion"] != "3.6.0" || cluster["KafkaVersion"] != nil {
		t.Fatalf("msk software %v", cluster)
	}
}

func TestDataServicesServeOfficialProtocols(t *testing.T) {
	h := dataHarness(t)
	created := h.rest("dsql", "POST", "/cluster", map[string]any{"deletionProtectionEnabled": false, "kmsEncryptionKey": "arn:aws:kms:us-east-1:000000000000:key/k", "tags": map[string]any{"a": "b"}})
	expect(t, created, 200, "")
	id := str(created.body["identifier"])
	if len(id) != 26 || created.body["arn"] != "arn:aws:dsql:us-east-1:000000000000:cluster/"+id {
		t.Fatalf("dsql identity %v", created.body)
	}
	got := h.rest("dsql", "GET", "/cluster/"+id, nil)
	if got.body["encryptionDetails"].(map[string]any)["encryptionType"] != "CUSTOMER_MANAGED_KMS_KEY" || got.body["endpoint"] != nil {
		t.Fatalf("dsql cluster %v", got.body)
	}
	expect(t, h.rest("dsql", "POST", "/cluster", map[string]any{"deletionProtectionEnabled": false, "policy": "{}"}), 400, "ValidationException")
	expect(t, h.rest("dsql", "GET", "/clusters/"+id+"/vpc-endpoint-service-name", nil), 501, "UnknownOperationException")

	expect(t, h.json1("timestream-write", "CreateDatabase", map[string]any{"DatabaseName": "db"}), 200, "")
	expect(t, h.json1("timestream-write", "CreateTable", map[string]any{"DatabaseName": "db", "TableName": "t"}), 200, "")
	expect(t, h.json1("timestream-write", "DeleteDatabase", map[string]any{"DatabaseName": "db"}), 400, "ValidationException")
	expect(t, h.json1("timestream-write", "CreateTable", map[string]any{"DatabaseName": "missing", "TableName": "t"}), 400, "ResourceNotFoundException")

	expect(t, h.json1("memorydb", "CreateSubnetGroup", map[string]any{"SubnetGroupName": "s", "SubnetIds": []any{"subnet-a"}}), 200, "")
	updated := h.json1("memorydb", "UpdateSubnetGroup", map[string]any{"SubnetGroupName": "s", "Description": "d"})
	if group := updated.body["SubnetGroup"].(map[string]any); group["Description"] != "d" || len(group["Subnets"].([]any)) != 1 {
		t.Fatalf("memorydb partial update %v", updated.body)
	}
	expect(t, h.json1("memorydb", "CreateUser", map[string]any{"UserName": "u", "AccessString": "on", "AuthenticationMode": map[string]any{"Type": "password", "Passwords": []any{"secret"}}}), 400, "InvalidParameterValueException")
	var stored int
	_ = h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources WHERE document LIKE '%secret%'").Scan(&stored)
	if stored != 0 {
		t.Fatal("a refused password must never be stored")
	}
	expect(t, h.json1("memorydb", "DescribeUsers", map[string]any{"Filters": []any{map[string]any{"Name": "n", "Values": []any{"v"}}}}), 400, "")

	expect(t, h.json1("fsx", "CreateFileSystem", map[string]any{"FileSystemType": "LUSTRE", "StorageCapacity": 1200, "SubnetIds": []any{"subnet-0a1b2c3d"}, "SecurityGroupIds": []any{"sg-0a1b2c3d"}}), 400, "BadRequest")
	cache := h.json1("fsx", "CreateFileCache", map[string]any{"FileCacheType": "LUSTRE", "FileCacheTypeVersion": "2.12", "StorageCapacity": 1200, "SubnetIds": []any{"subnet-0a1b2c3d"},
		"LustreConfiguration": map[string]any{"PerUnitStorageThroughput": 1000, "DeploymentType": "CACHE_1", "MetadataConfiguration": map[string]any{"StorageCapacity": 2400}},
		"Tags":                []any{map[string]any{"Key": "k", "Value": "v"}}})
	expect(t, cache, 200, "")
	if created := cache.body["FileCache"].(map[string]any); created["Tags"] == nil || created["Lifecycle"] != "CREATING" {
		t.Fatalf("file cache creation output %v", cache.body)
	}

	fs := h.rest("efs", "POST", "/2015-02-01/file-systems", map[string]any{"CreationToken": "t1"})
	expect(t, fs, 201, "")
	fsID := str(fs.body["FileSystemId"])
	listed := h.rest("efs", "GET", "/2015-02-01/file-systems?FileSystemId="+url.QueryEscape(str(fs.body["FileSystemArn"])), nil)
	if systems := listed.body["FileSystems"].([]any); len(systems) != 1 {
		t.Fatalf("efs ARN filter %v", listed.body)
	}
	expect(t, h.rest("efs", "PUT", "/2015-02-01/file-systems/"+fsID, map[string]any{"ThroughputMode": "provisioned"}), 400, "BadRequest")
	expect(t, h.rest("efs", "POST", "/2015-02-01/resource-tags/"+fsID, map[string]any{"Tags": []any{map[string]any{"Key": "k", "Value": "v"}}}), 400, "BadRequest")
	expect(t, h.rest("efs", "GET", "/2015-02-01/account-preferences", nil), 200, "")
	expect(t, h.rest("efs", "DELETE", "/2015-02-01/file-systems/"+fsID, nil), 204, "")

	expect(t, h.rest("backup", "PUT", "/backup-vaults/v1", map[string]any{}), 200, "")
	expect(t, h.rest("backup", "GET", "/backup-vaults/v1?backupVaultAccountId=111111111111", nil), 400, "InvalidParameterValueException")
	expect(t, h.rest("backup", "GET", "/backup-vaults/?shared=true", nil), 400, "InvalidParameterValueException")

	expect(t, h.json1("storagegateway", "CreateTapePool", map[string]any{"PoolName": "p", "StorageClass": "GLACIER"}), 200, "")
	expect(t, h.json1("storagegateway", "ListTapePools", map[string]any{"PoolARNs": []any{"arn:aws:storagegateway:us-east-1:000000000000:tapepool/pool-00000000"}}), 400, "")

	vault := h.rest("glacier", "PUT", "/-/vaults/archive", nil)
	expect(t, vault, 201, "")
	if vault.header.Get("Location") != "/000000000000/vaults/archive" {
		t.Fatalf("glacier location %v", vault.header)
	}
	expect(t, h.rest("glacier", "PUT", "/-/vaults/bad%20name", nil), 400, "InvalidParameterValueException")
	expect(t, h.rest("glacier", "GET", "/-/vaults?limit=5", nil), 400, "InvalidParameterValueException")
	expect(t, h.rest("glacier", "PUT", "/-/policies/data-retrieval", map[string]any{}), 400, "MissingParameterValueException")
	expect(t, h.rest("glacier", "POST", "/-/vaults/archive/tags?operation=add", map[string]any{"Tags": map[string]any{"k": "v"}}), 501, "UnknownOperationException")
	expect(t, h.rest("glacier", "DELETE", "/-/vaults/archive", nil), 204, "")

	configuration := h.rest("mq", "POST", "/v1/configurations", map[string]any{"name": "c", "engineType": "RABBITMQ", "engineVersion": "3.13"})
	expect(t, configuration, 200, "")
	if id := str(configuration.body["id"]); len(id) != 34 || configuration.body["arn"] != "arn:aws:mq:us-east-1:000000000000:configuration:"+id {
		t.Fatalf("mq configuration %v", configuration.body)
	}
	expect(t, h.rest("mq", "POST", "/v1/brokers", map[string]any{"brokerName": "b"}), 501, "UnknownOperationException")

	expect(t, h.json1("firehose", "CreateDeliveryStream", map[string]any{"DeliveryStreamName": "s", "ExtendedS3DestinationConfiguration": map[string]any{"RoleARN": "arn:aws:iam::000000000000:role/r", "BucketARN": "arn:aws:s3:::b"}}), 200, "")
	expect(t, h.json1("firehose", "StartDeliveryStreamEncryption", map[string]any{"DeliveryStreamName": "s"}), 200, "")
	if status := h.json1("firehose", "DescribeDeliveryStream", map[string]any{"DeliveryStreamName": "s"}).body["DeliveryStreamDescription"].(map[string]any)["DeliveryStreamEncryptionConfiguration"].(map[string]any); status["KeyType"] != "AWS_OWNED_CMK" || status["Status"] != "ENABLED" {
		t.Fatalf("firehose default encryption %v", status)
	}
	expect(t, h.json1("firehose", "ListDeliveryStreams", map[string]any{"Limit": 1}), 400, "ValidationException")
	expect(t, h.json1("firehose", "TagDeliveryStream", map[string]any{"DeliveryStreamName": "s", "Tags": []any{map[string]any{"Key": "k"}}}), 501, "UnknownOperationException")

	cluster := h.rest("kafka", "POST", "/v1/clusters", map[string]any{"clusterName": "c", "kafkaVersion": "3.6.0", "numberOfBrokerNodes": 2,
		"brokerNodeGroupInfo": map[string]any{"instanceType": "kafka.m5.large", "clientSubnets": []any{"subnet-a", "subnet-b"}}})
	expect(t, cluster, 200, "")
	arn := url.PathEscape(str(cluster.body["clusterArn"]))
	described := h.rest("kafka", "GET", "/v1/clusters/"+arn, nil)
	if info := described.body["clusterInfo"].(map[string]any); info["currentVersion"] != mskClusterVersion || info["currentBrokerSoftwareInfo"].(map[string]any)["kafkaVersion"] != "3.6.0" {
		t.Fatalf("msk cluster %v", described.body)
	}
	expect(t, h.rest("kafka", "DELETE", "/v1/clusters/"+arn+"?currentVersion=K9", nil), 400, "BadRequestException")
	expect(t, h.rest("kafka", "DELETE", "/v1/clusters/"+arn+"?currentVersion="+mskClusterVersion, nil), 200, "")

	network := h.json1("odb", "CreateOdbNetwork", map[string]any{"displayName": "n", "availabilityZone": "us-east-1a", "clientSubnetCidr": "10.0.0.0/24", "s3Access": "ENABLED"})
	expect(t, network, 400, "ValidationException")
	network = h.json1("odb", "CreateOdbNetwork", map[string]any{"displayName": "n", "availabilityZone": "us-east-1a", "clientSubnetCidr": "10.0.0.0/24"})
	expect(t, network, 200, "")
	if id := str(network.body["odbNetworkId"]); len(id) != 17 || id[:7] != "odbnet_" {
		t.Fatalf("odb network identity %v", network.body)
	}
	expect(t, h.json1("odb", "UpdateOdbNetwork", map[string]any{"odbNetworkId": str(network.body["odbNetworkId"]), "peeredCidrsToBeAdded": []any{"10.1.0.0/16"}}), 400, "ValidationException")
}
