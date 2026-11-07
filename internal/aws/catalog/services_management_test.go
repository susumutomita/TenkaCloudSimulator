package catalog

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func managementHarness(t *testing.T) *harness {
	return newHarness(t, appConfig, cloudTrail, awsConfig, dataLifecycleManager, managedPrometheus, resourceGroups, serviceCatalog)
}

func faultCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	var f *fault
	if !errors.As(err, &f) {
		t.Fatalf("not a fault: %v", err)
	}
	return f.code
}

func TestManagementCheckHooksApplyDocumentedRules(t *testing.T) {
	cases := []struct {
		name  string
		check func(map[string]any) error
		input map[string]any
		code  string
	}{
		{"hosted profile", appConfigProfile, map[string]any{"LocationUri": "hosted", "Type": "AWS.AppConfig.FeatureFlags"}, ""},
		{"pipeline without role", appConfigProfile, map[string]any{"LocationUri": "codepipeline://deliver"}, ""},
		{"parameter ARN with role", appConfigProfile, map[string]any{"LocationUri": "arn:aws:ssm:us-east-1:000000000000:parameter/app", "RetrievalRoleArn": "arn:aws:iam::000000000000:role/r"}, ""},
		{"s3 without role", appConfigProfile, map[string]any{"LocationUri": "s3://bucket/key"}, "BadRequestException"},
		{"unknown location", appConfigProfile, map[string]any{"LocationUri": "ftp://host/file"}, "BadRequestException"},
		{"unknown type", appConfigProfile, map[string]any{"LocationUri": "hosted", "Type": "Custom"}, "BadRequestException"},
		{"remote feature flags", appConfigProfile, map[string]any{"LocationUri": "s3://b/k", "RetrievalRoleArn": "arn:aws:iam::000000000000:role/r", "Type": "AWS.AppConfig.FeatureFlags"}, "BadRequestException"},
		{"strategy without replication", appConfigStrategy, map[string]any{"ReplicateTo": "NONE"}, ""},
		{"strategy replicated", appConfigStrategy, map[string]any{"ReplicateTo": "SSM_DOCUMENT"}, "BadRequestException"},
		{"trail name", trailCreate, map[string]any{"Name": "audit.trail_1"}, ""},
		{"short trail name", trailCreate, map[string]any{"Name": "ab"}, "InvalidTrailNameException"},
		{"adjacent symbols", trailCreate, map[string]any{"Name": "my-_trail"}, "InvalidTrailNameException"},
		{"symbol edge", trailCreate, map[string]any{"Name": "-trail"}, "InvalidTrailNameException"},
		{"ip trail name", trailCreate, map[string]any{"Name": "192.168.5.4"}, "InvalidTrailNameException"},
		{"long trail name", trailCreate, map[string]any{"Name": strings.Repeat("a", 129)}, "InvalidTrailNameException"},
		{"organization trail", trailCreate, map[string]any{"Name": "org", "IsOrganizationTrail": true}, "OrganizationsNotInUseException"},
		{"account trail update", noOrganizationTrail, map[string]any{"IsOrganizationTrail": false}, ""},
		{"stored query", storedQueryExpression, map[string]any{"StoredQuery": map[string]any{"QueryName": "q", "Expression": "SELECT resourceId"}}, ""},
		{"stored query without expression", storedQueryExpression, map[string]any{"StoredQuery": map[string]any{"QueryName": "q"}}, "ValidationException"},
		{"default retention", retentionName, map[string]any{"RetentionConfigurationName": "default"}, ""},
		{"other retention", retentionName, map[string]any{"RetentionConfigurationName": "other"}, "NoSuchRetentionConfigurationException"},
		{"custom policy", customLifecyclePolicy, map[string]any{"PolicyDetails": map[string]any{}}, ""},
		{"default policy", customLifecyclePolicy, map[string]any{"DefaultPolicy": "VOLUME"}, "InvalidRequestException"},
		{"policy without details", customLifecyclePolicy, map[string]any{}, "InvalidRequestException"},
		{"yaml rules", prometheusRules, map[string]any{"data": []byte("# rules\ngroups:\n  - name: g\n")}, ""},
		{"json rules", prometheusRules, map[string]any{"data": []byte(`{"groups":[]}`)}, ""},
		{"no data", prometheusRules, map[string]any{}, ""},
		{"rules without groups", prometheusRules, map[string]any{"data": []byte("rules: []\n")}, "ValidationException"},
		{"binary rules", prometheusRules, map[string]any{"data": []byte{0xff, 0xfe}}, "ValidationException"},
		{"group name", groupName, map[string]any{"Name": "team-a"}, ""},
		{"reserved group name", groupName, map[string]any{"Name": "awsGroup"}, "BadRequestException"},
		{"group identifier", groupIdentifier, map[string]any{"Group": "g"}, ""},
		{"missing group identifier", groupIdentifier, map[string]any{"GroupName": "g"}, "BadRequestException"},
		{"english", englishOnly, map[string]any{"AcceptLanguage": "en"}, ""},
		{"default language", englishOnly, map[string]any{}, ""},
		{"japanese", englishOnly, map[string]any{"AcceptLanguage": "jp"}, "InvalidParametersException"},
	}
	for _, c := range cases {
		if got := faultCode(t, c.check(c.input)); got != c.code {
			t.Errorf("%s: want %q, got %q", c.name, c.code, got)
		}
	}
}

