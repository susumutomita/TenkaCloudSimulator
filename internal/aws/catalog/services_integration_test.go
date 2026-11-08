package catalog

import (
	"strings"
	"testing"
)

func TestEventBridgeBusesRulesArchivesAndConnections(t *testing.T) {
	h := newHarness(t, eventBridge)
	expect(t, h.json1("events", "CreateEventBus", map[string]any{"Name": "default"}), 400, "ValidationException")
	expect(t, h.json1("events", "CreateEventBus", map[string]any{"Name": "aws.partner/example.com/feed"}), 400, "ValidationException")
	expect(t, h.json1("events", "CreateEventBus", map[string]any{"Name": "orders", "EventSourceName": "aws.partner/example.com/feed"}), 400, "ValidationException")
	created := h.json1("events", "CreateEventBus", map[string]any{"Name": "orders", "Tags": []any{map[string]any{"Key": "team", "Value": "a"}}})
	expect(t, created, 200, "")
	bus := str(created.body["EventBusArn"])
	if bus != "arn:aws:events:us-east-1:000000000000:event-bus/orders" {
		t.Fatalf("event bus ARN %v", created.body)
	}
	for _, input := range []map[string]any{{}, {"Name": "default"}, {"Name": "arn:aws:events:us-east-1:000000000000:event-bus/default"}} {
		expect(t, h.json1("events", "DescribeEventBus", input), 400, "ValidationException")
	}
	if got := h.json1("events", "DescribeEventBus", map[string]any{"Name": bus}); got.body["Name"] != "orders" {
		t.Fatalf("describe by ARN %v", got.body)
	}

	pattern := `{"source":["shop"],"detail":{"status":["PAID"]}}`
	expect(t, h.json1("events", "PutRule", map[string]any{"Name": "paid", "EventPattern": pattern}), 400, "ValidationException")
	expect(t, h.json1("events", "PutRule", map[string]any{"Name": "paid", "EventBusName": "orders", "ScheduleExpression": "rate(1 day)"}), 400, "ValidationException")
	expect(t, h.json1("events", "PutRule", map[string]any{"Name": "paid", "EventBusName": "orders"}), 400, "ValidationException")
	for _, bad := range []string{`not json`, `{}`, `{"source":"shop"}`, `{"source":[]}`, `{"detail":{}}`, `{"detail":{"x":1}}`} {
		expect(t, h.json1("events", "PutRule", map[string]any{"Name": "paid", "EventBusName": "orders", "EventPattern": bad}), 400, "InvalidEventPatternException")
	}
	rule := h.json1("events", "PutRule", map[string]any{"Name": "paid", "EventBusName": "orders", "EventPattern": pattern})
	expect(t, rule, 200, "")
	if str(rule.body["RuleArn"]) != "arn:aws:events:us-east-1:000000000000:rule/orders/paid" {
		t.Fatalf("rule ARN %v", rule.body)
	}
	expect(t, h.json1("events", "DisableRule", map[string]any{"Name": "paid", "EventBusName": bus}), 200, "")
	described := h.json1("events", "DescribeRule", map[string]any{"Name": "paid", "EventBusName": "orders"})
	if described.body["State"] != "DISABLED" || described.body["EventBusName"] != "orders" || described.body["CreatedBy"] != "000000000000" {
		t.Fatalf("rule %v", described.body)
	}
	expect(t, h.json1("events", "DeleteEventBus", map[string]any{"Name": "orders"}), 400, "")
	expect(t, h.json1("events", "TagResource", map[string]any{"ResourceARN": str(rule.body["RuleArn"]), "Tags": []any{map[string]any{"Key": "k", "Value": "v"}}}), 200, "")

	expect(t, h.json1("events", "CreateArchive", map[string]any{"ArchiveName": "bad", "EventSourceArn": bus, "EventPattern": `[1]`}), 400, "InvalidEventPatternException")
	expect(t, h.json1("events", "CreateArchive", map[string]any{"ArchiveName": "all", "EventSourceArn": bus}), 200, "")
	expect(t, h.json1("events", "UpdateArchive", map[string]any{"ArchiveName": "all", "EventPattern": pattern}), 200, "")

	basic := map[string]any{"BasicAuthParameters": map[string]any{"Username": "u", "Password": "dummy-secret"}}
	refusals := []map[string]any{
		{"AuthorizationType": "OAUTH_CLIENT_CREDENTIALS", "AuthParameters": basic},
		{"AuthorizationType": "BASIC", "AuthParameters": basic, "InvocationConnectivityParameters": map[string]any{"ResourceParameters": map[string]any{"ResourceConfigurationArn": "arn:aws:vpc-lattice:us-east-1:000000000000:resourceconfiguration/rcfg-0123456789abcdef0"}}},
		{"AuthorizationType": "BASIC", "AuthParameters": map[string]any{"BasicAuthParameters": basic["BasicAuthParameters"], "ConnectivityParameters": map[string]any{"ResourceParameters": map[string]any{"ResourceConfigurationArn": "arn:aws:vpc-lattice:us-east-1:000000000000:resourceconfiguration/rcfg-0123456789abcdef0"}}}},
		{"AuthorizationType": "BASIC", "AuthParameters": map[string]any{"BasicAuthParameters": basic["BasicAuthParameters"], "InvocationHttpParameters": map[string]any{"HeaderParameters": []any{map[string]any{"Key": "X-Token", "Value": "v", "IsValueSecret": true}}}}},
		{"AuthorizationType": "BASIC", "AuthParameters": map[string]any{"BasicAuthParameters": basic["BasicAuthParameters"], "ApiKeyAuthParameters": map[string]any{"ApiKeyName": "k", "ApiKeyValue": "v"}}},
		{"AuthorizationType": "API_KEY", "AuthParameters": basic},
		{"AuthorizationType": "API_KEY", "AuthParameters": map[string]any{}},
	}
	for _, input := range refusals {
		input["Name"] = "refused"
		refused := h.json1("events", "CreateConnection", input)
		expect(t, refused, 400, "ValidationException")
		if strings.Contains(refused.raw, "official") {
			t.Fatalf("refusal must come from the bounded connection rules: %s", refused.raw)
		}
	}
	connection := h.json1("events", "CreateConnection", map[string]any{"Name": "hook", "AuthorizationType": "BASIC", "AuthParameters": basic})
	expect(t, connection, 200, "")
	if connection.body["ConnectionState"] != "AUTHORIZED" || !strings.HasPrefix(str(connection.body["ConnectionArn"]), "arn:aws:events:us-east-1:000000000000:connection/hook/") {
		t.Fatalf("connection %v", connection.body)
	}
	got := h.json1("events", "DescribeConnection", map[string]any{"Name": "hook"})
	if strings.Contains(got.raw, "dummy-secret") || got.body["AuthParameters"].(map[string]any)["BasicAuthParameters"].(map[string]any)["Username"] != "u" {
		t.Fatalf("connection secrets must never be returned: %s", got.raw)
	}
	expect(t, h.json1("events", "UpdateConnection", map[string]any{"Name": "hook", "Description": "partner"}), 200, "")
	expect(t, h.json1("events", "UpdateConnection", map[string]any{"Name": "hook", "AuthParameters": basic}), 400, "ValidationException")
	expect(t, h.json1("events", "UpdateConnection", map[string]any{"Name": "hook", "AuthorizationType": "API_KEY", "AuthParameters": map[string]any{"ApiKeyAuthParameters": map[string]any{"ApiKeyName": "k"}}}), 400, "ValidationException")
	expect(t, h.json1("events", "UpdateConnection", map[string]any{"Name": "hook", "AuthorizationType": "OAUTH_CLIENT_CREDENTIALS"}), 400, "ValidationException")
	expect(t, h.json1("events", "UpdateConnection", map[string]any{"Name": "hook", "AuthorizationType": "API_KEY", "AuthParameters": map[string]any{"ApiKeyAuthParameters": map[string]any{"ApiKeyName": "k", "ApiKeyValue": "v"}}}), 200, "")
	if h.json1("events", "DescribeConnection", map[string]any{"Name": "hook"}).body["AuthorizationType"] != "API_KEY" {
		t.Fatal("authorization update did not persist")
	}
	expect(t, h.json1("events", "TagResource", map[string]any{"ResourceARN": str(connection.body["ConnectionArn"]), "Tags": []any{map[string]any{"Key": "k", "Value": "v"}}}), 400, "ValidationException")
	deleted := h.json1("events", "DeleteConnection", map[string]any{"Name": "hook"})
	if deleted.body["ConnectionState"] != "DELETING" {
		t.Fatalf("delete %v", deleted.body)
	}
	expect(t, h.json1("events", "PutEvents", map[string]any{"Entries": []any{}}), 501, "UnknownOperationException")
}

