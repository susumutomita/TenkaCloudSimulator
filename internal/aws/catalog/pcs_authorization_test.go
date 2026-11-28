package catalog

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func pcsGrantOnly(h *harness, action string, resources ...string) {
	resources = append([]string(nil), resources...)
	decide := func(actual, target string) string {
		if actual == "pcs:"+action {
			for _, allowed := range resources {
				if target == allowed {
					return "allowed"
				}
			}
		}
		return "implicit-deny"
	}
	h.engine.cfg.Authorize = func(_ context.Context, _, actual, target string) (string, error) { return decide(actual, target), nil }
	h.engine.cfg.AuthorizeSnapshot = func(context.Context, string) (func(string, string) string, error) { return decide, nil }
}

func pcsIAMSeed(t *testing.T, h *harness) (string, string, string) {
	t.Helper()
	cluster, group := pcsAssociationSeed(t, h, "iam-cluster")
	queue := h.json1("pcs", "CreateQueue", map[string]any{"clusterIdentifier": cluster, "queueName": "iam-queue", "computeNodeGroupConfigurations": []any{map[string]any{"computeNodeGroupId": group}}})
	expect(t, queue, 200, "")
	return cluster, group, str(queue.body["queue"].(map[string]any)["id"])
}

func TestPCSOfficialActionResourceTable(t *testing.T) {
	actions := []string{"CreateCluster", "ListClusters", "GetCluster", "DeleteCluster", "CreateComputeNodeGroup", "ListComputeNodeGroups", "GetComputeNodeGroup", "UpdateComputeNodeGroup", "DeleteComputeNodeGroup", "CreateQueue", "ListQueues", "GetQueue", "UpdateQueue", "DeleteQueue", "TagResource", "UntagResource", "ListTagsForResource"}
	expected := map[string]bool{}
	for _, action := range actions {
		expected[action] = true
	}
	if len(pcs.Ops) != len(actions) {
		t.Fatal("PCS operation inventory changed without a reviewed IAM row")
	}
	for _, op := range pcs.Ops {
		if !expected[op.Name] {
			t.Fatal("unreviewed PCS IAM action", op.Name)
		}
	}
	for _, operation := range actions {
		t.Run(operation, func(t *testing.T) {
			h := newPCSHarness(t)
			cluster, group, queue := pcsIAMSeed(t, h)
			other, _ := pcsAssociationSeed(t, h, "other-real")
			parent := "arn:aws:pcs:us-east-1:000000000000:cluster/" + cluster
			groupARN := parent + "/computenodegroup/" + group
			queueARN := parent + "/queue/" + queue
			input := map[string]any{}
			targets := []string{parent}
			switch operation {
			case "CreateCluster":
				input = map[string]any{"clusterName": "new-cluster", "scheduler": map[string]any{"type": "SLURM", "version": "24.11"}, "size": "SMALL", "networking": map[string]any{"subnetIds": []any{"subnet-0a1b2c3d"}}}
				targets = []string{"*"}
			case "ListClusters":
				targets = []string{"*"}
			case "GetCluster", "DeleteCluster":
				input["clusterIdentifier"] = cluster
			case "CreateComputeNodeGroup":
				input = map[string]any{"clusterIdentifier": cluster, "computeNodeGroupName": "new-compute", "subnetIds": []any{"subnet-0a1b2c3d"}, "instanceConfigs": []any{map[string]any{"instanceType": "c6i.large"}}, "scalingConfiguration": map[string]any{"minInstanceCount": 0, "maxInstanceCount": 2}, "customLaunchTemplate": map[string]any{"id": "lt-0123456789abcdef0", "version": "1"}, "iamInstanceProfileArn": "arn:aws:iam::000000000000:instance-profile/pcs"}
			case "ListComputeNodeGroups", "ListQueues":
				input["clusterIdentifier"] = cluster
			case "GetComputeNodeGroup", "UpdateComputeNodeGroup", "DeleteComputeNodeGroup":
				input["clusterIdentifier"] = cluster
				input["computeNodeGroupIdentifier"] = group
				targets = []string{parent, groupARN}
				if operation == "UpdateComputeNodeGroup" {
					input["amiId"] = "ami-0123"
				}
			case "CreateQueue":
				input = map[string]any{"clusterIdentifier": cluster, "queueName": "new-queue", "computeNodeGroupConfigurations": []any{map[string]any{"computeNodeGroupId": group}}}
			case "GetQueue", "UpdateQueue", "DeleteQueue":
				input["clusterIdentifier"] = cluster
				input["queueIdentifier"] = queue
				targets = []string{parent, queueARN}
				if operation == "UpdateQueue" {
					input["computeNodeGroupConfigurations"] = []any{}
				}
			case "TagResource":
				input = map[string]any{"resourceArn": queueARN, "tags": map[string]any{"test": "value"}}
				targets = []string{queueARN}
			case "UntagResource":
				input = map[string]any{"resourceArn": queueARN, "tagKeys": []any{"test"}}
				targets = []string{queueARN}
			case "ListTagsForResource":
				input = map[string]any{"resourceArn": queueARN}
				targets = []string{"*"}
			}
			if operation == "DeleteComputeNodeGroup" || operation == "DeleteCluster" {
				expect(t, h.json1("pcs", "DeleteQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": queue}), 200, "")
			}
			if operation == "DeleteCluster" {
				expect(t, h.json1("pcs", "DeleteComputeNodeGroup", map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group}), 200, "")
			}
			// Every missing required resource and an unrelated real-looking target
			// must deny without changing even a generation or a tag.
			for i := range targets {
				grants := append([]string{}, targets[:i]...)
				grants = append(grants, targets[i+1:]...)
				grants = append(grants, "arn:aws:pcs:us-east-1:000000000000:cluster/"+other)
				before, err := list(h.db, "pcs", "us-east-1", "cluster", "")
				if err != nil {
					t.Fatal(err)
				}
				groups, _ := listKind(h.db, "pcs", "computeNodeGroup")
				queues, _ := listKind(h.db, "pcs", "queue")
				pcsGrantOnly(h, operation, grants...)
				expect(t, h.json1("pcs", operation, input), 403, "AccessDeniedException")
				after, _ := list(h.db, "pcs", "us-east-1", "cluster", "")
				newGroups, _ := listKind(h.db, "pcs", "computeNodeGroup")
				newQueues, _ := listKind(h.db, "pcs", "queue")
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(groups, newGroups) || !reflect.DeepEqual(queues, newQueues) {
					t.Fatal("denied operation changed persisted rows")
				}
			}
			pcsGrantOnly(h, operation, targets...)
			got := h.json1("pcs", operation, input)
			expect(t, got, 200, "")
			if operation == "CreateComputeNodeGroup" {
				if arn := str(got.body["computeNodeGroup"].(map[string]any)["arn"]); !strings.HasPrefix(arn, parent+"/computenodegroup/") {
					t.Fatal("noncanonical new group ARN", arn)
				}
			}
			if operation == "CreateQueue" {
				if arn := str(got.body["queue"].(map[string]any)["arn"]); !strings.HasPrefix(arn, parent+"/queue/") {
					t.Fatal("noncanonical new queue ARN", arn)
				}
			}
		})
	}
}

