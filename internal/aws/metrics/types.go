// Package metrics implements an independently authored, bounded CloudWatch
// Query leaf. Host authentication and provider dispatch remain the embedder's job.
package metrics

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	AccountID        = "000000000000"
	MaxRequestBytes  = 1 << 20
	MaxResponseBytes = 2 << 20
	retention        = 15 * 24 * time.Hour
	listingAge       = 14 * 24 * time.Hour
	cursorLife       = time.Hour
)

type Error struct {
	Code, Message string
	Status        int
}

func (e *Error) Error() string         { return e.Message }
func invalid(message string) error     { return &Error{"InvalidParameterValue", message, 400} }
func missing(message string) error     { return &Error{"MissingParameter", message, 400} }
func unsupported(message string) error { return &Error{"InvalidParameterCombination", message, 400} }
func internal() error {
	return &Error{"InternalServiceError", "Local metric storage operation failed.", 500}
}
func denied() error {
	return &Error{"AccessDenied", "The selected identity is not authorized for this metric action.", 403}
}
func quota() error {
	return &Error{"SimulatorQuotaExceeded", "The local metric storage budget is exhausted.", 400}
}

// AuthorizeTx must use the supplied transaction or a previously captured,
// immutable policy. It must never open a nested transaction/connection.
// The bool result is permission, not a replacement for host authentication.
type AuthorizeTx func(context.Context, *sql.Tx, string, string) (bool, error)
type Scope struct {
	Region    string
	Authorize AuthorizeTx
}
type Limits struct{ Series, Points, Bytes, Regions, PageSize int }
type Config struct {
	Clock  func() time.Time
	Limits Limits
}

var defaultLimits = Limits{Series: 1000, Points: 100000, Bytes: 64 << 20, Regions: 64, PageSize: 500}

type Dimension struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type DimensionFilter struct {
	Name  string  `json:"name"`
	Value *string `json:"value,omitempty"`
}
type Metric struct {
	Namespace  string      `json:"namespace"`
	Name       string      `json:"name"`
	Dimensions []Dimension `json:"dimensions"`
}
type Datum struct {
	MetricName        string
	Dimensions        []Dimension
	Timestamp         *time.Time
	Value             float64
	Unit              *string
	StorageResolution *int
}
type PutInput struct {
	Namespace string
	Data      []Datum
}
type ListInput struct {
	Namespace, MetricName *string
	Dimensions            []DimensionFilter
	NextToken             string
}
type ListOutput struct {
	Metrics   []Metric
	NextToken string
}
type StatisticsInput struct {
	Namespace, MetricName string
	Dimensions            []Dimension
	Start, End            time.Time
	Period                int
	Statistics            []string
	Unit                  *string
}
type Datapoint struct {
	Timestamp                                   time.Time
	Unit                                        string
	SampleCount, Sum, Minimum, Maximum, Average float64
}
type StatisticsOutput struct {
	Label      string
	Datapoints []Datapoint
	Statistics []string
}
type pointDocument struct {
	Timestamp int64   `json:"timestamp"`
	Unit      string  `json:"unit"`
	Value     float64 `json:"value"`
}
type normalizedPoint struct {
	Metric Metric
	Point  pointDocument
}

var units = map[string]bool{
	"Seconds": true, "Microseconds": true, "Milliseconds": true, "Bytes": true, "Kilobytes": true, "Megabytes": true, "Gigabytes": true, "Terabytes": true,
	"Bits": true, "Kilobits": true, "Megabits": true, "Gigabits": true, "Terabits": true, "Percent": true, "Count": true,
	"Bytes/Second": true, "Kilobytes/Second": true, "Megabytes/Second": true, "Gigabytes/Second": true, "Terabytes/Second": true,
	"Bits/Second": true, "Kilobits/Second": true, "Megabits/Second": true, "Gigabits/Second": true, "Terabits/Second": true, "Count/Second": true, "None": true,
}
var statistics = map[string]bool{"SampleCount": true, "Sum": true, "Minimum": true, "Maximum": true, "Average": true}

