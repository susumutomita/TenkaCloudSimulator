package catalog

import (
	"net/url"
	"strings"
	"testing"
)

func s3Flow(name string) map[string]any {
	return map[string]any{
		"flowName":      name,
		"triggerConfig": map[string]any{"triggerType": "OnDemand"},
		"sourceFlowConfig": map[string]any{"connectorType": "S3",
			"sourceConnectorProperties": map[string]any{"S3": map[string]any{"bucketName": "source-bucket"}}},
		"destinationFlowConfigList": []any{map[string]any{"connectorType": "S3",
			"destinationConnectorProperties": map[string]any{"S3": map[string]any{"bucketName": "target-bucket"}}}},
		"tasks": []any{map[string]any{"taskType": "Map_all", "sourceFields": []any{}, "connectorOperator": map[string]any{"S3": "NO_OP"}}},
		"tags":  map[string]any{"team": "a"},
	}
}

func TestAppFlowStoresFlowsWithoutRunningThem(t *testing.T) {
	h := newHarness(t, appFlow)
	created := h.rest("appflow", "POST", "/create-flow", s3Flow("copy-reports"))
	expect(t, created, 200, "")
	arn := str(created.body["flowArn"])
	if arn != "arn:aws:appflow:us-east-1:000000000000:flow/copy-reports" || created.body["flowStatus"] != "Active" {
		t.Fatalf("created %v", created.body)
	}
	expect(t, h.rest("appflow", "POST", "/create-flow", s3Flow("copy-reports")), 409, "ConflictException")
	described := h.rest("appflow", "POST", "/describe-flow", map[string]any{"flowName": "copy-reports"})
	expect(t, described, 200, "")
	if described.body["flowArn"] != arn || described.body["createdAt"] == nil || described.body["lastRunExecutionDetails"] != nil ||
		described.body["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("described %v", described.body)
	}
	listed := h.rest("appflow", "POST", "/list-flows", map[string]any{}).body["flows"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["flowName"] != "copy-reports" {
		t.Fatalf("listed %v", listed)
	}
	update := s3Flow("copy-reports")
	delete(update, "tags")
	update["description"] = "nightly"
	update["triggerConfig"] = map[string]any{"triggerType": "Scheduled", "triggerProperties": map[string]any{"Scheduled": map[string]any{"scheduleExpression": "rate(1days)"}}}
	update["clientToken"] = "token-1"
	if updated := h.rest("appflow", "POST", "/update-flow", update); updated.status != 200 || updated.body["flowStatus"] != "Active" {
		t.Fatalf("updated %d %v", updated.status, updated.raw)
	}
	described = h.rest("appflow", "POST", "/describe-flow", map[string]any{"flowName": "copy-reports"})
	if described.body["description"] != "nightly" || described.body["triggerConfig"].(map[string]any)["triggerType"] != "Scheduled" || described.body["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("updated flow %v", described.body)
	}
	path := "/tags/" + url.PathEscape(arn)
	expect(t, h.rest("appflow", "POST", path, map[string]any{"tags": map[string]any{"k": "v"}}), 200, "")
	expect(t, h.rest("appflow", "DELETE", path+"?tagKeys=team", nil), 200, "")
	if tags := h.rest("appflow", "GET", path, nil).body["tags"].(map[string]any); len(tags) != 1 || tags["k"] != "v" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("appflow", "POST", "/delete-flow", map[string]any{"flowName": "copy-reports", "forceDelete": true}), 200, "")
	expect(t, h.rest("appflow", "POST", "/delete-flow", map[string]any{"flowName": "copy-reports"}), 404, "ResourceNotFoundException")
	expect(t, h.rest("appflow", "POST", "/describe-flow", map[string]any{"flowName": "copy-reports"}), 404, "ResourceNotFoundException")
	missing := s3Flow("missing")
	delete(missing, "tags")
	expect(t, h.rest("appflow", "POST", "/update-flow", missing), 404, "ResourceNotFoundException")
	// Operations that would run or stop a flow are not implemented.
	expect(t, h.rest("appflow", "POST", "/start-flow", map[string]any{"flowName": "copy-reports"}), 501, "UnknownOperationException")
	expect(t, h.rest("appflow", "POST", "/create-connector-profile", map[string]any{}), 501, "UnknownOperationException")
}

