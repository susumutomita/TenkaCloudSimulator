package guesttcp

import (
	"testing"

	gvisorsync "gvisor.dev/gvisor/pkg/sync"
)

func TestDependencyRaceCASPreservesOperands(t *testing.T) {
	for _, tc := range []struct {
		name               string
		initial, old, next uintptr
		want               bool
		value              uintptr
	}{
		{"replace matching value", 1, 1, 2, true, 2},
		{"reject different expected value", 2, 1, 3, false, 2},
		{"replacement is not expected value", 3, 1, 3, false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := tc.initial
			got := gvisorsync.RaceUncheckedAtomicCompareAndSwapUintptr(&value, tc.old, tc.next)
			if got != tc.want || value != tc.value {
				t.Fatalf("CAS returned (%t, %d), want (%t, %d)", got, value, tc.want, tc.value)
			}
		})
	}
}
