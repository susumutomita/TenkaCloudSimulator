package aws

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func intrinsicYAML(t *testing.T, source string) any {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(source), &node); err != nil {
		t.Fatal(err)
	}
	value, err := taggedYAML(&node)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func battleIntrinsicTemplate(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("../../test/fixtures/hello-world-battle-c1aaaa9/template.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != "4de530625bd2a3c0b5a5881f511061709c94c9eb6884d8d6fd1037f3d9bc6bc5" {
		t.Fatal("original fixture changed")
	}
	return string(body)
}

func TestStackIntrinsicOriginalBattleSupportedStage(t *testing.T) {
	body := battleIntrinsicTemplate(t)
	template := object(intrinsicYAML(t, body))
	resources := object(template["Resources"])
	props := func(name string) map[string]any { return object(object(resources[name])["Properties"]) }
	refs := map[string]string{"AWS::Region": "ap-northeast-1", "AWS::AccountId": "000000000000", "NamePrefix": "tc-hello-team"}
	az, err := resolve(props("PublicSubnet")["AvailabilityZone"], refs, nil)
	if err != nil || az != "ap-northeast-1a" {
		t.Fatalf("AZ: %v %v", az, err)
	}
	userData := props("Ec2")["UserData"]
	selected, omit, err := stackSelectConditional(userData, nil)
	if err != nil || omit {
		t.Fatalf("UserData shape: %v", err)
	}
	if err = stackConditionalLinks(selected, resources, refs, nil, false); err != nil {
		t.Fatal(err)
	}
	encoded, err := resolve(selected, refs, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(stringValue(encoded))
	original := stringValue(object(object(userData)["Fn::Base64"])["Fn::Sub"])
	if err != nil || string(decoded) != strings.ReplaceAll(original, "${NamePrefix}", refs["NamePrefix"]) {
		t.Fatal("UserData bytes/newlines changed")
	}
	policies := props("ParticipantViewerRole")["Policies"].([]any)
	statements := object(object(policies[0])["PolicyDocument"])["Statement"].([]any)
	found := false
	for _, statement := range statements {
		s := object(statement)
		if s["Sid"] != "OpenOwnSessionDataChannel" {
			continue
		}
		found = true
		selected, _, err := stackSelectConditional(s["Resource"], nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = stackConditionalLinks(selected, resources, refs, nil, false); err != nil {
			t.Fatal(err)
		}
		value, err := resolve(selected, refs, nil)
		if err != nil || value != "arn:aws:ssm:ap-northeast-1:000000000000:session/${aws:userid}-*" {
			t.Fatalf("literal IAM variable: %v %v", value, err)
		}
	}
	if !found {
		t.Fatal("original escaped ARN not found")
	}
	// Network/instance types now have a bounded compiler, but parsing a script
	// still cannot run it or supply missing template inputs. Full-template
	// acceptance is separately exercised without skipping its remaining blockers.
	if _, err = compileStack("ap-northeast-1", "battle", body, nil, "CAPABILITY_IAM"); err == nil || !strings.Contains(err.Error(), "missing required value") {
		t.Fatalf("required input admission changed: %v", err)
	}
	unsupported := `{"Resources":{"Future":{"Type":"AWS::EC2::NatGateway","Properties":{}}}}`
	if _, err = compileStack("ap-northeast-1", "unsupported", unsupported, nil, ""); err == nil || !strings.Contains(err.Error(), "Unsupported resource type:") {
		t.Fatalf("unsupported resource admission changed: %v", err)
	}
}

func TestStackIntrinsicYAMLAndJSONForms(t *testing.T) {
	refs := map[string]string{"AWS::Region": "us-east-1", "Region": "eu-west-1", "Index": "1", "Value": "雪\nline\n"}
	for _, tc := range []struct {
		source string
		want   any
	}{
		{`!Select [0, !GetAZs ""]`, "us-east-1a"},
		{`Fn::Select: [0, {Fn::GetAZs: {Ref: 'AWS::Region'}}]`, "us-east-1a"},
		{`!GetAZs {Ref: Region}`, []any{"eu-west-1a"}},
		{`{"Fn::Select":[1,["a","b"]]}`, "b"},
		{`!Select [!Ref Index, [a,b]]`, "b"},
		{`!Select [0, [{plain: value}]]`, map[string]any{"plain": "value"}},
		{`!Base64 {Ref: Value}`, base64.StdEncoding.EncodeToString([]byte(refs["Value"]))},
		{`Fn::Base64: !Sub '${Value}${!aws:userid}'`, base64.StdEncoding.EncodeToString([]byte(refs["Value"] + "${aws:userid}"))},
		{`!Base64 ""`, ""},
	} {
		t.Run(tc.source, func(t *testing.T) {
			value := intrinsicYAML(t, tc.source)
			selected, omit, err := stackSelectConditional(value, nil)
			if err != nil || omit {
				t.Fatalf("select: %v", err)
			}
			actual, err := resolve(selected, refs, nil)
			if err != nil || !reflect.DeepEqual(actual, tc.want) {
				t.Fatalf("got %#v, %v; want %#v", actual, err, tc.want)
			}
		})
	}
	for _, region := range Regions {
		for _, arg := range []any{region, "", map[string]any{"Ref": "AWS::Region"}} {
			got, err := resolve(map[string]any{"Fn::GetAZs": arg}, map[string]string{"AWS::Region": region}, nil)
			if err != nil || !reflect.DeepEqual(got, []any{region + "a"}) {
				t.Fatal(region, got, err)
			}
		}
	}
	zones, _ := localAvailabilityZones("us-east-1")
	zones[0] = "mutated"
	next, _ := localAvailabilityZones("us-east-1")
	if next[0] != "us-east-1a" {
		t.Fatal("AZ caller mutated provider result")
	}
	got, err := resolve(map[string]any{"Fn::Select": []any{json.Number("1"), []any{"a", "b"}}}, nil, nil)
	if err != nil || got != "b" {
		t.Fatal(got, err)
	}
}

func TestStackIntrinsicShapesAndResolvedTypes(t *testing.T) {
	cases := []any{
		map[string]any{"Fn::Select": nil}, map[string]any{"Fn::Select": []any{}}, map[string]any{"Fn::Select": []any{"0"}}, map[string]any{"Fn::Select": []any{"0", []any{"a"}, "extra"}},
		map[string]any{"Fn::Select": []any{"0", nil}}, map[string]any{"Fn::Select": []any{"0", "not-list"}}, map[string]any{"Fn::Select": []any{"0", []any{}}},
		map[string]any{"Fn::Select": []any{"0", []any{nil}}}, map[string]any{"Fn::Select": []any{"0", []any{"a", nil}}},
		map[string]any{"Fn::GetAZs": nil}, map[string]any{"Fn::GetAZs": true}, map[string]any{"Fn::GetAZs": []any{"us-east-1"}},
		map[string]any{"Fn::GetAZs": map[string]any{"Fn::Sub": "us-east-1"}},
		map[string]any{"Fn::Base64": nil}, map[string]any{"Fn::Base64": true}, map[string]any{"Fn::Base64": []any{"text"}}, map[string]any{"Fn::Base64": map[string]any{"plain": "text"}},
		map[string]any{"Fn::Base64": "text", "extra": "x"}, map[string]any{"Fn::Select": []any{"0", []any{"x"}}, "extra": "x"}, map[string]any{"Fn::GetAZs": "", "extra": "x"},
	}
	for _, index := range []any{nil, true, -1, 0, 1.0, json.Number("1.0"), "-1", "+1", "1.5", "1e0", "NaN", "Inf", "", " 0", "0 ", "00", "０", "999999999999999999999999", "10000", "4294967295", map[string]any{"Fn::Sub": "0"}} {
		cases = append(cases, map[string]any{"Fn::Select": []any{index, []any{"a", "b"}}})
	}
	for i, value := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if _, _, err := stackSelectConditional(value, nil); err == nil {
				t.Fatalf("invalid shape accepted: %#v", value)
			}
			if _, err := resolve(value, nil, nil); err == nil {
				t.Fatalf("invalid resolve accepted: %#v", value)
			}
			inactive := map[string]any{"Fn::If": []any{"Off", "safe", value}}
			if _, _, err := stackSelectConditional(inactive, map[string]bool{"Off": true}); err == nil {
				t.Fatal("inactive malformed intrinsic accepted")
			}
		})
	}
	for _, value := range []any{
		map[string]any{"Fn::Select": []any{"2", []any{"a", "b"}}},
		map[string]any{"Fn::Select": []any{map[string]any{"Ref": "Index"}, []any{"a"}}},
		map[string]any{"Fn::Select": []any{"0", map[string]any{"Ref": "Value"}}},
		map[string]any{"Fn::Base64": map[string]any{"Fn::Select": []any{"0", []any{true}}}},
		map[string]any{"Fn::Base64": map[string]any{"Fn::Select": []any{"0", []any{map[string]any{"a": "b"}}}}},
		map[string]any{"Fn::GetAZs": "unknown-1"}, map[string]any{"Fn::GetAZs": ""}, map[string]any{"Fn::GetAZs": map[string]any{"Ref": "Missing"}},
	} {
		if _, err := resolve(value, map[string]string{"Index": "-1", "Value": "a,b"}, nil); err == nil {
			t.Fatalf("bad evaluated operand accepted: %#v", value)
		}
	}
	for _, body := range []string{`!Select x`, `!Select {a: b}`, `!GetAZs [x]`, `!Base64 [x]`, `!Base64 !Sub x`, `!GetAZs !Ref AWS::Region`, `!Sub [x, {}]`, `!Unsupported x`} {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(body), &node); err != nil {
			continue
		}
		if _, err := taggedYAML(&node); err == nil {
			t.Fatalf("bad tag accepted: %s", body)
		}
	}
}

