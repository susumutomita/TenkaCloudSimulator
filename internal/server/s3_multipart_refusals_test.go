package server

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"testing"
)

func TestS3MultipartHTTPRefusesMissingStateAndBadDigests(t *testing.T) {
	s := instance(t)
	status(t, s, "POST", "/missing-bucket/key?uploads", "", 404, nil)
	status(t, s, "PUT", "/mp-refusals", "", 200, nil)
	missing := "0123456789abcdef0123456789abcdef"
	status(t, s, "PUT", "/mp-refusals/key?uploadId="+missing+"&partNumber=1", "data", 404, nil)
	status(t, s, "DELETE", "/mp-refusals/key?uploadId="+missing, "", 404, nil)
	status(t, s, "PUT", "/mp-refusals?versioning", `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Enabled</Status></VersioningConfiguration>`, 200, nil)
	start := func(key string) string {
		t.Helper()
		var u struct {
			ID string `xml:"UploadId"`
		}
		if err := xml.Unmarshal(status(t, s, "POST", "/mp-refusals/"+key+"?uploads", "", 200, nil), &u); err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	id := start("key")
	status(t, s, "GET", "/mp-refusals/key?uploadId="+id+"&part-number-marker=10001", "", 400, nil)
	status(t, s, "GET", "/mp-refusals/key?uploadId="+id+"&part-number-marker=x", "", 400, nil)
	r, _ := call(t, s, "PUT", "/mp-refusals/key?uploadId="+id+"&partNumber=1", "payload", nil)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	complete := `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>` + r.Header.Get("ETag") + `</ETag></Part></CompleteMultipartUpload>`
	wrong := md5.Sum([]byte("other"))
	status(t, s, "POST", "/mp-refusals/key?uploadId="+id, complete, 400, map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString(wrong[:])})
	// A listing cursor naming an upload that has since finished fails closed.
	other := start("other")
	status(t, s, "DELETE", "/mp-refusals/other?uploadId="+other, "", 204, nil)
	status(t, s, "GET", "/mp-refusals?uploads&key-marker=other&upload-id-marker="+other, "", 400, nil)
	good := md5.Sum([]byte(complete))
	resp, _ := call(t, s, "POST", "/mp-refusals/key?uploadId="+id, complete, map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString(good[:])})
	if resp.StatusCode != 200 || resp.Header.Get("x-amz-version-id") == "" {
		t.Fatal("versioned completion", resp.StatusCode, resp.Header)
	}
	if b := status(t, s, "GET", "/mp-refusals/key", "", 200, nil); string(b) != "payload" {
		t.Fatal(string(b))
	}
}
