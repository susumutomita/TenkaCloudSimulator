package aws

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/codestarnotifications"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type branchNotifyFixture struct {
	t                        *testing.T
	team                     *core.Store
	rules                    *codestarnotifications.Store
	repo                     *catalog.Engine
	sns                      *SNSStore
	queues                   *SQSStore
	region, source, id, rule string
	topic                    SNSTopic
	subs                     []SNSSubscription
}

func branchFixture(t *testing.T, path string) *branchNotifyFixture {
	t.Helper()
	team, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { team.Close() })
	sns, err := NewSNSStore(team.Database())
	if err != nil {
		t.Fatal(err)
	}
	queues, err := NewSQSStore(team.Database())
	if err != nil {
		t.Fatal(err)
	}
	rules, err := NewNotificationRules(team.Database())
	if err != nil {
		t.Fatal(err)
	}
	ids := core.NewIdentities(team)
	repo, err := catalog.New(team.Database(), catalog.Config{Regions: Regions, Account: "000000000000", DefaultPrincipal: core.Administrator, Authorize: ids.DecisionContext, RecordCodeCommitRef: rules.RecordRef}, catalog.Services())
	if err != nil {
		t.Fatal(err)
	}
	return &branchNotifyFixture{t: t, team: team, rules: rules, repo: repo, sns: sns, queues: queues, region: "us-east-1"}
}

func (f *branchNotifyFixture) git(op string, in map[string]any, want int) map[string]any {
	f.t.Helper()
	return f.gitContext(f.t.Context(), op, in, want)
}

