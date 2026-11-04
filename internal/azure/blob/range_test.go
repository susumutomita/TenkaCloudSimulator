package blob

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/base64"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestAzureBlobSingleRangeDownloadMatchesDocumentedSubset(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	tag := put(t, s, "box", "data", "0123456789", nil, 201).Header().Get("ETag")
	put(t, s, "box", "empty", "", nil, 201)
	before := persistedRows(t, s)
	cases := []struct {
		name               string
		headers            map[string]string
		status             int
		body, contentRange string
	}{
		{"closed", map[string]string{"Range": "bytes=2-5"}, 206, "2345", "bytes 2-5/10"},
		{"open", map[string]string{"Range": "bytes=7-"}, 206, "789", "bytes 7-9/10"},
		{"clamped", map[string]string{"Range": "bytes=8-999"}, 206, "89", "bytes 8-9/10"},
		{"whole-range", map[string]string{"Range": "bytes=0-"}, 206, "0123456789", "bytes 0-9/10"},
		{"native-precedence", map[string]string{"Range": "invalid-ignored", "x-ms-range": "bytes=3-4"}, 206, "34", "bytes 3-4/10"},
		{"range-md5", map[string]string{"x-ms-range": "bytes=2-5", "x-ms-range-get-content-md5": "true"}, 206, "2345", "bytes 2-5/10"},
		{"past-end", map[string]string{"Range": "bytes=10-"}, 416, "", ""},
		{"reversed", map[string]string{"Range": "bytes=5-2"}, 416, "", ""},
		{"suffix-unsupported", map[string]string{"Range": "bytes=-3"}, 400, "", ""},
		{"multirange-unsupported", map[string]string{"Range": "bytes=0-1,3-4"}, 400, "", ""},
		{"overflow", map[string]string{"Range": "bytes=999999999999999999999999-"}, 400, "", ""},
		{"signed", map[string]string{"Range": "bytes=+1-2"}, 400, "", ""},
		{"empty", map[string]string{"Range": ""}, 400, "", ""},
		{"missing-range-md5", map[string]string{"x-ms-range-get-content-md5": "true"}, 400, "", ""},
		{"invalid-md5-boolean", map[string]string{"Range": "bytes=0-1", "x-ms-range-get-content-md5": "false"}, 400, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := request(t, s, "GET", "/accountone/box/data", nil, c.headers, learner(), c.status)
			if c.status == 206 {
				if w.Body.String() != c.body || w.Header().Get("Content-Range") != c.contentRange || w.Header().Get("ETag") != tag || w.Header().Get("Accept-Ranges") != "bytes" {
					t.Fatal("range response mismatch", w.Header(), w.Body.String())
				}
				wantMD5 := ""
				if c.headers["x-ms-range-get-content-md5"] == "true" {
					sum := md5.Sum([]byte(c.body))
					wantMD5 = base64.StdEncoding.EncodeToString(sum[:])
				}
				if w.Header().Get("Content-MD5") != wantMD5 {
					t.Fatal("range advertised whole-blob or incorrect MD5")
				}
			} else if w.Header().Get("Content-Range") != "" || w.Header().Get("ETag") != "" || w.Header().Get("Content-MD5") != "" {
				t.Fatal("range failure published object headers", w.Header())
			}
		})
	}
	request(t, s, "GET", "/accountone/box/empty", nil, map[string]string{"Range": "bytes=0-"}, learner(), 416)
	request(t, s, "GET", "/accountone/box/data", nil, map[string]string{"Range": "bytes=2-5", "If-Match": `"wrong"`}, learner(), 412)
	w := request(t, s, "GET", "/accountone/box/data", nil, map[string]string{"Range": "bytes=2-5", "If-None-Match": tag}, learner(), 304)
	if w.Body.Len() != 0 {
		t.Fatal("conditional304 returned range body")
	}
	full := request(t, s, "GET", "/accountone/box/data", nil, nil, learner(), 200)
	sum := md5.Sum([]byte("0123456789"))
	if full.Header().Get("Content-MD5") != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Fatal("whole object MD5 lost")
	}
	for _, method := range []string{"HEAD", "PUT", "DELETE"} {
		request(t, s, method, "/accountone/box/data", nil, map[string]string{"Range": "bytes=0-1"}, learner(), 400)
	}
	r := httptest.NewRequest("GET", "/accountone/box/data", nil).WithContext(WithActor(context.Background(), *learner()))
	r.Header.Set("x-ms-version", APIVersion)
	r.Header.Add("Range", "bytes=0-1")
	r.Header.Add("Range", "bytes=2-3")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	if rec.Code != 400 {
		t.Fatal("repeated range accepted")
	}
	if !reflect.DeepEqual(before, persistedRows(t, s)) {
		t.Fatal("range read/refusal changed saved state")
	}
}

func TestAzureBlobRangeMD5BoundAndAuthorizationPrecedence(t *testing.T) {
	denied := false
	auth := func(ctx context.Context, tx *sql.Tx, a Authorization) (bool, error) {
		return !denied && a.Actor.Subject == "learner" && a.Resource.Owner == "owner", nil
	}
	_, s := fixture(t, ":memory:", config("accountone", "owner"), auth)
	create(t, s, "box")
	data := bytes.Repeat([]byte{'a'}, (4<<20)+1)
	tag := put(t, s, "box", "large", string(data), nil, 201).Header().Get("ETag")
	before := persistedRows(t, s)
	good := request(t, s, "GET", "/accountone/box/large", nil, map[string]string{"x-ms-range": "bytes=0-4194303", "x-ms-range-get-content-md5": "true"}, learner(), 206)
	sum := md5.Sum(data[:4<<20])
	if good.Body.Len() != 4<<20 || good.Header().Get("Content-MD5") != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Fatal("4MiB checksum boundary")
	}
	request(t, s, "GET", "/accountone/box/large", nil, map[string]string{"Range": "bytes=0-4194304", "x-ms-range-get-content-md5": "true"}, learner(), 400)
	denied = true
	for _, path := range []string{"large", "missing"} {
		for _, headers := range []map[string]string{{"Range": "bytes=99999999-"}, {"Range": "bytes=-1"}, {"x-ms-range-get-content-md5": "true"}, {"Range": "bytes=0-1", "If-None-Match": tag}} {
			w := request(t, s, "GET", "/accountone/box/"+path, nil, headers, learner(), 403)
			if w.Header().Get("ETag") != "" || w.Header().Get("Content-Range") != "" || w.Header().Get("Content-MD5") != "" {
				t.Fatal("denied range disclosed object identity/size")
			}
		}
	}
	if !reflect.DeepEqual(before, persistedRows(t, s)) {
		t.Fatal("range errors or authorization refusal changed state")
	}
}
