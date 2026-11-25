package catalog

import "testing"

func TestMediaCatalogRejectsIPv4MappedIPv6CIDR(t *testing.T) {
	const mapped = "::ffff:192.0.2.0/120"
	t.Run("MediaConnect gateway", func(t *testing.T) {
		h := media2Harness(t)
		create := func(cidr string) reply {
			return h.rest("mediaconnect", "POST", "/v1/gateways", map[string]any{
				"name": "gateway", "egressCidrBlocks": []any{cidr},
				"networks": []any{map[string]any{"name": "lan", "cidrBlock": "10.1.0.0/24"}},
			})
		}
		bad := create(mapped)
		list := h.rest("mediaconnect", "GET", "/v1/gateways", nil)
		expect(t, list, 200, "")
		t.Logf("create status=%d, stored rows=%d", bad.status, len(list.body["gateways"].([]any)))
		expect(t, bad, 400, "BadRequestException")
		if len(list.body["gateways"].([]any)) != 0 {
			t.Fatal("invalid metadata persisted")
		}
		expect(t, create("192.0.2.0/24"), 201, "")
	})
	t.Run("MediaLive whitelist update", func(t *testing.T) {
		h := media2Harness(t)
		rules := func(cidr string) map[string]any {
			return map[string]any{"whitelistRules": []any{map[string]any{"cidr": cidr}}}
		}
		const original = "192.0.2.0/24"
		made := h.rest("medialive", "POST", "/prod/inputSecurityGroups", rules(original))
		expect(t, made, 200, "")
		id := str(member(t, made.body, "securityGroup")["id"])
		path := "/prod/inputSecurityGroups/" + id
		bad := h.rest("medialive", "PUT", path, rules(mapped))
		got := h.rest("medialive", "GET", path, nil)
		expect(t, got, 200, "")
		saved := got.body["whitelistRules"].([]any)[0].(map[string]any)["cidr"]
		t.Logf("update status=%d, original retained=%t", bad.status, saved == original)
		expect(t, bad, 400, "BadRequestException")
		if saved != original {
			t.Fatal("rejected update changed stored metadata")
		}
	})
}
