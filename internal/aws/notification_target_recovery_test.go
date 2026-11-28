package aws

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/codestarnotifications"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestNotificationTopologyCapacityAndDeletedQueue(t *testing.T) {
	f := branchFixture(t, filepath.Join(t.TempDir(), "destinations.db"))
	f.createResources()
	w := ruleWire{f, &Gateway{Notifications: f.rules, Identities: core.NewIdentities(f.team)}}
	arn := f.topic.ARN
	targets := []map[string]string{{"TargetType": "SNS", "TargetAddress": arn}}
	update := map[string]any{"Arn": f.rule, "Targets": targets}
	// Both a removed physical queue and an unrelated HTTP metadata subscription
	// prevent new target configuration; neither path publishes externally.
	if err := f.queues.DeleteQueue(f.region, "first"); err != nil {
		t.Fatal(err)
	}
	w.op("UpdateNotificationRule", update, 400, "ConfigurationException")
	f.git("PutFile", map[string]any{"branchName": "main", "filePath": "note", "fileContent": []byte("first")}, 200)
	if ok, err := f.deliver(t.Context()); !ok || err != nil || f.count("aws_sns_messages") != 0 {
		t.Fatal(ok, err, f.counts())
	}
	if err := f.sns.Unsubscribe(f.subs[0].ARN); err != nil {
		t.Fatal(err)
	}
	httpSub, err := f.sns.Subscribe(f.topic, "http", "http://127.0.0.1:9/metadata")
	if err != nil {
		t.Fatal(err)
	}
	w.op("UpdateNotificationRule", update, 400, "ConfigurationException")
	if err = f.sns.Unsubscribe(httpSub.ARN); err != nil {
		t.Fatal(err)
	}
	var final SNSSubscription
	for i := 1; i <= 20; i++ {
		q, err := f.queues.CreateQueue(f.region, SQSQueue{Name: fmt.Sprintf("queue%02d", i), VisibilityTimeout: 30, RetentionSeconds: 3600})
		if err != nil {
			t.Fatal(err)
		}
		final, err = f.sns.subscribeQueue(t.Context(), f.queues, f.topic, "arn:aws:sqs:"+f.region+":000000000000:"+q.Name)
		if err != nil {
			t.Fatal(err)
		}
		if i == 19 {
			w.op("UpdateNotificationRule", update, 200, "")
		}
	}
	w.op("UpdateNotificationRule", update, 400, "ConfigurationException")
	if err := f.sns.Unsubscribe(final.ARN); err != nil {
		t.Fatal(err)
	}
	w.op("UpdateNotificationRule", update, 200, "")
	if len(f.ruleCall("DescribeNotificationRule", codestarnotifications.Input{ARN: f.rule})["Targets"].([]map[string]string)) != 1 {
		t.Fatal("target failed to recover")
	}
}

func TestNotificationOriginalCreatorRecoveryAndRetirement(t *testing.T) {
	f := branchFixture(t, filepath.Join(t.TempDir(), "owner.db"))
	f.createResources()
	f.ruleCall("DeleteNotificationRule", codestarnotifications.Input{ARN: f.rule})
	ids := core.NewIdentities(f.team)
	owner := "creator"
	region := f.region
	set := func(p string) {
		t.Helper()
		doc, err := core.ParsePolicy([]byte(p))
		if err != nil {
			t.Fatal(err)
		}
		if err = ids.Set(owner, doc); err != nil {
			t.Fatal(err)
		}
	}
	allow := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}`
	set(allow)
	detail, name := "BASIC", "Owned"
	events := []string{codestarnotifications.BranchCreated, codestarnotifications.BranchUpdated}
	targets := []codestarnotifications.Target{{Type: "SNS", Address: f.topic.ARN}}
	r, err := f.rules.Run(t.Context(), "CreateNotificationRule", region, owner, codestarnotifications.Input{Name: &name, Source: f.source, DetailType: &detail, Events: &events, Targets: &targets}, func(string, string) string { return "allowed" })
	if err != nil {
		t.Fatal(err)
	}
	rule := r["Arn"].(string)
	tip := f.put("one", "", 200)["commitId"].(string)
	set(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"arn:aws:sqs:us-east-1:000000000000:first"}]}`)
	if ok, err := f.deliver(t.Context()); !ok || err != nil || f.count("aws_sns_messages") != 0 || f.count("aws_notification_outbox") != 1 {
		t.Fatal(ok, err, f.counts())
	}
	set(allow)
	s := f.ruleCall("DescribeNotificationRule", codestarnotifications.Input{ARN: rule})["Targets"].([]map[string]string)[0]["TargetStatus"]
	if s != "UNREACHABLE" {
		t.Fatal("failed attempt awaiting retry was reported healthy", s)
	}
	f.execute("UPDATE aws_notification_outbox SET retry_ms=0")
	if ok, err := f.deliver(t.Context()); !ok || err != nil {
		t.Fatal(ok, err)
	}
	tip = f.put("two", tip, 200)["commitId"].(string)
	if err = ids.Delete(owner); err != nil {
		t.Fatal(err)
	}
	set(allow)
	if ok, err := f.deliver(t.Context()); !ok || err != nil || f.count("aws_notification_outbox") != 0 || f.count("aws_sns_messages") != 1 {
		t.Fatal("new principal adopted previous work", ok, err, f.counts())
	}
	if f.git("GetBranch", map[string]any{"branchName": "main"}, 200)["branch"].(map[string]any)["commitId"] != tip {
		t.Fatal("retirement changed the Git tip")
	}
}

