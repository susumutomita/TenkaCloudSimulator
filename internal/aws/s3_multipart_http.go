package aws

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func multipartIntent(q url.Values) bool {
	for _, k := range []string{"uploads", "uploadId", "partNumber", "max-parts", "part-number-marker", "max-uploads", "upload-id-marker"} {
		if q.Has(k) {
			return true
		}
	}
	return false
}
func mpInteger(q url.Values, key string, fallback, max int) (int, error) {
	if !q.Has(key) {
		return fallback, nil
	}
	n, err := strconv.Atoi(q.Get(key))
	if err != nil || n < 0 || n > max {
		return 0, core.Fail("InvalidArgument", "Invalid "+key+".")
	}
	return n, nil
}

// Parse the exact supported completion grammar, rejecting ignored extension
// fields, duplicates, trailing documents, DTDs and wrong namespaces.
func multipartXML(body []byte) ([]S3Part, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	depth := 0
	closed := false
	var parts []S3Part
	var fields map[string]string
	field := ""
	invalid := func() ([]S3Part, error) {
		return nil, core.Fail("MalformedXML", "Expected CompleteMultipartUpload with Part/PartNumber/ETag only.")
	}
	for {
		token, err := dec.Token()
		if err == io.EOF {
			if closed {
				return parts, nil
			}
			return invalid()
		}
		if err != nil {
			return invalid()
		}
		switch t := token.(type) {
		case xml.StartElement:
			if closed || t.Name.Space != "" && t.Name.Space != ns {
				return invalid()
			}
			switch depth {
			case 0:
				if t.Name.Local != "CompleteMultipartUpload" {
					return invalid()
				}
				for _, a := range t.Attr {
					if a.Name.Local != "xmlns" {
						return invalid()
					}
				}
			case 1:
				if t.Name.Local != "Part" || len(t.Attr) != 0 || len(parts) >= 10000 {
					return invalid()
				}
				fields = map[string]string{}
			case 2:
				if (t.Name.Local != "PartNumber" && t.Name.Local != "ETag") || len(t.Attr) != 0 {
					return invalid()
				}
				if _, ok := fields[t.Name.Local]; ok {
					return invalid()
				}
				field = t.Name.Local
				fields[field] = ""
			default:
				return invalid()
			}
			depth++
		case xml.CharData:
			if depth == 3 {
				if len(fields[field])+len(t) > 64 {
					return invalid()
				}
				fields[field] += string(t)
			} else if strings.TrimSpace(string(t)) != "" {
				return invalid()
			}
		case xml.EndElement:
			depth--
			if depth == 1 {
				n, err := strconv.Atoi(strings.TrimSpace(fields["PartNumber"]))
				if err != nil || len(fields) != 2 {
					return invalid()
				}
				parts = append(parts, S3Part{Number: n, ETag: strings.TrimSpace(fields["ETag"])})
			}
			if depth == 0 {
				closed = true
			}
		case xml.Comment:
		case xml.ProcInst:
			if depth != 0 || closed || t.Target != "xml" {
				return invalid()
			}
		default:
			return invalid()
		}
	}
}
func (g *Gateway) multipart(w http.ResponseWriter, r *http.Request, bucket, key string) {
	q := r.URL.Query()
	op := ""
	allowed := map[string]bool{"x-id": true}
	switch {
	case key == "" && q.Has("uploads") && r.Method == "GET":
		op = "list"
		for _, k := range []string{"uploads", "prefix", "max-uploads", "key-marker", "upload-id-marker"} {
			allowed[k] = true
		}
	case key != "" && q.Has("uploads") && r.Method == "POST":
		op = "create"
		allowed["uploads"] = true
	case key != "" && q.Has("uploadId"):
		allowed["uploadId"] = true
		switch r.Method {
		case "PUT":
			op = "part"
			allowed["partNumber"] = true
		case "POST":
			op = "complete"
		case "DELETE":
			op = "abort"
		case "GET":
			op = "parts"
			allowed["max-parts"] = true
			allowed["part-number-marker"] = true
		}
	}
	if op == "" {
		fail(w, r, "InvalidArgument", "Invalid multipart method or resource.")
		return
	}
	for k := range q {
		if !allowed[k] {
			fail(w, r, "NotImplemented", "Unsupported multipart query: "+k)
			return
		}
	}
	if q.Has("uploads") && q.Get("uploads") != "" || q.Has("uploadId") && (len(q.Get("uploadId")) != 32 || !validText(q.Get("uploadId"))) {
		fail(w, r, "InvalidArgument", "Invalid multipart upload identifier.")
		return
	}
	for k := range r.Header {
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "x-amz-checksum-") || lower == "x-amz-sdk-checksum-algorithm" || strings.HasPrefix(lower, "if-") {
			fail(w, r, "NotImplemented", "Multipart conditional/checksum extensions are unsupported; use Content-MD5 for a part.")
			return
		}
	}
	if g.S3 == nil {
		fail(w, r, "InternalError", "S3 multipart state is unavailable.")
		return
	}
	region, err := apiScopeRegion(r, "s3", "")
	if err != nil {
		fail(w, r, "InvalidArgument", "Invalid multipart service or region scope.")
		return
	}
	b, err := g.Store.BucketContext(r.Context(), bucket)
	if err != nil {
		fromError(w, r, err)
		return
	}
	if b.Region != region {
		w.Header().Set("x-amz-bucket-region", b.Region)
		fail(w, r, "IllegalLocationConstraintException", "Use the bucket's region for multipart operations.")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), multipartScopeKey{}, multipartBucketScope{region, b.Created.Format(time.RFC3339Nano)}))
	id := q.Get("uploadId")
	if op == "list" || op == "parts" || op == "abort" {
		body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
		if e != nil || len(body) != 0 {
			fail(w, r, "InvalidArgument", "This multipart operation requires an empty body.")
			return
		}
	}
	switch op {
	case "create":
		for _, header := range []string{"Cache-Control", "Content-Disposition", "Content-Encoding", "Content-Language", "Expires"} {
			if r.Header.Get(header) != "" {
				fail(w, r, "NotImplemented", "Unsupported multipart object metadata: "+header)
				return
			}
		}
		body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
		if e != nil || len(body) != 0 {
			fail(w, r, "InvalidArgument", "CreateMultipartUpload requires an empty body.")
			return
		}
		ct := r.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		u, e := g.S3.CreateUpload(r.Context(), bucket, key, ct)
		if e != nil {
			fromError(w, r, e)
			return
		}
		writeXML(w, 200, struct {
			XMLName               xml.Name `xml:"InitiateMultipartUploadResult"`
			XMLNS                 string   `xml:"xmlns,attr"`
			Bucket, Key, UploadId string
		}{XMLNS: ns, Bucket: bucket, Key: key, UploadId: u.ID})
	case "part":
		n, e := mpInteger(q, "partNumber", 0, 10000)
		if e != nil || n == 0 {
			fail(w, r, "InvalidArgument", "partNumber must be 1–10000.")
			return
		}
		data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, core.MaxObjectBytes))
		if e != nil {
			fail(w, r, "EntityTooLarge", "Part exceeds the 8 MiB simulator limit.")
			return
		}
		if e = checksums(r, data); e != nil {
			fail(w, r, "BadDigest", e.Error())
			return
		}
		part, e := g.S3.UploadPart(r.Context(), bucket, key, id, n, data)
		if e != nil {
			fromError(w, r, e)
			return
		}
		w.Header().Set("ETag", part.ETag)
		w.WriteHeader(200)
	case "abort":
		if e := g.S3.AbortUpload(r.Context(), bucket, key, id); e != nil {
			fromError(w, r, e)
			return
		}
		w.WriteHeader(204)
	case "complete":
		body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if e != nil {
			fail(w, r, "MalformedXML", "Completion XML exceeds 1 MiB.")
			return
		}
		if e = checksums(r, body); e != nil {
			fail(w, r, "BadDigest", e.Error())
			return
		}
		parts, e := multipartXML(body)
		if e != nil {
			fromError(w, r, e)
			return
		}
		v, e := g.S3.CompleteUpload(r.Context(), bucket, key, id, parts)
		if e != nil {
			fromError(w, r, e)
			return
		}
		if v.ID != "" {
			w.Header().Set("x-amz-version-id", v.ID)
		}
		writeXML(w, 200, struct {
			XMLName                     xml.Name `xml:"CompleteMultipartUploadResult"`
			XMLNS                       string   `xml:"xmlns,attr"`
			Location, Bucket, Key, ETag string
		}{XMLNS: ns, Location: "/" + bucket + "/" + url.PathEscape(key), Bucket: bucket, Key: key, ETag: v.ETag})
	case "parts":
		limit, e := mpInteger(q, "max-parts", 1000, 1000)
		if e != nil {
			fromError(w, r, e)
			return
		}
		marker, e := mpInteger(q, "part-number-marker", 0, 10000)
		if e != nil {
			fromError(w, r, e)
			return
		}
		parts, e := g.S3.Parts(r.Context(), bucket, key, id)
		if e != nil {
			fromError(w, r, e)
			return
		}
		page := []S3Part{}
		for _, p := range parts {
			if p.Number > marker {
				page = append(page, p)
			}
		}
		truncated := limit > 0 && len(page) > limit
		if limit == 0 {
			page = nil
		} else if truncated {
			page = page[:limit]
		}
		next := 0
		if truncated {
			next = page[len(page)-1].Number
		}
		writeXML(w, 200, struct {
			XMLName               xml.Name `xml:"ListPartsResult"`
			XMLNS                 string   `xml:"xmlns,attr"`
			Bucket, Key, UploadId string
			PartNumberMarker      int
			NextPartNumberMarker  int `xml:",omitempty"`
			MaxParts              int
			IsTruncated           bool
			Parts                 []S3Part `xml:"Part"`
		}{XMLNS: ns, Bucket: bucket, Key: key, UploadId: id, PartNumberMarker: marker, NextPartNumberMarker: next, MaxParts: limit, IsTruncated: truncated, Parts: page})
	case "list":
		limit, e := mpInteger(q, "max-uploads", 1000, 1000)
		if e != nil {
			fromError(w, r, e)
			return
		}
		prefix, km, um := q.Get("prefix"), q.Get("key-marker"), q.Get("upload-id-marker")
		if len(prefix) > 1024 || len(km) > 1024 || len(um) > 128 || !validText(prefix+km+um) || um != "" && km == "" {
			fail(w, r, "InvalidArgument", "Invalid multipart listing cursor.")
			return
		}
		uploads, e := g.S3.Uploads(r.Context(), bucket)
		if e != nil {
			fromError(w, r, e)
			return
		}
		page := []S3Upload{}
		markerIndex := -1
		if um != "" {
			for i, u := range uploads {
				if u.Key == km && u.ID == um {
					markerIndex = i
					break
				}
			}
			if markerIndex < 0 {
				fail(w, r, "InvalidArgument", "Multipart cursor upload no longer exists; restart the listing.")
				return
			}
		}
		for i, u := range uploads {
			if strings.HasPrefix(u.Key, prefix) && (km == "" || u.Key > km || u.Key == km && um != "" && i > markerIndex) {
				page = append(page, u)
			}
		}
		truncated := limit > 0 && len(page) > limit
		nk, nu := "", ""
		if limit == 0 {
			page = nil
		} else if truncated {
			page = page[:limit]
			last := page[len(page)-1]
			nk, nu = last.Key, last.ID
		}
		writeXML(w, 200, struct {
			XMLName                           xml.Name `xml:"ListMultipartUploadsResult"`
			XMLNS                             string   `xml:"xmlns,attr"`
			Bucket, KeyMarker, UploadIdMarker string
			NextKeyMarker                     string `xml:",omitempty"`
			NextUploadIdMarker                string `xml:",omitempty"`
			Prefix                            string
			MaxUploads                        int
			IsTruncated                       bool
			Uploads                           []S3Upload `xml:"Upload"`
		}{XMLNS: ns, Bucket: bucket, KeyMarker: km, UploadIdMarker: um, NextKeyMarker: nk, NextUploadIdMarker: nu, Prefix: prefix, MaxUploads: limit, IsTruncated: truncated, Uploads: page})
	}
}
