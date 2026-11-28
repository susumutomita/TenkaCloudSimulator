package catalog

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/jsonwire"
)

// Reject ambiguous envelope fields before the generic REST decoder normalizes them.
func sesMessageWire(data []byte) error {
	if !utf8.Valid(data) || !jsonwire.ValidUnicodeEscapes(data) {
		return invalid("Use valid Unicode text in a local message.")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	nodes := 0
	var walk func(int) error
	walk = func(depth int) error {
		nodes++
		if nodes > maxNodes || depth > maxDepth {
			return invalid("Local message JSON exceeds the input limit.")
		}
		token, err := d.Token()
		if err != nil || token == nil {
			return invalid("Use one JSON message with non-null fields.")
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			keys := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				name, ok := key.(string)
				if e != nil || !ok || keys[name] {
					return invalid("Local message object keys must be unique.")
				}
				keys[name] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return invalid("Invalid local message JSON structure.")
		}
		if _, err = d.Token(); err != nil {
			return invalid("Invalid local message JSON structure.")
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid("Use one JSON object per message.")
	}
	return nil
}
