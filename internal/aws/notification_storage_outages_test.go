package aws

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/codestarnotifications"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type notificationOutage struct {
	name, operation       string
	action                sqlite3.AuthorizerActionCode
	table, column, staged string
}

func TestNotificationRuleStorageFaultAndRecovery(t *testing.T) {
	const read = sqlite3.AUTH_READ
	const insert = sqlite3.AUTH_INSERT
	const update = sqlite3.AUTH_UPDATE
	const remove = sqlite3.AUTH_DELETE
	// Each failure is attached to a real read/write prerequisite. Specified
	// staged tables must already have changed via a SQLite update hook.
	for _, fault := range []notificationOutage{
		{"client token lookup", "create", read, "aws_notification_rules", "fingerprint", ""},
		{"capture original repository", "create", read, "aws_catalog_resources", "id", ""},
		{"check name availability", "create", read, "aws_notification_rules", "name", ""},
		{"register rule", "create", insert, "aws_notification_rules", "", ""},
		{"retain original principal", "create", insert, "aws_notification_owners", "", "aws_notification_rules"},
		{"replace target links", "create", remove, "aws_notification_targets", "", "aws_notification_owners"},
		{"read current topic policy", "create", read, "aws_sns_notification_policies", "policy", "aws_notification_owners"},
		{"capture physical subscribers", "create", read, "aws_sns_subscriptions", "protocol", "aws_notification_owners"},
		{"capture queue instance", "create", insert, "aws_notification_queue_instances", "", "aws_notification_owners"},
		{"read queue incarnation", "create", read, "aws_notification_queue_instances", "generation", "aws_notification_owners"},
		{"capture original subscription", "create", insert, "aws_notification_subscriptions", "", "aws_notification_owners"},
		{"read subscription incarnation", "create", read, "aws_notification_subscriptions", "queue_generation", "aws_notification_owners"},
		{"persist target", "create", insert, "aws_notification_targets", "", "aws_notification_owners"},
		{"check exact target generation", "create", read, "aws_notification_targets", "generation", "aws_notification_targets"},
		{"verify created row", "create", read, "aws_notification_rules", "created_ms", "aws_notification_targets"},
		{"describe stored identity", "describe", read, "aws_notification_rules", "created_ms", ""},
		{"describe saved target generation", "describe", read, "aws_notification_targets", "generation", ""},
		{"describe actual topic policy", "describe", read, "aws_sns_notification_policies", "policy", ""},
		{"describe physical subscriptions", "describe", read, "aws_sns_subscriptions", "endpoint", ""},
		{"describe delivery retry", "describe", read, "aws_notification_outbox", "retry_ms", ""},
		{"read rule page", "list", read, "aws_notification_rules", "seq", ""},
		{"read target filter", "list", read, "aws_notification_targets", "topic_arn", ""},
		{"change target links", "update", insert, "aws_notification_targets", "", "aws_notification_targets"},
		{"write revised rule", "update", update, "aws_notification_rules", "name", "aws_notification_targets"},
		{"remove registered rule", "delete", remove, "aws_notification_rules", "", ""},
		{"scope revision read", "capture", read, "aws_notification_rules", "events", ""},
		{"match target region", "capture", read, "aws_notification_targets", "generation", ""},
		{"capture subscription set", "capture", read, "aws_sns_subscriptions", "protocol", "aws_codecommit_branches"},
		{"read pending budget", "capture", read, "aws_notification_outbox", "", "aws_codecommit_branches"},
		{"save pending change", "capture", insert, "aws_notification_outbox", "", "aws_codecommit_branches"},
		{"read pending work", "deliver", read, "aws_notification_outbox", "envelope", ""},
		{"check current service grant", "deliver", read, "aws_sns_notification_policies", "policy", ""},
		{"check current topology", "deliver", read, "aws_sns_subscriptions", "protocol", ""},
		{"write delivery receipt", "deliver", insert, "aws_notification_deliveries", "", "aws_sqs_messages"},
		{"remove confirmed pending record", "deliver", remove, "aws_notification_outbox", "", "aws_notification_deliveries"},
		{"commit new rule and its destinations", "create", sqlite3.AUTH_TRANSACTION, "", "", "aws_notification_targets"},
		{"commit replacement target generation", "update", sqlite3.AUTH_TRANSACTION, "", "", "aws_notification_rules"},
		{"commit cascading rule deletion", "delete", sqlite3.AUTH_TRANSACTION, "", "", "aws_notification_rules"},
		{"commit native Git and pending event", "capture", sqlite3.AUTH_TRANSACTION, "", "", "aws_notification_outbox"},
		{"commit durable fanout and deduplication", "deliver", sqlite3.AUTH_TRANSACTION, "", "", "aws_notification_deliveries"},
	} {
		t.Run(fault.name, func(t *testing.T) {
			f := branchFixture(t, filepath.Join(t.TempDir(), "fault.db"))
			f.createResources()
			policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			if err = core.NewIdentities(f.team).Set("creator", policy); err != nil {
				t.Fatal(err)
			}
			// A second rule for the same repository retains durable Git and target
			// state while the tested registration or reference is interrupted.
			f.createRule("Sibling")
			parent := f.put("old", "", 200)["commitId"].(string)
			for f.count("aws_notification_outbox") > 0 {
				if ok, err := f.deliver(t.Context()); !ok || err != nil {
					t.Fatal(ok, err)
				}
			}
			if fault.operation == "deliver" {
				f.put("pending", parent, 200)
			}
			before := kmsAliasDatabaseFrame(t, f.team.Database())
			staged := map[string]bool{}
			fired := false
			conn, err := f.team.Database().Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = conn.Raw(func(raw any) error {
				c := raw.(sqlitedriver.Conn).Raw()
				c.UpdateHook(func(_ sqlite3.AuthorizerActionCode, _, table string, _ int64) { staged[table] = true })
				if fault.action == sqlite3.AUTH_TRANSACTION {
					c.CommitHook(func() bool {
						if !fired && staged[fault.staged] {
							fired = true
							return false
						}
						return true
					})
				}
				return nil
			})
			conn.Close()
			if err != nil {
				t.Fatal(err)
			}
			snsSetAuthorizer(t, f.team.Database(), func(action sqlite3.AuthorizerActionCode, table, column, _, _ string) sqlite3.AuthorizerReturnCode {
				if action == fault.action && table == fault.table && (fault.column == "" || column == fault.column) && (fault.staged == "" || staged[fault.staged]) {
					fired = true
					return sqlite3.AUTH_DENY
				}
				return sqlite3.AUTH_OK
			})
			invoke := func(healthy bool) error {
				name, detail := "New registration", "BASIC"
				events := []string{codestarnotifications.BranchCreated}
				targets := []codestarnotifications.Target{{Type: "SNS", Address: f.topic.ARN}}
				var operation string
				actor := core.Administrator
				in := codestarnotifications.Input{ARN: f.rule}
				switch fault.operation {
				case "create":
					operation = "CreateNotificationRule"
					actor = "creator"
					in = codestarnotifications.Input{Name: &name, DetailType: &detail, Source: f.source, Events: &events, Targets: &targets, Token: "request-once"}
				case "describe":
					operation = "DescribeNotificationRule"
				case "list":
					operation = "ListNotificationRules"
					in.Filters = []codestarnotifications.Filter{{Name: "TARGET_ADDRESS", Value: f.topic.ARN}}
				case "update":
					operation = "UpdateNotificationRule"
					in.Name = &name
					in.Targets = &targets
				case "delete":
					operation = "DeleteNotificationRule"
				case "capture":
					status := 500
					if healthy {
						status = 200
					}
					reply := f.put("pending", parent, status)
					if healthy {
						if reply["commitId"] == nil || reply["commitId"] == parent {
							t.Fatal(reply)
						}
						return nil
					}
					code, ok := reply["__type"].(string)
					if !ok || code == "" {
						t.Fatal("missing native Git refusal", reply)
					}
					return errors.New(code)
				case "deliver":
					ok, err := f.deliver(t.Context())
					if ok && err == nil && !healthy {
						t.Fatal("storage outage acknowledged native delivery")
					}
					return err
				}
				result, err := f.rules.Run(t.Context(), operation, f.region, actor, in, func(string, string) string { return "allowed" })
				if !healthy && result != nil {
					t.Fatal("acknowledged partially written result", result)
				}
				if healthy {
					switch fault.operation {
					case "create", "delete":
						if result["Arn"] == nil {
							t.Fatal(result)
						}
					case "list":
						if len(result["NotificationRules"].([]map[string]string)) != 2 {
							t.Fatal(result)
						}
					case "describe":
						if result["Name"] != "Branches" {
							t.Fatal(result)
						}
					}
				}
				return err
			}
			callErr := invoke(false)
			snsSetAuthorizer(t, f.team.Database(), nil)
			if !fired || callErr == nil {
				t.Fatalf("missing intended SQL refusal: fired=%v err=%v, observed stages=%v", fired, callErr, staged)
			}
			if after := kmsAliasDatabaseFrame(t, f.team.Database()); after != before {
				t.Fatal("unacknowledged operation changed persisted state")
			}
			if err := invoke(true); err != nil {
				t.Fatal("repaired operation failed", err)
			}
			if fault.operation == "capture" && f.count("aws_notification_outbox") != 2 {
				t.Fatal(f.counts())
			}
			if fault.operation == "deliver" && (f.count("aws_notification_deliveries") != 3 || f.count("aws_notification_outbox") != 1) {
				t.Fatal(f.counts())
			}
			if fault.operation == "update" && f.ruleCall("DescribeNotificationRule", codestarnotifications.Input{ARN: f.rule})["Name"] != "New registration" {
				t.Fatal("update retry did not persist")
			}
			if fault.operation == "delete" && f.count("aws_notification_rules") != 1 {
				t.Fatal("deletion retry removed siblings")
			}
		})
	}
}
