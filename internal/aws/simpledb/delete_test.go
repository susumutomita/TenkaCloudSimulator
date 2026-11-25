package simpledb

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

// AWS Query 2009-04-15 DeleteAttributes: omitted value deletes the whole
// attribute; an explicit empty value deletes only that value. No attributes
// means delete the item. All these changes and the single Expected are atomic.
func TestDeleteAttributesNative(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	const account, region, domain, item = "acct", "us-east-1", "deletions", "row"
	allowed := true
	h := NewHTTPHandler(s, Scope{account, region}, func(_ context.Context, act, arn string) bool {
		if act != "sdb:DeleteAttributes" || arn != "arn:aws:sdb:us-east-1:acct:domain/"+domain {
			t.Fatalf("unexpected policy: %s %s", act, arn)
		}
		return allowed
	})
	if e := s.CreateDomain(ctx, account, region, domain); e != nil {
		t.Fatal(e)
	}
	seed := map[string][]string{"color": {"", "blue", "red"}, "gate": {"v1"}, "last": {"stay"}}
	if err := s.Put(ctx, account, region, domain, item, seed, nil); err != nil {
		t.Fatal(err)
	}
	check := func(extra url.Values, status int, code string) {
		t.Helper()
		v := params("DeleteAttributes", map[string]string{"DomainName": domain, "ItemName": item})
		for k, a := range extra {
			v[k] = a
		}
		w := callHTTP(h, v)
		var b struct {
			XMLName xml.Name
			Code    string `xml:"Errors>Error>Code"`
		}
		err := xml.Unmarshal(w.Body.Bytes(), &b)
		if err != nil || w.Code != status || b.Code != code || (status == http.StatusOK && b.XMLName.Local != "DeleteAttributesResponse") {
			t.Fatalf("%s: %d (%s), %v", v.Encode(), w.Code, w.Body.String(), err)
		}
	}
	read := func(want map[string][]string) {
		t.Helper()
		got, err := s.Get(ctx, account, region, domain, item, nil)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v (%v), want %v", got, err, want)
		}
	}
	check(url.Values{"Expected.Name": {"gate"}, "Expected.Value": {"old"}}, 409, "ConditionalCheckFailed")
	check(url.Values{"Expected.Name": {"missing"}, "Expected.Value": {"x"}}, 404, "AttributeDoesNotExist")
	check(url.Values{"Expected.Name": {"color"}, "Expected.Value": {"red"}}, 409, "MultiValuedAttribute")
	check(url.Values{"Attribute.0.Value": {"red"}}, 400, "MissingParameter")
	check(url.Values{"Attribute.1.Name": {"gate"}, "Attribute.1.Replace": {"true"}}, 400, "InvalidParameterValue")
	check(url.Values{"Expected.Exists": {"false", "true"}}, 400, "MultipleExistsConditions")
	read(seed)
	allowed = false
	check(url.Values{"Expected.Name": {"missing"}, "Expected.Value": {"x"}}, 403, "AccessDenied")
	allowed = true
	read(seed)
	// Invalid requests and stale checks above must never delete a row.
	check(url.Values{"Attribute.0.Name": {"color"}, "Attribute.0.Value": {""}, "Attribute.900.Name": {"color"}, "Attribute.900.Value": {"red"}, "Expected.Name": {"gate"}, "Expected.Value": {"v1"}}, 200, "")
	read(map[string][]string{"color": {"blue"}, "gate": {"v1"}, "last": {"stay"}})
	// Unspecified value dominates any exact-value entry for the same name.
	check(url.Values{"Attribute.0.Name": {"color"}, "Attribute.9.Name": {"color"}, "Attribute.9.Value": {"unknown"}, "Attribute.1.Name": {"gate"}, "Attribute.1.Value": {"v1"}, "Expected.Name": {"missing"}, "Expected.Exists": {"false"}}, 200, "")
	read(map[string][]string{"last": {"stay"}})
	check(url.Values{"Attribute.0.Name": {"unknown"}}, 200, "")
	if _, err := db.Exec("CREATE TRIGGER block_row BEFORE DELETE ON aws_simpledb_items BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	check(nil, 500, "InternalError")
	read(map[string][]string{"last": {"stay"}})
	if _, err := db.Exec("DROP TRIGGER block_row"); err != nil {
		t.Fatal(err)
	}
	check(url.Values{"Attribute.1.Name": {"last"}}, 200, "")
	check(nil, 200, "") // absent item is an idempotent success
	stats, err := s.DomainMetadata(ctx, account, region, domain)
	if err != nil || stats.ItemCount != 0 {
		t.Fatalf("phantom row %v %v", stats, err)
	}
	if err = s.Put(ctx, account, region, domain, item, seed, nil); err != nil {
		t.Fatal(err)
	}
	check(nil, 200, "")
	read(map[string][]string{})
}
