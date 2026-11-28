package catalog

import "slices"

const b2biProfilesQuota = "L-05F6A9EF"

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
	},
	Bounds: []string{
		"GetServiceQuota reads the enforced B2B Data Interchange Profiles limit (b2bi/L-05F6A9EF) in us-east-1, us-west-2 and eu-west-1. Value is total capacity, including profiles already created.",
		"Adjustable describes the AWS quota. An increase request is stored against current account capacity. SupportCaseAllowed=false records NOT_APPROVED. Omitted or true records PENDING locally, with at most one outstanding request per quota and Region. No support case is opened and the local profile limit is unchanged.",
	},
}

// getServiceQuota projects the live CreateProfile binding without writing a quota row.
func (q *request) getServiceQuota() (map[string]any, error) {
	arn := q.formatARN(q.resource(), "", "", nil)
	if err := q.authorize(arn); err != nil {
		return nil, err
	}
	if _, ok := q.input["ContextId"]; ok {
		return nil, invalid("The supported quota applies at account level and does not accept a resource context.")
	}
	limit := q.e.b2biProfileLimit()
	if q.input["ServiceCode"] != "b2bi" || q.input["QuotaCode"] != b2biProfilesQuota || limit == 0 ||
		!slices.Contains([]string{"us-east-1", "us-west-2", "eu-west-1"}, q.region) {
		return nil, &fault{kind: "notfound", message: "The requested quota is unavailable in this Region."}
	}
	return map[string]any{"Quota": map[string]any{
		"ServiceCode": "b2bi", "QuotaCode": b2biProfilesQuota, "QuotaName": "Profiles", "QuotaArn": arn,
		"Value": limit, "GlobalQuota": false, "Adjustable": true, "QuotaAppliedAtLevel": "ACCOUNT",
	}}, nil
}

func (e *Engine) b2biProfileLimit() int {
	b := e.byTarget["B2BI"]
	if b == nil || b.ops["CreateProfile"] == nil {
		return 0
	}
	return b.spec.resource(b.ops["CreateProfile"].Resource).liveLimit()
}
