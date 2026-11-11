package aws

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const elbDataPrefix = "/_elb/"

func elbSigningService(authorization string) string {
	_, scope, ok := strings.Cut(authorization, "Credential=")
	if !ok {
		return ""
	}
	credential, _, _ := strings.Cut(scope, ",")
	parts := strings.Split(credential, "/")
	if len(parts) < 4 {
		return ""
	}
	return parts[3]
}

// isELB routes Elastic Load Balancing v2 Query requests by explicit service
// selection or SigV4 scope, and the local load balancer data plane by prefix.
func isELB(r *http.Request) bool {
	if service := r.Header.Get("X-Simulator-Service"); service != "" {
		return service == "elasticloadbalancing"
	}
	return strings.HasPrefix(r.URL.Path, elbDataPrefix) || elbSigningService(r.Header.Get("Authorization")) == "elasticloadbalancing"
}

func elbRegion(r *http.Request) (string, error) {
	region := r.Header.Get("X-Simulator-Region")
	if r.Header.Get("X-Amz-Security-Token") != "" {
		return "", elbInvalid("AWS session tokens are unsupported.")
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		_, scope, ok := strings.Cut(auth, "Credential=")
		credential, _, _ := strings.Cut(scope, ",")
		parts := strings.Split(credential, "/")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || !ok || len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[1] == "" || parts[3] != "elasticloadbalancing" || parts[4] != "aws4_request" {
			return "", elbDenied("Use unsigned requests or dummy TCSIM Elastic Load Balancing credentials.")
		}
		if region != "" && region != parts[2] {
			return "", elbInvalid("Region header and credential scope disagree.")
		}
		region = parts[2]
	}
	if region == "" {
		region = "us-east-1"
	}
	if !slices.Contains(Regions, region) {
		return "", elbInvalid("Unsupported simulator region.")
	}
	return region, nil
}

func (g *Gateway) serveELB(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{"Authorization", "X-Simulator-Service", "X-Simulator-Region", "X-Simulator-Principal", "X-Amz-Target", "X-Amz-Security-Token"} {
		if len(r.Header.Values(name)) > 1 {
			elbError(w, elbInvalid("Duplicate routing or authority headers are unsupported."))
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, elbDataPrefix) {
		g.serveELBTraffic(w, r)
		return
	}
	if r.Header.Get("X-Amz-Target") != "" {
		elbError(w, elbInvalid("Elastic Load Balancing uses Query/XML, not JSON target dispatch."))
		return
	}
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		elbError(w, elbInvalid("Use a form-encoded POST to the regional root endpoint without query parameters."))
		return
	}
	region, err := elbRegion(r)
	if err != nil {
		elbError(w, err)
		return
	}
	if g.ELB == nil {
		elbError(w, elbFailure{"InternalFailure", "Elastic Load Balancing state is not configured.", 500})
		return
	}
	if g.Identities == nil {
		elbError(w, elbDenied("Elastic Load Balancing identity authority is unavailable."))
		return
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	var policy core.Policy
	if principal != core.Administrator {
		var found bool
		policy, found, err = g.Identities.PolicyContext(r.Context(), principal)
		if err != nil {
			elbError(w, err)
			return
		}
		if !found {
			elbError(w, elbDenied("Elastic Load Balancing identity is unavailable."))
			return
		}
	}
	// One immutable caller policy is captured before parsing; every callback
	// evaluates it, including authorization performed inside store transactions.
	authorize := func(action, resource string) error {
		decision := "allowed"
		if principal != core.Administrator {
			decision = core.Evaluate(policy, action, resource)
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			return elbDenied("User is not authorized to perform " + action + " on " + elbQuote(resource) + ". IAM evaluation: " + decision)
		}
		return nil
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			elbError(w, elbFailure{"RequestEntityTooLarge", "Elastic Load Balancing Query requests are limited to 64 KiB.", 413})
		} else {
			elbError(w, elbInvalid("Could not read the Query body."))
		}
		return
	}
	values, err := url.ParseQuery(string(data))
	if err != nil {
		elbError(w, elbInvalid("Malformed Query body."))
		return
	}
	for _, v := range values {
		if len(v) != 1 {
			elbError(w, elbInvalid("Duplicate Query fields are unsupported."))
			return
		}
	}
	form := &elbForm{values: values, used: map[string]bool{}}
	version, _ := form.get("Version")
	action, _ := form.get("Action")
	if version == "2012-06-01" {
		elbError(w, elbErr("InvalidAction", "The Classic Load Balancer API (2012-06-01) is not simulated; use Elastic Load Balancing v2."))
		return
	}
	if version != "2015-12-01" {
		elbError(w, elbInvalid("Use Elastic Load Balancing Version 2015-12-01."))
		return
	}
	operation, ok := elbOperations[action]
	if !ok {
		elbError(w, elbErr("InvalidAction", "This Elastic Load Balancing operation is not implemented: "+elbQuote(action)))
		return
	}
	result, err := operation(g.ELB, r, form, region, authorize)
	if err == nil {
		err = form.finish()
	}
	if err != nil {
		elbError(w, err)
		return
	}
	elbResponse(w, action, result)
}

