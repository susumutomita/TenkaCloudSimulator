package metrics

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsQueryChecksEveryTargetHeaderBeforeBodyAndState(t *testing.T) {
	cases := []struct {
		name    string
		targets []string
		denied  bool
	}{
		{"absent", nil, false},
		{"single_empty", []string{""}, false},
		{"first_empty_second_nonempty", []string{"", "TrentService.CreateKey"}, true},
		{"first_nonempty_second_empty", []string{"TrentService.CreateKey", ""}, true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			s, db, now, _ := fixture(t, Limits{})
			h, err := NewQueryHandler(s, contextScope)
			if err != nil {
				t.Fatal(err)
			}
			scope := Scope{"us-east-1", allow}
			peer := Scope{"us-west-2", allow}
			peerForm := rawPut(*now)
			peerForm.Set("MetricData.member.1.Value", "13")
			wire(t, h, peer, peerForm, http.StatusOK)
			before := frame(t, db)
			form := rawPut(*now)
			form.Set("MetricData.member.1.Value", "7")
			body := &countedBody{reader: strings.NewReader(form.Encode())}
			r := httptest.NewRequest("POST", "http://127.0.0.1/", body)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			for _, target := range item.targets {
				r.Header.Add("X-Amz-Target", target)
			}
			r = r.WithContext(context.WithValue(r.Context(), scopeKey{}, scope))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if item.denied {
				var envelope struct {
					Failure struct{ Code string } `xml:"Error"`
				}
				if w.Code != http.StatusBadRequest || xml.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Failure.Code != "InvalidParameterCombination" {
					t.Fatal("nonempty target header was not refused with its native XML error")
				}
				if body.bytes != 0 {
					t.Fatal("target conflict consumed the Query body")
				}
				unchanged(t, db, before)
				return
			}
			if w.Code != http.StatusOK || body.bytes == 0 || !strings.Contains(w.Body.String(), "<PutMetricDataResponse") {
				t.Fatal("absent or single empty target changed normal Query admission")
			}
			for _, expected := range []struct {
				scope Scope
				value float64
			}{{scope, 7}, {peer, 13}} {
				result, err := s.GetMetricStatistics(context.Background(), expected.scope, statInput(*now, "Metric"))
				if err != nil || len(result.Datapoints) != 1 || result.Datapoints[0].SampleCount != 1 || result.Datapoints[0].Sum != expected.value {
					t.Fatal("valid admission did not preserve the literal sample and peer")
				}
			}
		})
	}
}
