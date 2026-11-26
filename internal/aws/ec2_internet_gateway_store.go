package aws

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"sort"
	"strings"
)

var errEC2InternetGatewayState = errors.New("inconsistent internet gateway configuration")

// SQLite's JSON paths are case sensitive; the canonical Go decoding of saved
// declarations is not. Check the typed document independently of the claim,
// including when a damaged database retains only one of those witnesses.
func regionalGatewayStacks(ctx context.Context, tx *sql.Tx, region string) (map[string]StackRecord, map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,document FROM aws_cfn_stacks WHERE region=? ORDER BY id LIMIT 101`, region)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	stacks := map[string]StackRecord{}
	declared := map[string]bool{}
	for rows.Next() {
		var id, raw string
		var stack StackRecord
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, nil, err
		}
		if len(stacks) == 100 || json.Unmarshal([]byte(raw), &stack) != nil ||
			stack.ID != id || stack.Region != region || len(stack.Resources) > 100 {
			return nil, nil, errEC2InternetGatewayState
		}
		stacks[id] = stack
		for _, member := range stack.Resources {
			// Only members in the EC2 network namespace own these IDs.
			// Legal S3 bucket, IAM role or function names may match the
			// literal prefix (or even the whole derived relation ID).
			if !stackNetworkKind(member.Type) {
				continue
			}
			if strings.HasPrefix(member.PhysicalID, nativeInternetGatewayLinkPrefix) || strings.HasPrefix(member.PhysicalID, nativeDefaultRoutePrefix) {
				return nil, nil, errEC2InternetGatewayState
			}
			declared[member.PhysicalID] = true
		}
	}
	return stacks, declared, rows.Err()
}

// No native gateway or its endpoints adopt a CloudFormation claim or
// declaration. A caller can pass declarations from this same SQLite snapshot.
func ensureNativeInternetGatewayUnclaimed(ctx context.Context, tx *sql.Tx, region, id string, declared map[string]bool) error {
	var claimed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_cfn_ownership WHERE namespace=? AND physical_id=?)`,
		stackResourceNamespace(region, ec2RTGateway), id).Scan(&claimed)
	if err != nil {
		return err
	}
	if claimed {
		return errEC2InternetGatewayState
	}
	if declared == nil {
		_, declared, err = regionalGatewayStacks(ctx, tx, region)
		if err != nil {
			return err
		}
	}
	if declared[id] {
		return errEC2InternetGatewayState
	}
	return nil
}

