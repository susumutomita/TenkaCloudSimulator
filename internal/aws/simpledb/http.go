package simpledb

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Scope must come from trusted server configuration, never request headers.
type Scope struct{ Account, Region string }

// Authorize evaluates the authenticated local principal against an IAM action
// and resource. It must not treat dummy AWS signing fields as authentication.
type Authorize func(context.Context, string, string) bool

// NewHTTPHandler implements only the five bounded Query operations. The caller
// retains authentication, dispatch, region validation and database ownership.
func NewHTTPHandler(s *Store, sc Scope, authorize Authorize) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail := func(status int, code string) {
			writeQuery(w, status, queryResponse{XMLName: xml.Name{Local: "Response"}, Errors: &queryErrors{queryError{code, code}}})
		}
		if s == nil || authorize == nil || scope(sc.Account, sc.Region) != nil {
			fail(403, "AccessDenied")
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" || err != nil || media != "application/x-www-form-urlencoded" {
			fail(400, "InvalidParameterValue")
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			fail(400, "InvalidParameterValue")
			return
		}
		p, err := url.ParseQuery(string(raw))
		if err != nil {
			fail(400, "InvalidParameterValue")
			return
		}
		for _, vs := range p {
			if len(vs) != 1 {
				fail(400, "InvalidParameterValue")
				return
			}
		}
		action := p.Get("Action")
		if action == "" || p.Get("Version") == "" {
			fail(400, "MissingParameter")
			return
		}
		if p.Get("Version") != "2009-04-15" {
			fail(400, "InvalidParameterValue")
			return
		}
		allowed := map[string]bool{"Action": true, "Version": true}
		// Signing fields are inert wire metadata, not an authentication mechanism.
		for _, k := range []string{"AWSAccessKeyId", "Signature", "SignatureMethod", "SignatureVersion", "Timestamp"} {
			allowed[k] = true
		}
		switch action {
		case "CreateDomain", "DeleteDomain":
			allowed["DomainName"] = true
		case "ListDomains":
			allowed["MaxNumberOfDomains"] = true
			allowed["NextToken"] = true
		case "PutAttributes":
			allowed["DomainName"] = true
			allowed["ItemName"] = true
		case "GetAttributes":
			allowed["DomainName"] = true
			allowed["ItemName"] = true
			allowed["ConsistentRead"] = true
		default:
			fail(400, "InvalidAction")
			return
		}
		indices := map[string]bool{}
		for key := range p {
			if allowed[key] {
				continue
			}
			parts := strings.Split(key, ".")
			valid := false
			if action == "PutAttributes" && len(parts) == 3 && parts[0] == "Attribute" && (parts[2] == "Name" || parts[2] == "Value" || parts[2] == "Replace") {
				valid = true
			}
			if action == "GetAttributes" && len(parts) == 2 && parts[0] == "AttributeName" {
				valid = true
			}
			if !valid {
				fail(400, "InvalidParameterValue")
				return
			}
			n := parts[1]
			if !queryIndex(n) {
				fail(400, "InvalidParameterValue")
				return
			}
			indices[n] = true
			if len(indices) > 256 {
				fail(400, "InvalidParameterValue")
				return
			}
		}
		domain := p.Get("DomainName")
		if action != "ListDomains" {
			if _, ok := p["DomainName"]; !ok {
				fail(400, "MissingParameter")
				return
			}
			if err := domainScope(sc.Account, sc.Region, domain); err != nil {
				fail(400, "InvalidParameterValue")
				return
			}
		}
		item := p.Get("ItemName")
		if action == "PutAttributes" || action == "GetAttributes" {
			if _, ok := p["ItemName"]; !ok {
				fail(400, "MissingParameter")
				return
			}
			if !validText(item, false) {
				fail(400, "InvalidParameterValue")
				return
			}
		}
		values := map[string][]string{}
		replace := map[string]bool{}
		names := []string{}
		// Work is bounded by supplied entries, never the greatest numeric index.
		ordered := make([]string, 0, len(indices))
		for n := range indices {
			ordered = append(ordered, n)
		}
		sort.Slice(ordered, func(i, j int) bool {
			if len(ordered[i]) != len(ordered[j]) {
				return len(ordered[i]) < len(ordered[j])
			}
			return ordered[i] < ordered[j]
		})
		for _, n := range ordered {
			if action == "PutAttributes" {
				prefix := "Attribute." + n + "."
				name, nok := p[prefix+"Name"]
				value, vok := p[prefix+"Value"]
				if !nok || !vok {
					fail(400, "MissingParameter")
					return
				}
				if !validText(name[0], false) || !validText(value[0], true) {
					fail(400, "InvalidParameterValue")
					return
				}
				rep := false
				if v, ok := p[prefix+"Replace"]; ok {
					if v[0] != "true" && v[0] != "false" {
						fail(400, "InvalidParameterValue")
						return
					}
					rep = v[0] == "true"
				}
				if old, ok := replace[name[0]]; ok && old != rep {
					fail(400, "InvalidParameterValue")
					return
				}
				values[name[0]] = append(values[name[0]], value[0])
				replace[name[0]] = rep
			} else {
				name := p.Get("AttributeName." + n)
				if !validText(name, false) {
					fail(400, "InvalidParameterValue")
					return
				}
				names = append(names, name)
			}
		}
		if action == "PutAttributes" && len(values) == 0 {
			fail(400, "MissingParameter")
			return
		}
		if v, ok := p["ConsistentRead"]; ok && v[0] != "true" && v[0] != "false" {
			fail(400, "InvalidParameterValue")
			return
		}
		limit := 0
		if v, ok := p["MaxNumberOfDomains"]; ok {
			limit, err = strconv.Atoi(v[0])
			if err != nil || limit < 1 || limit > 100 {
				fail(400, "InvalidParameterValue")
				return
			}
		}
		resource := "*"
		if action != "ListDomains" {
			resource = "arn:aws:sdb:" + sc.Region + ":" + sc.Account + ":domain/" + domain
		}
		if !authorize(r.Context(), "sdb:"+action, resource) {
			fail(403, "AccessDenied")
			return
		}
		out := queryResponse{XMLName: xml.Name{Local: action + "Response"}}
		switch action {
		case "CreateDomain":
			err = s.CreateDomain(r.Context(), sc.Account, sc.Region, domain)
		case "DeleteDomain":
			err = s.DeleteDomain(r.Context(), sc.Account, sc.Region, domain)
		case "ListDomains":
			var ds []string
			var next string
			ds, next, err = s.ListDomains(r.Context(), sc.Account, sc.Region, limit, p.Get("NextToken"))
			out.List = &queryList{ds, next}
		case "PutAttributes":
			err = s.Put(r.Context(), sc.Account, sc.Region, domain, item, values, replace)
		case "GetAttributes":
			var attrs map[string][]string
			attrs, err = s.Get(r.Context(), sc.Account, sc.Region, domain, item, names)
			out.Get = &queryGet{}
			keys := make([]string, 0, len(attrs))
			for k := range attrs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				sort.Strings(attrs[k])
				for _, v := range attrs[k] {
					out.Get.Attributes = append(out.Get.Attributes, queryAttribute{k, v})
				}
			}
		}
		if err != nil {
			var coded interface{ Code() string }
			if errors.As(err, &coded) {
				switch coded.Code() {
				case "InvalidParameterValue", "InvalidNextToken", "NoSuchDomain":
					fail(400, coded.Code())
					return
				}
			}
			fail(500, "InternalError")
			return
		}
		writeQuery(w, 200, out)
	})
}

