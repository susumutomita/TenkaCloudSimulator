package blob

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestAzureBlobNativeListingExactWireFieldsAndEscaping(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	container := boundedWire(s, "PUT", "/accountone/box?restype=container", nil, mapHeader("X-Ms-Meta-Z", "last", "X-Ms-Meta-A", "<&\"'"))
	if container.Code != 201 {
		t.Fatal(container.Body.String())
	}
	create(t, s, "peer")
	value := "<&\"'雪>"
	created := put(t, s, "box", "pref/saved", value, map[string]string{"x-ms-meta-z": "last", "x-ms-meta-a": "<&\"'"}, 201)
	before := fullBlobSnapshot(t, s)
	w := boundedWire(s, "GET", "/accountone/box?restype=container&comp=list&prefix=pref/&include=metadata&maxresults=1", nil, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/xml" || w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) || strings.HasPrefix(w.Body.String(), "<?xml") {
		t.Fatal("native listing envelope changed", w.Code, w.Header())
	}
	type itemMetadata struct {
		Fields []struct {
			XMLName xml.Name
			Value   string `xml:",chardata"`
		} `xml:",any"`
	}
	type properties struct {
		Modified    string `xml:"Last-Modified"`
		ETag        string `xml:"Etag"`
		Length      int64  `xml:"Content-Length"`
		ContentType string `xml:"Content-Type"`
		MD5         string `xml:"Content-MD5"`
		Type        string `xml:"BlobType"`
	}
	var doc struct {
		XMLName   xml.Name `xml:"EnumerationResults"`
		Endpoint  string   `xml:"ServiceEndpoint,attr"`
		Container string   `xml:"ContainerName,attr"`
		Prefix    string
		Limit     int    `xml:"MaxResults"`
		Next      string `xml:"NextMarker"`
		Items     []struct {
			Name       string
			Properties properties
			Metadata   itemMetadata
		} `xml:"Blobs>Blob"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Endpoint != "/accountone/" || doc.Container != "box" || doc.Prefix != "pref/" || doc.Limit != 1 || doc.Next != "" || len(doc.Items) != 1 {
		t.Fatal("wrong listing attrs/pagination fields", doc)
	}
	item := doc.Items[0]
	sum := md5.Sum([]byte(value))
	if item.Name != "pref/saved" || item.Properties.ETag != created.Header().Get("ETag") || item.Properties.Modified != created.Header().Get("Last-Modified") || item.Properties.Length != int64(len(value)) || item.Properties.ContentType != "text/plain" || item.Properties.MD5 != base64.StdEncoding.EncodeToString(sum[:]) || item.Properties.Type != "BlockBlob" {
		t.Fatal("wrong native blob listing properties", item.Properties)
	}
	if len(item.Metadata.Fields) != 2 || item.Metadata.Fields[0].XMLName.Local != "a" || item.Metadata.Fields[0].Value != "<&\"'" || item.Metadata.Fields[1].XMLName.Local != "z" || item.Metadata.Fields[1].Value != "last" {
		t.Fatal("metadata order/escaping changed", item.Metadata)
	}
	c := boundedWire(s, "GET", "/accountone?comp=list&prefix=box&include=metadata", nil, nil)
	var containers struct {
		Container string `xml:"ContainerName,attr"`
		Items     []struct {
			Name       string
			Properties properties
			Metadata   itemMetadata
		} `xml:"Containers>Container"`
	}
	if c.Code != 200 {
		t.Fatal(c.Body.String())
	}
	if err := xml.Unmarshal(c.Body.Bytes(), &containers); err != nil {
		t.Fatal(err)
	}
	if containers.Container != "" || strings.Contains(c.Body.String(), "ContainerName=") || len(containers.Items) != 1 || containers.Items[0].Name != "box" || containers.Items[0].Properties.ETag != container.Header().Get("ETag") || containers.Items[0].Properties.Modified != container.Header().Get("Last-Modified") {
		t.Fatal("container listing schema changed", containers)
	}
	for _, check := range []struct{ path, collection string }{{"/accountone?comp=list&prefix=nonexistent", "Containers"}, {"/accountone/box?restype=container&comp=list&prefix=nonexistent", "Blobs"}} {
		empty := boundedWire(s, "GET", check.path, nil, nil)
		if empty.Code != 200 || !strings.Contains(empty.Body.String(), "<"+check.collection+"></"+check.collection+">") {
			t.Fatal("empty selected collection disappeared", empty.Body.String())
		}
	}
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("serialization changed persisted state")
	}
}

func mapHeader(values ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(values); i += 2 {
		h.Set(values[i], values[i+1])
	}
	return h
}
