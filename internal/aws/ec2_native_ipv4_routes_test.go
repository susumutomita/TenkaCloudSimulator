package aws

import (
	"encoding/xml"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func nativeIPv4TableRoutes(t *testing.T, f ec2VPCFixture, table, vpc, igw string, want ...string) {
	t.Helper()
	raw := f.expect(f.call("", igwRegion, nil, url.Values{"Action": {"DescribeRouteTables"}, "RouteTableId.1": {table}}), 200, "")
	var read ec2DescribeRouteTablesResponse
	if err := xml.Unmarshal([]byte(raw), &read); err != nil || len(read.Tables) != 1 || read.Tables[0].ID != table || read.Tables[0].VPC != vpc {
		t.Fatalf("invalid one-table projection: %s (%v)", raw, err)
	}
	var got []string
	localCount := 0
	local := expectsNativeLocalRoute(t, f, igwRegion, vpc)
	for _, route := range read.Tables[0].Routes {
		if route == local {
			localCount++
			continue
		}
		if route.Gateway != igw || route.State != "active" || route.Origin != "CreateRoute" {
			t.Fatalf("unexpected modeled route %+v", route)
		}
		got = append(got, route.CIDR)
	}
	if localCount != 1 || !slices.Equal(got, want) {
		t.Fatalf("canonical routes %v, want %v", got, want)
	}
}

func TestEC2NativeIPv4RoutesIndependentCanonicalKeys(t *testing.T) {
	x := newIGWLinkFaultFixture(t, true)
	table := nativeDefaultRouteTestTable(t, x.f, x.vpc)
	sibling := nativeDefaultRouteTestTable(t, x.f, x.vpc)
	create := func(t *testing.T, id, prefix string) {
		t.Helper()
		expectNativeDefaultRoute(t, x.f, "", igwRegion, ec2CreateNativeDefaultRoute, id, prefix, x.gateway)
	}
	remove := func(t *testing.T, id, prefix string) {
		t.Helper()
		expectNativeDefaultRoute(t, x.f, "", igwRegion, ec2DeleteNativeDefaultRoute, id, prefix, "")
	}
	create(t, table, "198.51.100.199/25")
	create(t, table, "198.51.100.111/24")
	create(t, sibling, "198.51.100.220/24")
	nativeIPv4TableRoutes(t, x.f, table, x.vpc, x.gateway, "198.51.100.0/24", "198.51.100.128/25")
	nativeIPv4TableRoutes(t, x.f, sibling, x.vpc, x.gateway, "198.51.100.0/24")

	// The signed base's default route uses the identical durable ID and is
	// independent of the newer keys, not migrated or rewritten on reopen.
	create(t, table, "203.0.113.99/0")
	var id string
	if err := x.f.stacks.db.QueryRow("SELECT id FROM aws_ec2_network_resources WHERE region=? AND id=?",
		igwRegion, nativeDefaultRouteID(table)).Scan(&id); err != nil || id != nativeDefaultRouteID(table) {
		t.Fatalf("legacy /0 identity changed: %s (%v)", id, err)
	}
	frozen := x.image(t)
	x.f.expect(x.f.call("", igwRegion, nil, defaultRouteQuery(ec2CreateNativeDefaultRoute, table, "198.51.100.253/24", x.gateway)), 400, "RouteAlreadyExists")
	x.f.expect(x.f.call("", igwRegion, nil, defaultRouteQuery(ec2DeleteNativeDefaultRoute, table, "198.51.100.241/24", "")), 400, "InvalidRoute.NotFound")
	for _, prefix := range []string{
		"10.81.0.0/16", "10.81.0.3/32", "10.80.0.0/15",
		"169.254.168.0/22", "169.254.169.1/32", "169.254.160.0/19",
	} {
		x.f.expect(x.f.call("", igwRegion, nil, defaultRouteQuery(ec2CreateNativeDefaultRoute, table, prefix, x.gateway)), 400, "InvalidParameterValue")
	}
	if !slices.Equal(frozen, x.image(t)) {
		t.Fatal("a refusal changed stored routes or parents")
	}
	remove(t, table, "198.51.100.0/24")
	nativeIPv4TableRoutes(t, x.f, table, x.vpc, x.gateway, ec2NativeDefaultCIDR, "198.51.100.128/25")
	nativeIPv4TableRoutes(t, x.f, sibling, x.vpc, x.gateway, "198.51.100.0/24")
	remove(t, table, ec2NativeDefaultCIDR)
	x.f.expect(x.f.call("", igwRegion, nil, nativeGatewayLinkQuery(ec2DetachInternetGateway, x.gateway, x.vpc)), 400, "DependencyViolation")
	x.f.expect(x.f.call("", igwRegion, nil, url.Values{"Action": {"DeleteRouteTable"}, "RouteTableId": {table}}), 400, "DependencyViolation")
	remove(t, table, "198.51.100.128/25")
	nativeIPv4TableRoutes(t, x.f, table, x.vpc, x.gateway)
	x.f.expect(x.f.call("", igwRegion, nil, nativeGatewayLinkQuery(ec2DetachInternetGateway, x.gateway, x.vpc)), 400, "DependencyViolation")
	remove(t, sibling, "198.51.100.0/24")
	nativeGatewayTestChange(t, x.f, "", ec2DetachInternetGateway, x.gateway, x.vpc)
}

func TestEC2NativeIPv4RouteBoundIsPerTableAndRecovers(t *testing.T) {
	x := newIGWLinkFaultFixture(t, true)
	table := nativeDefaultRouteTestTable(t, x.f, x.vpc)
	sibling := nativeDefaultRouteTestTable(t, x.f, x.vpc)
	var want []string
	for n := range EC2NativeIPv4RouteLimit {
		prefix := fmt.Sprintf("203.0.113.%d/32", n)
		expectNativeDefaultRoute(t, x.f, "", igwRegion, ec2CreateNativeDefaultRoute, table, prefix, x.gateway)
		want = append(want, prefix)
	}
	nativeIPv4TableRoutes(t, x.f, table, x.vpc, x.gateway, want...)
	const spare = "203.0.113.100/32"
	before := x.image(t)
	x.f.expect(x.f.call("", igwRegion, nil, defaultRouteQuery(ec2CreateNativeDefaultRoute, table, spare, x.gateway)), 400, "RouteLimitExceeded")
	if !slices.Equal(before, x.image(t)) {
		t.Fatal("capacity refusal modified the bounded route graph")
	}
	expectNativeDefaultRoute(t, x.f, "", igwRegion, ec2CreateNativeDefaultRoute, sibling, spare, x.gateway)
	expectNativeDefaultRoute(t, x.f, "", igwRegion, ec2DeleteNativeDefaultRoute, table, want[0], "")
	expectNativeDefaultRoute(t, x.f, "", igwRegion, ec2CreateNativeDefaultRoute, table, spare, x.gateway)
	nativeIPv4TableRoutes(t, x.f, table, x.vpc, x.gateway, append(want[1:], spare)...)
	nativeIPv4TableRoutes(t, x.f, sibling, x.vpc, x.gateway, spare)

	// A durable over-bound state must not be silently trimmed to len=16 by
	// an exact selector, detach or mutation. This is a private SQLite fixture.
	const overflow = "203.0.113.200/32"
	key := nativeIPv4RouteID(table, netip.MustParsePrefix(overflow))
	x.f.writeNetwork(EC2NetworkResource{ID: key, Kind: ec2Route, Region: igwRegion, StackID: ec2NativeOwner,
		VPCID: x.vpc, RouteTableID: table, GatewayID: x.gateway, CIDR: overflow, RouteEnabled: true, Revision: 1})
	image := x.image(t)
	for _, q := range []url.Values{
		{"Action": {"DescribeRouteTables"}, "RouteTableId.1": {sibling}},
		defaultRouteQuery(ec2DeleteNativeDefaultRoute, table, overflow, ""),
		nativeGatewayLinkQuery(ec2DetachInternetGateway, x.gateway, x.vpc),
	} {
		raw := x.f.expect(x.f.call("", igwRegion, nil, q), 500, "InternalError")
		if strings.Contains(raw, "<return>") || strings.Contains(raw, "<routeTableSet>") {
			t.Fatalf("over-bound state was partially published: %s", raw)
		}
	}
	if !slices.Equal(image, x.image(t)) {
		t.Fatal("operation changed the rejected image")
	}
	x.exec(t, "DELETE FROM aws_ec2_network_resources WHERE region=? AND id=?", igwRegion, key)
	nativeIPv4TableRoutes(t, x.f, table, x.vpc, x.gateway, append(want[1:], spare)...)
}

func TestEC2NativeIPv4RouteScopedRollbackPreservesSibling(t *testing.T) {
	x := newIGWLinkFaultFixture(t, true)
	table := nativeDefaultRouteTestTable(t, x.f, x.vpc)
	const a, b = "192.0.2.0/24", "192.0.2.128/25"
	for _, cidr := range []string{a, b} {
		expectNativeDefaultRoute(t, x.f, "", igwRegion, ec2CreateNativeDefaultRoute, table, cidr, x.gateway)
	}
	// The storage fault would silently erase a different saved prefix if
	// post-delete readback verified only absence of the selected route.
	x.exec(t, fmt.Sprintf(`CREATE TRIGGER drop_unrelated_prefix AFTER DELETE ON aws_ec2_network_resources
		WHEN OLD.id=%q BEGIN DELETE FROM aws_ec2_network_resources WHERE region=OLD.region AND id=%q; END`,
		nativeIPv4RouteID(table, netip.MustParsePrefix(a)), nativeIPv4RouteID(table, netip.MustParsePrefix(b))))
	orig := x.image(t)
	x.f.expect(x.f.call("", igwRegion, nil, defaultRouteQuery(ec2DeleteNativeDefaultRoute, table, a, "")), 500, "InternalError")
	if !slices.Equal(orig, x.image(t)) {
		t.Fatal("mutation readback did not roll back loss of sibling prefix")
	}
	x.exec(t, "DROP TRIGGER drop_unrelated_prefix")
	nativeIPv4TableRoutes(t, x.f, table, x.vpc, x.gateway, a, b)
	expectNativeDefaultRoute(t, x.f, "", igwRegion, ec2DeleteNativeDefaultRoute, table, a, "")
	nativeIPv4TableRoutes(t, x.f, table, x.vpc, x.gateway, b)
}