type elbOperation func(s *ELBStore, r *http.Request, f *elbForm, region string, authorize func(string, string) error) (any, error)

// ELBOperations is the implemented ELBv2 subset; all other official
// operations return InvalidAction rather than an approximate success.
var ELBOperations = []string{"CreateLoadBalancer", "DescribeLoadBalancers", "DeleteLoadBalancer", "CreateTargetGroup", "DescribeTargetGroups", "ModifyTargetGroup", "DeleteTargetGroup", "RegisterTargets", "DeregisterTargets", "DescribeTargetHealth", "CreateListener", "DescribeListeners", "DeleteListener"}

var elbOperations = map[string]elbOperation{
	"CreateLoadBalancer":    elbCreateLoadBalancer,
	"DescribeLoadBalancers": elbDescribeLoadBalancers,
	"DeleteLoadBalancer":    elbDeleteLoadBalancer,
	"CreateTargetGroup":     elbCreateTargetGroup,
	"DescribeTargetGroups":  elbDescribeTargetGroups,
	"ModifyTargetGroup":     elbModifyTargetGroup,
	"DeleteTargetGroup":     elbDeleteTargetGroup,
	"RegisterTargets":       elbRegisterTargets,
	"DeregisterTargets":     elbDeregisterTargets,
	"DescribeTargetHealth":  elbDescribeTargetHealth,
	"CreateListener":        elbCreateListener,
	"DescribeListeners":     elbDescribeListeners,
	"DeleteListener":        elbDeleteListener,
}

// elbFinishFirst validates field consumption before a mutation reaches state.
func elbFinishFirst(f *elbForm) error { return f.finish() }

func elbCreateLoadBalancer(s *ELBStore, r *http.Request, f *elbForm, region string, authorize func(string, string) error) (any, error) {
	lb := ELBLoadBalancer{Region: region, Scheme: "internet-facing"}
	lb.Name, _ = f.get("Name")
	if err := elbValidName(lb.Name, true); err != nil {
		return nil, err
	}
	if kind, ok := f.get("Type"); ok && kind != "application" {
		return nil, elbConfig("Only application load balancers are simulated.")
	}
	if scheme, ok := f.get("Scheme"); ok {
		if scheme != "internet-facing" && scheme != "internal" {
			return nil, elbErr("InvalidScheme", "Scheme must be internet-facing or internal.")
		}
		lb.Scheme = scheme
	}
	if address, ok := f.get("IpAddressType"); ok && address != "ipv4" {
		return nil, elbConfig("Only the ipv4 address type is simulated.")
	}
	var err error
	if lb.Subnets, err = f.strings("Subnets", 6); err != nil {
		return nil, err
	}
	if len(lb.Subnets) < 2 {
		return nil, elbConfig("An application load balancer needs at least two subnets.")
	}
	for i, subnet := range lb.Subnets {
		if !elbSubnetPattern.MatchString(subnet) || slices.Contains(lb.Subnets[:i], subnet) {
			return nil, elbInvalid("Subnets must be distinct subnet-xxxxxxxx IDs.")
		}
	}
	if lb.SecurityGroups, err = f.strings("SecurityGroups", 5); err != nil {
		return nil, err
	}
	for i, group := range lb.SecurityGroups {
		if !elbGroupPattern.MatchString(group) || slices.Contains(lb.SecurityGroups[:i], group) {
			return nil, elbInvalid("SecurityGroups must be distinct sg-xxxxxxxx IDs.")
		}
	}
	if err = elbFinishFirst(f); err != nil {
		return nil, err
	}
	created, err := s.CreateLoadBalancer(r.Context(), lb, authorize)
	if err != nil {
		return nil, err
	}
	return struct {
		LoadBalancers []elbLBWire `xml:"LoadBalancers>member"`
	}{[]elbLBWire{elbLBOut(created)}}, nil
}

