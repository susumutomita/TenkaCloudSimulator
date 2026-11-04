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
	"time"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// SQS uses the AWS JSON 1.0 protocol (X-Amz-Target: AmazonSQS.<Operation>,
// application/x-amz-json-1.0), which is what current AWS SDKs speak.
// The Query protocol, FIFO queues, dead-letter redrive, encryption settings,
// message timers beyond the documented subset and long-poll waits above the
// requested value are not simulated; unsupported fields are rejected, never
// silently ignored.

type sqsFailure struct{ code, message string }

func (e sqsFailure) Error() string { return e.message }

var queueNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

func validQueueName(name string) bool { return queueNamePattern.MatchString(name) }

func sqsJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func sqsError(w http.ResponseWriter, err error) {
	code, message, status := "InternalError", "Simulator queue state operation failed.", 500
	var e sqsFailure
	if errors.As(err, &e) {
		code, message, status = e.code, e.message, 400
		switch code {
		case "AccessDenied":
			status = 403
		case "UnknownOperationException":
			status = 501
		}
	}
	sqsJSON(w, status, map[string]string{"__type": code, "Message": message})
}

type sqsRequest struct {
	QueueName                   string
	QueueUrl                    string
	QueueNamePrefix             string
	Attributes                  map[string]string
	AttributeNames              []string
	MessageBody                 string
	DelaySeconds                *int
	MessageAttributes           map[string]SQSMessageAttribute
	MaxNumberOfMessages         int
	VisibilityTimeout           *int
	WaitTimeSeconds             int
	MaxResults                  int
	NextToken                   string
	ReceiptHandle               string
	MessageAttributeNames       []string
	MessageSystemAttributeNames []string
}

// queueNameFromURL accepts the QueueUrl this simulator issues
// (https://sqs.<region>.amazonaws.com/000000000000/<name> shape, on any host)
// or a bare queue name.
func queueNameFromURL(raw string) string {
	raw = strings.TrimSuffix(raw, "/")
	if i := strings.LastIndex(raw, "/"); i >= 0 {
		return raw[i+1:]
	}
	return raw
}

