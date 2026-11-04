package aws

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
)

// wafDecode checks exact native field spelling, unknown keys, duplicates and trailing values.
func wafDecode(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var parse func(int) (any, error)
	parse = func(depth int) (any, error) {
		if depth > 32 {
			return nil, fmt.Errorf("JSON nesting exceeds simulator limit")
		}
		t, e := d.Token()
		if e != nil {
			return nil, e
		}
		if delimiter, ok := t.(json.Delim); ok {
			switch delimiter {
			case '{':
				m := map[string]any{}
				for d.More() {
					key, e := d.Token()
					if e != nil {
						return nil, e
					}
					k, ok := key.(string)
					if !ok {
						return nil, fmt.Errorf("invalid object key")
					}
					if _, ok = m[k]; ok {
						return nil, fmt.Errorf("duplicate field %s", k)
					}
					v, e := parse(depth + 1)
					if e != nil {
						return nil, e
					}
					m[k] = v
				}
				_, e = d.Token()
				return m, e
			case '[':
				a := []any{}
				for d.More() {
					v, e := parse(depth + 1)
					if e != nil {
						return nil, e
					}
					a = append(a, v)
				}
				_, e = d.Token()
				return a, e
			default:
				return nil, fmt.Errorf("unexpected delimiter")
			}
		}
		if t == nil {
			return nil, fmt.Errorf("null values are unsupported")
		}
		return t, nil
	}
	value, e := parse(0)
	if e != nil {
		return e
	}
	if _, e = d.Token(); e != io.EOF {
		return fmt.Errorf("trailing JSON content")
	}
	var check func(any, reflect.Type) error
	check = func(v any, t reflect.Type) error {
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Struct:
			m, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("expected object")
			}
			fields := map[string]reflect.Type{}
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				name := strings.Split(f.Tag.Get("json"), ",")[0]
				if name == "-" {
					continue
				}
				if name == "" {
					name = f.Name
				}
				fields[name] = f.Type
			}
			if t != reflect.TypeFor[wafRequest]() {
				for i := 0; i < t.NumField(); i++ {
					f := t.Field(i)
					tag := f.Tag.Get("json")
					name := strings.Split(tag, ",")[0]
					if name == "-" || strings.Contains(tag, "omitempty") {
						continue
					}
					if name == "" {
						name = f.Name
					}
					if _, ok := m[name]; !ok {
						return fmt.Errorf("missing field %s", name)
					}
				}
			}
			for k, x := range m {
				ft, ok := fields[k]
				if !ok {
					return fmt.Errorf("unknown field %s", k)
				}
				if e := check(x, ft); e != nil {
					return e
				}
			}
		case reflect.Slice:
			if t.Elem().Kind() == reflect.Uint8 {
				return nil
			}
			a, ok := v.([]any)
			if !ok {
				return fmt.Errorf("expected array")
			}
			for _, x := range a {
				if e := check(x, t.Elem()); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e = check(value, reflect.TypeOf(target)); e != nil {
		return e
	}
	return json.Unmarshal(data, target)
}
func wafHTTPError(w http.ResponseWriter, e error) {
	code, message, status := "WAFInternalErrorException", "Simulator WAF state operation failed.", 500
	var f ssmFailure
	if errors.As(e, &f) {
		code, message, status = f.code, f.message, 400
		if code == "AccessDeniedException" {
			status = 403
		}
		if code == "UnknownOperationException" {
			status = 501
		}
	}
	ssmJSON(w, status, map[string]string{"__type": code, "message": message})
}

type wafRequest struct {
	Tags             []WAFTag      `json:"Tags"`
	ResourceARN      string        `json:"ResourceARN"`
	Name             string        `json:"Name"`
	ID               string        `json:"Id"`
	Scope            string        `json:"Scope"`
	Description      string        `json:"Description"`
	DefaultAction    WAFAction     `json:"DefaultAction"`
	Rules            []WAFRule     `json:"Rules"`
	VisibilityConfig WAFVisibility `json:"VisibilityConfig"`
	LockToken        string        `json:"LockToken"`
	NextMarker       string        `json:"NextMarker"`
	Limit            int           `json:"Limit"`
	ResourceArn      string        `json:"ResourceArn"`
	WebACLArn        string        `json:"WebACLArn"`
	ResourceType     string        `json:"ResourceType"`
}

