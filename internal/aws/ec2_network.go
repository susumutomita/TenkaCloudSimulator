package aws

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
)

// EC2NetworkStore contains real local route/SG policy state. It neither opens
// host sockets nor claims a VM was launched. Trusted NIC provisioning must use
// a fresh snapshot/seal and enforce its rules on the actual packet path.
type EC2NetworkStore struct{ db *sql.DB }
type EC2IngressRule struct {
	Protocol         string
	FromPort, ToPort int
	CIDR             string
}
type EC2NetworkResource struct {
	ID, Kind, Region, StackID                        string
	VPCID, SubnetID, GatewayID, RouteTableID         string
	CIDR, AvailabilityZone, Description              string
	DNSHostnames, DNSSupport, PublicIP, RouteEnabled bool
	Ingress                                          []EC2IngressRule
	Tags                                             map[string]string
	Revision                                         int64
}

func NewEC2NetworkStore(db *sql.DB) (*EC2NetworkStore, error) {
	_, e := db.Exec(`CREATE TABLE IF NOT EXISTS aws_ec2_network_resources(region TEXT NOT NULL,id TEXT NOT NULL,stack_id TEXT NOT NULL,kind TEXT NOT NULL,document TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),PRIMARY KEY(region,id));`)
	if e != nil {
		return nil, e
	}
	return &EC2NetworkStore{db}, nil
}
func networkRead(ctx context.Context, tx *sql.Tx, region, id string) (EC2NetworkResource, error) {
	var raw string
	var r EC2NetworkResource
	var kind, owner string
	var revision int64
	e := tx.QueryRowContext(ctx, `SELECT document,kind,stack_id,revision FROM aws_ec2_network_resources WHERE region=? AND id=?`, region, id).Scan(&raw, &kind, &owner, &revision)
	if e != nil {
		return r, e
	}
	if json.Unmarshal([]byte(raw), &r) != nil || r.ID != id || r.Region != region || r.Kind != kind || r.StackID != owner || r.Revision != revision {
		return r, fmt.Errorf("network state identity invariant failed")
	}
	return r, nil
}
func networkWrite(ctx context.Context, tx *sql.Tx, r EC2NetworkResource, create bool) error {
	raw, e := json.Marshal(r)
	if e != nil {
		return e
	}
	var result sql.Result
	if create {
		result, e = tx.ExecContext(ctx, `INSERT INTO aws_ec2_network_resources VALUES(?,?,?,?,?,?)`, r.Region, r.ID, r.StackID, r.Kind, string(raw), r.Revision)
	} else {
		result, e = tx.ExecContext(ctx, `UPDATE aws_ec2_network_resources SET document=?,revision=? WHERE region=? AND id=? AND stack_id=? AND revision=?`, string(raw), r.Revision, r.Region, r.ID, r.StackID, r.Revision-1)
	}
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return fmt.Errorf("network write did not persist exactly one resource")
	}
	stored, e := networkRead(ctx, tx, r.Region, r.ID)
	if e != nil {
		return e
	}
	actual, _ := json.Marshal(stored)
	if string(actual) != string(raw) {
		return fmt.Errorf("network write readback differs")
	}
	return nil
}
func networkParent(ctx context.Context, tx *sql.Tx, r EC2NetworkResource, id, kind string) (EC2NetworkResource, error) {
	parent, e := networkRead(ctx, tx, r.Region, id)
	if e != nil {
		return parent, e
	}
	if parent.StackID != r.StackID || parent.Kind != kind {
		return parent, fmt.Errorf("network parent belongs to another owner or type")
	}
	return parent, nil
}
func createEC2Network(ctx context.Context, tx *sql.Tx, r EC2NetworkResource) error {
	var count int
	if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_network_resources`).Scan(&count); e != nil {
		return e
	}
	if count >= 1000 {
		return fmt.Errorf("local network resource quota exceeded")
	}
	switch r.Kind {
	case "AWS::EC2::Subnet", "AWS::EC2::RouteTable", "AWS::EC2::SecurityGroup":
		vpc, e := networkParent(ctx, tx, r, r.VPCID, "AWS::EC2::VPC")
		if e != nil {
			return e
		}
		if r.Kind == "AWS::EC2::Subnet" {
			v, _ := netip.ParsePrefix(vpc.CIDR)
			sub, _ := netip.ParsePrefix(r.CIDR)
			if !v.Contains(sub.Addr()) || sub.Bits() < v.Bits() {
				return fmt.Errorf("subnet lies outside VPC")
			}
		}
	case "AWS::EC2::VPCGatewayAttachment":
		if _, e := networkParent(ctx, tx, r, r.VPCID, "AWS::EC2::VPC"); e != nil {
			return e
		}
		if _, e := networkParent(ctx, tx, r, r.GatewayID, "AWS::EC2::InternetGateway"); e != nil {
			return e
		}
	case "AWS::EC2::Route":
		rt, e := networkParent(ctx, tx, r, r.RouteTableID, "AWS::EC2::RouteTable")
		if e != nil {
			return e
		}
		r.VPCID = rt.VPCID
		if _, e = networkParent(ctx, tx, r, r.GatewayID, "AWS::EC2::InternetGateway"); e != nil {
			return e
		}
		all, e := networkList(ctx, tx, r.Region, r.StackID)
		if e != nil {
			return e
		}
		attached := false
		for _, v := range all {
			if v.Kind == "AWS::EC2::VPCGatewayAttachment" && v.VPCID == r.VPCID && v.GatewayID == r.GatewayID {
				attached = true
			}
		}
		if !attached {
			return fmt.Errorf("route requires attached gateway")
		}
	case "AWS::EC2::SubnetRouteTableAssociation":
		subnet, e := networkParent(ctx, tx, r, r.SubnetID, "AWS::EC2::Subnet")
		if e != nil {
			return e
		}
		rt, e := networkParent(ctx, tx, r, r.RouteTableID, "AWS::EC2::RouteTable")
		if e != nil {
			return e
		}
		if subnet.VPCID != rt.VPCID {
			return fmt.Errorf("route table and subnet VPC differ")
		}
		r.VPCID = subnet.VPCID
	}
	existing, e := networkList(ctx, tx, r.Region, r.StackID)
	if e != nil {
		return e
	}
	for _, v := range existing {
		if r.Kind == "AWS::EC2::VPCGatewayAttachment" && v.Kind == r.Kind && (v.VPCID == r.VPCID || v.GatewayID == r.GatewayID) {
			return fmt.Errorf("gateway or VPC already attached")
		}
		if r.Kind == "AWS::EC2::SubnetRouteTableAssociation" && v.Kind == r.Kind && v.SubnetID == r.SubnetID {
			return fmt.Errorf("subnet already associated")
		}
		if r.Kind == "AWS::EC2::Route" && v.Kind == r.Kind && v.RouteTableID == r.RouteTableID && v.CIDR == r.CIDR {
			return fmt.Errorf("route destination already exists")
		}
		if r.Kind == "AWS::EC2::Subnet" && v.Kind == r.Kind && v.VPCID == r.VPCID {
			a, _ := netip.ParsePrefix(r.CIDR)
			b, _ := netip.ParsePrefix(v.CIDR)
			if a.Overlaps(b) {
				return fmt.Errorf("subnet CIDR overlaps")
			}
		}
	}
	r.Revision = 1
	return networkWrite(ctx, tx, r, true)
}
func networkList(ctx context.Context, tx *sql.Tx, region, owner string) ([]EC2NetworkResource, error) {
	rows, e := tx.QueryContext(ctx, `SELECT id FROM aws_ec2_network_resources WHERE region=? AND stack_id=? ORDER BY id`, region, owner)
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
		if len(ids) > 1000 {
			rows.Close()
			return nil, fmt.Errorf("network quota invariant")
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []EC2NetworkResource{}
	for _, id := range ids {
		r, e := networkRead(ctx, tx, region, id)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}

type EC2NetworkSnapshot struct {
	Region, StackID, SubnetID, VPCCIDR, SubnetCIDR, GatewayIPv4, Seal string
	PublicRoute                                                       bool
	SecurityGroupIDs                                                  []string
	Ingress                                                           []EC2IngressRule
}

// AllowsIngress is policy admission, not evidence of successful HTTP or a VM.
func (s EC2NetworkSnapshot) AllowsIngress(source netip.Addr, port int) bool {
	if !s.PublicRoute || !source.Is4() || port < 1 || port > 65535 {
		return false
	}
	for _, rule := range s.Ingress {
		p, e := netip.ParsePrefix(rule.CIDR)
		if e == nil && rule.Protocol == "tcp" && port >= rule.FromPort && port <= rule.ToPort && p.Contains(source) {
			return true
		}
	}
	return false
}
func (s *EC2NetworkStore) Snapshot(ctx context.Context, region, owner, subnetID string, groups []string) (EC2NetworkSnapshot, error) {
	out := EC2NetworkSnapshot{}
	if owner == "" || len(groups) == 0 || len(groups) > 5 {
		return out, fmt.Errorf("network scope and security groups required")
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	all, e := networkList(ctx, tx, region, owner)
	if e != nil {
		return out, e
	}
	byID := map[string]EC2NetworkResource{}
	for _, v := range all {
		byID[v.ID] = v
	}
	sub, ok := byID[subnetID]
	if !ok || sub.Kind != "AWS::EC2::Subnet" {
		return out, fmt.Errorf("unknown owned subnet")
	}
	vpc, ok := byID[sub.VPCID]
	if !ok || vpc.Kind != "AWS::EC2::VPC" {
		return out, fmt.Errorf("unknown VPC")
	}
	out = EC2NetworkSnapshot{Region: region, StackID: owner, SubnetID: subnetID, VPCCIDR: vpc.CIDR, SubnetCIDR: sub.CIDR, SecurityGroupIDs: append([]string{}, groups...)}
	prefix, err := netip.ParsePrefix(sub.CIDR)
	vpcPrefix, vpcErr := netip.ParsePrefix(vpc.CIDR)
	if err != nil || vpcErr != nil || !prefix.Addr().Is4() || !vpcPrefix.Addr().Is4() || prefix.Bits() < 16 || prefix.Bits() > 28 || vpcPrefix.Bits() > prefix.Bits() || !vpcPrefix.Contains(prefix.Addr()) {
		return EC2NetworkSnapshot{}, fmt.Errorf("invalid stored IPv4 subnet/VPC")
	}
	out.GatewayIPv4 = prefix.Addr().Next().String()
	sort.Strings(out.SecurityGroupIDs)
	seen := map[string]bool{}
	for _, id := range out.SecurityGroupIDs {
		sg, ok := byID[id]
		if !ok || sg.Kind != "AWS::EC2::SecurityGroup" || sg.VPCID != sub.VPCID || seen[id] {
			return EC2NetworkSnapshot{}, fmt.Errorf("invalid owned security group")
		}
		seen[id] = true
		out.Ingress = append(out.Ingress, sg.Ingress...)
	}
	for _, assoc := range all {
		if assoc.Kind != "AWS::EC2::SubnetRouteTableAssociation" || assoc.SubnetID != subnetID {
			continue
		}
		for _, route := range all {
			if route.Kind != "AWS::EC2::Route" || route.RouteTableID != assoc.RouteTableID || route.CIDR != "0.0.0.0/0" || !route.RouteEnabled {
				continue
			}
			for _, attach := range all {
				if attach.Kind == "AWS::EC2::VPCGatewayAttachment" && attach.GatewayID == route.GatewayID && attach.VPCID == sub.VPCID && sub.PublicIP {
					out.PublicRoute = true
				}
			}
		}
	}
	raw, _ := json.Marshal(struct {
		Resources []EC2NetworkResource
		Snapshot  EC2NetworkSnapshot
	}{all, out})
	hash := sha256.Sum256(raw)
	out.Seal = hex.EncodeToString(hash[:])
	if e = tx.Commit(); e != nil {
		return EC2NetworkSnapshot{}, e
	}
	return out, nil
}
