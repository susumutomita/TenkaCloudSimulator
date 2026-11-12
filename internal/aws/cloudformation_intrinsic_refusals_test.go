package aws

import (
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestStackIntrinsicResolutionRefusesUnsupportedShapes(t *testing.T) {
	refs := map[string]string{"AWS::Region": "us-east-1", "Index": "1"}
	attributes := map[string]string{"Bucket.Arn": "arn:aws:s3:::bucket"}
	resolve := func(v any) (any, error) {
		nodes := 0
		return stackResolveValue(v, refs, attributes, 0, &nodes)
	}
	for name, c := range map[string]struct {
		in   any
		want any
	}{
		"GetAtt":       {map[string]any{"Fn::GetAtt": "Bucket.Arn"}, "arn:aws:s3:::bucket"},
		"GetAtt list":  {map[string]any{"Fn::GetAtt": []any{"Bucket", "Arn"}}, "arn:aws:s3:::bucket"},
		"Select ref":   {map[string]any{"Fn::Select": []any{map[string]any{"Ref": "Index"}, []any{"a", "b"}}}, "b"},
		"plain object": {map[string]any{"Key": map[string]any{"Ref": "AWS::Region"}}, map[string]any{"Key": "us-east-1"}},
	} {
		got, err := resolve(c.in)
		if err != nil || ssmEncode(got) != ssmEncode(c.want) {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	for name, in := range map[string]any{
		"unknown GetAtt":    map[string]any{"Fn::GetAtt": "Missing.Arn"},
		"GetAtt extra key":  map[string]any{"Fn::GetAtt": "Bucket.Arn", "Extra": "x"},
		"list Sub":          map[string]any{"Fn::Sub": []any{"${A}", map[string]any{"A": "x"}}},
		"Sub extra key":     map[string]any{"Fn::Sub": "x", "Extra": "y"},
		"Select unknown":    map[string]any{"Fn::Select": []any{map[string]any{"Ref": "Missing"}, []any{"a"}}},
		"Select null item":  map[string]any{"Fn::Select": []any{"0", []any{"a", nil}}},
		"GetAZs non-string": map[string]any{"Fn::GetAZs": map[string]any{"Fn::Select": []any{"0", []any{[]any{"x"}}}}},
		"unsupported Join":  map[string]any{"Fn::Join": []any{",", []any{"a"}}},
		"nested unknown":    map[string]any{"Key": map[string]any{"Fn::FindInMap": []any{"M", "a", "b"}}},
	} {
		if got, err := resolve(in); err == nil {
			t.Errorf("%s resolved to %v", name, got)
		}
	}
}

func TestStackTaggedYAMLRefusesMalformedShorthand(t *testing.T) {
	if _, err := taggedYAML(&yaml.Node{Kind: yaml.DocumentNode}); err == nil {
		t.Fatal("empty document accepted")
	}
	for name, source := range map[string]string{
		"Select child": "!Select [!Bogus x, [a]]",
		"Base64 child": "!Base64 {Key: !Bogus x}",
		"list child":   "[a, !Ref [b]]",
		"map child":    "{Key: !Unknown x}",
	} {
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(source), &n); err != nil {
			t.Fatal(name, err)
		}
		if got, err := taggedYAML(&n); err == nil {
			t.Errorf("%s accepted: %v", name, got)
		}
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("!Base64 {Ref: Name}"), &n); err != nil {
		t.Fatal(err)
	}
	got, err := taggedYAML(&n)
	if err != nil || ssmEncode(got) != `{"Fn::Base64":{"Ref":"Name"}}` {
		t.Fatal(got, err)
	}
}
