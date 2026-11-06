// SPDX-License-Identifier: MIT
// Original TenkaCloudSimulator serialization for its bounded AWS projections.
package aws

import "encoding/json"

// awsOwnedWireField is an explicit provider-selected JSON member. Omission is
// decided by the operation's contract, not reflection tags on stored domain data.
type awsOwnedWireField struct {
	name  string
	value any
	omit  bool
}

// marshalAWSOwnedWireObject preserves member order for existing SQLite documents
// and snapshots. Values still use the standard encoder: maps sort their keys,
// strings retain JSON escaping, nil collections stay null, and NaN/Inf fail.
// The byte-slice assembly has no I/O failure; every value-encoding error returns
// before a complete object is published. Field names are fixed source literals.
func marshalAWSOwnedWireObject(fields ...awsOwnedWireField) ([]byte, error) {
	out := []byte{'{'}
	first := true
	for _, field := range fields {
		if field.omit {
			continue
		}
		value, err := json.Marshal(field.value)
		if err != nil {
			return nil, err
		}
		if !first {
			out = append(out, ',')
		}
		first = false
		// json.Marshal of a string cannot fail and retains the standard escaping
		// behavior if a future provider-selected name contains special characters.
		name, _ := json.Marshal(field.name)
		out = append(out, name...)
		out = append(out, ':')
		out = append(out, value...)
	}
	return append(out, '}'), nil
}
