package aws

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/inference"
)

// Bedrock Runtime request decoding is strict: every member is either applied
// to the local backend or refused with its name. Nothing is silently dropped.

func writeBedrockJSON(w http.ResponseWriter, status int, value any) {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(body.Len()))
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

// bedrockObject decodes exactly one JSON object whose members are all named
// in allowed. Null members and trailing data are refused.
func bedrockObject(data []byte, where string, allowed ...string) (map[string]json.RawMessage, *bedrockError) {
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&fields) != nil || fields == nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		return nil, bedrockInvalid("%s must be one JSON object.", where)
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		known := false
		for _, candidate := range allowed {
			known = known || name == candidate
		}
		if !known {
			return nil, bedrockInvalid("%s member %q is not supported by the simulator's local text-only adapter.", where, name)
		}
		if bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			return nil, bedrockInvalid("%s member %q cannot be null.", where, name)
		}
	}
	return fields, nil
}

func bedrockString(raw json.RawMessage, where string) (string, *bedrockError) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", bedrockInvalid("%s must be a string.", where)
	}
	return value, nil
}

func bedrockText(raw json.RawMessage, where string) (string, *bedrockError) {
	value, failure := bedrockString(raw, where)
	if failure != nil {
		return "", failure
	}
	if strings.TrimSpace(value) == "" {
		return "", bedrockInvalid("%s must contain non-whitespace text.", where)
	}
	return value, nil
}

func bedrockArray(raw json.RawMessage, where string, minimum, maximum int) ([]json.RawMessage, *bedrockError) {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || items == nil {
		return nil, bedrockInvalid("%s must be an array.", where)
	}
	if len(items) < minimum || len(items) > maximum {
		return nil, bedrockInvalid("%s must contain %d–%d items.", where, minimum, maximum)
	}
	return items, nil
}

func bedrockInteger(raw json.RawMessage, where string, minimum, maximum int) (int, *bedrockError) {
	value, err := strconv.Atoi(string(bytes.TrimSpace(raw)))
	if err != nil || value < minimum || value > maximum {
		return 0, bedrockInvalid("%s must be an integer from %d to %d.", where, minimum, maximum)
	}
	return value, nil
}

func bedrockUnit(raw json.RawMessage, where string) (*float64, *bedrockError) {
	var value float64
	if json.Unmarshal(raw, &value) != nil || value < 0 || value > 1 {
		return nil, bedrockInvalid("%s must be a number from 0 to 1.", where)
	}
	return &value, nil
}

func bedrockRole(raw json.RawMessage, where string) (inference.Role, *bedrockError) {
	role, failure := bedrockString(raw, where)
	if failure != nil {
		return "", failure
	}
	if role != string(inference.RoleUser) && role != string(inference.RoleAssistant) {
		return "", bedrockInvalid("%s must be user or assistant.", where)
	}
	return inference.Role(role), nil
}

// bedrockTurns enforces the shared conversation shape: a user turn first, a
// user turn last (assistant prefill is not supported) and, when alternate is
// set, strictly alternating roles as Converse requires.
func bedrockTurns(messages []inference.Message, alternate bool) *bedrockError {
	if messages[0].Role != inference.RoleUser {
		return bedrockInvalid("The first message must use the user role.")
	}
	if messages[len(messages)-1].Role != inference.RoleUser {
		return bedrockInvalid("The last message must use the user role; assistant prefill is not supported by the local adapter.")
	}
	for index := 1; alternate && index < len(messages); index++ {
		if messages[index].Role == messages[index-1].Role {
			return bedrockInvalid("A conversation must alternate between user and assistant roles.")
		}
	}
	return nil
}

func bedrockValidBody(data []byte) *bedrockError {
	if !utf8.Valid(data) {
		return bedrockInvalid("The request body must be valid UTF-8 JSON.")
	}
	return nil
}

// anthropicText decodes Anthropic Messages content: a string or an array of
// {"type":"text","text":...} blocks. Multiple blocks are joined by newlines.
func anthropicText(raw json.RawMessage, where string) (string, *bedrockError) {
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte(`"`)) {
		return bedrockText(raw, where)
	}
	blocks, failure := bedrockArray(raw, where, 1, bedrockMaxBlocks)
	if failure != nil {
		return "", failure
	}
	texts := make([]string, 0, len(blocks))
	for index, block := range blocks {
		at := where + "[" + strconv.Itoa(index) + "]"
		fields, failure := bedrockObject(block, at, "type", "text")
		if failure != nil {
			return "", failure
		}
		kind, failure := bedrockString(fields["type"], at+".type")
		if failure != nil {
			return "", failure
		}
		if kind != "text" {
			return "", bedrockInvalid("%s content type %q is not supported; only text content is available.", at, kind)
		}
		if fields["text"] == nil {
			return "", bedrockInvalid("%s.text is required.", at)
		}
		text, failure := bedrockText(fields["text"], at+".text")
		if failure != nil {
			return "", failure
		}
		texts = append(texts, text)
	}
	return strings.Join(texts, "\n"), nil
}

