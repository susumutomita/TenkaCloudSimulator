package aws

import (
	"context"
	"encoding/xml"
	"fmt"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const stsNamespace = "https://sts.amazonaws.com/doc/2011-06-15/"

var stsSessionName = regexp.MustCompile(`^[A-Za-z0-9_+=,.@-]{2,64}$`)
var stsExternalID = regexp.MustCompile(`^[A-Za-z0-9_+=,.@:/-]+$`)

func isSTS(r *http.Request) bool {
	return r.Header.Get("X-Simulator-Service") == "sts" || strings.Contains(r.Header.Get("Authorization"), "/sts/aws4_request")
}
func stsError(w http.ResponseWriter, status int, code, message string) {
	type detail struct{ Type, Code, Message string }
	writeXML(w, status, struct {
		XMLName   xml.Name `xml:"ErrorResponse"`
		Namespace string   `xml:"xmlns,attr"`
		Error     detail
		RequestID string `xml:"RequestId"`
	}{Namespace: stsNamespace, Error: detail{"Sender", code, message}, RequestID: fmt.Sprintf("sts-local-%016x", sequence.Add(1))})
}
func stsResponse(w http.ResponseWriter, action string, result any) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(200)
	_, _ = io.WriteString(w, xml.Header)
	e := xml.NewEncoder(w)
	root := xml.StartElement{Name: xml.Name{Local: action + "Response"}, Attr: []xml.Attr{{Name: xml.Name{Local: "xmlns"}, Value: stsNamespace}}}
	_ = e.EncodeToken(root)
	_ = e.EncodeElement(result, xml.StartElement{Name: xml.Name{Local: action + "Result"}})
	_ = e.EncodeElement(struct {
		RequestID string `xml:"RequestId"`
	}{fmt.Sprintf("sts-local-%016x", sequence.Add(1))}, xml.StartElement{Name: xml.Name{Local: "ResponseMetadata"}})
	_ = e.EncodeToken(root.End())
	_ = e.Flush()
}
func (g *Gateway) serveSTS(w http.ResponseWriter, r *http.Request) {
	invalid := func(message string) { stsError(w, 400, "ValidationError", message) }
	denied := func() { stsError(w, 403, "AccessDenied", "The caller is not authorized to assume this role.") }
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" {
		invalid("Use root POST with STS Query parameters.")
		return
	}
	caller, ok := SDKIdentityFromContext(r.Context())
	authority, hasAuthority := STSAuthorityFromContext(r.Context())
	if !ok || !hasAuthority || authority == nil {
		stsError(w, 403, "InvalidClientTokenId", "Authenticated simulator SDK credentials are required.")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil {
		invalid("STS Query body exceeds 32KiB.")
		return
	}
	q, err := url.ParseQuery(string(data))
	if err != nil {
		invalid("Malformed STS Query body.")
		return
	}
	for _, v := range q {
		if len(v) != 1 {
			invalid("Duplicate STS Query fields are unsupported.")
			return
		}
	}
	if q.Get("Version") != "2011-06-15" {
		invalid("Use STS Version 2011-06-15.")
		return
	}
	action := q.Get("Action")
	if action != "AssumeRole" && action != "GetCallerIdentity" {
		stsError(w, 501, "NotImplemented", "Only GetCallerIdentity and AssumeRole are implemented.")
		return
	}
	for field := range q {
		supported := field == "Action" || field == "Version"
		if action == "AssumeRole" {
			supported = supported || field == "RoleArn" || field == "RoleSessionName" || field == "DurationSeconds" || field == "ExternalId"
		}
		if !supported {
			invalid("Unsupported STS Query field.")
			return
		}
	}
	if action == "GetCallerIdentity" {
		stsResponse(w, action, struct {
			UserID       string `xml:"UserId"`
			Account, Arn string
		}{caller.UserID, caller.Account, caller.ARN})
		return
	}
	if !stsSessionName.MatchString(q.Get("RoleSessionName")) {
		invalid("RoleSessionName must contain 2–64 supported characters.")
		return
	}
	if ext, exists := q["ExternalId"]; exists && !stsValidExternalID(ext[0]) {
		invalid("ExternalId must contain 2–1224 supported characters.")
		return
	}
	duration := 3600
	if value, exists := q["DurationSeconds"]; exists {
		duration, err = strconv.Atoi(value[0])
		if err != nil || duration < 900 || duration > 43200 {
			invalid("DurationSeconds must be an integer from 900 to 43200.")
			return
		}
	}
	role, err := g.stsRole(r.Context(), q.Get("RoleArn"))
	if err != nil {
		denied()
		return
	}
	if duration > role.MaxSessionDuration || (!caller.Expires.IsZero() && duration > 3600) {
		invalid("Duration exceeds the role or role-chaining limit.")
		return
	}
	if g.Identities == nil {
		denied()
		return
	}
	decision, err := g.sdkDecision(r, caller.Principal, "sts:AssumeRole", role.Arn)
	if err != nil || decision != "allowed" {
		denied()
		return
	}
	allowed, err := stsTrustAllows(role.AssumeRolePolicyDocument, caller, q.Get("ExternalId"))
	if err != nil {
		invalid("Unsupported or invalid role trust policy.")
		return
	}
	if !allowed {
		denied()
		return
	}
	credentials, err := authority.Issue(STSSessionRequest{Principal: role.RoleName, RoleARN: role.Arn, RoleID: role.RoleID, SessionName: q.Get("RoleSessionName"), Duration: time.Duration(duration) * time.Second, Snapshot: func(ctx context.Context) (core.Policy, error) { return g.stsSessionPolicy(ctx, role) }, Validate: func(ctx context.Context) error {
		current, err := g.stsRole(ctx, role.Arn)
		if err != nil {
			return err
		}
		if current.RoleID != role.RoleID {
			return fmt.Errorf("role generation changed")
		}
		return nil
	}})
	if err != nil {
		denied()
		return
	}
	type credentialXML struct {
		AccessKeyID                   string `xml:"AccessKeyId"`
		SecretAccessKey, SessionToken string
		Expiration                    time.Time
	}
	type userXML struct {
		AssumedRoleID string `xml:"AssumedRoleId"`
		Arn           string
	}
	stsResponse(w, action, struct {
		Credentials     credentialXML
		AssumedRoleUser userXML
	}{credentialXML{credentials.AccessKeyID, credentials.SecretAccessKey, credentials.SessionToken, credentials.Expiration}, userXML{role.RoleID + ":" + q.Get("RoleSessionName"), "arn:aws:sts::000000000000:assumed-role/" + role.RoleName + "/" + q.Get("RoleSessionName")}})
}

func stsValidExternalID(s string) bool {
	return len(s) >= 2 && len(s) <= 1224 && stsExternalID.MatchString(s)
}
