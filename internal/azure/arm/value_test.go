package arm

import (
	"encoding/json"
	"strings"
	"testing"
)

func intp(n int) *int           { return &n }
func floatp(n float64) *float64 { return &n }

func TestDecoderFollowsTheOfficialDefinitions(t *testing.T) {
	m := &Model{Namespace: "Test", Definitions: map[string]*Schema{
		"Base":   {Type: "object", Discriminator: "kind", Subtypes: map[string]string{"Alpha": "Alpha"}, Properties: map[string]*Schema{"kind": {Type: "string"}, "shared": {Type: "string"}}, Required: []string{"kind"}},
		"Alpha":  {AllOf: []*Schema{{Ref: "Base"}}, Properties: map[string]*Schema{"alpha": {Type: "integer", Format: "int32"}}},
		"Mixed":  {Type: "object", Properties: map[string]*Schema{"known": {Type: "string"}}, Additional: json.RawMessage(`{"type":"integer"}`)},
		"Open":   {Type: "object", Additional: json.RawMessage(`true`)},
		"Closed": {Type: "object", Additional: json.RawMessage(`false`)},
		"Free":   {Type: "object"},
		"Main": {Type: "object", Required: []string{"needed", "server"}, Properties: map[string]*Schema{
			"needed":   {Type: "string"},
			"server":   {Type: "string", ReadOnly: true},
			"written":  {Type: "string", Mutability: []string{"read"}},
			"nullable": {Type: "string", Nullable: true},
			"secret":   {Type: "string", Secret: true},
			"password": {Type: "string", Format: "password"},
			"enum":     {Type: "string", Enum: []any{"One", "Two"}},
			"pattern":  {Type: "string", Pattern: `^[a-z]+$`},
			"length":   {Type: "string", MinLength: intp(2), MaxLength: intp(3)},
			"stamp":    {Type: "string", Format: "date-time"},
			"day":      {Type: "string", Format: "date"},
			"id":       {Type: "string", Format: "uuid"},
			"span":     {Type: "string", Format: "duration"},
			"bytes":    {Type: "string", Format: "byte"},
			"link":     {Type: "string", Format: "uri"},
			"arm":      {Type: "string", Format: "arm-id"},
			"small":    {Type: "integer", Format: "int32", Minimum: floatp(1), Maximum: floatp(10)},
			"big":      {Type: "integer", Format: "int64"},
			"ratio":    {Type: "number", Minimum: floatp(0), Maximum: floatp(1), ExclusiveMax: true, ExclusiveMin: true},
			"flag":     {Type: "boolean"},
			"list":     {Type: "array", Items: &Schema{Type: "string"}, MinItems: intp(1), MaxItems: intp(2)},
			"base":     {Ref: "Base"},
			"mixed":    {Ref: "Mixed"},
			"open":     {Ref: "Open"},
			"closed":   {Ref: "Closed"},
			"free":     {Ref: "Free"},
			"any":      {},
			"odd":      {Type: "mystery"},
		}},
	}}
	if err := validateModel(&Model{Namespace: "x"}); err == nil {
		t.Fatal("provenance is required")
	}
	m.Source = Source{Repository: "github.com/Azure/azure-rest-api-specs", Commit: strings.Repeat("a", 40), License: "MIT", Files: []SourceFile{{Path: "specification/x.json", SHA256: strings.Repeat("b", 64)}}}
	m.APIVersion = "2026-01-01"
	if err := validateModel(m); err != nil {
		t.Fatal(err)
	}
	decode := func(body map[string]any) (map[string]any, error) {
		raw, _ := json.Marshal(body)
		parsed := map[string]any{}
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.UseNumber()
		_ = d.Decode(&parsed)
		out, err := (&decoder{model: m}).decode(&Schema{Ref: "Main"}, parsed, "", 0)
		if err != nil {
			return nil, err
		}
		return out.(map[string]any), nil
	}
	good, err := decode(map[string]any{"needed": "x", "server": "dropped", "written": "dropped", "nullable": nil, "enum": "two", "pattern": "abc", "length": "ab",
		"stamp": "2026-11-06T12:00:00Z", "day": "2026-11-06", "id": "00000000-0000-4000-8000-000000000001", "span": "PT5M", "bytes": "aGk=", "link": "https://example.test/x", "arm": "/subscriptions/x",
		"small": 5, "big": 9007199254740993, "ratio": 0.5, "flag": true, "list": []any{"a"}, "base": map[string]any{"kind": "alpha", "alpha": 3},
		"mixed": map[string]any{"known": "k", "extra": 4}, "open": map[string]any{"x": []any{map[string]any{"y": 1.5}}}, "free": map[string]any{"a": "b"}, "any": []any{"z"}})
	if err != nil {
		t.Fatal(err)
	}
	if good["server"] != nil || good["written"] != nil || good["enum"] != "Two" || good["big"] != int64(9007199254740993) || good["nullable"] != nil {
		t.Fatalf("decoded %v", good)
	}
	bad := map[string]map[string]any{
		"unknown":       {"needed": "x", "zzz": 1},
		"required":      {},
		"secret":        {"needed": "x", "secret": "s"},
		"password":      {"needed": "x", "password": "s"},
		"enum":          {"needed": "x", "enum": "Three"},
		"pattern":       {"needed": "x", "pattern": "ABC"},
		"short":         {"needed": "x", "length": "a"},
		"stamp":         {"needed": "x", "stamp": "yesterday"},
		"day":           {"needed": "x", "day": "06/11/2026"},
		"uuid":          {"needed": "x", "id": "nope"},
		"duration":      {"needed": "x", "span": "5 minutes"},
		"empty span":    {"needed": "x", "span": "PT"},
		"bytes":         {"needed": "x", "bytes": "%%"},
		"uri":           {"needed": "x", "link": "relative"},
		"arm":           {"needed": "x", "arm": "subscriptions/x"},
		"range":         {"needed": "x", "small": 11},
		"int32":         {"needed": "x", "small": 99999999999},
		"fraction":      {"needed": "x", "small": 1.5},
		"integer type":  {"needed": "x", "small": "5"},
		"exclusive":     {"needed": "x", "ratio": 1},
		"number type":   {"needed": "x", "ratio": "half"},
		"boolean":       {"needed": "x", "flag": "yes"},
		"string type":   {"needed": 3},
		"array type":    {"needed": "x", "list": "a"},
		"array bounds":  {"needed": "x", "list": []any{}},
		"array null":    {"needed": "x", "list": []any{nil}},
		"array item":    {"needed": "x", "list": []any{1}},
		"object type":   {"needed": "x", "base": "x"},
		"null":          {"needed": "x", "enum": nil},
		"discriminator": {"needed": "x", "base": map[string]any{"kind": "Beta"}},
		"subtype field": {"needed": "x", "base": map[string]any{"kind": "Alpha", "alpha": "x"}},
		"base required": {"needed": "x", "base": map[string]any{"shared": "s"}},
		"map value":     {"needed": "x", "mixed": map[string]any{"extra": "four"}},
		"map null":      {"needed": "x", "mixed": map[string]any{"extra": nil}},
		"closed":        {"needed": "x", "closed": map[string]any{"a": 1}},
		"odd":           {"needed": "x", "odd": 1},
		"long string":   {"needed": strings.Repeat("x", maxStringBytes+1)},
		"free string":   {"needed": "x", "free": map[string]any{"a": strings.Repeat("x", maxStringBytes+1)}},
	}
	for name, body := range bad {
		if _, err := decode(body); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := (&decoder{model: m}).document(json.Number("1e999"), "f", 0); err == nil {
		t.Fatal("an infinite free-form number is refused")
	}
	deep := map[string]any{}
	cursor := deep
	for i := 0; i < maxDepth+2; i++ {
		next := map[string]any{}
		cursor["x"] = next
		cursor = next
	}
	if _, err := decode(map[string]any{"needed": "x", "free": deep}); err == nil {
		t.Fatal("depth bound")
	}
	if _, err := (&decoder{model: m}).decode(&Schema{Ref: "Missing"}, map[string]any{}, "", 0); err == nil {
		t.Fatal("a missing definition is refused")
	}
	if (&armError{500, "x", "m"}).Error() != "m" || definitionName("") != "the request body" || definitionName("a") != "a" {
		t.Fatal("error text")
	}
}

func TestParametersPathsAndPatterns(t *testing.T) {
	cases := []struct {
		p    *Parameter
		in   string
		want any
		ok   bool
	}{
		{&Parameter{Name: "b", In: "query", Type: "boolean"}, "TRUE", true, true},
		{&Parameter{Name: "b", In: "query", Type: "boolean"}, "false", false, true},
		{&Parameter{Name: "b", In: "query", Type: "boolean"}, "yes", nil, false},
		{&Parameter{Name: "n", In: "query", Type: "integer", Minimum: floatp(1), Maximum: floatp(5)}, "3", int64(3), true},
		{&Parameter{Name: "n", In: "query", Type: "integer", Minimum: floatp(1)}, "0", nil, false},
		{&Parameter{Name: "e", In: "query", Type: "string", Enum: []any{"A"}}, "a", "A", true},
		{&Parameter{Name: "e", In: "query", Type: "string", Enum: []any{"A"}}, "b", nil, false},
		{&Parameter{Name: "l", In: "path", Type: "string", MaxLength: intp(2)}, "abc", nil, false},
		{&Parameter{Name: "u", In: "path", Type: "string", Format: "uuid"}, "x", nil, false},
		{&Parameter{Name: "s", In: "path", Type: "string"}, "\xff", nil, false},
	}
	for i, c := range cases {
		got, err := parameterValue(c.p, c.in)
		if (err == nil) != c.ok || c.ok && got != c.want {
			t.Fatalf("case %d: %v %v", i, got, err)
		}
	}
	word, err := compilePattern(`^[-\w\._\(\)]+$`)
	if err != nil || !word.MatchString("日本-rg_(1).x") || word.MatchString("a/b") {
		t.Fatal(`\w inside a class matches Unicode word characters`)
	}
	outside, err := compilePattern(`^\w+\d$`)
	if err != nil || !outside.MatchString("グループ1") {
		t.Fatal(`\w outside a class matches Unicode word characters`)
	}
	if _, err := compilePattern(`(?<=x)`); err == nil {
		t.Fatal("lookbehind is not RE2")
	}
	doc := map[string]any{"a": map[string]any{"list": []any{map[string]any{"id": "x"}, map[string]any{"id": 3}}, "map": map[string]any{"k1": 1}}}
	if got := collect(doc, splitRef("a.list[].id")); len(got) != 1 || got[0] != "x" {
		t.Fatalf("array refs %v", got)
	}
	if got := collect(doc, splitRef("a.map.{}")); len(got) != 1 || got[0] != "k1" {
		t.Fatalf("map refs %v", got)
	}
	if collect(doc, splitRef("a.none.id")) != nil || collect(doc, splitRef("a.list.id")) != nil {
		t.Fatal("missing refs")
	}
	assign(doc, "b.c.d", 1)
	if v, ok := lookup(doc, "b.c.d"); !ok || v != 1 {
		t.Fatal("assign/lookup")
	}
	if _, ok := lookup(doc, "a.list.x"); ok {
		t.Fatal("lookup through an array")
	}
	if !readOnly(&Schema{Mutability: []string{"read"}}, nil) || readOnly(&Schema{Mutability: []string{"create"}}, nil) || !writeOnly(&Schema{Mutability: []string{"create", "update"}}) || writeOnly(&Schema{}) || !createOnly(&Schema{Mutability: []string{"create", "read"}}) || createOnly(&Schema{}) {
		t.Fatal("mutability helpers")
	}
	if numbers(map[string]any{"n": json.Number("1.5"), "l": []any{json.Number("2")}}).(map[string]any)["n"] != 1.5 {
		t.Fatal("stored numbers")
	}
	if _, err := numberValue(&Schema{}, "x", "f"); err == nil {
		t.Fatal("number type")
	}
	if equalsAny("x", "a", "b") || !equalsAny("A", "a") {
		t.Fatal("equalsAny")
	}
}
