package dns

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

const (
	maxZones   = 32
	maxRecords = 256
	maxChanges = 128
	maxBatch   = 64
)

// Local validation is intentionally a subset: lowercase absolute ASCII domains,
// A/AAAA/CNAME/NS/PTR/MX/SRV and one plain quoted TXT string per rdata. Wildcards,
// DNSSEC signatures, routing policies and other types are explicitly refused.
type record struct {
	Kind string   `json:"kind,omitempty"`
	Name string   `json:"name"`
	Type string   `json:"type"`
	TTL  *int64   `json:"ttl"`
	Data []string `json:"rrdatas"`
}

func seconds(n int64) *int64 { return &n }
func validDomain(s string) bool {
	if len(s) < 2 || len(s) > 253 || !strings.HasSuffix(s, ".") {
		return false
	}
	for _, label := range strings.Split(s[:len(s)-1], ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}
func knownType(s string) bool {
	switch s {
	case "A", "AAAA", "CNAME", "NS", "SOA", "PTR", "MX", "SRV", "TXT":
		return true
	}
	return false
}

// MX and SRV use canonical decimal 16-bit unsigned fields in this local subset.
func canonicalUint16(s string) bool {
	n, err := strconv.ParseUint(s, 10, 16)
	return err == nil && strconv.FormatUint(n, 10) == s
}
func (r record) key() string { return r.Name + "/" + r.Type }
func (r *record) validate() error {
	if !validDomain(r.Name) || r.TTL == nil || *r.TTL < 0 || *r.TTL > 2147483647 || len(r.Data) == 0 || len(r.Data) > 32 {
		return bad("RRsets require an absolute lowercase name, ttl 0..2147483647 and 1..32 rrdatas.")
	}
	if r.Kind != "" && r.Kind != "dns#resourceRecordSet" {
		return bad("Invalid RRset kind.")
	}
	if r.Type == "CNAME" && len(r.Data) != 1 {
		return bad("A CNAME requires one target.")
	}
	seen := map[string]bool{}
	for _, s := range r.Data {
		if len(s) == 0 || len(s) > 1024 || seen[s] {
			return bad("Empty, duplicate or oversized rdata.")
		}
		seen[s] = true
		good := false
		switch r.Type {
		case "A", "AAAA":
			ip, err := netip.ParseAddr(s)
			good = err == nil && ip.Zone() == "" && (r.Type == "A" && ip.Is4() || r.Type == "AAAA" && ip.Is6() && !ip.Is4In6())
		case "CNAME", "NS", "PTR":
			good = validDomain(s)
		case "MX":
			parts := strings.Split(s, " ")
			good = len(parts) == 2 && canonicalUint16(parts[0]) && validDomain(parts[1])
		case "SRV":
			// RFC 2782: priority weight port target. Store the service advertisement;
			// do not resolve its target or interpret its ordering/weight.
			parts := strings.Split(s, " ")
			good = len(parts) == 4 && canonicalUint16(parts[0]) && canonicalUint16(parts[1]) &&
				canonicalUint16(parts[2]) && (parts[3] == "." || validDomain(parts[3]))
		case "TXT":
			if len(s) >= 2 && len(s) <= 257 && s[0] == '"' && s[len(s)-1] == '"' {
				good = true
				for _, c := range s[1 : len(s)-1] {
					if c < 32 || c > 126 || c == '"' || c == '\\' {
						good = false
					}
				}
			}
		default:
			return bad("Unsupported mutable RRset type.")
		}
		if !good {
			return bad("Unsupported or malformed " + r.Type + " rdata.")
		}
	}
	r.Kind = "dns#resourceRecordSet"
	sort.Strings(r.Data)
	return nil
}
func sameRecord(a, b record) bool {
	return a.Name == b.Name && a.Type == b.Type && a.TTL != nil && b.TTL != nil && *a.TTL == *b.TTL && reflect.DeepEqual(a.Data, b.Data)
}
func initialRecords(domain string) []record {
	return []record{
		{"dns#resourceRecordSet", domain, "NS", seconds(21600), []string{"ns1.tenkacloud.invalid."}},
		{"dns#resourceRecordSet", domain, "SOA", seconds(21600), []string{"ns1.tenkacloud.invalid. hostmaster.tenkacloud.invalid. 1 21600 3600 259200 300"}},
	}
}

type zoneInput struct {
	Name        string  `json:"name"`
	DNSName     string  `json:"dnsName"`
	Description *string `json:"description"`
	Visibility  string  `json:"visibility,omitempty"`
	Kind        string  `json:"kind,omitempty"`
}
type changeInput struct {
	Add    []record `json:"additions"`
	Delete []record `json:"deletions"`
	Kind   string   `json:"kind,omitempty"`
}

func decodeInput(c call) (any, error) {
	switch c.id {
	case "managedZones.create":
		var z zoneInput
		if err := jsonObject(c.data, &z); err != nil {
			return nil, err
		}
		if !namePattern.MatchString(z.Name) || !validDomain(z.DNSName) || z.Description == nil || len(*z.Description) > 1024 {
			return nil, bad("Zone requires name, absolute lowercase dnsName and description of at most 1024 bytes.")
		}
		if z.Visibility != "" && z.Visibility != "public" {
			return nil, bad("Only public metadata zones are supported; there is no DNS serving.")
		}
		if z.Kind != "" && z.Kind != "dns#managedZone" {
			return nil, bad("Invalid zone kind.")
		}
		return z, nil
	case "changes.create":
		var change changeInput
		if err := jsonObject(c.data, &change); err != nil {
			return nil, err
		}
		if len(change.Add)+len(change.Delete) == 0 || len(change.Add)+len(change.Delete) > maxBatch {
			return nil, bad("Change requires 1..64 total additions/deletions.")
		}
		if change.Kind != "" && change.Kind != "dns#change" {
			return nil, bad("Invalid change kind.")
		}
		for _, list := range [][]record{change.Add, change.Delete} {
			seen := map[string]bool{}
			for i := range list {
				r := &list[i]
				if err := r.validate(); err != nil {
					return nil, err
				}
				if seen[r.key()] {
					return nil, bad("A change cannot repeat an RRset within additions or deletions.")
				}
				seen[r.key()] = true
			}
		}
		return change, nil
	}
	return nil, nil
}
func encode(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }

// dnsOrder compares labels from the root towards the leaf, then record type.
func dnsOrder(r record) string {
	labels := strings.Split(strings.TrimSuffix(r.Name, "."), ".")
	for l, h := 0, len(labels)-1; l < h; l, h = l+1, h-1 {
		labels[l], labels[h] = labels[h], labels[l]
	}
	return strings.Join(labels, ".") + "/" + r.Type
}
