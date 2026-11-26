package aws

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"strings"
)

const nativeInternetGatewayLinkPrefix = "igwattach-"

func ec2InternetGatewayVPCID(id string) bool {
	return (len(id) == 12 || len(id) == 21) && ec2VPCIDPattern.MatchString(id)
}

// The internal relation identity is reserved to native attachments. It is
// stable for a gateway, independent of its current VPC, and is not on the wire.
func nativeInternetGatewayLinkID(gateway string) string {
	return nativeInternetGatewayLinkPrefix + strings.TrimPrefix(gateway, "igw-")
}

type nativeInternetGatewayLinks struct {
	byGateway map[string]EC2NetworkResource
	byVPC     map[string]EC2NetworkResource
}

func validNativeInternetGatewayLink(row EC2NetworkResource) bool {
	return row.Kind == ec2RTAttachment && row.StackID == ec2NativeOwner && row.Revision == 1 &&
		ec2InternetGatewayID.MatchString(row.GatewayID) && ec2InternetGatewayVPCID(row.VPCID) &&
		row.ID == nativeInternetGatewayLinkID(row.GatewayID) &&
		row.SubnetID == "" && row.RouteTableID == "" && row.CIDR == "" && row.AvailabilityZone == "" &&
		row.Description == "" && !row.DNSSupport && !row.DNSHostnames && !row.PublicIP && !row.RouteEnabled &&
		len(row.Tags) == 0 && len(row.Ingress) == 0
}

func checkNativeInternetGatewayVPC(ctx context.Context, tx *sql.Tx, region string, vpc EC2NetworkResource, declared map[string]bool) error {
	cidr, valid := nativeSubnetPrefix(vpc.CIDR)
	if vpc.Kind != ec2RTVPC || vpc.Region != region || vpc.StackID != ec2NativeOwner ||
		!ec2InternetGatewayVPCID(vpc.ID) || !valid || cidr.Masked() != cidr ||
		vpc.VPCID != "" || vpc.SubnetID != "" || vpc.GatewayID != "" || vpc.RouteTableID != "" ||
		vpc.PublicIP || vpc.RouteEnabled {
		return errEC2InternetGatewayState
	}
	return ensureNativeInternetGatewayUnclaimed(ctx, tx, region, vpc.ID, declared)
}

// All rows come from one SQLite snapshot through networkList. Native routes
// are still unsupported; a wrong-kind row occupying a reserved identity must
// not turn a damaged attachment into an apparently detached gateway.
func readNativeInternetGatewayLinks(ctx context.Context, tx *sql.Tx, region string, network []EC2NetworkResource, declared map[string]bool) (nativeInternetGatewayLinks, error) {
	out := nativeInternetGatewayLinks{byGateway: map[string]EC2NetworkResource{}, byVPC: map[string]EC2NetworkResource{}}
	byID := make(map[string]EC2NetworkResource, len(network))
	for _, row := range network {
		byID[row.ID] = row
		if row.Kind == ec2Route || strings.HasPrefix(row.ID, nativeInternetGatewayLinkPrefix) && row.Kind != ec2RTAttachment {
			return out, errEC2InternetGatewayState
		}
		if row.Kind == ec2RTGateway {
			if !validInternetGatewayRow(row) {
				return out, errEC2InternetGatewayState
			}
			if err := ensureNativeInternetGatewayUnclaimed(ctx, tx, region, row.ID, declared); err != nil {
				return out, err
			}
		}
	}
	for _, row := range network {
		if row.Kind != ec2RTAttachment {
			continue
		}
		if !validNativeInternetGatewayLink(row) || row.Region != region ||
			byID[row.GatewayID].Kind != ec2RTGateway ||
			out.byGateway[row.GatewayID].ID != "" || out.byVPC[row.VPCID].ID != "" {
			return out, errEC2InternetGatewayState
		}
		if err := ensureNativeInternetGatewayUnclaimed(ctx, tx, region, row.ID, declared); err != nil {
			return out, err
		}
		if err := checkNativeInternetGatewayVPC(ctx, tx, region, byID[row.VPCID], declared); err != nil {
			return out, err
		}
		out.byGateway[row.GatewayID], out.byVPC[row.VPCID] = row, row
	}
	return out, nil
}

