package catalog

import "testing"

func TestAthenaRefusesFederatedCatalogs(t *testing.T) {
	h := newHarness(t, athena)
	expect(t, h.json1("athena", "CreateDataCatalog", map[string]any{"Name": "fed", "Type": "FEDERATED"}), 400, "InvalidRequestException")
	expect(t, h.json1("athena", "CreateDataCatalog", map[string]any{"Name": "hive", "Type": "HIVE", "Parameters": map[string]any{"metadata-function": "arn:aws:lambda:us-east-1:000000000000:function:hms"}}), 200, "")
	expect(t, h.json1("athena", "UpdateDataCatalog", map[string]any{"Name": "hive", "Type": "FEDERATED"}), 400, "InvalidRequestException")
	expect(t, h.json1("athena", "CreateWorkGroup", map[string]any{"Name": "wg"}), 200, "")
	expect(t, h.json1("athena", "UpdateWorkGroup", map[string]any{"WorkGroup": "wg", "ConfigurationUpdates": map[string]any{"EnforceWorkGroupConfiguration": true}}), 400, "InvalidRequestException")
	expect(t, h.json1("athena", "DeleteWorkGroup", map[string]any{"WorkGroup": "wg", "RecursiveDeleteOption": true}), 200, "")
}

func TestGlueCatalogNamesLinksAndCascade(t *testing.T) {
	h := newHarness(t, glue)
	expect(t, h.json1("glue", "CreateDatabase", map[string]any{"DatabaseInput": map[string]any{"Name": "Sales"}}), 400, "InvalidInputException")
	expect(t, h.json1("glue", "CreateDatabase", map[string]any{"DatabaseInput": map[string]any{"Name": "linked", "TargetDatabase": map[string]any{"CatalogId": "111111111111", "DatabaseName": "x"}}}), 400, "InvalidInputException")
	expect(t, h.json1("glue", "CreateDatabase", map[string]any{"CatalogId": "111111111111", "DatabaseInput": map[string]any{"Name": "other"}}), 400, "InvalidInputException")
	expect(t, h.json1("glue", "CreateDatabase", map[string]any{"CatalogId": "000000000000", "DatabaseInput": map[string]any{"Name": "sales"}}), 200, "")
	expect(t, h.json1("glue", "UpdateDatabase", map[string]any{"Name": "sales", "DatabaseInput": map[string]any{"Name": "renamed"}}), 400, "InvalidInputException")
	expect(t, h.json1("glue", "UpdateDatabase", map[string]any{"Name": "sales", "DatabaseInput": map[string]any{"Name": "sales", "Description": "d"}}), 200, "")

	table := map[string]any{"Name": "orders", "StorageDescriptor": map[string]any{"Columns": []any{map[string]any{"Name": "id", "Type": "string"}}}}
	expect(t, h.json1("glue", "CreateTable", map[string]any{"DatabaseName": "sales", "Name": "orders"}), 400, "InvalidInputException")
	expect(t, h.json1("glue", "CreateTable", map[string]any{"DatabaseName": "sales", "Name": "other", "TableInput": table}), 400, "InvalidInputException")
	expect(t, h.json1("glue", "CreateTable", map[string]any{"DatabaseName": "sales", "TableInput": map[string]any{"Name": "view", "ViewDefinition": map[string]any{"Definer": "arn:aws:iam::000000000000:role/x"}}}), 400, "InvalidInputException")
	expect(t, h.json1("glue", "CreateTable", map[string]any{"DatabaseName": "sales", "Name": "orders", "TableInput": table}), 200, "")
	// The official contract identifies an updated table by TableInput.Name.
	updated := map[string]any{"Name": "orders", "Description": "by day"}
	expect(t, h.json1("glue", "UpdateTable", map[string]any{"DatabaseName": "sales", "TableInput": updated, "SkipArchive": true}), 200, "")
	got := h.json1("glue", "GetTable", map[string]any{"DatabaseName": "sales", "Name": "orders"})
	expect(t, got, 200, "")
	stored := got.body["Table"].(map[string]any)
	if stored["Description"] != "by day" || stored["DatabaseName"] != "sales" || stored["StorageDescriptor"] == nil {
		t.Fatalf("table update %v", stored)
	}
	expect(t, h.json1("glue", "UpdateTable", map[string]any{"DatabaseName": "sales", "Name": "orders", "TableInput": map[string]any{"Name": "moved"}}), 400, "InvalidInputException")
	expect(t, h.json1("glue", "UpdateTable", map[string]any{"DatabaseName": "sales", "TableInput": map[string]any{"Name": "orders", "VersionId": "2"}}), 400, "InvalidInputException")
	tableArn := "arn:aws:glue:us-east-1:000000000000:table/sales/orders"
	expect(t, h.json1("glue", "TagResource", map[string]any{"ResourceArn": tableArn, "TagsToAdd": map[string]any{"k": "v"}}), 200, "")

	expect(t, h.json1("glue", "DeleteDatabase", map[string]any{"Name": "sales"}), 200, "")
	expect(t, h.json1("glue", "GetTable", map[string]any{"DatabaseName": "sales", "Name": "orders"}), 400, "EntityNotFoundException")
	expect(t, h.json1("glue", "GetTags", map[string]any{"ResourceArn": tableArn}), 400, "EntityNotFoundException")
}

