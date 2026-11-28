package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"
)

func pcsAssociationSeed(t *testing.T, h *harness, name string) (string, string) {
	t.Helper()
	c := h.json1("pcs", "CreateCluster", map[string]any{"clusterName": name, "scheduler": map[string]any{"type": "SLURM", "version": "24.11"}, "size": "SMALL", "networking": map[string]any{"subnetIds": []any{"subnet-0a1b2c3d"}}})
	expect(t, c, 200, "")
	id := str(c.body["cluster"].(map[string]any)["id"])
	g := h.json1("pcs", "CreateComputeNodeGroup", map[string]any{"clusterIdentifier": id, "computeNodeGroupName": "compute", "subnetIds": []any{"subnet-0a1b2c3d"}, "instanceConfigs": []any{map[string]any{"instanceType": "c6i.large"}}, "scalingConfiguration": map[string]any{"minInstanceCount": 0, "maxInstanceCount": 2}, "customLaunchTemplate": map[string]any{"id": "lt-0123456789abcdef0", "version": "1"}, "iamInstanceProfileArn": "arn:aws:iam::000000000000:instance-profile/pcs"})
	expect(t, g, 200, "")
	return id, str(g.body["computeNodeGroup"].(map[string]any)["id"])
}

func TestPCSQueueReferenceOwnershipAndDeletion(t *testing.T) {
	h := newPCSHarness(t)
	cluster, group := pcsAssociationSeed(t, h, "first")
	_, other := pcsAssociationSeed(t, h, "second")
	refs := func(ids ...string) []any {
		out := []any{}
		for _, id := range ids {
			out = append(out, map[string]any{"computeNodeGroupId": id})
		}
		return out
	}
	queue := func(name string, ids ...string) reply {
		return h.json1("pcs", "CreateQueue", map[string]any{"clusterIdentifier": cluster, "queueName": name, "computeNodeGroupConfigurations": refs(ids...)})
	}
	for _, id := range []string{"pcs_missing", other, "compute"} {
		expect(t, queue("rejected", id), 400, "ResourceNotFoundException")
	}
	expect(t, queue("firstqueue", group), 200, "")
	expect(t, queue("secondqueue", group), 200, "")
	get := func() reply {
		return h.json1("pcs", "GetQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": "firstqueue"})
	}
	before := get().body
	expect(t, h.json1("pcs", "UpdateQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": "firstqueue", "computeNodeGroupConfigurations": []any{map[string]any{}}}), 400, "ValidationException")
	if !reflect.DeepEqual(before, get().body) {
		t.Fatal("missing ID changed queue")
	}
	expect(t, h.json1("pcs", "UpdateQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": "firstqueue", "computeNodeGroupConfigurations": refs(group, other)}), 400, "ResourceNotFoundException")
	if !reflect.DeepEqual(before, get().body) {
		t.Fatal("failed multi-reference update changed queue")
	}
	del := func() reply {
		return h.json1("pcs", "DeleteComputeNodeGroup", map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group})
	}
	expect(t, del(), 400, "ConflictException")
	expect(t, h.json1("pcs", "UpdateQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": "firstqueue", "computeNodeGroupConfigurations": []any{}}), 200, "")
	expect(t, del(), 400, "ConflictException")
	expect(t, h.json1("pcs", "DeleteQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": "secondqueue"}), 200, "")
	expect(t, del(), 200, "")
	expect(t, queue("afterdelete", group), 400, "ResourceNotFoundException")
	expect(t, h.json1("pcs", "DeleteQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": "firstqueue"}), 200, "")
	expect(t, h.json1("pcs", "DeleteCluster", map[string]any{"clusterIdentifier": cluster}), 200, "")
}

