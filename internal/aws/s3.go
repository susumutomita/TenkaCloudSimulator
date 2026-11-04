// Package aws adapts the declared S3 REST subset to the shared object store.
package aws

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const ns = "http://s3.amazonaws.com/doc/2006-03-01/"

var names = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var sequence atomic.Uint64
var Regions = []string{"us-east-1", "us-west-2", "ap-northeast-1", "eu-west-1"}

type Gateway struct {
	Secrets           *SecretStore
	APIGateway        *APIGatewayStore
	LambdaPermissions *LambdaPermissionStore
	Store             *core.Store
	S3                *S3Store
	Identities        *core.Identities
	Parameters        *ParameterStore
	Stacks            *StackStore
	Lambdas           *LambdaStore
	Roles             *IAMRoleStore
}
type failure struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string
	Message   string
	Resource  string
	RequestID string `xml:"RequestId"`
}

func writeXML(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, r *http.Request, code, message string) {
	status := http.StatusBadRequest
	switch code {
	case "InternalError":
		status = 500
	case "NoSuchBucket", "NoSuchKey", "NoSuchVersion", "NoSuchPublicAccessBlockConfiguration", "NoSuchTagSet":
		status = 404
	case "BucketNotEmpty", "BucketAlreadyExists", "BucketAlreadyOwnedByYou":
		status = 409
	case "NotImplemented":
		status = 501
	case "MethodNotAllowed":
		status = 405
	case "AccessDenied":
		status = 403
	case "EntityTooLarge":
		status = 413
	case "ServiceUnavailable":
		status = 503
	case "PreconditionFailed":
		status = 412
	case "InvalidRange":
		status = 416
	}
	writeXML(w, status, failure{Code: code, Message: message, Resource: r.URL.Path, RequestID: w.Header().Get("x-amz-request-id")})
}
func fromError(w http.ResponseWriter, r *http.Request, err error) {
	if core.Code(err) == "InternalError" {
		slog.Error("S3 state operation failed", "error", err)
		fail(w, r, "InternalError", "Simulator S3 state operation failed.")
		return
	}
	fail(w, r, core.Code(err), err.Error())
}
func ValidBucket(name string) bool {
	return names.MatchString(name) && net.ParseIP(name) == nil && !strings.Contains(name, "..") && !strings.Contains(name, ".-") && !strings.Contains(name, "-.") && !strings.HasPrefix(name, "xn--") && !strings.HasSuffix(name, "-s3alias") && !strings.HasSuffix(name, "--ol-s3")
}
func validText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if c < 32 || c == 0xfffe || c == 0xffff {
			return false
		}
	}
	return true
}
func (g *Gateway) serveS3(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("x-amz-request-id", fmt.Sprintf("TCSIM%016x", sequence.Add(1)))
	if a := r.Header.Get("Authorization"); a != "" {
		_, credential, ok := strings.Cut(a, "Credential=")
		key, _, _ := strings.Cut(credential, "/")
		if !ok || !strings.HasPrefix(key, "TCSIM") {
			fail(w, r, "AccessDenied", "Use simulator-only TCSIM credentials. AWS signature verification and sessions are unsupported.")
			return
		}
	}
	if r.Header.Get("X-Amz-Security-Token") != "" || r.URL.Query().Get("X-Amz-Credential") != "" {
		fail(w, r, "NotImplemented", "Session tokens and presigned requests are not supported.")
		return
	}
	for k, values := range r.URL.Query() {
		if len(values) != 1 {
			fail(w, r, "InvalidArgument", "Duplicate query parameters are unsupported.")
			return
		}
		switch k {
		case "list-type", "prefix", "delimiter", "max-keys", "continuation-token", "start-after", "encoding-type", "location", "x-id", "versioning", "publicAccessBlock", "versions", "versionId", "key-marker", "version-id-marker", "tagging":
		default:
			fail(w, r, "NotImplemented", "Unsupported S3 query: "+k)
			return
		}
	}
	if r.Header.Get("Content-Encoding") == "aws-chunked" || strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
		fail(w, r, "NotImplemented", "Streaming signatures are not supported. Use a buffered body and optional checksums.")
		return
	}
	if r.Method == "DELETE" && (r.Header.Get("If-Match") != "" || r.Header.Get("If-None-Match") != "") {
		fail(w, r, "NotImplemented", "Conditional DELETE is unsupported.")
		return
	}
	principal := r.Header.Get("x-simulator-principal")
	if principal == "" {
		principal = core.Administrator
	}
	if g.Identities != nil {
		action, resource := permission(r)
		decision, err := g.Identities.DecisionContext(r.Context(), principal, action, resource)
		if err != nil {
			fail(w, r, "InternalError", "Identity state could not be read.")
			return
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			fail(w, r, "AccessDenied", "IAM evaluation: "+decision+" for "+principal+" ("+action+").")
			return
		}
	}
	for _, header := range []string{"x-amz-expected-bucket-owner", "x-amz-request-payer", "x-amz-optional-object-attributes", "x-amz-copy-source", "x-amz-acl", "x-amz-server-side-encryption", "x-amz-tagging", "x-amz-storage-class"} {
		if r.Header.Get(header) != "" {
			fail(w, r, "NotImplemented", "Unsupported option: "+header)
			return
		}
	}
	for name := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-amz-") {
			switch strings.ToLower(name) {
			case "x-amz-date", "x-amz-content-sha256", "x-amz-user-agent", "x-amz-checksum-sha1", "x-amz-checksum-sha256", "x-amz-checksum-crc32", "x-amz-checksum-crc32c", "x-amz-sdk-checksum-algorithm":
			default:
				fail(w, r, "NotImplemented", "Unsupported header: "+name)
				return
			}
		}
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	name, key, object := strings.Cut(path, "/")
	if name == "" {
		for k := range r.URL.Query() {
			if k != "x-id" {
				fail(w, r, "NotImplemented", "Unsupported ListBuckets query: "+k)
				return
			}
		}
		g.root(w, r)
		return
	}
	if !ValidBucket(name) {
		fail(w, r, "InvalidBucketName", "Use a DNS-compatible bucket name of 3–63 characters.")
		return
	}
	if g.S3 == nil && (object || r.Method == "PUT" || r.Method == "DELETE") {
		fail(w, r, "InternalError", "S3 version state is unavailable.")
		return
	}
	if object && key != "" {
		if len(key) > 1024 || !validText(key) {
			fail(w, r, "InvalidArgument", "Object keys must be valid printable UTF-8, at most 1024 bytes.")
			return
		}
		for k := range r.URL.Query() {
			if k != "x-id" && k != "versionId" {
				fail(w, r, "NotImplemented", "Unsupported object query: "+k)
				return
			}
		}
		if r.URL.Query().Has("versionId") && (r.URL.Query().Get("versionId") == "" || len(r.URL.Query().Get("versionId")) > 128 || !validText(r.URL.Query().Get("versionId")) || r.Method == "PUT") {
			fail(w, r, "InvalidArgument", "versionId must be printable UTF-8, 1–128 bytes, with GET, HEAD or DELETE.")
			return
		}
		g.object(w, r, name, key)
		return
	}
	for _, kind := range []string{"versioning", "publicAccessBlock", "versions", "tagging"} {
		if r.URL.Query().Has(kind) {
			allowed := map[string]bool{kind: true, "x-id": true}
			if kind == "versions" {
				for _, k := range []string{"prefix", "delimiter", "max-keys", "encoding-type", "key-marker", "version-id-marker"} {
					allowed[k] = true
				}
			}
			for k := range r.URL.Query() {
				if !allowed[k] {
					fail(w, r, "InvalidArgument", "Incompatible bucket query: "+k)
					return
				}
			}
			if r.URL.Query().Get(kind) != "" {
				fail(w, r, "InvalidArgument", "Bucket subresource must not have a value.")
				return
			}
			if kind == "tagging" {
				g.bucketTags(w, r, name)
			} else if kind == "versions" {
				g.listVersions(w, r, name)
			} else {
				g.bucketConfiguration(w, r, name, kind)
			}
			return
		}
	}
	if r.URL.Query().Has("versionId") || r.URL.Query().Has("key-marker") || r.URL.Query().Has("version-id-marker") {
		fail(w, r, "InvalidArgument", "Version cursors require ListObjectVersions.")
		return
	}
	if r.Method != "GET" {
		for k := range r.URL.Query() {
			if k != "x-id" {
				fail(w, r, "NotImplemented", "Unsupported bucket query: "+k)
				return
			}
		}
	}
	g.bucket(w, r, name)
}
func (g *Gateway) root(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, r, "MethodNotAllowed", "Use GET to list buckets.")
		return
	}
	type item struct {
		Name         string
		CreationDate string
		BucketRegion string
	}
	result := struct {
		XMLName xml.Name `xml:"ListAllMyBucketsResult"`
		XMLNS   string   `xml:"xmlns,attr"`
		Buckets []item   `xml:"Buckets>Bucket"`
	}{XMLNS: ns, Buckets: []item{}}
	buckets, err := g.Store.Buckets()
	if err != nil {
		fromError(w, r, err)
		return
	}
	for _, b := range buckets {
		result.Buckets = append(result.Buckets, item{b.Name, b.Created.Format(time.RFC3339), b.Region})
	}
	writeXML(w, 200, result)
}
func (g *Gateway) bucket(w http.ResponseWriter, r *http.Request, name string) {
	if r.URL.Query().Has("location") {
		if r.Method != "GET" {
			fail(w, r, "NotImplemented", "Only GetBucketLocation is supported.")
			return
		}
		b, err := g.Store.BucketContext(r.Context(), name)
		if err != nil {
			fromError(w, r, err)
			return
		}
		region := b.Region
		if region == "us-east-1" {
			region = ""
		}
		writeXML(w, 200, struct {
			XMLName xml.Name `xml:"LocationConstraint"`
			XMLNS   string   `xml:"xmlns,attr"`
			Region  string   `xml:",chardata"`
		}{XMLNS: ns, Region: region})
		return
	}
	switch r.Method {
	case "PUT":
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil {
			fail(w, r, "MalformedXML", "Bucket configuration exceeds 4 KiB.")
			return
		}
		region := "us-east-1"
		if len(body) > 0 {
			var conf struct {
				XMLName xml.Name `xml:"CreateBucketConfiguration"`
				Region  string   `xml:"LocationConstraint"`
			}
			if xml.Unmarshal(body, &conf) != nil || conf.Region == "" {
				fail(w, r, "MalformedXML", "Expected a CreateBucketConfiguration with LocationConstraint.")
				return
			}
			region = conf.Region
		}
		found := false
		for _, v := range Regions {
			if region == v {
				found = true
			}
		}
		if !found {
			fail(w, r, "InvalidLocationConstraint", "This simulator supports us-east-1, us-west-2, ap-northeast-1 and eu-west-1.")
			return
		}
		if err := g.S3.Create(name, region); err != nil {
			fromError(w, r, err)
			return
		}
		w.Header().Set("Location", "/"+name)
		w.Header().Set("x-amz-bucket-region", region)
		w.WriteHeader(200)
	case "HEAD":
		b, err := g.Store.BucketContext(r.Context(), name)
		if err != nil {
			fromError(w, r, err)
			return
		}
		w.Header().Set("x-amz-bucket-region", b.Region)
		w.WriteHeader(200)
	case "DELETE":
		if err := g.S3.DeleteBucket(name); err != nil {
			fromError(w, r, err)
			return
		}
		w.WriteHeader(204)
	case "GET":
		g.list(w, r, name)
	default:
		fail(w, r, "MethodNotAllowed", "Unsupported bucket method.")
	}
}

