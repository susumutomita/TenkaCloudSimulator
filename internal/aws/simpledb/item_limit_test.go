package simpledb

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"reflect"
	"testing"
)

// https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/SDB_API_PutAttributes.html
// The item ceiling applies to the merged distinct name/value pairs, not the
// number of attributes in an individual request.
func TestItemPairLimitRejectsRacingConditionalGrowth(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	const account, region, domain, item = "local", "us-east-1", "versions", "row"
	if err := s.CreateDomain(ctx, account, region, domain); err != nil {
		t.Fatal(err)
	}
	values := make([]string, 255)
	for i := range values {
		values[i] = fmt.Sprintf("%03d", i)
	}
	if err := s.Put(ctx, account, region, domain, item, map[string][]string{"seed": values}, nil); err != nil {
		t.Fatal(err)
	}
	h := NewHTTPHandler(s, Scope{account, region}, func(context.Context, string, string) bool { return true })
	conditional := func(name string) url.Values {
		return params("PutAttributes", map[string]string{"DomainName": domain, "ItemName": item,
			"Attribute.1.Name": name, "Attribute.1.Value": "1", "Attribute.1.Replace": "true",
			"Expected.Name": name, "Expected.Exists": "false"})
	}
	start := make(chan struct{})
	type result struct {
		name   string
		status int
		code   string
	}
	results := make(chan result, 2)
	for _, name := range []string{"left", "right"} {
		go func() {
			<-start
			w := callHTTP(h, conditional(name))
			var body struct {
				Code string `xml:"Errors>Error>Code"`
			}
			_ = xml.Unmarshal(w.Body.Bytes(), &body)
			results <- result{name, w.Code, body.Code}
		}()
	}
	close(start)
	winner := ""
	for range 2 {
		got := <-results
		if got.status == 200 {
			if winner != "" {
				t.Fatal("both writers consumed the last slot")
			}
			winner = got.name
		} else if got.status != 409 || got.code != "NumberItemAttributesExceeded" {
			t.Fatalf("unexpected result: %+v", got)
		}
	}
	if winner == "" {
		t.Fatal("one writer must fill the last slot")
	}
	expected := map[string][]string{"seed": values, winner: {"1"}}
	mixed := params("PutAttributes", map[string]string{"DomainName": domain, "ItemName": item,
		"Attribute.1.Name": winner, "Attribute.1.Value": "must not persist", "Attribute.1.Replace": "true",
		"Attribute.2.Name": "seed", "Attribute.2.Value": "257th", "Expected.Name": winner, "Expected.Value": "1"})
	w := callHTTP(h, mixed)
	var body struct {
		Code string `xml:"Errors>Error>Code"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 409 || body.Code != "NumberItemAttributesExceeded" {
		t.Fatalf("mixed over-limit write: %d %s %v", w.Code, w.Body, err)
	}
	// Duplicate append at the exact ceiling is idempotent and does not grow it.
	if err := s.Put(ctx, account, region, domain, item, map[string][]string{"seed": {"000"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, account, region, domain, item, nil); err != nil || !reflect.DeepEqual(got, expected) {
		t.Fatalf("over-limit request changed data: %v %v", got, err)
	}
}

func TestItemPairLimitPermitsExistingItemRepair(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	const account, region, domain, item = "local", "us-east-1", "versions", "legacy"
	if err := s.CreateDomain(ctx, account, region, domain); err != nil {
		t.Fatal(err)
	}
	legacy := make([]string, 257)
	for i := range legacy {
		legacy[i] = fmt.Sprintf("%03d", i)
	}
	// Seed a row an earlier simulator could write, not a malformed record.
	state, err := json.Marshal(map[string][]string{"legacy": legacy, "version": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO aws_simpledb_items(account,region,domain,name,attributes) VALUES(?,?,?,?,?)",
		account, region, domain, item, string(state)); err != nil {
		t.Fatal(err)
	}
	if err = s.Put(ctx, account, region, domain, item, map[string][]string{"version": {"2"}}, map[string]bool{"version": true}); err != nil {
		t.Fatalf("non-growing legacy update: %v", err)
	}
	if err = s.Put(ctx, account, region, domain, item, map[string][]string{"new": {"1"}}, nil); err != storeError("NumberItemAttributesExceeded") {
		t.Fatalf("grew an oversized legacy item: %v", err)
	}
	if err = s.Put(ctx, account, region, domain, item, map[string][]string{"legacy": {"repaired"}}, map[string]bool{"legacy": true}); err != nil {
		t.Fatalf("cannot repair oversized item: %v", err)
	}
	expected := map[string][]string{"legacy": {"repaired"}, "version": {"2"}}
	if got, err := s.Get(ctx, account, region, domain, item, nil); err != nil || !reflect.DeepEqual(got, expected) {
		t.Fatalf("repaired values mismatch: %v %v", got, err)
	}
}
