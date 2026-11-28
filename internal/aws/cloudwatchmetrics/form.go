// Package cloudwatchmetrics implements two standard-resolution CloudWatch Query operations.
package cloudwatchmetrics

import (
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxBatch      = 1000
	maxDimensions = 30
	maxPoints     = 1440
	retention     = 15 * 24 * time.Hour
)

var units = strings.Fields("Seconds Microseconds Milliseconds Bytes Kilobytes Megabytes Gigabytes Terabytes Bits Kilobits Megabits Gigabits Terabits Percent Count Bytes/Second Kilobytes/Second Megabytes/Second Gigabytes/Second Terabytes/Second Bits/Second Kilobits/Second Megabits/Second Gigabits/Second Terabits/Second Count/Second None")
var decimal = regexp.MustCompile(`^[+-]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][+-]?[0-9]+)?$`)
var statistics = strings.Fields("SampleCount Average Sum Minimum Maximum")

// The HTTP decoder rejects duplicates before a field is consumed. Leftover keys
// after parsing identify unsupported properties, missing members, and skipped indices.
type form url.Values

func (f form) take(key string) (string, bool) {
	values, ok := f[key]
	if !ok {
		return "", false
	}
	delete(f, key)
	return values[0], true
}

func printable(s string, max int) bool {
	if len(s) > max || strings.TrimSpace(s) == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 32 || s[i] > 126 {
			return false
		}
	}
	return true
}

func metricName(s string) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= 255 && strings.TrimSpace(s) != "" &&
		!strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == 0xfffe || r == 0xffff })
}

func (f form) namespace() (string, error) {
	n, ok := f.take("Namespace")
	if !ok {
		return "", missing("Namespace")
	}
	if !printable(n, 255) || n[0] == ':' || strings.HasPrefix(n, "AWS/") {
		return "", invalid("Use a printable custom namespace outside AWS/.")
	}
	return n, nil
}

func (f form) dimensions(prefix string) (string, error) {
	// The AWS Query serializer emits an explicit empty list as the bare key.
	// Any accompanying numbered members remain unconsumed and fail admission.
	if literal, present := f.take(prefix); present {
		if literal != "" {
			return "", invalid("Dimension lists use numbered members or an empty value.")
		}
		return "{}", nil
	}
	dims := make(map[string]string)
	for i := 1; ; i++ {
		p := prefix + ".member." + strconv.Itoa(i) + "."
		name, found := f.take(p + "Name")
		if !found {
			break
		}
		value, present := f.take(p + "Value")
		if i > maxDimensions || !present || !printable(name, 255) || name[0] == ':' || !printable(value, 1024) {
			return "", invalid("Invalid metric dimension.")
		}
		if _, duplicate := dims[name]; duplicate {
			return "", invalid("Dimension names must be unique.")
		}
		dims[name] = value
	}
	// encoding/json sorts string map keys and preserves delimiter-bearing values.
	data, err := json.Marshal(dims)
	return string(data), err
}

type sample struct {
	name, dimensions, unit string
	at                     time.Time
	value                  float64
}

func (f form) samples(now time.Time) ([]sample, error) {
	var result []sample
	for i := 1; ; i++ {
		p := "MetricData.member." + strconv.Itoa(i) + "."
		name, found := f.take(p + "MetricName")
		if !found {
			break
		}
		if i > maxBatch || !metricName(name) {
			return nil, invalid("Invalid metric name or batch size.")
		}
		dims, err := f.dimensions(p + "Dimensions")
		if err != nil {
			return nil, err
		}
		raw, found := f.take(p + "Value")
		if !found {
			return nil, missing("Value")
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || !decimal.MatchString(raw) || math.IsNaN(value) || math.Abs(value) > 0x1p360 {
			return nil, invalid("Value must be a finite number between -2^360 and 2^360.")
		}
		unit, set := f.take(p + "Unit")
		if !set {
			unit = "None"
		}
		if !slices.Contains(units, unit) {
			return nil, invalid("Unknown metric unit.")
		}
		if resolution, set := f.take(p + "StorageResolution"); set && resolution != "60" {
			return nil, invalid("Only 60-second storage resolution is supported.")
		}
		at := now
		if stamp, set := f.take(p + "Timestamp"); set {
			at, err = time.Parse(time.RFC3339Nano, stamp)
			if err != nil {
				return nil, invalid("Invalid metric timestamp.")
			}
		}
		if at.Before(now.Add(-14*24*time.Hour)) || at.After(now.Add(2*time.Hour)) {
			return nil, invalid("Timestamp must be within the last two weeks or next two hours.")
		}
		result = append(result, sample{name, dims, unit, at, value})
	}
	if len(result) == 0 {
		return nil, missing("MetricData")
	}
	if len(f) != 0 {
		return nil, invalid("Unsupported, incomplete or noncontiguous metric fields.")
	}
	return result, nil
}

type query struct {
	name, dimensions, unit string
	start, end             time.Time
	period                 int64
	stats                  map[string]bool
}

func (f form) query() (query, error) {
	var q query
	var ok bool
	q.name, ok = f.take("MetricName")
	if !ok {
		return q, missing("MetricName")
	}
	if !metricName(q.name) {
		return q, invalid("Invalid metric name.")
	}
	var err error
	q.dimensions, err = f.dimensions("Dimensions")
	if err != nil {
		return q, err
	}
	if q.unit, ok = f.take("Unit"); ok && !slices.Contains(units, q.unit) {
		return q, invalid("Unknown metric unit.")
	}
	q.stats = map[string]bool{}
	for i := 1; ; i++ {
		stat, found := f.take("Statistics.member." + strconv.Itoa(i))
		if !found {
			break
		}
		if !slices.Contains(statistics, stat) || q.stats[stat] {
			return q, invalid("Specify distinct basic statistics.")
		}
		q.stats[stat] = true
	}
	if len(q.stats) == 0 {
		return q, missing("Statistics")
	}
	for _, field := range []struct {
		name string
		dest *time.Time
	}{{"StartTime", &q.start}, {"EndTime", &q.end}} {
		raw, present := f.take(field.name)
		if !present {
			return q, missing(field.name)
		}
		*field.dest, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return q, invalid("Invalid query timestamp.")
		}
	}
	raw, ok := f.take("Period")
	if !ok {
		return q, missing("Period")
	}
	q.period, err = strconv.ParseInt(raw, 10, 32)
	if err != nil || q.period < 60 || q.period%60 != 0 || !q.start.Before(q.end) {
		return q, invalid("Use a positive multiple of 60 seconds and an increasing interval.")
	}
	q.start = q.start.UTC().Truncate(time.Minute)
	seconds := q.end.Unix() - q.start.Unix()
	if q.end.Nanosecond() != 0 {
		seconds++
	}
	if (seconds+q.period-1)/q.period > maxPoints {
		return q, invalid("The request spans more than 1440 time buckets.")
	}
	if len(f) != 0 {
		return q, invalid("Unsupported or incomplete statistics fields.")
	}
	return q, nil
}
