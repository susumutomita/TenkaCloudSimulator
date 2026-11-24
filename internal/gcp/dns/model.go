package dns

import "strings"

// Native bindings independently transcribed from dns.v1 discovery shipped in
// google-api-python-client 2.199.0, revision 20260714, original SHA256
// 98e5e8539715e33c0c4fbd6bc44d6b76b3004196839f8610feba7d33783066c4.
// No generated model/extractor or other service catalog is required by this leaf.
// Unsupported methods (including direct RRset writes) receive explicit 501.
type binding struct{ verb, path, id string }

var bindings = []binding{
	{"POST", "dns/v1/projects/{project}/managedZones", "managedZones.create"},
	{"GET", "dns/v1/projects/{project}/managedZones", "managedZones.list"},
	{"GET", "dns/v1/projects/{project}/managedZones/{zone}", "managedZones.get"},
	{"DELETE", "dns/v1/projects/{project}/managedZones/{zone}", "managedZones.delete"},
	{"POST", "dns/v1/projects/{project}/managedZones/{zone}/changes", "changes.create"},
	{"GET", "dns/v1/projects/{project}/managedZones/{zone}/changes", "changes.list"},
	{"GET", "dns/v1/projects/{project}/managedZones/{zone}/changes/{change}", "changes.get"},
	{"GET", "dns/v1/projects/{project}/managedZones/{zone}/rrsets", "resourceRecordSets.list"},
	{"GET", "dns/v1/projects/{project}/managedZones/{zone}/rrsets/{name}/{type}", "resourceRecordSets.get"},
}

func resolve(verb string, parts []string) (string, map[string]string) {
	for _, b := range bindings {
		shape := strings.Split(b.path, "/")
		if b.verb != verb || len(parts) != len(shape) {
			continue
		}
		values := map[string]string{}
		ok := true
		for i, s := range shape {
			if strings.HasPrefix(s, "{") {
				values[s[1:len(s)-1]] = parts[i]
			} else if s != parts[i] {
				ok = false
				break
			}
		}
		if ok {
			return b.id, values
		}
	}
	return "", nil
}
