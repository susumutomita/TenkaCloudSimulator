package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"reflect"
	"strings"
)

const nativeDefaultRoutePrefix = "rtbroute-"

var errEC2NativeDefaultRouteState = errors.New("inconsistent native IPv4 route state")

func nativeDefaultRouteID(table string) string {
	return nativeDefaultRoutePrefix + strings.TrimPrefix(table, "rtb-")
}

// Preserve the 582 default route's identity so no migration or dual-key
// adoption is needed. Non-default keys use the canonical address and length.
func nativeIPv4RouteID(table string, destination netip.Prefix) string {
	if destination.Bits() == 0 {
		return nativeDefaultRouteID(table)
	}
	return fmt.Sprintf("%s-%x-%02d", nativeDefaultRouteID(table), destination.Addr().As4(), destination.Bits())
}

var reservedEC2RouteDestination = netip.MustParsePrefix("169.254.168.0/22")

func nativeRouteDestinationAllowed(destination, vpc netip.Prefix) bool {
	// The existing default remains valid. For non-default routes this
	// bounded IGW target does not model local route replacement or special
	// address paths: require full separation from the VPC and reserved range.
	return vpc.IsValid() && (destination.Bits() == 0 ||
		!vpc.Overlaps(destination) && !reservedEC2RouteDestination.Overlaps(destination))
}

func validNativeIPv4Route(row EC2NetworkResource) bool {
	destination, err := netip.ParsePrefix(row.CIDR)
	return err == nil && destination.Addr().Is4() && destination.Masked().String() == row.CIDR &&
		row.Kind == ec2Route && row.StackID == ec2NativeOwner && row.Revision == 1 &&
		ec2NativeRouteTableID.MatchString(row.RouteTableID) && row.ID == nativeIPv4RouteID(row.RouteTableID, destination) &&
		ec2InternetGatewayID.MatchString(row.GatewayID) && ec2InternetGatewayVPCID(row.VPCID) &&
		row.RouteEnabled &&
		row.SubnetID == "" && row.Description == "" && row.AvailabilityZone == "" &&
		!row.DNSHostnames && !row.DNSSupport && !row.PublicIP && len(row.Tags) == 0 && len(row.Ingress) == 0
}

type nativeIPv4RouteKey struct {
	table       string
	destination netip.Prefix
}

type nativeIPv4Route struct {
	gateway, vpc string
}

func nativeIPv4RoutesForTable(routes map[nativeIPv4RouteKey]nativeIPv4Route, table string) int {
	count := 0
	for key := range routes {
		if key.table == table {
			count++
		}
	}
	return count
}

