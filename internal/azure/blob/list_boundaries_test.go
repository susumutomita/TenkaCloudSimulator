package blob

import (
	"encoding/base64"
	"encoding/xml"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func listNames(t *testing.T, s *Service, path string) ([]string, string) {
	t.Helper()
	w := boundedWire(s, "GET", path, nil, nil)
	if w.Code != 200 {
		t.Fatal("listing failed", w.Code, w.Body.String())
	}
	var document struct {
		XMLName    xml.Name                `xml:"EnumerationResults"`
		Blobs      []struct{ Name string } `xml:"Blobs>Blob"`
		Containers []struct{ Name string } `xml:"Containers>Container"`
		Next       string                  `xml:"NextMarker"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, b := range document.Blobs {
		names = append(names, b.Name)
	}
	for _, c := range document.Containers {
		names = append(names, c.Name)
	}
	return names, document.Next
}
func TestAzureBlobListMarkersPrefixOptionsAndCrossScopeAreFramed(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	for _, name := range []string{"alpha", "bravo", "charlie", "peer"} {
		create(t, s, name)
	}
	for _, key := range []string{"p/a", "p/b", "p/c", "q/peer"} {
		put(t, s, "alpha", key, "value:"+key, map[string]string{"x-ms-meta-label": "<&kept>"}, 201)
	}
	before := fullBlobSnapshot(t, s)
	names, marker := listNames(t, s, "/accountone/alpha?restype=container&comp=list&prefix=p/&maxresults=1&include=metadata")
	if !reflect.DeepEqual(names, []string{"p/a"}) || marker == "" {
		t.Fatal("first page not exact")
	}
	names, next := listNames(t, s, "/accountone/alpha?restype=container&comp=list&prefix=p/&maxresults=2&marker="+url.QueryEscape(marker))
	if !reflect.DeepEqual(names, []string{"p/b", "p/c"}) || next != "" {
		t.Fatal("valid marker options change lost/duplicated key")
	}
	meta := boundedWire(s, "GET", "/accountone/alpha?restype=container&comp=list&prefix=p/&include=metadata", nil, nil)
	if !strings.Contains(meta.Body.String(), "&lt;&amp;kept&gt;") {
		t.Fatal("metadata XML escaping absent")
	}
	plain := boundedWire(s, "GET", "/accountone/alpha?restype=container&comp=list&prefix=p/", nil, nil)
	if strings.Contains(plain.Body.String(), "<Metadata>") {
		t.Fatal("metadata default selection not honored")
	}
	containers, cmarker := listNames(t, s, "/accountone?comp=list&maxresults=1")
	if !reflect.DeepEqual(containers, []string{"alpha"}) || cmarker == "" {
		t.Fatal("container continuation absent")
	}
	remaining, _ := listNames(t, s, "/accountone?comp=list&maxresults=2000&marker="+url.QueryEscape(cmarker))
	if !reflect.DeepEqual(remaining, []string{"bravo", "charlie", "peer"}) {
		t.Fatal("container continuation order")
	}
	badMarkers := []string{marker + "x", "nodot", "%bad.signature", base64.RawURLEncoding.EncodeToString([]byte("{}")) + ".%bad", strings.Repeat("x", 16001)}
	for _, bad := range badMarkers {
		assertNativeError(t, boundedWire(s, "GET", "/accountone/alpha?restype=container&comp=list&prefix=p/&marker="+url.QueryEscape(bad), nil, nil), 400, "InvalidQueryParameterValue")
	}
	for _, path := range []string{"/accountone/alpha?restype=container&comp=list&prefix=q/&marker=", "/accountone/bravo?restype=container&comp=list&prefix=p/&marker=", "/accountone?comp=list&prefix=p/&marker="} {
		assertNativeError(t, boundedWire(s, "GET", path+url.QueryEscape(marker), nil, nil), 400, "InvalidQueryParameterValue")
	}
	_, other := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, other, "alpha")
	otherBefore := fullBlobSnapshot(t, other)
	assertNativeError(t, boundedWire(other, "GET", "/accountone/alpha?restype=container&comp=list&prefix=p/&marker="+url.QueryEscape(marker), nil, nil), 400, "InvalidQueryParameterValue")
	if fullBlobSnapshot(t, other) != otherBefore {
		t.Fatal("foreign token changed peer database")
	}
	assertNativeError(t, boundedWire(s, "GET", "/accountone/missing?restype=container&comp=list", nil, nil), 404, "ContainerNotFound")
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("listing/tamper changed state")
	}
}
func TestAzureBlobPersistedListingCorruptionDoesNotPublishPartialSuccess(t *testing.T) {
	cases := []struct{ name, table, field, value string }{
		{"blob-tag", "azure_blob_objects", "etag", "bad"}, {"blob-time", "azure_blob_objects", "modified", "bad"}, {"blob-type", "azure_blob_objects", "content_type", "\t"}, {"blob-md5", "azure_blob_objects", "md5", "not-base64"}, {"blob-meta", "azure_blob_objects", "metadata", "{broken"}, {"blob-noncanonical-meta", "azure_blob_objects", "metadata", "{\"Z\":\"value\"}"},
		{"container-tag", "azure_blob_containers", "etag", "bad"}, {"container-time", "azure_blob_containers", "modified", "bad"}, {"container-meta", "azure_blob_containers", "metadata", "null"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
			create(t, s, "box")
			create(t, s, "peer")
			put(t, s, "box", "healthy", "saved", nil, 201)
			put(t, s, "peer", "untouched", "private-peer-bytes", nil, 201)
			where := "container='box'"
			if c.table == "azure_blob_containers" {
				where = "name='box'"
			}
			if _, err := s.db.Exec("UPDATE "+c.table+" SET "+c.field+"=? WHERE "+where, c.value); err != nil {
				t.Fatal(err)
			}
			before := fullBlobSnapshot(t, s)
			paths := []string{"/accountone/box/healthy", "/accountone/box?restype=container&comp=list"}
			if c.table == "azure_blob_containers" {
				paths = append(paths, "/accountone?comp=list")
			}
			for _, path := range paths {
				w := boundedWire(s, "GET", path, nil, nil)
				assertNativeError(t, w, 500, "InternalError")
				if strings.Contains(w.Body.String(), "EnumerationResults") || strings.Contains(w.Body.String(), "healthy") {
					t.Fatal("corrupt listing emitted partial success")
				}
			}
			if fullBlobSnapshot(t, s) != before {
				t.Fatal("corruption error silently repaired/deleted persisted data")
			}
			peer := boundedWire(s, "GET", "/accountone/peer/untouched", nil, nil)
			if peer.Code != 200 || peer.Body.String() != "private-peer-bytes" {
				t.Fatal("corrupt neighbor broke peer")
			}
		})
	}
}
func TestAzureBlobListPrefixExactSetAndBodyIntegrityFault(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	keys := []string{"a%", "a_", "a/", "a/next", "ab", "other"}
	for _, k := range keys {
		put(t, s, "box", k, "body:"+k, nil, 201)
	}
	names, _ := listNames(t, s, "/accountone/box?restype=container&comp=list&prefix=a%25")
	if !reflect.DeepEqual(names, []string{"a%"}) {
		t.Fatal("prefix wildcard expansion")
	}
	names, _ = listNames(t, s, "/accountone/box?restype=container&comp=list&prefix=a_")
	if !reflect.DeepEqual(names, []string{"a_"}) {
		t.Fatal("underscore expanded")
	}
	names, _ = listNames(t, s, "/accountone/box?restype=container&comp=list&prefix=a/")
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"a/", "a/next"}) {
		t.Fatal("slash prefix not literal")
	}
	if _, err := s.db.Exec("UPDATE azure_blob_objects SET body=? WHERE name='ab'", []byte("tampered")); err != nil {
		t.Fatal(err)
	}
	before := fullBlobSnapshot(t, s)
	assertNativeError(t, boundedWire(s, "GET", "/accountone/box/ab", nil, nil), 500, "InternalError")
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("integrity refusal changed stored state")
	}
	// The constructor does not promise body hash scrub; this check is at actual GET.
}

func TestAzureBlobOversizeEscapedListingFailsBeforeSuccessAndCanBePaged(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	create(t, s, "peer")
	put(t, s, "peer", "saved", "private-peer-bytes", nil, 201)
	put(t, s, "box", "prototype", "", map[string]string{"x-ms-meta-quotes": strings.Repeat("\"", 3500)}, 201)
	// Every copied fixture row has valid native-produced metadata/time/MD5 fields;
	// JSON is below8KiB per row but XML quote escaping makes a1000-row page >16MiB.
	execSQL(t, s, "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO azure_blob_objects SELECT environment,account,container,printf('large%04d',x),body,etag,modified,metadata,content_type,md5 FROM azure_blob_objects,n WHERE name='prototype'")
	before := fullBlobSnapshot(t, s)
	w := boundedWire(s, "GET", "/accountone/box?restype=container&comp=list&include=metadata", nil, nil)
	assertNativeError(t, w, 413, "SimulatorResponseTooLarge")
	if strings.Contains(w.Body.String(), "EnumerationResults") {
		t.Fatal("listing limit published partial successful XML")
	}
	names, marker := listNames(t, s, "/accountone/box?restype=container&comp=list&include=metadata&maxresults=1")
	if !reflect.DeepEqual(names, []string{"large0001"}) || marker == "" {
		t.Fatal("smaller page did not recover")
	}
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("response bound changed persisted graph")
	}
}
