package catalog

import (
	"slices"
	"sort"
	"strings"
)

// MemberDescriptor describes one top-level official input member.
type MemberDescriptor struct {
	Name         string   `json:"name"`
	Location     string   `json:"location,omitempty"`
	LocationName string   `json:"locationName,omitempty"`
	Type         string   `json:"type"`
	Element      string   `json:"element,omitempty"`
	Required     bool     `json:"required,omitempty"`
	Enum         []string `json:"enum,omitempty"`
	Min          *float64 `json:"min,omitempty"`
	Max          *float64 `json:"max,omitempty"`
	// Pattern is the official string pattern, so clients send a value the
	// member accepts (for example an ID rather than a display name).
	Pattern string `json:"pattern,omitempty"`
	// Query protocol only. Key is the form parameter name; a list sends
	// its elements as Key.ItemKey.N, or Key.N when Flattened, and an empty
	// list as EmptyKey= (botocore's own name for an empty renamed list).
	Key       string `json:"key,omitempty"`
	ItemKey   string `json:"itemKey,omitempty"`
	EmptyKey  string `json:"emptyKey,omitempty"`
	Flattened bool   `json:"flattened,omitempty"`
	// Shape names the member's official shape in the service's InputShapes
	// when it is a structure, map or list of non-strings of a create,
	// update, put or tag operation, so clients can validate and serialize it.
	Shape string `json:"shape,omitempty"`
}

// InputMember references one shape from a structure member, a list element
// or a map key or value. Name is the official member name (the key clients
// use), Wire the JSON/REST body key when it differs, and Key the Query
// parameter segment when it differs (for a list element: its element name).
type InputMember struct {
	Name      string `json:"name,omitempty"`
	Wire      string `json:"wire,omitempty"`
	Key       string `json:"key,omitempty"`
	EmptyKey  string `json:"emptyKey,omitempty"`
	Shape     string `json:"shape"`
	Required  bool   `json:"required,omitempty"`
	Flattened bool   `json:"flattened,omitempty"`
}

// InputShape is the documentation-free official input shape clients need
// to validate and serialize a complex member in the service's own protocol.
type InputShape struct {
	Type     string        `json:"type"`
	Members  []InputMember `json:"members,omitempty"`
	Member   *InputMember  `json:"member,omitempty"`
	Key      *InputMember  `json:"key,omitempty"`
	Value    *InputMember  `json:"value,omitempty"`
	Enum     []string      `json:"enum,omitempty"`
	Min      *float64      `json:"min,omitempty"`
	Max      *float64      `json:"max,omitempty"`
	Document bool          `json:"document,omitempty"`
	Union    bool          `json:"union,omitempty"`
}

// XMLMember is one member of a Query response structure: Name is the key
// of the decoded object (the same key JSON protocols use) and Element the
// XML element that carries it.
type XMLMember struct {
	Name      string `json:"name"`
	Element   string `json:"element"`
	Shape     string `json:"shape"`
	Flattened bool   `json:"flattened,omitempty"`
}

// XMLShape tells first-party clients how to decode a Query XML response
// unambiguously: an empty list from a missing member, a one-element list
// from a structure, and each scalar type.
type XMLShape struct {
	Type    string      `json:"type"`
	Members []XMLMember `json:"members,omitempty"`
	// Item is a list's element (Element is "" when only flattened); Key
	// and Value are a map entry's elements.
	Item  *XMLMember `json:"item,omitempty"`
	Key   *XMLMember `json:"key,omitempty"`
	Value *XMLMember `json:"value,omitempty"`
}

