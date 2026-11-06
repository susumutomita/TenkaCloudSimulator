package catalog

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"strconv"
	"strings"
	"time"
)

// Flat conventions of APIs such as Compute Engine (compute.v1), selected by
// API.SelfLink and the "zonal" scope. Resource paths carry separate
// parameters (projects/{project}/zones/{zone}/disks/{disk}), resources carry
// kind, a numeric id, selfLink and creationTimestamp, and mutations return a
// completed operation resource readable through its scope's operations.

// zones lists the published Compute Engine zones of each supported region.
var zones = map[string][]string{
	"us-central1": {"a", "b", "c", "f"}, "us-east1": {"b", "c", "d"}, "us-west1": {"a", "b", "c"},
	"europe-west1": {"b", "c", "d"}, "asia-northeast1": {"a", "b", "c"},
}

// zoneRegion returns the region of a published zone.
func zoneRegion(zone string) (string, bool) {
	i := strings.LastIndex(zone, "-")
	if i < 0 {
		return "", false
	}
	for _, suffix := range zones[zone[:i]] {
		if zone[i+1:] == suffix {
			return zone[:i], true
		}
	}
	return "", false
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// numericID is a server-assigned unsigned 64-bit identifier in decimal,
// drawn from [2^62, 2^63) so that IDs have 19 digits like Compute Engine IDs.
func numericID() string {
	return strconv.FormatUint(binary.BigEndian.Uint64(randomBytes(8))&^(1<<63)|1<<62, 10)
}

// fingerprint is an opaque optimistic-locking value in base64.
func fingerprint() string { return base64.StdEncoding.EncodeToString(randomBytes(8)) }

// defaults assigns the official default of every output-only field, such
// as the Compute Engine kind "compute#disk".
func defaults(schema *Property, document map[string]any) {
	if schema == nil {
		return
	}
	for key, p := range schema.Properties {
		if p.ReadOnly && p.Default != "" {
			document[key] = p.Default
		}
	}
}

// root is the scope of the request: projects/{project} followed by
// locations/{location}, global, regions/{region} or zones/{zone}.
func (q *request) root() string {
	root := "projects/" + q.e.cfg.Project
	if q.scopePath != "" {
		return root + "/" + q.scopePath
	}
	if q.location != "" {
		// A project-scope collection whose official path names a location
		// lives under that location.
		root += "/locations/" + q.location
	}
	return root
}

func (q *request) link(name string) string { return q.b.api.SelfLink + name }

// segment is the collection segment of the official create or list path.
func (q *request) segment() string {
	segments := splitPath(q.method.FlatPath)
	last, _, _ := strings.Cut(segments[len(segments)-1], ":")
	return last
}

// verb is the final element of the official method ID, such as "insert".
func (q *request) verb() string {
	return q.method.ID[strings.LastIndex(q.method.ID, ".")+1:]
}

// flatFields assigns the standard output fields of a created flat resource.
func (q *request) flatFields(schema *Property, document map[string]any, name string) {
	if q.b.api.SelfLink == "" {
		return
	}
	set := func(field string, value any) {
		if schema.Properties[field] != nil {
			document[field] = value
		}
	}
	set("id", numericID())
	set("selfLink", q.link(name))
	set("creationTimestamp", q.now().Format(time.RFC3339Nano))
	switch {
	case strings.HasPrefix(q.scopePath, "zones/"):
		set("zone", q.link(q.root()))
	case strings.HasPrefix(q.scopePath, "regions/"):
		set("region", q.link(q.root()))
	}
}

// flatOperation reports whether the method returns a flat operation
// resource rather than google.longrunning.Operation.
func (q *request) flatOperation() bool {
	s := q.b.model.Schemas[q.method.Response]
	return q.b.api.SelfLink != "" && s != nil && s.Properties["targetLink"] != nil && s.Properties["status"] != nil
}

// completed stores and returns a finished flat operation for target.
func (q *request) completed(target map[string]any, tx *sql.Tx) (any, error) {
	schema := q.b.model.Schemas[q.method.Response]
	now := q.now()
	short := "operation-" + strconv.FormatInt(now.UnixMilli(), 10) + "-" + randomHex(8)
	scope := q.root()
	name := scope + "/operations/" + short
	op := map[string]any{}
	defaults(schema, op)
	set := func(field string, value any) {
		if schema.Properties[field] != nil && value != nil {
			op[field] = value
		}
	}
	link, _ := target["selfLink"].(string)
	if link == "" {
		link = q.link(q.targetName)
	}
	stamp := now.Format(time.RFC3339Nano)
	set("id", numericID())
	set("name", short)
	set("operationType", q.verb())
	set("status", "DONE")
	set("progress", int64(100))
	set("targetLink", link)
	set("targetId", target["id"])
	set("user", q.subject)
	set("insertTime", stamp)
	set("startTime", stamp)
	set("endTime", stamp)
	set("selfLink", q.link(name))
	if requestID, _ := q.params["requestId"].(string); requestID != "" {
		set("clientOperationId", requestID)
	}
	switch {
	case strings.HasPrefix(q.scopePath, "zones/"):
		set("zone", q.link(scope))
	case strings.HasPrefix(q.scopePath, "regions/"):
		set("region", q.link(scope))
	}
	return q.storeOperation(tx, name, scope, op)
}

// mergePatch applies an RFC 7396 JSON merge patch; null members, which
// would delete, are refused by the request decoder.
func mergePatch(target, patch map[string]any) {
	for key, value := range patch {
		if child, ok := value.(map[string]any); ok {
			if existing, ok := target[key].(map[string]any); ok {
				mergePatch(existing, child)
				continue
			}
		}
		target[key] = value
	}
}
