package blob

import (
	"net/http"
	"strings"
	"testing"
)

func TestAzureBlobRangeRejectsMissingOrInconsistentStoredChecksum(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	const original = "secret-suffix"
	for _, name := range []string{"missing-checksum", "damaged-tail", "healthy"} {
		put(t, s, "box", name, original, nil, http.StatusCreated)
	}
	execSQL(t, s, "UPDATE azure_blob_objects SET md5='' WHERE name='missing-checksum'")
	// Same length and requested prefix; only the bytes outside the range changed.
	execSQL(t, s, "UPDATE azure_blob_objects SET body=? WHERE name='damaged-tail'", []byte("secret-change"))
	before := fullBlobSnapshot(t, s)
	for _, name := range []string{"missing-checksum", "damaged-tail"} {
		t.Run(name, func(t *testing.T) {
			w := boundedWire(s, http.MethodGet, "/accountone/box/"+name, nil,
				http.Header{"X-Ms-Range": {"bytes=0-5"}, "X-Ms-Range-Get-Content-Md5": {"true"}})
			assertNativeError(t, w, http.StatusInternalServerError, "InternalError")
			for _, h := range []string{"ETag", "Content-Range", "Content-MD5", "x-ms-blob-content-md5"} {
				if w.Result().Header.Get(h) != "" {
					t.Fatalf("storage integrity refusal published %s", h)
				}
			}
			if strings.Contains(w.Body.String(), "secret") {
				t.Fatal("storage integrity refusal returned object bytes")
			}
		})
	}
	w := boundedWire(s, http.MethodGet, "/accountone/box/healthy", nil,
		http.Header{"X-Ms-Range": {"bytes=0-5"}, "X-Ms-Range-Get-Content-Md5": {"true"}})
	if w.Code != http.StatusPartialContent || w.Body.String() != "secret" ||
		w.Result().Header.Get("Content-MD5") == "" || w.Result().Header.Get("x-ms-blob-content-md5") == "" {
		t.Fatal("healthy range incorrectly rejected")
	}
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("range reads modified persisted blob state")
	}
}