// OpDescriptor tells first-party clients how to send one official operation.
type OpDescriptor struct {
	Name    string             `json:"name"`
	Method  string             `json:"method"`
	URI     string             `json:"uri"`
	Members []MemberDescriptor `json:"members"`
	// Wrapper is the serialized output key holding the resource, or ""
	// when the output structure itself carries the resource members.
	Wrapper string `json:"wrapper,omitempty"`
	// Items is the serialized output key of a list operation's items.
	Items string `json:"items,omitempty"`
	// ValueItems is the serialized output key of a list that returns plain
	// identifiers (ARNs or names) instead of resource structures, and
	// ValueMember the serialized resource key those identifiers fill.
	ValueItems  string `json:"valueItems,omitempty"`
	ValueMember string `json:"valueMember,omitempty"`
	// ItemName, ItemARN and ItemID are the serialized identity keys of
	// this list's items, which may differ from the read shape's keys (for
	// example Athena DataCatalogSummary.CatalogName versus DataCatalog.Name).
	ItemName string `json:"itemName,omitempty"`
	ItemARN  string `json:"itemArn,omitempty"`
	ItemID   string `json:"itemId,omitempty"`
	// Names is the input member of a list that describes named resources
	// (for example ECS DescribeClusters "clusters"); clients use it to
	// describe one resource when no read operation exists.
	Names string `json:"names,omitempty"`
	// InputToken is an input member name; OutputToken a serialized key.
	InputToken  string `json:"inputToken,omitempty"`
	OutputToken string `json:"outputToken,omitempty"`
	Lookup      string `json:"lookup,omitempty"`
	// LookupKey says which resource identity the lookup member names:
	// "arn", "id" or "name".
	LookupKey string `json:"lookupKey,omitempty"`
	Parent    string `json:"parent,omitempty"`
	// Fixed holds input members, by official name, that every request of
	// this operation sends with these values.
	Fixed map[string]any `json:"fixed,omitempty"`
	// Query protocol only: the output shape (a key of the service's
	// XMLShapes), its result wrapper element and, for list-tags, the
	// decoded key that holds the tags.
	Output        string `json:"output,omitempty"`
	ResultWrapper string `json:"resultWrapper,omitempty"`
	TagsKey       string `json:"tagsKey,omitempty"`
	// Editable lists, for an update operation, the input members a client
	// may change: every official member except those identifying the
	// resource, fixed and account members, and resource members the bounded
	// operation refuses (outside its reviewed fields).
	Editable []string `json:"editable,omitempty"`
	// Token is the input member that must carry the resource's current
	// concurrency token (ResourceDescriptor.TokenWire).
	Token string `json:"token,omitempty"`
}

// ResourceDescriptor describes one reviewed resource kind. The *Member
// fields are official member names (input keys); the *Wire fields are the
// serialized keys that responses carry.
type ResourceDescriptor struct {
	Kind       string                  `json:"kind"`
	ARNMember  string                  `json:"arnMember,omitempty"`
	NameMember string                  `json:"nameMember,omitempty"`
	IDMember   string                  `json:"idMember,omitempty"`
	ARNWire    string                  `json:"arnWire,omitempty"`
	NameWire   string                  `json:"nameWire,omitempty"`
	IDWire     string                  `json:"idWire,omitempty"`
	Parent     string                  `json:"parent,omitempty"`
	Limit      int                     `json:"limit"`
	Ops        map[string]OpDescriptor `json:"operations"`
	// ARNTemplate is published when the resource shape carries no ARN but
	// the service tags resources by ARN ({partition}, {region}, {account},
	// {name} and {id} placeholders, as the simulator forms it).
	ARNTemplate string `json:"arnTemplate,omitempty"`
	// TagsWire is the resource shape's tag member when the service binds no
	// list-tags operation, so clients read tags from the description.
	TagsWire string `json:"tagsWire,omitempty"`
	// TokenWire is the serialized key of the resource's concurrency token:
	// a resource member, or a top-level member of read outputs when the
	// token is kept beside the resource shape.
	TokenWire string `json:"tokenWire,omitempty"`
}

// Descriptor is the capability entry of one catalog service.
type Descriptor struct {
	Model        string `json:"model"`
	Title        string `json:"title"`
	ServiceID    string `json:"serviceId"`
	APIVersion   string `json:"apiVersion"`
	Protocol     string `json:"protocol"`
	JSONVersion  string `json:"jsonVersion,omitempty"`
	TargetPrefix string `json:"targetPrefix,omitempty"`
	SigningName  string `json:"signingName"`
	IAMPrefix    string `json:"iamPrefix"`
	Global       bool   `json:"global,omitempty"`
	// Account is the simulator account, and AccountMembers the input
	// members that must name it; first-party clients fill required ones.
	Account        string                  `json:"account"`
	AccountMembers []string                `json:"accountMembers,omitempty"`
	Issues         []int                   `json:"issues"`
	Operations     []string                `json:"operations"`
	Official       int                     `json:"officialOperations"`
	Resources      []ResourceDescriptor    `json:"resources"`
	Tagging        map[string]OpDescriptor `json:"tagging,omitempty"`
	Bounds         []string                `json:"bounds"`
	Source         Source                  `json:"source"`
	// Query protocol only: the request Version and every output shape the
	// bound operations can return.
	XMLShapes map[string]XMLShape `json:"xmlShapes,omitempty"`
	// InputShapes holds every shape the complex members of create, update,
	// put and tag operations reach (see MemberDescriptor.Shape).
	InputShapes map[string]InputShape `json:"inputShapes,omitempty"`
}

