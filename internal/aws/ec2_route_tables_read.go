package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"maps"
	"sort"
)

const (
	ec2RT           = "AWS::EC2::RouteTable"
	ec2Route        = "AWS::EC2::Route"
	ec2Assoc        = "AWS::EC2::SubnetRouteTableAssociation"
	ec2RTVPC        = "AWS::EC2::VPC"
	ec2RTSubnet     = "AWS::EC2::Subnet"
	ec2RTGateway    = "AWS::EC2::InternetGateway"
	ec2RTAttachment = "AWS::EC2::VPCGatewayAttachment"
)

type ec2DescribeRouteTablesResponse struct {
	XMLName   xml.Name            `xml:"DescribeRouteTablesResponse"`
	XMLNS     string              `xml:"xmlns,attr"`
	RequestID string              `xml:"requestId"`
	Tables    []ec2RouteTableRead `xml:"routeTableSet>item"`
}
type ec2RouteTableRead struct {
	ID           string                    `xml:"routeTableId"`
	VPC          string                    `xml:"vpcId"`
	Owner        string                    `xml:"ownerId"`
	Routes       []ec2RouteRead            `xml:"routeSet>item"`
	Associations []ec2RouteAssociationRead `xml:"associationSet>item"`
	Tags         []ec2Tag                  `xml:"tagSet>item"`
}
type ec2RouteRead struct {
	CIDR    string `xml:"destinationCidrBlock"`
	Gateway string `xml:"gatewayId"`
	Origin  string `xml:"origin"`
	State   string `xml:"state"`
}
type ec2RouteAssociationRead struct {
	ID     string `xml:"routeTableAssociationId"`
	Table  string `xml:"routeTableId"`
	Subnet string `xml:"subnetId"`
	Main   bool   `xml:"main"`
}

var errEC2RouteSnapshot = errors.New("inconsistent EC2 route table snapshot")

func ec2RouteMember(kind string) bool {
	switch kind {
	case ec2RT, ec2Route, ec2Assoc, ec2RTVPC, ec2RTSubnet, ec2RTGateway, ec2RTAttachment:
		return true
	}
	return false
}

// Query both sources so losing a network row or an ownership row cannot turn
// a damaged table or child into a successful empty response. Each stack is
// projected once, from the same SQLite snapshot. Only the exact native table
// kind is handled separately; native route or association rows remain invalid.
func (s *StackStore) describeEC2RouteTables(ctx context.Context, region string) ([]ec2RouteTableRead, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT stack_id FROM aws_cfn_ownership WHERE namespace=? AND kind IN (?,?,?)
		UNION SELECT stack_id FROM aws_ec2_network_resources WHERE region=? AND kind IN (?,?,?)
			AND NOT (stack_id=? AND kind=?) ORDER BY 1 LIMIT 1001`,
		stackResourceNamespace(region, ec2RT), ec2RT, ec2Route, ec2Assoc, region, ec2RT, ec2Route, ec2Assoc, ec2NativeOwner, ec2RT)
	if err != nil {
		return nil, err
	}
	var owners []string
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			rows.Close()
			return nil, err
		}
		owners = append(owners, owner)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(owners) > 1000 {
		return nil, errEC2RouteSnapshot
	}
	// The normal network decoder cannot distinguish missing/null RouteEnabled
	// from an intentional false. Reject malformed raw witnesses only for this
	// read; all other network consumers keep their existing contract.
	var badRouteFlag bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (
        SELECT 1 FROM aws_ec2_network_resources AS route
        WHERE region=? AND kind=? AND (
            COALESCE(json_type(route.document,'$.RouteEnabled'),'') NOT IN ('true','false')
            OR (SELECT count(*) FROM json_each(route.document)
                WHERE lower(key)='routeenabled') <> 1))`, region, ec2Route).Scan(&badRouteFlag)
	if err != nil {
		return nil, err
	}
	if badRouteFlag {
		return nil, errEC2RouteSnapshot
	}
	var out []ec2RouteTableRead
	count := 0
	for _, owner := range owners {
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT document FROM aws_cfn_stacks WHERE id=? AND region=?`, owner, region).Scan(&raw); err != nil {
			return nil, err
		}
		var stack StackRecord
		if err := json.Unmarshal([]byte(raw), &stack); err != nil || stack.ID != owner || stack.Region != region {
			return nil, errEC2RouteSnapshot
		}
		net, err := networkList(ctx, tx, region, owner)
		if err != nil {
			return nil, err
		}
		count += len(net)
		if count > 1000 {
			return nil, errEC2RouteSnapshot
		}
		claims, err := ec2RouteClaims(ctx, tx, region, owner)
		if err != nil {
			return nil, err
		}
		tables, err := projectEC2RouteTables(stack, net, claims)
		if err != nil {
			return nil, err
		}
		out = append(out, tables...)
	}
	native, err := listNativeRouteTables(ctx, tx, region)
	if err != nil {
		return nil, err
	}
	if count+len(native) > ec2NativeRouteTableRowLimit {
		return nil, errEC2RouteSnapshot
	}
	for _, item := range native {
		out = append(out, ec2NativeRouteTableRecord(item))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, tx.Commit()
}

func ec2RouteClaims(ctx context.Context, tx *sql.Tx, region, owner string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT physical_id,kind FROM aws_cfn_ownership WHERE namespace=? AND stack_id=? LIMIT 1001`, stackResourceNamespace(region, ec2RT), owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	claims := map[string]string{}
	n := 0
	for rows.Next() {
		var id, kind string
		if err := rows.Scan(&id, &kind); err != nil {
			return nil, err
		}
		n++
		if n > 1000 {
			return nil, errEC2RouteSnapshot
		}
		if ec2RouteMember(kind) {
			if _, ok := claims[id]; ok {
				return nil, errEC2RouteSnapshot
			}
			claims[id] = kind
		}
	}
	return claims, rows.Err()
}

