package catalog

import "sort"

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
	// InputToken is an input member name; OutputToken a serialized key.
	InputToken  string `json:"inputToken,omitempty"`
	OutputToken string `json:"outputToken,omitempty"`
	Lookup      string `json:"lookup,omitempty"`
	Parent      string `json:"parent,omitempty"`
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
}

// Descriptor is the capability entry of one catalog service.
type Descriptor struct {
	Model        string                  `json:"model"`
	Title        string                  `json:"title"`
	ServiceID    string                  `json:"serviceId"`
	APIVersion   string                  `json:"apiVersion"`
	Protocol     string                  `json:"protocol"`
	JSONVersion  string                  `json:"jsonVersion,omitempty"`
	TargetPrefix string                  `json:"targetPrefix,omitempty"`
	SigningName  string                  `json:"signingName"`
	IAMPrefix    string                  `json:"iamPrefix"`
	Global       bool                    `json:"global,omitempty"`
	Issues       []int                   `json:"issues"`
	Operations   []string                `json:"operations"`
	Official     int                     `json:"officialOperations"`
	Resources    []ResourceDescriptor    `json:"resources"`
	Tagging      map[string]OpDescriptor `json:"tagging,omitempty"`
	Bounds       []string                `json:"bounds"`
	Source       Source                  `json:"source"`
}

// Describe lists every catalog service for capabilities and clients.
func (e *Engine) Describe() []Descriptor {
	out := make([]Descriptor, 0, len(e.bindings))
	for _, b := range e.bindings {
		out = append(out, b.describe())
	}
	return out
}

func (b *binding) describe() Descriptor {
	m := b.model
	d := Descriptor{Model: b.spec.Model, Title: b.spec.Title, ServiceID: m.Metadata.ServiceID, APIVersion: m.Metadata.APIVersion,
		Protocol: m.Metadata.Protocol, JSONVersion: m.Metadata.JSONVersion, TargetPrefix: m.Metadata.TargetPrefix,
		SigningName: b.signingName(), IAMPrefix: b.spec.IAMPrefix, Global: b.spec.Global, Issues: b.spec.Issues,
		Official: len(m.Routes), Bounds: b.spec.Bounds, Source: m.Source}
	for _, op := range b.spec.Ops {
		d.Operations = append(d.Operations, op.Name)
	}
	sort.Strings(d.Operations)
	for _, res := range b.spec.Resources {
		rd := ResourceDescriptor{Kind: res.Kind, ARNMember: res.ARNMember, NameMember: res.NameMember, IDMember: res.IDMember, Parent: res.Parent, Limit: res.Limit, Ops: map[string]OpDescriptor{},
			ARNWire: b.resourceWire(res, res.ARNMember), NameWire: b.resourceWire(res, res.NameMember), IDWire: b.resourceWire(res, res.IDMember)}
		if rd.Limit == 0 {
			rd.Limit = defaultKindLimit
		}
		for i := range b.spec.Ops {
			op := &b.spec.Ops[i]
			if op.Resource == res.Kind {
				rd.Ops[string(op.Kind)] = b.describeOp(op, res)
			}
		}
		d.Resources = append(d.Resources, rd)
	}
	for i := range b.spec.Ops {
		op := &b.spec.Ops[i]
		if op.Kind == Tag || op.Kind == Untag || op.Kind == ListTags {
			if d.Tagging == nil {
				d.Tagging = map[string]OpDescriptor{}
			}
			d.Tagging[string(op.Kind)] = b.describeOp(op, &Resource{})
		}
	}
	return d
}

func (b *binding) describeOp(op *Op, res *Resource) OpDescriptor {
	official := b.model.Operations[op.Name]
	method, uri := official.HTTP.Method, official.HTTP.RequestURI
	if b.model.Metadata.Protocol == "json" {
		method, uri = "POST", "/"
	}
	od := OpDescriptor{Name: op.Name, Method: method, URI: uri, Lookup: op.Lookup, Parent: op.Parent}
	if input := b.model.Shapes[official.Input]; input != nil {
		for _, name := range input.sortedMembers() {
			member := input.Members[name]
			shape := b.model.shape(member.Shape)
			md := MemberDescriptor{Name: name, Location: member.Location, LocationName: member.LocationName, Type: shape.Type, Enum: shape.Enum, Min: shape.Min, Max: shape.Max}
			for _, required := range input.Required {
				md.Required = md.Required || required == name
			}
			if shape.Type == "list" {
				md.Element = b.model.shape(shape.Member.Shape).Type
			}
			od.Members = append(od.Members, md)
		}
	}
	if output := b.model.Shapes[official.Output]; output != nil {
		for _, name := range output.sortedMembers() {
			member := output.Members[name]
			if member.Shape == res.Shape || member.Shape == res.Summary {
				od.Wrapper = member.wire(name)
			}
			if shape := b.model.shape(member.Shape); shape.Type == "list" && (shape.Member.Shape == res.Shape || shape.Member.Shape == res.Summary) {
				od.Items = member.wire(name)
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
