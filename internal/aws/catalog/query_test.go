package catalog

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// xnode is a generic parsed XML element.
type xnode struct {
	name  string
	attrs map[string]string
	text  string
	kids  []*xnode
}

func parseXML(t *testing.T, data []byte) *xnode {
	t.Helper()
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var stack []*xnode
	var root *xnode
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("invalid XML %v: %s", err, data)
		}
		switch tok := token.(type) {
		case xml.StartElement:
			n := &xnode{name: tok.Name.Local, attrs: map[string]string{}}
			for _, a := range tok.Attr {
				n.attrs[a.Name.Local] = a.Value
			}
			if tok.Name.Space != "" {
				n.attrs["xmlns"] = tok.Name.Space
			}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.kids = append(parent.kids, n)
			} else {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(tok)
			}
		}
	}
	return root
}

// get follows a path of first children by name; nil when missing.
func (n *xnode) get(path ...string) *xnode {
	current := n
	for _, name := range path {
		var next *xnode
		for _, kid := range current.kids {
			if kid.name == name {
				next = kid
				break
			}
		}
		if next == nil {
			return nil
		}
		current = next
	}
	return current
}

func (n *xnode) all(name string) []*xnode {
	var out []*xnode
	for _, kid := range n.kids {
		if kid.name == name {
			out = append(out, kid)
		}
	}
	return out
}

type queryReply struct {
	status int
	header http.Header
	raw    string
	root   *xnode
}

func (r queryReply) code() string {
	if code := r.root.get("Error", "Code"); r.root.name == "ErrorResponse" && code != nil {
		return code.text
	}
	return ""
}

// result returns the result wrapper element of a successful response.
func (r queryReply) result(t *testing.T, action string) *xnode {
	t.Helper()
	if r.status != 200 || r.root.name != action+"Response" {
		t.Fatalf("%s failed: %d %s", action, r.status, r.raw)
	}
	if id := r.root.get("ResponseMetadata", "RequestId"); id == nil || len(id.text) != 36 || r.header.Get("X-Amzn-RequestId") != id.text {
		t.Fatalf("%s lacks ResponseMetadata/RequestId: %s", action, r.raw)
	}
	return r.root.get(action + "Result")
}

func expectQuery(t *testing.T, r queryReply, status int, code string) {
	t.Helper()
	if r.status != status || r.code() != code {
		t.Fatalf("want %d %s, got %d %s: %s", status, code, r.status, r.code(), r.raw)
	}
	if r.header.Get("Content-Type") != "text/xml" || r.root.attrs["xmlns"] != "http://rds.amazonaws.com/doc/2014-10-31/" {
		t.Fatalf("error envelope %v %s", r.header, r.raw)
	}
	if r.root.get("RequestId") == nil || r.root.get("Error", "Message") == nil {
		t.Fatalf("error envelope lacks RequestId or Message: %s", r.raw)
	}
	party := "Sender"
	if status >= 500 {
		party = "Receiver"
	}
	if r.root.get("Error", "Type").text != party {
		t.Fatalf("error type %s", r.raw)
	}
}

func (h *harness) send(req *http.Request) queryReply {
	h.t.Helper()
	recorder := httptest.NewRecorder()
	h.engine.ServeHTTP(recorder, req)
	out := queryReply{status: recorder.Code, header: recorder.Header(), raw: recorder.Body.String()}
	out.root = parseXML(h.t, recorder.Body.Bytes())
	return out
}

// query sends one RDS Action with key/value parameter pairs.
func (h *harness) query(action string, pairs ...string) queryReply {
	h.t.Helper()
	form := url.Values{"Action": {action}, "Version": {"2014-10-31"}}
	for i := 0; i+1 < len(pairs); i += 2 {
		form.Add(pairs[i], pairs[i+1])
	}
	return h.form(form.Encode(), nil)
}

func (h *harness) form(body string, headers map[string]string) queryReply {
	h.t.Helper()
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	sign(req, "rds", "us-east-1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if !h.engine.Owns(req) {
		h.t.Fatal("the catalog does not own an rds-signed Query request")
	}
	return h.send(req)
}