type listedObject struct {
	Key          string
	LastModified string
	ETag         string
	Size         int
	StorageClass string
}
type commonPrefix struct{ Prefix string }
type cursor struct{ Bucket, Prefix, Delimiter, Last string }

func (g *Gateway) list(w http.ResponseWriter, r *http.Request, name string) {
	q := r.URL.Query()
	if q.Get("list-type") != "2" {
		fail(w, r, "NotImplemented", "Use ListObjectsV2 (list-type=2).")
		return
	}
	b, err := g.Store.BucketContext(r.Context(), name)
	if err != nil {
		fromError(w, r, err)
		return
	}
	prefix, delimiter := q.Get("prefix"), q.Get("delimiter")
	if !validText(prefix) || !validText(delimiter) {
		fail(w, r, "InvalidArgument", "Invalid prefix or delimiter.")
		return
	}
	limit := 1000
	if q.Has("max-keys") {
		limit, err = strconv.Atoi(q.Get("max-keys"))
		if err != nil || limit < 0 || limit > 1000 {
			fail(w, r, "InvalidArgument", "max-keys must be between 0 and 1000.")
			return
		}
	}
	encoded := q.Get("encoding-type")
	if encoded != "" && encoded != "url" {
		fail(w, r, "InvalidArgument", "Only URL encoding is supported.")
		return
	}
	after := q.Get("start-after")
	if t := q.Get("continuation-token"); t != "" {
		raw, e := base64.RawURLEncoding.DecodeString(t)
		var c cursor
		if e != nil || json.Unmarshal(raw, &c) != nil || c.Bucket != name || c.Prefix != prefix || c.Delimiter != delimiter {
			fail(w, r, "InvalidArgument", "Invalid continuation token for this listing.")
			return
		}
		after = c.Last
	}
	type entry struct {
		name string
		obj  *core.Object
	}
	entries := []entry{}
	seen := map[string]bool{}
	for i := range b.Objects {
		o := &b.Objects[i]
		if !strings.HasPrefix(o.Key, prefix) {
			continue
		}
		if delimiter != "" {
			if j := strings.Index(strings.TrimPrefix(o.Key, prefix), delimiter); j >= 0 {
				p := o.Key[:len(prefix)+j+len(delimiter)]
				if !seen[p] && p > after {
					entries = append(entries, entry{name: p})
					seen[p] = true
				}
				continue
			}
		}
		if o.Key > after {
			entries = append(entries, entry{o.Key, o})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	result := struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		XMLNS                 string   `xml:"xmlns,attr"`
		Name                  string
		Prefix                string
		Delimiter             string `xml:",omitempty"`
		MaxKeys               int
		KeyCount              int
		IsTruncated           bool
		ContinuationToken     string `xml:",omitempty"`
		NextContinuationToken string `xml:",omitempty"`
		StartAfter            string `xml:",omitempty"`
		EncodingType          string `xml:",omitempty"`
		Contents              []listedObject
		CommonPrefixes        []commonPrefix
	}{XMLNS: ns, Name: name, Prefix: prefix, Delimiter: delimiter, MaxKeys: limit, ContinuationToken: q.Get("continuation-token"), StartAfter: q.Get("start-after"), EncodingType: encoded}
	if len(entries) > limit && limit > 0 {
		result.IsTruncated = true
		entries = entries[:limit]
		raw, _ := json.Marshal(cursor{name, prefix, delimiter, entries[len(entries)-1].name})
		result.NextContinuationToken = base64.RawURLEncoding.EncodeToString(raw)
	} else if limit == 0 {
		entries = nil
	}
	encode := func(s string) string {
		if encoded == "url" {
			return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
		}
		return s
	}
	result.Prefix = encode(prefix)
	result.Delimiter = encode(delimiter)
	result.StartAfter = encode(result.StartAfter)
	for _, e := range entries {
		result.KeyCount++
		if e.obj == nil {
			result.CommonPrefixes = append(result.CommonPrefixes, commonPrefix{encode(e.name)})
		} else {
			o := e.obj
			result.Contents = append(result.Contents, listedObject{encode(o.Key), o.Modified.Format(time.RFC3339), o.ETag, o.Size, "STANDARD"})
		}
	}
	writeXML(w, 200, result)
}
func (g *Gateway) object(w http.ResponseWriter, r *http.Request, name, key string) {
	switch r.Method {
	case "PUT":
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, core.MaxObjectBytes))
		if err != nil {
			fail(w, r, "EntityTooLarge", "Object exceeds the 8 MiB simulator limit.")
			return
		}
		if err := checksums(r, data); err != nil {
			fail(w, r, "BadDigest", err.Error())
			return
		}
		ct := r.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		obj, err := g.S3.PutContext(r.Context(), name, key, ct, data, r.Header.Get("If-Match"), r.Header.Get("If-None-Match"))
		if err != nil {
			fromError(w, r, err)
			return
		}
		w.Header().Set("ETag", obj.ETag)
		if obj.ID != "" {
			w.Header().Set("x-amz-version-id", obj.ID)
		}
		w.WriteHeader(200)
	case "GET", "HEAD":
		obj, err := g.S3.GetContext(r.Context(), name, key, r.URL.Query().Get("versionId"))
		if err != nil {
			fromError(w, r, err)
			return
		}
		if obj.ID != "" {
			w.Header().Set("x-amz-version-id", obj.ID)
		}
		if obj.Marker {
			w.Header().Set("x-amz-delete-marker", "true")
			if r.URL.Query().Has("versionId") {
				w.Header().Set("Last-Modified", obj.Modified.Format(http.TimeFormat))
				fail(w, r, "MethodNotAllowed", "The specified version is a delete marker.")
			} else {
				fail(w, r, "NoSuchKey", "The current version is a delete marker.")
			}
			return
		}
		w.Header().Set("ETag", obj.ETag)
		w.Header().Set("Last-Modified", obj.Modified.Format(http.TimeFormat))
		w.Header().Set("Accept-Ranges", "bytes")
		if match := r.Header.Get("If-Match"); match != "" && match != "*" && match != obj.ETag {
			fail(w, r, "PreconditionFailed", "ETag does not match.")
			return
		}
		if none := r.Header.Get("If-None-Match"); none == "*" || none == obj.ETag {
			w.WriteHeader(304)
			return
		}
		if value := r.Header.Get("Range"); value != "" && !validRange(value, obj.Size) {
			fail(w, r, "InvalidRange", "Only a valid single byte range is supported.")
			return
		}
		// Serve downloaded data as an attachment. User HTML is never part of the console origin.
		w.Header().Set("Content-Type", obj.ContentType)
		w.Header().Set("Content-Disposition", "attachment")
		http.ServeContent(w, r, key, obj.Modified, bytes.NewReader(obj.Data))
	case "DELETE":
		obj, err := g.S3.DeleteContext(r.Context(), name, key, r.URL.Query().Get("versionId"))
		if err != nil {
			fromError(w, r, err)
			return
		}
		if obj.ID != "" {
			w.Header().Set("x-amz-version-id", obj.ID)
		}
		if obj.Marker {
			w.Header().Set("x-amz-delete-marker", "true")
		}
		w.WriteHeader(204)
	default:
		fail(w, r, "MethodNotAllowed", "Unsupported object method.")
	}
}

