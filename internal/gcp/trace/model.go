// Package trace persists observations for the local Google Cloud Trace v1 API.
package trace

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/gcp/internal/jsonwire"
)

const (
	maxBody         = 256 << 10
	maxBatch        = 100
	maxProjectSpans = 1024
	maxTraceSpans   = 128
	retention       = 30 * 24 * time.Hour
	stampLayout     = "2006-01-02T15:04:05.000000000Z"
)

type span struct {
	ID     string            `json:"spanId"`
	Parent string            `json:"parentSpanId,omitempty"`
	Name   string            `json:"name"`
	Kind   string            `json:"kind,omitempty"`
	Start  string            `json:"startTime"`
	End    string            `json:"endTime"`
	Labels map[string]string `json:"labels,omitempty"`
}

type traceRecord struct {
	Project string `json:"projectId"`
	ID      string `json:"traceId"`
	Spans   []span `json:"spans,omitempty"`
}

type batch struct {
	Traces []traceRecord `json:"traces"`
}

func validTraceID(id string) bool {
	if len(id) != 32 || id == strings.Repeat("0", 32) || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func validSpanID(id string) bool {
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n != 0 && strconv.FormatUint(n, 10) == id
}

// Go's RFC3339 parser also accepts single-digit hours, comma fractions,
// extra precision and normalized out-of-range offsets. Require the protobuf
// Timestamp representation before asking time.Parse to check the calendar.
var timestampPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func stamp(raw string) (time.Time, error) {
	if !timestampPattern.MatchString(raw) {
		return time.Time{}, invalid("An RFC 3339 timestamp with at most nanosecond precision is required.")
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || t.UTC().Year() < 1 || t.UTC().Year() > 9999 {
		return time.Time{}, invalid("An RFC 3339 timestamp is required.")
	}
	return t.UTC(), nil
}

func (s *span) normalize(now time.Time) error {
	if !validSpanID(s.ID) || (s.Parent != "" && s.Parent != "0" && !validSpanID(s.Parent)) {
		return invalid("Use decimal strings for span IDs. The span ID must be nonzero.")
	}
	if s.Name == "" || len(s.Name) >= 128 {
		return invalid("The span name must contain between 1 and 127 bytes.")
	}
	switch s.Kind {
	case "", "SPAN_KIND_UNSPECIFIED", "RPC_CLIENT", "RPC_SERVER":
	default:
		return invalid("Unknown v1 span kind.")
	}
	if len(s.Labels) > 16 {
		return invalid("The simulator accepts at most 16 labels per span.")
	}
	for key, value := range s.Labels {
		if key == "" || len(key) >= 128 || len(value) > 1024 {
			return invalid("The simulator accepts 1–127 byte label keys and values up to 1024 bytes.")
		}
	}
	start, err := stamp(s.Start)
	if err != nil {
		return err
	}
	end, err := stamp(s.End)
	if err != nil {
		return err
	}
	if end.Before(start) || start.Before(now.Add(-14*24*time.Hour)) || end.After(now.Add(3*24*time.Hour)) {
		return invalid("The span interval must be ordered and within the local ingestion window.")
	}
	s.Start, s.End = start.Format(stampLayout), end.Format(stampLayout)
	return nil
}

// The standard struct decoder also accepts case-insensitive names and repeated
// members. Check the wire names before decoding so one request has one meaning.
var members = map[string]map[string]bool{
	"batch":  {"traces": true},
	"traces": {"projectId": true, "traceId": true, "spans": true},
	"spans":  {"spanId": true, "parentSpanId": true, "name": true, "kind": true, "startTime": true, "endTime": true, "labels": true},
}

func checkMembers(d *json.Decoder, parent string, depth int) error {
	if depth > 7 {
		return invalid("Trace JSON is too deeply nested.")
	}
	token, err := d.Token()
	if err != nil || token == nil {
		return invalid("Malformed or null trace member.")
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			key, ok := k.(string)
			if err != nil || !ok || seen[key] || (parent != "labels" && !members[parent][key]) {
				return invalid("Unknown or duplicate trace member.")
			}
			seen[key] = true
			if err := checkMembers(d, key, depth+1); err != nil {
				return err
			}
		}
	case json.Delim('['):
		if parent != "traces" && parent != "spans" {
			return invalid("Unexpected trace array.")
		}
		for d.More() {
			if err := checkMembers(d, parent, depth+1); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	_, err = d.Token()
	return err
}

func decodeBatch(raw []byte, project string, now time.Time) ([]traceRecord, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	if len(raw) > maxBody || !jsonwire.ValidUnicode(raw) {
		return nil, invalid("Invalid UTF-8 or request too large.")
	}
	if err := checkMembers(d, "batch", 0); err != nil {
		return nil, invalid("Invalid trace JSON members.")
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, invalid("Trailing trace JSON.")
	}
	var b batch
	if err := json.Unmarshal(raw, &b); err != nil || len(b.Traces) == 0 || len(b.Traces) > maxBatch {
		return nil, invalid("Supply a nonempty array of traces within the local request limit.")
	}
	count := 0
	for i := range b.Traces {
		t := &b.Traces[i]
		if t.Project != project {
			return nil, invalid("A trace project must match the request project.")
		}
		if !validTraceID(t.ID) || len(t.Spans) == 0 {
			return nil, invalid("Each trace requires a nonzero 32-character lowercase hexadecimal ID and at least one span.")
		}
		count += len(t.Spans)
		if count > maxBatch {
			return nil, invalid("The simulator accepts at most 100 spans per request.")
		}
		for j := range t.Spans {
			if err := t.Spans[j].normalize(now); err != nil {
				return nil, err
			}
		}
	}
	return b.Traces, nil
}
