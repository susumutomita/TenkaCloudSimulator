package catalog

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

const (
	migrationRole   = "arn:aws:iam::000000000000:role/migration-fixture"
	migrationSubnet = "subnet-0a1b2c3d4e5f60718"
)

func TestMainframeApplicationLifecycle(t *testing.T) {
	h := newHarness(t, mainframeModernization)
	created := h.rest("m2", "POST", "/applications", map[string]any{
		"name": "payroll", "engineType": "bluage", "description": "fixture", "roleArn": migrationRole,
		"definition": map[string]any{"content": `{"template-version":"2.0"}`}, "tags": map[string]any{"team": "a"},
	})
	expect(t, created, 200, "")
	id, arn := str(created.body["applicationId"]), str(created.body["applicationArn"])
	if len(id) != 10 || arn != "arn:aws:m2:us-east-1:000000000000:app/"+id || fmt.Sprint(created.body["applicationVersion"]) != "1" {
		t.Fatalf("create %v", created.body)
	}
	got := h.rest("m2", "GET", "/applications/"+id, nil)
	expect(t, got, 200, "")
	latest := got.body["latestVersion"].(map[string]any)
	if got.body["status"] != "Created" || got.body["name"] != "payroll" || got.body["engineType"] != "bluage" || latest["status"] != "Available" ||
		got.body["tags"].(map[string]any)["team"] != "a" || got.body["loadBalancerDnsName"] != nil || got.body["environmentId"] != nil || got.body["deployedVersion"] != nil {
		t.Fatalf("get %v", got.body)
	}
	list := h.rest("m2", "GET", "/applications", nil)
	expect(t, list, 200, "")
	if items := list.body["applications"].([]any); len(items) != 1 || items[0].(map[string]any)["versionStatus"] != "Available" {
		t.Fatalf("list %v", list.body)
	}
	if items := h.rest("m2", "GET", "/applications?environmentId=env1", nil).body["applications"].([]any); len(items) != 0 {
		t.Fatalf("environment filter %v", items)
	}

	updated := h.rest("m2", "PATCH", "/applications/"+id, map[string]any{"currentApplicationVersion": 1, "description": "second", "definition": map[string]any{"s3Location": "s3://bucket/definition.json"}})
	expect(t, updated, 200, "")
	if fmt.Sprint(updated.body["applicationVersion"]) != "2" {
		t.Fatalf("update %v", updated.body)
	}
	again := h.rest("m2", "GET", "/applications/"+id, nil)
	version := again.body["latestVersion"].(map[string]any)
	if again.body["description"] != "second" || fmt.Sprint(version["applicationVersion"]) != "2" || version["creationTime"] == nil {
		t.Fatalf("after update %v", again.body)
	}
	expect(t, h.rest("m2", "PATCH", "/applications/"+id, map[string]any{"currentApplicationVersion": 1, "description": "stale"}), 409, "ConflictException")

	escaped := url.PathEscape(arn)
	expect(t, h.rest("m2", "POST", "/tags/"+escaped, map[string]any{"tags": map[string]any{"k": "v"}}), 200, "")
	tags := h.rest("m2", "GET", "/tags/"+escaped, nil)
	expect(t, tags, 200, "")
	if len(tags.body["tags"].(map[string]any)) != 2 {
		t.Fatalf("tags %v", tags.body)
	}
	expect(t, h.rest("m2", "DELETE", "/tags/"+escaped+"?tagKeys=k", nil), 200, "")
	expect(t, h.rest("m2", "GET", "/tags/"+url.PathEscape("arn:aws:m2:us-east-1:000000000000:app/missing"), nil), 404, "ResourceNotFoundException")

	expect(t, h.rest("m2", "POST", "/applications", map[string]any{"name": "payroll", "engineType": "bluage", "definition": map[string]any{"content": "{}"}}), 409, "ConflictException")
	expect(t, h.rest("m2", "GET", "/applications/missing", nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("m2", "GET", "/applications?names=payroll", nil), 400, "ValidationException")
	expect(t, h.rest("m2", "DELETE", "/applications/"+id, nil), 200, "")
	expect(t, h.rest("m2", "DELETE", "/applications/"+id, nil), 400, "ValidationException")
}

func TestMainframeDefinitionRules(t *testing.T) {
	h := newHarness(t, mainframeModernization)
	create := func(definition map[string]any) reply {
		return h.rest("m2", "POST", "/applications", map[string]any{"name": "app-" + strings.ToLower(str(definition["tag"])), "engineType": "microfocus", "definition": withoutTag(definition)})
	}
	expect(t, create(map[string]any{"tag": "a", "content": "{}", "s3Location": "s3://b/k"}), 400, "ValidationException")
	expect(t, create(map[string]any{"tag": "b"}), 400, "ValidationException")
	expect(t, create(map[string]any{"tag": "c", "s3Location": ""}), 400, "ValidationException")
	expect(t, create(map[string]any{"tag": "d", "content": "[1]"}), 400, "ValidationException")
	expect(t, create(map[string]any{"tag": "e", "content": "not json"}), 400, "ValidationException")
	expect(t, create(map[string]any{"tag": "f", "content": "null"}), 400, "ValidationException")
	expect(t, h.rest("m2", "POST", "/applications", map[string]any{"name": "nodef", "engineType": "microfocus", "definition": map[string]any{"content": "{}"}, "kmsKeyId": "alias/x", "environmentId": "env"}), 400, "ValidationException")
	expect(t, h.rest("m2", "POST", "/applications", map[string]any{"name": "ok-def", "engineType": "microfocus", "definition": map[string]any{"s3Location": "s3://b/k"}}), 200, "")
	expect(t, h.rest("m2", "POST", "/applications", map[string]any{"name": "x", "engineType": "other", "definition": map[string]any{"content": "{}"}}), 400, "ValidationException")
}

func withoutTag(definition map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range definition {
		if key != "tag" {
			out[key] = value
		}
	}
	return out
}

func TestMainframeUnboundOperationsAnswer501(t *testing.T) {
	h := newHarness(t, mainframeModernization)
	expect(t, h.rest("m2", "POST", "/environments", map[string]any{"name": "env", "engineType": "bluage", "instanceType": "M2.m5.large"}), 501, "UnknownOperationException")
	expect(t, h.rest("m2", "POST", "/applications/abc/start", nil), 501, "UnknownOperationException")
	expect(t, h.rest("m2", "GET", "/signed-bi-url", nil), 501, "UnknownOperationException")
}

func TestMigrationHubStreamsAndTasks(t *testing.T) {
	h := newHarness(t, migrationHub)
	expect(t, h.json1("mgh", "CreateProgressUpdateStream", map[string]any{"ProgressUpdateStreamName": "tool-a"}), 200, "")
	expect(t, h.json1("mgh", "CreateProgressUpdateStream", map[string]any{"ProgressUpdateStreamName": "tool-a"}), 400, "InvalidInputException")
	expect(t, h.json1("mgh", "CreateProgressUpdateStream", map[string]any{"ProgressUpdateStreamName": "bad/name"}), 400, "InvalidInputException")
	expect(t, h.json1("mgh", "CreateProgressUpdateStream", map[string]any{"ProgressUpdateStreamName": "tool-b", "DryRun": true}), 400, "InvalidInputException")
	list := h.json1("mgh", "ListProgressUpdateStreams", map[string]any{})
	expect(t, list, 200, "")
	if items := list.body["ProgressUpdateStreamSummaryList"].([]any); len(items) != 1 || items[0].(map[string]any)["ProgressUpdateStreamName"] != "tool-a" {
		t.Fatalf("streams %v", list.body)
	}

	expect(t, h.json1("mgh", "ImportMigrationTask", map[string]any{"ProgressUpdateStream": "missing", "MigrationTaskName": "web-1"}), 400, "ResourceNotFoundException")
	expect(t, h.json1("mgh", "ImportMigrationTask", map[string]any{"ProgressUpdateStream": "tool-a", "MigrationTaskName": "web-1"}), 200, "")
	expect(t, h.json1("mgh", "ImportMigrationTask", map[string]any{"ProgressUpdateStream": "tool-a", "MigrationTaskName": "web-1"}), 400, "InvalidInputException")
	expect(t, h.json1("mgh", "ImportMigrationTask", map[string]any{"ProgressUpdateStream": "tool-a", "MigrationTaskName": "bad|name"}), 400, "InvalidInputException")
	described := h.json1("mgh", "DescribeMigrationTask", map[string]any{"ProgressUpdateStream": "tool-a", "MigrationTaskName": "web-1"})
	expect(t, described, 200, "")
	task := described.body["MigrationTask"].(map[string]any)
	if task["ProgressUpdateStream"] != "tool-a" || task["MigrationTaskName"] != "web-1" || task["Task"] != nil || task["UpdateDateTime"] == nil {
		t.Fatalf("task %v", task)
	}
	notify := map[string]any{"ProgressUpdateStream": "tool-a", "MigrationTaskName": "web-1", "UpdateDateTime": 1790000000, "NextUpdateSeconds": 60,
		"Task": map[string]any{"Status": "IN_PROGRESS", "ProgressPercent": 40, "StatusDetail": "copying"}}
	expect(t, h.json1("mgh", "NotifyMigrationTaskState", notify), 200, "")
	task = h.json1("mgh", "DescribeMigrationTask", map[string]any{"ProgressUpdateStream": "tool-a", "MigrationTaskName": "web-1"}).body["MigrationTask"].(map[string]any)
	if state := task["Task"].(map[string]any); state["Status"] != "IN_PROGRESS" || state["StatusDetail"] != "copying" {
		t.Fatalf("state %v", task)
	}
	expect(t, h.json1("mgh", "NotifyMigrationTaskState", map[string]any{"ProgressUpdateStream": "tool-a", "MigrationTaskName": "none", "UpdateDateTime": 1790000000, "NextUpdateSeconds": 60, "Task": map[string]any{"Status": "FAILED"}}), 400, "ResourceNotFoundException")
	expect(t, h.json1("mgh", "DescribeMigrationTask", map[string]any{"ProgressUpdateStream": "tool-a", "MigrationTaskName": "none"}), 400, "ResourceNotFoundException")

	expect(t, h.json1("mgh", "DeleteProgressUpdateStream", map[string]any{"ProgressUpdateStreamName": "tool-a"}), 200, "")
	expect(t, h.json1("mgh", "DescribeMigrationTask", map[string]any{"ProgressUpdateStream": "tool-a", "MigrationTaskName": "web-1"}), 400, "ResourceNotFoundException")
	expect(t, h.json1("mgh", "DeleteProgressUpdateStream", map[string]any{"ProgressUpdateStreamName": "tool-a"}), 400, "ResourceNotFoundException")
	expect(t, h.json1("mgh", "ListMigrationTasks", map[string]any{}), 501, "UnknownOperationException")
	expect(t, h.json1("mgh", "PutResourceAttributes", map[string]any{}), 501, "UnknownOperationException")
}

func TestTransferWorkflowProfileAndConnector(t *testing.T) {
	h := newHarness(t, transferFamily)
	copyStep := map[string]any{"Type": "COPY", "CopyStepDetails": map[string]any{"Name": "copy", "DestinationFileLocation": map[string]any{"S3FileLocation": map[string]any{"Bucket": "archive", "Key": "in/"}}}}
	created := h.json1("transfer", "CreateWorkflow", map[string]any{"Description": "archive", "Steps": []any{copyStep}, "Tags": []any{map[string]any{"Key": "team", "Value": "a"}}})
	expect(t, created, 200, "")
	workflow := str(created.body["WorkflowId"])
	if !strings.HasPrefix(workflow, "w-") || len(workflow) != 19 {
		t.Fatalf("workflow id %v", created.body)
	}
	described := h.json1("transfer", "DescribeWorkflow", map[string]any{"WorkflowId": workflow})
	expect(t, described, 200, "")
	detail := described.body["Workflow"].(map[string]any)
	if detail["Arn"] != "arn:aws:transfer:us-east-1:000000000000:workflow/"+workflow || detail["Description"] != "archive" || len(detail["Steps"].([]any)) != 1 || len(detail["Tags"].([]any)) != 1 {
		t.Fatalf("workflow %v", detail)
	}
	if items := h.json1("transfer", "ListWorkflows", map[string]any{}).body["Workflows"].([]any); len(items) != 1 {
		t.Fatalf("workflows %v", items)
	}
	for _, steps := range [][]any{
		{map[string]any{"Type": "COPY"}},
		{map[string]any{"Type": "TAG", "CopyStepDetails": map[string]any{}}},
		{map[string]any{"Type": "DELETE", "DeleteStepDetails": map[string]any{}, "TagStepDetails": map[string]any{}}},
		{map[string]any{"CustomStepDetails": map[string]any{}}},
	} {
		expect(t, h.json1("transfer", "CreateWorkflow", map[string]any{"Steps": steps}), 400, "InvalidRequestException")
	}
	expect(t, h.json1("transfer", "CreateWorkflow", map[string]any{"Steps": []any{copyStep}, "OnExceptionSteps": []any{map[string]any{"Type": "DELETE"}}}), 400, "InvalidRequestException")
	expect(t, h.json1("transfer", "CreateWorkflow", map[string]any{"Steps": []any{map[string]any{"Type": "DELETE", "DeleteStepDetails": map[string]any{"Name": "rm"}}}, "OnExceptionSteps": []any{map[string]any{"Type": "TAG", "TagStepDetails": map[string]any{"Name": "tag"}}}}), 200, "")
	expect(t, h.json1("transfer", "DescribeWorkflow", map[string]any{"WorkflowId": "w-00000000000000000"}), 400, "ResourceNotFoundException")
	expect(t, h.json1("transfer", "DeleteWorkflow", map[string]any{"WorkflowId": workflow}), 200, "")
	expect(t, h.json1("transfer", "DeleteWorkflow", map[string]any{"WorkflowId": workflow}), 400, "ResourceNotFoundException")

	profile := h.json1("transfer", "CreateProfile", map[string]any{"As2Id": "LOCALPARTY", "ProfileType": "LOCAL", "CertificateIds": []any{"cert-0123456789abcdef0"}})
	expect(t, profile, 200, "")
	profileID := str(profile.body["ProfileId"])
	expect(t, h.json1("transfer", "CreateProfile", map[string]any{"As2Id": "PARTNER", "ProfileType": "PARTNER"}), 200, "")
	expect(t, h.json1("transfer", "CreateProfile", map[string]any{"As2Id": "has space", "ProfileType": "LOCAL"}), 400, "InvalidRequestException")
	expect(t, h.json1("transfer", "UpdateProfile", map[string]any{"ProfileId": profileID, "CertificateIds": []any{"cert-0123456789abcdef1", "cert-0123456789abcdef2"}}), 200, "")
	got := h.json1("transfer", "DescribeProfile", map[string]any{"ProfileId": profileID}).body["Profile"].(map[string]any)
	if got["As2Id"] != "LOCALPARTY" || len(got["CertificateIds"].([]any)) != 2 || got["Arn"] != "arn:aws:transfer:us-east-1:000000000000:profile/"+profileID {
		t.Fatalf("profile %v", got)
	}
	if items := h.json1("transfer", "ListProfiles", map[string]any{"ProfileType": "PARTNER"}).body["Profiles"].([]any); len(items) != 1 || items[0].(map[string]any)["As2Id"] != "PARTNER" {
		t.Fatalf("filtered profiles %v", items)
	}
	if items := h.json1("transfer", "ListProfiles", map[string]any{}).body["Profiles"].([]any); len(items) != 2 {
		t.Fatalf("profiles %v", items)
	}
	expect(t, h.json1("transfer", "UpdateProfile", map[string]any{"ProfileId": "p-00000000000000000", "CertificateIds": []any{"cert-0123456789abcdef3"}}), 400, "ResourceNotFoundException")

	connector := h.json1("transfer", "CreateConnector", map[string]any{"Url": "sftp://partner.example.com", "AccessRole": migrationRole,
		"SftpConfig": map[string]any{"UserSecretId": "secret-id", "MaxConcurrentConnections": 2}})
	expect(t, connector, 200, "")
	connectorID := str(connector.body["ConnectorId"])
	described = h.json1("transfer", "DescribeConnector", map[string]any{"ConnectorId": connectorID})
	expect(t, described, 200, "")
	record := described.body["Connector"].(map[string]any)
	if record["Status"] != "PENDING" || record["EgressType"] != "SERVICE_MANAGED" || record["Url"] != "sftp://partner.example.com" || record["ServiceManagedEgressIpAddresses"] != nil {
		t.Fatalf("connector %v", record)
	}
	updated := h.json1("transfer", "UpdateConnector", map[string]any{"ConnectorId": connectorID, "Url": "sftp://other.example.com", "LoggingRole": migrationRole})
	expect(t, updated, 200, "")
	if items := h.json1("transfer", "ListConnectors", map[string]any{}).body["Connectors"].([]any); len(items) != 1 || items[0].(map[string]any)["Url"] != "sftp://other.example.com" {
		t.Fatalf("connectors %v", items)
	}
	expect(t, h.json1("transfer", "CreateConnector", map[string]any{"Url": "", "AccessRole": migrationRole}), 400, "InvalidRequestException")
	expect(t, h.json1("transfer", "CreateConnector", map[string]any{"AccessRole": migrationRole, "EgressConfig": map[string]any{"VpcLattice": map[string]any{"ResourceConfigurationArn": "arn:aws:vpc-lattice:us-east-1:000000000000:resourceconfiguration/rcfg-0123456789abcdef0"}}}), 400, "InvalidRequestException")
	expect(t, h.json1("transfer", "CreateConnector", map[string]any{"AccessRole": migrationRole}), 400, "InvalidRequestException")
	expect(t, h.json1("transfer", "CreateConnector", map[string]any{"Url": "sftp://partner.example.com", "AccessRole": migrationRole}), 400, "InvalidRequestException")
	expect(t, h.json1("transfer", "CreateConnector", map[string]any{"AccessRole": migrationRole, "SftpConfig": map[string]any{"UserSecretId": "secret-id"}}), 400, "InvalidRequestException")
	expect(t, h.json1("transfer", "CreateConnector", map[string]any{"Url": "sftp://partner.example.com", "AccessRole": migrationRole,
		"SftpConfig": map[string]any{"UserSecretId": "secret-id"}, "As2Config": map[string]any{"LocalProfileId": "p-0123456789abcdef0"}}), 400, "InvalidRequestException")
	// A trusted host key is a public key: stored as given and returned.
	keyed := h.json1("transfer", "CreateConnector", map[string]any{"Url": "sftp://keyed.example.com", "AccessRole": migrationRole,
		"SftpConfig": map[string]any{"UserSecretId": "secret-id", "TrustedHostKeys": []any{"ssh-ed25519 AAAAexamplepublickey"}}})
	expect(t, keyed, 200, "")
	keyedRecord := h.json1("transfer", "DescribeConnector", map[string]any{"ConnectorId": str(keyed.body["ConnectorId"])}).body["Connector"].(map[string]any)
	if keys := keyedRecord["SftpConfig"].(map[string]any)["TrustedHostKeys"].([]any); len(keys) != 1 || keys[0] != "ssh-ed25519 AAAAexamplepublickey" {
		t.Fatalf("trusted host keys %v", keyedRecord)
	}
	expect(t, h.json1("transfer", "DeleteConnector", map[string]any{"ConnectorId": str(keyed.body["ConnectorId"])}), 200, "")
	expect(t, h.json1("transfer", "UpdateConnector", map[string]any{"ConnectorId": connectorID, "Url": ""}), 400, "InvalidRequestException")
	expect(t, h.json1("transfer", "DeleteConnector", map[string]any{"ConnectorId": connectorID}), 200, "")
	expect(t, h.json1("transfer", "DeleteConnector", map[string]any{"ConnectorId": connectorID}), 400, "ResourceNotFoundException")
}

func TestTransferServerIsAnOfflineRecord(t *testing.T) {
	h := newHarness(t, transferFamily)
	created := h.json1("transfer", "CreateServer", map[string]any{"LoggingRole": migrationRole, "Tags": []any{map[string]any{"Key": "team", "Value": "a"}}})
	expect(t, created, 200, "")
	id := str(created.body["ServerId"])
	described := h.json1("transfer", "DescribeServer", map[string]any{"ServerId": id})
	expect(t, described, 200, "")
	server := described.body["Server"].(map[string]any)
	if server["State"] != "OFFLINE" || server["EndpointType"] != "PUBLIC" || server["Domain"] != "S3" || server["IdentityProviderType"] != "SERVICE_MANAGED" ||
		server["Protocols"].([]any)[0] != "SFTP" || server["Arn"] != "arn:aws:transfer:us-east-1:000000000000:server/"+id ||
		server["HostKeyFingerprint"] != nil || server["EndpointDetails"] != nil {
		t.Fatalf("server %v", server)
	}
	if items := h.json1("transfer", "ListServers", map[string]any{}).body["Servers"].([]any); len(items) != 1 || items[0].(map[string]any)["State"] != "OFFLINE" {
		t.Fatalf("servers %v", items)
	}
	expect(t, h.json1("transfer", "UpdateServer", map[string]any{"ServerId": id, "PreAuthenticationLoginBanner": "welcome", "SecurityPolicyName": "TransferSecurityPolicy-2024-01"}), 200, "")
	if banner := h.json1("transfer", "DescribeServer", map[string]any{"ServerId": id}).body["Server"].(map[string]any)["PreAuthenticationLoginBanner"]; banner != "welcome" {
		t.Fatalf("banner %v", banner)
	}

	arn := "arn:aws:transfer:us-east-1:000000000000:server/" + id
	expect(t, h.json1("transfer", "TagResource", map[string]any{"Arn": arn, "Tags": []any{map[string]any{"Key": "k", "Value": "v"}}}), 200, "")
	expect(t, h.json1("transfer", "UntagResource", map[string]any{"Arn": arn, "TagKeys": []any{"k"}}), 200, "")
	tags := h.json1("transfer", "ListTagsForResource", map[string]any{"Arn": arn})
	expect(t, tags, 200, "")
	if len(tags.body["Tags"].([]any)) != 1 {
		t.Fatalf("tags %v", tags.body)
	}
	expect(t, h.json1("transfer", "ListTagsForResource", map[string]any{"Arn": "arn:aws:transfer:us-east-1:000000000000:server/s-00000000000000000"}), 400, "InvalidRequestException")

	// Documented relations between protocols, endpoint, identity provider and certificate.
	refused := []map[string]any{
		{"Protocols": []any{"FTP"}},
		{"Protocols": []any{"FTPS"}, "EndpointType": "VPC"},
		{"Protocols": []any{"FTP"}, "EndpointType": "VPC", "IdentityProviderType": "AWS_LAMBDA", "IdentityProviderDetails": map[string]any{}},
		{"Protocols": []any{"FTPS"}, "EndpointType": "VPC", "IdentityProviderType": "AWS_LAMBDA", "IdentityProviderDetails": map[string]any{"Function": "arn:aws:lambda:us-east-1:000000000000:function:auth"}},
		{"IdentityProviderType": "AWS_DIRECTORY_SERVICE"},
		{"IdentityProviderType": "API_GATEWAY", "IdentityProviderDetails": map[string]any{"Url": "https://example.com/auth"}},
		{"EndpointType": "VPC_ENDPOINT"},
		{"HostKey": "material"},
	}
	for _, request := range refused {
		expect(t, h.json1("transfer", "CreateServer", request), 400, "InvalidRequestException")
	}
	expect(t, h.json1("transfer", "CreateServer", map[string]any{"Protocols": []any{"FTPS"}, "EndpointType": "VPC", "Certificate": "arn:aws:acm:us-east-1:000000000000:certificate/c",
		"IdentityProviderType": "AWS_LAMBDA", "IdentityProviderDetails": map[string]any{"Function": "arn:aws:lambda:us-east-1:000000000000:function:auth"}}), 200, "")
	expect(t, h.json1("transfer", "CreateServer", map[string]any{"IdentityProviderType": "API_GATEWAY",
		"IdentityProviderDetails": map[string]any{"Url": "https://example.com/auth", "InvocationRole": migrationRole}}), 200, "")
	expect(t, h.json1("transfer", "CreateServer", map[string]any{"IdentityProviderType": "AWS_DIRECTORY_SERVICE",
		"IdentityProviderDetails": map[string]any{"DirectoryId": "d-0123456789"}}), 200, "")
	expect(t, h.json1("transfer", "UpdateServer", map[string]any{"ServerId": id, "Protocols": []any{"FTP"}}), 400, "InvalidRequestException")
	expect(t, h.json1("transfer", "UpdateServer", map[string]any{"ServerId": id, "HostKey": "material"}), 400, "InvalidRequestException")
	expect(t, h.json1("transfer", "UpdateServer", map[string]any{"ServerId": "s-00000000000000000", "Protocols": []any{"SFTP"}}), 400, "ResourceNotFoundException")
	expect(t, h.json1("transfer", "DescribeServer", map[string]any{"ServerId": "s-00000000000000000"}), 400, "ResourceNotFoundException")
	expect(t, h.json1("transfer", "DeleteServer", map[string]any{"ServerId": id}), 200, "")
	expect(t, h.json1("transfer", "DeleteServer", map[string]any{"ServerId": id}), 400, "ResourceNotFoundException")
}

func TestTransferUnboundOperationsAnswer501(t *testing.T) {
	h := newHarness(t, transferFamily)
	for _, op := range []string{"StartServer", "StopServer", "StartFileTransfer", "TestConnection", "ImportCertificate", "ImportHostKey", "ImportSshPublicKey", "CreateUser", "ListUsers"} {
		expect(t, h.json1("transfer", op, map[string]any{}), 501, "UnknownOperationException")
	}
}

func TestApplicationMigrationApplicationsAndWaves(t *testing.T) {
	h := newHarness(t, applicationMigration)
	app := h.rest("mgn", "POST", "/CreateApplication", map[string]any{"name": "shop", "description": "storefront", "tags": map[string]any{"team": "a"}})
	expect(t, app, 201, "")
	appID := str(app.body["applicationID"])
	if !strings.HasPrefix(appID, "app-") || len(appID) != 21 || app.body["arn"] != "arn:aws:mgn:us-east-1:000000000000:application/"+appID || app.body["isArchived"] != false || app.body["waveID"] != nil {
		t.Fatalf("application %v", app.body)
	}
	expect(t, h.rest("mgn", "POST", "/CreateApplication", map[string]any{"name": "shop", "accountID": "999999999999"}), 400, "")
	expect(t, h.rest("mgn", "POST", "/UpdateApplication", map[string]any{"applicationID": appID, "name": "shop2", "description": "renamed", "accountID": "000000000000"}), 200, "")
	archived := h.rest("mgn", "POST", "/ArchiveApplication", map[string]any{"applicationID": appID})
	expect(t, archived, 200, "")
	if archived.body["isArchived"] != true || archived.body["name"] != "shop2" {
		t.Fatalf("archive %v", archived.body)
	}
	list := h.rest("mgn", "POST", "/ListApplications", map[string]any{})
	expect(t, list, 200, "")
	if items := list.body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["isArchived"] != true {
		t.Fatalf("applications %v", list.body)
	}
	expect(t, h.rest("mgn", "POST", "/ListApplications", map[string]any{"filters": map[string]any{"isArchived": true}}), 400, "")
	expect(t, h.rest("mgn", "POST", "/UnarchiveApplication", map[string]any{"applicationID": appID}), 200, "")
	expect(t, h.rest("mgn", "POST", "/UpdateApplication", map[string]any{"applicationID": "app-00000000000000000", "name": "x"}), 404, "ResourceNotFoundException")

	wave := h.rest("mgn", "POST", "/CreateWave", map[string]any{"name": "wave one", "description": "first"})
	expect(t, wave, 201, "")
	waveID := str(wave.body["waveID"])
	if !strings.HasPrefix(waveID, "wave-") || len(waveID) != 22 {
		t.Fatalf("wave %v", wave.body)
	}
	expect(t, h.rest("mgn", "POST", "/UpdateWave", map[string]any{"waveID": waveID, "name": "wave 1"}), 200, "")
	expect(t, h.rest("mgn", "POST", "/ArchiveWave", map[string]any{"waveID": waveID}), 200, "")
	expect(t, h.rest("mgn", "POST", "/UnarchiveWave", map[string]any{"waveID": waveID}), 200, "")
	if items := h.rest("mgn", "POST", "/ListWaves", map[string]any{}).body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["name"] != "wave 1" {
		t.Fatalf("waves %v", items)
	}
	arn := str(wave.body["arn"])
	escaped := url.PathEscape(arn)
	expect(t, h.rest("mgn", "POST", "/tags/"+escaped, map[string]any{"tags": map[string]any{"k": "v"}}), 200, "")
	if tags := h.rest("mgn", "GET", "/tags/"+escaped, nil).body["tags"].(map[string]any); tags["k"] != "v" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("mgn", "DELETE", "/tags/"+escaped+"?tagKeys=k", nil), 200, "")
	expect(t, h.rest("mgn", "POST", "/DeleteWave", map[string]any{"waveID": waveID}), 204, "")
	expect(t, h.rest("mgn", "POST", "/DeleteWave", map[string]any{"waveID": waveID}), 404, "ResourceNotFoundException")
	expect(t, h.rest("mgn", "POST", "/DeleteApplication", map[string]any{"applicationID": appID}), 204, "")
}