// permission maps protocol operations to the documented S3 IAM action and ARN.
func permission(r *http.Request) (string, string) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	name, key, object := strings.Cut(path, "/")
	if name == "" {
		return "s3:ListAllMyBuckets", "*"
	}
	resource := "arn:aws:s3:::" + name
	if object && key != "" {
		resource += "/" + key
		switch r.Method {
		case "PUT":
			return "s3:PutObject", resource
		case "DELETE":
			if r.URL.Query().Has("versionId") {
				return "s3:DeleteObjectVersion", resource
			}
			return "s3:DeleteObject", resource
		default:
			if r.URL.Query().Has("versionId") {
				return "s3:GetObjectVersion", resource
			}
			return "s3:GetObject", resource
		}
	}
	if r.URL.Query().Has("tagging") {
		if r.Method == "GET" {
			return "s3:GetBucketTagging", resource
		}
		return "s3:PutBucketTagging", resource
	}
	if r.URL.Query().Has("versions") {
		return "s3:ListBucketVersions", resource
	}
	if r.URL.Query().Has("versioning") {
		if r.Method == "PUT" {
			return "s3:PutBucketVersioning", resource
		}
		return "s3:GetBucketVersioning", resource
	}
	if r.URL.Query().Has("publicAccessBlock") {
		if r.Method == "GET" {
			return "s3:GetBucketPublicAccessBlock", resource
		}
		return "s3:PutBucketPublicAccessBlock", resource
	}
	if r.URL.Query().Has("location") {
		return "s3:GetBucketLocation", resource
	}
	switch r.Method {
	case "PUT":
		return "s3:CreateBucket", resource
	case "DELETE":
		return "s3:DeleteBucket", resource
	default:
		return "s3:ListBucket", resource
	}
}

