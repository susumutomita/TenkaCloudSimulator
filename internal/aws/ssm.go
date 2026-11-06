package aws

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type ssmFailure struct{ code, message string }

func (e ssmFailure) Error() string          { return e.message }
func trimParameterSlash(name string) string { return strings.TrimPrefix(name, "/") }

var parameterName = regexp.MustCompile(`^/?[a-zA-Z0-9_.-]+(/[a-zA-Z0-9_.-]+)*$`)

func validParameterName(name string) bool {
	return len(name) <= 1011 && parameterName.MatchString(name) && strings.Count(name, "/") <= 15 && !strings.HasPrefix(strings.ToLower(trimParameterSlash(name)), "aws") && !strings.HasPrefix(strings.ToLower(trimParameterSlash(name)), "ssm")
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if isMetricsRequest(r) {
		g.serveMetrics(w, r)
		return
	}
	if isSecretRequest(r) {
		serveSecretGateway(w, r, g.Secrets, g.Identities)
		return
	}
	if isCloudWatchLogs(r) {
		g.serveCloudWatchLogs(w, r)
		return
	}
	if isKMS(r) {
		g.serveKMS(w, r)
		return
	}
	if isAPIGateway(r) {
		g.serveAPIGateway(w, r)
		return
	}
	if isIAM(r) {
		g.serveIAM(w, r)
		return
	}
	if isLambda(r) {
		g.serveLambda(w, r)
		return
	}
	if r.Method == "POST" && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		g.serveCFN(w, r)
		return
	}
	if strings.HasPrefix(r.Header.Get("X-Amz-Target"), "AmazonSQS.") {
		g.serveSQS(w, r)
		return
	}
	if strings.HasPrefix(r.Header.Get("X-Amz-Target"), "AmazonSSM.") {
		g.serveSSM(w, r)
		return
	}
	if strings.HasPrefix(r.Header.Get("X-Amz-Target"), "DynamoDB_20120810.") {
		g.serveDynamoDB(w, r)
		return
	}
	g.serveS3(w, r)
}
func ssmJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func ssmError(w http.ResponseWriter, err error) {
	code, message, status := "InternalServerError", "Simulator parameter state operation failed.", 500
	var e ssmFailure
	if errors.As(err, &e) {
		code, message, status = e.code, e.message, 400
		if code == "AccessDeniedException" {
			status = 403
		}
		if code == "UnknownOperationException" {
			status = 501
		}
	}
	ssmJSON(w, status, map[string]string{"__type": code, "message": message})
}

type ssmRequest struct {
	Name           string
	Names          []string
	Value          string
	Type           string
	Description    string
	Overwrite      bool
	WithDecryption bool
	Tier           string
	DataType       string
	Path           string
	Recursive      bool
	MaxResults     int
	NextToken      string
}