func TestPCSLegacyFlatARNCanonicalPoliciesAndReopen(t *testing.T) {
	h, cluster, group, queue := pcsHistoricalHarness(t)
	parent := "arn:aws:pcs:us-east-1:000000000000:cluster/" + cluster
	canonicalGroup, canonicalQueue := parent+"/computenodegroup/"+group, parent+"/queue/"+queue
	flatGroup, flatQueue := "arn:aws:pcs:us-east-1:000000000000:computenodegroup/"+group, "arn:aws:pcs:us-east-1:000000000000:queue/"+queue
	beforeReads := pcsRawRows(t, h.db)
	pcsGrantOnly(h, "ListQueues", parent)
	listed := h.json1("pcs", "ListQueues", map[string]any{"clusterIdentifier": cluster})
	expect(t, listed, 200, "")
	if rows := listed.body["queues"].([]any); len(rows) != 1 || rows[0].(map[string]any)["arn"] != flatQueue {
		t.Fatal("legacy queue list changed identity")
	}
	pcsGrantOnly(h, "ListComputeNodeGroups", parent)
	listed = h.json1("pcs", "ListComputeNodeGroups", map[string]any{"clusterIdentifier": cluster})
	expect(t, listed, 200, "")
	if rows := listed.body["computeNodeGroups"].([]any); len(rows) != 1 || rows[0].(map[string]any)["arn"] != flatGroup {
		t.Fatal("legacy group list changed identity")
	}
	pcsGrantOnly(h, "ListTagsForResource", "*")
	tags := h.json1("pcs", "ListTagsForResource", map[string]any{"resourceArn": canonicalQueue})
	expect(t, tags, 200, "")
	if tags.body["tags"].(map[string]any)["kept"] != "legacy" {
		t.Fatal("historical tags lost")
	}
	pcsGrantOnly(h, "GetQueue", parent, canonicalQueue)
	got := h.json1("pcs", "GetQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": queue})
	expect(t, got, 200, "")
	if got.body["queue"].(map[string]any)["arn"] != flatQueue {
		t.Fatal("legacy ARN silently rewritten")
	}
	pcsGrantOnly(h, "GetComputeNodeGroup", parent, flatGroup)
	expect(t, h.json1("pcs", "GetComputeNodeGroup", map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group}), 403, "AccessDeniedException")
	pcsGrantOnly(h, "GetComputeNodeGroup", parent, canonicalGroup)
	got = h.json1("pcs", "GetComputeNodeGroup", map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group})
	expect(t, got, 200, "")
	if got.body["computeNodeGroup"].(map[string]any)["arn"] != flatGroup {
		t.Fatal("legacy group ARN silently rewritten")
	}
	if !reflect.DeepEqual(beforeReads, pcsRawRows(t, h.db)) {
		t.Fatal("reads rewrote historical fields")
	}
	for _, alias := range []string{flatQueue, canonicalQueue, flatGroup, canonicalGroup} {
		flat, canonical := flatQueue, canonicalQueue
		if alias == flatGroup || alias == canonicalGroup {
			flat, canonical = flatGroup, canonicalGroup
		}
		before, err := findARN(h.db, "pcs", flat)
		if err != nil {
			t.Fatal(err)
		}
		pcsGrantOnly(h, "TagResource", flat)
		expect(t, h.json1("pcs", "TagResource", map[string]any{"resourceArn": alias, "tags": map[string]any{"tag": "value"}}), 403, "AccessDeniedException")
		pcsGrantOnly(h, "TagResource", canonical)
		expect(t, h.json1("pcs", "TagResource", map[string]any{"resourceArn": alias, "tags": map[string]any{"tag": "value"}}), 200, "")
		after, err := findARN(h.db, "pcs", flat)
		if err != nil || !reflect.DeepEqual(before.Document, after.Document) {
			t.Fatal("tagging rewrote legacy metadata", err)
		}
		pcsGrantOnly(h, "ListTagsForResource", canonical)
		expect(t, h.json1("pcs", "ListTagsForResource", map[string]any{"resourceArn": alias}), 403, "AccessDeniedException")
		pcsGrantOnly(h, "ListTagsForResource", "*")
		listed := h.json1("pcs", "ListTagsForResource", map[string]any{"resourceArn": alias})
		expect(t, listed, 200, "")
		if listed.body["tags"].(map[string]any)["tag"] != "value" {
			t.Fatal("canonical tag read missed legacy row")
		}
		pcsGrantOnly(h, "UntagResource", canonical)
		expect(t, h.json1("pcs", "UntagResource", map[string]any{"resourceArn": alias, "tagKeys": []any{"tag"}}), 200, "")
	}
	h.engine.cfg.Authorize = h.policy.authorize
	for _, bad := range []string{strings.Replace(canonicalQueue, "000000000000", "111111111111", 1), strings.Replace(canonicalQueue, "us-east-1", "ap-northeast-1", 1), strings.Replace(canonicalQueue, cluster, "pcs_missing", 1), strings.Replace(canonicalQueue, "/queue/", "/computenodegroup/", 1)} {
		expect(t, h.json1("pcs", "TagResource", map[string]any{"resourceArn": bad, "tags": map[string]any{"bad": "value"}}), 400, "ResourceNotFoundException")
	}
	pcsGrantOnly(h, "DeleteComputeNodeGroup", parent, canonicalGroup)
	expect(t, h.json1("pcs", "DeleteComputeNodeGroup", map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group}), 400, "ConflictException")
	pcsGrantOnly(h, "UpdateQueue", parent, canonicalQueue)
	expect(t, h.json1("pcs", "UpdateQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": queue, "computeNodeGroupConfigurations": []any{}}), 200, "")
	pcsGrantOnly(h, "DeleteComputeNodeGroup", parent, canonicalGroup)
	expect(t, h.json1("pcs", "DeleteComputeNodeGroup", map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group}), 200, "")
	pcsGrantOnly(h, "DeleteQueue", parent, canonicalQueue)
	expect(t, h.json1("pcs", "DeleteQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": queue}), 200, "")
	pcsGrantOnly(h, "DeleteCluster", parent)
	expect(t, h.json1("pcs", "DeleteCluster", map[string]any{"clusterIdentifier": cluster}), 200, "")
}