// Describe lists every catalog service for capabilities and clients.
func (e *Engine) Describe() []Descriptor {
	out := make([]Descriptor, 0, len(e.bindings))
	for _, b := range e.bindings {
		d := b.describe()
		d.Account = e.cfg.Account
		out = append(out, d)
	}
	return out
}

func (b *binding) describe() Descriptor {
	m := b.model
	d := Descriptor{Model: b.spec.Model, Title: b.spec.Title, ServiceID: m.Metadata.ServiceID, APIVersion: m.Metadata.APIVersion,
		Protocol: m.Metadata.Protocol, JSONVersion: m.Metadata.JSONVersion, TargetPrefix: m.Metadata.TargetPrefix,
		SigningName: b.signingName(), IAMPrefix: b.spec.IAMPrefix, Global: b.spec.Global, Issues: b.spec.Issues, AccountMembers: b.spec.AccountMembers,
		Official: len(m.Routes), Bounds: b.spec.Bounds, Source: m.Source}
	if m.Metadata.Protocol == "query" {
		d.XMLShapes = map[string]XMLShape{}
		for _, op := range b.spec.Ops {
			if output := m.Operations[op.Name].Output; output != "" {
				b.xmlShapes(output, d.XMLShapes)
			}
		}
	}
	for _, op := range b.spec.Ops {
		d.Operations = append(d.Operations, op.Name)
	}
	sort.Strings(d.Operations)
	inputs := map[string]InputShape{}
	for i := range b.spec.Ops {
		op := &b.spec.Ops[i]
		if op.Kind != Tag && op.Kind != Untag && op.Kind != ListTags {
			continue
		}
		od := b.describeOp(op, &Resource{}, inputs)
		// The generic Query console reads only flat tag lists. Retain
		// description tags for wrapped responses such as Redshift DescribeTags.
		if op.Kind == ListTags && m.Metadata.Protocol == "query" && od.TagsKey == "" {
			continue
		}
		if d.Tagging == nil {
			d.Tagging = map[string]OpDescriptor{}
		}
		d.Tagging[string(op.Kind)] = od
	}
	for _, res := range b.spec.Resources {
		rd := ResourceDescriptor{Kind: res.Kind, ARNMember: res.ARNMember, NameMember: res.NameMember, IDMember: res.IDMember, Parent: res.Parent, Limit: res.Limit, Ops: map[string]OpDescriptor{},
			ARNWire: b.resourceWire(res, res.ARNMember), NameWire: b.resourceWire(res, res.NameMember), IDWire: b.resourceWire(res, res.IDMember)}
		if rd.Limit == 0 {
			rd.Limit = defaultKindLimit
		}
		if res.Token != "" {
			rd.TokenWire = b.resourceWire(res, res.Token)
		}
		if res.ARNMember == "" && len(d.Tagging) != 0 {
			rd.ARNTemplate = res.ARN
		}
		if d.Tagging[string(ListTags)].Name == "" {
			shape := m.shape(res.Shape)
			for _, name := range shape.sortedMembers() {
				if m.isTagShape(shape.Members[name].Shape) && strings.Contains(strings.ToLower(name), "tag") {
					rd.TagsWire = shape.Members[name].wire(name)
				}
			}
		}
		for i := range b.spec.Ops {
			op := &b.spec.Ops[i]
			if op.Resource == res.Kind {
				od := b.describeOp(op, res, inputs)
				if op.Kind == List && op.Names != "" {
					od.Names = op.Names
					rd.Ops["describe"] = od
				}
				// Clients list with the most useful official list: structures
				// over plain identifiers, and never one that needs identifiers.
				if existing, ok := rd.Ops[string(op.Kind)]; ok && op.Kind == List && listRank(b.spec, existing) >= listRank(b.spec, od) {
					continue
				}
				rd.Ops[string(op.Kind)] = od
			}
		}
		d.Resources = append(d.Resources, rd)
	}
	if len(inputs) > 0 {
		d.InputShapes = inputs
	}
	return d
}

