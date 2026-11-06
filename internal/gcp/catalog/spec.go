package catalog

import "regexp"

// Kind is the reviewed role of one official discovery method.
type Kind string

const (
	Create        Kind = "create"
	Get           Kind = "get"
	List          Kind = "list"
	Patch         Kind = "patch"
	Delete        Kind = "delete"
	OperationGet  Kind = "operation-get"
	OperationList Kind = "operation-list"
	// Set is a custom method, such as Compute Engine setLabels or resize,
	// that writes every field of its request onto an existing resource.
	Set Kind = "set"
)

// Binding binds one official discovery method to a reviewed role.
type Binding struct {
	Method     string
	Kind       Kind
	Collection string
	// Check validates semantic constraints the schema cannot express.
	Check func(body map[string]any) error
	// Transition validates a Patch or Set against the stored resource,
	// for example that a disk only grows.
	Transition func(current, body map[string]any) error
	// Fingerprint names a request field that guards a Patch or Set: when
	// supplied it must equal the stored field of the same name, which is
	// regenerated on success (Compute Engine fingerprint, labelFingerprint).
	Fingerprint string
	// Refuse lists official query parameters whose effect is not simulated.
	Refuse []string
}

// Collection is one resource collection in Google resource-name form.
type Collection struct {
	// ID is the collection segment, for example "clusters". Create and
	// list read the segment from the official method path, so a flat API
	// may give a global and a regional collection with the same segment
	// distinct IDs (for example "globalForwardingRules").
	ID string
	// Schema is the official resource schema, for example "Cluster".
	Schema string
	// Parent names the parent collection; "" means the API scope root
	// (projects/{project}/locations/{location} or projects/{project}).
	Parent string
	// IDParam is the create query parameter carrying the client-chosen ID;
	// "" with an empty IDField means the server generates one.
	IDParam string
	// IDField is the top-level create body field carrying the client-chosen
	// ID, for APIs that take it in the request body (for example "name").
	IDField string
	// IDPattern replaces the AIP-122 ID pattern for APIs with other rules.
	IDPattern *regexp.Regexp
	// ShortName stores the ID rather than the full resource name in the
	// "name" field, as APIs such as Cloud Logging sinks do.
	ShortName bool
	// Limit bounds live resources per parent.
	Limit int
	// Generate assigns output fields at creation: $name, $now, $uid,
	// $etag, $fingerprint, $id, $project, $location, $parent or literal
	// values. Output-only fields with an official default always take it.
	Generate map[string]any
	// Mutable lists the fields an update mask may name.
	Mutable []string
	// Permission is the IAM permission prefix, for example "alloydb.clusters".
	Permission string
	// Relax lists fields whose official "Required." marker is superseded
	// by an alternative field; a Check hook then enforces the alternative.
	Relax []string
	// Cascade lets delete with force=true remove children; otherwise
	// deleting a parent with children fails with FAILED_PRECONDITION.
	Cascade bool
	// References maps field paths (dots for objects, [] for list items,
	// for example "network" or "nats[].subnetworks[].name") to the
	// collections of this API they may name. Each value must name an
	// existing resource in the configured project by selfLink, partial URL
	// or resource name, is stored as the selfLink (flat APIs) or resource
	// name, and keeps that resource from being deleted
	// (FAILED_PRECONDITION). Cascade children are not checked.
	References map[string][]string
	// Weak references are checked and canonicalized like References at
	// creation only and do not keep their target from being deleted, as a
	// snapshot outlives its source disk.
	Weak map[string][]string
	// Conflict refuses a created or updated resource that clashes with
	// another resource of the same collection in this API, such as
	// overlapping subnetwork ranges in one network.
	Conflict func(candidate, other map[string]any) error
}

// API is a reviewed bounded specification of one official Google API.
type API struct {
	// Key is the embedded discovery document key, for example "alloydb.v1".
	Key    string
	Title  string
	Issues []int
	// TypeURL prefixes response @type values in long-running operations,
	// for example "type.googleapis.com/google.cloud.alloydb.v1.".
	TypeURL string
	// Scope is the root under which collections live: "location"
	// (projects/{project}/locations/{location}), "project" (projects/{project})
	// or "zonal" (projects/{project}/global, .../regions/{region} or
	// .../zones/{zone}, as each official method path names).
	Scope string
	// SelfLink selects the flat conventions of APIs such as Compute Engine
	// (compute.v1) and is the prefix of their selfLink URLs, for example
	// "https://www.googleapis.com/compute/v1/". Created resources then carry
	// a numeric id, selfLink, creationTimestamp and zone or region where
	// their schema has them; patches without updateMask merge objects as a
	// JSON merge patch; and methods returning an operation resource with
	// status and targetLink (instead of google.longrunning.Operation)
	// complete with status DONE, stored under their scope.
	SelfLink    string
	Collections []*Collection
	Bindings    []Binding
	Bounds      []string
}

func (a *API) collection(id string) *Collection {
	for _, c := range a.Collections {
		if c.ID == id {
			return c
		}
	}
	return nil
}
