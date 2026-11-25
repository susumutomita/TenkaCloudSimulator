package blob

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/base64"
	"net/http"
	"reflect"
	"testing"
)

func TestStructuredCRC64BoundedNativeGet(t *testing.T) {
	denied := false
	_, s := fixture(t, ":memory:", config("accountone", "owner"), func(ctx context.Context, tx *sql.Tx, a Authorization) (bool, error) {
		allowed, err := permitted(ctx, tx, a)
		return !denied && allowed, err
	})
	create(t, s, "box")
	path := "/accountone/box/fixture"
	payload := []byte{0x00, 0x11, 0x22, 0x33}
	fullMD5 := md5.Sum(payload)
	wantStoredMD5 := base64.StdEncoding.EncodeToString(fullMD5[:])
	tag := put(t, s, "box", "fixture", string(payload), map[string]string{"x-ms-meta-label": "synthetic"}, 201).Header().Get("ETag")
	put(t, s, "box", "empty", "", nil, 201)
	large := bytes.Repeat([]byte{'x'}, (4<<20)+1)
	put(t, s, "box", "large", string(large), nil, 201)
	before := persistedRows(t, s)

	newHeaders := func(marker string) map[string]string {
		return map[string]string{"x-ms-version": "2025-01-05", "x-ms-structured-body": marker}
	}
	const marker = "XSM/1.0; properties=crc64"
	nativeRange := func() map[string]string {
		h := newHeaders(marker)
		h["x-ms-range"] = "bytes=1-2"
		return h
	}
	t.Run("native-range", func(t *testing.T) {
		w := request(t, s, "GET", path, nil, nativeRange(), learner(), 206)
		h := w.Header()
		for k, v := range map[string]string{"x-ms-version": "2025-01-05", "x-ms-structured-body": marker,
			"Content-Length": "41", "x-ms-structured-content-length": "2", "Content-Range": "bytes 1-2/4",
			"ETag": tag, "x-ms-meta-label": "synthetic", "x-ms-blob-type": "BlockBlob", "Accept-Ranges": "bytes",
			"x-ms-content-crc64": "", "Content-MD5": "", "x-ms-blob-content-md5": wantStoredMD5} {
			if h.Get(k) != v {
				t.Errorf("%s: got %q want %q", k, h.Get(k), v)
			}
		}
		if h.Get("Last-Modified") == "" {
			t.Error("missing entity timestamp")
		}
		data, ok := decodeStructuredCRC64TestSlice(w.Body.Bytes())
		if !ok || !bytes.Equal(data, payload[1:3]) {
			t.Fatal("structured body did not independently decode the exact selected bytes")
		}
	})
	t.Run("full-and-empty", func(t *testing.T) {
		for _, tc := range []struct {
			route string
			want  []byte
		}{{path, payload}, {"/accountone/box/empty", nil}} {
			w := request(t, s, "GET", tc.route, nil, newHeaders(marker), learner(), 200)
			data, ok := decodeStructuredCRC64TestSlice(w.Body.Bytes())
			if !ok || !bytes.Equal(data, tc.want) || w.Header().Get("Content-Range") != "" {
				t.Fatal("invalid complete structured content")
			}
			if w.Header().Get("x-ms-blob-content-md5") != "" || w.Header().Get("Content-MD5") != "" {
				t.Errorf("structured 200 must not advertise either legacy MD5 header: %v", w.Header())
			}
		}
	})
	t.Run("old-version-unmodified", func(t *testing.T) {
		legacy := request(t, s, "GET", path, nil, map[string]string{"x-ms-range": "bytes=1-2", "x-ms-range-get-content-crc64": "true"}, learner(), 206)
		if !bytes.Equal(legacy.Body.Bytes(), payload[1:3]) || legacy.Header().Get("x-ms-content-crc64") != "4qY3dFCtwu8=" ||
			legacy.Header().Get("Content-MD5") != "" || legacy.Header().Get("x-ms-blob-content-md5") != wantStoredMD5 ||
			legacy.Header().Get("x-ms-version") != "2023-11-03" || legacy.Header().Get("x-ms-structured-body") != "" {
			t.Fatal("old native range CRC64 changed")
		}
		whole := request(t, s, "GET", path, nil, nil, learner(), 200)
		if !bytes.Equal(whole.Body.Bytes(), payload) || whole.Header().Get("Content-MD5") != wantStoredMD5 ||
			whole.Header().Get("x-ms-blob-content-md5") != "" || whole.Header().Get("x-ms-version") != "2023-11-03" {
			t.Fatal("old native whole-blob MD5 changed")
		}
	})
	for _, tc := range []struct {
		name, method, route string
		headers             map[string]string
		status              int
	}{
		{"unknown-marker", "GET", path, newHeaders("XSM/2.0"), 400},
		{"marker-required", "GET", path, map[string]string{"x-ms-version": "2025-01-05"}, 400},
		{"old-version-with-marker", "GET", path, map[string]string{"x-ms-structured-body": marker}, 400},
		{"unrecognized-version", "GET", path, map[string]string{"x-ms-version": "2025-05-05", "x-ms-structured-body": marker}, 400},
		{"head", "HEAD", path, newHeaders(marker), 400},
		{"put", "PUT", path, newHeaders(marker), 400},
		{"delete", "DELETE", path, newHeaders(marker), 400},
		{"list", "GET", "/accountone/box?restype=container&comp=list", newHeaders(marker), 400},
		{"unknown-field", "GET", path, map[string]string{"x-ms-version": "2025-01-05", "x-ms-structured-body": marker, "x-ms-structured-content-length": "4"}, 400},
		{"range-crc-conflict", "GET", path, map[string]string{"x-ms-version": "2025-01-05", "x-ms-structured-body": marker, "x-ms-range": "bytes=1-2", "x-ms-range-get-content-crc64": "true"}, 400},
		{"range-md5-conflict", "GET", path, map[string]string{"x-ms-version": "2025-01-05", "x-ms-structured-body": marker, "x-ms-range": "bytes=1-2", "x-ms-range-get-content-md5": "true"}, 400},
		{"oversize", "GET", "/accountone/box/large", newHeaders(marker), 400},
		{"oversize-range", "GET", "/accountone/box/large", map[string]string{"x-ms-version": "2025-01-05", "x-ms-structured-body": marker, "x-ms-range": "bytes=0-4194304"}, 400},
		{"past-end-range", "GET", path, map[string]string{"x-ms-version": "2025-01-05", "x-ms-structured-body": marker, "x-ms-range": "bytes=8-"}, 416},
		{"not-modified", "GET", path, map[string]string{"x-ms-version": "2025-01-05", "x-ms-structured-body": marker, "If-None-Match": tag, "x-ms-range": "bytes=1-2"}, 304},
		{"precondition-failed", "GET", path, map[string]string{"x-ms-version": "2025-01-05", "x-ms-structured-body": marker, "If-Match": `"wrong"`, "x-ms-range": "bytes=1-2"}, 412},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, s, tc.method, tc.route, nil, tc.headers, learner(), tc.status)
			requireNoStructuredHeaders(t, w.Header())
			if tc.status == 304 && (w.Body.Len() != 0 || w.Header().Get("ETag") != tag) {
				t.Fatal("conditional response not preserved")
			}
		})
	}
	denied = true
	for _, path := range []string{path, "/accountone/box/missing", "/accountone/box/large"} {
		w := request(t, s, "GET", path, nil, nativeRange(), learner(), 403)
		requireNoStructuredHeaders(t, w.Header())
		if w.Header().Get("ETag") != "" {
			t.Fatal("denial published entity metadata")
		}
	}
	if !reflect.DeepEqual(before, persistedRows(t, s)) {
		t.Fatal("structured and failed requests changed persisted storage")
	}
}

func requireNoStructuredHeaders(t *testing.T, h http.Header) {
	t.Helper()
	if h.Get("x-ms-structured-body") != "" || h.Get("x-ms-structured-content-length") != "" || h.Get("Content-Range") != "" {
		t.Fatal("structured/range headers escaped a non-success response", h)
	}
}
