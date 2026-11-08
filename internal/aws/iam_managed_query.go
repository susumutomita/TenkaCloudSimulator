package aws

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// These operations enter only after serveIAM's shared transport, credentials,
// Version and exact single-valued field checks. Policy requests have no RoleName.
var iamManagedQueryFields = map[string][]string{
	"CreatePolicy": {"PolicyName", "PolicyDocument", "Path", "Description"},
	"GetPolicy":    {"PolicyArn"}, "ListPolicies": {"Scope", "OnlyAttached", "PathPrefix", "PolicyUsageFilter", "Marker", "MaxItems"},
	"DeletePolicy": {"PolicyArn"}, "CreatePolicyVersion": {"PolicyArn", "PolicyDocument", "SetAsDefault"},
	"GetPolicyVersion": {"PolicyArn", "VersionId"}, "ListPolicyVersions": {"PolicyArn", "Marker", "MaxItems"},
	"SetDefaultPolicyVersion": {"PolicyArn", "VersionId"}, "DeletePolicyVersion": {"PolicyArn", "VersionId"},
	"AttachRolePolicy": {"RoleName", "PolicyArn"}, "DetachRolePolicy": {"RoleName", "PolicyArn"},
	"ListAttachedRolePolicies": {"RoleName", "PathPrefix", "Marker", "MaxItems"},
}
var iamManagedQueryVersionID = regexp.MustCompile(`^v[1-9][0-9]*(\.[A-Za-z0-9-]*)?$`)
var iamManagedQueryCursorVersion = regexp.MustCompile(`^v[1-9][0-9]*$`)

const iamManagedQueryPolicyPrefix = "arn:aws:iam::000000000000:policy/"

// Markers are canonical bounded cursors, not authentication or capabilities.
// Context and current parent generation are rechecked; no snapshot consistency
// or cryptographic tamper-resistance is claimed for this local subset.
type iamManagedQueryMarker struct {
	Action    string `json:"action"`
	Scope     string `json:"scope"`
	Prefix    string `json:"prefix"`
	ParentARN string `json:"parentArn"`
	ParentID  string `json:"parentId"`
	After     string `json:"after"`
}

