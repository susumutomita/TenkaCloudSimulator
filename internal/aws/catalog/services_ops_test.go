package catalog

import (
	"net/url"
	"strings"
	"testing"
)

func TestOpsCheckHooksApplyDocumentedRules(t *testing.T) {
	alarm := map[string]any{"source": "aws:cloudwatch:alarm", "value": "arn:aws:cloudwatch:us-east-1:000000000000:alarm:stop"}
	cases := []struct {
		name  string
		check func(map[string]any) error
		input map[string]any
		code  string
	}{
		{"own account", standaloneAccount, map[string]any{"AlternateContactType": "BILLING"}, ""},
		{"member account", standaloneAccount, map[string]any{"AccountId": "000000000000"}, "ValidationException"},
		{"policy update without targets", resiliencyPolicyTargets, map[string]any{"tier": "Critical"}, ""},
		{"policy without AZ", resiliencyPolicyTargets, map[string]any{"policy": map[string]any{"Software": map[string]any{}, "Hardware": map[string]any{}}}, "ValidationException"},
		{"complete policy", resiliencyPolicyTargets, map[string]any{"policy": map[string]any{"Software": map[string]any{}, "Hardware": map[string]any{}, "AZ": map[string]any{}}}, ""},
		{"disabled schedule", unscheduledAssessments, map[string]any{"assessmentSchedule": "Disabled"}, ""},
		{"daily schedule", unscheduledAssessments, map[string]any{"assessmentSchedule": "Daily"}, "ValidationException"},
		{"account view", accountScopeView, map[string]any{"ViewName": "v"}, ""},
		{"organization view", accountScopeView, map[string]any{"Scope": "arn:aws:organizations::000000000000:organization/o-1"}, "ValidationException"},
		{"plan without integrations", noIncidentIntegrations, map[string]any{"name": "p"}, ""},
		{"pagerduty plan", noIncidentIntegrations, map[string]any{"integrations": []any{}}, "ValidationException"},
		{"disabled function package", disabledPackage, map[string]any{"operationalState": "DISABLED"}, ""},
		{"enabled function package", disabledPackage, map[string]any{"operationalState": "ENABLED"}, "ValidationException"},
		{"enabled network package", disabledPackage, map[string]any{"nsdOperationalState": "ENABLED"}, "ValidationException"},
		{"plain workload", workloadIntegrations, map[string]any{"WorkloadName": "w"}, ""},
		{"jira workload", workloadIntegrations, map[string]any{"JiraConfiguration": map[string]any{}}, "ValidationException"},
		{"trusted advisor workload", workloadIntegrations, map[string]any{"DiscoveryConfig": map[string]any{"TrustedAdvisorIntegrationStatus": "ENABLED"}}, "ValidationException"},
		{"discovery disabled", workloadIntegrations, map[string]any{"DiscoveryConfig": map[string]any{"TrustedAdvisorIntegrationStatus": "DISABLED"}}, ""},
		{"disabled settings", standaloneSettings, map[string]any{"OrganizationSharingStatus": "DISABLED", "DiscoveryIntegrationStatus": "DISABLED"}, ""},
		{"jira settings", standaloneSettings, map[string]any{"JiraConfiguration": map[string]any{}}, "ValidationException"},
		{"organization sharing", standaloneSettings, map[string]any{"OrganizationSharingStatus": "ENABLED"}, "ValidationException"},
		{"discovery settings", standaloneSettings, map[string]any{"DiscoveryIntegrationStatus": "ENABLED"}, "ValidationException"},
		{"cost budget", budgetDefinition, map[string]any{"Budget": map[string]any{"BudgetType": "COST", "BudgetLimit": map[string]any{}}}, ""},
		{"planned usage budget", budgetDefinition, map[string]any{"NewBudget": map[string]any{"BudgetType": "USAGE", "PlannedBudgetLimits": map[string]any{}}}, ""},
		{"coverage budget", budgetDefinition, map[string]any{"Budget": map[string]any{"BudgetType": "RI_COVERAGE"}}, ""},
		{"calculated spend", budgetDefinition, map[string]any{"Budget": map[string]any{"CalculatedSpend": map[string]any{}}}, "InvalidParameterException"},
		{"updated health", budgetDefinition, map[string]any{"NewBudget": map[string]any{"HealthStatus": map[string]any{}}}, "InvalidParameterException"},
		{"two limits", budgetDefinition, map[string]any{"Budget": map[string]any{"BudgetType": "COST", "BudgetLimit": map[string]any{}, "PlannedBudgetLimits": map[string]any{}}}, "InvalidParameterException"},
		{"cost without limit", budgetDefinition, map[string]any{"Budget": map[string]any{"BudgetType": "COST"}}, "InvalidParameterException"},
		{"global markup", pricingRuleScope, map[string]any{"Scope": "GLOBAL", "Type": "MARKUP", "ModifierPercentage": 5.0}, ""},
		{"sku discount", pricingRuleScope, map[string]any{"Scope": "SKU", "Type": "DISCOUNT", "ModifierPercentage": 5.0, "Service": "AmazonEC2", "UsageType": "BoxUsage", "Operation": "RunInstances"}, ""},
		{"global tiering", pricingRuleScope, map[string]any{"Scope": "GLOBAL", "Type": "TIERING", "Tiering": map[string]any{}}, ""},
		{"service without service", pricingRuleScope, map[string]any{"Scope": "SERVICE", "Type": "MARKUP", "ModifierPercentage": 5.0}, "ValidationException"},
		{"entity without entity", pricingRuleScope, map[string]any{"Scope": "BILLING_ENTITY", "Type": "MARKUP", "ModifierPercentage": 5.0}, "ValidationException"},
		{"sku without operation", pricingRuleScope, map[string]any{"Scope": "SKU", "Type": "MARKUP", "ModifierPercentage": 5.0, "Service": "AmazonEC2", "UsageType": "BoxUsage"}, "ValidationException"},
		{"global with service", pricingRuleScope, map[string]any{"Scope": "GLOBAL", "Type": "MARKUP", "ModifierPercentage": 5.0, "Service": "AmazonEC2"}, "ValidationException"},
		{"tiering without tiering", pricingRuleScope, map[string]any{"Scope": "GLOBAL", "Type": "TIERING"}, "ValidationException"},
		{"tiering with modifier", pricingRuleScope, map[string]any{"Scope": "GLOBAL", "Type": "TIERING", "Tiering": map[string]any{}, "ModifierPercentage": 5.0}, "ValidationException"},
		{"markup without modifier", pricingRuleScope, map[string]any{"Scope": "GLOBAL", "Type": "MARKUP"}, "ValidationException"},
		{"markup with tiering", pricingRuleScope, map[string]any{"Scope": "GLOBAL", "Type": "MARKUP", "ModifierPercentage": 5.0, "Tiering": map[string]any{}}, "ValidationException"},
		{"modifier update", pricingRuleModifier, map[string]any{"ModifierPercentage": 5.0}, ""},
		{"modifier and tiering update", pricingRuleModifier, map[string]any{"ModifierPercentage": 5.0, "Tiering": map[string]any{}}, "ValidationException"},
		{"csv report", reportDefinition, map[string]any{"ReportDefinition": map[string]any{"Format": "textORcsv", "Compression": "GZIP", "AdditionalArtifacts": []any{"REDSHIFT", "QUICKSIGHT"}}}, ""},
		{"athena report", reportDefinition, map[string]any{"ReportDefinition": map[string]any{"Format": "Parquet", "Compression": "Parquet", "ReportVersioning": "OVERWRITE_REPORT", "AdditionalArtifacts": []any{"ATHENA"}}}, ""},
		{"report status", reportDefinition, map[string]any{"ReportDefinition": map[string]any{"ReportStatus": map[string]any{}}}, "ValidationException"},
		{"billing view report", reportDefinition, map[string]any{"ReportDefinition": map[string]any{"BillingViewArn": "x"}}, "ValidationException"},
		{"parquet zip", reportDefinition, map[string]any{"ReportDefinition": map[string]any{"Format": "Parquet", "Compression": "ZIP"}}, "ValidationException"},
		{"csv parquet", reportDefinition, map[string]any{"ReportDefinition": map[string]any{"Format": "textORcsv", "Compression": "Parquet"}}, "ValidationException"},
		{"athena with redshift", reportDefinition, map[string]any{"ReportDefinition": map[string]any{"Format": "Parquet", "Compression": "Parquet", "ReportVersioning": "OVERWRITE_REPORT", "AdditionalArtifacts": []any{"ATHENA", "REDSHIFT"}}}, "ValidationException"},
		{"athena new versions", reportDefinition, map[string]any{"ReportDefinition": map[string]any{"Format": "Parquet", "Compression": "Parquet", "ReportVersioning": "CREATE_NEW_REPORT", "AdditionalArtifacts": []any{"ATHENA"}}}, "ValidationException"},
		{"redshift parquet", reportDefinition, map[string]any{"ReportDefinition": map[string]any{"Format": "Parquet", "Compression": "Parquet", "AdditionalArtifacts": []any{"REDSHIFT"}}}, "ValidationException"},
		{"same report name", reportModification, map[string]any{"ReportName": "r", "ReportDefinition": map[string]any{"ReportName": "r", "Format": "textORcsv", "Compression": "ZIP"}}, ""},
		{"renamed report", reportModification, map[string]any{"ReportName": "r", "ReportDefinition": map[string]any{"ReportName": "s", "Format": "textORcsv", "Compression": "ZIP"}}, "ValidationException"},
		{"modified report rules", reportModification, map[string]any{"ReportName": "r", "ReportDefinition": map[string]any{"ReportName": "r", "Format": "Parquet", "Compression": "GZIP"}}, "ValidationException"},
		{"single-account template", experimentTemplate, map[string]any{"experimentOptions": map[string]any{"accountTargeting": "single-account"}, "stopConditions": []any{map[string]any{"source": "none"}, alarm}}, ""},
		{"multi-account template", experimentTemplate, map[string]any{"experimentOptions": map[string]any{"accountTargeting": "multi-account"}}, "ValidationException"},
		{"none with value", experimentTemplate, map[string]any{"stopConditions": []any{map[string]any{"source": "none", "value": alarm["value"]}}}, "ValidationException"},
		{"alarm without value", experimentTemplate, map[string]any{"stopConditions": []any{map[string]any{"source": "aws:cloudwatch:alarm"}}}, "ValidationException"},
		{"unknown source", experimentTemplate, map[string]any{"stopConditions": []any{map[string]any{"source": "aws:sns:topic"}}}, "ValidationException"},
		{"cloud9 alias", cloud9Environment, map[string]any{"ownerArn": "arn:aws:iam::000000000000:user/dev", "imageId": "amazonlinux-2023-x86_64"}, ""},
		{"cloud9 ssm path", cloud9Environment, map[string]any{"ownerArn": "arn:aws:iam::000000000000:user/dev", "imageId": "resolve:ssm:/aws/service/cloud9/amis/ubuntu-22.04-x86_64"}, ""},
		{"cloud9 without owner", cloud9Environment, map[string]any{"imageId": "amazonlinux-2023-x86_64"}, "BadRequestException"},
		{"cloud9 retired image", cloud9Environment, map[string]any{"ownerArn": "arn:aws:iam::000000000000:user/dev", "imageId": "ubuntu-18.04-x86_64"}, "BadRequestException"},
	}
	for _, c := range cases {
		if got := faultCode(t, c.check(c.input)); got != c.code {
			t.Errorf("%s: want %q, got %q", c.name, c.code, got)
		}
	}
}

