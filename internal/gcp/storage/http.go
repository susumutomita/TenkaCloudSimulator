package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var scopePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var projectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*[a-z0-9]$`)

func validScope(s string) bool   { return scopePattern.MatchString(s) }
func validProject(s string) bool { return projectPattern.MatchString(s) }
func validBucket(s string) bool {
	if len(s) < 3 || len(s) > 222 || !bucketPattern.MatchString(s) || net.ParseIP(s) != nil || strings.HasPrefix(s, "goog") || strings.Contains(s, "google") {
		return false
	}
	if !strings.Contains(s, ".") && len(s) > 63 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
	}
	return true
}
func validObject(s string) bool {
	return len(s) > 0 && len(s) <= 1024 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n") && s != "." && s != ".."
}

type bucketInput struct {
	Name         string `json:"name"`
	Location     string `json:"location,omitempty"`
	StorageClass string `json:"storageClass,omitempty"`
}
type objectInput struct {
	Name        string            `json:"name,omitempty"`
	Bucket      string            `json:"bucket,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CRC32C      string            `json:"crc32c,omitempty"`
	MD5         string            `json:"md5Hash,omitempty"`
}

// strictJSON rejects duplicate keys, null, non-string metadata and unknown
// struct fields before execution. It never invokes a caller-provided decoder.
func strictJSON(data []byte, target any) error {
	if !utf8.Valid(data) || !validJSONEscapes(data) {
		return invalid("JSON must preserve valid UTF-8 scalar values.")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	allowed := map[string]bool{}
	typeOf := reflect.TypeOf(target)
	if typeOf != nil && typeOf.Kind() == reflect.Pointer && typeOf.Elem().Kind() == reflect.Struct {
		typ := typeOf.Elem()
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" {
				name = field.Name
			}
			if name != "-" {
				allowed[name] = true
			}
		}
	}
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 16 {
			return invalid("JSON nesting exceeds the local limit.")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if token == nil {
			return invalid("Null JSON fields are not supported.")
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				seen := map[string]bool{}
				for decoder.More() {
					key, err := decoder.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok || seen[name] {
						return invalid("Duplicate JSON field.")
					}
					if depth == 0 && len(allowed) > 0 && !allowed[name] {
						return invalid("Unsupported JSON field.")
					}
					seen[name] = true
					if err = visit(depth + 1); err != nil {
						return err
					}
				}
				_, err = decoder.Token()
				return err
			case '[':
				for decoder.More() {
					if err = visit(depth + 1); err != nil {
						return err
					}
				}
				_, err = decoder.Token()
				return err
			default:
				return invalid("Invalid JSON structure.")
			}
		}
		return nil
	}
	if err := visit(0); err != nil {
		return invalid("Expected one strict JSON object without duplicate/null fields.")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return invalid("Expected exactly one JSON object.")
	}
	decode := json.NewDecoder(bytes.NewReader(data))
	decode.DisallowUnknownFields()
	if err := decode.Decode(target); err != nil {
		return invalid("Invalid or unsupported JSON field.")
	}
	return nil
}

