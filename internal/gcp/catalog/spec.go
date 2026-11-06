package catalog

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
	// "" means the server generates one.
	IDParam string
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
