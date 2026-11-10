package aws

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"
)

type logsRequest struct {
	LogGroupName        string           `json:"logGroupName"`
	LogGroupNamePrefix  string           `json:"logGroupNamePrefix"`
	LogGroupClass       string           `json:"logGroupClass"`
	LogStreamName       string           `json:"logStreamName"`
	LogStreamNamePrefix string           `json:"logStreamNamePrefix"`
	LogEvents           []LogsInputEvent `json:"logEvents"`
	SequenceToken       string           `json:"sequenceToken"`
	NextToken           string           `json:"nextToken"`
	Limit               *int             `json:"limit"`
	StartTime           *int64           `json:"startTime"`
	EndTime             *int64           `json:"endTime"`
	StartFromHead       bool             `json:"startFromHead"`
	Descending          bool             `json:"descending"`
	OrderBy             string           `json:"orderBy"`
	Unmask              bool             `json:"unmask"`
}

func logsJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func logsWriteError(w http.ResponseWriter, err error) {
	code, message, status := "ServiceUnavailableException", "Simulator log state operation failed.", 500
	var e logsFailure
	var legacy ssmFailure
	if errors.As(err, &e) {
		code, message, status = e.code, e.message, 400
	} else if errors.As(err, &legacy) && legacy.code == "AccessDeniedException" {
		code, message, status = legacy.code, legacy.message, 403
	}
	if code == "AccessDeniedException" {
		status = 403
	}
	if code == "UnknownOperationException" {
		status = 501
	}
	w.Header().Set("X-Amzn-Errortype", code)
	logsJSON(w, status, map[string]string{"__type": code, "message": message})
}

// logsUniqueJSON rejects ambiguous duplicate keys, nulls and excessive nesting
// before decoding the exact case-sensitive API field set.
func logsUniqueJSON(dec *json.Decoder, depth int) error {
	if depth > 32 {
		return logsInvalid("JSON nesting is limited to 32 levels.")
	}
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if t == nil {
		return logsInvalid("Null fields are unsupported.")
	}
	d, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	if d == '{' {
		seen := map[string]bool{}
		for dec.More() {
			key, e := dec.Token()
			if e != nil {
				return e
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return logsInvalid("Duplicate JSON fields are unsupported.")
			}
			seen[name] = true
			if e = logsUniqueJSON(dec, depth+1); e != nil {
				return e
			}
		}
	} else if d == '[' {
		for dec.More() {
			if err = logsUniqueJSON(dec, depth+1); err != nil {
				return err
			}
		}
	} else {
		return logsInvalid("Invalid JSON request.")
	}
	_, err = dec.Token()
	return err
}

