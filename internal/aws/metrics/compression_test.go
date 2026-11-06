package metrics

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func compressedQuery(t *testing.T, body []byte, level int, extra int) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, level)
	if err != nil {
		t.Fatal(err)
	}
	writer.Extra = make([]byte, extra)
	if _, err = writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
func compressionRequest(h http.Handler, scope *Scope, body io.ReadCloser, encodings []string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://127.0.0.1/", nil)
	r.Body = body
	r.ContentLength = 1 // Deliberately not trusted for either limit.
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	if encodings != nil {
		r.Header["Content-Encoding"] = encodings
	}
	if scope != nil {
		r = r.WithContext(context.WithValue(r.Context(), scopeKey{}, *scope))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func compressionStatus(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("gzip Query status: want%d got%d", status, w.Code)
	}
	if status != 200 {
		var got struct {
			Code string `xml:"Error>Code"`
		}
		if xml.Unmarshal(w.Body.Bytes(), &got) != nil || got.Code != code {
			t.Fatal("wrong literal gzip Query error code")
		}
	}
}

func TestMetricsGzipQueryPutListAndStatisticsUseActualStore(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	h, err := NewQueryHandler(s, contextScope)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{"us-east-1", allow}
	put(t, s, "us-west-2", "Peer", "untouched", 91, nil, nil)
	form := rawPut(*now)
	for _, entry := range []struct{ unit, value string }{{"None", "0"}, {"Count", "7"}} {
		form.Set("MetricData.member.1.Unit", entry.unit)
		form.Set("MetricData.member.1.Value", entry.value)
		body := compressedQuery(t, []byte(form.Encode()), gzip.DefaultCompression, 0)
		w := compressionRequest(h, &scope, io.NopCloser(bytes.NewReader(body)), []string{"GZip"})
		compressionStatus(t, w, 200, "")
	}
	before := frame(t, db)
	list := compressionRequest(h, &scope, io.NopCloser(bytes.NewReader(compressedQuery(t, []byte(base("ListMetrics").Encode()), gzip.DefaultCompression, 0))), []string{"gzip"})
	compressionStatus(t, list, 200, "")
	var listing struct {
		Metrics []struct{ Namespace, MetricName string } `xml:"ListMetricsResult>Metrics>member"`
	}
	if xml.Unmarshal(list.Body.Bytes(), &listing) != nil || len(listing.Metrics) != 1 || listing.Metrics[0].Namespace != "Practice/App" || listing.Metrics[0].MetricName != "Metric" {
		t.Fatal("compressed list did not use exact persisted namespace")
	}
	stats := base("GetMetricStatistics")
	stats.Set("Namespace", "Practice/App")
	stats.Set("MetricName", "Metric")
	stats.Set("StartTime", now.Truncate(time.Minute).Format(time.RFC3339Nano))
	stats.Set("EndTime", now.Add(time.Minute).Format(time.RFC3339Nano))
	stats.Set("Period", "60")
	stats.Set("Statistics.member.1", "Sum")
	result := compressionRequest(h, &scope, io.NopCloser(bytes.NewReader(compressedQuery(t, []byte(stats.Encode()), gzip.DefaultCompression, 0))), []string{"gzip"})
	compressionStatus(t, result, 200, "")
	var output struct {
		Points []struct {
			Unit string
			Sum  float64
		} `xml:"GetMetricStatisticsResult>Datapoints>member"`
	}
	if xml.Unmarshal(result.Body.Bytes(), &output) != nil || len(output.Points) != 2 {
		t.Fatal("compressed statistics lost unit streams")
	}
	values := map[string]float64{}
	for _, p := range output.Points {
		values[p.Unit] = p.Sum
	}
	zero, found := values["None"]
	if !found || zero != 0 || values["Count"] != 7 {
		t.Fatal("compressed zero/Count output changed")
	}
	unchanged(t, db, before)
}

func TestMetricsGzipMalformedEncodingAndDecodedInputPreserveWholePeerFrame(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	h, err := NewQueryHandler(s, contextScope)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{"us-east-1", allow}
	put(t, s, "us-east-1", "Practice/App", "live", 2, nil, nil)
	put(t, s, "us-west-2", "Peer", "same", 9, nil, nil)
	before := frame(t, db)
	good := compressedQuery(t, []byte(rawPut(*now).Encode()), gzip.DefaultCompression, 0)
	crc := append([]byte{}, good...)
	crc[len(crc)-8] ^= 1
	size := append([]byte{}, good...)
	size[len(size)-4] ^= 1
	joined := append(append([]byte{}, good...), good...)
	trailing := append(append([]byte{}, good...), 0)
	cases := []struct {
		name string
		body []byte
		enc  []string
		code string
	}{
		{"notgzip", []byte("not a gzip member"), []string{"gzip"}, "InvalidParameterValue"},
		{"truncatedheader", good[:5], []string{"gzip"}, "InvalidParameterValue"},
		{"truncateddeflate", good[:len(good)/2], []string{"gzip"}, "InvalidParameterValue"},
		{"truncatedtrailer", good[:len(good)-1], []string{"gzip"}, "InvalidParameterValue"},
		{"crc", crc, []string{"gzip"}, "InvalidParameterValue"},
		{"length", size, []string{"gzip"}, "InvalidParameterValue"},
		{"trailing", trailing, []string{"gzip"}, "InvalidParameterValue"},
		{"secondmember", joined, []string{"gzip"}, "InvalidParameterValue"},
		{"brotli", good, []string{"br"}, "InvalidParameterCombination"},
		{"combined", good, []string{"gzip, gzip"}, "InvalidParameterCombination"},
		{"repeated", good, []string{"gzip", "gzip"}, "InvalidParameterCombination"},
		{"emptyencoding", good, []string{""}, "InvalidParameterCombination"},
		{"decodedutf8", compressedQuery(t, []byte{0xff}, gzip.DefaultCompression, 0), []string{"gzip"}, "InvalidParameterValue"},
		{"decodedpercent", compressedQuery(t, []byte("Action=PutMetricData&Version=2010-08-01&Namespace=%GG"), gzip.DefaultCompression, 0), []string{"gzip"}, "InvalidParameterValue"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			body := &countedBody{reader: bytes.NewReader(item.body)}
			w := compressionRequest(h, &scope, body, item.enc)
			compressionStatus(t, w, 400, item.code)
			if item.code == "InvalidParameterCombination" && body.bytes != 0 {
				t.Fatal("unsupported encoding consumed body")
			}
			unchanged(t, db, before)
		})
	}
}

