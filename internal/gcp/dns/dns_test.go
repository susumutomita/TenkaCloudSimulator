package dns

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func leafFixture(t *testing.T) (factoryDNS, *core.Store) {
	t.Helper()
	db, err := core.New(filepath.Join(t.TempDir(), "dns.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return factoryEngine(t, db.Database(), "project-one"), db
}
func TestDNSAdmissionAndFiniteRecordSyntax(t *testing.T) {
	f, _ := leafFixture(t)
	f.create("zone-one")
	base := f.root() + "/zone-one"
	before := f.snapshot("zone-one")
	for _, body := range []string{`null`, `[]`, `{} {}`, `{"name":"one","name":"two"}`, `{"name":`, "{\"name\":\"one\",}", `{"name":"one","dnsName":"bad.test."}`, `{"name":"one","dnsName":"bad.test.","description":"ok","kind":"wrong"}`, `{"name":"one","dnsName":"bad.test.","description":"ok","dnssecConfig":{}}`, `{"name":"Upper","dnsName":"example.test.","description":"x"}`} {
		f.call("POST", f.root(), body, 400)
	}
	for _, body := range []string{`null`, `[]`, `{}`, `{"kind":"bad","additions":[]}`, `{"kind":"bad","additions":[{"name":"a.example.test.","type":"A","ttl":300,"rrdatas":["192.0.2.1"]}]}`, `{"additions":[{"name":"a.example.test.","ttl":1,"ttl":2}]}`, `{"additions":[{"name":"a.example.test.","type":"A","ttl":1,"rrdatas":["192.0.2.1"],"routingPolicy":{}}]}`} {
		f.call("POST", base+"/changes", body, 400)
	}
	for _, q := range []string{"maxResults=0", "maxResults=101", "maxResults=foo", "maxResults=1&maxResults=2", "pageToken=", "pageToken=%%%", "pageToken=" + strings.Repeat("a", 4097), "alt=media", "prettyPrint=wrong", "unknown=true", "clientOperationId=x", "filter=x", "type=A", "name=not-absolute", "name=a.example.test.&type=INVALID", "name=a.example.test.&type=", "name=a.example.test.&type=SOA&sortBy=foo"} {
		if strings.Contains(q, "%%%") {
			q = "pageToken=%25%25%25"
		}
		f.call("GET", base+"/rrsets?"+q, nil, 400)
	}
	for _, q := range []string{"sortBy=foo", "sortOrder=backwards"} {
		f.call("GET", base+"/changes?"+q, nil, 400)
	}
	f.call("GET", f.root()+"?dnsName=bad", nil, 400)
	f.call("GET", base, `{}`, 400)
	f.call("GET", base+"//rrsets", nil, 400)
	f.call("GET", f.root()+"/zone%2fone", nil, 400)
	f.call("POST", base+"/changes", strings.Repeat(" ", 256<<10)+"{}", 400)
	r := httptest.NewRequest("GET", base+"?bad=%xx", nil)
	r = r.WithContext(context.WithValue(r.Context(), factorySubjectKey{}, "owner"))
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	if f.snapshot("zone-one") != before {
		t.Fatal("admission refusal changed state")
	}
	valid := []map[string]any{
		{"name": "v6.example.test.", "type": "AAAA", "ttl": 0, "rrdatas": []string{"2001:db8::1"}},
		{"name": "alias.example.test.", "type": "CNAME", "ttl": 300, "rrdatas": []string{"target.example.test."}},
		{"name": "child.example.test.", "type": "NS", "ttl": 300, "rrdatas": []string{"ns.example.test."}},
		{"name": "ptr.example.test.", "type": "PTR", "ttl": 300, "rrdatas": []string{"target.example.test."}},
		{"name": "mx.example.test.", "type": "MX", "ttl": 2147483647, "rrdatas": []string{"10 mx.example.test."}},
		{"name": "_txt.example.test.", "type": "TXT", "ttl": 300, "rrdatas": []string{`"hello world"`}},
	}
	adds := []any{}
	for _, r := range valid {
		adds = append(adds, r)
	}
	f.call("POST", base+"/changes", factoryChange(adds, nil), 200)
	for _, v := range valid {
		f.call("GET", base+"/rrsets/"+v["name"].(string)+"/"+v["type"].(string), nil, 200)
	}
	listed := factoryItems(t, f.call("GET", base+"/rrsets?name=v6.example.test.&type=AAAA&alt=json&prettyPrint=false", nil, 200), "rrsets")
	if len(listed) != 1 {
		t.Fatal(listed)
	}
	malformed := []map[string]any{
		{"type": "A", "rrdatas": []string{"999.1.1.1"}}, {"type": "AAAA", "rrdatas": []string{"192.0.2.1"}}, {"type": "AAAA", "rrdatas": []string{"fe80::1%en0"}},
		{"type": "MX", "rrdatas": []string{"99999 mx.example.test."}}, {"type": "MX", "rrdatas": []string{"01 mx.example.test."}}, {"type": "MX", "rrdatas": []string{"10  mx.example.test."}},
		{"type": "TXT", "rrdatas": []string{"unquoted"}}, {"type": "TXT", "rrdatas": []string{`"with\escape"`}},
		{"type": "CNAME", "rrdatas": []string{"one.example.test.", "two.example.test."}}, {"type": "CNAME", "rrdatas": []string{"UPPER.test."}},
		{"type": "SRV", "rrdatas": []string{"1 2 3 srv.test."}}, {"kind": "invalid"}, {"ttl": -1}, {"ttl": 2147483648.0}, {"ttl": nil}, {"rrdatas": []string{}}, {"rrdatas": []string{"192.0.2.1", "192.0.2.1"}}, {"rrdatas": []string{strings.Repeat("a", 1025)}},
		{"name": "bad..example.test."}, {"name": "-bad.example.test."}, {"name": "Upper.example.test."}, {"name": strings.Repeat("a", 64) + ".example.test."}, {"name": "bad*.example.test."},
	}
	before = f.snapshot("zone-one")
	for _, patch := range malformed {
		r := factoryA("invalid.example.test.", "192.0.2.1")
		for k, v := range patch {
			r[k] = v
		}
		f.call("POST", base+"/changes", factoryChange([]any{r}, nil), 400)
	}
	if f.snapshot("zone-one") != before {
		t.Fatal("invalid RRset changed data or receipt")
	}
	f.call("POST", base+"/changes", factoryChange(nil, adds), 200)
	f.call("DELETE", base, nil, 200)
}
func TestDNSBudgetsKeepDeletionCleanupPossible(t *testing.T) {
	f, _ := leafFixture(t)
	f.create("zone-one")
	base := f.root() + "/zone-one"
	all := []any{}
	for i := 0; i < maxRecords-2; i++ {
		all = append(all, factoryA(fmt.Sprintf("r%d.example.test.", i), "192.0.2.1"))
	}
	for start := 0; start < len(all); start += maxBatch {
		f.call("POST", base+"/changes", factoryChange(all[start:min(start+maxBatch, len(all))], nil), 200)
	}
	before := f.snapshot("zone-one")
	f.call("POST", base+"/changes", factoryChange([]any{factoryA("extra.example.test.", "192.0.2.2")}, nil), 429)
	if f.snapshot("zone-one") != before {
		t.Fatal("record quota left mutation")
	}
	for start := 0; start < len(all); start += maxBatch {
		f.call("POST", base+"/changes", factoryChange(nil, all[start:min(start+maxBatch, len(all))]), 200)
	}
	f.call("DELETE", base, nil, 200)
	f.create("zone-one")
	current := factoryA("history.example.test.", "192.0.2.1")
	f.call("POST", base+"/changes", factoryChange([]any{current}, nil), 200)
	for i := 1; i < maxChanges; i++ {
		next := factoryA("history.example.test.", fmt.Sprintf("192.0.2.%d", 1+i))
		f.call("POST", base+"/changes", factoryChange([]any{next}, []any{current}), 200)
		current = next
	}
	before = f.snapshot("zone-one")
	f.call("POST", base+"/changes", factoryChange([]any{factoryA("extra.example.test.", "192.0.2.2")}, nil), 429)
	if f.snapshot("zone-one") != before {
		t.Fatal("history quota left mutation")
	}
	f.call("POST", base+"/changes", factoryChange(nil, []any{current}), 200)
	f.call("DELETE", base, nil, 200)
	for i := 0; i < maxZones; i++ {
		f.create(fmt.Sprintf("zone-%d", i))
	}
	f.call("POST", f.root(), map[string]any{"name": "over-budget", "dnsName": "example.test.", "description": "x"}, 429)
	for i := 0; i < maxZones; i++ {
		f.call("DELETE", f.root()+fmt.Sprintf("/zone-%d", i), nil, 200)
	}
}
func TestDNSCompositionAndAuthenticatedTokens(t *testing.T) {
	f, db := leafFixture(t)
	cfg := Config{Project: "project-one", Owner: "owner", Subject: func(context.Context) (string, bool) { return "owner", true }}
	if _, err := New(nil, cfg); err == nil {
		t.Fatal("nil db accepted")
	}
	for _, modify := range []func(*Config){func(c *Config) { c.Project = "../bad" }, func(c *Config) { c.Owner = "" }, func(c *Config) { c.Subject = nil }, func(c *Config) { c.Owner = "replacement" }} {
		v := cfg
		modify(&v)
		if _, err := New(db.Database(), v); err == nil {
			t.Fatal("invalid config/owner migration accepted")
		}
	}
	if _, err := New(db.Database(), cfg); err != nil {
		t.Fatal(err)
	}
	f.create("zone-one")
	f.create("zone-two")
	_, token := f.pages(f.root(), "managedZones")
	f.call("GET", f.root()+"?pageToken="+token[:len(token)-1]+"!", nil, 400)
	other, _ := leafFixture(t)
	other.create("zone-one")
	other.create("zone-two")
	other.call("GET", other.root()+"?pageToken="+token, nil, 400)
	filtered := f.call("GET", f.root()+"?dnsName=absent.test.", nil, 200)
	if len(factoryItems(t, filtered, "managedZones")) != 0 {
		t.Fatal(filtered)
	}
	base := f.root() + "/zone-one"
	first := f.call("POST", base+"/changes", factoryChange([]any{factoryA("one.example.test.", "192.0.2.1")}, nil), 200)
	second := f.call("POST", base+"/changes", factoryChange([]any{factoryA("two.example.test.", "192.0.2.2")}, nil), 200)
	desc := factoryItems(t, f.call("GET", base+"/changes?sortBy=changeSequence&sortOrder=descending", nil, 200), "changes")
	factoryEqual(t, desc, []any{second, first})
	if bad("test").Error() != "test" {
		t.Fatal("error lost message")
	}
}

func TestDNSByteBudgetReservesCleanup(t *testing.T) {
	f, _ := leafFixture(t)
	f.create("zone-one")
	base := f.root() + "/zone-one"
	texts := []string{}
	for i := 0; i < 32; i++ {
		texts = append(texts, fmt.Sprintf(`"%02d-%s"`, i, strings.Repeat("x", 240)))
	}
	all := []any{}
	hit := false
	for batch := 0; batch < 20; batch++ {
		adds := []any{}
		for i := 0; i < 8; i++ {
			adds = append(adds, map[string]any{"name": fmt.Sprintf("big-%03d.example.test.", batch*8+i), "type": "TXT", "ttl": 300, "rrdatas": texts})
		}
		body := factoryCanonical(factoryChange(adds, nil))
		r := httptest.NewRequest("POST", base+"/changes", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), factorySubjectKey{}, "owner"))
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		if w.Code == 429 {
			hit = true
			break
		}
		if w.Code != 200 {
			t.Fatalf("unexpected byte-budget result: %d %s", w.Code, w.Body.String())
		}
		all = append(all, adds...)
	}
	if !hit {
		t.Fatal("global byte budget did not reject bounded large records")
	}
	records := []any{}
	query := base + "/rrsets?maxResults=100"
	for i := 0; i < 4; i++ {
		page := f.call("GET", query, nil, 200)
		records = append(records, factoryItems(t, page, "rrsets")...)
		next, _ := page["nextPageToken"].(string)
		if next == "" {
			break
		}
		query = base + "/rrsets?maxResults=100&pageToken=" + next
	}
	if len(records) != len(all)+2 {
		t.Fatal("byte rejection partially stored RRsets")
	}
	for i := 0; i < len(all); i += 8 {
		f.call("POST", base+"/changes", factoryChange(nil, all[i:min(i+8, len(all))]), 200)
	}
	f.call("DELETE", base, nil, 200)
}
