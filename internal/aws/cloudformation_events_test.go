package aws

import (
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func eventFixture(t *testing.T, path string) (*Gateway, *core.Store) {
	t.Helper()
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := NewParameterStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	stacks, err := NewStackStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	return &Gateway{Store: db, Parameters: parameters, Stacks: stacks, Identities: core.NewIdentities(db)}, db
}
func eventQuery(g *Gateway, name, token, region, principal, access string) *httptest.ResponseRecorder {
	form := url.Values{"Action": {"DescribeStackEvents"}, "Version": {"2010-05-15"}, "StackName": {name}}
	if token != "" {
		form.Set("NextToken", token)
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Simulator-Region", region)
	r.Header.Set("X-Simulator-Principal", principal)
	r.Header.Set("X-Simulator-Access-Role", access)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}
func parseEvents(t *testing.T, w *httptest.ResponseRecorder) ([]StackEvent, string) {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("events status %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		XMLName xml.Name `xml:"DescribeStackEventsResponse"`
		Result  struct {
			Events    []StackEvent `xml:"StackEvents>member"`
			NextToken string       `xml:"NextToken"`
		} `xml:"DescribeStackEventsResult"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Result.Events, response.Result.NextToken
}

const eventTemplate = `Parameters:
  Secret:
    Type: String
    NoEcho: true
    Default: private-value-do-not-log
Resources:
  P:
    Type: AWS::SSM::Parameter
    Properties:
      Name: /events/parameter
      Type: String
      Value: !Ref Secret
Outputs:
  SecretOutput:
    Value: !Ref Secret
`

func TestStackEventsNativeLifecycleRestartAndGeneration(t *testing.T) {
	path := t.TempDir() + "/events.db"
	g, db := eventFixture(t, path)
	w := cleanupACKQuery(g, "CreateStack", "events", eventTemplate)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	records, err := g.Stacks.Records("us-east-1", "events")
	if err != nil {
		t.Fatal(err)
	}
	id := records[0].ID
	events, token := parseEvents(t, eventQuery(g, "events", "", "us-east-1", "", ""))
	if token != "" || len(events) != 4 {
		t.Fatalf("events %d token %q", len(events), token)
	}
	for i, status := range []string{"CREATE_COMPLETE", "CREATE_COMPLETE", "CREATE_IN_PROGRESS", "CREATE_IN_PROGRESS"} {
		if events[i].ResourceStatus != status || events[i].StackID != id || events[i].EventID == "" {
			t.Fatalf("event %+v", events[i])
		}
		if _, err = time.Parse(time.RFC3339Nano, events[i].Timestamp); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(eventQuery(g, id, "", "us-east-1", "", "").Body.String(), "private-value") {
		t.Fatal("secret leaked")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	g, db = eventFixture(t, path)
	defer db.Close()
	persisted, _ := parseEvents(t, eventQuery(g, id, "", "us-east-1", "", ""))
	if len(persisted) != 4 || persisted[0].EventID != events[0].EventID {
		t.Fatal("history changed on restart")
	}
	if w = cleanupACKQuery(g, "DeleteStack", id, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	deleted, _ := parseEvents(t, eventQuery(g, id, "", "us-east-1", "", ""))
	if len(deleted) != 8 || deleted[0].ResourceStatus != "DELETE_COMPLETE" {
		t.Fatalf("deleted %+v", deleted)
	}
	if eventQuery(g, "events", "", "us-east-1", "", "").Code == 200 {
		t.Fatal("deleted generation accessible by name")
	}
	if w = cleanupACKQuery(g, "CreateStack", "events", eventTemplate); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	replacement, _ := parseEvents(t, eventQuery(g, "events", "", "us-east-1", "", ""))
	if len(replacement) != 4 || replacement[0].StackID == id {
		t.Fatal("generation cross-talk")
	}
	if eventQuery(g, "events", deleted[0].EventID, "us-east-1", "", "").Code == 200 {
		t.Fatal("cross-generation cursor accepted")
	}
	if eventQuery(g, id, "", "ap-northeast-1", "", "").Code == 200 {
		t.Fatal("cross-region history")
	}
}

func TestStackEventsAuthorizationAndPagination(t *testing.T) {
	g, db := eventFixture(t, "")
	defer db.Close()
	body := "Resources:\n"
	for i := 0; i < 60; i++ {
		body += fmt.Sprintf("  P%d:\n    Type: AWS::SSM::Parameter\n    Properties: {Name: /events/p%d, Type: String, Value: private-value-do-not-log}\n", i, i)
	}
	if w := cleanupACKQuery(g, "CreateStack", "many", body); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	first, token := parseEvents(t, eventQuery(g, "many", "", "us-east-1", "", ""))
	if len(first) != 100 || token == "" {
		t.Fatal("page bound")
	}
	second, next := parseEvents(t, eventQuery(g, "many", token, "us-east-1", "", ""))
	if len(second) != 22 || next != "" {
		t.Fatal("last page bound")
	}
	seen := map[string]bool{}
	for _, e := range append(first, second...) {
		if seen[e.EventID] {
			t.Fatal("duplicate page event")
		}
		seen[e.EventID] = true
	}
	if eventQuery(g, "many", "invalid", "us-east-1", "", "").Code == 200 {
		t.Fatal("invalid cursor")
	}
	if eventQuery(g, "", "", "us-east-1", "", "").Code == 200 {
		t.Fatal("unbounded list")
	}
	id := first[0].StackID
	policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"cloudformation:DescribeStackEvents"}, Resource: core.StringList{id}}}}
	if err := g.Identities.Set("reader", policy); err != nil {
		t.Fatal(err)
	}
	parseEvents(t, eventQuery(g, "many", "", "us-east-1", "reader", ""))
	if eventQuery(g, "many", "", "us-east-1", "reader", "participant").Code != 403 {
		t.Fatal("participant read host history")
	}
	if eventQuery(g, "many", "", "us-east-1", "unknown", "").Code != 403 {
		t.Fatal("unknown reader")
	}
	policy.Statement[0].Action = core.StringList{"cloudformation:DescribeStacks"}
	if err := g.Identities.Set("reader", policy); err != nil {
		t.Fatal(err)
	}
	if eventQuery(g, "many", "", "us-east-1", "reader", "").Code != 403 {
		t.Fatal("wrong IAM action allowed")
	}
}

func TestStackEventsFailedTransactionsDoNotPublishSuccess(t *testing.T) {
	g, db := eventFixture(t, "")
	defer db.Close()
	if _, err := db.Database().Exec(`CREATE TRIGGER reject_event BEFORE INSERT ON aws_cfn_events WHEN NEW.status='CREATE_COMPLETE' BEGIN SELECT RAISE(ABORT,'fault'); END`); err != nil {
		t.Fatal(err)
	}
	if w := cleanupACKQuery(g, "CreateStack", "events", eventTemplate); w.Code == 200 {
		t.Fatal("failed event write acknowledged")
	}
	var count int
	if err := db.Database().QueryRow("SELECT count(*) FROM aws_cfn_events").Scan(&count); err != nil || count != 0 {
		t.Fatal("rolled back create history", count, err)
	}
	if _, err := db.Database().Exec("DROP TRIGGER reject_event"); err != nil {
		t.Fatal(err)
	}
	if w := cleanupACKQuery(g, "CreateStack", "events", eventTemplate); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	before := cleanupACKFrame(t, db.Database())
	if _, err := db.Database().Exec(`CREATE TRIGGER reject_delete BEFORE DELETE ON aws_cfn_stacks BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if w := cleanupACKQuery(g, "DeleteStack", "events", ""); w.Code == 200 {
		t.Fatal("failed delete acknowledged")
	}
	// The frame hashes table rows, not trigger/schema definitions.
	if after := cleanupACKFrame(t, db.Database()); before != after {
		t.Fatal("failed delete changed data/events")
	}
	events, _ := parseEvents(t, eventQuery(g, "events", "", "us-east-1", "", ""))
	if len(events) != 4 || events[0].ResourceStatus != "CREATE_COMPLETE" {
		t.Fatal("fabricated delete success")
	}
}

func TestStackEventsRetentionAndAtomicPruning(t *testing.T) {
	g, db := eventFixture(t, "")
	defer db.Close()
	// Live history is never a pruning candidate, even if it is older.
	if w := cleanupACKQuery(g, "CreateStack", "live", strings.ReplaceAll(eventTemplate, "/events/parameter", "/events/live")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var oldest string
	for i := 0; i <= stackDeletedHistoryLimit; i++ {
		record, err := compileStack("us-east-1", "reused", eventTemplate, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if err = g.Stacks.Create(record); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldest = record.ID
		}
		if err = g.Stacks.Delete(record.Region, record.ID); err != nil {
			t.Fatal(err)
		}
	}
	var deleted, live, count int
	if err := db.Database().QueryRow(`SELECT sum(deleted),sum(1-deleted) FROM aws_cfn_event_history`).Scan(&deleted, &live); err != nil {
		t.Fatal(err)
	}
	if deleted != stackDeletedHistoryLimit || live != 1 {
		t.Fatal("retention", deleted, live)
	}
	if err := db.Database().QueryRow("SELECT count(*) FROM aws_cfn_events WHERE stack_id=?", oldest).Scan(&count); err != nil || count != 0 {
		t.Fatal("orphan events", count, err)
	}
	if eventQuery(g, oldest, "", "us-east-1", "", "").Code == 200 {
		t.Fatal("pruned history retained")
	}
	parseEvents(t, eventQuery(g, "live", "", "us-east-1", "", ""))
	// If pruning cannot commit, resource deletion and newly added events also roll back.
	if w := cleanupACKQuery(g, "CreateStack", "pending", eventTemplate); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	before := cleanupACKFrame(t, db.Database())
	if _, err := db.Database().Exec(`CREATE TRIGGER block_prune BEFORE DELETE ON aws_cfn_event_history BEGIN SELECT RAISE(ABORT,'fault'); END`); err != nil {
		t.Fatal(err)
	}
	if w := cleanupACKQuery(g, "DeleteStack", "pending", ""); w.Code == 200 {
		t.Fatal("pruning failure acknowledged")
	}
	if before != cleanupACKFrame(t, db.Database()) {
		t.Fatal("failed pruning leaked changes")
	}
}

func TestStackEventsQueryRejectsUnsupportedAndIgnoredWrites(t *testing.T) {
	g, db := eventFixture(t, "")
	defer db.Close()
	if _, err := db.Database().Exec(`CREATE TRIGGER ignore_event BEFORE INSERT ON aws_cfn_events BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if w := cleanupACKQuery(g, "CreateStack", "ignored", eventTemplate); w.Code == 200 {
		t.Fatal("ignored event write acknowledged")
	}
	var n int
	if err := db.Database().QueryRow(`SELECT count(*) FROM aws_cfn_stacks`).Scan(&n); err != nil || n != 0 {
		t.Fatal("ignored event leaked state")
	}
	for _, extra := range []string{"&MaxItems=1", "&NextToken=a&NextToken=b", "&TemplateBody=x", "&Parameters.member.1.ParameterKey=Secret&Parameters.member.1.ParameterValue=private"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader("Action=DescribeStackEvents&Version=2010-05-15&StackName=ignored"+extra))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("unsupported field status %d", w.Code)
		}
	}
}
