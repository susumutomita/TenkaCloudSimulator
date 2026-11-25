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

// Contradictory selectors are pinned: the rds scope decides for handlers
// consulted after the catalog (IAM), RDS refuses other methods, and
// handlers consulted before the catalog (SNS by its hint) keep the request.
// No combination grants authority beyond the selected service's own checks.
func TestCatalogQueryContradictorySelectorsArePinned(t *testing.T) {
	s := instance(t)
	const namespace = `xmlns="http://rds.amazonaws.com/doc/2014-10-31/"`
	headers := func(extra map[string]string) map[string]string {
		out := map[string]string{"Content-Type": "application/x-www-form-urlencoded",
			"Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMROUTE/20261120/us-east-1/rds/aws4_request, SignedHeaders=host, Signature=0"}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	body := string(status(t, s, "POST", "/", "Action=ListRoles&Version=2010-05-08", 400, headers(map[string]string{"X-Simulator-Service": "iam"})))
	if !strings.Contains(body, namespace) || !strings.Contains(body, "<Code>InvalidAction</Code>") {
		t.Fatalf("rds scope with an iam hint: %s", body)
	}
	for _, method := range []string{"GET", "PUT"} {
		body := string(status(t, s, method, "/", "Action=DescribeDBSubnetGroups&Version=2014-10-31", 400, headers(nil)))
		if !strings.Contains(body, namespace) || !strings.Contains(body, "<Code>MalformedQueryString</Code>") {
			t.Fatalf("rds-signed %s: %s", method, body)
		}
	}
	if _, data := call(t, s, "POST", "/", "Action=ListTopics&Version=2010-03-31", headers(map[string]string{"X-Simulator-Service": "sns"})); strings.Contains(string(data), namespace) {
		t.Fatalf("an sns hint is served by SNS before the catalog: %s", data)
	}
}

// The Auto Scaling Query catalog (signing name autoscaling) and the native
// Auto Scaling Plans handler (autoscaling-plans) are told apart by the
// credential scope or the service hint, in both directions.
func TestCatalogAutoScalingAndNativeAutoScalingPlansRouteApart(t *testing.T) {
	s := instance(t)
	scope := func(service string) string {
		return "AWS4-HMAC-SHA256 Credential=TCSIM/20261120/us-east-1/" + service + "/aws4_request, SignedHeaders=host, Signature=0"
	}
	const namespace = `xmlns="http://autoscaling.amazonaws.com/doc/2011-01-01/"`
	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Authorization": scope("autoscaling")}
	created := string(status(t, s, "POST", "/", "Action=CreateLaunchConfiguration&Version=2011-01-01&LaunchConfigurationName=route&ImageId=ami-0123456789abcdef0&InstanceType=t3.micro", 200, form))
	if !strings.Contains(created, "<CreateLaunchConfigurationResponse "+namespace+">") {
		t.Fatalf("autoscaling Query response %s", created)
	}
	listed := string(status(t, s, "POST", "/", "Action=DescribeLaunchConfigurations&Version=2011-01-01", 200, form))
	if !strings.Contains(listed, "<LaunchConfigurationName>route</LaunchConfigurationName>") {
		t.Fatalf("autoscaling state %s", listed)
	}
	// The hint selects the catalog for an unsigned first-party request.
	hinted := string(status(t, s, "POST", "/", "Action=DescribeLaunchConfigurations&Version=2011-01-01", 200,
		map[string]string{"Content-Type": "application/x-www-form-urlencoded", "X-Simulator-Service": "autoscaling"}))
	if !strings.Contains(hinted, "<LaunchConfigurationName>route</LaunchConfigurationName>") {
		t.Fatalf("autoscaling hint %s", hinted)
	}

	// An autoscaling-plans request, signed or hinted, still reaches the
	// native JSON handler and never sees the launch configuration.
	plans := string(status(t, s, "POST", "/", `{}`, 200, map[string]string{"Content-Type": "application/x-amz-json-1.1",
		"X-Amz-Target": "AnyScaleScalingPlannerFrontendService.DescribeScalingPlans", "Authorization": scope("autoscaling-plans")}))
	if !strings.Contains(plans, `"ScalingPlans"`) || strings.Contains(plans, "route") || strings.Contains(plans, namespace) {
		t.Fatalf("autoscaling-plans response %s", plans)
	}
	hintedPlans := string(status(t, s, "POST", "/", `{}`, 200, map[string]string{"Content-Type": "application/x-amz-json-1.1",
		"X-Amz-Target": "AnyScaleScalingPlannerFrontendService.DescribeScalingPlans", "X-Simulator-Service": "autoscaling-plans"}))
	if !strings.Contains(hintedPlans, `"ScalingPlans"`) {
		t.Fatalf("autoscaling-plans hint %s", hintedPlans)
	}
	// A form POST under the plans scope is the native handler's, not the catalog's.
	_, refused := call(t, s, "POST", "/", "Action=DescribeLaunchConfigurations&Version=2011-01-01",
		map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Authorization": scope("autoscaling-plans")})
	if strings.Contains(string(refused), namespace) || strings.Contains(string(refused), "route") {
		t.Fatalf("an autoscaling-plans form request reached the Auto Scaling catalog: %s", refused)
	}
	// A plans target under the autoscaling scope is still the native handler's:
	// it refuses the scope itself instead of the catalog serving it.
	crossed := string(status(t, s, "POST", "/", `{}`, 403, map[string]string{"Content-Type": "application/x-amz-json-1.1",
		"X-Amz-Target": "AnyScaleScalingPlannerFrontendService.DescribeScalingPlans", "Authorization": scope("autoscaling")}))
	if !strings.Contains(crossed, "AccessDeniedException") || !strings.Contains(crossed, "autoscaling-plans") || strings.Contains(crossed, namespace) {
		t.Fatalf("a plans target under the autoscaling scope %s", crossed)
	}
}
