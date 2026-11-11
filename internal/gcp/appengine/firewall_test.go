package appengine

import (
	"fmt"
	"net/netip"
	"testing"
)

func TestFirewallRulesKeepAPermanentDefaultRuleAndUniquePriorities(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	rules := base + "/firewall/ingressRules"
	h.fails("NOT_FOUND", "POST", rules, map[string]any{"priority": 1, "action": "ALLOW", "sourceRange": "*"})
	h.fails("NOT_FOUND", "GET", rules, nil)
	h.fails("NOT_FOUND", "GET", rules+"/1", nil)
	h.fails("NOT_FOUND", "PATCH", rules+"/1?updateMask=action", map[string]any{"action": "DENY"})
	h.fails("NOT_FOUND", "DELETE", rules+"/1", nil)
	h.fails("NOT_FOUND", "POST", rules+":batchUpdate", map[string]any{})
	h.createApp("us-central")
	fallback := h.ok("GET", rules+"/2147483647", nil)
	if fallback["action"] != "ALLOW" || fallback["sourceRange"] != "*" {
		t.Fatalf("default rule: %v", fallback)
	}
	for _, body := range []map[string]any{
		{"action": "ALLOW", "sourceRange": "*"},
		{"priority": 0, "action": "ALLOW", "sourceRange": "*"},
		{"priority": 2147483647, "action": "ALLOW", "sourceRange": "*"},
		{"priority": "ten", "action": "ALLOW", "sourceRange": "*"},
		{"priority": 10, "action": "MAYBE", "sourceRange": "*"},
		{"priority": 10, "action": "ALLOW", "sourceRange": "10.0.0.0/99"},
		{"priority": 10, "action": "ALLOW", "sourceRange": "example.com"},
		{"priority": 10, "action": "ALLOW", "sourceRange": 10},
		{"priority": 10, "action": "ALLOW", "sourceRange": "*", "description": string(make([]rune, 401))},
		{"priority": 10, "action": "ALLOW", "sourceRange": "*", "comment": "x"},
	} {
		h.fails("INVALID_ARGUMENT", "POST", rules, body)
	}
	created := h.ok("POST", rules, map[string]any{"priority": 100, "action": "DENY", "sourceRange": "192.0.2.0/24", "description": "documentation range"})
	if created["priority"] != float64(100) || created["description"] != "documentation range" {
		t.Fatalf("created: %v", created)
	}
	h.ok("POST", rules, map[string]any{"priority": 200, "action": "ALLOW", "sourceRange": "2001:db8::1"})
	h.fails("ALREADY_EXISTS", "POST", rules, map[string]any{"priority": 100, "action": "ALLOW", "sourceRange": "*"})
	matching := h.ok("GET", rules+"?matchingAddress=192.0.2.7", nil)["ingressRules"].([]any)
	if len(matching) != 2 || matching[0].(map[string]any)["priority"] != float64(100) {
		t.Fatalf("matching: %v", matching)
	}
	h.fails("INVALID_ARGUMENT", "GET", rules+"?matchingAddress=nowhere", nil)
	page := h.ok("GET", rules+"?pageSize=2", nil)
	if len(page["ingressRules"].([]any)) != 2 || page["nextPageToken"] == nil {
		t.Fatalf("page: %v", page)
	}
	if last := h.ok("GET", rules+"?pageToken="+page["nextPageToken"].(string), nil)["ingressRules"].([]any); len(last) != 1 || last[0].(map[string]any)["priority"] != float64(defaultRule) {
		t.Fatalf("last page: %v", last)
	}
	patched := h.ok("PATCH", rules+"/100?updateMask=action,description", map[string]any{"action": "ALLOW", "sourceRange": "ignored"})
	if patched["action"] != "ALLOW" || patched["sourceRange"] != "192.0.2.0/24" || patched["description"] != nil {
		t.Fatalf("patched: %v", patched)
	}
	h.ok("PATCH", rules+"/2147483647?updateMask=action", map[string]any{"action": "DENY"})
	h.fails("INVALID_ARGUMENT", "PATCH", rules+"/2147483647?updateMask=sourceRange", map[string]any{"sourceRange": "10.0.0.1"})
	h.fails("INVALID_ARGUMENT", "PATCH", rules+"/100?updateMask=priority", map[string]any{"priority": 5})
	h.fails("INVALID_ARGUMENT", "PATCH", rules+"/100?updateMask=action", map[string]any{"action": "NONE"})
	h.fails("INVALID_ARGUMENT", "PATCH", rules+"/100?updateMask=action", map[string]any{"verdict": "x"})
	h.fails("INVALID_ARGUMENT", "PATCH", rules+"/abc?updateMask=action", map[string]any{"action": "DENY"})
	h.fails("NOT_FOUND", "PATCH", rules+"/300?updateMask=action", map[string]any{"action": "DENY"})
	h.fails("FAILED_PRECONDITION", "DELETE", rules+"/2147483647", nil)
	h.fails("NOT_FOUND", "DELETE", rules+"/300", nil)
	h.fails("INVALID_ARGUMENT", "DELETE", rules+"/0", nil)
	h.fails("INVALID_ARGUMENT", "GET", rules+"/2147483648", nil)
	h.fails("NOT_FOUND", "GET", rules+"/300", nil)
	if r := h.ok("DELETE", rules+"/200", nil); len(r) != 0 {
		t.Fatalf("delete returns Empty: %v", r)
	}
}