func opsHarness(t *testing.T) *harness {
	return newHarness(t, accountManagement, billingConductor, budgets, chatbot, cloud9, costUsageReports, faultInjection, incidentManager, licenseManager,
		proton, resilienceHub, resourceExplorer, telcoNetworkBuilder, userNotifications, wellArchitected, xray)
}

func opsRows(t *testing.T, h *harness, service string) int {
	t.Helper()
	var rows int
	if err := h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources WHERE service=?", service).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestAccountAlternateContactsAreKeyedByType(t *testing.T) {
	h := opsHarness(t)
	contact := map[string]any{"AlternateContactType": "SECURITY", "EmailAddress": "sec@example.com", "Name": "Sec", "PhoneNumber": "+1 555 0100", "Title": "CISO"}
	expect(t, h.rest("account", "POST", "/getAlternateContact", map[string]any{"AlternateContactType": "SECURITY"}), 404, "ResourceNotFoundException")
	expect(t, h.rest("account", "POST", "/putAlternateContact", contact), 200, "")
	contact["Title"] = "Security lead"
	expect(t, h.rest("account", "POST", "/putAlternateContact", contact), 200, "")
	got := h.rest("account", "POST", "/getAlternateContact", map[string]any{"AlternateContactType": "SECURITY"})
	expect(t, got, 200, "")
	if c := got.body["AlternateContact"].(map[string]any); c["Title"] != "Security lead" || c["AlternateContactType"] != "SECURITY" {
		t.Fatalf("alternate contact %v", c)
	}
	if !strings.Contains(strings.Join(h.policy.calls, "\n"), "account:GetAlternateContact arn:aws:account::000000000000:account/alternate-contact/SECURITY") {
		t.Fatalf("authorization resource %v", h.policy.calls)
	}
	expect(t, h.rest("account", "POST", "/getAlternateContact", map[string]any{"AlternateContactType": "SECURITY", "AccountId": "000000000000"}), 400, "ValidationException")
	expect(t, h.rest("account", "POST", "/getContactInformation", map[string]any{}), 501, "UnknownOperationException")
	expect(t, h.rest("account", "POST", "/deleteAlternateContact", map[string]any{"AlternateContactType": "SECURITY"}), 200, "")
	expect(t, h.rest("account", "POST", "/deleteAlternateContact", map[string]any{"AlternateContactType": "SECURITY"}), 404, "ResourceNotFoundException")
	if opsRows(t, h, "account") != 0 {
		t.Fatal("deleted contact left a row")
	}
}