func TestIntegrationServicesRefuseUnsupportedConfiguration(t *testing.T) {
	h := newHarness(t, mwaa, appFabric, pinpoint, smsVoice, sesV2, managedBlockchain)
	environment := map[string]any{"ExecutionRoleArn": "arn:aws:iam::000000000000:role/mwaa", "SourceBucketArn": "arn:aws:s3:::dags", "DagS3Path": "dags",
		"NetworkConfiguration": map[string]any{"SubnetIds": []any{"subnet-1", "subnet-2"}}}
	for _, pair := range [][2]string{{"MinWorkers", "MaxWorkers"}, {"MinWebservers", "MaxWebservers"}} {
		bad := map[string]any{pair[0]: 3, pair[1]: 2}
		for k, v := range environment {
			bad[k] = v
		}
		expect(t, h.rest("mwaa", "PUT", "/environments/etl", bad), 400, "ValidationException")
	}
	created := h.rest("mwaa", "PUT", "/environments/etl", environment)
	expect(t, created, 200, "")
	if str(created.body["Arn"]) != "arn:aws:airflow:us-east-1:000000000000:environment/etl" {
		t.Fatalf("environment %v", created.body)
	}
	if got := h.rest("mwaa", "GET", "/environments/etl", nil).body["Environment"].(map[string]any); got["Status"] != "CREATING" {
		t.Fatalf("environment status %v", got)
	}
	expect(t, h.rest("mwaa", "PATCH", "/environments/etl", map[string]any{"NetworkConfiguration": map[string]any{"SecurityGroupIds": []any{"sg-1"}}}), 400, "ValidationException")

	expect(t, h.rest("appfabric", "POST", "/appbundles", map[string]any{"customerManagedKeyIdentifier": "00000000-0000-4000-8000-000000000000"}), 400, "ValidationException")
	bundle := h.rest("appfabric", "POST", "/appbundles", map[string]any{"customerManagedKeyIdentifier": "arn:aws:kms:us-east-1:000000000000:key/00000000-0000-4000-8000-000000000000"})
	expect(t, bundle, 201, "")
	if bundle.body["appBundle"].(map[string]any)["customerManagedKeyArn"] == nil {
		t.Fatalf("app bundle key %v", bundle.body)
	}

	expect(t, h.rest("pinpoint", "POST", "/v1/apps", map[string]any{"Name": "shop", "tags": map[string]any{"team": "a"}}), 400, "BadRequestException")
	app := h.rest("pinpoint", "POST", "/v1/apps", map[string]any{"Name": "shop"})
	expect(t, app, 201, "")
	if len(str(app.body["Id"])) != 32 || !strings.HasPrefix(str(app.body["Arn"]), "arn:aws:mobiletargeting:us-east-1:000000000000:apps/") {
		t.Fatalf("pinpoint app %v", app.body)
	}

	expect(t, h.json1("pinpoint-sms-voice-v2", "CreateOptOutList", map[string]any{"OptOutListName": "Default"}), 400, "ValidationException")
	expect(t, h.json1("pinpoint-sms-voice-v2", "CreateOptOutList", map[string]any{"OptOutListName": "marketing"}), 200, "")
	expect(t, h.json1("pinpoint-sms-voice-v2", "SendTextMessage", map[string]any{"DestinationPhoneNumber": "+15555550100"}), 501, "UnknownOperationException")

	expect(t, h.rest("sesv2", "POST", "/v2/email/contact-lists", map[string]any{"ContactListName": "one"}), 200, "")
	expect(t, h.rest("sesv2", "POST", "/v2/email/contact-lists", map[string]any{"ContactListName": "two"}), 400, "LimitExceededException")

	accessor := h.rest("managedblockchain", "POST", "/accessors", map[string]any{"ClientRequestToken": "token-1", "AccessorType": "BILLING_TOKEN"})
	expect(t, accessor, 200, "")
	if len(str(accessor.body["BillingToken"])) != 42 || !strings.HasPrefix(str(accessor.body["AccessorId"]), "ac-") {
		t.Fatalf("accessor %v", accessor.body)
	}
}
