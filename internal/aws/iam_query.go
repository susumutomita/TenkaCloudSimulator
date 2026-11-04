package aws

import (
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const iamNamespace = "https://iam.amazonaws.com/doc/2010-05-08/"

func isIAM(r *http.Request) bool {
	return r.Header.Get("X-Simulator-Service") == "iam" || strings.Contains(r.Header.Get("Authorization"), "/iam/aws4_request")
}
func iamEncoded(document string) string {
	return strings.ReplaceAll(url.QueryEscape(document), "+", "%20")
}
func iamWireRole(role IAMRole) IAMRole {
	role.AssumeRolePolicyDocument = iamEncoded(role.AssumeRolePolicyDocument)
	return role
}
func iamResponse(w http.ResponseWriter, action string, result any) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(200)
	_, _ = io.WriteString(w, xml.Header)
	encoder := xml.NewEncoder(w)
	root := xml.StartElement{Name: xml.Name{Local: action + "Response"}, Attr: []xml.Attr{{Name: xml.Name{Local: "xmlns"}, Value: iamNamespace}}}
	_ = encoder.EncodeToken(root)
	if result != nil {
		_ = encoder.EncodeElement(result, xml.StartElement{Name: xml.Name{Local: action + "Result"}})
	}
	_ = encoder.EncodeElement(struct {
		RequestID string `xml:"RequestId"`
	}{fmt.Sprintf("iam-local-%016x", sequence.Add(1))}, xml.StartElement{Name: xml.Name{Local: "ResponseMetadata"}})
	_ = encoder.EncodeToken(root.End())
	_ = encoder.Flush()
}
func iamError(w http.ResponseWriter, err error) {
	code, message, status := "ServiceFailure", "Local IAM state operation failed.", 500
	var failure ssmFailure
	if errors.As(err, &failure) {
		code, message, status = failure.code, failure.message, 400
		switch code {
		case "NoSuchEntity":
			status = 404
		case "EntityAlreadyExists", "DeleteConflict", "LimitExceeded":
			status = 409
		case "AccessDenied":
			status = 403
		case "NotImplemented":
			status = 501
		case "RequestEntityTooLarge":
			status = 413
		}
	}
	type detail struct {
		Type    string `xml:"Type"`
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	type response struct {
		XMLName   xml.Name `xml:"ErrorResponse"`
		Namespace string   `xml:"xmlns,attr"`
		Error     detail
		RequestID string `xml:"RequestId"`
	}
	kind := "Sender"
	if status >= 500 && status != 501 {
		kind = "Receiver"
	}
	writeXML(w, status, response{Namespace: iamNamespace, Error: detail{kind, code, message}, RequestID: fmt.Sprintf("iam-local-%016x", sequence.Add(1))})
}

func (g *Gateway) serveIAM(w http.ResponseWriter, r *http.Request) {
	invalid := func(message string) { iamError(w, ssmFailure{"ValidationError", message}) }
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" {
		invalid("Use root POST with IAM Query parameters.")
		return
	}
	if g.Roles == nil || g.Identities == nil {
		iamError(w, ssmFailure{"NotImplemented", "Native IAM state is unavailable."})
		return
	}
	if r.Header.Get("X-Amz-Security-Token") != "" {
		iamError(w, ssmFailure{"AccessDenied", "Real or session credentials are unsupported."})
		return
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		_, credential, ok := strings.Cut(auth, "Credential=")
		key, _, _ := strings.Cut(credential, "/")
		if !ok || !strings.HasPrefix(key, "TCSIM") {
			iamError(w, ssmFailure{"AccessDenied", "Use unsigned requests or dummy TCSIM credentials."})
			return
		}
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		iamError(w, ssmFailure{"RequestEntityTooLarge", "IAM Query requests are limited to128KiB."})
		return
	}
	input, err := url.ParseQuery(string(data))
	if err != nil {
		invalid("Malformed IAM Query body.")
		return
	}
	action := input.Get("Action")
	if input.Get("Version") != "2010-05-08" {
		invalid("Use IAM Version2010-05-08.")
		return
	}
	fields := map[string][]string{"CreateRole": {"RoleName", "AssumeRolePolicyDocument", "Description", "Path", "MaxSessionDuration"}, "GetRole": {"RoleName"}, "ListRoles": {"PathPrefix", "Marker", "MaxItems"}, "DeleteRole": {"RoleName"}, "PutRolePolicy": {"RoleName", "PolicyName", "PolicyDocument"}, "GetRolePolicy": {"RoleName", "PolicyName"}, "DeleteRolePolicy": {"RoleName", "PolicyName"}, "ListRolePolicies": {"RoleName", "Marker", "MaxItems"}, "UpdateAssumeRolePolicy": {"RoleName", "PolicyDocument"}}
	allowed, ok := fields[action]
	if !ok {
		iamError(w, ssmFailure{"NotImplemented", "Only the declared native role and inline-policy Query operations are implemented."})
		return
	}
	for name, values := range input {
		if len(values) != 1 {
			invalid("Duplicate IAM fields are unsupported.")
			return
		}
		supported := name == "Action" || name == "Version"
		for _, field := range allowed {
			supported = supported || name == field
		}
		if !supported {
			iamError(w, ssmFailure{"NotImplemented", "Unsupported IAM field: " + name})
			return
		}
	}
	name := input.Get("RoleName")
	if action != "ListRoles" && (!iamRoleName.MatchString(name) || strings.EqualFold(name, core.Administrator)) {
		invalid("Use the documented non-reserved root-path role-name subset.")
		return
	}
	resource := "*"
	var role IAMRole
	var readError error
	if name != "" {
		resource = "arn:aws:iam::000000000000:role/" + name
		if action != "CreateRole" {
			role, readError = g.Roles.Get(r.Context(), name)
			if readError == nil {
				resource = role.Arn
			}
		}
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	decision, err := g.Identities.DecisionContext(r.Context(), principal, "iam:"+action, resource)
	if err != nil {
		iamError(w, err)
		return
	}
	w.Header().Set("X-Simulator-IAM-Decision", decision)
	if decision != "allowed" {
		iamError(w, ssmFailure{"AccessDenied", "IAM evaluation: " + decision})
		return
	}
	if readError != nil {
		iamError(w, readError)
		return
	}
	max := 100
	if raw := input.Get("MaxItems"); raw != "" {
		max, err = strconv.Atoi(raw)
		if err != nil || max < 1 || max > 1000 {
			invalid("MaxItems must be1–1000.")
			return
		}
	}
	cursor := ""
	if marker := input.Get("Marker"); marker != "" {
		decoded, e := base64.RawURLEncoding.DecodeString(marker)
		if e != nil || len(decoded) > 140 {
			invalid("Invalid pagination marker.")
			return
		}
		cursor = string(decoded)
		if action == "ListRoles" && !iamRoleName.MatchString(cursor) || action == "ListRolePolicies" && !iamInlineName.MatchString(cursor) {
			invalid("Invalid pagination marker.")
			return
		}
	}
	switch action {
	case "CreateRole":
		if path := input.Get("Path"); path != "" && path != "/" {
			iamError(w, ssmFailure{"NotImplemented", "Only the root role path is supported."})
			return
		}
		duration := 3600
		if raw := input.Get("MaxSessionDuration"); raw != "" {
			duration, err = strconv.Atoi(raw)
			if err != nil {
				invalid("MaxSessionDuration must be an integer.")
				return
			}
		}
		role, err = g.Roles.Create(r.Context(), name, input.Get("AssumeRolePolicyDocument"), input.Get("Description"), duration)
		if err == nil {
			iamResponse(w, action, struct{ Role IAMRole }{iamWireRole(role)})
		}
	case "GetRole":
		iamResponse(w, action, struct{ Role IAMRole }{iamWireRole(role)})
	case "ListRoles":
		if prefix := input.Get("PathPrefix"); prefix != "" && prefix != "/" {
			iamError(w, ssmFailure{"NotImplemented", "Only root-path roles are supported."})
			return
		}
		var roles []IAMRole
		roles, err = g.Roles.List(r.Context(), cursor, max+1)
		if err == nil {
			truncated := len(roles) > max
			marker := ""
			if truncated {
				roles = roles[:max]
				marker = base64.RawURLEncoding.EncodeToString([]byte(roles[len(roles)-1].RoleName))
			}
			for i := range roles {
				roles[i] = iamWireRole(roles[i])
			}
			iamResponse(w, action, struct {
				Roles       []IAMRole `xml:"Roles>member"`
				IsTruncated bool
				Marker      string `xml:"Marker,omitempty"`
			}{roles, truncated, marker})
		}
	case "PutRolePolicy":
		err = g.Roles.PutPolicy(r.Context(), role.RoleName, input.Get("PolicyName"), input.Get("PolicyDocument"))
		if err == nil {
			iamResponse(w, action, nil)
		}
	case "GetRolePolicy":
		var document string
		document, err = g.Roles.Policy(r.Context(), role.RoleName, input.Get("PolicyName"))
		if err == nil {
			iamResponse(w, action, struct {
				RoleName       string
				PolicyName     string
				PolicyDocument string
			}{role.RoleName, input.Get("PolicyName"), iamEncoded(document)})
		}
	case "DeleteRolePolicy":
		err = g.Roles.DeletePolicy(r.Context(), role.RoleName, input.Get("PolicyName"))
		if err == nil {
			iamResponse(w, action, nil)
		}
	case "ListRolePolicies":
		var names []string
		names, err = g.Roles.PolicyNames(r.Context(), role.RoleName)
		if err == nil {
			filtered := []string{}
			for _, name := range names {
				if name > cursor {
					filtered = append(filtered, name)
				}
			}
			truncated := len(filtered) > max
			marker := ""
			if truncated {
				filtered = filtered[:max]
				marker = base64.RawURLEncoding.EncodeToString([]byte(filtered[len(filtered)-1]))
			}
			iamResponse(w, action, struct {
				Names       []string `xml:"PolicyNames>member"`
				IsTruncated bool
				Marker      string `xml:"Marker,omitempty"`
			}{filtered, truncated, marker})
		}
	case "UpdateAssumeRolePolicy":
		err = g.Roles.UpdateTrust(r.Context(), role.RoleName, input.Get("PolicyDocument"))
		if err == nil {
			iamResponse(w, action, nil)
		}
	case "DeleteRole":
		err = g.Roles.Delete(r.Context(), role.RoleName)
		if err == nil {
			iamResponse(w, action, nil)
		}
	}
	if err != nil {
		iamError(w, err)
	}
}
