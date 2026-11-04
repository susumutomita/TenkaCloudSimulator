package blob

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ServeHTTP must be invoked directly, without a path-cleaning ServeMux. The
// host authenticates first and installs a trusted Azure Actor into the context.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("x-ms-version", APIVersion)
	w.Header().Set("x-ms-request-id", fmt.Sprintf("%016x", s.requests.Add(1)))
	finish := func(err error) { writeError(w, r, err) }
	if r.Header.Get("x-ms-version") != APIVersion {
		finish(fail(400, "InvalidHeaderValue", "Only the documented local x-ms-version is supported."))
		return
	}
	if len(r.URL.RawQuery) > 16<<10 {
		finish(fail(400, "InvalidQueryParameterValue", "Query is too large."))
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		finish(fail(400, "InvalidQueryParameterValue", "Invalid query encoding."))
		return
	}
	for name, values := range q {
		if len(values) != 1 || name != "restype" && name != "comp" && name != "prefix" && name != "marker" && name != "maxresults" && name != "include" && name != "timeout" {
			finish(fail(400, "UnsupportedQueryParameter", "Query option is unsupported."))
			return
		}
	}
	if r.Header.Get("Authorization") != "" {
		finish(fail(403, "AuthenticationFailed", "Real Azure credentials/SharedKey/Bearer authorization are unsupported; trusted host actor binding is required."))
		return
	}
	if timeout := q.Get("timeout"); timeout != "" {
		n, e := strconv.Atoi(timeout)
		if e != nil || n < 1 || n > 30 {
			finish(fail(400, "InvalidQueryParameterValue", "Timeout must be 1–30 seconds."))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(n)*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
	}
	allowedMS := map[string]bool{"x-ms-version": true, "x-ms-date": true, "x-ms-client-request-id": true, "x-ms-blob-type": true, "x-ms-blob-content-type": true, "x-ms-blob-content-md5": true, "x-ms-range": true, "x-ms-range-get-content-md5": true}
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		if len(values) != 1 && (strings.HasPrefix(lower, "x-ms-") || strings.HasPrefix(lower, "if-") || lower == "range") {
			finish(fail(400, "InvalidHeaderValue", "Repeated native headers are unsupported."))
			return
		}
		if strings.HasPrefix(lower, "x-ms-") && !allowedMS[lower] && !strings.HasPrefix(lower, "x-ms-meta-") {
			finish(fail(400, "UnsupportedHeader", "Native header is unsupported."))
			return
		}
	}
	if id := r.Header.Get("x-ms-client-request-id"); id != "" {
		if len(id) > 1024 || !visibleASCII(id) {
			finish(fail(400, "InvalidHeaderValue", "Invalid client request ID."))
			return
		}
		w.Header().Set("x-ms-client-request-id", id)
	}
	if r.Header.Get("If-Modified-Since") != "" || r.Header.Get("If-Unmodified-Since") != "" || r.Header.Get("Content-Encoding") != "" {
		finish(fail(400, "UnsupportedHeader", "Date conditions and encoded bodies are unsupported."))
		return
	}
	account, container, key, hasKey, err := route(r.URL)
	if err != nil {
		finish(err)
		return
	}
	if account != s.config.Account {
		finish(fail(404, "ResourceNotFound", "Configured synthetic account not found."))
		return
	}
	cond, err := conditions(r.Header)
	if err != nil {
		finish(err)
		return
	}
	var op Operation
	switch {
	case container == "" && !hasKey && r.Method == "GET" && q.Get("comp") == "list" && q.Get("restype") == "":
		op = ListContainers
	case container != "" && !hasKey && q.Get("restype") == "container" && q.Get("comp") == "list" && r.Method == "GET":
		op = ListBlobs
	case container != "" && !hasKey && q.Get("restype") == "container" && q.Get("comp") == "":
		switch r.Method {
		case "PUT":
			op = CreateContainer
		case "HEAD":
			op = GetContainerProperties
		case "DELETE":
			op = DeleteContainer
		}
	case hasKey && q.Get("restype") == "" && q.Get("comp") == "":
		switch r.Method {
		case "PUT":
			op = PutBlob
		case "GET":
			op = GetBlob
		case "HEAD":
			op = GetBlobProperties
		case "DELETE":
			op = DeleteBlob
		}
	}
	if op != GetBlob && (len(r.Header.Values("Range")) > 0 || len(r.Header.Values("x-ms-range")) > 0 || len(r.Header.Values("x-ms-range-get-content-md5")) > 0) {
		finish(fail(400, "UnsupportedHeader", "Range headers are supported only for GET Blob."))
		return
	}
	if op == "" {
		finish(fail(501, "NotImplemented", "Operation is outside the supported Azure Blob subset."))
		return
	}
	list := op == ListBlobs || op == ListContainers
	if !list {
		for name := range q {
			if name != "comp" && name != "restype" && name != "timeout" {
				finish(fail(400, "UnsupportedQueryParameter", "Listing option on a non-list operation."))
				return
			}
		}
	}
	if !hasKey && (cond.Match != "" || cond.None != "") {
		finish(fail(400, "UnsupportedHeader", "Conditional container/list operations are unsupported."))
		return
	}
	if op != PutBlob && (r.Header.Get("x-ms-blob-type") != "" || r.Header.Get("x-ms-blob-content-type") != "" || r.Header.Get("x-ms-blob-content-md5") != "" || r.Header.Get("Content-MD5") != "") {
		finish(fail(400, "UnsupportedHeader", "Blob upload header on another operation."))
		return
	}
	meta := map[string]string{}
	for name, values := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-ms-meta-") {
			if op != PutBlob && op != CreateContainer {
				finish(fail(400, "UnsupportedHeader", "Metadata mutation is unsupported for this operation."))
				return
			}
			k := name[len("x-ms-meta-"):]
			if _, exists := meta[strings.ToLower(k)]; exists {
				finish(fail(400, "InvalidMetadata", "Duplicate metadata name."))
				return
			}
			meta[strings.ToLower(k)] = values[0]
		}
	}
	if _, _, err = metadata(meta); err != nil {
		finish(err)
		return
	}
	// Body reads are bounded even if Content-Length is absent or incorrect.
	limit := int64(0)
	if op == PutBlob {
		limit = MaxBlobBytes
	}
	reader := io.Reader(http.NoBody)
	if r.Body != nil {
		reader = r.Body
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		finish(fail(400, "InvalidRequestBody", "Body read failed."))
		return
	}
	if int64(len(data)) > limit {
		finish(fail(413, "RequestBodyTooLarge", "Body exceeds the local operation limit."))
		return
	}
	if list {
		options := listOptions{Prefix: q.Get("prefix"), Marker: q.Get("marker"), Limit: 1000}
		if options.Prefix != "" && !validBlob(options.Prefix) {
			finish(fail(400, "InvalidQueryParameterValue", "Invalid prefix."))
			return
		}
		if n := q.Get("maxresults"); n != "" {
			v, e := strconv.ParseInt(n, 10, 64)
			if e != nil || v <= 0 {
				finish(fail(400, "InvalidQueryParameterValue", "maxresults must be positive."))
				return
			}
			if v < 1000 {
				options.Limit = int(v)
			}
		}
		if include := q.Get("include"); include != "" {
			if include != "metadata" {
				finish(fail(400, "UnsupportedQueryParameter", "Only include=metadata is supported."))
				return
			}
			options.Metadata = true
		}
		result, e := s.list(r.Context(), op, container, options)
		if e != nil {
			finish(e)
			return
		}
		if err = writeListing(w, r, s.config.Account, container, options, result); err != nil {
			finish(err)
		}
		return
	}
	if !hasKey {
		result, e := s.container(r.Context(), op, container, meta)
		if e != nil {
			finish(e)
			return
		}
		entityHeaders(w, result)
		if op == CreateContainer {
			w.WriteHeader(201)
		} else if op == DeleteContainer {
			w.WriteHeader(202)
		} else {
			w.WriteHeader(200)
		}
		return
	}
	contentType := r.Header.Get("x-ms-blob-content-type")
	if contentType == "" {
		contentType = r.Header.Get("Content-Type")
	}
	if op == PutBlob {
		if r.Header.Get("x-ms-blob-type") != "BlockBlob" {
			finish(fail(400, "InvalidBlobType", "Only BlockBlob is supported."))
			return
		}
		if contentType != "" {
			if _, _, e := mime.ParseMediaType(contentType); e != nil {
				finish(fail(400, "InvalidHeaderValue", "Invalid blob content type."))
				return
			}
		}
		expected := r.Header.Get("x-ms-blob-content-md5")
		if expected == "" {
			expected = r.Header.Get("Content-MD5")
		}
		if expected != "" {
			sum := md5.Sum(data)
			if expected != base64.StdEncoding.EncodeToString(sum[:]) {
				finish(fail(400, "Md5Mismatch", "The content hash does not match the body."))
				return
			}
		}
	}
	result, e := s.object(r.Context(), op, container, key, cond, data, meta, contentType)
	if e != nil {
		var native *failure
		if errors.As(e, &native) && native.Status == 304 {
			entityHeaders(w, result)
		}
		finish(e)
		return
	}
	contentRange := ""
	if op == GetBlob {
		result, contentRange, e = applyDownloadRange(r, result)
		if e != nil {
			finish(e)
			return
		}
	}
	entityHeaders(w, result)
	switch op {
	case PutBlob:
		w.WriteHeader(201)
	case DeleteBlob:
		w.WriteHeader(202)
	case GetBlob, GetBlobProperties:
		w.Header().Set("Content-Type", result.ContentType)
		w.Header().Set("Content-Length", strconv.FormatInt(result.Length, 10))
		w.Header().Set("Content-Disposition", "attachment")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("x-ms-blob-type", "BlockBlob")
		w.Header().Set("Accept-Ranges", "bytes")
		if contentRange != "" {
			w.Header().Set("Content-Range", contentRange)
			w.WriteHeader(206)
		} else {
			w.WriteHeader(200)
		}
		if op == GetBlob {
			_, _ = w.Write(result.Body)
		}
	}
}
func route(u *url.URL) (string, string, string, bool, error) {
	invalid := fail(400, "InvalidUri", "Invalid account/container/blob URI.")
	if u.RawPath != "" {
		decoded, err := url.PathUnescape(u.RawPath)
		if err != nil || decoded != u.Path {
			return "", "", "", false, invalid
		}
	}
	raw := u.EscapedPath()
	if !strings.HasPrefix(raw, "/") {
		return "", "", "", false, invalid
	}
	a, rest, has := strings.Cut(raw[1:], "/")
	account, err := url.PathUnescape(a)
	if err != nil || !accountName.MatchString(account) {
		return "", "", "", false, invalid
	}
	if !has || rest == "" {
		return account, "", "", false, nil
	}
	c, b, blob := strings.Cut(rest, "/")
	container, err := url.PathUnescape(c)
	if err != nil || !validContainer(container) {
		return "", "", "", false, invalid
	}
	if !blob {
		return account, container, "", false, nil
	}
	key, err := url.PathUnescape(b)
	if err != nil || !validBlob(key) {
		return "", "", "", false, invalid
	}
	return account, container, key, true, nil
}
func visibleASCII(s string) bool {
	for _, c := range s {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
func conditions(h http.Header) (condition, error) {
	c := condition{h.Get("If-Match"), h.Get("If-None-Match")}
	invalid := fail(400, "InvalidHeaderValue", "Only one strong ETag or wildcard condition is supported.")
	if c.Match != "" && c.None != "" {
		return c, invalid
	}
	for _, v := range []string{c.Match, c.None} {
		if v == "" || v == "*" {
			continue
		}
		if len(v) > 128 || len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' || !visibleASCII(v) || strings.ContainsAny(v[1:len(v)-1], "\",\\") {
			return c, invalid
		}
	}
	return c, nil
}
func entityHeaders(w http.ResponseWriter, e entity) {
	w.Header().Set("ETag", e.ETag)
	w.Header().Set("Last-Modified", e.Modified)
	if e.MD5 != "" {
		w.Header().Set("Content-MD5", e.MD5)
	}
	for key, value := range e.Metadata {
		w.Header().Set("x-ms-meta-"+key, value)
	}
}
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	e := &failure{500, "InternalError", "The local Azure Blob operation failed."}
	var native *failure
	if errors.As(err, &native) {
		e = native
	}
	w.Header().Set("x-ms-error-code", e.Code)
	if e.Status == 304 {
		w.WriteHeader(304)
		return
	}
	data, _ := xml.Marshal(struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Message string   `xml:"Message"`
	}{Code: e.Code, Message: e.Message})
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(e.Status)
	if r.Method != "HEAD" {
		_, _ = w.Write(data)
	}
}