// ServeHTTP handles only the bounded Logs JSON operations. The trusted
// Gateway supplies region and an IAM callback; HTTP/team authentication remains
// server middleware. Every request, including continuation requests, reauthorizes.
func (s *CloudWatchLogsStore) ServeHTTP(w http.ResponseWriter, r *http.Request, region string, authorize func(action, resource string) error) {
	fail := func(err error) { logsWriteError(w, err) }
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" || mediaErr != nil || mediaType != "application/x-amz-json-1.1" {
		fail(logsInvalid("Use JSON 1.1 root POST without query parameters."))
		return
	}
	if !slices.Contains(Regions, region) {
		fail(logsInvalid("Unsupported simulator region."))
		return
	}
	if selected := r.Header.Get("X-Simulator-Service"); selected != "" && selected != "logs" {
		fail(logsInvalid("Service selection and Logs target disagree."))
		return
	}
	if r.Header.Get("X-Amz-Security-Token") != "" {
		fail(logsInvalid("Session tokens are unsupported."))
		return
	}
	if a := r.Header.Get("Authorization"); a != "" {
		_, scope, ok := strings.Cut(a, "Credential=")
		scope, _, _ = strings.Cut(scope, ",")
		parts := strings.Split(scope, "/")
		if !ok || !strings.HasPrefix(a, "AWS4-HMAC-SHA256 ") || len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[2] != region || parts[3] != "logs" || parts[4] != "aws4_request" {
			fail(logsFailure{"AccessDeniedException", "Use dummy TCSIM credentials scoped to this region and logs; signatures are not real AWS authentication."})
			return
		}
	}
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "Logs_20140328.")
	allowed := map[string][]string{
		"CreateLogGroup": {"logGroupName", "logGroupClass"}, "DescribeLogGroups": {"logGroupNamePrefix", "logGroupClass", "nextToken", "limit"}, "DeleteLogGroup": {"logGroupName"},
		"CreateLogStream": {"logGroupName", "logStreamName"}, "DeleteLogStream": {"logGroupName", "logStreamName"}, "DescribeLogStreams": {"logGroupName", "logStreamNamePrefix", "orderBy", "descending", "nextToken", "limit"},
		"PutLogEvents": {"logGroupName", "logStreamName", "logEvents", "sequenceToken"}, "GetLogEvents": {"logGroupName", "logStreamName", "startTime", "endTime", "startFromHead", "nextToken", "limit", "unmask"},
	}
	fields, ok := allowed[op]
	if !ok || r.Header.Get("X-Amz-Target") != "Logs_20140328."+op {
		fail(logsFailure{"UnknownOperationException", "This CloudWatch Logs operation is not implemented."})
		return
	}
	// JSON escaping may expand a valid 1 MiB message substantially.
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		fail(logsInvalid("Request JSON exceeds the 8 MiB simulator transport limit."))
		return
	}
	if !utf8.Valid(data) {
		fail(logsInvalid("Request JSON must be UTF-8."))
		return
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err = logsUniqueJSON(dec, 0); err != nil {
		fail(logsInvalid("Expected one non-null JSON object with unique fields."))
		return
	}
	if _, err = dec.Token(); err != io.EOF {
		fail(logsInvalid("Expected exactly one JSON object."))
		return
	}
	var raw map[string]json.RawMessage
	var input logsRequest
	if json.Unmarshal(data, &raw) != nil || raw == nil || json.Unmarshal(data, &input) != nil {
		fail(logsInvalid("Invalid request object or field type."))
		return
	}
	for field := range raw {
		if !slices.Contains(fields, field) {
			fail(logsInvalid("Unsupported request field: " + field))
			return
		}
	}
	if op != "DescribeLogGroups" && !logsGroupName.MatchString(input.LogGroupName) {
		fail(logsInvalid("Invalid or missing logGroupName."))
		return
	}
	if op == "CreateLogStream" || op == "DeleteLogStream" || op == "PutLogEvents" || op == "GetLogEvents" {
		if !validLogsStream(input.LogStreamName) {
			fail(logsInvalid("Invalid or missing logStreamName."))
			return
		}
	}
	if input.LogGroupClass != "" && input.LogGroupClass != "STANDARD" {
		fail(logsInvalid("Only STANDARD log groups are implemented."))
		return
	}
	if _, present := raw["logGroupClass"]; present && input.LogGroupClass == "" {
		fail(logsInvalid("logGroupClass must be STANDARD when supplied."))
		return
	}
	if input.LogGroupNamePrefix != "" && !logsGroupName.MatchString(input.LogGroupNamePrefix) || input.LogStreamNamePrefix != "" && !validLogsStream(input.LogStreamNamePrefix) {
		fail(logsInvalid("Invalid log group or stream prefix."))
		return
	}
	if input.Unmask {
		fail(logsInvalid("Data protection and unmask are not implemented."))
		return
	}
	if op == "PutLogEvents" {
		var events []map[string]json.RawMessage
		if json.Unmarshal(raw["logEvents"], &events) != nil {
			fail(logsInvalid("logEvents is required."))
			return
		}
		for _, e := range events {
			if len(e) != 2 || e["timestamp"] == nil || e["message"] == nil {
				fail(logsInvalid("Each log event requires exactly timestamp and message."))
				return
			}
		}
	}
	resource := logsGroupARN(region, input.LogGroupName)
	if op == "DescribeLogGroups" {
		resource = "*"
	}
	if op == "CreateLogStream" || op == "DeleteLogStream" || op == "PutLogEvents" || op == "GetLogEvents" {
		resource = logsStreamARN(region, input.LogGroupName, input.LogStreamName)
	}
	if authorize == nil {
		fail(errors.New("missing Logs authorizer"))
		return
	}
	if err = authorize("logs:"+op, resource); err != nil {
		fail(err)
		return
	}
	ctx := r.Context()
	limit := 50
	if input.Limit != nil {
		limit = *input.Limit
	}
	var output any
	switch op {
	case "CreateLogGroup":
		err = s.CreateGroup(ctx, region, input.LogGroupName)
	case "DeleteLogGroup":
		err = s.DeleteGroup(ctx, region, input.LogGroupName)
	case "DeleteLogStream":
		err = s.DeleteStream(ctx, region, input.LogGroupName, input.LogStreamName)
	case "CreateLogStream":
		err = s.CreateStream(ctx, region, input.LogGroupName, input.LogStreamName)
	case "DescribeLogGroups":
		var groups []LogsGroup
		var next string
		groups, next, err = s.Groups(ctx, region, input.LogGroupNamePrefix, input.NextToken, limit)
		result := map[string]any{"logGroups": groups}
		if next != "" {
			result["nextToken"] = next
		}
		output = result
	case "DescribeLogStreams":
		var streams []LogsStream
		var next string
		streams, next, err = s.Streams(ctx, region, input.LogGroupName, LogsStreamsQuery{Prefix: input.LogStreamNamePrefix, Token: input.NextToken, OrderBy: input.OrderBy, Descending: input.Descending, Limit: limit})
		result := map[string]any{"logStreams": streams}
		if next != "" {
			result["nextToken"] = next
		}
		output = result
	case "PutLogEvents":
		var rejected *LogsRejected
		rejected, err = s.PutEvents(ctx, region, input.LogGroupName, input.LogStreamName, input.LogEvents)
		result := map[string]any{}
		if rejected != nil {
			result["rejectedLogEventsInfo"] = rejected
		}
		output = result
	case "GetLogEvents":
		if input.Limit == nil {
			limit = 10000
		}
		output, err = s.Events(ctx, region, input.LogGroupName, input.LogStreamName, LogsEventsQuery{Start: input.StartTime, End: input.EndTime, StartFromHead: input.StartFromHead, Limit: limit, Token: input.NextToken})
	}
	if err != nil {
		fail(err)
		return
	}
	logsJSON(w, 200, output)
}
