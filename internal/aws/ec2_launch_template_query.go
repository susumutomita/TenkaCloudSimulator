package aws

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

var ec2LaunchTemplateOperations = []string{"CreateLaunchTemplate", "DescribeLaunchTemplates", "DescribeLaunchTemplateVersions", "DeleteLaunchTemplate"}
var ec2LaunchTemplateName = regexp.MustCompile(`^[A-Za-z0-9()./_-]{3,128}$`)
var ec2LaunchTemplateID = regexp.MustCompile(`^lt-[0-9a-f]{17}$`)

type ec2LaunchTemplateQuery struct {
	name, id, token, description string
	ids, names, groups, versions []string
}

func parseEC2LaunchTemplateQuery(action string, fields url.Values) (ec2LaunchTemplateQuery, error) {
	q := ec2LaunchTemplateQuery{}
	allowed := map[string]bool{"Action": true, "Version": true, "DryRun": true}
	lists := map[string]*[]string{}
	switch action {
	case "CreateLaunchTemplate":
		for _, k := range []string{"LaunchTemplateName", "ClientToken", "VersionDescription"} {
			allowed[k] = true
		}
		lists["LaunchTemplateData.SecurityGroupId."] = &q.groups
	case "DescribeLaunchTemplates":
		lists["LaunchTemplateId."] = &q.ids
		lists["LaunchTemplateName."] = &q.names
	case "DescribeLaunchTemplateVersions":
		allowed["LaunchTemplateId"] = true
		allowed["LaunchTemplateName"] = true
		lists["LaunchTemplateVersion."] = &q.versions
	case "DeleteLaunchTemplate":
		allowed["LaunchTemplateId"] = true
		allowed["LaunchTemplateName"] = true
	}
	for key, values := range fields {
		if len(values) != 1 || values[0] == "" {
			return q, ec2Invalid("InvalidParameterValue", "Empty or repeated Query fields are unsupported.")
		}
		if allowed[key] {
			continue
		}
		supported := false
		for prefix := range lists {
			if strings.HasPrefix(key, prefix) {
				suffix := strings.TrimPrefix(key, prefix)
				n, e := strconv.Atoi(suffix)
				if e != nil || n < 1 || n > 20 || strconv.Itoa(n) != suffix {
					return q, ec2Invalid("InvalidParameterValue", "Malformed list index.")
				}
				supported = true
			}
		}
		if !supported {
			return q, ec2Invalid("UnsupportedOperation", "This Query field is outside the supported launch-template profile.")
		}
	}
	if d := fields.Get("DryRun"); d != "" && d != "false" {
		return q, ec2Invalid("UnsupportedOperation", "DryRun is unsupported.")
	}
	for prefix, destination := range lists {
		count := 0
		for key := range fields {
			if strings.HasPrefix(key, prefix) {
				count++
			}
		}
		seen := map[string]bool{}
		for i := 1; i <= count; i++ {
			v := fields.Get(prefix + strconv.Itoa(i))
			if v == "" || seen[v] {
				return q, ec2Invalid("InvalidParameterValue", "List entries must be contiguous and unique.")
			}
			seen[v] = true
			*destination = append(*destination, v)
		}
	}
	q.name = fields.Get("LaunchTemplateName")
	q.id = fields.Get("LaunchTemplateId")
	q.token = fields.Get("ClientToken")
	q.description = fields.Get("VersionDescription")
	if q.name != "" && !ec2LaunchTemplateName.MatchString(q.name) {
		return q, ec2Invalid("InvalidParameterValue", "Invalid launch-template name.")
	}
	for _, name := range q.names {
		if !ec2LaunchTemplateName.MatchString(name) {
			return q, ec2Invalid("InvalidParameterValue", "Invalid launch-template name.")
		}
	}
	for _, id := range append(slices.Clone(q.ids), q.id) {
		if id != "" && !ec2LaunchTemplateID.MatchString(id) {
			return q, ec2Invalid("InvalidLaunchTemplateId.Malformed", "Invalid launch-template ID.")
		}
	}
	switch action {
	case "CreateLaunchTemplate":
		if q.name == "" || len(q.groups) < 1 || len(q.groups) > 5 {
			return q, ec2Invalid("InvalidParameterValue", "Supply a name and one to five native security groups.")
		}
		for _, id := range q.groups {
			if !regexp.MustCompile(`^sg-[0-9a-f]{8,17}$`).MatchString(id) {
				return q, ec2Invalid("InvalidParameterValue", "Invalid security-group ID.")
			}
		}
		if len(q.token) > 128 || len(q.description) > 255 {
			return q, ec2Invalid("InvalidParameterValue", "Token or description exceeds the supported bound.")
		}
		for _, value := range []string{q.token, q.description} {
			for _, r := range value {
				if r < 32 || r > 126 {
					return q, ec2Invalid("InvalidParameterValue", "Use printable ASCII token and description.")
				}
			}
		}
		sort.Strings(q.groups)
	case "DescribeLaunchTemplates":
		if len(q.ids) > 0 && len(q.names) > 0 {
			return q, ec2Invalid("InvalidParameterValue", "Choose IDs or names, not both.")
		}
	case "DescribeLaunchTemplateVersions", "DeleteLaunchTemplate":
		if (q.name == "") == (q.id == "") {
			return q, ec2Invalid("InvalidParameterValue", "Supply exactly one template selector.")
		}
		if len(q.versions) > 1 {
			return q, ec2Invalid("UnsupportedOperation", "Only one immutable version is supported.")
		}
		for _, v := range q.versions {
			if v != "1" && v != "$Default" && v != "$Latest" {
				return q, ec2Invalid("InvalidParameterValue", "Only immutable version 1 is supported.")
			}
		}
	}
	return q, nil
}

