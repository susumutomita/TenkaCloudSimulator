package catalog

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
)

func TestServiceQuotaReadsEnforcedRegionalB2BIProfileLimit(t *testing.T) {
	h := newHarness(t, b2bi, serviceQuotas)
	h.engine.cfg.Regions = append(h.engine.cfg.Regions, "us-west-2", "eu-west-1")
	call := func(model, op, region string, input map[string]any) reply {
		t.Helper()
		wire, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		return h.do(newJSONRequest(h, model, op, wire, region))
	}
	args := map[string]any{"ServiceCode": "b2bi", "QuotaCode": "L-05F6A9EF"}
	read := func(region string) {
		t.Helper()
		r := call("service-quotas", "GetServiceQuota", region, args)
		expect(t, r, 200, "")
		quota := r.body["Quota"].(map[string]any)
		arn := "arn:aws:servicequotas:" + region + ":000000000000:b2bi/L-05F6A9EF"
		if quota["Value"] != json.Number("5") || quota["QuotaArn"] != arn || quota["QuotaAppliedAtLevel"] != "ACCOUNT" || quota["QuotaName"] != "Profiles" || quota["Adjustable"] != true || quota["GlobalQuota"] != false || len(quota) != 8 {
			t.Fatalf("quota: %#v", quota)
		}
	}
	create := func(region, name string) reply {
		return call("b2bi", "CreateProfile", region, map[string]any{"name": name, "businessName": "Fixture", "phone": "+1 555 0100", "logging": "DISABLED"})
	}
	provision := func(region, name string) string {
		t.Helper()
		r := create(region, name)
		expect(t, r, 200, "")
		return r.body["profileId"].(string)
	}
	remove := func(region, id string) {
		t.Helper()
		expect(t, call("b2bi", "DeleteProfile", region, map[string]any{"profileId": id}), 200, "")
	}
	count := func(region string, n int) {
		t.Helper()
		r := call("b2bi", "ListProfiles", region, map[string]any{})
		expect(t, r, 200, "")
		if got := len(r.body["profiles"].([]any)); got != n {
			t.Fatalf("%s profiles = %d, want %d", region, got, n)
		}
	}
	read("us-east-1")
	read("us-west-2")
	read("eu-west-1")
	var east []string
	for i := 0; i < 5; i++ {
		east = append(east, provision("us-east-1", fmt.Sprintf("quota-profile-%d", i)))
	}
	expect(t, create("us-east-1", "sixth"), 400, "ServiceQuotaExceededException")
	count("us-east-1", 5)
	read("us-east-1")
	west := provision("us-west-2", "independent")
	count("us-west-2", 1)
	remove("us-east-1", east[0])
	east[0] = provision("us-east-1", "replacement")
	count("us-east-1", 5)
	expect(t, call("service-quotas", "GetServiceQuota", "ap-northeast-1", args), 400, "NoSuchResourceException")
	for _, input := range []map[string]any{{"ServiceCode": "b2bi", "QuotaCode": "L-00000000"}, {"ServiceCode": "kms", "QuotaCode": "L-05F6A9EF"}} {
		expect(t, call("service-quotas", "GetServiceQuota", "us-east-1", input), 400, "NoSuchResourceException")
	}
	expect(t, call("service-quotas", "GetServiceQuota", "us-east-1", map[string]any{"ServiceCode": "b2bi", "QuotaCode": "L-05F6A9EF", "ContextId": "resource"}), 400, "IllegalArgumentException")
	arn := "arn:aws:servicequotas:us-east-1:000000000000:b2bi/L-05F6A9EF"
	if !slices.Contains(h.policy.calls, "simulator-admin servicequotas:GetServiceQuota "+arn) {
		t.Fatal("read did not authorize the regional quota ARN")
	}
	h.policy.deny["servicequotas:GetServiceQuota "+arn] = true
	expect(t, call("service-quotas", "GetServiceQuota", "us-east-1", args), 400, "AccessDeniedException")
	read("us-west-2")
	expect(t, call("service-quotas", "GetServiceQuotaIncreaseRequestFromTemplate", "us-east-1", map[string]any{"ServiceCode": "b2bi", "QuotaCode": b2biProfilesQuota, "AwsRegion": "us-east-1"}), 501, "UnknownOperationException")
	for _, id := range east {
		remove("us-east-1", id)
	}
	remove("us-west-2", west)
	count("us-east-1", 0)
	count("us-west-2", 0)
}

func TestGetServiceQuotaRequiresActiveB2BIProfileBinding(t *testing.T) {
	h := newHarness(t, serviceQuotas)
	r := h.json1("service-quotas", "GetServiceQuota", map[string]any{"ServiceCode": "b2bi", "QuotaCode": "L-05F6A9EF"})
	expect(t, r, 400, "NoSuchResourceException")
}

func TestGetServiceQuotaUsesEffectiveBoundFromActiveProfileResource(t *testing.T) {
	// A composed engine may select a different maximum or use the standard catalog default.
	for _, limit := range []int{2, 0} {
		service := *b2bi
		service.Resources = slices.Clone(b2bi.Resources)
		for index, resource := range service.Resources {
			if resource.Kind == "profile" {
				profile := *resource
				profile.Limit = limit
				service.Resources[index] = &profile
			}
		}
		h := newHarness(t, &service, serviceQuotas)
		r := h.json1("service-quotas", "GetServiceQuota", map[string]any{"ServiceCode": "b2bi", "QuotaCode": "L-05F6A9EF"})
		expect(t, r, 200, "")
		expected := limit
		if limit == 0 {
			expected = defaultKindLimit
		}
		if got := r.body["Quota"].(map[string]any)["Value"]; got != json.Number(fmt.Sprint(expected)) {
			t.Fatalf("applied quota = %v, want %d", got, expected)
		}
	}
}
