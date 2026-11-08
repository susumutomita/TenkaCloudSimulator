package server

import (
	"encoding/xml"
	"fmt"
	"strings"
	"testing"
)

func TestS3MultipartHTTPAdmissionPaginationAndIAM(t *testing.T) {
	s := instance(t)
	status(t, s, "PUT", "/multipart-bucket", "", 200, nil)
	start := func(key string) string {
		t.Helper()
		body := status(t, s, "POST", "/multipart-bucket/"+key+"?uploads", "", 200, map[string]string{"Content-Type": "text/plain"})
		var u struct {
			ID string `xml:"UploadId"`
		}
		if err := xml.Unmarshal(body, &u); err != nil || len(u.ID) != 32 {
			t.Fatal(string(body), err)
		}
		return u.ID
	}
	id := start("key")
	other := start("key")
	start("z")
	body := status(t, s, "GET", "/multipart-bucket?uploads&max-uploads=1", "", 200, nil)
	var list struct {
		Truncated bool   `xml:"IsTruncated"`
		Key       string `xml:"NextKeyMarker"`
		ID        string `xml:"NextUploadIdMarker"`
	}
	if e := xml.Unmarshal(body, &list); e != nil || !list.Truncated || list.Key != "key" || list.ID == "" {
		t.Fatal(string(body), e)
	}
	status(t, s, "GET", "/multipart-bucket?uploads&max-uploads=1&key-marker="+list.Key+"&upload-id-marker="+list.ID, "", 200, nil)
	status(t, s, "GET", "/multipart-bucket?uploads&max-uploads=0", "", 200, nil)
	part := func(n string, data string) string {
		t.Helper()
		r, _ := call(t, s, "PUT", "/multipart-bucket/key?uploadId="+id+"&partNumber="+n, data, nil)
		if r.StatusCode != 200 {
			t.Fatal(r.StatusCode)
		}
		return r.Header.Get("ETag")
	}
	etag := part("1", "payload")
	part("2", "second")
	b := status(t, s, "GET", "/multipart-bucket/key?uploadId="+id+"&max-parts=1", "", 200, nil)
	if !strings.Contains(string(b), "<NextPartNumberMarker>1</NextPartNumberMarker>") {
		t.Fatal(string(b))
	}
	status(t, s, "GET", "/multipart-bucket/key?uploadId="+id+"&max-parts=0", "", 200, nil)
	status(t, s, "GET", "/multipart-bucket/key?uploadId="+id+"&part-number-marker=1", "", 200, nil)
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:DeleteObject"],"Resource":"*"}]}`
	status(t, s, "PUT", "/_simulator/v1/iam/principals/mp-reader/policy", policy, 200, nil)
	reader := map[string]string{"X-Simulator-Principal": "mp-reader"}
	for _, op := range []struct{ method, path, body string }{{"POST", "/multipart-bucket/denied?uploads", ""}, {"GET", "/multipart-bucket?uploads", ""}, {"GET", "/multipart-bucket/key?uploadId=" + id, ""}, {"DELETE", "/multipart-bucket/key?uploadId=" + id, ""}, {"POST", "/multipart-bucket/key?uploadId=" + id, "<CompleteMultipartUpload/>"}, {"PUT", "/multipart-bucket/key?uploadId=" + id + "&partNumber=1", "forbidden"}} {
		status(t, s, op.method, op.path, op.body, 403, reader)
	}
	for _, header := range []string{"Cache-Control", "Content-Disposition", "Content-Encoding", "Content-Language", "Expires"} {
		status(t, s, "POST", "/multipart-bucket/key?uploads", "", 501, map[string]string{header: "unsupported"})
	}
	// Browser cache policy is not a request to persist unsupported S3 object
	// metadata. The console sends an explicit empty value, while nonempty
	// metadata remains rejected rather than silently discarded.
	for _, value := range []string{"no-cache", "no-store", "max-age=60"} {
		status(t, s, "POST", "/multipart-bucket/key?uploads", "", 501, map[string]string{"Cache-Control": value})
	}
	emptyCache := status(t, s, "POST", "/multipart-bucket/browser?uploads", "", 200, map[string]string{"Cache-Control": ""})
	var browserUpload struct {
		ID string `xml:"UploadId"`
	}
	if err := xml.Unmarshal(emptyCache, &browserUpload); err != nil || len(browserUpload.ID) != 32 {
		t.Fatal(string(emptyCache), err)
	}
	status(t, s, "DELETE", "/multipart-bucket/browser?uploadId="+browserUpload.ID, "", 204, nil)
	for _, tt := range []struct {
		method, path, body string
		code               int
		headers            map[string]string
	}{
		{"PUT", "/multipart-bucket/key?uploadId=" + id + "&partNumber=0", "x", 400, nil},
		{"PUT", "/multipart-bucket/key?uploadId=" + id + "&partNumber=1", "x", 400, map[string]string{"Content-MD5": "bad"}},
		{"POST", "/multipart-bucket/key?uploads", "x", 400, nil},
		{"GET", "/multipart-bucket?uploads", "x", 400, nil},
		{"POST", "/multipart-bucket/key?uploads=bad", "", 400, nil},
		{"GET", "/multipart-bucket/key?uploadId=bad", "", 400, nil},
		{"GET", "/multipart-bucket/key?partNumber=1", "", 400, nil},
		{"GET", "/multipart-bucket?uploads&max-uploads=1001", "", 400, nil},
		{"GET", "/multipart-bucket?uploads&upload-id-marker=x", "", 400, nil},
		{"GET", "/multipart-bucket?uploads&delimiter=/", "", 501, nil},
		{"GET", "/multipart-bucket/key?uploadId=" + id + "&max-parts=-1", "", 400, nil},
		{"POST", "/multipart-bucket/key?uploadId=" + id, "<CompleteMultipartUpload/>", 400, nil},
		{"GET", "/multipart-bucket/key?uploadId=" + id, "", 501, map[string]string{"If-Match": "*"}},
		{"POST", "/multipart-bucket/key?uploads", "", 501, map[string]string{"x-amz-checksum-algorithm": "SHA256"}},
		{"POST", "/multipart-bucket/key?uploads", "", 400, map[string]string{"X-Simulator-Region": "us-west-2"}},
		{"POST", "/multipart-bucket/key?uploads", "", 400, map[string]string{"X-Simulator-Region": "unknown"}},
		{"GET", "/multipart-bucket/neighbor?uploadId=" + id, "", 404, nil},
	} {
		t.Run(fmt.Sprint(tt.method, tt.path, tt.code), func(t *testing.T) { status(t, s, tt.method, tt.path, tt.body, tt.code, tt.headers) })
	}
	for _, bad := range []string{`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>` + strings.Repeat("x<!-- -->", 10000) + `</ETag></Part></CompleteMultipartUpload>`, "", `<bad/>`, `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>x</ETag><Unknown>y</Unknown></Part></CompleteMultipartUpload>`, `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><PartNumber>2</PartNumber><ETag>x</ETag></Part></CompleteMultipartUpload>`, `<CompleteMultipartUpload xmlns="wrong"/>`, `<!DOCTYPE x><CompleteMultipartUpload/>`, `<CompleteMultipartUpload/><CompleteMultipartUpload/>`, `<CompleteMultipartUpload><Part><PartNumber>x</PartNumber></Part></CompleteMultipartUpload>`} {
		status(t, s, "POST", "/multipart-bucket/key?uploadId="+id, bad, 400, nil)
	}
	status(t, s, "DELETE", "/multipart-bucket", "", 409, nil)
	complete := `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>` + etag + `</ETag></Part></CompleteMultipartUpload>`
	status(t, s, "POST", "/multipart-bucket/key?uploadId="+id, complete, 200, nil)
	status(t, s, "GET", "/multipart-bucket/key?uploadId="+id, "", 404, nil)
	if b := status(t, s, "GET", "/multipart-bucket/key", "", 200, nil); string(b) != "payload" {
		t.Fatal(string(b))
	}
	status(t, s, "DELETE", "/multipart-bucket/key?uploadId="+other, "", 204, nil)
	status(t, s, "DELETE", "/multipart-bucket/key", "", 204, nil)
}
