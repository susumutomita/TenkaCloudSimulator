package aws

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Amazon SNS Query protocol subset (API version 2010-03-31).
// Scope and deliberate deviations are documented in docs/sns.md.
const snsNamespace = "http://sns.amazonaws.com/doc/2010-03-31/"

var (
	snsTopicName      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	snsFIFOTopicName  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,251}\.fifo$`)
	snsAttributeField = regexp.MustCompile(`^Attributes\.entry\.([1-9][0-9]*)\.(key|value)$`)
	// Protocols whose subscriptions are stored as locally confirmed.
	// "sqs" is handled separately: it is only accepted when a
	// SNSQueueFanout adapter is wired (see below), and is otherwise
	// rejected explicitly instead of silently simulating queue delivery.
	snsProtocols = map[string]bool{
		"http": true, "https": true, "email": true, "email-json": true,
		"sms": true, "lambda": true, "application": true, "firehose": true,
	}
)

func isSNS(r *http.Request) bool {
	if service := r.Header.Get("X-Simulator-Service"); service != "" {
		return service == "sns"
	}
	return strings.Contains(r.Header.Get("Authorization"), "/sns/aws4_request")
}

// SNSQueueFanout is the contract SNS needs from the SQS workstream to
// deliver published messages into queues. The SQS branch owns SQSStore;
// an adapter in the composition layer (see docs/sns.md) implements this
// interface with SQSStore.GetQueue/Send. A nil Gateway.QueueFanout means
// no adapter is wired in this build, and sqs subscriptions are then
// rejected explicitly instead of being silently accepted.
type SNSQueueFanout interface {
	QueueExists(region, queueName string) (bool, error)
	DeliverToQueue(region, queueName, body string) error
}

// snsQueueEndpoint parses an SQS queue ARN of the form
// arn:aws:sqs:{region}:000000000000:{queue}; anything else is not a queue
// endpoint this simulator can fan out to.
func snsQueueEndpoint(endpoint string) (region, queue string, ok bool) {
	parts := strings.Split(endpoint, ":")
	if len(parts) != 6 || parts[0] != "arn" || parts[1] != "aws" || parts[2] != "sqs" || parts[4] != "000000000000" || parts[5] == "" {
		return "", "", false
	}
	return parts[3], parts[5], true
}

// snsNotificationEnvelope renders the JSON document real SNS places in an
// SQS queue for a raw publish (MessageAttributes are not in this subset).
func snsNotificationEnvelope(messageID string, topic SNSTopic, message, subject string) string {
	envelope := map[string]string{
		"Type":      "Notification",
		"MessageId": messageID,
		"TopicArn":  topic.ARN,
		"Message":   message,
		"Timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if subject != "" {
		envelope["Subject"] = subject
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return message
	}
	return string(data)
}

func snsResponse(w http.ResponseWriter, action string, result any) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(200)
	_, _ = io.WriteString(w, xml.Header)
	encoder := xml.NewEncoder(w)
	root := xml.StartElement{Name: xml.Name{Local: action + "Response"}, Attr: []xml.Attr{{Name: xml.Name{Local: "xmlns"}, Value: snsNamespace}}}
	_ = encoder.EncodeToken(root)
	if result != nil {
		_ = encoder.EncodeElement(result, xml.StartElement{Name: xml.Name{Local: action + "Result"}})
	}
	_ = encoder.EncodeElement(struct {
		RequestID string `xml:"RequestId"`
	}{fmt.Sprintf("sns-local-%016x", sequence.Add(1))}, xml.StartElement{Name: xml.Name{Local: "ResponseMetadata"}})
	_ = encoder.EncodeToken(root.End())
	_ = encoder.Flush()
}

func snsError(w http.ResponseWriter, err error) {
	code, message, status := "InternalError", "Simulator SNS state operation failed.", 500
	var failure ssmFailure
	if errors.As(err, &failure) {
		code, message, status = failure.code, failure.message, 400
		switch code {
		case "InvalidParameterException":
			code = "InvalidParameter"
		case "NotFoundException":
			code, status = "NotFound", 404
		case "AuthorizationErrorException":
			code, status = "AuthorizationError", 403
		case "LimitExceededException":
			status = 409
		case "RequestEntityTooLarge":
			status = 413
		case "InternalError":
			status = 500
		}
	}
	type detail struct {
		Type    string `xml:"Type"`
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	type response struct {
		XMLName   xml.Name `xml:"ErrorResponse"`
		Namespace string   `xml:"xmlns,attr"`
		Error     detail
		RequestID string `xml:"RequestId"`
	}
	kind := "Sender"
	if status >= 500 {
		kind = "Receiver"
	}
	writeXML(w, status, response{Namespace: snsNamespace, Error: detail{kind, code, message}, RequestID: fmt.Sprintf("sns-local-%016x", sequence.Add(1))})
}

type snsMemberString struct {
	TopicArn string `xml:"TopicArn"`
}
type snsTopicsResult struct {
	Topics    []snsMemberString `xml:"Topics>member"`
	NextToken string            `xml:"NextToken,omitempty"`
}
type snsSubscriptionWire struct {
	SubscriptionArn string
	TopicArn        string
	Protocol        string
	Endpoint        string
	Owner           string
}
type snsSubscriptionsResult struct {
	Subscriptions []snsSubscriptionWire `xml:"Subscriptions>member"`
	NextToken     string                `xml:"NextToken,omitempty"`
}
type snsAttributeEntry struct {
	Key   string `xml:"key"`
	Value string `xml:"value"`
}
type snsTopicAttributesResult struct {
	Attributes []snsAttributeEntry `xml:"Attributes>entry"`
}

func attributeEntries(form url.Values) (map[string]string, error) {
	type entry struct {
		key, value       string
		hasKey, hasValue bool
	}
	entries := map[string]entry{}
	for name, values := range form {
		if !strings.HasPrefix(name, "Attributes.entry.") {
			continue
		}
		parts := snsAttributeField.FindStringSubmatch(name)
		if len(parts) != 3 || len(values) != 1 {
			return nil, ssmFailure{"InvalidParameterException", "Malformed or duplicate topic attribute field."}
		}
		item := entries[parts[1]]
		if parts[2] == "key" {
			item.key, item.hasKey = values[0], true
		} else {
			item.value, item.hasValue = values[0], true
		}
		entries[parts[1]] = item
	}
	attrs := map[string]string{}
	for _, item := range entries {
		if !item.hasKey || !item.hasValue {
			return nil, ssmFailure{"InvalidParameterException", "Attributes entries require paired key and value."}
		}
		if _, dup := attrs[item.key]; dup {
			return nil, ssmFailure{"InvalidParameterException", "Duplicate topic attribute: " + item.key}
		}
		attrs[item.key] = item.value
	}
	return attrs, nil
}

func snsPage[T any](items []T, token, scope string, limit int, arnOf func(T) string) ([]T, string, error) {
	start := 0
	if token != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "SNS1."))
		bound, cursor, ok := strings.Cut(string(decoded), "\x00")
		if err != nil || len(token) > 2048 || !strings.HasPrefix(token, "SNS1.") || !ok || bound != scope || cursor == "" || !validText(cursor) {
			return nil, "", ssmFailure{"InvalidParameterException", "Invalid NextToken for this request."}
		}
		for start < len(items) && arnOf(items[start]) <= cursor {
			start++
		}
	}
	end := start + limit
	next := ""
	if end < len(items) {
		next = "SNS1." + base64.RawURLEncoding.EncodeToString([]byte(scope+"\x00"+arnOf(items[end-1])))
		items = items[start:end]
	} else {
		items = items[start:]
	}
	return items, next, nil
}

func (g *Gateway) serveSNS(w http.ResponseWriter, r *http.Request) {
	invalid := func(message string) { snsError(w, ssmFailure{"ValidationError", message}) }
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" {
		invalid("Use the regional root POST endpoint without query parameters.")
		return
	}
	if r.Header.Get("X-Amz-Target") != "" {
		invalid("SNS uses Query/XML, not JSON target dispatch.")
		return
	}
	if g.SNS == nil || g.Identities == nil {
		snsError(w, ssmFailure{"InternalError", "SNS state is unavailable."})
		return
	}
	if r.Header.Get("X-Amz-Security-Token") != "" {
		snsError(w, ssmFailure{"AuthorizationErrorException", "Session tokens are not supported."})
		return
	}
	if a := r.Header.Get("Authorization"); a != "" {
		_, credential, ok := strings.Cut(a, "Credential=")
		credential, _, _ = strings.Cut(credential, ",")
		parts := strings.Split(credential, "/")
		if !ok || len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[1] == "" || parts[3] != "sns" || parts[4] != "aws4_request" {
			snsError(w, ssmFailure{"AuthorizationErrorException", "Use unsigned requests or dummy TCSIM credentials with an SNS signing scope."})
			return
		}
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
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			snsError(w, ssmFailure{"RequestEntityTooLarge", "SNS Query requests are limited to 1 MiB."})
		} else {
			invalid("Could not read SNS Query body.")
		}
		return
	}
	form, err := url.ParseQuery(string(data))
	if err != nil {
		invalid("Malformed SNS Query body.")
		return
	}
	for _, values := range form {
		if len(values) != 1 {
			invalid("Duplicate SNS Query fields are unsupported.")
			return
		}
	}
	if form.Get("Version") != "2010-03-31" {
		invalid("Use SNS Version 2010-03-31.")
		return
	}
	action := form.Get("Action")
	fields := map[string][]string{
		"CreateTopic":              {"Name"},
		"ListTopics":               {"NextToken"},
		"DeleteTopic":              {"TopicArn"},
		"GetTopicAttributes":       {"TopicArn"},
		"Subscribe":                {"TopicArn", "Protocol", "Endpoint", "ReturnSubscriptionArn"},
		"Unsubscribe":              {"SubscriptionArn"},
		"ListSubscriptions":        {"NextToken"},
		"ListSubscriptionsByTopic": {"TopicArn", "NextToken"},
		"Publish":                  {"TopicArn", "Message", "Subject"},
	}
	allowed, ok := fields[action]
	if !ok {
		snsError(w, ssmFailure{"InvalidAction", "This SNS operation is not implemented: " + action})
		return
	}
	for key := range form {
		if key == "Action" || key == "Version" || strings.HasPrefix(key, "Attributes.entry.") {
			continue
		}
		found := false
		for _, f := range allowed {
			if key == f {
				found = true
			}
		}
		if !found {
			invalid("Unsupported request field: " + key)
			return
		}
	}
	hasAttributes := false
	for key := range form {
		if strings.HasPrefix(key, "Attributes.entry.") {
			hasAttributes = true
		}
	}
	if hasAttributes && action != "CreateTopic" {
		invalid("Attributes are only supported for CreateTopic in this subset.")
		return
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	authorize := func(resource string) bool {
		decision, err := g.Identities.DecisionContext(r.Context(), principal, "sns:"+action, resource)
		if err != nil {
			snsError(w, err)
			return false
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			snsError(w, ssmFailure{"AuthorizationErrorException", "IAM evaluation: " + decision})
			return false
		}
		return true
	}
	topicByARN := func(arn string) (SNSTopic, bool) {
		topic, err := g.SNS.GetTopic(arn)
		if err != nil {
			snsError(w, err)
			return SNSTopic{}, false
		}
		if topic.Region != region {
			snsError(w, ssmFailure{"NotFoundException", "Topic does not exist in this region."})
			return SNSTopic{}, false
		}
		return topic, true
	}
	pageLimit := 100

	switch action {
	case "CreateTopic":
		name := form.Get("Name")
		attrs, err := attributeEntries(form)
		if err != nil {
			snsError(w, err)
			return
		}
		displayName, fifo := "", false
		for key, value := range attrs {
			switch key {
			case "DisplayName":
				if !validText(value) || utf8.RuneCountInString(value) > 100 {
					invalid("DisplayName requires at most 100 UTF-8 characters without unsupported XML controls.")
					return
				}
				displayName = value
			case "FifoTopic":
				if value != "true" && value != "false" {
					invalid("FifoTopic must be true or false.")
					return
				}
				fifo = value == "true"
			default:
				snsError(w, ssmFailure{"InvalidParameterException", "Unsupported topic attribute: " + key})
				return
			}
		}
		if fifo && !snsFIFOTopicName.MatchString(name) || !fifo && !snsTopicName.MatchString(name) {
			snsError(w, ssmFailure{"InvalidParameterException", "Topic names are 1-256 alphanumeric, hyphen or underscore characters; FIFO topic names must end in .fifo."})
			return
		}
		arn := snsARN(region, name)
		if !authorize(arn) {
			return
		}
		topic, _, err := g.SNS.CreateTopic(region, name, displayName, fifo)
		if err != nil {
			snsError(w, err)
			return
		}
		snsResponse(w, action, struct {
			TopicArn string
		}{topic.ARN})
	case "ListTopics":
		if !authorize("*") {
			return
		}
		topics, err := g.SNS.ListTopics(r.Context(), region)
		if err != nil {
			snsError(w, err)
			return
		}
		page, next, err := snsPage(topics, form.Get("NextToken"), action+":"+region, pageLimit, func(t SNSTopic) string { return t.ARN })
		if err != nil {
			snsError(w, err)
			return
		}
		result := snsTopicsResult{NextToken: next}
		for _, t := range page {
			result.Topics = append(result.Topics, snsMemberString{TopicArn: t.ARN})
		}
		if result.Topics == nil {
			result.Topics = []snsMemberString{}
		}
		snsResponse(w, action, result)
	case "DeleteTopic":
		arn := form.Get("TopicArn")
		parts := strings.Split(arn, ":")
		if len(parts) != 6 || parts[0] != "arn" || parts[1] != "aws" || parts[2] != "sns" || parts[4] != "000000000000" || (!snsTopicName.MatchString(parts[5]) && !snsFIFOTopicName.MatchString(parts[5])) {
			snsError(w, ssmFailure{"InvalidParameterException", "TopicArn must be a simulator SNS topic ARN."})
			return
		}
		if parts[3] != region {
			snsError(w, ssmFailure{"NotFoundException", "Topic does not exist in this region."})
			return
		}
		if !authorize(arn) {
			return
		}
		if err := g.SNS.DeleteTopic(arn); err != nil {
			snsError(w, err)
			return
		}
		snsResponse(w, action, nil)
	case "GetTopicAttributes":
		arn := form.Get("TopicArn")
		topic, ok := topicByARN(arn)
		if !ok {
			return
		}
		if !authorize(arn) {
			return
		}
		subs, err := g.SNS.ListSubscriptionsByTopic(r.Context(), arn)
		if err != nil {
			snsError(w, err)
			return
		}
		entries := []snsAttributeEntry{
			{"TopicArn", topic.ARN},
			{"Owner", "000000000000"},
			{"DisplayName", topic.DisplayName},
			{"SubscriptionsConfirmed", strconv.Itoa(len(subs))},
			{"SubscriptionsPending", "0"},
			{"SubscriptionsDeleted", "0"},
		}
		if topic.FIFO {
			entries = append(entries, snsAttributeEntry{"FifoTopic", "true"})
		}
		snsResponse(w, action, snsTopicAttributesResult{Attributes: entries})
	case "Subscribe":
		if value, present := form["ReturnSubscriptionArn"]; present && value[0] != "true" && value[0] != "false" {
			invalid("ReturnSubscriptionArn must be true or false.")
			return
		}
		arn := form.Get("TopicArn")
		topic, ok := topicByARN(arn)
		if !ok {
			return
		}
		protocol := strings.ToLower(form.Get("Protocol"))
		endpoint := form.Get("Endpoint")
		if protocol == "sqs" {
			if g.QueueFanout == nil {
				snsError(w, ssmFailure{"InvalidParameterException", "SNS-to-SQS fanout is not integrated in this build: no SQS fanout adapter is wired, so the subscription was not created."})
				return
			}
			queueRegion, queueName, ok := snsQueueEndpoint(endpoint)
			if !ok || queueRegion != region {
				snsError(w, ssmFailure{"InvalidParameterException", "SQS endpoints must be a queue ARN in this region: arn:aws:sqs:" + region + ":000000000000:<queue>."})
				return
			}
			exists, err := g.QueueFanout.QueueExists(region, queueName)
			if err != nil {
				snsError(w, err)
				return
			}
			if !exists {
				snsError(w, ssmFailure{"NotFoundException", "Queue does not exist: " + queueName})
				return
			}
		} else if !snsProtocols[protocol] {
			snsError(w, ssmFailure{"InvalidParameterException", "Unsupported subscription protocol: " + protocol})
			return
		}
		if endpoint == "" || len(endpoint) > 2048 || !validText(endpoint) {
			snsError(w, ssmFailure{"InvalidParameterException", "Endpoint is required and limited to 2048 bytes."})
			return
		}
		if protocol == "lambda" && !strings.HasPrefix(endpoint, "arn:aws:lambda:") {
			snsError(w, ssmFailure{"InvalidParameterException", "Lambda endpoints must be Lambda function ARNs."})
			return
		}
		if !authorize(arn) {
			return
		}
		sub, err := g.SNS.Subscribe(topic, protocol, endpoint)
		if err != nil {
			snsError(w, err)
			return
		}
		snsResponse(w, action, struct {
			SubscriptionArn string
		}{sub.ARN})
	case "Unsubscribe":
		arn := form.Get("SubscriptionArn")
		sub, err := g.SNS.GetSubscription(arn)
		if err != nil {
			snsError(w, err)
			return
		}
		if _, ok := topicByARN(sub.TopicARN); !ok {
			return
		}
		if !authorize(sub.TopicARN) {
			return
		}
		if err := g.SNS.Unsubscribe(arn); err != nil {
			snsError(w, err)
			return
		}
		snsResponse(w, action, nil)
	case "ListSubscriptions", "ListSubscriptionsByTopic":
		var subs []SNSSubscription
		var err error
		if action == "ListSubscriptions" {
			if !authorize("*") {
				return
			}
			subs, err = g.SNS.ListSubscriptions(r.Context(), region)
		} else {
			arn := form.Get("TopicArn")
			if _, ok := topicByARN(arn); !ok {
				return
			}
			if !authorize(arn) {
				return
			}
			subs, err = g.SNS.ListSubscriptionsByTopic(r.Context(), arn)
		}
		if err != nil {
			snsError(w, err)
			return
		}
		page, next, err := snsPage(subs, form.Get("NextToken"), action+":"+region+":"+form.Get("TopicArn"), pageLimit, func(s SNSSubscription) string { return s.ARN })
		if err != nil {
			snsError(w, err)
			return
		}
		result := snsSubscriptionsResult{NextToken: next}
		for _, s := range page {
			result.Subscriptions = append(result.Subscriptions, snsSubscriptionWire{SubscriptionArn: s.ARN, TopicArn: s.TopicARN, Protocol: s.Protocol, Endpoint: s.Endpoint, Owner: "000000000000"})
		}
		if result.Subscriptions == nil {
			result.Subscriptions = []snsSubscriptionWire{}
		}
		snsResponse(w, action, result)
	case "Publish":
		arn := form.Get("TopicArn")
		topic, ok := topicByARN(arn)
		if !ok {
			return
		}
		message := form.Get("Message")
		subject := form.Get("Subject")
		if message == "" || len(message) > 262144 || !utf8.ValidString(message) {
			snsError(w, ssmFailure{"InvalidParameterException", "Message is required and limited to 256 KiB of UTF-8."})
			return
		}
		if !utf8.ValidString(subject) || utf8.RuneCountInString(subject) >= 100 || strings.ContainsFunc(subject, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) {
			snsError(w, ssmFailure{"InvalidParameterException", "Subject must be UTF-8 text with fewer than 100 characters and no line breaks or control characters."})
			return
		}
		if !authorize(arn) {
			return
		}
		if topic.FIFO {
			snsError(w, ssmFailure{"InvalidParameterException", "FIFO publication, ordering and deduplication are unsupported; no message was recorded."})
			return
		}
		subs, err := g.SNS.ListSubscriptionsByTopic(r.Context(), arn)
		if err != nil {
			snsError(w, err)
			return
		}
		id := snsRandomID()
		if g.QueueFanout != nil {
			envelope := snsNotificationEnvelope(id, topic, message, subject)
			for _, sub := range subs {
				if sub.Protocol != "sqs" {
					continue
				}
				queueRegion, queueName, ok := snsQueueEndpoint(sub.Endpoint)
				if !ok || queueRegion != topic.Region {
					snsError(w, ssmFailure{"InternalError", "Saved SQS subscription endpoint is invalid for this topic."})
					return
				}
				if err := g.QueueFanout.DeliverToQueue(topic.Region, queueName, envelope); err != nil {
					// SQLite publish records are not committed on failure. Earlier
					// callback effects cannot be rolled back by this interface;
					// the standalone service intentionally wires no adapter.
					snsError(w, err)
					return
				}
			}
		}
		id, _, err = g.SNS.PublishWithID(topic, id, message, subject)
		if err != nil {
			snsError(w, err)
			return
		}
		snsResponse(w, action, struct {
			MessageId string
		}{id})
	}
}
