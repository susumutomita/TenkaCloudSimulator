package server

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"strings"
	"testing"
)

func tagChecksum(body string) map[string]string {
	sum := md5.Sum([]byte(body))
	return map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString(sum[:])}
}
func TestBucketTagsNativeStateAndScopedIAM(t *testing.T) {
	s := instance(t)
	status(t, s, "PUT", "/tagged-bucket", "", 200, nil)
	status(t, s, "GET", "/tagged-bucket?tagging", "", 404, nil)
	body := `<Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet><Tag><Key>TenkaCloud:NamePrefix</Key><Value> lab </Value></Tag><Tag><Key>日本語</Key><Value>東京 開発</Value></Tag></TagSet></Tagging>`
	status(t, s, "PUT", "/tagged-bucket?tagging", body, 200, tagChecksum(body))
	var result struct {
		Tags []struct{ Key, Value string } `xml:"TagSet>Tag"`
	}
	raw := status(t, s, "GET", "/tagged-bucket?tagging", "", 200, nil)
	if err := xml.Unmarshal(raw, &result); err != nil || len(result.Tags) != 2 || result.Tags[0].Value != " lab " {
		t.Fatal(string(raw), err)
	}
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetBucketTagging","Resource":"arn:aws:s3:::tagged-bucket"}]}`
	status(t, s, "PUT", "/_simulator/v1/iam/principals/tag-reader/policy", policy, 200, nil)
	reader := map[string]string{"X-Simulator-Principal": "tag-reader"}
	status(t, s, "GET", "/tagged-bucket?tagging", "", 200, reader)
	status(t, s, "DELETE", "/tagged-bucket?tagging", "", 403, reader)
	headers := tagChecksum(body)
	headers["X-Simulator-Principal"] = "tag-reader"
	status(t, s, "PUT", "/tagged-bucket?tagging", body, 403, headers)
	status(t, s, "GET", "/another-bucket?tagging", "", 403, reader)
	// A replacement is a full tag set, never a merge with hidden stale tags.
	replacement := `<Tagging><TagSet><Tag><Key>empty-value</Key><Value/></Tag></TagSet></Tagging>`
	status(t, s, "PUT", "/tagged-bucket?tagging", replacement, 200, tagChecksum(replacement))
	got := status(t, s, "GET", "/tagged-bucket?tagging", "", 200, nil)
	if strings.Contains(string(got), "NamePrefix") || !strings.Contains(string(got), "empty-value") {
		t.Fatal(string(got))
	}
	status(t, s, "DELETE", "/tagged-bucket?tagging", "", 204, nil)
	status(t, s, "GET", "/tagged-bucket?tagging", "", 404, nil)
	empty := `<Tagging><TagSet/></Tagging>`
	status(t, s, "PUT", "/tagged-bucket?tagging", empty, 200, tagChecksum(empty))
	status(t, s, "GET", "/tagged-bucket?tagging", "", 404, nil)
	status(t, s, "DELETE", "/tagged-bucket", "", 204, nil)
}
func TestBucketTagValidationRejectsAmbiguousOrUnsupportedInput(t *testing.T) {
	s := instance(t)
	status(t, s, "PUT", "/tag-boundary", "", 200, nil)
	bodies := []string{
		``, `<`, `<Tagging>`, `<Tagging/>`, `<Tagging><TagSet><Tag><Key>only-key</Key></Tag></TagSet></Tagging>`,
		`<Tagging><TagSet><Tag><Key>a</Key><Key>b</Key><Value>x</Value></Tag></TagSet></Tagging>`,
		`<Tagging><TagSet><Tag><Key>a</Key><Value><Unknown>x</Unknown></Value></Tag></TagSet></Tagging>`,
		`<Tagging><TagSet/><TagSet/></Tagging>`, `<Tagging><TagSet><Unknown/></TagSet></Tagging>`,
		`<Tagging><TagSet><Tag><Key>a</Key><Value>x</Value></Tag><Tag><Key>a</Key><Value>y</Value></Tag></TagSet></Tagging>`,
		`<Tagging><TagSet><Tag><Key>aws:reserved</Key><Value>x</Value></Tag></TagSet></Tagging>`,
		`<Tagging><TagSet><Tag><Key>emoji</Key><Value>😀</Value></Tag></TagSet></Tagging>`,
		`<Tagging><TagSet>ignored text</TagSet></Tagging>`, `<Tagging><TagSet/></Tagging><Other/>`,
		`<Tagging><TagSet><Tag><Key>a</Key><Value>x</Value></Bad></TagSet></Tagging>`,
		`<Tagging><TagSet><Tag attribute="ignored"><Key>a</Key><Value>x</Value></Tag></TagSet></Tagging>`,
		`<!DOCTYPE Tagging><Tagging><TagSet/></Tagging>`, `<?unsafe data?><Tagging><TagSet/></Tagging>`,
		`<Tagging xmlns="https://unknown.invalid"><TagSet/></Tagging>`,
		`<Tagging><TagSet><Tag><Key>` + strings.Repeat("x", 129) + `</Key><Value>x</Value></Tag></TagSet></Tagging>`,
		`<Tagging><TagSet><Tag><Key>x</Key><Value>` + strings.Repeat("x", 257) + `</Value></Tag></TagSet></Tagging>`,
		strings.Repeat("x", 65537),
	}
	var fiftyOne strings.Builder
	fiftyOne.WriteString("<Tagging><TagSet>")
	for i := range 51 {
		fmt.Fprintf(&fiftyOne, "<Tag><Key>key%d</Key><Value>x</Value></Tag>", i)
	}
	fiftyOne.WriteString("</TagSet></Tagging>")
	bodies = append(bodies, fiftyOne.String())
	for i, body := range bodies {
		t.Run(fmt.Sprint(i), func(t *testing.T) { status(t, s, "PUT", "/tag-boundary?tagging", body, 400, tagChecksum(body)) })
	}
	valid := `<Tagging><TagSet><Tag><Key>key</Key><Value>value</Value></Tag></TagSet></Tagging>`
	status(t, s, "PUT", "/tag-boundary?tagging", valid, 400, nil)
	status(t, s, "PUT", "/tag-boundary?tagging", valid, 400, map[string]string{"Content-MD5": "bad"})
	status(t, s, "POST", "/tag-boundary?tagging", "", 405, nil)
	status(t, s, "GET", "/tag-boundary?tagging&versioning", "", 400, nil)
	status(t, s, "GET", "/tag-boundary/key?tagging", "", 501, nil)
	status(t, s, "GET", "/tag-boundary?tagging=ignored", "", 400, nil)
}