func checksums(r *http.Request, data []byte) error {
	md := md5.Sum(data)
	sh1 := sha1.Sum(data)
	sh256 := sha256.Sum256(data)
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(data))
	crcc := make([]byte, 4)
	binary.BigEndian.PutUint32(crcc, crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli)))
	for name, sum := range map[string][]byte{"Content-MD5": md[:], "x-amz-checksum-sha1": sh1[:], "x-amz-checksum-sha256": sh256[:], "x-amz-checksum-crc32": crc, "x-amz-checksum-crc32c": crcc} {
		if expected := r.Header.Get(name); expected != "" && expected != base64.StdEncoding.EncodeToString(sum) {
			return fmt.Errorf("The %s checksum does not match the object body.", name)
		}
	}
	if r.Header.Get("x-amz-checksum-crc64nvme") != "" {
		return fmt.Errorf("CRC64NVME checksums are not supported.")
	}
	return nil
}

func validRange(value string, size int) bool {
	if !strings.HasPrefix(value, "bytes=") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
	if len(parts) != 2 || size == 0 {
		return false
	}
	if parts[0] == "" {
		suffix, e := strconv.Atoi(parts[1])
		return e == nil && suffix > 0
	}
	start, e := strconv.Atoi(parts[0])
	if e != nil || start < 0 || start >= size {
		return false
	}
	if parts[1] == "" {
		return true
	}
	end, e := strconv.Atoi(parts[1])
	return e == nil && end >= start
}
