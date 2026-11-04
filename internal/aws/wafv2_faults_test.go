package aws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestWAFByteMatchPositionsAndBinaryValues(t *testing.T) {
	for _, c := range []struct {
		position, search, input string
		match                   bool
	}{{"EXACTLY", "foo", "foo", true}, {"EXACTLY", "foo", "food", false}, {"STARTS_WITH", "foo", "food", true}, {"STARTS_WITH", "foo", "afoo", false}, {"ENDS_WITH", "foo", "afoo", true}, {"ENDS_WITH", "foo", "fooa", false}, {"CONTAINS", "\x00foo", "a\x00foob", true}, {"CONTAINS", "foo", "bar", false}, {"CONTAINS_WORD", "foo", "afoo food (foo) end", true}, {"CONTAINS_WORD", "foo", "afoo food", false}, {"CONTAINS_WORD", "foo", "foo", true}, {"CONTAINS_WORD", "foo", "bar", false}, {"CONTAINS_WORD", "foo", "foo_bar", false}} {
		statement := WAFStatement{ByteMatchStatement: &WAFByteMatch{SearchString: base64.StdEncoding.EncodeToString([]byte(c.search)), PositionalConstraint: c.position, FieldToMatch: WAFField{Body: &WAFBody{"CONTINUE"}}, TextTransformations: []WAFTransformation{{0, "NONE"}}}}
		if _, e := wafStatement(statement, 0); e != nil {
			t.Fatal(e)
		}
		if got := wafMatch(statement, "", []byte(c.input)); got != c.match {
			t.Fatal(c, got)
		}
	}
	// A low priority number runs first even if declared last; default Block is honored.
	store, stage := wafFixture(t)
	acl := wafTestACL()
	acl.DefaultAction = WAFAction{Block: &WAFEmpty{}}
	acl.Rules = nil
	summary, e := store.Create(context.Background(), wafTestRegion, acl)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Associate(context.Background(), wafTestRegion, stage, summary.ARN); e != nil {
		t.Fatal(e)
	}
	d, e := store.Evaluate(context.Background(), wafTestRegion, stage, "/safe", nil)
	if e != nil || d.Action != "BLOCK" || d.TerminatingRuleID != "Default_Action" {
		t.Fatal(d, e)
	}
}
func TestWAFStoreNativeAndCFNTransactionsRollback(t *testing.T) {
	store, stage := wafFixture(t)
	ctx := context.Background()
	tx, e := store.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	summary, e := CreateWebACLTx(ctx, tx, wafTestRegion, wafTestACL())
	if e != nil {
		t.Fatal(e)
	}
	acl, lock, e := GetWebACLTx(ctx, tx, wafTestRegion, summary.ID, summary.Name)
	if e != nil || acl.ARN != summary.ARN || lock != summary.LockToken {
		t.Fatal(acl, lock, e)
	}
	if e = AssociateWebACLTx(ctx, tx, wafTestRegion, stage, summary.ARN); e != nil {
		t.Fatal(e)
	}
	if e = DisassociateWebACLTx(ctx, tx, wafTestRegion, stage); e != nil {
		t.Fatal(e)
	}
	if e = DeleteWebACLTx(ctx, tx, wafTestRegion, summary.ID, summary.Name, lock); e != nil {
		t.Fatal(e)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	if _, _, e = store.Get(ctx, wafTestRegion, summary.ID, summary.Name); iamCode(e) != "WAFNonexistentItemException" {
		t.Fatal(e)
	}
	if _, e = store.db.Exec(`CREATE TRIGGER reject_waf_tags BEFORE INSERT ON aws_wafv2_tags BEGIN SELECT RAISE(ABORT,'test tag failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e = store.Create(ctx, wafTestRegion, wafTestACL()); e == nil {
		t.Fatal("tag failure did not abort create")
	}
	list, e := store.List(ctx, wafTestRegion, "", 100)
	if e != nil || len(list) != 0 {
		t.Fatal("tag failure left acl", list, e)
	}
	if _, e = store.db.Exec("DROP TRIGGER reject_waf_tags"); e != nil {
		t.Fatal(e)
	}
	summary, e = store.Create(ctx, wafTestRegion, wafTestACL())
	if e != nil {
		t.Fatal(e)
	}
	acl, lock, e = store.Get(ctx, wafTestRegion, summary.ID, summary.Name)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.db.Exec(`CREATE TRIGGER reject_waf_update BEFORE UPDATE ON aws_wafv2_acls BEGIN SELECT RAISE(ABORT,'test write failure'); END`); e != nil {
		t.Fatal(e)
	}
	acl.Description = "must rollback"
	if _, e = store.Update(ctx, wafTestRegion, acl, lock); e == nil {
		t.Fatal("failed update reported success")
	}
	read, current, e := store.Get(ctx, wafTestRegion, summary.ID, summary.Name)
	if e != nil || read.Description != "" || current != lock {
		t.Fatal(read, current, e)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = store.Create(cancelCtx, wafTestRegion, wafTestACL()); e == nil {
		t.Fatal("canceled create succeeded")
	}
	if _, e = store.Update(cancelCtx, wafTestRegion, acl, lock); e == nil {
		t.Fatal("canceled update succeeded")
	}
	if e = store.Delete(cancelCtx, wafTestRegion, summary.ID, summary.Name, lock); e == nil {
		t.Fatal("canceled delete succeeded")
	}
	if e = store.Associate(cancelCtx, wafTestRegion, stage, summary.ARN); e == nil {
		t.Fatal("canceled associate succeeded")
	}
	if e = store.Disassociate(cancelCtx, wafTestRegion, stage); e == nil {
		t.Fatal("canceled disassociate succeeded")
	}
}
func TestWAFConcurrentUpdatesHaveOneWinner(t *testing.T) {
	store, _ := wafFixture(t)
	ctx := context.Background()
	summary, e := store.Create(ctx, wafTestRegion, wafTestACL())
	if e != nil {
		t.Fatal(e)
	}
	acl, lock, e := store.Get(ctx, wafTestRegion, summary.ID, summary.Name)
	if e != nil {
		t.Fatal(e)
	}
	start := make(chan struct{})
	result := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { <-start; _, e := store.Update(ctx, wafTestRegion, acl, lock); result <- e })
	}
	close(start)
	wg.Wait()
	close(result)
	wins, conflicts := 0, 0
	for e := range result {
		if e == nil {
			wins++
		} else if iamCode(e) == "WAFOptimisticLockException" {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal(wins, conflicts)
	}
}
func TestWAFDamagedStateAndMissingTablesFailClosed(t *testing.T) {
	for _, table := range []string{"aws_wafv2_acls", "aws_wafv2_associations", "aws_wafv2_tags", "aws_apigateway_stages"} {
		t.Run(table, func(t *testing.T) {
			store, stage := wafFixture(t)
			ctx := context.Background()
			summary, e := store.Create(ctx, wafTestRegion, wafTestACL())
			if e != nil {
				t.Fatal(e)
			}
			if e = store.Associate(ctx, wafTestRegion, stage, summary.ARN); e != nil {
				t.Fatal(e)
			}
			acl, token, e := store.Get(ctx, wafTestRegion, summary.ID, summary.Name)
			if e != nil {
				t.Fatal(e)
			}
			if table == "aws_wafv2_acls" || table == "aws_apigateway_stages" {
				if _, e = store.db.Exec("DELETE FROM aws_wafv2_associations"); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = store.db.Exec("DROP TABLE " + table); e != nil {
				t.Fatal(e)
			}
			var checks []func() error
			switch table {
			case "aws_wafv2_acls":
				checks = []func() error{func() error { _, e := store.Create(ctx, wafTestRegion, wafTestACL()); return e }, func() error { _, _, e := store.Get(ctx, wafTestRegion, summary.ID, summary.Name); return e }, func() error { _, e := store.Update(ctx, wafTestRegion, acl, token); return e }, func() error { _, e := store.List(ctx, wafTestRegion, "", 100); return e }, func() error { _, e := store.ForResource(ctx, wafTestRegion, stage); return e }, func() error { _, e := store.Resources(ctx, wafTestRegion, summary.ARN); return e }, func() error { _, e := store.Tags(ctx, wafTestRegion, summary.ARN); return e }}
			case "aws_wafv2_associations":
				checks = []func() error{func() error { return store.Associate(ctx, wafTestRegion, stage, summary.ARN) }, func() error { return store.Disassociate(ctx, wafTestRegion, stage) }, func() error { return store.Delete(ctx, wafTestRegion, summary.ID, summary.Name, token) }, func() error { _, e := store.Resources(ctx, wafTestRegion, summary.ARN); return e }, func() error { _, e := store.Evaluate(ctx, wafTestRegion, stage, "/safe", nil); return e }}
			case "aws_wafv2_tags":
				checks = []func() error{func() error {
					a := wafTestACL()
					a.Name = "second"
					_, e := store.Create(ctx, wafTestRegion, a)
					return e
				}, func() error { _, e := store.Tags(ctx, wafTestRegion, summary.ARN); return e }}
			case "aws_apigateway_stages":
				checks = []func() error{func() error { return store.Associate(ctx, wafTestRegion, stage, summary.ARN) }}
			}
			for _, check := range checks {
				if e = check(); e == nil {
					t.Fatal("missing table reported success", table)
				}
			}
		})
	}
	store, stage := wafFixture(t)
	ctx := context.Background()
	summary, e := store.Create(ctx, wafTestRegion, wafTestACL())
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Associate(ctx, wafTestRegion, stage, summary.ARN); e != nil {
		t.Fatal(e)
	}
	for _, document := range []string{`not-json`, `{"Name":"wrong"}`, strings.Replace(wafJSON(t, wafTestACL()), `"Name":"incident"`, `"Name":"tampered"`, 1)} {
		if _, e = store.db.Exec("UPDATE aws_wafv2_acls SET document=?", document); e != nil {
			t.Fatal(e)
		}
		if _, _, e = store.Get(ctx, wafTestRegion, summary.ID, summary.Name); e == nil {
			t.Fatal("damaged state read succeeded")
		}
		if _, e = store.List(ctx, wafTestRegion, "", 100); e == nil {
			t.Fatal("damaged summary listed")
		}
		if _, e = store.Evaluate(ctx, wafTestRegion, stage, "/safe", nil); e == nil {
			t.Fatal("damaged WAF failed open")
		}
	}
}

func TestWAFRegionalAssociationValidationAndPagination(t *testing.T) {
	store, stage := wafFixture(t)
	ctx := context.Background()
	summary, e := store.Create(ctx, wafTestRegion, wafTestACL())
	if e != nil {
		t.Fatal(e)
	}
	for _, resource := range []string{"bad", strings.Replace(stage, "us-east-1", "us-west-2", 1)} {
		if e = store.Associate(ctx, wafTestRegion, resource, summary.ARN); iamCode(e) != "WAFInvalidParameterException" {
			t.Fatal(e)
		}
		if e = store.Disassociate(ctx, wafTestRegion, resource); iamCode(e) != "WAFInvalidParameterException" {
			t.Fatal(e)
		}
	}
	for _, arn := range []string{"bad", strings.Replace(summary.ARN, "/incident/", "/invalid name/", 1), strings.Replace(summary.ARN, "us-east-1", "us-west-2", 1)} {
		if e = store.Associate(ctx, wafTestRegion, stage, arn); iamCode(e) != "WAFInvalidParameterException" {
			t.Fatal(e)
		}
		if _, e = store.Resources(ctx, wafTestRegion, arn); iamCode(e) != "WAFInvalidParameterException" {
			t.Fatal(e)
		}
		if _, e = store.Tags(ctx, wafTestRegion, arn); iamCode(e) != "WAFInvalidParameterException" {
			t.Fatal(e)
		}
	}
	missingStage := strings.Replace(stage, "/prod", "/missing", 1)
	if e = store.Associate(ctx, wafTestRegion, missingStage, summary.ARN); iamCode(e) != "WAFNonexistentItemException" {
		t.Fatal(e)
	}
	missingACL := strings.Replace(summary.ARN, summary.Name, "missing", 1)
	if e = store.Associate(ctx, wafTestRegion, stage, missingACL); iamCode(e) != "WAFNonexistentItemException" {
		t.Fatal(e)
	}
	a := wafTestACL()
	a.Name = "second"
	if _, e = store.Create(ctx, wafTestRegion, a); e != nil {
		t.Fatal(e)
	}
	w := wafCall(store, "ListWebACLs", `{"Scope":"REGIONAL","Limit":1}`, wafAllow)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var page struct {
		WebACLs    []WAFSummary
		NextMarker string
	}
	if e = json.Unmarshal(w.Body.Bytes(), &page); e != nil || len(page.WebACLs) != 1 || page.NextMarker == "" {
		t.Fatal(page, e)
	}
	w = wafCall(store, "ListWebACLs", wafJSON(t, map[string]any{"Scope": "REGIONAL", "Limit": 1, "NextMarker": page.NextMarker}), wafAllow)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var last struct {
		WebACLs    []WAFSummary
		NextMarker string
	}
	if e = json.Unmarshal(w.Body.Bytes(), &last); e != nil || len(last.WebACLs) != 1 || last.NextMarker != "" || last.WebACLs[0].ID == page.WebACLs[0].ID {
		t.Fatal(last, e)
	}
	w = wafCall(store, "ListResourcesForWebACL", wafJSON(t, map[string]string{"WebACLArn": summary.ARN, "ResourceType": "CLOUDFRONT"}), wafAllow)
	if w.Code != 400 {
		t.Fatal(w.Body.String())
	}
	w = wafCall(store, "GetWebACLForResource", wafJSON(t, map[string]string{"ResourceArn": stage}), wafAllow)
	if w.Code != 200 || w.Body.String() != "{}\n" {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestWAFValidationBounds(t *testing.T) {
	for _, mutate := range []func(*WAFWebACL){
		func(a *WAFWebACL) { a.Rules = make([]WAFRule, 101) }, func(a *WAFWebACL) { a.Tags = make([]WAFTag, 51) }, func(a *WAFWebACL) { a.Tags = []WAFTag{{"aws:reserved", "bad"}} }, func(a *WAFWebACL) { a.Rules[0].Priority = -1 }, func(a *WAFWebACL) { a.Rules[0].Statement = WAFStatement{} }, func(a *WAFWebACL) { a.Rules[0].Statement.ByteMatchStatement = &WAFByteMatch{} }, func(a *WAFWebACL) { a.Rules[0].Statement.AndStatement.Statements = nil }, func(a *WAFWebACL) {
			b := a.Rules[0].Statement.AndStatement.Statements[0].ByteMatchStatement
			b.PositionalConstraint = "BAD"
		}, func(a *WAFWebACL) {
			b := a.Rules[0].Statement.AndStatement.Statements[0].ByteMatchStatement
			b.PositionalConstraint = "CONTAINS_WORD"
		}, func(a *WAFWebACL) {
			b := a.Rules[0].Statement.AndStatement.Statements[0].ByteMatchStatement
			b.FieldToMatch.Body = &WAFBody{"MATCH"}
		}, func(a *WAFWebACL) {
			b := a.Rules[0].Statement.AndStatement.Statements[0].ByteMatchStatement
			b.TextTransformations = nil
		}, func(a *WAFWebACL) {
			b := a.Rules[0].Statement.AndStatement.Statements[0].ByteMatchStatement
			b.TextTransformations = []WAFTransformation{{0, "NONE"}, {0, "URL_DECODE"}}
		}, func(a *WAFWebACL) {
			s := a.Rules[0].Statement
			for range 5 {
				s = WAFStatement{AndStatement: &WAFAnd{[]WAFStatement{s, s}}}
			}
			a.Rules[0].Statement = s
		},
	} {
		a := wafTestACL()
		mutate(&a)
		if e := wafValidate(wafTestRegion, &a); e == nil {
			t.Fatal("invalid rule accepted", a)
		}
	}
}

func TestWAFAssociatedStageCannotBeDeletedOrReused(t *testing.T) {
	store, stage := wafFixture(t)
	ctx := context.Background()
	summary, e := store.Create(ctx, wafTestRegion, wafTestACL())
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Associate(ctx, wafTestRegion, stage, summary.ARN); e != nil {
		t.Fatal(e)
	}
	api, name, e := wafStageParts(wafTestRegion, stage)
	if e != nil {
		t.Fatal(e)
	}
	apis, e := NewAPIGatewayStore(store.db)
	if e != nil {
		t.Fatal(e)
	}
	if e = apis.DeleteStage(ctx, wafTestRegion, api, name); e == nil {
		t.Fatal("associated stage deleted")
	}
	if e = apis.DeleteAPI(ctx, wafTestRegion, api); e == nil {
		t.Fatal("associated API deleted")
	}
	if _, e = apis.Stage(ctx, wafTestRegion, api, name); e != nil {
		t.Fatal("failed deletion altered state", e)
	}
	if e = store.Disassociate(ctx, wafTestRegion, stage); e != nil {
		t.Fatal(e)
	}
	if e = apis.DeleteAPI(ctx, wafTestRegion, api); e != nil {
		t.Fatal(e)
	}
}

func TestWAFRequiresAPIStateBeforeSchemaInitialization(t *testing.T) {
	db, _ := s3TestState(t)
	if _, e := NewWAFStore(db.Database()); e == nil {
		t.Fatal("WAF initialized before API Gateway schema")
	}
	var n int
	if e := db.Database().QueryRow("SELECT count(*) FROM sqlite_schema WHERE name LIKE 'aws_wafv2_%'").Scan(&n); e != nil || n != 0 {
		t.Fatal("failed constructor left partial state", n, e)
	}
}

func TestWAFTrustedPreallocatedIDIsValidatedAndNotNativeInput(t *testing.T) {
	store, _ := wafFixture(t)
	ctx := context.Background()
	tx, e := store.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	for _, id := range []string{"", "not-a-uuid", "01234567-89AB-4def-8123-0123456789ab", "01234567-89ab-4def-8123-0123456789ab/extra"} {
		if _, e = CreateWebACLWithIDTx(ctx, tx, wafTestRegion, wafTestACL(), id); iamCode(e) != "WAFInvalidParameterException" {
			t.Fatal(id, e)
		}
	}
	const id = "01234567-89ab-4def-8123-0123456789ab"
	summary, e := CreateWebACLWithIDTx(ctx, tx, wafTestRegion, wafTestACL(), id)
	if e != nil || summary.ID != id || summary.ARN != wafARN(wafTestRegion, "incident", id) {
		t.Fatal(summary, e)
	}
	acl := wafTestACL()
	acl.Name = "distinct-name"
	if _, e = CreateWebACLWithIDTx(ctx, tx, wafTestRegion, acl, id); iamCode(e) != "WAFDuplicateItemException" {
		t.Fatal("same preallocated ID reused", e)
	}
	if _, lock, e := GetWebACLTx(ctx, tx, wafTestRegion, id, "incident"); e != nil || lock != summary.LockToken {
		t.Fatal(lock, e)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	if _, _, e = store.Get(ctx, wafTestRegion, id, "incident"); iamCode(e) != "WAFNonexistentItemException" {
		t.Fatal("rolled-back trusted ID remained", e)
	}
	native := wafTestACL()
	w := wafCall(store, "CreateWebACL", wafJSON(t, map[string]any{"Name": native.Name, "Id": id, "Scope": "REGIONAL", "DefaultAction": native.DefaultAction, "Rules": native.Rules, "VisibilityConfig": native.VisibilityConfig}), wafAllow)
	if w.Code != 400 {
		t.Fatal("native caller supplied ID accepted", w.Code, w.Body.String())
	}
}
