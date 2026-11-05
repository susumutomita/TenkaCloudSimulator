package aws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"net/url"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Mutate an actually issued cursor, not a duplicate of the admission predicate.
// Every refusal must preserve the complete persisted authority frame; the
// unmodified cursor remains usable afterward.
func TestIAMManagedAdmissionIssuedCursorRejectsWrongContextAndShape(t *testing.T) {
	p := newManagedQueryProbe(t)
	p.create("AdmissionAlpha")
	p.create("AdmissionBeta")
	body := p.call("ListPolicies", "", url.Values{"Scope": {"Local"}, "MaxItems": {"1"}}, 200, "<IsTruncated>true</IsTruncated>")
	issued := queryMarker(t, body, "ListPoliciesResult")
	raw, err := base64.RawURLEncoding.DecodeString(issued)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	encode := func(key string, value any) string {
		// Preserve the actual wire field ordering except the deliberately malformed
		// value: marshaling a map would itself change the canonical envelope.
		data := string(raw)
		old, _ := json.Marshal(fields[key])
		replacement, _ := json.Marshal(value)
		if _, exists := fields[key]; !exists {
			return base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSuffix(data, "}") + `,"` + key + `":` + string(replacement) + `}`))
		}
		data = strings.Replace(data, `"`+key+`":`+string(old), `"`+key+`":`+string(replacement), 1)
		return base64.RawURLEncoding.EncodeToString([]byte(data))
	}
	cases := map[string]string{
		"invalid-base64":               issued + "=",
		"overlong-wire":                strings.Repeat("A", 2049),
		"null-json":                    base64.RawURLEncoding.EncodeToString([]byte("null")),
		"array-json":                   base64.RawURLEncoding.EncodeToString([]byte("[]")),
		"unknown-field":                encode("unrecognized", true),
		"wrong-value-type":             encode("after", 7),
		"wrong-action":                 encode("action", "ListAttachedRolePolicies"),
		"wrong-scope":                  encode("scope", "AWS"),
		"wrong-prefix":                 encode("prefix", "/unrelated/"),
		"unexpected-parent":            encode("parentArn", "arn:aws:iam::000000000000:role/Other"),
		"unexpected-parent-generation": encode("parentId", "AROAOTHER"),
		"empty-position":               encode("after", ""),
		"invalid-position":             encode("after", "bad/name"),
	}
	before := managedFrame(t, p.gateway.Store.Database())
	for name, marker := range cases {
		t.Run(name, func(t *testing.T) {
			probe := p
			probe.t = t
			probe.call("ListPolicies", "", url.Values{"Scope": {"Local"}, "Marker": {marker}}, 400, "InvalidInput")
			if managedFrame(t, p.gateway.Store.Database()) != before {
				t.Fatal("refused cursor changed persisted authority")
			}
		})
	}
	p.call("ListPolicies", "", url.Values{"Scope": {"Local"}, "Marker": {issued}}, 200, "<PolicyName>AdmissionBeta</PolicyName>")
	if managedFrame(t, p.gateway.Store.Database()) != before {
		t.Fatal("read-only recovery changed state")
	}
}

