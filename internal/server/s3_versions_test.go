package server

import (
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestS3VersionsNativeProtocolAndIAM(t *testing.T) {
	s := instance(t)
	status(t, s, "PUT", "/version-bucket", "", 200, nil)
	conf := status(t, s, "GET", "/version-bucket?versioning", "", 200, nil)
	if strings.Contains(string(conf), "Status") {
		t.Fatal(string(conf))
	}
	status(t, s, "PUT", "/version-bucket/report.txt", "original", 200, nil)
	status(t, s, "PUT", "/version-bucket?versioning", `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Enabled</Status></VersioningConfiguration>`, 200, nil)
	r, _ := call(t, s, "PUT", "/version-bucket/report.txt", "updated", nil)
	id := r.Header.Get("x-amz-version-id")
	if r.StatusCode != 200 || id == "" || id == "null" {
		t.Fatal(r.StatusCode, r.Header)
	}
	status(t, s, "PUT", "/version-bucket/dir/a%20%26b.txt", "group", 200, nil)
	r, data := call(t, s, "GET", "/version-bucket/report.txt?versionId=null", "", nil)
	if string(data) != "original" || r.Header.Get("x-amz-version-id") != "null" {
		t.Fatal(r.StatusCode, r.Header, string(data))
	}
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObjectVersion","s3:DeleteObjectVersion"],"Resource":"arn:aws:s3:::version-bucket/report.txt"},{"Effect":"Allow","Action":["s3:ListBucketVersions","s3:GetBucketVersioning","s3:GetBucketPublicAccessBlock"],"Resource":"arn:aws:s3:::version-bucket"}]}`
	status(t, s, "PUT", "/_simulator/v1/iam/principals/version-reader/policy", policy, 200, nil)
	reader := map[string]string{"X-Simulator-Principal": "version-reader"}
	status(t, s, "GET", "/version-bucket/report.txt?versionId="+id, "", 200, reader)
	status(t, s, "GET", "/version-bucket/report.txt", "", 403, reader)
	status(t, s, "DELETE", "/version-bucket/report.txt", "", 403, reader)
	status(t, s, "GET", "/version-bucket?list-type=2", "", 403, reader)
	for _, kind := range []string{"versioning", "publicAccessBlock", "versions"} {
		status(t, s, "GET", "/version-bucket?"+kind, "", 200, reader)
	}
	status(t, s, "PUT", "/version-bucket?versioning", `<VersioningConfiguration><Status>Suspended</Status></VersioningConfiguration>`, 403, reader)
	r, _ = call(t, s, "DELETE", "/version-bucket/report.txt", "", nil)
	marker := r.Header.Get("x-amz-version-id")
	if r.StatusCode != 204 || r.Header.Get("x-amz-delete-marker") != "true" {
		t.Fatal(r.StatusCode, r.Header)
	}
	r, _ = call(t, s, "GET", "/version-bucket/report.txt", "", nil)
	if r.StatusCode != 404 || r.Header.Get("x-amz-delete-marker") != "true" || r.Header.Get("x-amz-version-id") != marker {
		t.Fatal(r.StatusCode, r.Header)
	}
	r, _ = call(t, s, "HEAD", "/version-bucket/report.txt?versionId="+marker, "", nil)
	if r.StatusCode != 405 || r.Header.Get("Last-Modified") == "" || r.Header.Get("x-amz-delete-marker") != "true" {
		t.Fatal(r.StatusCode, r.Header)
	}
	status(t, s, "GET", "/version-bucket/report.txt?versionId=absent", "", 404, nil)
	status(t, s, "DELETE", "/version-bucket", "", 409, nil)
	page := status(t, s, "GET", "/version-bucket?versions&prefix=report&max-keys=1", "", 200, nil)
	var first struct {
		IsTruncated         bool
		NextKeyMarker       string
		NextVersionIDMarker string `xml:"NextVersionIdMarker"`
		Markers             []struct {
			ID     string `xml:"VersionId"`
			Latest bool   `xml:"IsLatest"`
		} `xml:"DeleteMarker"`
	}
	if err := xml.Unmarshal(page, &first); err != nil || !first.IsTruncated || len(first.Markers) != 1 || first.Markers[0].ID != marker || !first.Markers[0].Latest {
		t.Fatal(string(page), err)
	}
	next := status(t, s, "GET", "/version-bucket?versions&prefix=report&max-keys=1&key-marker="+url.QueryEscape(first.NextKeyMarker)+"&version-id-marker="+first.NextVersionIDMarker, "", 200, nil)
	if !strings.Contains(string(next), "<VersionId>"+id+"</VersionId>") {
		t.Fatal(string(next))
	}
	grouped := status(t, s, "GET", "/version-bucket?versions&delimiter=/&encoding-type=url", "", 200, nil)
	if !strings.Contains(string(grouped), "<CommonPrefixes><Prefix>dir%2F</Prefix></CommonPrefixes>") {
		t.Fatal(string(grouped))
	}
	encoded := status(t, s, "GET", "/version-bucket?versions&prefix=dir&encoding-type=url", "", 200, nil)
	if !strings.Contains(string(encoded), "dir%2Fa%20%26b.txt") {
		t.Fatal(string(encoded))
	}
	zero := status(t, s, "GET", "/version-bucket?versions&max-keys=0", "", 200, nil)
	if strings.Contains(string(zero), "<Version>") || strings.Contains(string(zero), "<DeleteMarker>") {
		t.Fatal(string(zero))
	}
	r, _ = call(t, s, "DELETE", "/version-bucket/report.txt?versionId="+marker, "", reader)
	if r.StatusCode != 204 || r.Header.Get("x-amz-delete-marker") != "true" {
		t.Fatal(r.StatusCode, r.Header)
	}
	if data = status(t, s, "GET", "/version-bucket/report.txt", "", 200, nil); string(data) != "updated" {
		t.Fatal(string(data))
	}
	status(t, s, "DELETE", "/version-bucket/report.txt?versionId="+id, "", 204, reader)
	if data = status(t, s, "GET", "/version-bucket/report.txt", "", 200, nil); string(data) != "original" {
		t.Fatal(string(data))
	}
}
func TestS3ConfigurationStrictnessAndPublicAccessState(t *testing.T) {
	s := instance(t)
	status(t, s, "PUT", "/config-bucket", "", 200, nil)
	initial := status(t, s, "GET", "/config-bucket?publicAccessBlock", "", 200, nil)
	if strings.Count(string(initial), ">true<") != 4 {
		t.Fatal(string(initial))
	}
	status(t, s, "PUT", "/config-bucket?publicAccessBlock", `<PublicAccessBlockConfiguration><BlockPublicAcls>true</BlockPublicAcls></PublicAccessBlockConfiguration>`, 200, nil)
	got := status(t, s, "GET", "/config-bucket?publicAccessBlock", "", 200, nil)
	if strings.Count(string(got), ">true<") != 1 || strings.Count(string(got), ">false<") != 3 {
		t.Fatal(string(got))
	}
	status(t, s, "DELETE", "/config-bucket?publicAccessBlock", "", 204, nil)
	status(t, s, "GET", "/config-bucket?publicAccessBlock", "", 404, nil)
	status(t, s, "DELETE", "/config-bucket?publicAccessBlock", "", 204, nil)
	for _, tc := range []struct {
		path, body string
		want       int
	}{
		{"versioning", `<VersioningConfiguration><Status>Suspended</Status></VersioningConfiguration>`, 200},
		{"versioning", `<VersioningConfiguration><Status>Enabled</Status><MfaDelete>Disabled</MfaDelete></VersioningConfiguration>`, 501},
		{"versioning", `<VersioningConfiguration><Status>Disabled</Status></VersioningConfiguration>`, 400},
		{"versioning", `<VersioningConfiguration><Status>Enabled</Status><Status>Suspended</Status></VersioningConfiguration>`, 400},
		{"versioning", `<VersioningConfiguration><Status><Nested>Enabled</Nested></Status></VersioningConfiguration>`, 400},
		{"versioning", `<VersioningConfiguration><Status>Enabled</Status><Ignored>true</Ignored></VersioningConfiguration>`, 400},
		{"versioning", `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration><extra/>`, 400},
		{"versioning", `<VersioningConfiguration wrong="true"><Status>Enabled</Status></VersioningConfiguration>`, 400},
		{"publicAccessBlock", `<PublicAccessBlockConfiguration><BlockPublicPolicy>yes</BlockPublicPolicy></PublicAccessBlockConfiguration>`, 400},
		{"publicAccessBlock", `<PublicAccessBlockConfiguration><Unknown>true</Unknown></PublicAccessBlockConfiguration>`, 400},
	} {
		t.Run(tc.path+fmt.Sprint(tc.want)+tc.body, func(t *testing.T) { status(t, s, "PUT", "/config-bucket?"+tc.path, tc.body, tc.want, nil) })
	}
	for _, path := range []string{"?versions&max-keys=-1", "?versions&max-keys=1001", "?versions&max-keys=no", "?versions&encoding-type=bad", "?versions&version-id-marker=no", "?versions&key-marker=x&version-id-marker=no", "?versions&versioning", "?versioning&list-type=2", "?versioning=Enabled", "?versioning&versioning", "?versionId=null", "?key-marker=x", "/key?versionId=", "/key?versioning"} {
		status(t, s, "GET", "/config-bucket"+path, "", status400Or501(path), nil)
	}
	status(t, s, "GET", "/config-bucket/key?versionId="+strings.Repeat("x", 129), "", 400, nil)
	status(t, s, "DELETE", "/config-bucket/key?versionId=%0D%0Ainvalid", "", 400, nil)
	status(t, s, "PUT", "/config-bucket/key?versionId=null", "", 400, nil)
	status(t, s, "POST", "/config-bucket?versions", "", 405, nil)
	status(t, s, "DELETE", "/config-bucket?versioning", "", 405, nil)
	status(t, s, "PUT", "/config-bucket?versioning", `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`, 400, map[string]string{"Content-MD5": "wrong"})
	status(t, s, "DELETE", "/config-bucket/key", "", 501, map[string]string{"If-Match": "*"})
	// Public ACLs/policies remain rejected regardless of these four stored flags.
	status(t, s, "PUT", "/config-bucket/key", "x", 400, map[string]string{"Content-Type": strings.Repeat("x", 1025)})
	status(t, s, "GET", "/config-bucket/key", "", 404, nil)
	status(t, s, "PUT", "/config-bucket/key", "x", 501, map[string]string{"x-amz-acl": "public-read"})
	status(t, s, "PUT", "/config-bucket?policy", "{}", 501, nil)
}
func status400Or501(path string) int {
	if path == "/key?versioning" {
		return 501
	}
	return 400
}

func TestS3ProviderStateFailureDoesNotLeakDatabaseDetails(t *testing.T) {
	h, closeState, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err = closeState(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path string }{
		{"PUT", "/closed-state"}, {"DELETE", "/closed-state"}, {"PUT", "/closed-state/key"}, {"GET", "/closed-state/key"}, {"DELETE", "/closed-state/key"}, {"GET", "/closed-state?versioning"}, {"PUT", "/closed-state?versioning"}, {"GET", "/closed-state?versions"}, {"GET", "/closed-state?publicAccessBlock"},
	} {
		w := httptest.NewRecorder()
		body := strings.NewReader("")
		if tc.method == "PUT" && strings.Contains(tc.path, "?versioning") {
			body = strings.NewReader(`<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)
		}
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, body))
		if w.Code != 500 || !strings.Contains(w.Body.String(), "InternalError") || strings.Contains(w.Body.String(), "sql:") || strings.Contains(w.Body.String(), "SELECT") {
			t.Fatalf("state failure leaked or succeeded: %s %s %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}
