package simpledb_test

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
)

type attribute struct{ Name, Value string }
type vector struct {
	Domain, Item string
	Domains      []string
	Attributes   []attribute
	Replaced     []attribute `json:"replaced_attributes"`
	Negative     []struct {
		ID, Action string
		Parameters map[string]string
		Status     int
		Code       string
	} `json:"negative_cases"`
}

func fixture(t *testing.T) vector {
	t.Helper()
	b, e := os.ReadFile("../oracle/fixtures.json")
	if e != nil {
		t.Fatalf("HARNESS_ERROR fixture: %v", e)
	}
	var f vector
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatalf("HARNESS_ERROR fixture JSON: %v", e)
	}
	return f
}

const token = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
const namespace = "http://sdb.amazonaws.com/doc/2009-04-15/"

type harness struct {
	handler http.Handler
	close   func() error
}

func open(t *testing.T, path string) *harness {
	t.Helper()
	h, c, e := server.New(path)
	if e != nil {
		t.Fatalf("HARNESS_ERROR server construction: %v", e)
	}
	a, e := server.NewAccess(h, token, false)
	if e != nil {
		_ = c()
		t.Fatalf("HARNESS_ERROR access construction: %v", e)
	}
	out := &harness{a, c}
	t.Cleanup(func() { out.stop(t) })
	return out
}
func (h *harness) stop(t *testing.T) {
	t.Helper()
	if h.close != nil {
		c := h.close
		h.close = nil
		if e := c(); e != nil {
			t.Errorf("HARNESS_ERROR store cleanup: %v", e)
		}
	}
}

type response struct {
	XMLName   xml.Name
	RequestID string `xml:"ResponseMetadata>RequestId"`
	Result    struct {
		Domains   []string `xml:"DomainName"`
		NextToken string   `xml:"NextToken"`
	} `xml:"ListDomainsResult"`
	Get struct {
		Attributes []attribute `xml:"Attribute"`
	} `xml:"GetAttributesResult"`
	Errors []struct{ Code string } `xml:"Errors>Error"`
	Error  struct{ Code string }   `xml:"Error"`
}

func (h *harness) call(t *testing.T, action string, p map[string]string, status int, code string) response {
	t.Helper()
	v := url.Values{"Action": {action}, "Version": {"2009-04-15"}}
	for k, x := range p {
		v.Set(k, x)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Simulator-Token", token)
	r.Header.Set("X-Simulator-Service", "sdb")
	r.Header.Set("X-Simulator-Region", "us-east-1")
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("PRODUCT_CONTRACT %s status=%d want=%d body=%q", action, w.Code, status, w.Body.String())
	}
	var out response
	if e := xml.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatalf("PRODUCT_CONTRACT %s XML: %v body=%q", action, e, w.Body.String())
	}
	if status == 200 {
		if out.XMLName.Local != action+"Response" || out.XMLName.Space != namespace || out.RequestID == "" {
			t.Fatalf("PRODUCT_CONTRACT %s response envelope: %+v", action, out)
		}
	} else {
		got := out.Error.Code
		if got == "" && len(out.Errors) > 0 {
			got = out.Errors[0].Code
		}
		if got != code {
			t.Fatalf("PRODUCT_CONTRACT %s error=%q want=%q", action, got, code)
		}
	}
	return out
}
func create(t *testing.T, h *harness, domain string) {
	t.Helper()
	h.call(t, "CreateDomain", map[string]string{"DomainName": domain}, 200, "")
}
func put(t *testing.T, h *harness, f vector) {
	t.Helper()
	p := map[string]string{"DomainName": f.Domain, "ItemName": f.Item}
	for i, a := range f.Attributes {
		p[fmt.Sprintf("Attribute.%d.Name", i+1)] = a.Name
		p[fmt.Sprintf("Attribute.%d.Value", i+1)] = a.Value
	}
	h.call(t, "PutAttributes", p, 200, "")
}
func get(t *testing.T, h *harness, f vector) []attribute {
	t.Helper()
	return h.call(t, "GetAttributes", map[string]string{"DomainName": f.Domain, "ItemName": f.Item, "ConsistentRead": "true"}, 200, "").Get.Attributes
}
func equalAttributes(t *testing.T, got, want []attribute) {
	t.Helper()
	keys := func(a []attribute) []string {
		out := []string{}
		for _, x := range a {
			out = append(out, x.Name+"\x00"+x.Value)
		}
		sort.Strings(out)
		return out
	}
	if !reflect.DeepEqual(keys(got), keys(want)) {
		t.Fatalf("PRODUCT_CONTRACT attributes got=%+v want=%+v", got, want)
	}
}

