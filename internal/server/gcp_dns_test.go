package server

// Test-only composition oracle. Uses the normal server registry, token access
// layer and real numeric-loopback HTTP; never registers a test DNS route.
// The approved leaf oracle covers SQL faults; these tests cover composition.
import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type dnsCompositionHost struct {
	server         *httptest.Server
	client         *http.Client
	token, project string
}

func dnsCompositionStart(t *testing.T, cfg CloudStorageConfig, letter string) dnsCompositionHost {
	t.Helper()
	handler, closeState, err := NewWithCloudStorage(filepath.Join(t.TempDir(), "state.db"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat(letter, 64)
	access, err := NewAccess(handler, token, false)
	if err != nil {
		_ = closeState()
		t.Fatal(err)
	}
	srv := httptest.NewServer(access)
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		srv.Close()
		if err := closeState(); err != nil {
			t.Error(err)
		}
	})
	return dnsCompositionHost{srv, client, token, cfg.GCPProject}
}
func (h dnsCompositionHost) root() string {
	return "/_simulator/providers/gcp/dns/v1/projects/" + h.project + "/managedZones"
}
func (h dnsCompositionHost) call(t *testing.T, method, path, token, selected string, body any, want int) map[string]any {
	t.Helper()
	var data io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		data = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, h.server.URL+path, data)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Simulator-Token", token)
	}
	if selected != "" {
		req.Header.Set("X-Simulator-Principal", selected)
	}
	response, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s: status=%d want=%d body=%q", method, path, response.StatusCode, want, raw)
	}
	out := map[string]any{}
	if len(raw) > 0 && want < 400 {
		if err = json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}
func dnsCompositionA(ip string) map[string]any {
	return map[string]any{"name": "www.example.test.", "type": "A", "ttl": 300, "rrdatas": []string{ip}}
}
func dnsCompositionChange(add, del []any) map[string]any {
	return map[string]any{"additions": add, "deletions": del}
}
func dnsCompositionJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func (h dnsCompositionHost) create(t *testing.T, zone, token string) {
	t.Helper()
	out := h.call(t, "POST", h.root(), token, "", map[string]any{"name": zone, "dnsName": "example.test.", "description": "composition oracle"}, 200)
	if out["name"] != zone || out["dnsName"] != "example.test." || out["id"] == nil {
		t.Fatalf("create returned no zone: %#v", out)
	}
}
func (h dnsCompositionHost) snapshot(t *testing.T, zone, token string) string {
	t.Helper()
	base := h.root() + "/" + zone
	return dnsCompositionJSON([]any{h.call(t, "GET", h.root(), token, "", nil, 200), h.call(t, "GET", base, token, "", nil, 200), h.call(t, "GET", base+"/rrsets", token, "", nil, 200), h.call(t, "GET", base+"/changes", token, "", nil, 200)})
}

