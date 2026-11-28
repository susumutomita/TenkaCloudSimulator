package jsonwire

import "strconv"

// ValidUnicodeEscapes rejects unpaired UTF-16 escapes before encoding/json can replace them. Valid
// escaped pairs and literal UTF-8 must retain their exact decoded value bytes.
func ValidUnicodeEscapes(data []byte) bool {
	inside := false
	for i := 0; i < len(data); i++ {
		if !inside {
			if data[i] == '"' {
				inside = true
			}
			continue
		}
		if data[i] == '"' {
			inside = false
			continue
		}
		if data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		value, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+10 >= len(data) || data[i+5] != '\\' || data[i+6] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+7:i+11]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 10
		} else {
			if value >= 0xdc00 && value <= 0xdfff {
				return false
			}
			i += 4
		}
	}
	return !inside
}