func elbPaging(f *elbForm) (string, int, error) {
	marker, _ := f.get("Marker")
	size, ok, err := f.integer("PageSize", 1, 400)
	if !ok {
		size = 400
	}
	return marker, size, err
}
func elbScope(action, region string, parts ...[]string) string {
	scope := action + "\x00" + region
	for _, part := range parts {
		scope += "\x00" + strings.Join(part, "\x01")
	}
	return scope
}

func elbDescribeLoadBalancers(s *ELBStore, r *http.Request, f *elbForm, region string, authorize func(string, string) error) (any, error) {
	arns, err := f.strings("LoadBalancerArns", 20)
	if err != nil {
		return nil, err
	}
	names, err := f.strings("Names", 20)
	if err != nil {
		return nil, err
	}
	if len(arns) > 0 && len(names) > 0 {
		return nil, elbInvalid("Specify LoadBalancerArns or Names, not both.")
	}
	marker, size, err := elbPaging(f)
	if err != nil {
		return nil, err
	}
	if err = authorize("elasticloadbalancing:DescribeLoadBalancers", "*"); err != nil {
		return nil, err
	}
	items, err := s.DescribeLoadBalancers(r.Context(), region, arns, names)
	if err != nil {
		return nil, err
	}
	page, next, err := elbPage(items, marker, elbScope("DescribeLoadBalancers", region, arns, names), size, func(lb ELBLoadBalancer) string { return lb.Name })
	if err != nil {
		return nil, err
	}
	out := make([]elbLBWire, 0, len(page))
	for _, lb := range page {
		out = append(out, elbLBOut(lb))
	}
	return struct {
		LoadBalancers []elbLBWire `xml:"LoadBalancers>member"`
		NextMarker    string      `xml:",omitempty"`
	}{out, next}, nil
}

func elbDeleteLoadBalancer(s *ELBStore, r *http.Request, f *elbForm, region string, authorize func(string, string) error) (any, error) {
	arn, _ := f.get("LoadBalancerArn")
	if err := elbFinishFirst(f); err != nil {
		return nil, err
	}
	return nil, s.DeleteLoadBalancer(r.Context(), region, arn, authorize)
}

func elbRefuseInstanceFields(f *elbForm) error {
	for _, field := range []string{"Protocol", "Port", "VpcId", "ProtocolVersion", "HealthCheckProtocol", "HealthCheckPort", "IpAddressType", "TargetControlPort"} {
		if _, ok := f.values[field]; ok {
			return elbConfig("Lambda target groups do not accept " + field + ".")
		}
	}
	return nil
}

func elbCreateTargetGroup(s *ELBStore, r *http.Request, f *elbForm, region string, authorize func(string, string) error) (any, error) {
	tg := ELBTargetGroup{Region: region}
	tg.Name, _ = f.get("Name")
	if err := elbValidName(tg.Name, false); err != nil {
		return nil, err
	}
	if kind, _ := f.get("TargetType"); kind != "lambda" {
		return nil, elbConfig("Only TargetType lambda is simulated; instance, ip and alb targets need EC2/VPC networking that is not available.")
	}
	if err := elbRefuseInstanceFields(f); err != nil {
		return nil, err
	}
	var err error
	if tg.Health, err = elbParseHealth(f, elbDefaultHealth()); err != nil {
		return nil, err
	}
	if err = elbFinishFirst(f); err != nil {
		return nil, err
	}
	created, err := s.CreateTargetGroup(r.Context(), tg, authorize)
	if err != nil {
		return nil, err
	}
	return struct {
		TargetGroups []elbTGWire `xml:"TargetGroups>member"`
	}{[]elbTGWire{elbTGOut(created)}}, nil
}

