package arm

import "regexp"

// Kind is the reviewed role of one official ARM operation.
type Kind string

const (
	Put    Kind = "put"
	Get    Kind = "get"
	Head   Kind = "head"
	Patch  Kind = "patch"
	Delete Kind = "delete"
	List   Kind = "list"
)

// Binding binds one official operation to a reviewed role on a resource type.
type Binding struct {
	Operation string
	Kind      Kind
	Type      string
	// Query lists the official optional query parameters this binding
	// honours. Any other supplied query parameter is refused.
	Query []string
}

// Ref declares that a request field names another catalog resource by ARM ID.
// Paths use dots; "[]" crosses arrays and "{}" selects map keys, for example
// "properties.networkSecurityGroup.id" or "identity.userAssignedIdentities.{}".
type Ref struct {
	Path string
	Type string // full type, for example "Microsoft.Network/networkSecurityGroups"
	// InUse is the error code returned when deleting a referenced target;
	// "" means the reference does not block deletion (as for identities).
	InUse string
}

// Change is the semantic context of one validated write, evaluated inside its
// write transaction. Hooks may refuse it or assign server-generated fields.
type Change struct {
	Document  map[string]any   // the resource document about to be stored
	Previous  map[string]any   // the stored document on update, nil on create
	Parent    map[string]any   // the parent resource document, nil at group scope
	Siblings  []map[string]any // other resources of the same type and parent
	Children  map[string][]map[string]any
	Tenant    string
	Name      string
	Locations []string
}

// ResourceType is one reviewed ARM resource type.
type ResourceType struct {
	// Name is the type path below the namespace, such as "vaults" or
	// "virtualNetworks/subnets"; "resourceGroups" in Microsoft.Resources
	// is the subscription-scoped resource group type.
	Name string
	// Title is the English display name used by the console.
	Title string
	// NamePattern is the documented Azure naming rule of the resource name.
	NamePattern *regexp.Regexp
	// GlobalName refuses a second resource of this type with the same name
	// anywhere in the environment, as DNS-scoped Azure names are.
	GlobalName bool
	// Limit bounds live resources of this type per parent.
	Limit int
	// Generate assigns server fields at dotted paths: "$uuid" (kept across
	// updates), "$tenant", "$now" (each write), "$created" (kept) or literals.
	Generate map[string]any
	// Unsupported lists request fields the simulator refuses because they
	// need behaviour, data planes or resources it does not provide.
	Unsupported map[string]string
	// Secrets lists request fields that would carry secret values; they are
	// refused and never stored.
	Secrets []string
	Refs    []Ref
	// Check validates semantic rules the definitions cannot express.
	Check func(*Change) error
	// Form lists dotted request fields the console create form offers.
	Form []string
	// Defaults prefills console form fields ("$tenant" or literals).
	Defaults map[string]any
}

// Provider is a reviewed bounded specification of one resource provider
// namespace at one stable api-version.
type Provider struct {
	Namespace  string
	APIVersion string
	Title      string
	Issues     []int
	Types      []*ResourceType
	Bindings   []Binding
	Bounds     []string
}
