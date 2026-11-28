package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func quotaDiscoveryCall(t *testing.T, h *harness, operation, region string, input map[string]any) reply {
	t.Helper()
	b, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return h.do(newJSONRequest(h, "service-quotas", operation, b, region))
}

func quotaDiscoveryInput(operation string) map[string]any {
	in := map[string]any{}
	if operation != "ListServices" {
		in["ServiceCode"] = "b2bi"
	}
	if operation == "GetServiceQuota" || operation == "GetAWSDefaultServiceQuota" {
		in["QuotaCode"] = b2biProfilesQuota
	}
	return in
}

func TestQuotaDiscoveryDefaultAndAppliedProjections(t *testing.T) {
	for _, limit := range []int{2, 0, -1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			service := *b2bi
			service.Resources = slices.Clone(b2bi.Resources)
			profile := *service.Resources[0]
			profile.Limit = limit
			service.Resources[0] = &profile
			services := []*Service{&service, serviceQuotas}
			if limit == -1 {
				services = []*Service{serviceQuotas}
			}
			h := newHarness(t, services...)
			h.engine.cfg.Regions = []string{"us-east-1", "us-west-2", "eu-west-1", "ap-northeast-1"}
			before := groupCatalogFrame(t, h.db)
			for _, region := range []string{"us-east-1", "us-west-2", "eu-west-1"} {
				for _, operation := range []string{"GetAWSDefaultServiceQuota", "ListAWSDefaultServiceQuotas", "GetServiceQuota", "ListServiceQuotas"} {
					r := quotaDiscoveryCall(t, h, operation, region, quotaDiscoveryInput(operation))
					applied := operation == "GetServiceQuota" || operation == "ListServiceQuotas"
					if limit == -1 && operation == "GetServiceQuota" {
						expect(t, r, 400, "NoSuchResourceException")
						continue
					}
					expect(t, r, 200, "")
					if _, present := r.body["NextToken"]; present {
						t.Fatal("single quota invented a continuation", r.body)
					}
					quota, single := r.body["Quota"].(map[string]any)
					if !single {
						items, ok := r.body["Quotas"].([]any)
						if !ok || items == nil {
							t.Fatal("expected an actual array", r.body)
						}
						if applied && limit == -1 {
							if len(items) != 0 {
								t.Fatal("absent live binding invented applied quota", items)
							}
							continue
						}
						if len(items) != 1 {
							t.Fatal(items)
						}
						quota = items[0].(map[string]any)
					}
					value, fields := b2biProfilesDefault, 6
					if applied {
						value, fields = limit, 8
						if limit == 0 {
							value = defaultKindLimit
						}
						if quota["QuotaArn"] != "arn:aws:servicequotas:"+region+":000000000000:b2bi/"+b2biProfilesQuota || quota["QuotaAppliedAtLevel"] != "ACCOUNT" {
							t.Fatal(quota)
						}
					}
					if len(quota) != fields || quota["Value"] != json.Number(fmt.Sprint(value)) || quota["QuotaName"] != "Profiles" || quota["ServiceCode"] != "b2bi" || quota["QuotaCode"] != b2biProfilesQuota || quota["Adjustable"] != true || quota["GlobalQuota"] != false {
						t.Fatal("wrong or speculative quota projection", quota)
					}
				}
			}
			if !reflect.DeepEqual(before, groupCatalogFrame(t, h.db)) {
				t.Fatal("read projection changed persisted resources or sequences")
			}
		})
	}
}

