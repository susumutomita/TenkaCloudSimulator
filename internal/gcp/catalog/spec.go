package catalog

import "regexp"

// Kind is the reviewed role of one official discovery method.
type Kind string

const (
	Create        Kind = "create"
	Get           Kind = "get"
	List          Kind = "list"
	Patch         Kind = "patch"
	Replace       Kind = "replace" // full-resource update: PUT, or PATCH without a mask
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
	// Refuse lists official query parameters whose effect the simulator
	// does not implement; a request setting one to a non-default value is
	// refused.
	Refuse []string
	// Unwrap maps a request body that wraps or derives the resource, such
	// as Spanner's CreateInstanceRequest, to the resource and its key: the
	// new resource ID for create, the update mask for patch.
	Unwrap func(body map[string]any) (resource map[string]any, key string, err error)
	// Permission overrides the derived IAM permission, for APIs whose
	// operation permissions use another prefix.
	Permission string
	// Transition validates a Patch or Set against the stored resource,
	// for example that a disk only grows.
	Transition func(current, body map[string]any) error
	// Fingerprint names a request field that guards a Patch or Set: when
	// supplied it must equal the stored field of the same name, which is
	// regenerated on success (Compute Engine fingerprint, labelFingerprint).
	Fingerprint string
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
	// Implicit names an unstored collection segment between Parent and
	// this collection, such as Firestore collection groups: the parent path
	// is <parent>/<Implicit>/<id>, where id matches ImplicitPattern (the
	// AIP-122 ID pattern when nil).
	Implicit        string
	ImplicitPattern *regexp.Regexp
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
	// FullName means IDField carries the full resource name
	// (parent/collection/ID) rather than the bare ID, as Cloud Composer
	// environments do; the named parent must be the request parent.
	FullName bool
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
	// Cascade lets delete remove children: with force=true when the
	// official delete method has a force parameter, and always when it has
	// none. Otherwise deleting a parent with children fails with
	// FAILED_PRECONDITION.
	Cascade bool
	// References maps field paths (dots for objects, [] for list items,
	// for example "network" or "nats[].subnetworks[].name") to the
	// collections of this API they must name. Each value must name an
	// existing resource of the configured project (by resource name, or for
	// flat APIs also by selfLink or partial URL) at creation and whenever
	// an update writes the field, and is stored as the resource name, or
	// the selfLink for flat APIs.
	References map[string]Reference
	// Conflict refuses a created or updated resource that clashes with
	// another resource of the same collection in this API, such as
	// overlapping subnetwork ranges in one network.
	Conflict func(candidate, other map[string]any) error
	// Protection lists stored field values, such as a deletion protection
	// flag, that make delete fail with FAILED_PRECONDITION.
	Protection map[string]any
	// Operations names the collection (this one or an ancestor) whose
	// resource name prefixes this collection's long-running operations,
	// as in projects/{project}/databases/{database}/operations/{operation}.
	// "" places them under the API scope root.
	Operations string
	// Message is the protobuf message name in response @type values when it
	// differs from Schema, as for discovery schemas such as
	// GoogleFirestoreAdminV1Database or GoogleCloudDataplexV1Lake: a name
	// under the API TypeURL or a full type.googleapis.com/ URL.
	Message string
	// Zonal places the collection of a "location" or "project" scope API in
	// zones of the supported regions, such as us-central1-a, instead of the
	// regions and global themselves.
	Zonal bool
}

// Reference is a field path that names other resources of the same API.
type Reference struct {
	// Collection is the collection the field names; Or lists further
	// collections it may name instead, as a URL map's default service may
	// be a backend service or a backend bucket.
	Collection string
	Or         []string
	// Blocks refuses deleting the referenced resource while it is named,
	// as Compute Engine refuses to delete a network a subnetwork uses.
	// Without it the reference is weak, as a snapshot outlives its disk.
	Blocks bool
	// Override is a delete query parameter that lifts Blocks when true,
	// such as ignoreBackupPlanReferences.
	Override string
}

// targets lists every collection a reference may name.
func (r Reference) targets() []string { return append([]string{r.Collection}, r.Or...) }

// message is the protobuf message name of the collection's resource.
func (c *Collection) message() string {
	if c.Message != "" {
		return c.Message
	}
	return c.Schema
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
	// (projects/{project}/locations/{location}), "project" (projects/{project};
	// a collection whose official path names a location, as Cloud Logging
	// buckets do, lives under that location) or "zonal" (projects/{project}/
	// global, .../regions/{region} or .../zones/{zone}, as each official
	// method path of a flat API names).
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