func (f *branchNotifyFixture) gitContext(ctx context.Context, op string, in map[string]any, want int) map[string]any {
	f.t.Helper()
	if in == nil {
		in = map[string]any{}
	}
	in["repositoryName"] = "exercises"
	b, err := json.Marshal(in)
	if err != nil {
		f.t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(ctx, "POST", "/", bytes.NewReader(b))
	r.Header.Set("X-Amz-Target", "CodeCommit_20150413."+op)
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Simulator-Region", f.region)
	w := httptest.NewRecorder()
	f.repo.ServeHTTP(w, r)
	if w.Code != want {
		f.t.Fatalf("%s returned %d want %d: %s", op, w.Code, want, w.Body.String())
	}
	var out map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *branchNotifyFixture) createResources() {
	f.t.Helper()
	r := f.git("CreateRepository", nil, 200)["repositoryMetadata"].(map[string]any)
	f.source, f.id = r["Arn"].(string), r["repositoryId"].(string)
	var err error
	f.topic, _, err = f.sns.CreateTopic(f.region, "branches", "", false)
	if err != nil {
		f.t.Fatal(err)
	}
	f.policy("Allow")
	for _, name := range []string{"first", "second"} {
		q, err := f.queues.CreateQueue(f.region, SQSQueue{Name: name, VisibilityTimeout: 30, RetentionSeconds: 3600})
		if err != nil {
			f.t.Fatal(err)
		}
		sub, err := f.sns.subscribeQueue(f.t.Context(), f.queues, f.topic, "arn:aws:sqs:"+f.region+":000000000000:"+q.Name)
		if err != nil {
			f.t.Fatal(err)
		}
		f.subs = append(f.subs, sub)
	}
	f.rule = f.createRule("Branches")
}

func (f *branchNotifyFixture) policy(effect string) {
	f.t.Helper()
	if err := f.sns.setNotificationPolicy(f.t.Context(), f.topic.ARN, snsTestPolicy(snsPolicyStatement(f.topic.ARN, effect))); err != nil {
		f.t.Fatal(err)
	}
}

func (f *branchNotifyFixture) ruleCall(op string, in codestarnotifications.Input) map[string]any {
	f.t.Helper()
	out, err := f.rules.Run(f.t.Context(), op, f.region, core.Administrator, in, func(string, string) string { return "allowed" })
	if err != nil {
		f.t.Fatal(op, err)
	}
	return out
}

func (f *branchNotifyFixture) createRule(n string) string {
	f.t.Helper()
	ev := []string{codestarnotifications.BranchCreated, codestarnotifications.BranchUpdated}
	targets := []codestarnotifications.Target{{Type: "SNS", Address: f.topic.ARN}}
	detail := "BASIC"
	return f.ruleCall("CreateNotificationRule", codestarnotifications.Input{Name: &n, Source: f.source, DetailType: &detail, Targets: &targets, Events: &ev})["Arn"].(string)
}

func (f *branchNotifyFixture) put(data string, parent string, want int) map[string]any {
	f.t.Helper()
	fields := map[string]any{"branchName": "main", "filePath": "note.txt", "fileContent": []byte(data)}
	if parent != "" {
		fields["parentCommitId"] = parent
	}
	return f.git("PutFile", fields, want)
}

func (f *branchNotifyFixture) execute(query string, args ...any) {
	f.t.Helper()
	if _, err := f.team.Database().Exec(query, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *branchNotifyFixture) count(table string) int {
	f.t.Helper()
	var n int
	if err := f.team.Database().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *branchNotifyFixture) counts() []int {
	tables := []string{"aws_codecommit_objects", "aws_codecommit_branches", "aws_notification_outbox", "aws_sns_messages", "aws_sns_deliveries", "aws_sqs_messages", "aws_notification_deliveries"}
	counts := make([]int, 0, len(tables))
	for _, name := range tables {
		counts = append(counts, f.count(name))
	}
	return counts
}

func (f *branchNotifyFixture) deliver(ctx context.Context) (bool, error) {
	return f.rules.DeliverOne(ctx, func(_ context.Context, tx *sql.Tx, topicARN, body string, allow func(string) error) (string, error) {
		if topicARN != f.topic.ARN {
			f.t.Fatal(topicARN)
		}
		return snsPublishQueuesTx(tx, f.topic, body, "", allow)
	})
}

func TestCodeCommitNotificationAtomicSourceAndFanout(t *testing.T) {
	f := branchFixture(t, filepath.Join(t.TempDir(), "owner.sqlite"))
	f.createResources()
	before := f.counts()
	f.execute(`CREATE TEMP TRIGGER reject_intent BEFORE INSERT ON aws_notification_outbox BEGIN SELECT RAISE(IGNORE); END`)
	f.put("first", "", 500)
	if got := f.counts(); !slices.Equal(got, before) {
		t.Fatalf("Git/intent refusal changed state: %v", got)
	}
	f.execute("DROP TRIGGER reject_intent")
	first := f.put("first", "", 200)["commitId"].(string)
	if f.count("aws_notification_outbox") != 1 {
		t.Fatal("missing first pending target")
	}
	f.put("second", first, 200)
	if f.count("aws_notification_outbox") != 2 {
		t.Fatal("missing second pending target")
	}
	// Refuse only the second real SQS insert. SNS and the first SQS insert
	// must roll back as well; the source commits survive the damaged sink.
	f.execute(`CREATE TEMP TRIGGER reject_second BEFORE INSERT ON aws_sqs_messages
	  WHEN (SELECT count(*) FROM aws_sqs_messages)=1 BEGIN SELECT RAISE(IGNORE); END`)
	before = f.counts()
	ok, err := f.deliver(t.Context())
	if !ok || err != nil || !slices.Equal(before, f.counts()) {
		t.Fatal("partial target publish", ok, err, f.counts())
	}
	f.execute("DROP TRIGGER reject_second")
	// A delivery-marker failure follows both real enqueues.
	f.execute(`CREATE TEMP TRIGGER reject_marker BEFORE INSERT ON aws_notification_deliveries BEGIN SELECT RAISE(IGNORE); END`)
	ok, err = f.deliver(t.Context())
	if ok || err == nil || !slices.Equal(before, f.counts()) {
		t.Fatal("partial acknowledged delivery", ok, err, f.counts())
	}
	f.execute("DROP TRIGGER reject_marker")
	f.execute("UPDATE aws_notification_outbox SET retry_ms=0")
	for range 2 {
		ok, err = f.deliver(t.Context())
		if !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	ok, err = f.deliver(t.Context())
	if ok || err != nil {
		t.Fatal("unwanted replay", err)
	}
	if f.count("aws_notification_outbox") != 0 || f.count("aws_sns_messages") != 2 || f.count("aws_sqs_messages") != 4 || f.count("aws_notification_deliveries") != 2 {
		t.Fatal(f.counts())
	}
}

func TestNotificationPhysicalQueueAndRuleLifetimes(t *testing.T) {
	f := branchFixture(t, filepath.Join(t.TempDir(), "lifetimes.db"))
	f.createResources()
	parent := f.put("first", "", 200)["commitId"].(string)
	if err := f.queues.DeleteQueue(f.region, "first"); err != nil {
		t.Fatal(err)
	}
	q, err := f.queues.CreateQueue(f.region, SQSQueue{Name: "first", VisibilityTimeout: 30, RetentionSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := f.deliver(t.Context())
	if !ok || err != nil || f.count("aws_sns_messages") != 0 {
		t.Fatal("old queue adopted old intent", ok, err)
	}
	parent = f.put("second", parent, 200)["commitId"].(string)
	ok, err = f.deliver(t.Context())
	if !ok || err != nil || f.count("aws_sns_messages") != 0 {
		t.Fatal("old subscription adopted replacement queue", ok, err)
	}
	if err := f.sns.Unsubscribe(f.subs[0].ARN); err != nil {
		t.Fatal(err)
	}
	_, err = f.sns.subscribeQueue(t.Context(), f.queues, f.topic, "arn:aws:sqs:"+f.region+":000000000000:"+q.Name)
	if err != nil {
		t.Fatal(err)
	}
	parent = f.put("third", parent, 200)["commitId"].(string)
	ok, err = f.deliver(t.Context())
	if !ok || err != nil || f.count("aws_sqs_messages") != 2 {
		t.Fatal("fresh subscription", ok, err, f.counts())
	}
	parent = f.put("fourth", parent, 200)["commitId"].(string)
	disabled := "DISABLED"
	f.ruleCall("UpdateNotificationRule", codestarnotifications.Input{ARN: f.rule, Status: &disabled})
	f.put("fifth", parent, 200)
	ok, err = f.deliver(t.Context())
	if ok || err != nil || f.count("aws_sns_messages") != 1 {
		t.Fatal("old rule generation survived", ok, err)
	}
	f.ruleCall("DeleteNotificationRule", codestarnotifications.Input{ARN: f.rule})
	f.git("DeleteRepository", nil, 200)
	newRepo := f.git("CreateRepository", nil, 200)["repositoryMetadata"].(map[string]any)
	if newRepo["Arn"] != f.source || newRepo["repositoryId"] == f.id {
		t.Fatal(newRepo)
	}
	f.put("fresh", "", 200)
	if got := f.count("aws_notification_outbox"); got != 0 {
		t.Fatal("new repository adopted rule", got)
	}
}

func TestNotificationClientBodyAdmission(t *testing.T) {
	for _, data := range []string{`null`, `[]`, `{} {}`, `{"Name":null}`, `{"Name":"a","Name":"b"}`, `{"Name":"\ud800"}`, `{"name":"case"}`, `{"NextToken":""}`, `{"ClientRequestToken":""}`, `{"Filters":[{"name":"RESOURCE","Value":"x"}]}`, `{"Targets":[{"TargetType":"SNS","targetAddress":"a"}]}`, `{"Targets":[{"TargetType":"SNS","TargetAddress":"a","Other":1}]}`, `{"MaxResults":1.1}`} {
		fields := []string{"Name", "NextToken", "ClientRequestToken", "Filters", "Targets", "MaxResults"}
		if _, err := decodeNotificationInput([]byte(data), fields); err == nil {
			t.Errorf("accepted %s", data)
		}
	}
	for _, op := range []string{"CreateNotificationRule", "ListNotificationRules", "UpdateNotificationRule", "DescribeNotificationRule", "DeleteNotificationRule"} {
		if len(notificationFields[op]) == 0 {
			t.Fatal(op)
		}
	}
}

func TestNotificationPendingCapacityAndResume(t *testing.T) {
	f := branchFixture(t, filepath.Join(t.TempDir(), "capacity.db"))
	f.createResources()
	other, _, err := f.sns.CreateTopic(f.region, "audit", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.sns.setNotificationPolicy(t.Context(), other.ARN, snsTestPolicy(snsPolicyStatement(other.ARN, "Allow"))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sns.subscribeQueue(t.Context(), f.queues, other, f.subs[0].Endpoint); err != nil {
		t.Fatal(err)
	}
	targets := []codestarnotifications.Target{{Type: "SNS", Address: f.topic.ARN}, {Type: "SNS", Address: other.ARN}}
	for i := 0; i < 8; i++ {
		arn := f.rule
		if i > 0 {
			arn = f.createRule(fmt.Sprintf("Rule%d", i))
		}
		f.ruleCall("UpdateNotificationRule", codestarnotifications.Input{ARN: arn, Targets: &targets})
	}
	var tip string
	for i := 0; i < 128; i++ {
		tip = f.put(fmt.Sprintf("content%d", i), tip, 200)["commitId"].(string)
	}
	before := f.counts()
	if before[2] != 2048 {
		t.Fatal("did not fill exact pending target capacity", before)
	}
	response := f.put("next content", tip, 503)
	if response["__type"] != "ServiceUnavailable" || !slices.Equal(before, f.counts()) {
		t.Fatal("capacity ref refusal", response, f.counts())
	}
	after := f.git("GetBranch", map[string]any{"branchName": "main"}, 200)["branch"].(map[string]any)
	if after["commitId"] != tip {
		t.Fatal(after)
	}
	for range 16 {
		ok, err := f.rules.DeliverOne(t.Context(), func(ctx context.Context, tx *sql.Tx, arn, body string, allow func(string) error) (string, error) {
			topic := f.topic
			if arn == other.ARN {
				topic = other
			}
			return snsPublishQueuesTx(tx, topic, body, "", allow)
		})
		if !ok || err != nil {
			t.Fatal("failed pending delivery", ok, err)
		}
	}
	result := f.put("next content", tip, 200)
	if result["commitId"] == tip || f.count("aws_notification_outbox") != 2048 || f.count("aws_notification_deliveries") != 16 {
		t.Fatal(result, f.counts())
	}
}

func TestNotificationStoredFrameReadback(t *testing.T) {
	f := branchFixture(t, filepath.Join(t.TempDir(), "frame.db"))
	f.createResources()
	name := "Wanted"
	f.execute(`CREATE TEMP TRIGGER rename_rule AFTER UPDATE ON aws_notification_rules BEGIN UPDATE aws_notification_rules SET name='Other' WHERE arn=NEW.arn; END`)
	_, err := f.rules.Run(t.Context(), "UpdateNotificationRule", f.region, core.Administrator, codestarnotifications.Input{ARN: f.rule, Name: &name}, func(string, string) string { return "allowed" })
	if err == nil {
		t.Fatal("wrong saved name acknowledged")
	}
	f.execute("DROP TRIGGER rename_rule")
	if got := f.ruleCall("DescribeNotificationRule", codestarnotifications.Input{ARN: f.rule})["Name"]; got != "Branches" {
		t.Fatal("old name changed", got)
	}
	f.execute(`CREATE TEMP TRIGGER change_envelope AFTER INSERT ON aws_notification_outbox BEGIN UPDATE aws_notification_outbox SET envelope='{}' WHERE seq=NEW.seq; END`)
	before := f.counts()
	f.put("first", "", 500)
	if !slices.Equal(before, f.counts()) {
		t.Fatal("altered envelope committed", f.counts())
	}
	f.execute("DROP TRIGGER change_envelope")
	f.put("first", "", 200)
	before = f.counts()
	f.execute(`CREATE TEMP TRIGGER restore_pending AFTER DELETE ON aws_notification_outbox BEGIN
	INSERT INTO aws_notification_outbox(rule_arn,rule_generation,topic_arn,topic_generation,event_id,envelope,topology)
	VALUES(OLD.rule_arn,OLD.rule_generation,OLD.topic_arn,OLD.topic_generation,OLD.event_id,OLD.envelope,OLD.topology); END`)
	if ok, err := f.deliver(t.Context()); ok || err == nil || !slices.Equal(before, f.counts()) {
		t.Fatal("restored pending row double-delivery", ok, err, f.counts())
	}
	f.execute("DROP TRIGGER restore_pending")
	if ok, err := f.deliver(t.Context()); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := f.deliver(t.Context()); ok || err != nil {
		t.Fatal("duplicate delivery", ok, err)
	}
}

func TestNotificationInFlightRollback(t *testing.T) {
	for _, phase := range []string{"capture", "publish"} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(phase+"/"+cause.Error(), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "team.db")
				f := branchFixture(t, path)
				f.createResources()
				tip := f.put("first", "", 200)["commitId"].(string)
				before := f.counts()
				var ctx context.Context
				var cancel context.CancelFunc
				var witnessed bool
				hook := "CREATE TEMP TRIGGER interrupt_target AFTER INSERT ON aws_notification_outbox BEGIN SELECT interrupt_notification(" +
					"(SELECT count(*) FROM aws_notification_outbox)=2 AND EXISTS(SELECT 1 FROM aws_codecommit_branches WHERE tip=json_extract(NEW.envelope,'$.detail.commitId'))); END"
				if phase == "publish" {
					hook = "CREATE TEMP TRIGGER interrupt_target AFTER INSERT ON aws_notification_deliveries BEGIN SELECT interrupt_notification(" +
						"(SELECT count(*) FROM aws_sqs_messages)=2 AND (SELECT count(*) FROM aws_sns_deliveries)=2 AND EXISTS(SELECT 1 FROM aws_sns_messages WHERE id=NEW.message_id)); END"
				}
				conn, err := f.team.Database().Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				err = conn.Raw(func(raw any) error {
					return raw.(sqlitedriver.Conn).Raw().CreateFunction("interrupt_notification", 1, 0, func(rc sqlite3.Context, a ...sqlite3.Value) {
						witnessed = a[0].Bool()
						if cause == context.Canceled {
							cancel()
						}
						select {
						case <-ctx.Done():
							rc.ResultNull()
						case <-time.After(5 * time.Second):
							rc.ResultError(errors.New("request deadline did not fire"))
						}
					})
				})
				if err == nil {
					_, err = conn.ExecContext(t.Context(), hook)
				}
				conn.Close()
				if err != nil {
					t.Fatal(err)
				}
				if cause == context.Canceled {
					ctx, cancel = context.WithCancel(t.Context())
				} else {
					ctx, cancel = context.WithTimeout(t.Context(), 450*time.Millisecond)
				}
				defer cancel()
				if phase == "capture" {
					f.gitContext(ctx, "PutFile", map[string]any{"branchName": "main", "filePath": "note.txt", "fileContent": []byte("second"), "parentCommitId": tip}, 500)
				} else {
					ok, err := f.deliver(ctx)
					if ok || err == nil {
						t.Fatal("publisher acknowledged interrupted delivery", ok, err)
					}
				}
				if !witnessed || !errors.Is(ctx.Err(), cause) {
					t.Fatal("wrong transaction phase", witnessed, ctx.Err())
				}
				f.team.Close()
				g := branchFixture(t, path)
				g.topic = f.topic
				if !slices.Equal(before, g.counts()) {
					t.Fatal("interrupted transaction changed durable rows", before, g.counts())
				}
				current := g.git("GetBranch", map[string]any{"branchName": "main"}, 200)["branch"].(map[string]any)
				if current["commitId"] != tip {
					t.Fatal("tip changed", current)
				}
				if phase == "capture" {
					g.put("second", tip, 200)
				}
				for n := g.count("aws_notification_outbox"); n > 0; n-- {
					if ok, err := g.deliver(t.Context()); !ok || err != nil {
						t.Fatal(ok, err)
					}
				}
				expected := 1
				if phase == "capture" {
					expected = 2
				}
				if g.count("aws_sns_messages") != expected || g.count("aws_sqs_messages") != 2*expected || g.count("aws_notification_deliveries") != expected {
					t.Fatal(g.counts())
				}
			})
		}
	}
}

func TestNotificationFilterPagination(t *testing.T) {
	f := branchFixture(t, filepath.Join(t.TempDir(), "filter.db"))
	f.createResources()
	rules := []string{f.rule, f.createRule("Second"), f.createRule("Third")}
	filter := []codestarnotifications.Filter{{Name: "RESOURCE", Value: "missing"}, {Name: "RESOURCE", Value: f.source}, {Name: "CREATED_BY", Value: core.Administrator}, {Name: "EVENT_TYPE_ID", Value: codestarnotifications.BranchUpdated}}
	one := 1
	in := codestarnotifications.Input{Filters: filter, MaxResults: &one}
	for index := len(rules) - 1; index >= 0; index-- {
		page := f.ruleCall("ListNotificationRules", in)
		items := page["NotificationRules"].([]map[string]string)
		if len(items) != 1 || items[0]["Arn"] != rules[index] {
			t.Fatal(page)
		}
		token, _ := page["NextToken"].(string)
		if (index == 0) != (token == "") {
			t.Fatal(page)
		}
		in.NextToken = token
		slices.Reverse(filter)
	}
	in.Filters = []codestarnotifications.Filter{{Name: "RESOURCE", Value: f.source}, {Name: "CREATED_BY", Value: "nobody"}}
	if result := f.ruleCall("ListNotificationRules", in)["NotificationRules"].([]map[string]string); len(result) != 0 {
		t.Fatal(result)
	}
}

func TestNotificationTargetStatusUsesCurrentCreatorPolicyBeforeAnyEvent(t *testing.T) {
	f := branchFixture(t, filepath.Join(t.TempDir(), "creator-policy.db"))
	f.createResources()
	const owner = "notification-manager"
	ids := core.NewIdentities(f.team)
	set := func(effect string) {
		t.Helper()
		doc, err := core.ParsePolicy([]byte(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":%q,"Action":"sqs:SendMessage","Resource":"*"}]}`, effect)))
		if err != nil {
			t.Fatal(err)
		}
		if err = ids.Set(owner, doc); err != nil {
			t.Fatal(err)
		}
	}
	set("Allow")
	name, detail := "Manager rule", "BASIC"
	targets := []codestarnotifications.Target{{Type: "SNS", Address: f.topic.ARN}}
	events := []string{codestarnotifications.BranchCreated}
	result, err := f.rules.Run(t.Context(), "CreateNotificationRule", f.region, owner, codestarnotifications.Input{
		Name: &name, DetailType: &detail, Events: &events, Source: f.source, Targets: &targets, Token: "registered",
	}, func(string, string) string { return "allowed" })
	if err != nil {
		t.Fatal(err)
	}
	arn := result["Arn"].(string)
	expect := func(want string) {
		t.Helper()
		state := f.ruleCall("DescribeNotificationRule", codestarnotifications.Input{ARN: arn})
		if state["Targets"].([]map[string]string)[0]["TargetStatus"] != want || f.count("aws_notification_outbox") != 0 || f.count("aws_sns_messages") != 0 {
			t.Fatal(state, f.counts())
		}
	}
	expect("ACTIVE")
	set("Deny")
	expect("UNREACHABLE")
	set("Allow")
	expect("ACTIVE")
	if err := ids.Delete(owner); err != nil {
		t.Fatal(err)
	}
	set("Allow")
	expect("UNREACHABLE")
}