func elbDescribeTargetGroups(s *ELBStore, r *http.Request, f *elbForm, region string, authorize func(string, string) error) (any, error) {
	loadBalancer, _ := f.get("LoadBalancerArn")
	arns, err := f.strings("TargetGroupArns", 20)
	if err != nil {
		return nil, err
	}
	names, err := f.strings("Names", 20)
	if err != nil {
		return nil, err
	}
	filters := 0
	for _, used := range []bool{loadBalancer != "", len(arns) > 0, len(names) > 0} {
		if used {
			filters++
		}
	}
	if filters > 1 {
		return nil, elbInvalid("Specify at most one of LoadBalancerArn, TargetGroupArns or Names.")
	}
	marker, size, err := elbPaging(f)
	if err != nil {
		return nil, err
	}
	if err = authorize("elasticloadbalancing:DescribeTargetGroups", "*"); err != nil {
		return nil, err
	}
	items, err := s.DescribeTargetGroups(r.Context(), region, loadBalancer, arns, names)
	if err != nil {
		return nil, err
	}
	page, next, err := elbPage(items, marker, elbScope("DescribeTargetGroups", region, []string{loadBalancer}, arns, names), size, func(tg ELBTargetGroup) string { return tg.Name })
	if err != nil {
		return nil, err
	}
	out := make([]elbTGWire, 0, len(page))
	for _, tg := range page {
		out = append(out, elbTGOut(tg))
	}
	return struct {
		TargetGroups []elbTGWire `xml:"TargetGroups>member"`
		NextMarker   string      `xml:",omitempty"`
	}{out, next}, nil
}

func elbModifyTargetGroup(s *ELBStore, r *http.Request, f *elbForm, region string, authorize func(string, string) error) (any, error) {
	arn, _ := f.get("TargetGroupArn")
	for _, field := range []string{"HealthCheckProtocol", "HealthCheckPort"} {
		if _, ok := f.values[field]; ok {
			return nil, elbConfig("Lambda target groups do not accept " + field + ".")
		}
	}
	// Parse once against the stored configuration inside the store transaction.
	snapshot := map[string]bool{}
	for key := range f.values {
		snapshot[key] = true
	}
	for _, key := range []string{"HealthCheckEnabled", "HealthCheckPath", "HealthCheckIntervalSeconds", "HealthCheckTimeoutSeconds", "HealthyThresholdCount", "UnhealthyThresholdCount", "Matcher.HttpCode"} {
		if snapshot[key] {
			f.used[key] = true
		}
	}
	if err := elbFinishFirst(f); err != nil {
		return nil, err
	}
	modified, err := s.ModifyTargetGroup(r.Context(), region, arn, func(current ELBHealthCheck) (ELBHealthCheck, error) {
		parse := &elbForm{values: f.values, used: map[string]bool{}}
		return elbParseHealth(parse, current)
	}, authorize)
	if err != nil {
		return nil, err
	}
	return struct {
		TargetGroups []elbTGWire `xml:"TargetGroups>member"`
	}{[]elbTGWire{elbTGOut(modified)}}, nil
}

func elbDeleteTargetGroup(s *ELBStore, r *http.Request, f *elbForm, region string, authorize func(string, string) error) (any, error) {
	arn, _ := f.get("TargetGroupArn")
	if err := elbFinishFirst(f); err != nil {
		return nil, err
	}
	return nil, s.DeleteTargetGroup(r.Context(), region, arn, authorize)
}

// elbTargets reads target descriptions. Lambda targets accept only an Id and
// the optional AvailabilityZone value all; ports are refused.
func elbTargets(f *elbForm, limit int) ([]string, error) {
	n, err := f.count("Targets", limit)
	if err != nil {
		return nil, err
	}
	var result []string
	for i := 1; i <= n; i++ {
		p := "Targets.member." + strconv.Itoa(i) + "."
		id, _ := f.get(p + "Id")
		if zone, ok := f.get(p + "AvailabilityZone"); ok && zone != "all" {
			return nil, elbErr("InvalidTarget", "Lambda targets accept only the AvailabilityZone value all.")
		}
		if _, ok := f.values[p+"Port"]; ok {
			return nil, elbErr("InvalidTarget", "Lambda targets do not accept a port.")
		}
		result = append(result, id)
	}
	return result, nil
}

func elbRegisterTargets(s *ELBStore, r *http.Request, f *elbForm, region string, authorize func(string, string) error) (any, error) {
	arn, _ := f.get("TargetGroupArn")
	targets, err := elbTargets(f, 5)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, elbInvalid("Targets must contain one Lambda function.")
	}
	if len(targets) > 1 {
		return nil, elbErr("TooManyTargets", "A Lambda target group can have only one registered function.")
	}
	if err = elbFinishFirst(f); err != nil {
		return nil, err
	}
	return nil, s.RegisterTarget(r.Context(), region, arn, targets[0], authorize)
}

