package metrics

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const xmlNamespace = "http://monitoring.amazonaws.com/doc/2010-08-01/"

var ErrUnauthenticated = errors.New("trusted host authentication is missing")

// ScopeExtractor must authenticate the caller and capture immutable authority
// before this handler reads the body. Action-specific authorization happens only
// after the bounded Query form identifies Action, inside the operation's SQL tx.
type ScopeExtractor func(*http.Request) (Scope, error)
type Handler struct {
	store    *Store
	scope    ScopeExtractor
	requests atomic.Uint64
}

func NewQueryHandler(store *Store, extract ScopeExtractor) (*Handler, error) {
	if store == nil || store.db == nil || extract == nil {
		return nil, fmt.Errorf("initialized metric store and trusted scope extractor are required")
	}
	return &Handler{store: store, scope: extract}, nil
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := fmt.Sprintf("local-metrics-%016x", h.requests.Add(1))
	scope, err := h.scope(r)
	if err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			h.failure(w, &Error{"NotAuthorized", "Local host authentication is required.", 401}, requestID)
		} else {
			h.failure(w, internal(), requestID)
		}
		return
	}
	if scope.Authorize == nil {
		h.failure(w, internal(), requestID)
		return
	}
	if r.Method != "POST" || r.URL.Path != "/" {
		h.failure(w, &Error{"UnknownOperationException", "Only the local Query POST endpoint is supported.", 404}, requestID)
		return
	}
	media, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" {
		h.failure(w, invalid("Query form Content-Type is required."), requestID)
		return
	}
	for k, v := range parameters {
		if k != "charset" || !strings.EqualFold(v, "utf-8") {
			h.failure(w, invalid("Unsupported form encoding."), requestID)
			return
		}
	}
	if r.URL.RawQuery != "" || r.Header.Get("X-Amz-Target") != "" {
		h.failure(w, unsupported("Query-string or JSON-target routing is unsupported by this leaf."), requestID)
		return
	}
	body, err := queryBody(r)
	if err != nil {
		h.failure(w, err, requestID)
		return
	}
	if !utf8.Valid(body) {
		h.failure(w, invalid("Query form must be valid UTF8."), requestID)
		return
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		h.failure(w, invalid("Query form encoding is invalid."), requestID)
		return
	}
	for k, v := range values {
		if !utf8.ValidString(k) || len(v) != 1 || !utf8.ValidString(v[0]) {
			h.failure(w, invalid("Repeated or invalid form fields are unsupported."), requestID)
			return
		}
	}
	if len(values["Action"]) != 1 || values.Get("Action") == "" {
		h.failure(w, missing("Action is required."), requestID)
		return
	}
	if values.Get("Version") != "2010-08-01" {
		h.failure(w, invalid("Version 2010-08-01 is required."), requestID)
		return
	}
	action := values.Get("Action")
	if action != "PutMetricData" && action != "ListMetrics" && action != "GetMetricStatistics" {
		h.failure(w, &Error{"UnknownOperationException", "This metric operation is not implemented.", 404}, requestID)
		return
	}
	var response any
	var data []byte
	err = h.store.run(r.Context(), scope, action, func(tx *sql.Tx, now time.Time) error {
		switch action {
		case "PutMetricData":
			in, e := decodePut(values)
			if e != nil {
				return e
			}
			if e = h.store.putTx(r.Context(), tx, scope.Region, in, now); e != nil {
				return e
			}
			response = putXML{Namespace: xmlNamespace, Metadata: metadataXML{requestID}}
		case "ListMetrics":
			in, e := decodeList(values)
			if e != nil {
				return e
			}
			out, e := h.store.listTx(r.Context(), tx, scope.Region, in, now)
			if e != nil {
				return e
			}
			response = listXML{Namespace: xmlNamespace, Result: listResultXML{Metrics: metricMembersXML{metricsXML(out.Metrics)}, NextToken: out.NextToken}, Metadata: metadataXML{requestID}}
		case "GetMetricStatistics":
			in, e := decodeStatistics(values)
			if e != nil {
				return e
			}
			out, e := h.store.statisticsTx(r.Context(), tx, scope.Region, in, now)
			if e != nil {
				return e
			}
			response = statisticsXML{Namespace: xmlNamespace, Result: statisticsResultXML{Label: out.Label, Datapoints: datapointMembersXML{datapointsXML(out)}}, Metadata: metadataXML{requestID}}
		}
		// Prepare the entire wire response before committing. Serialization or
		// output-budget failure must roll back a Put, not acknowledge lost data.
		var encodeErr error
		data, encodeErr = xml.Marshal(response)
		if encodeErr != nil {
			return internal()
		}
		if len(data)+len(xml.Header) > MaxResponseBytes {
			return &Error{"RequestEntityTooLargeException", "Metric response exceeds the local output limit.", 413}
		}
		return nil
	})
	if err != nil {
		h.failure(w, err, requestID)
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write(append([]byte(xml.Header), data...))
}

