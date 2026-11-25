package catalog

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Official contract: https://docs.aws.amazon.com/redshift/latest/APIReference/API_DescribeTags.html
func TestRedshiftDescribeTagsForARN(t *testing.T) {
	h := newHarness(t, redshift)
	const arn = "arn:aws:redshift:us-east-1:000000000000:parametergroup:tags"
	h.warehouse("CreateClusterParameterGroup",
		"ParameterGroupName", "tags", "ParameterGroupFamily", "redshift-1.0", "Description", "fixture",
		"Tags.Tag.1.Key", "owner", "Tags.Tag.1.Value", "admin",
		"Tags.Tag.2.Key", "environment", "Tags.Tag.2.Value", "test",
		"Tags.Tag.3.Key", "unrelated", "Tags.Tag.3.Value", "").result(t, "CreateClusterParameterGroup")
	assert := func(r queryReply, pairs ...string) {
		t.Helper()
		list := r.result(t, "DescribeTags").get("TaggedResources")
		if list == nil {
			t.Fatalf("DescribeTags missing TaggedResources: %s", r.raw)
		}
		got := list.all("TaggedResource")
		if r.root.attrs["xmlns"] != "http://redshift.amazonaws.com/doc/2012-12-01/" || len(got)*2 != len(pairs) {
			t.Fatalf("unexpected DescribeTags result: %s", r.raw)
		}
		for i, tag := range got {
			if tag.get("ResourceName").text != arn || tag.get("ResourceType").text != "parametergroup" ||
				tag.get("Tag", "Key").text != pairs[2*i] || tag.get("Tag", "Value").text != pairs[2*i+1] {
				t.Fatalf("unexpected DescribeTags result: %s", r.raw)
			}
		}
	}
	assert(h.warehouse("DescribeTags", "ResourceName", arn), "environment", "test", "owner", "admin", "unrelated", "")
	assert(h.warehouse("DescribeTags", "ResourceName", arn,
		"TagKeys.TagKey.1", "owner", "TagKeys.TagKey.2", "environment",
		"TagValues.TagValue.1", "admin", "TagValues.TagValue.2", "test"), "environment", "test", "owner", "admin")
	assert(h.warehouse("DescribeTags", "ResourceName", arn, "TagKeys.TagKey.1", "owner", "TagValues.TagValue.1", "test"))

	for _, fields := range [][]string{
		{},
		{"ResourceType", "parametergroup"},
		{"ResourceName", "tags"},
		{"ResourceName", arn, "ResourceType", "parametergroup"},
		{"ResourceName", arn, "MaxRecords", "10"},
		{"ResourceName", arn, "Marker", "wrong"},
	} {
		expectFault(t, h.warehouse("DescribeTags", fields...), 400, "InvalidParameterValue")
	}
	const missing = "arn:aws:redshift:us-east-1:000000000000:parametergroup:missing"
	expectFault(t, h.warehouse("DescribeTags", "ResourceName", missing), 404, "ResourceNotFoundFault")
	h.policy.deny["redshift:DescribeTags "+arn] = true
	h.policy.deny["redshift:DescribeTags "+missing] = true
	expectFault(t, h.warehouse("DescribeTags", "ResourceName", arn), 403, "AccessDenied")
	expectFault(t, h.warehouse("DescribeTags", "ResourceName", missing), 403, "AccessDenied")
	delete(h.policy.deny, "redshift:DescribeTags "+arn)
	delete(h.policy.deny, "redshift:DescribeTags "+missing)
	form := url.Values{"Action": {"DescribeTags"}, "Version": {"2012-12-01"}, "ResourceName": {arn}}
	req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	sign(req, "redshift", "ap-northeast-1")
	expectFault(t, h.send(req), 404, "ResourceNotFoundFault")

	h.warehouse("DeleteTags", "ResourceName", arn, "TagKeys.TagKey.1", "owner").result(t, "DeleteTags")
	assert(h.warehouse("DescribeTags", "ResourceName", arn), "environment", "test", "unrelated", "")
	h.warehouse("DeleteClusterParameterGroup", "ParameterGroupName", "tags").result(t, "DeleteClusterParameterGroup")
	expectFault(t, h.warehouse("DescribeTags", "ResourceName", arn), 404, "ResourceNotFoundFault")
}
