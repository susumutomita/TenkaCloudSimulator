package aws

import (
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const elbNamespace = "http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/"

// elbForm is a strict Query reader: every supplied field must be consumed by
// the selected operation, or the request is refused instead of ignored.
type elbForm struct {
	values url.Values
	used   map[string]bool
}

func (f *elbForm) get(key string) (string, bool) {
	values, ok := f.values[key]
	if !ok {
		return "", false
	}
	f.used[key] = true
	return values[0], true
}
func (f *elbForm) integer(key string, low, high int) (int, bool, error) {
	value, ok := f.get(key)
	if !ok {
		return 0, false, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < low || n > high || strconv.Itoa(n) != value {
		return 0, true, elbInvalid(fmt.Sprintf("Field %s must be an integer from %d to %d.", key, low, high))
	}
	return n, true, nil
}
func (f *elbForm) boolean(key string) (bool, bool, error) {
	value, ok := f.get(key)
	if !ok {
		return false, false, nil
	}
	if value != "true" && value != "false" {
		return false, true, elbInvalid("Field " + key + " must be true or false.")
	}
	return value == "true", true, nil
}

var elbMemberIndex = regexp.MustCompile(`^([1-9][0-9]{0,3})(?:\.|$)`)

// count returns the number of members of a Query list. Members must be numbered
// contiguously from 1. An empty list may be sent as the bare list name.
func (f *elbForm) count(list string, limit int) (int, error) {
	if value, ok := f.get(list); ok && value != "" {
		return 0, elbInvalid("List " + list + " must use numbered members.")
	}
	seen := map[int]bool{}
	for key := range f.values {
		rest, ok := strings.CutPrefix(key, list+".member.")
		if !ok {
			continue
		}
		match := elbMemberIndex.FindStringSubmatch(rest)
		if match == nil {
			return 0, elbInvalid("Malformed member index in " + list + ".")
		}
		n, _ := strconv.Atoi(match[1])
		seen[n] = true
	}
	for i := 1; i <= len(seen); i++ {
		if !seen[i] {
			return 0, elbInvalid("List " + list + " members must be numbered contiguously from 1.")
		}
	}
	if len(seen) > limit {
		return 0, elbInvalid(fmt.Sprintf("List %s accepts at most %d members.", list, limit))
	}
	return len(seen), nil
}
func (f *elbForm) strings(list string, limit int) ([]string, error) {
	n, err := f.count(list, limit)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		value, ok := f.get(fmt.Sprintf("%s.member.%d", list, i))
		if !ok || value == "" || !validText(value) || len(value) > 2048 {
			return nil, elbInvalid("List " + list + " contains an empty or invalid member.")
		}
		result = append(result, value)
	}
	return result, nil
}
func (f *elbForm) finish() error {
	var unused []string
	for key := range f.values {
		if !f.used[key] {
			unused = append(unused, key)
		}
	}
	if len(unused) > 0 {
		slices.Sort(unused)
		return elbInvalid("Unsupported request field: " + elbQuote(unused[0]))
	}
	return nil
}

func elbParseHealth(f *elbForm, h ELBHealthCheck) (ELBHealthCheck, error) {
	var err error
	if enabled, ok, e := f.boolean("HealthCheckEnabled"); e != nil {
		return h, e
	} else if ok {
		h.Enabled = enabled
	}
	if path, ok := f.get("HealthCheckPath"); ok {
		h.Path = path
	}
	for _, field := range []struct {
		key       string
		target    *int
		low, high int
	}{{"HealthCheckIntervalSeconds", &h.Interval, 5, 300}, {"HealthCheckTimeoutSeconds", &h.Timeout, 2, 120}, {"HealthyThresholdCount", &h.Healthy, 2, 10}, {"UnhealthyThresholdCount", &h.Unhealthy, 2, 10}} {
		if value, ok, e := f.integer(field.key, field.low, field.high); e != nil {
			return h, e
		} else if ok {
			*field.target = value
		}
	}
	if code, ok := f.get("Matcher.HttpCode"); ok {
		h.Matcher = code
	}
	if err = elbValidHealth(h); err != nil {
		return h, err
	}
	return h, nil
}

var elbFixedStatus = regexp.MustCompile(`^[245][0-9][0-9]$`)
var elbContentTypes = []string{"text/plain", "text/css", "text/html", "application/javascript", "application/json"}

// elbParseAction reads the single default action. Forward to one target group
// and fixed-response are supported; redirect and authentication are refused.
func elbParseAction(f *elbForm) (ELBAction, error) {
	n, err := f.count("DefaultActions", 5)
	if err != nil {
		return ELBAction{}, err
	}
	if n != 1 {
		return ELBAction{}, elbErr("InvalidLoadBalancerAction", "Exactly one default action is supported.")
	}
	p := "DefaultActions.member.1."
	action := ELBAction{}
	action.Type, _ = f.get(p + "Type")
	if order, ok, e := f.integer(p+"Order", 1, 50000); e != nil {
		return action, e
	} else if ok {
		action.Order = order
	}
	_, hasForward := f.values[p+"ForwardConfig.TargetGroups.member.1.TargetGroupArn"]
	fixed := false
	for key := range f.values {
		fixed = fixed || strings.HasPrefix(key, p+"FixedResponseConfig.")
	}
	switch action.Type {
	case "forward":
		if fixed {
			return action, elbErr("InvalidLoadBalancerAction", "A forward action cannot include a fixed response.")
		}
		action.TargetGroupARN, _ = f.get(p + "TargetGroupArn")
		action.Weight = 1
		if hasForward || f.values.Has(p+"ForwardConfig.TargetGroups") {
			groups, e := f.count(p+"ForwardConfig.TargetGroups", 5)
			if e != nil {
				return action, e
			}
			if groups != 1 {
				return action, elbErr("InvalidLoadBalancerAction", "Forward actions support exactly one target group.")
			}
			arn, _ := f.get(p + "ForwardConfig.TargetGroups.member.1.TargetGroupArn")
			if action.TargetGroupARN != "" && action.TargetGroupARN != arn {
				return action, elbErr("InvalidLoadBalancerAction", "TargetGroupArn and ForwardConfig must name the same target group.")
			}
			action.TargetGroupARN = arn
			if weight, ok, e := f.integer(p+"ForwardConfig.TargetGroups.member.1.Weight", 0, 999); e != nil {
				return action, e
			} else if ok && weight == 0 {
				return action, elbErr("InvalidLoadBalancerAction", "A single forward target group needs a positive weight.")
			} else if ok {
				action.Weight = weight
			}
			if sticky, _, e := f.boolean(p + "ForwardConfig.TargetGroupStickinessConfig.Enabled"); e != nil {
				return action, e
			} else if sticky {
				return action, elbErr("InvalidLoadBalancerAction", "Target group stickiness is not simulated.")
			}
		}
		if action.TargetGroupARN == "" {
			return action, elbErr("InvalidLoadBalancerAction", "A forward action needs a target group.")
		}
	case "fixed-response":
		action.FixedStatus, _ = f.get(p + "FixedResponseConfig.StatusCode")
		if !elbFixedStatus.MatchString(action.FixedStatus) {
			return action, elbInvalid("FixedResponseConfig StatusCode must be a 2XX, 4XX or 5XX code.")
		}
		if contentType, ok := f.get(p + "FixedResponseConfig.ContentType"); ok {
			if !slices.Contains(elbContentTypes, contentType) {
				return action, elbInvalid("FixedResponseConfig ContentType is not one of the documented values.")
			}
			action.FixedContentType = contentType
		}
		if body, ok := f.get(p + "FixedResponseConfig.MessageBody"); ok {
			if len(body) > 1024 || !utf8Text(body) {
				return action, elbInvalid("FixedResponseConfig MessageBody must be UTF-8 text of at most 1024 bytes.")
			}
			action.FixedBody = body
		}
	case "redirect", "authenticate-oidc", "authenticate-cognito", "jwt-validation":
		return action, elbErr("InvalidLoadBalancerAction", "Only forward and fixed-response actions are simulated.")
	default:
		return action, elbInvalid("Unknown action type.")
	}
	return action, nil
}
func utf8Text(s string) bool {
	for _, r := range s {
		if r == 0xfffd || r < 32 && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

// Wire projections of the official ELBv2 XML shapes.
type elbAZWire struct {
	SubnetID string `xml:"SubnetId"`
}
type elbLBWire struct {
	LoadBalancerArn   string
	DNSName           string
	CreatedTime       string
	LoadBalancerName  string
	Scheme            string
	State             struct{ Code string }
	Type              string
	AvailabilityZones []elbAZWire `xml:"AvailabilityZones>member"`
	SecurityGroups    []string    `xml:"SecurityGroups>member"`
	IPAddressType     string      `xml:"IpAddressType"`
}
type elbTGWire struct {
	TargetGroupArn             string
	TargetGroupName            string
	HealthCheckEnabled         bool
	HealthCheckIntervalSeconds int
	HealthCheckTimeoutSeconds  int
	HealthyThresholdCount      int
	UnhealthyThresholdCount    int
	HealthCheckPath            string
	Matcher                    struct {
		HTTPCode string `xml:"HttpCode"`
	}
	LoadBalancerArns []string `xml:"LoadBalancerArns>member"`
	TargetType       string
}
type elbTupleWire struct {
	TargetGroupArn string
	Weight         int
}
type elbForwardWire struct {
	TargetGroups                []elbTupleWire `xml:"TargetGroups>member"`
	TargetGroupStickinessConfig struct{ Enabled bool }
}
type elbFixedWire struct {
	MessageBody string `xml:",omitempty"`
	StatusCode  string
	ContentType string `xml:",omitempty"`
}
type elbActionWire struct {
	Type                string
	TargetGroupArn      string          `xml:",omitempty"`
	Order               int             `xml:",omitempty"`
	ForwardConfig       *elbForwardWire `xml:",omitempty"`
	FixedResponseConfig *elbFixedWire   `xml:",omitempty"`
}
type elbListenerWire struct {
	ListenerArn     string
	LoadBalancerArn string
	Port            int
	Protocol        string
	DefaultActions  []elbActionWire `xml:"DefaultActions>member"`
}
type elbHealthWire struct {
	Target struct {
		ID string `xml:"Id"`
	}
	TargetHealth struct {
		State       string
		Reason      string `xml:",omitempty"`
		Description string `xml:",omitempty"`
	}
}

func elbLBOut(lb ELBLoadBalancer) elbLBWire {
	out := elbLBWire{LoadBalancerArn: lb.ARN, DNSName: lb.DNSName, CreatedTime: lb.Created, LoadBalancerName: lb.Name, Scheme: lb.Scheme, Type: "application", SecurityGroups: lb.SecurityGroups, IPAddressType: "ipv4"}
	out.State.Code = "active"
	for _, subnet := range lb.Subnets {
		out.AvailabilityZones = append(out.AvailabilityZones, elbAZWire{subnet})
	}
	return out
}
func elbTGOut(tg ELBTargetGroup) elbTGWire {
	h := tg.Health
	out := elbTGWire{TargetGroupArn: tg.ARN, TargetGroupName: tg.Name, HealthCheckEnabled: h.Enabled, HealthCheckIntervalSeconds: h.Interval, HealthCheckTimeoutSeconds: h.Timeout, HealthyThresholdCount: h.Healthy, UnhealthyThresholdCount: h.Unhealthy, HealthCheckPath: h.Path, LoadBalancerArns: tg.LoadBalancerARNs, TargetType: "lambda"}
	out.Matcher.HTTPCode = h.Matcher
	return out
}
func elbListenerOut(l ELBListener) elbListenerWire {
	a := l.Action
	action := elbActionWire{Type: a.Type, Order: a.Order}
	if a.Type == "forward" {
		action.TargetGroupArn = a.TargetGroupARN
		action.ForwardConfig = &elbForwardWire{TargetGroups: []elbTupleWire{{a.TargetGroupARN, a.Weight}}}
	} else {
		action.FixedResponseConfig = &elbFixedWire{a.FixedBody, a.FixedStatus, a.FixedContentType}
	}
	return elbListenerWire{l.ARN, l.LoadBalancerARN, l.Port, l.Protocol, []elbActionWire{action}}
}

func elbResponse(w http.ResponseWriter, action string, result any) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(200)
	_, _ = io.WriteString(w, xml.Header)
	encoder := xml.NewEncoder(w)
	root := xml.StartElement{Name: xml.Name{Local: action + "Response"}, Attr: []xml.Attr{{Name: xml.Name{Local: "xmlns"}, Value: elbNamespace}}}
	_ = encoder.EncodeToken(root)
	if result == nil {
		result = struct{}{}
	}
	_ = encoder.EncodeElement(result, xml.StartElement{Name: xml.Name{Local: action + "Result"}})
	_ = encoder.EncodeElement(struct {
		RequestID string `xml:"RequestId"`
	}{fmt.Sprintf("elb-local-%016x", sequence.Add(1))}, xml.StartElement{Name: xml.Name{Local: "ResponseMetadata"}})
	_ = encoder.EncodeToken(root.End())
	_ = encoder.Flush()
}

func elbError(w http.ResponseWriter, err error) {
	code, message, status := "InternalFailure", "Simulator load balancing state operation failed.", 500
	var failure elbFailure
	if errors.As(err, &failure) {
		code, message, status = failure.code, failure.message, failure.status
	}
	type detail struct {
		Type, Code, Message string
	}
	kind := "Sender"
	if status >= 500 {
		kind = "Receiver"
	}
	writeXML(w, status, struct {
		XMLName   xml.Name `xml:"ErrorResponse"`
		Namespace string   `xml:"xmlns,attr"`
		Error     detail
		RequestID string `xml:"RequestId"`
	}{Namespace: elbNamespace, Error: detail{kind, code, message}, RequestID: fmt.Sprintf("elb-local-%016x", sequence.Add(1))})
}

// elbPage returns one live page over a stable ordering. The marker is bound to
// the operation, region and filter, names the last returned key, and is refused
// for any other request. It is a position, never an authority.
func elbPage[T any](items []T, marker, scope string, size int, key func(T) string) ([]T, string, error) {
	start := 0
	encode := base64.RawURLEncoding.EncodeToString
	if marker != "" {
		fields := strings.Split(marker, ".")
		var cursor []byte
		var err error
		if len(marker) <= 4096 && len(fields) == 3 && fields[0] == "ELB1" && fields[1] == encode([]byte(scope)) {
			cursor, err = base64.RawURLEncoding.DecodeString(fields[2])
		}
		if len(cursor) == 0 || err != nil {
			return nil, "", elbInvalid("Invalid Marker for this request.")
		}
		for start < len(items) && key(items[start]) <= string(cursor) {
			start++
		}
	}
	end := min(start+size, len(items))
	next := ""
	if end < len(items) {
		next = "ELB1." + encode([]byte(scope)) + "." + encode([]byte(key(items[end-1])))
	}
	return items[start:end], next, nil
}
