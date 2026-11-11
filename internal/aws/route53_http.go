package aws

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const r53namespace = "https://route53.amazonaws.com/doc/2013-04-01/"

type r53node struct {
	name     string
	text     string
	children []*r53node
}

// Exact XML tree validation precedes typed decoding. encoding/xml alone silently
// drops unknown fields and accepts duplicate scalars, neither is safe for writes.
func r53decode(data []byte, root string, shape map[string][]string, out any) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var stack []*r53node
	var top *r53node
	for {
		token, e := dec.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return r53invalid("Malformed XML.")
		}
		switch t := token.(type) {
		case xml.StartElement:
			if len(stack) >= 12 {
				return r53invalid("XML nesting exceeds limit.")
			}
			if t.Name.Space != "" && t.Name.Space != r53namespace {
				return r53invalid("Unsupported XML namespace.")
			}
			for _, a := range t.Attr {
				if a.Name.Local != "xmlns" || a.Name.Space != "" {
					return r53invalid("XML attributes are unsupported.")
				}
			}
			n := &r53node{name: t.Name.Local}
			if len(stack) == 0 {
				if top != nil || n.name != root {
					return r53invalid("Expected one operation-specific XML document.")
				}
				top = n
			} else {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(t)) != "" {
					return r53invalid("Unexpected XML text.")
				}
			} else {
				stack[len(stack)-1].text += string(t)
			}
		case xml.Directive:
			return r53invalid("XML directives are unsupported.")
		case xml.ProcInst:
			if t.Target != "xml" || top != nil {
				return r53invalid("XML processing instructions are unsupported.")
			}
		}
	}
	if top == nil {
		return r53invalid("XML body required.")
	}
	var check func(*r53node, string) error
	check = func(n *r53node, path string) error {
		allowed, container := shape[path]
		if !container {
			if (n.name == "TTL" || n.name == "PrivateZone") && strings.TrimSpace(n.text) == "" {
				return r53invalid("Empty typed XML field.")
			}
			if len(n.children) > 0 {
				return r53invalid("Unexpected nested XML field.")
			}
			return nil
		}
		if strings.TrimSpace(n.text) != "" {
			return r53invalid("Unexpected container text.")
		}
		seen := map[string]bool{}
		for _, child := range n.children {
			found, repeat := false, false
			for _, field := range allowed {
				if strings.TrimSuffix(field, "*") == child.name {
					found = true
					repeat = strings.HasSuffix(field, "*")
				}
			}
			if !found || seen[child.name] && !repeat {
				return r53invalid("Unknown or duplicate XML field: " + child.name)
			}
			seen[child.name] = true
			if e := check(child, path+"/"+child.name); e != nil {
				return e
			}
		}
		return nil
	}
	if e := check(top, root); e != nil {
		return e
	}
	if e := xml.Unmarshal(data, out); e != nil {
		return r53invalid("Invalid XML field value.")
	}
	return nil
}
func r53write(w http.ResponseWriter, status int, root string, value any) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, xml.Header)
	enc := xml.NewEncoder(w)
	start := xml.StartElement{Name: xml.Name{Local: root}, Attr: []xml.Attr{{Name: xml.Name{Local: "xmlns"}, Value: r53namespace}}}
	_ = enc.EncodeElement(value, start)
}
func r53failure(w http.ResponseWriter, e error) {
	code, msg, status := "InternalFailure", "Simulator Route53 state operation failed.", 500
	var f route53Error
	if errors.As(e, &f) {
		code, msg, status = f.code, f.message, 400
	}
	switch code {
	case "NoSuchHostedZone", "NoSuchChange":
		status = 404
	case "AccessDenied":
		status = 403
	case "NotImplemented":
		status = 501
	case "HostedZoneAlreadyExists":
		status = 409
	}
	r53write(w, status, "ErrorResponse", struct {
		Error     struct{ Type, Code, Message string }
		RequestID string `xml:"RequestId"`
	}{struct{ Type, Code, Message string }{"Sender", code, msg}, "simulator"})
}
func r53query(q url.Values, keys ...string) error {
	for k, v := range q {
		found := false
		for _, key := range keys {
			if k == key {
				found = true
			}
		}
		if !found || len(v) != 1 || v[0] == "" {
			return r53invalid("Unknown, duplicate or empty query parameter.")
		}
	}
	return nil
}
func r53limit(q url.Values) (int, error) {
	if q.Get("maxitems") == "" {
		return 100, nil
	}
	n, e := strconv.Atoi(q.Get("maxitems"))
	if e != nil || n < 1 || n > 100 {
		return 0, r53invalid("maxitems must be 1..100.")
	}
	return n, nil
}
func r53pathID(s, prefix string) bool {
	if len(s) < 2 || len(s) > 32 || s[0] != prefix[0] {
		return false
	}
	for _, c := range s[1:] {
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// ServeHTTP receives a trusted IAM policy callback from Gateway. Local token
// authentication and identity binding belong to the existing server middleware.
// The callback must capture one immutable policy snapshot per request.
func (s *Route53Store) ServeHTTP(w http.ResponseWriter, r *http.Request, authorize func(string, string) error) {
	fail := func(e error) { r53failure(w, e) }
	if authorize == nil {
		fail(r53err("AccessDenied", "Authorization is required."))
		return
	}
	if svc := r.Header.Get("X-Simulator-Service"); svc != "" && svc != "route53" {
		fail(r53invalid("Service selection disagrees with Route53."))
		return
	}
	if r.Header.Get("X-Amz-Security-Token") != "" {
		fail(r53invalid("Session tokens are unsupported."))
		return
	}
	if a := r.Header.Get("Authorization"); a != "" {
		_, scope, ok := strings.Cut(a, "Credential=")
		scope, _, _ = strings.Cut(scope, ",")
		p := strings.Split(scope, "/")
		if !ok || !strings.HasPrefix(a, "AWS4-HMAC-SHA256 ") || len(p) != 5 || !strings.HasPrefix(p[0], "TCSIM") || p[2] != "us-east-1" || p[3] != "route53" || p[4] != "aws4_request" {
			fail(r53err("AccessDenied", "Use dummy TCSIM credentials with us-east-1/route53 signing scope."))
			return
		}
	}
	if (r.URL.RawPath != "" && r.URL.RawPath != r.URL.Path) || !strings.HasPrefix(r.URL.Path, "/2013-04-01/") {
		fail(r53invalid("Invalid Route53 path."))
		return
	}
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/2013-04-01/"), "/")
	parts := strings.Split(path, "/")
	op, id, resource := "", "", "*"
	switch {
	case path == "hostedzone" && r.Method == "POST":
		op = "CreateHostedZone"
	case path == "hostedzone" && r.Method == "GET":
		op = "ListHostedZones"
	case len(parts) == 2 && parts[0] == "hostedzone" && r53pathID(parts[1], "Z"):
		id = parts[1]
		if r.Method == "GET" {
			op = "GetHostedZone"
		} else if r.Method == "DELETE" {
			op = "DeleteHostedZone"
		}
	case len(parts) == 3 && parts[0] == "hostedzone" && r53pathID(parts[1], "Z") && parts[2] == "rrset":
		id = parts[1]
		if r.Method == "GET" {
			op = "ListResourceRecordSets"
		} else if r.Method == "POST" {
			op = "ChangeResourceRecordSets"
		}
	case len(parts) == 2 && parts[0] == "change" && r53pathID(parts[1], "C") && r.Method == "GET":
		op, id = "GetChange", parts[1]
	case path == "testdnsanswer" && r.Method == "GET":
		op = "TestDNSAnswer"
	}
	if op == "" {
		fail(r53err("NotImplemented", "Route53 operation is not supported."))
		return
	}
	if id != "" {
		resource = "arn:aws:route53:::hostedzone/" + id
		if op == "GetChange" {
			resource = "arn:aws:route53:::change/" + id
		}
	}
	if e := authorize("route53:"+op, resource); e != nil {
		fail(r53err("AccessDenied", "Route53 action is not permitted."))
		return
	}
	if op == "CreateHostedZone" {
		if e := authorize("ec2:DescribeVpcs", "*"); e != nil {
			fail(r53err("AccessDenied", "CreateHostedZone requires ec2:DescribeVpcs."))
			return
		}
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		fail(r53invalid("Malformed query."))
		return
	}
	keys := []string{}
	switch op {
	case "ListHostedZones":
		keys = []string{"marker", "maxitems"}
	case "ListResourceRecordSets":
		keys = []string{"name", "type", "maxitems"}
	case "TestDNSAnswer":
		keys = []string{"hostedzoneid", "recordname", "recordtype"}
	}
	if e = r53query(q, keys...); e != nil {
		fail(e)
		return
	}
	if r.Method == "POST" && r.Header.Get("Content-Type") != "" {
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || (media != "application/xml" && media != "text/xml") {
			fail(r53invalid("Route53 requires XML content."))
			return
		}
	}
	data, e := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if e != nil || len(data) > 1<<20 {
		fail(r53invalid("Request body exceeds 1 MiB or could not be read."))
		return
	}
	if r.Method != "POST" && len(data) != 0 {
		fail(r53invalid("This operation does not accept a body."))
		return
	}
	switch op {
	case "CreateHostedZone":
		var in struct {
			Name, CallerReference string
			Config                route53Config `xml:"HostedZoneConfig"`
		}
		shape := map[string][]string{"CreateHostedZoneRequest": {"Name", "CallerReference", "HostedZoneConfig"}, "CreateHostedZoneRequest/HostedZoneConfig": {"Comment", "PrivateZone"}}
		if e = r53decode(data, "CreateHostedZoneRequest", shape, &in); e != nil {
			fail(e)
			return
		}
		if in.Config.PrivateZone {
			fail(r53invalid("Private zones and VPC associations are unsupported."))
			return
		}
		z, c, e := s.create(r.Context(), in.Name, in.CallerReference, in.Config.Comment)
		if e != nil {
			fail(e)
			return
		}
		w.Header().Set("Location", "/2013-04-01"+z.ID)
		r53write(w, 201, "CreateHostedZoneResponse", struct {
			HostedZone    Route53Zone
			ChangeInfo    Route53Receipt
			DelegationSet r53delegation
		}{z, c, r53delegation{[]string{route53NS}}})
	case "GetHostedZone":
		z, _, e := s.snapshot(r.Context(), id)
		if e != nil {
			fail(e)
			return
		}
		r53write(w, 200, "GetHostedZoneResponse", struct {
			HostedZone    Route53Zone
			DelegationSet r53delegation
		}{z, r53delegation{[]string{route53NS}}})
	case "DeleteHostedZone":
		c, e := s.remove(r.Context(), id)
		if e != nil {
			fail(e)
			return
		}
		r53write(w, 200, "DeleteHostedZoneResponse", struct{ ChangeInfo Route53Receipt }{c})
	case "ChangeResourceRecordSets":
		var in struct {
			Batch struct {
				Comment string
				Changes []Route53Change `xml:"Changes>Change"`
			} `xml:"ChangeBatch"`
		}
		base := "ChangeResourceRecordSetsRequest"
		batch := base + "/ChangeBatch"
		change := batch + "/Changes/Change"
		rr := change + "/ResourceRecordSet"
		shape := map[string][]string{base: {"ChangeBatch"}, batch: {"Comment", "Changes"}, batch + "/Changes": {"Change*"}, change: {"Action", "ResourceRecordSet"}, rr: {"Name", "Type", "TTL", "ResourceRecords"}, rr + "/ResourceRecords": {"ResourceRecord*"}, rr + "/ResourceRecords/ResourceRecord": {"Value"}}
		if e = r53decode(data, base, shape, &in); e != nil {
			fail(e)
			return
		}
		c, e := s.change(r.Context(), id, in.Batch.Comment, in.Batch.Changes)
		if e != nil {
			fail(e)
			return
		}
		r53write(w, 200, "ChangeResourceRecordSetsResponse", struct{ ChangeInfo Route53Receipt }{c})
	case "GetChange":
		var raw string
		e := s.db.QueryRowContext(r.Context(), "SELECT document FROM aws_route53_changes WHERE id=?", id).Scan(&raw)
		if e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				e = r53err("NoSuchChange", "Change does not exist.")
			}
			fail(e)
			return
		}
		var c Route53Receipt
		if e = json.Unmarshal([]byte(raw), &c); e != nil {
			fail(e)
			return
		}
		r53write(w, 200, "GetChangeResponse", struct{ ChangeInfo Route53Receipt }{c})
	case "ListHostedZones":
		s.listZones(w, r, q, authorize)
	case "ListResourceRecordSets":
		s.listRecords(w, r, q, id)
	case "TestDNSAnswer":
		s.answer(w, r, q)
	}
}