// parseAnthropicInvoke decodes an InvokeModel body in the Anthropic Claude
// Messages shape used by Bedrock.
func parseAnthropicInvoke(data []byte) (inference.Request, *bedrockError) {
	var request inference.Request
	if failure := bedrockValidBody(data); failure != nil {
		return request, failure
	}
	fields, failure := bedrockObject(data, "The Anthropic Messages body", "anthropic_version", "max_tokens", "messages", "system", "temperature", "top_p", "top_k")
	if failure != nil {
		return request, failure
	}
	for _, required := range []string{"anthropic_version", "max_tokens", "messages"} {
		if fields[required] == nil {
			return request, bedrockInvalid("%s is required.", required)
		}
	}
	version, failure := bedrockString(fields["anthropic_version"], "anthropic_version")
	if failure != nil {
		return request, failure
	}
	if version != bedrockAnthropicVersion {
		return request, bedrockInvalid("anthropic_version must be %s.", bedrockAnthropicVersion)
	}
	if request.MaxTokens, failure = bedrockInteger(fields["max_tokens"], "max_tokens", 1, BedrockMaxOutputTokens); failure != nil {
		return request, failure
	}
	items, failure := bedrockArray(fields["messages"], "messages", 1, bedrockMaxMessages)
	if failure != nil {
		return request, failure
	}
	for index, item := range items {
		at := "messages[" + strconv.Itoa(index) + "]"
		message, failure := bedrockObject(item, at, "role", "content")
		if failure != nil {
			return request, failure
		}
		if message["role"] == nil || message["content"] == nil {
			return request, bedrockInvalid("%s requires role and content.", at)
		}
		role, failure := bedrockRole(message["role"], at+".role")
		if failure != nil {
			return request, failure
		}
		text, failure := anthropicText(message["content"], at+".content")
		if failure != nil {
			return request, failure
		}
		request.Messages = append(request.Messages, inference.Message{Role: role, Text: text})
	}
	if failure := bedrockTurns(request.Messages, false); failure != nil {
		return request, failure
	}
	if fields["system"] != nil {
		if request.System, failure = anthropicText(fields["system"], "system"); failure != nil {
			return request, failure
		}
	}
	if fields["temperature"] != nil {
		if request.Temperature, failure = bedrockUnit(fields["temperature"], "temperature"); failure != nil {
			return request, failure
		}
	}
	if fields["top_p"] != nil {
		if request.TopP, failure = bedrockUnit(fields["top_p"], "top_p"); failure != nil {
			return request, failure
		}
	}
	if fields["top_k"] != nil {
		topK, failure := bedrockInteger(fields["top_k"], "top_k", 1, bedrockMaxTopK)
		if failure != nil {
			return request, failure
		}
		request.TopK = &topK
	}
	return request, nil
}

// converseText decodes one Converse ContentBlock or SystemContentBlock union,
// which must set exactly one member; only text is supported.
func converseText(raw json.RawMessage, where string) (string, *bedrockError) {
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil || members == nil {
		return "", bedrockInvalid("%s must be an object.", where)
	}
	if len(members) != 1 {
		return "", bedrockInvalid("%s must set exactly one member.", where)
	}
	var name string
	var value json.RawMessage
	for name, value = range members {
	}
	if name != "text" {
		return "", bedrockInvalid("%s member %q is not supported; only text content is available.", where, name)
	}
	return bedrockText(value, where+".text")
}

func converseBlocks(raw json.RawMessage, where string, minimum int) (string, *bedrockError) {
	blocks, failure := bedrockArray(raw, where, minimum, bedrockMaxBlocks)
	if failure != nil {
		return "", failure
	}
	texts := make([]string, 0, len(blocks))
	for index, block := range blocks {
		text, failure := converseText(block, where+"["+strconv.Itoa(index)+"]")
		if failure != nil {
			return "", failure
		}
		texts = append(texts, text)
	}
	return strings.Join(texts, "\n"), nil
}

// parseConverse decodes a Converse request body. modelId travels in the URI.
func parseConverse(data []byte) (inference.Request, *bedrockError) {
	request := inference.Request{MaxTokens: BedrockDefaultMaxTokens}
	if failure := bedrockValidBody(data); failure != nil {
		return request, failure
	}
	fields, failure := bedrockObject(data, "The Converse request", "messages", "system", "inferenceConfig")
	if failure != nil {
		return request, failure
	}
	if fields["messages"] == nil {
		return request, bedrockInvalid("messages is required.")
	}
	items, failure := bedrockArray(fields["messages"], "messages", 1, bedrockMaxMessages)
	if failure != nil {
		return request, failure
	}
	for index, item := range items {
		at := "messages[" + strconv.Itoa(index) + "]"
		message, failure := bedrockObject(item, at, "role", "content")
		if failure != nil {
			return request, failure
		}
		if message["role"] == nil || message["content"] == nil {
			return request, bedrockInvalid("%s requires role and content.", at)
		}
		role, failure := bedrockRole(message["role"], at+".role")
		if failure != nil {
			return request, failure
		}
		text, failure := converseBlocks(message["content"], at+".content", 1)
		if failure != nil {
			return request, failure
		}
		request.Messages = append(request.Messages, inference.Message{Role: role, Text: text})
	}
	if failure := bedrockTurns(request.Messages, true); failure != nil {
		return request, failure
	}
	if fields["system"] != nil {
		if request.System, failure = converseBlocks(fields["system"], "system", 0); failure != nil {
			return request, failure
		}
	}
	if fields["inferenceConfig"] != nil {
		config, failure := bedrockObject(fields["inferenceConfig"], "inferenceConfig", "maxTokens", "temperature", "topP")
		if failure != nil {
			return request, failure
		}
		if config["maxTokens"] != nil {
			if request.MaxTokens, failure = bedrockInteger(config["maxTokens"], "inferenceConfig.maxTokens", 1, BedrockMaxOutputTokens); failure != nil {
				return request, failure
			}
		}
		if config["temperature"] != nil {
			if request.Temperature, failure = bedrockUnit(config["temperature"], "inferenceConfig.temperature"); failure != nil {
				return request, failure
			}
		}
		if config["topP"] != nil {
			if request.TopP, failure = bedrockUnit(config["topP"], "inferenceConfig.topP"); failure != nil {
				return request, failure
			}
		}
	}
	return request, nil
}