// encoding/json replaces lone UTF-16 surrogates. Reject those instead so two
// distinct supplied names cannot silently become one replacement-character key.
func validJSONEscapes(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// A leaf handler cannot authenticate HTTP credentials or principal headers.
	actor, ok := r.Context().Value(actorKey{}).(Actor)
	if !ok || actor.Subject == "" || actor.Environment != s.environment {
		writeError(w, &apiError{401, "required", "An authenticated local provider actor is required."})
		return
	}
	if r.Header.Get("Content-Encoding") != "" || r.Header.Get("If-Match") != "" || r.Header.Get("If-None-Match") != "" || r.Header.Get("Range") != "" {
		writeError(w, invalid("Content encodings, HTTP ETag conditions and byte ranges are outside this subset."))
		return
	}
	for key := range r.Header {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "x-goog-encryption-") || lower == "x-goog-user-project" || lower == "x-http-method-override" {
			writeError(w, invalid("Customer encryption, requester-pays and method overrides are unsupported."))
			return
		}
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, invalid("Malformed query encoding."))
		return
	}
	for _, values := range q {
		if len(values) != 1 {
			writeError(w, invalid("Repeated query parameters are not supported."))
			return
		}
	}
	// Match JSON API route separators before decoding its single object-name
	// segment. Unlike an S3 key tail, /o/foo/acl/entity is a subresource route,
	// not the literal object foo/acl/entity. Encoded slashes remain name bytes.
	path := r.URL.EscapedPath()
	if r.URL.RawPath != "" && path != r.URL.RawPath {
		writeError(w, invalid("Invalid paired escaped request path."))
		return
	}
	upload := strings.HasPrefix(path, "/upload/storage/v1/")
	download := strings.HasPrefix(path, "/download/storage/v1/")
	base := "/storage/v1/"
	if upload {
		base = "/upload/storage/v1/"
	}
	if download {
		base = "/download/storage/v1/"
		if r.Method != http.MethodGet {
			writeError(w, &apiError{405, "methodNotAllowed", "Download endpoint supports GET only."})
			return
		}
		if q.Get("alt") != "media" {
			writeError(w, invalid("Download endpoint requires alt=media."))
			return
		}
	}
	if !strings.HasPrefix(path, base) {
		writeError(w, absent())
		return
	}
	rest := strings.TrimPrefix(path, base)
	if rest == "b" || rest == "b/" {
		if download {
			writeError(w, absent())
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			writeError(w, &apiError{405, "methodNotAllowed", "Use GET or POST on the bucket collection."})
			return
		}
		if upload {
			writeError(w, invalid("Bucket operations do not use the upload endpoint."))
			return
		}
		if err = queries(q, "project", "prefix", "maxResults", "pageToken", "prettyPrint", "projection"); err != nil {
			writeError(w, err)
			return
		}
		if r.Method == http.MethodPost {
			if q.Has("prefix") || q.Has("maxResults") || q.Has("pageToken") {
				writeError(w, invalid("Listing parameters cannot be used for bucket creation."))
				return
			}
			var input bucketInput
			data, e := readBounded(r.Body, 64<<10)
			if e == nil {
				e = strictJSON(data, &input)
			}
			if e != nil {
				writeError(w, e)
				return
			}
			out, e := s.createBucket(r.Context(), q.Get("project"), input)
			writeResult(w, r, out, e)
			return
		}
		out, e := s.listBuckets(r.Context(), q)
		writeResult(w, r, out, e)
		return
	}
	if !strings.HasPrefix(rest, "b/") {
		writeError(w, absent())
		return
	}
	rest = strings.TrimPrefix(rest, "b/")
	name, tail, hasTail := strings.Cut(rest, "/")
	name, err = url.PathUnescape(name)
	if err != nil {
		writeError(w, invalid("Invalid escaped bucket segment."))
		return
	}
	if !validBucket(name) {
		writeError(w, invalid("Invalid bucket name."))
		return
	}
	if !hasTail {
		if download {
			writeError(w, absent())
			return
		}
		if upload {
			writeError(w, invalid("Expected an object upload collection."))
			return
		}
		if err = queries(q, "prettyPrint", "projection"); err != nil {
			writeError(w, err)
			return
		}
		out, e := s.bucketOperation(r.Context(), r.Method, name)
		writeResult(w, r, out, e)
		return
	}
	if tail == "o" || tail == "o/" && upload {
		if download {
			writeError(w, absent())
			return
		}
		if upload {
			if r.Method != http.MethodPost {
				writeError(w, &apiError{405, "methodNotAllowed", "Uploads require POST."})
				return
			}
			if err = queries(q, "uploadType", "name", "ifGenerationMatch", "prettyPrint", "projection"); err != nil {
				writeError(w, err)
				return
			}
			input, data, e := s.upload(r, q)
			if e != nil {
				writeError(w, e)
				return
			}
			condition, e := generationCondition(q)
			if e != nil {
				writeError(w, e)
				return
			}
			out, e := s.putObject(r.Context(), name, input, data, condition)
			writeResult(w, r, out, e)
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, &apiError{405, "methodNotAllowed", "Object collection supports GET; use /upload for POST."})
			return
		}
		if err = queries(q, "prefix", "delimiter", "maxResults", "pageToken", "prettyPrint", "projection"); err != nil {
			writeError(w, err)
			return
		}
		out, e := s.listObjects(r.Context(), name, q)
		writeResult(w, r, out, e)
		return
	}
	if upload || !strings.HasPrefix(tail, "o/") {
		writeError(w, absent())
		return
	}
	escapedObject := strings.TrimPrefix(tail, "o/")
	if strings.Contains(escapedObject, "/") {
		writeError(w, &apiError{501, "notImplemented", "Object subresources are unsupported; encode slashes inside the object-name segment."})
		return
	}
	object, err := url.PathUnescape(escapedObject)
	if err != nil {
		writeError(w, invalid("Invalid escaped object-name segment."))
		return
	}
	if !validObject(object) {
		writeError(w, invalid("Invalid object name."))
		return
	}
	if err = queries(q, "alt", "generation", "ifGenerationMatch", "prettyPrint", "projection"); err != nil {
		writeError(w, err)
		return
	}
	if alt := q.Get("alt"); alt != "" && alt != "json" && alt != "media" {
		writeError(w, invalid("Unsupported alt response representation."))
		return
	}
	if r.Method != http.MethodGet && q.Has("alt") {
		writeError(w, invalid("alt is supported only for object GET."))
		return
	}
	condition, e := generationCondition(q)
	if e != nil {
		writeError(w, e)
		return
	}
	selected, e := generationSelection(q)
	if e != nil {
		writeError(w, e)
		return
	}
	out, e := s.objectOperation(r.Context(), r.Method, name, object, condition, selected)
	if e != nil {
		writeError(w, e)
		return
	}
	if r.Method == http.MethodGet && q.Get("alt") == "media" {
		media := out.(storedObject)
		filename := object
		if i := strings.LastIndex(filename, "/"); i >= 0 {
			filename = filename[i+1:]
		}
		if filename == "" {
			filename = "object"
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
		w.Header().Set("Content-Type", media.ContentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(media.Data)))
		w.Header().Set("X-Goog-Generation", strconv.FormatInt(media.Generation, 10))
		w.Header().Set("X-Goog-Hash", "crc32c="+media.CRC32C+",md5="+media.MD5)
		w.Header().Set("ETag", `"`+media.etag()+`"`)
		w.WriteHeader(200)
		_, _ = w.Write(media.Data)
		return
	}
	if object, ok := out.(storedObject); ok {
		out = object.dto()
	}
	writeResult(w, r, out, e)
}