type r53delegation struct {
	Servers []string `xml:"NameServers>NameServer"`
}

// Reverse DNS-label ordering is the Route53 RRset listing order, followed by
// numeric DNS type order (SOA precedes MX; lexical type sorting is incorrect).
func r53key(r Route53Record) string {
	p := strings.Split(strings.TrimSuffix(r.Name, "."), ".")
	for i, j := 0, len(p)-1; i < j; i, j = i+1, j-1 {
		p[i], p[j] = p[j], p[i]
	}
	return strings.Join(p, ".") + "."
}

var r53types = map[string]int{"A": 1, "NS": 2, "CNAME": 5, "SOA": 6, "MX": 15, "TXT": 16, "AAAA": 28}

func r53less(a, b Route53Record) bool {
	ka, kb := r53key(a), r53key(b)
	return ka < kb || ka == kb && r53types[a.Type] < r53types[b.Type]
}
func (s *Route53Store) listRecords(w http.ResponseWriter, r *http.Request, q url.Values, id string) {
	_, rr, e := s.snapshot(r.Context(), id)
	if e != nil {
		r53failure(w, e)
		return
	}
	limit, e := r53limit(q)
	if e != nil {
		r53failure(w, e)
		return
	}
	sort.Slice(rr, func(i, j int) bool { return r53less(rr[i], rr[j]) })
	start := Route53Record{}
	if q.Get("type") != "" {
		if q.Get("name") == "" || r53types[q.Get("type")] == 0 {
			r53failure(w, r53invalid("type requires name and a supported DNS type."))
			return
		}
		start.Type = q.Get("type")
	}
	if q.Get("name") != "" {
		start.Name, e = r53name(q.Get("name"))
		if e != nil {
			r53failure(w, e)
			return
		}
	}
	out := struct {
		Records     []Route53Record `xml:"ResourceRecordSets>ResourceRecordSet"`
		IsTruncated bool
		MaxItems    int
		NextName    string `xml:"NextRecordName,omitempty"`
		NextType    string `xml:"NextRecordType,omitempty"`
	}{MaxItems: limit}
	for _, rec := range rr {
		if start.Name != "" && r53less(rec, start) {
			continue
		}
		if len(out.Records) == limit {
			out.IsTruncated = true
			out.NextName, out.NextType = rec.Name, rec.Type
			break
		}
		out.Records = append(out.Records, rec)
	}
	r53write(w, 200, "ListResourceRecordSetsResponse", out)
}
func (s *Route53Store) listZones(w http.ResponseWriter, r *http.Request, q url.Values, authorize func(string, string) error) {
	limit, e := r53limit(q)
	if e != nil {
		r53failure(w, e)
		return
	}
	marker := q.Get("marker")
	if marker != "" && !r53pathID(marker, "Z") {
		r53failure(w, r53invalid("Invalid zone marker."))
		return
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		r53failure(w, e)
		return
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(r.Context(), "SELECT id FROM aws_route53_zones ORDER BY id")
	if e != nil {
		r53failure(w, e)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			break
		}
		ids = append(ids, id)
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil {
		r53failure(w, e)
		return
	}
	if rowErr != nil {
		r53failure(w, rowErr)
		return
	}
	out := struct {
		Zones       []Route53Zone `xml:"HostedZones>HostedZone"`
		Marker      string
		IsTruncated bool
		NextMarker  string `xml:"NextMarker,omitempty"`
		MaxItems    int
	}{Marker: marker, MaxItems: limit}
	if marker != "" {
		found := false
		for _, id := range ids {
			if id == marker {
				found = true
			}
		}
		if !found {
			r53failure(w, r53invalid("Unknown or stale zone marker."))
			return
		}
	}
	for _, id := range ids {
		if marker != "" && id < marker {
			continue
		}
		if len(out.Zones) == limit {
			out.IsTruncated = true
			out.NextMarker = id
			break
		}
		z, err := r53zone(r.Context(), tx, id)
		if err != nil {
			r53failure(w, err)
			return
		}
		out.Zones = append(out.Zones, z)
	}
	if e = tx.Commit(); e != nil {
		r53failure(w, e)
		return
	}
	r53write(w, 200, "ListHostedZonesResponse", out)
}
func (s *Route53Store) answer(w http.ResponseWriter, r *http.Request, q url.Values) {
	id := strings.TrimPrefix(q.Get("hostedzoneid"), "/hostedzone/")
	if !r53pathID(id, "Z") {
		r53failure(w, r53invalid("A hostedzoneid is required."))
		return
	}
	name, e := r53name(q.Get("recordname"))
	if e != nil {
		r53failure(w, e)
		return
	}
	kind := q.Get("recordtype")
	if r53types[kind] == 0 {
		r53failure(w, r53invalid("Unsupported recordtype."))
		return
	}
	z, rr, e := s.snapshot(r.Context(), id)
	if e != nil {
		r53failure(w, e)
		return
	}
	if name != z.Name && !strings.HasSuffix(name, "."+z.Name) {
		r53failure(w, r53invalid("Name must be inside the hosted zone."))
		return
	}
	code, values := "NOERROR", []string{}
	seen := map[string]bool{}
	for depth := 0; ; depth++ {
		if depth >= 16 || seen[name] {
			code = "SERVFAIL"
			values = nil
			break
		}
		seen[name] = true
		exists := false
		target := ""
		for _, rec := range rr {
			if rec.Name == name {
				exists = true
				if rec.Type == kind {
					values = rec.Values
				}
				if rec.Type == "CNAME" {
					target = rec.Values[0]
				}
			} else if strings.HasSuffix(rec.Name, "."+name) {
				exists = true
			}
		}
		if len(values) > 0 {
			break
		}
		if target != "" && kind != "CNAME" {
			if target != z.Name && !strings.HasSuffix(target, "."+z.Name) {
				r53failure(w, r53err("NotImplemented", "CNAME target is outside the selected zone; external resolution is unsupported."))
				return
			}
			name = target
			continue
		}
		if !exists {
			code = "NXDOMAIN"
		}
		break
	}
	r53write(w, 200, "TestDNSAnswerResponse", struct {
		Nameserver, RecordName, RecordType string
		RecordData                         []string `xml:"RecordData>RecordDataEntry"`
		ResponseCode, Protocol             string
	}{route53NS, q.Get("recordname"), kind, values, code, "UDP"})
}