func TestChatbotWebhooksCustomActionsAndPreferences(t *testing.T) {
	h := opsHarness(t)
	webhook := map[string]any{"ConfigurationName": "alerts", "WebhookDescription": "ops room", "WebhookUrl": "https://hooks.chime.aws/incomingwebhooks/abc?token=x",
		"SnsTopicArns": []any{"arn:aws:sns:us-east-1:000000000000:alerts"}, "IamRoleArn": "arn:aws:iam::000000000000:role/chatbot", "Tags": []any{map[string]any{"TagKey": "team", "TagValue": "a"}}}
	created := h.rest("chatbot", "POST", "/create-chime-webhook-configuration", webhook)
	expect(t, created, 201, "")
	config := created.body["WebhookConfiguration"].(map[string]any)
	arn := str(config["ChatConfigurationArn"])
	if arn != "arn:aws:chatbot::000000000000:chat-configuration/chime-webhook/alerts" || config["State"] != "ENABLED" || config["Tags"].([]any)[0].(map[string]any)["TagKey"] != "team" {
		t.Fatalf("webhook %v", config)
	}
	if _, leaked := config["WebhookUrl"]; leaked {
		t.Fatal("the webhook URL must never be returned")
	}
	expect(t, h.rest("chatbot", "POST", "/create-chime-webhook-configuration", webhook), 409, "ConflictException")
	expect(t, h.rest("chatbot", "POST", "/update-chime-webhook-configuration", map[string]any{"ChatConfigurationArn": arn, "LoggingLevel": "ERROR", "WebhookUrl": "https://hooks.chime.aws/incomingwebhooks/def?token=y"}), 200, "")
	described := h.rest("chatbot", "POST", "/describe-chime-webhook-configurations", map[string]any{"ChatConfigurationArn": arn})
	if list := described.body["WebhookConfigurations"].([]any); len(list) != 1 || list[0].(map[string]any)["LoggingLevel"] != "ERROR" {
		t.Fatalf("describe %v", described.body)
	}
	action := h.rest("chatbot", "POST", "/create-custom-action", map[string]any{"ActionName": "restart", "Definition": map[string]any{"CommandText": "lambda invoke restart"}, "ClientToken": strings.Repeat("t", 33)})
	expect(t, action, 201, "")
	actionArn := str(action.body["CustomActionArn"])
	if actionArn != "arn:aws:chatbot::000000000000:custom-action/restart" {
		t.Fatalf("custom action %v", action.body)
	}
	if listed := h.rest("chatbot", "POST", "/list-custom-actions", map[string]any{}); listed.body["CustomActions"].([]any)[0] != actionArn {
		t.Fatalf("custom actions %v", listed.body)
	}
	expect(t, h.rest("chatbot", "POST", "/update-custom-action", map[string]any{"CustomActionArn": actionArn, "Definition": map[string]any{"CommandText": "lambda invoke restart-v2"}, "AliasName": "rs"}), 200, "")
	if got := h.rest("chatbot", "POST", "/get-custom-action", map[string]any{"CustomActionArn": actionArn}).body["CustomAction"].(map[string]any); got["AliasName"] != "rs" {
		t.Fatalf("custom action update %v", got)
	}
	if prefs := h.rest("chatbot", "POST", "/get-account-preferences", nil); len(prefs.body["AccountPreferences"].(map[string]any)) != 0 {
		t.Fatalf("unset preferences must be omitted: %v", prefs.body)
	}
	expect(t, h.rest("chatbot", "POST", "/update-account-preferences", map[string]any{"UserAuthorizationRequired": true}), 200, "")
	if prefs := h.rest("chatbot", "POST", "/get-account-preferences", nil).body["AccountPreferences"].(map[string]any); prefs["UserAuthorizationRequired"] != true {
		t.Fatalf("preferences %v", prefs)
	}
	expect(t, h.rest("chatbot", "POST", "/create-slack-channel-configuration", map[string]any{}), 501, "UnknownOperationException")
	expect(t, h.rest("chatbot", "POST", "/delete-chime-webhook-configuration", map[string]any{"ChatConfigurationArn": arn}), 204, "")
	expect(t, h.rest("chatbot", "POST", "/delete-custom-action", map[string]any{"CustomActionArn": actionArn}), 204, "")
	expect(t, h.rest("chatbot", "POST", "/get-custom-action", map[string]any{"CustomActionArn": actionArn}), 404, "ResourceNotFoundException")
}

