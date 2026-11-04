package aws

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
"mime"
	"net/http"
	"strings"
	"unicode/utf8"
)

type kmsRequest struct {
	Description         string
	KeySpec             string
	KeyUsage            string
	Origin              string
	MultiRegion         bool
	KeyId               string
	PolicyName          string
	Limit               int
	Marker              string
	Plaintext           []byte
	CiphertextBlob      []byte
	EncryptionContext   map[string]string
	EncryptionAlgorithm string
}

var kmsFields = map[string][]string{
	"CreateKey":   {"Description", "KeySpec", "KeyUsage", "Origin", "MultiRegion"},
	"DescribeKey": {"KeyId"}, "ListKeys": {"Limit", "Marker"}, "EnableKey": {"KeyId"}, "DisableKey": {"KeyId"},
	"GetKeyPolicy": {"KeyId", "PolicyName"},
	"Encrypt":      {"KeyId", "Plaintext", "EncryptionContext", "EncryptionAlgorithm"},
	"Decrypt":      {"KeyId", "CiphertextBlob", "EncryptionContext", "EncryptionAlgorithm"},
}

func kmsJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func kmsWriteError(w http.ResponseWriter, err error) {
	code, message, status := "KMSInternalException", "Local KMS state operation failed.", 500
	var failure kmsError
	if errors.As(err, &failure) {
		code, message, status = failure.code, failure.message, 400
		if code == "AccessDeniedException" {
			status = 403
		}
		if code == "UnknownOperationException" {
			status = 501
		}
		if code == "KMSInternalException" {
			status = 500
		}
	}
	w.Header().Set("X-Amzn-Errortype", code)
	kmsJSON(w, status, map[string]string{"__type": code, "message": message})
}