// Percent-encoding x as %78 changes only the legal wire representation of
// fixed bounded dimension values. Keep the complete form below Go's parameter
// guard; a million empty '&' fields is not a valid positive parser fixture.
func compressionBoundaryForm(t *testing.T, namespace string, size int) []byte {
	t.Helper()
	form := base("PutMetricData")
	form.Set("Namespace", namespace)
	for i := 1; i <= 1000; i++ {
		prefix := fmt.Sprintf("MetricData.member.%d.", i)
		form.Set(prefix+"MetricName", "Boundary")
		form.Set(prefix+"Value", "1")
		form.Set(prefix+"Dimensions.member.1.Name", "Padding")
		form.Set(prefix+"Dimensions.member.1.Value", strings.Repeat("x", 700))
	}
	text := form.Encode()
	missing := size - len(text)
	if missing < 0 || strings.Count(text, "x") < missing/2 {
		t.Fatal("bounded form cannot reach requested boundary")
	}
	text = strings.Replace(text, "x", "%78", missing/2)
	if missing%2 != 0 {
		text += "&"
	}
	if len(text) != size || strings.Count(text, "&")+1 > 10000 {
		t.Fatal("boundary form length/parameter count incorrect")
	}
	return []byte(text)
}

func TestMetricsGzipWireAndDecodedInclusiveLimitsAndNoLengthTrust(t *testing.T) {
	s, db, _, _ := fixture(t, Limits{})
	h, err := NewQueryHandler(s, contextScope)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{"us-east-1", allow}
	put(t, s, "us-west-2", "Peer", "preserved", 5, nil, nil)
	before := frame(t, db)
	exact := compressionBoundaryForm(t, "Limit/Decoded", MaxRequestBytes)
	zipped := compressedQuery(t, exact, gzip.DefaultCompression, 0)
	if len(zipped) >= MaxRequestBytes {
		t.Fatal("expansion fixture did not compress")
	}
	compressionStatus(t, compressionRequest(h, &scope, io.NopCloser(bytes.NewReader(zipped)), []string{"gzip"}), 200, "")
	var samples int
	if db.QueryRow("SELECT count(*) FROM aws_metrics_points WHERE region='us-east-1'").Scan(&samples) != nil || samples != 1000 {
		t.Fatal("exact decoded boundary did not persist all real samples")
	}
	before = frame(t, db)
	over := compressedQuery(t, append(append([]byte{}, exact...), '&'), gzip.DefaultCompression, 0)
	compressionStatus(t, compressionRequest(h, &scope, io.NopCloser(bytes.NewReader(over)), []string{"gzip"}), 413, "RequestEntityTooLargeException")
	unchanged(t, db, before)
	// A valid single-member no-compression envelope at exactly the wire limit.
	// The form is independently valid; legal gzip Extra pads the header,
	// never an ignored suffix after the member.
	body := compressionBoundaryForm(t, "Limit/Wire", MaxRequestBytes-1024)
	minimum := compressedQuery(t, body, gzip.NoCompression, 1)
	extra := 1 + MaxRequestBytes - len(minimum)
	if extra < 1 || extra >= 65535 {
		t.Fatal("wire boundary Extra cannot fit legal gzip header")
	}
	boundary := compressedQuery(t, body, gzip.NoCompression, extra)
	if len(boundary) != MaxRequestBytes || len(body) > MaxRequestBytes {
		t.Fatal("valid wire-boundary fixture not exact")
	}
	counted := &countedBody{reader: bytes.NewReader(boundary)}
	compressionStatus(t, compressionRequest(h, &scope, counted, []string{"gzip"}), 200, "")
	if counted.bytes != MaxRequestBytes {
		t.Fatal("wire boundary read count changed")
	}
	if db.QueryRow("SELECT count(*) FROM aws_metrics_points WHERE region='us-east-1'").Scan(&samples) != nil || samples != 2000 {
		t.Fatal("exact wire boundary did not persist all real samples")
	}
	before = frame(t, db)
	// Increasing the legal gzip Extra by one creates an otherwise valid member
	// with unchanged decoded body but wire size Max+1. Content-Length lies at1.
	wireOver := compressedQuery(t, body, gzip.NoCompression, extra+1)
	if len(wireOver) != MaxRequestBytes+1 {
		t.Fatal("wire overflow is not exactly one byte")
	}
	counted = &countedBody{reader: bytes.NewReader(wireOver)}
	compressionStatus(t, compressionRequest(h, &scope, counted, []string{"gzip"}), 413, "RequestEntityTooLargeException")
	if counted.bytes != MaxRequestBytes+1 {
		t.Fatal("wire limiter trusted Content-Length or overread")
	}
	unchanged(t, db, before)
}

