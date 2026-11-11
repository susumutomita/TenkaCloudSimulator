package guesttcp

import (
	"strings"
	"testing"
)

func TestMonotonicCommandVersionAndFieldIsolation(t *testing.T) {
	b := `{"version":2,"command":"start","binding":{"owner":"` + strings.Repeat("a", 64) + `","deployment":"` + strings.Repeat("b", 64) + `","lease":"` + strings.Repeat("c", 64) + `","backendIncarnation":"` + strings.Repeat("d", 64) + `"},"service":"web","key":"` + strings.Repeat("e", 32) + `","deadline":{"domain":"mono-ab","ticksMs":12345}}`
	if _, e := decodeProbeCommandVersion([]byte(b), 2, "ticksMs"); e != nil {
		t.Fatal(e)
	}
	if _, e := DecodeHTTPProbeCommand([]byte(b)); e == nil {
		t.Fatal("v2 accepted as Unix")
	}
	for _, bad := range []string{strings.Replace(b, `"version":2`, `"version":1`, 1), strings.Replace(b, "ticksMs", "epochMs", 1), strings.Replace(b, `"ticksMs":12345`, `"ticksMs":12345,"ticksMs":12346`, 1)} {
		if _, e := decodeProbeCommandVersion([]byte(bad), 2, "ticksMs"); e == nil {
			t.Fatal("mismatched command accepted")
		}
	}
}
