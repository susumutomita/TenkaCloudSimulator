package catalog

import (
	"maps"
	"slices"
	"strings"
)

// DescribeTags can list by ARN or enumerate across resources in AWS. Only
// the ARN form is supported here; it must not be combined with type or paging.
func redshiftDescribeTagsInput(input map[string]any) error {
	if name, _ := input["ResourceName"].(string); !strings.HasPrefix(name, "arn:") {
		return invalid("DescribeTags requires one ResourceName ARN in the simulator.")
	}
	for _, member := range []string{"ResourceType", "MaxRecords", "Marker"} {
		if _, present := input[member]; present {
			return invalid("%s cannot be used with ResourceName.", member)
		}
	}
	return nil
}

// Redshift's Query response wraps each tag with its resource's ARN and type,
// unlike other catalog services that return just tag keys and values. The
// common ListTags path resolves and authorizes the exact resource first.
func (q *request) redshiftTaggedResources(r row) (map[string]any, error) {
	var kind string
	switch r.Kind {
	case "cluster":
		kind = "cluster"
	case "clusterParameterGroup":
		kind = "parametergroup"
	case "clusterSubnetGroup":
		kind = "subnetgroup"
	default:
		return nil, &fault{kind: "notfound", message: "The resource " + r.ARN + " does not support DescribeTags."}
	}
	keys, _ := q.input["TagKeys"].([]any)
	values, _ := q.input["TagValues"].([]any)
	entries := make([]any, 0, len(r.Tags))
	for _, key := range slices.Sorted(maps.Keys(r.Tags)) {
		value := r.Tags[key]
		if (len(keys) > 0 && !slices.Contains(keys, any(key))) || (len(values) > 0 && !slices.Contains(values, any(value))) {
			continue
		}
		entries = append(entries, map[string]any{
			"ResourceName": r.ARN,
			"ResourceType": kind,
			"Tag":          map[string]any{"Key": key, "Value": value},
		})
	}
	return map[string]any{"TaggedResources": entries}, nil
}
