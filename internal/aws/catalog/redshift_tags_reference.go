package catalog

// Redshift's official model also supports aggregate tag enumeration. Reflect
// the simulator's per-ARN bound in the served form without changing that model.
func redshiftDescribeTagsReference(form, example map[string]any) string {
	props := form["properties"].(map[string]any)
	for _, name := range []string{"ResourceType", "MaxRecords", "Marker"} {
		delete(props, name)
	}
	resource := props["ResourceName"].(map[string]any)
	resource["pattern"] = "^arn:aws:redshift:[a-z0-9-]+:[0-9]{12}:(cluster|parametergroup|subnetgroup):[A-Za-z0-9-]+$"
	resource["description"] = "ARN of one stored Redshift cluster, parameter group, or subnet group in the selected local Region."
	form["required"] = append(form["required"].([]any), "ResourceName")
	example["ResourceName"] = "arn:aws:redshift:us-east-1:000000000000:parametergroup:fixture-params"
	return " DescribeTags reads a single local ResourceName ARN. The sample parameter group is not pre-created. To try it, " +
		"first use CreateClusterParameterGroup in us-east-1 with ParameterGroupName=fixture-params, ParameterGroupFamily=redshift-1.0, " +
		"and Description=Local tag fixture. Optionally add Tags.Tag.1.Key=purpose and Tags.Tag.1.Value=demo to see a nonempty result. " +
		"Otherwise replace ResourceName with an existing local cluster, parameter group, or subnet group ARN in the same local Region."
}