func TestRDSQueryLifecycleMatchesOfficialWire(t *testing.T) {
	h := newHarness(t, rds)
	created := h.query("CreateDBSubnetGroup", "DBSubnetGroupName", "App-Subnets", "DBSubnetGroupDescription", "app",
		"SubnetIds.SubnetIdentifier.1", "subnet-0a1b2c3d", "SubnetIds.SubnetIdentifier.2", "subnet-4e5f6a7b",
		"Tags.Tag.1.Key", "team", "Tags.Tag.1.Value", "a")
	group := created.result(t, "CreateDBSubnetGroup").get("DBSubnetGroup")
	if created.root.attrs["xmlns"] != "http://rds.amazonaws.com/doc/2014-10-31/" || created.header.Get("Content-Type") != "text/xml" {
		t.Fatalf("response envelope %v %s", created.header, created.raw)
	}
	if group.get("DBSubnetGroupName").text != "app-subnets" || group.get("DBSubnetGroupArn").text != "arn:aws:rds:us-east-1:000000000000:subgrp:app-subnets" {
		t.Fatalf("subnet group %s", created.raw)
	}
	subnets := group.get("Subnets").all("Subnet")
	if len(subnets) != 2 || subnets[1].get("SubnetIdentifier").text != "subnet-4e5f6a7b" || group.get("VpcId") != nil {
		t.Fatalf("subnets %s", created.raw)
	}
	expectQuery(t, h.query("CreateDBSubnetGroup", "DBSubnetGroupName", "app-subnets", "DBSubnetGroupDescription", "x",
		"SubnetIds.SubnetIdentifier.1", "subnet-0a1b2c3d", "SubnetIds.SubnetIdentifier.2", "subnet-4e5f6a7b"), 400, "DBSubnetGroupAlreadyExists")
	expectQuery(t, h.query("CreateDBSubnetGroup", "DBSubnetGroupName", "empty", "DBSubnetGroupDescription", "x", "SubnetIds", ""), 400, "DBSubnetGroupDoesNotCoverEnoughAZs")
	expectQuery(t, h.query("DescribeDBSubnetGroups", "DBSubnetGroupName", "missing"), 404, "DBSubnetGroupNotFoundFault")
	listed := h.query("DescribeDBSubnetGroups", "DBSubnetGroupName", "APP-SUBNETS").result(t, "DescribeDBSubnetGroups")
	if items := listed.get("DBSubnetGroups").all("DBSubnetGroup"); len(items) != 1 || listed.get("Marker") != nil {
		t.Fatalf("describe %v", listed)
	}
	modified := h.query("ModifyDBSubnetGroup", "DBSubnetGroupName", "app-subnets", "DBSubnetGroupDescription", "three",
		"SubnetIds.SubnetIdentifier.1", "subnet-0a1b2c3d", "SubnetIds.SubnetIdentifier.2", "subnet-4e5f6a7b", "SubnetIds.SubnetIdentifier.3", "subnet-0123456789abcdef0")
	if g := modified.result(t, "ModifyDBSubnetGroup").get("DBSubnetGroup"); len(g.get("Subnets").all("Subnet")) != 3 || g.get("DBSubnetGroupDescription").text != "three" {
		t.Fatalf("modify %s", modified.raw)
	}
	expectQuery(t, h.query("ModifyDBSubnetGroup", "DBSubnetGroupName", "missing", "SubnetIds.SubnetIdentifier.1", "subnet-0a1b2c3d", "SubnetIds.SubnetIdentifier.2", "subnet-4e5f6a7b"), 404, "DBSubnetGroupNotFoundFault")
	expectQuery(t, h.query("ModifyDBSubnetGroup", "DBSubnetGroupName", "app-subnets", "VpcId", "vpc-1", "SubnetIds.SubnetIdentifier.1", "subnet-0a1b2c3d"), 400, "InvalidParameterValue")
	expectQuery(t, h.query("DescribeDBSubnetGroups", "Filters.Filter.1.Name", "x", "Filters.Filter.1.Values.Value.1", "y"), 400, "InvalidParameterValue")
	expectQuery(t, h.query("DescribeDBSubnetGroups", "MaxRecords", "19"), 400, "InvalidParameterValue")

	pg := h.query("CreateDBParameterGroup", "DBParameterGroupName", "App-Params", "DBParameterGroupFamily", "mysql8.0", "Description", "p").
		result(t, "CreateDBParameterGroup").get("DBParameterGroup")
	if pg.get("DBParameterGroupName").text != "app-params" || pg.get("DBParameterGroupArn").text != "arn:aws:rds:us-east-1:000000000000:pg:app-params" {
		t.Fatalf("parameter group %v", pg)
	}
	expectQuery(t, h.query("DescribeDBParameterGroups", "DBParameterGroupName", "nope"), 404, "DBParameterGroupNotFound")
	expectQuery(t, h.query("DeleteDBParameterGroup", "DBParameterGroupName", "nope"), 404, "DBParameterGroupNotFound")
	expectQuery(t, h.query("CreateDBParameterGroup", "DBParameterGroupName", "app-params", "DBParameterGroupFamily", "mysql8.0", "Description", "p"), 400, "DBParameterGroupAlreadyExists")
	cluster := h.query("CreateDBClusterParameterGroup", "DBClusterParameterGroupName", "Aurora", "DBParameterGroupFamily", "aurora-postgresql16", "Description", "c").
		result(t, "CreateDBClusterParameterGroup").get("DBClusterParameterGroup")
	if cluster.get("DBClusterParameterGroupArn").text != "arn:aws:rds:us-east-1:000000000000:cluster-pg:aurora" {
		t.Fatalf("cluster parameter group %v", cluster)
	}
	if items := h.query("DescribeDBClusterParameterGroups").result(t, "DescribeDBClusterParameterGroups").get("DBClusterParameterGroups").all("DBClusterParameterGroup"); len(items) != 1 {
		t.Fatalf("cluster parameter groups %d", len(items))
	}
	og := h.query("CreateOptionGroup", "OptionGroupName", "app-options", "EngineName", "mysql", "MajorEngineVersion", "8.0", "OptionGroupDescription", "o").
		result(t, "CreateOptionGroup").get("OptionGroup")
	if og.get("OptionGroupArn").text != "arn:aws:rds:us-east-1:000000000000:og:app-options" || og.get("Options") == nil || len(og.get("Options").kids) != 0 {
		t.Fatalf("option group %v", og)
	}
	for engine, want := range map[string]int{"mysql": 1, "postgres": 0} {
		if items := h.query("DescribeOptionGroups", "EngineName", engine, "MajorEngineVersion", "8.0").result(t, "DescribeOptionGroups").get("OptionGroupsList").all("OptionGroup"); len(items) != want {
			t.Fatalf("option groups for %s: %d", engine, len(items))
		}
	}
	expectQuery(t, h.query("DescribeOptionGroups", "OptionGroupName", "missing"), 404, "OptionGroupNotFoundFault")

	arn := group.get("DBSubnetGroupArn").text
	h.query("AddTagsToResource", "ResourceName", arn, "Tags.Tag.1.Key", "stage", "Tags.Tag.1.Value", "dev").result(t, "AddTagsToResource")
	h.query("RemoveTagsFromResource", "ResourceName", arn, "TagKeys.member.1", "team").result(t, "RemoveTagsFromResource")
	tags := h.query("ListTagsForResource", "ResourceName", arn).result(t, "ListTagsForResource").get("TagList").all("Tag")
	if len(tags) != 1 || tags[0].get("Key").text != "stage" || tags[0].get("Value").text != "dev" {
		t.Fatalf("tags %v", tags)
	}
	expectQuery(t, h.query("ListTagsForResource", "ResourceName", "arn:aws:rds:us-east-1:000000000000:subgrp:missing"), 404, "DBInstanceNotFound")
	expectQuery(t, h.query("ListTagsForResource", "ResourceName", arn, "Filters.Filter.1.Name", "x"), 400, "InvalidParameterValue")
	expectQuery(t, h.query("AddTagsToResource", "ResourceName", arn, "Tags.Tag.1.Key", "rds:x", "Tags.Tag.1.Value", "y"), 400, "InvalidParameterValue")

	// Deleting returns only ResponseMetadata, and the group is gone.
	deleted := h.query("DeleteDBSubnetGroup", "DBSubnetGroupName", "App-Subnets")
	if deleted.status != 200 || deleted.root.name != "DeleteDBSubnetGroupResponse" || len(deleted.root.kids) != 1 {
		t.Fatalf("delete %s", deleted.raw)
	}
	expectQuery(t, h.query("DeleteDBSubnetGroup", "DBSubnetGroupName", "app-subnets"), 404, "DBSubnetGroupNotFoundFault")
	for _, op := range []string{"DeleteDBParameterGroup", "DeleteDBClusterParameterGroup", "DeleteOptionGroup"} {
		member := map[string][]string{"DeleteDBParameterGroup": {"DBParameterGroupName", "app-params"}, "DeleteDBClusterParameterGroup": {"DBClusterParameterGroupName", "aurora"}, "DeleteOptionGroup": {"OptionGroupName", "app-options"}}[op]
		h.query(op, member...).result(t, op)
	}
}

