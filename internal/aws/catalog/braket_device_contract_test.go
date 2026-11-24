package catalog

import "testing"

func TestBraketSpendingLimitRequiresPhysicalDeviceARN(t *testing.T) {
	for _, tc := range []struct{ name, arn string }{
		{"simulator", "arn:aws:braket:::device/quantum-simulator/amazon/sv1"},
		{"missing-provider", "arn:aws:braket:us-east-1::device/qpu/Forte-1"},
		{"missing-region", "arn:aws:braket:::device/qpu/ionq/Forte-1"},
		{"nonempty-account", "arn:aws:braket:us-east-1:000000000000:device/qpu/ionq/Forte-1"},
		{"extra-path-segment", "arn:aws:braket:us-east-1::device/qpu/ionq/Forte-1/alias"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, braket)
			created := h.rest("braket", "POST", "/spending-limit", map[string]any{
				"deviceArn": tc.arn, "clientToken": "device-fixture", "spendingLimit": "25.50",
			})
			listed := h.rest("braket", "POST", "/spending-limits", map[string]any{})
			expect(t, listed, 200, "")
			rows := listed.body["spendingLimits"].([]any)
			t.Logf("create status: %d, stored rows: %d", created.status, len(rows))
			expect(t, created, 400, "ValidationException")
			if len(rows) != 0 {
				t.Fatalf("rejected malformed device limit persisted %d rows", len(rows))
			}
		})
	}
}