func TestSimpleDBFirstHarnessReadiness(t *testing.T) {
	h := open(t, filepath.Join(t.TempDir(), "state.db"))
	r := httptest.NewRequest("GET", "http://127.0.0.1/_simulator/health", nil)
	r.Header.Set("X-Simulator-Token", token)
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("HARNESS_ERROR health status %d", w.Code)
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1/", nil)
	w = httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("HARNESS_ERROR missing local token status %d", w.Code)
	}
}
func TestSimpleDBFirstDomainItems(t *testing.T) {
	f := fixture(t)
	h := open(t, filepath.Join(t.TempDir(), "state.db"))
	create(t, h, f.Domain)
	create(t, h, f.Domain)
	list := h.call(t, "ListDomains", nil, 200, "")
	if len(list.Result.Domains) != 1 || list.Result.Domains[0] != f.Domain {
		t.Fatal("PRODUCT_CONTRACT create idempotence", list)
	}
	put(t, h, f)
	put(t, h, f)
	equalAttributes(t, get(t, h, f), f.Attributes)
	h.call(t, "PutAttributes", map[string]string{"DomainName": f.Domain, "ItemName": f.Item, "Attribute.1.Name": "colour", "Attribute.1.Value": "red", "Attribute.1.Replace": "true"}, 200, "")
	equalAttributes(t, get(t, h, f), f.Replaced)
	filtered := h.call(t, "GetAttributes", map[string]string{"DomainName": f.Domain, "ItemName": f.Item, "AttributeName.1": "colour", "ConsistentRead": "true"}, 200, "")
	equalAttributes(t, filtered.Get.Attributes, []attribute{{"colour", "red"}})
}
func TestSimpleDBFirstRestartDeletion(t *testing.T) {
	f := fixture(t)
	path := filepath.Join(t.TempDir(), "persist.db")
	h := open(t, path)
	create(t, h, f.Domain)
	put(t, h, f)
	h.stop(t)
	h = open(t, path)
	equalAttributes(t, get(t, h, f), f.Attributes)
	h.call(t, "DeleteDomain", map[string]string{"DomainName": f.Domain}, 200, "")
	h.call(t, "DeleteDomain", map[string]string{"DomainName": f.Domain}, 200, "")
	h.stop(t)
	h = open(t, path)
	if list := h.call(t, "ListDomains", nil, 200, ""); len(list.Result.Domains) != 0 || list.Result.NextToken != "" {
		t.Fatal("PRODUCT_CONTRACT deletion not persisted", list)
	}
	h.call(t, "GetAttributes", map[string]string{"DomainName": f.Domain, "ItemName": f.Item}, 400, "NoSuchDomain")
	create(t, h, f.Domain)
	equalAttributes(t, get(t, h, f), nil)
}
func TestSimpleDBFirstListPagination(t *testing.T) {
	f := fixture(t)
	h := open(t, filepath.Join(t.TempDir(), "state.db"))
	for _, d := range f.Domains {
		create(t, h, d)
	}
	for _, params := range []map[string]string{nil, {"MaxNumberOfDomains": "100"}} {
		out := h.call(t, "ListDomains", params, 200, "")
		got := append([]string{}, out.Result.Domains...)
		want := append([]string{}, f.Domains...)
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) || out.Result.NextToken != "" {
			t.Fatal("PRODUCT_CONTRACT default/max page", out)
		}
	}
	seen := map[string]bool{}
	tokens := map[string]bool{}
	next := ""
	for page := 0; page < 4; page++ {
		p := map[string]string{"MaxNumberOfDomains": "1"}
		if next != "" {
			p["NextToken"] = next
		}
		out := h.call(t, "ListDomains", p, 200, "")
		if len(out.Result.Domains) != 1 {
			t.Fatal("PRODUCT_CONTRACT page size", out)
		}
		d := out.Result.Domains[0]
		if seen[d] {
			t.Fatal("PRODUCT_CONTRACT duplicate domain", d)
		}
		seen[d] = true
		next = out.Result.NextToken
		if next == "" {
			break
		}
		if tokens[next] {
			t.Fatal("PRODUCT_CONTRACT non-progressing token")
		}
		tokens[next] = true
	}
	if next != "" || len(seen) != len(f.Domains) {
		t.Fatal("PRODUCT_CONTRACT pagination did not terminate completely", seen)
	}
	for _, d := range f.Domains {
		if !seen[d] {
			t.Fatal("PRODUCT_CONTRACT missing domain", d)
		}
	}
}

// With more than 100 domains, omitted/default and maximum must actually split.
func TestSimpleDBFirstDefaultPageBoundary(t *testing.T) {
	h := open(t, filepath.Join(t.TempDir(), "boundary.db"))
	want := map[string]bool{}
	for i := 0; i < 101; i++ {
		name := fmt.Sprintf("boundary-%03d", i)
		create(t, h, name)
		want[name] = true
	}
	for _, params := range []map[string]string{nil, {"MaxNumberOfDomains": "100"}} {
		first := h.call(t, "ListDomains", params, 200, "")
		if len(first.Result.Domains) != 100 || first.Result.NextToken == "" {
			t.Fatal("PRODUCT_CONTRACT default/max must split 101 into 100+1", first)
		}
		nextParams := map[string]string{"NextToken": first.Result.NextToken}
		for k, v := range params {
			nextParams[k] = v
		}
		last := h.call(t, "ListDomains", nextParams, 200, "")
		if len(last.Result.Domains) != 1 || last.Result.NextToken != "" {
			t.Fatal("PRODUCT_CONTRACT final 100-bound page", last)
		}
		seen := map[string]bool{}
		for _, name := range append(first.Result.Domains, last.Result.Domains...) {
			if !want[name] || seen[name] {
				t.Fatal("PRODUCT_CONTRACT duplicate/foreign domain", name)
			}
			seen[name] = true
		}
		if len(seen) != 101 {
			t.Fatal("PRODUCT_CONTRACT missing domain at 100 boundary")
		}
	}
}

func TestSimpleDBFirstValidation(t *testing.T) {
	f := fixture(t)
	h := open(t, filepath.Join(t.TempDir(), "state.db"))
	create(t, h, f.Domain)
	put(t, h, f)
	for _, c := range f.Negative {
		t.Run(c.ID, func(t *testing.T) {
			h.call(t, c.Action, c.Parameters, c.Status, c.Code)
			equalAttributes(t, get(t, h, f), f.Attributes)
			domains := h.call(t, "ListDomains", nil, 200, "")
			if len(domains.Result.Domains) != 1 || domains.Result.Domains[0] != f.Domain {
				t.Fatal("PRODUCT_CONTRACT rejected request left ghost/changed domains", domains)
			}
		})
	}
}