func TestPCSQueueAssociationIAMResources(t *testing.T) {
	h := newPCSHarness(t)
	cluster, group := pcsAssociationSeed(t, h, "authorize")
	clusterARN := "arn:aws:pcs:us-east-1:000000000000:cluster/" + cluster
	groupARN := "arn:aws:pcs:us-east-1:000000000000:cluster/" + cluster + "/computenodegroup/" + group
	input := map[string]any{"clusterIdentifier": cluster, "queueName": "workqueue", "computeNodeGroupConfigurations": []any{map[string]any{"computeNodeGroupId": group}}}
	h.policy.deny["pcs:CreateQueue "+clusterARN] = true
	expect(t, h.json1("pcs", "CreateQueue", input), 403, "AccessDeniedException")
	delete(h.policy.deny, "pcs:CreateQueue "+clusterARN)
	made := h.json1("pcs", "CreateQueue", input)
	expect(t, made, 200, "")
	queueARN := str(made.body["queue"].(map[string]any)["arn"])
	for _, target := range []string{clusterARN, queueARN} {
		h.policy.deny["pcs:UpdateQueue "+target] = true
		expect(t, h.json1("pcs", "UpdateQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": "workqueue", "computeNodeGroupConfigurations": []any{}}), 403, "AccessDeniedException")
		delete(h.policy.deny, "pcs:UpdateQueue "+target)
		expect(t, h.json1("pcs", "DeleteComputeNodeGroup", map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group}), 400, "ConflictException")
	}
	expect(t, h.json1("pcs", "UpdateQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": "workqueue", "computeNodeGroupConfigurations": []any{}}), 200, "")
	for _, target := range []string{clusterARN, groupARN} {
		h.policy.deny["pcs:DeleteComputeNodeGroup "+target] = true
		expect(t, h.json1("pcs", "DeleteComputeNodeGroup", map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group}), 403, "AccessDeniedException")
		delete(h.policy.deny, "pcs:DeleteComputeNodeGroup "+target)
		expect(t, h.json1("pcs", "GetComputeNodeGroup", map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group}), 200, "")
	}
	expect(t, h.json1("pcs", "DeleteComputeNodeGroup", map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group}), 200, "")
}

func TestPCSQueueReferencesAreRecheckedInTransaction(t *testing.T) {
	h := newPCSHarness(t)
	cluster, group := pcsAssociationSeed(t, h, "transaction")
	b := h.binding("pcs")
	q := request{e: h.engine, b: b, region: "us-east-1"}
	document := map[string]any{"clusterId": cluster, "computeNodeGroupConfigurations": []any{map[string]any{"computeNodeGroupId": group}}}
	if err := q.checkRefs(b.spec.resource("queue"), document); err != nil {
		t.Fatal(err)
	}
	expect(t, h.json1("pcs", "DeleteComputeNodeGroup", map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group}), 200, "")
	tx, err := h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := q.checkRefsIn(tx, b.spec.resource("queue"), document); err == nil {
		t.Fatal("stale preflight admitted deleted group")
	}
}

func TestPCSAssociationIndependentStoreAndRegion(t *testing.T) {
	h := newPCSHarness(t)
	cluster, group := pcsAssociationSeed(t, h, "isolate")
	peer := newPCSHarness(t)
	peerCluster, _ := pcsAssociationSeed(t, peer, "isolate")
	expect(t, peer.json1("pcs", "CreateQueue", map[string]any{"clusterIdentifier": peerCluster, "queueName": "missingpeer", "computeNodeGroupConfigurations": []any{map[string]any{"computeNodeGroupId": group}}}), 400, "ResourceNotFoundException")
	b := h.binding("pcs")
	q := request{e: h.engine, b: b, region: "ap-northeast-1"}
	if err := q.checkRefs(b.spec.resource("queue"), map[string]any{"clusterId": cluster, "computeNodeGroupConfigurations": []any{map[string]any{"computeNodeGroupId": group}}}); err == nil {
		t.Fatal("cross-region group admitted")
	}
	expect(t, h.json1("pcs", "GetComputeNodeGroup", map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group}), 200, "")
}

func TestPCSQueueCanceledAndFailedWritePreservesRows(t *testing.T) {
	h := newPCSHarness(t)
	cluster, group := pcsAssociationSeed(t, h, "rollback")
	input := map[string]any{"clusterIdentifier": cluster, "queueName": "cancelqueue", "computeNodeGroupConfigurations": []any{map[string]any{"computeNodeGroupId": group}}}
	before, err := listKind(h.db, "pcs", "computeNodeGroup")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(input)
	req := httptest.NewRequest("POST", "/", bytes.NewReader(data))
	req.Header.Set("X-Amz-Target", h.binding("pcs").model.Metadata.TargetPrefix+".CreateQueue")
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	sign(req, "pcs", "us-east-1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := h.do(req.WithContext(ctx)); got.status < 400 {
		t.Fatal("canceled create succeeded")
	}
	queues, err := listKind(h.db, "pcs", "queue")
	if err != nil || len(queues) != 0 {
		t.Fatalf("canceled create persisted: %v %v", queues, err)
	}
	if _, err := h.db.Exec(`CREATE TRIGGER pcs_reject_queue BEFORE INSERT ON aws_catalog_resources WHEN NEW.service='pcs' AND NEW.kind='queue' BEGIN SELECT RAISE(ABORT,'fixture rejected queue'); END`); err != nil {
		t.Fatal(err)
	}
	if got := h.json1("pcs", "CreateQueue", input); got.status < 400 {
		t.Fatal("failed database write succeeded")
	}
	after, err := listKind(h.db, "pcs", "computeNodeGroup")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed writes changed existing group")
	}
	if _, err := h.db.Exec("DROP TRIGGER pcs_reject_queue"); err != nil {
		t.Fatal(err)
	}
	expect(t, h.json1("pcs", "CreateQueue", input), 200, "")
	// Recompose the real engine over the persisted SQLite rows.
	restarted, err := New(h.db, h.engine.cfg, []*Service{pcs})
	if err != nil {
		t.Fatal(err)
	}
	h.engine = restarted
	expect(t, h.json1("pcs", "DeleteComputeNodeGroup", map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group}), 400, "ConflictException")
}