func queries(q url.Values, allowed ...string) error {
	for key := range q {
		found := false
		for _, name := range allowed {
			if key == name {
				found = true
			}
		}
		if !found {
			return invalid("Unsupported query parameter: " + key)
		}
	}
	if p := q.Get("projection"); p != "" && p != "noAcl" {
		return invalid("Only projection=noAcl is supported; ACL projections are not implemented.")
	}
	if p := q.Get("prettyPrint"); p != "" && p != "true" && p != "false" {
		return invalid("prettyPrint must be true or false.")
	}
	return nil
}
func generationCondition(q url.Values) (*int64, error) {
	values, present := q["ifGenerationMatch"]
	if !present {
		return nil, nil
	}
	value, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || value < 0 || strconv.FormatInt(value, 10) != values[0] {
		return nil, invalid("ifGenerationMatch must be a canonical nonnegative generation integer.")
	}
	return &value, nil
}
func generationSelection(q url.Values) (*int64, error) {
	values, present := q["generation"]
	if !present {
		return nil, nil
	}
	value, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || value <= 0 || strconv.FormatInt(value, 10) != values[0] {
		return nil, invalid("generation must be a canonical positive generation integer.")
	}
	return &value, nil
}
func readBounded(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, invalid("Request body could not be read.")
	}
	if int64(len(data)) > limit {
		return nil, &apiError{413, "uploadTooLarge", "Request exceeds the documented local byte budget."}
	}
	return data, nil
}
func (s *Store) upload(r *http.Request, q url.Values) (objectInput, []byte, error) {
	var input objectInput
	var data []byte
	var err error
	switch q.Get("uploadType") {
	case "media":
		input.Name = q.Get("name")
		input.ContentType = r.Header.Get("Content-Type")
		data, err = readBounded(r.Body, s.limits.ObjectBytes)
	case "multipart":
		kind, params, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || kind != "multipart/related" || params["boundary"] == "" {
			return input, nil, invalid("Use bounded multipart/related metadata and media parts.")
		}
		envelope, e := readBounded(r.Body, s.limits.ObjectBytes+(64<<10))
		if e != nil {
			return input, nil, e
		}
		reader := multipart.NewReader(bytes.NewReader(envelope), params["boundary"])
		part, e := reader.NextRawPart()
		if e != nil {
			return input, nil, invalid("Missing metadata part.")
		}
		if encoding := part.Header.Get("Content-Transfer-Encoding"); encoding != "" && encoding != "binary" {
			return input, nil, invalid("Multipart transfer encodings are unsupported.")
		}
		kind, _, e = mime.ParseMediaType(part.Header.Get("Content-Type"))
		if e != nil || kind != "application/json" {
			return input, nil, invalid("First multipart part must be JSON metadata.")
		}
		metadata, e := readBounded(part, 64<<10)
		if e == nil {
			e = strictJSON(metadata, &input)
		}
		if e != nil {
			return input, nil, e
		}
		part, e = reader.NextRawPart()
		if e != nil {
			return input, nil, invalid("Missing media part.")
		}
		if encoding := part.Header.Get("Content-Transfer-Encoding"); encoding != "" && encoding != "binary" {
			return input, nil, invalid("Multipart transfer encodings are unsupported.")
		}
		if input.ContentType == "" {
			input.ContentType = part.Header.Get("Content-Type")
		}
		data, err = readBounded(part, s.limits.ObjectBytes)
		if err != nil {
			return input, nil, err
		}
		if _, e = reader.NextRawPart(); e != io.EOF {
			return input, nil, invalid("Expected exactly two multipart parts.")
		}
		if _, supplied := q["name"]; supplied {
			input.Name = q.Get("name")
		} // Documented query-name precedence.
	default:
		return input, nil, invalid("Only uploadType=media or multipart is implemented; resumable uploads are unsupported.")
	}
	if err != nil {
		return input, nil, err
	}
	if !validObject(input.Name) {
		return input, nil, invalid("Use an exact 1–1024 byte UTF-8 object name without NUL/CR/LF.")
	}
	if input.ContentType == "" {
		input.ContentType = "application/octet-stream"
	}
	if _, _, err = mime.ParseMediaType(input.ContentType); err != nil || len(input.ContentType) > 256 || strings.ContainsAny(input.ContentType, "\r\n") {
		return input, nil, invalid("Invalid media content type.")
	}
	if err = validateMetadata(input.Metadata); err != nil {
		return input, nil, err
	}
	return input, data, nil
}
func validateMetadata(metadata map[string]string) error {
	if len(metadata) > 16 {
		return invalid("At most 16 custom metadata entries are supported.")
	}
	for key, value := range metadata {
		if len(key) == 0 || len(key) > 64 || len(value) > 256 || !utf8.ValidString(key) || !utf8.ValidString(value) || strings.ContainsAny(key+value, "\x00\r\n") {
			return invalid("Invalid bounded custom metadata.")
		}
	}
	return nil
}

func writeResult(w http.ResponseWriter, r *http.Request, out any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	if out == nil {
		w.WriteHeader(204)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	encoder := json.NewEncoder(w)
	if r.URL.Query().Get("prettyPrint") == "true" {
		encoder.SetIndent("", "  ")
	}
	_ = encoder.Encode(out)
}
func writeError(w http.ResponseWriter, err error) {
	e := &apiError{500, "internalError", "The local Storage state operation failed."}
	var expected *apiError
	if errors.As(err, &expected) {
		e = expected
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": e.status, "message": e.message, "errors": []any{map[string]string{"domain": "global", "reason": e.reason, "message": e.message}}}})
}
