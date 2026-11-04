package aws

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	pythonruntime "github.com/susumutomita/TenkaCloudSimulator/internal/workload/python"
)

//go:embed bridge/*.py bridge/botocore/*.py
var lambdaBridgeFiles embed.FS

func lambdaSDKFiles() fs.FS { sub, _ := fs.Sub(lambdaBridgeFiles, "bridge"); return sub }
func bridgeFailure(code, message string, status int) json.RawMessage {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": message, "status": status}})
	return body
}
func bridgeResult(result any) json.RawMessage {
	body, _ := json.Marshal(map[string]any{"result": result})
	return body
}

type capabilityResponse struct {
	header   http.Header
	code     int
	body     bytes.Buffer
	exceeded bool
}

func (w *capabilityResponse) Header() http.Header { return w.header }
func (w *capabilityResponse) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
}
func (w *capabilityResponse) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = 200
	}
	if len(p) > 48<<10-w.body.Len() {
		w.exceeded = true
		return 0, http.ErrContentLength
	}
	return w.body.Write(p)
}

// No socket or HTTP client is used here. Each call traverses the native gateway
// using only the saved function role and invocation region. Guest transport,
// credentials, identity headers and arbitrary destinations are never accepted.
func (g *Gateway) lambdaExchange(f LambdaFunction, region string) pythonruntime.Exchange {
	principal := strings.TrimPrefix(f.Role, "arn:aws:iam::000000000000:role/")
	return func(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
		if !localRole.MatchString(f.Role) || principal == core.Administrator || f.Region != region {
			return bridgeFailure("AccessDeniedException", "Invalid saved execution-role or region context.", 403), nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var request struct {
			Service, Operation, Region string
			Parameters                 json.RawMessage
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) != nil || json.Unmarshal(body, &request) != nil {
			return bridgeFailure("InvalidRequest", "Use a local capability request object.", 400), nil
		}
		for field := range fields {
			if field != "service" && field != "operation" && field != "parameters" && field != "region" {
				return bridgeFailure("InvalidRequest", "Transport and identity overrides are unsupported.", 400), nil
			}
		}
		if request.Region != "" && request.Region != region {
			return bridgeFailure("InvalidRequest", "The local SDK is bound to the function region.", 400), nil
		}
		var parameters map[string]json.RawMessage
		if json.Unmarshal(request.Parameters, &parameters) != nil || parameters == nil {
			return bridgeFailure("InvalidRequest", "Parameters must be an object.", 400), nil
		}
		if g.Identities == nil || request.Service == "ssm" && g.Parameters == nil || request.Service == "s3" && (g.S3 == nil || g.Store == nil) {
			return bridgeFailure("ServiceException", "Local capability state is unavailable.", 500), nil
		}
		method, path, data := "POST", "/", []byte(request.Parameters)
		headers := http.Header{"X-Simulator-Principal": {principal}, "X-Simulator-Region": {region}}
		if request.Service == "ssm" && (request.Operation == "GetParameter" || request.Operation == "GetParameters") {
			headers.Set("Content-Type", "application/x-amz-json-1.1")
			headers.Set("X-Amz-Target", "AmazonSSM."+request.Operation)
		} else if request.Service == "s3" && (request.Operation == "GetObject" || request.Operation == "PutObject" || request.Operation == "DeleteObject") {
			var input struct{ Bucket, Key, Body, ContentType, VersionId string }
			if json.Unmarshal(request.Parameters, &input) != nil || !ValidBucket(input.Bucket) || input.Key == "" {
				return bridgeFailure("InvalidRequest", "Use a valid bucket and object key.", 400), nil
			}
			for field, raw := range parameters {
				allowed := field == "Bucket" || field == "Key" || field == "VersionId" && request.Operation != "PutObject" || (field == "Body" || field == "ContentType") && request.Operation == "PutObject"
				var scalar string
				if !allowed || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &scalar) != nil {
					return bridgeFailure("InvalidRequest", "Unsupported or non-string local object parameter.", 400), nil
				}
			}
			object := url.URL{Scheme: "http", Host: "simulator.invalid", Path: "/" + input.Bucket + "/" + input.Key}
			if input.VersionId != "" {
				object.RawQuery = url.Values{"versionId": {input.VersionId}}.Encode()
			}
			path = object.RequestURI()
			data = nil
			switch request.Operation {
			case "GetObject":
				method = "GET"
			case "DeleteObject":
				method = "DELETE"
			case "PutObject":
				var err error
				data, err = base64.StdEncoding.DecodeString(input.Body)
				if _, ok := parameters["Body"]; !ok || err != nil || len(data) > 32<<10 {
					return bridgeFailure("InvalidRequest", "Object Body must be base64 data of at most 32 KiB.", 400), nil
				}
				method = "PUT"
				if input.ContentType != "" {
					headers.Set("Content-Type", input.ContentType)
				}
			}
		} else {
			return bridgeFailure("NotImplementedException", "Only declared SSM/S3 execution-role operations are supported.", 501), nil
		}
		r, err := http.NewRequestWithContext(ctx, method, "http://simulator.invalid"+path, bytes.NewReader(data))
		if err != nil {
			return bridgeFailure("InvalidRequest", "Invalid local request path.", 400), nil
		}
		r.Header = headers
		w := &capabilityResponse{header: make(http.Header)}
		if request.Service == "ssm" {
			g.serveSSM(w, r)
		} else {
			g.serveS3(w, r)
		}
		if w.exceeded {
			return bridgeFailure("EntityTooLarge", "Local SDK native responses support at most 48 KiB before capability encoding.", 400), nil
		}
		if w.code >= 300 {
			code, message := "ServiceException", "Local service operation failed."
			if request.Service == "ssm" {
				var e struct {
					Type    string `json:"__type"`
					Message string `json:"message"`
				}
				if json.Unmarshal(w.body.Bytes(), &e) == nil && e.Type != "" {
					code, message = e.Type, e.Message
				}
			} else {
				var e struct{ Code, Message string }
				if xml.Unmarshal(w.body.Bytes(), &e) == nil && e.Code != "" {
					code, message = e.Code, e.Message
				}
			}
			return bridgeFailure(code, message, w.code), nil
		}
		if request.Service == "ssm" {
			var result map[string]any
			if json.Unmarshal(w.body.Bytes(), &result) != nil {
				return bridgeFailure("ServiceException", "Invalid local service response.", 500), nil
			}
			result["ResponseMetadata"] = map[string]int{"HTTPStatusCode": w.code}
			return bridgeResult(result), nil
		}
		result := map[string]any{"ResponseMetadata": map[string]int{"HTTPStatusCode": w.code}}
		for _, pair := range [][2]string{{"ETag", "ETag"}, {"x-amz-version-id", "VersionId"}} {
			if v := w.Header().Get(pair[0]); v != "" {
				result[pair[1]] = v
			}
		}
		if v := w.Header().Get("x-amz-delete-marker"); v != "" {
			result["DeleteMarker"] = v == "true"
		}
		if request.Operation == "GetObject" {
			if w.body.Len() > 32<<10 {
				return bridgeFailure("EntityTooLarge", "Local SDK object bodies support at most 32 KiB.", 400), nil
			}
			result["Body"] = base64.StdEncoding.EncodeToString(w.body.Bytes())
			result["ContentLength"] = w.body.Len()
			result["ContentType"] = w.Header().Get("Content-Type")
			if length, err := strconv.Atoi(w.Header().Get("Content-Length")); err == nil {
				result["ContentLength"] = length
			}
		}
		return bridgeResult(result), nil
	}
}