type compressionReadError struct{ calls int }

func (r *compressionReadError) Read([]byte) (int, error) {
	r.calls++
	return 0, errors.New("private transport detail")
}
func (*compressionReadError) Close() error { return nil }

func TestMetricsGzipUnknownCallerBeforeBodyAndTxDenialBeforeDatum(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	h, err := NewQueryHandler(s, contextScope)
	if err != nil {
		t.Fatal(err)
	}
	before := frame(t, db)
	unread := &compressionReadError{}
	compressionStatus(t, compressionRequest(h, nil, unread, []string{"gzip"}), 401, "NotAuthorized")
	if unread.calls != 0 {
		t.Fatal("unknown caller read compressed bytes")
	}
	unchanged(t, db, before)
	form := rawPut(*now)
	form.Set("MetricData.member.1.Value", "not-a-number")
	calls := 0
	denied := Scope{"ap-northeast-1", func(c context.Context, tx *sql.Tx, action, resource string) (bool, error) {
		calls++
		if action != "cloudwatch:PutMetricData" || resource != "*" || tx == nil {
			t.Fatal("wrong transaction authorization contract")
		}
		var account string
		if tx.QueryRowContext(c, "SELECT account FROM aws_metrics_meta WHERE singleton=1").Scan(&account) != nil || account != "000000000000" {
			t.Fatal("authorizer could not use caller transaction")
		}
		return false, nil
	}}
	compressionStatus(t, compressionRequest(h, &denied, io.NopCloser(bytes.NewReader(compressedQuery(t, []byte(form.Encode()), gzip.DefaultCompression, 0))), []string{"gzip"}), 403, "AccessDenied")
	if calls != 1 {
		t.Fatal("action authorizer not called exactly once")
	}
	unchanged(t, db, before)
	allowed := Scope{"us-east-1", allow}
	broken := &compressionReadError{}
	failed := compressionRequest(h, &allowed, broken, []string{"gzip"})
	if broken.calls != 1 {
		t.Fatal("wire read failure was not observed exactly once")
	}
	compressionStatus(t, failed, 400, "InvalidParameterValue")
	if strings.Contains(failed.Body.String(), "private transport") {
		t.Fatal("transport failure leaked")
	}
	unchanged(t, db, before)
}
