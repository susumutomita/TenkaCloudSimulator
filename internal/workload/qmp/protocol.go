package qmp

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// Reject duplicate keys at every depth, excessive nesting, invalid UTF-8 and
// trailing JSON. encoding/json alone silently accepts duplicate object keys.
func validateJSON(data []byte) error {
	if !utf8.Valid(data) {
		return ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := value(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrProtocol
	}
	return nil
}
func value(d *json.Decoder, depth int) error {
	if depth > 32 {
		return ErrLimit
	}
	t, err := d.Token()
	if err != nil {
		return ErrProtocol
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for d.More() {
			t, err = d.Token()
			key, ok := t.(string)
			if err != nil || !ok || keys[key] {
				return ErrProtocol
			}
			keys[key] = true
			if err = value(d, depth+1); err != nil {
				return err
			}
		}
		t, err = d.Token()
		if err != nil || t != json.Delim('}') {
			return ErrProtocol
		}
	case '[':
		for d.More() {
			if err = value(d, depth+1); err != nil {
				return err
			}
		}
		t, err = d.Token()
		if err != nil || t != json.Delim(']') {
			return ErrProtocol
		}
	default:
		return ErrProtocol
	}
	return nil
}
func object(data []byte, allowed ...string) (map[string]json.RawMessage, error) {
	var f map[string]json.RawMessage
	if json.Unmarshal(data, &f) != nil || f == nil {
		return nil, ErrProtocol
	}
	for key := range f {
		ok := false
		for _, a := range allowed {
			if key == a {
				ok = true
				break
			}
		}
		if !ok {
			return nil, ErrProtocol
		}
	}
	return f, nil
}
func field(f map[string]json.RawMessage, key string, dst any) error {
	data, ok := f[key]
	if !ok || bytes.Equal(data, []byte("null")) || json.Unmarshal(data, dst) != nil {
		return ErrProtocol
	}
	return nil
}
func emptyResult(data []byte) error {
	f, err := object(data)
	if err != nil || len(f) != 0 {
		return ErrProtocol
	}
	return nil
}
func parseGreeting(data []byte) error {
	f, err := object(data, "QMP")
	if err != nil {
		return err
	}
	q, err := object(f["QMP"], "version", "capabilities")
	if err != nil {
		return err
	}
	var capabilities []string
	if field(q, "capabilities", &capabilities) != nil {
		return ErrProtocol
	}
	v, err := object(q["version"], "qemu", "package")
	if err != nil {
		return err
	}
	var pkg string
	if field(v, "package", &pkg) != nil {
		return ErrProtocol
	}
	version, err := object(v["qemu"], "major", "minor", "micro")
	if err != nil {
		return err
	}
	for _, key := range []string{"major", "minor", "micro"} {
		var n int64
		if field(version, key, &n) != nil || n < 0 {
			return ErrProtocol
		}
	}
	return nil
}
func parseStatus(data []byte) (Status, error) {
	f, err := object(data, "running", "singlestep", "status")
	if err != nil {
		return Status{}, err
	}
	var status Status
	if field(f, "running", &status.Running) != nil || field(f, "status", &status.State) != nil {
		return Status{}, ErrProtocol
	}
	if _, ok := f["singlestep"]; ok {
		var single bool
		if field(f, "singlestep", &single) != nil {
			return Status{}, ErrProtocol
		}
	}
	switch status.State {
	case "debug", "finish-migrate", "inmigrate", "internal-error", "io-error", "paused", "postmigrate", "prelaunch", "restore-vm", "running", "save-vm", "shutdown", "suspended", "watchdog", "guest-panicked", "colo":
	default:
		return Status{}, ErrProtocol
	}
	if status.Running != (status.State == "running") {
		return Status{}, ErrProtocol
	}
	return status, nil
}
func parseEvent(f map[string]json.RawMessage) error {
	for k := range f {
		if k != "event" && k != "data" && k != "timestamp" {
			return ErrProtocol
		}
	}
	var name string
	if field(f, "event", &name) != nil || name == "" {
		return ErrProtocol
	}
	if data, ok := f["data"]; ok {
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil || object == nil {
			return ErrProtocol
		}
	}
	if data, ok := f["timestamp"]; ok {
		ts, err := object(data, "seconds", "microseconds")
		if err != nil {
			return err
		}
		var sec, micro int64
		if field(ts, "seconds", &sec) != nil || field(ts, "microseconds", &micro) != nil || sec < 0 || micro < 0 || micro >= 1000000 {
			return ErrProtocol
		}
	}
	return nil
}
