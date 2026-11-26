package aws

import (
	"context"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

func TestEC2RouteTablePagesAcrossOwnersAndScope(t *testing.T) {
	f := newEC2VPCFixture(t)
	vpc := f.create("", url.Values{"CidrBlock": {"10.71.0.0/16"}}).ID
	stack, err := compileStack(igwRegion, "regional-route-page", `{"Resources":{
		"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.72.0.0/16"}},
		"Table":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"Vpc"}}},
		"Subnet":{"Type":"AWS::EC2::Subnet","Properties":{"VpcId":{"Ref":"Vpc"},"CidrBlock":"10.72.1.0/24"}},
		"Association":{"Type":"AWS::EC2::SubnetRouteTableAssociation","Properties":{"RouteTableId":{"Ref":"Table"},"SubnetId":{"Ref":"Subnet"}}}
	}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stacks.Create(stack); err != nil {
		t.Fatal(err)
	}
	var cfnTable, cfnSubnet string
	for _, r := range stack.Resources {
		switch r.Type {
		case ec2RT:
			cfnTable = r.PhysicalID
		case ec2RTSubnet:
			cfnSubnet = r.PhysicalID
		}
	}
	var ids, native, subnets []string
	ids, subnets = append(ids, cfnTable), append(subnets, cfnSubnet)
	for n := range 11 {
		table := nativeDefaultRouteTestTable(t, f, vpc)
		native, ids = append(native, table), append(ids, table)
		if n >= 6 {
			continue
		}
		var made ec2CreateSubnetResponse
		call := f.call("", igwRegion, nil, url.Values{"Action": {"CreateSubnet"}, "VpcId": {vpc}, "CidrBlock": {fmt.Sprintf("10.71.%d.0/24", n+1)}})
		if err := xml.Unmarshal([]byte(f.expect(call, 200, "")), &made); err != nil {
			t.Fatal(err)
		}
		subnets = append(subnets, made.Subnet.ID)
		f.expect(f.call("", igwRegion, nil, url.Values{"Action": {"AssociateRouteTable"}, "RouteTableId": {table}, "SubnetId": {made.Subnet.ID}}), 200, "")
	}
	slices.Sort(ids)
	identity := func(name, effect string) {
		t.Helper()
		p, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"` + effect + `","Action":"ec2:DescribeRouteTables","Resource":"*"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.g.Identities.Set(name, p); err != nil {
			t.Fatal(err)
		}
	}
	const reader, second = "route-page-reader", "route-page-other"
	identity(reader, "Allow")
	identity(second, "Allow")
	auth := gatewayPageContext(t, core.Administrator, reader, provider.CredentialOperator)
	call := func(principal, region string, ctx context.Context, q url.Values, status int, code string) string {
		t.Helper()
		q.Set("Action", "DescribeRouteTables")
		body := f.expect(f.call(principal, region, ctx, q), status, code)
		if status != 200 && (strings.Contains(body, "<routeTableSet>") || strings.Contains(body, "<nextToken>")) {
			t.Fatal("failed page returned a successful prefix or token")
		}
		return body
	}
	read := func(q url.Values) ec2DescribeRouteTablesResponse {
		t.Helper()
		var out ec2DescribeRouteTablesResponse
		data := call(reader, igwRegion, auth, q, 200, "")
		if err := xml.Unmarshal([]byte(data), &out); err != nil || out.RequestID == "" ||
			!strings.Contains(data, "<routeTableSet>") || (out.NextToken == "") == strings.Contains(data, "<nextToken>") {
			t.Fatalf("incorrect describe envelope: %v (%s)", err, data)
		}
		return out
	}
	getIDs := func(rows []ec2RouteTableRead) []string {
		var ids []string
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		return ids
	}
	unpaged := read(url.Values{})
	first := read(url.Values{"MaxResults": {"5"}})
	next := func(token string) url.Values { return url.Values{"NextToken": {token}} }
	mid := read(next(first.NextToken))
	last := read(next(mid.NextToken))
	if unpaged.NextToken != "" || len(first.NextToken) != ec2RouteTablePageWireLen ||
		len(mid.NextToken) != ec2RouteTablePageWireLen || mid.NextToken == first.NextToken || last.NextToken != "" ||
		!slices.Equal(getIDs(first.Tables), ids[:5]) || !slices.Equal(getIDs(mid.Tables), ids[5:10]) ||
		!slices.Equal(getIDs(last.Tables), ids[10:]) ||
		!reflect.DeepEqual(slices.Concat(first.Tables, mid.Tables, last.Tables), unpaged.Tables) {
		t.Fatal("paginated reads did not reproduce the exact legacy mixed snapshot")
	}
	exact := read(url.Values{"RouteTableId.1": {ids[6]}})
	if exact.NextToken != "" || !reflect.DeepEqual(exact.Tables, unpaged.Tables[6:7]) {
		t.Fatal("single-ID readback changed")
	}
	filter := func(members []string, token string) url.Values {
		q := url.Values{"Filter.1.Name": {"association.subnet-id"}, "MaxResults": {"5"}}
		for n, id := range members {
			q.Set(fmt.Sprintf("Filter.1.Value.%d", n+1), id)
		}
		if token != "" {
			q.Set("NextToken", token)
		}
		return q
	}
	projected := append(slices.Clone(native[:6]), cfnTable)
	slices.Sort(projected)
	firstFiltered := read(filter(subnets, ""))
	reshuffled := slices.Clone(subnets)
	slices.Reverse(reshuffled)
	reshuffled = append(reshuffled, subnets[0])
	lastFiltered := read(filter(reshuffled, firstFiltered.NextToken))
	if len(firstFiltered.NextToken) != ec2RouteTablePageWireLen || lastFiltered.NextToken != "" ||
		!slices.Equal(getIDs(slices.Concat(firstFiltered.Tables, lastFiltered.Tables)), projected) {
		t.Fatal("filtered pages differ from their seven explicit associations")
	}

	call(reader, igwRegion, auth, next(firstFiltered.NextToken), 400, "InvalidNextToken")
	call(reader, igwRegion, auth, filter(subnets[:6], firstFiltered.NextToken), 400, "InvalidNextToken")
	call(reader, igwRegion, auth, filter(subnets, first.NextToken), 400, "InvalidNextToken")
	q := next(first.NextToken)
	q.Set("MaxResults", "6")
	call(reader, igwRegion, auth, q, 400, "InvalidNextToken")
	call(reader, igwOther, auth, next(first.NextToken), 400, "InvalidNextToken")
	call(second, igwRegion, gatewayPageContext(t, core.Administrator, second, provider.CredentialOperator), next(first.NextToken), 400, "InvalidNextToken")
	call(reader, igwRegion, gatewayPageContext(t, reader, reader, provider.CredentialParticipant), next(first.NextToken), 400, "InvalidNextToken")
	call(reader, igwRegion, nil, next(first.NextToken), 403, "UnauthorizedOperation")
	scoped := context.WithValue(auth, consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: igwRegion, instance: "i-fixture"})
	call(reader, igwRegion, scoped, next(first.NextToken), 403, "UnauthorizedOperation")
	identity(reader, "Deny")
	call(reader, igwRegion, auth, next(first.NextToken), 403, "UnauthorizedOperation")
	identity(reader, "Allow")

	freshStore, err := NewStackStore(f.store.Database())
	if err != nil {
		t.Fatal(err)
	}
	f.g.Stacks = freshStore
	call(reader, igwRegion, auth, next(first.NextToken), 400, "InvalidNextToken")
	f.g.Stacks = f.stacks

	// Removing an unfiltered peer still retires a filtered token, since each
	// page attests to a fresh verified regional snapshot, not custom storage.
	f.expect(f.call("", igwRegion, nil, url.Values{"Action": {"DeleteRouteTable"}, "RouteTableId": {native[10]}}), 200, "")
	call(reader, igwRegion, auth, filter(subnets, firstFiltered.NextToken), 400, "InvalidNextToken")
	call(reader, igwRegion, auth, next(first.NextToken), 400, "InvalidNextToken")
	var document []byte
	if err := f.stacks.db.QueryRow(`SELECT document FROM aws_ec2_network_resources WHERE region=? AND id=?`, igwRegion, native[9]).Scan(&document); err != nil {
		t.Fatal(err)
	}
	update := func(mark string) {
		t.Helper()
		_, err := f.stacks.db.Exec(`UPDATE aws_ec2_network_resources SET document=json_set(?, '$.Kind',?) WHERE region=? AND id=?`, document, mark, igwRegion, native[9])
		if err != nil {
			t.Fatal(err)
		}
	}
	newFiltered := read(filter(subnets, ""))
	update(ec2Assoc) // surviving inconsistent, unrelated local row; not a wire request
	call(reader, igwRegion, auth, filter(subnets, newFiltered.NextToken), 500, "InternalError")
	call(reader, igwRegion, auth, url.Values{"RouteTableId.1": {cfnTable}}, 500, "InternalError")
	update(ec2RT)
	if fresh := read(filter(subnets, newFiltered.NextToken)); !reflect.DeepEqual(fresh.Tables, lastFiltered.Tables) {
		t.Fatal("restored snapshot did not retain exact selected data")
	}
}

func TestEC2RouteTablePageGrammarAndWireBudget(t *testing.T) {
	for _, raw := range []string{"", "4", "101", "1000", "0005", "+5", "-5", "5.0", " 5", strings.Repeat("9", 1024)} {
		if _, failure := parseEC2RouteTableMaxResults(raw); failure == nil || failure.code != "InvalidMaxResults" {
			t.Errorf("invalid MaxResults admitted: %q (%v)", raw, failure)
		}
	}
	for _, raw := range []string{"5", "6", "99", "100"} {
		if value, failure := parseEC2RouteTableMaxResults(raw); failure != nil || fmt.Sprint(value) != raw {
			t.Errorf("valid MaxResults rejected: %q (%v)", raw, failure)
		}
	}
	for _, tc := range []struct {
		q    url.Values
		want string
	}{
		{url.Values{"NextToken": {""}}, "InvalidNextToken"},
		{url.Values{"NextToken": {strings.Repeat("x", 49)}}, "InvalidNextToken"},
		{url.Values{"MaxResults": {"5", "6"}}, "InvalidParameterValue"},
		{url.Values{"NextToken": {"x", "y"}}, "InvalidParameterValue"},
		{url.Values{"RouteTableId.1": {"rtb-01234567890123456"}, "NextToken": {strings.Repeat("a", ec2RouteTablePageWireLen)}}, "InvalidParameterCombination"},
		{url.Values{"MaxResults": {"5"}, "Filter.1.Name": {"vpc-id"}}, "UnsupportedOperation"},
		{url.Values{"MaxResults": {"5"}, "DryRun": {"true"}}, "UnsupportedOperation"},
	} {
		if _, failure := parseEC2RouteTableSelection(tc.q); failure == nil || failure.code != tc.want {
			t.Errorf("invalid description members: %+v (%v)", tc.q, failure)
		}
	}

	tags := make([]ec2Tag, 50)
	for i := range tags {
		tags[i] = ec2Tag{Key: fmt.Sprintf("%02d", i) + strings.Repeat("\"", 126), Value: strings.Repeat("\t\n\r\"", 64)}
	}
	tables := make([]ec2RouteTableRead, 101)
	for i := range tables {
		tables[i] = ec2RouteTableRead{ID: fmt.Sprintf("rtb-%017x", i), VPC: "vpc-11111111111111111", Owner: "000000000000", Tags: tags}
	}
	// A page with 100 maximum-width tables and all remaining admissible
	// child rows has the largest supported combined markup shape.
	for n := range ec2NativeRouteTableRowLimit - 1 - ec2RouteTableMaxPageSize - EC2NativeIPv4RouteLimit {
		tables[0].Associations = append(tables[0].Associations, ec2RouteAssociationRead{
			ID: fmt.Sprintf("rtbassoc-%017x", n), Table: tables[0].ID, Subnet: fmt.Sprintf("subnet-%017x", n),
		})
	}
	for i := range EC2NativeIPv4RouteLimit {
		tables[0].Routes = append(tables[0].Routes, ec2RouteRead{CIDR: fmt.Sprintf("203.0.113.%d/32", i), Gateway: "igw-11111111111111111", Origin: "CreateRoute", State: "active"})
	}
	r := &ec2RouteTablePager{}
	actor := provider.Actor{CredentialIdentity: core.Administrator, ExerciseIdentity: core.Administrator, CredentialRole: provider.CredentialOperator}
	snap := ec2RouteTableSnapshot(tables)
	out, err := r.page(igwRegion, ec2RouteTableQuery{maxResults: ec2RouteTableMaxPageSize}, actor, snap, tables)
	if err != nil || len(out.Tables) != ec2RouteTableMaxPageSize || len(out.NextToken) != ec2RouteTablePageWireLen {
		t.Fatalf("maximum configured page not available: %v", err)
	}
	wire, err := encodeEC2RouteTableBounded(out, false)
	if err != nil || len(wire) < 9<<20 || len(wire) >= ec2RouteTablePageBytes {
		t.Fatalf("real wire encoder out of bound: %d (%v)", len(wire), err)
	}
	t.Logf("bounded full page: %d bytes", len(wire))
	exact := &ec2DescribeRouteTablesResponse{Tables: tables[:1]}
	wire, err = encodeEC2RouteTableBounded(exact, true)
	if err != nil || len(wire) < 240000 || len(wire) >= ec2RouteTableExactBytes {
		t.Fatalf("real exact encoder out of bound: %d (%v)", len(wire), err)
	}
	t.Logf("bounded exact confirmation: %d bytes", len(wire))
	oversized := &ec2DescribeRouteTablesResponse{Tables: tables[:2]}
	oversized.Tables[1].Tags = []ec2Tag{{Key: "future-field", Value: strings.Repeat("a", ec2RouteTableExactBytes)}}
	if wire, err := encodeEC2RouteTableBounded(oversized, true); err == nil || wire != nil {
		t.Fatal("a future incompatible oversized projection would have begun a successful response")
	}

	badContinuations := []string{strings.Repeat("=", 48), strings.Repeat("a", 47) + "\n"}
	for _, pair := range [][2]uint16{{4, 5}, {101, 5}, {5, 0}, {5, 101}, {5, 6}} {
		var body [ec2RouteTablePageBodyLen]byte
		binary.BigEndian.PutUint16(body[:], pair[0])
		binary.BigEndian.PutUint16(body[2:], pair[1])
		// Seed internal impossible positions, authenticated by this unit's
		// own pager; no invalid credentials or external calls.
		packet := append(body[:], r.signature(igwRegion, actor, nil, snap, body[:])...)
		badContinuations = append(badContinuations, ec2RouteTablePageEncoding.EncodeToString(packet))
	}
	for _, token := range badContinuations {
		if page, err := r.page(igwRegion, ec2RouteTableQuery{nextToken: token}, actor, snap, tables); err == nil || page != nil {
			t.Fatal("an impossible position returned table data")
		}
	}
	if page, err := r.page(igwRegion, ec2RouteTableQuery{maxResults: 100}, actor, snap, make([]ec2RouteTableRead, 1001)); err == nil || page != nil {
		t.Fatal("accepted regional bound overflow")
	}
}