func TestLicenseProtonAndCloud9JSONLifecycles(t *testing.T) {
	h := opsHarness(t)
	created := h.json1("license-manager", "CreateLicenseConfiguration", map[string]any{"Name": "sql", "LicenseCountingType": "vCPU", "LicenseCount": 8, "Tags": []any{map[string]any{"Key": "team", "Value": "a"}}})
	expect(t, created, 200, "")
	arn := str(created.body["LicenseConfigurationArn"])
	if !compiledPattern(`^arn:aws:license-manager:us-east-1:000000000000:license-configuration:lic-[0-9a-f]{32}$`).MatchString(arn) {
		t.Fatalf("license configuration ARN %s", arn)
	}
	expect(t, h.json1("license-manager", "UpdateLicenseConfiguration", map[string]any{"LicenseConfigurationArn": arn, "LicenseConfigurationStatus": "DISABLED"}), 200, "")
	got := h.json1("license-manager", "GetLicenseConfiguration", map[string]any{"LicenseConfigurationArn": arn})
	if got.body["Status"] != "DISABLED" || asText(got.body["ConsumedLicenses"]) != "0" || got.body["OwnerAccountId"] != "000000000000" || got.body["Tags"].([]any)[0].(map[string]any)["Key"] != "team" {
		t.Fatalf("license configuration %v", got.body)
	}
	expect(t, h.json1("license-manager", "ListLicenseConfigurations", map[string]any{"LicenseConfigurationArns": []any{arn + "0"}}), 400, "InvalidParameterValueException")
	expect(t, h.json1("license-manager", "CreateLicense", map[string]any{}), 501, "UnknownOperationException")
	expect(t, h.json1("license-manager", "DeleteLicenseConfiguration", map[string]any{"LicenseConfigurationArn": arn}), 200, "")

	template := h.json1("proton", "CreateEnvironmentTemplate", map[string]any{"name": "vpc", "displayName": "VPC"})
	expect(t, template, 200, "")
	if tpl := template.body["environmentTemplate"].(map[string]any); tpl["arn"] != "arn:aws:proton:us-east-1:000000000000:environment-template/vpc" || tpl["createdAt"] == nil {
		t.Fatalf("environment template %v", tpl)
	}
	h.now = h.now.Add(60e9)
	updated := h.json1("proton", "UpdateEnvironmentTemplate", map[string]any{"name": "vpc", "description": "network"})
	if tpl := updated.body["environmentTemplate"].(map[string]any); tpl["description"] != "network" || asText(tpl["lastModifiedAt"]) == asText(tpl["createdAt"]) {
		t.Fatalf("template update %v", tpl)
	}
	expect(t, h.json1("proton", "CreateEnvironmentTemplateVersion", map[string]any{}), 501, "UnknownOperationException")
	deleted := h.json1("proton", "DeleteEnvironmentTemplate", map[string]any{"name": "vpc"})
	if deleted.body["environmentTemplate"].(map[string]any)["name"] != "vpc" {
		t.Fatalf("deleted template %v", deleted.body)
	}

	expect(t, h.json1("cloud9", "CreateEnvironmentEC2", map[string]any{"name": "ide", "instanceType": "t3.small", "imageId": "amazonlinux-2023-x86_64"}), 400, "BadRequestException")
	env := h.json1("cloud9", "CreateEnvironmentEC2", map[string]any{"name": "ide", "instanceType": "t3.small", "imageId": "amazonlinux-2023-x86_64", "ownerArn": "arn:aws:iam::000000000000:user/dev", "automaticStopTimeMinutes": 30})
	expect(t, env, 200, "")
	id := str(env.body["environmentId"])
	described := h.json1("cloud9", "DescribeEnvironments", map[string]any{"environmentIds": []any{id}}).body["environments"].([]any)[0].(map[string]any)
	if described["arn"] != "arn:aws:cloud9:us-east-1:000000000000:environment:"+id || described["type"] != "ec2" || described["lifecycle"].(map[string]any)["status"] != "CREATED" {
		t.Fatalf("environment %v", described)
	}
	if _, leaked := described["instanceType"]; leaked {
		t.Fatal("instance parameters are not part of the environment description")
	}
	expect(t, h.json1("cloud9", "CreateEnvironmentEC2", map[string]any{"name": "dry", "instanceType": "t3.small", "imageId": "amazonlinux-2023-x86_64", "ownerArn": "arn:aws:iam::000000000000:user/dev", "dryRun": true}), 400, "BadRequestException")
	expect(t, h.json1("cloud9", "DescribeEnvironmentStatus", map[string]any{"environmentId": id}), 501, "UnknownOperationException")
	expect(t, h.json1("cloud9", "DeleteEnvironment", map[string]any{"environmentId": id}), 200, "")
}