func (b *binding) describeOp(op *Op, res *Resource, inputs map[string]InputShape) OpDescriptor {
	official := b.model.Operations[op.Name]
	method, uri := official.HTTP.Method, official.HTTP.RequestURI
	if b.model.Metadata.Protocol == "json" {
		method, uri = "POST", "/"
	}
	od := OpDescriptor{Name: op.Name, Method: method, URI: uri, Lookup: op.Lookup, Parent: op.Parent, LookupKey: lookupKey(op.Lookup, res), Fixed: op.Fixed, Token: op.Token}
	if input := b.model.Shapes[official.Input]; input != nil {
		for _, name := range input.sortedMembers() {
			member := input.Members[name]
			shape := b.model.shape(member.Shape)
			md := MemberDescriptor{Name: name, Location: member.Location, LocationName: member.LocationName, Type: shape.Type, Enum: shape.Enum, Min: shape.Min, Max: shape.Max, Pattern: shape.Pattern}
			for _, required := range input.Required {
				md.Required = md.Required || required == name
			}
			if shape.Type == "list" {
				md.Element = b.model.shape(shape.Member.Shape).Type
			}
			if complexInput(op.Kind) && (shape.Type == "structure" || shape.Type == "map" || shape.Type == "list" && b.model.shape(shape.Member.Shape).Type != "string") {
				md.Shape = member.Shape
				b.inputShape(member.Shape, inputs)
			}
			if b.model.Metadata.Protocol == "query" {
				md.Key = b.model.memberKey(name, member)
				if shape.Type == "list" {
					md.Flattened = b.model.flattened(&member)
					if !md.Flattened {
						md.ItemKey = b.model.serialName(shape.Member, "member")
					}
					if own := b.model.serialName(&member, name); own != md.Key {
						md.EmptyKey = own
					}
				}
			}
			od.Members = append(od.Members, md)
		}
	}
	if output := b.model.Shapes[official.Output]; output != nil {
		for _, name := range output.sortedMembers() {
			member := output.Members[name]
			// A REST payload member is the whole body, not a wrapper key.
			if (member.Shape == res.Shape || member.Shape == res.Summary) && output.Payload != name {
				od.Wrapper = member.wire(name)
			}
			if shape := b.model.shape(member.Shape); shape.Type == "list" && (shape.Member.Shape == res.Shape || shape.Member.Shape == res.Summary) {
				od.Items = member.wire(name)
				item := b.model.shape(shape.Member.Shape)
				od.ItemName, od.ItemARN, od.ItemID = itemKey(item, res, res.NameMember, "$name"), itemKey(item, res, res.ARNMember, "$arn"), itemKey(item, res, res.IDMember, "$id")
			}
		}
	}
	if op.Kind == List && od.Items == "" && op.ListValue != "" {
		if output := b.model.Shapes[official.Output]; output != nil {
			for _, name := range output.sortedMembers() {
				member := output.Members[name]
				if shape := b.model.shape(member.Shape); shape.Type == "list" && b.model.shape(shape.Member.Shape).Type == "string" {
					od.ValueItems, od.ValueMember = member.wire(name), b.resourceWire(res, op.ListValue)
					break
				}
			}
		}
	}
	if op.Kind == Update {
		od.Editable = b.editable(op, res)
	}
	if b.model.Metadata.Protocol == "query" {
		od.Output, od.ResultWrapper = official.Output, official.ResultWrapper
		if output := b.model.Shapes[official.Output]; output != nil && op.Kind == ListTags {
			for _, name := range output.sortedMembers() {
				if b.model.isTagShape(output.Members[name].Shape) {
					od.TagsKey = output.Members[name].wire(name)
				}
			}
		}
	}
	if op.Kind == List {
		q := &request{b: b, official: official}
		od.InputToken, od.OutputToken, _ = q.pagination()
		if output := b.model.Shapes[official.Output]; output != nil {
			if member, ok := output.Members[od.OutputToken]; ok {
				od.OutputToken = member.wire(od.OutputToken)
			}
		}
	}
	return od
}

// resourceWire is the serialized key of a resource member in responses.
func (b *binding) resourceWire(res *Resource, name string) string {
	for _, shapeName := range []string{res.Shape, res.Summary} {
		if shape := b.model.Shapes[shapeName]; shape != nil && name != "" {
			if member, ok := shape.Members[name]; ok {
				return member.wire(name)
			}
		}
	}
	return name
}

// listRank orders list operations for first-party clients: a structured
// list that needs no identifiers, then an identifier-only list, then a
// list that only describes named resources.
func listRank(spec *Service, od OpDescriptor) int {
	for i := range spec.Ops {
		if spec.Ops[i].Name == od.Name && spec.Ops[i].Names != "" {
			return 0
		}
	}
	if od.Items != "" {
		return 2
	}
	if od.ValueItems != "" {
		return 1
	}
	return 0
}

