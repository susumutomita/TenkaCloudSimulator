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
)

// Binding binds one official discovery method to a reviewed role.
type Binding struct {
	Method     string
	Kind       Kind
	Collection string
	// Check validates semantic constraints the schema cannot express.
	Check func(body map[string]any) error
}

// Collection is one resource collection in Google resource-name form.
type Collection struct {
	// ID is the collection segment, for example "clusters".
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
	// $etag, $id, $project, $location, $parent or literal values.
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
	// References maps top-level fields to the same-API collection whose
	// existing resource they must name at create and update.
	References map[string]Reference
	// Protection lists stored field values, such as a deletion protection
	// flag, that make delete fail with FAILED_PRECONDITION.
	Protection map[string]any
	// Operations names the collection (this one or an ancestor) whose
	// resource name prefixes this collection's long-running operations,
	// as in projects/{project}/databases/{database}/operations/{operation}.
	// "" places them under the API scope root.
	Operations string
	// Message is the operation response type name when it differs from
	// Schema, for discovery schemas such as GoogleFirestoreAdminV1Database.
	Message string
}

// Reference is a field that names another resource of the same API.
type Reference struct {
	Collection string
	// Blocks refuses deleting the referenced resource while it is named.
	Blocks bool
}

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
	// Scope is "location" or "project": the root under which collections live.
	Scope       string
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
