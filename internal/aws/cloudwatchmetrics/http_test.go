package cloudwatchmetrics

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func wireForm(action string) url.Values {
	return url.Values{"Action": {action}, "Version": {"2010-08-01"}, "Namespace": {"Testing"}}
}
func metricWire() url.Values {
	f := wireForm("PutMetricData")
	f.Set("MetricData.member.1.MetricName", "Metric")
	f.Set("MetricData.member.1.Value", "1")
	return f
}
func statsWire(start, end time.Time) url.Values {
	f := wireForm("GetMetricStatistics")
	for k, v := range map[string]string{"MetricName": "Metric", "Period": "60", "Statistics.member.1": "Sum", "StartTime": start.Format(time.RFC3339Nano), "EndTime": end.Format(time.RFC3339Nano)} {
		f.Set(k, v)
	}
	return f
}
func request(body []byte, encoding string) *http.Request {
	r := httptest.NewRequest("POST", "http://127.0.0.1/", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	if encoding != "" {
		r.Header.Set("Content-Encoding", encoding)
	}
	return r
}
func gzipBody(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func serve(s *Store, r *http.Request, allow bool) (int, string) {
	rec := httptest.NewRecorder()
	s.ServeQuery(rec, r, "us-east-1", func(string) (bool, error) { return allow, nil })
	return rec.Code, rec.Body.String()
}
func TestQueryAdmissionAndGzip(t *testing.T) {
	s, _ := testStore(t, filepath.Join(t.TempDir(), "m.db"))
	now := time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	count := func() int {
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM aws_cloudwatch_samples").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	raw := []byte(metricWire().Encode())
	compressed := gzipBody(t, raw)
	for _, r := range []*http.Request{request(raw, ""), request(compressed, "gzip")} {
		rec := httptest.NewRecorder()
		s.ServeQuery(rec, r, "us-east-1", func(action string) (bool, error) {
			if action != "cloudwatch:PutMetricData" {
				t.Fatal(action)
			}
			return true, nil
		})
		var reply struct {
			XMLName  xml.Name
			Metadata struct {
				RequestID string `xml:"RequestId"`
			} `xml:"ResponseMetadata"`
		}
		if rec.Code != 200 || xml.Unmarshal(rec.Body.Bytes(), &reply) != nil || reply.XMLName != (xml.Name{Space: xmlNamespace, Local: "PutMetricDataResponse"}) || reply.Metadata.RequestID == "" {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	if status, _ := serve(s, request(raw, ""), false); status != 403 {
		t.Fatal(status)
	}
	for _, r := range []*http.Request{
		request(append(slices.Clone(compressed), compressed...), "gzip"),
		request(compressed[:len(compressed)-1], "gzip"), request([]byte("garbage"), "gzip"),
		request(gzipBody(t, bytes.Repeat([]byte{'x'}, bodyLimit+1)), "gzip"),
		request(bytes.Repeat([]byte{'x'}, bodyLimit+1), ""), request(raw, "br"),
		request(append(slices.Clone(raw), []byte("&Version=2010-08-01")...), ""),
		request([]byte(strings.Repeat("k&", 10000)), ""), request([]byte("x=%ZZ"), ""),
	} {
		if status, _ := serve(s, r, true); status != 400 {
			t.Fatal(status)
		}
	}
	for name, mutate := range map[string]func(*http.Request){
		"path": func(r *http.Request) { r.URL.Path = "/other" }, "query": func(r *http.Request) { r.URL.RawQuery = "x=1" },
		"method": func(r *http.Request) { r.Method = "GET" }, "target": func(r *http.Request) { r.Header.Set("X-Amz-Target", "unsupported") },
		"type": func(r *http.Request) { r.Header.Set("Content-Type", "application/json") },
		"charset": func(r *http.Request) {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-16")
		},
		"ambiguous encoding": func(r *http.Request) {
			r.Header.Add("Content-Encoding", "gzip")
			r.Header.Add("Content-Encoding", "gzip")
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := request(raw, "")
			mutate(r)
			if status, _ := serve(s, r, true); status != 400 {
				t.Fatal(status)
			}
		})
	}
	for _, op := range []string{"PutMetricAlarm", "DeleteMetric", "ListMetrics", "GetMetricData"} {
		f := metricWire()
		f.Set("Action", op)
		status, body := serve(s, request([]byte(f.Encode()), ""), true)
		if status != 400 || !strings.Contains(body, "InvalidAction") {
			t.Fatal(op, status, body)
		}
	}
	f := statsWire(now.Add(-time.Minute), now.Add(time.Minute))
	status, body := serve(s, request([]byte(f.Encode()), ""), true)
	if status != 200 || !strings.Contains(body, "<Sum>2</Sum>") || !strings.Contains(body, "<Unit>None</Unit>") || count() != 2 {
		t.Fatal(status, body, count())
	}
	if status, _ = serve(s, request(gzipBody(t, []byte(f.Encode())), "gzip"), true); status != 400 {
		t.Fatal(status)
	}
	rec := httptest.NewRecorder()
	s.ServeQuery(rec, request(raw, ""), "us-east-1", func(string) (bool, error) { return false, errors.New("unpublished local details") })
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "unpublished") {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestStrictMetricFormsAndTimestampLimits(t *testing.T) {
	now := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	for _, value := range []string{"0", "+1.5e3", strconv.FormatFloat(0x1p360, 'g', -1, 64), strconv.FormatFloat(-0x1p360, 'g', -1, 64)} {
		f := metricWire()
		f.Set("MetricData.member.1.Value", value)
		parsed := form(f)
		parsed.take("Action")
		parsed.take("Version")
		parsed.take("Namespace")
		points, err := parsed.samples(now)
		if err != nil || len(points) != 1 || points[0].at != now {
			t.Fatal(value, points, err)
		}
	}
	for _, offset := range []time.Duration{-14 * 24 * time.Hour, 2 * time.Hour, -14*24*time.Hour - time.Nanosecond, 2*time.Hour + time.Nanosecond} {
		f := form(url.Values{"MetricData.member.1.MetricName": {"M"}, "MetricData.member.1.Value": {"1"}, "MetricData.member.1.Timestamp": {now.Add(offset).Format(time.RFC3339Nano)}})
		_, err := f.samples(now)
		if (err == nil) != (offset >= -14*24*time.Hour && offset <= 2*time.Hour) {
			t.Fatal(offset, err)
		}
	}
	for _, tc := range []struct{ key, value string }{
		{"Value", "NaN"}, {"Value", "Inf"}, {"Value", "2.5e108"}, {"Value", "0x1p2"}, {"Value", "1_0"},
		{"StorageResolution", "1"}, {"Unit", "Opaque"}, {"Timestamp", "yesterday"},
		{"StatisticValues.Sum", "4"}, {"Values.member.1", "4"}, {"Counts.member.1", "1"},
		{"MetricName", "\x00"}, {"Dimensions.member.1.Name", ":private"}, {"Dimensions.member.2.Value", "orphan"},
	} {
		f := form(url.Values{"MetricData.member.1.MetricName": {"M"}, "MetricData.member.1.Value": {"1"}})
		f["MetricData.member.1."+tc.key] = []string{tc.value}
		if _, err := f.samples(now); err == nil {
			t.Fatal(tc)
		}
	}
	for _, key := range []string{"EntityMetricData.member.1.Entity.KeyAttributes", "StrictEntityValidation", "MetricData.member.3.Value"} {
		f := form(url.Values{"MetricData.member.1.MetricName": {"M"}, "MetricData.member.1.Value": {"1"}, key: {"1"}})
		if _, err := f.samples(now); err == nil {
			t.Fatal(key)
		}
	}
	for _, ns := range []string{"", ":Invalid", "AWS/EC2", "\n", "☁"} {
		f := form(url.Values{"Namespace": {ns}})
		if _, err := f.namespace(); err == nil {
			t.Fatal(ns)
		}
	}
	q := statsWire(now.Add(29*time.Second), now.Add(90*time.Second))
	q.Del("Namespace")
	q.Del("Action")
	q.Del("Version")
	parsed, err := form(q).query()
	if err != nil || parsed.start != now {
		t.Fatal(parsed, err)
	}
	for _, tc := range []struct{ key, value string }{{"EndTime", now.Format(time.RFC3339)}, {"Period", "1"}, {"Period", "-60"}, {"MetricName", " "}, {"Statistics.member.2", "Sum"}, {"ExtendedStatistics.member.1", "p99"}, {"EndTime", now.Add(1440*time.Minute + time.Nanosecond).Format(time.RFC3339Nano)}} {
		q = statsWire(now, now.Add(time.Minute))
		q.Del("Namespace")
		q.Del("Action")
		q.Del("Version")
		q.Set(tc.key, tc.value)
		if _, err := form(q).query(); err == nil {
			t.Fatal(tc)
		}
	}
}

func TestDimensionIdentityAndInclusiveBodyLimit(t *testing.T) {
	f := form(url.Values{"dims.member.1.Name": {"b"}, "dims.member.1.Value": {"2"}, "dims.member.2.Name": {"a"}, "dims.member.2.Value": {"b=1;2"}})
	key, err := f.dimensions("dims")
	if err != nil || key != `{"a":"b=1;2","b":"2"}` {
		t.Fatal(key, err)
	}
	for _, n := range []int{30, 31} {
		v := form{}
		for i := 1; i <= n; i++ {
			p := "dims.member." + strconv.Itoa(i)
			v[p+".Name"] = []string{strconv.Itoa(i)}
			v[p+".Value"] = []string{"V"}
		}
		_, err = v.dimensions("dims")
		if (err == nil) != (n == 30) {
			t.Fatal(n, err)
		}
	}
	equal := []byte("x=" + strings.Repeat("a", bodyLimit-2))
	for _, r := range []*http.Request{request(equal, ""), request(gzipBody(t, equal), "gzip")} {
		if _, err := readForm(r); err != nil {
			t.Fatal(err)
		}
	}
	r := request([]byte("x=y"), "")
	r = r.WithContext(context.Background())
	r.Header.Add("Content-Type", "application/json")
	if _, err := readForm(r); err == nil {
		t.Fatal("duplicate content type")
	}
}

func TestMetricBatchValidationBoundariesAndUnusedFields(t *testing.T) {
	for _, size := range []int{1000, 1001} {
		f := form{}
		for i := 1; i <= size; i++ {
			prefix := "MetricData.member." + strconv.Itoa(i) + "."
			f[prefix+"MetricName"] = []string{"m"}
			f[prefix+"Value"] = []string{"0"}
		}
		got, err := f.samples(time.Now())
		if (err == nil) != (size == 1000) || err == nil && len(got) != size {
			t.Fatal(size, len(got), err)
		}
	}
	for _, f := range []form{
		{"x.member.1.Name": {"foo"}, "x.member.1.Value": {"a"}, "x.member.2.Name": {"foo"}, "x.member.2.Value": {"b"}},
		{"x.member.1.Name": {"\x01"}, "x.member.1.Value": {"v"}},
		{"x.member.1.Name": {"f"}, "x.member.1.Value": {"   "}},
		{"x.member.1.Name": {strings.Repeat("x", 256)}, "x.member.1.Value": {"v"}},
	} {
		if _, err := f.dimensions("x"); err == nil {
			t.Fatal(f)
		}
	}
	stamp := time.Now().UTC().Truncate(time.Minute)
	for _, field := range []string{"MetricName", "Period", "Statistics.member.1", "StartTime", "EndTime"} {
		f := statsWire(stamp, stamp.Add(time.Minute))
		f.Del("Namespace")
		f.Del("Version")
		f.Del("Action")
		f.Del(field)
		if _, err := form(f).query(); err == nil {
			t.Fatal(field)
		}
	}
	for k, v := range map[string]string{"Unit": "wrong", "StartTime": "bad", "EndTime": "bad", "Statistics.member.1": "Percentile"} {
		f := statsWire(stamp, stamp.Add(time.Minute))
		f.Del("Namespace")
		f.Del("Version")
		f.Del("Action")
		f.Set(k, v)
		if _, err := form(f).query(); err == nil {
			t.Fatal(k)
		}
	}
}

func TestStockEmptyDimensionListAndMixedMarkers(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	for _, action := range []string{"PutMetricData", "GetMetricStatistics"} {
		for _, tc := range []struct {
			value                   string
			present, member, accept bool
		}{
			{"", false, false, true}, {"", true, false, true}, {"unexpected", true, false, false}, {"", true, true, false},
		} {
			isPut := action == "PutMetricData"
			fields := statsWire(now, now.Add(time.Minute))
			prefix := "Dimensions"
			if isPut {
				fields = metricWire()
				prefix = "MetricData.member.1.Dimensions"
			}
			fields.Del("Action")
			fields.Del("Version")
			fields.Del("Namespace")
			if tc.present {
				fields.Set(prefix, tc.value)
			}
			if tc.member {
				fields.Set(prefix+".member.1.Name", "Host")
				fields.Set(prefix+".member.1.Value", "example")
			}
			var dimensions string
			var err error
			if isPut {
				var batch []sample
				batch, err = form(fields).samples(now)
				if err == nil {
					dimensions = batch[0].dimensions
				}
			} else {
				var q query
				q, err = form(fields).query()
				dimensions = q.dimensions
			}
			if (err == nil) != tc.accept || tc.accept && dimensions != "{}" {
				t.Fatalf("%s %+v: %q, %v", action, tc, dimensions, err)
			}
		}
	}
}
