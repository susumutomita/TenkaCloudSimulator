package blob

import (
	"encoding/xml"
	"net/http"
	"sort"
	"strconv"
)

// Typed wire structures keep the fixed listing schema in one place. Metadata
// names come from already validated native metadata and are sorted explicitly.
// Pointer collections preserve empty <Containers>/<Blobs> instead of omitting
// the selected collection. No XML declaration was present in the original wire.
type listingDocument struct {
	XMLName    xml.Name             `xml:"EnumerationResults"`
	Endpoint   string               `xml:"ServiceEndpoint,attr"`
	Container  string               `xml:"ContainerName,attr,omitempty"`
	Prefix     string               `xml:"Prefix"`
	Limit      int                  `xml:"MaxResults"`
	Containers *containerCollection `xml:"Containers,omitempty"`
	Blobs      *blobCollection      `xml:"Blobs,omitempty"`
	Next       string               `xml:"NextMarker"`
}
type containerCollection struct {
	Items []containerListingItem `xml:"Container"`
}
type blobCollection struct {
	Items []blobListingItem `xml:"Blob"`
}
type containerListingItem struct {
	Name       string                     `xml:"Name"`
	Properties containerListingProperties `xml:"Properties"`
	Metadata   *listingMetadata           `xml:"Metadata,omitempty"`
}
type blobListingItem struct {
	Name       string                `xml:"Name"`
	Properties blobListingProperties `xml:"Properties"`
	Metadata   *listingMetadata      `xml:"Metadata,omitempty"`
}
type containerListingProperties struct {
	Modified string `xml:"Last-Modified"`
	ETag     string `xml:"Etag"`
}
type blobListingProperties struct {
	Modified    string `xml:"Last-Modified"`
	ETag        string `xml:"Etag"`
	Length      int64  `xml:"Content-Length"`
	ContentType string `xml:"Content-Type"`
	MD5         string `xml:"Content-MD5"`
	Type        string `xml:"BlobType"`
}
type listingMetadata struct {
	Values []listingMetadataElement `xml:",any"`
}
type listingMetadataElement struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

func metadataDocument(e entity, include bool) *listingMetadata {
	if !include {
		return nil
	}
	result := &listingMetadata{Values: []listingMetadataElement{}}
	keys := make([]string, 0, len(e.Metadata))
	for k := range e.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		result.Values = append(result.Values, listingMetadataElement{XMLName: xml.Name{Local: k}, Value: e.Metadata[k]})
	}
	return result
}

// List XML is fully serialized and bounded before publishing any successful
// HTTP headers/body. One checked marshal replaces duplicated per-token error
// returns over a concrete memory buffer, while preserving error propagation.
func writeListing(w http.ResponseWriter, r *http.Request, account, container string, o listOptions, p page) error {
	document := listingDocument{Endpoint: "/" + account + "/", Container: container, Prefix: o.Prefix, Limit: o.Limit, Next: p.Next}
	if container == "" {
		document.Containers = &containerCollection{Items: []containerListingItem{}}
		for _, e := range p.Items {
			document.Containers.Items = append(document.Containers.Items, containerListingItem{Name: e.Name, Properties: containerListingProperties{Modified: e.Modified, ETag: e.ETag}, Metadata: metadataDocument(e, o.Metadata)})
		}
	} else {
		document.Blobs = &blobCollection{Items: []blobListingItem{}}
		for _, e := range p.Items {
			document.Blobs.Items = append(document.Blobs.Items, blobListingItem{Name: e.Name, Properties: blobListingProperties{Modified: e.Modified, ETag: e.ETag, Length: e.Length, ContentType: e.ContentType, MD5: e.MD5, Type: "BlockBlob"}, Metadata: metadataDocument(e, o.Metadata)})
		}
	}
	body, err := xml.Marshal(document)
	if err != nil {
		return err
	}
	if len(body) > 16<<20 {
		return fail(413, "SimulatorResponseTooLarge", "Use a smaller maxresults value.")
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(200)
	_, _ = w.Write(body)
	return nil
}