// Validate JSON structurally before decoding: duplicate keys, nulls, excessive depth,
// invalid UTF-8 and unpaired escaped UTF-16 are rejected rather than normalized.
func kmsJSONValue(d *json.Decoder, depth int) error {
	if depth > 8 {
		return kmsInvalid()
	}
	t, err := d.Token()
	if err != nil || t == nil {
		return kmsInvalid()
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				key, ok := k.(string)
				if e != nil || !ok || seen[key] {
					return kmsInvalid()
				}
				seen[key] = true
				if e = kmsJSONValue(d, depth+1); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return kmsInvalid()
			}
		case '[':
			for d.More() {
				if e := kmsJSONValue(d, depth+1); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return kmsInvalid()
			}
		default:
			return kmsInvalid()
		}
	}
	return nil
}
func kmsHex(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	}
	return -1
}
func kmsUnit(data []byte) int {
	if len(data) < 4 {
		return -1
	}
	n := 0
	for _, b := range data[:4] {
		v := kmsHex(b)
		if v < 0 {
			return -1
		}
		n = n*16 + v
	}
	return n
}
func kmsUnicode(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
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
		n := kmsUnit(data[i+1:])
		if n < 0 {
			return false
		}
		i += 4
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low := kmsUnit(data[i+3:])
			if low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		} else if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
	}
	return true
}
func kmsDecode(data []byte, op string) (kmsRequest, error) {
	var request kmsRequest
	fields, ok := kmsFields[op]
	if !ok {
		return request, kmsFailure("UnknownOperationException", "This KMS operation is not implemented.")
	}
	if !kmsUnicode(data) {
		return request, kmsInvalid()
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := kmsJSONValue(d, 0); err != nil {
		return request, err
	}
	if _, err := d.Token(); err != io.EOF {
		return request, kmsInvalid()
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return request, kmsInvalid()
	}
	allowed := map[string]bool{}
	for _, f := range fields {
		allowed[f] = true
	}
	for f := range raw {
		if !allowed[f] {
			return request, kmsInvalid()
		}
	}
	for _, f := range []string{"Plaintext", "CiphertextBlob"} {
		if value, ok := raw[f]; ok {
			var encoded string
			if json.Unmarshal(value, &encoded) != nil {
				return request, kmsInvalid()
			}
		}
	}
	if json.Unmarshal(data, &request) != nil {
		return request, kmsInvalid()
	}
	for _, f := range []string{"KeySpec", "KeyUsage", "Origin", "PolicyName", "KeyId", "EncryptionAlgorithm", "Marker"} {
		if value, ok := raw[f]; ok {
			var s string
			if json.Unmarshal(value, &s) != nil || s == "" {
				return request, kmsInvalid()
			}
		}
	}
	if request.KeySpec != "" && request.KeySpec != "SYMMETRIC_DEFAULT" || request.KeyUsage != "" && request.KeyUsage != "ENCRYPT_DECRYPT" || request.Origin != "" && request.Origin != "AWS_KMS" || request.MultiRegion || !kmsDescription(request.Description) || !kmsAlgorithm(request.EncryptionAlgorithm) {
		return request, kmsInvalid()
	}
	if _, err := kmsContext(request.EncryptionContext); err != nil {
		return request, err
	}
	if op != "CreateKey" && op != "ListKeys" && op != "Decrypt" && request.KeyId == "" {
		return request, kmsInvalid()
	}
	if op == "ListKeys" {
		if _, present := raw["Limit"]; !present {
			request.Limit = 100
		}
		if request.Limit < 1 || request.Limit > 1000 {
			return request, kmsInvalid()
		}
	}
	if op == "Encrypt" && (len(request.Plaintext) < 1 || len(request.Plaintext) > 4096) || op == "Decrypt" && (len(request.CiphertextBlob) < 1 || len(request.CiphertextBlob) > 6144) {
		return request, kmsInvalid()
	}
	if op == "GetKeyPolicy" && request.PolicyName != "" && request.PolicyName != "default" {
		return request, kmsInvalid()
	}
	return request, nil
}
func (s *KMSStore) ServeHTTP(w http.ResponseWriter, r *http.Request, region string, authorize func(string, string) bool) {
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "TrentService.")
 mediaType,parameters,mediaError:=mime.ParseMediaType(r.Header.Get("Content-Type"))
 validMedia:=mediaError==nil && mediaType=="application/x-amz-json-1.1"
 for name,value:=range parameters{if name!="charset" || !strings.EqualFold(value,"utf-8"){validMedia=false}}
	if !strings.HasPrefix(r.Header.Get("X-Amz-Target"), "TrentService.") || len(r.Header.Values("X-Amz-Target"))!=1 || len(r.Header.Values("Content-Type"))!=1 || r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" || !validMedia || !kmsRegion(region) {
		kmsWriteError(w, kmsInvalid())
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		kmsWriteError(w, kmsInvalid())
		return
	}
	request, err := kmsDecode(data, op)
	if err != nil {
		kmsWriteError(w, err)
		return
	}
	resource := "*"
	if op != "CreateKey" && op != "ListKeys" {
		selected := request.KeyId
		if op == "Decrypt" && selected == "" {
			blobRegion, id, _, e := kmsBlob(request.CiphertextBlob)
			if e != nil || blobRegion != region {
				kmsWriteError(w, kmsFailure("InvalidCiphertextException", "The local ciphertext is invalid for this request."))
				return
			}
			selected = id
		}
		id, e := kmsID(region, selected)
		if e != nil {
			kmsWriteError(w, e)
			return
		}
		resource = kmsARN(region, id)
	}
	if authorize == nil || !authorize("kms:"+op, resource) {
		kmsWriteError(w, kmsFailure("AccessDeniedException", "KMS permission denied."))
		return
	}
	var response any
	switch op {
	case "CreateKey":
		var key KMSKeyMetadata
		key, err = s.CreateKey(r.Context(), region, request.Description)
		response = map[string]any{"KeyMetadata": key}
	case "DescribeKey":
		var key KMSKeyMetadata
		key, err = s.DescribeKey(r.Context(), region, request.KeyId)
		response = map[string]any{"KeyMetadata": key}
	case "ListKeys":
		response, err = s.ListKeys(r.Context(), region, request.Limit, request.Marker)
	case "EnableKey", "DisableKey":
		err = s.SetEnabled(r.Context(), region, request.KeyId, op == "EnableKey")
	case "GetKeyPolicy":
		_, err = s.DescribeKey(r.Context(), region, request.KeyId)
		response = map[string]string{"Policy": kmsDefaultPolicy, "PolicyName": "default"}
	case "Encrypt":
		var blob []byte
		var arn string
		blob, arn, err = s.Encrypt(r.Context(), region, request.KeyId, request.EncryptionContext, request.Plaintext)
		response = map[string]any{"CiphertextBlob": blob, "KeyId": arn, "EncryptionAlgorithm": "SYMMETRIC_DEFAULT"}
	case "Decrypt":
		var plaintext []byte
		var arn string
		plaintext, arn, err = s.Decrypt(r.Context(), region, request.KeyId, request.EncryptionContext, request.CiphertextBlob)
		response = map[string]any{"Plaintext": plaintext, "KeyId": arn, "EncryptionAlgorithm": "SYMMETRIC_DEFAULT"}
	}
	if err != nil {
		kmsWriteError(w, err)
		return
	}
	kmsJSON(w, 200, response)
}