func TestGlueJobsAndCrawlersRefuseRunsAndSourceControl(t *testing.T) {
	h := newHarness(t, glue)
	job := map[string]any{"Name": "etl", "Role": "r", "Command": map[string]any{"Name": "glueetl", "ScriptLocation": "s3://b/k.py"}}
	withSource := map[string]any{"SourceControlDetails": map[string]any{"Provider": "GITHUB"}}
	for k, v := range job {
		withSource[k] = v
	}
	expect(t, h.json1("glue", "CreateJob", withSource), 400, "InvalidInputException")
	created := h.json1("glue", "CreateJob", job)
	expect(t, created, 200, "")
	if created.body["Name"] != "etl" {
		t.Fatalf("create job output %v", created.body)
	}
	expect(t, h.json1("glue", "UpdateJob", map[string]any{"JobName": "etl", "JobUpdate": map[string]any{"SourceControlDetails": map[string]any{"Provider": "GITHUB"}}}), 400, "InvalidInputException")
	expect(t, h.json1("glue", "UpdateJob", map[string]any{"JobName": "etl", "JobUpdate": map[string]any{"MaxRetries": 2}}), 200, "")
	if h.json1("glue", "GetJob", map[string]any{"JobName": "etl"}).body["Job"].(map[string]any)["Command"] == nil {
		t.Fatal("a partial job update must keep the stored command")
	}
	if names := h.json1("glue", "ListJobs", map[string]any{}).body["JobNames"].([]any); len(names) != 1 || names[0] != "etl" {
		t.Fatalf("job names %v", names)
	}
	expect(t, h.json1("glue", "ListJobs", map[string]any{"Tags": map[string]any{"a": "b"}}), 400, "InvalidInputException")
	deleted := h.json1("glue", "DeleteJob", map[string]any{"JobName": "etl"})
	expect(t, deleted, 200, "")
	if deleted.body["JobName"] != "etl" {
		t.Fatalf("delete job output %v", deleted.body)
	}
	crawler := map[string]any{"Name": "c", "Role": "r", "Targets": map[string]any{"S3Targets": []any{map[string]any{"Path": "s3://b/"}}}}
	expect(t, h.json1("glue", "CreateCrawler", map[string]any{"Name": "s", "Role": "r", "Targets": map[string]any{}, "Schedule": "cron(0 1 * * ? *)"}), 400, "InvalidInputException")
	expect(t, h.json1("glue", "CreateCrawler", crawler), 200, "")
	expect(t, h.json1("glue", "UpdateCrawler", map[string]any{"Name": "c", "Schedule": "cron(0 2 * * ? *)"}), 400, "InvalidInputException")
	if state := h.json1("glue", "GetCrawler", map[string]any{"Name": "c"}).body["Crawler"].(map[string]any)["State"]; state != "READY" {
		t.Fatalf("crawler state %v", state)
	}
}

