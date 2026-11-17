package aws

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const vpcOriginTemplate = `Parameters:
  EndpointArn: {Type: String}
Resources:
  Origin:
    Type: AWS::CloudFront::VpcOrigin
    Properties:
      VpcOriginEndpointConfig:
        Arn: !Ref EndpointArn
        Name: stack-origin
        OriginProtocolPolicy: http-only
      Tags: [{Key: owner, Value: local}]
Outputs:
  Id: {Value: !GetAtt Origin.Id}
  Ref: {Value: !Ref Origin}
  Arn: {Value: !GetAtt Origin.Arn}
`

func TestCloudFrontVpcOriginStackNativeLifecycle(t *testing.T) {
	h := cfHarnessFor(t)
	store, err := NewStackStore(h.store.Database())
	if err != nil {
		t.Fatal(err)
	}
	h.gateway.Stacks = store
	cfn := func(action string, params ...string) string {
		t.Helper()
		q := url.Values{"Action": {action}, "Version": {"2010-05-15"}}
		for i := 0; i < len(params); i += 2 {
			q.Set(params[i], params[i+1])
		}
		w := h.raw(map[string]string{"X-Simulator-Service": "cloudformation"}, q.Encode())
		if w.Code != 200 {
			t.Fatalf("%s %d: %s", action, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	cfn("ValidateTemplate", "TemplateBody", vpcOriginTemplate)
	cfn("CreateStack", "StackName", "origin-demo", "TemplateBody", vpcOriginTemplate, "Parameters.member.1.ParameterKey", "EndpointArn", "Parameters.member.1.ParameterValue", h.alb)
	rows, err := store.Records("us-east-1", "origin-demo")
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	id := rows[0].Outputs["Ref"]
	if id != rows[0].Outputs["Id"] || rows[0].Outputs["Arn"] != cloudFrontARNPrefix+id {
		t.Fatal(rows[0].Outputs)
	}
	o := h.origin(h.request("GET", cloudFrontPath+"/"+id, ""), 200)
	if o.Config.HTTPPort != 80 || o.Config.HTTPSPort != 443 || o.Config.ARN != h.alb {
		t.Fatal(o)
	}
	origin, err := h.gateway.CloudFront.Get(t.Context(), id)
	if err != nil || len(origin.Tags) != 1 || origin.Tags[0].Value != "local" {
		t.Fatal(origin, err)
	}
	cfn("DeleteStack", "StackName", "origin-demo")
	cfExpectError(t, h.request("GET", cloudFrontPath+"/"+id, ""), 404, "EntityNotFound")
	h.ok("DeleteLoadBalancer", "LoadBalancerArn", h.alb)
}

func TestCloudFrontVpcOriginStackRefusalsAndRollback(t *testing.T) {
	h := cfHarnessFor(t)
	db := h.store.Database()
	store, err := NewStackStore(db)
	if err != nil {
		t.Fatal(err)
	}
	compile := func(body string) (StackRecord, error) {
		return compileStack("us-east-1", "origin-check", body, map[string]string{"EndpointArn": h.alb}, "")
	}
	for _, bad := range []string{
		strings.Replace(vpcOriginTemplate, "http-only", "https-only", 1),
		strings.Replace(vpcOriginTemplate, "Name: stack-origin", "Name: stack-origin\n        HTTPPort: 65536", 1),
		strings.Replace(vpcOriginTemplate, "Name: stack-origin", "Name: stack-origin\n        AdditionalConfig: 1", 1),
		strings.Replace(vpcOriginTemplate, "Tags: [", "OtherTags: [", 1),
		strings.Replace(vpcOriginTemplate, "Tags: [{Key: owner, Value: local}]", "Tags: {Key: owner, Value: local}", 1),
		strings.Replace(vpcOriginTemplate, "Key: owner, Value: local", "Key: owner, Value: local, Extra: true", 1),
		strings.Replace(vpcOriginTemplate, "!GetAtt Origin.Id", "!GetAtt Origin.Misspelled", 1),
	} {
		if _, err := compile(bad); err == nil {
			t.Fatal("accepted unsupported properties", bad)
		}
	}
	r, err := compile(vpcOriginTemplate)
	if err != nil {
		t.Fatal(err)
	}
	refused := errors.New("denied")
	if err := store.createChecked(r, func(action, resource string) error {
		switch action {
		case "cloudfront:CreateVpcOrigin":
			if resource != "*" {
				t.Fatal(action, resource)
			}
		case "cloudfront:GetVpcOrigin", "cloudfront:TagResource":
			if resource != r.Outputs["Arn"] {
				t.Fatal(action, resource)
			}
		default:
			t.Fatal(action)
		}
		if action == "cloudfront:TagResource" {
			return refused
		}
		return nil
	}); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	if _, err := store.Records("us-east-1", "origin-check"); err == nil {
		t.Fatal("refused stack was persisted")
	}
	missing, err := compileStack("us-east-1", "missing-target", vpcOriginTemplate, map[string]string{"EndpointArn": strings.Replace(h.alb, "origin-target", "missing-target", 1)}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Create(missing); err == nil {
		t.Fatal("created origin for missing target")
	}
	if _, err := store.Records("us-east-1", "missing-target"); err == nil {
		t.Fatal("ownership without target")
	}
	if err := store.Create(r); err != nil {
		t.Fatal(err)
	}
	o, err := h.gateway.CloudFront.Get(t.Context(), r.Outputs["Id"])
	if err != nil {
		t.Fatal(err)
	}
	c := o.Config
	c.Name = "out-of-band"
	changed, err := h.gateway.CloudFront.Update(t.Context(), o.ID, o.ETag, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("us-east-1", "origin-check"); err == nil {
		t.Fatal("deleted a modified origin without its matching revision")
	}
	if _, err := h.gateway.CloudFront.Delete(t.Context(), o.ID, changed.ETag); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("us-east-1", "origin-check"); err != nil {
		t.Fatal(err)
	}
}

func TestCloudFrontVpcOriginTaggedCreationRequiresTagResource(t *testing.T) {
	h := cfHarnessFor(t)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"cloudfront:CreateVpcOrigin","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = h.gateway.Identities.Set("creator", policy); err != nil {
		t.Fatal(err)
	}
	h.principal = "creator"
	cfExpectError(t, h.request("POST", cloudFrontPath, cfCreateXML(h.config)), 403, "AccessDenied")
	h.origin(h.request("POST", cloudFrontPath, "<CreateVpcOriginRequest>"+h.config+"</CreateVpcOriginRequest>"), 202)
}