func TestPCSCanonicalIAMRefusesRetargetedStoredARN(t *testing.T) {
	h := newPCSHarness(t)
	cluster, _, queue := pcsIAMSeed(t, h)
	other, _ := pcsAssociationSeed(t, h, "real-other")
	parent := "arn:aws:pcs:us-east-1:000000000000:cluster/" + cluster
	canonical := parent + "/queue/" + queue
	original, err := findARN(h.db, "pcs", canonical)
	if err != nil {
		t.Fatal(err)
	}
	originalDocument, _ := json.Marshal(original.Document)
	targets := []string{parent + "/queue/pcs_wrongid", strings.Replace(canonical, cluster, other, 1), strings.Replace(canonical, "000000000000", "111111111111", 1), strings.Replace(canonical, "us-east-1", "ap-northeast-1", 1), strings.Replace(canonical, "/queue/", "/computenodegroup/", 1)}
	for _, wrong := range targets {
		original.Document["arn"] = wrong
		badDocument, _ := json.Marshal(original.Document)
		if _, err := h.db.Exec("UPDATE aws_catalog_resources SET arn=?,document=? WHERE id=? AND kind='queue'", wrong, string(badDocument), queue); err != nil {
			t.Fatal(err)
		}
		pcsGrantOnly(h, "GetQueue", parent, wrong)
		got := h.json1("pcs", "GetQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": queue})
		if got.status != 500 {
			t.Fatalf("retargeted stored identity admitted: %d", got.status)
		}
		var actualARN, actualDocument string
		if err := h.db.QueryRow("SELECT arn,document FROM aws_catalog_resources WHERE id=? AND kind='queue'", queue).Scan(&actualARN, &actualDocument); err != nil || actualARN != wrong || actualDocument != string(badDocument) {
			t.Fatal("bad identity was silently migrated", err)
		}
	}
	if _, err := h.db.Exec("UPDATE aws_catalog_resources SET arn=?,document=? WHERE id=? AND kind='queue'", canonical, string(originalDocument), queue); err != nil {
		t.Fatal(err)
	}
}
