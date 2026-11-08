package aws

// This file owns pure, bounded value intrinsics. It does not create resources,
// inspect real AWS topology, grant IAM permissions, or execute UserData.
import (
	"fmt"
	"strconv"
	"strings"
)

const stackIntrinsicStringBytes = 51200

// localAvailabilityZones is the provider's deliberately synthetic availability
// contract: one local zone per supported region. No real account, default subnet
// or EC2 availability is inferred. A future local network inventory can replace
// this provider-owned source without changing intrinsic syntax.
func localAvailabilityZones(region string) ([]any, error) {
	for _, supported := range Regions {
		if region == supported {
			return []any{region + "a"}, nil
		}
	}
	return nil, fmt.Errorf("GetAZs requires a supported local region")
}

func stackSelectIndex(value any) (int, error) {
	text, ok := stackScalarText(value)
	if !ok || text == "" || len(text) > 10 || len(text) > 1 && text[0] == '0' {
		return 0, fmt.Errorf("Select index requires a nonnegative decimal integer")
	}
	for _, c := range text {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("Select index requires a nonnegative decimal integer")
		}
	}
	// The node budget bounds every literal/computed list. This also avoids an
	// architecture-dependent int overflow before indexing on 32-bit platforms.
	index, err := strconv.ParseUint(text, 10, 32)
	if err != nil || index >= stackConditionalNodes {
		return 0, fmt.Errorf("Select index exceeds local list bound")
	}
	return int(index), nil
}

// Validate immediate carrier shapes; the caller walks every nested operand with
// its existing depth/node budget and validates symbols even in inactive If arms.
func stackValueIntrinsicShape(key string, value any) error {
	call := func(v any, allowed ...string) bool {
		m, ok := v.(map[string]any)
		if !ok || len(m) != 1 {
			return false
		}
		for _, name := range allowed {
			if _, ok := m[name]; ok {
				return true
			}
		}
		return false
	}
	switch key {
	case "Fn::GetAZs":
		if _, ok := value.(string); !ok && !call(value, "Ref") {
			return fmt.Errorf("GetAZs requires a region string or Ref")
		}
	case "Fn::Base64":
		if _, ok := value.(string); !ok && !call(value, "Ref", "Fn::GetAtt", "Fn::Sub", "Fn::Base64", "Fn::Select", "Fn::If") {
			return fmt.Errorf("Base64 requires a string or supported string intrinsic")
		}
	case "Fn::Select":
		args, ok := value.([]any)
		if !ok || len(args) != 2 {
			return fmt.Errorf("Select requires exactly two arguments")
		}
		if !call(args[0], "Ref") {
			if _, err := stackSelectIndex(args[0]); err != nil {
				return err
			}
		}
		if list, ok := args[1].([]any); ok {
			if len(list) == 0 || len(list) > stackConditionalNodes {
				return fmt.Errorf("Select requires a nonempty bounded list")
			}
			for _, item := range list {
				if item == nil {
					return fmt.Errorf("Select list cannot contain null")
				}
			}
		} else if !call(args[1], "Ref", "Fn::GetAtt", "Fn::GetAZs", "Fn::If") {
			return fmt.Errorf("Select requires a list or supported list intrinsic")
		}
	}
	return nil
}

type stackSubPart struct {
	text, name string
	escaped    bool
}

// Tokenize once using the same grammar in resolution, linking and dependency
// ordering. Escapes are literal output, never new variables or graph edges.
func stackSubParts(source string) ([]stackSubPart, error) {
	if len(source) > stackIntrinsicStringBytes {
		return nil, fmt.Errorf("Sub exceeds local byte bound")
	}
	parts := []stackSubPart{}
	for {
		start := strings.Index(source, "${")
		if start < 0 {
			return append(parts, stackSubPart{text: source}), nil
		}
		parts = append(parts, stackSubPart{text: source[:start]})
		source = source[start+2:]
		end := strings.IndexByte(source, '}')
		if end < 0 {
			return nil, fmt.Errorf("unterminated substitution")
		}
		name := source[:end]
		escaped := strings.HasPrefix(name, "!")
		if escaped {
			name = name[1:]
		}
		if name == "" || strings.ContainsAny(name, "{}\r\n") {
			return nil, fmt.Errorf("invalid substitution")
		}
		if !escaped {
			for _, c := range name {
				if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == ':') {
					return nil, fmt.Errorf("invalid substitution name")
				}
			}
		}
		parts = append(parts, stackSubPart{name: name, escaped: escaped})
		source = source[end+1:]
	}
}

func stackResolveSub(source string, refs, attributes map[string]string) (string, error) {
	parts, err := stackSubParts(source)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, part := range parts {
		value := part.text
		if part.escaped {
			value = "${" + part.name + "}"
		} else if part.name != "" {
			var ok bool
			value, ok = refs[part.name]
			if !ok {
				value, ok = attributes[part.name]
			}
			if !ok {
				return "", fmt.Errorf("unknown substitution %s", part.name)
			}
		}
		if len(value) > stackIntrinsicStringBytes-out.Len() {
			return "", fmt.Errorf("Sub result exceeds local byte bound")
		}
		out.WriteString(value)
	}
	return out.String(), nil
}