// queryBody supports the official SDK's gzip request compression without
// trusting Content-Length. Both physical wire bytes and expanded Query bytes
// have the same explicit local limit. This is not a claim about AWS's expanded
// size policy. Exactly one gzip member is accepted; no ignored suffix exists.
func queryBody(r *http.Request) ([]byte, error) {
	encodings := r.Header.Values("Content-Encoding")
	compressed := false
	if len(encodings) > 0 {
		if len(encodings) != 1 || !strings.EqualFold(strings.TrimSpace(encodings[0]), "gzip") {
			return nil, unsupported("Only a single gzip Content-Encoding is supported.")
		}
		compressed = true
	}
	wire, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes+1))
	if err != nil {
		return nil, invalid("The request body could not be read.")
	}
	tooLarge := func() error {
		return &Error{"RequestEntityTooLargeException", "Metric wire or expanded request exceeds the local body limit.", 413}
	}
	if len(wire) > MaxRequestBytes {
		return nil, tooLarge()
	}
	if !compressed {
		return wire, nil
	}
	// bytes.Reader implements ReadByte, so gzip does not prefetch an ignored
	// suffix. Read to EOF (unless oversized) to verify the member's CRC/length.
	source := bytes.NewReader(wire)
	reader, err := gzip.NewReader(source)
	if err != nil {
		return nil, invalid("The gzip request is malformed.")
	}
	reader.Multistream(false)
	body, readErr := io.ReadAll(io.LimitReader(reader, MaxRequestBytes+1))
	closeErr := reader.Close()
	if len(body) > MaxRequestBytes {
		return nil, tooLarge()
	}
	if readErr != nil || closeErr != nil || source.Len() != 0 {
		return nil, invalid("The gzip request is malformed or has trailing data.")
	}
	return body, nil
}

var hostErrorSequence atomic.Uint64

// WriteQueryError lets a trusted protocol/actor adapter refuse a request using
// the same bounded XML envelope, without decoding a body or opening a store Tx.
// Non-API errors remain generic, as in the native handler.
func WriteQueryError(w http.ResponseWriter, err error) {
	(&Handler{}).failure(w, err, fmt.Sprintf("local-metrics-host-%016x", hostErrorSequence.Add(1)))
}

func (h *Handler) failure(w http.ResponseWriter, err error, id string) {
	var api *Error
	if !errors.As(err, &api) {
		api = internal().(*Error)
	}
	kind := "Sender"
	if api.Status >= 500 {
		kind = "Receiver"
	}
	body, marshalErr := xml.Marshal(errorXML{Namespace: xmlNamespace, Failure: errorBodyXML{kind, api.Code, api.Message}, RequestID: id})
	if marshalErr != nil {
		http.Error(w, "Local metric response failed.", 500)
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(api.Status)
	_, _ = w.Write(append([]byte(xml.Header), body...))
}

type metadataXML struct {
	RequestID string `xml:"RequestId"`
}
type putXML struct {
	XMLName   xml.Name    `xml:"PutMetricDataResponse"`
	Namespace string      `xml:"xmlns,attr"`
	Metadata  metadataXML `xml:"ResponseMetadata"`
}
type dimensionXML struct {
	Name  string `xml:"Name"`
	Value string `xml:"Value"`
}
type dimensionMembersXML struct {
	Members []dimensionXML `xml:"member"`
}
type metricXML struct {
	Namespace  string              `xml:"Namespace"`
	MetricName string              `xml:"MetricName"`
	Dimensions dimensionMembersXML `xml:"Dimensions"`
}
type metricMembersXML struct {
	Members []metricXML `xml:"member"`
}
type listResultXML struct {
	Metrics   metricMembersXML `xml:"Metrics"`
	NextToken string           `xml:"NextToken,omitempty"`
}
type listXML struct {
	XMLName   xml.Name      `xml:"ListMetricsResponse"`
	Namespace string        `xml:"xmlns,attr"`
	Result    listResultXML `xml:"ListMetricsResult"`
	Metadata  metadataXML   `xml:"ResponseMetadata"`
}
type datapointXML struct {
	Timestamp   string   `xml:"Timestamp"`
	SampleCount *float64 `xml:"SampleCount,omitempty"`
	Average     *float64 `xml:"Average,omitempty"`
	Sum         *float64 `xml:"Sum,omitempty"`
	Minimum     *float64 `xml:"Minimum,omitempty"`
	Maximum     *float64 `xml:"Maximum,omitempty"`
	Unit        string   `xml:"Unit"`
}
type datapointMembersXML struct {
	Members []datapointXML `xml:"member"`
}
type statisticsResultXML struct {
	Label      string              `xml:"Label"`
	Datapoints datapointMembersXML `xml:"Datapoints"`
}
type statisticsXML struct {
	XMLName   xml.Name            `xml:"GetMetricStatisticsResponse"`
	Namespace string              `xml:"xmlns,attr"`
	Result    statisticsResultXML `xml:"GetMetricStatisticsResult"`
	Metadata  metadataXML         `xml:"ResponseMetadata"`
}
type errorBodyXML struct {
	Type    string `xml:"Type"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}
type errorXML struct {
	XMLName   xml.Name     `xml:"ErrorResponse"`
	Namespace string       `xml:"xmlns,attr"`
	Failure   errorBodyXML `xml:"Error"`
	RequestID string       `xml:"RequestId"`
}

func metricsXML(metrics []Metric) []metricXML {
	out := make([]metricXML, 0, len(metrics))
	for _, m := range metrics {
		dims := make([]dimensionXML, 0, len(m.Dimensions))
		for _, d := range m.Dimensions {
			dims = append(dims, dimensionXML{d.Name, d.Value})
		}
		out = append(out, metricXML{m.Namespace, m.Name, dimensionMembersXML{dims}})
	}
	return out
}
func datapointsXML(output StatisticsOutput) []datapointXML {
	out := make([]datapointXML, 0, len(output.Datapoints))
	for _, point := range output.Datapoints {
		p := point
		wire := datapointXML{Timestamp: p.Timestamp.UTC().Format(time.RFC3339Nano), Unit: p.Unit}
		for _, stat := range output.Statistics {
			switch stat {
			case "SampleCount":
				wire.SampleCount = &p.SampleCount
			case "Sum":
				wire.Sum = &p.Sum
			case "Minimum":
				wire.Minimum = &p.Minimum
			case "Maximum":
				wire.Maximum = &p.Maximum
			case "Average":
				wire.Average = &p.Average
			}
		}
		out = append(out, wire)
	}
	return out
}
