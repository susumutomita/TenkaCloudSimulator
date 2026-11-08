package catalog

import "encoding/json"

// Kind is the reviewed semantic role of one implemented official operation.
type Kind string

const (
	Create   Kind = "create"
	Read     Kind = "read"
	List     Kind = "list"
	Update   Kind = "update"
	Delete   Kind = "delete"
	Tag      Kind = "tag"
	Untag    Kind = "untag"
	ListTags Kind = "list-tags"
	// Put creates the identified resource or replaces its supplied members.
	Put Kind = "put"
)

// Op binds one official operation to a reviewed semantic role.
//
// Lookup names the input member that identifies the resource (an ARN, name or
// identifier as permitted by the official pattern). Fields limits which input
// members an Update may change. Filters maps list-request members to stored
// resource members compared for equality. ListValue selects the stored member
// rendered when the official list output is a list of strings.
type Op struct {
	Name string
	// Merged runs on the merged stored document of an update before it is
	// committed, with the members the request supplied. It may normalize the
	// document (for example drop settings a transition makes inapplicable)
	// and refuses invalid combinations a Check on the request cannot see.
	Merged func(document, supplied map[string]any) error
	// Action is the documented IAM action when it differs from the
	// operation name (for example budgets:ModifyBudget for CreateBudget).
	Action    string
	Kind      Kind
	Resource  string
	Lookup    string
	Parent    string
	Fields    []string
	Filters   map[string]string
	ListValue string
	// Touch lists stored members set to the current time by this operation.
	Touch []string
	// Set assigns fixed values to stored members after a successful mutation,
	// for documented state transitions such as an immediately completed stop.
	Set map[string]any
	// From names a structure input member whose members describe the
	// resource (for example DatabaseInput); they are stored like top-level
	// members.
	From string
	// Accept lists input members whose official effect is already implied by
	// the bounded model (for example force on a resource without children).
	Accept []string
	// Fixed names input members a client must always send with these values,
	// because the bounded model serves only that selection (for example
	// Bedrock ListInferenceProfiles typeEquals APPLICATION). Descriptors
	// publish them so first-party clients send the same request as SDKs.
	Fixed map[string]any
	// Names is a list input member of identifiers that must all exist; the
	// list operation then returns exactly those resources in request order.
	Names string
	// Check validates semantic constraints the official shape cannot express.
	Check func(input map[string]any) error
	// Failures names the output list that reports identifiers of Names that do
	// not exist, instead of failing the whole request; FailureItem builds
	// each element, replacing "$name" with the missing identifier. Without
	// FailureItem the list holds the missing identifiers themselves.
	Failures    string
	FailureItem map[string]any
	// LookupAlt lists alternative identifying members (for example a name
	// or an ARN); the first one supplied identifies the resource. Lookup may
	// also be a dotted path into a structure member.
	LookupAlt []string
	// IdempotentDelete makes deleting a missing resource succeed, as the
	// official operation documents.
	IdempotentDelete bool
	// HasMore names a boolean output member that reports a further page.
	HasMore string
}

// Failure returns a semantic validation error with a modeled exception code
// for use in Check hooks. The code is used only when the operation models it.
func Failure(code, message string) error {
	return &fault{kind: "validation", code: code, message: message}
}

// Resource describes one resource kind owned by a service specification.
type Resource struct {
	Kind string
	// Shape is the official structure that holds the resource description.
	Shape string
	// Summary is the official list element shape when it differs from Shape.
	Summary string
	// ARN is a template with {partition}, {region}, {account}, {id}, {name}
	// and {parent} placeholders. Global resources omit {region} content.
	ARN string
	// ARNMember, NameMember and IDMember name stored members holding identity.
	ARNMember  string
	NameMember string
	IDMember   string
	// ID selects the identifier generator: uuid, name or alnum:<n>[:<prefix>].
	ID string
	// Unique enforces name uniqueness within the region (and parent).
	Unique bool
	// Limit bounds live resources of this kind per region and parent.
	Limit int
	// Parent names the parent resource kind; ParentMember is the stored
	// member holding the parent's identifier.
	Parent       string
	ParentMember string
	// Generate assigns stored members at creation. Values starting with $
	// are generators: $arn, $id, $name, $now, $account, $region, $parentArn,
	// $parentId, $parentName. Other values are stored literally.
	Generate map[string]any
	// Aliases maps create/update input members onto differently named
	// stored members of the official resource shape.
	Aliases map[string]string
	// Cascade deletes children with this resource; otherwise deletion with
	// live children is refused.
	Cascade bool
	// Refs maps stored members holding ARNs to the resource kind they must
	// reference; creation and update fail when the referenced one is missing.
	Refs map[string]string
	// Singleton resources have exactly one instance per region (account
	// settings). Reads without a stored instance return the Generate
	// defaults; Put and Update store it.
	Singleton bool
}

// Service is a reviewed bounded specification of one official AWS service.
type Service struct {
	// Model is the botocore service name of the embedded official model.
	Model string
	// Title is the official full service name shown in the console.
	Title string
	// IAMPrefix is the service authorization prefix of IAM actions.
	IAMPrefix string
	// Issues lists repository Issues whose initial bounded scope this serves.
	Issues []int
	// Global services have one partition-wide scope and region-less ARNs.
	Global    bool
	Resources []*Resource
	Ops       []Op
	// AccountMembers are input members that name the owning account (for
	// example registryId or CatalogId); when supplied they must equal it.
	AccountMembers []string
	// Bounds documents explicit differences and unsupported behavior.
	Bounds []string
}

func (s *Service) resource(kind string) *Resource {
	for _, r := range s.Resources {
		if r.Kind == kind {
			return r
		}
	}
	return nil
}

// Number reads a numeric member as a request decodes it (int64, float64)
// or as a stored document holds it (json.Number), so Check and Merged
// hooks compare request and stored values alike.
func Number(value any) (float64, bool) {
	switch v := value.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	}
	return 0, false
}