func TestAppFlowRefusesConnectorsThatNeedCredentials(t *testing.T) {
	h := newHarness(t, appFlow)
	salesforce := s3Flow("from-crm")
	salesforce["sourceFlowConfig"].(map[string]any)["connectorType"] = "Salesforce"
	expect(t, h.rest("appflow", "POST", "/create-flow", salesforce), 400, "ValidationException")
	profile := s3Flow("profiled")
	profile["destinationFlowConfigList"].([]any)[0].(map[string]any)["connectorProfileName"] = "profile"
	expect(t, h.rest("appflow", "POST", "/create-flow", profile), 400, "ValidationException")
	source := s3Flow("profiled-source")
	source["sourceFlowConfig"].(map[string]any)["connectorProfileName"] = "profile"
	expect(t, h.rest("appflow", "POST", "/create-flow", source), 400, "ValidationException")
	two := s3Flow("two")
	two["destinationFlowConfigList"] = append(two["destinationFlowConfigList"].([]any), two["destinationFlowConfigList"].([]any)[0])
	expect(t, h.rest("appflow", "POST", "/create-flow", two), 400, "ValidationException")
	none := s3Flow("none")
	none["destinationFlowConfigList"] = []any{}
	expect(t, h.rest("appflow", "POST", "/create-flow", none), 400, "ValidationException")
	event := s3Flow("event")
	event["triggerConfig"] = map[string]any{"triggerType": "Event"}
	expect(t, h.rest("appflow", "POST", "/create-flow", event), 400, "ValidationException")
	if listed := h.rest("appflow", "POST", "/list-flows", map[string]any{}).body["flows"].([]any); len(listed) != 0 {
		t.Fatalf("refused flows were stored: %v", listed)
	}
	if err := appFlowDefinition(map[string]any{"tasks": []any{}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(appFlow.Bounds[1], "connector profile") {
		t.Fatal("Bounds must say why connector profiles are unbound")
	}
}

func TestCleanRoomsConfiguredTablesAreStoredRecords(t *testing.T) {
	h := newHarness(t, cleanRooms)
	table := map[string]any{"name": "orders", "description": "orders table", "analysisMethod": "DIRECT_QUERY",
		"tableReference": map[string]any{"glue": map[string]any{"databaseName": "sales", "tableName": "orders"}},
		"allowedColumns": []any{"order_id", "region"}, "tags": map[string]any{"team": "a"}}
	created := h.rest("cleanrooms", "POST", "/configuredTables", table)
	expect(t, created, 200, "")
	made := created.body["configuredTable"].(map[string]any)
	id := str(made["id"])
	if made["arn"] != "arn:aws:cleanrooms:us-east-1:000000000000:configuredtable/"+id || len(made["analysisRuleTypes"].([]any)) != 0 || made["createTime"] == nil {
		t.Fatalf("created %v", made)
	}
	got := h.rest("cleanrooms", "GET", "/configuredTables/"+id, nil).body["configuredTable"].(map[string]any)
	if got["name"] != "orders" || got["tableReference"].(map[string]any)["glue"].(map[string]any)["tableName"] != "orders" {
		t.Fatalf("read %v", got)
	}
	listed := h.rest("cleanrooms", "GET", "/configuredTables", nil).body["configuredTableSummaries"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["id"] != id {
		t.Fatalf("listed %v", listed)
	}
	updated := h.rest("cleanrooms", "PATCH", "/configuredTables/"+id, map[string]any{"description": "changed", "allowedColumns": []any{"order_id"}, "analysisMethod": "MULTIPLE", "selectedAnalysisMethods": []any{"DIRECT_QUERY", "DIRECT_JOB"}})
	expect(t, updated, 200, "")
	if after := updated.body["configuredTable"].(map[string]any); after["description"] != "changed" || len(after["allowedColumns"].([]any)) != 1 || after["name"] != "orders" {
		t.Fatalf("updated %v", after)
	}
	expect(t, h.rest("cleanrooms", "PATCH", "/configuredTables/"+id, map[string]any{"selectedAnalysisMethods": []any{"DIRECT_JOB"}}), 400, "ValidationException")
	snowflake := map[string]any{"snowflake": map[string]any{"secretArn": "arn:aws:secretsmanager:us-east-1:000000000000:secret:s", "accountIdentifier": "a", "databaseName": "d", "tableName": "t", "schemaName": "s", "tableSchema": map[string]any{"v1": []any{map[string]any{"columnName": "c", "columnType": "t"}}}}}
	bad := map[string]any{"name": "snow", "analysisMethod": "DIRECT_QUERY", "tableReference": snowflake, "allowedColumns": []any{"c"}}
	expect(t, h.rest("cleanrooms", "POST", "/configuredTables", bad), 400, "ValidationException")
	expect(t, h.rest("cleanrooms", "POST", "/configuredTables", map[string]any{"name": "empty", "analysisMethod": "DIRECT_QUERY", "tableReference": table["tableReference"], "allowedColumns": []any{}}), 400, "ValidationException")
	arn := str(made["arn"])
	expect(t, h.rest("cleanrooms", "POST", "/tags/"+url.PathEscape(arn), map[string]any{"tags": map[string]any{"k": "v"}}), 200, "")
	if tags := h.rest("cleanrooms", "GET", "/tags/"+url.PathEscape(arn), nil).body["tags"].(map[string]any); len(tags) != 2 {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("cleanrooms", "DELETE", "/tags/"+url.PathEscape(arn)+"?tagKeys=k", nil), 200, "")
	expect(t, h.rest("cleanrooms", "DELETE", "/configuredTables/"+id, nil), 204, "")
	expect(t, h.rest("cleanrooms", "GET", "/configuredTables/"+id, nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("cleanrooms", "POST", "/memberships/"+id+"/analysistemplates", map[string]any{}), 501, "UnknownOperationException")
	expect(t, h.rest("cleanrooms", "POST", "/configuredTables/"+id+"/analysisRule", map[string]any{}), 501, "UnknownOperationException")
}

func TestCleanRoomsCollaborationHasOnlyItsCreator(t *testing.T) {
	h := newHarness(t, cleanRooms)
	body := func(name string) map[string]any {
		return map[string]any{"name": name, "description": "joint analysis", "members": []any{}, "creatorMemberAbilities": []any{"CAN_QUERY"},
			"creatorDisplayName": "Creator", "queryLogStatus": "DISABLED", "autoApprovedChangeRequestTypes": []any{"ADD_MEMBER"}, "tags": map[string]any{"team": "a"}}
	}
	created := h.rest("cleanrooms", "POST", "/collaborations", body("study"))
	expect(t, created, 200, "")
	made := created.body["collaboration"].(map[string]any)
	id := str(made["id"])
	if made["creatorAccountId"] != "000000000000" || made["memberStatus"] != "ACTIVE" || made["membershipId"] != nil || made["creatorDisplayName"] != "Creator" ||
		len(made["autoApprovedChangeTypes"].([]any)) != 1 || made["arn"] != "arn:aws:cleanrooms:us-east-1:000000000000:collaboration/"+id {
		t.Fatalf("created %v", made)
	}
	invite := body("invite")
	invite["members"] = []any{map[string]any{"accountId": "111122223333", "memberAbilities": []any{"CAN_QUERY"}, "displayName": "Partner"}}
	expect(t, h.rest("cleanrooms", "POST", "/collaborations", invite), 400, "ValidationException")
	ml := body("ml")
	ml["creatorMLMemberAbilities"] = map[string]any{"customMLMemberAbilities": []any{"CAN_RECEIVE_MODEL_OUTPUT"}}
	expect(t, h.rest("cleanrooms", "POST", "/collaborations", ml), 400, "ValidationException")
	pay := body("pay")
	pay["creatorPaymentConfiguration"] = map[string]any{"queryCompute": map[string]any{"isResponsible": true}}
	expect(t, h.rest("cleanrooms", "POST", "/collaborations", pay), 400, "ValidationException")
	listed := h.rest("cleanrooms", "GET", "/collaborations?memberStatus=ACTIVE", nil).body["collaborationList"].([]any)
	if len(listed) != 1 || len(h.rest("cleanrooms", "GET", "/collaborations?memberStatus=INVITED", nil).body["collaborationList"].([]any)) != 0 {
		t.Fatalf("listed %v", listed)
	}
	updated := h.rest("cleanrooms", "PATCH", "/collaborations/"+id, map[string]any{"name": "renamed", "analyticsEngine": "SPARK"})
	expect(t, updated, 200, "")
	if after := updated.body["collaboration"].(map[string]any); after["name"] != "renamed" || after["analyticsEngine"] != "SPARK" || after["description"] != "joint analysis" {
		t.Fatalf("updated %v", after)
	}
	expect(t, h.rest("cleanrooms", "GET", "/collaborations/"+id, nil), 200, "")
	expect(t, h.rest("cleanrooms", "DELETE", "/collaborations/"+id, nil), 204, "")
	expect(t, h.rest("cleanrooms", "GET", "/collaborations/"+id, nil), 400, "ValidationException")
	expect(t, h.rest("cleanrooms", "POST", "/memberships", map[string]any{"collaborationIdentifier": id, "queryLogStatus": "DISABLED"}), 501, "UnknownOperationException")
}

func TestCleanRoomsMLStoresTrainingRecords(t *testing.T) {
	h := newHarness(t, cleanRoomsML)
	role := "arn:aws:iam::000000000000:role/cleanrooms-ml"
	dataset := h.rest("cleanroomsml", "POST", "/training-dataset", map[string]any{"name": "events", "roleArn": role, "description": "events", "tags": map[string]any{"team": "a"},
		"trainingData": []any{map[string]any{"type": "INTERACTIONS", "inputConfig": map[string]any{"schema": []any{map[string]any{"columnName": "user", "columnTypes": []any{"USER_ID"}}},
			"dataSource": map[string]any{"glueDataSource": map[string]any{"tableName": "events", "databaseName": "ml"}}}}}})
	expect(t, dataset, 200, "")
	arn := str(dataset.body["trainingDatasetArn"])
	if !strings.HasPrefix(arn, "arn:aws:cleanrooms-ml:us-east-1:000000000000:training-dataset/") {
		t.Fatalf("arn %v", dataset.body)
	}
	got := h.rest("cleanroomsml", "GET", "/training-dataset/"+url.PathEscape(arn), nil)
	if got.status != 200 || got.body["status"] != "ACTIVE" || got.body["roleArn"] != role || got.body["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("read %d %v", got.status, got.raw)
	}
	if listed := h.rest("cleanroomsml", "GET", "/training-dataset", nil).body["trainingDatasets"].([]any); len(listed) != 1 || listed[0].(map[string]any)["name"] != "events" {
		t.Fatalf("listed %v", listed)
	}
	algorithm := h.rest("cleanroomsml", "POST", "/configured-model-algorithms", map[string]any{"name": "trainer", "roleArn": role,
		"trainingContainerConfig": map[string]any{"imageUri": "registry.example/trainer:1"}})
	expect(t, algorithm, 200, "")
	algorithmArn := str(algorithm.body["configuredModelAlgorithmArn"])
	if got := h.rest("cleanroomsml", "GET", "/configured-model-algorithms/"+url.PathEscape(algorithmArn), nil); got.body["name"] != "trainer" || got.body["trainingContainerConfig"] == nil {
		t.Fatalf("algorithm %v", got.raw)
	}
	if listed := h.rest("cleanroomsml", "GET", "/configured-model-algorithms", nil).body["configuredModelAlgorithms"].([]any); len(listed) != 1 {
		t.Fatalf("listed %v", listed)
	}
	expect(t, h.rest("cleanroomsml", "POST", "/tags/"+url.PathEscape(arn), map[string]any{"tags": map[string]any{"k": "v"}}), 200, "")
	expect(t, h.rest("cleanroomsml", "DELETE", "/tags/"+url.PathEscape(arn)+"?tagKeys=k", nil), 200, "")
	if tags := h.rest("cleanroomsml", "GET", "/tags/"+url.PathEscape(arn), nil).body["tags"].(map[string]any); len(tags) != 1 {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("cleanroomsml", "DELETE", "/training-dataset/"+url.PathEscape(arn), nil), 200, "")
	expect(t, h.rest("cleanroomsml", "DELETE", "/configured-model-algorithms/"+url.PathEscape(algorithmArn), nil), 200, "")
	expect(t, h.rest("cleanroomsml", "GET", "/training-dataset/"+url.PathEscape(arn), nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("cleanroomsml", "POST", "/audience-model", map[string]any{}), 501, "UnknownOperationException")
}

func (h *harness) search(action string, pairs ...string) queryReply {
	h.t.Helper()
	return h.call("cloudsearch", "2013-01-01", action, pairs...)
}

func TestCloudSearchDomainsAreStoredRecords(t *testing.T) {
	h := newHarness(t, cloudSearch)
	created := h.search("CreateDomain", "DomainName", "movies")
	status := created.result(t, "CreateDomain").get("DomainStatus")
	if status.get("DomainName").text != "movies" || status.get("DomainId").text != "000000000000/movies" || status.get("Created").text != "true" ||
		status.get("Deleted").text != "false" || status.get("Processing").text != "false" || status.get("RequiresIndexDocuments").text != "false" ||
		status.get("ARN").text != "arn:aws:cloudsearch:us-east-1:000000000000:domain/movies" || status.get("DocService") != nil || status.get("SearchService") != nil {
		t.Fatalf("domain %s", created.raw)
	}
	expectFault(t, h.search("CreateDomain", "DomainName", "movies"), 409, "ResourceAlreadyExists")
	expectFault(t, h.search("CreateDomain", "DomainName", "Movies"), 400, "ValidationException")
	expectFault(t, h.search("CreateDomain", "DomainName", "ab"), 400, "ValidationException")
	h.search("CreateDomain", "DomainName", "books").result(t, "CreateDomain")
	all := h.search("DescribeDomains").result(t, "DescribeDomains").get("DomainStatusList").all("member")
	if len(all) != 2 {
		t.Fatalf("all domains %d", len(all))
	}
	one := h.search("DescribeDomains", "DomainNames.member.1", "books").result(t, "DescribeDomains").get("DomainStatusList").all("member")
	if len(one) != 1 || one[0].get("DomainName").text != "books" {
		t.Fatalf("one domain %v", one)
	}
	expectFault(t, h.search("DescribeDomains", "DomainNames.member.1", "missing"), 400, "InvalidParameterValue")
	deleted := h.search("DeleteDomain", "DomainName", "movies").result(t, "DeleteDomain").get("DomainStatus")
	if deleted.get("Deleted").text != "true" || deleted.get("DomainName").text != "movies" {
		t.Fatalf("deleted %v", deleted)
	}
	expectFault(t, h.search("DeleteDomain", "DomainName", "movies"), 400, "InvalidParameterValue")
	if left := h.search("DescribeDomains").result(t, "DescribeDomains").get("DomainStatusList").all("member"); len(left) != 1 {
		t.Fatalf("remaining %d", len(left))
	}
	// Index, expression and access policy operations are not implemented.
	expectFault(t, h.search("IndexDocuments", "DomainName", "books"), 501, "UnknownOperationException")
	expectFault(t, h.search("DefineIndexField", "DomainName", "books"), 501, "UnknownOperationException")
	expectFault(t, h.search("ListDomainNames"), 501, "UnknownOperationException")
	h.policy.deny["cloudsearch:CreateDomain"] = true
	expectFault(t, h.search("CreateDomain", "DomainName", "denied"), 403, "AccessDenied")
}

func TestDataZoneDomainsAndProjectsAreStoredRecords(t *testing.T) {
	h := newHarness(t, dataZone)
	role := "arn:aws:iam::000000000000:role/datazone-domain"
	created := h.rest("datazone", "POST", "/v2/domains", map[string]any{"name": "analytics", "domainExecutionRole": role, "description": "domain",
		"singleSignOn": map[string]any{"type": "DISABLED"}, "tags": map[string]any{"team": "a"}})
	expect(t, created, 201, "")
	id := str(created.body["id"])
	if !strings.HasPrefix(id, "dzd_") || created.body["status"] != "AVAILABLE" || created.body["portalUrl"] != nil || created.body["rootDomainUnitId"] != nil ||
		created.body["arn"] != "arn:aws:datazone:us-east-1:000000000000:domain/"+id {
		t.Fatalf("domain %v", created.body)
	}
	expect(t, h.rest("datazone", "POST", "/v2/domains", map[string]any{"name": "analytics", "domainExecutionRole": role}), 409, "ConflictException")
	expect(t, h.rest("datazone", "POST", "/v2/domains", map[string]any{"name": "sso", "domainExecutionRole": role, "singleSignOn": map[string]any{"type": "IAM_IDC", "userAssignment": "AUTOMATIC"}}), 400, "ValidationException")
	got := h.rest("datazone", "GET", "/v2/domains/"+id, nil)
	if got.body["name"] != "analytics" || got.body["tags"].(map[string]any)["team"] != "a" || got.body["createdAt"] == nil {
		t.Fatalf("read %v", got.body)
	}
	items := h.rest("datazone", "GET", "/v2/domains?status=AVAILABLE", nil).body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["managedAccountId"] != "000000000000" || len(h.rest("datazone", "GET", "/v2/domains?status=DELETING", nil).body["items"].([]any)) != 0 {
		t.Fatalf("list %v", items)
	}
	updated := h.rest("datazone", "PUT", "/v2/domains/"+id+"?clientToken=t-1", map[string]any{"description": "changed", "singleSignOn": map[string]any{"type": "DISABLED"}})
	if updated.status != 200 || updated.body["description"] != "changed" || updated.body["name"] != "analytics" {
		t.Fatalf("update %d %v", updated.status, updated.raw)
	}
	expect(t, h.rest("datazone", "PUT", "/v2/domains/"+id, map[string]any{"singleSignOn": map[string]any{"type": "IAM_IDC"}}), 400, "ValidationException")

	base := "/v2/domains/" + id + "/projects"
	project := h.rest("datazone", "POST", base, map[string]any{"name": "sales", "description": "sales project"})
	expect(t, project, 201, "")
	pid := str(project.body["id"])
	if project.body["domainId"] != id || project.body["projectStatus"] != "ACTIVE" || project.body["createdBy"] != "simulator" || len(pid) != 14 {
		t.Fatalf("project %v", project.body)
	}
	expect(t, h.rest("datazone", "POST", base, map[string]any{"name": "sales"}), 409, "ConflictException")
	for _, member := range []string{"domainUnitId", "projectProfileId", "glossaryTerms", "userParameters"} {
		var value any = "x"
		if member == "glossaryTerms" || member == "userParameters" {
			value = []any{}
		}
		expect(t, h.rest("datazone", "POST", base, map[string]any{"name": "other", member: value}), 400, "ValidationException")
	}
	expect(t, h.rest("datazone", "POST", base, map[string]any{"name": "tagged", "resourceTags": map[string]any{"a": "b"}}), 400, "ValidationException")
	expect(t, h.rest("datazone", "POST", "/v2/domains/dzd_missing/projects", map[string]any{"name": "orphan"}), 404, "ResourceNotFoundException")
	if got := h.rest("datazone", "GET", base+"/"+pid, nil); got.body["name"] != "sales" || got.body["lastUpdatedAt"] == nil {
		t.Fatalf("project read %v", got.body)
	}
	projects := h.rest("datazone", "GET", base+"?name=sales", nil).body["items"].([]any)
	if len(projects) != 1 || projects[0].(map[string]any)["updatedAt"] == nil || len(h.rest("datazone", "GET", base+"?name=none", nil).body["items"].([]any)) != 0 {
		t.Fatalf("projects %v", projects)
	}
	renamed := h.rest("datazone", "PATCH", base+"/"+pid, map[string]any{"name": "sales-eu", "description": "eu"})
	if renamed.status != 200 || renamed.body["name"] != "sales-eu" || renamed.body["description"] != "eu" {
		t.Fatalf("rename %d %v", renamed.status, renamed.raw)
	}
	expect(t, h.rest("datazone", "PATCH", base+"/"+pid, map[string]any{"glossaryTerms": []any{"term"}}), 400, "ValidationException")
	expect(t, h.rest("datazone", "DELETE", "/v2/domains/"+id, nil), 409, "ConflictException")
	expect(t, h.rest("datazone", "DELETE", "/v2/domains/"+id+"?skipDeletionCheck=true", nil), 409, "ConflictException")

	arn := str(created.body["arn"])
	expect(t, h.rest("datazone", "POST", "/tags/"+url.PathEscape(arn), map[string]any{"tags": map[string]any{"k": "v"}}), 200, "")
	expect(t, h.rest("datazone", "DELETE", "/tags/"+url.PathEscape(arn)+"?tagKeys=k", nil), 200, "")
	if tags := h.rest("datazone", "GET", "/tags/"+url.PathEscape(arn), nil).body["tags"].(map[string]any); len(tags) != 1 {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("datazone", "DELETE", base+"/"+pid+"?skipDeletionCheck=true", nil), 204, "")
	expect(t, h.rest("datazone", "GET", base+"/"+pid, nil), 404, "ResourceNotFoundException")
	deleted := h.rest("datazone", "DELETE", "/v2/domains/"+id, nil)
	if deleted.status != 202 && deleted.status != 200 || deleted.body["status"] != "DELETING" {
		t.Fatalf("delete %d %v", deleted.status, deleted.raw)
	}
	expect(t, h.rest("datazone", "GET", "/v2/domains/"+id, nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("datazone", "POST", "/v2/domains/"+id+"/glossaries", map[string]any{"name": "g", "owningProjectIdentifier": pid}), 501, "UnknownOperationException")
}

func TestEMRStoresSecurityConfigurationsAndBlockPublicAccess(t *testing.T) {
	h := newHarness(t, emr)
	doc := `{"EncryptionConfiguration":{"EnableInTransitEncryption":false,"EnableAtRestEncryption":false}}`
	created := h.json1("emr", "CreateSecurityConfiguration", map[string]any{"Name": "plain", "SecurityConfiguration": doc})
	expect(t, created, 200, "")
	if created.body["Name"] != "plain" || created.body["CreationDateTime"] == nil {
		t.Fatalf("created %v", created.body)
	}
	expect(t, h.json1("emr", "CreateSecurityConfiguration", map[string]any{"Name": "plain", "SecurityConfiguration": doc}), 400, "InvalidRequestException")
	expect(t, h.json1("emr", "CreateSecurityConfiguration", map[string]any{"Name": "bad", "SecurityConfiguration": "not json"}), 400, "InvalidRequestException")
	expect(t, h.json1("emr", "CreateSecurityConfiguration", map[string]any{"Name": "null", "SecurityConfiguration": "null"}), 400, "InvalidRequestException")
	described := h.json1("emr", "DescribeSecurityConfiguration", map[string]any{"Name": "plain"})
	if described.body["SecurityConfiguration"] != doc {
		t.Fatalf("described %v", described.body)
	}
	listed := h.json1("emr", "ListSecurityConfigurations", map[string]any{}).body["SecurityConfigurations"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["Name"] != "plain" {
		t.Fatalf("listed %v", listed)
	}
	expect(t, h.json1("emr", "DeleteSecurityConfiguration", map[string]any{"Name": "plain"}), 200, "")
	expect(t, h.json1("emr", "DescribeSecurityConfiguration", map[string]any{"Name": "plain"}), 400, "InvalidRequestException")

	defaults := h.json1("emr", "GetBlockPublicAccessConfiguration", map[string]any{})
	expect(t, defaults, 200, "")
	config := defaults.body["BlockPublicAccessConfiguration"].(map[string]any)
	if config["BlockPublicSecurityGroupRules"] != true || len(config["PermittedPublicSecurityGroupRuleRanges"].([]any)) != 1 {
		t.Fatalf("defaults %v", defaults.body)
	}
	put := map[string]any{"BlockPublicSecurityGroupRules": true, "PermittedPublicSecurityGroupRuleRanges": []any{
		map[string]any{"MinRange": 22, "MaxRange": 22}, map[string]any{"MinRange": 443}}}
	expect(t, h.json1("emr", "PutBlockPublicAccessConfiguration", map[string]any{"BlockPublicAccessConfiguration": put}), 200, "")
	stored := h.json1("emr", "GetBlockPublicAccessConfiguration", map[string]any{})
	changed, _ := Number(stored.body["BlockPublicAccessConfigurationMetadata"].(map[string]any)["CreationDateTime"])
	if len(stored.body["BlockPublicAccessConfiguration"].(map[string]any)["PermittedPublicSecurityGroupRuleRanges"].([]any)) != 2 || changed < 1 {
		t.Fatalf("stored %v", stored.body)
	}
	for _, ranges := range [][]any{
		{map[string]any{"MinRange": 30, "MaxRange": 20}},
		{map[string]any{"MinRange": 22}, map[string]any{"MinRange": 22}},
	} {
		bad := map[string]any{"BlockPublicSecurityGroupRules": true, "PermittedPublicSecurityGroupRuleRanges": ranges}
		expect(t, h.json1("emr", "PutBlockPublicAccessConfiguration", map[string]any{"BlockPublicAccessConfiguration": bad}), 400, "InvalidRequestException")
	}
	expect(t, h.json1("emr", "RunJobFlow", map[string]any{"Name": "c"}), 501, "UnknownOperationException")
	expect(t, h.json1("emr", "CreateStudio", map[string]any{"Name": "s"}), 501, "UnknownOperationException")
}

func TestEMRServerlessApplicationsAreStoredRecords(t *testing.T) {
	h := newHarness(t, emrServerless)
	created := h.rest("emr-serverless", "POST", "/applications", map[string]any{"name": "etl", "releaseLabel": "emr-7.5.0", "type": "SPARK",
		"clientToken": "t-1", "tags": map[string]any{"team": "a"}})
	expect(t, created, 200, "")
	id := str(created.body["applicationId"])
	arn := str(created.body["arn"])
	if arn != "arn:aws:emr-serverless:us-east-1:000000000000:/applications/"+id || created.body["name"] != "etl" {
		t.Fatalf("created %v", created.body)
	}
	got := h.rest("emr-serverless", "GET", "/applications/"+id, nil).body["application"].(map[string]any)
	if got["state"] != "CREATED" || got["releaseLabel"] != "emr-7.5.0" || got["tags"].(map[string]any)["team"] != "a" || got["stateDetails"] != nil {
		t.Fatalf("read %v", got)
	}
	listed := h.rest("emr-serverless", "GET", "/applications", nil).body["applications"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["id"] != id || listed[0].(map[string]any)["state"] != "CREATED" {
		t.Fatalf("listed %v", listed)
	}
	updated := h.rest("emr-serverless", "PATCH", "/applications/"+id, map[string]any{"clientToken": "t-2", "architecture": "ARM64",
		"autoStopConfiguration": map[string]any{"enabled": true, "idleTimeoutMinutes": 5}})
	expect(t, updated, 200, "")
	after := updated.body["application"].(map[string]any)
	if after["architecture"] != "ARM64" || after["name"] != "etl" || after["releaseLabel"] != "emr-7.5.0" {
		t.Fatalf("updated %v", after)
	}
	path := "/tags/" + url.PathEscape(arn)
	expect(t, h.rest("emr-serverless", "POST", path, map[string]any{"tags": map[string]any{"k": "v"}}), 200, "")
	expect(t, h.rest("emr-serverless", "DELETE", path+"?tagKeys=team", nil), 200, "")
	if tags := h.rest("emr-serverless", "GET", path, nil).body["tags"].(map[string]any); len(tags) != 1 || tags["k"] != "v" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("emr-serverless", "DELETE", "/applications/"+id, nil), 200, "")
	expect(t, h.rest("emr-serverless", "GET", "/applications/"+id, nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("emr-serverless", "POST", "/applications/"+id+"/start", nil), 501, "UnknownOperationException")
	expect(t, h.rest("emr-serverless", "POST", "/applications/"+id+"/jobruns", map[string]any{}), 501, "UnknownOperationException")
}

func TestEMRContainersStoresVirtualClustersAndTemplates(t *testing.T) {
	h := newHarness(t, emrContainers)
	cluster := h.rest("emr-containers", "POST", "/virtualclusters", map[string]any{"name": "analytics", "clientToken": "t-1",
		"containerProvider": map[string]any{"type": "EKS", "id": "my-eks", "info": map[string]any{"eksInfo": map[string]any{"namespace": "spark"}}},
		"tags":              map[string]any{"team": "a"}})
	expect(t, cluster, 200, "")
	id := str(cluster.body["id"])
	arn := str(cluster.body["arn"])
	if arn != "arn:aws:emr-containers:us-east-1:000000000000:/virtualclusters/"+id {
		t.Fatalf("cluster %v", cluster.body)
	}
	got := h.rest("emr-containers", "GET", "/virtualclusters/"+id, nil).body["virtualCluster"].(map[string]any)
	if got["state"] != "RUNNING" || got["containerProvider"].(map[string]any)["id"] != "my-eks" || got["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("read %v", got)
	}
	expect(t, h.rest("emr-containers", "POST", "/virtualclusters", map[string]any{"name": "bad", "clientToken": "t-2",
		"containerProvider": map[string]any{"type": "ECS", "id": "x"}}), 400, "ValidationException")
	listed := h.rest("emr-containers", "GET", "/virtualclusters", nil).body["virtualClusters"].([]any)
	if len(listed) != 1 {
		t.Fatalf("listed %v", listed)
	}
	if filtered := h.rest("emr-containers", "GET", "/virtualclusters?containerProviderId=other", nil).body["virtualClusters"].([]any); len(filtered) != 0 {
		t.Fatalf("filtered %v", filtered)
	}

	template := h.rest("emr-containers", "POST", "/jobtemplates", map[string]any{"name": "nightly", "clientToken": "t-3",
		"jobTemplateData": map[string]any{"executionRoleArn": "arn:aws:iam::000000000000:role/emr", "releaseLabel": "emr-6.9.0-latest",
			"jobDriver": map[string]any{"sparkSubmitJobDriver": map[string]any{"entryPoint": "s3://code/job.py"}}}})
	expect(t, template, 200, "")
	templateID := str(template.body["id"])
	if template.body["createdAt"] == nil {
		t.Fatalf("template %v", template.body)
	}
	expect(t, h.rest("emr-containers", "GET", "/jobtemplates/"+templateID, nil), 200, "")
	if templates := h.rest("emr-containers", "GET", "/jobtemplates", nil).body["templates"].([]any); len(templates) != 1 {
		t.Fatalf("templates %v", templates)
	}
	expect(t, h.rest("emr-containers", "DELETE", "/jobtemplates/"+templateID, nil), 200, "")
	expect(t, h.rest("emr-containers", "GET", "/jobtemplates/"+templateID, nil), 400, "ResourceNotFoundException")

	security := h.rest("emr-containers", "POST", "/securityconfigurations", map[string]any{"name": "lf", "clientToken": "t-4",
		"securityConfigurationData": map[string]any{"authorizationConfiguration": map[string]any{"lakeFormationConfiguration": map[string]any{"queryEngineRoleArn": "arn:aws:iam::000000000000:role/q"}}}})
	expect(t, security, 200, "")
	securityID := str(security.body["id"])
	expect(t, h.rest("emr-containers", "GET", "/securityconfigurations/"+securityID, nil), 200, "")
	if all := h.rest("emr-containers", "GET", "/securityconfigurations", nil).body["securityConfigurations"].([]any); len(all) != 1 {
		t.Fatalf("security %v", all)
	}

	path := "/tags/" + url.PathEscape(arn)
	expect(t, h.rest("emr-containers", "POST", path, map[string]any{"tags": map[string]any{"k": "v"}}), 200, "")
	expect(t, h.rest("emr-containers", "DELETE", path+"?tagKeys=team", nil), 200, "")
	if tags := h.rest("emr-containers", "GET", path, nil).body["tags"].(map[string]any); len(tags) != 1 {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.rest("emr-containers", "DELETE", "/virtualclusters/"+id, nil), 200, "")
	expect(t, h.rest("emr-containers", "GET", "/virtualclusters/"+id, nil), 400, "ResourceNotFoundException")
	expect(t, h.rest("emr-containers", "POST", "/virtualclusters/"+id+"/jobruns", map[string]any{}), 501, "UnknownOperationException")
}