// Canonical decimal labels avoid aliases without imposing machine-int limits.
// Their textual size is bounded by the request body limit.
func queryIndex(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

type queryAttribute struct{ Name, Value string }

func (a queryAttribute) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	type field struct {
		Encoding string `xml:"encoding,attr,omitempty"`
		Text     string `xml:",chardata"`
	}
	encode := func(s string) field {
		for _, c := range s {
			// XML 1.1 RestrictedChar, NUL and Unicode noncharacters.
			if c == 0 || (c >= 1 && c <= 8) || c == 11 || c == 12 || (c >= 14 && c <= 31) ||
				(c >= 0x7f && c <= 0x84) || (c >= 0x86 && c <= 0x9f) ||
				(c >= 0xfdd0 && c <= 0xfdef) || c&0xffff == 0xfffe || c&0xffff == 0xffff {
				return field{"base64", base64.StdEncoding.EncodeToString([]byte(s))}
			}
		}
		return field{Text: s}
	}
	return e.EncodeElement(struct{ Name, Value field }{encode(a.Name), encode(a.Value)}, start)
}

type queryGet struct {
	Attributes []queryAttribute `xml:"Attribute"`
}
type queryList struct {
	Domains []string `xml:"DomainName"`
	Next    string   `xml:"NextToken,omitempty"`
}
type queryError struct{ Code, Message string }
type queryErrors struct{ Error queryError }
type queryResponse struct {
	XMLName   xml.Name
	Namespace string       `xml:"xmlns,attr"`
	List      *queryList   `xml:"ListDomainsResult,omitempty"`
	Get       *queryGet    `xml:"GetAttributesResult,omitempty"`
	Errors    *queryErrors `xml:"Errors,omitempty"`
	Metadata  struct {
		ID string `xml:"RequestId"`
	} `xml:"ResponseMetadata"`
}

func writeQuery(w http.ResponseWriter, status int, out queryResponse) {
	out.Namespace = "http://sdb.amazonaws.com/doc/2009-04-15/"
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		http.Error(w, "InternalError", 500)
		return
	}
	out.Metadata.ID = hex.EncodeToString(id[:])
	b, err := xml.Marshal(out)
	if err != nil {
		http.Error(w, "InternalError", 500)
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append([]byte(xml.Header), b...))
}
