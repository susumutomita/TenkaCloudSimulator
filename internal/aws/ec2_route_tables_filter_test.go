package aws

import (
	"net/url"
	"slices"
	"testing"
)

func ec2TestSubnetRouteFilter(subnet string) url.Values {
	return url.Values{"Filter.1.Name": {"association.subnet-id"}, "Filter.1.Value.1": {subnet}}
}

func TestEC2RouteTableSubnetFilterAdmission(t *testing.T) {
	a, b := "subnet-01234567", "subnet-0123456789abcdef0"
	named := []string{"association.subnet-id"}
	for _, tc := range []struct {
		name   string
		fields url.Values
		ids    []string
		code   string
	}{
		{"query without filter", url.Values{"Action": {"DescribeRouteTables"}, "Version": {"2016-11-15"}}, nil, ""},
		{"one local ID", ec2TestSubnetRouteFilter(a), []string{a}, ""},
		{"multiple alternatives", url.Values{"Filter.1.Name": named, "Filter.1.Value.2": {b}, "Filter.1.Value.1": {a}}, []string{a, b}, ""},
		{"duplicated named member", url.Values{"Filter.1.Name": {named[0], named[0]}}, nil, "InvalidParameterValue"},
		{"unknown parameter", url.Values{"RouteTableId.1": {"rtb-0123456789abcdef0"}}, nil, "UnsupportedOperation"},
		{"unknown filter", url.Values{"Filter.1.Name": {"vpc-id"}}, nil, "UnsupportedOperation"},
		{"multiple filter groups", url.Values{"Filter.2.Name": named}, nil, "UnsupportedOperation"},
		{"without values", url.Values{"Filter.1.Name": named}, nil, "InvalidParameterValue"},
		{"without name", url.Values{"Filter.1.Value.1": {a}}, nil, "InvalidParameterValue"},
		{"index gap", url.Values{"Filter.1.Name": named, "Filter.1.Value.1": {a}, "Filter.1.Value.3": {b}}, nil, "InvalidParameterValue"},
		{"above bound", url.Values{"Filter.1.Name": named, "Filter.1.Value.9": {a}}, nil, "InvalidParameterValue"},
		{"zero index", url.Values{"Filter.1.Name": named, "Filter.1.Value.0": {a}}, nil, "InvalidParameterValue"},
		{"noncanonical index", url.Values{"Filter.1.Name": named, "Filter.1.Value.01": {a}}, nil, "InvalidParameterValue"},
		{"malformed ID", ec2TestSubnetRouteFilter("subnet-*"), nil, "InvalidSubnetID.Malformed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, failure := parseEC2RouteTableSubnetFilter(tc.fields)
			if tc.code != "" {
				if failure == nil || failure.code != tc.code || failure.status != 400 || len(got) != 0 {
					t.Fatalf("expected refusal %s, got %v / %+v", tc.code, failure, got)
				}
				return
			}
			if failure != nil || !slices.Equal(got, tc.ids) {
				t.Fatalf("filter %#v: %v", got, failure)
			}
		})
	}
}

func TestEC2RouteTableSubnetFilterRequiresExplicitMatch(t *testing.T) {
	const selected = "subnet-0123456789abcdef0"
	rows := []ec2RouteTableRead{
		{ID: "bare"},
		{ID: "different", Associations: []ec2RouteAssociationRead{{Subnet: "other-subnet"}}},
		{ID: "main", Associations: []ec2RouteAssociationRead{{Subnet: selected, Main: true}}},
		{ID: "match", Routes: []ec2RouteRead{{CIDR: "0.0.0.0/0"}},
			Associations: []ec2RouteAssociationRead{{Subnet: selected}, {Subnet: "another-subnet"}}},
	}
	kept := filterEC2RouteTablesOnSubnet(slices.Clone(rows), []string{selected})
	if len(kept) != 1 || kept[0].ID != "match" || len(kept[0].Associations) != 2 || len(kept[0].Routes) != 1 {
		t.Fatalf("filter did not return the whole matching route table: %+v", kept)
	}
	if all := filterEC2RouteTablesOnSubnet(rows, nil); len(all) != len(rows) {
		t.Fatalf("unfiltered regional read changed: %+v", all)
	}
}
