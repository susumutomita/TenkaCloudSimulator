package dns

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

// These pure observation checks can run without any DNS implementation. They
// validate the comparator used by the lifecycle test, not the service itself.
func factoryA(name, ip string) map[string]any {
	return map[string]any{"name": name, "type": "A", "ttl": float64(300), "rrdatas": []any{ip}}
}
func factoryCanonical(v any) string { b, _ := json.Marshal(v); return string(b) }
func factoryRecordValues(items []any) []string {
	out := []string{}
	for _, item := range items {
		m := item.(map[string]any)
		out = append(out, factoryCanonical([]any{m["name"], m["type"], m["ttl"], m["rrdatas"]}))
	}
	sort.Strings(out)
	return out
}

func TestCloudDNSOracleRejectsNoopAndPartialWrites(t *testing.T) {
	a, b := factoryA("a.example.test.", "192.0.2.1"), factoryA("b.example.test.", "192.0.2.2")
	want := factoryRecordValues([]any{a, b})
	for _, got := range [][]any{nil, {a}, {b}, {a, a}, {a, factoryA("b.example.test.", "192.0.2.99")}} {
		if reflect.DeepEqual(factoryRecordValues(got), want) {
			t.Fatal("add oracle accepted no-op, partial or wrong write", got)
		}
	}
	if !reflect.DeepEqual(factoryRecordValues([]any{b, a}), want) {
		t.Fatal("record order should not matter")
	}
	if reflect.DeepEqual(factoryRecordValues([]any{a}), factoryRecordValues(nil)) {
		t.Fatal("delete oracle accepted no-op")
	}
}

// A successful receipt is insufficient: both full-list and individual-get
// observations must match independently fixed data. Rollback uses this same
// predicate with the old data and the complete pre-change receipt collection.
type factoryObservation struct {
	List, Gets, Changes []any
}

func factoryCheckObservation(got, want factoryObservation) error {
	if !reflect.DeepEqual(factoryRecordValues(got.List), factoryRecordValues(want.List)) {
		return fmt.Errorf("RRset list mismatch: got %s want %s", factoryCanonical(got.List), factoryCanonical(want.List))
	}
	if !reflect.DeepEqual(factoryRecordValues(got.Gets), factoryRecordValues(want.Gets)) {
		return fmt.Errorf("RRset get mismatch: got %s want %s", factoryCanonical(got.Gets), factoryCanonical(want.Gets))
	}
	if factoryCanonical(got.Changes) != factoryCanonical(want.Changes) {
		return fmt.Errorf("change history mismatch: got %s want %s", factoryCanonical(got.Changes), factoryCanonical(want.Changes))
	}
	return nil
}

func TestCloudDNSOracleReplacementAndSuccessNegativeControls(t *testing.T) {
	oldA, oldB := factoryA("a.example.test.", "192.0.2.1"), factoryA("b.example.test.", "192.0.2.2")
	newA, newB := factoryA("a.example.test.", "192.0.2.11"), factoryA("b.example.test.", "192.0.2.22")
	sentinel := factoryA("sentinel.example.test.", "192.0.2.99")
	beforeHistory := []any{map[string]any{"id": "seed", "status": "done"}}
	afterHistory := append(append([]any{}, beforeHistory...), map[string]any{"id": "replace", "status": "done"})
	before := factoryObservation{[]any{sentinel, oldA, oldB}, []any{oldA, oldB}, beforeHistory}
	after := factoryObservation{[]any{sentinel, newA, newB}, []any{newA, newB}, afterHistory}
	winner := factoryA("race.example.test.", "192.0.2.4")
	winnerHistory := []any{map[string]any{"id": "winner", "status": "done"}}
	concurrent := factoryObservation{[]any{sentinel, winner}, []any{winner}, winnerHistory}
	cases := []struct {
		name      string
		got, want factoryObservation
	}{
		{"rollback-left-deletion", factoryObservation{[]any{sentinel, oldB}, []any{oldB}, beforeHistory}, before},
		{"rollback-left-partial-addition", factoryObservation{[]any{sentinel, newA, oldB}, []any{newA, oldB}, beforeHistory}, before},
		{"rollback-left-wrong-values", factoryObservation{[]any{sentinel, newA, newB}, []any{newA, newB}, beforeHistory}, before},
		{"rollback-left-receipt", factoryObservation{before.List, before.Gets, afterHistory}, before},
		{"retry-noop", before, after},
		{"retry-partial-apply", factoryObservation{[]any{sentinel, newA, oldB}, []any{newA, oldB}, afterHistory}, after},
		{"retry-wrong-value", factoryObservation{[]any{sentinel, newA, factoryA("b.example.test.", "192.0.2.222")}, []any{newA, factoryA("b.example.test.", "192.0.2.222")}, afterHistory}, after},
		{"retry-history-only-success", factoryObservation{before.List, before.Gets, afterHistory}, after},
		{"retry-list-only-success", factoryObservation{after.List, before.Gets, afterHistory}, after},
		{"retry-get-only-success", factoryObservation{before.List, after.Gets, afterHistory}, after},
		{"concurrent-winner-history-only", factoryObservation{[]any{sentinel}, nil, winnerHistory}, concurrent},
		{"concurrent-winner-wrong-value", factoryObservation{[]any{sentinel, factoryA("race.example.test.", "192.0.2.44")}, []any{factoryA("race.example.test.", "192.0.2.44")}, winnerHistory}, concurrent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := factoryCheckObservation(tc.got, tc.want); err == nil {
				t.Fatal("oracle accepted defective observation")
			} else {
				t.Logf("rejected: %s", err)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		value factoryObservation
	}{{"rollback-correct", before}, {"retry-correct", after}, {"concurrent-correct", concurrent}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := factoryCheckObservation(tc.value, tc.value); err != nil {
				t.Fatal(err)
			}
		})
	}
}