func TestNotificationPublisherSavepointFailureAndStoredReceipts(t *testing.T) {
	for _, phase := range []string{"BEGIN", "ROLLBACK", "RELEASE", "receipt after insert", "retry persistence"} {
		t.Run(phase, func(t *testing.T) {
			f := branchFixture(t, filepath.Join(t.TempDir(), "savepoint.db"))
			f.createResources()
			f.put("first", "", 200)
			before := kmsAliasDatabaseFrame(t, f.team.Database())
			refused := false
			if phase == "ROLLBACK" || phase == "RELEASE" {
				f.execute(`CREATE TEMP TRIGGER reject_second BEFORE INSERT ON aws_sqs_messages WHEN (SELECT count(*) FROM aws_sqs_messages)=1 BEGIN SELECT RAISE(ABORT,'bounded test write refusal'); END`)
			}
			if phase == "receipt after insert" {
				f.execute(`CREATE TEMP TRIGGER remove_new AFTER INSERT ON aws_notification_deliveries BEGIN DELETE FROM aws_notification_deliveries WHERE message_id=NEW.message_id; END`)
			} else if phase == "retry persistence" {
				f.policy("Deny")
				before = kmsAliasDatabaseFrame(t, f.team.Database())
				snsSetAuthorizer(t, f.team.Database(), func(op sqlite3.AuthorizerActionCode, table, column, _, _ string) sqlite3.AuthorizerReturnCode {
					if op == sqlite3.AUTH_UPDATE && table == "aws_notification_outbox" && column == "retry_ms" {
						refused = true
						return sqlite3.AUTH_DENY
					}
					return sqlite3.AUTH_OK
				})
			} else {
				snsSetAuthorizer(t, f.team.Database(), func(op sqlite3.AuthorizerActionCode, action, name, _, _ string) sqlite3.AuthorizerReturnCode {
					if op == sqlite3.AUTH_SAVEPOINT && action == phase && name == "notification_publish" {
						refused = true
						return sqlite3.AUTH_DENY
					}
					return sqlite3.AUTH_OK
				})
			}
			ok, err := f.deliver(t.Context())
			snsSetAuthorizer(t, f.team.Database(), nil)
			if err == nil || ok || phase != "receipt after insert" && !refused || kmsAliasDatabaseFrame(t, f.team.Database()) != before {
				t.Fatal("incomplete publish acknowledged or durable frame changed", ok, err, refused)
			}
			switch phase {
			case "ROLLBACK", "RELEASE":
				f.execute("DROP TRIGGER reject_second")
			case "receipt after insert":
				f.execute("DROP TRIGGER remove_new")
			case "retry persistence":
				f.policy("Allow")
			}
			if ok, err := f.deliver(t.Context()); !ok || err != nil || f.count("aws_sns_messages") != 1 || f.count("aws_sqs_messages") != 2 || f.count("aws_notification_deliveries") != 1 {
				t.Fatal(ok, err, f.counts())
			}
		})
	}
}