func (s *WAFStore) ServeHTTP(w http.ResponseWriter, r *http.Request, region string, authorize func(string, string) bool) {
	fail := func(e error) { wafHTTPError(w, e) }
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" || !slices.Contains(Regions, region) {
		fail(wafInvalid("Use POST to a supported regional root endpoint without query parameters."))
		return
	}
	operation := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AWSWAF_20190729.")
	allowed := map[string][]string{
		"CreateWebACL": {"Name", "Scope", "Description", "DefaultAction", "Rules", "VisibilityConfig", "Tags"}, "GetWebACL": {"Name", "Id", "Scope"}, "UpdateWebACL": {"Name", "Id", "Scope", "Description", "DefaultAction", "Rules", "VisibilityConfig", "LockToken"}, "DeleteWebACL": {"Name", "Id", "Scope", "LockToken"}, "ListWebACLs": {"Scope", "NextMarker", "Limit"}, "AssociateWebACL": {"ResourceArn", "WebACLArn"}, "DisassociateWebACL": {"ResourceArn"}, "GetWebACLForResource": {"ResourceArn"}, "ListResourcesForWebACL": {"WebACLArn", "ResourceType"}, "ListTagsForResource": {"ResourceARN"},
	}
	fields, ok := allowed[operation]
	if !ok || r.Header.Get("X-Amz-Target") != "AWSWAF_20190729."+operation {
		fail(ssmFailure{"UnknownOperationException", "Unsupported WAFv2 operation."})
		return
	}
	body, e := io.ReadAll(io.LimitReader(r.Body, 262145))
	if e != nil || len(body) > 262144 {
		fail(wafInvalid("Invalid or excessive request body (maximum 256KiB)."))
		return
	}
	var q wafRequest
	if e = wafDecode(body, &q); e != nil {
		fail(wafInvalid("Invalid WAF JSON: " + e.Error()))
		return
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(body, &raw)
	for k := range raw {
		if !slices.Contains(fields, k) {
			fail(wafInvalid("Unsupported field for this operation: " + k))
			return
		}
	}
	required := map[string][]string{"CreateWebACL": {"Name", "Scope", "DefaultAction", "VisibilityConfig"}, "GetWebACL": {"Name", "Id", "Scope"}, "UpdateWebACL": {"Name", "Id", "Scope", "DefaultAction", "VisibilityConfig", "LockToken"}, "DeleteWebACL": {"Name", "Id", "Scope", "LockToken"}, "ListWebACLs": {"Scope"}, "AssociateWebACL": {"ResourceArn", "WebACLArn"}, "DisassociateWebACL": {"ResourceArn"}, "GetWebACLForResource": {"ResourceArn"}, "ListResourcesForWebACL": {"WebACLArn"}, "ListTagsForResource": {"ResourceARN"}}
	for _, key := range required[operation] {
		if _, ok := raw[key]; !ok {
			fail(wafInvalid("Missing required field: " + key))
			return
		}
	}
	if slices.Contains(fields, "Scope") && q.Scope != "REGIONAL" {
		fail(wafInvalid("Only REGIONAL scope is supported."))
		return
	}
	if slices.Contains(fields, "Id") && (!wafUUID.MatchString(q.ID) || !wafName.MatchString(q.Name)) {
		fail(wafInvalid("Specify a valid Web ACL Id and Name."))
		return
	}
	if slices.Contains(fields, "LockToken") && !wafUUID.MatchString(q.LockToken) {
		fail(wafInvalid("Specify a valid LockToken."))
		return
	}
	if q.NextMarker != "" && !wafUUID.MatchString(q.NextMarker) {
		fail(wafInvalid("Invalid NextMarker."))
		return
	}
	if q.Limit == 0 {
		if _, provided := raw["Limit"]; provided {
			fail(wafInvalid("Limit must be 1–100."))
			return
		}
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 100 {
		fail(wafInvalid("Limit must be 1–100."))
		return
	}
	arn := "*"
	if q.ResourceARN != "" {
		arn = q.ResourceARN
	}
	if q.ID != "" {
		arn = wafARN(region, q.Name, q.ID)
	} else if q.WebACLArn != "" {
		arn = q.WebACLArn
	}
	if operation == "DisassociateWebACL" || operation == "GetWebACLForResource" {
		associated, lookupErr := s.ForResource(r.Context(), region, q.ResourceArn)
		if lookupErr != nil {
			fail(lookupErr)
			return
		}
		if associated != nil {
			arn = associated.ARN
		}
	}
	if authorize == nil || !authorize("wafv2:"+operation, arn) {
		fail(ssmFailure{"AccessDeniedException", "WAF action is not allowed."})
		return
	}
	if (operation == "AssociateWebACL" || operation == "DisassociateWebACL") && (authorize == nil || !authorize("apigateway:SetWebACL", q.ResourceArn)) {
		fail(ssmFailure{"AccessDeniedException", "API Gateway stage association is not allowed."})
		return
	}
	if operation == "GetWebACLForResource" && !authorize("wafv2:GetWebACL", arn) {
		fail(ssmFailure{"AccessDeniedException", "Web ACL read is not allowed."})
		return
	}
	ctx := r.Context()
	var response any = map[string]any{}
	switch operation {
	case "CreateWebACL":
		var summary WAFSummary
		summary, e = s.Create(ctx, region, WAFWebACL{Tags: q.Tags, Name: q.Name, Description: q.Description, DefaultAction: q.DefaultAction, Rules: q.Rules, VisibilityConfig: q.VisibilityConfig})
		response = map[string]any{"Summary": summary}
	case "GetWebACL":
		var acl WAFWebACL
		var token string
		acl, token, e = s.Get(ctx, region, q.ID, q.Name)
		response = map[string]any{"WebACL": acl, "LockToken": token}
	case "UpdateWebACL":
		var token string
		token, e = s.Update(ctx, region, WAFWebACL{Name: q.Name, ID: q.ID, Description: q.Description, DefaultAction: q.DefaultAction, Rules: q.Rules, VisibilityConfig: q.VisibilityConfig}, q.LockToken)
		response = map[string]string{"NextLockToken": token}
	case "DeleteWebACL":
		e = s.Delete(ctx, region, q.ID, q.Name, q.LockToken)
	case "ListWebACLs":
		var a []WAFSummary
		a, e = s.List(ctx, region, q.NextMarker, q.Limit+1)
		v := map[string]any{"WebACLs": a}
		if len(a) > q.Limit {
			v["WebACLs"] = a[:q.Limit]
			v["NextMarker"] = a[q.Limit-1].ID
		}
		response = v
	case "AssociateWebACL":
		e = s.Associate(ctx, region, q.ResourceArn, q.WebACLArn)
	case "DisassociateWebACL":
		e = s.Disassociate(ctx, region, q.ResourceArn)
	case "GetWebACLForResource":
		var a *WAFWebACL
		a, e = s.ForResource(ctx, region, q.ResourceArn)
		if a != nil {
			response = map[string]any{"WebACL": a}
		}
	case "ListTagsForResource":
		var tags []WAFTag
		tags, e = s.Tags(ctx, region, q.ResourceARN)
		response = map[string]any{"TagInfoForResource": map[string]any{"ResourceARN": q.ResourceARN, "TagList": tags}}
	case "ListResourcesForWebACL":
		if q.ResourceType != "" && q.ResourceType != "API_GATEWAY" {
			fail(wafInvalid("Only API_GATEWAY resources are supported."))
			return
		}
		var a []string
		a, e = s.Resources(ctx, region, q.WebACLArn)
		response = map[string]any{"ResourceArns": a}
	}
	if e != nil {
		fail(e)
		return
	}
	ssmJSON(w, 200, response)
}
