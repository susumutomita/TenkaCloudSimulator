package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const mskConfigurations = "/v1/configurations"
const mskClusters = "/v1/clusters"

func mskAttachmentHarness(t *testing.T, svc *Service) *harness {
	t.Helper()
	h := newHarness(t, svc)
	h.engine.cfg.AuthorizeSnapshot = func(ctx context.Context, principal string) (func(string, string) string, error) {
		return func(action, resource string) string {
			decision, err := h.engine.cfg.Authorize(ctx, principal, action, resource)
			if err != nil {
				t.Fatal(err)
			}
			return decision
		}, nil
	}
	return h
}

func createMSKTestConfiguration(t *testing.T, h *harness, name string, versions []any) (string, string) {
	t.Helper()
	spec := map[string]any{"name": name, "serverProperties": "YQD/Cg=="}
	if versions != nil {
		spec["kafkaVersions"] = versions
	}
	r := h.rest("kafka", "POST", mskConfigurations, spec)
	expect(t, r, 200, "")
	arn := str(r.body["arn"])
	return arn, mskConfigurations + "/" + url.PathEscape(arn)
}

func mskTestCluster(name string, info any) map[string]any {
	spec := map[string]any{
		"clusterName": name, "kafkaVersion": "3.6.0", "numberOfBrokerNodes": 2,
		"brokerNodeGroupInfo": map[string]any{"instanceType": "kafka.m5.large", "clientSubnets": []any{"subnet-a", "subnet-b"}},
		"tags":                map[string]any{"purpose": "practice"},
	}
	if info != nil {
		spec["configurationInfo"] = info
	}
	return spec
}

func TestMSKAttachmentLifecycle(t *testing.T) {
	h := mskAttachmentHarness(t, msk)
	arn, config := createMSKTestConfiguration(t, h, "practice", []any{"3.5.0", "3.6.0"})
	info := map[string]any{"arn": arn, "revision": 1}
	get := func(path string) reply { return h.rest("kafka", "GET", path, nil) }
	remove := func(path string) reply { return h.rest("kafka", "DELETE", path, nil) }
	initialRevision := get(config + "/revisions/1")
	expect(t, initialRevision, 200, "")
	if initialRevision.body["serverProperties"] != "YQD/Cg==" {
		t.Fatal("unexpected original bytes")
	}
	create := func(name string, selected any) string {
		r := h.rest("kafka", "POST", mskClusters, mskTestCluster(name, selected))
		expect(t, r, 200, "")
		return mskClusters + "/" + url.PathEscape(str(r.body["clusterArn"]))
	}
	legacy := create("legacy", nil)
	first := create("first", info)
	second := create("second", info)
	listed := get(mskClusters)
	expect(t, listed, 200, "")
	rows := listed.body["clusterInfoList"].([]any)
	if len(rows) != 3 {
		t.Fatalf("unexpected clusters %v", rows)
	}
	for i, path := range []string{legacy, first, second} {
		described := get(path)
		expect(t, described, 200, "")
		details := described.body["clusterInfo"].(map[string]any)
		expected := map[string]any{"kafkaVersion": "3.6.0"}
		if i != 0 {
			expected["configurationArn"] = arn
			expected["configurationRevision"] = json.Number("1")
		}
		if !reflect.DeepEqual(details["currentBrokerSoftwareInfo"], expected) || !reflect.DeepEqual(details, rows[i]) || details["state"] != "CREATING" {
			t.Fatalf("incorrect details/list at %d: %v", i, details)
		}
	}
	refuse := func() {
		t.Helper()
		before := get(mskClusters)
		r := remove(config)
		expect(t, r, 400, "BadRequestException")
		if !reflect.DeepEqual(get(mskClusters).body, before.body) || !reflect.DeepEqual(get(config+"/revisions/1").body, initialRevision.body) {
			t.Fatal("refused deletion changed a cluster or saved revision")
		}
	}
	refuse()
	expect(t, remove(first), 200, "")
	refuse()
	expect(t, remove(second), 200, "")
	inject(t, h.db, "CREATE TRIGGER msk_attach_fail_delete BEFORE DELETE ON aws_catalog_resources BEGIN SELECT RAISE(ABORT,'test failure'); END")
	expect(t, remove(config), 500, "")
	inject(t, h.db, "DROP TRIGGER msk_attach_fail_delete")
	if !reflect.DeepEqual(get(config+"/revisions/1").body, initialRevision.body) {
		t.Fatal("failed write changed revision")
	}
	expect(t, remove(config), 200, "")
	expect(t, get(config), 404, "NotFoundException")
	expect(t, get(legacy), 200, "")
	expect(t, remove(legacy), 200, "")
	newer, _ := createMSKTestConfiguration(t, h, "practice", nil)
	if newer == arn {
		t.Fatal("reused ARN")
	}
	expect(t, h.rest("kafka", "POST", mskClusters, mskTestCluster("stale", info)), 400, "BadRequestException")
}

