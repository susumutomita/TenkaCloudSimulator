package dns

// Independent oracle fixed before implementation. Wire bindings and shapes:
// google-api-python-client 2.199.0 dns.v1 discovery, revision 20260714,
// SHA256 98e5e8539715e33c0c4fbd6bc44d6b76b3004196839f8610feba7d33783066c4.
// https://docs.cloud.google.com/dns/docs/reference/rest/v1/changes
// https://docs.cloud.google.com/dns/docs/reference/rest/v1/managedZones
// https://docs.cloud.google.com/dns/docs/reference/rest/v1/resourceRecordSets
// Atomic exact deletions and resource shapes follow Google. Local owner auth,
// synchronous completion, protected synthetic apex records, explicit refusal,
// bounded paging tokens and SQLite failure behavior are simulator contracts.
// These tests cover a leaf handler, not host auth, router, official SDK, DNS
// serving/propagation, a console, Google IAM or a whole-service implementation.
import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type factorySubjectKey struct{}
type factoryDNS struct {
	t       *testing.T
	h       http.Handler
	project string
}

func factoryEngine(t *testing.T, db *sql.DB, project string) factoryDNS {
	t.Helper()
	h, err := New(db, Config{Project: project, Owner: "owner", Subject: func(ctx context.Context) (string, bool) {
		s, ok := ctx.Value(factorySubjectKey{}).(string)
		return s, ok
	}, Now: func() time.Time { return time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return factoryDNS{t, h, project}
}
func (f factoryDNS) root() string { return "/dns/v1/projects/" + f.project + "/managedZones" }
func (f factoryDNS) callAs(subject, method, path string, body any, want int) map[string]any {
	f.t.Helper()
	text := ""
	if body != nil {
		if raw, ok := body.(string); ok {
			text = raw
		} else {
			b, err := json.Marshal(body)
			if err != nil {
				f.t.Fatal(err)
			}
			text = string(b)
		}
	}
	r := httptest.NewRequest(method, path, strings.NewReader(text))
	r.Header.Set("Content-Type", "application/json")
	if subject != "" {
		r = r.WithContext(context.WithValue(r.Context(), factorySubjectKey{}, subject))
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != want {
		f.t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	out := map[string]any{}
	if w.Body.Len() != 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			f.t.Fatal(err)
		}
	}
	if want >= 400 {
		e, ok := out["error"].(map[string]any)
		if !ok || e["code"] != float64(want) {
			f.t.Fatalf("missing Google JSON error: %#v", out)
		}
	}
	return out
}
func (f factoryDNS) call(method, path string, body any, want int) map[string]any {
	return f.callAs("owner", method, path, body, want)
}
func factoryChange(add, del []any) map[string]any {
	return map[string]any{"additions": add, "deletions": del}
}
func factoryEqual(t *testing.T, got, want any) {
	t.Helper()
	if factoryCanonical(got) != factoryCanonical(want) {
		t.Fatalf("got %s want %s", factoryCanonical(got), factoryCanonical(want))
	}
}
func (f factoryDNS) create(name string) map[string]any {
	return f.call("POST", f.root(), map[string]any{"name": name, "dnsName": "example.test.", "description": "oracle", "visibility": "public"}, 200)
}
func (f factoryDNS) snapshot(zone string) string {
	return factoryCanonical([]any{f.call("GET", f.root()+"/"+zone, nil, 200), f.call("GET", f.root()+"/"+zone+"/rrsets", nil, 200), f.call("GET", f.root()+"/"+zone+"/changes", nil, 200)})
}
func factoryItems(t *testing.T, m map[string]any, key string) []any {
	t.Helper()
	v, ok := m[key]
	if !ok {
		return nil
	}
	items, ok := v.([]any)
	if !ok {
		t.Fatalf("%s is not a list: %#v", key, v)
	}
	return items
}
func (f factoryDNS) pages(path, key string) ([]any, string) {
	f.t.Helper()
	var out []any
	token, first := "", ""
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		q := path + sep + "maxResults=1"
		if token != "" {
			q += "&pageToken=" + url.QueryEscape(token)
		}
		m := f.call("GET", q, nil, 200)
		items := factoryItems(f.t, m, key)
		if len(items) > 1 {
			f.t.Fatal("page limit ignored")
		}
		out = append(out, items...)
		token, _ = m["nextPageToken"].(string)
		if token == "" {
			return out, first
		}
		if first == "" {
			first = token
		}
		if seen[token] {
			f.t.Fatal("pagination token cycle")
		}
		seen[token] = true
	}
	f.t.Fatal("pagination failed to terminate")
	return nil, ""
}

// observe reads actual leaf HTTP APIs, never the write response as state.
func (f factoryDNS) observe(base string, records []any) factoryObservation {
	f.t.Helper()
	got := factoryObservation{
		List:    factoryItems(f.t, f.call("GET", base+"/rrsets", nil, 200), "rrsets"),
		Changes: factoryItems(f.t, f.call("GET", base+"/changes", nil, 200), "changes"),
	}
	for _, record := range records {
		r := record.(map[string]any)
		got.Gets = append(got.Gets, f.call("GET", base+"/rrsets/"+url.PathEscape(r["name"].(string))+"/"+r["type"].(string), nil, 200))
	}
	return got
}
func (f factoryDNS) requireObservation(base string, want factoryObservation) {
	f.t.Helper()
	if err := factoryCheckObservation(f.observe(base, want.Gets), want); err != nil {
		f.t.Fatal(err)
	}
}

func TestCloudDNSFactoryAtomicLifecycle(t *testing.T) {
	t.Run("wire-isolation-persistence-paging-cleanup", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "dns.db")
		db, err := core.New(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		f := factoryEngine(t, db.Database(), "project-one")
		otherDB, err := core.New(filepath.Join(t.TempDir(), "other.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer otherDB.Close()
		other := factoryEngine(t, otherDB.Database(), "project-two")
		// Another configured project in the same SQLite DB must also remain separate.
		sibling := factoryEngine(t, db.Database(), "project-two")
		z := f.create("zone-one")
		id, ok := z["id"].(string)
		if !ok || id == "" {
			t.Fatal("zone ID must be a nonempty string")
		}
		if z["dnsName"] != "example.test." || z["name"] != "zone-one" {
			t.Fatal(z)
		}
		factoryEqual(t, f.call("GET", f.root()+"/"+id, nil, 200), z)
		f.call("POST", f.root(), map[string]any{"name": "zone-one", "dnsName": "example.test.", "description": "oracle"}, 409)
		f.call("POST", f.root(), map[string]any{"name": "private", "dnsName": "private.test.", "description": "oracle", "visibility": "private"}, 400)
		base := f.root() + "/zone-one"
		initial := factoryItems(t, f.call("GET", base+"/rrsets", nil, 200), "rrsets")
		if len(initial) != 2 {
			t.Fatalf("expected initial apex NS and SOA: %#v", initial)
		}
		kinds := map[string]bool{}
		for _, item := range initial {
			r := item.(map[string]any)
			if r["name"] != "example.test." {
				t.Fatal(r)
			}
			kinds[r["type"].(string)] = true
		}
		if !kinds["NS"] || !kinds["SOA"] {
			t.Fatal(kinds)
		}
		a, b := factoryA("a.example.test.", "192.0.2.1"), factoryA("b.example.test.", "192.0.2.2")
		change := f.call("POST", base+"/changes", factoryChange([]any{a, b}, nil), 200)
		cid, ok := change["id"].(string)
		if !ok || cid == "" || change["status"] != "done" {
			t.Fatal(change)
		}
		if _, err := time.Parse(time.RFC3339, fmt.Sprint(change["startTime"])); err != nil {
			t.Fatal("change must have RFC3339 startTime", change)
		}
		factoryEqual(t, factoryRecordValues(factoryItems(t, change, "additions")), factoryRecordValues([]any{a, b}))
		factoryEqual(t, f.call("GET", base+"/changes/"+cid, nil, 200), change)
		got := f.call("GET", base+"/rrsets/a.example.test./A", nil, 200)
		factoryEqual(t, factoryRecordValues([]any{got}), factoryRecordValues([]any{a}))
		factoryEqual(t, factoryRecordValues(factoryItems(t, f.call("GET", base+"/rrsets", nil, 200), "rrsets")), factoryRecordValues(append(initial, a, b)))
		// A matching delete plus a stale delete cannot partially remove the first RRset.
		before := f.snapshot("zone-one")
		beforeZones := factoryCanonical(f.call("GET", f.root(), nil, 200))
		rejects := []struct {
			method, path string
			body         any
			code         int
		}{
			{"POST", base + "/changes", factoryChange(nil, []any{a, factoryA("b.example.test.", "192.0.2.99")}), 412},
			{"POST", base + "/changes", factoryChange([]any{a}, nil), 409},
			{"POST", base + "/changes", factoryChange([]any{factoryA("new.example.test.", "192.0.2.3"), factoryA("new.example.test.", "192.0.2.3")}, nil), 400},
			{"POST", base + "/changes", factoryChange(nil, []any{factoryA("absent.example.test.", "192.0.2.3")}), 412},
			{"POST", base + "/changes", factoryChange([]any{map[string]any{"name": "a.example.test.", "type": "CNAME", "ttl": 300, "rrdatas": []string{"target.example.test."}}}, nil), 400},
			{"POST", base + "/changes", factoryChange([]any{factoryA("outside.test.", "192.0.2.3")}, nil), 400},
			{"POST", base + "/changes", `{"additions":[`, 400},
			{"POST", base + "/changes", map[string]any{"unsupported": true}, 400},
			{"POST", base + "/changes", factoryChange(nil, initial), 400},
			{"POST", base + "/rrsets", factoryA("direct.example.test.", "192.0.2.3"), 501},
			{"DELETE", base, nil, 400},
		}
		for _, r := range rejects {
			out := f.call(r.method, r.path, r.body, r.code)
			if r.code == 412 {
				errors := factoryItems(t, out["error"].(map[string]any), "errors")
				if len(errors) == 0 || errors[0].(map[string]any)["reason"] != "preconditionFailed" {
					t.Fatal("stale deletion requires preconditionFailed", out)
				}
			}
			if f.snapshot("zone-one") != before {
				t.Fatalf("rejected %s %s changed state", r.method, r.path)
			}
		}
		for _, actor := range []string{"stranger", "", "owner"} {
			code := 403
			if actor == "" {
				code = 401
			}
			for _, operation := range []struct {
				method, path string
				body         any
			}{
				{"POST", f.root(), map[string]any{"name": "denied-zone", "dnsName": "denied.test.", "description": "oracle"}},
				{"GET", f.root(), nil}, {"GET", base, nil}, {"DELETE", base, nil},
				{"POST", base + "/changes", factoryChange(nil, []any{a})},
				{"GET", base + "/changes", nil}, {"GET", base + "/changes/" + cid, nil},
				{"GET", base + "/rrsets", nil}, {"GET", base + "/rrsets/a.example.test./A", nil},
			} {
				path := operation.path
				if actor == "owner" {
					path = strings.Replace(path, "project-one", "project-two", 1)
				}
				f.callAs(actor, operation.method, path, operation.body, code)
			}
		}
		if f.snapshot("zone-one") != before || factoryCanonical(f.call("GET", f.root(), nil, 200)) != beforeZones {
			t.Fatal("denial mutated state")
		}
		for _, team := range []factoryDNS{other, sibling} {
			if len(factoryItems(t, team.call("GET", team.root(), nil, 200), "managedZones")) != 0 {
				t.Fatal("project/team state leaked")
			}
		}
		sibling.create("zone-one") // same resource name is project-local
		secondZone := f.create("zone-two")
		zones, zt := f.pages(f.root(), "managedZones")
		if len(zones) != 2 || zt == "" {
			t.Fatal("zone paging failed")
		}
		factoryEqual(t, zones, []any{z, secondZone})
		records, rt := f.pages(base+"/rrsets", "rrsets")
		factoryEqual(t, factoryRecordValues(records), factoryRecordValues(append(initial, a, b)))
		// Make a second successful change to require changes paging.
		c := factoryA("c.example.test.", "192.0.2.3")
		secondChange := f.call("POST", base+"/changes", factoryChange([]any{c}, nil), 200)
		changes, ct := f.pages(base+"/changes", "changes")
		if len(changes) != 2 || ct == "" {
			t.Fatal("changes paging failed")
		}
		factoryEqual(t, changes, []any{change, secondChange})
		for _, p := range []string{f.root() + "/zone-two/rrsets?pageToken=" + url.QueryEscape(rt), base + "/changes?pageToken=" + url.QueryEscape(rt), base + "/rrsets?name=a.example.test.&pageToken=" + url.QueryEscape(rt), f.root() + "/zone-two/changes?pageToken=" + url.QueryEscape(ct)} {
			f.call("GET", p, nil, 400)
		}
		sibling.call("GET", sibling.root()+"?pageToken="+url.QueryEscape(zt), nil, 400)
		saved := f.snapshot("zone-one")
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db, err = core.New(path)
		if err != nil {
			t.Fatal(err)
		}
		f = factoryEngine(t, db.Database(), "project-one")
		if f.snapshot("zone-one") != saved {
			t.Fatal("state changed across restart")
		}
		replacement := factoryA("b.example.test.", "192.0.2.22")
		f.call("POST", base+"/changes", factoryChange([]any{replacement}, []any{b}), 200)
		factoryEqual(t, factoryRecordValues([]any{f.call("GET", base+"/rrsets/b.example.test./A", nil, 200)}), factoryRecordValues([]any{replacement}))
		b = replacement
		f.call("POST", base+"/changes", factoryChange(nil, []any{a, b, c}), 200)
		// Observe deletions before zone cascade; cleanup must not mask Delete no-ops.
		factoryEqual(t, factoryRecordValues(factoryItems(t, f.call("GET", base+"/rrsets", nil, 200), "rrsets")), factoryRecordValues(initial))
		f.call("GET", base+"/rrsets/a.example.test./A", nil, 404)
		f.call("DELETE", base, nil, 200)
		f.call("GET", base, nil, 404)
		f.call("GET", f.root()+"/"+id, nil, 404)
		f.call("GET", base+"/changes/"+cid, nil, 404)
		recreated := f.create("zone-one")
		if len(factoryItems(t, f.call("GET", base+"/changes", nil, 200), "changes")) != 0 {
			t.Fatal("recreated zone inherited history")
		}
		if recreated["id"] == id {
			t.Fatal("zone ID reused")
		}
		f.call("GET", base+"/changes/"+cid, nil, 404)
		f.call("GET", base+"/rrsets?pageToken="+url.QueryEscape(rt), nil, 400)
		f.call("DELETE", base, nil, 200)
		f.call("DELETE", f.root()+"/zone-two", nil, 200)
		if len(factoryItems(t, f.call("GET", f.root(), nil, 200), "managedZones")) != 0 {
			t.Fatal("cleanup retained zones")
		}
		sibling = factoryEngine(t, db.Database(), "project-two")
		sibling.call("GET", sibling.root()+"/zone-one", nil, 200)
		sibling.call("DELETE", sibling.root()+"/zone-one", nil, 200)
	})
	t.Run("conflicting-writes", func(t *testing.T) {
		db, err := core.New(filepath.Join(t.TempDir(), "race.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		f := factoryEngine(t, db.Database(), "project-one")
		f.create("zone-one")
		base := f.root() + "/zone-one"
		initial := factoryItems(t, f.call("GET", base+"/rrsets", nil, 200), "rrsets")
		winner := factoryA("race.example.test.", "192.0.2.4")
		body := factoryCanonical(factoryChange([]any{winner}, nil))
		var wg sync.WaitGroup
		codes := make(chan int, 2)
		for range 2 {
			wg.Go(func() {
				r := httptest.NewRequest("POST", base+"/changes", strings.NewReader(body))
				r = r.WithContext(context.WithValue(r.Context(), factorySubjectKey{}, "owner"))
				w := httptest.NewRecorder()
				f.h.ServeHTTP(w, r)
				codes <- w.Code
			})
		}
		wg.Wait()
		close(codes)
		got := []int{}
		for c := range codes {
			got = append(got, c)
		}
		sort.Ints(got)
		if !reflect.DeepEqual(got, []int{200, 409}) {
			t.Fatal("conflict must commit once", got)
		}
		history := factoryItems(t, f.call("GET", base+"/changes", nil, 200), "changes")
		if len(history) != 1 {
			t.Fatal("duplicate or missing receipt")
		}
		receipt := history[0].(map[string]any)
		factoryEqual(t, factoryRecordValues(factoryItems(t, receipt, "additions")), factoryRecordValues([]any{winner}))
		f.requireObservation(base, factoryObservation{List: append(initial, winner), Gets: []any{winner}, Changes: history})
	})
	t.Run("sqlite-rollback", factoryRollback)
}
