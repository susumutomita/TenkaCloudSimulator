package server

import (
	"fmt"
	"testing"
)

func TestProviderPrefixPreservesLiteralKeys(t *testing.T) {
	s := instance(t)
	status(t, s, "PUT", "/prefix-bucket", "", 200, nil)
	status(t, s, "PUT", "/prefix-bucket/victim", "sentinel", 200, nil)
	for i, key := range []string{"a/../victim", "a/./victim", "a//victim"} {
		root := "/prefix-bucket/" + key
		prefixed := "/_simulator/providers/aws" + root
		before := fmt.Sprintf("literal-%d", i)
		status(t, s, "PUT", root, before, 200, nil)
		if got := status(t, s, "GET", prefixed, "", 200, nil); string(got) != before {
			t.Fatalf("prefixed GET changed key %q: %q", key, got)
		}
		status(t, s, "PUT", prefixed, "replacement", 200, nil)
		if got := status(t, s, "GET", root, "", 200, nil); string(got) != "replacement" {
			t.Fatalf("prefixed PUT changed key %q: %q", key, got)
		}
		status(t, s, "DELETE", prefixed, "", 204, nil)
		status(t, s, "GET", root, "", 404, nil)
		if got := status(t, s, "GET", "/prefix-bucket/victim", "", 200, nil); string(got) != "sentinel" {
			t.Fatal("neighbor object changed", string(got))
		}
	}
}
