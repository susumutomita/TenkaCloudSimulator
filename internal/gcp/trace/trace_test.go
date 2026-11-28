package trace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	driver "github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const (
	project    = "trace-local"
	idA        = "00000000000000000000000000000001"
	idB        = "00000000000000000000000000000002"
	idC        = "00000000000000000000000000000003"
	collection = "/cloudtrace/v1/projects/" + project + "/traces"
)

type actorKey struct{}
type fixture struct {
	db  *core.Store
	e   *Engine
	now *time.Time
}

func openTrace(t *testing.T, file string, clock *time.Time) fixture {
	t.Helper()
	db, err := core.New(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e, err := New(db.Database(), Config{Project: project, Owner: "owner", Now: func() time.Time { return *clock }, Subject: func(ctx context.Context) (string, bool) { s, ok := ctx.Value(actorKey{}).(string); return s, ok }})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{db, e, clock}
}

func (f fixture) send(t *testing.T, ctx context.Context, actor, method, path, body string, want int) []byte {
	t.Helper()
	req := httptest.NewRequestWithContext(context.WithValue(ctx, actorKey{}, actor), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.e.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("%s expected %d, got %d: %s", method, want, w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatal("missing JSON response")
	}
	return w.Body.Bytes()
}

func makeSpan(id, name, parent string) span {
	return span{ID: id, Parent: parent, Name: name, Start: "2026-11-28T12:00:00.123456789Z", End: "2026-11-28T12:00:00.223456789Z", Kind: "RPC_SERVER", Labels: map[string]string{"fixture": "local"}}
}
func payload(t *testing.T, traces ...traceRecord) string {
	t.Helper()
	buf, err := json.Marshal(batch{traces})
	if err != nil {
		t.Fatal(err)
	}
	return string(buf)
}
func traceAt(id string, spans ...span) traceRecord { return traceRecord{project, id, spans} }
func listAt(view string) string {
	return collection + "?" + url.Values{"startTime": {"2026-11-28T12:00:00.223456789Z"}, "endTime": {"2026-11-28T12:00:00.223456789Z"}, "view": {view}, "pageSize": {"1"}}.Encode()
}
func parse[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTraceAppendViewsAndPaging(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	file := filepath.Join(t.TempDir(), "observations.db")
	ctx := t.Context()
	f := openTrace(t, file, &clock)
	root := makeSpan("9007199254740993", "root", "")
	child := makeSpan("18446744073709551615", "child", root.ID)
	if got := string(f.send(t, ctx, "owner", "PATCH", collection, payload(t, traceAt(idA, root, child), traceAt(idB, root)), 200)); strings.TrimSpace(got) != "{}" {
		t.Fatal(got)
	}
	changed := root
	changed.Name = "another observation"
	f.send(t, ctx, "owner", "PATCH", collection, payload(t, traceAt(idA, root, changed)), 200)
	whole := parse[traceRecord](t, f.send(t, ctx, "owner", "GET", collection+"/"+idA, "", 200))
	if !reflect.DeepEqual(whole, traceAt(idA, root, child, root, changed)) {
		t.Fatal(whole)
	}
	minimal := parse[listResponse](t, f.send(t, ctx, "owner", "GET", listAt("MINIMAL"), "", 200))
	if len(minimal.Traces) != 1 || minimal.Next == "" || !reflect.DeepEqual(minimal.Traces[0], traceAt(idA)) {
		t.Fatal(minimal)
	}
	f.send(t, ctx, "owner", "PATCH", collection, payload(t, traceAt(idC, root), traceAt(idB, changed)), 200)
	second := parse[listResponse](t, f.send(t, ctx, "owner", "GET", listAt("MINIMAL")+"&pageToken="+url.QueryEscape(minimal.Next), "", 200))
	if len(second.Traces) != 1 || second.Traces[0].ID != idB || second.Next != "" {
		t.Fatal(second)
	}
	for view, want := range map[string]traceRecord{"ROOTSPAN": traceAt(idA, root, root, changed), "COMPLETE": whole} {
		read := parse[listResponse](t, f.send(t, ctx, "owner", "GET", listAt(view), "", 200))
		if len(read.Traces) != 1 || !reflect.DeepEqual(read.Traces[0], want) {
			t.Fatal(read, view)
		}
	}
	f.send(t, ctx, "owner", "GET", listAt("COMPLETE")+"&pageToken="+url.QueryEscape(minimal.Next), "", 400)
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f = openTrace(t, file, &clock)
	f.send(t, ctx, "owner", "GET", listAt("MINIMAL")+"&pageToken="+url.QueryEscape(minimal.Next), "", 400)
	if saved := parse[traceRecord](t, f.send(t, ctx, "owner", "GET", collection+"/"+idA, "", 200)); !reflect.DeepEqual(saved, whole) {
		t.Fatal(saved)
	}
}

func TestTraceBoundariesAndRollback(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	file := filepath.Join(t.TempDir(), "observations.db")
	ctx := t.Context()
	f := openTrace(t, file, &clock)
	s := makeSpan("1", "base", "")
	body := payload(t, traceAt(idA, s))
	f.send(t, ctx, "owner", "PATCH", collection, body, 200)
	for _, tc := range []struct {
		actor, method, path, body string
		code                      int
	}{
		{"peer", "GET", collection + "/" + idA, "", 403}, {"peer", "PATCH", collection, body, 403},
		{"owner", "GET", strings.Replace(collection, project, "another-project", 1) + "/" + idA, "", 403},
		{"owner", "GET", collection + "/" + idC, "", 404}, {"owner", "GET", collection + "/bad", "", 400},
		{"owner", "GET", collection, "", 400}, {"owner", "GET", listAt("COMPLETE") + "&filter=latency", "", 501},
		{"owner", "GET", listAt("UNKNOWN"), "", 400}, {"owner", "GET", listAt("MINIMAL") + "&pageToken=bad", "", 400},
		{"owner", "GET", listAt("ROOTSPAN") + "&pageSize=2", "", 400}, {"owner", "DELETE", collection + "/" + idA, "", 501},
		{"owner", "PATCH", collection, strings.Replace(body, "\"spanId\":\"1\"", "\"spanId\":1", 1), 400},
		{"owner", "PATCH", collection, strings.Replace(body, "\"spanId\":\"1\"", "\"spanId\":\"1\",\"spanId\":\"2\"", 1), 400},
		{"owner", "PATCH", collection, strings.Replace(body, "\"projectId\"", "\"ProjectID\"", 1), 400},
		{"owner", "PATCH", collection, strings.Replace(body, "\"spanId\":\"1\"", "\"spanId\":\"18446744073709551616\"", 1), 400},
		{"owner", "PATCH", collection, body + "{}", 400},
		{"owner", "PATCH", collection, payload(t, traceAt(idB, s), traceAt(idC, makeSpan("0", "bad", ""))), 400},
	} {
		f.send(t, ctx, tc.actor, tc.method, tc.path, tc.body, tc.code)
	}
	f.send(t, ctx, "owner", "GET", collection+"/"+idB, "", 404)
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	db := f.db.Database()
	connection, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var observed int
	err = connection.Raw(func(raw any) error {
		return raw.(driver.Conn).Raw().CreateFunction("trace_interrupt", 1, 0, func(result sqlite3.Context, values ...sqlite3.Value) {
			observed = int(values[0].Int64())
			cancel()
			result.ResultNull()
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = connection.ExecContext(ctx, `CREATE TEMP TRIGGER trace_test_interrupt AFTER INSERT ON gcp_trace_spans BEGIN
		SELECT trace_interrupt((SELECT count(*) FROM gcp_trace_spans WHERE trace_id=NEW.trace_id)); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err = connection.Close(); err != nil {
		t.Fatal(err)
	}
	f.send(t, request, "owner", "PATCH", collection, payload(t, traceAt(idB, s, s)), 500)
	if !errors.Is(request.Err(), context.Canceled) || observed != 1 {
		t.Fatal("interruption did not follow the first insert", observed)
	}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f = openTrace(t, file, &clock)
	f.send(t, ctx, "owner", "GET", collection+"/"+idB, "", 404)
	if a := parse[traceRecord](t, f.send(t, ctx, "owner", "GET", collection+"/"+idA, "", 200)); !reflect.DeepEqual(a, traceAt(idA, s)) {
		t.Fatal(a)
	}
	f.send(t, ctx, "owner", "PATCH", collection, payload(t, traceAt(idB, s, s)), 200)
	peer := openTrace(t, filepath.Join(t.TempDir(), "other.db"), &clock)
	peer.send(t, ctx, "owner", "GET", collection+"/"+idB, "", 404)
	cfg := f.e.config
	cfg.Owner = "replacement"
	if _, err := New(f.db.Database(), cfg); err == nil {
		t.Fatal("owner silently replaced")
	}
}

func TestTraceBoundsAndRetention(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	f := openTrace(t, filepath.Join(t.TempDir(), "observations.db"), &clock)
	ctx := t.Context()
	s := makeSpan("1", "sample", "")
	many := make([]span, 100)
	for i := range many {
		many[i] = s
	}
	f.send(t, ctx, "owner", "PATCH", collection, payload(t, traceAt(idA, many...)), 200)
	f.send(t, ctx, "owner", "PATCH", collection, payload(t, traceAt(idA, many[:28]...)), 200)
	f.send(t, ctx, "owner", "PATCH", collection, payload(t, traceAt(idB, s), traceAt(idA, s)), 429)
	f.send(t, ctx, "owner", "GET", collection+"/"+idB, "", 404)
	for i := 2; i <= 8; i++ {
		id := fmt.Sprintf("%032x", i)
		f.send(t, ctx, "owner", "PATCH", collection, payload(t, traceAt(id, many...)), 200)
		f.send(t, ctx, "owner", "PATCH", collection, payload(t, traceAt(id, many[:28]...)), 200)
	}
	f.send(t, ctx, "owner", "PATCH", collection, payload(t, traceAt("ffffffffffffffffffffffffffffffff", s)), 429)
	clock = clock.Add(retention + time.Second)
	f.send(t, ctx, "owner", "GET", collection+"/"+idA, "", 404)
	s.Start, s.End = clock.Format(stampLayout), clock.Format(stampLayout)
	f.send(t, ctx, "owner", "PATCH", collection, payload(t, traceAt(idA, s)), 200)
	var remaining int
	if err := f.db.Database().QueryRow("SELECT count(*) FROM gcp_trace_spans").Scan(&remaining); err != nil || remaining != 1 {
		t.Fatal(err, remaining)
	}
}

func TestTraceDeadlineRollsBackStagedObservation(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	file := filepath.Join(t.TempDir(), "trace.db")
	f := openTrace(t, file, &clock)
	s := makeSpan("9007199254740993", "before deadline", "")
	f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, traceAt(idA, s)), 200)

	conn, err := f.db.Database().Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Start the HTTP deadline only after the connection and its trigger are ready.
	var request context.Context
	var staged int64
	err = conn.Raw(func(raw any) error {
		return raw.(driver.Conn).Raw().CreateFunction("await_trace_deadline", 1, 0, func(result sqlite3.Context, args ...sqlite3.Value) {
			staged = args[0].Int64()
			<-request.Done()
			result.ResultNull()
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(t.Context(), `CREATE TEMP TRIGGER await_deadline AFTER INSERT ON gcp_trace_spans BEGIN
		SELECT await_trace_deadline((SELECT count(*) FROM gcp_trace_spans WHERE trace_id=NEW.trace_id)); END`); err != nil {
		t.Fatal(err)
	}
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	request, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	f.send(t, request, "owner", "PATCH", collection, payload(t, traceAt(idB, s, s)), 500)
	if !errors.Is(request.Err(), context.DeadlineExceeded) || staged != 1 {
		t.Fatalf("deadline did not interrupt a staged observation: %v, staged=%d", request.Err(), staged)
	}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f = openTrace(t, file, &clock)
	f.send(t, t.Context(), "owner", "GET", collection+"/"+idB, "", 404)
	got := parse[traceRecord](t, f.send(t, t.Context(), "owner", "GET", collection+"/"+idA, "", 200))
	if !reflect.DeepEqual(got, traceAt(idA, s)) {
		t.Fatal(got)
	}
	f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, traceAt(idB, s, s)), 200)
}

func TestTraceFinalBatchReadbackDetectsChangedEarlierInsert(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	f := openTrace(t, filepath.Join(t.TempDir(), "trace.db"), &clock)
	s := makeSpan("17", "original", "")
	f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, traceAt(idA, s)), 200)
	_, err := f.db.Database().Exec(`CREATE TEMP TRIGGER change_previous_insert AFTER INSERT ON gcp_trace_spans
		WHEN NEW.trace_id='` + idC + `' BEGIN
		UPDATE gcp_trace_spans SET document='{}' WHERE trace_id='` + idB + `'; END`)
	if err != nil {
		t.Fatal(err)
	}
	f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, traceAt(idB, s), traceAt(idC, s)), 500)
	for _, id := range []string{idB, idC} {
		f.send(t, t.Context(), "owner", "GET", collection+"/"+id, "", 404)
	}
	got := parse[traceRecord](t, f.send(t, t.Context(), "owner", "GET", collection+"/"+idA, "", 200))
	if !reflect.DeepEqual(got, traceAt(idA, s)) {
		t.Fatal(got)
	}
}

func TestTimestampWireFormatAndAtomicRejection(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	f := openTrace(t, filepath.Join(t.TempDir(), "timestamps.db"), &clock)
	original := makeSpan("1", "baseline", "")
	f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, traceAt(idA, original)), 200)
	for _, raw := range []string{
		"2026-11-28T12:00:00,123Z", "2026-11-28T12:00:00.1234567890Z",
		"2026-11-28T12:00:00+24:00", "2026-11-28T12:00:00+00:60", "2026-11-28T2:00:00Z",
	} {
		t.Run(raw, func(t *testing.T) {
			bad := original
			bad.Start, bad.End = raw, raw
			f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, traceAt(idB, original), traceAt(idC, bad)), 400)
			values := url.Values{"startTime": {raw}, "endTime": {raw}}
			f.send(t, t.Context(), "owner", "GET", collection+"?"+values.Encode(), "", 400)
			for _, id := range []string{idB, idC} {
				f.send(t, t.Context(), "owner", "GET", collection+"/"+id, "", 404)
			}
		})
	}
	if got := parse[traceRecord](t, f.send(t, t.Context(), "owner", "GET", collection+"/"+idA, "", 200)); !reflect.DeepEqual(got, traceAt(idA, original)) {
		t.Fatal(got)
	}
	valid := original
	valid.Start, valid.End = "2026-11-28T16:30:00.123456789+04:30", "2026-11-28T07:00:00.223456789-05:00"
	f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, traceAt(idB, valid)), 200)
	if got := parse[traceRecord](t, f.send(t, t.Context(), "owner", "GET", collection+"/"+idB, "", 200)); !reflect.DeepEqual(got, traceAt(idB, original)) {
		t.Fatal(got)
	}
}

func TestTracePreservesUnicodeScalars(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	f := openTrace(t, filepath.Join(t.TempDir(), "unicode.db"), &clock)
	s := makeSpan("27", "original", "")
	f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, traceAt(idA, s)), 200)

	s.Name = "wire-name"
	body := payload(t, traceAt(idB, s), traceAt(idC, s))
	for _, tc := range []struct{ original, invalid string }{
		{"wire-name", `\ud800`}, {"wire-name", `\udc00`},
		{"fixture", `\ud800`}, {"local", `\udfff`},
	} {
		// Corrupt only the second trace. Its failure must roll back the first.
		pos := strings.LastIndex(body, tc.original)
		if pos < 0 {
			t.Fatal(tc.original)
		}
		f.send(t, t.Context(), "owner", "PATCH", collection, body[:pos]+tc.invalid+body[pos+len(tc.original):], 400)
		f.send(t, t.Context(), "owner", "GET", collection+"/"+idB, "", 404)
		f.send(t, t.Context(), "owner", "GET", collection+"/"+idC, "", 404)
	}
	for i, tc := range []struct{ wire, name string }{
		{`\ud83d\ude80`, "🚀"}, {`\ufffd`, "�"}, {`\\ud800`, `\ud800`},
	} {
		id := fmt.Sprintf("%032x", 25+i)
		full := strings.Replace(payload(t, traceAt(id, s)), "wire-name", tc.wire, 1)
		f.send(t, t.Context(), "owner", "PATCH", collection, full, 200)
		saved := parse[traceRecord](t, f.send(t, t.Context(), "owner", "GET", collection+"/"+id, "", 200))
		if len(saved.Spans) != 1 || saved.Spans[0].Name != tc.name {
			t.Fatal(saved)
		}
	}
}
