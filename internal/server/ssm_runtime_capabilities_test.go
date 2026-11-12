package server

import "testing"

func TestNativeSSMCapabilitiesRequireConfiguredRuntime(t *testing.T) {
	for name, describe := range map[string]func(bool) map[string]any{
		"commands": nativeCommandCapabilities,
		"sessions": nativeSessionCapabilities,
	} {
		t.Run(name, func(t *testing.T) {
			off := describe(false)
			if off["enabled"] != false || len(off["operations"].([]string)) != 0 {
				t.Fatal("default mode advertised runtime operations")
			}
			on := describe(true)
			if on["enabled"] != true || len(on["operations"].([]string)) != 3 || on["physicalReadiness"] == "" {
				t.Fatal("configured runtime must describe bounded operations and readiness limitation")
			}
		})
	}
}