func TestStackIntrinsicSubEscapesAndMalformed(t *testing.T) {
	refs := map[string]string{"Name": "雪\n", "Nested": "${Missing}", "Literal": "should-not-replace"}
	attrs := map[string]string{"R.Arn": "arn:local"}
	for _, tc := range []struct{ source, want string }{
		{"plain\n雪\n", "plain\n雪\n"}, {"${Name}${!aws:userid}-${R.Arn}", "雪\n${aws:userid}-arn:local"},
		{"${!Literal}${!Literal}", "${Literal}${Literal}"}, {"${Nested}", "${Missing}"}, {"\\${!Literal}", "\\${Literal}"}, {"${!雪}", "${雪}"}, {"${!!Literal}", "${!Literal}"},
	} {
		got, err := resolve(map[string]any{"Fn::Sub": tc.source}, refs, attrs)
		if err != nil || got != tc.want {
			t.Fatal(tc.source, got, err)
		}
	}
	for _, source := range []string{"${", "${Name", "${}", "${!}", "${bad name}", "${a${Name}}", "${!a${Name}}", "${Name\n}", "${Name\r}", "${Missing}", "${雪}"} {
		if _, err := resolve(map[string]any{"Fn::Sub": source}, refs, attrs); err == nil {
			t.Fatalf("bad Sub accepted: %q", source)
		}
	}
}