func elbDeregisterTargets(s *ELBStore, r *http.Request, f *elbForm, region string, authorize func(string, string) error) (any, error) {
	arn, _ := f.get("TargetGroupArn")
	targets, err := elbTargets(f, 1)
	if err != nil {
		return nil, err
	}
	if len(targets) != 1 {
		return nil, elbInvalid("Targets must contain one Lambda function.")
	}
	if err = elbFinishFirst(f); err != nil {
		return nil, err
	}
	return nil, s.DeregisterTarget(r.Context(), region, arn, targets[0], authorize)
}

func elbDescribeTargetHealth(s *ELBStore, r *http.Request, f *elbForm, region string, authorize func(string, string) error) (any, error) {
	arn, _ := f.get("TargetGroupArn")
	targets, err := elbTargets(f, 20)
	if err != nil {
		return nil, err
	}
	if err = authorize("elasticloadbalancing:DescribeTargetHealth", "*"); err != nil {
		return nil, err
	}
	health, err := s.DescribeTargetHealth(r.Context(), region, arn, targets)
	if err != nil {
		return nil, err
	}
	out := make([]elbHealthWire, 0, len(health))
	for _, item := range health {
		var wire elbHealthWire
		wire.Target.ID = item.Target
		wire.TargetHealth.State, wire.TargetHealth.Reason, wire.TargetHealth.Description = item.State, item.Reason, item.Description
		out = append(out, wire)
	}
	return struct {
		TargetHealthDescriptions []elbHealthWire `xml:"TargetHealthDescriptions>member"`
	}{out}, nil
}

func elbCreateListener(s *ELBStore, r *http.Request, f *elbForm, region string, authorize func(string, string) error) (any, error) {
	listener := ELBListener{Region: region}
	listener.LoadBalancerARN, _ = f.get("LoadBalancerArn")
	protocol, _ := f.get("Protocol")
	switch protocol {
	case "HTTP":
		listener.Protocol = protocol
	case "HTTPS", "TCP", "TLS", "UDP", "TCP_UDP", "GENEVE", "QUIC", "TCP_QUIC":
		return nil, elbErr("UnsupportedProtocol", "Only HTTP listeners are simulated; HTTPS needs certificates that this simulator does not provide.")
	default:
		return nil, elbInvalid("Protocol must be HTTP.")
	}
	port, ok, err := f.integer("Port", 1, 65535)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, elbInvalid("Port is required for an application load balancer listener.")
	}
	listener.Port = port
	if listener.Action, err = elbParseAction(f); err != nil {
		return nil, err
	}
	if err = elbFinishFirst(f); err != nil {
		return nil, err
	}
	created, err := s.CreateListener(r.Context(), listener, authorize)
	if err != nil {
		return nil, err
	}
	return struct {
		Listeners []elbListenerWire `xml:"Listeners>member"`
	}{[]elbListenerWire{elbListenerOut(created)}}, nil
}

func elbDescribeListeners(s *ELBStore, r *http.Request, f *elbForm, region string, authorize func(string, string) error) (any, error) {
	loadBalancer, _ := f.get("LoadBalancerArn")
	arns, err := f.strings("ListenerArns", 20)
	if err != nil {
		return nil, err
	}
	if (loadBalancer == "") == (len(arns) == 0) {
		return nil, elbInvalid("Specify either LoadBalancerArn or ListenerArns.")
	}
	marker, size, err := elbPaging(f)
	if err != nil {
		return nil, err
	}
	if err = authorize("elasticloadbalancing:DescribeListeners", "*"); err != nil {
		return nil, err
	}
	items, err := s.DescribeListeners(r.Context(), region, loadBalancer, arns)
	if err != nil {
		return nil, err
	}
	page, next, err := elbPage(items, marker, elbScope("DescribeListeners", region, []string{loadBalancer}, arns), size, func(l ELBListener) string {
		return l.LoadBalancerARN + "\x00" + strconv.Itoa(100000+l.Port)
	})
	if err != nil {
		return nil, err
	}
	out := make([]elbListenerWire, 0, len(page))
	for _, listener := range page {
		out = append(out, elbListenerOut(listener))
	}
	return struct {
		Listeners  []elbListenerWire `xml:"Listeners>member"`
		NextMarker string            `xml:",omitempty"`
	}{out, next}, nil
}

func elbDeleteListener(s *ELBStore, r *http.Request, f *elbForm, region string, authorize func(string, string) error) (any, error) {
	arn, _ := f.get("ListenerArn")
	if err := elbFinishFirst(f); err != nil {
		return nil, err
	}
	return nil, s.DeleteListener(r.Context(), region, arn, authorize)
}