func iamManagedQueryDecodeMarker(raw string, expected iamManagedQueryMarker) (iamManagedQueryMarker, error) {
	if raw == "" {
		return expected, nil
	}
	invalid := ssmFailure{"InvalidInput", "Invalid or mismatched managed-policy pagination marker."}
	if len(raw) > 2048 {
		return expected, invalid
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) > 1536 {
		return expected, invalid
	}
	var marker iamManagedQueryMarker
	if iamStrictJSON(data, &marker) != nil {
		return expected, invalid
	}
	canonical, err := json.Marshal(marker)
	if err != nil || !bytes.Equal(data, canonical) {
		return expected, invalid
	}
	if expected.ParentARN == "" && marker.ParentID != "" {
		return expected, invalid
	}
	if marker.Action != expected.Action || marker.Scope != expected.Scope || marker.Prefix != expected.Prefix || marker.ParentARN != expected.ParentARN || marker.ParentID == "" && expected.ParentARN != "" || marker.After == "" {
		return expected, invalid
	}
	if expected.Action == "ListPolicyVersions" {
		if !iamManagedQueryCursorVersion.MatchString(marker.After) {
			return expected, invalid
		}
	} else if !iamInlineName.MatchString(marker.After) || marker.After != strings.ToLower(marker.After) {
		return expected, invalid
	}
	return marker, nil
}
func iamManagedQueryEncodeMarker(marker iamManagedQueryMarker) string {
	data, _ := json.Marshal(marker)
	return base64.RawURLEncoding.EncodeToString(data)
}
func iamManagedQueryARN(raw string) bool {
	return strings.HasPrefix(raw, iamManagedQueryPolicyPrefix) && iamInlineName.MatchString(strings.TrimPrefix(raw, iamManagedQueryPolicyPrefix))
}
func iamManagedQueryVersionLess(a, b string) bool {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func (g *Gateway) serveIAMManaged(w http.ResponseWriter, r *http.Request, action string, input url.Values) {
	fail := func(code, message string) { iamError(w, ssmFailure{code, message}) }
	invalid := func(message string) { fail("InvalidInput", message) }
	name, arn := input.Get("RoleName"), input.Get("PolicyArn")
	if action == "CreatePolicy" {
		if !iamInlineName.MatchString(input.Get("PolicyName")) || input.Get("PolicyDocument") == "" {
			invalid("PolicyName and PolicyDocument are required.")
			return
		}
		if path := input.Get("Path"); path != "" && path != "/" {
			fail("NotImplemented", "Only root-path customer managed policies are supported.")
			return
		}
		arn = iamManagedQueryPolicyPrefix + input.Get("PolicyName")
	} else if action != "ListPolicies" && action != "ListAttachedRolePolicies" && !iamManagedQueryARN(arn) {
		invalid("Use an exact root-path local customer policy ARN.")
		return
	}
	if action == "CreatePolicyVersion" && input.Get("PolicyDocument") == "" {
		invalid("PolicyDocument is required.")
		return
	}
	if action == "GetPolicyVersion" || action == "SetDefaultPolicyVersion" || action == "DeletePolicyVersion" {
		if !iamManagedQueryVersionID.MatchString(input.Get("VersionId")) {
			invalid("Use a policy VersionId.")
			return
		}
	}
	if action == "ListPolicies" {
		if input.Get("Scope") != "Local" {
			fail("NotImplemented", "ListPolicies requires Scope=Local; AWS and combined catalogs are unavailable.")
			return
		}
		if _, present := input["OnlyAttached"]; present {
			fail("NotImplemented", "OnlyAttached filtering is unavailable.")
			return
		}
		if _, present := input["PolicyUsageFilter"]; present {
			fail("NotImplemented", "PolicyUsageFilter is unavailable.")
			return
		}
	}
	if action == "ListPolicies" || action == "ListAttachedRolePolicies" {
		if prefix := input.Get("PathPrefix"); prefix != "" && prefix != "/" {
			fail("NotImplemented", "Only the root PathPrefix is supported.")
			return
		}
	}
	setDefault := false
	if action == "CreatePolicyVersion" {
		if raw, present := input["SetAsDefault"]; present {
			if raw[0] != "true" && raw[0] != "false" {
				invalid("SetAsDefault must be true or false.")
				return
			}
			setDefault = raw[0] == "true"
		}
	}
	resource := arn
	var readError error
	var role IAMRole
	if action == "ListPolicies" {
		resource = "*"
	}
	if action == "AttachRolePolicy" || action == "DetachRolePolicy" || action == "ListAttachedRolePolicies" {
		if !iamRoleName.MatchString(name) || strings.EqualFold(name, core.Administrator) {
			invalid("Use a non-reserved native role name.")
			return
		}
		resource = "arn:aws:iam::000000000000:role/" + name
		role, readError = g.Roles.Get(r.Context(), name)
		if readError == nil {
			resource = role.Arn
		}
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	decision, err := g.sdkDecision(r, principal, "iam:"+action, resource)
	if err != nil {
		iamError(w, err)
		return
	}
	w.Header().Set("X-Simulator-IAM-Decision", decision)
	if decision != "allowed" {
		fail("AccessDenied", "IAM evaluation: "+decision)
		return
	}
	if readError != nil {
		iamError(w, readError)
		return
	}
	max := 100
	if raw, present := input["MaxItems"]; present {
		max, err = strconv.Atoi(raw[0])
		if err != nil || max < 1 || max > 1000 {
			invalid("MaxItems must be1–1000.")
			return
		}
	}
	markerContext := iamManagedQueryMarker{Action: action, Scope: input.Get("Scope"), Prefix: "/"}
	if action == "ListPolicyVersions" {
		markerContext.ParentARN = arn
	}
	if action == "ListAttachedRolePolicies" {
		markerContext.ParentARN = role.Arn
	}
	marker, err := iamManagedQueryDecodeMarker(input.Get("Marker"), markerContext)
	if err != nil {
		iamError(w, err)
		return
	}
	switch action {
	case "CreatePolicy":
		var policy IAMManagedPolicy
		policy, err = g.Roles.CreateManagedPolicy(r.Context(), input.Get("PolicyName"), "/", input.Get("Description"), input.Get("PolicyDocument"))
		if err == nil {
			iamResponse(w, action, struct{ Policy IAMManagedPolicy }{policy})
		}
	case "GetPolicy":
		var policy IAMManagedPolicy
		policy, err = g.Roles.ManagedPolicy(r.Context(), arn)
		if err == nil {
			iamResponse(w, action, struct{ Policy IAMManagedPolicy }{policy})
		}
	case "DeletePolicy":
		err = g.Roles.DeleteManagedPolicy(r.Context(), arn)
		if err == nil {
			iamResponse(w, action, nil)
		}
	case "CreatePolicyVersion":
		var version IAMManagedPolicyVersion
		version, err = g.Roles.CreateManagedPolicyVersion(r.Context(), arn, input.Get("PolicyDocument"), setDefault)
		if err == nil {
			version.Document = ""
			iamResponse(w, action, struct{ PolicyVersion IAMManagedPolicyVersion }{version})
		}
	case "GetPolicyVersion":
		var version IAMManagedPolicyVersion
		version, err = g.Roles.ManagedPolicyVersion(r.Context(), arn, input.Get("VersionId"))
		if err == nil {
			version.Document = iamEncoded(version.Document)
			iamResponse(w, action, struct{ PolicyVersion IAMManagedPolicyVersion }{version})
		}
	case "SetDefaultPolicyVersion":
		err = g.Roles.SetDefaultManagedPolicyVersion(r.Context(), arn, input.Get("VersionId"))
		if err == nil {
			iamResponse(w, action, nil)
		}
	case "DeletePolicyVersion":
		err = g.Roles.DeleteManagedPolicyVersion(r.Context(), arn, input.Get("VersionId"))
		if err == nil {
			iamResponse(w, action, nil)
		}
	case "AttachRolePolicy":
		err = g.Roles.AttachRolePolicy(r.Context(), role.RoleName, arn, role.RoleID)
		if err == nil {
			iamResponse(w, action, nil)
		}
	case "DetachRolePolicy":
		err = g.Roles.DetachRolePolicy(r.Context(), role.RoleName, arn, role.RoleID)
		if err == nil {
			iamResponse(w, action, nil)
		}
	case "ListPolicies":
		var policies []IAMManagedPolicy
		policies, err = g.Roles.ManagedPolicies(r.Context(), marker.After, max+1)
		if err == nil {
			truncated := len(policies) > max
			next := ""
			if truncated {
				policies = policies[:max]
				marker.After = strings.ToLower(policies[len(policies)-1].PolicyName)
				next = iamManagedQueryEncodeMarker(marker)
			}
			for i := range policies {
				policies[i].Description = ""
			}
			iamResponse(w, action, struct {
				Policies    []IAMManagedPolicy `xml:"Policies>member"`
				IsTruncated bool
				Marker      string `xml:"Marker,omitempty"`
			}{policies, truncated, next})
		}
	case "ListPolicyVersions":
		var parent IAMManagedPolicy
		var versions []IAMManagedPolicyVersion
		parent, versions, err = g.Roles.ManagedPolicyVersions(r.Context(), arn)
		if err != nil {
			break
		}
		if marker.ParentID != "" && marker.ParentID != parent.PolicyID {
			invalid("Pagination marker refers to a different policy generation.")
			return
		}
		marker.ParentID = parent.PolicyID
		sort.Slice(versions, func(i, j int) bool { return iamManagedQueryVersionLess(versions[i].VersionID, versions[j].VersionID) })
		page := []IAMManagedPolicyVersion{}
		for _, version := range versions {
			if marker.After == "" || iamManagedQueryVersionLess(marker.After, version.VersionID) {
				version.Document = ""
				page = append(page, version)
			}
		}
		truncated := len(page) > max
		next := ""
		if truncated {
			page = page[:max]
			marker.After = page[len(page)-1].VersionID
			next = iamManagedQueryEncodeMarker(marker)
		}
		iamResponse(w, action, struct {
			Versions    []IAMManagedPolicyVersion `xml:"Versions>member"`
			IsTruncated bool
			Marker      string `xml:"Marker,omitempty"`
		}{page, truncated, next})
	case "ListAttachedRolePolicies":
		var parent IAMRole
		var policies []IAMAttachedPolicy
		parent, policies, err = g.Roles.AttachedRolePolicies(r.Context(), role.RoleName, marker.After, max+1, role.RoleID)
		if err != nil {
			break
		}
		if marker.ParentID != "" && marker.ParentID != parent.RoleID {
			invalid("Pagination marker refers to a different role generation.")
			return
		}
		marker.ParentID = parent.RoleID
		truncated := len(policies) > max
		next := ""
		if truncated {
			policies = policies[:max]
			marker.After = strings.ToLower(policies[len(policies)-1].PolicyName)
			next = iamManagedQueryEncodeMarker(marker)
		}
		iamResponse(w, action, struct {
			Policies    []IAMAttachedPolicy `xml:"AttachedPolicies>member"`
			IsTruncated bool
			Marker      string `xml:"Marker,omitempty"`
		}{policies, truncated, next})
	}
	if err != nil {
		iamError(w, err)
	}
}