func TestApplicationMigrationConnectorsAndTemplates(t *testing.T) {
	h := newHarness(t, applicationMigration)
	connector := h.rest("mgn", "POST", "/CreateConnector", map[string]any{"name": "ssm-link", "ssmInstanceID": "i-0123456789abcdef0",
		"ssmCommandConfig": map[string]any{"cloudWatchOutputEnabled": true, "s3OutputEnabled": false, "cloudWatchLogGroupName": "mgn"}})
	expect(t, connector, 201, "")
	connectorID := str(connector.body["connectorID"])
	if !strings.HasPrefix(connectorID, "connector-") {
		t.Fatalf("connector %v", connector.body)
	}
	expect(t, h.rest("mgn", "POST", "/UpdateConnector", map[string]any{"connectorID": connectorID, "name": "renamed"}), 200, "")
	if items := h.rest("mgn", "POST", "/ListConnectors", map[string]any{}).body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["name"] != "renamed" {
		t.Fatalf("connectors %v", items)
	}
	expect(t, h.rest("mgn", "POST", "/DeleteConnector", map[string]any{"connectorID": connectorID}), 204, "")
	expect(t, h.rest("mgn", "POST", "/DeleteConnector", map[string]any{"connectorID": connectorID}), 404, "ResourceNotFoundException")

	launch := h.rest("mgn", "POST", "/CreateLaunchConfigurationTemplate", map[string]any{"launchDisposition": "STOPPED", "bootMode": "UEFI", "copyTags": true, "tags": map[string]any{"team": "a"}})
	expect(t, launch, 201, "")
	launchID := str(launch.body["launchConfigurationTemplateID"])
	if !strings.HasPrefix(launchID, "lct-") || launch.body["ec2LaunchTemplateID"] != nil || launch.body["arn"] != "arn:aws:mgn:us-east-1:000000000000:launch-configuration-template/"+launchID {
		t.Fatalf("launch template %v", launch.body)
	}
	expect(t, h.rest("mgn", "POST", "/UpdateLaunchConfigurationTemplate", map[string]any{"launchConfigurationTemplateID": launchID, "launchDisposition": "STARTED"}), 200, "")
	found := h.rest("mgn", "POST", "/DescribeLaunchConfigurationTemplates", map[string]any{"launchConfigurationTemplateIDs": []any{launchID}})
	expect(t, found, 200, "")
	if items := found.body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["launchDisposition"] != "STARTED" || items[0].(map[string]any)["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("launch templates %v", found.body)
	}
	expect(t, h.rest("mgn", "POST", "/DescribeLaunchConfigurationTemplates", map[string]any{"launchConfigurationTemplateIDs": []any{"lct-00000000000000000"}}), 404, "ResourceNotFoundException")

	replication := h.rest("mgn", "POST", "/CreateReplicationConfigurationTemplate", map[string]any{
		"associateDefaultSecurityGroup": true, "bandwidthThrottling": 0, "createPublicIP": false, "dataPlaneRouting": "PRIVATE_IP",
		"defaultLargeStagingDiskType": "GP3", "ebsEncryption": "DEFAULT", "replicationServerInstanceType": "t3.small",
		"replicationServersSecurityGroupsIDs": []any{"sg-0123456789abcdef0"}, "stagingAreaSubnetId": migrationSubnet,
		"stagingAreaTags": map[string]any{"purpose": "staging"}, "useDedicatedReplicationServer": false, "tags": map[string]any{"team": "a"}})
	expect(t, replication, 201, "")
	assertStagingAreaTags(t, replication.body)
	replicationID := str(replication.body["replicationConfigurationTemplateID"])
	updated := h.rest("mgn", "POST", "/UpdateReplicationConfigurationTemplate", map[string]any{"replicationConfigurationTemplateID": replicationID, "bandwidthThrottling": 50})
	expect(t, updated, 200, "")
	assertStagingAreaTags(t, updated.body)
	found = h.rest("mgn", "POST", "/DescribeReplicationConfigurationTemplates", map[string]any{})
	if items := found.body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["stagingAreaSubnetId"] != migrationSubnet {
		t.Fatalf("replication templates %v", found.body)
	}
	expect(t, h.rest("mgn", "POST", "/UpdateReplicationConfigurationTemplate", map[string]any{"replicationConfigurationTemplateID": replicationID, "arn": "arn:aws:mgn:us-east-1:000000000000:x/y"}), 400, "")
	expect(t, h.rest("mgn", "POST", "/CreateReplicationConfigurationTemplate", map[string]any{"bandwidthThrottling": 20000}), 400, "")
	expect(t, h.rest("mgn", "POST", "/DeleteReplicationConfigurationTemplate", map[string]any{"replicationConfigurationTemplateID": replicationID}), 204, "")
	expect(t, h.rest("mgn", "POST", "/DeleteLaunchConfigurationTemplate", map[string]any{"launchConfigurationTemplateID": launchID}), 204, "")
	expect(t, h.rest("mgn", "POST", "/DeleteLaunchConfigurationTemplate", map[string]any{"launchConfigurationTemplateID": launchID}), 404, "ResourceNotFoundException")
}