func (g *Gateway) serveSQS(w http.ResponseWriter, r *http.Request) {
	invalid := func(message string) { sqsError(w, sqsFailure{"InvalidParameterValue", message}) }
	if g.Queues == nil {
		sqsError(w, sqsFailure{"InternalError", "SQS store is not configured."})
		return
	}
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" {
		invalid("Use the regional root POST endpoint without query parameters.")
		return
	}
	if a := r.Header.Get("Authorization"); a != "" {
		_, credential, ok := strings.Cut(a, "Credential=")
		key, _, _ := strings.Cut(credential, "/")
		if !ok || !strings.HasPrefix(key, "TCSIM") {
			sqsError(w, sqsFailure{"AccessDenied", "Use unsigned requests or dummy TCSIM credentials."})
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
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AmazonSQS.")
	allowed := map[string][]string{
		"CreateQueue":             {"QueueName", "Attributes"},
		"ListQueues":              {"QueueNamePrefix", "MaxResults", "NextToken"},
		"GetQueueUrl":             {"QueueName"},
		"DeleteQueue":             {"QueueUrl"},
		"SendMessage":             {"QueueUrl", "MessageBody", "DelaySeconds", "MessageAttributes"},
		"ReceiveMessage":          {"QueueUrl", "MaxNumberOfMessages", "VisibilityTimeout", "WaitTimeSeconds", "MessageAttributeNames", "MessageSystemAttributeNames"},
		"DeleteMessage":           {"QueueUrl", "ReceiptHandle"},
		"ChangeMessageVisibility": {"QueueUrl", "ReceiptHandle", "VisibilityTimeout"},
		"GetQueueAttributes":      {"QueueUrl", "AttributeNames"},
	}
	fields, ok := allowed[op]
	if !ok {
		sqsError(w, sqsFailure{"UnknownOperationException", "This SQS operation is not implemented."})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 320<<10))
	if err != nil {
		invalid("SQS requests are limited to 320 KiB.")
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
	if body, supplied := raw["MessageBody"]; supplied && !validSQSJSONString(body) {
		invalid("MessageBody must encode Unicode without invalid UTF-8 or unpaired surrogates.")
		return
	}
	var input sqsRequest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		invalid("Invalid request field type.")
		return
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	authorize := func(resource string) bool {
		decision, err := g.Identities.DecisionContext(r.Context(), principal, "sqs:"+op, resource)
		if err != nil {
			sqsError(w, err)
			return false
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			sqsError(w, sqsFailure{"AccessDenied", "IAM evaluation: " + decision})
			return false
		}
		return true
	}
	queueARN := func(name string) string {
		return fmt.Sprintf("arn:aws:sqs:%s:000000000000:%s", region, name)
	}
	queueURL := func(name string) string {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		return fmt.Sprintf("%s://%s/000000000000/%s", scheme, r.Host, name)
	}
	// Resolve the target queue for queue-scoped operations.
	queueName := input.QueueName
	if input.QueueUrl != "" {
		queueName = queueNameFromURL(input.QueueUrl)
	}
	resource := "*"
	if op != "ListQueues" && queueName != "" {
		resource = queueARN(queueName)
	}
	if !authorize(resource) {
		return
	}
	loadQueue := func() (SQSQueue, bool) {
		if !validQueueName(queueName) {
			invalid("Use a queue name of 1–80 letters, digits, hyphens or underscores; FIFO (.fifo) queues are not simulated.")
			return SQSQueue{}, false
		}
		q, err := g.Queues.GetQueue(region, queueName)
		if err != nil {
			sqsError(w, err)
			return SQSQueue{}, false
		}
		return q, true
	}

	switch op {
	case "CreateQueue":
		if strings.HasSuffix(input.QueueName, ".fifo") {
			invalid("FIFO queues are not simulated.")
			return
		}
		if !validQueueName(input.QueueName) {
			invalid("Use a queue name of 1–80 letters, digits, hyphens or underscores.")
			return
		}
		q := SQSQueue{Name: input.QueueName, VisibilityTimeout: 30, DelaySeconds: 0, RetentionSeconds: 345600}
		for key, value := range input.Attributes {
			n, err := strconv.Atoi(value)
			if err != nil {
				sqsError(w, sqsFailure{"InvalidAttributeValue", "Attribute " + key + " must be an integer string."})
				return
			}
			switch key {
			case "VisibilityTimeout":
				if n < 0 || n > 43200 {
					sqsError(w, sqsFailure{"InvalidAttributeValue", "VisibilityTimeout must be 0–43200 seconds."})
					return
				}
				q.VisibilityTimeout = n
			case "DelaySeconds":
				if n < 0 || n > 900 {
					sqsError(w, sqsFailure{"InvalidAttributeValue", "DelaySeconds must be 0–900 seconds."})
					return
				}
				q.DelaySeconds = n
			case "MessageRetentionPeriod":
				if n < 60 || n > 1209600 {
					sqsError(w, sqsFailure{"InvalidAttributeValue", "MessageRetentionPeriod must be 60–1209600 seconds."})
					return
				}
				q.RetentionSeconds = n
			default:
				sqsError(w, sqsFailure{"InvalidAttributeName", "Unsupported queue attribute: " + key})
				return
			}
		}
		created, err := g.Queues.CreateQueue(region, q)
		if err != nil {
			sqsError(w, err)
			return
		}
		_ = created
		sqsJSON(w, 200, map[string]any{"QueueUrl": queueURL(input.QueueName)})
	case "ListQueues":
		queues, err := g.Queues.ListQueues(region, input.QueueNamePrefix)
		if err != nil {
			sqsError(w, err)
			return
		}
		limit := 1000
		if _, supplied := raw["MaxResults"]; supplied {
			if input.MaxResults < 1 || input.MaxResults > 1000 {
				invalid("MaxResults must be 1–1000.")
				return
			}
			limit = input.MaxResults
		}
		cursor := ""
		if input.NextToken != "" {
			decoded, e := base64.RawURLEncoding.DecodeString(input.NextToken)
			prefix := region + "|ListQueues|" + input.QueueNamePrefix + "|"
			if e != nil || !strings.HasPrefix(string(decoded), prefix) {
				invalid("Invalid pagination token for this request.")
				return
			}
			cursor = strings.TrimPrefix(string(decoded), prefix)
		}
		urls := []string{}
		last := ""
		for _, q := range queues {
			if q.Name <= cursor {
				continue
			}
			if len(urls) == limit {
				raw := region + "|ListQueues|" + input.QueueNamePrefix + "|" + last
				sqsJSON(w, 200, map[string]any{"QueueUrls": urls, "NextToken": base64.RawURLEncoding.EncodeToString([]byte(raw))})
				return
			}
			urls = append(urls, queueURL(q.Name))
			last = q.Name
		}
		sqsJSON(w, 200, map[string]any{"QueueUrls": urls})
	case "GetQueueUrl":
		if _, ok := loadQueue(); !ok {
			return
		}
		sqsJSON(w, 200, map[string]any{"QueueUrl": queueURL(queueName)})
	case "DeleteQueue":
		if !validQueueName(queueName) {
			invalid("Invalid queue URL.")
			return
		}
		if err := g.Queues.DeleteQueue(region, queueName); err != nil {
			sqsError(w, err)
			return
		}
		sqsJSON(w, 200, map[string]any{})
	case "SendMessage":
		q, ok := loadQueue()
		if !ok {
			return
		}
		if err := validateSQSBody(input.MessageBody); err != nil {
			sqsError(w, err)
			return
		}
		delay := -1
		if input.DelaySeconds != nil {
			if *input.DelaySeconds < 0 || *input.DelaySeconds > 900 {
				invalid("DelaySeconds must be 0–900 seconds.")
				return
			}
			delay = *input.DelaySeconds
		}
		attrs := map[string]SQSMessageAttribute{}
		for name, a := range input.MessageAttributes {
			if len(name) == 0 || len(name) > 256 {
				invalid("Invalid message attribute name.")
				return
			}
			base := a.DataType
			if i := strings.Index(base, "."); i >= 0 {
				base = base[:i]
			}
			switch base {
			case "String", "Number":
				if a.StringValue == nil || a.BinaryValue != nil {
					invalid("Message attribute " + name + " requires a StringValue.")
					return
				}
			case "Binary":
				if a.BinaryValue == nil || a.StringValue != nil {
					invalid("Message attribute " + name + " requires a BinaryValue.")
					return
				}
				if _, err := base64.StdEncoding.DecodeString(*a.BinaryValue); err != nil {
					invalid("Message attribute " + name + " has an invalid base64 BinaryValue.")
					return
				}
			default:
				invalid("Unsupported message attribute data type for " + name + ": " + a.DataType)
				return
			}
			attrs[name] = a
		}
		if len(attrs) > 10 {
			invalid("A message can have at most 10 attributes.")
			return
		}
		m, err := g.Queues.Send(region, q, input.MessageBody, attrs, delay)
		if err != nil {
			sqsError(w, err)
			return
		}
		out := map[string]any{"MessageId": m.ID, "MD5OfMessageBody": m.MD5OfBody}
		if m.MD5OfAttributes != "" {
			out["MD5OfMessageAttributes"] = m.MD5OfAttributes
		}
		sqsJSON(w, 200, out)
	case "ReceiveMessage":
		q, ok := loadQueue()
		if !ok {
			return
		}
		maxN := 1
		if _, supplied := raw["MaxNumberOfMessages"]; supplied {
			if input.MaxNumberOfMessages < 1 || input.MaxNumberOfMessages > 10 {
				invalid("MaxNumberOfMessages must be 1–10.")
				return
			}
			maxN = input.MaxNumberOfMessages
		}
		visibility := q.VisibilityTimeout
		if input.VisibilityTimeout != nil {
			if *input.VisibilityTimeout < 0 || *input.VisibilityTimeout > 43200 {
				invalid("VisibilityTimeout must be 0–43200 seconds.")
				return
			}
			visibility = *input.VisibilityTimeout
		}
		if input.WaitTimeSeconds < 0 || input.WaitTimeSeconds > 20 {
			invalid("WaitTimeSeconds must be 0–20.")
			return
		}
		deadline := time.Now().Add(time.Duration(input.WaitTimeSeconds) * time.Second)
		var messages []SQSMessage
		for {
			var err error
			messages, err = g.Queues.Receive(region, q, maxN, visibility)
			if err != nil {
				sqsError(w, err)
				return
			}
			if len(messages) > 0 || time.Now().After(deadline) || input.WaitTimeSeconds == 0 {
				break
			}
			select {
			case <-r.Context().Done():
				sqsJSON(w, 200, map[string]any{})
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
		out := []map[string]any{}
		for _, m := range messages {
			entry := map[string]any{
				"MessageId":     m.ID,
				"ReceiptHandle": m.ReceiptHandle,
				"MD5OfBody":     m.MD5OfBody,
				"Body":          m.Body,
				"Attributes": map[string]string{
					"SenderId":                         "000000000000",
					"SentTimestamp":                    strconv.FormatInt(int64(m.Sent*1000), 10),
					"ApproximateReceiveCount":          strconv.Itoa(m.ReceiveCount),
					"ApproximateFirstReceiveTimestamp": strconv.FormatInt(int64(m.Sent*1000), 10),
				},
			}
			if len(m.Attributes) > 0 {
				if len(input.MessageAttributeNames) > 0 {
					filtered := map[string]SQSMessageAttribute{}
					for _, name := range input.MessageAttributeNames {
						if name == "All" || name == ".*" {
							filtered = m.Attributes
							break
						}
						if a, ok := m.Attributes[name]; ok {
							filtered[name] = a
						}
					}
					if len(filtered) > 0 {
						entry["MessageAttributes"] = filtered
					}
				} else {
					entry["MessageAttributes"] = m.Attributes
				}
			}
			out = append(out, entry)
		}
		if len(out) == 0 {
			// Real SQS omits the Messages element when nothing is delivered.
			sqsJSON(w, 200, map[string]any{})
			return
		}
		sqsJSON(w, 200, map[string]any{"Messages": out})
	case "DeleteMessage":
		if _, ok := loadQueue(); !ok {
			return
		}
		if input.ReceiptHandle == "" {
			invalid("ReceiptHandle is required.")
			return
		}
		if err := g.Queues.DeleteMessage(region, queueName, input.ReceiptHandle); err != nil {
			sqsError(w, err)
			return
		}
		sqsJSON(w, 200, map[string]any{})
	case "ChangeMessageVisibility":
		if _, ok := loadQueue(); !ok {
			return
		}
		if input.ReceiptHandle == "" || input.VisibilityTimeout == nil {
			invalid("ReceiptHandle and VisibilityTimeout are required.")
			return
		}
		if *input.VisibilityTimeout < 0 || *input.VisibilityTimeout > 43200 {
			invalid("VisibilityTimeout must be 0–43200 seconds.")
			return
		}
		if err := g.Queues.ChangeVisibility(region, queueName, input.ReceiptHandle, *input.VisibilityTimeout); err != nil {
			sqsError(w, err)
			return
		}
		sqsJSON(w, 200, map[string]any{})
	case "GetQueueAttributes":
		q, ok := loadQueue()
		if !ok {
			return
		}
		visible, notVisible, err := g.Queues.Counts(region, q)
		if err != nil {
			sqsError(w, err)
			return
		}
		all := map[string]string{
			"QueueArn":                              queueARN(queueName),
			"CreatedTimestamp":                      strconv.FormatInt(int64(q.Created), 10),
			"VisibilityTimeout":                     strconv.Itoa(q.VisibilityTimeout),
			"DelaySeconds":                          strconv.Itoa(q.DelaySeconds),
			"MessageRetentionPeriod":                strconv.Itoa(q.RetentionSeconds),
			"ApproximateNumberOfMessages":           strconv.Itoa(visible),
			"ApproximateNumberOfMessagesNotVisible": strconv.Itoa(notVisible),
		}
		out := map[string]string{}
		if len(input.AttributeNames) == 0 {
			sqsError(w, sqsFailure{"InvalidAttributeName", "AttributeNames is required; use All for every supported attribute."})
			return
		}
		// Validate every name before applying All, so order cannot hide an unsupported field.
		for _, name := range input.AttributeNames {
			if _, supported := all[name]; name != "All" && !supported {
				sqsError(w, sqsFailure{"InvalidAttributeName", "Unsupported queue attribute: " + name})
				return
			}
		}
		for _, name := range input.AttributeNames {
			if name == "All" {
				out = all
				break
			}
			out[name] = all[name]
		}
		sqsJSON(w, 200, map[string]any{"Attributes": out})
	}
}

// AWS permits XML-compatible Unicode in message bodies. The byte limit is this
// simulator's explicit 256 KiB local bound, not AWS's current 1 MiB maximum.
func validateSQSBody(body string) error {
	if len(body) == 0 || len(body) > 262144 || !utf8.ValidString(body) {
		return sqsFailure{"InvalidParameterValue", "Use a 1–262144 byte UTF-8 message body (local limit)."}
	}
	for _, r := range body {
		if r == 0x9 || r == 0xA || r == 0xD || (r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || (r >= 0x10000 && r <= 0x10FFFF) {
			continue
		}
		return sqsFailure{"InvalidMessageContents", "The message contains a character SQS does not permit."}
	}
	return nil
}

// JSON's Go decoder replaces malformed UTF-8 and isolated UTF-16 surrogates.
// Reject those body encodings before decode rather than store a changed payload.
// Syntax is already checked by json.Unmarshal into raw request fields.
func validSQSJSONString(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		if i+1 >= len(raw) {
			return false
		}
		if raw[i+1] != 'u' {
			i++
			continue
		}
		if i+6 > len(raw) {
			return false
		}
		value, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
		if err != nil || (value >= 0xDC00 && value <= 0xDFFF) {
			return false
		}
		if value >= 0xD800 && value <= 0xDBFF {
			if i+12 > len(raw) || string(raw[i+6:i+8]) != "\\u" {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+8:i+12]), 16, 16)
			if err != nil || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			i += 6
		}
		i += 5
	}
	return true
}
