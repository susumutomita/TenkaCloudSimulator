package aws

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func validationRequest(g *Gateway, body, principal string, extra url.Values) *httptest.ResponseRecorder {
	form := url.Values{"Action": {"ValidateTemplate"}, "Version": {"2010-05-15"}}
	if body != "" {
		form.Set("TemplateBody", body)
	}
	for key, values := range extra {
		form[key] = append(form[key], values...)
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Simulator-Principal", principal)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}

func TestValidateTemplateReadsOnlyDeclarations(t *testing.T) {
	const body = `Description: local <&> preview
Parameters:
  Required: {Type: String, Description: "input <&>"}
  Hidden: {Type: String, NoEcho: TRUE, Default: hidden-value}
  Empty: {Type: String, Default: ''}
  Count: {Type: Number, Default: 3, MinValue: 2}
  Config: {Type: "AWS::SSM::Parameter::Value<String>", Default: /missing/local}
Resources:
  Result: {Type: 'AWS::SSM::Parameter', Properties: {Type: String, Value: !Ref Config}}
  Role: {Type: 'AWS::IAM::Role', Properties: {RoleName: !Ref Required}}
`
	g := ssmValueGateway(t)
	const reader = "template-reader"
	if err := g.Identities.Set(reader, core.Policy{Version: "2012-10-17", Statement: []core.Statement{{Effect: "Allow", Action: []string{"cloudformation:ValidateTemplate"}, Resource: []string{"*"}}}}); err != nil {
		t.Fatal(err)
	}
	before := stackConditionalDBFrame(t, g.Store.Database())
	w := validationRequest(g, body, reader, nil)
	var response struct {
		XMLName xml.Name `xml:"http://cloudformation.amazonaws.com/doc/2010-05-15/ ValidateTemplateResponse"`
		Result  struct {
			Description string
			Parameters  []struct {
				ParameterKey, Description string
				NoEcho                    bool
				DefaultValue              *string
			} `xml:"Parameters>member"`
			Capabilities       []string `xml:"Capabilities>member"`
			CapabilitiesReason string
		} `xml:"ValidateTemplateResult"`
	}
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "hidden-value") {
		t.Fatalf("status=%d or NoEcho default present", w.Code)
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	result := response.Result
	if result.Description != "local <&> preview" || !slices.Equal(result.Capabilities, []string{"CAPABILITY_NAMED_IAM"}) || result.CapabilitiesReason != "Role" {
		t.Fatal("missing description or named IAM capability", result.Description, result.Capabilities)
	}
	ps := result.Parameters
	wantNames := []string{"Config", "Count", "Empty", "Hidden", "Required"}
	if len(ps) != len(wantNames) {
		t.Fatalf("got %d parameters", len(ps))
	}
	for i, key := range wantNames {
		if ps[i].ParameterKey != key {
			t.Errorf("parameter %d = %q, want %q", i, ps[i].ParameterKey, key)
		}
	}
	if ps[0].DefaultValue == nil || *ps[0].DefaultValue != "/missing/local" || ps[1].DefaultValue == nil || *ps[1].DefaultValue != "3" ||
		ps[2].DefaultValue == nil || *ps[2].DefaultValue != "" || !ps[3].NoEcho || ps[3].DefaultValue != nil ||
		ps[4].DefaultValue != nil || ps[4].NoEcho || ps[4].Description != "input <&>" {
		t.Fatal("parameter metadata did not preserve optional, required or NoEcho semantics")
	}
	if before != stackConditionalDBFrame(t, g.Store.Database()) {
		t.Fatal("validation mutated state")
	}
}

func TestValidateTemplateIAMCapabilities(t *testing.T) {
	for _, tc := range []struct{ kind, property, capability string }{
		{"AWS::S3::Bucket", "{}", ""},
		{"AWS::IAM::Role", "{}", "CAPABILITY_IAM"},
		{"AWS::IAM::InstanceProfile", "{InstanceProfileName: named}", "CAPABILITY_NAMED_IAM"},
	} {
		w := validationRequest(ssmValueGateway(t), "Resources: {Resource: {Type: '"+tc.kind+"', Properties: "+tc.property+"}}", "", nil)
		if w.Code != http.StatusOK || (tc.capability == "" && strings.Contains(w.Body.String(), "<Capabilities>")) ||
			(tc.capability != "" && !strings.Contains(w.Body.String(), "<member>"+tc.capability+"</member>")) {
			t.Fatalf("%s: %d %s", tc.kind, w.Code, w.Body.String())
		}
	}
}

func TestValidateTemplateRejectsUnsupportedDeclarationsAndInputs(t *testing.T) {
	const supported = "Resources: {Bucket: {Type: 'AWS::S3::Bucket'}}"
	const unknown = "Resources: {Unsupported: {Type: 'Example::Local::Unknown'}}"
	g := ssmValueGateway(t)
	if err := g.Identities.Set("creator", core.Policy{Version: "2012-10-17", Statement: []core.Statement{{Effect: "Allow", Action: []string{"cloudformation:CreateStack"}, Resource: []string{"*"}}}}); err != nil {
		t.Fatal(err)
	}
	before := stackConditionalDBFrame(t, g.Store.Database())
	for _, tc := range []struct {
		name, body, message string
		extra               url.Values
	}{
		{"unsupported resource", unknown, "Unsupported resource type", nil},
		{"unsupported section", "Transform: Example\n" + supported, "Unsupported template section", nil},
		{"missing template", "", "TemplateBody", nil},
		{"oversized template", strings.Repeat(" ", 51201), "51200", nil},
		{"multiple documents", supported + "\n---\n" + supported, "Invalid YAML/JSON", nil},
		{"invalid default", "Parameters: {Hidden: {Type: String, NoEcho: true, Default: hidden-value, MinLength: 45}}\n" + supported, "default violates constraints", nil},
		{"external reference", "", "inline TemplateBody", url.Values{"TemplateURL": {"https://example.invalid/template"}}},
		{"both template sources", supported, "inline TemplateBody", url.Values{"TemplateURL": {"https://example.invalid/template"}}},
		{"stack name", supported, "inline TemplateBody", url.Values{"StackName": {"test"}}},
		{"parameters", supported, "inline TemplateBody", url.Values{"Parameters.member.1.ParameterKey": {"X"}}},
		{"repeated body", supported, "Repeated Query fields", url.Values{"TemplateBody": {supported}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := validationRequest(g, tc.body, "", tc.extra)
			conditionBoundaryError(t, w, http.StatusBadRequest, "ValidationError", tc.message)
			if strings.Contains(w.Body.String(), "hidden-value") {
				t.Fatal("NoEcho default disclosed on failure")
			}
		})
	}
	conditionBoundaryError(t, validationRequest(g, unknown, "creator", nil), http.StatusForbidden, "AccessDenied", "cloudformation:ValidateTemplate")
	conditionBoundaryError(t, conditionBoundarySend(g, "CreateStack", unknown, "", nil), http.StatusBadRequest, "ValidationError", "Unsupported resource type")
	if before != stackConditionalDBFrame(t, g.Store.Database()) {
		t.Fatal("rejected templates mutated state")
	}
}