func TestApplicationMigrationUnboundOperationsAnswer501(t *testing.T) {
	h := newHarness(t, applicationMigration)
	for _, path := range []string{"/StartReplication", "/StartCutover", "/DescribeSourceServers", "/InitializeService", "/AssociateApplications"} {
		expect(t, h.rest("mgn", "POST", path, map[string]any{}), 501, "UnknownOperationException")
	}
}

func TestElasticDisasterRecoveryTemplatesAndLaunchActions(t *testing.T) {
	h := newHarness(t, elasticDisasterRecovery)
	launch := h.rest("drs", "POST", "/CreateLaunchConfigurationTemplate", map[string]any{"launchDisposition": "STARTED", "copyTags": true, "postLaunchEnabled": true, "tags": map[string]any{"team": "a"}})
	expect(t, launch, 201, "")
	template := launch.body["launchConfigurationTemplate"].(map[string]any)
	id := str(template["launchConfigurationTemplateID"])
	if !strings.HasPrefix(id, "lct-") || template["arn"] != "arn:aws:drs:us-east-1:000000000000:launch-configuration-template/"+id || template["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("launch template %v", launch.body)
	}
	updated := h.rest("drs", "POST", "/UpdateLaunchConfigurationTemplate", map[string]any{"launchConfigurationTemplateID": id, "launchDisposition": "STOPPED"})
	expect(t, updated, 200, "")
	if updated.body["launchConfigurationTemplate"].(map[string]any)["launchDisposition"] != "STOPPED" {
		t.Fatalf("update %v", updated.body)
	}
	found := h.rest("drs", "POST", "/DescribeLaunchConfigurationTemplates", map[string]any{"launchConfigurationTemplateIDs": []any{id}})
	expect(t, found, 200, "")
	if len(found.body["items"].([]any)) != 1 {
		t.Fatalf("describe %v", found.body)
	}
	expect(t, h.rest("drs", "POST", "/DescribeLaunchConfigurationTemplates", map[string]any{"launchConfigurationTemplateIDs": []any{"lct-00000000000000000"}}), 404, "ResourceNotFoundException")

	replication := h.rest("drs", "POST", "/CreateReplicationConfigurationTemplate", map[string]any{
		"associateDefaultSecurityGroup": true, "bandwidthThrottling": 0, "createPublicIP": false, "dataPlaneRouting": "PRIVATE_IP",
		"defaultLargeStagingDiskType": "AUTO", "ebsEncryption": "DEFAULT", "replicationServerInstanceType": "t3.small",
		"replicationServersSecurityGroupsIDs": []any{"sg-0123456789abcdef0"}, "stagingAreaSubnetId": migrationSubnet,
		"stagingAreaTags": map[string]any{"purpose": "staging"}, "useDedicatedReplicationServer": false, "tags": map[string]any{"team": "a"},
		"pitPolicy": []any{map[string]any{"enabled": true, "interval": 10, "retentionDuration": 60, "units": "MINUTE"}}})
	expect(t, replication, 201, "")
	assertStagingAreaTags(t, replication.body)
	replicationID := str(replication.body["replicationConfigurationTemplateID"])
	updated = h.rest("drs", "POST", "/UpdateReplicationConfigurationTemplate", map[string]any{"replicationConfigurationTemplateID": replicationID, "autoReplicateNewDisks": false})
	expect(t, updated, 200, "")
	assertStagingAreaTags(t, updated.body)
	if items := h.rest("drs", "POST", "/DescribeReplicationConfigurationTemplates", map[string]any{}).body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["autoReplicateNewDisks"] != false {
		t.Fatalf("replication templates %v", items)
	}

	action := map[string]any{"resourceId": id, "actionId": "6c2e4a7a-1f6f-4d4c-9d2a-0a1b2c3d4e5f", "actionCode": "AWS-RunShellScript", "actionVersion": "$LATEST", "active": true,
		"category": "CONFIGURATION", "description": "configure", "name": "configure host", "optional": true, "order": 3, "parameters": map[string]any{"commands": map[string]any{"type": "SSM_STORE", "value": "migration.commands"}}}
	put := h.rest("drs", "POST", "/PutLaunchAction", action)
	expect(t, put, 200, "")
	if put.body["resourceId"] != id || put.body["actionId"] != action["actionId"] || put.body["name"] != "configure host" || put.body["type"] != nil {
		t.Fatalf("put %v", put.body)
	}
	action["description"] = "reconfigured"
	action["active"] = false
	expect(t, h.rest("drs", "POST", "/PutLaunchAction", action), 200, "")
	listed := h.rest("drs", "POST", "/ListLaunchActions", map[string]any{"resourceId": id})
	expect(t, listed, 200, "")
	if items := listed.body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["description"] != "reconfigured" || items[0].(map[string]any)["active"] != false {
		t.Fatalf("actions %v", listed.body)
	}
	expect(t, h.rest("drs", "POST", "/ListLaunchActions", map[string]any{"resourceId": id, "filters": map[string]any{"actionIds": []any{"x"}}}), 400, "ValidationException")
	missing := map[string]any{}
	for key, value := range action {
		missing[key] = value
	}
	missing["resourceId"] = "s-0123456789abcdef0"
	expect(t, h.rest("drs", "POST", "/PutLaunchAction", missing), 404, "ResourceNotFoundException")

	arn := str(template["arn"])
	escaped := url.PathEscape(arn)
	expect(t, h.rest("drs", "POST", "/tags/"+escaped, map[string]any{"tags": map[string]any{"k": "v"}}), 200, "")
	if tags := h.rest("drs", "GET", "/tags/"+escaped, nil).body["tags"].(map[string]any); len(tags) != 2 {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("drs", "DELETE", "/tags/"+escaped+"?tagKeys=k", nil), 200, "")

	expect(t, h.rest("drs", "POST", "/DeleteLaunchAction", map[string]any{"resourceId": id, "actionId": action["actionId"]}), 204, "")
	expect(t, h.rest("drs", "POST", "/DeleteLaunchAction", map[string]any{"resourceId": id, "actionId": action["actionId"]}), 404, "ResourceNotFoundException")
	expect(t, h.rest("drs", "POST", "/PutLaunchAction", action), 200, "")
	expect(t, h.rest("drs", "POST", "/DeleteLaunchConfigurationTemplate", map[string]any{"launchConfigurationTemplateID": id}), 204, "")
	expect(t, h.rest("drs", "POST", "/ListLaunchActions", map[string]any{"resourceId": id}), 404, "ResourceNotFoundException")
	expect(t, h.rest("drs", "POST", "/DeleteReplicationConfigurationTemplate", map[string]any{"replicationConfigurationTemplateID": replicationID}), 204, "")
	expect(t, h.rest("drs", "POST", "/DeleteReplicationConfigurationTemplate", map[string]any{"replicationConfigurationTemplateID": replicationID}), 404, "ResourceNotFoundException")
}

func TestElasticDisasterRecoveryUnboundOperationsAnswer501(t *testing.T) {
	h := newHarness(t, elasticDisasterRecovery)
	for _, path := range []string{"/StartRecovery", "/StartReplication", "/DescribeSourceServers", "/InitializeService", "/CreateSourceNetwork"} {
		expect(t, h.rest("drs", "POST", path, map[string]any{}), 501, "UnknownOperationException")
	}
}

// assertStagingAreaTags checks that a replication template answer carries the
// stored staging-area tags and, separately, the resource's own tags.
func assertStagingAreaTags(t *testing.T, body map[string]any) {
	t.Helper()
	staging, _ := body["stagingAreaTags"].(map[string]any)
	tags, _ := body["tags"].(map[string]any)
	if len(staging) != 1 || staging["purpose"] != "staging" || len(tags) != 1 || tags["team"] != "a" {
		t.Fatalf("stagingAreaTags %v tags %v", body["stagingAreaTags"], body["tags"])
	}
}