func TestResilienceHubPoliciesAndApps(t *testing.T) {
	h := opsHarness(t)
	targets := map[string]any{"rpoInSecs": 300, "rtoInSecs": 600}
	policy := h.rest("resiliencehub", "POST", "/create-resiliency-policy", map[string]any{"policyName": "gold", "tier": "Critical",
		"policy": map[string]any{"Software": targets, "Hardware": targets, "AZ": targets}, "tags": map[string]any{"team": "a"}})
	expect(t, policy, 200, "")
	policyArn := str(policy.body["policy"].(map[string]any)["policyArn"])
	if policy.body["policy"].(map[string]any)["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("policy %v", policy.body)
	}
	expect(t, h.rest("resiliencehub", "POST", "/create-app", map[string]any{"name": "shop", "policyArn": policyArn + "0"}), 404, "ResourceNotFoundException")
	expect(t, h.rest("resiliencehub", "POST", "/create-app", map[string]any{"name": "shop", "assessmentSchedule": "Daily"}), 400, "ValidationException")
	app := h.rest("resiliencehub", "POST", "/create-app", map[string]any{"name": "shop", "policyArn": policyArn, "assessmentSchedule": "Disabled"})
	expect(t, app, 200, "")
	appArn := str(app.body["app"].(map[string]any)["appArn"])
	if app.body["app"].(map[string]any)["status"] != "Active" || !strings.HasPrefix(appArn, "arn:aws:resiliencehub:us-east-1:000000000000:app/") {
		t.Fatalf("app %v", app.body)
	}
	if listed := h.rest("resiliencehub", "GET", "/list-apps?name=shop", nil); len(listed.body["appSummaries"].([]any)) != 1 {
		t.Fatalf("apps %v", listed.body)
	}
	if listed := h.rest("resiliencehub", "GET", "/list-resiliency-policies?policyName=other", nil); len(listed.body["resiliencyPolicies"].([]any)) != 0 {
		t.Fatalf("policies %v", listed.body)
	}
	expect(t, h.rest("resiliencehub", "POST", "/update-app", map[string]any{"appArn": appArn, "clearResiliencyPolicyArn": true}), 400, "ValidationException")
	expect(t, h.rest("resiliencehub", "POST", "/start-app-assessment", map[string]any{"appArn": appArn, "appVersion": "release", "assessmentName": "a"}), 501, "UnknownOperationException")
	expect(t, h.rest("resiliencehub", "POST", "/delete-app", map[string]any{"appArn": appArn, "forceDelete": true}), 200, "")
	expect(t, h.rest("resiliencehub", "POST", "/delete-resiliency-policy", map[string]any{"policyArn": policyArn}), 200, "")
}

