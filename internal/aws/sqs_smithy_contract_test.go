package aws

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
)

// Test-only shape oracle. This does not generate service behavior, IAM checks,
// storage semantics, wire encoders or assertions of full AWS compatibility.
type smithyMember struct {
	Target string                     `json:"target"`
	Traits map[string]json.RawMessage `json:"traits"`
}
type smithyShape struct {
	Type    string                  `json:"type"`
	Members map[string]smithyMember `json:"members"`
	Member  smithyMember            `json:"member"`
	Key     smithyMember            `json:"key"`
	Value   smithyMember            `json:"value"`
}
type sqsSmithyContracts struct {
	SourceSHA256 string                       `json:"sourceSHA256"`
	Operations   map[string]map[string]string `json:"operations"`
	Shapes       map[string]smithyShape       `json:"shapes"`
}

func loadSQSContracts(t *testing.T) sqsSmithyContracts {
	t.Helper()
	raw, err := os.ReadFile("testdata/smithy/sqs-contracts.json")
	if err != nil {
		t.Fatal(err)
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	var c sqsSmithyContracts
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("testdata/smithy/sqs.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(source)) != c.SourceSHA256 {
		t.Fatal("Smithy source/contract checksum mismatch")
	}
	// Independently compare every consumed shape and operation against the source.
	// Generated fixture edits cannot silently weaken the Go test's oracle.
	var sourceModel map[string]any
	if err := json.Unmarshal(source, &sourceModel); err != nil {
		t.Fatal(err)
	}
	var stripDocumentation func(any)
	stripDocumentation = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			delete(v, "smithy.api#documentation")
			for _, child := range v {
				stripDocumentation(child)
			}
		case []any:
			for _, child := range v {
				stripDocumentation(child)
			}
		}
	}
	stripDocumentation(sourceModel)
	rawShapes := sourceModel["shapes"].(map[string]any)
	for name, shape := range c.Shapes {
		expected := rawShapes[name]
		if name == "smithy.api#Unit" {
			expected = map[string]any{"type": "structure", "members": map[string]any{}}
		}
		encoded, err := json.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		var upstream smithyShape
		if err := json.Unmarshal(encoded, &upstream); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(shape, upstream) {
			t.Fatalf("generated shape differs from source: %s", name)
		}
	}
	for op, contract := range c.Operations {
		upstream, ok := rawShapes["com.amazonaws.sqs#"+op].(map[string]any)
		if !ok {
			t.Fatalf("missing upstream operation %s", op)
		}
		for _, direction := range []string{"input", "output"} {
			if upstream[direction].(map[string]any)["target"] != contract[direction] {
				t.Fatalf("operation mapping differs: %s %s", op, direction)
			}
		}
	}

	return c
}
func (c sqsSmithyContracts) validate(id string, value any) error {
	s, ok := c.Shapes[id]
	if !ok {
		return fmt.Errorf("missing shape %s", id)
	}
	bad := func() error { return fmt.Errorf("%s: expected %s, got %T", id, s.Type, value) }
	switch s.Type {
	case "structure":
		object, ok := value.(map[string]any)
		if !ok {
			return bad()
		}
		for key, m := range s.Members {
			if _, required := m.Traits["smithy.api#required"]; required {
				if v, found := object[key]; !found || v == nil {
					return fmt.Errorf("%s: required %s missing", id, key)
				}
			}
		}
		for key, v := range object {
			m, found := s.Members[key]
			if !found {
				return fmt.Errorf("%s: unknown member %s", id, key)
			}
			if err := c.validate(m.Target, v); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	case "string", "enum", "blob":
		str, ok := value.(string)
		if !ok {
			return bad()
		}
		if s.Type == "blob" {
			if _, err := base64.StdEncoding.DecodeString(str); err != nil {
				return err
			}
		}
		// Smithy string enums are open for forward compatibility. Values are not
		// evidence that the simulator implements every enumerated feature.
	case "integer":
		n, ok := value.(float64)
		if !ok || math.Trunc(n) != n || n < -2147483648 || n > 2147483647 {
			return bad()
		}
	case "list":
		values, ok := value.([]any)
		if !ok {
			return bad()
		}
		for _, v := range values {
			if err := c.validate(s.Member.Target, v); err != nil {
				return err
			}
		}
	case "map":
		values, ok := value.(map[string]any)
		if !ok {
			return bad()
		}
		for k, v := range values {
			if err := c.validate(s.Key.Target, k); err != nil {
				return err
			}
			if err := c.validate(s.Value.Target, v); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported Smithy shape type %s", s.Type)
	}
	return nil
}
func TestSQSSmithyLifecycleContract(t *testing.T) {
	c := loadSQSContracts(t)
	g, _, _ := sqsAdditionalFixture(t)
	invoke := func(op string, input map[string]any) map[string]any {
		t.Helper()
		// Round-trip produces the same JSON scalar types as the real request wire.
		raw, _ := json.Marshal(input)
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		contract, found := c.Operations[op]
		if !found {
			t.Fatalf("no contract for %s", op)
		}
		if err := c.validate(contract["input"], decoded); err != nil {
			t.Fatal(err)
		}
		out := sqsWire(t, g, op, input, 200)
		if err := c.validate(contract["output"], out); err != nil {
			t.Fatalf("%s response: %v", op, err)
		}
		return out
	}
	queue := invoke("CreateQueue", map[string]any{"QueueName": "smithy-contract"})["QueueUrl"]
	if queue == nil || queue == "" {
		t.Fatal("missing queue URL")
	}
	sent := invoke("SendMessage", map[string]any{"QueueUrl": queue, "MessageBody": "contract body"})
	if sent["MessageId"] == nil || sent["MD5OfMessageBody"] == nil {
		t.Fatal("message acknowledgement missing")
	}
	received := invoke("ReceiveMessage", map[string]any{"QueueUrl": queue, "MaxNumberOfMessages": 1, "VisibilityTimeout": 0})
	messages, ok := received["Messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatal("expected persisted message", received)
	}
	message := messages[0].(map[string]any)
	if message["Body"] != "contract body" || message["MessageId"] != sent["MessageId"] {
		t.Fatal("message changed", message)
	}
	invoke("DeleteMessage", map[string]any{"QueueUrl": queue, "ReceiptHandle": message["ReceiptHandle"]})
	empty := invoke("ReceiveMessage", map[string]any{"QueueUrl": queue})
	if values, ok := empty["Messages"].([]any); ok && len(values) != 0 {
		t.Fatal("deleted message still present")
	}
	invoke("DeleteQueue", map[string]any{"QueueUrl": queue})
	sqsWire(t, g, "GetQueueUrl", map[string]any{"QueueName": "smithy-contract"}, 400)
}
func TestSQSSmithyRequiredInputs(t *testing.T) {
	c := loadSQSContracts(t)
	for op, contract := range c.Operations {
		for field, member := range c.Shapes[contract["input"]].Members {
			if _, required := member.Traits["smithy.api#required"]; !required {
				continue
			}
			t.Run(op+"/"+field, func(t *testing.T) {
				g, _, q := sqsAdditionalFixture(t)
				input := map[string]any{}
				for key, m := range c.Shapes[contract["input"]].Members {
					if _, req := m.Traits["smithy.api#required"]; req {
						input[key] = "placeholder"
					}
				}
				input["QueueUrl"] = q.Name
				if _, exists := c.Shapes[contract["input"]].Members["QueueUrl"]; !exists {
					delete(input, "QueueUrl")
				}
				delete(input, field)
				if c.validate(contract["input"], input) == nil {
					t.Fatal("oracle accepted missing required field")
				}
				// Actual gateway must reject missing required fields, not return success.
				sqsWire(t, g, op, input, 400)
			})
		}
	}
}
func TestSQSSmithyOracleRejectsInvalidShape(t *testing.T) {
	c := loadSQSContracts(t)
	id := c.Operations["ReceiveMessage"]["output"]
	for _, value := range []any{map[string]any{"Unexpected": true}, map[string]any{"Messages": "bad"}, map[string]any{"Messages": []any{map[string]any{"Body": 42}}}} {
		if c.validate(id, value) == nil {
			t.Fatal("accepted malformed response", value)
		}
	}
	if c.validate("unknown", map[string]any{}) == nil {
		t.Fatal("accepted unknown shape")
	}
}
