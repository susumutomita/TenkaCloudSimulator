package blob

import (
	"net/http"
	"testing"
	"time"
)

func TestAzureBlobReadIfModifiedSince(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	const key = "/accountone/box/read"
	const payload = "candidate"
	put(t, s, "box", "read", payload, nil, 201)
	baseline := request(t, s, http.MethodGet, key, nil, nil, learner(), 200)
	stamp := baseline.Header().Get("Last-Modified")
	modified, err := http.ParseTime(stamp)
	if err != nil {
		t.Fatal(err)
	}
	earlier := modified.Add(-time.Second).Format(http.TimeFormat)
	later := modified.Add(time.Second).Format(http.TimeFormat)
	before := fullBlobSnapshot(t, s)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, date := range []string{stamp, later} {
			w := request(t, s, method, key, nil, map[string]string{"If-Modified-Since": date}, learner(), 304)
			if w.Body.Len() != 0 || w.Header().Get("ETag") != baseline.Header().Get("ETag") || w.Header().Get("Last-Modified") != stamp {
				t.Fatal("not-modified response lost validators or returned data")
			}
		}
		w := request(t, s, method, key, nil, map[string]string{"If-Modified-Since": earlier}, learner(), 200)
		if method == http.MethodGet && w.Body.String() != payload || method == http.MethodHead && w.Body.Len() != 0 {
			t.Fatal("fresh representation was not returned normally")
		}
	}
	w := request(t, s, "GET", key, nil, map[string]string{"If-Modified-Since": stamp, "Range": "bytes=0-1"}, learner(), 304)
	if w.Header().Get("Content-Range") != "" || w.Body.Len() != 0 {
		t.Fatal("conditional hit returned an unsolicited partial representation")
	}
	for _, h := range []map[string]string{{"If-Modified-Since": "invalid"}, {"If-Modified-Since": stamp, "If-Match": "*"}, {"If-Modified-Since": stamp, "If-None-Match": "*"}} {
		request(t, s, "GET", key, nil, h, learner(), 400)
	}
	for _, target := range []string{key, "/accountone/box/missing"} {
		denied := request(t, s, http.MethodGet, target, nil, map[string]string{"If-Modified-Since": stamp},
			&Actor{Subject: "reader", Environment: s.config.Environment}, 403)
		if denied.Header().Get("ETag") != "" || denied.Header().Get("Last-Modified") != "" {
			t.Fatal("denied condition emitted object validators")
		}
	}
	request(t, s, "DELETE", key, nil, map[string]string{"If-Modified-Since": stamp}, learner(), 400)
	request(t, s, "GET", "/accountone/box/missing", nil, map[string]string{"If-Modified-Since": stamp}, learner(), 404)
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("read conditions mutated stored state")
	}
}