func TestNotificationStartupStorageRecovery(t *testing.T) {
	team, err := core.New(filepath.Join(t.TempDir(), "startup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer team.Close()
	db := team.Database()
	before := kmsAliasDatabaseFrame(t, db)
	blocked := false
	snsSetAuthorizer(t, db, func(op sqlite3.AuthorizerActionCode, table, _, _, _ string) sqlite3.AuthorizerReturnCode {
		if op == sqlite3.AUTH_CREATE_TABLE && table == "aws_notification_deliveries" {
			blocked = true
			return sqlite3.AUTH_DENY
		}
		return sqlite3.AUTH_OK
	})
	store, err := NewNotificationRules(db)
	snsSetAuthorizer(t, db, nil)
	if !blocked || err == nil || store != nil || kmsAliasDatabaseFrame(t, db) != before {
		t.Fatal("partial schema initialization", blocked, err)
	}
	store, err = NewNotificationRules(db)
	if err != nil || store == nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ok, err := store.DeliverOne(ctx, func(context.Context, *sql.Tx, string, string, func(string) error) (string, error) {
		t.Fatal("canceled startup attempted publication")
		return "", nil
	}); ok || !errors.Is(err, context.Canceled) {
		t.Fatal(ok, err)
	}
}

func TestNotificationUnreadableStoredPlacementAndCreatorPolicy(t *testing.T) {
	f := branchFixture(t, filepath.Join(t.TempDir(), "persisted.db"))
	f.createResources()
	ids := core.NewIdentities(f.team)
	wire := ruleWire{f, &Gateway{Notifications: f.rules, Identities: ids}}
	describe := map[string]any{"Arn": f.rule}
	db := f.team.Database()
	var generation int64
	if err := db.QueryRow("SELECT generation FROM aws_notification_targets WHERE rule_arn=?", f.rule).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	f.execute("UPDATE aws_notification_targets SET generation=? WHERE rule_arn=?", "unreadable", f.rule)
	before := kmsAliasDatabaseFrame(t, db)
	wire.op("DescribeNotificationRule", describe, 500, "InternalServerException")
	f.put("branch content", "", 500)
	if kmsAliasDatabaseFrame(t, db) != before {
		t.Fatal("reading an invalid target modified the rule or committed a Git branch")
	}
	f.execute("UPDATE aws_notification_targets SET generation=? WHERE rule_arn=?", generation, f.rule)
	wire.op("DescribeNotificationRule", describe, 200, "")
	f.ruleCall("DeleteNotificationRule", codestarnotifications.Input{ARN: f.rule})

	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`
	doc, err := core.ParsePolicy([]byte(policy))
	if err != nil {
		t.Fatal(err)
	}
	if err = ids.Set("creator", doc); err != nil {
		t.Fatal(err)
	}
	name, detail := "Owned rule", "BASIC"
	events := []string{codestarnotifications.BranchCreated}
	targets := []codestarnotifications.Target{{Type: "SNS", Address: f.topic.ARN}}
	result, err := f.rules.Run(t.Context(), "CreateNotificationRule", f.region, "creator", codestarnotifications.Input{
		Name: &name, Source: f.source, DetailType: &detail, Events: &events, Targets: &targets,
	}, func(action, resource string) string { return core.Evaluate(doc, action, resource) })
	if err != nil {
		t.Fatal(err)
	}
	describe["Arn"] = result["Arn"]
	f.put("branch content", "", 200)
	var saved []byte
	if err := db.QueryRow("SELECT document FROM identity_policies WHERE name='creator'").Scan(&saved); err != nil {
		t.Fatal(err)
	}
	// Exercise both the reader's ordinary gateway authorization and the worker's
	// separate retrieval of the original rule creator's current policy.
	f.execute("UPDATE identity_policies SET document=? WHERE name='creator'", []byte("{"))
	before = kmsAliasDatabaseFrame(t, db)
	wire.post("creator", "POST", "/listNotificationRules", []byte("{}"), nil, 500, "InternalServerException")
	wire.op("DescribeNotificationRule", describe, 500, "InternalServerException")
	if ok, err := f.deliver(t.Context()); ok || err == nil || kmsAliasDatabaseFrame(t, db) != before {
		t.Fatal("unreadable ownership policy lost or delivered pending work", ok, err)
	}
	f.execute("UPDATE identity_policies SET document=? WHERE name='creator'", saved)
	if got := wire.op("DescribeNotificationRule", describe, 200, "")["Targets"].([]any)[0].(map[string]any)["TargetStatus"]; got != "ACTIVE" {
		t.Fatal(got)
	}
	wire.post("creator", "POST", "/listNotificationRules", []byte("{}"), nil, 200, "")
	if ok, err := f.deliver(t.Context()); !ok || err != nil || f.count("aws_sqs_messages") != 2 || f.count("aws_notification_deliveries") != 1 {
		t.Fatal(ok, err, f.counts())
	}
}

func TestNotificationRegistrationReadbackAndRetirementRecovery(t *testing.T) {
	// An ordinary operation can report one affected row while a subsequent local
	// database action changes its contents. Acknowledgement requires its exact
	// saved value, including the captured physical target and owner.
	for _, phase := range []string{"rule", "owner", "target", "pending", "deletion"} {
		t.Run(phase, func(t *testing.T) {
			f := branchFixture(t, filepath.Join(t.TempDir(), "saved.db"))
			f.createResources()
			ids := core.NewIdentities(f.team)
			policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			for _, owner := range []string{"creator", "sibling"} {
				if err = ids.Set(owner, policy); err != nil {
					t.Fatal(err)
				}
			}
			f.put("already saved branch", "", 200)
			operation, actor := "CreateNotificationRule", "creator"
			name, detail := "New rule", "BASIC"
			events := []string{codestarnotifications.BranchCreated}
			targets := []codestarnotifications.Target{{Type: "SNS", Address: f.topic.ARN}}
			in := codestarnotifications.Input{Name: &name, Source: f.source, DetailType: &detail, Events: &events, Targets: &targets, Token: "stable-request"}
			var trigger string
			switch phase {
			case "rule":
				trigger = "AFTER INSERT ON aws_notification_rules BEGIN UPDATE aws_notification_rules SET name='Different' WHERE arn=NEW.arn; END"
			case "owner":
				trigger = "AFTER INSERT ON aws_notification_owners BEGIN UPDATE aws_notification_owners SET principal='sibling' WHERE rule_arn=NEW.rule_arn; END"
			case "target":
				trigger = "AFTER INSERT ON aws_notification_targets BEGIN UPDATE aws_notification_targets SET generation=NEW.generation+1 WHERE rule_arn=NEW.rule_arn; END"
			case "pending":
				operation, actor = "UpdateNotificationRule", core.Administrator
				in = codestarnotifications.Input{ARN: f.rule, Name: &name}
				trigger = "BEFORE DELETE ON aws_notification_outbox BEGIN SELECT RAISE(IGNORE); END"
			case "deletion":
				operation, actor = "DeleteNotificationRule", core.Administrator
				in = codestarnotifications.Input{ARN: f.rule}
				trigger = `AFTER DELETE ON aws_notification_rules BEGIN INSERT INTO aws_notification_rules
				(seq,arn,region,name,owner,source,repository_id,created_ms,updated_ms,generation,enabled,events,token,fingerprint) VALUES
				(OLD.seq,OLD.arn,OLD.region,OLD.name,OLD.owner,OLD.source,OLD.repository_id,OLD.created_ms,OLD.updated_ms,OLD.generation,OLD.enabled,OLD.events,OLD.token,OLD.fingerprint); END`
			}
			f.execute("CREATE TEMP TRIGGER change_storage " + trigger)
			before := kmsAliasDatabaseFrame(t, f.team.Database())
			invoke := func() (map[string]any, error) {
				return f.rules.Run(t.Context(), operation, f.region, actor, in, func(action, resource string) string { return core.Evaluate(policy, action, resource) })
			}
			if ack, err := invoke(); ack != nil || err == nil || kmsAliasDatabaseFrame(t, f.team.Database()) != before {
				t.Fatal("altered state survived or was acknowledged", ack, err)
			}
			f.execute("DROP TRIGGER change_storage")
			ack, err := invoke()
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "DeleteNotificationRule":
				if ack["Arn"] != f.rule || f.count("aws_notification_rules") != 0 || f.count("aws_notification_outbox") != 0 {
					t.Fatal(ack, f.counts())
				}
			case "UpdateNotificationRule":
				if f.ruleCall("DescribeNotificationRule", codestarnotifications.Input{ARN: f.rule})["Name"] != name || f.count("aws_notification_outbox") != 0 {
					t.Fatal(ack, f.counts())
				}
			case "CreateNotificationRule":
				current := f.ruleCall("DescribeNotificationRule", codestarnotifications.Input{ARN: ack["Arn"].(string)})
				again, err := invoke()
				if err != nil || again["Arn"] != ack["Arn"] || current["Name"] != name || f.count("aws_notification_rules") != 2 || f.count("aws_notification_outbox") != 1 {
					t.Fatal(ack, again, err, current)
				}
			}
		})
	}
}

func TestNotificationSameNameSourceAndTopicReplacements(t *testing.T) {
	f := branchFixture(t, filepath.Join(t.TempDir(), "replacements.db"))
	f.createResources()
	parent := f.put("queued before topic deletion", "", 200)["commitId"].(string)
	if err := f.sns.DeleteTopic(f.topic.ARN); err != nil {
		t.Fatal(err)
	}
	replacement, _, err := f.sns.CreateTopic(f.region, f.topic.Name, "", false)
	if err != nil || replacement.ARN != f.topic.ARN {
		t.Fatal(replacement, err)
	}
	f.topic = replacement
	f.policy("Allow")
	for _, sub := range f.subs {
		if _, err := f.sns.subscribeQueue(t.Context(), f.queues, replacement, sub.Endpoint); err != nil {
			t.Fatal(err)
		}
	}
	detail := f.ruleCall("DescribeNotificationRule", codestarnotifications.Input{ARN: f.rule})
	if len(detail["Targets"].([]map[string]string)) != 0 || f.count("aws_notification_outbox") != 0 {
		t.Fatal("same-name topic inherited the deleted placement", detail, f.counts())
	}
	parent = f.put("first change after replacement", parent, 200)["commitId"].(string)
	if ok, err := f.deliver(t.Context()); ok || err != nil || f.count("aws_sqs_messages") != 0 {
		t.Fatal(ok, err, f.counts())
	}
	targets := []codestarnotifications.Target{{Type: "SNS", Address: replacement.ARN}}
	f.ruleCall("UpdateNotificationRule", codestarnotifications.Input{ARN: f.rule, Targets: &targets})
	parent = f.put("first change after registration", parent, 200)["commitId"].(string)
	if ok, err := f.deliver(t.Context()); !ok || err != nil || f.count("aws_sqs_messages") != 2 {
		t.Fatal(ok, err, f.counts())
	}
	f.put("queued before repository deletion", parent, 200)
	f.git("DeleteRepository", nil, 200)
	repo := f.git("CreateRepository", nil, 200)["repositoryMetadata"].(map[string]any)
	if repo["Arn"] != f.source || repo["repositoryId"] == f.id || f.count("aws_notification_rules") != 0 || f.count("aws_notification_targets") != 0 || f.count("aws_notification_outbox") != 0 {
		t.Fatal("recreated repository inherited deleted rules or pending events", repo, f.counts())
	}
	f.put("first change in new repository", "", 200)
	if ok, err := f.deliver(t.Context()); ok || err != nil || f.count("aws_notification_deliveries") != 1 || f.count("aws_sqs_messages") != 2 {
		t.Fatal(ok, err, f.counts())
	}
}

func TestNotificationReopenFromClosedRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopened.db")
	f := branchFixture(t, path)
	f.createResources()
	tip := f.put("saved before stopping", "", 200)["commitId"].(string)
	before := kmsAliasDatabaseFrame(t, f.team.Database())
	if err := f.team.Close(); err != nil {
		t.Fatal(err)
	}
	if fresh, err := NewNotificationRules(f.team.Database()); fresh != nil || err == nil {
		t.Fatal("closed store accepted registration", fresh, err)
	}
	if ack, err := f.rules.Run(t.Context(), "DescribeNotificationRule", f.region, core.Administrator, codestarnotifications.Input{ARN: f.rule}, func(string, string) string { return "allowed" }); ack != nil || err == nil {
		t.Fatal("closed store acknowledged a read", ack, err)
	}
	reopened := branchFixture(t, path)
	current := reopened.git("GetRepository", nil, 200)["repositoryMetadata"].(map[string]any)
	if current["repositoryId"] != f.id || reopened.git("GetBranch", map[string]any{"branchName": "main"}, 200)["branch"].(map[string]any)["commitId"] != tip || kmsAliasDatabaseFrame(t, reopened.team.Database()) != before {
		t.Fatal("reopening changed stored objects", current)
	}
	reopened.topic = f.topic
	if ok, err := reopened.deliver(t.Context()); !ok || err != nil || reopened.count("aws_sns_messages") != 1 || reopened.count("aws_sqs_messages") != 2 || reopened.count("aws_notification_outbox") != 0 {
		t.Fatal(ok, err, reopened.counts())
	}
}