func TestAppConfigChildrenAndDeletionProtection(t *testing.T) {
	h := managementHarness(t)
	app := h.rest("appconfig", "POST", "/applications", map[string]any{"Name": "shop"})
	expect(t, app, 201, "")
	appID := str(app.body["Id"])
	env := h.rest("appconfig", "POST", "/applications/"+appID+"/environments", map[string]any{"Name": "prod"})
	expect(t, env, 201, "")
	if env.body["State"] != "READY_FOR_DEPLOYMENT" || env.body["ApplicationId"] != appID {
		t.Fatalf("environment %v", env.body)
	}
	expect(t, h.rest("appconfig", "POST", "/applications/"+appID+"/configurationprofiles", map[string]any{"Name": "p", "LocationUri": "s3://b/k"}), 400, "BadRequestException")
	expect(t, h.rest("appconfig", "DELETE", "/applications/"+appID, nil), 400, "BadRequestException")
	req := httptest.NewRequest("DELETE", "/applications/"+appID+"/environments/"+str(env.body["Id"]), nil)
	req.Header.Set("x-amzn-deletion-protection-check", "BYPASS")
	sign(req, "appconfig", "us-east-1")
	expect(t, h.do(req), 204, "")
	expect(t, h.rest("appconfig", "DELETE", "/applications/"+appID, nil), 204, "")
	expect(t, h.rest("appconfig", "POST", "/applications/"+appID+"/environments/x/deployments", map[string]any{}), 501, "UnknownOperationException")
}

func TestCloudTrailTrailsUseOfficialDefaultsAndRefusals(t *testing.T) {
	h := managementHarness(t)
	created := h.json1("cloudtrail", "CreateTrail", map[string]any{"Name": "audit", "S3BucketName": "logs", "EnableLogFileValidation": true})
	expect(t, created, 200, "")
	if created.body["LogFileValidationEnabled"] != true || created.body["IsOrganizationTrail"] != false || created.body["TrailARN"] != "arn:aws:cloudtrail:us-east-1:000000000000:trail/audit" {
		t.Fatalf("trail %v", created.body)
	}
	expect(t, h.json1("cloudtrail", "CreateTrail", map[string]any{"Name": "audit", "S3BucketName": "logs"}), 400, "TrailAlreadyExistsException")
	expect(t, h.json1("cloudtrail", "UpdateTrail", map[string]any{"Name": "audit", "IsOrganizationTrail": true}), 400, "OrganizationsNotInUseException")
	expect(t, h.json1("cloudtrail", "DescribeTrails", map[string]any{"trailNameList": []any{"audit"}}), 400, "")
	listed := h.json1("cloudtrail", "ListTrails", map[string]any{})
	if trails := listed.body["Trails"].([]any); len(trails) != 1 || trails[0].(map[string]any)["HomeRegion"] != "us-east-1" {
		t.Fatalf("list %v", listed.body)
	}
	expect(t, h.json1("cloudtrail", "StartLogging", map[string]any{"Name": "audit"}), 501, "UnknownOperationException")
	expect(t, h.json1("cloudtrail", "DeleteTrail", map[string]any{"Name": created.body["TrailARN"]}), 200, "")
	expect(t, h.json1("cloudtrail", "GetTrail", map[string]any{"Name": "audit"}), 400, "TrailNotFoundException")
}

func TestConfigStoredQueriesAndDefaultRetention(t *testing.T) {
	h := managementHarness(t)
	put := h.json1("config", "PutStoredQuery", map[string]any{"StoredQuery": map[string]any{"QueryName": "buckets", "Expression": "SELECT resourceId"}})
	expect(t, put, 200, "")
	if arn := str(put.body["QueryArn"]); !compiledPattern(h.binding("config").model.shape("QueryArn").Pattern).MatchString(arn) {
		t.Fatalf("query ARN %s does not match the official pattern", arn)
	}
	expect(t, h.json1("config", "PutStoredQuery", map[string]any{"StoredQuery": map[string]any{"QueryName": "buckets", "Expression": "SELECT 1"}}), 400, "ValidationException")
	if got := h.json1("config", "DescribeRetentionConfigurations", map[string]any{}); len(got.body["RetentionConfigurations"].([]any)) != 0 {
		t.Fatalf("retention before put %v", got.body)
	}
	expect(t, h.json1("config", "DescribeRetentionConfigurations", map[string]any{"RetentionConfigurationNames": []any{"default"}}), 400, "NoSuchRetentionConfigurationException")
	retained := h.json1("config", "PutRetentionConfiguration", map[string]any{"RetentionPeriodInDays": 90})
	expect(t, retained, 200, "")
	if rc := retained.body["RetentionConfiguration"].(map[string]any); rc["Name"] != "default" || asText(rc["RetentionPeriodInDays"]) != "90" {
		t.Fatalf("retention %v", rc)
	}
	expect(t, h.json1("config", "PutRetentionConfiguration", map[string]any{"RetentionPeriodInDays": 7}), 400, "InvalidParameterValueException")
	expect(t, h.json1("config", "DeleteRetentionConfiguration", map[string]any{"RetentionConfigurationName": "other"}), 400, "NoSuchRetentionConfigurationException")
	expect(t, h.json1("config", "DeleteRetentionConfiguration", map[string]any{"RetentionConfigurationName": "default"}), 200, "")
	expect(t, h.json1("config", "PutConfigurationRecorder", map[string]any{"ConfigurationRecorder": map[string]any{"name": "default"}}), 501, "UnknownOperationException")
}