func nativeIPv4Routes(ctx context.Context, tx *sql.Tx, region string, all []EC2NetworkResource,
	declared map[string]bool, links nativeInternetGatewayLinks) (map[nativeIPv4RouteKey]nativeIPv4Route, error) {
	out := map[nativeIPv4RouteKey]nativeIPv4Route{}
	count := map[string]int{}
	vpcPrefixes := map[string]netip.Prefix{}
	for _, row := range all {
		if row.Kind == ec2RTVPC && links.byVPC[row.ID].ID != "" {
			prefix, _ := netip.ParsePrefix(row.CIDR)
			vpcPrefixes[row.ID] = prefix
		}
	}
	for _, r := range all {
		if r.Kind != ec2Route && !strings.HasPrefix(r.ID, nativeDefaultRoutePrefix) {
			continue
		}
		if !validNativeIPv4Route(r) || r.Region != region {
			return nil, errEC2NativeDefaultRouteState
		}
		// Only this bounded native writer may persist these rows. Require the
		// precise canonical stored document; no duplicate or case-folded keys
		// may turn a damaged route into a different dependency.
		encoded, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		var canonical bool
		if err := tx.QueryRowContext(ctx, `SELECT document=? FROM aws_ec2_network_resources WHERE region=? AND id=?`,
			string(encoded), region, r.ID).Scan(&canonical); err != nil {
			return nil, err
		}
		if !canonical {
			return nil, errEC2NativeDefaultRouteState
		}
		if err := ensureNativeInternetGatewayUnclaimed(ctx, tx, region, r.ID, declared); err != nil {
			return nil, err
		}
		table, err := readNativeRouteTableInSnapshot(ctx, tx, region, r.RouteTableID, &declared)
		if err != nil || table.VPCID != r.VPCID {
			return nil, errEC2NativeDefaultRouteState
		}
		if err := ensureNativeInternetGatewayUnclaimed(ctx, tx, region, table.ID, declared); err != nil {
			return nil, err
		}
		link := links.byGateway[r.GatewayID]
		destination, _ := netip.ParsePrefix(r.CIDR) // checked by validNativeIPv4Route
		key := nativeIPv4RouteKey{table.ID, destination}
		if link.ID == "" || link.VPCID != r.VPCID || links.byVPC[r.VPCID].ID != link.ID ||
			out[key].gateway != "" || count[table.ID] >= EC2NativeIPv4RouteLimit ||
			!nativeRouteDestinationAllowed(destination, vpcPrefixes[r.VPCID]) {
			return nil, errEC2NativeDefaultRouteState
		}
		out[key] = nativeIPv4Route{r.GatewayID, r.VPCID}
		count[table.ID]++
	}
	return out, nil
}

// A reserved native identity cannot be used by CFN (even when only one
// ownership witness survived) or occupied by an unrelated native kind.
func checkNativeDefaultRouteReservation(ctx context.Context, tx *sql.Tx, region string) error {
	var bad bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_cfn_ownership
		WHERE namespace=? AND physical_id GLOB ?) OR EXISTS(SELECT 1 FROM aws_ec2_network_resources
		WHERE region=? AND stack_id<>? AND id GLOB ?)`, stackResourceNamespace(region, ec2Route),
		nativeDefaultRoutePrefix+"*", region, ec2NativeOwner, nativeDefaultRoutePrefix+"*").Scan(&bad); err != nil {
		return err
	}
	if bad {
		return errEC2NativeDefaultRouteState
	}
	return nil
}

// All reads use the same snapshot. Avoid imposing IGW projection semantics
// on unrelated CFN tables: only the native route graph is imported here.
func nativeIPv4RoutesInRegion(ctx context.Context, tx *sql.Tx, region string) (map[nativeIPv4RouteKey]nativeIPv4Route, error) {
	if err := checkNativeDefaultRouteReservation(ctx, tx, region); err != nil {
		return nil, err
	}
	_, declared, err := regionalGatewayStacks(ctx, tx, region)
	if err != nil {
		return nil, err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_network_resources
		WHERE region=? AND stack_id=? AND (kind=? OR id GLOB ?))`, region, ec2NativeOwner,
		ec2Route, nativeDefaultRoutePrefix+"*").Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	all, err := networkList(ctx, tx, region, ec2NativeOwner)
	if err != nil {
		return nil, err
	}
	links, err := readNativeInternetGatewayLinks(ctx, tx, region, all, declared)
	if err != nil {
		return nil, err
	}
	return nativeIPv4Routes(ctx, tx, region, all, declared, links)
}

