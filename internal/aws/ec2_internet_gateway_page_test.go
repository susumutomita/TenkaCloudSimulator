package aws

import (
	"context"
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

func gatewayPageContext(t *testing.T, credential, exercise string, role provider.CredentialRole) context.Context {
	t.Helper()
	return provider.WithActor(t.Context(), provider.Actor{CredentialIdentity: credential, ExerciseIdentity: exercise, CredentialRole: role})
}

func gatewayPageTestIDs(items []ec2InternetGateway) []string {
	var ids []string
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func TestEC2InternetGatewayPagesScopedSnapshot(t *testing.T) {
	f := newEC2VPCFixture(t)
	seen := []string{}
	for n := range 13 {
		id := fmt.Sprintf("igw-%017x", n+1)
		f.writeNetwork(EC2NetworkResource{ID: id, Region: igwRegion, Kind: ec2RTGateway, StackID: ec2NativeOwner, Revision: 1})
		seen = append(seen, id)
	}
	set := func(name string, grant bool) {
		t.Helper()
		effect := "Allow"
		if !grant {
			effect = "Deny"
		}
		p, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"` + effect + `","Action":"ec2:DescribeInternetGateways","Resource":"*"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err = f.g.Identities.Set(name, p); err != nil {
			t.Fatal(err)
		}
	}
	set("reader-one", true)
	set("reader-two", true)
	allowed := gatewayPageContext(t, core.Administrator, "reader-one", provider.CredentialOperator)
	other := gatewayPageContext(t, core.Administrator, "reader-two", provider.CredentialOperator)
	differentRole := gatewayPageContext(t, "reader-one", "reader-one", provider.CredentialParticipant)
	read := func(principal, region string, ctx context.Context, fields url.Values) ec2InternetGatewayResponse {
		t.Helper()
		fields.Set("Action", ec2DescribeInternetGateway)
		return internetGatewayTestXML(t, f.expect(f.call(principal, region, ctx, fields), 200, ""), ec2DescribeInternetGateway)
	}
	first := read("reader-one", igwRegion, allowed, url.Values{"MaxResults": {"5"}})
	if len(first.NextToken) != ec2GatewayPageWireLen ||
		!slices.Equal(gatewayPageTestIDs(first.Gateways.Items), seen[:5]) {
		t.Fatalf("unexpected first page: %+v", first)
	}
	second := read("reader-one", igwRegion, allowed, url.Values{"NextToken": {first.NextToken}})
	if !slices.Equal(gatewayPageTestIDs(second.Gateways.Items), seen[5:10]) ||
		len(second.NextToken) != ec2GatewayPageWireLen || second.NextToken == first.NextToken {
		t.Fatalf("unexpected second page: %+v", second)
	}
	last := read("reader-one", igwRegion, allowed, url.Values{"MaxResults": {"5"}, "NextToken": {second.NextToken}})
	if last.NextToken != "" || !slices.Equal(gatewayPageTestIDs(last.Gateways.Items), seen[10:]) {
		t.Fatalf("unexpected final page: %+v", last)
	}
	all := read("reader-one", igwRegion, nil, url.Values{})
	if !reflect.DeepEqual(all.Gateways.Items, append(append(first.Gateways.Items, second.Gateways.Items...), last.Gateways.Items...)) || all.NextToken != "" {
		t.Fatal("paged and pre-existing unpaginated view differ")
	}

	token := func() url.Values {
		return url.Values{"Action": {ec2DescribeInternetGateway}, "NextToken": {first.NextToken}}
	}
	f.expect(f.call("reader-one", igwOther, allowed, token()), 400, "InvalidNextToken")
	f.expect(f.call("reader-two", igwRegion, other, token()), 400, "InvalidNextToken")
	f.expect(f.call("reader-one", igwRegion, differentRole, token()), 400, "InvalidNextToken")
	f.expect(f.call("reader-one", igwRegion, nil, token()), 403, "UnauthorizedOperation")
	set("reader-one", false)
	f.expect(f.call("reader-one", igwRegion, allowed, token()), 403, "UnauthorizedOperation")
	set("reader-one", true)
	q := token()
	q.Set("MaxResults", "6")
	f.expect(f.call("reader-one", igwRegion, allowed, q), 400, "InvalidNextToken")
	q = token()
	q.Set("InternetGatewayId.1", seen[0])
	f.expect(f.call("reader-one", igwRegion, allowed, q), 400, "InvalidParameterCombination")
	q = token()
	q.Set("NextToken", "x"+first.NextToken[1:])
	if q.Get("NextToken") == first.NextToken {
		q.Set("NextToken", "y"+first.NextToken[1:])
	}
	f.expect(f.call("reader-one", igwRegion, allowed, q), 400, "InvalidNextToken")

	// Retained rows are unchanged through refusals; a fresh adapter with the
	// same SQLite contents does not accept a prior process's key.
	old := f.g.Stacks
	newStore, err := NewStackStore(f.store.Database())
	if err != nil {
		t.Fatal(err)
	}
	f.g.Stacks = newStore
	f.expect(f.call("reader-one", igwRegion, allowed, token()), 400, "InvalidNextToken")
	f.g.Stacks = old
	if got := read("reader-one", igwRegion, nil, url.Values{}); !reflect.DeepEqual(got.Gateways.Items, all.Gateways.Items) {
		t.Fatal("scope refusals changed inventory")
	}
	f.expect(f.call("", igwRegion, nil, url.Values{"Action": {ec2DeleteInternetGateway}, "InternetGatewayId": {seen[0]}}), 200, "")
	f.expect(f.call("reader-one", igwRegion, allowed, token()), 400, "InvalidNextToken")
	fresh := read("reader-one", igwRegion, allowed, url.Values{"MaxResults": {"5"}})
	if !slices.Equal(gatewayPageTestIDs(fresh.Gateways.Items), seen[1:6]) || fresh.NextToken == first.NextToken {
		t.Fatalf("new snapshot not reflected: %+v", fresh)
	}
}

func TestEC2InternetGatewayPageBoundAndGrammar(t *testing.T) {
	for _, input := range []string{"", "4", "0", "-5", "+5", "5.00", "5e0", " 5", "0005", "2147483648", "1001"} {
		if _, err := parseEC2GatewayMaxResults(input); err == nil || err.code != "InvalidMaxResults" {
			t.Errorf("unexpected MaxResults admission for %q: %v", input, err)
		}
	}
	for _, input := range []string{"5", "100", "1000"} {
		if n, err := parseEC2GatewayMaxResults(input); n < 5 || err != nil {
			t.Errorf("valid MaxResults %q rejected: %v", input, err)
		}
	}
	for _, query := range []url.Values{
		{"NextToken": {""}}, {"NextToken": {strings.Repeat("a", 49)}},
	} {
		if _, f := parseEC2InternetGateway(ec2DescribeInternetGateway, query); f == nil || f.code != "InvalidNextToken" {
			t.Fatalf("invalid NextToken representation was accepted")
		}
	}

	all := make([]ec2InternetGateway, ec2GatewayListLimit)
	for i := range all {
		all[i] = ec2InternetGateway{
			ID: fmt.Sprintf("igw-%017x", i), OwnerID: "000000000000", ManagedBy: ec2ManagedByCloudFormation,
			Attachments: []ec2GatewayAttachment{{VPCID: fmt.Sprintf("vpc-%017x", i), State: "available"}},
		}
		// Valid maximal UTF-8 byte lengths. Legal XML whitespace, quotes
		// and punctuation exercise the real encoder, not a guessed ratio.
		for k := range 50 {
			all[i].Tags = append(all[i].Tags, ec2Tag{Key: fmt.Sprintf("%02d", k) + strings.Repeat("\"", 126), Value: strings.Repeat("\t\n\r\"", 64)})
		}
	}
	pager := &ec2InternetGatewayPager{}
	witness := ec2InternetGatewayQuery{maxResults: 1000, caller: provider.Actor{
		CredentialIdentity: core.Administrator, ExerciseIdentity: core.Administrator, CredentialRole: provider.CredentialOperator}}
	start := 0
	for i := 0; i < 10; i++ {
		page, next, err := pager.page(igwRegion, witness, all)
		if err != nil || len(page) != EC2InternetGatewayPageLimit || (next == "") != (i == 9) {
			t.Fatalf("page %d invalid: %d items, terminal=%v, err=%v", i, len(page), next == "", err)
		}
		if page[0].ID != all[start].ID || page[len(page)-1].ID != all[start+len(page)-1].ID {
			t.Fatal("page discontinuity")
		}
		start += len(page)
		// Avoid repeated serialization; first page carries maximum tags
		// and a continuation, every fixture record has the same byte length.
		if i == 0 {
			wire := ec2InternetGatewayResponse{XMLName: xml.Name{Local: ec2DescribeInternetGateway + "Response"},
				XMLNS: igwXML, RequestID: "ec2-local-0000000000000000",
				Gateways: &ec2InternetGatewaySet{Items: page}, NextToken: next}
			var n countedXMLBytes
			if err := xml.NewEncoder(&n).Encode(wire); err != nil || n < 9<<20 || n >= 16<<20 {
				t.Fatalf("unexpected wire size: %d (%v)", n, err)
			}
			t.Logf("max-width %d-row sample serialized to %d bytes, below the unchanged 16777216 limit", len(page), n)
		}
		witness.nextToken = next
	}
	if start != ec2GatewayListLimit {
		t.Fatalf("missing tail: got %d", start)
	}
}

type countedXMLBytes int

func (n *countedXMLBytes) Write(p []byte) (int, error) {
	*n += countedXMLBytes(len(p))
	return len(p), nil
}