func TestBatchUpdateReplacesEveryRuleAndRestoresAnAllowAllDefault(t *testing.T) {
	h := newHarness(t, options{runner: &fakeRunner{}})
	h.createApp("us-central")
	path := base + "/firewall/ingressRules:batchUpdate"
	h.ok("POST", base+"/firewall/ingressRules", map[string]any{"priority": 5, "action": "DENY", "sourceRange": "*"})
	h.ok("PATCH", base+"/firewall/ingressRules/2147483647?updateMask=action", map[string]any{"action": "DENY"})
	out := h.ok("POST", path, map[string]any{"ingressRules": []any{
		map[string]any{"priority": 20, "action": "ALLOW", "sourceRange": "127.0.0.1"},
		map[string]any{"priority": 10, "action": "DENY", "sourceRange": "10.0.0.0/8"},
	}})
	if fmt.Sprint(out["ingressRules"]) != "[map[action:DENY priority:10 sourceRange:10.0.0.0/8] map[action:ALLOW priority:20 sourceRange:127.0.0.1] map[action:ALLOW description:The default action. priority:2.147483647e+09 sourceRange:*]]" {
		t.Fatalf("batch: %v", out)
	}
	out = h.ok("POST", path, map[string]any{"ingressRules": []any{map[string]any{"priority": 2147483647, "action": "DENY", "sourceRange": "*"}}})
	if len(out["ingressRules"].([]any)) != 1 || out["ingressRules"].([]any)[0].(map[string]any)["action"] != "DENY" {
		t.Fatalf("default-only batch: %v", out)
	}
	h.fails("INVALID_ARGUMENT", "POST", path, map[string]any{"ingressRules": []any{map[string]any{"priority": 1, "action": "ALLOW", "sourceRange": "*"}, map[string]any{"priority": 1, "action": "DENY", "sourceRange": "*"}}})
	h.fails("INVALID_ARGUMENT", "POST", path, map[string]any{"ingressRules": []any{nil}})
	h.fails("INVALID_ARGUMENT", "POST", path, map[string]any{"ingressRules": "all"})
	h.fails("INVALID_ARGUMENT", "POST", path, map[string]any{"ingressRules": []any{map[string]any{"priority": 1, "action": "ALLOW"}}})
	h.fails("INVALID_ARGUMENT", "POST", path, map[string]any{"rules": []any{}})
	many := []any{}
	for i := 1; i <= maxFirewallRules+2; i++ {
		many = append(many, map[string]any{"priority": i, "action": "ALLOW", "sourceRange": "*"})
	}
	h.fails("INVALID_ARGUMENT", "POST", path, map[string]any{"ingressRules": many})
	h.ok("POST", path, map[string]any{"ingressRules": many[:maxFirewallRules]})
	h.fails("RESOURCE_EXHAUSTED", "POST", base+"/firewall/ingressRules", map[string]any{"priority": 500, "action": "ALLOW", "sourceRange": "*"})
	if all := h.ok("POST", path, map[string]any{})["ingressRules"].([]any); len(all) != 1 || all[0].(map[string]any)["action"] != "ALLOW" {
		t.Fatalf("empty batch keeps only an allow-all default: %v", all)
	}
}

func TestFirewallMatchingUsesTheFirstRuleInPriorityOrder(t *testing.T) {
	rules := []rule{{1, "DENY", "10.1.0.0/16", ""}, {2, "ALLOW", "10.0.0.0/8", ""}, {3, "ALLOW", "2001:db8::1", ""}, {defaultRule, "DENY", "*", ""}}
	for address, want := range map[string]bool{"10.1.2.3": false, "10.2.0.1": true, "2001:db8::1": true, "::ffff:10.2.0.1": true, "192.0.2.1": false} {
		if got := allowed(rules, netip.MustParseAddr(address).Unmap()); got != want {
			t.Errorf("%s: %v", address, got)
		}
	}
	if allowed(nil, netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("no rules must not allow")
	}
	if (rule{SourceRange: "bad"}).matches(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("a corrupt range must not match")
	}
	for remote, ok := range map[string]bool{"127.0.0.1:80": true, "[::1]:80": true, "::1": true, "[::1]": true, "nonsense": false} {
		if _, err := clientAddress(remote); (err == nil) != ok {
			t.Errorf("client address %s: %v", remote, err)
		}
	}
}
