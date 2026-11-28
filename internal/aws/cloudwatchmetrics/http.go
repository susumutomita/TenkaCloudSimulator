package cloudwatchmetrics

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

const xmlNamespace = "http://monitoring.amazonaws.com/doc/2010-08-01/"
const bodyLimit = 1 << 20

type requestError struct {
	code, message string
	status        int
}

func (e requestError) Error() string { return e.message }
func invalid(s string) error         { return requestError{"InvalidParameterValue", s, 400} }
func missing(s string) error {
	return requestError{"MissingParameter", "Missing required " + s + ".", 400}
}

func writeXML(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, err error) {
	var e requestError
	if !errors.As(err, &e) {
		e = requestError{"InternalServiceError", "Metric storage operation failed.", 500}
	}
	kind := "Sender"
	if e.status >= 500 {
		kind = "Receiver"
	}
	type detail struct{ Type, Code, Message string }
	writeXML(w, e.status, struct {
		XMLName   xml.Name
		Error     detail
		RequestID string `xml:"RequestId"`
	}{xml.Name{Space: xmlNamespace, Local: "ErrorResponse"}, detail{kind, e.code, e.message}, "local-cloudwatch"})
}

func readForm(r *http.Request) (form, error) {
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" ||
		len(params) > 1 || len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8") {
		return nil, invalid("Expected a URL-encoded Query request.")
	}
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("X-Amz-Target") != "" {
		return nil, invalid("Use Query POST / without a target or query string.")
	}
	if len(r.Header.Values("Content-Type")) != 1 || len(r.Header.Values("Content-Encoding")) > 1 {
		return nil, invalid("Ambiguous body headers.")
	}
	wire, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
	if err != nil || len(wire) > bodyLimit {
		return nil, invalid("Metric request exceeds the local wire limit or is unreadable.")
	}
	raw := wire
	switch strings.ToLower(r.Header.Get("Content-Encoding")) {
	case "":
	case "gzip":
		reader := bytes.NewReader(wire)
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return nil, invalid("Invalid compressed metric request.")
		}
		gz.Multistream(false)
		raw, err = io.ReadAll(io.LimitReader(gz, bodyLimit+1))
		closeErr := gz.Close()
		if err != nil || closeErr != nil || len(raw) > bodyLimit || reader.Len() != 0 {
			return nil, invalid("Expected one complete metric gzip member within the expanded limit.")
		}
	default:
		return nil, invalid("Unsupported content encoding.")
	}
	if bytes.Count(raw, []byte("&")) >= 10000 {
		return nil, invalid("Too many Query fields.")
	}
	values, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil, invalid("Malformed URL-encoded metric request.")
	}
	for _, v := range values {
		if len(v) != 1 {
			return nil, invalid("Duplicate Query field.")
		}
	}
	return form(values), nil
}

// ServeQuery receives a trusted region and uses the gateway's authenticated
// IAM decision before reading or mutating sample state.
func (s *Store) ServeQuery(w http.ResponseWriter, r *http.Request, region string, authorize func(string) (bool, error)) {
	fields, err := readForm(r)
	if err != nil {
		writeError(w, err)
		return
	}
	action, _ := fields.take("Action")
	version, _ := fields.take("Version")
	if version != "2010-08-01" {
		writeError(w, invalid("Expected CloudWatch version 2010-08-01."))
		return
	}
	if action != "PutMetricData" && action != "GetMetricStatistics" {
		writeError(w, requestError{"InvalidAction", "Unsupported CloudWatch action.", 400})
		return
	}
	if action != "PutMetricData" && r.Header.Get("Content-Encoding") != "" {
		writeError(w, invalid("Only PutMetricData accepts compression."))
		return
	}
	allowed, err := authorize("cloudwatch:" + action)
	if err != nil {
		writeError(w, err)
		return
	}
	if !allowed {
		writeError(w, requestError{"AccessDenied", "CloudWatch action is not allowed.", 403})
		return
	}
	namespace, err := fields.namespace()
	if err != nil {
		writeError(w, err)
		return
	}
	type result struct {
		Label      string
		Datapoints struct {
			Members []datapoint `xml:"member"`
		}
	}
	reply := struct {
		XMLName  xml.Name
		Result   *result `xml:"GetMetricStatisticsResult,omitempty"`
		Metadata struct {
			RequestID string `xml:"RequestId"`
		} `xml:"ResponseMetadata"`
	}{XMLName: xml.Name{Space: xmlNamespace, Local: action + "Response"}}
	reply.Metadata.RequestID = "local-cloudwatch"
	now := s.now().UTC()
	switch action {
	case "PutMetricData":
		samples, err := fields.samples(now)
		if err == nil {
			err = s.put(r.Context(), region, namespace, samples, now)
		}
		if err != nil {
			writeError(w, err)
			return
		}
	case "GetMetricStatistics":
		q, err := fields.query()
		if err != nil {
			writeError(w, err)
			return
		}
		points, err := s.get(r.Context(), region, namespace, q, now)
		if err != nil {
			writeError(w, err)
			return
		}
		reply.Result = &result{Label: q.name}
		reply.Result.Datapoints.Members = points
	}
	writeXML(w, 200, reply)
}

func Reject(w http.ResponseWriter, denied bool) {
	if denied {
		writeError(w, requestError{"AccessDenied", "An authenticated local identity is required.", 403})
		return
	}
	writeError(w, invalid("Invalid CloudWatch destination or region."))
}