func TestStackIntrinsicBoundsAndNoInputMutation(t *testing.T) {
	for _, size := range []int{38400, 38401} {
		value := map[string]any{"Fn::Base64": strings.Repeat("a", size)}
		got, err := resolve(value, nil, nil)
		if size == 38400 && (err != nil || len(stringValue(got)) != 51200) || size == 38401 && err == nil {
			t.Fatal("Base64 expansion bound", size, err)
		}
	}
	if _, err := resolve(map[string]any{"Fn::Sub": "${V}${V}"}, map[string]string{"V": strings.Repeat("a", 25601)}, nil); err == nil {
		t.Fatal("Sub expansion unbounded")
	}
	if _, err := resolve(map[string]any{"Ref": "V"}, map[string]string{"V": strings.Repeat("a", 51201)}, nil); err == nil {
		t.Fatal("resolved Ref unbounded")
	}
	var nested any = "safe"
	for i := 0; i < 34; i++ {
		nested = map[string]any{"Fn::Base64": nested}
	}
	if _, err := resolve(nested, nil, nil); err == nil {
		t.Fatal("resolver depth unbounded")
	}
	if _, _, err := stackSelectConditional(nested, nil); err == nil {
		t.Fatal("preparation depth unbounded")
	}
	cycle := map[string]any{}
	cycle["Fn::Base64"] = cycle
	if _, err := resolve(cycle, nil, nil); err == nil {
		t.Fatal("cyclic resolver carrier accepted")
	}
	if _, _, err := stackSelectConditional(cycle, nil); err == nil {
		t.Fatal("cyclic preparation accepted")
	}
	wide := make([]any, stackConditionalNodes)
	for i := range wide {
		wide[i] = "a"
	}
	if _, err := resolve(wide, nil, nil); err == nil {
		t.Fatal("resolver node budget unbounded")
	}
	original := map[string]any{"Fn::Select": []any{"0", []any{map[string]any{"value": "kept"}}}}
	before, _ := json.Marshal(original)
	got, err := resolve(original, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	object(got)["value"] = "changed"
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("resolution mutated original carrier")
	}
}
