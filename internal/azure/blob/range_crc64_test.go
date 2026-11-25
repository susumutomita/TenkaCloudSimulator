package blob

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func TestAzureBlobTransactionalRangeCRC64(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	put(t, s, "box", "data", "\x11\x22", nil, 201)
	const path = "/accountone/box/data"
	const flag = "x-ms-range-get-content-crc64"
	// Independent Azure Storage CRC64 vector for byte 0x22 from the official
	// structured-body example. Only the checksum, not that body framing, is used.
	want := base64.StdEncoding.EncodeToString([]byte{0xd8, 0x4a, 0xfb, 0x9e, 0xa0, 0x4f, 0xc6, 0xda})
	got := request(t, s, http.MethodGet, path, nil, map[string]string{"x-ms-range": "bytes=1-1", flag: "true"}, learner(), 206)
	if got.Body.String() != "\x22" || got.Header().Get("x-ms-content-crc64") != want ||
		got.Header().Get("Content-MD5") != "" || got.Header().Get("x-ms-blob-content-md5") == "" {
		t.Fatalf("wrong checksum scope or serialization: %v", got.Header())
	}
	// A malformed checksum request must not reach lookup; an absent blob is
	// not allowed to change the header-level 400 into an object-level 404.
	missing := "/accountone/box/missing"
	for _, key := range []string{path, missing} {
		badHeader := map[string]string{flag: "true"}
		bad := request(t, s, http.MethodGet, key, nil, badHeader, learner(), 400)
		if bad.Header().Get("x-ms-error-code") != "InvalidHeaderValue" {
			t.Fatal("missing range did not fail header validation")
		}
		denied := request(t, s, http.MethodGet, key, nil, badHeader,
			&Actor{Subject: "reader", Environment: s.config.Environment}, 403)
		if denied.Header().Get("x-ms-error-code") != "AuthorizationPermissionMismatch" {
			t.Fatal("malformed request bypassed the authorization boundary")
		}
	}
	validCRC := map[string]string{"Range": "bytes=0-0", flag: "true"}
	denied := request(t, s, http.MethodGet, path, nil, validCRC,
		&Actor{Subject: "reader", Environment: s.config.Environment}, 403)
	if denied.Header().Get("x-ms-content-crc64") != "" || denied.Header().Get("Content-Range") != "" {
		t.Fatal("refused subject received range response headers")
	}
	request(t, s, http.MethodGet, missing, nil, validCRC, learner(), 404)
	full := request(t, s, http.MethodGet, path, nil, nil, learner(), 200)
	if full.Header().Get("x-ms-content-crc64") != "" {
		t.Fatal("unrequested CRC64")
	}
	put(t, s, "box", "large", strings.Repeat("a", (4<<20)+1), nil, 201)
	good := request(t, s, http.MethodGet, "/accountone/box/large", nil, map[string]string{"Range": "bytes=0-4194303", flag: "true"}, learner(), 206)
	if good.Body.Len() != 4<<20 || good.Header().Get("x-ms-content-crc64") == "" {
		t.Fatal("exact-cap CRC64 refused")
	}
	for _, tc := range []struct {
		path    string
		headers map[string]string
	}{
		{path, map[string]string{flag: "true"}},
		{path, map[string]string{"Range": "bytes=0-1", flag: "false"}},
		{path, map[string]string{"Range": "bytes=0-1", flag: "true", "x-ms-range-get-content-md5": "true"}},
		{"/accountone/box/large", map[string]string{"Range": "bytes=0-", flag: "true"}},
	} {
		w := request(t, s, http.MethodGet, tc.path, nil, tc.headers, learner(), 400)
		if w.Header().Get("x-ms-content-crc64") != "" || w.Header().Get("Content-Range") != "" {
			t.Fatal("error returned checksum/range")
		}
	}
}
