package simpledb

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type observedSelect struct {
	Items []struct {
		Name struct {
			Text     string `xml:",chardata"`
			Encoding string `xml:"encoding,attr"`
		}
		Attr []struct{ Name, Value string } `xml:"Attribute"`
	} `xml:"SelectResult>Item"`
	Next string `xml:"SelectResult>NextToken"`
	Code string `xml:"Errors>Error>Code"`
}

func selectCall(t *testing.T, h http.Handler, expr string, extra ...string) (int, observedSelect) {
	t.Helper()
	v := params("Select", map[string]string{"SelectExpression": expr})
	for i := 0; i < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}
	w := callHTTP(h, v)
	var x observedSelect
	if err := xml.Unmarshal(w.Body.Bytes(), &x); err != nil {
		t.Fatal(err)
	}
	return w.Code, x
}
func TestBoundedSelectEndToEnd(t *testing.T) {
	s, _ := fixture(t)
	sc := Scope{"local", "us-east-1"}
	ctx := context.Background()
	allow := false
	var action, arn string
	h := NewHTTPHandler(s, sc, func(_ context.Context, a, r string) bool { action, arn = a, r; return allow })
	if err := s.CreateDomain(ctx, sc.Account, sc.Region, "my-domain"); err != nil {
		t.Fatal(err)
	}
	for i, n := range []string{"A", "B", "C"} {
		vals := []string{"other"}
		if i != 1 {
			vals = append(vals, "it's red")
		}
		if err := s.Put(ctx, sc.Account, sc.Region, "my-domain", n, map[string][]string{"color": vals, "label": {"<ok>"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	q := "select * from `my-domain` where color = 'it''s red' limit 1"
	if st, x := selectCall(t, h, q); st != 403 || x.Code != "AccessDenied" || action != "sdb:Select" || arn != "arn:aws:sdb:us-east-1:local:domain/my-domain" {
		t.Fatalf("authority %d %s %s %s", st, x.Code, action, arn)
	}
	allow = true
	st, x := selectCall(t, h, q, "ConsistentRead", "true")
	if st != 200 || len(x.Items) != 1 || x.Items[0].Name.Text != "A" || len(x.Items[0].Attr) != 3 || x.Next == "" {
		t.Fatalf("first %d %#v", st, x)
	}
	token := x.Next
	st, x = selectCall(t, h, q, "NextToken", token, "ConsistentRead", "true")
	if st != 200 || len(x.Items) != 1 || x.Items[0].Name.Text != "C" || x.Next != "" {
		t.Fatalf("next %d %#v", st, x)
	}
	for _, p := range [][]string{{q, "NextToken", token}, {q + " ", "ConsistentRead", "true", "NextToken", token}, {q, "ConsistentRead", "true", "NextToken", token + "x"}} {
		if st, x := selectCall(t, h, p[0], p[1:]...); st != 400 || x.Code != "InvalidNextToken" {
			t.Fatal(st, x)
		}
	}
	allow = false
	if st, x := selectCall(t, h, q, "NextToken", token, "ConsistentRead", "true"); st != 403 || x.Code != "AccessDenied" {
		t.Fatal(st, x)
	}
	allow = true
	st, x = selectCall(t, h, "SELECT itemName() FROM `my-domain` WHERE itemName() >= 'B' ORDER BY itemName() DESC LIMIT 2")
	if st != 200 || len(x.Items) != 2 || x.Items[0].Name.Text != "C" || x.Items[1].Name.Text != "B" || len(x.Items[0].Attr) != 0 {
		t.Fatal(st, x)
	}
}
func TestBoundedSelectStrictGrammarAndScope(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	for _, sc := range []Scope{{"a", "r"}, {"b", "r"}, {"a", "other"}} {
		if err := s.CreateDomain(ctx, sc.Account, sc.Region, "Sample"); err != nil {
			t.Fatal(err)
		}
		if err := s.Put(ctx, sc.Account, sc.Region, "Sample", sc.Account+"/"+sc.Region, map[string][]string{"k": {"v"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	h := NewHTTPHandler(s, Scope{"a", "r"}, func(context.Context, string, string) bool { return true })
	if st, x := selectCall(t, h, "select * from Sample"); st != 200 || len(x.Items) != 1 || x.Items[0].Name.Text != "a/r" {
		t.Fatal(st, x)
	}
	for _, q := range []string{"", "select count(*) from Sample", "select * from missing domain", "select * from Sample where k like 'v%'", "select * from Sample OR ignored", "select * from Sample; select * from Sample", "select * from Sample where k = 'v' or other = 'x'", "select * from 123", "select * from where", "select * from Sample where k = 'bad", "select * from Sample where k > 'v'"} {
		if st, x := selectCall(t, h, q); st != 400 || x.Code != "InvalidQueryExpression" {
			t.Fatal(q, st, x)
		}
	}
	for _, q := range []string{"select * from Sample order by itemName()", "select * from Sample where k = 'v' order by k"} {
		if st, x := selectCall(t, h, q); st != 400 || x.Code != "InvalidSortExpression" {
			t.Fatal(q, st, x)
		}
	}
	for _, x := range []struct {
		p    url.Values
		code string
	}{{params("Select", nil), "MissingParameter"}, {params("Select", map[string]string{"SelectExpression": "select * from Sample", "DomainName": "other"}), "InvalidParameterValue"}, {params("Select", map[string]string{"SelectExpression": "select * from Sample", "ConsistentRead": "TRUE"}), "InvalidParameterValue"}, {params("Select", map[string]string{"SelectExpression": "select * from Sample limit 2501"}), "InvalidParameterValue"}, {params("Select", map[string]string{"SelectExpression": "select * from Missing"}), "NoSuchDomain"}} {
		w := callHTTP(h, x.p)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "<Code>"+x.code+"</Code>") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestBoundedSelectScanPagesAndEncodedNames(t *testing.T) {
	s, _ := fixture(t)
	sc := Scope{"a", "r"}
	ctx := context.Background()
	if err := s.CreateDomain(ctx, "a", "r", "Sample"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 145; i++ {
		if err := s.Put(ctx, "a", "r", "Sample", fmt.Sprintf("%03d", i), map[string][]string{"tag": {"no"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	name := "last\x00"
	if err := s.Put(ctx, "a", "r", "Sample", name, map[string][]string{"tag": {"yes"}}, nil); err != nil {
		t.Fatal(err)
	}
	h := NewHTTPHandler(s, sc, func(context.Context, string, string) bool { return true })
	q := "select itemName() from Sample where tag = 'yes'"
	token := ""
	count := 0
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("cursor did not progress")
		}
		args := []string{}
		if token != "" {
			args = []string{"NextToken", token}
		}
		st, x := selectCall(t, h, q, args...)
		if st != 200 {
			t.Fatal(st, x)
		}
		if pages == 0 && (len(x.Items) != 0 || x.Next == "") {
			t.Fatal("missing bounded empty page", x)
		}
		for _, item := range x.Items {
			if item.Name.Encoding != "base64" || item.Name.Text != base64.StdEncoding.EncodeToString([]byte(name)) {
				t.Fatal(item)
			}
			count++
		}
		if x.Next == "" {
			break
		}
		if token == x.Next {
			t.Fatal("stuck")
		}
		token = x.Next
	}
	if count != 1 {
		t.Fatal(count)
	}
}

func TestSelectContinuationScopesAndReopen(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	q := "select itemName() from domain limit 1"
	makeHandler := func(s *Store, a, r string) http.Handler {
		return NewHTTPHandler(s, Scope{a, r}, func(context.Context, string, string) bool { return true })
	}
	for _, d := range []string{"domain", "second"} {
		if e := s.CreateDomain(ctx, "a", "r", d); e != nil {
			t.Fatal(e)
		}
	}
	names := []string{"a\x00b", "a\x00c", "a\x00d"}
	for _, n := range names {
		if e := s.Put(ctx, "a", "r", "domain", n, map[string][]string{"a`b": {"it's \"fine\""}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	h := makeHandler(s, "a", "r")
	status, x := selectCall(t, h, q)
	if status != 200 || x.Next == "" {
		t.Fatal(status, x)
	}
	originalToken := x.Next
	token := originalToken
	for _, sc := range []Scope{{"peer", "r"}, {"a", "other"}} {
		if st, x := selectCall(t, makeHandler(s, sc.Account, sc.Region), q, "NextToken", token); st != 400 || x.Code != "InvalidNextToken" {
			t.Fatal(sc, st, x)
		}
	}
	_, listToken, e := s.ListDomains(ctx, "a", "r", 1, "")
	if e != nil || listToken == "" {
		t.Fatal(e, listToken)
	}
	if _, _, e = s.ListDomains(ctx, "a", "r", 1, token); e != storeError("InvalidNextToken") {
		t.Fatal(e)
	}
	if st, x := selectCall(t, h, q, "NextToken", listToken); st != 400 || x.Code != "InvalidNextToken" {
		t.Fatal(st, x)
	}
	if st, x := selectCall(t, h, strings.Replace(q, "domain", "second", 1), "NextToken", token); st != 400 || x.Code != "InvalidNextToken" {
		t.Fatal(st, x)
	}
	// NUL-bearing ordered keys must advance without truncation.
	for i, want := range names {
		args := []string{}
		if i > 0 {
			args = []string{"NextToken", token}
		}
		st, x := selectCall(t, h, q, args...)
		if st != 200 || len(x.Items) != 1 || x.Items[0].Name.Text != base64.StdEncoding.EncodeToString([]byte(want)) || (x.Next == "") != (i == len(names)-1) {
			t.Fatal(i, st, x)
		}
		token = x.Next
	}
	fresh, e := NewStore(db)
	if e != nil {
		t.Fatal(e)
	}
	freshH := makeHandler(fresh, "a", "r")
	if st, x := selectCall(t, freshH, q, "NextToken", originalToken); st != 400 || x.Code != "InvalidNextToken" {
		t.Fatal(st, x)
	}
	if st, x := selectCall(t, freshH, "select * from domain where `a``b` = \"it's \"\"fine\"\"\""); st != 200 || len(x.Items) != 3 {
		t.Fatal(st, x)
	}
}

func TestSelectXMLResponseBudget(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	if e := s.CreateDomain(ctx, "a", "r", "domain"); e != nil {
		t.Fatal(e)
	}
	save := func(name string, n int) {
		var v []string
		for i := 0; i < n; i++ {
			v = append(v, fmt.Sprintf("%03d", i)+strings.Repeat("\"", 1018))
		}
		if e := s.Put(ctx, "a", "r", "domain", name, map[string][]string{"txt": v}, nil); e != nil {
			t.Fatal(e)
		}
	}
	save("A", 156)
	save("B", 156)
	save("C", 254)
	h := NewHTTPHandler(s, Scope{"a", "r"}, func(context.Context, string, string) bool { return true })
	q := "select * from domain limit 2500"
	token := ""
	for _, name := range []string{"A", "B"} {
		p := params("Select", map[string]string{"SelectExpression": q})
		if token != "" {
			p.Set("NextToken", token)
		}
		w := callHTTP(h, p)
		var x observedSelect
		if e := xml.Unmarshal(w.Body.Bytes(), &x); e != nil {
			t.Fatal(e)
		}
		if w.Code != 200 || w.Body.Len() > 1_000_000 || len(x.Items) != 1 || x.Items[0].Name.Text != name || x.Next == "" {
			t.Fatalf("want %s, status=%d bytes=%d items=%d next=%t error=%s", name, w.Code, w.Body.Len(), len(x.Items), x.Next != "", x.Code)
		}
		token = x.Next
	}
	if st, x := selectCall(t, h, q, "NextToken", token); st != 400 || x.Code != "ItemTooLargeForSelect" {
		t.Fatal(st, x.Code)
	}
	if st, x := selectCall(t, h, "select itemName() from domain"); st != 200 || len(x.Items) != 3 {
		t.Fatal(st, x)
	}
}