func internetGatewayHasDependents(ctx context.Context, tx *sql.Tx, region, id, except string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM aws_ec2_network_resources WHERE region=?
		AND (kind IN (?,?) OR id GLOB ? OR id GLOB ?) ORDER BY id LIMIT 1001`, region, ec2RTAttachment, ec2Route,
		nativeInternetGatewayLinkPrefix+"*", nativeDefaultRoutePrefix+"*")
	if err != nil {
		return false, err
	}
	var ids []string
	for rows.Next() {
		var child string
		if err := rows.Scan(&child); err != nil {
			rows.Close()
			return false, err
		}
		ids = append(ids, child)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if len(ids) > 1000 {
		return false, errEC2InternetGatewayState
	}
	for _, child := range ids {
		related, err := networkRead(ctx, tx, region, child)
		if err != nil {
			return false, err
		}
		if !ec2InternetGatewayID.MatchString(related.GatewayID) ||
			(strings.HasPrefix(related.ID, nativeInternetGatewayLinkPrefix) || related.StackID == ec2NativeOwner && related.Kind == ec2RTAttachment) &&
				!validNativeInternetGatewayLink(related) ||
			strings.HasPrefix(related.ID, nativeDefaultRoutePrefix) && related.Kind != ec2Route {
			return false, errEC2InternetGatewayState
		}
		if related.GatewayID == id && related.ID != except {
			return true, nil
		}
	}
	return false, nil
}

func (s *StackStore) createNativeInternetGateway(ctx context.Context, region string, q ec2InternetGatewayQuery) (EC2NetworkResource, error) {
	var entropy [9]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return EC2NetworkResource{}, err
	}
	id := "igw-" + hex.EncodeToString(entropy[:])[:17]
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EC2NetworkResource{}, err
	}
	defer tx.Rollback()
	if q.authorizeCreatedID != nil {
		if err = q.authorizeCreatedID(id); err != nil {
			return EC2NetworkResource{}, err
		}
	}
	row := EC2NetworkResource{ID: id, Kind: ec2RTGateway, Region: region, StackID: ec2NativeOwner, Tags: q.tags, Revision: 1}
	if !validInternetGatewayRow(row) {
		return EC2NetworkResource{}, errEC2InternetGatewayState
	}
	if err := createEC2Network(ctx, tx, row); err != nil {
		if errors.Is(err, errEC2NetworkQuota) {
			return EC2NetworkResource{}, ec2Invalid("InternetGatewayLimitExceeded", "The local network resource bound was reached.")
		}
		return EC2NetworkResource{}, err
	}
	if err = ensureNativeInternetGatewayUnclaimed(ctx, tx, region, id, nil); err != nil {
		return EC2NetworkResource{}, err
	}
	if found, err := internetGatewayHasDependents(ctx, tx, region, id, ""); err != nil || found {
		return EC2NetworkResource{}, errEC2InternetGatewayState
	}
	return row, tx.Commit()
}

func (s *StackStore) deleteNativeInternetGateway(ctx context.Context, region, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, err := networkRead(ctx, tx, region, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.Kind != ec2RTGateway {
		return missingInternetGatewayID()
	}
	if err != nil {
		return err
	}
	if row.StackID != ec2NativeOwner {
		return ec2Invalid("UnsupportedOperation", "This internet gateway is CloudFormation-managed; remove it through its stack.")
	}
	if !validInternetGatewayRow(row) {
		return errEC2InternetGatewayState
	}
	if err = ensureNativeInternetGatewayUnclaimed(ctx, tx, region, id, nil); err != nil {
		return err
	}
	if found, err := internetGatewayHasDependents(ctx, tx, region, id, ""); err != nil {
		return err
	} else if found {
		return ec2Invalid("DependencyViolation", "The internet gateway is still referenced; it was not deleted.")
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources WHERE region=?
		AND id=? AND stack_id=? AND kind=? AND revision=?`, region, id, ec2NativeOwner, ec2RTGateway, row.Revision)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errEC2InternetGatewayState
	}
	if _, err := networkRead(ctx, tx, region, id); !errors.Is(err, sql.ErrNoRows) {
		return errEC2InternetGatewayState
	}
	if err = ensureNativeInternetGatewayUnclaimed(ctx, tx, region, id, nil); err != nil {
		return err
	}
	if found, err := internetGatewayHasDependents(ctx, tx, region, id, ""); err != nil || found {
		return errEC2InternetGatewayState
	}
	return tx.Commit()
}

