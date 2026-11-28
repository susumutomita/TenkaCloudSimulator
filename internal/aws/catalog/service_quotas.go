package catalog

import (
	"encoding/json"
	"slices"
)

const (
	b2biProfilesQuota   = "L-05F6A9EF"
	b2biProfilesDefault = 5
)

var serviceQuotas = &Service{
	Model: "service-quotas", Title: "Service Quotas", IAMPrefix: "servicequotas", Issues: []int{210},
	Resources: []*Resource{
		{Kind: "quota", Shape: "ServiceQuota", ID: "name", Singleton: true,
			ARN: "arn:{partition}:servicequotas:{region}:{account}:{member:ServiceCode}/{member:QuotaCode}", ARNMember: "QuotaArn", NameMember: "QuotaName"},
	},
	Ops: []Op{
		{Name: "RequestServiceQuotaIncrease", Kind: Create, Resource: "quota", Fixed: map[string]any{"ServiceCode": "b2bi", "QuotaCode": b2biProfilesQuota, "DesiredValue": 8, "SupportCaseAllowed": false}},
		{Name: "GetRequestedServiceQuotaChange", Kind: Read, Resource: "quota", Lookup: "RequestId", Fixed: map[string]any{"RequestId": "REPLACE-WITH-REQUEST-ID"}},
		{Name: "ListRequestedServiceQuotaChangeHistory", Kind: List, Resource: "quota"},
		{Name: "ListRequestedServiceQuotaChangeHistoryByQuota", Kind: List, Resource: "quota", Fixed: map[string]any{"ServiceCode": "b2bi", "QuotaCode": b2biProfilesQuota}},
		{Name: "GetServiceQuota", Kind: Read, Resource: "quota", Fixed: map[string]any{"ServiceCode": "b2bi", "QuotaCode": b2biProfilesQuota}},
		{Name: "GetAWSDefaultServiceQuota", Kind: Read, Resource: "quota", Fixed: map[string]any{"ServiceCode": "b2bi", "QuotaCode": b2biProfilesQuota}},
		{Name: "ListServices", Kind: List, Resource: "quota"},
		{Name: "ListServiceQuotas", Kind: List, Resource: "quota", Fixed: map[string]any{"ServiceCode": "b2bi"}},
		{Name: "ListAWSDefaultServiceQuotas", Kind: List, Resource: "quota", Fixed: map[string]any{"ServiceCode": "b2bi"}},
	},
	Bounds: []string{
		"Discover the B2B Data Interchange Profiles quota (b2bi/L-05F6A9EF) in us-east-1, us-west-2 and eu-west-1. Its AWS default is five. Applied reads use the total capacity enforced by CreateProfile, including slots already occupied.",
		"The three discovery lists return at most one item. ListServiceQuotas supports an exact QuotaCode and ACCOUNT, RESOURCE or ALL. Only account-level capacity is configured. Resource contexts and usage metrics are unsupported.",
		"Adjustable describes the AWS quota. An increase request is stored against current account capacity. SupportCaseAllowed=false records NOT_APPROVED. Omitted or true records PENDING locally, with at most one outstanding request per quota and Region. No support case is opened and the local profile limit is unchanged.",
	},
}

func b2biQuotaRegion(region string) bool {
	return slices.Contains([]string{"us-east-1", "us-west-2", "eu-west-1"}, region)
}

func (q *request) profileQuotaARN() string {
	return "arn:aws:servicequotas:" + q.region + ":" + q.e.cfg.Account + ":b2bi/" + b2biProfilesQuota
}

func unavailableQuota() error {
	return &fault{kind: "notfound", message: "The requested service or quota is unavailable in this Region."}
}

// Service Quotas is JSON, unlike the Query callers that need the shared
// decoder's string-to-integer conversion. Keep its raw number type intact.
func validateQuotaJSON(input map[string]any) error {
	for _, field := range []string{"MaxResults", "DesiredValue"} {
		if value, supplied := input[field]; supplied {
			if _, number := value.(json.Number); !number {
				return invalid("%s must be a JSON number.", field)
			}
		}
	}
	return nil
}

// profileQuota returns the official default or the currently enforced regional capacity.
// The default is a published property and does not depend on a particular engine configuration.
func (q *request) profileQuota(applied bool) map[string]any {
	quota := map[string]any{
		"ServiceCode": "b2bi", "QuotaCode": b2biProfilesQuota, "QuotaName": "Profiles",
		"Value": b2biProfilesDefault, "GlobalQuota": false, "Adjustable": true,
	}
	if applied {
		limit := q.e.b2biProfileLimit()
		if limit == 0 {
			return nil
		}
		quota["Value"] = limit
		quota["QuotaAppliedAtLevel"] = "ACCOUNT"
		quota["QuotaArn"] = q.profileQuotaARN()
	}
	return quota
}

func (q *request) readQuota() (map[string]any, error) {
	applied := q.op.Name == "GetServiceQuota"
	resource := "*"
	if applied {
		resource = q.formatARN(q.resource(), "", "", nil)
	}
	if err := q.authorize(resource); err != nil {
		return nil, err
	}
	if _, ok := q.input["ContextId"]; ok {
		return nil, invalid("The supported quota applies at account level and does not accept a resource context.")
	}
	if !b2biQuotaRegion(q.region) || q.input["ServiceCode"] != "b2bi" || q.input["QuotaCode"] != b2biProfilesQuota {
		return nil, unavailableQuota()
	}
	quota := q.profileQuota(applied)
	if quota == nil {
		return nil, unavailableQuota()
	}
	return map[string]any{"Quota": quota}, nil
}

func (q *request) listQuotas() (map[string]any, error) {
	if err := q.authorize("*"); err != nil {
		return nil, err
	}
	// No supported response has more than one item, so no continuation token can be valid.
	if _, supplied := q.input["NextToken"]; supplied {
		return nil, &fault{kind: "token", code: "InvalidPaginationTokenException", message: "The pagination token is not valid for this list."}
	}
	items := []map[string]any{}
	if q.op.Name == "ListServices" {
		if b2biQuotaRegion(q.region) {
			items = append(items, map[string]any{"ServiceCode": "b2bi", "ServiceName": "AWS B2B Data Interchange"})
		}
		return map[string]any{"Services": items}, nil
	}
	if q.input["ServiceCode"] != "b2bi" || !b2biQuotaRegion(q.region) {
		return nil, unavailableQuota()
	}
	code := q.input["QuotaCode"]
	if (code == nil || code == b2biProfilesQuota) && q.input["QuotaAppliedAtLevel"] != "RESOURCE" {
		if quota := q.profileQuota(q.op.Name == "ListServiceQuotas"); quota != nil {
			items = append(items, quota)
		}
	}
	return map[string]any{"Quotas": items}, nil
}

func (e *Engine) b2biProfileLimit() int {
	b := e.byTarget["B2BI"]
	if b == nil || b.ops["CreateProfile"] == nil {
		return 0
	}
	return b.spec.resource(b.ops["CreateProfile"].Resource).liveLimit()
}
