package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// Read-only discovery of actual CloudFormation-owned local resources. No VM
// liveness is inferred from a saved plan or a successful stack creation.
func isEC2Read(r *http.Request) bool {
	if s := r.Header.Get("X-Simulator-Service"); s != "" {
		return s == "ec2"
	}
	return elbSigningService(r.Header.Get("Authorization")) == "ec2"
}
func ec2ReadError(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"Response"`
		Code    string   `xml:"Errors>Error>Code"`
		Message string   `xml:"Errors>Error>Message"`
	}{Code: code, Message: message})
}
func (g *Gateway) serveEC2Read(w http.ResponseWriter, r *http.Request) {
	fail := func(code, msg string, status int) { ec2ReadError(w, code, msg, status) }
	for _, h := range []string{"Authorization", "X-Simulator-Service", "X-Simulator-Region", "X-Simulator-Principal", "X-Amz-Security-Token", "X-Amz-Target"} {
		if len(r.Header.Values(h)) > 1 {
			fail("InvalidParameterValue", "Duplicate headers are unsupported.", 400)
			return
		}
	}
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || r.Header.Get("X-Amz-Target") != "" {
		fail("InvalidRequest", "Use POST Query/XML on /.", 400)
		return
	}
	region := r.Header.Get("X-Simulator-Region")
	if r.Header.Get("X-Amz-Security-Token") != "" {
		fail("UnsupportedOperation", "Session credentials are unsupported by local EC2 discovery.", 400)
		return
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		_, scope, ok := strings.Cut(auth, "Credential=")
		credential, _, _ := strings.Cut(scope, ",")
		parts := strings.Split(credential, "/")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || !ok || len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[1] == "" || parts[3] != "ec2" || parts[4] != "aws4_request" {
			fail("UnauthorizedOperation", "Use dummy local EC2 credentials.", 403)
			return
		}
		if region != "" && region != parts[2] {
			fail("InvalidParameterValue", "Region scope mismatch.", 400)
			return
		}
		region = parts[2]
	}
	if region == "" {
		region = "us-east-1"
	}
	if !slices.Contains(Regions, region) {
		fail("InvalidParameterValue", "Unsupported region.", 400)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil {
		fail("InvalidRequest", "Query exceeds local bound or could not be read.", 400)
		return
	}
	fields, err := url.ParseQuery(string(raw))
	if err != nil {
		fail("InvalidParameterValue", "Malformed Query.", 400)
		return
	}
	for key, values := range fields {
		if len(values) != 1 || (key != "Action" && key != "Version") {
			fail("UnsupportedOperation", "Only Action and Version are supported; filters and pagination are not implemented.", 400)
			return
		}
	}
	action := fields.Get("Action")
	if fields.Get("Version") != "2016-11-15" || (action != "DescribeInstances" && action != "DescribeVpcs") {
		fail("UnsupportedOperation", "Only DescribeInstances and DescribeVpcs version 2016-11-15 are supported.", 400)
		return
	}
	if g.Stacks == nil || g.Identities == nil {
		fail("InternalError", "Discovery stores are unavailable.", 500)
		return
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	if principal != core.Administrator {
		policy, found, e := g.Identities.PolicyContext(r.Context(), principal)
		if e != nil {
			fail("InternalError", "Identity state unavailable.", 500)
			return
		}
		if !found || core.Evaluate(policy, "ec2:"+action, "*") != "allowed" {
			fail("UnauthorizedOperation", "EC2 discovery is not authorized.", 403)
			return
		}
	}
	out, err := g.Stacks.ec2Read(r.Context(), region, action)
	if err != nil {
		fail("InternalError", "Local discovery state is inconsistent or exceeds the 1000-resource bound.", 500)
		return
	}
	out.XMLName = xml.Name{Local: action + "Response"}
	out.XMLNS = "http://ec2.amazonaws.com/doc/2016-11-15/"
	out.RequestID = fmt.Sprintf("ec2-local-%016x", sequence.Add(1))
	w.Header().Set("Content-Type", "text/xml")
	_ = xml.NewEncoder(w).Encode(out)
}

type ec2Tag struct {
	Key   string `xml:"key"`
	Value string `xml:"value"`
}
type ec2Group struct {
	ID string `xml:"groupId"`
}
type ec2InstanceRead struct {
	ID      string     `xml:"instanceId"`
	Image   string     `xml:"imageId"`
	Type    string     `xml:"instanceType"`
	Subnet  string     `xml:"subnetId"`
	VPC     string     `xml:"vpcId"`
	Runtime string     `xml:"simulatorRuntimeState"`
	Groups  []ec2Group `xml:"groupSet>item"`
	Tags    []ec2Tag   `xml:"tagSet>item"`
}
type ec2ReservationRead struct {
	Owner     string            `xml:"ownerId"`
	Instances []ec2InstanceRead `xml:"instancesSet>item"`
}
type ec2VPCRead struct {
	ID   string   `xml:"vpcId"`
	CIDR string   `xml:"cidrBlock"`
	Tags []ec2Tag `xml:"tagSet>item"`
}
type ec2ReadResponse struct {
	XMLName      xml.Name
	XMLNS        string               `xml:"xmlns,attr"`
	RequestID    string               `xml:"requestId"`
	Reservations []ec2ReservationRead `xml:"reservationSet>item,omitempty"`
	VPCs         []ec2VPCRead         `xml:"vpcSet>item,omitempty"`
}

func ec2ReadTags(tags map[string]string) []ec2Tag {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []ec2Tag{}
	for _, k := range keys {
		out = append(out, ec2Tag{k, tags[k]})
	}
	return out
}
func (s *StackStore) ec2Read(ctx context.Context, region, action string) (ec2ReadResponse, error) {
	out := ec2ReadResponse{}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	kind := "AWS::EC2::VPC"
	if action == "DescribeInstances" {
		kind = "AWS::EC2::Instance"
	}
	rows, e := tx.QueryContext(ctx, `SELECT o.physical_id,o.stack_id,s.document FROM aws_cfn_ownership o JOIN aws_cfn_stacks s ON s.id=o.stack_id WHERE o.namespace=? AND o.kind=? AND s.region=? ORDER BY o.physical_id LIMIT 1001`, stackResourceNamespace(region, kind), kind, region)
	if e != nil {
		return out, e
	}
	type owned struct{ id, stack, document string }
	var all []owned
	for rows.Next() {
		var item owned
		if e = rows.Scan(&item.id, &item.stack, &item.document); e != nil {
			rows.Close()
			return out, e
		}
		all = append(all, item)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(all) > 1000 {
		return out, fmt.Errorf("resource bound")
	}
	for _, item := range all {
		var record StackRecord
		if json.Unmarshal([]byte(item.document), &record) != nil || record.ID != item.stack || record.Region != region {
			return out, fmt.Errorf("stack identity")
		}
		var resource *StackResource
		for i := range record.Resources {
			r := &record.Resources[i]
			if r.Type == kind && r.PhysicalID == item.id {
				if resource != nil {
					return out, fmt.Errorf("duplicate resource")
				}
				resource = r
			}
		}
		if resource == nil {
			return out, fmt.Errorf("missing resource")
		}
		if kind == "AWS::EC2::VPC" {
			v, e := networkRead(ctx, tx, region, item.id)
			if e != nil || v.StackID != item.stack || v.Kind != kind {
				return out, fmt.Errorf("network identity")
			}
			out.VPCs = append(out.VPCs, ec2VPCRead{v.ID, v.CIDR, ec2ReadTags(v.Tags)})
			continue
		}
		props := resource.Properties
		subnet, e := networkRead(ctx, tx, region, stringValue(props["SubnetId"]))
		if e != nil || subnet.StackID != item.stack || subnet.Kind != "AWS::EC2::Subnet" {
			return out, fmt.Errorf("subnet identity")
		}
		tags, e := networkTags(props["Tags"])
		if e != nil {
			return out, e
		}
		instance := ec2InstanceRead{ID: item.id, Image: stringValue(props["ImageId"]), Type: stringValue(props["InstanceType"]), Subnet: subnet.ID, VPC: subnet.VPCID, Runtime: "unknown", Tags: ec2ReadTags(tags)}
		groups, ok := props["SecurityGroupIds"].([]any)
		if !ok {
			return out, fmt.Errorf("groups")
		}
		for _, id := range groups {
			group, e := networkRead(ctx, tx, region, stringValue(id))
			if e != nil || group.Kind != "AWS::EC2::SecurityGroup" || group.StackID != item.stack || group.VPCID != subnet.VPCID {
				return out, fmt.Errorf("group identity")
			}
			instance.Groups = append(instance.Groups, ec2Group{group.ID})
		}
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='aws_ec2_instance_runtime'`).Scan(&count); e != nil {
			return out, e
		}
		if count == 1 {
			var state string
			e = tx.QueryRowContext(ctx, `SELECT state FROM aws_ec2_instance_runtime WHERE region=? AND stack_id=? AND instance=?`, region, item.stack, item.id).Scan(&state)
			if e != nil && e != sql.ErrNoRows {
				return out, e
			}
			if e == nil {
				switch state {
				case "ready", "launch_pending", "seal_pending":
					instance.Runtime = state
				default:
					instance.Runtime = "unknown"
				}
			}
		}
		out.Reservations = append(out.Reservations, ec2ReservationRead{Owner: "000000000000", Instances: []ec2InstanceRead{instance}})
	}
	return out, tx.Commit()
}