func TestResourceExplorerIndexesAndViews(t *testing.T) {
	h := opsHarness(t)
	index := h.rest("resource-explorer-2", "POST", "/CreateIndex", map[string]any{"Tags": map[string]any{"team": "a"}})
	expect(t, index, 200, "")
	indexArn := str(index.body["Arn"])
	if index.body["State"] != "ACTIVE" || !strings.HasPrefix(indexArn, "arn:aws:resource-explorer-2:us-east-1:000000000000:index/") {
		t.Fatalf("index %v", index.body)
	}
	expect(t, h.rest("resource-explorer-2", "POST", "/CreateIndex", map[string]any{}), 400, "ValidationException")
	if listed := h.rest("resource-explorer-2", "POST", "/ListIndexes", map[string]any{"Type": "LOCAL"}).body["Indexes"].([]any); len(listed) != 1 || listed[0].(map[string]any)["Region"] != "us-east-1" {
		t.Fatalf("indexes %v", listed)
	}
	if updated := h.rest("resource-explorer-2", "POST", "/UpdateIndexType", map[string]any{"Arn": indexArn, "Type": "AGGREGATOR"}); updated.body["Type"] != "AGGREGATOR" {
		t.Fatalf("index type %v", updated.body)
	}
	expect(t, h.rest("resource-explorer-2", "POST", "/CreateView", map[string]any{"ViewName": "all", "Scope": "arn:aws:organizations::000000000000:organization/o-1"}), 400, "ValidationException")
	view := h.rest("resource-explorer-2", "POST", "/CreateView", map[string]any{"ViewName": "all", "Filters": map[string]any{"FilterString": "service:s3"}})
	expect(t, view, 200, "")
	detail := view.body["View"].(map[string]any)
	viewArn := str(detail["ViewArn"])
	if !strings.HasPrefix(viewArn, "arn:aws:resource-explorer-2:us-east-1:000000000000:view/all/") || detail["Scope"] != "arn:aws:iam::000000000000:root" || detail["Owner"] != "000000000000" {
		t.Fatalf("view %v", detail)
	}
	batch := h.rest("resource-explorer-2", "POST", "/BatchGetView", map[string]any{"ViewArns": []any{viewArn, viewArn + "x"}})
	if len(batch.body["Views"].([]any)) != 1 || batch.body["Errors"].([]any)[0].(map[string]any)["ViewArn"] != viewArn+"x" {
		t.Fatalf("batch %v", batch.body)
	}
	expect(t, h.rest("resource-explorer-2", "POST", "/UpdateView", map[string]any{"ViewArn": viewArn, "Filters": map[string]any{"FilterString": "region:us-east-1"}}), 200, "")
	got := h.rest("resource-explorer-2", "POST", "/GetView", map[string]any{"ViewArn": viewArn})
	if got.body["View"].(map[string]any)["Filters"].(map[string]any)["FilterString"] != "region:us-east-1" {
		t.Fatalf("view update %v", got.body)
	}
	expect(t, h.rest("resource-explorer-2", "POST", "/Search", map[string]any{"QueryString": "*"}), 501, "UnknownOperationException")
	expect(t, h.rest("resource-explorer-2", "POST", "/DeleteView", map[string]any{"ViewArn": viewArn}), 200, "")
	if deleted := h.rest("resource-explorer-2", "POST", "/DeleteIndex", map[string]any{"Arn": indexArn}); deleted.body["State"] != "DELETED" {
		t.Fatalf("deleted index %v", deleted.body)
	}
}

func TestIncidentPlansPackagesNotificationsAndGroups(t *testing.T) {
	h := opsHarness(t)
	plan := h.rest("ssm-incidents", "POST", "/createResponsePlan", map[string]any{"name": "sev2", "incidentTemplate": map[string]any{"title": "Outage", "impact": 2}})
	expect(t, plan, 201, "")
	planArn := str(plan.body["arn"])
	if planArn != "arn:aws:ssm-incidents::000000000000:response-plan/sev2" {
		t.Fatalf("response plan %v", plan.body)
	}
	expect(t, h.rest("ssm-incidents", "POST", "/updateResponsePlan", map[string]any{"arn": planArn, "displayName": "Severity 2", "clientToken": "t"}), 204, "")
	expect(t, h.rest("ssm-incidents", "POST", "/updateResponsePlan", map[string]any{"arn": planArn, "incidentTemplateTitle": "x"}), 400, "ValidationException")
	if got := h.rest("ssm-incidents", "GET", "/getResponsePlan?arn="+url.QueryEscape(planArn), nil); got.body["displayName"] != "Severity 2" || got.body["incidentTemplate"].(map[string]any)["title"] != "Outage" {
		t.Fatalf("response plan %v", got.body)
	}
	expect(t, h.rest("ssm-incidents", "POST", "/startIncident", map[string]any{"responsePlanArn": planArn}), 501, "UnknownOperationException")
	expect(t, h.rest("ssm-incidents", "POST", "/deleteResponsePlan", map[string]any{"arn": planArn}), 204, "")
	expect(t, h.rest("ssm-incidents", "POST", "/deleteResponsePlan", map[string]any{"arn": planArn}), 204, "")

	pkg := h.rest("tnb", "POST", "/sol/vnfpkgm/v1/vnf_packages", map[string]any{"tags": map[string]any{"team": "a"}})
	expect(t, pkg, 201, "")
	id := str(pkg.body["id"])
	if !compiledPattern(`^fp-[a-f0-9]{17}$`).MatchString(id) || pkg.body["operationalState"] != "DISABLED" || pkg.body["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("function package %v", pkg.body)
	}
	if got := h.rest("tnb", "GET", "/sol/vnfpkgm/v1/vnf_packages/"+id, nil); got.body["metadata"].(map[string]any)["createdAt"] == nil || got.body["onboardingState"] != "CREATED" {
		t.Fatalf("function package metadata %v", got.body)
	}
	expect(t, h.rest("tnb", "PATCH", "/sol/vnfpkgm/v1/vnf_packages/"+id, map[string]any{"operationalState": "ENABLED"}), 400, "ValidationException")
	expect(t, h.rest("tnb", "PUT", "/sol/vnfpkgm/v1/vnf_packages/"+id+"/package_content", nil), 501, "UnknownOperationException")
	expect(t, h.rest("tnb", "DELETE", "/sol/vnfpkgm/v1/vnf_packages/"+id, nil), 204, "")

	config := h.rest("notifications", "POST", "/notification-configurations", map[string]any{"name": "ops", "description": "operations"})
	expect(t, config, 201, "")
	configArn := str(config.body["arn"])
	if !compiledPattern(`^arn:aws:notifications::000000000000:configuration/[a-z0-9]{27}$`).MatchString(configArn) || config.body["status"] != "INACTIVE" {
		t.Fatalf("notification configuration %v", config.body)
	}
	expect(t, h.rest("notifications", "PUT", "/notification-configurations/"+url.PathEscape(configArn), map[string]any{"aggregationDuration": "SHORT"}), 200, "")
	if listed := h.rest("notifications", "GET", "/notification-configurations?status=INACTIVE", nil); listed.body["notificationConfigurations"].([]any)[0].(map[string]any)["aggregationDuration"] != "SHORT" {
		t.Fatalf("configurations %v", listed.body)
	}
	expect(t, h.rest("notifications", "GET", "/notification-configurations?eventRuleSource=aws.ec2", nil), 400, "ValidationException")
	expect(t, h.rest("notifications", "DELETE", "/notification-configurations/"+url.PathEscape(configArn), nil), 200, "")

	group := h.rest("xray", "POST", "/CreateGroup", map[string]any{"GroupName": "errors", "FilterExpression": "fault = true"})
	expect(t, group, 200, "")
	groupArn := str(group.body["Group"].(map[string]any)["GroupARN"])
	if !strings.HasPrefix(groupArn, "arn:aws:xray:us-east-1:000000000000:group/errors/") {
		t.Fatalf("group %v", group.body)
	}
	expect(t, h.rest("xray", "POST", "/UpdateGroup", map[string]any{"GroupARN": groupArn, "InsightsConfiguration": map[string]any{"InsightsEnabled": true}}), 200, "")
	if got := h.rest("xray", "POST", "/GetGroup", map[string]any{"GroupName": "errors"}).body["Group"].(map[string]any); got["InsightsConfiguration"].(map[string]any)["InsightsEnabled"] != true {
		t.Fatalf("group update %v", got)
	}
	expect(t, h.rest("xray", "POST", "/GetGroup", map[string]any{"GroupName": "missing"}), 400, "InvalidRequestException")
	expect(t, h.rest("xray", "POST", "/CreateSamplingRule", map[string]any{}), 501, "UnknownOperationException")
	expect(t, h.rest("xray", "POST", "/DeleteGroup", map[string]any{"GroupName": "errors"}), 200, "")
}