func TestKinesisCapacityIdentityRetentionAndEncryption(t *testing.T) {
	h := newHarness(t, kinesis)
	expect(t, h.json1("kinesis", "CreateStream", map[string]any{"StreamName": "none"}), 400, "InvalidArgumentException")
	expect(t, h.json1("kinesis", "CreateStream", map[string]any{"StreamName": "mixed", "ShardCount": 1, "StreamModeDetails": map[string]any{"StreamMode": "ON_DEMAND"}}), 400, "InvalidArgumentException")
	expect(t, h.json1("kinesis", "CreateStream", map[string]any{"StreamName": "bare", "StreamModeDetails": map[string]any{"StreamMode": "PROVISIONED"}}), 400, "InvalidArgumentException")
	expect(t, h.json1("kinesis", "CreateStream", map[string]any{"StreamName": "warm", "ShardCount": 1, "WarmThroughputMiBps": 10}), 400, "ValidationException")
	expect(t, h.json1("kinesis", "CreateStream", map[string]any{"StreamName": "clicks", "ShardCount": 3}), 200, "")
	arn := "arn:aws:kinesis:us-east-1:000000000000:stream/clicks"
	summary := func(input map[string]any) map[string]any {
		t.Helper()
		got := h.json1("kinesis", "DescribeStreamSummary", input)
		expect(t, got, 200, "")
		return got.body["StreamDescriptionSummary"].(map[string]any)
	}
	if s := summary(map[string]any{"StreamARN": arn}); asText(s["OpenShardCount"]) != "3" || s["StreamARN"] != arn {
		t.Fatalf("summary by ARN %v", s)
	}
	summary(map[string]any{"StreamName": "clicks", "StreamARN": arn})
	expect(t, h.json1("kinesis", "DescribeStreamSummary", map[string]any{"StreamName": "other", "StreamARN": arn}), 400, "InvalidArgumentException")
	expect(t, h.json1("kinesis", "IncreaseStreamRetentionPeriod", map[string]any{"StreamName": "clicks", "RetentionPeriodHours": 12}), 400, "InvalidArgumentException")
	expect(t, h.json1("kinesis", "IncreaseStreamRetentionPeriod", map[string]any{"StreamARN": arn, "RetentionPeriodHours": 168}), 200, "")
	expect(t, h.json1("kinesis", "StartStreamEncryption", map[string]any{"StreamName": "clicks", "EncryptionType": "NONE", "KeyId": "k"}), 400, "InvalidArgumentException")
	expect(t, h.json1("kinesis", "StartStreamEncryption", map[string]any{"StreamName": "clicks", "EncryptionType": "KMS", "KeyId": "alias/aws/kinesis"}), 200, "")
	if s := summary(map[string]any{"StreamName": "clicks"}); s["EncryptionType"] != "KMS" || asText(s["RetentionPeriodHours"]) != "168" {
		t.Fatalf("encryption %v", s)
	}
	expect(t, h.json1("kinesis", "StopStreamEncryption", map[string]any{"StreamName": "clicks", "EncryptionType": "KMS", "KeyId": "alias/aws/kinesis"}), 200, "")
	if s := summary(map[string]any{"StreamName": "clicks"}); s["EncryptionType"] != "NONE" || s["KeyId"] != nil {
		t.Fatalf("stopped encryption must clear the key: %v", s)
	}
	expect(t, h.json1("kinesis", "CreateStream", map[string]any{"StreamName": "events", "StreamModeDetails": map[string]any{"StreamMode": "ON_DEMAND"}}), 200, "")
	if s := summary(map[string]any{"StreamName": "events"}); asText(s["OpenShardCount"]) != "4" {
		t.Fatalf("on-demand shards %v", s)
	}
	expect(t, h.json1("kinesis", "PutRecord", map[string]any{"StreamName": "clicks", "Data": "aGk=", "PartitionKey": "k"}), 501, "UnknownOperationException")
	expect(t, h.json1("kinesis", "DeleteStream", map[string]any{"StreamARN": arn}), 200, "")
	expect(t, h.json1("kinesis", "DescribeStreamSummary", map[string]any{"StreamARN": arn}), 400, "ResourceNotFoundException")
}

func TestOpenSearchDomainsRefuseNetworksAndAuthentication(t *testing.T) {
	h := newHarness(t, openSearch)
	create := func(input map[string]any) reply {
		t.Helper()
		return h.rest("opensearch", "POST", "/2021-01-01/opensearch/domain", input)
	}
	expect(t, create(map[string]any{"DomainName": "legacy", "EngineVersion": "Elasticsearch_7.10"}), 400, "ValidationException")
	for _, member := range []string{"VPCOptions", "AdvancedSecurityOptions", "CognitoOptions", "IdentityCenterOptions", "AutoTuneOptions", "AIMLOptions"} {
		expect(t, create(map[string]any{"DomainName": "refused", member: map[string]any{}}), 400, "ValidationException")
	}
	created := create(map[string]any{"DomainName": "logs", "EngineVersion": "OpenSearch_2.11", "AccessPolicies": "{}"})
	expect(t, created, 200, "")
	status := created.body["DomainStatus"].(map[string]any)
	if status["ARN"] != "arn:aws:es:us-east-1:000000000000:domain/logs" || status["DomainId"] != "000000000000/logs" || status["Endpoint"] != nil {
		t.Fatalf("domain status %v", status)
	}
	if names := h.rest("opensearch", "GET", "/2021-01-01/domain?engineType=OpenSearch", nil).body["DomainNames"].([]any); len(names) != 1 {
		t.Fatalf("domain names %v", names)
	}
	deleted := h.rest("opensearch", "DELETE", "/2021-01-01/opensearch/domain/logs", nil)
	expect(t, deleted, 200, "")
	if deleted.body["DomainStatus"].(map[string]any)["Deleted"] != true {
		t.Fatalf("delete output %v", deleted.body)
	}
	expect(t, h.rest("opensearch", "GET", "/2021-01-01/opensearch/domain/logs", nil), 409, "ResourceNotFoundException")
}
