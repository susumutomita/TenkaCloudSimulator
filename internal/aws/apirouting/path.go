package apirouting

import "strings"

// These utilities implement the simulator's bounded path language. API Gateway's
// public proxy contract distinguishes literal segments, {name}, and a terminal
// {name+}; request path bytes are data, not filesystem paths.
// Reference: https://docs.aws.amazon.com/apigateway/latest/developerguide/set-up-lambda-proxy-integrations.html

// SplitPath ignores outer separators only. In particular, it neither cleans dot
// segments nor decodes escapes, and repeated interior separators remain visible.
func SplitPath(path string) []string {
	begin, end := 0, len(path)
	for begin < end && path[begin] == '/' {
		begin++
	}
	for end > begin && path[end-1] == '/' {
		end--
	}
	if begin == end {
		return nil
	}
	var parts []string
	for cursor := begin; cursor <= end; cursor++ {
		if cursor == end || path[cursor] == '/' {
			parts = append(parts, path[begin:cursor])
			begin = cursor + 1
		}
	}
	return parts
}

type routeLiteral struct {
	position int
	text     string
}
type routeCapture struct {
	position int
	name     string
}
type routeProgram struct {
	width    int
	literals []routeLiteral
	captures []routeCapture
	rest     *routeCapture
}

// Compile first, then check cardinality and all literals before extracting any
// captures. A failed candidate therefore never publishes partial parameters.
func compileRoute(template string) (routeProgram, bool) {
	parts := SplitPath(template)
	program := routeProgram{width: len(parts)}
	for position, text := range parts {
		if len(text) < 2 || text[0] != '{' || text[len(text)-1] != '}' {
			program.literals = append(program.literals, routeLiteral{position, text})
			continue
		}
		name := text[1 : len(text)-1]
		greedy := len(name) > 0 && name[len(name)-1] == '+'
		if greedy {
			name = name[:len(name)-1]
		}
		if name == "" || greedy && position != len(parts)-1 {
			return routeProgram{}, false
		}
		capture := routeCapture{position, name}
		if greedy {
			program.rest = &capture
			program.width--
		} else {
			program.captures = append(program.captures, capture)
		}
	}
	return program, true
}

// MatchPath returns captures and the literal count used by the provider's route
// precedence rules. Method/ANY selection remains the provider's responsibility.
func MatchPath(template string, request []string) (map[string]string, int, bool) {
	program, valid := compileRoute(template)
	if !valid || program.rest == nil && len(request) != program.width || program.rest != nil && len(request) <= program.width {
		return nil, -1, false
	}
	for _, constraint := range program.literals {
		if request[constraint.position] != constraint.text {
			return nil, -1, false
		}
	}
	values := make(map[string]string, len(program.captures)+1)
	for _, slot := range program.captures {
		values[slot.name] = request[slot.position]
	}
	if slot := program.rest; slot != nil {
		values[slot.name] = strings.Join(request[slot.position:], "/")
	}
	return values, len(program.literals), true
}
