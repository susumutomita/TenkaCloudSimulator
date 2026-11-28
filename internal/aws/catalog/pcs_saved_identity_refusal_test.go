package catalog

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// Maintenance corruption is not an API input. The ordinary handler must
// preserve it for explicit repair rather than authorize or mutate a different
// resource. A populated independent store must remain untouched throughout.
func TestPCSSavedIdentityRefusalAndRepair(t *testing.T) {
	cases := []struct {
		name, kind, member, value, operation string
	}{
		{"compound parent identity", "cluster", "id", "pcs_other", "GetQueue"},
		{"tagged child identity", "queue", "id", "pcs_other", "TagResource"},
		{"tagged parent identity", "cluster", "arn", "arn:aws:pcs:us-east-1:000000000000:cluster/pcs_other", "TagResource"},
		{"tagged child parent", "queue", "clusterId", "pcs_other", "TagResource"},
		{"queue persisted parent", "queue", "clusterId", "pcs_other", "UpdateQueue"},
		{"referenced group parent", "computeNodeGroup", "clusterId", "pcs_other", "UpdateQueue"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			host, peer := newPCSHarness(t), newPCSHarness(t)
			cluster, group, queue := pcsIAMSeed(t, host)
			if test.operation == "UpdateQueue" {
				originalGroup, err := find(host.db, "pcs", "us-east-1", "computeNodeGroup", cluster, group)
				if err != nil {
					t.Fatal(err)
				}
				input := map[string]any{"clusterIdentifier": cluster, "computeNodeGroupName": "replacement"}
				for _, member := range []string{"subnetIds", "customLaunchTemplate", "iamInstanceProfileArn", "scalingConfiguration", "instanceConfigs"} {
					input[member] = originalGroup.Document[member]
				}
				created := host.json1("pcs", "CreateComputeNodeGroup", input)
				expect(t, created, 200, "")
				replacement := str(created.body["computeNodeGroup"].(map[string]any)["id"])
				if replacement == group {
					t.Fatal("replacement group reused original identity")
				}
				group = replacement
			}
			pcsIAMSeed(t, peer)
			peerBefore := groupCatalogFrame(t, peer.db)
			id := map[string]string{"cluster": cluster, "computeNodeGroup": group, "queue": queue}[test.kind]
			var original string
			if err := host.db.QueryRow("SELECT document FROM aws_catalog_resources WHERE service='pcs' AND kind=? AND id=?", test.kind, id).Scan(&original); err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal([]byte(original), &document); err != nil {
				t.Fatal(err)
			}
			document[test.member] = test.value
			changed, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = host.db.Exec("UPDATE aws_catalog_resources SET document=? WHERE service='pcs' AND kind=? AND id=?", string(changed), test.kind, id); err != nil {
				t.Fatal(err)
			}
			before := groupCatalogFrame(t, host.db)
			input := map[string]any{"clusterIdentifier": cluster, "queueIdentifier": queue}
			switch test.operation {
			case "TagResource":
				stored, err := find(host.db, "pcs", "us-east-1", test.kind, map[string]string{"cluster": "", "queue": cluster}[test.kind], id)
				if err != nil {
					t.Fatal(err)
				}
				input = map[string]any{"resourceArn": stored.ARN, "tags": map[string]any{"repaired": "yes"}}
			case "UpdateQueue":
				input["computeNodeGroupConfigurations"] = []any{map[string]any{"computeNodeGroupId": group}}
			}
			refused := host.json1("pcs", test.operation, input)
			errorCode := "InternalServerException"
			if test.operation == "TagResource" {
				errorCode = "InternalFailure"
			}
			expect(t, refused, 500, errorCode)
			if refused.body["queue"] != nil || refused.body["cluster"] != nil || refused.body["tags"] != nil {
				t.Fatal("refusal claimed saved metadata")
			}
			if !reflect.DeepEqual(before, groupCatalogFrame(t, host.db)) || !reflect.DeepEqual(peerBefore, groupCatalogFrame(t, peer.db)) {
				t.Fatal("refusal changed persisted state")
			}
			if _, err = host.db.Exec("UPDATE aws_catalog_resources SET document=? WHERE service='pcs' AND kind=? AND id=?", original, test.kind, id); err != nil {
				t.Fatal(err)
			}
			recovered := host.json1("pcs", test.operation, input)
			expect(t, recovered, 200, "")
			if test.operation == "UpdateQueue" {
				want := []any{map[string]any{"computeNodeGroupId": group}}
				if !reflect.DeepEqual(recovered.body["queue"].(map[string]any)["computeNodeGroupConfigurations"], want) {
					t.Fatal("repair did not return the replacement association")
				}
				fresh := host.json1("pcs", "GetQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": queue})
				expect(t, fresh, 200, "")
				if !reflect.DeepEqual(fresh.body["queue"].(map[string]any)["computeNodeGroupConfigurations"], want) {
					t.Fatal("replacement association was not saved")
				}
			}
			if test.operation == "TagResource" {
				listed := host.json1("pcs", "ListTagsForResource", map[string]any{"resourceArn": input["resourceArn"]})
				expect(t, listed, 200, "")
				if listed.body["tags"].(map[string]any)["repaired"] != "yes" {
					t.Fatal("repair did not save target tag")
				}
			}
			if !reflect.DeepEqual(peerBefore, groupCatalogFrame(t, peer.db)) {
				t.Fatal("repair retry changed peer")
			}
		})
	}
}

