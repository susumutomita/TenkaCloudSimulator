package catalog

import "testing"

func TestPCSCreateQueueRequiresComputeNodeGroupAssociation(t *testing.T) {
	h := newPCSHarness(t)
	cluster := map[string]any{"clusterName": "queue-cluster", "scheduler": map[string]any{"type": "SLURM", "version": "24.11"},
		"size": "SMALL", "networking": map[string]any{"subnetIds": []any{"subnet-0a1b2c3d"}}}
	expect(t, h.json1("pcs", "CreateCluster", cluster), 200, "")
	group := map[string]any{"clusterIdentifier": "queue-cluster", "computeNodeGroupName": "compute", "subnetIds": []any{"subnet-0a1b2c3d"},
		"customLaunchTemplate":  map[string]any{"id": "lt-0123456789abcdef0", "version": "1"},
		"iamInstanceProfileArn": "arn:aws:iam::000000000000:instance-profile/pcs",
		"scalingConfiguration":  map[string]any{"minInstanceCount": 0, "maxInstanceCount": 4},
		"instanceConfigs":       []any{map[string]any{"instanceType": "c6i.large"}}}
	made := h.json1("pcs", "CreateComputeNodeGroup", group)
	expect(t, made, 200, "")
	id := made.body["computeNodeGroup"].(map[string]any)["id"]
	for _, test := range []struct {
		name   string
		groups []any
	}{
		{name: "omitted-groups"},
		{name: "empty-groups", groups: []any{}},
		{name: "positive-control", groups: []any{map[string]any{"computeNodeGroupId": id}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := map[string]any{"clusterIdentifier": "queue-cluster", "queueName": test.name}
			if test.groups != nil {
				input["computeNodeGroupConfigurations"] = test.groups
			}
			got := h.json1("pcs", "CreateQueue", input)
			read := h.json1("pcs", "GetQueue", map[string]any{"clusterIdentifier": "queue-cluster", "queueIdentifier": test.name})
			if test.name == "positive-control" {
				expect(t, got, 200, "")
				expect(t, read, 200, "")
				replace := h.json1("pcs", "UpdateQueue", map[string]any{"clusterIdentifier": "queue-cluster",
					"queueIdentifier": test.name, "computeNodeGroupConfigurations": []any{}})
				expect(t, replace, 200, "")
				read = h.json1("pcs", "GetQueue", map[string]any{"clusterIdentifier": "queue-cluster", "queueIdentifier": test.name})
				expect(t, read, 200, "")
				list, ok := read.body["queue"].(map[string]any)["computeNodeGroupConfigurations"].([]any)
				if !ok || len(list) != 0 {
					t.Fatal("existing UpdateQueue empty replacement did not persist")
				}
			} else if got.status != 400 || got.code() != "ValidationException" || read.status != 400 || read.code() != "ResourceNotFoundException" {
				t.Errorf("missing one-or-more associations: Create status=%d code=%s; Get status=%d code=%s", got.status, got.code(), read.status, read.code())
			}
		})
	}
}