func (g *Gateway) serveSSM(w http.ResponseWriter, r *http.Request) {
	invalid := func(message string) { ssmError(w, ssmFailure{"ValidationException", message}) }
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" {
		invalid("Use the regional root POST endpoint without query parameters.")
		return
	}
	if a := r.Header.Get("Authorization"); a != "" {
		_, credential, ok := strings.Cut(a, "Credential=")
		key, _, _ := strings.Cut(credential, "/")
		if !ok || !strings.HasPrefix(key, "TCSIM") {
			ssmError(w, ssmFailure{"AccessDeniedException", "Use unsigned requests or dummy TCSIM credentials."})
			return
		}
	}
	if r.Header.Get("X-Amz-Security-Token") != "" {
		invalid("Session tokens are not supported.")
		return
	}
	region := r.Header.Get("X-Simulator-Region")
	if region == "" {
		region = "us-east-1"
	}
	if a := r.Header.Get("Authorization"); a != "" {
		_, scope, _ := strings.Cut(a, "Credential=")
		parts := strings.Split(scope, "/")
		if len(parts) > 3 {
			if r.Header.Get("X-Simulator-Region") != "" && region != parts[2] {
				invalid("Region header and credential scope disagree.")
				return
			}
			region = parts[2]
		}
	}
	supported := false
	for _, v := range Regions {
		if region == v {
			supported = true
		}
	}
	if !supported {
		invalid("Unsupported simulator region.")
		return
	}
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AmazonSSM.")
	allowed := map[string][]string{
		"PutParameter": {"Name", "Value", "Type", "Description", "Overwrite", "Tier", "DataType"},
		"GetParameter": {"Name", "WithDecryption"}, "GetParameters": {"Names", "WithDecryption"}, "DeleteParameter": {"Name"},
		"DescribeParameters": {"MaxResults", "NextToken"}, "GetParametersByPath": {"Path", "Recursive", "MaxResults", "NextToken", "WithDecryption"},
	}
	fields, ok := allowed[op]
	if !ok {
		ssmError(w, ssmFailure{"UnknownOperationException", "This SSM operation is not implemented."})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 48<<10))
	if err != nil {
		invalid("Parameter requests are limited to 48 KiB.")
		return
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		invalid("Expected one JSON request object.")
		return
	}
	for field := range raw {
		if strings.TrimSpace(string(raw[field])) == "null" {
			invalid("Request fields cannot be null.")
			return
		}
		found := false
		for _, f := range fields {
			if field == f {
				found = true
			}
		}
		if !found {
			invalid("Unsupported request field: " + field)
			return
		}
	}
	var input ssmRequest
	if json.Unmarshal(data, &input) != nil {
		invalid("Invalid request field type.")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if (op == "PutParameter" || op == "GetParameter" || op == "DeleteParameter") && !validParameterName(input.Name) {
		invalid("Use a parameter name of up to 1011 characters and at most 15 path levels; labels/versions/ARN selectors are not supported.")
		return
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	authorize := func(resource string) bool {
		decision, err := g.Identities.DecisionContext(r.Context(), principal, "ssm:"+op, resource)
		if err != nil {
			ssmError(w, err)
			return false
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			ssmError(w, ssmFailure{"AccessDeniedException", "IAM evaluation: " + decision})
			return false
		}
		return true
	}
	arn := func(name string) string {
		return fmt.Sprintf("arn:aws:ssm:%s:000000000000:parameter/%s", region, trimParameterSlash(name))
	}
	resource := arn(input.Name)
	if op == "DescribeParameters" {
		resource = "*"
	}
	if op == "GetParametersByPath" {
		resource = arn(strings.TrimSuffix(input.Path, "/")) + "/*"
	}
	if op != "GetParameters" && !authorize(resource) {
		return
	}
	switch op {
	case "PutParameter":
		if input.Type == "" && input.Overwrite {
			existing, err := g.Parameters.GetContext(r.Context(), region, input.Name)
			if err != nil {
				ssmError(w, err)
				return
			}
			input.Type = existing.Type
		}
		if input.Type != "String" && input.Type != "StringList" {
			invalid("Only String and StringList are supported; KMS/SecureString are not simulated.")
			return
		}
		if input.Tier != "" && input.Tier != "Standard" || input.DataType != "" && input.DataType != "text" {
			invalid("Only Standard tier and text data type are supported.")
			return
		}
		if len(input.Value) == 0 || len(input.Value) > 4096 || !utf8.ValidString(input.Value) || len(input.Description) > 1024 {
			invalid("Use a 1–4096 byte UTF-8 value and description up to 1024 bytes.")
			return
		}
		if input.Type == "StringList" {
			for _, v := range strings.Split(input.Value, ",") {
				if strings.TrimSpace(v) == "" {
					invalid("StringList elements cannot be empty.")
					return
				}
			}
		}
		var description *string
		if _, present := raw["Description"]; present {
			description = &input.Description
		}
		version, err := g.Parameters.Put(region, input.Name, input.Type, input.Value, description, input.Overwrite)
		if err != nil {
			ssmError(w, err)
			return
		}
		ssmJSON(w, 200, map[string]any{"Version": version, "Tier": "Standard"})
	case "GetParameter":
		p, err := g.Parameters.GetContext(r.Context(), region, input.Name)
		if err != nil {
			ssmError(w, err)
			return
		}
		ssmJSON(w, 200, map[string]any{"Parameter": p})
	case "GetParameters":
		if len(input.Names) == 0 || len(input.Names) > 10 {
			invalid("Names requires 1–10 names.")
			return
		}
		for _, n := range input.Names {
			if !validParameterName(n) {
				invalid("Invalid parameter name.")
				return
			}
			if !authorize(arn(n)) {
				return
			}
		}
		values := []Parameter{}
		missing := []string{}
		for _, n := range input.Names {
			p, err := g.Parameters.GetContext(r.Context(), region, n)
			if err != nil {
				var e ssmFailure
				if errors.As(err, &e) && e.code == "ParameterNotFound" {
					missing = append(missing, n)
					continue
				}
				ssmError(w, err)
				return
			}
			values = append(values, p)
		}
		ssmJSON(w, 200, map[string]any{"Parameters": values, "InvalidParameters": missing})
	case "DeleteParameter":
		if err := g.Parameters.Delete(region, input.Name); err != nil {
			ssmError(w, err)
			return
		}
		ssmJSON(w, 200, map[string]any{})
	default:
		if op == "GetParametersByPath" && (!strings.HasPrefix(input.Path, "/") || input.Path != "/" && !validParameterName(strings.TrimSuffix(input.Path, "/"))) {
			invalid("Use a slash-prefixed hierarchy path.")
			return
		}
		limit := 50
		if op == "GetParametersByPath" {
			limit = 10
		}
		if input.MaxResults != 0 {
			if input.MaxResults < 1 || input.MaxResults > limit {
				invalid("MaxResults exceeds the supported limit.")
				return
			}
			limit = input.MaxResults
		}
		cursor := ""
		if input.NextToken != "" {
			decoded, e := base64.RawURLEncoding.DecodeString(input.NextToken)
			prefix := region + "|" + op + "|" + input.Path + "|" + strconv.FormatBool(input.Recursive) + "|"
			if e != nil || !strings.HasPrefix(string(decoded), prefix) {
				invalid("Invalid pagination token for this request.")
				return
			}
			cursor = strings.TrimPrefix(string(decoded), prefix)
		}
		values, err := g.Parameters.List(region)
		if err != nil {
			ssmError(w, err)
			return
		}
		filtered := []Parameter{}
		for _, p := range values {
			if p.Name <= cursor {
				continue
			}
			if op == "GetParametersByPath" {
				prefix := strings.TrimSuffix(input.Path, "/") + "/"
				if !strings.HasPrefix(p.Name, prefix) {
					continue
				}
				if !input.Recursive && strings.Contains(strings.TrimPrefix(p.Name, prefix), "/") {
					continue
				}
			}
			filtered = append(filtered, p)
		}
		token := ""
		if len(filtered) > limit {
			filtered = filtered[:limit]
			raw := region + "|" + op + "|" + input.Path + "|" + strconv.FormatBool(input.Recursive) + "|" + filtered[len(filtered)-1].Name
			token = base64.RawURLEncoding.EncodeToString([]byte(raw))
		}
		out := map[string]any{}
		if op == "DescribeParameters" {
			metadata := []map[string]any{}
			for _, p := range filtered {
				metadata = append(metadata, map[string]any{"Name": p.Name, "Type": p.Type, "Description": p.Description, "Version": p.Version, "LastModifiedDate": p.LastModifiedDate, "DataType": "text", "Tier": "Standard"})
			}
			out["Parameters"] = metadata
		} else {
			out["Parameters"] = filtered
		}
		if token != "" {
			out["NextToken"] = token
		}
		ssmJSON(w, 200, out)
	}
}