// Require nonempty apex NS/SOA even when cleanup expects zero user records.
// Compare all user data, rather than merely counting returned rows.
func dnsCompositionRecords(body map[string]any, want map[string]string) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var list struct {
		Records []struct {
			Name, Type string
			TTL        int
			Data       []string `json:"rrdatas"`
		} `json:"rrsets"`
	}
	if err = json.Unmarshal(raw, &list); err != nil {
		return err
	}
	apex := map[string]bool{}
	got := map[string]string{}
	for _, r := range list.Records {
		if r.Name == "example.test." && (r.Type == "NS" || r.Type == "SOA") {
			if apex[r.Type] || len(r.Data) == 0 {
				return fmt.Errorf("invalid duplicate/empty apex")
			}
			apex[r.Type] = true
			continue
		}
		if r.Type != "A" || r.TTL != 300 || len(r.Data) != 1 {
			return fmt.Errorf("unexpected RRset shape")
		}
		if _, exists := got[r.Name]; exists {
			return fmt.Errorf("duplicate RRset")
		}
		got[r.Name] = r.Data[0]
	}
	if !apex["NS"] || !apex["SOA"] {
		return fmt.Errorf("missing initial apex records")
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("RRset values mismatch: got %v want %v", got, want)
	}
	return nil
}
func (h dnsCompositionHost) records(t *testing.T, zone, token, ip string) {
	t.Helper()
	want := map[string]string{}
	if ip != "" {
		want["www.example.test."] = ip
	}
	body := h.call(t, "GET", h.root()+"/"+zone+"/rrsets", token, "", nil, 200)
	if err := dnsCompositionRecords(body, want); err != nil {
		t.Fatal(err)
	}
	if ip != "" {
		out := h.call(t, "GET", h.root()+"/"+zone+"/rrsets/www.example.test./A", token, "", nil, 200)
		if out["name"] != "www.example.test." || out["type"] != "A" || out["ttl"] != float64(300) || dnsCompositionJSON(out["rrdatas"]) != dnsCompositionJSON([]string{ip}) {
			t.Fatal("get did not observe expected RRset", out)
		}
	}
}
func TestGCPDNSAuthenticatedComposition(t *testing.T) {
	t.Run("normal-route-crud", func(t *testing.T) {
		h := dnsCompositionStart(t, DefaultCloudStorageConfig(), "a")
		h.create(t, "normal-zone", h.token)
		base := h.root() + "/normal-zone"
		h.records(t, "normal-zone", h.token, "")
		a, b := dnsCompositionA("192.0.2.1"), dnsCompositionA("192.0.2.2")
		receipt := h.call(t, "POST", base+"/changes", h.token, "", dnsCompositionChange([]any{a}, nil), 200)
		id, ok := receipt["id"].(string)
		if !ok || id == "" || receipt["status"] != "done" {
			t.Fatal("missing completed receipt", receipt)
		}
		got := h.call(t, "GET", base+"/changes/"+id, h.token, "", nil, 200)
		if dnsCompositionJSON(got) != dnsCompositionJSON(receipt) {
			t.Fatal("change get differs")
		}
		history := h.call(t, "GET", base+"/changes", h.token, "", nil, 200)
		if dnsCompositionJSON(history["changes"]) != dnsCompositionJSON([]any{receipt}) {
			t.Fatal("change list differs")
		}
		h.records(t, "normal-zone", h.token, "192.0.2.1")
		h.call(t, "POST", base+"/changes", h.token, "", dnsCompositionChange([]any{b}, []any{a}), 200)
		h.records(t, "normal-zone", h.token, "192.0.2.2")
		h.call(t, "DELETE", base, h.token, "", nil, 400)
		h.call(t, "POST", base+"/changes", h.token, "", dnsCompositionChange(nil, []any{b}), 200)
		h.records(t, "normal-zone", h.token, "")
		h.call(t, "GET", base+"/rrsets/www.example.test./A", h.token, "", nil, 404)
		h.call(t, "DELETE", base, h.token, "", nil, 200)
		h.call(t, "GET", base, h.token, "", nil, 404)
		empty := h.call(t, "GET", h.root(), h.token, "", nil, 200)
		if dnsCompositionJSON(empty["managedZones"]) != "[]" {
			t.Fatal("cleanup retained zone", empty)
		}
	})
	t.Run("owner-project-denial-preserves-state", func(t *testing.T) {
		cfg := DefaultCloudStorageConfig()
		cfg.GCPProject = "project-one"
		cfg.GCPOwner = "learner"
		a := dnsCompositionStart(t, cfg, "b")
		cfg.GCPProject = "project-two"
		cfg.GCPProjectNumber = "100000000002"
		b := dnsCompositionStart(t, cfg, "c")
		mint := func(h dnsCompositionHost, who string) string {
			out := h.call(t, "POST", "/_simulator/v1/access", h.token, "", map[string]string{"Principal": who}, 200)
			token, ok := out["AccessToken"].(string)
			if !ok || token == "" {
				t.Fatal("no participant token")
			}
			return token
		}
		ownerA, ownerB, outsider := mint(a, "learner"), mint(b, "learner"), mint(a, "outsider")
		for i, h := range []dnsCompositionHost{a, b} {
			token := []string{ownerA, ownerB}[i]
			h.create(t, "same-zone", token)
			h.call(t, "POST", h.root()+"/same-zone/changes", token, "", dnsCompositionChange([]any{dnsCompositionA([]string{"192.0.2.1", "192.0.2.9"}[i])}, nil), 200)
		}
		beforeA, beforeB := a.snapshot(t, "same-zone", ownerA), b.snapshot(t, "same-zone", ownerB)
		check := func() {
			if a.snapshot(t, "same-zone", ownerA) != beforeA || b.snapshot(t, "same-zone", ownerB) != beforeB {
				t.Fatal("denial mutated either project")
			}
		}
		base := a.root() + "/same-zone"
		for _, tc := range []struct {
			token, selected, path string
			code                  int
		}{
			{"", "", base + "/changes", 401}, {"invalid", "", base + "/changes", 401},
			{a.token, "", base + "/changes", 403}, {outsider, "", base + "/changes", 403},
			{outsider, "learner", base + "/changes", 403}, {ownerA, "simulator-admin", base + "/changes", 403},
			{ownerB, "", base + "/changes", 401},
			{ownerA, "", strings.Replace(base, "project-one", "project-two", 1) + "/changes", 403},
		} {
			a.call(t, "POST", tc.path, tc.token, tc.selected, dnsCompositionChange(nil, []any{dnsCompositionA("192.0.2.1")}), tc.code)
			check()
		}
		a.call(t, "GET", strings.Replace(base, "project-one", "project-two", 1), ownerA, "", nil, 403)
		check()
		b.records(t, "same-zone", ownerB, "192.0.2.9")
		for i, h := range []dnsCompositionHost{a, b} {
			token := []string{ownerA, ownerB}[i]
			h.call(t, "POST", h.root()+"/same-zone/changes", token, "", dnsCompositionChange(nil, []any{dnsCompositionA([]string{"192.0.2.1", "192.0.2.9"}[i])}), 200)
			h.call(t, "DELETE", h.root()+"/same-zone", token, "", nil, 200)
		}
	})
}
func TestGCPDNSCompositionOracleNonVacuity(t *testing.T) {
	apex := []any{map[string]any{"name": "example.test.", "type": "NS", "rrdatas": []string{"ns.invalid."}}, map[string]any{"name": "example.test.", "type": "SOA", "rrdatas": []string{"synthetic"}}}
	a, b := dnsCompositionA("192.0.2.1"), dnsCompositionA("192.0.2.2")
	want := map[string]string{"www.example.test.": "192.0.2.2"}
	for name, rows := range map[string][]any{"empty": {}, "apex-only": apex, "wrong-value": append(append([]any{}, apex...), a), "duplicate": append(append([]any{}, apex...), b, b)} {
		t.Run(name, func(t *testing.T) {
			if dnsCompositionRecords(map[string]any{"rrsets": rows}, want) == nil {
				t.Fatal("oracle accepted defective observation")
			}
		})
	}
	if dnsCompositionRecords(map[string]any{"rrsets": append(append([]any{}, apex...), b)}, want) != nil {
		t.Fatal("correct record rejected")
	}
	if dnsCompositionRecords(map[string]any{"rrsets": apex}, map[string]string{}) != nil {
		t.Fatal("correct cleanup rejected")
	}
	if dnsCompositionRecords(map[string]any{"rrsets": append(append([]any{}, apex...), a)}, map[string]string{}) == nil {
		t.Fatal("cleanup oracle accepted deletion no-op")
	}
}