func TestLifecyclePoliciesPrometheusGroupsAndPortfolios(t *testing.T) {
	h := managementHarness(t)
	policy := h.rest("dlm", "POST", "/policies", map[string]any{"ExecutionRoleArn": "arn:aws:iam::000000000000:role/dlm", "Description": "daily", "State": "ENABLED",
		"PolicyDetails": map[string]any{"PolicyType": "EBS_SNAPSHOT_MANAGEMENT"}})
	expect(t, policy, 200, "")
	got := h.rest("dlm", "GET", "/policies/"+str(policy.body["PolicyId"]), nil).body["Policy"].(map[string]any)
	if !compiledPattern(h.binding("dlm").model.shape("PolicyArn").Pattern).MatchString(str(got["PolicyArn"])) || got["DefaultPolicy"] != false {
		t.Fatalf("policy %v", got)
	}
	expect(t, h.rest("dlm", "POST", "/policies", map[string]any{"ExecutionRoleArn": "arn:aws:iam::000000000000:role/dlm", "Description": "d", "State": "ENABLED", "CreateInterval": 1}), 400, "InvalidRequestException")

	workspace := h.rest("amp", "POST", "/workspaces", map[string]any{"alias": "metrics", "clientToken": "t-1"})
	expect(t, workspace, 202, "")
	id := str(workspace.body["workspaceId"])
	expect(t, h.rest("amp", "POST", "/workspaces/"+id+"/rulegroupsnamespaces", map[string]any{"name": "rules", "data": "Z3JvdXBzOiBbXQo=", "clientToken": "t-2"}), 202, "")
	expect(t, h.rest("amp", "POST", "/workspaces/"+id+"/alias", map[string]any{"alias": "renamed", "clientToken": "t-3"}), 204, "")
	expect(t, h.rest("amp", "DELETE", "/workspaces/"+id+"?clientToken=t-4", nil), 202, "")
	var rows int
	_ = h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources WHERE service='amp'").Scan(&rows)
	if rows != 0 {
		t.Fatalf("workspace deletion left %d rows", rows)
	}

	expect(t, h.rest("resource-groups", "POST", "/groups", map[string]any{"Name": "team"}), 200, "")
	listed := h.rest("resource-groups", "POST", "/groups-list", map[string]any{})
	if ids := listed.body["GroupIdentifiers"].([]any); len(ids) != 1 || ids[0].(map[string]any)["GroupName"] != "team" {
		t.Fatalf("groups %v", listed.body)
	}
	expect(t, h.rest("resource-groups", "POST", "/groups-list", map[string]any{"Filters": []any{map[string]any{"Name": "owner", "Values": []any{"x"}}}}), 400, "BadRequestException")
	expect(t, h.rest("resource-groups", "POST", "/get-group", map[string]any{}), 400, "BadRequestException")

	portfolio := h.json1("servicecatalog", "CreatePortfolio", map[string]any{"DisplayName": "P", "ProviderName": "Team", "IdempotencyToken": "token-1", "AcceptLanguage": "en"})
	expect(t, portfolio, 200, "")
	detail := portfolio.body["PortfolioDetail"].(map[string]any)
	if !strings.HasPrefix(str(detail["Id"]), "port-") || detail["ARN"] != "arn:aws:catalog:us-east-1:000000000000:portfolio/"+str(detail["Id"]) {
		t.Fatalf("portfolio %v", detail)
	}
	expect(t, h.json1("servicecatalog", "CreatePortfolio", map[string]any{"DisplayName": "P", "ProviderName": "Team", "IdempotencyToken": "token-2", "AcceptLanguage": "zh"}), 400, "InvalidParametersException")
	expect(t, h.json1("servicecatalog", "UpdatePortfolio", map[string]any{"Id": detail["Id"], "AddTags": []any{map[string]any{"Key": "k", "Value": "v"}}}), 400, "")
	expect(t, h.json1("servicecatalog", "DeletePortfolio", map[string]any{"Id": detail["Id"]}), 200, "")
}
