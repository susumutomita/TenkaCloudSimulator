package aws

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ssmDocumentName = regexp.MustCompile(`^[a-zA-Z0-9_\-.]{3,128}$`)

func validSSMDocumentRegion(region string) bool {
	for _, r := range Regions {
		if r == region {
			return true
		}
	}
	return false
}
func validSSMDocumentAddress(region, name string) bool {
	if !validSSMDocumentRegion(region) || !ssmDocumentName.MatchString(name) {
		return false
	}
	lower := strings.ToLower(name)
	for _, prefix := range []string{"aws", "amazon", "amzn"} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	return true
}

// Reject duplicate keys at every nesting level, null, invalid UTF-8, trailing
// data and excessive nesting before typed decoding. No unknown fields vanish.
func strictDocumentJSON(raw []byte, out any) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 16 {
			return fmt.Errorf("JSON nesting exceeds limit")
		}
		t, e := dec.Token()
		if e != nil {
			return e
		}
		if t == nil {
			return fmt.Errorf("null is unsupported")
		}
		d, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch d {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				k, e := dec.Token()
				if e != nil {
					return e
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return fmt.Errorf("duplicate object field")
				}
				seen[key] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for dec.More() {
				if e := walk(depth + 1); e != nil {
					return e
				}
			}
		default:
			return fmt.Errorf("invalid delimiter")
		}
		_, e = dec.Token()
		return e
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	var shape any
	if err := json.Unmarshal(raw, &shape); err != nil {
		return err
	}
	if err := exactDocumentFields(shape, reflect.TypeOf(out)); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

type ssmDocumentContent struct {
	SchemaVersion string `json:"schemaVersion"`
	Description   string `json:"description,omitempty"`
	MainSteps     []struct {
		Action string `json:"action"`
		Name   string `json:"name"`
		Inputs struct {
			RunCommand []string `json:"runCommand"`
		} `json:"inputs"`
	} `json:"mainSteps"`
}

func validateSSMDocument(region, name, content string) error {
	if !validSSMDocumentAddress(region, name) {
		return documentFailure("ValidationException", "Invalid local document name or region.")
	}
	if len(content) == 0 || len(content) > 64<<10 {
		return documentFailure("MaxDocumentSizeExceeded", "Content must contain 1 to 65536 UTF-8 bytes.")
	}
	var doc ssmDocumentContent
	if err := strictDocumentJSON([]byte(content), &doc); err != nil {
		return documentFailure("InvalidDocumentContent", err.Error())
	}
	if doc.SchemaVersion != "2.2" || len(doc.MainSteps) < 1 || len(doc.MainSteps) > 8 || len(doc.Description) > 1024 {
		return documentFailure("InvalidDocumentContent", "Only bounded Command schema 2.2 is supported.")
	}
	names := map[string]bool{}
	for _, step := range doc.MainSteps {
		if step.Action != "aws:runShellScript" || !regexp.MustCompile(`^[a-zA-Z0-9_]{1,128}$`).MatchString(step.Name) || names[step.Name] || len(step.Inputs.RunCommand) < 1 || len(step.Inputs.RunCommand) > 64 {
			return documentFailure("InvalidDocumentContent", "Unsupported or duplicate document step.")
		}
		names[step.Name] = true
		for _, line := range step.Inputs.RunCommand {
			if len(line) > 8192 || strings.Contains(line, "\x00") || strings.Contains(line, "{{") {
				return documentFailure("InvalidDocumentContent", "Only bounded literal commands are accepted; interpolation is unsupported.")
			}
		}
	}
	return nil
}

// encoding/json accepts case-insensitive struct field matches; the wire contract
// intentionally does not. Check nested content fields as well as API envelopes.
func exactDocumentFields(value any, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("expected object")
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" {
				name = f.Name
			}
			fields[name] = f.Type
		}
		for key, item := range obj {
			ft, ok := fields[key]
			if !ok {
				return fmt.Errorf("unsupported field: %s", key)
			}
			if err := exactDocumentFields(item, ft); err != nil {
				return err
			}
		}
	case reflect.Slice:
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("expected array")
		}
		for _, item := range items {
			if err := exactDocumentFields(item, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
