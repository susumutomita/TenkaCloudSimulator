package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"
	"time"
)

func quotaRequestArgs(value any, caseAllowed bool) map[string]any {
	return map[string]any{"ServiceCode": "b2bi", "QuotaCode": b2biProfilesQuota, "DesiredValue": value, "SupportCaseAllowed": caseAllowed}
}

func TestServiceQuotaIncreaseActualRequestsAndScopedLists(t *testing.T) {
	h := newHarness(t, b2bi, serviceQuotas)
	h.engine.cfg.Regions = append(h.engine.cfg.Regions, "us-west-2")
	peer := newHarness(t, b2bi, serviceQuotas)
	const submit = "RequestServiceQuotaIncrease"
	const get = "GetRequestedServiceQuotaChange"
	const list = "ListRequestedServiceQuotaChangeHistory"
	const byQuota = "ListRequestedServiceQuotaChangeHistoryByQuota"
	const arn = "arn:aws:servicequotas:us-east-1:000000000000:b2bi/L-05F6A9EF"
	target := map[string]any{"ServiceCode": "b2bi", "QuotaCode": b2biProfilesQuota}
	call := func(env *harness, region, actor, op string, in map[string]any) reply {
		t.Helper()
		wire, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		req := newJSONRequest(env, "service-quotas", op, wire, region)
		if actor != "" {
			req.Header.Set("X-Simulator-Principal", actor)
		}
		return env.do(req)
	}
	ok := func(op string, in map[string]any) map[string]any {
		t.Helper()
		r := call(h, "us-east-1", "", op, in)
		expect(t, r, 200, "")
		return r.body
	}
	save := func(value float64) map[string]any {
		t.Helper()
		rec := ok(submit, quotaRequestArgs(value, false))["RequestedQuota"].(map[string]any)
		if rec["Status"] != "NOT_APPROVED" || rec["DesiredValue"] != json.Number(fmt.Sprint(value)) || rec["CaseId"] != nil || rec["QuotaArn"] != arn || rec["Requester"] != "simulator-admin" || rec["Created"] != rec["LastUpdated"] || rec["QuotaRequestedAtLevel"] != "ACCOUNT" {
			t.Fatal(rec)
		}
		if len(rec["Id"].(string)) == 0 {
			t.Fatal("empty ID")
		}
		if got := ok(get, map[string]any{"RequestId": rec["Id"]})["RequestedQuota"]; !reflect.DeepEqual(rec, got) {
			t.Fatal(got, rec)
		}
		return rec
	}
	old := save(6.5)
	h.now = h.now.Add(90 * 24 * time.Hour)
	atCutoff := ok(list, map[string]any{})["RequestedQuotas"].([]any)
	if len(atCutoff) != 1 {
		t.Fatal(atCutoff)
	}
	h.now = h.now.Add(time.Millisecond)
	if len(ok(list, map[string]any{})["RequestedQuotas"].([]any)) != 0 {
		t.Fatal("general history included a request older than 90 days")
	}
	if len(ok(byQuota, target)["RequestedQuotas"].([]any)) != 1 || ok(get, map[string]any{"RequestId": old["Id"]})["RequestedQuota"] == nil {
		t.Fatal("old ID disappeared")
	}
	first, second := save(8.25), save(9.5)
	if first["Id"] == second["Id"] {
		t.Fatal("terminal requests shared an ID")
	}
	page := ok(list, map[string]any{"MaxResults": 1, "Status": "NOT_APPROVED"})
	items := page["RequestedQuotas"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["Id"] != second["Id"] {
		t.Fatal(page)
	}
	token := page["NextToken"].(string)
	save(10.75)
	next := ok(list, map[string]any{"MaxResults": 2, "Status": "NOT_APPROVED", "NextToken": token})
	items = next["RequestedQuotas"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["Id"] != first["Id"] || next["NextToken"] != nil {
		t.Fatal(next)
	}
	for _, bad := range []struct {
		env               *harness
		region, actor, op string
		in                map[string]any
	}{
		{h, "us-west-2", "", list, map[string]any{"Status": "NOT_APPROVED", "NextToken": token}},
		{h, "us-east-1", "different", list, map[string]any{"Status": "NOT_APPROVED", "NextToken": token}},
		{peer, "us-east-1", "", list, map[string]any{"Status": "NOT_APPROVED", "NextToken": token}},
		{h, "us-east-1", "", list, map[string]any{"Status": "PENDING", "NextToken": token}},
		{h, "us-east-1", "", byQuota, map[string]any{"ServiceCode": "b2bi", "QuotaCode": b2biProfilesQuota, "Status": "NOT_APPROVED", "NextToken": token}},
	} {
		expect(t, call(bad.env, bad.region, bad.actor, bad.op, bad.in), 400, "InvalidPaginationTokenException")
	}
	for _, op := range []string{list, byQuota} {
		args := maps.Clone(target)
		if op == list {
			delete(args, "QuotaCode")
		}
		args["QuotaRequestedAtLevel"] = "RESOURCE"
		resp := ok(op, args)
		if len(resp["RequestedQuotas"].([]any)) != 0 || resp["NextToken"] != nil {
			t.Fatal(resp)
		}
	}
	denied := map[string]struct {
		resource string
		args     map[string]any
	}{
		submit: {arn, quotaRequestArgs(12, true)}, get: {"*", map[string]any{"RequestId": "nonexistent-id"}}, list: {"*", map[string]any{"NextToken": token, "Status": "NOT_APPROVED"}}, byQuota: {arn, target},
	}
	for op, x := range denied {
		action := "servicequotas:" + op
		h.policy.deny[action+" "+x.resource] = true
		r := call(h, "us-east-1", "", op, x.args)
		expect(t, r, 400, "AccessDeniedException")
		if r.body["RequestedQuota"] != nil || r.body["RequestedQuotas"] != nil || !slices.Contains(h.policy.calls, "simulator-admin "+action+" "+x.resource) {
			t.Fatal(op, r.body)
		}
		delete(h.policy.deny, action+" "+x.resource)
	}
	for _, v := range []any{0, 5, 10000000001.0, "7", true} {
		expect(t, call(h, "us-east-1", "", submit, quotaRequestArgs(v, true)), 400, "IllegalArgumentException")
	}
	for _, member := range []string{"DesiredValue", "ServiceCode", "QuotaCode"} {
		args := quotaRequestArgs(9, false)
		delete(args, member)
		expect(t, call(h, "us-east-1", "", submit, args), 400, "IllegalArgumentException")
	}
	badCase := quotaRequestArgs(8, false)
	badCase["SupportCaseAllowed"] = "false"
	expect(t, call(h, "us-east-1", "", submit, badCase), 400, "IllegalArgumentException")
	for _, bad := range []map[string]any{{"MaxResults": 0}, {"MaxResults": "1"}, {"MaxResults": 101}, {"Status": "unknown"}, {"QuotaRequestedAtLevel": "unknown"}} {
		expect(t, call(h, "us-east-1", "", list, bad), 400, "IllegalArgumentException")
	}
	input := quotaRequestArgs(15, true)
	input["ContextId"] = "resource"
	expect(t, call(h, "us-east-1", "", submit, input), 400, "IllegalArgumentException")
	expect(t, call(h, "ap-northeast-1", "", submit, quotaRequestArgs(15, true)), 400, "NoSuchResourceException")
	pending := quotaRequestArgs(16, true)
	delete(pending, "SupportCaseAllowed")
	if rec := ok(submit, pending)["RequestedQuota"].(map[string]any); rec["Status"] != "PENDING" || rec["CaseId"] != nil {
		t.Fatal(rec)
	}
	expect(t, call(h, "us-east-1", "", submit, quotaRequestArgs(18, true)), 400, "ResourceAlreadyExistsException")
	expect(t, call(h, "us-west-2", "", submit, quotaRequestArgs(18, true)), 200, "")
	expect(t, call(peer, "us-east-1", "", submit, quotaRequestArgs(18, true)), 200, "")
	expect(t, call(peer, "us-east-1", "", get, map[string]any{"RequestId": old["Id"]}), 400, "NoSuchResourceException")
	if ok("GetServiceQuota", target)["Quota"].(map[string]any)["Value"] != json.Number("5") {
		t.Fatal("request raised the enforced capacity")
	}
}

func TestServiceQuotaIncreaseRetainedLimitAcrossRegions(t *testing.T) {
	h := newHarness(t, b2bi, serviceQuotas)
	_, err := h.db.Exec(`WITH RECURSIVE request(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM request WHERE n<?)
		INSERT INTO aws_b2bi_profile_quota_requests(id,region,requester,desired,status,created_ms)
		SELECT 'west-'||n,'us-west-2','fixture',7,'NOT_APPROVED',0 FROM request`, maxQuotaRequestRecords-1)
	if err != nil {
		t.Fatal(err)
	}
	ok := h.json1("service-quotas", "RequestServiceQuotaIncrease", quotaRequestArgs(7, false))
	expect(t, ok, 200, "")
	id := ok.body["RequestedQuota"].(map[string]any)["Id"]
	full := h.json1("service-quotas", "RequestServiceQuotaIncrease", quotaRequestArgs(9, true))
	expect(t, full, 400, "QuotaExceededException")
	if full.body["RequestedQuota"] != nil {
		t.Fatal(full.body)
	}
	var count int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM aws_b2bi_profile_quota_requests").Scan(&count); err != nil || count != maxQuotaRequestRecords {
		t.Fatalf("retained %d rows, error %v", count, err)
	}
	get := h.json1("service-quotas", "GetRequestedServiceQuotaChange", map[string]any{"RequestId": id})
	expect(t, get, 200, "")
	if !reflect.DeepEqual(ok.body, get.body) {
		t.Fatal("capacity refusal changed the saved request")
	}
}

func TestServiceQuotaIncreaseConcurrentAndFailedWrites(t *testing.T) {
	h := newHarness(t, b2bi, serviceQuotas)
	for _, sql := range []string{
		"CREATE TRIGGER reject_quota AFTER INSERT ON aws_b2bi_profile_quota_requests BEGIN SELECT RAISE(ABORT, 'injected'); END",
		"CREATE TRIGGER reject_quota AFTER INSERT ON aws_b2bi_profile_quota_requests BEGIN UPDATE aws_b2bi_profile_quota_requests SET desired=desired+1 WHERE seq=NEW.seq; END",
	} {
		if _, err := h.db.Exec(sql); err != nil {
			t.Fatal(err)
		}
		r := h.json1("service-quotas", "RequestServiceQuotaIncrease", quotaRequestArgs(6, false))
		expect(t, r, 500, "ServiceException")
		if r.body["RequestedQuota"] != nil {
			t.Fatalf("failed staged request: %d %v", r.status, r.body)
		}
		if _, err := h.db.Exec("DROP TRIGGER reject_quota"); err != nil {
			t.Fatal(err)
		}
		if history := h.json1("service-quotas", "ListRequestedServiceQuotaChangeHistory", map[string]any{}); len(history.body["RequestedQuotas"].([]any)) != 0 {
			t.Fatal(history.body)
		}
	}
	start := make(chan struct{})
	results := make(chan reply, 2)
	for range 2 {
		go func() {
			<-start
			results <- h.json1("service-quotas", "RequestServiceQuotaIncrease", quotaRequestArgs(9, true))
		}()
	}
	close(start)
	seen := map[int]int{}
	for range 2 {
		r := <-results
		seen[r.status]++
		if r.status == 400 {
			expect(t, r, 400, "ResourceAlreadyExistsException")
		}
	}
	if seen[200] != 1 || seen[400] != 1 {
		t.Fatal(seen)
	}
	history := h.json1("service-quotas", "ListRequestedServiceQuotaChangeHistory", map[string]any{})
	if len(history.body["RequestedQuotas"].([]any)) != 1 {
		t.Fatal(history.body)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	raw, _ := json.Marshal(quotaRequestArgs(10, false))
	req := newJSONRequest(h, "service-quotas", "RequestServiceQuotaIncrease", raw, "us-east-1").WithContext(ctx)
	if r := h.do(req); r.status == 200 || r.body["RequestedQuota"] != nil {
		t.Fatal(r.body)
	}
}