func TestPCSNativeTagRegionAndLegacyAliasRefusal(t *testing.T) {
	t.Run("existing resource in another Region", func(t *testing.T) {
		host, peer := newPCSHarness(t), newPCSHarness(t)
		cluster, _, queue := pcsIAMSeed(t, host)
		pcsIAMSeed(t, peer)
		arn := "arn:aws:pcs:us-east-1:000000000000:cluster/" + cluster + "/queue/" + queue
		input := map[string]any{"resourceArn": arn, "tags": map[string]any{"regional": "allowed"}}
		before, peerBefore := groupCatalogFrame(t, host.db), groupCatalogFrame(t, peer.db)
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		request.Header.Set("X-Amz-Target", "AWSParallelComputingService.TagResource")
		request.Header.Set("Content-Type", "application/x-amz-json-1.0")
		sign(request, "pcs", "ap-northeast-1")
		expect(t, host.do(request), 400, "ResourceNotFoundException")
		if !reflect.DeepEqual(before, groupCatalogFrame(t, host.db)) || !reflect.DeepEqual(peerBefore, groupCatalogFrame(t, peer.db)) {
			t.Fatal("cross-region tag changed saved state")
		}
		expect(t, host.json1("pcs", "TagResource", input), 200, "")
		listed := host.json1("pcs", "ListTagsForResource", map[string]any{"resourceArn": arn})
		expect(t, listed, 200, "")
		if listed.body["tags"].(map[string]any)["regional"] != "allowed" {
			t.Fatal("same-region repair did not tag actual resource")
		}
		if !reflect.DeepEqual(peerBefore, groupCatalogFrame(t, peer.db)) {
			t.Fatal("same-region retry changed peer")
		}
	})
	t.Run("canonical alias of inconsistent historical child", func(t *testing.T) {
		host, cluster, _, queue := pcsHistoricalHarness(t)
		peer := newPCSHarness(t)
		pcsIAMSeed(t, peer)
		peerBefore := groupCatalogFrame(t, peer.db)
		canonical := "arn:aws:pcs:us-east-1:000000000000:cluster/" + cluster + "/queue/" + queue
		var original string
		if err := host.db.QueryRow("SELECT document FROM aws_catalog_resources WHERE service='pcs' AND kind='queue' AND id=?", queue).Scan(&original); err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal([]byte(original), &document); err != nil {
			t.Fatal(err)
		}
		document["clusterId"] = "pcs_other"
		changed, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = host.db.Exec("UPDATE aws_catalog_resources SET document=? WHERE service='pcs' AND kind='queue' AND id=?", string(changed), queue); err != nil {
			t.Fatal(err)
		}
		before := groupCatalogFrame(t, host.db)
		input := map[string]any{"resourceArn": canonical, "tags": map[string]any{"repaired": "yes"}}
		expect(t, host.json1("pcs", "TagResource", input), 500, "InternalFailure")
		if !reflect.DeepEqual(before, groupCatalogFrame(t, host.db)) || !reflect.DeepEqual(peerBefore, groupCatalogFrame(t, peer.db)) {
			t.Fatal("alias refusal changed historical or peer rows")
		}
		if _, err = host.db.Exec("UPDATE aws_catalog_resources SET document=? WHERE service='pcs' AND kind='queue' AND id=?", original, queue); err != nil {
			t.Fatal(err)
		}
		expect(t, host.json1("pcs", "TagResource", input), 200, "")
		listed := host.json1("pcs", "ListTagsForResource", map[string]any{"resourceArn": canonical})
		expect(t, listed, 200, "")
		if listed.body["tags"].(map[string]any)["repaired"] != "yes" {
			t.Fatal("canonical alias repair did not save historical target tag")
		}
		saved, err := find(host.db, "pcs", "us-east-1", "queue", cluster, queue)
		if err != nil || saved.ARN != "arn:aws:pcs:us-east-1:000000000000:queue/"+queue || saved.Document["arn"] != saved.ARN {
			t.Fatal("repair retry migrated historical identity", err)
		}
		if !reflect.DeepEqual(peerBefore, groupCatalogFrame(t, peer.db)) {
			t.Fatal("alias retry changed peer")
		}
	})
}
