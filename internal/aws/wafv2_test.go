package aws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

const wafTestRegion = "us-east-1"

func wafFixture(t *testing.T) (*WAFStore, string) {
	t.Helper()
	db, functions, _ := lambdaState(t)
	apis, e := NewAPIGatewayStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	api, e := apis.CreateAPI(context.Background(), wafTestRegion, "waf-stage", "", nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = functions.save(&LambdaFunction{Name: "waf-target", Region: wafTestRegion, Role: "arn:aws:iam::000000000000:role/executor", Code: []byte("dummy")}, true, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = apis.PutMethod(context.Background(), wafTestRegion, api.ID, api.RootResourceID, "POST", nil); e != nil {
		t.Fatal(e)
	}
	if _, e = apis.PutIntegration(context.Background(), wafTestRegion, api.ID, api.RootResourceID, "POST", "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:waf-target/invocations"); e != nil {
		t.Fatal(e)
	}
	deployment, e := apis.CreateDeployment(context.Background(), wafTestRegion, api.ID, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = apis.CreateStage(context.Background(), wafTestRegion, api.ID, "prod", deployment.ID, nil); e != nil {
		t.Fatal(e)
	}
	store, e := NewWAFStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	return store, "arn:aws:apigateway:us-east-1::/restapis/" + api.ID + "/stages/prod"
}
func wafTestACL() WAFWebACL {
	return WAFWebACL{Name: "incident", DefaultAction: WAFAction{Allow: &WAFEmpty{}}, VisibilityConfig: WAFVisibility{true, true, "incident"}, Tags: []WAFTag{{"TenkaCloud:NamePrefix", "dummy"}}, Rules: []WAFRule{{Name: "detect-batch-sqli", Priority: 0, Action: WAFAction{Count: &WAFEmpty{}}, VisibilityConfig: WAFVisibility{true, true, "detect-batch-sqli"}, Statement: WAFStatement{AndStatement: &WAFAnd{[]WAFStatement{
		{ByteMatchStatement: &WAFByteMatch{SearchString: base64.StdEncoding.EncodeToString([]byte("/wp-json/batch/v1")), FieldToMatch: WAFField{UriPath: &WAFEmpty{}}, PositionalConstraint: "ENDS_WITH", TextTransformations: []WAFTransformation{{0, "NONE"}}}},
		{SqliMatchStatement: &WAFSQLiMatch{FieldToMatch: WAFField{Body: &WAFBody{"CONTINUE"}}, TextTransformations: []WAFTransformation{{0, "URL_DECODE"}}}},
	}}}}}}
}
func wafCall(store *WAFStore, operation, body string, authorize func(string, string) bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.Header.Set("X-Amz-Target", "AWSWAF_20190729."+operation)
	w := httptest.NewRecorder()
	store.ServeHTTP(w, r, wafTestRegion, authorize)
	return w
}
func wafAllow(string, string) bool { return true }
func wafJSON(t *testing.T, x any) string {
	t.Helper()
	b, e := json.Marshal(x)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func TestWAFOriginalRuleAndGeneralSQLiEvaluation(t *testing.T) {
	store, stage := wafFixture(t)
	ctx := context.Background()
	acl := wafTestACL()
	summary, e := store.Create(ctx, wafTestRegion, acl)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Associate(ctx, wafTestRegion, stage, summary.ARN); e != nil {
		t.Fatal(e)
	}
	// Original template SQLI_CATEGORY, plus unrelated token patterns; not a UNION shortcut.
	attacks := []string{`{"requests": [{"method": "GET", "path": "/wp2shell-lab/v1/reports/", "body": {"category": "x' UNION SELECT id, marker, note FROM vault -- "}}]}`, "x' UNION SELECT id, marker, note FROM vault -- ", "1 OR 1=1 -- ", "' OR '1'='1' -- ", "1; DROP TABLE accounts -- ", "x%27%20UNION%20SELECT%20id%2C%20marker%2C%20note%20FROM%20vault%20--%20"}
	for _, attack := range attacks {
		decision, e := store.Evaluate(ctx, wafTestRegion, stage, "/wp-json/batch/v1", []byte(attack))
		if e != nil || decision.Action != "ALLOW" || len(decision.CountedRules) != 1 || decision.TerminatingRuleID != "Default_Action" {
			t.Fatalf("Count failed for %q: %+v %v", attack, decision, e)
		}
	}
	saved, token, e := store.Get(ctx, wafTestRegion, summary.ID, summary.Name)
	if e != nil {
		t.Fatal(e)
	}
	saved.Rules[0].Action = WAFAction{Block: &WAFEmpty{}}
	if _, e = store.Update(ctx, wafTestRegion, saved, token); e != nil {
		t.Fatal(e)
	}
	for _, attack := range attacks {
		decision, e := store.Evaluate(ctx, wafTestRegion, stage, "/prefix/wp-json/batch/v1", []byte(attack))
		if e != nil || decision.Action != "BLOCK" || decision.TerminatingRuleID != "detect-batch-sqli" || decision.WebACLARN != summary.ARN {
			t.Fatalf("Block failed for %q: %+v %v", attack, decision, e)
		}
	}
	benign := []string{"", `{"requests":[{"method":"GET","path":"/wp/v2/posts"}]}`, "The labor union selected a new representative", `{"category":"news","search":"summer sale"}`, "ordinary %ZZ invalid escaping"}
	for _, body := range benign {
		d, e := store.Evaluate(ctx, wafTestRegion, stage, "/wp-json/batch/v1", []byte(body))
		if e != nil || d.Action != "ALLOW" || len(d.CountedRules) != 0 {
			t.Fatalf("false positive %q: %+v %v", body, d, e)
		}
	}
	for _, path := range []string{"/wp-json/posts", "/wp-json/batch/v1/", "/WP-json/batch/v1"} {
		d, e := store.Evaluate(ctx, wafTestRegion, stage, path, []byte(attacks[0]))
		if e != nil || d.Action != "ALLOW" {
			t.Fatal(path, d, e)
		}
	}
}
func TestWAFBodyLimitsTransformAndRuleOrdering(t *testing.T) {
	rule := wafTestACL().Rules[0].Statement
	attack := []byte("x' UNION SELECT id FROM vault -- ")
	before := string(attack)
	if !wafMatch(rule, "/wp-json/batch/v1", attack) || string(attack) != before {
		t.Fatal("original body modified or no match")
	}
	oversize := append([]byte(strings.Repeat(" ", wafBodyLimit)), attack...)
	if wafMatch(rule, "/wp-json/batch/v1", oversize) {
		t.Fatal("inspected beyond forwarded body prefix")
	}
	sql := rule.AndStatement.Statements[1].SqliMatchStatement
	sql.FieldToMatch.Body.OversizeHandling = "MATCH"
	if !wafMatch(rule, "/wp-json/batch/v1", oversize) {
		t.Fatal("MATCH oversize ignored")
	}
	sql.FieldToMatch.Body.OversizeHandling = "NO_MATCH"
	if wafMatch(rule, "/wp-json/batch/v1", append(attack, []byte(strings.Repeat(" ", wafBodyLimit))...)) {
		t.Fatal("NO_MATCH oversize ignored")
	}
	if string(wafTransform([]byte("%2527+%27%ZZ"), []WAFTransformation{{2, "URL_DECODE"}, {1, "URL_DECODE"}})) != "' '%ZZ" {
		t.Fatal("transforms not applied in priority order")
	}
	store, stage := wafFixture(t)
	ctx := context.Background()
	acl := wafTestACL()
	first := acl.Rules[0]
	first.Name = "allow-first"
	first.Priority = 0
	first.Action = WAFAction{Allow: &WAFEmpty{}}
	acl.Rules[0].Priority = 10
	acl.Rules = append(acl.Rules, first)
	summary, e := store.Create(ctx, wafTestRegion, acl)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Associate(ctx, wafTestRegion, stage, summary.ARN); e != nil {
		t.Fatal(e)
	}
	d, e := store.Evaluate(ctx, wafTestRegion, stage, "/wp-json/batch/v1", attack)
	if e != nil || d.Action != "ALLOW" || d.TerminatingRuleID != "allow-first" || len(d.CountedRules) != 0 {
		t.Fatal(d, e)
	}
	acl, token, e := store.Get(ctx, wafTestRegion, summary.ID, summary.Name)
	if e != nil {
		t.Fatal(e)
	}
	acl.Rules[1].Action = WAFAction{Count: &WAFEmpty{}}
	acl.Rules[0].Action = WAFAction{Block: &WAFEmpty{}}
	if _, e = store.Update(ctx, wafTestRegion, acl, token); e != nil {
		t.Fatal(e)
	}
	d, e = store.Evaluate(ctx, wafTestRegion, stage, "/wp-json/batch/v1", attack)
	if e != nil || d.Action != "BLOCK" || len(d.CountedRules) != 1 || d.CountedRules[0] != "allow-first" {
		t.Fatal(d, e)
	}
}
func TestWAFStoreLifecycleLocksIsolationTagsAndCleanup(t *testing.T) {
	store, stage := wafFixture(t)
	ctx := context.Background()
	summary, e := store.Create(ctx, wafTestRegion, wafTestACL())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Create(ctx, wafTestRegion, wafTestACL()); iamCode(e) != "WAFDuplicateItemException" {
		t.Fatal(e)
	}
	if _, _, e = store.Get(ctx, "us-west-2", summary.ID, summary.Name); iamCode(e) != "WAFNonexistentItemException" {
		t.Fatal(e)
	}
	reopened, e := NewWAFStore(store.db)
	if e != nil {
		t.Fatal(e)
	}
	acl, lock, e := reopened.Get(ctx, wafTestRegion, summary.ID, summary.Name)
	if e != nil || lock != summary.LockToken {
		t.Fatal(acl, lock, e)
	}
	tags, e := store.Tags(ctx, wafTestRegion, summary.ARN)
	if e != nil || len(tags) != 1 || tags[0].Key != "TenkaCloud:NamePrefix" {
		t.Fatal(tags, e)
	}
	next, e := store.Update(ctx, wafTestRegion, acl, lock)
	if e != nil || next == lock {
		t.Fatal(next, e)
	}
	if _, e = store.Update(ctx, wafTestRegion, acl, lock); iamCode(e) != "WAFOptimisticLockException" {
		t.Fatal(e)
	}
	if e = store.Delete(ctx, wafTestRegion, summary.ID, summary.Name, lock); iamCode(e) != "WAFOptimisticLockException" {
		t.Fatal(e)
	}
	if e = store.Associate(ctx, wafTestRegion, stage, summary.ARN); e != nil {
		t.Fatal(e)
	}
	resources, e := store.Resources(ctx, wafTestRegion, summary.ARN)
	if e != nil || len(resources) != 1 || resources[0] != stage {
		t.Fatal(resources, e)
	}
	if e = store.Delete(ctx, wafTestRegion, summary.ID, summary.Name, next); iamCode(e) != "WAFAssociatedItemException" {
		t.Fatal(e)
	}
	other, otherStage := wafFixture(t)
	d, e := other.Evaluate(ctx, wafTestRegion, otherStage, "/wp-json/batch/v1", []byte("1 OR 1=1 -- "))
	if e != nil || d.WebACLARN != "" || len(d.CountedRules) != 0 {
		t.Fatal("team boundary crossed", d, e)
	}
	if e = store.Disassociate(ctx, wafTestRegion, stage); e != nil {
		t.Fatal(e)
	}
	if e = store.Disassociate(ctx, wafTestRegion, stage); e != nil {
		t.Fatal(e)
	}
	if e = store.Delete(ctx, wafTestRegion, summary.ID, summary.Name, next); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = store.db.QueryRow("SELECT count(*) FROM aws_wafv2_tags").Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	if _, _, e = store.Get(ctx, wafTestRegion, summary.ID, summary.Name); iamCode(e) != "WAFNonexistentItemException" {
		t.Fatal(e)
	}
	list, e := store.List(ctx, wafTestRegion, "", 100)
	if e != nil || len(list) != 0 {
		t.Fatal(list, e)
	}
}
func TestWAFHTTPNativeLifecycleAndIAM(t *testing.T) {
	store, stage := wafFixture(t)
	acl := wafTestACL()
	create := map[string]any{"Name": acl.Name, "Scope": "REGIONAL", "DefaultAction": acl.DefaultAction, "Rules": acl.Rules, "VisibilityConfig": acl.VisibilityConfig, "Tags": acl.Tags}
	w := wafCall(store, "CreateWebACL", wafJSON(t, create), wafAllow)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct{ Summary WAFSummary }
	if e := json.Unmarshal(w.Body.Bytes(), &created); e != nil {
		t.Fatal(e)
	}
	summary := created.Summary
	get := map[string]any{"Name": summary.Name, "Id": summary.ID, "Scope": "REGIONAL"}
	w = wafCall(store, "GetWebACL", wafJSON(t, get), wafAllow)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var read struct {
		WebACL    WAFWebACL
		LockToken string
	}
	if e := json.Unmarshal(w.Body.Bytes(), &read); e != nil {
		t.Fatal(e)
	}
	create["Id"] = summary.ID
	create["LockToken"] = read.LockToken
	delete(create, "Tags")
	w = wafCall(store, "UpdateWebACL", wafJSON(t, create), wafAllow)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = wafCall(store, "UpdateWebACL", wafJSON(t, create), wafAllow)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "WAFOptimisticLockException") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, op := range []string{"GetWebACL", "DeleteWebACL"} {
		body := wafJSON(t, get)
		if op == "DeleteWebACL" {
			get["LockToken"] = read.LockToken
			body = wafJSON(t, get)
			delete(get, "LockToken")
		}
		w = wafCall(store, op, body, func(string, string) bool { return false })
		if w.Code != 403 {
			t.Fatal(op, w.Body.String())
		}
	}
	assoc := wafJSON(t, map[string]string{"ResourceArn": stage, "WebACLArn": summary.ARN})
	w = wafCall(store, "AssociateWebACL", assoc, func(action, resource string) bool { return action != "apigateway:SetWebACL" })
	if w.Code != 403 {
		t.Fatal(w.Body.String())
	}
	w = wafCall(store, "AssociateWebACL", assoc, wafAllow)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = wafCall(store, "GetWebACLForResource", wafJSON(t, map[string]string{"ResourceArn": stage}), func(action, resource string) bool { return action != "wafv2:GetWebACL" })
	if w.Code != 403 {
		t.Fatal(w.Body.String())
	}
	w = wafCall(store, "GetWebACLForResource", wafJSON(t, map[string]string{"ResourceArn": stage}), func(action, resource string) bool { return resource == summary.ARN })
	if w.Code != 200 || !strings.Contains(w.Body.String(), summary.ID) {
		t.Fatal(w.Body.String())
	}
	for op, body := range map[string]string{"ListWebACLs": `{"Scope":"REGIONAL","Limit":1}`, "ListResourcesForWebACL": wafJSON(t, map[string]string{"WebACLArn": summary.ARN, "ResourceType": "API_GATEWAY"}), "ListTagsForResource": wafJSON(t, map[string]string{"ResourceARN": summary.ARN}), "DisassociateWebACL": wafJSON(t, map[string]string{"ResourceArn": stage})} {
		w = wafCall(store, op, body, wafAllow)
		if w.Code != 200 {
			t.Fatal(op, w.Code, w.Body.String())
		}
	}
	_, lock, e := store.Get(context.Background(), wafTestRegion, summary.ID, summary.Name)
	if e != nil {
		t.Fatal(e)
	}
	get["LockToken"] = lock
	w = wafCall(store, "DeleteWebACL", wafJSON(t, get), wafAllow)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
func TestWAFStrictWireAndUnsupportedFeaturesFail(t *testing.T) {
	store, _ := wafFixture(t)
	for _, body := range []string{`{"Scope":"REGIONAL","Oops":1}`, `{"scope":"REGIONAL"}`, `{"Scope":"REGIONAL","Scope":"REGIONAL"}`, `{"Scope":null}`, `{"Scope":"REGIONAL"} {}`, `{"Scope":"CLOUDFRONT"}`, `{"Scope":"REGIONAL","Limit":-1}`, `{"Scope":"REGIONAL","NextMarker":"bad"}`, `[]`, `{"Scope":"REGIONAL","Rules":[]}`} {
		w := wafCall(store, "ListWebACLs", body, wafAllow)
		if w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	for _, op := range []string{"GetWebACL", "UpdateWebACL", "DeleteWebACL", "AssociateWebACL", "GetWebACLForResource", "ListResourcesForWebACL", "ListTagsForResource", "DisassociateWebACL"} {
		w := wafCall(store, op, `{}`, wafAllow)
		if w.Code != 400 {
			t.Fatal(op, w.Code, w.Body.String())
		}
	}
	w := wafCall(store, "Unsupported", `{}`, wafAllow)
	if w.Code != 501 {
		t.Fatal(w.Code)
	}
	w = wafCall(store, "ListWebACLs", `{"Scope":"REGIONAL"}`, nil)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	for _, mutate := range []func(*WAFWebACL){func(a *WAFWebACL) { a.DefaultAction = WAFAction{} }, func(a *WAFWebACL) { a.DefaultAction.Count = &WAFEmpty{} }, func(a *WAFWebACL) { a.Name = "invalid name" }, func(a *WAFWebACL) { a.VisibilityConfig.MetricName = "All" }, func(a *WAFWebACL) { a.Rules = append(a.Rules, a.Rules[0]) }, func(a *WAFWebACL) {
		a.Rules[0].Statement.AndStatement.Statements[0].ByteMatchStatement.SearchString = "not base64"
	}, func(a *WAFWebACL) {
		a.Rules[0].Statement.AndStatement.Statements[1].SqliMatchStatement.SensitivityLevel = "HIGH"
	}, func(a *WAFWebACL) {
		a.Rules[0].Statement.AndStatement.Statements[1].SqliMatchStatement.TextTransformations[0].Type = "UNSUPPORTED"
	}, func(a *WAFWebACL) {
		a.Rules[0].Statement.AndStatement.Statements[1].SqliMatchStatement.FieldToMatch.Body.OversizeHandling = ""
	}, func(a *WAFWebACL) { a.Tags = append(a.Tags, a.Tags[0]) }} {
		a := wafTestACL()
		mutate(&a)
		if _, e := store.Create(context.Background(), wafTestRegion, a); iamCode(e) != "WAFInvalidParameterException" {
			t.Fatal(e)
		}
	}
	a := wafTestACL()
	raw := wafJSON(t, map[string]any{"Name": a.Name, "Scope": "REGIONAL", "DefaultAction": a.DefaultAction, "Rules": a.Rules, "VisibilityConfig": a.VisibilityConfig})
	raw = strings.Replace(raw, `"UriPath":{}`, `"UriPath":{"Unsupported":1}`, 1)
	w = wafCall(store, "CreateWebACL", raw, wafAllow)
	if w.Code != 400 {
		t.Fatal(w.Body.String())
	}
}