func readNativeInternetGatewayPair(ctx context.Context, tx *sql.Tx, region, gatewayID, vpcID string) (EC2NetworkResource, EC2NetworkResource, error) {
	gateway, err := networkRead(ctx, tx, region, gatewayID)
	var vpc EC2NetworkResource
	if errors.Is(err, sql.ErrNoRows) || err == nil && gateway.Kind != ec2RTGateway {
		return gateway, vpc, missingInternetGatewayID()
	}
	if err != nil {
		return gateway, vpc, err
	}
	if gateway.StackID != ec2NativeOwner {
		return gateway, vpc, ec2Invalid("UnsupportedOperation", "CloudFormation owns this internet gateway; change its attachment through the stack.")
	}
	vpc, err = networkRead(ctx, tx, region, vpcID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && vpc.Kind != ec2RTVPC {
		return gateway, vpc, ec2Invalid("InvalidVpcID.NotFound", "The VPC does not exist in this region.")
	}
	if err != nil {
		return gateway, vpc, err
	}
	if vpc.StackID != ec2NativeOwner {
		return gateway, vpc, ec2Invalid("UnsupportedOperation", "CloudFormation owns this VPC; change its attachment through the stack.")
	}
	return gateway, vpc, nil
}

func (s *StackStore) changeNativeInternetGatewayAttachment(ctx context.Context, region, gatewayID, vpcID string, attach bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	gateway, vpc, err := readNativeInternetGatewayPair(ctx, tx, region, gatewayID, vpcID)
	if err != nil {
		return err
	}
	before, err := readInternetGatewayInventory(ctx, tx, region)
	if err != nil {
		return err
	}
	if err := checkNativeInternetGatewayVPC(ctx, tx, region, vpc, before.declared); err != nil {
		return err
	}
	link := before.links.byGateway[gatewayID]
	id := nativeInternetGatewayLinkID(gatewayID)
	if err := ensureNativeInternetGatewayUnclaimed(ctx, tx, region, id, before.declared); err != nil {
		return err
	}
	// Current native VPCs cannot host EC2 workloads with public IPs. CFN and
	// its guest stack are not eligible endpoints. Never erase a surviving
	// modeled reference to this gateway when detaching its native link.
	if attach {
		if link.ID != "" || before.links.byVPC[vpcID].ID != "" {
			return ec2Invalid("Resource.AlreadyAssociated", "The internet gateway or VPC is already attached.")
		}
		link = EC2NetworkResource{ID: id, Kind: ec2RTAttachment, Region: region, StackID: ec2NativeOwner, GatewayID: gatewayID, VPCID: vpcID, Revision: 1}
		if err := createEC2Network(ctx, tx, link); err != nil {
			if errors.Is(err, errEC2NetworkQuota) {
				return ec2Invalid("ResourceLimitExceeded", "The local network resource bound was reached.")
			}
			return err
		}
	} else {
		if link.ID == "" || link.VPCID != vpcID {
			return ec2Invalid("Gateway.NotAttached", "The internet gateway is not attached to that VPC.")
		}
		if found, err := internetGatewayHasDependents(ctx, tx, region, gatewayID, link.ID); err != nil {
			return err
		} else if found {
			return ec2Invalid("DependencyViolation", "The attachment still has a modeled route reference; it was not detached.")
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources
			WHERE region=? AND id=? AND stack_id=? AND kind=? AND revision=?`,
			region, id, ec2NativeOwner, ec2RTAttachment, link.Revision)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return errEC2InternetGatewayState
		}
		if _, err := networkRead(ctx, tx, region, id); !errors.Is(err, sql.ErrNoRows) {
			return errEC2InternetGatewayState
		}
	}

	// Re-read both parents, claims, declarations and exact reciprocal link
	// before committing. A failed or disagreeing readback rolls back the write.
	currentGateway, currentVPC, err := readNativeInternetGatewayPair(ctx, tx, region, gatewayID, vpcID)
	if err != nil || currentVPC.Revision != vpc.Revision || currentVPC.CIDR != vpc.CIDR || !maps.Equal(currentGateway.Tags, gateway.Tags) {
		return errEC2InternetGatewayState
	}
	after, err := readInternetGatewayInventory(ctx, tx, region)
	if err != nil {
		return err
	}
	if err := checkNativeInternetGatewayVPC(ctx, tx, region, currentVPC, after.declared); err != nil {
		return err
	}
	gotGateway, gotVPC := after.links.byGateway[gatewayID], after.links.byVPC[vpcID]
	if attach {
		if gotGateway.ID != id || gotVPC.ID != id || gotGateway.VPCID != vpcID || gotVPC.GatewayID != gatewayID {
			return errEC2InternetGatewayState
		}
	} else {
		if gotGateway.ID != "" || gotVPC.ID != "" {
			return errEC2InternetGatewayState
		}
		if found, err := internetGatewayHasDependents(ctx, tx, region, gatewayID, ""); err != nil || found {
			return errEC2InternetGatewayState
		}
	}
	return tx.Commit()
}
