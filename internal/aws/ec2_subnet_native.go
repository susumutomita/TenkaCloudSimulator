package aws

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"regexp"
	"sort"
)

const (
	ec2NativeSubnetKind   = "AWS::EC2::Subnet"
	ec2ModifyNativeSubnet = "ModifySubnetAttribute"
)

var ec2SubnetIDPattern = regexp.MustCompile(`^subnet-([0-9a-f]{8}|[0-9a-f]{17})$`)

type ec2SubnetMutation struct {
	vpc, id, cidr, az string
	publicIPv4        *bool
	// An admitted non-admin caller must also be authorized for the allocated ID.
	authorizeCreatedID func(string) error
}

func parseEC2SubnetMutation(action string, fields map[string][]string) (ec2SubnetMutation, *ec2Failure) {
	var q ec2SubnetMutation
	for key, values := range fields {
		if len(values) != 1 {
			return q, ec2Invalid("InvalidParameterValue", "Parameter "+key+" must be sent once.")
		}
		value := values[0]
		switch {
		case key == "Action" || key == "Version":
		case key == "DryRun" && value == "false":
		case action == "CreateSubnet" && key == "VpcId":
			q.vpc = value
		case action == "CreateSubnet" && key == "CidrBlock":
			q.cidr = value
		case action == "CreateSubnet" && key == "AvailabilityZone" && value != "":
			q.az = value
		case (action == "DeleteSubnet" || action == ec2ModifyNativeSubnet) && key == "SubnetId":
			q.id = value
		case action == ec2ModifyNativeSubnet && key == "MapPublicIpOnLaunch.Value":
			if value != "true" && value != "false" {
				return q, ec2Invalid("InvalidParameterValue", "MapPublicIpOnLaunch.Value must be true or false.")
			}
			on := value == "true"
			q.publicIPv4 = &on
		default:
			return q, ec2Invalid("UnsupportedOperation", "Parameter "+key+" is not supported by local "+action+".")
		}
	}
	if action == "CreateSubnet" {
		if q.vpc == "" || q.cidr == "" {
			return q, ec2Invalid("MissingParameter", "VpcId and CidrBlock are required.")
		}
		if !ec2VPCIDPattern.MatchString(q.vpc) {
			return q, ec2Invalid("InvalidVpcID.Malformed", "VpcId is not a valid VPC ID.")
		}
		prefix, valid := nativeSubnetPrefix(q.cidr)
		if !valid {
			return q, ec2Invalid("InvalidSubnet.Range", "CidrBlock must be an IPv4 range between /16 and /28.")
		}
		q.cidr = prefix.Masked().String()
	} else {
		if q.id == "" {
			return q, ec2Invalid("MissingParameter", "SubnetId is required.")
		}
		if !ec2SubnetIDPattern.MatchString(q.id) {
			return q, ec2Invalid("InvalidSubnetID.Malformed", "SubnetId is not a valid subnet ID.")
		}
		if action == ec2ModifyNativeSubnet && q.publicIPv4 == nil {
			return q, ec2Invalid("MissingParameter", "MapPublicIpOnLaunch.Value is required.")
		}
	}
	return q, nil
}

func nativeSubnetPrefix(text string) (netip.Prefix, bool) {
	p, err := netip.ParsePrefix(text)
	return p, err == nil && p.Addr().Is4() && p.Bits() >= 16 && p.Bits() <= 28
}

func nativeSubnetAZ(region, az string) bool {
	return az == region+"a" || az == region+"b" || az == region+"c"
}

func nativeSubnetCreatePermission(region, id string) [2]string {
	return [2]string{"ec2:CreateSubnet", networkARN(region, "subnet", id)}
}

func (q ec2SubnetMutation) permissions(action, region string) [][2]string {
	if action == "CreateSubnet" {
		return [][2]string{{"ec2:CreateSubnet", networkARN(region, "vpc", q.vpc)}, nativeSubnetCreatePermission(region, "*")}
	}
	return [][2]string{{"ec2:" + action, networkARN(region, "subnet", q.id)}}
}