func TestIAMManagedAdmissionStrictFieldsAndMissingSelectorsAreAtomic(t *testing.T) {
	p := newManagedQueryProbe(t)
	policy := p.create("AdmissionPolicy")
	p.role("AdmissionRole")
	type request struct {
		action string
		input  url.Values
		status int
		code   string
	}
	cases := []request{
		{"CreatePolicy", url.Values{"PolicyName": {"bad/name"}, "PolicyDocument": {managedQueryAllow}}, 400, "InvalidInput"},
		{"CreatePolicy", url.Values{"PolicyName": {"MissingDocument"}}, 400, "InvalidInput"},
		{"CreatePolicy", url.Values{"PolicyDocument": {managedQueryAllow}}, 400, "InvalidInput"},
		{"GetPolicy", url.Values{"PolicyArn": {"arn:aws:iam::aws:policy/AdministratorAccess"}}, 400, "InvalidInput"},
		{"GetPolicy", url.Values{"PolicyArn": {"arn:aws:iam::111111111111:policy/AdmissionPolicy"}}, 400, "InvalidInput"},
		{"GetPolicy", url.Values{"PolicyArn": {policy.Arn}, "VersionId": {"v1"}}, 501, "NotImplemented"},
		{"GetPolicy", url.Values{"PolicyArn": {policy.Arn, policy.Arn}}, 400, "ValidationError"},
		{"CreatePolicyVersion", url.Values{"PolicyArn": {policy.Arn}}, 400, "InvalidInput"},
		{"CreatePolicyVersion", url.Values{"PolicyArn": {policy.Arn}, "PolicyDocument": {managedQueryAllow}, "SetAsDefault": {" true "}}, 400, "InvalidInput"},
		{"GetPolicyVersion", url.Values{"PolicyArn": {policy.Arn}}, 400, "InvalidInput"},
		{"GetPolicyVersion", url.Values{"PolicyArn": {policy.Arn}, "VersionId": {"v0"}}, 400, "InvalidInput"},
		{"GetPolicyVersion", url.Values{"PolicyArn": {policy.Arn}, "VersionId": {"v1.unstored"}}, 404, "NoSuchEntity"},
		{"ListPolicyVersions", url.Values{"PolicyArn": {iamManagedQueryPolicyPrefix + "MissingPolicy"}}, 404, "NoSuchEntity"},
		{"AttachRolePolicy", url.Values{"RoleName": {core.Administrator}, "PolicyArn": {policy.Arn}}, 400, "InvalidInput"},
		{"AttachRolePolicy", url.Values{"RoleName": {"bad/name"}, "PolicyArn": {policy.Arn}}, 400, "InvalidInput"},
		{"AttachRolePolicy", url.Values{"RoleName": {"AdmissionRole"}, "PolicyArn": {policy.Arn}, "ExpectedRoleID": {"wire-cannot-select-authority"}}, 501, "NotImplemented"},
		{"ListPolicyVersions", url.Values{"PolicyArn": {policy.Arn}, "Scope": {"Local"}}, 501, "NotImplemented"},
	}
	before := managedFrame(t, p.gateway.Store.Database())
	for i, c := range cases {
		body := p.call(c.action, "", c.input, c.status, c.code)
		if strings.Contains(body, "<Policy>") || strings.Contains(body, "<PolicyVersion>") || strings.Contains(body, "<Document>") {
			t.Fatal("refusal exposed success metadata", i)
		}
		if managedFrame(t, p.gateway.Store.Database()) != before {
			t.Fatal("refused request changed graph", i)
		}
	}
	p.call("GetPolicy", "", url.Values{"PolicyArn": {policy.Arn}}, 200, "<PolicyName>AdmissionPolicy</PolicyName>")
	p.call("AttachRolePolicy", "", url.Values{"RoleName": {"AdmissionRole"}, "PolicyArn": {policy.Arn}}, 200, "AttachRolePolicyResponse")
	p.decision("AdmissionRole", "allowed")
}

func TestIAMManagedAdmissionDeniedMissingAndPolicyOutageStayDistinct(t *testing.T) {
	p := newManagedQueryProbe(t)
	policy := p.create("OutagePolicy")
	ctx := context.Background()
	allow, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["iam:GetPolicy","iam:AttachRolePolicy"],"Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = p.gateway.Identities.Set("admission-reader", allow); err != nil {
		t.Fatal(err)
	}
	before := managedFrame(t, p.gateway.Store.Database())
	missing := url.Values{"RoleName": {"MissingRole"}, "PolicyArn": {policy.Arn}}
	p.call("AttachRolePolicy", "ungranted", missing, 403, "AccessDenied")
	p.call("AttachRolePolicy", "admission-reader", missing, 404, "NoSuchEntity")
	if managedFrame(t, p.gateway.Store.Database()) != before {
		t.Fatal("denied or missing selector changed authority")
	}
	var original string
	if err = p.gateway.Store.Database().QueryRowContext(ctx, "SELECT document FROM identity_policies WHERE name=?", "admission-reader").Scan(&original); err != nil {
		t.Fatal(err)
	}
	corrupt := `{"Version":"2012-10-17","Statement":[],"PRIVATE_SENTINEL":"do-not-expose-stored-policy"}`
	if _, err = p.gateway.Store.Database().ExecContext(ctx, "UPDATE identity_policies SET document=? WHERE name=?", corrupt, "admission-reader"); err != nil {
		t.Fatal(err)
	}
	before = managedFrame(t, p.gateway.Store.Database())
	body := p.call("GetPolicy", "admission-reader", url.Values{"PolicyArn": {policy.Arn}}, 500, "ServiceFailure")
	if strings.Contains(body, "PRIVATE_SENTINEL") || strings.Contains(body, "do-not-expose") || strings.Contains(body, "<Policy>") {
		t.Fatal("infrastructure failure disclosed stored data")
	}
	if managedFrame(t, p.gateway.Store.Database()) != before {
		t.Fatal("failed policy snapshot changed state")
	}
	if _, err = p.gateway.Store.Database().ExecContext(ctx, "UPDATE identity_policies SET document=? WHERE name=?", original, "admission-reader"); err != nil {
		t.Fatal(err)
	}
	p.call("GetPolicy", "admission-reader", url.Values{"PolicyArn": {policy.Arn}}, 200, "<PolicyName>OutagePolicy</PolicyName>")
}

