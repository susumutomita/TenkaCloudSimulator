package aws

import (
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"math"
)

const (
	ec2DescribeVPCAttribute = "DescribeVpcAttribute"
	ec2ModifyVPCAttribute   = "ModifyVpcAttribute"
	vpcDNSSupport           = "enableDnsSupport"
	vpcDNSHostnames         = "enableDnsHostnames"
)

type ec2VPCAttributeQuery struct {
	name          string
	value, dryRun bool
}

func parseEC2VPCAttribute(action string, fields map[string][]string) (ec2Request, *ec2Failure) {
	q := ec2Request{}
	for key, vs := range fields {
		if len(vs) != 1 {
			return q, ec2Invalid("InvalidParameterValue", "Parameter "+key+" must be sent once.")
		}
		v := vs[0]
		switch {
		case key == "Action" || key == "Version":
		case key == "VpcId":
			q.vpcID = v
		case action == ec2DescribeVPCAttribute && key == "Attribute":
			if v != vpcDNSSupport && v != vpcDNSHostnames {
				return q, ec2Invalid("UnsupportedOperation", "Only enableDnsSupport and enableDnsHostnames are supported locally.")
			}
			q.vpcAttribute.name = v
		case action == ec2DescribeVPCAttribute && key == "DryRun":
			if v != "true" && v != "false" {
				return q, ec2Invalid("InvalidParameterValue", "DryRun must be true or false.")
			}
			q.vpcAttribute.dryRun = v == "true"
		case action == ec2ModifyVPCAttribute && (key == "EnableDnsSupport.Value" || key == "EnableDnsHostnames.Value"):
			if q.vpcAttribute.name != "" {
				return q, ec2Invalid("InvalidParameterCombination", "Change only one DNS attribute per request.")
			}
			if v != "true" && v != "false" {
				return q, ec2Invalid("InvalidParameterValue", "The DNS attribute value must be true or false.")
			}
			q.vpcAttribute.name, q.vpcAttribute.value = vpcDNSSupport, v == "true"
			if key == "EnableDnsHostnames.Value" {
				q.vpcAttribute.name = vpcDNSHostnames
			}
		default:
			return q, ec2Invalid("UnsupportedOperation", "That parameter is not supported for local "+action+".")
		}
	}
	if q.vpcID == "" || q.vpcAttribute.name == "" {
		return q, ec2Invalid("MissingParameter", "A VPC ID and one DNS attribute are required.")
	}
	if !ec2VPCIDPattern.MatchString(q.vpcID) {
		return q, ec2Invalid("InvalidVpcID.Malformed", "Malformed VPC ID.")
	}
	return q, nil
}

type ec2VPCBoolean struct {
	Value bool `xml:"value"`
}

type ec2VPCAttributeResponse struct {
	XMLName   xml.Name
	XMLNS     string         `xml:"xmlns,attr"`
	RequestID string         `xml:"requestId"`
	VpcID     string         `xml:"vpcId,omitempty"`
	Support   *ec2VPCBoolean `xml:"enableDnsSupport,omitempty"`
	Hostnames *ec2VPCBoolean `xml:"enableDnsHostnames,omitempty"`
	Return    bool           `xml:"return,omitempty"`
}

// DNS edits and the optional offline legacy main-route repair advance the
// same optimistic row revision. Track exactly how many edits were DNS writes;
// odd generations without a matching history must not silently become valid.
func validNativeDNSCounter(r EC2NetworkResource, routeGeneration int64) bool {
	return r.NativeDNSUpdates >= 0 && r.Revision >= routeGeneration && r.Revision-routeGeneration == r.NativeDNSUpdates
}
func validNativeDNSGeneration(r EC2NetworkResource) bool {
	return r.Kind == ec2RTVPC && r.StackID == ec2NativeOwner &&
		(r.MainRouteRepair == nil && validNativeDNSCounter(r, 1) || validNativeVPCMainRepair(r))
}

var errVPCDNSState = errors.New("inconsistent local VPC DNS ownership or generation")

func readDNSVPC(ctx context.Context, tx *sql.Tx, region, id string, write bool) (EC2NetworkResource, error) {
	v, err := networkRead(ctx, tx, region, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && v.Kind != ec2RTVPC {
		return v, ec2Invalid("InvalidVpcID.NotFound", "The VPC does not exist in this region.")
	}
	if err != nil {
		return v, err
	}
	stacks, declared, err := regionalGatewayStacks(ctx, tx, region)
	if err != nil {
		return v, err
	}
	if v.StackID == ec2NativeOwner {
		if !validNativeDNSGeneration(v) || declared[id] {
			return v, errVPCDNSState
		}
		// Also checks the namespace-wide claims index, CIDR and the main
		// route pairing; no adoption even with only one CFN witness left.
		return nativeRouteTableVPC(ctx, tx, region, id, &declared)
	}
	// Stack VPCs can be read, never modified. Verify a single claim and
	// declaration, in the same snapshot as the resource.
	if !declared[id] {
		return v, errVPCDNSState
	}
	count := 0
	for owner, st := range stacks {
		for _, res := range st.Resources {
			if stackNetworkKind(res.Type) && res.PhysicalID == id {
				if owner != v.StackID || res.Type != ec2RTVPC {
					return v, errVPCDNSState
				}
				count++
			}
		}
	}
	var claims, ours int
	err = tx.QueryRowContext(ctx, `SELECT count(*),count(CASE WHEN kind=? AND stack_id=? THEN 1 END) FROM aws_cfn_ownership
		WHERE namespace=? AND physical_id=?`, ec2RTVPC, v.StackID, stackResourceNamespace(region, ec2RTVPC), id).Scan(&claims, &ours)
	if err != nil {
		return v, err
	}
	if count != 1 || claims != 1 || ours != 1 {
		return v, errVPCDNSState
	}
	if write {
		return v, ec2Invalid("UnsupportedOperation", "CloudFormation owns this VPC. Native DNS settings do not change its stack.")
	}
	return v, nil
}

func (s *StackStore) vpcAttribute(ctx context.Context, action, region, id string, q ec2VPCAttributeQuery) (*ec2VPCAttributeResponse, error) {
	write := action == ec2ModifyVPCAttribute
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	v, err := readDNSVPC(ctx, tx, region, id, write)
	if err != nil {
		return nil, err
	}
	out := &ec2VPCAttributeResponse{XMLName: xml.Name{Local: action + "Response"}}
	current := &v.DNSSupport
	if q.name == vpcDNSHostnames {
		current = &v.DNSHostnames
	}
	if !write {
		out.VpcID = id
		setting := &ec2VPCBoolean{Value: *current}
		if q.name == vpcDNSHostnames {
			out.Hostnames = setting
		} else {
			out.Support = setting
		}
	} else {
		if q.name == vpcDNSHostnames && q.value && !v.DNSSupport {
			return nil, ec2Invalid("InvalidParameterCombination", "Enable DNS support before enabling DNS hostnames.")
		}
		if *current != q.value {
			if v.Revision == math.MaxInt64 {
				return nil, errVPCDNSState
			}
			// Avoid remarshal of ambiguous or unknown fields: only mutate
			// documents emitted by supported writers.
			if err := exactNativeRepairDocument(ctx, tx, v); err != nil {
				return nil, err
			}
			*current = q.value
			v.NativeDNSUpdates++
			v.Revision++
			if err := networkWrite(ctx, tx, v, false); err != nil {
				return nil, err
			}
			if _, err := readDNSVPC(ctx, tx, region, id, true); err != nil {
				return nil, err
			}
		}
		out.Return = true
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