func TestMSKAttachmentAdmission(t *testing.T) {
	h := mskAttachmentHarness(t, msk)
	arn, path := createMSKTestConfiguration(t, h, "practice", nil)
	empty, _ := createMSKTestConfiguration(t, h, "empty", []any{})
	mismatch, _ := createMSKTestConfiguration(t, h, "older", []any{"3.5.0"})
	info := map[string]any{"arn": arn, "revision": 1}
	create := func(spec any) reply { return h.rest("kafka", "POST", mskClusters, mskTestCluster("candidate", spec)) }
	for _, bad := range []map[string]any{
		{}, {"arn": arn}, {"revision": 1},
		{"arn": arn, "revision": 0}, {"arn": arn, "revision": 2}, {"arn": "practice", "revision": 1},
		{"arn": strings.Replace(arn, ":configuration/", ":cluster/", 1), "revision": 1},
		{"arn": strings.Replace(arn, "000000000000", "111111111111", 1), "revision": 1},
		{"arn": empty, "revision": 1}, {"arn": mismatch, "revision": 1},
	} {
		r := create(bad)
		expect(t, r, 400, "BadRequestException")
	}
	raw, err := json.Marshal(mskTestCluster("candidate", info))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", mskClusters, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	sign(req, "kafka", "ap-northeast-1")
	expect(t, h.do(req), 400, "BadRequestException")
	foreignCfg := h.engine.cfg
	foreignCfg.Account = "111111111111"
	foreignEngine, err := New(h.db, foreignCfg, []*Service{msk})
	if err != nil {
		t.Fatal(err)
	}
	foreignHarness := *h
	foreignHarness.engine = foreignEngine
	foreign, _ := createMSKTestConfiguration(t, &foreignHarness, "other-account", nil)
	expect(t, create(map[string]any{"arn": foreign, "revision": 1}), 400, "BadRequestException")
	authorize := h.engine.cfg.Authorize
	for _, denyKind := range []string{":cluster/", ":configuration/"} {
		h.engine.cfg.Authorize = func(ctx context.Context, p, action, resource string) (string, error) {
			if action == "kafka:CreateCluster" && strings.Contains(resource, denyKind) {
				return "implicit-deny", nil
			}
			return authorize(ctx, p, action, resource)
		}
		expect(t, create(info), 403, "")
	}
	h.engine.cfg.Authorize = authorize
	inject(t, h.db, "CREATE TRIGGER msk_attach_fail_insert BEFORE INSERT ON aws_catalog_resources BEGIN SELECT RAISE(ABORT,'test failure'); END")
	expect(t, create(info), 500, "")
	inject(t, h.db, "DROP TRIGGER msk_attach_fail_insert")
	if r := h.rest("kafka", "GET", mskClusters, nil); len(r.body["clusterInfoList"].([]any)) != 0 {
		t.Fatal("a rejected request created a cluster")
	}
	if r := h.rest("kafka", "GET", path+"/revisions/1", nil); r.body["serverProperties"] != "YQD/Cg==" {
		t.Fatal("rejected request changed configuration")
	}
	h.policy.calls = nil
	created := create(info)
	expect(t, created, 200, "")
	want := []string{"simulator-admin kafka:CreateCluster " + str(created.body["clusterArn"]), "simulator-admin kafka:CreateCluster " + arn, "simulator-admin kafka:TagResource " + str(created.body["clusterArn"])}
	if !slices.Equal(h.policy.calls, want) {
		t.Fatalf("authorization calls %v, want %v", h.policy.calls, want)
	}
}

func TestMSKAttachmentInterleavedCreateDelete(t *testing.T) {
	for _, first := range []string{"cluster", "configuration"} {
		t.Run(first, func(t *testing.T) {
			reached, resume := make(chan struct{}), make(chan struct{})
			spec := *msk
			cluster := *msk.resource("cluster")
			spec.Resources = []*Resource{&cluster, msk.resource("configuration")}
			if first == "configuration" {
				// Stop after the preliminary lookup succeeded. The delete commits
				// before cluster creation begins its transaction and rechecks the ARN.
				check := cluster.CheckRef
				cluster.CheckRef = func(region, account string, document map[string]any, target row) error {
					if err := check(region, account, document, target); err != nil {
						return err
					}
					close(reached)
					<-resume
					return nil
				}
			}
			h := mskAttachmentHarness(t, &spec)
			arn, path := createMSKTestConfiguration(t, h, "practice", nil)
			create := func() reply {
				return h.rest("kafka", "POST", mskClusters, mskTestCluster("interleaved", map[string]any{"arn": arn, "revision": 1}))
			}
			remove := func() reply { return h.rest("kafka", "DELETE", path, nil) }
			if first == "cluster" {
				// Stop the authenticated delete before its transaction, then commit
				// a cluster referencing that exact configuration generation.
				authorize := h.engine.cfg.Authorize
				h.engine.cfg.Authorize = func(ctx context.Context, principal, action, resource string) (string, error) {
					decision, err := authorize(ctx, principal, action, resource)
					if action == "kafka:DeleteConfiguration" && resource == arn {
						close(reached)
						<-resume
					}
					return decision, err
				}
			}
			result := make(chan reply, 1)
			if first == "configuration" {
				go func() { result <- create() }()
			} else {
				go func() { result <- remove() }()
			}
			select {
			case <-reached:
			case unexpected := <-result:
				t.Fatalf("operation returned %d before the planned interleaving", unexpected.status)
			case <-time.After(5 * time.Second):
				close(resume)
				t.Fatal("timed out waiting for the planned interleaving")
			}
			var winner reply
			if first == "configuration" {
				winner = remove()
			} else {
				winner = create()
			}
			close(resume)
			expect(t, winner, 200, "")
			expect(t, <-result, 400, "BadRequestException")
			clusters := h.rest("kafka", "GET", mskClusters, nil).body["clusterInfoList"].([]any)
			configs := h.rest("kafka", "GET", mskConfigurations, nil).body["configurations"].([]any)
			if first == "configuration" {
				if len(clusters) != 0 || len(configs) != 0 {
					t.Fatal("deleted config acquired a cluster")
				}
			} else if len(clusters) != 1 || len(configs) != 1 {
				t.Fatal("in-use configuration was lost")
			}
		})
	}
}