type ec2LaunchTemplateWire struct {
	ID      string `xml:"launchTemplateId"`
	Name    string `xml:"launchTemplateName"`
	Created string `xml:"createTime"`
	Default int    `xml:"defaultVersionNumber"`
	Latest  int    `xml:"latestVersionNumber"`
}
type ec2LaunchTemplateVersionWire struct {
	ID          string   `xml:"launchTemplateId"`
	Name        string   `xml:"launchTemplateName"`
	Number      int      `xml:"versionNumber"`
	Description string   `xml:"versionDescription,omitempty"`
	Created     string   `xml:"createTime"`
	Default     bool     `xml:"defaultVersion"`
	Groups      []string `xml:"launchTemplateData>securityGroupIdSet>item"`
}
type ec2LaunchTemplateResponse struct {
	XMLName   xml.Name
	XMLNS     string                          `xml:"xmlns,attr"`
	RequestID string                          `xml:"requestId"`
	Template  *ec2LaunchTemplateWire          `xml:"launchTemplate,omitempty"`
	Templates *[]ec2LaunchTemplateWire        `xml:"launchTemplates>item,omitempty"`
	Versions  *[]ec2LaunchTemplateVersionWire `xml:"launchTemplateVersionSet>item,omitempty"`
}

func launchTemplateWire(v ec2LaunchTemplate) ec2LaunchTemplateWire {
	return ec2LaunchTemplateWire{v.ID, v.Name, v.Created, 1, 1}
}

func (g *Gateway) serveEC2LaunchTemplate(w http.ResponseWriter, r *http.Request, action, region string, fields url.Values) {
	fail := func(err error) {
		var f *ec2Failure
		if errors.As(err, &f) {
			ec2Error(w, f.code, f.message, f.status)
		} else {
			ec2Error(w, "InternalError", "Launch-template state is unavailable.", 500)
		}
	}
	q, err := parseEC2LaunchTemplateQuery(action, fields)
	if err != nil {
		fail(err)
		return
	}
	if g.Stacks == nil || g.Identities == nil {
		fail(fmt.Errorf("stores unavailable"))
		return
	}
	if _, scoped := r.Context().Value(consoleInstanceBoundaryKey{}).(consoleInstanceBoundary); scoped {
		ec2Error(w, "UnauthorizedOperation", "This console session is limited to its own instance.", 403)
		return
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	decision, err := g.Identities.DecisionSnapshot(r.Context(), principal)
	if err != nil || decision == nil {
		fail(fmt.Errorf("identity snapshot unavailable"))
		return
	}
	authorize := func(id string) error {
		target := "*"
		if action == "CreateLaunchTemplate" || action == "DeleteLaunchTemplate" {
			target = networkARN(region, "launch-template", id)
		}
		if decision("ec2:"+action, target) != "allowed" {
			return &ec2Failure{"UnauthorizedOperation", "You are not authorized for this launch-template operation.", 403}
		}
		return nil
	}
	// Describe permissions are explicitly wildcard-only in the EC2 SAR.
	// Mutations check the generated/resolved physical ARN using this snapshot.
	if action == "DescribeLaunchTemplates" || action == "DescribeLaunchTemplateVersions" || q.id != "" {
		id := q.id
		if id == "" {
			id = "*"
		}
		if err = authorize(id); err != nil {
			fail(err)
			return
		}
	}
	out := ec2LaunchTemplateResponse{XMLName: xml.Name{Local: action + "Response"}, XMLNS: "http://ec2.amazonaws.com/doc/2016-11-15/", RequestID: fmt.Sprintf("ec2-local-%016x", sequence.Add(1))}
	switch action {
	case "CreateLaunchTemplate":
		raw, _ := json.Marshal(struct {
			Name, Description string
			Groups            []string
		}{q.name, q.description, q.groups})
		digest := sha256.Sum256(raw)
		saved, e := g.Stacks.createEC2LaunchTemplate(r.Context(), ec2LaunchTemplate{Name: q.name, Region: region, Creator: principal, Description: q.description, SecurityGroups: q.groups}, q.token, hex.EncodeToString(digest[:]), authorize)
		if e != nil {
			fail(e)
			return
		}
		value := launchTemplateWire(saved)
		out.Template = &value
	case "DeleteLaunchTemplate":
		saved, e := g.Stacks.deleteEC2LaunchTemplate(r.Context(), region, q.id, q.name, authorize)
		if e != nil {
			fail(e)
			return
		}
		value := launchTemplateWire(saved)
		out.Template = &value
	default:
		ids, names := q.ids, q.names
		if action == "DescribeLaunchTemplateVersions" {
			if q.id != "" {
				ids = []string{q.id}
			} else {
				names = []string{q.name}
			}
		}
		saved, e := g.Stacks.selectEC2LaunchTemplates(r.Context(), region, ids, names)
		if e != nil {
			fail(e)
			return
		}
		if action == "DescribeLaunchTemplates" {
			values := make([]ec2LaunchTemplateWire, 0, len(saved))
			for _, v := range saved {
				values = append(values, launchTemplateWire(v))
			}
			out.Templates = &values
		} else {
			v := saved[0]
			values := []ec2LaunchTemplateVersionWire{{v.ID, v.Name, 1, v.Description, v.Created, true, v.SecurityGroups}}
			out.Versions = &values
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	_ = xml.NewEncoder(w).Encode(out)
}