func (s *StackStore) changeNativeDefaultRoute(ctx context.Context, region string, q ec2NativeDefaultRouteQuery, create bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	table, err := readNativeRouteTable(ctx, tx, region, q.table)
	if err != nil {
		return err
	}
	if table.CIDR != "" && q.destination.String() == table.CIDR {
		return ec2Invalid("InvalidParameterValue", "The automatic VPC-local route is not editable in this simulator.")
	}
	before, err := readInternetGatewayInventory(ctx, tx, region)
	if err != nil {
		return err
	}
	key := nativeIPv4RouteKey{table.ID, q.destination}
	if !key.destination.Addr().Is4() || key.destination.Masked() != key.destination {
		return errEC2NativeDefaultRouteState
	}
	id := nativeIPv4RouteID(table.ID, key.destination)
	for _, resource := range []string{id, table.ID, table.VPCID} {
		if err := ensureNativeInternetGatewayUnclaimed(ctx, tx, region, resource, before.declared); err != nil {
			return err
		}
	}
	wantRoutes := maps.Clone(before.routes)
	if wantRoutes == nil {
		wantRoutes = map[nativeIPv4RouteKey]nativeIPv4Route{}
	}
	route, exists := before.routes[key]
	if create {
		if exists {
			return ec2Invalid("RouteAlreadyExists", "This IPv4 destination already exists in this route table.")
		}
		if nativeIPv4RoutesForTable(before.routes, table.ID) >= EC2NativeIPv4RouteLimit {
			return ec2Invalid("RouteLimitExceeded", "The local IPv4 route-table bound has been reached.")
		}
		route = nativeIPv4Route{q.gateway, table.VPCID}
	} else if !exists {
		return ec2Invalid("InvalidRoute.NotFound", "That IPv4 destination does not exist in this route table.")
	}
	gateway, vpc, err := readNativeInternetGatewayPair(ctx, tx, region, route.gateway, table.VPCID)
	if err != nil {
		return err
	}
	link := before.links.byGateway[gateway.ID]
	if link.ID == "" || link.VPCID != table.VPCID {
		return ec2Invalid("Gateway.NotAttached", "The native internet gateway is not attached to this VPC.")
	}
	if create {
		prefix, _ := netip.ParsePrefix(vpc.CIDR) // verified by the attachment snapshot
		if !nativeRouteDestinationAllowed(key.destination, prefix) {
			return ec2Invalid("InvalidParameterValue", "A non-default route cannot overlap the VPC or the reserved IPv4 service range.")
		}
		row := EC2NetworkResource{ID: id, Region: region, StackID: ec2NativeOwner, Kind: ec2Route,
			VPCID: table.VPCID, RouteTableID: table.ID, GatewayID: route.gateway, CIDR: key.destination.String(), RouteEnabled: true}
		if err := createEC2Network(ctx, tx, row); err != nil {
			if errors.Is(err, errEC2NetworkQuota) {
				return ec2Invalid("RouteLimitExceeded", "The local network resource bound has been reached.")
			}
			return err
		}
		wantRoutes[key] = route
	} else {
		result, err := tx.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources
			WHERE region=? AND id=? AND stack_id=? AND kind=? AND revision=1`, region, id, ec2NativeOwner, ec2Route)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return errEC2NativeDefaultRouteState
		}
		if _, err := networkRead(ctx, tx, region, id); !errors.Is(err, sql.ErrNoRows) {
			return errEC2NativeDefaultRouteState
		}
		delete(wantRoutes, key)
	}
	current, err := readNativeRouteTable(ctx, tx, region, table.ID)
	if err != nil || !reflect.DeepEqual(current, table) {
		return errEC2NativeDefaultRouteState
	}
	currentGateway, currentVPC, err := readNativeInternetGatewayPair(ctx, tx, region, gateway.ID, vpc.ID)
	if err != nil || !reflect.DeepEqual(currentGateway, gateway) || !reflect.DeepEqual(currentVPC, vpc) {
		return errEC2NativeDefaultRouteState
	}
	after, err := readInternetGatewayInventory(ctx, tx, region)
	if err != nil {
		return err
	}
	for _, resource := range []string{id, table.ID, table.VPCID} {
		if err := ensureNativeInternetGatewayUnclaimed(ctx, tx, region, resource, after.declared); err != nil {
			return err
		}
	}
	if !maps.Equal(wantRoutes, after.routes) ||
		!reflect.DeepEqual(after.links.byGateway[route.gateway], link) ||
		!reflect.DeepEqual(after.links.byVPC[table.VPCID], link) {
		return errEC2NativeDefaultRouteState
	}
	return tx.Commit()
}