func TestRDSQueryPaginationQuotasAndAuthorization(t *testing.T) {
	h := newHarness(t, rds)
	for i := 0; i < 25; i++ {
		h.query("CreateDBParameterGroup", "DBParameterGroupName", fmt.Sprintf("p%d", i), "DBParameterGroupFamily", "postgres16", "Description", "d").result(t, "CreateDBParameterGroup")
	}
	first := h.query("DescribeDBParameterGroups", "MaxRecords", "20").result(t, "DescribeDBParameterGroups")
	marker := first.get("Marker")
	if len(first.get("DBParameterGroups").all("DBParameterGroup")) != 20 || marker == nil {
		t.Fatalf("first page %v", first)
	}
	second := h.query("DescribeDBParameterGroups", "MaxRecords", "20", "Marker", marker.text).result(t, "DescribeDBParameterGroups")
	if len(second.get("DBParameterGroups").all("DBParameterGroup")) != 5 || second.get("Marker") != nil {
		t.Fatalf("second page %v", second)
	}
	expectQuery(t, h.query("DescribeDBParameterGroups", "Marker", strings.Repeat("0", 32)), 400, "InvalidParameterValue")
	for i := 0; i < 20; i++ {
		h.query("CreateOptionGroup", "OptionGroupName", fmt.Sprintf("o%d", i), "EngineName", "postgres", "MajorEngineVersion", "16", "OptionGroupDescription", "d").result(t, "CreateOptionGroup")
	}
	expectQuery(t, h.query("CreateOptionGroup", "OptionGroupName", "over", "EngineName", "postgres", "MajorEngineVersion", "16", "OptionGroupDescription", "d"), 400, "OptionGroupQuotaExceededFault")

	h.policy.deny["rds:CreateDBSubnetGroup"] = true
	expectQuery(t, h.query("CreateDBSubnetGroup", "DBSubnetGroupName", "denied", "DBSubnetGroupDescription", "d", "SubnetIds.SubnetIdentifier.1", "subnet-0a1b2c3d", "SubnetIds.SubnetIdentifier.2", "subnet-4e5f6a7b"), 403, "AccessDenied")
	h.policy.deny["rds:DescribeDBSubnetGroups"] = true
	expectQuery(t, h.query("DescribeDBSubnetGroups"), 403, "AccessDenied")
	h.policy.err = errors.New("policy store unavailable")
	expectQuery(t, h.query("DescribeDBParameterGroups"), 500, "InternalFailure")
	h.policy.err = nil
	last := h.policy.calls[len(h.policy.calls)-1]
	if last != "simulator-admin rds:DescribeDBParameterGroups arn:aws:rds:us-east-1:000000000000:pg:*" {
		t.Fatalf("authorization %q", last)
	}
	// Region scope follows the credential and the simulator's regions.
	req := httptest.NewRequest("POST", "/", strings.NewReader("Action=DescribeDBParameterGroups&Version=2014-10-31"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	sign(req, "rds", "ap-northeast-1")
	if items := h.send(req).result(t, "DescribeDBParameterGroups").get("DBParameterGroups").all("DBParameterGroup"); len(items) != 0 {
		t.Fatalf("regions share state: %d", len(items))
	}
	req = httptest.NewRequest("POST", "/", strings.NewReader("Action=DescribeDBParameterGroups&Version=2014-10-31"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	sign(req, "rds", "eu-west-9")
	expectQuery(t, h.send(req), 400, "InvalidParameterValue")
	expectQuery(t, h.form("Action=DescribeDBParameterGroups&Version=2014-10-31", map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=AKIAREAL/20261106/us-east-1/rds/aws4_request, SignedHeaders=host, Signature=0"}), 403, "AccessDenied")
}

func TestQueryRequestFramingErrors(t *testing.T) {
	h := newHarness(t, rds)
	post := func(method, target, contentType, body string) queryReply {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		sign(req, "rds", "us-east-1")
		return h.send(req)
	}
	form := "application/x-www-form-urlencoded"
	expectQuery(t, post("GET", "/", form, ""), 400, "MalformedQueryString")
	expectQuery(t, post("POST", "/x", form, ""), 400, "MalformedQueryString")
	expectQuery(t, post("POST", "/?Action=DescribeDBSubnetGroups", form, ""), 400, "MalformedQueryString")
	expectQuery(t, post("POST", "/", "application/json", "{}"), 400, "MalformedQueryString")
	expectQuery(t, post("POST", "/", form, "Action=%zz"), 400, "MalformedQueryString")
	expectQuery(t, post("POST", "/", form, "Version=2014-10-31"), 400, "MissingAction")
	expectQuery(t, post("POST", "/", form, "Action=DescribeDBSubnetGroups&Action=DescribeDBSubnetGroups"), 400, "MalformedQueryString")
	expectQuery(t, post("POST", "/", form, "Action=LaunchRockets&Version=2014-10-31"), 400, "InvalidAction")
	unbound := post("POST", "/", form, "Action=CreateDBInstance&Version=2014-10-31&DBInstanceIdentifier=db")
	expectQuery(t, unbound, 501, "UnknownOperationException")
	expectQuery(t, post("POST", "/", form, "Action=DescribeDBSubnetGroups"), 400, "MissingParameter")
	expectQuery(t, post("POST", "/", form, "Action=DescribeDBSubnetGroups&Version=2013-01-10"), 400, "InvalidParameterValue")
	expectQuery(t, post("POST", "/", form, "Action=DescribeDBSubnetGroups&Version=2014-10-31&Unknown=1"), 400, "InvalidParameterValue")
	expectQuery(t, post("POST", "/", form, "Action=DescribeDBSubnetGroups&Version=2014-10-31&MaxRecords=many"), 400, "InvalidParameterValue")
	expectQuery(t, post("POST", "/", form, "Action=DescribeDBSubnetGroups&Version=2014-10-31&"+strings.Repeat("x", maxRequestBytes)), 400, "InvalidParameterValue")
	expectQuery(t, post("POST", "/", form, "Action=CreateDBSubnetGroup&Version=2014-10-31&DBSubnetGroupName=a"), 400, "InvalidParameterValue")
	// The message never names internal state.
	if r := post("POST", "/", form, "Action=LaunchRockets&Version=2014-10-31"); strings.Contains(r.raw, "sql") {
		t.Fatalf("leak %s", r.raw)
	}
	// An unsigned first-party request is routed by X-Simulator-Service.
	req := httptest.NewRequest("POST", "/", strings.NewReader("Action=DescribeDBSubnetGroups&Version=2014-10-31"))
	req.Header.Set("Content-Type", form)
	req.Header.Set("X-Simulator-Service", "rds")
	if !h.engine.Owns(req) {
		t.Fatal("unsigned rds request not owned")
	}
	h.send(req).result(t, "DescribeDBSubnetGroups")
}

// The catalog claims Query requests only by an owned signing scope, so the
// form POSTs of native Query services keep reaching their handlers.
func TestQueryRoutingNeverClaimsNativeQueryServices(t *testing.T) {
	h := newHarness(t)
	body := "Action=ListRoles&Version=2010-05-08"
	for _, service := range []string{"iam", "sts", "sns", "elasticloadbalancing", "cloudformation", "ec2", "sqs", "monitoring", "autoscaling", "s3"} {
		for _, header := range []string{"signed", "hinted"} {
			req := httptest.NewRequest("POST", "/", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if header == "signed" {
				sign(req, service, "us-east-1")
			} else {
				req.Header.Set("X-Simulator-Service", service)
			}
			if h.engine.Owns(req) {
				t.Fatalf("the catalog claimed a %s %s Query request", header, service)
			}
		}
	}
	for _, headers := range []map[string]string{
		{},
		{"X-Amz-Target": "AmazonRDS.DescribeDBSubnetGroups"},
		{"X-Simulator-Service": "rds", "Authorization": "AWS4-HMAC-SHA256 Credential=TCSIM/20261106/us-east-1/iam/aws4_request"},
	} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if h.engine.Owns(req) || h.engine.queryBinding(req) != nil {
			t.Fatalf("the catalog claimed %v", headers)
		}
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	sign(req, "rds", "us-east-1")
	if !h.engine.Owns(req) {
		t.Fatal("rds-signed request not owned")
	}
	// Another service's Action under the rds scope is refused by RDS itself.
	expectQuery(t, h.send(req), 400, "InvalidAction")
}

// queryModel exercises every Query serialization trait: nested and flattened
// lists, maps in both forms, renamed elements and every scalar type.
func queryModel() *Model {
	text := &Shape{Type: "string"}
	return &Model{
		Name: "querysynthetic", Source: Source{Package: "botocore", Version: "test", SHA256: "test", License: "Apache-2.0"},
		Metadata: Metadata{Protocol: "query", APIVersion: "2026-01-01", SigningName: "querysynthetic", XMLNamespace: map[string]any{"uri": "urn:query"}},
		Routes:   map[string]Route{"Echo": {HTTP: HTTP{Method: "POST", RequestURI: "/"}}},
		Operations: map[string]*Operation{
			"Echo": {Name: "Echo", Input: "EchoInput", Output: "EchoOutput", HTTP: HTTP{Method: "POST", RequestURI: "/"}},
		},
		Shapes: map[string]*Shape{
			"Text":     text,
			"Int":      {Type: "integer"},
			"Dbl":      {Type: "double"},
			"Flag":     {Type: "boolean"},
			"Time":     {Type: "timestamp"},
			"Data":     {Type: "blob"},
			"Pair":     {Type: "structure", Members: map[string]Member{"Key": {Shape: "Text"}, "Value": {Shape: "Text"}}},
			"Pairs":    {Type: "list", Member: &Member{Shape: "Pair", LocationName: "Pair"}},
			"Plain":    {Type: "list", Member: &Member{Shape: "Text"}},
			"Flat":     {Type: "list", Member: &Member{Shape: "Text", LocationName: "Item"}},
			"FlatBare": {Type: "list", Flattened: true, Member: &Member{Shape: "Text"}},
			"Grid":     {Type: "list", Member: &Member{Shape: "Plain"}},
			"Dict":     {Type: "map", Key: &Member{Shape: "Text", LocationName: "Name"}, Value: &Member{Shape: "Text", LocationName: "Val"}},
			"FlatDict": {Type: "map", Flattened: true, Key: &Member{Shape: "Text"}, Value: &Member{Shape: "Pair"}},
			"Inner":    {Type: "structure", LocationName: "In", Members: map[string]Member{"X": {Shape: "Text"}}},
			"Node":     {Type: "structure", Members: map[string]Member{"Child": {Shape: "Node"}, "Name": {Shape: "Text"}}},
			"EchoInput": {Type: "structure", Members: map[string]Member{
				"Name": {Shape: "Text"}, "Count": {Shape: "Int"}, "Ratio": {Shape: "Dbl"}, "On": {Shape: "Flag"}, "At": {Shape: "Time"}, "Data": {Shape: "Data"},
				"Pairs": {Shape: "Pairs"}, "Plain": {Shape: "Plain"}, "Flat": {Shape: "Flat", Flattened: true}, "FlatBare": {Shape: "FlatBare"}, "Grid": {Shape: "Grid"},
				"Dict": {Shape: "Dict"}, "FlatDict": {Shape: "FlatDict"}, "Inner": {Shape: "Inner"}, "Renamed": {Shape: "Text", LocationName: "Alias"}, "Node": {Shape: "Node"}}},
		},
	}
}

func TestQueryInputFollowsBotocoreSerialization(t *testing.T) {
	m := queryModel()
	m.Shapes["EchoOutput"] = m.Shapes["EchoInput"]
	op := m.Operations["Echo"]
	form := url.Values{
		"Name": {"n"}, "Count": {"7"}, "Ratio": {"1.5"}, "On": {"true"}, "At": {"2026-11-20T01:02:03Z"}, "Data": {"aGk="},
		"Pairs.Pair.1.Key": {"k1"}, "Pairs.Pair.1.Value": {"v1"}, "Pairs.Pair.2.Key": {"k2"},
		"Plain": {""}, "Item.1": {"a"}, "Item.2": {"b"}, "FlatBare.1": {"c"},
		"Grid.member.1.member.1": {"g"}, "Dict.entry.1.Name": {"dk"}, "Dict.entry.1.Val": {"dv"},
		"FlatDict.1.key": {"fk"}, "FlatDict.1.value.Key": {"pk"}, "In.X": {"x"}, "Alias": {"r"}, "Node.Child.Name": {"leaf"},
	}
	raw, err := queryInput(m, op, form)
	if err != nil {
		t.Fatal(err)
	}
	d := &decoder{model: m}
	decoded, err := d.decode("EchoInput", raw, "EchoInput", 0)
	if err != nil {
		t.Fatal(err)
	}
	in := decoded.(map[string]any)
	pairs := in["Pairs"].([]any)
	if in["Count"] != int64(7) || in["Ratio"] != 1.5 || in["On"] != true || string(in["Data"].([]byte)) != "hi" || len(pairs) != 2 || pairs[1].(map[string]any)["Key"] != "k2" {
		t.Fatalf("decoded %v", in)
	}
	if len(in["Plain"].([]any)) != 0 || fmt.Sprint(in["Flat"]) != "[a b]" || fmt.Sprint(in["FlatBare"]) != "[c]" || fmt.Sprint(in["Grid"]) != "[[g]]" {
		t.Fatalf("lists %v", in)
	}
	if in["Dict"].(map[string]any)["dk"] != "dv" || in["FlatDict"].(map[string]any)["fk"].(map[string]any)["Key"] != "pk" || in["Inner"].(map[string]any)["X"] != "x" || in["Renamed"] != "r" {
		t.Fatalf("maps and names %v", in)
	}
	if in["Node"].(map[string]any)["Child"].(map[string]any)["Name"] != "leaf" {
		t.Fatalf("recursive %v", in)
	}
	for name, bad := range map[string]url.Values{
		"unknown":          {"Nope": {"1"}},
		"member name":      {"Flat": {"x"}},
		"repeated":         {"Name": {"a", "b"}},
		"empty segment":    {"Pairs..Key": {"x"}},
		"zero index":       {"Pairs.Pair.0.Key": {"x"}},
		"gap":              {"Pairs.Pair.2.Key": {"x"}},
		"leading zero":     {"Pairs.Pair.01.Key": {"x"}},
		"word index":       {"Pairs.Pair.one.Key": {"x"}},
		"wrong item":       {"Pairs.member.1.Key": {"x"}},
		"list value":       {"Pairs": {"x"}},
		"list container":   {"Pairs.Pair": {"x"}},
		"item value":       {"Pairs.Pair.1": {"x"}},
		"item unknown":     {"Pairs.Pair.1.Other": {"x"}},
		"structure value":  {"In": {"x"}},
		"scalar children":  {"Name.Sub": {"x"}},
		"map value":        {"Dict": {"x"}},
		"entry value":      {"Dict.entry.1": {"x"}},
		"map no key":       {"Dict.entry.1.Val": {"x"}},
		"map no value":     {"Dict.entry.1.Name": {"x"}},
		"map nested key":   {"Dict.entry.1.Name.X": {"x"}, "Dict.entry.1.Val": {"y"}},
		"map empty value":  {"FlatDict.1.key": {"x"}, "FlatDict.1.value.Other": {"y"}},
		"map duplicate":    {"Dict.entry.1.Name": {"x"}, "Dict.entry.1.Val": {"1"}, "Dict.entry.2.Name": {"x"}, "Dict.entry.2.Val": {"2"}},
		"flat map no key":  {"FlatDict.1.value.Key": {"x"}},
		"nested list item": {"Grid.member.1.other": {"x"}},
		"deep":             {"Node." + strings.Repeat("Child.", maxDepth+1) + "Name": {"x"}},
	} {
		if _, err := queryInput(m, op, bad); err == nil {
			t.Fatalf("%s was accepted: %v", name, bad)
		}
	}
	// Map and list containers without their element name consume nothing.
	if raw, err := queryInput(m, op, url.Values{}); err != nil || len(raw) != 0 {
		t.Fatalf("empty input %v %v", raw, err)
	}
	if _, err := queryInput(m, &Operation{Name: "NoInput"}, url.Values{"Name": {"x"}}); err == nil {
		t.Fatal("an operation without input accepted parameters")
	}
}

func TestQueryOutputWritesBotocoreXML(t *testing.T) {
	m := queryModel()
	m.Shapes["EchoOutput"] = m.Shapes["EchoInput"]
	op := m.Operations["Echo"]
	op.ResultWrapper = "EchoResult"
	output := map[string]any{
		"Name": "a&b", "Count": int64(3), "Ratio": 0.25, "On": false, "At": "2026-11-20T01:02:03.000Z", "Data": "aGk=",
		"Pairs": []any{map[string]any{"Key": "k", "Value": "v"}}, "Plain": []any{"p"}, "Flat": []any{"f1", "f2"}, "Grid": []any{[]any{"g"}},
		"Dict": map[string]any{"b": "2", "a": "1"}, "FlatDict": map[string]any{"z": map[string]any{"Key": "zk"}}, "Inner": map[string]any{"X": "x"},
		"Renamed": "r", "Node": map[string]any{"Name": "n"}, "Unknown": oddValue{},
	}
	recorder := httptest.NewRecorder()
	writeOutput(recorder, m, op, output)
	root := parseXML(t, recorder.Body.Bytes())
	result := root.get("EchoResult")
	if root.name != "EchoResponse" || root.attrs["xmlns"] != "urn:query" || result == nil || recorder.Code != 200 {
		t.Fatalf("envelope %s", recorder.Body)
	}
	checks := map[string]string{
		"Name": "a&b", "Count": "3", "Ratio": "0.25", "On": "false", "At": "2026-11-20T01:02:03.000Z", "Data": "aGk=", "Alias": "r", "In/X": "x", "Node/Name": "n",
	}
	for path, want := range checks {
		if got := result.get(strings.Split(path, "/")...); got == nil || got.text != want {
			t.Fatalf("%s = %v, want %s: %s", path, got, want, recorder.Body)
		}
	}
	if items := result.all("Item"); len(items) != 2 || items[1].text != "f2" {
		t.Fatalf("flattened list %s", recorder.Body)
	}
	if result.get("Pairs", "Pair", "Key").text != "k" || result.get("Plain", "member").text != "p" || result.get("Grid", "member", "member").text != "g" {
		t.Fatalf("lists %s", recorder.Body)
	}
	entries := result.get("Dict").all("entry")
	if len(entries) != 2 || entries[0].get("Name").text != "a" || entries[1].get("Val").text != "2" {
		t.Fatalf("map %s", recorder.Body)
	}
	if flat := result.all("FlatDict"); len(flat) != 1 || flat[0].get("key").text != "z" || flat[0].get("value", "Key").text != "zk" {
		t.Fatalf("flattened map %s", recorder.Body)
	}
	// Without a result wrapper the members sit directly in the response.
	op.ResultWrapper = ""
	recorder = httptest.NewRecorder()
	writeOutput(recorder, m, op, map[string]any{"Name": "direct"})
	if root := parseXML(t, recorder.Body.Bytes()); root.get("Name").text != "direct" {
		t.Fatalf("unwrapped %s", recorder.Body)
	}
	if xmlNamespace(&Model{}) != "" {
		t.Fatal("a missing namespace was invented")
	}
}

// oddValue is a value of an unexpected Go type; it is written as text.
type oddValue struct{}

func TestQueryCompositionRefusesUnsupportedTraitsAndSharedScopes(t *testing.T) {
	h := newHarness(t, rds)
	for name, mutate := range map[string]func(*Model){
		"document":  func(m *Model) { m.Shapes["Doc"] = &Shape{Type: "structure", Document: true} },
		"stream":    func(m *Model) { m.Shapes["Data"].Streaming = true },
		"attribute": func(m *Model) { m.Shapes["Pair"].Members["Key"] = Member{Shape: "Text", XMLAttribute: true} },
		"header":    func(m *Model) { m.Shapes["Pair"].Members["Key"] = Member{Shape: "Text", Location: "header"} },
	} {
		m := queryModel()
		m.Shapes["EchoOutput"] = &Shape{Type: "structure"}
		mutate(m)
		if err := queryCompatible(m); err == nil {
			t.Fatalf("%s query model was accepted", name)
		}
	}
	m := queryModel()
	m.Shapes["EchoOutput"] = &Shape{Type: "structure"}
	spec := &Service{Model: "querysynthetic", Title: "Query synthetic", IAMPrefix: "qs", Issues: []int{1}, Bounds: []string{"test"},
		Ops: []Op{{Name: "Echo", Kind: Tag, Lookup: "Name"}}}
	b, err := bindModel(spec, m)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatalf("a compatible query model was refused: %v", err)
	}
	if err := h.engine.add(b); err == nil {
		t.Fatal("two Query services shared a signing name")
	}
	rest := wireModel()
	rest.Metadata.SigningName = "querysynthetic"
	if err := h.engine.add(&binding{spec: spec, model: rest}); err == nil {
		t.Fatal("a REST service shared a Query signing name")
	}
	m.Metadata.SigningName = "rds"
	m.Shapes["Doc"] = &Shape{Type: "structure", Document: true}
	if err := h.engine.add(&binding{spec: spec, model: m}); err == nil {
		t.Fatal("an incompatible Query service was registered")
	}
	m.Metadata.SigningName = "querysynthetic2"
	if err := h.engine.add(&binding{spec: spec, model: m}); err == nil {
		t.Fatal("a document Query model was registered")
	}
}

func TestRDSChecksApplyDocumentedRules(t *testing.T) {
	ids := func(n int) []any {
		out := []any{}
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("subnet-%08x", i+1))
		}
		return out
	}
	cases := []struct {
		name  string
		check func(map[string]any) error
		input map[string]any
		code  string
	}{
		{"subnet group", rdsCreateSubnetGroup, map[string]any{"DBSubnetGroupName": "App", "SubnetIds": ids(2)}, ""},
		{"subnet group default", rdsCreateSubnetGroup, map[string]any{"DBSubnetGroupName": "Default", "SubnetIds": ids(2)}, "validation"},
		{"subnet group digit", rdsCreateSubnetGroup, map[string]any{"DBSubnetGroupName": "1app", "SubnetIds": ids(2)}, "validation"},
		{"subnet group 21 subnets", rdsCreateSubnetGroup, map[string]any{"DBSubnetGroupName": "app", "SubnetIds": ids(21)}, "DBSubnetQuotaExceededFault"},
		{"subnet group one subnet", rdsCreateSubnetGroup, map[string]any{"DBSubnetGroupName": "app", "SubnetIds": ids(1)}, "DBSubnetGroupDoesNotCoverEnoughAZs"},
		{"subnet group duplicate", rdsCreateSubnetGroup, map[string]any{"DBSubnetGroupName": "app", "SubnetIds": []any{"subnet-0a1b2c3d", "subnet-0a1b2c3d"}}, "validation"},
		{"subnet group bad id", rdsCreateSubnetGroup, map[string]any{"DBSubnetGroupName": "app", "SubnetIds": []any{"sn-1", "sn-2"}}, "InvalidSubnet"},
		{"subnet group tag", rdsCreateSubnetGroup, map[string]any{"DBSubnetGroupName": "app", "SubnetIds": ids(2), "Tags": []any{map[string]any{"Key": "AWS:x"}}}, "validation"},
		{"modify default", rdsModifySubnetGroup, map[string]any{"DBSubnetGroupName": "DEFAULT", "SubnetIds": ids(2)}, "validation"},
		{"modify", rdsModifySubnetGroup, map[string]any{"DBSubnetGroupName": "App", "SubnetIds": ids(3)}, ""},
		{"parameter group trailing hyphen", rdsCreateGroup("DBParameterGroupName", true), map[string]any{"DBParameterGroupName": "app-"}, "validation"},
		{"parameter group period", rdsCreateGroup("DBParameterGroupName", true), map[string]any{"DBParameterGroupName": "default.mysql8.0"}, "validation"},
		{"cluster parameter group", rdsCreateGroup("DBClusterParameterGroupName", false), map[string]any{"DBClusterParameterGroupName": "Any.Name"}, ""},
		{"option group engine", rdsCreateOptionGroup, map[string]any{"OptionGroupName": "og", "EngineName": "aurora-mysql"}, "validation"},
		{"option group name", rdsCreateOptionGroup, map[string]any{"OptionGroupName": "og--x", "EngineName": "mysql"}, "validation"},
		{"option group", rdsCreateOptionGroup, map[string]any{"OptionGroupName": "og", "EngineName": "sqlserver-web"}, ""},
		{"major without engine", rdsDescribeOptionGroups, map[string]any{"MajorEngineVersion": "8.0"}, "validation"},
		{"records too many", rdsDescribe("DBSubnetGroupName"), map[string]any{"MaxRecords": int64(101)}, "validation"},
		{"records", rdsDescribe("DBSubnetGroupName"), map[string]any{"MaxRecords": int64(20)}, ""},
		{"tag without key", rdsTags, map[string]any{"Tags": []any{map[string]any{"Value": "v"}}}, "validation"},
		{"tag", rdsTags, map[string]any{"Tags": []any{map[string]any{"Key": "team"}}}, ""},
		{"delete lowercases", rdsLower("DBSubnetGroupName"), map[string]any{"DBSubnetGroupName": "ABC"}, ""},
	}
	for _, c := range cases {
		err := c.check(c.input)
		var f *fault
		switch {
		case c.code == "" && err != nil:
			t.Fatalf("%s refused: %v", c.name, err)
		case c.code != "" && (!errors.As(err, &f) || f.kind != "validation" || c.code != "validation" && f.code != c.code):
			t.Fatalf("%s = %v, want %s", c.name, err, c.code)
		}
	}
	input := map[string]any{"DBSubnetGroupName": "MixedCase", "SubnetIds": ids(2)}
	if err := rdsCreateSubnetGroup(input); err != nil || input["DBSubnetGroupName"] != "mixedcase" || input["SubnetIds"] != nil || len(input["Subnets"].([]any)) != 2 {
		t.Fatalf("normalized %v %v", input, err)
	}
}

func TestQueryReferenceDescribesFormAndXMLWire(t *testing.T) {
	h := newHarness(t, rds)
	document := h.engine.OpenAPI()["rds"]
	post := document["paths"].(map[string]any)["/"].(map[string]any)["post"].(map[string]any)
	content := post["requestBody"].(map[string]any)["content"].(map[string]any)
	if _, ok := content["application/x-www-form-urlencoded"]; !ok || len(content) != 1 {
		t.Fatalf("request media %v", content)
	}
	responses := post["responses"].(map[string]any)
	if _, ok := responses["200"].(map[string]any)["content"].(map[string]any)["text/xml"]; !ok {
		t.Fatalf("response media %v", responses["200"])
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	form := schemas["CreateDBSubnetGroupRequest"].(map[string]any)
	if _, ok := form["patternProperties"].(map[string]any)[`^SubnetIds\.SubnetIdentifier\.[1-9][0-9]*$`]; !ok {
		t.Fatalf("form patterns %v", form["patternProperties"])
	}
	response := schemas["CreateDBSubnetGroupResponse"].(map[string]any)
	if response["xml"].(map[string]any)["namespace"] != "http://rds.amazonaws.com/doc/2014-10-31/" || response["properties"].(map[string]any)["CreateDBSubnetGroupResult"] == nil {
		t.Fatalf("response schema %v", response)
	}
	if _, ok := schemas["DeleteDBSubnetGroupResponse"].(map[string]any)["properties"].(map[string]any)["ResponseMetadata"]; !ok {
		t.Fatal("delete response lacks ResponseMetadata")
	}
	// Maps and unwrapped outputs are described by the same rules.
	m := queryModel()
	m.Shapes["EchoOutput"] = m.Shapes["EchoInput"]
	b := &binding{spec: &Service{Model: "querysynthetic", Title: "Q", Ops: []Op{{Name: "Echo"}}}, model: m}
	schemas = map[string]any{}
	b.queryPath([]Op{{Name: "Echo"}}, schemas, "Q")
	patterns := schemas["EchoRequest"].(map[string]any)["patternProperties"].(map[string]any)
	for _, want := range []string{`^Dict\.entry\.[1-9][0-9]*\.Name$`, `^Dict\.entry\.[1-9][0-9]*\.Val$`, `^FlatDict\.[1-9][0-9]*\.value\.Key$`, `^Item\.[1-9][0-9]*$`, `^Grid\.member\.[1-9][0-9]*\.member\.[1-9][0-9]*$`} {
		if _, ok := patterns[want]; !ok {
			t.Fatalf("missing %s in %v", want, patterns)
		}
	}
	if props := schemas["EchoRequest"].(map[string]any)["properties"].(map[string]any); props["In.X"] == nil || props["Alias"] == nil {
		t.Fatalf("named fields %v", props)
	}
	if schemas["EchoResponse"].(map[string]any)["properties"].(map[string]any)["EchoOutput"] == nil {
		t.Fatal("unwrapped output not described")
	}
}
