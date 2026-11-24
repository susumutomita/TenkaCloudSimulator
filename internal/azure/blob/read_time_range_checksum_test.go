package blob

import (
	"encoding/base64"
	"net/http"
	"testing"
	"time"
)

func TestAzureBlobConditionalReadWithRangeCRC64(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	const key = "/accountone/box/data"
	put(t, s, "box", "data", "\x11\x22", nil, 201)
	full := request(t, s, http.MethodGet, key, nil, nil, learner(), 200)
	stamp := full.Header().Get("Last-Modified")
	modified, err := http.ParseTime(stamp)
	if err != nil {
		t.Fatal(err)
	}
	before := fullBlobSnapshot(t, s)
	h := map[string]string{"If-Modified-Since": stamp, "Range": "bytes=1-1", "x-ms-range-get-content-crc64": "true"}
	unchanged := request(t, s, http.MethodGet, key, nil, h, learner(), 304)
	if unchanged.Body.Len() != 0 || unchanged.Header().Get("ETag") != full.Header().Get("ETag") ||
		unchanged.Header().Get("x-ms-content-crc64") != "" || unchanged.Header().Get("Content-Range") != "" ||
		unchanged.Header().Get("x-ms-blob-content-md5") != "" {
		t.Fatal("unchanged object emitted partial response data")
	}
	h["If-Modified-Since"] = modified.Add(-time.Second).Format(http.TimeFormat)
	changed := request(t, s, http.MethodGet, key, nil, h, learner(), 206)
	// Microsoft Azure Storage CRC64 byte vector for 0x22.
	want := base64.StdEncoding.EncodeToString([]byte{0xd8, 0x4a, 0xfb, 0x9e, 0xa0, 0x4f, 0xc6, 0xda})
	if changed.Body.String() != "\x22" || changed.Header().Get("Content-Range") != "bytes 1-1/2" ||
		changed.Header().Get("x-ms-content-crc64") != want || changed.Header().Get("Content-MD5") != "" {
		t.Fatal("changed object did not emit only requested transactional digest")
	}
	denied := request(t, s, http.MethodGet, key, nil, h, &Actor{Subject: "reader", Environment: s.config.Environment}, 403)
	if denied.Header().Get("ETag") != "" || denied.Header().Get("x-ms-content-crc64") != "" {
		t.Fatal("denied request emitted object information")
	}
	h["If-None-Match"] = "*"
	request(t, s, http.MethodGet, key, nil, h, learner(), 400)
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("conditional range reads altered durable state")
	}
}
