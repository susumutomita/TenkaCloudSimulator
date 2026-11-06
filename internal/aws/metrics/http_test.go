package metrics

import (
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

type scopeKey struct{}

func contextScope(r *http.Request) (Scope, error) {
	scope, ok := r.Context().Value(scopeKey{}).(Scope)
	if !ok {
		return Scope{}, ErrUnauthenticated
	}
	return scope, nil
}
func query(t *testing.T, h http.Handler, scope *Scope, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	if scope != nil {
		r = r.WithContext(context.WithValue(r.Context(), scopeKey{}, *scope))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func wire(t *testing.T, h http.Handler, scope Scope, form url.Values, want int) *httptest.ResponseRecorder {
	t.Helper()
	w := query(t, h, &scope, form)
	if w.Code != want {
		t.Fatalf("native Query expected HTTP%d got%d", want, w.Code)
	}
	return w
}
func base(action string) url.Values { return url.Values{"Action": {action}, "Version": {"2010-08-01"}} }
func rawPut(now time.Time) url.Values {
	v := base("PutMetricData")
	v.Set("Namespace", "Practice/App")
	v.Set("MetricData.member.1.MetricName", "Metric")
	v.Set("MetricData.member.1.Value", "0")
	v.Set("MetricData.member.1.Timestamp", now.Format(time.RFC3339Nano))
	return v
}

type countedBody struct {
	reader io.Reader
	bytes  int
}

func (b *countedBody) Read(p []byte) (int, error) {
	n, e := b.reader.Read(p)
	b.bytes += n
	return n, e
}
func (b *countedBody) Close() error { return nil }

func TestMetricsNativeQueryXMLRawZeroAndOmittedUnitStreams(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	h, err := NewQueryHandler(s, contextScope)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{"us-east-1", allow}
	putForm := rawPut(*now)
	w := wire(t, h, scope, putForm, 200)
	if !strings.Contains(w.Body.String(), `<PutMetricDataResponse xmlns="http://monitoring.amazonaws.com/doc/2010-08-01/">`) || !strings.Contains(w.Body.String(), "<ResponseMetadata><RequestId>") {
		t.Fatal("wrong independent Query success envelope")
	}
	putForm.Set("MetricData.member.1.Unit", "Count")
	putForm.Set("MetricData.member.1.Value", "7")
	wire(t, h, scope, putForm, 200)
	before := frame(t, db)
	list := wire(t, h, scope, base("ListMetrics"), 200)
	var listed struct {
		Metrics []struct{ Namespace, MetricName string } `xml:"ListMetricsResult>Metrics>member"`
	}
	if err = xml.Unmarshal(list.Body.Bytes(), &listed); err != nil || len(listed.Metrics) != 1 || listed.Metrics[0].Namespace != "Practice/App" || listed.Metrics[0].MetricName != "Metric" {
		t.Fatal("unit changed metric identity or listing envelope")
	}
	stats := base("GetMetricStatistics")
	stats.Set("Namespace", "Practice/App")
	stats.Set("MetricName", "Metric")
	stats.Set("StartTime", now.Truncate(time.Minute).Format(time.RFC3339Nano))
	stats.Set("EndTime", now.Add(time.Minute).Format(time.RFC3339Nano))
	stats.Set("Period", "60")
	stats.Set("Statistics.member.1", "Sum")
	result := wire(t, h, scope, stats, 200)
	var parsed struct {
		Label  string `xml:"GetMetricStatisticsResult>Label"`
		Points []struct {
			Timestamp, Unit string
			Sum             float64
		} `xml:"GetMetricStatisticsResult>Datapoints>member"`
	}
	if err = xml.Unmarshal(result.Body.Bytes(), &parsed); err != nil || parsed.Label != "Metric" || len(parsed.Points) != 2 {
		t.Fatal("native Query statistics structure changed")
	}
	values := map[string]float64{}
	for _, p := range parsed.Points {
		values[p.Unit] = p.Sum
	}
	_, hasNone := values["None"]
	if values["Count"] != 7 || !hasNone || values["None"] != 0 {
		t.Fatal("omittedUnit lost or coalesced a recorded unit stream")
	}
	if !strings.Contains(result.Body.String(), "<Sum>0</Sum>") {
		t.Fatal("zero sum was omitted")
	}
	if strings.Contains(result.Body.String(), "<Average>") || strings.Contains(result.Body.String(), "<SampleCount>") {
		t.Fatal("unrequested statistics were invented")
	}
	unchanged(t, db, before)
	stats.Set("Unit", "Count")
	result = wire(t, h, scope, stats, 200)
	if strings.Contains(result.Body.String(), "<Unit>None</Unit>") {
		t.Fatal("explicit unit filter converted another stream")
	}
	unchanged(t, db, before)
	// Explicit filters must return their literal stream, not an empty success.
	assertUnitPoint := func(unit string, sum float64) {
		t.Helper()
		parsed.Label = ""
		parsed.Points = nil
		if err = xml.Unmarshal(result.Body.Bytes(), &parsed); err != nil || parsed.Label != "Metric" || len(parsed.Points) != 1 {
			t.Fatal("explicit unit filter lost its single recorded datapoint")
		}
		point := parsed.Points[0]
		if point.Unit != unit || point.Sum != sum || point.Timestamp != now.Truncate(time.Minute).Format(time.RFC3339Nano) {
			t.Fatal("explicit unit filter changed the literal unit, sum, or bucket")
		}
	}
	assertUnitPoint("Count", 7)
	stats.Set("Unit", "None")
	result = wire(t, h, scope, stats, 200)
	assertUnitPoint("None", 0)
	if !strings.Contains(result.Body.String(), "<Sum>0</Sum>") {
		t.Fatal("explicit None filter omitted its real zero sum")
	}
	unchanged(t, db, before)
}

func TestMetricsHostScopeBeforeBodyAndActionAuthorizationBeforeDatumDecode(t *testing.T) {
	s, db, _, _ := fixture(t, Limits{})
	h, err := NewQueryHandler(s, contextScope)
	if err != nil {
		t.Fatal(err)
	}
	before := frame(t, db)
	body := &countedBody{reader: strings.NewReader("Action=PutMetricData&Version=2010-08-01&MetricData.member.1.Value=not-a-number")}
	r := httptest.NewRequest("POST", "http://127.0.0.1/", nil)
	r.Body = body
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 || body.bytes != 0 {
		t.Fatal("unknown host caller caused body consumption")
	}
	unchanged(t, db, before)
	calls := 0
	scope := Scope{"us-west-2", func(ctx context.Context, tx *sql.Tx, action, resource string) (bool, error) {
		calls++
		if action != "cloudwatch:PutMetricData" || resource != "*" || tx == nil {
			t.Fatal("wrong action/transaction literal")
		}
		return false, nil
	}}
	body = &countedBody{reader: strings.NewReader("Action=PutMetricData&Version=2010-08-01&MetricData.member.1.Value=not-a-number")}
	r = httptest.NewRequest("POST", "http://127.0.0.1/", nil)
	r.Body = body
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(context.WithValue(r.Context(), scopeKey{}, scope))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || calls != 1 || body.bytes == 0 || !strings.Contains(w.Body.String(), "<Code>AccessDenied</Code>") {
		t.Fatal("action authorization did not precede semantic MetricData decoding")
	}
	unchanged(t, db, before)
	if _, err = NewQueryHandler(s, nil); err == nil {
		t.Fatal("missingtrustedextractoraccepted")
	}
	if _, err = NewQueryHandler(nil, contextScope); err == nil {
		t.Fatal("missingstoreaccepted")
	}
}

func TestMetricsQueryRefusalsPreserveAllStateAndSanitizeInfrastructure(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	h, err := NewQueryHandler(s, contextScope)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{"us-east-1", allow}
	wire(t, h, scope, rawPut(*now), 200)
	before := frame(t, db)
	cases := []url.Values{}
	for _, change := range []struct{ key, value string }{{"MetricData.member.0.MetricName", "Bad"}, {"MetricData.member.3.MetricName", "Bad"}, {"MetricData.member.1.Unknown", "x"}, {"MetricData.member.1.StorageResolution", "1"}, {"MetricData.member.1.Value", "NaN"}, {"MetricData.member.1.Unit", ""}, {"MetricData.member.1.Timestamp", "wrong"}} {
		v := rawPut(*now)
		v.Set(change.key, change.value)
		cases = append(cases, v)
	}
	v := rawPut(*now)
	v.Add("Namespace", "Different")
	cases = append(cases, v)
	v = base("ListMetrics")
	v.Set("MaxResults", "1")
	cases = append(cases, v)
	v = base("ListMetrics")
	v.Set("RecentlyActive", "PT3H")
	cases = append(cases, v)
	v = rawPut(*now)
	v.Del("MetricData.member.1.Value")
	v.Set("MetricData.member.1.Values.member.1", "2")
	cases = append(cases, v)
	for i, v := range cases {
		w := wire(t, h, scope, v, 400)
		if strings.Contains(w.Body.String(), "sqlite") {
			t.Fatalf("storage detail leaked in refusal %d", i)
		}
		unchanged(t, db, before)
	}
	wire(t, h, scope, base("DeleteMetric"), 404)
	unchanged(t, db, before)
	for _, change := range []string{"raw-query", "gzip", "json-target", "oversize", "method"} {
		r := httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader(rawPut(*now).Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r = r.WithContext(context.WithValue(r.Context(), scopeKey{}, scope))
		want := 400
		switch change {
		case "raw-query":
			r.URL.RawQuery = "Action=ListMetrics"
		case "gzip":
			r.Header.Set("Content-Encoding", "gzip")
		case "json-target":
			r.Header.Set("X-Amz-Target", "Unsupported.JSON")
		case "oversize":
			r.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", MaxRequestBytes+1)))
			want = 413
		case "method":
			r.Method = "GET"
			want = 404
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("refusal %s returned%d", change, w.Code)
		}
		unchanged(t, db, before)
	}
	badScope := Scope{"us-east-1", func(context.Context, *sql.Tx, string, string) (bool, error) {
		return false, errors.New("private policy database payload")
	}}
	w := wire(t, h, badScope, base("ListMetrics"), 500)
	if strings.Contains(w.Body.String(), "private") {
		t.Fatal("infrastructure payload leaked")
	}
	unchanged(t, db, before)
}

func TestMetricsQueryLiteralUnicodeAndXMLCharactersRoundTrip(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	h, err := NewQueryHandler(s, contextScope)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{"us-east-1", allow}
	name := "雪/%2F/<metric>&"
	form := rawPut(*now)
	form.Set("MetricData.member.1.MetricName", name)
	form.Set("MetricData.member.1.Dimensions.member.1.Name", "tag")
	form.Set("MetricData.member.1.Dimensions.member.1.Value", "<quoted>&literal%2F")
	wire(t, h, scope, form, 200)
	before := frame(t, db)
	result := wire(t, h, scope, base("ListMetrics"), 200)
	var parsed struct {
		Metrics []struct {
			MetricName string
			Dimensions struct {
				Members []struct{ Name, Value string } `xml:"member"`
			}
		} `xml:"ListMetricsResult>Metrics>member"`
	}
	if err = xml.Unmarshal(result.Body.Bytes(), &parsed); err != nil || len(parsed.Metrics) != 1 || parsed.Metrics[0].MetricName != name || len(parsed.Metrics[0].Dimensions.Members) != 1 || parsed.Metrics[0].Dimensions.Members[0].Value != "<quoted>&literal%2F" {
		t.Fatal("literal name/dimension was normalized or XML-interpreted")
	}
	unchanged(t, db, before)
}

// The 1440 limit counts returned (minute,unit) groups, not just time buckets.
// All records enter through native Query HTTP; SQL is used only by frame().
func TestMetricsNativeReturnedUnitGroups1440Accept1441RefuseAndUnitRepair(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	h, err := NewQueryHandler(s, contextScope)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{"us-east-1", allow}
	peer := Scope{"us-west-2", allow}
	peerPut := rawPut(*now)
	peerPut.Set("MetricData.member.1.Value", "101")
	wire(t, h, peer, peerPut, 200)
	start := now.Truncate(time.Minute).Add(-719 * time.Minute)
	// Two legal 720-record batches produce one series and 1440 unit groups.
	for _, stream := range []struct{ unit, value string }{{"Count", "7"}, {"Bytes", "11"}} {
		put := base("PutMetricData")
		put.Set("Namespace", "Practice/App")
		for i := 0; i < 720; i++ {
			prefix := "MetricData.member." + strconv.Itoa(i+1) + "."
			put.Set(prefix+"MetricName", "Metric")
			put.Set(prefix+"Timestamp", start.Add(time.Duration(i)*time.Minute).Format(time.RFC3339Nano))
			put.Set(prefix+"Unit", stream.unit)
			put.Set(prefix+"Value", stream.value)
		}
		wire(t, h, scope, put, 200)
	}
	stats := base("GetMetricStatistics")
	stats.Set("Namespace", "Practice/App")
	stats.Set("MetricName", "Metric")
	stats.Set("StartTime", start.Format(time.RFC3339Nano))
	stats.Set("EndTime", start.Add(720*time.Minute).Format(time.RFC3339Nano))
	stats.Set("Period", "60")
	for i, name := range []string{"SampleCount", "Sum", "Minimum", "Maximum", "Average"} {
		stats.Set("Statistics.member."+strconv.Itoa(i+1), name)
	}
	beforeReads := frame(t, db)
	result := wire(t, h, scope, stats, 200)
	var parsed struct {
		XMLName xml.Name
		Label   string `xml:"GetMetricStatisticsResult>Label"`
		Points  []struct {
			Timestamp, Unit                             string
			SampleCount, Sum, Minimum, Maximum, Average float64
		} `xml:"GetMetricStatisticsResult>Datapoints>member"`
	}
	if err = xml.Unmarshal(result.Body.Bytes(), &parsed); err != nil || parsed.XMLName.Local != "GetMetricStatisticsResponse" || parsed.Label != "Metric" || len(parsed.Points) != 1440 {
		t.Fatal("native 1440 returned-unit-group boundary was not accepted")
	}
	// This literal oracle also requires all five statistics and unique sorted
	// timestamps/units; a silent truncation, coalescence or empty success fails.
	for i, point := range parsed.Points {
		unit, value := "Bytes", float64(11)
		if i%2 == 1 {
			unit, value = "Count", 7
		}
		stamp := start.Add(time.Duration(i/2) * time.Minute).Format(time.RFC3339Nano)
		if point.Timestamp != stamp || point.Unit != unit || point.SampleCount != 1 || point.Sum != value || point.Minimum != value || point.Maximum != value || point.Average != value {
			t.Fatal("1440 boundary response lost a literal timestamp/unit/statistic")
		}
	}
	unchanged(t, db, beforeReads)
	// The 1441st group is a distinct valid unit at an existing timestamp,
	// so requested time buckets remain 720 and cannot explain the refusal.
	extra := rawPut(start)
	extra.Set("MetricData.member.1.Unit", "None")
	extra.Set("MetricData.member.1.Value", "42")
	wire(t, h, scope, extra, 200)
	afterPut := frame(t, db)
	refused := wire(t, h, scope, stats, 400)
	var failure struct {
		XMLName xml.Name
		Code    string     `xml:"Error>Code"`
		Points  []struct{} `xml:"GetMetricStatisticsResult>Datapoints>member"`
	}
	if err = xml.Unmarshal(refused.Body.Bytes(), &failure); err != nil || failure.XMLName.Local != "ErrorResponse" || failure.Code != "InvalidParameterValue" || len(failure.Points) != 0 || strings.Contains(refused.Body.String(), "<GetMetricStatisticsResult>") {
		t.Fatal("1441 groups did not refuse without a partial statistics response")
	}
	unchanged(t, db, afterPut)
	stats.Set("Unit", "Count")
	repaired := wire(t, h, scope, stats, 200)
	parsed.Points = nil
	if err = xml.Unmarshal(repaired.Body.Bytes(), &parsed); err != nil || parsed.Label != "Metric" || len(parsed.Points) != 720 {
		t.Fatal("explicit unit did not repair the over-grouped query to 720 points")
	}
	for i, point := range parsed.Points {
		stamp := start.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
		if point.Timestamp != stamp || point.Unit != "Count" || point.SampleCount != 1 || point.Sum != 7 || point.Minimum != 7 || point.Maximum != 7 || point.Average != 7 {
			t.Fatal("unit repair leaked another stream or changed literal statistics")
		}
	}
	unchanged(t, db, afterPut)
}