func TestIAMManagedAdmissionVersionPagesHaveNumericOrderAndNoDocuments(t *testing.T) {
	p := newManagedQueryProbe(t)
	policy := p.create("NumericVersions")
	for n := 2; n <= 11; n++ {
		body := p.call("CreatePolicyVersion", "", url.Values{"PolicyArn": {policy.Arn}, "PolicyDocument": {managedQueryAllow}}, 200, "CreatePolicyVersionResponse")
		var created struct {
			Result struct {
				Version IAMManagedPolicyVersion `xml:"PolicyVersion"`
			} `xml:"CreatePolicyVersionResult"`
		}
		if err := xml.Unmarshal([]byte(body), &created); err != nil {
			t.Fatal(err)
		}
		if n >= 3 && n <= 9 {
			p.call("DeletePolicyVersion", "", url.Values{"PolicyArn": {policy.Arn}, "VersionId": {created.Result.Version.VersionID}}, 200, "DeletePolicyVersionResponse")
		}
	}
	before := managedFrame(t, p.gateway.Store.Database())
	got := []string{}
	marker := ""
	for page := 0; page < 4; page++ {
		input := url.Values{"PolicyArn": {policy.Arn}, "MaxItems": {"1"}}
		if marker != "" {
			input.Set("Marker", marker)
		}
		body := p.call("ListPolicyVersions", "", input, 200, "ListPolicyVersionsResponse")
		if strings.Contains(body, "<Document>") {
			t.Fatal("version metadata page disclosed document")
		}
		var result struct {
			Result struct {
				Versions    []IAMManagedPolicyVersion `xml:"Versions>member"`
				IsTruncated bool
				Marker      string
			} `xml:"ListPolicyVersionsResult"`
		}
		if err := xml.Unmarshal([]byte(body), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Result.Versions) != 1 {
			t.Fatal("page size mismatch")
		}
		got = append(got, result.Result.Versions[0].VersionID)
		marker = result.Result.Marker
		if result.Result.IsTruncated != (page < 3) || (page < 3 && marker == "") || (page == 3 && marker != "") {
			t.Fatal("incorrect continuation contract")
		}
		if managedFrame(t, p.gateway.Store.Database()) != before {
			t.Fatal("pagination changed policy versions")
		}
	}
	if strings.Join(got, ",") != "v1,v2,v10,v11" {
		t.Fatal("versions were not numerically ordered", got)
	}
}

func TestIAMManagedAdmissionCorruptAttachmentParentIsInfrastructureFailure(t *testing.T) {
	p := newManagedQueryProbe(t)
	policy := p.create("ReferencedPolicy")
	p.role("ReferenceRole")
	p.call("AttachRolePolicy", "", url.Values{"RoleName": {"ReferenceRole"}, "PolicyArn": {policy.Arn}}, 200, "AttachRolePolicyResponse")
	p.call("ListAttachedRolePolicies", "", url.Values{"RoleName": {"ReferenceRole"}}, 200, "<PolicyName>ReferencedPolicy</PolicyName>")
	// Valid SQLite exposes a corrupt stored reference while leaving the live
	// role readable. An API list must fail500, not silently return404 or an empty
	// successful list. This is a native error contract, not a host-tamper defense.
	changed, err := p.gateway.Store.Database().Exec("UPDATE aws_iam_managed_policies SET deleted=1 WHERE name=? AND policy_id=?", policy.PolicyName, policy.PolicyID)
	if err != nil {
		t.Fatal(err)
	}
	if n, e := changed.RowsAffected(); e != nil || n != 1 {
		t.Fatal("fixture did not retire referenced policy", n, e)
	}
	before := managedFrame(t, p.gateway.Store.Database())
	body := p.call("ListAttachedRolePolicies", "", url.Values{"RoleName": {"ReferenceRole"}}, 500, "ServiceFailure")
	if strings.Contains(body, "<AttachedPolicies>") || strings.Contains(body, policy.Arn) || strings.Contains(body, managedQueryAllow) {
		t.Fatal("failed attachment list disclosed policy data")
	}
	if managedFrame(t, p.gateway.Store.Database()) != before {
		t.Fatal("failed attachment list changed state")
	}
	if _, err = p.gateway.Store.Database().Exec("UPDATE aws_iam_managed_policies SET deleted=0 WHERE name=? AND policy_id=?", policy.PolicyName, policy.PolicyID); err != nil {
		t.Fatal(err)
	}
	p.call("ListAttachedRolePolicies", "", url.Values{"RoleName": {"ReferenceRole"}}, 200, "<PolicyName>ReferencedPolicy</PolicyName>")
	p.decision("ReferenceRole", "allowed")
}
