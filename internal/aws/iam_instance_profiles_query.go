package aws

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

var iamInstanceProfileQueryFields = map[string][]string{
	"CreateInstanceProfile":         {"InstanceProfileName", "Path"},
	"GetInstanceProfile":            {"InstanceProfileName"},
	"ListInstanceProfiles":          {"PathPrefix", "Marker", "MaxItems"},
	"DeleteInstanceProfile":         {"InstanceProfileName"},
	"AddRoleToInstanceProfile":      {"InstanceProfileName", "RoleName"},
	"RemoveRoleFromInstanceProfile": {"InstanceProfileName", "RoleName"},
	"ListInstanceProfilesForRole":   {"RoleName", "Marker", "MaxItems"},
}

type iamInstanceProfileMarker struct {
	Action   string `json:"action"`
	RoleID   string `json:"roleId"`
	RoleName string `json:"roleName"`
	After    string `json:"after"`
}

func iamInstanceProfileDecodeMarker(raw string, want iamInstanceProfileMarker) (iamInstanceProfileMarker, error) {
	if raw == "" {
		return want, nil
	}
	invalid := ssmFailure{"InvalidInput", "Invalid or mismatched instance profile pagination marker."}
	if len(raw) > 1024 {
		return want, invalid
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(b) != raw {
		return want, invalid
	}
	var m iamInstanceProfileMarker
	if iamStrictJSON(b, &m) != nil {
		return want, invalid
	}
	canonical, _ := json.Marshal(m)
	if !bytes.Equal(b, canonical) || m.Action != want.Action || m.RoleID != want.RoleID || m.RoleName != want.RoleName || !iamInlineName.MatchString(m.After) || m.After != strings.ToLower(m.After) {
		return want, invalid
	}
	return m, nil
}
func iamInstanceProfileEncodeMarker(m iamInstanceProfileMarker) string {
	b, _ := json.Marshal(m)
	return base64.RawURLEncoding.EncodeToString(b)
}
func iamWireInstanceProfile(p IAMInstanceProfile) IAMInstanceProfile {
	for i := range p.Roles {
		p.Roles[i] = iamWireRole(p.Roles[i])
	}
	return p
}
func (g *Gateway) serveIAMInstanceProfiles(w http.ResponseWriter, r *http.Request, action string, input url.Values) {
	fail := func(err error) { iamError(w, err) }
	invalid := func(message string) { fail(ssmFailure{"ValidationError", message}) }
	name, roleName := input.Get("InstanceProfileName"), input.Get("RoleName")
	list := action == "ListInstanceProfiles" || action == "ListInstanceProfilesForRole"
	if !list && !iamInlineName.MatchString(name) {
		invalid("Use an instance profile name of 1–128 IAM name characters.")
		return
	}
	needsRole := action == "AddRoleToInstanceProfile" || action == "RemoveRoleFromInstanceProfile" || action == "ListInstanceProfilesForRole"
	if needsRole && (!iamRoleName.MatchString(roleName) || strings.EqualFold(roleName, core.Administrator)) {
		invalid("Use a native non-reserved root-path role name.")
		return
	}
	for _, field := range []string{"Path", "PathPrefix"} {
		if value, ok := input[field]; ok && value[0] != "/" {
			fail(ssmFailure{"NotImplemented", "Only the root instance profile path is supported."})
			return
		}
	}
	max := 100
	if raw, ok := input["MaxItems"]; ok {
		var err error
		max, err = strconv.Atoi(raw[0])
		if err != nil || max < 1 || max > 1000 {
			invalid("MaxItems must be 1–1000.")
			return
		}
	}
	var p IAMInstanceProfile
	var role IAMRole
	var profileErr, roleErr error
	resource := "*"
	if name != "" {
		resource = iamInstanceProfileARN + name
		p, profileErr = g.Roles.GetInstanceProfile(r.Context(), name)
		if profileErr == nil && action != "CreateInstanceProfile" {
			resource = p.Arn
		}
		if action == "CreateInstanceProfile" && iamProfileErrorCode(profileErr) == "NoSuchEntity" {
			profileErr = nil
		}
	}
	if needsRole {
		role, roleErr = g.Roles.Get(r.Context(), roleName)
	}
	if action == "ListInstanceProfilesForRole" {
		resource = "arn:aws:iam::000000000000:role/" + roleName
		if roleErr == nil {
			resource = role.Arn
		}
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	allow := func(action, resource string) bool {
		decision, err := g.sdkDecision(r, principal, action, resource)
		if err != nil {
			fail(err)
			return false
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			fail(ssmFailure{"AccessDenied", "IAM evaluation: " + decision})
			return false
		}
		return true
	}
	if !allow("iam:"+action, resource) {
		return
	}
	if profileErr != nil {
		fail(profileErr)
		return
	}
	if roleErr != nil {
		fail(roleErr)
		return
	}
	if action == "AddRoleToInstanceProfile" && !allow("iam:PassRole", role.Arn) {
		return
	}
	var err error
	switch action {
	case "CreateInstanceProfile":
		p, err = g.Roles.CreateInstanceProfile(r.Context(), name)
		if err == nil {
			iamResponse(w, action, struct{ InstanceProfile IAMInstanceProfile }{iamWireInstanceProfile(p)})
		}
	case "GetInstanceProfile":
		p, err = g.Roles.GetInstanceProfile(r.Context(), p.InstanceProfileName, p.InstanceProfileID)
		if err == nil {
			iamResponse(w, action, struct{ InstanceProfile IAMInstanceProfile }{iamWireInstanceProfile(p)})
		}
	case "DeleteInstanceProfile":
		err = g.Roles.DeleteInstanceProfile(r.Context(), p.InstanceProfileName, p.InstanceProfileID)
		if err == nil {
			iamResponse(w, action, nil)
		}
	case "AddRoleToInstanceProfile", "RemoveRoleFromInstanceProfile":
		err = g.Roles.ChangeInstanceProfileRole(r.Context(), p.InstanceProfileName, p.InstanceProfileID, role.RoleName, role.RoleID, action == "AddRoleToInstanceProfile")
		if err == nil {
			iamResponse(w, action, nil)
		}
	case "ListInstanceProfiles", "ListInstanceProfilesForRole":
		marker := iamInstanceProfileMarker{Action: action, RoleID: role.RoleID, RoleName: role.RoleName}
		marker, err = iamInstanceProfileDecodeMarker(input.Get("Marker"), marker)
		if err != nil {
			break
		}
		var profiles []IAMInstanceProfile
		profiles, err = g.Roles.ListInstanceProfiles(r.Context(), marker.After, max+1, role.RoleName, role.RoleID)
		if err != nil {
			break
		}
		truncated := len(profiles) > max
		next := ""
		if truncated {
			profiles = profiles[:max]
			marker.After = strings.ToLower(profiles[len(profiles)-1].InstanceProfileName)
			next = iamInstanceProfileEncodeMarker(marker)
		}
		for i := range profiles {
			profiles[i] = iamWireInstanceProfile(profiles[i])
		}
		iamResponse(w, action, struct {
			Profiles    []IAMInstanceProfile `xml:"InstanceProfiles>member"`
			IsTruncated bool
			Marker      string `xml:"Marker,omitempty"`
		}{profiles, truncated, next})
	}
	if err != nil {
		fail(err)
	}
}
