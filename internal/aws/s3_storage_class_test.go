package aws

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestS3ExplicitStandardStorageClassAdmissionAndRefusalFrame(t *testing.T) {
	db, store := s3TestState(t)
	s3Enable(t, store, "Enabled")
	g := &Gateway{Store: db, S3: store}

	wire := func(method, path, body string, classes []string, extra http.Header) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		for _, class := range classes {
			req.Header.Add("X-Amz-Storage-Class", class)
		}
		for name, values := range extra {
			for _, value := range values {
				req.Header.Add(name, value)
			}
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, req)
		return w
	}
	ok := func(method, path, body string, classes ...string) *httptest.ResponseRecorder {
		t.Helper()
		w := wire(method, path, body, classes, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s = %d: %s", method, path, w.Code, w.Body.String())
		}
		if w.Header().Get("X-Amz-Storage-Class") != "" {
			t.Fatalf("%s %s gained a storage-class response header", method, path)
		}
		return w
	}
	const bucket = "/versions-test"
	const key = bucket + "/ordinary.txt"
	const multipart = bucket + "/staged.txt"
	implicit := ok("PUT", key, "same persisted content")
	explicit := ok("PUT", key+"?x-id=PutObject", "same persisted content", "STANDARD")
	if implicit.Header().Get("ETag") == "" || implicit.Header().Get("ETag") != explicit.Header().Get("ETag") ||
		implicit.Header().Get("X-Amz-Version-Id") == "" || explicit.Header().Get("X-Amz-Version-Id") == "" ||
		implicit.Header().Get("X-Amz-Version-Id") == explicit.Header().Get("X-Amz-Version-Id") {
		t.Fatal("explicit STANDARD did not use the ordinary hash and version path")
	}
	var upload struct {
		UploadID string `xml:"UploadId"`
	}
	start := ok("POST", multipart+"?uploads&x-id=CreateMultipartUpload", "", "STANDARD")
	if err := xml.Unmarshal(start.Body.Bytes(), &upload); err != nil || len(upload.UploadID) != 32 {
		t.Fatal("explicit STANDARD did not return a stored upload", err)
	}
	target := multipart + "?uploadId=" + upload.UploadID
	part := ok("PUT", target+"&partNumber=1", "retained part")
	complete := "<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>" +
		part.Header().Get("ETag") + "</ETag></Part></CompleteMultipartUpload>"

	snapshot := func() []string {
		return []string{
			ok("GET", "/", "").Body.String(),
			ok("GET", key, "").Body.String(),
			ok("GET", bucket+"?versions", "").Body.String(),
			ok("GET", bucket+"?uploads", "").Body.String(),
			ok("GET", target, "").Body.String(),
		}
	}
	before := snapshot()
	if !strings.Contains(before[2], "<StorageClass>STANDARD</StorageClass>") {
		t.Fatal("expected the existing canonical STANDARD listing")
	}
	for _, test := range []struct {
		name, method, path, body string
		classes                  []string
		extra                    http.Header
	}{
		{"archive", "PUT", key, "untouched", []string{"GLACIER"}, nil},
		{"deep archive initiation", "POST", multipart + "?uploads", "", []string{"DEEP_ARCHIVE"}, nil},
		{"wrong spelling", "PUT", key, "untouched", []string{"standard"}, nil},
		{"empty", "PUT", key, "untouched", []string{""}, nil},
		{"same repeated", "PUT", key, "untouched", []string{"STANDARD", "STANDARD"}, nil},
		{"different repeated", "POST", multipart + "?uploads", "", []string{"STANDARD", "GLACIER"}, nil},
		{"comma separated", "PUT", key, "untouched", []string{"STANDARD,GLACIER"}, nil},
		{"read object", "GET", key, "", []string{"STANDARD"}, nil},
		{"create bucket", "PUT", "/otherwise-valid-bucket", "", []string{"STANDARD"}, nil},
		{"missing object key", "POST", bucket + "/?uploads", "", []string{"STANDARD"}, nil},
		{"malformed routing hint", "PUT", key + "?x-id=%z", "untouched", []string{"STANDARD"}, nil},
		{"version selector", "PUT", key + "?versionId=not-a-put-option", "untouched", []string{"STANDARD"}, nil},
		{"missing initiation selector", "POST", multipart, "", []string{"STANDARD"}, nil},
		{"valued initiation flag", "POST", multipart + "?uploads=yes", "", []string{"STANDARD"}, nil},
		{"mixed selectors", "POST", target + "&uploads", "", []string{"STANDARD"}, nil},
		{"upload part", "PUT", target + "&partNumber=1", "would replace the part", []string{"STANDARD"}, nil},
		{"complete", "POST", target, complete, []string{"STANDARD"}, nil},
		{"abort", "DELETE", target, "", []string{"STANDARD"}, nil},
		{"copy", "PUT", key, "", []string{"STANDARD"}, http.Header{"X-Amz-Copy-Source": {key}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := wire(test.method, test.path, test.body, test.classes, test.extra)
			var rejected failure
			if err := xml.Unmarshal(w.Body.Bytes(), &rejected); err != nil ||
				w.Code != http.StatusNotImplemented || rejected.Code != "NotImplemented" {
				t.Fatalf("unsupported input = %d %s; XML error %v", w.Code, w.Body.String(), err)
			}
			if after := snapshot(); !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal changed the bucket, object, version, upload or part inventory: %s", test.name)
			}
		})
	}
}