func TestQuotaDiscoveryNativeInputsAndEmptyRegions(t *testing.T) {
	h := newHarness(t, b2bi, serviceQuotas)
	before := groupCatalogFrame(t, h.db)
	for _, operation := range []string{"ListServices", "ListAWSDefaultServiceQuotas", "ListServiceQuotas"} {
		for _, max := range []any{nil, 1, 100, 0, 101, "1", 1.5, true, false} {
			in := quotaDiscoveryInput(operation)
			if max != nil {
				in["MaxResults"] = max
			}
			r := quotaDiscoveryCall(t, h, operation, "us-east-1", in)
			if max == nil || max == 1 || max == 100 {
				expect(t, r, 200, "")
			} else {
				expect(t, r, 400, "IllegalArgumentException")
			}
		}
		in := quotaDiscoveryInput(operation)
		in["NextToken"] = "QQ=="
		expect(t, quotaDiscoveryCall(t, h, operation, "us-east-1", in), 400, "InvalidPaginationTokenException")
		in = quotaDiscoveryInput(operation)
		in["MaxResults"] = nil
		expect(t, quotaDiscoveryCall(t, h, operation, "us-east-1", in), 400, "IllegalArgumentException")
	}
	for _, level := range []any{nil, "ACCOUNT", "ALL", "RESOURCE", "other", 1} {
		in := quotaDiscoveryInput("ListServiceQuotas")
		if level != nil {
			in["QuotaAppliedAtLevel"] = level
		}
		r := quotaDiscoveryCall(t, h, "ListServiceQuotas", "us-east-1", in)
		if level == "other" || level == 1 {
			expect(t, r, 400, "IllegalArgumentException")
			continue
		}
		expect(t, r, 200, "")
		want := 1
		if level == "RESOURCE" {
			want = 0
		}
		if got, ok := r.body["Quotas"].([]any); !ok || len(got) != want {
			t.Fatal(r.body)
		}
	}
	r := quotaDiscoveryCall(t, h, "ListServices", "ap-northeast-1", map[string]any{})
	expect(t, r, 200, "")
	if rows, ok := r.body["Services"].([]any); !ok || rows == nil || len(rows) != 0 || len(r.body) != 1 {
		t.Fatal("Tokyo discovery must be an empty array", r.body)
	}
	for _, op := range []string{"ListAWSDefaultServiceQuotas", "ListServiceQuotas", "GetAWSDefaultServiceQuota"} {
		expect(t, quotaDiscoveryCall(t, h, op, "ap-northeast-1", quotaDiscoveryInput(op)), 400, "NoSuchResourceException")
		in := quotaDiscoveryInput(op)
		in["ServiceCode"] = "kms"
		expect(t, quotaDiscoveryCall(t, h, op, "us-east-1", in), 400, "NoSuchResourceException")
	}
	for _, op := range []string{"GetAWSDefaultServiceQuota", "ListAWSDefaultServiceQuotas"} {
		in := quotaDiscoveryInput(op)
		in["QuotaAppliedAtLevel"] = "ACCOUNT"
		expect(t, quotaDiscoveryCall(t, h, op, "us-east-1", in), 400, "IllegalArgumentException")
	}
	input := quotaDiscoveryInput("GetAWSDefaultServiceQuota")
	input["ContextId"] = "unsupported-context"
	expect(t, quotaDiscoveryCall(t, h, "GetAWSDefaultServiceQuota", "us-east-1", input), 400, "IllegalArgumentException")
	input = quotaDiscoveryInput("ListAWSDefaultServiceQuotas")
	input["QuotaCode"] = b2biProfilesQuota
	expect(t, quotaDiscoveryCall(t, h, "ListAWSDefaultServiceQuotas", "us-east-1", input), 400, "IllegalArgumentException")
	input = quotaDiscoveryInput("ListServiceQuotas")
	input["QuotaCode"] = "L-00000000"
	r = quotaDiscoveryCall(t, h, "ListServiceQuotas", "us-east-1", input)
	expect(t, r, 200, "")
	if rows, ok := r.body["Quotas"].([]any); !ok || rows == nil || len(rows) != 0 {
		t.Fatal("unknown exact quota filter must be empty", r.body)
	}
	if !reflect.DeepEqual(before, groupCatalogFrame(t, h.db)) {
		t.Fatal("validation or refused reads changed persisted state")
	}
}

