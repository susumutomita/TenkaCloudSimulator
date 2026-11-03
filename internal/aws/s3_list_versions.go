package aws

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (g *Gateway) listVersions(w http.ResponseWriter, r *http.Request, bucket string) {
	if r.Method != "GET" {
		fail(w, r, "MethodNotAllowed", "Use GET to list versions.")
		return
	}
	if g.S3 == nil {
		fail(w, r, "InternalError", "S3 version state is unavailable.")
		return
	}
	q := r.URL.Query()
	prefix, delimiter, marker, versionMarker := q.Get("prefix"), q.Get("delimiter"), q.Get("key-marker"), q.Get("version-id-marker")
	if !validText(prefix) || !validText(delimiter) || !validText(marker) || len(versionMarker) > 128 || !validText(versionMarker) {
		fail(w, r, "InvalidArgument", "Invalid version-list selection.")
		return
	}
	if q.Has("version-id-marker") && marker == "" {
		fail(w, r, "InvalidArgument", "version-id-marker requires key-marker.")
		return
	}
	encoding := q.Get("encoding-type")
	if encoding != "" && encoding != "url" {
		fail(w, r, "InvalidArgument", "Only URL encoding is supported.")
		return
	}
	limit := 1000
	var err error
	if q.Has("max-keys") {
		limit, err = strconv.Atoi(q.Get("max-keys"))
		if err != nil || limit < 0 || limit > 1000 {
			fail(w, r, "InvalidArgument", "max-keys must be between 0 and 1000.")
			return
		}
	}
	versions, err := g.S3.Versions(bucket)
	if err != nil {
		fromError(w, r, err)
		return
	}
	// A version marker is an actual version cursor, never a guessed lexical ID:
	// version IDs are opaque and order is newest first within each key.
	afterSequence := int64(0)
	if versionMarker != "" {
		for _, v := range versions {
			if v.Key == marker && v.ID == versionMarker {
				afterSequence = v.Sequence
				break
			}
		}
		if afterSequence == 0 {
			fail(w, r, "InvalidArgument", "The version cursor does not identify an existing version.")
			return
		}
	}
	type item struct {
		XMLName      xml.Name
		Key          string
		VersionID    string `xml:"VersionId"`
		IsLatest     bool
		LastModified string
		ETag         string `xml:",omitempty"`
		Size         *int   `xml:",omitempty"`
		StorageClass string `xml:",omitempty"`
	}
	type entry struct {
		key, id string
		v       *S3Version
	}
	entries := []entry{}
	seen := map[string]bool{}
	for i := range versions {
		v := &versions[i]
		if !strings.HasPrefix(v.Key, prefix) || v.Key < marker || (v.Key == marker && (versionMarker == "" || v.Sequence >= afterSequence)) {
			continue
		}
		if delimiter != "" {
			if n := strings.Index(strings.TrimPrefix(v.Key, prefix), delimiter); n >= 0 {
				p := v.Key[:len(prefix)+n+len(delimiter)]
				if p > marker && !seen[p] {
					entries = append(entries, entry{key: p})
					seen[p] = true
				}
				continue
			}
		}
		entries = append(entries, entry{key: v.Key, id: v.ID, v: v})
	}
	result := struct {
		XMLName             xml.Name `xml:"ListVersionsResult"`
		XMLNS               string   `xml:"xmlns,attr"`
		Name                string
		Prefix              string
		KeyMarker           string
		VersionIDMarker     string `xml:"VersionIdMarker"`
		NextKeyMarker       string `xml:",omitempty"`
		NextVersionIDMarker string `xml:"NextVersionIdMarker,omitempty"`
		MaxKeys             int
		IsTruncated         bool
		Delimiter           string `xml:",omitempty"`
		EncodingType        string `xml:",omitempty"`
		Items               []item
		CommonPrefixes      []commonPrefix
	}{XMLNS: ns, Name: bucket, Prefix: prefix, KeyMarker: marker, VersionIDMarker: versionMarker, MaxKeys: limit, Delimiter: delimiter, EncodingType: encoding}
	if len(entries) > limit && limit > 0 {
		result.IsTruncated = true
		entries = entries[:limit]
		last := entries[len(entries)-1]
		result.NextKeyMarker = last.key
		result.NextVersionIDMarker = last.id
	} else if limit == 0 {
		entries = nil
	}
	encode := func(v string) string {
		if encoding == "url" {
			return strings.ReplaceAll(url.QueryEscape(v), "+", "%20")
		}
		return v
	}
	result.Prefix = encode(result.Prefix)
	result.KeyMarker = encode(result.KeyMarker)
	result.NextKeyMarker = encode(result.NextKeyMarker)
	result.Delimiter = encode(result.Delimiter)
	for _, e := range entries {
		if e.v == nil {
			result.CommonPrefixes = append(result.CommonPrefixes, commonPrefix{encode(e.key)})
			continue
		}
		v := e.v
		value := item{XMLName: xml.Name{Local: "Version"}, Key: encode(v.Key), VersionID: v.ID, IsLatest: v.Latest, LastModified: v.Modified.Format(time.RFC3339Nano)}
		if v.Marker {
			value.XMLName.Local = "DeleteMarker"
		} else {
			value.ETag = v.ETag
			value.Size = &v.Size
			value.StorageClass = "STANDARD"
		}
		result.Items = append(result.Items, value)
	}
	writeXML(w, 200, result)
}
