package metrics

import (
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These are native Query/parser/store tests with the existing trusted scope
// fixture. They do not exercise SDK-side parameter validation or host tokens.
func TestMetricsQueryThirtyDimensionsAcceptedThirtyOneRejected(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	h, err := NewQueryHandler(s, contextScope)
	if err != nil {
		t.Fatal(err)
	}
	queryBoundaryPeer(t, h, *now)
	scope := Scope{"us-east-1", allow}
	form := rawPut(*now)
	form.Set("Namespace", "Boundary/Dimensions")
	form.Set("MetricData.member.1.MetricName", "Thirty")
	form.Set("MetricData.member.1.Value", "7")
	want := map[string]string{}
	for i := 1; i <= 30; i++ {
		name, value := fmt.Sprintf("D%02d", i), fmt.Sprintf("v%02d", i)
		prefix := fmt.Sprintf("MetricData.member.1.Dimensions.member.%d.", i)
		form.Set(prefix+"Name", name)
		form.Set(prefix+"Value", value)
		want[name] = value
	}
	wire(t, h, scope, form, 200)
	stable := frame(t, db)
	list := base("ListMetrics")
	list.Set("Namespace", "Boundary/Dimensions")
	list.Set("MetricName", "Thirty")
	listed := wire(t, h, scope, list, 200)
	metric := queryBoundaryOneMetric(t, listed, "Boundary/Dimensions", "Thirty")
	if !reflect.DeepEqual(metric, want) {
		t.Fatal("native Query did not preserve all thirty literal dimension pairs")
	}
	unchanged(t, db, stable)
	form.Set("MetricData.member.1.Dimensions.member.31.Name", "D31")
	form.Set("MetricData.member.1.Dimensions.member.31.Value", "v31")
	queryBoundaryRefusal(t, wire(t, h, scope, form, 400))
	unchanged(t, db, stable)
}

func TestMetricsQueryTenListFiltersAcceptedElevenRejected(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	h, err := NewQueryHandler(s, contextScope)
	if err != nil {
		t.Fatal(err)
	}
	queryBoundaryPeer(t, h, *now)
	scope := Scope{"us-east-1", allow}
	seed := rawPut(*now)
	seed.Set("Namespace", "Boundary/Filters")
	seed.Set("MetricData.member.1.MetricName", "ElevenDimensions")
	seed.Set("MetricData.member.1.Value", "5")
	want := map[string]string{}
	for i := 1; i <= 11; i++ {
		name, value := fmt.Sprintf("F%02d", i), fmt.Sprintf("value%02d", i)
		prefix := fmt.Sprintf("MetricData.member.1.Dimensions.member.%d.", i)
		seed.Set(prefix+"Name", name)
		seed.Set(prefix+"Value", value)
		want[name] = value
	}
	wire(t, h, scope, seed, 200)
	stable := frame(t, db)
	list := base("ListMetrics")
	list.Set("Namespace", "Boundary/Filters")
	list.Set("MetricName", "ElevenDimensions")
	for i := 1; i <= 10; i++ {
		prefix := fmt.Sprintf("Dimensions.member.%d.", i)
		list.Set(prefix+"Name", fmt.Sprintf("F%02d", i))
		list.Set(prefix+"Value", fmt.Sprintf("value%02d", i))
	}
	listed := wire(t, h, scope, list, 200)
	if !reflect.DeepEqual(queryBoundaryOneMetric(t, listed, "Boundary/Filters", "ElevenDimensions"), want) {
		t.Fatal("ten actual wire filters did not return the complete eleven-dimension metric")
	}
	unchanged(t, db, stable)
	// This eleventh filter really matches the saved metric; rejection must be
	// the native filter-count boundary, not a successful empty match result.
	list.Set("Dimensions.member.11.Name", "F11")
	list.Set("Dimensions.member.11.Value", "value11")
	queryBoundaryRefusal(t, wire(t, h, scope, list, 400))
	unchanged(t, db, stable)
}

func TestMetricsQueryLaterInvalidDatumHasNoPartialWriteAndRepairs(t *testing.T) {
	s, db, now, _ := fixture(t, Limits{})
	h, err := NewQueryHandler(s, contextScope)
	if err != nil {
		t.Fatal(err)
	}
	queryBoundaryPeer(t, h, *now)
	scope := Scope{"us-east-1", allow}
	seed := rawPut(*now)
	seed.Set("Namespace", "Boundary/Batch")
	seed.Set("MetricData.member.1.MetricName", "Existing")
	seed.Set("MetricData.member.1.Value", "5")
	wire(t, h, scope, seed, 200)
	before := frame(t, db)
	batch := rawPut(*now)
	batch.Set("Namespace", "Boundary/Batch")
	batch.Set("MetricData.member.1.MetricName", "NewFirst")
	batch.Set("MetricData.member.1.Value", "7")
	batch.Set("MetricData.member.2.MetricName", "NewSecond")
	batch.Set("MetricData.member.2.Timestamp", now.Format(time.RFC3339Nano))
	batch.Set("MetricData.member.2.Value", "not-a-number")
	queryBoundaryRefusal(t, wire(t, h, scope, batch, 400))
	// Includes schema, constructor key/config, regional revisions, all series,
	// every sample, sqlite_sequence and the nonempty peer region.
	unchanged(t, db, before)
	batch.Set("MetricData.member.2.Value", "11")
	wire(t, h, scope, batch, 200)
	var series, points, revision int
	if db.QueryRow("SELECT count(*) FROM aws_metrics_series WHERE region='us-east-1'").Scan(&series) != nil || series != 3 || db.QueryRow("SELECT count(*) FROM aws_metrics_points WHERE region='us-east-1'").Scan(&points) != nil || points != 3 || db.QueryRow("SELECT revision FROM aws_metrics_regions WHERE region='us-east-1'").Scan(&revision) != nil || revision != 2 {
		t.Fatal("repaired two-record wire batch did not commit exactly once")
	}
	after := frame(t, db)
	for _, expected := range []struct {
		region, namespace, metric string
		sum                       float64
	}{
		{"us-east-1", "Boundary/Batch", "Existing", 5},
		{"us-east-1", "Boundary/Batch", "NewFirst", 7},
		{"us-east-1", "Boundary/Batch", "NewSecond", 11},
		{"us-west-2", "Peer/Boundary", "Peer", 13},
	} {
		statistics := base("GetMetricStatistics")
		statistics.Set("Namespace", expected.namespace)
		statistics.Set("MetricName", expected.metric)
		statistics.Set("StartTime", now.Truncate(time.Minute).Format(time.RFC3339Nano))
		statistics.Set("EndTime", now.Truncate(time.Minute).Add(time.Minute).Format(time.RFC3339Nano))
		statistics.Set("Period", "60")
		statistics.Set("Statistics.member.1", "Sum")
		statistics.Set("Statistics.member.2", "SampleCount")
		w := wire(t, h, Scope{expected.region, allow}, statistics, 200)
		var parsed struct {
			Label string `xml:"GetMetricStatisticsResult>Label"`
			Rows  []struct {
				Unit             string
				Sum, SampleCount float64
			} `xml:"GetMetricStatisticsResult>Datapoints>member"`
		}
		if xml.Unmarshal(w.Body.Bytes(), &parsed) != nil || parsed.Label != expected.metric || len(parsed.Rows) != 1 || parsed.Rows[0].Unit != "None" || parsed.Rows[0].Sum != expected.sum || parsed.Rows[0].SampleCount != 1 {
			t.Fatal("repaired native batch or preserved seed/peer has wrong literal statistics")
		}
		unchanged(t, db, after)
	}
}

func queryBoundaryPeer(t *testing.T, h *Handler, now time.Time) {
	t.Helper()
	peer := rawPut(now)
	peer.Set("Namespace", "Peer/Boundary")
	peer.Set("MetricData.member.1.MetricName", "Peer")
	peer.Set("MetricData.member.1.Value", "13")
	wire(t, h, Scope{"us-west-2", allow}, peer, 200)
}

func queryBoundaryOneMetric(t *testing.T, w *httptest.ResponseRecorder, namespace, name string) map[string]string {
	t.Helper()
	var listed struct {
		Metrics []struct {
			Namespace, MetricName string
			Dimensions            []struct{ Name, Value string } `xml:"Dimensions>member"`
		} `xml:"ListMetricsResult>Metrics>member"`
	}
	if xml.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed.Metrics) != 1 || listed.Metrics[0].Namespace != namespace || listed.Metrics[0].MetricName != name {
		t.Fatal("native wire ListMetrics did not return the exact seeded identity")
	}
	result := map[string]string{}
	for _, d := range listed.Metrics[0].Dimensions {
		if _, exists := result[d.Name]; exists {
			t.Fatal("native wire returned duplicate dimension names")
		}
		result[d.Name] = d.Value
	}
	return result
}

func queryBoundaryRefusal(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	var parsed struct {
		XMLName xml.Name
		Failure struct{ Type, Code string } `xml:"Error"`
		ID      string                      `xml:"RequestId"`
	}
	if w.Code != 400 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/xml") || xml.Unmarshal(w.Body.Bytes(), &parsed) != nil || parsed.XMLName.Local != "ErrorResponse" || parsed.XMLName.Space != "http://monitoring.amazonaws.com/doc/2010-08-01/" || parsed.Failure.Code != "InvalidParameterValue" || parsed.Failure.Type != "Sender" || parsed.ID == "" {
		t.Fatal("native boundary refusal did not carry the expected400 XML contract")
	}
	if strings.Contains(w.Body.String(), "<Datapoints>") || strings.Contains(w.Body.String(), "<ListMetricsResult>") || strings.Contains(w.Body.String(), "<PutMetricDataResponse") {
		t.Fatal("native boundary refusal disclosed a partial success result")
	}
}