func TestWellArchitectedBudgetsPricingRulesReportsAndTemplates(t *testing.T) {
	h := opsHarness(t)
	workload := h.rest("wellarchitected", "POST", "/workloads", map[string]any{"WorkloadName": "shop", "Description": "web shop", "Environment": "PRODUCTION",
		"Lenses": []any{"wellarchitected"}, "AwsRegions": []any{"us-east-1"}, "ClientRequestToken": "token-1"})
	expect(t, workload, 200, "")
	id := str(workload.body["WorkloadId"])
	expect(t, h.rest("wellarchitected", "POST", "/workloads", map[string]any{"WorkloadName": "jira", "Description": "web shop", "Environment": "PRODUCTION",
		"Lenses": []any{"wellarchitected"}, "ClientRequestToken": "token-2", "JiraConfiguration": map[string]any{"IssueManagementStatus": "ENABLED"}}), 400, "ValidationException")
	expect(t, h.rest("wellarchitected", "PATCH", "/workloads/"+id, map[string]any{"Notes": "reviewed"}), 200, "")
	if got := h.rest("wellarchitected", "GET", "/workloads/"+id, nil).body["Workload"].(map[string]any); got["Notes"] != "reviewed" || got["Owner"] != "000000000000" {
		t.Fatalf("workload %v", got)
	}
	if settings := h.rest("wellarchitected", "GET", "/global-settings", nil); settings.body["OrganizationSharingStatus"] != "DISABLED" {
		t.Fatalf("global settings %v", settings.body)
	}
	expect(t, h.rest("wellarchitected", "PATCH", "/global-settings", map[string]any{"OrganizationSharingStatus": "ENABLED"}), 400, "ValidationException")
	expect(t, h.rest("wellarchitected", "PATCH", "/global-settings", map[string]any{"DiscoveryIntegrationStatus": "DISABLED"}), 200, "")
	expect(t, h.rest("wellarchitected", "GET", "/workloads/"+id+"/lensReviews", nil), 501, "UnknownOperationException")
	expect(t, h.rest("wellarchitected", "DELETE", "/workloads/"+id+"?ClientRequestToken=token-3", nil), 200, "")

	budget := map[string]any{"BudgetName": "monthly", "BudgetType": "COST", "TimeUnit": "MONTHLY", "BudgetLimit": map[string]any{"Amount": "100", "Unit": "USD"}}
	expect(t, h.json1("budgets", "CreateBudget", map[string]any{"AccountId": "000000000000", "Budget": budget, "ResourceTags": []any{map[string]any{"Key": "team", "Value": "a"}}}), 200, "")
	expect(t, h.json1("budgets", "CreateBudget", map[string]any{"AccountId": "000000000000", "Budget": budget}), 400, "DuplicateRecordException")
	expect(t, h.json1("budgets", "CreateBudget", map[string]any{"AccountId": "111111111111", "Budget": budget}), 400, "InvalidParameterException")
	budget["BudgetLimit"] = map[string]any{"Amount": "150", "Unit": "USD"}
	expect(t, h.json1("budgets", "UpdateBudget", map[string]any{"AccountId": "000000000000", "NewBudget": budget}), 200, "")
	got := h.json1("budgets", "DescribeBudget", map[string]any{"AccountId": "000000000000", "BudgetName": "monthly"}).body["Budget"].(map[string]any)
	if got["BudgetLimit"].(map[string]any)["Amount"] != "150" || got["CalculatedSpend"] != nil {
		t.Fatalf("budget %v", got)
	}
	tags := h.json1("budgets", "ListTagsForResource", map[string]any{"ResourceARN": "arn:aws:budgets::000000000000:budget/monthly"})
	if tags.body["ResourceTags"].([]any)[0].(map[string]any)["Key"] != "team" {
		t.Fatalf("budget tags %v", tags.body)
	}
	expect(t, h.json1("budgets", "DescribeBudgetPerformanceHistory", map[string]any{}), 501, "UnknownOperationException")
	expect(t, h.json1("budgets", "DeleteBudget", map[string]any{"AccountId": "000000000000", "BudgetName": "monthly"}), 200, "")
	expect(t, h.json1("budgets", "DescribeBudget", map[string]any{"AccountId": "000000000000", "BudgetName": "monthly"}), 400, "NotFoundException")

	rule := h.rest("billingconductor", "POST", "/create-pricing-rule", map[string]any{"Name": "markup", "Scope": "GLOBAL", "Type": "MARKUP", "ModifierPercentage": 10})
	expect(t, rule, 200, "")
	ruleArn := str(rule.body["Arn"])
	if !compiledPattern(`^arn:aws:billingconductor::000000000000:pricingrule/[a-z0-9]{10}$`).MatchString(ruleArn) {
		t.Fatalf("pricing rule %v", rule.body)
	}
	if updated := h.rest("billingconductor", "PUT", "/update-pricing-rule", map[string]any{"Arn": ruleArn, "ModifierPercentage": 12.5}); asText(updated.body["ModifierPercentage"]) != "12.5" || asText(updated.body["AssociatedPricingPlanCount"]) != "0" {
		t.Fatalf("pricing rule update %v", updated.body)
	}
	expect(t, h.rest("billingconductor", "POST", "/list-pricing-rules", map[string]any{"BillingPeriod": "2026-10"}), 400, "ValidationException")
	expect(t, h.rest("billingconductor", "POST", "/delete-pricing-rule", map[string]any{"Arn": ruleArn}), 200, "")

	report := map[string]any{"ReportName": "daily", "TimeUnit": "DAILY", "Format": "textORcsv", "Compression": "GZIP", "AdditionalSchemaElements": []any{"RESOURCES"},
		"S3Bucket": "reports", "S3Prefix": "cur", "S3Region": "us-east-1"}
	expect(t, h.json1("cur", "PutReportDefinition", map[string]any{"ReportDefinition": report}), 200, "")
	report["TimeUnit"] = "HOURLY"
	expect(t, h.json1("cur", "ModifyReportDefinition", map[string]any{"ReportName": "daily", "ReportDefinition": report}), 200, "")
	if listed := h.json1("cur", "DescribeReportDefinitions", map[string]any{}).body["ReportDefinitions"].([]any); listed[0].(map[string]any)["TimeUnit"] != "HOURLY" {
		t.Fatalf("report definitions %v", listed)
	}
	expect(t, h.json1("cur", "PutReportDefinition", map[string]any{"ReportDefinition": report}), 400, "DuplicateReportNameException")
	expect(t, h.json1("cur", "ListTagsForResource", map[string]any{"ReportName": "daily"}), 501, "UnknownOperationException")
	expect(t, h.json1("cur", "DeleteReportDefinition", map[string]any{"ReportName": "daily"}), 200, "")

	tpl := h.rest("fis", "POST", "/experimentTemplates", map[string]any{"clientToken": "c-1", "description": "stop instances", "roleArn": "arn:aws:iam::000000000000:role/fis",
		"stopConditions": []any{map[string]any{"source": "none"}}, "actions": map[string]any{"stop": map[string]any{"actionId": "aws:ec2:stop-instances"}}})
	expect(t, tpl, 200, "")
	template := tpl.body["experimentTemplate"].(map[string]any)
	tplID := str(template["id"])
	if !strings.HasPrefix(tplID, "EXT") || template["experimentOptions"].(map[string]any)["emptyTargetResolutionMode"] != "fail" || template["arn"] != "arn:aws:fis:us-east-1:000000000000:experiment-template/"+tplID {
		t.Fatalf("experiment template %v", template)
	}
	expect(t, h.rest("fis", "PATCH", "/experimentTemplates/"+tplID, map[string]any{"experimentOptions": map[string]any{"emptyTargetResolutionMode": "skip"}}), 400, "ValidationException")
	expect(t, h.rest("fis", "PATCH", "/experimentTemplates/"+tplID, map[string]any{"description": "stop all"}), 200, "")
	expect(t, h.rest("fis", "POST", "/experiments", map[string]any{"clientToken": "s-1", "experimentTemplateId": tplID}), 501, "UnknownOperationException")
	expect(t, h.rest("fis", "DELETE", "/experimentTemplates/"+tplID, nil), 200, "")
	for _, service := range []string{"wellarchitected", "budgets", "billingconductor", "cur", "fis"} {
		if service != "wellarchitected" && opsRows(t, h, service) != 0 {
			t.Fatalf("%s left rows after cleanup", service)
		}
	}
}
