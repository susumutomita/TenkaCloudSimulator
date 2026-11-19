package simpledb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func callHTTP(h http.Handler, p url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader(p.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func params(action string, p map[string]string) url.Values {
	v := url.Values{"Action": {action}, "Version": {"2009-04-15"}}
	for k, x := range p {
		v.Set(k, x)
	}
	return v
}
func TestHTTPFrozenVectors(t *testing.T) {
	s, _ := fixture(t)
	h := NewHTTPHandler(s, Scope{"local", "us-east-1"}, func(context.Context, string, string) bool { return true })
	var f struct {
		Domain, Item string
		Attributes   []struct{ Name, Value string }
		Negative     []struct {
			ID, Action string
			Parameters map[string]string
			Status     int
			Code       string
		} `json:"negative_cases"`
	}
	b, e := os.ReadFile("../../../test/factory/testdata/simpledb/oracle/fixtures.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	check := func(action string, p map[string]string, status int, code string) {
		t.Helper()
		w := callHTTP(h, params(action, p))
		if w.Code != status {
			t.Fatalf("%s got %d want %d: %s", action, w.Code, status, w.Body.String())
		}
		var x struct {
			XMLName xml.Name
			ID      string `xml:"ResponseMetadata>RequestId"`
			Code    string `xml:"Errors>Error>Code"`
		}
		if e := xml.Unmarshal(w.Body.Bytes(), &x); e != nil {
			t.Fatal(e)
		}
		if status == 200 && (x.XMLName.Local != action+"Response" || x.XMLName.Space != "http://sdb.amazonaws.com/doc/2009-04-15/" || x.ID == "") {
			t.Fatalf("envelope %+v", x)
		}
		if status != 200 && x.Code != code {
			t.Fatalf("code %q want %q", x.Code, code)
		}
	}
	check("CreateDomain", map[string]string{"DomainName": f.Domain}, 200, "")
	check("PutAttributes", map[string]string{"DomainName": f.Domain, "ItemName": f.Item, "Attribute.1.Name": "label", "Attribute.1.Value": "<&日本語>\""}, 200, "")
	for _, c := range f.Negative {
		t.Run(c.ID, func(t *testing.T) { check(c.Action, c.Parameters, c.Status, c.Code) })
	}
	w := callHTTP(h, params("GetAttributes", map[string]string{"DomainName": f.Domain, "ItemName": f.Item, "ConsistentRead": "true"}))
	var out struct {
		Values []string `xml:"GetAttributesResult>Attribute>Value"`
	}
	if e := xml.Unmarshal(w.Body.Bytes(), &out); e != nil || len(out.Values) != 1 || out.Values[0] != "<&日本語>\"" {
		t.Fatalf("atomic/escaped read %s %v", w.Body, e)
	}
	check("ListDomains", nil, 200, "")
	check("DeleteDomain", map[string]string{"DomainName": f.Domain}, 200, "")
	check("GetAttributes", map[string]string{"DomainName": f.Domain, "ItemName": f.Item}, 400, "NoSuchDomain")
}
func TestHTTPFailClosedAndStrict(t *testing.T) {
	s, _ := fixture(t)
	sc := Scope{"local", "us-east-1"}
	for _, auth := range []Authorize{nil, func(context.Context, string, string) bool { return false }} {
		w := callHTTP(NewHTTPHandler(s, sc, auth), params("CreateDomain", map[string]string{"DomainName": "denied"}))
		if w.Code != 403 {
			t.Fatalf("fail closed: %d", w.Code)
		}
	}
	h := NewHTTPHandler(s, sc, func(_ context.Context, action, resource string) bool {
		if action != "sdb:CreateDomain" || resource != "arn:aws:sdb:us-east-1:local:domain/valid" {
			t.Fatalf("authority %q %q", action, resource)
		}
		return true
	})
	for _, p := range []url.Values{
		{"Action": {"CreateDomain", "DeleteDomain"}, "Version": {"2009-04-15"}, "DomainName": {"valid"}},
		{"Action": {"CreateDomain"}, "Version": {"bad"}, "DomainName": {"valid"}},
		{"Action": {"CreateDomain"}, "Version": {"2009-04-15"}, "DomainName": {"valid"}, "Unexpected": {"x"}},
	} {
		if w := callHTTP(h, p); w.Code != 400 {
			t.Fatalf("strict reject %d", w.Code)
		}
	}
	if w := callHTTP(h, params("CreateDomain", map[string]string{"DomainName": "valid"})); w.Code != 200 {
		t.Fatal(w.Body)
	}
	ds, _, e := s.ListDomains(context.Background(), sc.Account, sc.Region, 100, "")
	if e != nil || len(ds) != 1 || ds[0] != "valid" {
		t.Fatal(ds, e)
	}
}

func TestHTTPPaginationReplacementAndScope(t *testing.T) {
	s, _ := fixture(t)
	h := NewHTTPHandler(s, Scope{"local", "us-east-1"}, func(context.Context, string, string) bool { return true })
	call := func(action string, p map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		w := callHTTP(h, params(action, p))
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		return w
	}
	for _, d := range []string{"aaa", "bbb"} {
		call("CreateDomain", map[string]string{"DomainName": d})
	}
	first := call("ListDomains", map[string]string{"MaxNumberOfDomains": "1"})
	var page struct {
		Domains []string `xml:"ListDomainsResult>DomainName"`
		Next    string   `xml:"ListDomainsResult>NextToken"`
	}
	if e := xml.Unmarshal(first.Body.Bytes(), &page); e != nil || len(page.Domains) != 1 || page.Domains[0] != "aaa" || page.Next == "" {
		t.Fatal(first.Body, e)
	}
	last := call("ListDomains", map[string]string{"MaxNumberOfDomains": "1", "NextToken": page.Next})
	var end struct {
		Domains []string `xml:"ListDomainsResult>DomainName"`
		Next    string   `xml:"ListDomainsResult>NextToken"`
	}
	if e := xml.Unmarshal(last.Body.Bytes(), &end); e != nil || len(end.Domains) != 1 || end.Domains[0] != "bbb" || end.Next != "" {
		t.Fatal(last.Body, e)
	}
	for _, v := range []string{"blue", "green"} {
		call("PutAttributes", map[string]string{"DomainName": "aaa", "ItemName": "item", "Attribute.1.Name": "colour", "Attribute.1.Value": v})
	}
	call("PutAttributes", map[string]string{"DomainName": "aaa", "ItemName": "item", "Attribute.1.Name": "colour", "Attribute.1.Value": "red", "Attribute.1.Replace": "true", "Attribute.2.Name": "other", "Attribute.2.Value": ""})
	got := call("GetAttributes", map[string]string{"DomainName": "aaa", "ItemName": "item", "AttributeName.1": "colour", "ConsistentRead": "false"})
	var attrs struct {
		Values []queryAttribute `xml:"GetAttributesResult>Attribute"`
	}
	if e := xml.Unmarshal(got.Body.Bytes(), &attrs); e != nil || len(attrs.Values) != 1 || attrs.Values[0] != (queryAttribute{"colour", "red"}) {
		t.Fatal(got.Body, e)
	}
	other := NewHTTPHandler(s, Scope{"other", "us-east-1"}, func(context.Context, string, string) bool { return true })
	if w := callHTTP(other, params("GetAttributes", map[string]string{"DomainName": "aaa", "ItemName": "item"})); w.Code != 400 {
		t.Fatal(w.Body)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader(params("CreateDomain", map[string]string{"DomainName": "scoped"}).Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Simulator-Account", "other")
	r.Header.Set("X-Simulator-Region", "elsewhere")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	ds, _, e := s.ListDomains(context.Background(), "other", "elsewhere", 100, "")
	if e != nil || len(ds) != 0 {
		t.Fatal("untrusted scope", ds, e)
	}
}

func TestHTTPNonnegativeSparseIndices(t *testing.T) {
	s, _ := fixture(t)
	h := NewHTTPHandler(s, Scope{"local", "us-east-1"}, func(context.Context, string, string) bool { return true })
	callHTTP(h, params("CreateDomain", map[string]string{"DomainName": "index-test"}))
	for _, index := range []string{"0", "999", "999999999999999999999999999999"} {
		w := callHTTP(h, params("PutAttributes", map[string]string{"DomainName": "index-test", "ItemName": "item", "Attribute." + index + ".Name": "name", "Attribute." + index + ".Value": index}))
		if w.Code != 200 {
			t.Errorf("index %s: %d %s", index, w.Code, w.Body)
		}
	}
	p := map[string]string{"DomainName": "index-test", "ItemName": "bounded"}
	for i := 0; i < 256; i++ {
		p[fmt.Sprintf("Attribute.%d.Name", i*1000)] = "name"
		p[fmt.Sprintf("Attribute.%d.Value", i*1000)] = fmt.Sprint(i)
	}
	if w := callHTTP(h, params("PutAttributes", p)); w.Code != 200 {
		t.Errorf("256 sparse: %d", w.Code)
	}
	p["Attribute.999999.Name"] = "name"
	p["Attribute.999999.Value"] = "overflow"
	if w := callHTTP(h, params("PutAttributes", p)); w.Code != 400 {
		t.Errorf("257 indices: %d", w.Code)
	}
	for _, index := range []string{"-1", "+1", "01", "1e2", ""} {
		w := callHTTP(h, params("PutAttributes", map[string]string{"DomainName": "index-test", "ItemName": "item", "Attribute." + index + ".Name": "name", "Attribute." + index + ".Value": "bad"}))
		if w.Code != 400 {
			t.Errorf("invalid %q: %d", index, w.Code)
		}
	}
}
func TestHTTPXMLRestrictedRoundTrip(t *testing.T) {
	s, _ := fixture(t)
	h := NewHTTPHandler(s, Scope{"local", "us-east-1"}, func(context.Context, string, string) bool { return true })
	callHTTP(h, params("CreateDomain", map[string]string{"DomainName": "xml-test"}))
	for i, text := range []string{"ordinary<&日本語>", "a\x00b", "a\x01b", "a\u007fb", "a\u0086b", "a\ufdd0b", "a\ufffeb", "a\U0001ffffb"} {
		item := fmt.Sprint(i)
		w := callHTTP(h, params("PutAttributes", map[string]string{"DomainName": "xml-test", "ItemName": item, "Attribute.1.Name": text, "Attribute.1.Value": text}))
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		w = callHTTP(h, params("GetAttributes", map[string]string{"DomainName": "xml-test", "ItemName": item}))
		type field struct {
			Encoding string `xml:"encoding,attr"`
			Text     string `xml:",chardata"`
		}
		var got struct {
			Attrs []struct{ Name, Value field } `xml:"GetAttributesResult>Attribute"`
		}
		if e := xml.Unmarshal(w.Body.Bytes(), &got); e != nil || len(got.Attrs) != 1 {
			t.Fatalf("XML %v %s", e, w.Body)
		}
		for _, f := range []field{got.Attrs[0].Name, got.Attrs[0].Value} {
			decoded := f.Text
			if i == 0 {
				if f.Encoding != "" {
					t.Errorf("ordinary encoded")
				}
			} else {
				if f.Encoding != "base64" {
					t.Errorf("restricted text %q lost encoding: %+v", text, f)
				}
				b, e := base64.StdEncoding.DecodeString(f.Text)
				if e != nil {
					t.Errorf("base64 %v", e)
				}
				decoded = string(b)
			}
			if decoded != text {
				t.Errorf("lossy %q != %q", decoded, text)
			}
		}
	}
}
