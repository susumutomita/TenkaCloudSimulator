package catalog

import (
	"maps"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestRedshiftDescribeTagsReferenceMatchesLocalContract(t *testing.T) {
	h := newHarness(t, redshift)
	document := h.engine.OpenAPI()["redshift"]
	post := document["paths"].(map[string]any)["/#Action=DescribeTags"].(map[string]any)["post"].(map[string]any)
	body := post["requestBody"].(map[string]any)["content"].(map[string]any)["application/x-www-form-urlencoded"].(map[string]any)
	component := document["components"].(map[string]any)["schemas"].(map[string]any)["DescribeTagsRequest"].(map[string]any)
	props := component["properties"].(map[string]any)
	if required := component["required"]; !reflect.DeepEqual(required, []any{"Action", "Version", "ResourceName"}) {
		t.Fatalf("the published form must require ResourceName alongside Action and Version, got %v", required)
	}
	if keys := slices.Sorted(maps.Keys(props)); !slices.Equal(keys, []string{"Action", "ResourceName", "TagKeys", "TagValues", "Version"}) {
		t.Fatalf("published form includes unsupported inputs: %v", keys)
	}
	const arn = "arn:aws:redshift:us-east-1:000000000000:parametergroup:fixture-params"
	pattern := props["ResourceName"].(map[string]any)["pattern"].(string)
	if rule := regexp.MustCompile(pattern); !rule.MatchString(arn) || rule.MatchString("fixture-params") {
		t.Fatalf("incorrect ARN constraint: %s", pattern)
	}
	description := post["description"].(string)
	for _, needed := range []string{"CreateClusterParameterGroup", "fixture-params", "redshift-1.0", "not pre-created", "same local Region"} {
		if !strings.Contains(description, needed) {
			t.Fatalf("missing fixture or region guidance %q: %s", needed, description)
		}
	}
	if got := document["info"].(map[string]any)["description"].(string); strings.Contains(got, "Describe operations refuse") ||
		!strings.Contains(got, "DescribeClusters, DescribeClusterParameterGroups and DescribeClusterSubnetGroups refuse TagKeys") {
		t.Fatalf("Redshift bounds do not isolate resource-list limits from DescribeTags: %s", got)
	}

	// Send the generated form as it is published, not a separately constructed
	// SDK request, with the same explicit service selector as the reference.
	form := url.Values{}
	for k, val := range body["example"].(map[string]any) {
		form.Set(k, val.(string))
	}
	if form.Get("ResourceName") != arn {
		t.Fatalf("missing local fixture ARN: %s", form.Encode())
	}
	sendExample := func() queryReply {
		req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Simulator-Service", "redshift")
		req.Header.Set("X-Simulator-Region", "us-east-1")
		if !h.engine.Owns(req) {
			t.Fatal("the generated reference form did not route to Redshift")
		}
		return h.send(req)
	}
	expectFault(t, sendExample(), 404, "ResourceNotFoundFault")
	h.warehouse("CreateClusterParameterGroup", "ParameterGroupName", "fixture-params",
		"ParameterGroupFamily", "redshift-1.0", "Description", "Local tag fixture",
		"Tags.Tag.1.Key", "purpose", "Tags.Tag.1.Value", "demo").result(t, "CreateClusterParameterGroup")
	item := sendExample().result(t, "DescribeTags").get("TaggedResources", "TaggedResource")
	if item == nil || item.get("ResourceName").text != arn || item.get("Tag", "Key").text != "purpose" || item.get("Tag", "Value").text != "demo" {
		t.Fatalf("generated form did not read the known local fixture: %v", item)
	}
	h.warehouse("DeleteClusterParameterGroup", "ParameterGroupName", "fixture-params").result(t, "DeleteClusterParameterGroup")
	expectFault(t, sendExample(), 404, "ResourceNotFoundFault")
}