func (s *StackStore) describeInternetGateways(ctx context.Context, region string, ids []string) ([]ec2InternetGateway, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	inventory, err := readInternetGatewayInventory(ctx, tx, region)
	if err != nil {
		return nil, err
	}
	out := inventory.rows
	if len(ids) > 0 {
		out = slices.DeleteFunc(out, func(gw ec2InternetGateway) bool { return !slices.Contains(ids, gw.ID) })
		if len(out) != len(ids) {
			return nil, missingInternetGatewayID()
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, tx.Commit()
}

type ec2InternetGatewayInventory struct {
	rows     []ec2InternetGateway
	links    nativeInternetGatewayLinks
	routes   map[nativeIPv4RouteKey]nativeIPv4Route
	declared map[string]bool // physical IDs, independently of the claims
}

// The same authoritative graph drives wire readback and admission and fresh
// transaction readback of native attachment changes.
func readInternetGatewayInventory(ctx context.Context, tx *sql.Tx, region string) (ec2InternetGatewayInventory, error) {
	out := ec2InternetGatewayInventory{}
	// Seeds are deliberately independent. Losing the typed ownership and
	// network rows does not erase a surviving declaration, and a route-only
	// orphan must still enter the regional consistency check before filtering.
	stacks, declared, err := regionalGatewayStacks(ctx, tx, region)
	if err != nil {
		return out, err
	}
	out.declared = declared
	if err := checkNativeDefaultRouteReservation(ctx, tx, region); err != nil {
		return out, err
	}
	owners := map[string]bool{}
	for id, record := range stacks {
		for _, member := range record.Resources {
			if member.Type == ec2RTGateway || member.Type == ec2RTAttachment || member.Type == ec2Route {
				owners[id] = true
			}
		}
	}
	namespace := stackResourceNamespace(region, ec2RTGateway)
	var reservedClaim bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_cfn_ownership
			WHERE namespace=? AND physical_id GLOB ?)`, namespace, nativeInternetGatewayLinkPrefix+"*").Scan(&reservedClaim); err != nil {
		return out, err
	}
	if reservedClaim {
		return out, errEC2InternetGatewayState
	}
	rows, err := tx.QueryContext(ctx, `SELECT stack_id FROM aws_cfn_ownership WHERE namespace=? AND kind IN (?,?,?)
		UNION SELECT stack_id FROM aws_ec2_network_resources WHERE region=?
		AND (kind IN (?,?,?) OR id GLOB ? OR id GLOB ?) ORDER BY 1 LIMIT 1001`,
		namespace, ec2RTGateway, ec2RTAttachment, ec2Route, region, ec2RTGateway, ec2RTAttachment, ec2Route,
		nativeInternetGatewayLinkPrefix+"*", nativeDefaultRoutePrefix+"*")
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var owner string
		if err = rows.Scan(&owner); err != nil {
			rows.Close()
			return out, err
		}
		owners[owner] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(owners) > 1000 {
		return out, errEC2InternetGatewayState
	}
	vpcUsed := map[string]bool{}
	appendGateways := func(rows []ec2InternetGateway) error {
		for _, row := range rows {
			for _, link := range row.Attachments {
				if vpcUsed[link.VPCID] {
					return errEC2InternetGatewayState
				}
				vpcUsed[link.VPCID] = true
			}
			out.rows = append(out.rows, row)
		}
		return nil
	}
	count := 0
	for _, owner := range slices.Sorted(maps.Keys(owners)) {
		network, err := networkList(ctx, tx, region, owner)
		if err != nil {
			return out, err
		}
		count += len(network)
		if count > 1000 {
			return out, errEC2InternetGatewayState
		}
		if owner == ec2NativeOwner {
			out.links, err = readNativeInternetGatewayLinks(ctx, tx, region, network, declared)
			if err != nil {
				return out, err
			}
			out.routes, err = nativeIPv4Routes(ctx, tx, region, network, declared, out.links)
			if err != nil {
				return out, err
			}
			for _, r := range network {
				if r.Kind == ec2RTGateway {
					if err := appendGateways([]ec2InternetGateway{internetGatewayProjection(r, out.links.byGateway[r.ID].VPCID)}); err != nil {
						return out, err
					}
				}
			}
			continue
		}
		for _, row := range network {
			if strings.HasPrefix(row.ID, nativeInternetGatewayLinkPrefix) {
				return out, errEC2InternetGatewayState
			}
		}
		stack, found := stacks[owner]
		if !found {
			return out, errEC2InternetGatewayState
		}
		claims, err := ec2RouteClaims(ctx, tx, region, owner)
		if err != nil {
			return out, err
		}
		gateways, err := projectCFNInternetGateways(stack, network, claims)
		if err != nil {
			return out, err
		}
		if err := appendGateways(gateways); err != nil {
			return out, err
		}
	}
	return out, nil
}

func projectCFNInternetGateways(stack StackRecord, network []EC2NetworkResource, claims map[string]string) ([]ec2InternetGateway, error) {
	// Reuse the same declared/claimed/committed graph check used by
	// DescribeRouteTables, including same-owner attachments and uniqueness.
	if _, err := projectEC2RouteTables(stack, network, claims); err != nil {
		return nil, err
	}
	attachment := map[string]string{}
	tags := map[string]map[string]string{}
	for _, member := range stack.Resources {
		if member.Type == ec2RTGateway {
			want, err := networkTags(member.Properties["Tags"])
			if err != nil {
				return nil, err
			}
			tags[member.PhysicalID] = want
		}
	}
	for _, r := range network {
		if r.Kind == ec2RTAttachment {
			attachment[r.GatewayID] = r.VPCID
		}
	}
	var out []ec2InternetGateway
	for _, r := range network {
		if r.Kind != ec2RTGateway {
			continue
		}
		if !validInternetGatewayRow(r) || !maps.Equal(tags[r.ID], r.Tags) {
			return nil, errEC2InternetGatewayState
		}
		out = append(out, internetGatewayProjection(r, attachment[r.ID]))
	}
	return out, nil
}