func TestQuotaDiscoveryUsesWildcardAndPreservesNamedQuotaIAM(t *testing.T) {
	h := newHarness(t, b2bi, serviceQuotas)
	expect(t, h.json1("b2bi", "CreateProfile", map[string]any{"name": "retained", "businessName": "Fixture", "phone": "+1 555 0100", "logging": "DISABLED"}), 200, "")
	peer := newHarness(t, b2bi, serviceQuotas)
	expect(t, peer.json1("b2bi", "CreateProfile", map[string]any{"name": "retained", "businessName": "Peer", "phone": "+1 555 0100", "logging": "DISABLED"}), 200, "")
	peerBefore := groupCatalogFrame(t, peer.db)
	arn := "arn:aws:servicequotas:us-east-1:000000000000:b2bi/" + b2biProfilesQuota
	before := groupCatalogFrame(t, h.db)
	for _, op := range []string{"ListServices", "ListAWSDefaultServiceQuotas", "ListServiceQuotas", "GetAWSDefaultServiceQuota", "GetServiceQuota"} {
		for _, resource := range []string{arn, "*"} {
			for _, deny := range []bool{false, true} {
				action := "servicequotas:" + op
				statements := []map[string]any{{"Effect": "Allow", "Action": action, "Resource": resource}}
				if deny {
					statements = append(statements, map[string]any{"Effect": "Deny", "Action": action, "Resource": "*"})
				}
				b, _ := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
				p, err := core.ParsePolicy(b)
				if err != nil {
					t.Fatal(err)
				}
				h.engine.cfg.Authorize = func(_ context.Context, _, a, r string) (string, error) { return core.Evaluate(p, a, r), nil }
				r := quotaDiscoveryCall(t, h, op, "us-east-1", quotaDiscoveryInput(op))
				if deny || resource != "*" && op != "GetServiceQuota" {
					expect(t, r, 400, "AccessDeniedException")
					for _, key := range []string{"Services", "Quotas", "Quota", "NextToken"} {
						if _, ok := r.body[key]; ok {
							t.Fatal("denied read disclosed inventory", r.body)
						}
					}
					if op != "ListServices" {
						unknown := quotaDiscoveryInput(op)
						unknown["ServiceCode"] = "kms"
						expect(t, quotaDiscoveryCall(t, h, op, "us-east-1", unknown), 400, "AccessDeniedException")
					}
				} else {
					expect(t, r, 200, "")
				}
			}
		}
		policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"unrelated:Get","Resource":"*"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		h.engine.cfg.Authorize = func(_ context.Context, _, a, r string) (string, error) {
			return core.Evaluate(policy, a, r), nil
		}
		expect(t, quotaDiscoveryCall(t, h, op, "us-east-1", quotaDiscoveryInput(op)), 400, "AccessDeniedException")
	}
	if !reflect.DeepEqual(before, groupCatalogFrame(t, h.db)) {
		t.Fatal("IAM refusal changed database")
	}
	if !reflect.DeepEqual(peerBefore, groupCatalogFrame(t, peer.db)) {
		t.Fatal("read or IAM refusal changed the separate team")
	}
}

func TestQuotaDiscoveryReopenPreservesPopulatedScopes(t *testing.T) {
	h := newHarness(t, b2bi, serviceQuotas)
	var ids []string
	for i := 0; i < b2biProfilesDefault; i++ {
		r := h.json1("b2bi", "CreateProfile", map[string]any{"name": fmt.Sprintf("saved-%d", i), "businessName": "Fixture", "phone": "+1 555 0100", "logging": "DISABLED"})
		expect(t, r, 200, "")
		ids = append(ids, r.body["profileId"].(string))
	}
	peer := newHarness(t, b2bi, serviceQuotas)
	expect(t, peer.json1("b2bi", "CreateProfile", map[string]any{"name": "independent", "businessName": "Peer", "phone": "+1 555 0100", "logging": "DISABLED"}), 200, "")
	before, peerBefore := groupCatalogFrame(t, h.db), groupCatalogFrame(t, peer.db)
	var sequence int
	var name, path string
	if err := h.db.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &path); err != nil || path == "" {
		t.Fatalf("file-backed state path: %q, %v", path, err)
	}
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h.db = store.Database()
	h.engine, err = New(h.db, h.engine.cfg, []*Service{b2bi, serviceQuotas})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"ListServices", "ListServiceQuotas", "ListAWSDefaultServiceQuotas", "GetAWSDefaultServiceQuota", "GetServiceQuota"} {
		r := quotaDiscoveryCall(t, h, op, "us-east-1", quotaDiscoveryInput(op))
		expect(t, r, 200, "")
		if op == "GetServiceQuota" && r.body["Quota"].(map[string]any)["Value"] != json.Number("5") {
			t.Fatal("full occupancy changed applied capacity", r.body)
		}
	}
	if !reflect.DeepEqual(before, groupCatalogFrame(t, h.db)) || !reflect.DeepEqual(peerBefore, groupCatalogFrame(t, peer.db)) {
		t.Fatal("reopen or discovery changed retained team state")
	}
	for _, id := range ids {
		expect(t, h.json1("b2bi", "DeleteProfile", map[string]any{"profileId": id}), 200, "")
	}
	if !reflect.DeepEqual(peerBefore, groupCatalogFrame(t, peer.db)) {
		t.Fatal("cleanup changed peer resources")
	}
}