func nativeSubnetUnclaimed(ctx context.Context, tx *sql.Tx, region, id string) error {
	var claimed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_cfn_ownership WHERE namespace=? AND physical_id=?)`,
		stackResourceNamespace(region, ec2NativeSubnetKind), id).Scan(&claimed)
	if err != nil {
		return err
	}
	if claimed {
		return errors.New("native subnet or VPC has a CloudFormation ownership claim")
	}
	return nil
}

func nativeSubnetParent(ctx context.Context, tx *sql.Tx, region, id string) (netip.Prefix, error) {
	v, err := networkRead(ctx, tx, region, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && v.Kind != "AWS::EC2::VPC" {
		return netip.Prefix{}, ec2Invalid("InvalidVpcID.NotFound", "The VPC does not exist in this region.")
	}
	if err != nil {
		return netip.Prefix{}, err
	}
	if v.StackID != ec2NativeOwner {
		return netip.Prefix{}, ec2Invalid("UnsupportedOperation", "CloudFormation owns this VPC; native subnet writes do not change its stack.")
	}
	if err = nativeSubnetUnclaimed(ctx, tx, region, id); err != nil {
		return netip.Prefix{}, err
	}
	p, valid := nativeSubnetPrefix(v.CIDR)
	if !valid || p.Masked() != p {
		return netip.Prefix{}, errors.New("invalid native VPC CIDR state")
	}
	return p, nil
}

func nativeSubnetReadback(ctx context.Context, tx *sql.Tx, r EC2NetworkResource) (ec2SubnetRead, error) {
	if r.Kind != ec2NativeSubnetKind || r.StackID != ec2NativeOwner || !ec2SubnetIDPattern.MatchString(r.ID) ||
		!ec2VPCIDPattern.MatchString(r.VPCID) || !nativeSubnetAZ(r.Region, r.AvailabilityZone) || r.Revision < 1 || !validNativeEC2Tags(r.Tags) {
		return ec2SubnetRead{}, errors.New("invalid native subnet identity or settings")
	}
	if err := nativeSubnetUnclaimed(ctx, tx, r.Region, r.ID); err != nil {
		return ec2SubnetRead{}, err
	}
	parent, err := nativeSubnetParent(ctx, tx, r.Region, r.VPCID)
	if err != nil {
		return ec2SubnetRead{}, fmt.Errorf("invalid native subnet parent: %w", err)
	}
	p, valid := nativeSubnetPrefix(r.CIDR)
	if !valid || p.Masked() != p {
		return ec2SubnetRead{}, errors.New("invalid native subnet CIDR state")
	}
	if p.Bits() < parent.Bits() || !parent.Contains(p.Addr()) {
		return ec2SubnetRead{}, errEC2NetworkOutsideVPC
	}
	return ec2SubnetRead{ID: r.ID, VPC: r.VPCID, CIDR: r.CIDR, State: "available", AZ: r.AvailabilityZone,
		ManagedBy: ec2ManagedByEC2, PublicIPv4: &r.PublicIP, Tags: ec2ReadTags(r.Tags)}, nil
}

func nativeSubnetRows(ctx context.Context, tx *sql.Tx, region string) ([]ec2SubnetRead, error) {
	rows, err := networkList(ctx, tx, region, ec2NativeOwner)
	if err != nil {
		return nil, err
	}
	answer := []ec2SubnetRead{}
	for _, r := range rows {
		if r.Kind != ec2NativeSubnetKind {
			continue
		}
		s, err := nativeSubnetReadback(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		answer = append(answer, s)
	}
	return answer, nil
}

func (s *StackStore) createNativeSubnet(ctx context.Context, region string, q ec2SubnetMutation) (ec2SubnetRead, error) {
	if q.az == "" {
		q.az = region + "a"
	}
	if !nativeSubnetAZ(region, q.az) {
		return ec2SubnetRead{}, ec2Invalid("InvalidParameterValue", "AvailabilityZone must be a supported zone of this region.")
	}
	random := make([]byte, 9)
	if _, err := rand.Read(random); err != nil {
		return ec2SubnetRead{}, err
	}
	id := "subnet-" + hex.EncodeToString(random)[:17]
	if q.authorizeCreatedID != nil {
		if err := q.authorizeCreatedID(id); err != nil {
			return ec2SubnetRead{}, err
		}
	}
	r := EC2NetworkResource{Region: region, ID: id, Kind: ec2NativeSubnetKind,
		StackID: ec2NativeOwner, VPCID: q.vpc, CIDR: q.cidr, AvailabilityZone: q.az, Revision: 1}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ec2SubnetRead{}, err
	}
	defer tx.Rollback()
	read, err := nativeSubnetReadback(ctx, tx, r)
	if errors.Is(err, errEC2NetworkOutsideVPC) {
		return ec2SubnetRead{}, ec2Invalid("InvalidSubnet.Range", "The subnet lies outside its VPC.")
	}
	if err != nil {
		return ec2SubnetRead{}, err
	}
	_, marked, err := rawNativeACLDefault(ctx, tx, region, r.VPCID)
	if err != nil {
		return ec2SubnetRead{}, err
	}
	before, err := nativeACLWriteCount(ctx, tx)
	if err != nil {
		return ec2SubnetRead{}, err
	}
	if err := createEC2Network(ctx, tx, r); err != nil {
		switch {
		case errors.Is(err, errEC2NetworkOverlap):
			return ec2SubnetRead{}, ec2Invalid("InvalidSubnet.Conflict", "The subnet CIDR overlaps an existing subnet of this VPC.")
		case errors.Is(err, errEC2NetworkQuota):
			return ec2SubnetRead{}, ec2Invalid("SubnetLimitExceeded", "The local limit of 1000 network resources has been reached.")
		}
		return ec2SubnetRead{}, err
	}
	if err := createNativeSubnetACLAssociation(ctx, tx, r); err != nil {
		return ec2SubnetRead{}, err
	}
	want := int64(1)
	if marked {
		want++
	}
	if err := nativeACLRequireWriteDelta(ctx, tx, before, want); err != nil {
		return ec2SubnetRead{}, err
	}
	return read, tx.Commit()
}

func (s *StackStore) deleteNativeSubnet(ctx context.Context, region, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := networkRead(ctx, tx, region, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && r.Kind != ec2NativeSubnetKind {
		return ec2Invalid("InvalidSubnetID.NotFound", "The subnet does not exist in this region.")
	}
	if err != nil {
		return err
	}
	if r.StackID != ec2NativeOwner {
		return ec2Invalid("UnsupportedOperation", "CloudFormation owns this subnet; delete its stack to remove it.")
	}
	if _, err := nativeSubnetReadback(ctx, tx, r); err != nil {
		return err
	}
	var dependent bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_network_resources WHERE region=? AND id<>? AND json_extract(document,'$.SubnetID')=?)`, region, id, id).Scan(&dependent); err != nil {
		return err
	}
	if dependent {
		return ec2Invalid("DependencyViolation", "The subnet still has a network dependency.")
	}
	_, marked, err := rawNativeACLDefault(ctx, tx, region, r.VPCID)
	if err != nil {
		return err
	}
	before, err := nativeACLWriteCount(ctx, tx)
	if err != nil {
		return err
	}
	if err := removeNativeSubnetACLAssociation(ctx, tx, r); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources WHERE region=? AND id=? AND kind=? AND stack_id=? AND revision=?`,
		region, id, ec2NativeSubnetKind, ec2NativeOwner, r.Revision)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errors.New("subnet delete was not acknowledged")
	}
	if _, err := networkRead(ctx, tx, region, id); !errors.Is(err, sql.ErrNoRows) {
		return errNativeNetworkACLAssociationState
	}
	if err := checkNativeACLSubnetBindingAbsent(ctx, tx, r); err != nil {
		return err
	}
	want := int64(1)
	if marked {
		want++
	}
	if err := nativeACLRequireWriteDelta(ctx, tx, before, want); err != nil {
		return err
	}
	return tx.Commit()
}

// Store only the requested launch preference. This never allocates an address,
// changes a route, or turns a configuration-only subnet into a guest network.
func (s *StackStore) modifyNativeSubnetPublicIPv4(ctx context.Context, region, id string, on bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, err := networkRead(ctx, tx, region, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.Kind != ec2NativeSubnetKind {
		return ec2Invalid("InvalidSubnetID.NotFound", "The subnet does not exist in this region.")
	}
	if err != nil {
		return err
	}
	if row.StackID != ec2NativeOwner {
		return ec2Invalid("UnsupportedOperation", "CloudFormation owns this subnet; native settings do not change its stack.")
	}
	if _, err := nativeSubnetReadback(ctx, tx, row); err != nil {
		return err
	}
	parent, err := networkRead(ctx, tx, region, row.VPCID)
	if err != nil {
		return err
	}
	checkDeclarations := func() error {
		_, declared, err := regionalGatewayStacks(ctx, tx, region)
		if err != nil {
			return err
		}
		if declared[row.ID] || declared[row.VPCID] {
			return errors.New("native subnet settings conflict with a CloudFormation declaration")
		}
		return nil
	}
	if err := checkDeclarations(); err != nil {
		return err
	}
	if row.PublicIP == on {
		return tx.Commit() // A repeat does not change the row revision.
	}
	row.PublicIP = on
	row.Revision++
	if err := networkWrite(ctx, tx, row, false); err != nil {
		return err
	}
	stored, err := networkRead(ctx, tx, region, id)
	if err != nil {
		return err
	}
	observed, err := nativeSubnetReadback(ctx, tx, stored)
	if err != nil {
		return err
	}
	currentParent, err := networkRead(ctx, tx, region, row.VPCID)
	if err != nil {
		return err
	}
	if observed.PublicIPv4 == nil || *observed.PublicIPv4 != on || !reflect.DeepEqual(parent, currentParent) {
		return errors.New("native subnet settings readback differs")
	}
	if err := checkDeclarations(); err != nil {
		return err
	}
	return tx.Commit()
}

type ec2ModifySubnetResponse struct {
	XMLName   xml.Name `xml:"ModifySubnetAttributeResponse"`
	XMLNS     string   `xml:"xmlns,attr"`
	RequestID string   `xml:"requestId"`
	Return    bool     `xml:"return"`
}

type ec2CreateSubnetResponse struct {
	XMLName   xml.Name      `xml:"CreateSubnetResponse"`
	XMLNS     string        `xml:"xmlns,attr"`
	RequestID string        `xml:"requestId"`
	Subnet    ec2SubnetRead `xml:"subnet"`
}

type ec2DeleteSubnetResponse struct {
	XMLName   xml.Name `xml:"DeleteSubnetResponse"`
	XMLNS     string   `xml:"xmlns,attr"`
	RequestID string   `xml:"requestId"`
	Return    bool     `xml:"return"`
}

type ec2DescribeSubnetsResponse struct {
	XMLName   xml.Name     `xml:"DescribeSubnetsResponse"`
	XMLNS     string       `xml:"xmlns,attr"`
	RequestID string       `xml:"requestId"`
	Subnets   ec2SubnetSet `xml:"subnetSet"`
}

func addNativeSubnetReadback(ctx context.Context, tx *sql.Tx, region string, out *ec2SubnetSet) error {
	native, err := nativeSubnetRows(ctx, tx, region)
	if err != nil || len(out.Items)+len(native) > 1000 {
		return errors.New("subnet identity or bound")
	}
	out.Items = append(out.Items, native...)
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].ID < out.Items[j].ID })
	return nil
}