func nameOK(value string, maximum int, ascii bool, colon bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) == 0 || utf8.RuneCountInString(value) > maximum || strings.TrimSpace(value) == "" || colon && strings.HasPrefix(value, ":") {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) || c == 0xfffe || c == 0xffff || ascii && c > 127 {
			return false
		}
	}
	return true
}
func namespaceOK(value string) bool {
	return nameOK(value, 255, true, true) && !strings.HasPrefix(value, "AWS/")
}
func regionOK(region string) bool {
	if len(region) < 5 || len(region) > 32 {
		return false
	}
	parts := strings.Split(region, "-")
	if len(parts) < 3 || len(parts) > 5 || len(parts[0]) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
				return false
			}
		}
	}
	last := parts[len(parts)-1]
	for _, c := range last {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func canonicalDimensions(input []Dimension) ([]Dimension, error) {
	if len(input) > 30 {
		return nil, invalid("At most 30 dimensions are supported.")
	}
	out := append([]Dimension{}, input...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	for i, d := range out {
		if !nameOK(d.Name, 255, true, true) || !nameOK(d.Value, 1024, true, false) || i > 0 && out[i-1].Name == d.Name {
			return nil, invalid("Dimensions require unique valid names and values.")
		}
	}
	return out, nil
}
func nano(t time.Time) (int64, error) {
	n := t.UnixNano()
	if n <= 0 || !time.Unix(0, n).Equal(t) {
		return 0, invalid("Timestamp is outside the local supported range.")
	}
	return n, nil
}
func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && math.Abs(value) <= math.Ldexp(1, 360)
}
func normalizePut(input PutInput, now time.Time) ([]normalizedPoint, error) {
	if !namespaceOK(input.Namespace) {
		return nil, invalid("A valid custom namespace is required; AWS/ namespaces are locally unsupported.")
	}
	if len(input.Data) == 0 || len(input.Data) > 1000 {
		return nil, invalid("MetricData requires 1 to 1000 raw-value records.")
	}
	if _, err := nano(now); err != nil {
		return nil, err
	}
	out := make([]normalizedPoint, 0, len(input.Data))
	for _, d := range input.Data {
		if !nameOK(d.MetricName, 255, false, false) || !finite(d.Value) {
			return nil, invalid("Metric name or numeric value is invalid.")
		}
		dims, err := canonicalDimensions(d.Dimensions)
		if err != nil {
			return nil, err
		}
		if d.StorageResolution != nil && *d.StorageResolution != 60 {
			return nil, unsupported("Only standard StorageResolution 60 is supported.")
		}
		unit := "None"
		if d.Unit != nil {
			unit = *d.Unit
		}
		if !units[unit] {
			return nil, invalid("Metric unit is invalid.")
		}
		stamp := now
		if d.Timestamp != nil {
			stamp = *d.Timestamp
		}
		if stamp.Before(now.Add(-14*24*time.Hour)) || stamp.After(now.Add(2*time.Hour)) {
			return nil, invalid("Metric timestamp is outside the ingestion window.")
		}
		n, err := nano(stamp)
		if err != nil {
			return nil, err
		}
		out = append(out, normalizedPoint{Metric{input.Namespace, d.MetricName, dims}, pointDocument{n, unit, d.Value}})
	}
	return out, nil
}
func normalizeList(input ListInput) (ListInput, error) {
	if input.Namespace != nil && !namespaceOK(*input.Namespace) || input.MetricName != nil && !nameOK(*input.MetricName, 255, false, false) || len(input.Dimensions) > 10 {
		return input, invalid("Metric list filters are invalid.")
	}
	input.Dimensions = append([]DimensionFilter{}, input.Dimensions...)
	sort.Slice(input.Dimensions, func(i, j int) bool { return input.Dimensions[i].Name < input.Dimensions[j].Name })
	for i, d := range input.Dimensions {
		if !nameOK(d.Name, 255, true, true) || d.Value != nil && !nameOK(*d.Value, 1024, true, false) || i > 0 && input.Dimensions[i-1].Name == d.Name {
			return input, invalid("Dimension filters are invalid.")
		}
	}
	return input, nil
}
func normalizeStatistics(input StatisticsInput, now time.Time) (StatisticsInput, error) {
	if !namespaceOK(input.Namespace) || !nameOK(input.MetricName, 255, false, false) {
		return input, invalid("A valid custom metric identity is required.")
	}
	dims, err := canonicalDimensions(input.Dimensions)
	if err != nil {
		return input, err
	}
	input.Dimensions = dims
	if input.Period < 60 || input.Period%60 != 0 || input.Period > int((15*24*time.Hour)/time.Second) {
		return input, unsupported("Only bounded standard-resolution periods are supported.")
	}
	if _, err = nano(input.Start); err != nil {
		return input, err
	}
	if _, err = nano(input.End); err != nil {
		return input, err
	}
	if !input.End.After(input.Start) {
		return input, invalid("EndTime must follow StartTime.")
	}
	if input.Start.Before(now.Add(-retention)) {
		return input, unsupported("Queries older than the local 15-day raw window are unsupported.")
	}
	input.Start = input.Start.UTC().Truncate(time.Minute)
	duration := input.End.Sub(input.Start)
	period := time.Duration(input.Period) * time.Second
	if duration > 1440*period {
		return input, invalid("At most 1440 requested time buckets are supported.")
	}
	if len(input.Statistics) < 1 || len(input.Statistics) > 5 {
		return input, missing("One to five basic Statistics are required.")
	}
	seen := map[string]bool{}
	input.Statistics = append([]string{}, input.Statistics...)
	for _, v := range input.Statistics {
		if !statistics[v] || seen[v] {
			return input, invalid("Basic statistics must be valid and unique.")
		}
		seen[v] = true
	}
	if input.Unit != nil && !units[*input.Unit] {
		return input, invalid("Metric unit is invalid.")
	}
	return input, nil
}
func strictDocument(data string, target any) error {
	d := json.NewDecoder(strings.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err == nil {
		return fmt.Errorf("multiple stored values")
	} else if err != io.EOF {
		return err
	}
	return nil
}
