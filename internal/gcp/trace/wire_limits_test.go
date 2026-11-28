package trace

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTraceV1SpanAndBatchLimitsPreserveStoredObservations(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	f := openTrace(t, filepath.Join(t.TempDir(), "limits.db"), &now)
	valid := makeSpan("9007199254740993", "valid", "")
	saved := traceAt(idA, valid)
	f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, saved), 200)

	for _, tc := range []struct {
		name string
		edit func(*span)
	}{
		{"empty name", func(s *span) { s.Name = "" }},
		{"128 byte name", func(s *span) { s.Name = strings.Repeat("界", 42) + "ab" }},
		{"unsupported kind", func(s *span) { s.Kind = "SERVER" }},
		{"17 labels", func(s *span) {
			for i := 0; i < 17; i++ {
				s.Labels[fmt.Sprint(i)] = "v"
			}
		}},
		{"empty label key", func(s *span) { s.Labels[""] = "blank" }},
		{"128 byte label key", func(s *span) { s.Labels[strings.Repeat("k", 128)] = "v" }},
		{"oversize label value", func(s *span) { s.Labels["field"] = strings.Repeat("v", 1025) }},
		{"noncanonical parent", func(s *span) { s.Parent = "01" }},
		{"wrong calendar date", func(s *span) { s.Start = "2026-11-31T12:00:00Z" }},
		{"unsupported leap second", func(s *span) { s.End = "2026-11-28T12:00:60Z" }},
		{"end precedes start", func(s *span) { s.End = now.Add(-time.Nanosecond).Format(stampLayout) }},
		{"older than ingestion window", func(s *span) { s.Start = now.Add(-14*24*time.Hour - time.Nanosecond).Format(stampLayout) }},
		{"later than ingestion window", func(s *span) { s.End = now.Add(3*24*time.Hour + time.Nanosecond).Format(stampLayout) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := makeSpan("1", "later observation", "")
			tc.edit(&bad)
			f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, traceAt(idB, valid, bad)), 400)
			f.send(t, t.Context(), "owner", "GET", collection+"/"+idB, "", 404)
		})
	}
	edge := makeSpan("18446744073709551615", strings.Repeat("界", 42)+"a", "0")
	edge.Start = now.Add(-14 * 24 * time.Hour).Format(stampLayout)
	edge.End = now.Add(3 * 24 * time.Hour).Format(stampLayout)
	edge.Labels = map[string]string{strings.Repeat("k", 127): strings.Repeat("v", 1024)}
	for i := 1; i < 16; i++ {
		edge.Labels[fmt.Sprint(i)] = ""
	}
	f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, traceAt(idC, edge)), 200)
	if got := parse[traceRecord](t, f.send(t, t.Context(), "owner", "GET", collection+"/"+idC, "", 200)); !reflect.DeepEqual(got, traceAt(idC, edge)) {
		t.Fatal(got)
	}
	large := make([]span, maxBatch+1)
	for i := range large {
		large[i] = valid
	}
	for _, raw := range []string{
		`{}`, `{"traces":[]}`, `{"traces":null}`, `{"traces":[null]}`, `{"traces":[}`,
		`{"traces":[[[[[[[[]]]]]]]]}`, `{"traces":[{"projectId":[]}]}`, `{"traces":[{"spans":false}]}`,
		payload(t, traceRecord{Project: project, ID: idB}), payload(t, traceAt(strings.Repeat("0", 32), valid)),
		payload(t, traceAt(idB, large...)), payload(t, traceRecord{Project: "different-project", ID: idB, Spans: []span{valid}}),
		strings.Repeat(" ", maxBody+1), string([]byte{0xff, 0x7b, 0x7d}),
	} {
		f.send(t, t.Context(), "owner", "PATCH", collection, raw, 400)
	}
	f.send(t, t.Context(), "owner", "GET", collection+"/"+idB, "", 404)
	if got := parse[traceRecord](t, f.send(t, t.Context(), "owner", "GET", collection+"/"+idA, "", 200)); !reflect.DeepEqual(got, saved) {
		t.Fatal(got)
	}
}

func TestTraceHTTPParametersAndViewDefaults(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	f := openTrace(t, filepath.Join(t.TempDir(), "queries.db"), &now)
	s := makeSpan("1", "saved", "")
	f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, traceAt(idA, s)), 200)
	rangeQuery := url.Values{"startTime": {s.Start}, "endTime": {s.End}}.Encode()
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/cloudtrace/v2/projects/" + project + "/traces", 501},
		{collection + "?startTime=" + url.QueryEscape(s.Start), 400},
		{collection + "?" + url.Values{"startTime": {s.End}, "endTime": {s.Start}}.Encode(), 400},
		{collection + "?" + rangeQuery + "&pageToken=" + strings.Repeat("a", 2049), 400},
		{collection + "?" + rangeQuery + "&pageSize=999999999999999", 400},
		{collection + "?" + rangeQuery + "&orderBy=start_time", 501},
		{collection + "?" + rangeQuery + "&bad=%zz", 400},
		{collection + "?" + rangeQuery + "&fields=traces", 400},
		{collection + "/" + idA + "?prettyPrint=maybe", 400},
		{collection + "/" + idA + "?alt=media", 400},
		{collection + "/" + idA + "?view=COMPLETE", 400},
		{collection + "/" + idA + "?prettyPrint=true&prettyPrint=false", 400},
	} {
		f.send(t, t.Context(), "owner", "GET", tc.path, "", tc.want)
	}
	for _, suffix := range []string{"", "&prettyPrint=true&alt=json", "&view=VIEW_TYPE_UNSPECIFIED&pageSize=-1", "&view=MINIMAL&orderBy=trace_id&pageSize=0"} {
		page := parse[listResponse](t, f.send(t, t.Context(), "owner", "GET", collection+"?"+rangeQuery+suffix, "", 200))
		if !reflect.DeepEqual(page.Traces, []traceRecord{traceAt(idA)}) || page.Next != "" {
			t.Fatal(page)
		}
	}
	f.send(t, t.Context(), "owner", "GET", collection+"/"+idA, "{}", 400)
	req := httptest.NewRequestWithContext(t.Context(), "PATCH", collection, strings.NewReader(payload(t, traceAt(idB, s))))
	// Pass the same authenticated actor as the ordinary fixture but a different media type.
	req = req.WithContext(context.WithValue(req.Context(), actorKey{}, "owner"))
	req.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	f.e.ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatal(response.Code)
	}
	f.send(t, t.Context(), "owner", "GET", collection+"/"+idB, "", 404)
}