// itemKey is the serialized key of a resource identity member within a list
// item shape: the member itself, or the item member the resource generates
// from the same identity (CatalogName: $name).
func itemKey(item *Shape, res *Resource, member, generator string) string {
	if member == "" {
		return ""
	}
	if m, ok := item.Members[member]; ok {
		return m.wire(member)
	}
	keys := make([]string, 0, len(res.Generate))
	for key := range res.Generate {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if res.Generate[key] == generator {
			if m, ok := item.Members[key]; ok {
				return m.wire(key)
			}
		}
	}
	return ""
}

// lookupKey names the resource identity a lookup member carries: the ARN
// member or an *Arn member, the ID member or an *Id/*Identifier member, or
// otherwise the name.
func lookupKey(lookup string, res *Resource) string {
	if lookup == "" {
		return ""
	}
	last := lookup[strings.LastIndex(lookup, ".")+1:]
	lower := strings.ToLower(last)
	switch {
	case last == res.ARNMember || strings.HasSuffix(lower, "arn"):
		return "arn"
	case last == res.IDMember || res.IDMember != "" && (strings.HasSuffix(lower, "id") || strings.HasSuffix(lower, "identifier")):
		return "id"
	}
	return "name"
}

// xmlShapes registers a shape and every shape it reaches for Query decoding.
func (b *binding) xmlShapes(name string, out map[string]XMLShape) {
	if _, done := out[name]; done {
		return
	}
	m := b.model
	shape := m.shape(name)
	x := XMLShape{Type: shape.Type}
	out[name] = x
	ref := func(r *Member, fallback string) *XMLMember {
		b.xmlShapes(r.Shape, out)
		return &XMLMember{Element: m.serialName(r, fallback), Shape: r.Shape}
	}
	switch shape.Type {
	case "structure":
		for _, member := range shape.sortedMembers() {
			r := shape.Members[member]
			b.xmlShapes(r.Shape, out)
			x.Members = append(x.Members, XMLMember{Name: r.wire(member), Element: m.memberKey(member, r), Shape: r.Shape, Flattened: m.flattened(&r)})
		}
	case "list":
		x.Item = ref(shape.Member, "member")
	case "map":
		x.Key, x.Value = ref(shape.Key, "key"), ref(shape.Value, "value")
	}
	out[name] = x
}

func complexInput(kind Kind) bool {
	return kind == Create || kind == Update || kind == Put || kind == Tag
}

// editable lists an update operation's changeable input members.
func (b *binding) editable(op *Op, res *Resource) []string {
	input := b.model.Shapes[b.model.Operations[op.Name].Input]
	if input == nil {
		return nil
	}
	resource := b.model.shape(res.Shape)
	q := &request{b: b, op: op}
	var out []string
	for _, name := range input.sortedMembers() {
		if q.passive(name) || op.Fixed[name] != nil {
			continue
		}
		target := name
		if alias, ok := res.Aliases[name]; ok {
			target = alias
		}
		if _, stored := resource.Members[target]; stored && !slices.Contains(op.Fields, name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// inputShape registers a shape and every shape it reaches for clients.
func (b *binding) inputShape(name string, inputs map[string]InputShape) {
	if _, done := inputs[name]; done {
		return
	}
	m := b.model
	shape := m.shape(name)
	query := m.Metadata.Protocol == "query"
	x := InputShape{Type: shape.Type, Enum: shape.Enum, Min: shape.Min, Max: shape.Max, Document: shape.Document, Union: shape.Union}
	inputs[name] = x
	ref := func(r *Member, fallback string) *InputMember {
		b.inputShape(r.Shape, inputs)
		out := &InputMember{Shape: r.Shape}
		if query {
			out.Key = m.serialName(r, fallback)
		}
		return out
	}
	switch shape.Type {
	case "structure":
		if shape.Document {
			break
		}
		for _, member := range shape.sortedMembers() {
			r := shape.Members[member]
			b.inputShape(r.Shape, inputs)
			im := InputMember{Name: member, Shape: r.Shape, Required: slices.Contains(shape.Required, member)}
			if wire := r.wire(member); wire != member {
				im.Wire = wire
			}
			if query {
				if key := m.memberKey(member, r); key != member {
					im.Key = key
				}
				if own := m.serialName(&r, member); own != m.memberKey(member, r) {
					im.EmptyKey = own
				}
				im.Flattened = m.flattened(&r)
			}
			x.Members = append(x.Members, im)
		}
	case "list":
		x.Member = ref(shape.Member, "member")
	case "map":
		x.Key, x.Value = ref(shape.Key, "key"), ref(shape.Value, "value")
	}
	inputs[name] = x
}