func TestPCSCompetingAssociationOrders(t *testing.T) {
	for _, deleteFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(deleteFirst), func(t *testing.T) {
			h := newPCSHarness(t)
			cluster, group := pcsAssociationSeed(t, h, "ordering")
			original := h.engine.cfg.Authorize
			originalSnapshot := h.engine.cfg.AuthorizeSnapshot
			intervened := false
			attach := func() reply {
				return h.json1("pcs", "CreateQueue", map[string]any{"clusterIdentifier": cluster, "queueName": "competing", "computeNodeGroupConfigurations": []any{map[string]any{"computeNodeGroupId": group}}})
			}
			remove := func() reply {
				return h.json1("pcs", "DeleteComputeNodeGroup", map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group})
			}
			h.engine.cfg.Authorize = func(ctx context.Context, principal, action, resource string) (string, error) {
				target := "pcs:CreateQueue"
				if !deleteFirst {
					target = "pcs:DeleteComputeNodeGroup"
				}
				if action == target && !intervened {
					intervened = true
					if deleteFirst {
						expect(t, remove(), 200, "")
					} else {
						expect(t, attach(), 200, "")
					}
				}
				return original(ctx, principal, action, resource)
			}
			h.engine.cfg.AuthorizeSnapshot = func(ctx context.Context, principal string) (func(string, string) string, error) {
				decide, err := originalSnapshot(ctx, principal)
				if err != nil {
					return nil, err
				}
				return func(action, resource string) string {
					if !deleteFirst && action == "pcs:DeleteComputeNodeGroup" && !intervened {
						intervened = true
						expect(t, attach(), 200, "")
					}
					return decide(action, resource)
				}, nil
			}
			if deleteFirst {
				expect(t, attach(), 400, "ResourceNotFoundException")
			} else {
				expect(t, remove(), 400, "ConflictException")
			}
			if !intervened {
				t.Fatal("competing authorized operation did not execute")
			}
		})
	}
}

func TestPCSQueueAssociationRejectsCorruptParentIdentity(t *testing.T) {
	h := newPCSHarness(t)
	cluster, group := pcsAssociationSeed(t, h, "canonical")
	other, _ := pcsAssociationSeed(t, h, "different")
	made := h.json1("pcs", "CreateQueue", map[string]any{"clusterIdentifier": cluster, "queueName": "canonicalqueue", "computeNodeGroupConfigurations": []any{map[string]any{"computeNodeGroupId": group}}})
	expect(t, made, 200, "")
	queueARN := str(made.body["queue"].(map[string]any)["arn"])
	for _, kind := range []string{"queue", "computeNodeGroup"} {
		t.Run(kind, func(t *testing.T) {
			arn := queueARN
			if kind == "computeNodeGroup" {
				arn = "arn:aws:pcs:us-east-1:000000000000:cluster/" + cluster + "/computenodegroup/" + group
			}
			saved, err := findARN(h.db, "pcs", arn)
			if err != nil {
				t.Fatal(err)
			}
			original, _ := json.Marshal(saved.Document)
			saved.Document["clusterId"] = other
			corrupt, _ := json.Marshal(saved.Document)
			if _, err := h.db.Exec("UPDATE aws_catalog_resources SET document=? WHERE arn=?", string(corrupt), arn); err != nil {
				t.Fatal(err)
			}
			result := h.json1("pcs", "UpdateQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": "canonicalqueue", "computeNodeGroupConfigurations": []any{map[string]any{"computeNodeGroupId": group}}})
			if result.status != 500 {
				t.Fatalf("corrupt identity admitted: %d", result.status)
			}
			var after string
			if err := h.db.QueryRow("SELECT document FROM aws_catalog_resources WHERE arn=?", arn).Scan(&after); err != nil || after != string(corrupt) {
				t.Fatal("corrupt state rewritten", err)
			}
			if _, err := h.db.Exec("UPDATE aws_catalog_resources SET document=? WHERE arn=?", string(original), arn); err != nil {
				t.Fatal(err)
			}
		})
	}
}