// A route, its gateway/attachment, each explicit association, the subnet and
// its table must agree across the declaration, typed owner and stored rows.
// Deliberately do not manufacture a VPC main table or implicit/local route.
func projectEC2RouteTables(stack StackRecord, resources []EC2NetworkResource, claims map[string]string) ([]ec2RouteTableRead, error) {
	byID := map[string]EC2NetworkResource{}
	for _, r := range resources {
		if !ec2RouteMember(r.Kind) {
			continue
		}
		if claims[r.ID] != r.Kind || r.Region != stack.Region || r.StackID != stack.ID || r.Revision < 1 {
			return nil, errEC2RouteSnapshot
		}
		if _, ok := byID[r.ID]; ok {
			return nil, errEC2RouteSnapshot
		}
		byID[r.ID] = r
	}
	if len(byID) != len(claims) {
		return nil, errEC2RouteSnapshot
	}
	seen := map[string]bool{}
	tables := map[string]*ec2RouteTableRead{}
	attachments := map[string]string{}
	vpcGateway := map[string]bool{}
	for _, member := range stack.Resources {
		if !ec2RouteMember(member.Type) {
			continue
		}
		r, ok := byID[member.PhysicalID]
		if !ok || seen[r.ID] || r.Kind != member.Type {
			return nil, errEC2RouteSnapshot
		}
		seen[r.ID] = true
		want, err := compileNetworkResource(stack.Region, stack.ID, member.LogicalID, member.Type, member.Properties)
		if err != nil || want.ID != r.ID || want.RouteTableID != r.RouteTableID || want.SubnetID != r.SubnetID || want.GatewayID != r.GatewayID || want.CIDR != r.CIDR {
			return nil, errEC2RouteSnapshot
		}
		if r.Kind == ec2Route || r.Kind == ec2Assoc {
			table := byID[r.RouteTableID]
			if table.Kind != ec2RT {
				return nil, errEC2RouteSnapshot
			}
			want.VPCID = table.VPCID
		}
		if r.VPCID != want.VPCID || (r.VPCID != "" && byID[r.VPCID].Kind != ec2RTVPC) {
			return nil, errEC2RouteSnapshot
		}
		switch r.Kind {
		case ec2RT:
			if !maps.Equal(r.Tags, want.Tags) {
				return nil, errEC2RouteSnapshot
			}
			for k, v := range r.Tags {
				if !ec2SecurityGroupXMLText(k) || !ec2SecurityGroupXMLText(v) {
					return nil, errEC2RouteSnapshot
				}
			}
			tables[r.ID] = &ec2RouteTableRead{ID: r.ID, VPC: r.VPCID, Owner: "000000000000", Tags: ec2ReadTags(r.Tags)}
		case ec2RTAttachment:
			if byID[r.GatewayID].Kind != ec2RTGateway || attachments[r.GatewayID] != "" || vpcGateway[r.VPCID] {
				return nil, errEC2RouteSnapshot
			}
			attachments[r.GatewayID] = r.VPCID
			vpcGateway[r.VPCID] = true
		}
	}
	if len(seen) != len(byID) {
		return nil, errEC2RouteSnapshot
	}
	associated := map[string]bool{}
	defaultRoute := map[string]bool{}
	for _, r := range resources { // networkList orders by physical ID.
		switch r.Kind {
		case ec2Route:
			t := tables[r.RouteTableID]
			if t == nil || byID[r.GatewayID].Kind != ec2RTGateway || attachments[r.GatewayID] != t.VPC || defaultRoute[t.ID] || r.CIDR != "0.0.0.0/0" {
				return nil, errEC2RouteSnapshot
			}
			defaultRoute[t.ID] = true
			if r.RouteEnabled {
				t.Routes = append(t.Routes, ec2RouteRead{CIDR: r.CIDR, Gateway: r.GatewayID, Origin: "CreateRoute", State: "active"})
			}
		case ec2Assoc:
			t := tables[r.RouteTableID]
			subnet := byID[r.SubnetID]
			if t == nil || subnet.Kind != ec2RTSubnet || subnet.VPCID != t.VPC || associated[r.SubnetID] {
				return nil, errEC2RouteSnapshot
			}
			associated[r.SubnetID] = true
			t.Associations = append(t.Associations, ec2RouteAssociationRead{ID: r.ID, Table: t.ID, Subnet: subnet.ID})
		}
	}
	out := make([]ec2RouteTableRead, 0, len(tables))
	for _, table := range tables {
		out = append(out, *table)
	}
	return out, nil
}
