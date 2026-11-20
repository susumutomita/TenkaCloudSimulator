package server

import (
	"strings"
	"testing"
)

// Composed routing: an rds-signed Query form POST reaches the catalog, while
// form POSTs signed for native Query services keep their native handlers.
func TestCatalogQueryRoutingLeavesNativeQueryServicesAlone(t *testing.T) {
	s := instance(t)
	signed := func(service string) map[string]string {
		return map[string]string{"Content-Type": "application/x-www-form-urlencoded",
			"Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMROUTE/20261120/us-east-1/" + service + "/aws4_request, SignedHeaders=host, Signature=0"}
	}
	const namespace = `xmlns="http://rds.amazonaws.com/doc/2014-10-31/"`
	body := string(status(t, s, "POST", "/", "Action=DescribeDBSubnetGroups&Version=2014-10-31", 200, signed("rds")))
	if !strings.Contains(body, "<DescribeDBSubnetGroupsResponse "+namespace+">") || !strings.Contains(body, "<DescribeDBSubnetGroupsResult>") {
		t.Fatalf("rds Query response %s", body)
	}
	for service, form := range map[string]string{
		"iam":                  "Action=ListRoles&Version=2010-05-08",
		"sts":                  "Action=GetCallerIdentity&Version=2011-06-15",
		"sns":                  "Action=ListTopics&Version=2010-03-31",
		"elasticloadbalancing": "Action=DescribeLoadBalancers&Version=2015-12-01",
		"cloudformation":       "Action=DescribeStacks&Version=2010-05-15",
		"ec2":                  "Action=DescribeVpcs&Version=2016-11-15",
		"":                     "Action=DescribeStacks&Version=2010-05-15",
	} {
		headers := signed(service)
		if service == "" {
			delete(headers, "Authorization")
		}
		resp, data := call(t, s, "POST", "/", form, headers)
		if strings.Contains(string(data), namespace) || strings.Contains(string(data), "UnknownOperationException") {
			t.Fatalf("%q Query request reached the RDS catalog: %d %s", service, resp.StatusCode, data)
		}
	}
}
